package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/datatypes"
)

func TestExtractWorkflowRequestIdentityFromMap(t *testing.T) {
	payload := map[string]any{
		"workflow_run_id":     "84fd6178-a1f6-4717-bb72-6f9d476d6291",
		"workflow_step_id":    "subtitle.validate",
		"workflow_input_hash": strings.Repeat("a", 64),
		"client_instance_id":  "web-client-1",
		"prompt":              "provider payload remains available",
	}

	identity, err := extractWorkflowRequestIdentityFromMap(payload)
	if err != nil {
		t.Fatalf("extract complete identity: %v", err)
	}
	if identity == nil || identity.WorkflowStepID != "subtitle.validate" {
		t.Fatalf("identity = %#v", identity)
	}
	for _, key := range workflowRequestIdentityKeys {
		if _, exists := payload[key]; exists {
			t.Fatalf("identity key %q was forwarded in provider payload", key)
		}
	}
	if payload["prompt"] != "provider payload remains available" {
		t.Fatalf("non-identity provider payload changed: %#v", payload)
	}
}

func TestParseWorkflowRequestIdentityLegacyAndPartial(t *testing.T) {
	legacy, err := extractWorkflowRequestIdentityFromMap(map[string]any{"prompt": "legacy"})
	if err != nil || legacy != nil {
		t.Fatalf("legacy identity = %#v, err = %v", legacy, err)
	}

	complete := url.Values{
		"workflow_run_id":     {"84fd6178-a1f6-4717-bb72-6f9d476d6291"},
		"workflow_step_id":    {"script.generate"},
		"workflow_input_hash": {strings.Repeat("b", 64)},
		"client_instance_id":  {"pc-client-1"},
	}
	identity, err := extractWorkflowRequestIdentityFromValues(complete)
	if err != nil || identity == nil {
		t.Fatalf("values identity = %#v, err = %v", identity, err)
	}
	for _, key := range workflowRequestIdentityKeys {
		if complete.Has(key) {
			t.Fatalf("identity key %q was not stripped from form/query values", key)
		}
	}

	complete = url.Values{
		"workflow_run_id":     {"84fd6178-a1f6-4717-bb72-6f9d476d6291"},
		"workflow_step_id":    {"script.generate"},
		"workflow_input_hash": {strings.Repeat("b", 64)},
		"client_instance_id":  {"pc-client-1"},
	}
	for _, missing := range workflowRequestIdentityKeys {
		partial := cloneURLValues(complete)
		partial.Del(missing)
		_, err := parseWorkflowRequestIdentityValues(partial)
		assertWorkflowRequestError(t, err, http.StatusBadRequest, "partial_workflow_identity")
	}
}

func TestParseWorkflowRequestIdentityRejectsInvalidValues(t *testing.T) {
	base := map[string]any{
		"workflow_run_id":     "84fd6178-a1f6-4717-bb72-6f9d476d6291",
		"workflow_step_id":    "script.generate",
		"workflow_input_hash": strings.Repeat("c", 64),
		"client_instance_id":  "client-1",
	}
	tests := map[string]any{
		"workflow_run_id":     "not-a-uuid",
		"workflow_step_id":    "../../private/subtitle.txt",
		"workflow_input_hash": "hash with spaces",
		"client_instance_id":  "client with spaces",
	}
	for field, invalid := range tests {
		t.Run(field, func(t *testing.T) {
			payload := cloneAnyMap(base)
			payload[field] = invalid
			_, err := extractWorkflowRequestIdentityFromMap(payload)
			assertWorkflowRequestError(t, err, http.StatusBadRequest, "invalid_workflow_identity")
		})
	}

	payload := cloneAnyMap(base)
	payload["workflow_step_id"] = 42
	_, err := extractWorkflowRequestIdentityFromMap(payload)
	assertWorkflowRequestError(t, err, http.StatusBadRequest, "invalid_workflow_identity")
	for _, key := range workflowRequestIdentityKeys {
		if _, exists := payload[key]; exists {
			t.Fatalf("invalid identity key %q was not stripped", key)
		}
	}
}

func TestResolveWorkflowRequestIdentityValidatesOwnershipLeaseAndStepIdentity(t *testing.T) {
	t.Run("complete identity derives and enriches billing identity", func(t *testing.T) {
		app := newTestApp(t)
		userID := uuid.New()
		projectID := uuid.New()
		run := createWorkflowIdentityTestWebRun(t, app, userID, projectID, "input-hash-1", "client-a")
		identity := workflowRequestIdentityForTest(run.ID, "subtitle.validate", run.InputHash, "client-a")

		execution, err := app.resolveWorkflowRequestIdentity(context.Background(), userID, nil, identity)
		if err != nil {
			t.Fatalf("resolve identity: %v", err)
		}
		wantKey := workflowStepExecutionKey(run.ID, identity.WorkflowStepID, identity.WorkflowInputHash)
		if execution.Step.ExecutionKey != wantKey || execution.Step.Status != workflowRunStatusRunning {
			t.Fatalf("execution = %#v, want key %q and running", execution, wantKey)
		}

		consumption := execution.EnrichCreditConsumption(creditConsumption{UserID: userID, APIEndpoint: "/test"})
		if consumption.ProjectID == nil || *consumption.ProjectID != projectID ||
			consumption.WorkflowRunID == nil || *consumption.WorkflowRunID != run.ID ||
			consumption.WorkflowStepExecutionID == nil || *consumption.WorkflowStepExecutionID != execution.Step.ID ||
			consumption.ExecutionKey != wantKey {
			t.Fatalf("enriched consumption = %#v", consumption)
		}
	})

	t.Run("same client sequential duplicate is in progress", func(t *testing.T) {
		app := newTestApp(t)
		userID := uuid.New()
		run := createWorkflowIdentityTestRun(t, app, userID, "input-hash-duplicate", "client-a")
		identity := workflowRequestIdentityForTest(run.ID, "script.generate", run.InputHash, "client-a")

		if _, err := app.resolveWorkflowRequestIdentity(context.Background(), userID, nil, identity); err != nil {
			t.Fatalf("resolve first request: %v", err)
		}
		_, err := app.resolveWorkflowRequestIdentity(context.Background(), userID, nil, identity)
		assertWorkflowRequestError(t, err, http.StatusConflict, "step_execution_in_progress")
	})

	t.Run("same client concurrent duplicate starts once", func(t *testing.T) {
		app := newTestApp(t)
		userID := uuid.New()
		run := createWorkflowIdentityTestRun(t, app, userID, "input-hash-concurrent", "client-a")
		identity := workflowRequestIdentityForTest(run.ID, "script.generate", run.InputHash, "client-a")

		const requests = 6
		start := make(chan struct{})
		errs := make(chan error, requests)
		var wg sync.WaitGroup
		for range requests {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := app.resolveWorkflowRequestIdentity(context.Background(), userID, nil, identity)
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(errs)

		succeeded := 0
		inProgress := 0
		for err := range errs {
			if err == nil {
				succeeded++
				continue
			}
			requestErr, ok := asWorkflowRequestError(err)
			if ok && requestErr.Status == http.StatusConflict && requestErr.Code == "step_execution_in_progress" {
				inProgress++
				continue
			}
			t.Fatalf("unexpected concurrent error: %v", err)
		}
		if succeeded != 1 || inProgress != requests-1 {
			t.Fatalf("concurrent results: succeeded=%d in_progress=%d", succeeded, inProgress)
		}
	})

	t.Run("cross user is hidden as not found", func(t *testing.T) {
		app := newTestApp(t)
		ownerID := uuid.New()
		run := createWorkflowIdentityTestRun(t, app, ownerID, "input-hash-2", "client-a")
		identity := workflowRequestIdentityForTest(run.ID, "script.generate", run.InputHash, "client-a")

		_, err := app.resolveWorkflowRequestIdentity(context.Background(), uuid.New(), nil, identity)
		assertWorkflowRequestError(t, err, http.StatusNotFound, "workflow_run_not_found")
	})

	t.Run("run input identity must match", func(t *testing.T) {
		app := newTestApp(t)
		userID := uuid.New()
		run := createWorkflowIdentityTestRun(t, app, userID, "input-hash-3", "client-a")
		identity := workflowRequestIdentityForTest(run.ID, "script.generate", "different-input", "client-a")

		_, err := app.resolveWorkflowRequestIdentity(context.Background(), userID, nil, identity)
		assertWorkflowRequestError(t, err, http.StatusConflict, "workflow_input_mismatch")
	})

	t.Run("stale client cannot use current run lease", func(t *testing.T) {
		app := newTestApp(t)
		userID := uuid.New()
		run := createWorkflowIdentityTestRun(t, app, userID, "input-hash-4", "client-a")
		identity := workflowRequestIdentityForTest(run.ID, "script.generate", run.InputHash, "client-b")

		_, err := app.resolveWorkflowRequestIdentity(context.Background(), userID, nil, identity)
		assertWorkflowRequestError(t, err, http.StatusConflict, "workflow_run_lease_conflict")
	})

	t.Run("expired current client lease is stale", func(t *testing.T) {
		app := newTestApp(t)
		userID := uuid.New()
		run := createWorkflowIdentityTestRun(t, app, userID, "input-hash-expired", "client-a")
		expired := time.Now().UTC().Add(-time.Second)
		if err := app.db.Model(&models.WorkflowRun{}).Where("id = ?", run.ID).Update("lease_expires_at", expired).Error; err != nil {
			t.Fatalf("expire run lease: %v", err)
		}
		identity := workflowRequestIdentityForTest(run.ID, "script.generate", run.InputHash, "client-a")

		_, err := app.resolveWorkflowRequestIdentity(context.Background(), userID, nil, identity)
		assertWorkflowRequestError(t, err, http.StatusConflict, "workflow_run_lease_conflict")
	})

	t.Run("mismatched stored step identity is rejected", func(t *testing.T) {
		app := newTestApp(t)
		userID := uuid.New()
		run := createWorkflowIdentityTestRun(t, app, userID, "input-hash-5", "client-a")
		now := time.Now().UTC()
		wrongHash := "different-step-input"
		owner := "client-a"
		step := models.WorkflowStepExecution{
			WorkflowRunID:  run.ID,
			StepID:         "script.generate",
			InputHash:      wrongHash,
			ExecutionKey:   workflowStepExecutionKey(run.ID, "script.generate", wrongHash),
			Status:         workflowRunStatusRunning,
			AttemptCount:   1,
			Usage:          datatypes.JSON([]byte("{}")),
			LeaseOwner:     &owner,
			LeaseExpiresAt: timePointer(now.Add(time.Minute)),
			StartedAt:      &now,
		}
		if err := app.db.Create(&step).Error; err != nil {
			t.Fatalf("seed mismatched step: %v", err)
		}
		identity := workflowRequestIdentityForTest(run.ID, "script.generate", run.InputHash, "client-a")

		_, err := app.resolveWorkflowRequestIdentity(context.Background(), userID, nil, identity)
		assertWorkflowRequestError(t, err, http.StatusConflict, "workflow_step_identity_mismatch")
	})
}

func TestWorkflowExecutionHeartbeatRenewsProviderStepLease(t *testing.T) {
	app := newTestApp(t)
	base := time.Now().UTC()
	current := base
	app.workflowRuns.now = func() time.Time { return current }
	userID := uuid.New()
	projectID := uuid.New()
	run := createWorkflowIdentityTestWebRun(t, app, userID, projectID, "quality-gate-heartbeat", "quality-gate-client")
	identity := workflowRequestIdentityForTest(run.ID, "review-timeline-decisions.quality-gate-review-1", run.InputHash, run.ClientInstanceID)
	execution, err := app.resolveWorkflowRequestIdentity(context.Background(), userID, &projectID, identity)
	if err != nil {
		t.Fatalf("resolve execution: %v", err)
	}
	initialLease := *execution.Step.LeaseExpiresAt

	current = current.Add(time.Minute)
	stop := app.startWorkflowExecutionHeartbeat(context.Background(), execution, time.Hour)
	stop()

	var step models.WorkflowStepExecution
	if err := app.db.Where("id = ?", execution.Step.ID).Take(&step).Error; err != nil {
		t.Fatalf("load provider step: %v", err)
	}
	if step.LeaseExpiresAt == nil || !step.LeaseExpiresAt.After(initialLease) || !workflowStepLeaseOwnedByClient(&step, identity.ClientInstanceID, current) {
		t.Fatalf("provider step lease was not renewed: %#v", step)
	}
	var refreshedRun models.WorkflowRun
	if err := app.db.Where("id = ?", run.ID).Take(&refreshedRun).Error; err != nil {
		t.Fatalf("load workflow run: %v", err)
	}
	if refreshedRun.CurrentStepID == nil || *refreshedRun.CurrentStepID != identity.WorkflowStepID {
		t.Fatalf("workflow current step = %v, want %q", refreshedRun.CurrentStepID, identity.WorkflowStepID)
	}
}

func TestWorkflowRequestIdentityLegacyAndNoBusinessContentPersistence(t *testing.T) {
	legacy := workflowRequestExecution{}
	originalProjectID := uuid.New()
	legacyConsumption := creditConsumption{ProjectID: &originalProjectID, ExecutionKey: "legacy-key"}
	got := legacy.EnrichCreditConsumption(legacyConsumption)
	if got.ProjectID == nil || *got.ProjectID != originalProjectID || got.ExecutionKey != "legacy-key" {
		t.Fatalf("legacy consumption changed: %#v", got)
	}

	app := newTestApp(t)
	userID := uuid.New()
	run := createWorkflowIdentityTestRun(t, app, userID, "privacy-input", "client-a")
	payload := map[string]any{
		"workflow_run_id":     run.ID.String(),
		"workflow_step_id":    "privacy.check",
		"workflow_input_hash": run.InputHash,
		"client_instance_id":  "client-a",
		"prompt":              "secret narration prompt",
		"subtitle":            "secret subtitle",
		"local_path":          `C:\\private\\video.mp4`,
	}
	identity, err := extractWorkflowRequestIdentityFromMap(payload)
	if err != nil {
		t.Fatalf("extract privacy identity: %v", err)
	}
	execution, err := app.resolveWorkflowRequestIdentity(context.Background(), userID, nil, identity)
	if err != nil {
		t.Fatalf("resolve privacy identity: %v", err)
	}
	var stored models.WorkflowStepExecution
	if err := app.db.First(&stored, "id = ?", execution.Step.ID).Error; err != nil {
		t.Fatalf("load stored step: %v", err)
	}
	raw, _ := json.Marshal(stored)
	for _, forbidden := range []string{"secret narration prompt", "secret subtitle", `C:\\private\\video.mp4`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("stored workflow step contains business content %q: %s", forbidden, raw)
		}
	}
}

func TestRespondWorkflowRequestErrorUsesStructuredContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/tools/test", nil)

	err := newWorkflowRequestError(http.StatusBadRequest, "partial_workflow_identity", false, "all workflow identity fields are required", nil)
	respondWorkflowRequestError(c, err)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var body workflowRequestErrorResponse
	if decodeErr := json.Unmarshal(recorder.Body.Bytes(), &body); decodeErr != nil {
		t.Fatalf("decode response: %v", decodeErr)
	}
	if body.RequestID == "" || body.Code != "partial_workflow_identity" || body.Retryable || body.Message == "" {
		t.Fatalf("response = %#v", body)
	}
	if recorder.Header().Get("X-Request-ID") != body.RequestID {
		t.Fatalf("X-Request-ID = %q, body request_id = %q", recorder.Header().Get("X-Request-ID"), body.RequestID)
	}
}

func createWorkflowIdentityTestRun(t *testing.T, app *App, userID uuid.UUID, inputHash, clientID string) models.WorkflowRun {
	t.Helper()
	result, err := app.workflowRuns.Create(context.Background(), CreateWorkflowRunInput{
		UserID:            userID,
		ClientType:        "pc",
		ClientTaskID:      stringPointer("task-" + uuid.NewString()),
		ClientProjectName: "source.mp4",
		InputHash:         inputHash,
		ClientInstanceID:  clientID,
	})
	if err != nil {
		t.Fatalf("create workflow run: %v", err)
	}
	return result.Run
}

func createWorkflowIdentityTestWebRun(t *testing.T, app *App, userID, projectID uuid.UUID, inputHash, clientID string) models.WorkflowRun {
	t.Helper()
	now := time.Now().UTC()
	leaseExpiresAt := now.Add(workflowRunLeaseDuration)
	run := models.WorkflowRun{
		UserID:           userID,
		ProjectID:        &projectID,
		ClientType:       "web",
		InputHash:        inputHash,
		Status:           workflowRunStatusRunning,
		BillingStatus:    workflowRunBillingStatusPending,
		ClientInstanceID: clientID,
		LeaseExpiresAt:   &leaseExpiresAt,
		LastActiveAt:     now,
		ExpiresAt:        now.Add(workflowRunInactivityDuration),
	}
	if err := app.db.Create(&run).Error; err != nil {
		t.Fatalf("create web workflow run: %v", err)
	}
	return run
}

func workflowRequestIdentityForTest(runID uuid.UUID, stepID, inputHash, clientID string) *workflowRequestIdentity {
	return &workflowRequestIdentity{
		WorkflowRunID:     runID,
		WorkflowStepID:    stepID,
		WorkflowInputHash: inputHash,
		ClientInstanceID:  clientID,
	}
}

func assertWorkflowRequestError(t *testing.T, err error, status int, code string) {
	t.Helper()
	requestErr, ok := asWorkflowRequestError(err)
	if !ok {
		t.Fatalf("error = %v, want workflow request error %s", err, code)
	}
	if requestErr.Status != status || requestErr.Code != code {
		t.Fatalf("error = %#v, want status %d code %q", requestErr, status, code)
	}
}

func cloneAnyMap(input map[string]any) map[string]any {
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func cloneURLValues(input url.Values) url.Values {
	cloned := make(url.Values, len(input))
	for key, values := range input {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

func stringPointer(value string) *string { return &value }
