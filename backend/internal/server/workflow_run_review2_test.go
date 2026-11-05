package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/narratoai/backend-go/internal/models"
)

func TestWorkflowRunHeartbeatRenewsOnlyOwnedCurrentStepLeases(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-heartbeat-step@example.com")
	run := createWorkflowRunTestPC(t, app, token, "clip-0123456789abcdef01234567-1720000000000-a1b2c3", strings.Repeat("a", 64), "machine:client-a")
	path := "/api/v1/workflow-runs/" + run.ID + "/steps/generate-voiceover"

	for _, inputHash := range []string{strings.Repeat("b", 64), strings.Repeat("c", 64)} {
		resp := request(app, http.MethodPut, path, map[string]any{
			"input_hash": inputHash, "status": "running", "client_instance_id": "machine:client-a",
		}, token)
		if resp.Code != http.StatusOK {
			t.Fatalf("acquire step %s status = %d, body = %s", inputHash, resp.Code, resp.Body.String())
		}
	}

	soon := time.Now().UTC().Add(5 * time.Second)
	if err := app.db.Model(&models.WorkflowStepExecution{}).
		Where("workflow_run_id = ?", run.ID).
		Update("lease_expires_at", soon).Error; err != nil {
		t.Fatalf("shorten step leases: %v", err)
	}
	if err := app.db.Model(&models.WorkflowStepExecution{}).
		Where("workflow_run_id = ? AND input_hash = ?", run.ID, strings.Repeat("c", 64)).
		Updates(map[string]any{"lease_owner": "machine:client-b", "lease_expires_at": soon}).Error; err != nil {
		t.Fatalf("seed foreign step lease: %v", err)
	}

	heartbeatAt := time.Now().UTC()
	heartbeat := request(app, http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/heartbeat", map[string]any{
		"client_instance_id": "machine:client-a",
		"current_step_id":    "generate-voiceover",
	}, token)
	if heartbeat.Code != http.StatusOK {
		t.Fatalf("heartbeat status = %d, body = %s", heartbeat.Code, heartbeat.Body.String())
	}

	var steps []models.WorkflowStepExecution
	if err := app.db.Where("workflow_run_id = ?", run.ID).Order("input_hash ASC").Find(&steps).Error; err != nil {
		t.Fatalf("load heartbeat steps: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("step count = %d, want 2", len(steps))
	}
	for _, step := range steps {
		switch step.InputHash {
		case strings.Repeat("b", 64):
			if step.LeaseExpiresAt == nil || step.LeaseExpiresAt.Before(heartbeatAt.Add(89*time.Second)) {
				t.Fatalf("owned current step lease was not renewed: %#v", step.LeaseExpiresAt)
			}
		case strings.Repeat("c", 64):
			if step.LeaseExpiresAt == nil || step.LeaseExpiresAt.After(soon.Add(time.Second)) {
				t.Fatalf("foreign step lease was renewed: %#v", step.LeaseExpiresAt)
			}
		}
	}
}

func TestWorkflowStepMutationRequiresLiveRunLeaseOwner(t *testing.T) {
	for _, leaseState := range []string{"expired", "cleared"} {
		t.Run(leaseState, func(t *testing.T) {
			app := newTestApp(t)
			token := registerAndLogin(t, app, "workflow-run-owner-"+leaseState+"@example.com")
			run := createWorkflowRunTestPC(t, app, token, "desktop-task-"+leaseState, strings.Repeat("d", 64), "client-a")
			path := "/api/v1/workflow-runs/" + run.ID + "/steps/render-video"
			if resp := request(app, http.MethodPut, path, map[string]any{
				"input_hash": strings.Repeat("e", 64), "status": "running", "client_instance_id": "client-a",
			}, token); resp.Code != http.StatusOK {
				t.Fatalf("initial step status = %d, body = %s", resp.Code, resp.Body.String())
			}
			if resp := request(app, http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/resume", map[string]any{
				"client_instance_id": "client-b",
			}, token); resp.Code != http.StatusOK {
				t.Fatalf("resume status = %d, body = %s", resp.Code, resp.Body.String())
			}

			updates := map[string]any{"lease_expires_at": time.Now().UTC().Add(-time.Second)}
			if leaseState == "cleared" {
				updates = map[string]any{"status": "interrupted", "lease_owner": nil, "lease_expires_at": nil}
			}
			if err := app.db.Model(&models.WorkflowStepExecution{}).Where("workflow_run_id = ?", run.ID).Updates(updates).Error; err != nil {
				t.Fatalf("prepare unavailable step lease: %v", err)
			}

			stale := request(app, http.MethodPut, path, map[string]any{
				"input_hash": strings.Repeat("e", 64), "status": "running", "client_instance_id": "client-a",
			}, token)
			if stale.Code != http.StatusConflict {
				t.Fatalf("stale run owner status = %d, want 409, body = %s", stale.Code, stale.Body.String())
			}

			var persisted models.WorkflowStepExecution
			if err := app.db.Where("workflow_run_id = ?", run.ID).Take(&persisted).Error; err != nil {
				t.Fatalf("load persisted step: %v", err)
			}
			if persisted.LeaseOwner != nil && *persisted.LeaseOwner == "client-a" {
				t.Fatalf("stale client reacquired step: %#v", persisted)
			}
		})
	}
}

func TestWorkflowStepTransitionTable(t *testing.T) {
	t.Run("first mutation must acquire running", func(t *testing.T) {
		app := newTestApp(t)
		token := registerAndLogin(t, app, "workflow-transition-first@example.com")
		run := createWorkflowRunTestPC(t, app, token, "transition-first-task", strings.Repeat("f", 64), "client-a")
		for _, status := range []string{"succeeded", "interrupted", "failed", "blocked"} {
			resp := request(app, http.MethodPut, "/api/v1/workflow-runs/"+run.ID+"/steps/first-"+status, map[string]any{
				"input_hash": strings.Repeat(status[:1], 64), "status": status, "client_instance_id": "client-a",
			}, token)
			assertWorkflowErrorCode(t, resp, http.StatusConflict, "invalid_step_transition")
		}
		var count int64
		if err := app.db.Model(&models.WorkflowStepExecution{}).Where("workflow_run_id = ?", run.ID).Count(&count).Error; err != nil {
			t.Fatalf("count first-transition steps: %v", err)
		}
		if count != 0 {
			t.Fatalf("invalid first transitions persisted %d steps", count)
		}
	})

	t.Run("succeeded is terminal", func(t *testing.T) {
		app := newTestApp(t)
		token := registerAndLogin(t, app, "workflow-transition-succeeded@example.com")
		run := createWorkflowRunTestPC(t, app, token, "transition-succeeded-task", strings.Repeat("1", 64), "client-a")
		path := "/api/v1/workflow-runs/" + run.ID + "/steps/finalize-output"
		acquireWorkflowStep(t, app, token, path, strings.Repeat("2", 64), "client-a")
		if resp := request(app, http.MethodPut, path, map[string]any{
			"input_hash": strings.Repeat("2", 64), "status": "succeeded", "client_instance_id": "client-a",
		}, token); resp.Code != http.StatusOK {
			t.Fatalf("succeed step status = %d, body = %s", resp.Code, resp.Body.String())
		}
		for _, status := range []string{"running", "interrupted", "failed", "blocked"} {
			resp := request(app, http.MethodPut, path, map[string]any{
				"input_hash": strings.Repeat("2", 64), "status": status, "client_instance_id": "client-a",
			}, token)
			assertWorkflowErrorCode(t, resp, http.StatusConflict, "invalid_step_transition")
		}
		idempotent := request(app, http.MethodPut, path, map[string]any{
			"input_hash": strings.Repeat("2", 64), "status": "succeeded", "client_instance_id": "client-a",
		}, token)
		if idempotent.Code != http.StatusOK {
			t.Fatalf("idempotent succeeded status = %d, body = %s", idempotent.Code, idempotent.Body.String())
		}
		step := decodeWorkflowStepResponse(t, idempotent)
		if step.AttemptCount != 1 || step.Status != "succeeded" {
			t.Fatalf("terminal step regressed: %#v", step)
		}
	})

	t.Run("succeeded requires a live owned running attempt", func(t *testing.T) {
		app := newTestApp(t)
		token := registerAndLogin(t, app, "workflow-transition-owner@example.com")
		run := createWorkflowRunTestPC(t, app, token, "transition-owner-task", strings.Repeat("3", 64), "client-a")
		path := "/api/v1/workflow-runs/" + run.ID + "/steps/provider-operation"
		acquireWorkflowStep(t, app, token, path, strings.Repeat("4", 64), "client-a")
		if err := app.db.Model(&models.WorkflowStepExecution{}).Where("workflow_run_id = ?", run.ID).
			Updates(map[string]any{"lease_owner": nil, "lease_expires_at": nil}).Error; err != nil {
			t.Fatalf("clear owned attempt: %v", err)
		}
		resp := request(app, http.MethodPut, path, map[string]any{
			"input_hash": strings.Repeat("4", 64), "status": "succeeded", "client_instance_id": "client-a",
		}, token)
		assertWorkflowErrorCode(t, resp, http.StatusConflict, "invalid_step_transition")
	})

	for _, retryable := range []string{"interrupted", "failed", "blocked"} {
		t.Run("retry semantics from "+retryable, func(t *testing.T) {
			app := newTestApp(t)
			token := registerAndLogin(t, app, "workflow-transition-"+retryable+"@example.com")
			run := createWorkflowRunTestPC(t, app, token, "transition-"+retryable+"-task", strings.Repeat("5", 64), "client-a")
			path := "/api/v1/workflow-runs/" + run.ID + "/steps/retry-provider"
			inputHash := strings.Repeat("6", 64)
			acquireWorkflowStep(t, app, token, path, inputHash, "client-a")
			transition := request(app, http.MethodPut, path, map[string]any{
				"input_hash": inputHash, "status": retryable, "client_instance_id": "client-a",
			}, token)
			if transition.Code != http.StatusOK {
				t.Fatalf("running to %s status = %d, body = %s", retryable, transition.Code, transition.Body.String())
			}
			idempotent := request(app, http.MethodPut, path, map[string]any{
				"input_hash": inputHash, "status": retryable, "client_instance_id": "client-a",
			}, token)
			if idempotent.Code != http.StatusOK || decodeWorkflowStepResponse(t, idempotent).AttemptCount != 1 {
				t.Fatalf("idempotent %s response = %d %s", retryable, idempotent.Code, idempotent.Body.String())
			}

			other := map[string]string{"interrupted": "failed", "failed": "blocked", "blocked": "interrupted"}[retryable]
			assertWorkflowErrorCode(t, request(app, http.MethodPut, path, map[string]any{
				"input_hash": inputHash, "status": other, "client_instance_id": "client-a",
			}, token), http.StatusConflict, "invalid_step_transition")
			assertWorkflowErrorCode(t, request(app, http.MethodPut, path, map[string]any{
				"input_hash": inputHash, "status": "succeeded", "client_instance_id": "client-a",
			}, token), http.StatusConflict, "invalid_step_transition")

			retry := request(app, http.MethodPut, path, map[string]any{
				"input_hash": inputHash, "status": "running", "client_instance_id": "client-a",
			}, token)
			if retry.Code != http.StatusOK {
				t.Fatalf("retry from %s status = %d, body = %s", retryable, retry.Code, retry.Body.String())
			}
			if step := decodeWorkflowStepResponse(t, retry); step.AttemptCount != 2 || step.LeaseOwner == nil || *step.LeaseOwner != "client-a" {
				t.Fatalf("retry from %s = %#v", retryable, step)
			}
			if resp := request(app, http.MethodPut, path, map[string]any{
				"input_hash": inputHash, "status": "succeeded", "client_instance_id": "client-a",
			}, token); resp.Code != http.StatusOK {
				t.Fatalf("succeed retry from %s status = %d, body = %s", retryable, resp.Code, resp.Body.String())
			}
		})
	}
}

func TestWorkflowIdentityFieldsUseMachineSafeFormatsAndLimits(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-identities@example.com")
	validTaskID := "clip-0123456789abcdef01234567-1720000000000-a1b2c3d4"
	validHash := strings.Repeat("a", 64)
	validClientID := "pc-machine:550e8400-e29b-41d4-a716-446655440000"

	valid := request(app, http.MethodPost, "/api/v1/workflow-runs/", map[string]any{
		"client_type": "pc", "client_task_id": validTaskID, "client_project_name": "source.mp4", "input_hash": validHash, "client_instance_id": validClientID,
	}, token)
	if valid.Code != http.StatusCreated {
		t.Fatalf("valid PC identities status = %d, body = %s", valid.Code, valid.Body.String())
	}
	run := decodeWorkflowRunResponse(t, valid)

	invalidCreates := []map[string]any{
		{"client_type": "pc", "client_task_id": `C:\private\task`, "input_hash": validHash, "client_instance_id": validClientID},
		{"client_type": "pc", "client_task_id": "subtitle content", "input_hash": validHash, "client_instance_id": validClientID},
		{"client_type": "pc", "client_task_id": strings.Repeat("t", workflowClientTaskIDMaxLength+1), "input_hash": validHash, "client_instance_id": validClientID},
		{"client_type": "pc", "client_task_id": "batch-safe-id", "input_hash": `C:\private\hash`, "client_instance_id": validClientID},
		{"client_type": "pc", "client_task_id": "batch-safe-id", "input_hash": strings.Repeat("h", workflowInputHashMaxLength+1), "client_instance_id": validClientID},
		{"client_type": "pc", "client_task_id": "batch-safe-id", "input_hash": validHash, "client_instance_id": "client with spaces"},
		{"client_type": "pc", "client_task_id": "batch-safe-id", "input_hash": validHash, "client_instance_id": strings.Repeat("c", workflowClientInstanceIDMaxLength+1)},
	}
	for i, body := range invalidCreates {
		resp := request(app, http.MethodPost, "/api/v1/workflow-runs/", body, token)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("invalid create identity %d status = %d, body = %s", i, resp.Code, resp.Body.String())
		}
	}

	invalidRequests := []workflowTestRequest{
		{method: http.MethodPost, path: "/api/v1/workflow-runs/" + run.ID + "/heartbeat", body: map[string]any{"client_instance_id": validClientID, "current_step_id": "step with spaces"}},
		{method: http.MethodPost, path: "/api/v1/workflow-runs/" + run.ID + "/heartbeat", body: map[string]any{"client_instance_id": `C:\private\client`}},
		{method: http.MethodPost, path: "/api/v1/workflow-runs/" + run.ID + "/resume", body: map[string]any{"client_instance_id": strings.Repeat("r", workflowClientInstanceIDMaxLength+1)}},
		{method: http.MethodPost, path: "/api/v1/workflow-runs/" + run.ID + "/interrupt", body: map[string]any{"client_instance_id": "old client"}},
		{method: http.MethodPut, path: "/api/v1/workflow-runs/" + run.ID + "/steps/bad%20step", body: map[string]any{"input_hash": validHash, "status": "running", "client_instance_id": validClientID}},
		{method: http.MethodPut, path: "/api/v1/workflow-runs/" + run.ID + "/steps/" + strings.Repeat("s", workflowStepIDMaxLength+1), body: map[string]any{"input_hash": validHash, "status": "running", "client_instance_id": validClientID}},
		{method: http.MethodPut, path: "/api/v1/workflow-runs/" + run.ID + "/steps/safe-step", body: map[string]any{"input_hash": "prompt content", "status": "running", "client_instance_id": validClientID}},
		{method: http.MethodPut, path: "/api/v1/workflow-runs/" + run.ID + "/steps/safe-step", body: map[string]any{"input_hash": validHash, "status": "running", "client_instance_id": `C:\private\client`}},
	}
	for i, req := range invalidRequests {
		resp := request(app, req.method, req.path, req.body, token)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("invalid request identity %d status = %d, body = %s", i, resp.Code, resp.Body.String())
		}
	}
}

func acquireWorkflowStep(t *testing.T, app *App, token, path, inputHash, clientID string) workflowStepTestResponse {
	t.Helper()
	resp := request(app, http.MethodPut, path, map[string]any{
		"input_hash": inputHash, "status": "running", "client_instance_id": clientID,
	}, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("acquire workflow step status = %d, body = %s", resp.Code, resp.Body.String())
	}
	return decodeWorkflowStepResponse(t, resp)
}

func assertWorkflowErrorCode(t *testing.T, resp interface {
	Result() *http.Response
}, wantStatus int, wantCode string) {
	t.Helper()
	result := resp.Result()
	defer result.Body.Close()
	if result.StatusCode != wantStatus {
		t.Fatalf("status = %d, want %d", result.StatusCode, wantStatus)
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(result.Body).Decode(&body); err != nil {
		t.Fatalf("decode workflow error: %v", err)
	}
	if body.Code != wantCode {
		t.Fatalf("error code = %q, want %q", body.Code, wantCode)
	}
}
