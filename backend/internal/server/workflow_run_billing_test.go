package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/database"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
	"gorm.io/gorm"
)

func TestWorkflowRunBillingPCReservesWithoutProjectAndIsIdempotent(t *testing.T) {
	app, user, run, step := newWorkflowRunBillingFixture(t, "pc-reserve@example.com", nil)
	consumption := workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10)

	if err := app.billCredits(consumption); err != nil {
		t.Fatalf("first bill credits: %v", err)
	}
	if err := app.billCredits(consumption); err != nil {
		t.Fatalf("duplicate bill credits: %v", err)
	}

	var reservations []models.CreditReservation
	if err := app.db.Where("workflow_run_id = ?", run.ID).Find(&reservations).Error; err != nil {
		t.Fatalf("load reservations: %v", err)
	}
	if len(reservations) != 1 {
		t.Fatalf("reservation count = %d, want 1", len(reservations))
	}
	reservation := reservations[0]
	if reservation.ProjectID != nil || reservation.WorkflowRunID == nil || *reservation.WorkflowRunID != run.ID {
		t.Fatalf("reservation workflow identity = %#v", reservation)
	}
	if reservation.WorkflowStepExecutionID == nil || *reservation.WorkflowStepExecutionID != step.ID {
		t.Fatalf("reservation step execution id = %v, want %s", reservation.WorkflowStepExecutionID, step.ID)
	}
	if reservation.Status != creditReservationPending {
		t.Fatalf("reservation status = %q, want pending", reservation.Status)
	}
	assertWorkflowBillingCounts(t, app, user.ID, 0, 0, 0)

	conflict := consumption
	conflict.Usage.TotalTokens = 11
	if err := app.billCredits(conflict); !errors.Is(err, ErrCreditReservationConflict) {
		t.Fatalf("conflicting duplicate error = %v, want ErrCreditReservationConflict", err)
	}
}

func TestWorkflowRunBillingConcurrentDuplicateCreatesOneReservation(t *testing.T) {
	app, user, run, step := newWorkflowRunBillingFixture(t, "pc-concurrent@example.com", nil)
	consumption := workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10)

	const requests = 8
	start := make(chan struct{})
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- app.billCredits(consumption)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent bill credits: %v", err)
		}
	}

	var count int64
	if err := app.db.Model(&models.CreditReservation{}).Where("workflow_run_id = ?", run.ID).Count(&count).Error; err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if count != 1 {
		t.Fatalf("reservation count = %d, want 1", count)
	}
}

func TestWorkflowRunBillingRejectsNewReservationAfterTerminalState(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		finalize func(*App, models.User, models.WorkflowRun) error
	}{
		{
			name: "complete",
			finalize: func(app *App, user models.User, run models.WorkflowRun) error {
				_, err := app.WorkflowRuns().Complete(context.Background(), user.ID, run.ID, time.Now().UTC())
				return err
			},
		},
		{
			name: "cancel",
			finalize: func(app *App, user models.User, run models.WorkflowRun) error {
				_, err := app.WorkflowRuns().Cancel(context.Background(), user.ID, run.ID)
				return err
			},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			app, user, run, step := newWorkflowRunBillingFixture(t, "pc-terminal-"+scenario.name+"@example.com", nil)
			if err := scenario.finalize(app, user, run); err != nil {
				t.Fatalf("finalize run: %v", err)
			}

			err := app.billCredits(workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10))
			if !errors.Is(err, ErrWorkflowRunTerminal) {
				t.Fatalf("bill terminal run error = %v, want ErrWorkflowRunTerminal", err)
			}
			assertWorkflowReservationStatus(t, app, run.ID, creditReservationPending, 0)
			assertWorkflowBillingCounts(t, app, user.ID, 0, 0, 0)
		})
	}
}

func TestWorkflowRunBillingActivityExtendsReservationExpiry(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		activity func(*App, models.User, models.WorkflowRun) error
	}{
		{
			name: "heartbeat",
			activity: func(app *App, user models.User, run models.WorkflowRun) error {
				_, err := app.WorkflowRuns().Heartbeat(context.Background(), user.ID, run.ID, WorkflowRunHeartbeatInput{
					ClientInstanceID: "billing-client",
				})
				return err
			},
		},
		{
			name: "resume",
			activity: func(app *App, user models.User, run models.WorkflowRun) error {
				if _, err := app.WorkflowRuns().Interrupt(context.Background(), user.ID, run.ID, WorkflowRunInterruptInput{
					ClientInstanceID: "billing-client",
				}); err != nil {
					return err
				}
				_, err := app.WorkflowRuns().Resume(context.Background(), user.ID, run.ID, "billing-resumed-client")
				return err
			},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			app, user, run, step := newWorkflowRunBillingFixture(t, "pc-activity-"+scenario.name+"@example.com", nil)
			if err := app.billCredits(workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10)); err != nil {
				t.Fatalf("reserve credits: %v", err)
			}
			staleExpiry := time.Now().UTC().Add(-time.Hour)
			if err := app.db.Model(&models.CreditReservation{}).
				Where("workflow_run_id = ?", run.ID).
				Update("expires_at", staleExpiry).Error; err != nil {
				t.Fatalf("make reservation stale: %v", err)
			}

			if err := scenario.activity(app, user, run); err != nil {
				t.Fatalf("record activity: %v", err)
			}
			var reservation models.CreditReservation
			if err := app.db.Where("workflow_run_id = ?", run.ID).Take(&reservation).Error; err != nil {
				t.Fatalf("load reservation: %v", err)
			}
			if !reservation.ExpiresAt.After(time.Now().UTC().Add(23 * time.Hour)) {
				t.Fatalf("reservation expiry = %v, want activity + 24h", reservation.ExpiresAt)
			}

			if _, err := app.WorkflowRuns().Complete(context.Background(), user.ID, run.ID, time.Now().UTC()); err != nil {
				t.Fatalf("complete after original expiry: %v", err)
			}
			assertWorkflowReservationStatus(t, app, run.ID, creditReservationConfirmed, 1)
			assertWorkflowBillingCounts(t, app, user.ID, 10, 1, 1)
		})
	}
}

func TestWorkflowRunBillingPostgresSerializesReservationAndFinalization(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("TEST_POSTGRES_URL"))
	if databaseURL == "" {
		t.Skip("TEST_POSTGRES_URL is not configured")
	}
	if !strings.Contains(strings.ToLower(databaseURL), "workflow") {
		t.Fatalf("TEST_POSTGRES_URL must point to a dedicated workflow test database")
	}

	app := newWorkflowRunBillingPostgresApp(t, databaseURL)
	seedWorkflowRunBillingRate(t, app)

	for _, scenario := range []struct {
		name               string
		finalReservation   string
		finalize           func(*App, models.User, models.WorkflowRun) error
		expectedUsedCredit int64
	}{
		{
			name:             "complete",
			finalReservation: creditReservationConfirmed,
			finalize: func(app *App, user models.User, run models.WorkflowRun) error {
				_, err := app.WorkflowRuns().Complete(context.Background(), user.ID, run.ID, time.Now().UTC())
				return err
			},
			expectedUsedCredit: 10,
		},
		{
			name:             "cancel",
			finalReservation: creditReservationReleased,
			finalize: func(app *App, user models.User, run models.WorkflowRun) error {
				_, err := app.WorkflowRuns().Cancel(context.Background(), user.ID, run.ID)
				return err
			},
			expectedUsedCredit: 0,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			email := "postgres-race-" + scenario.name + "-" + uuid.NewString() + "@example.com"
			registerAndLoginWithCredits(t, app, email, 100)
			user := lookupUserByEmail(t, app, email)
			run := createWorkflowBillingRun(t, app, user.ID, nil)
			step := createWorkflowBillingStep(t, app, run.ID, "generate-script", "script-input")
			consumption := workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10)

			assertWorkflowFinalizationWaitsForReservation(t, app, consumption, func() error {
				return scenario.finalize(app, user, run)
			})

			assertWorkflowReservationStatus(t, app, run.ID, scenario.finalReservation, 1)
			expectedUsage := int64(0)
			expectedLedger := int64(0)
			if scenario.expectedUsedCredit > 0 {
				expectedUsage = 1
				expectedLedger = 1
			}
			assertWorkflowBillingCounts(t, app, user.ID, scenario.expectedUsedCredit, expectedUsage, expectedLedger)
		})
	}
}

func TestWorkflowRunBillingDifferentStepsReserveIndependently(t *testing.T) {
	app, user, run, stepA := newWorkflowRunBillingFixture(t, "pc-steps@example.com", nil)
	stepB := createWorkflowBillingStep(t, app, run.ID, "render-video", "render-input")

	if err := app.billCredits(workflowRunTestConsumption(user.ID, run.ID, stepA.ID, stepA.ExecutionKey, 10)); err != nil {
		t.Fatalf("bill first step: %v", err)
	}
	if err := app.billCredits(workflowRunTestConsumption(user.ID, run.ID, stepB.ID, stepB.ExecutionKey, 15)); err != nil {
		t.Fatalf("bill second step: %v", err)
	}

	var count int64
	if err := app.db.Model(&models.CreditReservation{}).Where("workflow_run_id = ?", run.ID).Count(&count).Error; err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if count != 2 {
		t.Fatalf("reservation count = %d, want 2", count)
	}
}

func TestWorkflowRunBillingCompleteConfirmsExactlyOnce(t *testing.T) {
	app, user, run, stepA := newWorkflowRunBillingFixture(t, "pc-complete-billing@example.com", nil)
	stepB := createWorkflowBillingStep(t, app, run.ID, "render-video", "render-input")
	for _, consumption := range []creditConsumption{
		workflowRunTestConsumption(user.ID, run.ID, stepA.ID, stepA.ExecutionKey, 10),
		workflowRunTestConsumption(user.ID, run.ID, stepB.ID, stepB.ExecutionKey, 15),
	} {
		if err := app.billCredits(consumption); err != nil {
			t.Fatalf("reserve credits: %v", err)
		}
	}

	finalResultAt := time.Now().UTC().Add(-time.Second)
	completed, err := app.WorkflowRuns().Complete(context.Background(), user.ID, run.ID, finalResultAt)
	if err != nil {
		t.Fatalf("complete workflow run: %v", err)
	}
	if completed.Status != workflowRunStatusSucceeded || completed.BillingStatus != workflowRunBillingStatusConfirmed {
		t.Fatalf("completed run = %#v", completed)
	}
	assertWorkflowBillingCounts(t, app, user.ID, 25, 2, 1)
	assertWorkflowReservationStatus(t, app, run.ID, creditReservationConfirmed, 2)
	var ledger models.CreditLedgerRecord
	if err := app.db.Where("user_id = ? AND event_type = ?", user.ID, creditLedgerEventConsume).Take(&ledger).Error; err != nil {
		t.Fatalf("load workflow ledger: %v", err)
	}
	if ledger.ProjectID != nil || ledger.ProjectName != "source.mp4" {
		t.Fatalf("PC ledger project snapshot = %v/%q", ledger.ProjectID, ledger.ProjectName)
	}

	completedAgain, err := app.WorkflowRuns().Complete(context.Background(), user.ID, run.ID, finalResultAt)
	if err != nil {
		t.Fatalf("repeat complete workflow run: %v", err)
	}
	if completedAgain.BillingStatus != workflowRunBillingStatusConfirmed {
		t.Fatalf("repeat complete billing status = %q", completedAgain.BillingStatus)
	}
	assertWorkflowBillingCounts(t, app, user.ID, 25, 2, 1)
}

func TestWorkflowRunBillingCompleteRollsBackRunWhenConfirmationFails(t *testing.T) {
	app, user, run, step := newWorkflowRunBillingFixture(t, "pc-complete-rollback@example.com", nil)
	if err := app.billCredits(workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10)); err != nil {
		t.Fatalf("reserve credits: %v", err)
	}
	if err := app.db.Model(&models.UserTokenAccount{}).Where("user_id = ?", user.ID).Update("used_tokens", 100).Error; err != nil {
		t.Fatalf("drain credits: %v", err)
	}

	if _, err := app.WorkflowRuns().Complete(context.Background(), user.ID, run.ID, time.Now().UTC()); err == nil {
		t.Fatal("complete workflow run succeeded with insufficient credits")
	}
	var stored models.WorkflowRun
	if err := app.db.First(&stored, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("load run: %v", err)
	}
	if stored.Status != workflowRunStatusRunning || stored.FinalResultAt != nil || stored.BillingStatus != workflowRunBillingStatusPending {
		t.Fatalf("run was not rolled back: %#v", stored)
	}
	assertWorkflowReservationStatus(t, app, run.ID, creditReservationPending, 1)
}

func TestWorkflowRunBillingCancelReleasesAndInterruptKeepsPending(t *testing.T) {
	t.Run("cancel releases with zero charge", func(t *testing.T) {
		app, user, run, step := newWorkflowRunBillingFixture(t, "pc-cancel-billing@example.com", nil)
		if err := app.billCredits(workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10)); err != nil {
			t.Fatalf("reserve credits: %v", err)
		}

		cancelled, err := app.WorkflowRuns().Cancel(context.Background(), user.ID, run.ID)
		if err != nil {
			t.Fatalf("cancel workflow run: %v", err)
		}
		if cancelled.Status != workflowRunStatusCancelled || cancelled.BillingStatus != workflowRunBillingStatusReleased {
			t.Fatalf("cancelled run = %#v", cancelled)
		}
		assertWorkflowReservationStatus(t, app, run.ID, creditReservationReleased, 1)
		assertWorkflowBillingCounts(t, app, user.ID, 0, 0, 0)

		if _, err := app.WorkflowRuns().Cancel(context.Background(), user.ID, run.ID); err != nil {
			t.Fatalf("repeat cancel: %v", err)
		}
		assertWorkflowBillingCounts(t, app, user.ID, 0, 0, 0)
	})

	t.Run("interrupt keeps reservation pending", func(t *testing.T) {
		app, user, run, step := newWorkflowRunBillingFixture(t, "pc-interrupt-billing@example.com", nil)
		if err := app.billCredits(workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10)); err != nil {
			t.Fatalf("reserve credits: %v", err)
		}
		if _, err := app.WorkflowRuns().Interrupt(context.Background(), user.ID, run.ID, WorkflowRunInterruptInput{ClientInstanceID: "billing-client"}); err != nil {
			t.Fatalf("interrupt workflow run: %v", err)
		}
		assertWorkflowReservationStatus(t, app, run.ID, creditReservationPending, 1)
		assertWorkflowBillingCounts(t, app, user.ID, 0, 0, 0)
	})
}

func TestWorkflowRunBillingLegacyProjectOperationsIgnoreRunReservations(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLoginWithCredits(t, app, "legacy-scope@example.com", 100)
	user := lookupUserByEmail(t, app, "legacy-scope@example.com")
	project := createTestProject(t, app, token, "legacy scope")
	seedWorkflowRunBillingRate(t, app)
	run := createWorkflowBillingRun(t, app, user.ID, &project.ID)
	step := createWorkflowBillingStep(t, app, run.ID, "generate-script", "script-input")
	workflowConsumption := workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10)
	workflowConsumption.ProjectID = &project.ID
	if err := app.billCredits(workflowConsumption); err != nil {
		t.Fatalf("reserve workflow credits: %v", err)
	}
	legacy := workflowRunTestConsumption(user.ID, uuid.Nil, uuid.Nil, "", 15)
	legacy.ProjectID = &project.ID
	legacy.WorkflowRunID = nil
	legacy.WorkflowStepExecutionID = nil
	if err := app.billCredits(legacy); err != nil {
		t.Fatalf("reserve legacy credits: %v", err)
	}

	if err := app.confirmProjectReservations(project.ID); err != nil {
		t.Fatalf("confirm project reservations: %v", err)
	}
	assertWorkflowReservationStatus(t, app, run.ID, creditReservationPending, 1)
	assertWorkflowBillingCounts(t, app, user.ID, 15, 1, 1)

	if err := app.releaseProjectReservations(project.ID, "legacy release"); err != nil {
		t.Fatalf("release project reservations: %v", err)
	}
	assertWorkflowReservationStatus(t, app, run.ID, creditReservationPending, 1)
}

func TestWorkflowRunBillingOldClientWithoutWorkflowIdentityConsumesImmediately(t *testing.T) {
	app := newTestApp(t)
	registerAndLoginWithCredits(t, app, "old-client-billing@example.com", 100)
	user := lookupUserByEmail(t, app, "old-client-billing@example.com")
	seedWorkflowRunBillingRate(t, app)
	consumption := workflowRunTestConsumption(user.ID, uuid.Nil, uuid.Nil, "", 10)
	consumption.WorkflowRunID = nil
	consumption.WorkflowStepExecutionID = nil

	if err := app.billCredits(consumption); err != nil {
		t.Fatalf("bill old client: %v", err)
	}
	assertWorkflowBillingCounts(t, app, user.ID, 10, 1, 1)
	var reservations int64
	if err := app.db.Model(&models.CreditReservation{}).Where("user_id = ?", user.ID).Count(&reservations).Error; err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if reservations != 0 {
		t.Fatalf("old client reservations = %d, want 0", reservations)
	}
}

func newWorkflowRunBillingFixture(t *testing.T, email string, projectID *uuid.UUID) (*App, models.User, models.WorkflowRun, models.WorkflowStepExecution) {
	t.Helper()
	app := newTestApp(t)
	registerAndLoginWithCredits(t, app, email, 100)
	user := lookupUserByEmail(t, app, email)
	seedWorkflowRunBillingRate(t, app)
	run := createWorkflowBillingRun(t, app, user.ID, projectID)
	step := createWorkflowBillingStep(t, app, run.ID, "generate-script", "script-input")
	return app, user, run, step
}

func seedWorkflowRunBillingRate(t *testing.T, app *App) {
	t.Helper()
	seedBillingRates(t, app, map[string]any{
		"llm": []map[string]any{{
			"provider":          "openai",
			"model_id":          "workflow-model",
			"tokens_per_credit": 1,
		}},
	})
}

func createWorkflowBillingRun(t *testing.T, app *App, userID uuid.UUID, projectID *uuid.UUID) models.WorkflowRun {
	t.Helper()
	clientType := "pc"
	var taskID *string
	clientProjectName := ""
	if projectID != nil {
		clientType = "web"
	} else {
		value := uuid.NewString()
		taskID = &value
		clientProjectName = "source.mp4"
	}
	result, err := app.WorkflowRuns().Create(context.Background(), CreateWorkflowRunInput{
		UserID:            userID,
		ProjectID:         projectID,
		ClientType:        clientType,
		ClientTaskID:      taskID,
		ClientProjectName: clientProjectName,
		InputHash:         "workflow-input-" + uuid.NewString(),
		ClientInstanceID:  "billing-client",
	})
	if err != nil {
		t.Fatalf("create workflow run: %v", err)
	}
	return result.Run
}

func createWorkflowBillingStep(t *testing.T, app *App, runID uuid.UUID, stepID, inputHash string) models.WorkflowStepExecution {
	t.Helper()
	step := models.WorkflowStepExecution{
		WorkflowRunID: runID,
		StepID:        stepID,
		InputHash:     inputHash,
		ExecutionKey:  workflowStepExecutionKey(runID, stepID, inputHash),
		Status:        workflowRunStatusSucceeded,
	}
	if err := app.db.Create(&step).Error; err != nil {
		t.Fatalf("create workflow step: %v", err)
	}
	return step
}

func workflowRunTestConsumption(userID, runID, stepExecutionID uuid.UUID, executionKey string, credits int64) creditConsumption {
	return creditConsumption{
		UserID:                  userID,
		WorkflowRunID:           &runID,
		WorkflowStepExecutionID: &stepExecutionID,
		ExecutionKey:            executionKey,
		APIEndpoint:             "/api/v1/tools/workflow-test",
		Provider:                "openai",
		ServiceKind:             "llm",
		ModelID:                 "workflow-model",
		Usage:                   providers.ChatUsage{TotalTokens: credits},
	}
}

func assertWorkflowBillingCounts(t *testing.T, app *App, userID uuid.UUID, usedCredits, usageRecords, ledgerRecords int64) {
	t.Helper()
	var account models.UserTokenAccount
	if err := app.db.First(&account, "user_id = ?", userID).Error; err != nil {
		t.Fatalf("load account: %v", err)
	}
	if account.UsedTokens != usedCredits {
		t.Fatalf("used credits = %d, want %d", account.UsedTokens, usedCredits)
	}
	var usageCount int64
	if err := app.db.Model(&models.TokenUsageRecord{}).Where("user_id = ?", userID).Count(&usageCount).Error; err != nil {
		t.Fatalf("count usage records: %v", err)
	}
	if usageCount != usageRecords {
		t.Fatalf("usage records = %d, want %d", usageCount, usageRecords)
	}
	var ledgerCount int64
	if err := app.db.Model(&models.CreditLedgerRecord{}).Where("user_id = ? AND event_type = ?", userID, creditLedgerEventConsume).Count(&ledgerCount).Error; err != nil {
		t.Fatalf("count ledger records: %v", err)
	}
	if ledgerCount != ledgerRecords {
		t.Fatalf("ledger records = %d, want %d", ledgerCount, ledgerRecords)
	}
}

func assertWorkflowReservationStatus(t *testing.T, app *App, runID uuid.UUID, status string, count int64) {
	t.Helper()
	var actual int64
	if err := app.db.Model(&models.CreditReservation{}).Where("workflow_run_id = ? AND status = ?", runID, status).Count(&actual).Error; err != nil {
		t.Fatalf("count %s reservations: %v", status, err)
	}
	if actual != count {
		t.Fatalf("%s reservations = %d, want %d", status, actual, count)
	}
}

func newWorkflowRunBillingPostgresApp(t *testing.T, databaseURL string) *App {
	t.Helper()
	cfg := config.Config{
		Debug:             true,
		Environment:       "testing",
		Host:              "127.0.0.1",
		Port:              0,
		APIv1Prefix:       "/api/v1",
		OpenAPIURL:        "/api/v1/openapi.json",
		DocsURL:           "/docs",
		RedocURL:          "/redoc",
		DatabaseURL:       databaseURL,
		AutoMigrate:       true,
		SecretKey:         "test-secret-key-test-secret-key-1234",
		CORSOrigins:       []string{"*"},
		AccessTTL:         30 * time.Minute,
		RefreshTTL:        24 * time.Hour,
		PromptTemplateDir: "../../prompt",
	}
	db, err := database.Open(cfg)
	if err != nil {
		t.Fatalf("open postgres test database: %v", err)
	}
	if err := database.AutoMigrate(db); err != nil {
		t.Fatalf("migrate postgres test database: %v", err)
	}
	if err := db.Unscoped().Where("key = ?", "billing_rates").Delete(&models.SystemSetting{}).Error; err != nil {
		t.Fatalf("reset postgres billing rates: %v", err)
	}
	return New(cfg, db)
}

func assertWorkflowFinalizationWaitsForReservation(
	t *testing.T,
	app *App,
	consumption creditConsumption,
	finalize func() error,
) {
	t.Helper()
	callbackName := "workflow_billing_race_" + uuid.NewString()
	reachedCreate := make(chan struct{})
	releaseCreate := make(chan struct{})
	var reachOnce sync.Once
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCreate) }) }
	if err := app.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "credit_reservations" {
			return
		}
		reachOnce.Do(func() {
			close(reachedCreate)
			<-releaseCreate
		})
	}); err != nil {
		t.Fatalf("register reservation barrier: %v", err)
	}
	defer func() {
		release()
		_ = app.db.Callback().Create().Remove(callbackName)
	}()

	reserveDone := make(chan error, 1)
	go func() { reserveDone <- app.billCredits(consumption) }()
	select {
	case <-reachedCreate:
	case <-time.After(5 * time.Second):
		t.Fatal("reservation did not reach create barrier")
	}

	finalizeDone := make(chan error, 1)
	go func() { finalizeDone <- finalize() }()
	select {
	case err := <-finalizeDone:
		t.Fatalf("finalization completed before reservation transaction: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	release()
	if err := <-reserveDone; err != nil {
		t.Fatalf("reserve credits: %v", err)
	}
	if err := <-finalizeDone; err != nil {
		t.Fatalf("finalize workflow run: %v", err)
	}
}

func TestWorkflowRunBillingCancelAfterFinalResultConflicts(t *testing.T) {
	app, user, run, step := newWorkflowRunBillingFixture(t, "pc-cancel-final@example.com", nil)
	if err := app.billCredits(workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10)); err != nil {
		t.Fatalf("reserve credits: %v", err)
	}
	if _, err := app.WorkflowRuns().Complete(context.Background(), user.ID, run.ID, time.Now().UTC()); err != nil {
		t.Fatalf("complete workflow run: %v", err)
	}
	if _, err := app.WorkflowRuns().Cancel(context.Background(), user.ID, run.ID); !errors.Is(err, ErrWorkflowFinalResultExists) {
		t.Fatalf("cancel after final result error = %v", err)
	}
	assertWorkflowReservationStatus(t, app, run.ID, creditReservationConfirmed, 1)
	assertWorkflowBillingCounts(t, app, user.ID, 10, 1, 1)
}
func TestWorkflowRunBillingRetryInheritsPendingReservations(t *testing.T) {
	app, user, run, step := newWorkflowRunBillingFixture(t, "pc-retry-inherit@example.com", nil)
	if err := app.billCredits(workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10)); err != nil {
		t.Fatalf("reserve credits on first run: %v", err)
	}

	// Rewind creates a replacement run that inherits the first run's pending reservations.
	taskID := *run.ClientTaskID
	replacement, err := app.WorkflowRuns().Create(context.Background(), CreateWorkflowRunInput{
		UserID:                       user.ID,
		ClientType:                   "pc",
		ClientTaskID:                 &taskID,
		ClientProjectName:            "source.mp4",
		InputHash:                    "workflow-input-" + uuid.NewString(),
		ClientInstanceID:             "billing-client",
		InheritReservationsFromRunID: &run.ID,
	})
	if err != nil {
		t.Fatalf("create replacement run: %v", err)
	}
	if replacement.Reused {
		t.Fatalf("replacement run was reused instead of created")
	}
	if replacement.Run.ID == run.ID {
		t.Fatalf("replacement run id matches the cancelled run")
	}

	var stored models.WorkflowRun
	if err := app.db.First(&stored, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("load cancelled run: %v", err)
	}
	if stored.Status != workflowRunStatusCancelled {
		t.Fatalf("cancelled run status = %q, want cancelled", stored.Status)
	}
	if stored.BillingStatus != workflowRunBillingStatusReleased {
		t.Fatalf("cancelled run billing status = %q, want released", stored.BillingStatus)
	}
	assertWorkflowReservationStatus(t, app, run.ID, creditReservationPending, 0)
	assertWorkflowReservationStatus(t, app, replacement.Run.ID, creditReservationPending, 1)

	// Cancelling the superseded run is idempotent.
	if _, err := app.WorkflowRuns().Cancel(context.Background(), user.ID, run.ID); err != nil {
		t.Fatalf("cancel superseded run: %v", err)
	}

	// The replacement run also bills its own execution, so the total is cumulative.
	replacementStep := createWorkflowBillingStep(t, app, replacement.Run.ID, "generate-script", "script-input-retry")
	if err := app.billCredits(workflowRunTestConsumption(user.ID, replacement.Run.ID, replacementStep.ID, replacementStep.ExecutionKey, 10)); err != nil {
		t.Fatalf("reserve credits on replacement run: %v", err)
	}
	assertWorkflowReservationStatus(t, app, replacement.Run.ID, creditReservationPending, 2)

	if _, err := app.WorkflowRuns().Complete(context.Background(), user.ID, replacement.Run.ID, time.Now().UTC()); err != nil {
		t.Fatalf("complete replacement run: %v", err)
	}
	assertWorkflowReservationStatus(t, app, replacement.Run.ID, creditReservationConfirmed, 2)
	assertWorkflowBillingCounts(t, app, user.ID, 20, 2, 1)
}

func TestWorkflowRunBillingRetryInheritanceRejectsInvalidScope(t *testing.T) {
	app, user, run, step := newWorkflowRunBillingFixture(t, "pc-inherit-invalid@example.com", nil)
	if err := app.billCredits(workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10)); err != nil {
		t.Fatalf("reserve credits: %v", err)
	}

	otherTaskID := "other-task-" + uuid.NewString()
	if _, err := app.WorkflowRuns().Create(context.Background(), CreateWorkflowRunInput{
		UserID:                       user.ID,
		ClientType:                   "pc",
		ClientTaskID:                 &otherTaskID,
		ClientProjectName:            "other.mp4",
		InputHash:                    "workflow-input-" + uuid.NewString(),
		ClientInstanceID:             "billing-client",
		InheritReservationsFromRunID: &run.ID,
	}); !errors.Is(err, ErrWorkflowReservationInheritanceInvalid) {
		t.Fatalf("cross-task inheritance error = %v, want ErrWorkflowReservationInheritanceInvalid", err)
	}
	assertWorkflowReservationStatus(t, app, run.ID, creditReservationPending, 1)

	_, _, otherRun, _ := newWorkflowRunBillingFixture(t, "pc-inherit-other@example.com", nil)
	if _, err := app.WorkflowRuns().Create(context.Background(), CreateWorkflowRunInput{
		UserID:                       user.ID,
		ClientType:                   "pc",
		ClientTaskID:                 run.ClientTaskID,
		ClientProjectName:            "source.mp4",
		InputHash:                    "workflow-input-" + uuid.NewString(),
		ClientInstanceID:             "billing-client",
		InheritReservationsFromRunID: &otherRun.ID,
	}); !errors.Is(err, ErrWorkflowRunNotFound) {
		t.Fatalf("cross-user inheritance error = %v, want ErrWorkflowRunNotFound", err)
	}
	assertWorkflowReservationStatus(t, app, run.ID, creditReservationPending, 1)
}

func TestWorkflowRunBillingRetryInheritsReservationsViaHTTP(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLoginWithCredits(t, app, "pc-inherit-http@example.com", 100)
	user := lookupUserByEmail(t, app, "pc-inherit-http@example.com")
	seedWorkflowRunBillingRate(t, app)

	created := request(app, http.MethodPost, "/api/v1/workflow-runs/", map[string]any{
		"client_type":         "pc",
		"client_task_id":      "http-task",
		"client_project_name": "source.mp4",
		"input_hash":          "hash-first",
		"client_instance_id":  "pc-client",
	}, token)
	if created.Code != http.StatusCreated {
		t.Fatalf("first create status = %d, body = %s", created.Code, created.Body.String())
	}
	first := decodeWorkflowRunResponse(t, created)
	firstRunID := uuid.MustParse(first.ID)
	step := createWorkflowBillingStep(t, app, firstRunID, "generate-script", "script-input")
	if err := app.billCredits(workflowRunTestConsumption(user.ID, firstRunID, step.ID, step.ExecutionKey, 10)); err != nil {
		t.Fatalf("reserve credits: %v", err)
	}

	replacement := request(app, http.MethodPost, "/api/v1/workflow-runs/", map[string]any{
		"client_type":                      "pc",
		"client_task_id":                   "http-task-retry",
		"client_project_name":              "source.mp4",
		"input_hash":                       "hash-retry",
		"client_instance_id":               "pc-client",
		"inherit_reservations_from_run_id": first.ID,
	}, token)
	if replacement.Code != http.StatusCreated {
		t.Fatalf("replacement create status = %d, body = %s", replacement.Code, replacement.Body.String())
	}
	second := decodeWorkflowRunResponse(t, replacement)
	if second.ID == first.ID {
		t.Fatalf("replacement reused the cancelled run")
	}
	assertWorkflowReservationStatus(t, app, firstRunID, creditReservationPending, 0)
	assertWorkflowReservationStatus(t, app, uuid.MustParse(second.ID), creditReservationPending, 1)

	bad := request(app, http.MethodPost, "/api/v1/workflow-runs/", map[string]any{
		"client_type":                      "pc",
		"client_task_id":                   "http-task",
		"client_project_name":              "source.mp4",
		"input_hash":                       "hash-bad",
		"client_instance_id":               "pc-client",
		"inherit_reservations_from_run_id": "not-a-uuid",
	}, token)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad inherit uuid status = %d, body = %s", bad.Code, bad.Body.String())
	}
}

func TestWorkflowRunBillingRetryInheritsAcrossTaskIds(t *testing.T) {
	app, user, run, step := newWorkflowRunBillingFixture(t, "pc-inherit-cross-task@example.com", nil)
	if err := app.billCredits(workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10)); err != nil {
		t.Fatalf("reserve credits on first run: %v", err)
	}

	// A retry of the same video uses a NEW client_task_id but the same client_project_name.
	newTaskID := "clip-source-" + uuid.NewString()
	replacement, err := app.WorkflowRuns().Create(context.Background(), CreateWorkflowRunInput{
		UserID:                       user.ID,
		ClientType:                   "pc",
		ClientTaskID:                 &newTaskID,
		ClientProjectName:            "source.mp4",
		InputHash:                    "workflow-input-" + uuid.NewString(),
		ClientInstanceID:             "billing-client",
		InheritReservationsFromRunID: &run.ID,
	})
	if err != nil {
		t.Fatalf("create replacement run: %v", err)
	}
	if replacement.Reused {
		t.Fatalf("replacement run was reused instead of created")
	}

	var stored models.WorkflowRun
	if err := app.db.First(&stored, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("load cancelled run: %v", err)
	}
	if stored.Status != workflowRunStatusCancelled {
		t.Fatalf("cancelled run status = %q, want cancelled", stored.Status)
	}
	if stored.BillingStatus != workflowRunBillingStatusReleased {
		t.Fatalf("cancelled run billing status = %q, want released", stored.BillingStatus)
	}
	assertWorkflowReservationStatus(t, app, run.ID, creditReservationPending, 0)
	assertWorkflowReservationStatus(t, app, replacement.Run.ID, creditReservationPending, 1)

	// The replacement run bills its own execution; the total is cumulative.
	replacementStep := createWorkflowBillingStep(t, app, replacement.Run.ID, "generate-script", "script-input-retry")
	if err := app.billCredits(workflowRunTestConsumption(user.ID, replacement.Run.ID, replacementStep.ID, replacementStep.ExecutionKey, 10)); err != nil {
		t.Fatalf("reserve credits on replacement run: %v", err)
	}
	assertWorkflowReservationStatus(t, app, replacement.Run.ID, creditReservationPending, 2)

	if _, err := app.WorkflowRuns().Complete(context.Background(), user.ID, replacement.Run.ID, time.Now().UTC()); err != nil {
		t.Fatalf("complete replacement run: %v", err)
	}
	assertWorkflowReservationStatus(t, app, replacement.Run.ID, creditReservationConfirmed, 2)
	assertWorkflowBillingCounts(t, app, user.ID, 20, 2, 1)
}

func TestWorkflowRunBillingRetryInheritanceTerminalSourceNoop(t *testing.T) {
	app, user, run, step := newWorkflowRunBillingFixture(t, "pc-inherit-terminal@example.com", nil)
	if err := app.billCredits(workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10)); err != nil {
		t.Fatalf("reserve credits: %v", err)
	}
	if _, err := app.WorkflowRuns().Cancel(context.Background(), user.ID, run.ID); err != nil {
		t.Fatalf("cancel source run: %v", err)
	}
	assertWorkflowReservationStatus(t, app, run.ID, creditReservationReleased, 1)

	newTaskID := "clip-source-" + uuid.NewString()
	replacement, err := app.WorkflowRuns().Create(context.Background(), CreateWorkflowRunInput{
		UserID:                       user.ID,
		ClientType:                   "pc",
		ClientTaskID:                 &newTaskID,
		ClientProjectName:            "source.mp4",
		InputHash:                    "workflow-input-" + uuid.NewString(),
		ClientInstanceID:             "billing-client",
		InheritReservationsFromRunID: &run.ID,
	})
	if err != nil {
		t.Fatalf("create replacement run after terminal source: %v", err)
	}
	if replacement.Reused {
		t.Fatalf("replacement run was reused instead of created")
	}
	assertWorkflowReservationStatus(t, app, run.ID, creditReservationReleased, 1)
	assertWorkflowReservationStatus(t, app, replacement.Run.ID, creditReservationPending, 0)
}

func TestWorkflowRunBillingRetryInheritanceReusePathMigrates(t *testing.T) {
	app, user, run, step := newWorkflowRunBillingFixture(t, "pc-inherit-reuse@example.com", nil)
	if err := app.billCredits(workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10)); err != nil {
		t.Fatalf("reserve credits on reused run: %v", err)
	}

	// Source run B is a separate non-terminal run with pending reservations.
	sourceTaskID := "clip-source-b-" + uuid.NewString()
	source, err := app.WorkflowRuns().Create(context.Background(), CreateWorkflowRunInput{
		UserID:            user.ID,
		ClientType:        "pc",
		ClientTaskID:      &sourceTaskID,
		ClientProjectName: "source.mp4",
		InputHash:         "workflow-input-" + uuid.NewString(),
		ClientInstanceID:  "billing-client",
	})
	if err != nil {
		t.Fatalf("create source run: %v", err)
	}
	sourceStep := createWorkflowBillingStep(t, app, source.Run.ID, "generate-script", "script-input-b")
	if err := app.billCredits(workflowRunTestConsumption(user.ID, source.Run.ID, sourceStep.ID, sourceStep.ExecutionKey, 10)); err != nil {
		t.Fatalf("reserve credits on source run: %v", err)
	}

	// The fixture run is still active for the original task id, so the new create reuses it.
	replacement, err := app.WorkflowRuns().Create(context.Background(), CreateWorkflowRunInput{
		UserID:                       user.ID,
		ClientType:                   "pc",
		ClientTaskID:                 run.ClientTaskID,
		ClientProjectName:            "source.mp4",
		InputHash:                    run.InputHash,
		ClientInstanceID:             "billing-client",
		InheritReservationsFromRunID: &source.Run.ID,
	})
	if err != nil {
		t.Fatalf("create replacement run: %v", err)
	}
	if !replacement.Reused {
		t.Fatalf("expected the active run to be reused")
	}
	var sourceStored models.WorkflowRun
	if err := app.db.First(&sourceStored, "id = ?", source.Run.ID).Error; err != nil {
		t.Fatalf("load source run: %v", err)
	}
	if sourceStored.Status != workflowRunStatusCancelled {
		t.Fatalf("source run status = %q, want cancelled", sourceStored.Status)
	}
	assertWorkflowReservationStatus(t, app, source.Run.ID, creditReservationPending, 0)
	assertWorkflowReservationStatus(t, app, replacement.Run.ID, creditReservationPending, 2)
}

func TestWorkflowRunBillingRetryRevivesReleasedReservations(t *testing.T) {
	app, user, run, step := newWorkflowRunBillingFixture(t, "pc-revive-released@example.com", nil)
	if err := app.billCredits(workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10)); err != nil {
		t.Fatalf("reserve credits on first run: %v", err)
	}
	if _, err := app.WorkflowRuns().Cancel(context.Background(), user.ID, run.ID); err != nil {
		t.Fatalf("cancel first run: %v", err)
	}
	assertWorkflowReservationStatus(t, app, run.ID, creditReservationReleased, 1)

	// The desktop client retry reuses the same client_task_id and does NOT pass an inherit param.
	replacement, err := app.WorkflowRuns().Create(context.Background(), CreateWorkflowRunInput{
		UserID:            user.ID,
		ClientType:        "pc",
		ClientTaskID:      run.ClientTaskID,
		ClientProjectName: "source.mp4",
		InputHash:         "workflow-input-" + uuid.NewString(),
		ClientInstanceID:  "billing-client",
	})
	if err != nil {
		t.Fatalf("create replacement run: %v", err)
	}
	if replacement.Reused {
		t.Fatalf("replacement run was reused instead of created")
	}
	assertWorkflowReservationStatus(t, app, replacement.Run.ID, creditReservationPending, 1)

	// The replacement bills its own execution too; the total is cumulative.
	replacementStep := createWorkflowBillingStep(t, app, replacement.Run.ID, "generate-script", "script-input-retry")
	if err := app.billCredits(workflowRunTestConsumption(user.ID, replacement.Run.ID, replacementStep.ID, replacementStep.ExecutionKey, 10)); err != nil {
		t.Fatalf("reserve credits on replacement run: %v", err)
	}
	assertWorkflowReservationStatus(t, app, replacement.Run.ID, creditReservationPending, 2)

	if _, err := app.WorkflowRuns().Complete(context.Background(), user.ID, replacement.Run.ID, time.Now().UTC()); err != nil {
		t.Fatalf("complete replacement run: %v", err)
	}
	assertWorkflowReservationStatus(t, app, replacement.Run.ID, creditReservationConfirmed, 2)
	assertWorkflowBillingCounts(t, app, user.ID, 20, 2, 1)
}

func TestWorkflowRunBillingRetryDoesNotReviveAcrossTaskIds(t *testing.T) {
	app, user, run, step := newWorkflowRunBillingFixture(t, "pc-no-revive-other@example.com", nil)
	if err := app.billCredits(workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10)); err != nil {
		t.Fatalf("reserve credits on first run: %v", err)
	}
	if _, err := app.WorkflowRuns().Cancel(context.Background(), user.ID, run.ID); err != nil {
		t.Fatalf("cancel first run: %v", err)
	}

	newTaskID := "clip-other-" + uuid.NewString()
	replacement, err := app.WorkflowRuns().Create(context.Background(), CreateWorkflowRunInput{
		UserID:            user.ID,
		ClientType:        "pc",
		ClientTaskID:      &newTaskID,
		ClientProjectName: "source.mp4",
		InputHash:         "workflow-input-" + uuid.NewString(),
		ClientInstanceID:  "billing-client",
	})
	if err != nil {
		t.Fatalf("create replacement run: %v", err)
	}
	assertWorkflowReservationStatus(t, app, replacement.Run.ID, creditReservationPending, 0)
	assertWorkflowReservationStatus(t, app, run.ID, creditReservationReleased, 1)
}
