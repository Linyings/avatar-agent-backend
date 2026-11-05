package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type workflowRunTestResponse struct {
	ID                string     `json:"id"`
	UserID            string     `json:"user_id"`
	ProjectID         *string    `json:"project_id"`
	ClientType        string     `json:"client_type"`
	ClientTaskID      *string    `json:"client_task_id"`
	ClientProjectName string     `json:"client_project_name"`
	InputHash         string     `json:"input_hash"`
	Status            string     `json:"status"`
	BillingStatus     string     `json:"billing_status"`
	CurrentStepID     *string    `json:"current_step_id"`
	LastErrorCode     *string    `json:"last_error_code"`
	LastErrorMessage  *string    `json:"last_error_message"`
	FinalResultAt     *time.Time `json:"final_result_at"`
	CompletedAt       *time.Time `json:"completed_at"`
	CancelledAt       *time.Time `json:"cancelled_at"`
	ClientInstanceID  string     `json:"client_instance_id"`
	LeaseExpiresAt    *time.Time `json:"lease_expires_at"`
	LastActiveAt      time.Time  `json:"last_active_at"`
	ExpiresAt         time.Time  `json:"expires_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type workflowStepTestResponse struct {
	ID                string          `json:"id"`
	WorkflowRunID     string          `json:"workflow_run_id"`
	StepID            string          `json:"step_id"`
	InputHash         string          `json:"input_hash"`
	ExecutionKey      string          `json:"execution_key"`
	Status            string          `json:"status"`
	AttemptCount      int             `json:"attempt_count"`
	ProviderRequestID *string         `json:"provider_request_id"`
	ErrorCode         *string         `json:"error_code"`
	ErrorMessage      *string         `json:"error_message"`
	Usage             json.RawMessage `json:"usage"`
	LeaseOwner        *string         `json:"lease_owner"`
	LeaseExpiresAt    *time.Time      `json:"lease_expires_at"`
	StartedAt         *time.Time      `json:"started_at"`
	CompletedAt       *time.Time      `json:"completed_at"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

func TestWorkflowRunMigrationRegistersTables(t *testing.T) {
	app := newTestApp(t)
	for _, table := range []string{"workflow_runs", "workflow_step_executions"} {
		if !app.db.Migrator().HasTable(table) {
			t.Fatalf("expected migration to create %s", table)
		}
	}
}

func TestWorkflowRunMigrationCreatesRequiredIndexes(t *testing.T) {
	app := newTestApp(t)
	expected := map[string][]string{
		"uq_workflow_runs_web_active_scope":                {"unique index", "workflow_runs", "project_id", "where", "client_type = 'web'", "status in"},
		"uq_workflow_runs_pc_active_scope":                 {"unique index", "workflow_runs", "user_id", "client_task_id", "where", "client_type = 'pc'", "status in"},
		"idx_workflow_runs_active_expiry":                  {"index", "workflow_runs", "expires_at", "where", "status in"},
		"idx_workflow_step_executions_run_created":         {"index", "workflow_step_executions", "workflow_run_id", "created_at"},
		"idx_workflow_step_executions_provider_submission": {"index", "workflow_step_executions", "workflow_run_id", "step_id", "provider_input_hash", "provider_request_id", "where"},
		"idx_workflow_step_executions_execution_key":       {"unique index", "workflow_step_executions", "execution_key"},
	}
	for name, fragments := range expected {
		var sql string
		if err := app.db.Raw("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?", name).Scan(&sql).Error; err != nil {
			t.Fatalf("load index %s: %v", name, err)
		}
		if sql == "" {
			t.Fatalf("required index %s is missing", name)
		}
		normalized := strings.ToLower(sql)
		for _, fragment := range fragments {
			if !strings.Contains(normalized, fragment) {
				t.Fatalf("index %s SQL %q missing %q", name, sql, fragment)
			}
		}
	}
}

func TestWorkflowRunCreateValidatesWebAndPCScopes(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-create@example.com")
	projectID := createWorkflowRunTestProject(t, app, token, "Workflow project")

	tests := []struct {
		name       string
		body       map[string]any
		wantStatus int
	}{
		{name: "web requires project", body: map[string]any{"client_type": "web", "input_hash": "hash-web", "client_instance_id": "web-a"}, wantStatus: http.StatusBadRequest},
		{name: "web accepts owned project", body: map[string]any{"client_type": "web", "project_id": projectID, "input_hash": "hash-web", "client_instance_id": "web-a"}, wantStatus: http.StatusCreated},
		{name: "web rejects client project name", body: map[string]any{"client_type": "web", "project_id": projectID, "client_project_name": "web.mp4", "input_hash": "hash-web-name", "client_instance_id": "web-b"}, wantStatus: http.StatusBadRequest},
		{name: "pc requires task id", body: map[string]any{"client_type": "pc", "input_hash": "hash-pc", "client_instance_id": "pc-a"}, wantStatus: http.StatusBadRequest},
		{name: "pc requires project name", body: map[string]any{"client_type": "pc", "client_task_id": "task-without-name", "input_hash": "hash-pc-name", "client_instance_id": "pc-a"}, wantStatus: http.StatusBadRequest},
		{name: "pc rejects project", body: map[string]any{"client_type": "pc", "project_id": projectID, "client_task_id": "task-with-project", "client_project_name": "source.mp4", "input_hash": "hash-pc", "client_instance_id": "pc-a"}, wantStatus: http.StatusBadRequest},
		{name: "pc rejects oversized project name", body: map[string]any{"client_type": "pc", "client_task_id": "task-long-name", "client_project_name": strings.Repeat("x", 201), "input_hash": "hash-pc-long-name", "client_instance_id": "pc-a"}, wantStatus: http.StatusBadRequest},
		{name: "pc accepts local task", body: map[string]any{"client_type": "pc", "client_task_id": "desktop-task", "client_project_name": "source.mp4", "input_hash": "hash-pc", "client_instance_id": "pc-a"}, wantStatus: http.StatusCreated},
		{name: "rejects unknown client", body: map[string]any{"client_type": "mobile", "input_hash": "hash-mobile"}, wantStatus: http.StatusBadRequest},
		{name: "requires input hash", body: map[string]any{"client_type": "pc", "client_task_id": "missing-hash"}, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := request(app, http.MethodPost, "/api/v1/workflow-runs/", tt.body, token)
			if resp.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", resp.Code, tt.wantStatus, resp.Body.String())
			}
		})
	}
}

func TestWorkflowRunCreateTrimsAndStoresPCProjectName(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-pc-project-name@example.com")
	resp := request(app, http.MethodPost, "/api/v1/workflow-runs/", map[string]any{
		"client_type":         "pc",
		"client_task_id":      "desktop-project-name",
		"client_project_name": " source.mp4 ",
		"input_hash":          "hash-pc-project-name",
		"client_instance_id":  "pc-name-client",
	}, token)
	if resp.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	run := decodeWorkflowRunResponse(t, resp)
	if run.ClientProjectName != "source.mp4" {
		t.Fatalf("response client_project_name = %q", run.ClientProjectName)
	}
	var stored struct{ ClientProjectName string }
	if err := app.db.Table("workflow_runs").Select("client_project_name").Where("id = ?", run.ID).Take(&stored).Error; err != nil {
		t.Fatalf("load stored project name: %v", err)
	}
	if stored.ClientProjectName != "source.mp4" {
		t.Fatalf("stored client_project_name = %q", stored.ClientProjectName)
	}
}

func TestSyncPCWorkflowProjectNamesBackfillsOwnedRunsAndLedgersIdempotently(t *testing.T) {
	app := newTestApp(t)
	ownerToken := registerAndLogin(t, app, "workflow-sync-owner@example.com")
	foreignToken := registerAndLogin(t, app, "workflow-sync-foreign@example.com")
	owner := lookupUserByEmail(t, app, "workflow-sync-owner@example.com")
	foreign := lookupUserByEmail(t, app, "workflow-sync-foreign@example.com")

	missing := createWorkflowRunTestPC(t, app, ownerToken, "sync-missing", "sync-missing-hash", "sync-owner-client")
	existing := createWorkflowRunTestPC(t, app, ownerToken, "sync-existing", "sync-existing-hash", "sync-owner-client")
	foreignRun := createWorkflowRunTestPC(t, app, foreignToken, "sync-missing", "sync-foreign-hash", "sync-foreign-client")
	if err := app.db.Model(&models.WorkflowRun{}).Where("id IN ?", []string{missing.ID, foreignRun.ID}).Update("client_project_name", "").Error; err != nil {
		t.Fatalf("clear historical run names: %v", err)
	}
	if err := app.db.Model(&models.WorkflowRun{}).Where("id = ?", existing.ID).Update("client_project_name", "keep.mp4").Error; err != nil {
		t.Fatalf("seed existing run name: %v", err)
	}

	missingMetadata, _ := json.Marshal(map[string]any{"workflow_run_id": missing.ID})
	existingMetadata, _ := json.Marshal(map[string]any{"workflow_run_id": existing.ID})
	foreignMetadata, _ := json.Marshal(map[string]any{"workflow_run_id": foreignRun.ID})
	ledgers := []models.CreditLedgerRecord{
		{UserID: owner.ID, EventType: creditLedgerEventConsume, ChangeAmount: -1, BalanceAfter: 9, RequestMetadata: datatypes.JSON(missingMetadata)},
		{UserID: owner.ID, ProjectName: "keep.mp4", EventType: creditLedgerEventConsume, ChangeAmount: -1, BalanceAfter: 8, RequestMetadata: datatypes.JSON(existingMetadata)},
		{UserID: foreign.ID, EventType: creditLedgerEventConsume, ChangeAmount: -1, BalanceAfter: 9, RequestMetadata: datatypes.JSON(foreignMetadata)},
	}
	if err := app.db.Create(&ledgers).Error; err != nil {
		t.Fatalf("seed historical ledgers: %v", err)
	}
	var projectsBefore, reservationsBefore, ledgersBefore int64
	app.db.Model(&models.Project{}).Count(&projectsBefore)
	app.db.Model(&models.CreditReservation{}).Count(&reservationsBefore)
	app.db.Model(&models.CreditLedgerRecord{}).Count(&ledgersBefore)

	body := map[string]any{"items": []map[string]any{
		{"client_task_id": "sync-missing", "project_name": " restored.mp4 "},
		{"client_task_id": "sync-existing", "project_name": "overwrite.mp4"},
		{"client_task_id": "unknown-task", "project_name": "unknown.mp4"},
	}}
	resp := request(app, http.MethodPost, "/api/v1/workflow-runs/pc/project-names/sync", body, ownerToken)
	if resp.Code != http.StatusOK {
		t.Fatalf("sync status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var result struct {
		Matched              int64 `json:"matched"`
		WorkflowRunsUpdated  int64 `json:"workflow_runs_updated"`
		LedgerRecordsUpdated int64 `json:"ledger_records_updated"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode sync response: %v", err)
	}
	if result.Matched != 2 || result.WorkflowRunsUpdated != 1 || result.LedgerRecordsUpdated != 1 {
		t.Fatalf("sync result = %#v", result)
	}

	assertWorkflowProjectName := func(id, want string) {
		t.Helper()
		var run models.WorkflowRun
		if err := app.db.First(&run, "id = ?", id).Error; err != nil {
			t.Fatalf("load run %s: %v", id, err)
		}
		if run.ClientProjectName != want {
			t.Fatalf("run %s project name = %q, want %q", id, run.ClientProjectName, want)
		}
	}
	assertWorkflowProjectName(missing.ID, "restored.mp4")
	assertWorkflowProjectName(existing.ID, "keep.mp4")
	assertWorkflowProjectName(foreignRun.ID, "")
	for index, want := range []string{"restored.mp4", "keep.mp4", ""} {
		var ledger models.CreditLedgerRecord
		if err := app.db.First(&ledger, "id = ?", ledgers[index].ID).Error; err != nil {
			t.Fatalf("load ledger %d: %v", index, err)
		}
		if ledger.ProjectName != want {
			t.Fatalf("ledger %d project name = %q, want %q", index, ledger.ProjectName, want)
		}
	}

	repeat := request(app, http.MethodPost, "/api/v1/workflow-runs/pc/project-names/sync", body, ownerToken)
	if repeat.Code != http.StatusOK {
		t.Fatalf("repeat sync status = %d, body = %s", repeat.Code, repeat.Body.String())
	}
	if err := json.Unmarshal(repeat.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode repeat sync response: %v", err)
	}
	if result.Matched != 2 || result.WorkflowRunsUpdated != 0 || result.LedgerRecordsUpdated != 0 {
		t.Fatalf("repeat sync result = %#v", result)
	}
	var projectsAfter, reservationsAfter, ledgersAfter int64
	app.db.Model(&models.Project{}).Count(&projectsAfter)
	app.db.Model(&models.CreditReservation{}).Count(&reservationsAfter)
	app.db.Model(&models.CreditLedgerRecord{}).Count(&ledgersAfter)
	if projectsAfter != projectsBefore || reservationsAfter != reservationsBefore || ledgersAfter != ledgersBefore {
		t.Fatalf("sync created side effects: projects %d/%d reservations %d/%d ledgers %d/%d", projectsBefore, projectsAfter, reservationsBefore, reservationsAfter, ledgersBefore, ledgersAfter)
	}
}

func TestSyncPCWorkflowProjectNamesRejectsInvalidBatchWithoutPartialUpdates(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-sync-invalid@example.com")
	run := createWorkflowRunTestPC(t, app, token, "sync-invalid", "sync-invalid-hash", "sync-invalid-client")
	if err := app.db.Model(&models.WorkflowRun{}).Where("id = ?", run.ID).Update("client_project_name", "").Error; err != nil {
		t.Fatalf("clear historical run name: %v", err)
	}

	invalid := request(app, http.MethodPost, "/api/v1/workflow-runs/pc/project-names/sync", map[string]any{"items": []map[string]any{
		{"client_task_id": "sync-invalid", "project_name": "would-update.mp4"},
		{"client_task_id": "bad task id", "project_name": "bad.mp4"},
	}}, token)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d, body = %s", invalid.Code, invalid.Body.String())
	}
	items := make([]map[string]any, 101)
	for index := range items {
		items[index] = map[string]any{"client_task_id": fmt.Sprintf("task-%d", index), "project_name": "source.mp4"}
	}
	oversized := request(app, http.MethodPost, "/api/v1/workflow-runs/pc/project-names/sync", map[string]any{"items": items}, token)
	if oversized.Code != http.StatusBadRequest {
		t.Fatalf("oversized status = %d, body = %s", oversized.Code, oversized.Body.String())
	}
	var stored models.WorkflowRun
	if err := app.db.First(&stored, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("load unchanged run: %v", err)
	}
	if stored.ClientProjectName != "" {
		t.Fatalf("invalid batch partially updated name to %q", stored.ClientProjectName)
	}
}

func TestWorkflowRunCreateReusesMatchingActiveRunAndRejectsHashConflict(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-reuse@example.com")
	projectID := createWorkflowRunTestProject(t, app, token, "Reusable project")
	body := map[string]any{
		"client_type":        "web",
		"project_id":         projectID,
		"input_hash":         "stable-input-hash",
		"client_instance_id": "browser-a",
	}

	created := request(app, http.MethodPost, "/api/v1/workflow-runs/", body, token)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	first := decodeWorkflowRunResponse(t, created)

	reused := request(app, http.MethodPost, "/api/v1/workflow-runs/", body, token)
	if reused.Code != http.StatusOK {
		t.Fatalf("reuse status = %d, body = %s", reused.Code, reused.Body.String())
	}
	second := decodeWorkflowRunResponse(t, reused)
	if second.ID != first.ID {
		t.Fatalf("reused run id = %s, want %s", second.ID, first.ID)
	}

	body["input_hash"] = "changed-input-hash"
	conflict := request(app, http.MethodPost, "/api/v1/workflow-runs/", body, token)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, body = %s", conflict.Code, conflict.Body.String())
	}
}

func TestWorkflowRunConcurrentCreateNormalizesWinnerAndInputConflict(t *testing.T) {
	t.Run("same input returns one created run and one reused run", func(t *testing.T) {
		app := newTestApp(t)
		token := registerAndLogin(t, app, "workflow-concurrent-reuse@example.com")
		projectID := createWorkflowRunTestProject(t, app, token, "Concurrent reusable project")
		body := map[string]any{
			"client_type":        "web",
			"project_id":         projectID,
			"input_hash":         "same-concurrent-input",
			"client_instance_id": "browser-a",
		}
		installWorkflowCreateBarrier(t, app, "workflow_runs", 2)

		responses := concurrentWorkflowRequests(app, token,
			workflowTestRequest{method: http.MethodPost, path: "/api/v1/workflow-runs/", body: body},
			workflowTestRequest{method: http.MethodPost, path: "/api/v1/workflow-runs/", body: body},
		)
		assertWorkflowResponseStatuses(t, responses, http.StatusCreated, http.StatusOK)
		first := decodeWorkflowRunResponse(t, responses[0])
		second := decodeWorkflowRunResponse(t, responses[1])
		if first.ID == "" || first.ID != second.ID {
			t.Fatalf("concurrent matching creates returned different runs: first=%s second=%s", first.ID, second.ID)
		}

		var count int64
		if err := app.db.Table("workflow_runs").Where("project_id = ?", projectID).Count(&count).Error; err != nil {
			t.Fatalf("count workflow runs: %v", err)
		}
		if count != 1 {
			t.Fatalf("workflow run count = %d, want 1", count)
		}
	})

	t.Run("different input returns one created run and one conflict", func(t *testing.T) {
		app := newTestApp(t)
		token := registerAndLogin(t, app, "workflow-concurrent-conflict@example.com")
		projectID := createWorkflowRunTestProject(t, app, token, "Concurrent conflicting project")
		installWorkflowCreateBarrier(t, app, "workflow_runs", 2)

		responses := concurrentWorkflowRequests(app, token,
			workflowTestRequest{method: http.MethodPost, path: "/api/v1/workflow-runs/", body: map[string]any{
				"client_type": "web", "project_id": projectID, "input_hash": "input-a", "client_instance_id": "browser-a",
			}},
			workflowTestRequest{method: http.MethodPost, path: "/api/v1/workflow-runs/", body: map[string]any{
				"client_type": "web", "project_id": projectID, "input_hash": "input-b", "client_instance_id": "browser-b",
			}},
		)
		assertWorkflowResponseStatuses(t, responses, http.StatusCreated, http.StatusConflict)

		var count int64
		if err := app.db.Table("workflow_runs").Where("project_id = ?", projectID).Count(&count).Error; err != nil {
			t.Fatalf("count workflow runs: %v", err)
		}
		if count != 1 {
			t.Fatalf("workflow run count = %d, want 1", count)
		}
	})
}

func TestWorkflowRunRejectsCrossUserAccessOnEveryEndpoint(t *testing.T) {
	app := newTestApp(t)
	ownerToken := registerAndLogin(t, app, "workflow-owner@example.com")
	otherToken := registerAndLogin(t, app, "workflow-other@example.com")
	run := createWorkflowRunTestPC(t, app, ownerToken, "owner-task", "owner-hash", "owner-client")
	finalResultAt := time.Now().UTC().Format(time.RFC3339Nano)

	tests := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{name: "get", method: http.MethodGet, path: "/api/v1/workflow-runs/" + run.ID},
		{name: "heartbeat", method: http.MethodPost, path: "/api/v1/workflow-runs/" + run.ID + "/heartbeat", body: map[string]any{"client_instance_id": "other"}},
		{name: "resume", method: http.MethodPost, path: "/api/v1/workflow-runs/" + run.ID + "/resume", body: map[string]any{"client_instance_id": "other"}},
		{name: "interrupt", method: http.MethodPost, path: "/api/v1/workflow-runs/" + run.ID + "/interrupt", body: map[string]any{"client_instance_id": "other"}},
		{name: "complete", method: http.MethodPost, path: "/api/v1/workflow-runs/" + run.ID + "/complete", body: map[string]any{"final_result_at": finalResultAt}},
		{name: "cancel", method: http.MethodPost, path: "/api/v1/workflow-runs/" + run.ID + "/cancel"},
		{name: "step", method: http.MethodPut, path: "/api/v1/workflow-runs/" + run.ID + "/steps/step-1", body: map[string]any{"input_hash": "step-hash", "status": "running", "client_instance_id": "other"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := request(app, tt.method, tt.path, tt.body, otherToken)
			if resp.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404, body = %s", resp.Code, resp.Body.String())
			}
		})
	}
}

func TestWorkflowRunHeartbeatAndResumeRenewLeaseAndTakeOver(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-lease@example.com")
	run := createWorkflowRunTestPC(t, app, token, "lease-task", "lease-hash", "client-a")
	now := time.Now().UTC()

	heartbeat := request(app, http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/heartbeat", map[string]any{
		"client_instance_id": "client-a",
		"current_step_id":    "step-voiceover",
	}, token)
	if heartbeat.Code != http.StatusOK {
		t.Fatalf("heartbeat status = %d, body = %s", heartbeat.Code, heartbeat.Body.String())
	}
	heartbeated := decodeWorkflowRunResponse(t, heartbeat)
	assertWorkflowRunLease(t, heartbeated, "client-a", now)
	if heartbeated.CurrentStepID == nil || *heartbeated.CurrentStepID != "step-voiceover" {
		t.Fatalf("current_step_id = %#v", heartbeated.CurrentStepID)
	}

	conflict := request(app, http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/heartbeat", map[string]any{"client_instance_id": "client-b"}, token)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("competing heartbeat status = %d, body = %s", conflict.Code, conflict.Body.String())
	}

	interruptedResp := request(app, http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/interrupt", map[string]any{
		"client_instance_id": "client-a",
		"error_code":         "network_unavailable",
		"error_message":      "network unavailable",
	}, token)
	if interruptedResp.Code != http.StatusOK {
		t.Fatalf("interrupt status = %d, body = %s", interruptedResp.Code, interruptedResp.Body.String())
	}
	interrupted := decodeWorkflowRunResponse(t, interruptedResp)
	if interrupted.Status != "interrupted" || interrupted.BillingStatus != "pending" {
		t.Fatalf("interrupt state = status %q billing %q", interrupted.Status, interrupted.BillingStatus)
	}

	resumeNow := time.Now().UTC()
	resumedResp := request(app, http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/resume", map[string]any{"client_instance_id": "client-b"}, token)
	if resumedResp.Code != http.StatusOK {
		t.Fatalf("resume status = %d, body = %s", resumedResp.Code, resumedResp.Body.String())
	}
	resumed := decodeWorkflowRunResponse(t, resumedResp)
	if resumed.Status != "running" {
		t.Fatalf("resume status field = %q", resumed.Status)
	}
	assertWorkflowRunLease(t, resumed, "client-b", resumeNow)
}

func TestWorkflowRunResumeTransfersActiveStepLease(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-step-takeover@example.com")
	run := createWorkflowRunTestPC(t, app, token, "step-takeover-task", "step-takeover-hash", "client-a")
	path := "/api/v1/workflow-runs/" + run.ID + "/steps/generate-script"

	first := request(app, http.MethodPut, path, map[string]any{
		"input_hash": "step-input", "status": "running", "client_instance_id": "client-a",
	}, token)
	if first.Code != http.StatusOK {
		t.Fatalf("first step status = %d, body = %s", first.Code, first.Body.String())
	}
	if resp := request(app, http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/interrupt", map[string]any{
		"client_instance_id": "client-a", "error_code": "network_unavailable",
	}, token); resp.Code != http.StatusOK {
		t.Fatalf("interrupt status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if resp := request(app, http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/resume", map[string]any{
		"client_instance_id": "client-b",
	}, token); resp.Code != http.StatusOK {
		t.Fatalf("resume status = %d, body = %s", resp.Code, resp.Body.String())
	}

	continuedResp := request(app, http.MethodPut, path, map[string]any{
		"input_hash": "step-input", "status": "running", "client_instance_id": "client-b",
	}, token)
	if continuedResp.Code != http.StatusOK {
		t.Fatalf("new client could not immediately continue step: status = %d, body = %s", continuedResp.Code, continuedResp.Body.String())
	}
	continued := decodeWorkflowStepResponse(t, continuedResp)
	if continued.LeaseOwner == nil || *continued.LeaseOwner != "client-b" || continued.AttemptCount != 1 {
		t.Fatalf("transferred step lease = %#v", continued)
	}

	stale := request(app, http.MethodPut, path, map[string]any{
		"input_hash": "step-input", "status": "succeeded", "client_instance_id": "client-a",
	}, token)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale client mutation status = %d, want 409, body = %s", stale.Code, stale.Body.String())
	}
}

func TestWorkflowRunInterruptRequiresCurrentClientInstance(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-interrupt-owner@example.com")
	run := createWorkflowRunTestPC(t, app, token, "interrupt-owner-task", "interrupt-owner-hash", "client-a")
	resume := request(app, http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/resume", map[string]any{
		"client_instance_id": "client-b",
	}, token)
	if resume.Code != http.StatusOK {
		t.Fatalf("resume status = %d, body = %s", resume.Code, resume.Body.String())
	}

	missing := request(app, http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/interrupt", map[string]any{}, token)
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing client interrupt status = %d, want 400, body = %s", missing.Code, missing.Body.String())
	}
	stale := request(app, http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/interrupt", map[string]any{
		"client_instance_id": "client-a",
	}, token)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale client interrupt status = %d, want 409, body = %s", stale.Code, stale.Body.String())
	}
	current := request(app, http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/interrupt", map[string]any{
		"client_instance_id": "client-b",
	}, token)
	if current.Code != http.StatusOK {
		t.Fatalf("current client interrupt status = %d, body = %s", current.Code, current.Body.String())
	}
	if resp := request(app, http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/resume", map[string]any{
		"client_instance_id": "client-c",
	}, token); resp.Code != http.StatusOK {
		t.Fatalf("second resume status = %d, body = %s", resp.Code, resp.Body.String())
	}
	invalidDiagnostic := request(app, http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/interrupt", map[string]any{
		"client_instance_id": "client-c",
		"error_message":      "C:\\private\\subtitle.txt contains secret dialogue",
	}, token)
	if invalidDiagnostic.Code != http.StatusBadRequest {
		t.Fatalf("content-bearing interrupt diagnostic status = %d, want 400, body = %s", invalidDiagnostic.Code, invalidDiagnostic.Body.String())
	}
}

func TestWorkflowRunCompleteAndCancelAreIdempotentWithBillingFinalization(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-terminal@example.com")

	cancelRun := createWorkflowRunTestPC(t, app, token, "cancel-task", "cancel-hash", "client-cancel")
	cancelledResp := request(app, http.MethodPost, "/api/v1/workflow-runs/"+cancelRun.ID+"/cancel", nil, token)
	if cancelledResp.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body = %s", cancelledResp.Code, cancelledResp.Body.String())
	}
	cancelled := decodeWorkflowRunResponse(t, cancelledResp)
	if cancelled.Status != "cancelled" || cancelled.CancelledAt == nil || cancelled.BillingStatus != "released" {
		t.Fatalf("cancelled run = %#v", cancelled)
	}
	cancelledAgainResp := request(app, http.MethodPost, "/api/v1/workflow-runs/"+cancelRun.ID+"/cancel", nil, token)
	if cancelledAgainResp.Code != http.StatusOK {
		t.Fatalf("second cancel status = %d, body = %s", cancelledAgainResp.Code, cancelledAgainResp.Body.String())
	}
	cancelledAgain := decodeWorkflowRunResponse(t, cancelledAgainResp)
	if cancelledAgain.CancelledAt == nil || !cancelledAgain.CancelledAt.Equal(*cancelled.CancelledAt) {
		t.Fatalf("idempotent cancel changed timestamp: first=%v second=%v", cancelled.CancelledAt, cancelledAgain.CancelledAt)
	}

	completeRun := createWorkflowRunTestPC(t, app, token, "complete-task", "complete-hash", "client-complete")
	finalResultAt := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	completeBody := map[string]any{"final_result_at": finalResultAt.Format(time.RFC3339Nano)}
	completedResp := request(app, http.MethodPost, "/api/v1/workflow-runs/"+completeRun.ID+"/complete", completeBody, token)
	if completedResp.Code != http.StatusOK {
		t.Fatalf("complete status = %d, body = %s", completedResp.Code, completedResp.Body.String())
	}
	completed := decodeWorkflowRunResponse(t, completedResp)
	if completed.Status != "succeeded" || completed.CompletedAt == nil || completed.FinalResultAt == nil || completed.BillingStatus != "confirmed" {
		t.Fatalf("completed run = %#v", completed)
	}
	if !completed.FinalResultAt.Equal(finalResultAt) {
		t.Fatalf("final_result_at = %v, want %v", completed.FinalResultAt, finalResultAt)
	}
	completedAgainResp := request(app, http.MethodPost, "/api/v1/workflow-runs/"+completeRun.ID+"/complete", completeBody, token)
	if completedAgainResp.Code != http.StatusOK {
		t.Fatalf("second complete status = %d, body = %s", completedAgainResp.Code, completedAgainResp.Body.String())
	}
	completedAgain := decodeWorkflowRunResponse(t, completedAgainResp)
	if completedAgain.CompletedAt == nil || !completedAgain.CompletedAt.Equal(*completed.CompletedAt) {
		t.Fatalf("idempotent complete changed timestamp: first=%v second=%v", completed.CompletedAt, completedAgain.CompletedAt)
	}

	cancelAfterFinal := request(app, http.MethodPost, "/api/v1/workflow-runs/"+completeRun.ID+"/cancel", nil, token)
	if cancelAfterFinal.Code != http.StatusConflict {
		t.Fatalf("cancel after final result status = %d, body = %s", cancelAfterFinal.Code, cancelAfterFinal.Body.String())
	}

	missingFinal := createWorkflowRunTestPC(t, app, token, "missing-final-task", "missing-final-hash", "client-missing")
	missingFinalResp := request(app, http.MethodPost, "/api/v1/workflow-runs/"+missingFinal.ID+"/complete", map[string]any{}, token)
	if missingFinalResp.Code != http.StatusBadRequest {
		t.Fatalf("complete without final_result_at status = %d, body = %s", missingFinalResp.Code, missingFinalResp.Body.String())
	}
}

func TestWorkflowRunExpiresAfterTwentyFourHoursOfInactivity(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-expiry@example.com")
	run := createWorkflowRunTestPC(t, app, token, "expiry-task", "expiry-hash", "client-expiry")
	stale := time.Now().UTC().Add(-time.Minute)
	if err := app.db.Exec("UPDATE workflow_runs SET expires_at = ?, last_active_at = ? WHERE id = ?", stale, stale.Add(-24*time.Hour), run.ID).Error; err != nil {
		t.Fatalf("age workflow run: %v", err)
	}

	resp := request(app, http.MethodGet, "/api/v1/workflow-runs/"+run.ID, nil, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("get expired status = %d, body = %s", resp.Code, resp.Body.String())
	}
	expired := decodeWorkflowRunResponse(t, resp)
	if expired.Status != "expired" {
		t.Fatalf("status = %q, want expired", expired.Status)
	}
	if expired.BillingStatus != "released" {
		t.Fatalf("billing_status = %q, want released", expired.BillingStatus)
	}
}

func TestWorkflowStepStableExecutionKeyAndConcurrentLeaseConflict(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-step@example.com")
	run := createWorkflowRunTestPC(t, app, token, "step-task", "step-run-hash", "client-a")
	path := "/api/v1/workflow-runs/" + run.ID + "/steps/generate-script"
	body := map[string]any{
		"input_hash":         "step-input-hash",
		"status":             "running",
		"client_instance_id": "client-a",
		"execution_key":      "client-supplied-key-must-be-ignored",
		"video":              "must not be persisted",
		"prompt":             "must not be persisted",
		"local_path":         "C:/private/video.mp4",
	}

	firstResp := request(app, http.MethodPut, path, body, token)
	if firstResp.Code != http.StatusOK {
		t.Fatalf("first step status = %d, body = %s", firstResp.Code, firstResp.Body.String())
	}
	first := decodeWorkflowStepResponse(t, firstResp)
	wantKeyBytes := sha256.Sum256([]byte(run.ID + "|generate-script|step-input-hash"))
	wantKey := hex.EncodeToString(wantKeyBytes[:])
	if first.ExecutionKey != wantKey {
		t.Fatalf("execution_key = %q, want %q", first.ExecutionKey, wantKey)
	}
	if first.AttemptCount != 1 || first.LeaseOwner == nil || *first.LeaseOwner != "client-a" {
		t.Fatalf("first attempt lease = %#v", first)
	}

	sameOwnerResp := request(app, http.MethodPut, path, body, token)
	if sameOwnerResp.Code != http.StatusOK {
		t.Fatalf("same owner step status = %d, body = %s", sameOwnerResp.Code, sameOwnerResp.Body.String())
	}
	sameOwner := decodeWorkflowStepResponse(t, sameOwnerResp)
	if sameOwner.ID != first.ID || sameOwner.ExecutionKey != first.ExecutionKey || sameOwner.AttemptCount != 1 {
		t.Fatalf("same owner retry changed execution identity or attempt: first=%#v retry=%#v", first, sameOwner)
	}

	body["client_instance_id"] = "client-b"
	conflict := request(app, http.MethodPut, path, body, token)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("competing client status = %d, body = %s", conflict.Code, conflict.Body.String())
	}

	if err := app.db.Exec("UPDATE workflow_step_executions SET lease_expires_at = ? WHERE execution_key = ?", time.Now().UTC().Add(-time.Second), wantKey).Error; err != nil {
		t.Fatalf("expire step lease: %v", err)
	}
	if resp := request(app, http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/resume", map[string]any{"client_instance_id": "client-b"}, token); resp.Code != http.StatusOK {
		t.Fatalf("resume before takeover status = %d, body = %s", resp.Code, resp.Body.String())
	}
	takeoverResp := request(app, http.MethodPut, path, body, token)
	if takeoverResp.Code != http.StatusOK {
		t.Fatalf("takeover status = %d, body = %s", takeoverResp.Code, takeoverResp.Body.String())
	}
	takeover := decodeWorkflowStepResponse(t, takeoverResp)
	if takeover.AttemptCount != 2 || takeover.LeaseOwner == nil || *takeover.LeaseOwner != "client-b" {
		t.Fatalf("takeover attempt = %#v", takeover)
	}
}

func TestWorkflowStepAllMutationsRequireAndEnforceClientInstance(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-step-owner@example.com")
	run := createWorkflowRunTestPC(t, app, token, "step-owner-task", "step-owner-run-hash", "client-a")
	path := "/api/v1/workflow-runs/" + run.ID + "/steps/render-video"
	if resp := request(app, http.MethodPut, path, map[string]any{
		"input_hash": "step-owner-input", "status": "running", "client_instance_id": "client-a",
	}, token); resp.Code != http.StatusOK {
		t.Fatalf("acquire step status = %d, body = %s", resp.Code, resp.Body.String())
	}

	for _, status := range []string{"succeeded", "interrupted", "failed", "blocked"} {
		t.Run(status, func(t *testing.T) {
			missing := request(app, http.MethodPut, path, map[string]any{
				"input_hash": "step-owner-input", "status": status,
			}, token)
			if missing.Code != http.StatusBadRequest {
				t.Fatalf("missing client status = %d, want 400, body = %s", missing.Code, missing.Body.String())
			}

			stale := request(app, http.MethodPut, path, map[string]any{
				"input_hash": "step-owner-input", "status": status, "client_instance_id": "client-b",
			}, token)
			if stale.Code != http.StatusConflict {
				t.Fatalf("stale client status = %d, want 409, body = %s", stale.Code, stale.Body.String())
			}
		})
	}

	var persisted struct {
		Status     string
		LeaseOwner *string
	}
	if err := app.db.Table("workflow_step_executions").Select("status", "lease_owner").Where("workflow_run_id = ?", run.ID).Take(&persisted).Error; err != nil {
		t.Fatalf("load persisted step: %v", err)
	}
	if persisted.Status != "running" || persisted.LeaseOwner == nil || *persisted.LeaseOwner != "client-a" {
		t.Fatalf("rejected mutations changed active lease: %#v", persisted)
	}
}

func TestWorkflowStepRejectsBusinessContentMetadataAndAcceptsNumericUsage(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-step-metadata@example.com")
	run := createWorkflowRunTestPC(t, app, token, "step-metadata-task", "step-metadata-run-hash", "client-a")
	path := "/api/v1/workflow-runs/" + run.ID + "/steps/provider-call"

	invalidBodies := []map[string]any{
		{"input_hash": "metadata-input", "status": "running", "client_instance_id": "client-a", "usage": map[string]any{"prompt": "write a script about private content"}},
		{"input_hash": "metadata-input", "status": "running", "client_instance_id": "client-a", "usage": map[string]any{"prompt_tokens": "ten"}},
		{"input_hash": "metadata-input", "status": "running", "client_instance_id": "client-a", "provider_request_id": "C:\\private\\video.mp4"},
		{"input_hash": "metadata-input", "status": "running", "client_instance_id": "client-a", "provider_request_id": strings.Repeat("a", workflowProviderRequestIDMaxLength+1)},
		{"input_hash": "metadata-input", "status": "running", "client_instance_id": "client-a", "error_code": "<script>alert(1)</script>"},
		{"input_hash": "metadata-input", "status": "running", "client_instance_id": "client-a", "error_code": strings.Repeat("e", workflowErrorCodeMaxLength+1)},
		{"input_hash": "metadata-input", "status": "running", "client_instance_id": "client-a", "error_message": "subtitle: secret dialogue from the source video"},
		{"input_hash": "metadata-input", "status": "running", "client_instance_id": "client-a", "error_message": strings.Repeat("x", workflowErrorMessageMaxLength+1)},
		{"input_hash": "metadata-input", "status": "running", "client_instance_id": "client-a", "usage": map[string]any{"total_tokens": -1}},
		{"input_hash": "metadata-input", "status": "running", "client_instance_id": "client-a", "usage": map[string]any{"duration_seconds": workflowUsageMaxValue + 1}},
	}
	for i, body := range invalidBodies {
		resp := request(app, http.MethodPut, path, body, token)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("invalid metadata case %d status = %d, want 400, body = %s", i, resp.Code, resp.Body.String())
		}
	}

	valid := request(app, http.MethodPut, path, map[string]any{
		"input_hash":          "metadata-input",
		"status":              "running",
		"client_instance_id":  "client-a",
		"provider_request_id": "req_01HZY-ABC.123",
		"usage": map[string]any{
			"prompt_tokens":     12,
			"completion_tokens": 8,
			"total_tokens":      20,
			"character_count":   42,
			"duration_seconds":  1.25,
		},
	}, token)
	if valid.Code != http.StatusOK {
		t.Fatalf("valid metadata status = %d, body = %s", valid.Code, valid.Body.String())
	}
	step := decodeWorkflowStepResponse(t, valid)
	var usage map[string]any
	if err := json.Unmarshal(step.Usage, &usage); err != nil {
		t.Fatalf("decode usage: %v", err)
	}
	if len(usage) != 5 || usage["total_tokens"] != float64(20) || usage["duration_seconds"] != 1.25 {
		t.Fatalf("persisted usage = %#v", usage)
	}
}

func TestWorkflowStepConcurrentFirstAcquisitionReturnsLeaseConflict(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-step-concurrent@example.com")
	run := createWorkflowRunTestPC(t, app, token, "step-concurrent-task", "step-concurrent-run-hash", "run-client")
	path := "/api/v1/workflow-runs/" + run.ID + "/steps/concurrent-step"
	if err := app.db.Exec("UPDATE workflow_runs SET lease_expires_at = ? WHERE id = ?", time.Now().UTC().Add(-time.Second), run.ID).Error; err != nil {
		t.Fatalf("expire run lease: %v", err)
	}
	installWorkflowCreateBarrier(t, app, "workflow_step_executions", 2)

	responses := concurrentWorkflowRequests(app, token,
		workflowTestRequest{method: http.MethodPut, path: path, body: map[string]any{
			"input_hash": "concurrent-step-input", "status": "running", "client_instance_id": "client-a",
		}},
		workflowTestRequest{method: http.MethodPut, path: path, body: map[string]any{
			"input_hash": "concurrent-step-input", "status": "running", "client_instance_id": "client-b",
		}},
	)
	assertWorkflowResponseStatuses(t, responses, http.StatusOK, http.StatusConflict)

	var count int64
	if err := app.db.Table("workflow_step_executions").Where("workflow_run_id = ?", run.ID).Count(&count).Error; err != nil {
		t.Fatalf("count workflow steps: %v", err)
	}
	if count != 1 {
		t.Fatalf("workflow step count = %d, want 1", count)
	}
}

type workflowTestRequest struct {
	method string
	path   string
	body   any
}

func concurrentWorkflowRequests(app *App, token string, requests ...workflowTestRequest) []*httptest.ResponseRecorder {
	start := make(chan struct{})
	responses := make([]*httptest.ResponseRecorder, len(requests))
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(len(requests))
	done.Add(len(requests))
	for i := range requests {
		go func(index int) {
			defer done.Done()
			ready.Done()
			<-start
			responses[index] = request(app, requests[index].method, requests[index].path, requests[index].body, token)
		}(i)
	}
	ready.Wait()
	close(start)
	done.Wait()
	return responses
}

func installWorkflowCreateBarrier(t *testing.T, app *App, table string, participants int) {
	t.Helper()
	ready := make(chan struct{}, participants)
	release := make(chan struct{})
	callbackName := "workflow_test_create_barrier_" + table
	remaining := participants
	var mu sync.Mutex
	if err := app.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != table {
			return
		}
		mu.Lock()
		participate := remaining > 0
		if participate {
			remaining--
		}
		mu.Unlock()
		if !participate {
			return
		}
		ready <- struct{}{}
		<-release
	}); err != nil {
		t.Fatalf("register create barrier: %v", err)
	}
	t.Cleanup(func() {
		_ = app.db.Callback().Create().Remove(callbackName)
	})
	go func() {
		for i := 0; i < participants; i++ {
			<-ready
		}
		close(release)
	}()
}

func assertWorkflowResponseStatuses(t *testing.T, responses []*httptest.ResponseRecorder, want ...int) {
	t.Helper()
	counts := make(map[int]int, len(responses))
	for _, resp := range responses {
		counts[resp.Code]++
	}
	for _, status := range want {
		if counts[status] == 0 {
			bodies := make([]string, 0, len(responses))
			for _, resp := range responses {
				bodies = append(bodies, resp.Body.String())
			}
			t.Fatalf("response status counts = %#v, missing %d, bodies = %#v", counts, status, bodies)
		}
		counts[status]--
	}
	for status, count := range counts {
		if count != 0 {
			t.Fatalf("unexpected response status %d count %d", status, count)
		}
	}
}

func createWorkflowRunTestProject(t *testing.T, app *App, token, name string) string {
	t.Helper()
	resp := request(app, http.MethodPost, "/api/v1/projects/", map[string]any{"name": name}, token)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create project status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	if body.ID == "" {
		t.Fatalf("created project missing id: %s", resp.Body.String())
	}
	return body.ID
}

func createWorkflowRunTestPC(t *testing.T, app *App, token, taskID, inputHash, clientID string) workflowRunTestResponse {
	t.Helper()
	resp := request(app, http.MethodPost, "/api/v1/workflow-runs/", map[string]any{
		"client_type":         "pc",
		"client_task_id":      taskID,
		"client_project_name": taskID + ".mp4",
		"input_hash":          inputHash,
		"client_instance_id":  clientID,
	}, token)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create pc run status = %d, body = %s", resp.Code, resp.Body.String())
	}
	return decodeWorkflowRunResponse(t, resp)
}

func decodeWorkflowRunResponse(t *testing.T, resp *httptest.ResponseRecorder) workflowRunTestResponse {
	t.Helper()
	var body workflowRunTestResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode workflow run: %v", err)
	}
	return body
}

func decodeWorkflowStepResponse(t *testing.T, resp *httptest.ResponseRecorder) workflowStepTestResponse {
	t.Helper()
	var body workflowStepTestResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode workflow step: %v", err)
	}
	return body
}

func assertWorkflowRunLease(t *testing.T, run workflowRunTestResponse, owner string, now time.Time) {
	t.Helper()
	if run.ClientInstanceID != owner {
		t.Fatalf("client_instance_id = %q, want %q", run.ClientInstanceID, owner)
	}
	if run.LeaseExpiresAt == nil || run.LeaseExpiresAt.Before(now.Add(89*time.Second)) || run.LeaseExpiresAt.After(now.Add(91*time.Second)) {
		t.Fatalf("lease_expires_at = %v, want about 90 seconds after %v", run.LeaseExpiresAt, now)
	}
	if run.ExpiresAt.Before(now.Add(23*time.Hour+59*time.Minute)) || run.ExpiresAt.After(now.Add(24*time.Hour+time.Minute)) {
		t.Fatalf("expires_at = %v, want about 24 hours after %v", run.ExpiresAt, now)
	}
}
