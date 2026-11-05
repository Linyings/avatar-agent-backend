package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
)

func TestWorkflowRunLazyExpiryBeforeMutatingEndpoints(t *testing.T) {
	t.Run("create expires the stale scope before inserting", func(t *testing.T) {
		app := newTestApp(t)
		token := registerAndLogin(t, app, "workflow-lazy-create@example.com")
		oldRun := createWorkflowRunTestPC(t, app, token, "lazy-create-task", strings.Repeat("a", 64), "client-a")
		markWorkflowRunStale(t, app, oldRun.ID)

		created := request(app, http.MethodPost, "/api/v1/workflow-runs/", map[string]any{
			"client_type": "pc", "client_task_id": "lazy-create-task", "client_project_name": "lazy-create.mp4", "input_hash": strings.Repeat("b", 64), "client_instance_id": "client-b",
		}, token)
		if created.Code != http.StatusCreated {
			t.Fatalf("create after stale scope status = %d, body = %s", created.Code, created.Body.String())
		}
		if replacement := decodeWorkflowRunResponse(t, created); replacement.ID == oldRun.ID {
			t.Fatalf("stale run was reused: %s", replacement.ID)
		}
		assertPersistedWorkflowRunStatus(t, app, oldRun.ID, workflowRunStatusExpired)
	})

	for _, operation := range []string{"resume", "complete", "cancel"} {
		t.Run(operation, func(t *testing.T) {
			app := newTestApp(t)
			token := registerAndLogin(t, app, "workflow-lazy-"+operation+"@example.com")
			run := createWorkflowRunTestPC(t, app, token, "lazy-"+operation+"-task", strings.Repeat(operation[:1], 64), "client-a")
			markWorkflowRunStale(t, app, run.ID)

			var body any
			if operation == "resume" {
				body = map[string]any{"client_instance_id": "client-b"}
			} else if operation == "complete" {
				body = map[string]any{"final_result_at": time.Now().UTC().Format(time.RFC3339Nano)}
			}
			resp := request(app, http.MethodPost, "/api/v1/workflow-runs/"+run.ID+"/"+operation, body, token)
			if resp.Code != http.StatusConflict {
				t.Fatalf("lazy %s status = %d, want 409, body = %s", operation, resp.Code, resp.Body.String())
			}
			assertPersistedWorkflowRunStatus(t, app, run.ID, workflowRunStatusExpired)
		})
	}
}

func TestWorkflowRunExpirationWorkerExecutes(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-worker-expiry@example.com")
	run := createWorkflowRunTestPC(t, app, token, "worker-expiry-task", strings.Repeat("c", 64), "client-a")
	markWorkflowRunStale(t, app, run.ID)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runWorkflowRunExpirationWorker(ctx, app.workflowRuns, 5*time.Millisecond)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		var persisted models.WorkflowRun
		if err := app.db.Where("id = ?", run.ID).Take(&persisted).Error; err != nil {
			t.Fatalf("load worker-expired run: %v", err)
		}
		if persisted.Status == workflowRunStatusExpired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expiration worker did not expire run before deadline")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("expiration worker did not stop after cancellation")
	}
}

func TestWorkflowRunConcurrentExpiryInvokesHookOnce(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-concurrent-expiry@example.com")
	run := createWorkflowRunTestPC(t, app, token, "concurrent-expiry-task", strings.Repeat("d", 64), "client-a")
	markWorkflowRunStale(t, app, run.ID)
	createWorkflowHookMarkerTable(t, app)
	app.workflowRuns.SetHooks(WorkflowRunStateHooks{
		AfterExpire: workflowHookMarker("expire"),
	})

	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(workers)
	done.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			errs <- app.workflowRuns.ExpireStale(context.Background())
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent expiry error: %v", err)
		}
	}
	assertPersistedWorkflowRunStatus(t, app, run.ID, workflowRunStatusExpired)
	if count := workflowHookMarkerCount(t, app, run.ID, "expire"); count != 1 {
		t.Fatalf("expire hook marker count = %d, want 1", count)
	}
}

func TestWorkflowRunAfterExpireRollbackIsAtomic(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-expiry-rollback@example.com")
	run := createWorkflowRunTestPC(t, app, token, "expiry-rollback-task", strings.Repeat("e", 64), "client-a")
	markWorkflowRunStale(t, app, run.ID)
	createWorkflowHookMarkerTable(t, app)
	hookErr := errors.New("expire hook failed")
	app.workflowRuns.SetHooks(WorkflowRunStateHooks{
		AfterExpire: func(ctx context.Context, tx *gorm.DB, run *models.WorkflowRun) error {
			if err := workflowHookMarker("expire")(ctx, tx, run); err != nil {
				return err
			}
			return hookErr
		},
	})

	if err := app.workflowRuns.ExpireStale(context.Background()); !errors.Is(err, hookErr) {
		t.Fatalf("ExpireStale error = %v, want %v", err, hookErr)
	}
	assertPersistedWorkflowRunStatus(t, app, run.ID, workflowRunStatusRunning)
	if count := workflowHookMarkerCount(t, app, run.ID, "expire"); count != 0 {
		t.Fatalf("rolled-back expire hook markers = %d, want 0", count)
	}

	app.workflowRuns.SetHooks(WorkflowRunStateHooks{AfterExpire: workflowHookMarker("expire")})
	if err := app.workflowRuns.ExpireStale(context.Background()); err != nil {
		t.Fatalf("ExpireStale after rollback: %v", err)
	}
	if err := app.workflowRuns.ExpireStale(context.Background()); err != nil {
		t.Fatalf("idempotent ExpireStale: %v", err)
	}
	assertPersistedWorkflowRunStatus(t, app, run.ID, workflowRunStatusExpired)
	if count := workflowHookMarkerCount(t, app, run.ID, "expire"); count != 1 {
		t.Fatalf("committed expire hook markers = %d, want 1", count)
	}
}

func TestWorkflowRunTerminalHooksAreAtomicAndIdempotent(t *testing.T) {
	for _, operation := range []string{"complete", "cancel"} {
		t.Run(operation, func(t *testing.T) {
			app := newTestApp(t)
			token := registerAndLogin(t, app, "workflow-hook-"+operation+"@example.com")
			run := createWorkflowRunTestPC(t, app, token, "hook-"+operation+"-task", strings.Repeat(operation[:1], 64), "client-a")
			createWorkflowHookMarkerTable(t, app)
			hookErr := errors.New(operation + " hook failed")
			failingHook := func(ctx context.Context, tx *gorm.DB, run *models.WorkflowRun) error {
				if err := workflowHookMarker(operation)(ctx, tx, run); err != nil {
					return err
				}
				return hookErr
			}
			hooks := WorkflowRunStateHooks{}
			if operation == "complete" {
				hooks.AfterComplete = failingHook
			} else {
				hooks.AfterCancel = failingHook
			}
			app.workflowRuns.SetHooks(hooks)

			userID := uuid.MustParse(run.UserID)
			runID := uuid.MustParse(run.ID)
			var err error
			if operation == "complete" {
				_, err = app.workflowRuns.Complete(context.Background(), userID, runID, time.Now().UTC())
			} else {
				_, err = app.workflowRuns.Cancel(context.Background(), userID, runID)
			}
			if !errors.Is(err, hookErr) {
				t.Fatalf("%s hook error = %v, want %v", operation, err, hookErr)
			}
			assertPersistedWorkflowRunStatus(t, app, run.ID, workflowRunStatusRunning)
			if count := workflowHookMarkerCount(t, app, run.ID, operation); count != 0 {
				t.Fatalf("rolled-back %s markers = %d, want 0", operation, count)
			}

			successHooks := WorkflowRunStateHooks{}
			if operation == "complete" {
				successHooks.AfterComplete = workflowHookMarker(operation)
			} else {
				successHooks.AfterCancel = workflowHookMarker(operation)
			}
			app.workflowRuns.SetHooks(successHooks)
			if operation == "complete" {
				_, err = app.workflowRuns.Complete(context.Background(), userID, runID, time.Now().UTC())
				if err == nil {
					_, err = app.workflowRuns.Complete(context.Background(), userID, runID, time.Now().UTC())
				}
			} else {
				_, err = app.workflowRuns.Cancel(context.Background(), userID, runID)
				if err == nil {
					_, err = app.workflowRuns.Cancel(context.Background(), userID, runID)
				}
			}
			if err != nil {
				t.Fatalf("idempotent %s: %v", operation, err)
			}
			wantStatus := workflowRunStatusCancelled
			if operation == "complete" {
				wantStatus = workflowRunStatusSucceeded
			}
			assertPersistedWorkflowRunStatus(t, app, run.ID, wantStatus)
			if count := workflowHookMarkerCount(t, app, run.ID, operation); count != 1 {
				t.Fatalf("committed %s markers = %d, want 1", operation, count)
			}
		})
	}
}

func markWorkflowRunStale(t *testing.T, app *App, runID string) {
	t.Helper()
	stale := time.Now().UTC().Add(-time.Minute)
	if err := app.db.Model(&models.WorkflowRun{}).Where("id = ?", runID).
		Updates(map[string]any{"expires_at": stale, "last_active_at": stale.Add(-24 * time.Hour), "lease_expires_at": stale}).Error; err != nil {
		t.Fatalf("mark workflow run stale: %v", err)
	}
}

func assertPersistedWorkflowRunStatus(t *testing.T, app *App, runID, want string) {
	t.Helper()
	var run models.WorkflowRun
	if err := app.db.Where("id = ?", runID).Take(&run).Error; err != nil {
		t.Fatalf("load workflow run %s: %v", runID, err)
	}
	if run.Status != want {
		t.Fatalf("workflow run %s status = %q, want %q", runID, run.Status, want)
	}
}

func createWorkflowHookMarkerTable(t *testing.T, app *App) {
	t.Helper()
	if err := app.db.Exec("CREATE TABLE workflow_hook_markers (run_id TEXT NOT NULL, hook TEXT NOT NULL, PRIMARY KEY (run_id, hook))").Error; err != nil {
		t.Fatalf("create workflow hook marker table: %v", err)
	}
}

func workflowHookMarker(hook string) func(context.Context, *gorm.DB, *models.WorkflowRun) error {
	return func(_ context.Context, tx *gorm.DB, run *models.WorkflowRun) error {
		return tx.Exec("INSERT INTO workflow_hook_markers (run_id, hook) VALUES (?, ?)", run.ID.String(), hook).Error
	}
}

func workflowHookMarkerCount(t *testing.T, app *App, runID, hook string) int64 {
	t.Helper()
	var count int64
	if err := app.db.Table("workflow_hook_markers").Where("run_id = ? AND hook = ?", runID, hook).Count(&count).Error; err != nil {
		t.Fatalf("count workflow hook markers: %v", err)
	}
	return count
}
