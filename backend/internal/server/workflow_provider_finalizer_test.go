package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
	"gorm.io/datatypes"
)

func TestFinalizeProviderSuccessCommitsStepAndReservationAtomically(t *testing.T) {
	app, user, execution := newProviderFinalizationTestExecution(t, "provider-success")
	providerRequestID := "provider-request-123"
	consumption := providerFinalizationTestConsumption(user.ID)
	consumption.RequestMetadata = map[string]any{
		"request_id": providerRequestID,
		"prompt":     "secret narration prompt",
		"subtitle":   "secret subtitle",
		"local_path": `C:\private\video.mp4`,
	}

	err := app.finalizeProviderSuccess(context.Background(), execution, providerSuccessFinalization{
		ProviderRequestID: &providerRequestID,
		Consumption:       consumption,
	})
	if err != nil {
		t.Fatalf("finalize provider success: %v", err)
	}

	step := loadProviderFinalizationStep(t, app, execution.Step.ID)
	if step.Status != workflowRunStatusSucceeded {
		t.Fatalf("step status = %q, want succeeded", step.Status)
	}
	if step.ProviderRequestID == nil || *step.ProviderRequestID != providerRequestID {
		t.Fatalf("provider request id = %v, want %q", step.ProviderRequestID, providerRequestID)
	}
	if step.CompletedAt == nil || step.LeaseOwner != nil || step.LeaseExpiresAt != nil {
		t.Fatalf("completed step lease fields are not cleared: %#v", step)
	}
	var usage map[string]any
	if err := json.Unmarshal(step.Usage, &usage); err != nil {
		t.Fatalf("decode step usage: %v", err)
	}
	for key, want := range map[string]float64{
		"prompt_tokens":     7,
		"completion_tokens": 5,
		"total_tokens":      12,
		"utf8_bytes":        24,
		"character_count":   8,
		"duration_seconds":  3.5,
	} {
		if got := usage[key]; got != want {
			t.Fatalf("usage[%q] = %#v, want %v", key, got, want)
		}
	}

	var reservations []models.CreditReservation
	if err := app.db.Where("workflow_run_id = ?", execution.Run.ID).Find(&reservations).Error; err != nil {
		t.Fatalf("load reservations: %v", err)
	}
	if len(reservations) != 1 {
		t.Fatalf("reservations = %d, want 1", len(reservations))
	}
	reservation := reservations[0]
	if reservation.Status != creditReservationPending || reservation.WorkflowStepExecutionID == nil || *reservation.WorkflowStepExecutionID != step.ID {
		t.Fatalf("unexpected reservation: %#v", reservation)
	}
	if reservation.PromptTokens != 7 || reservation.CompletionTokens != 5 || reservation.TotalModelTokens != 12 || reservation.UTF8Bytes != 24 || reservation.CharacterCount != 8 {
		t.Fatalf("reservation usage mismatch: %#v", reservation)
	}

	stored, err := json.Marshal(struct {
		Step        models.WorkflowStepExecution
		Reservation models.CreditReservation
	}{Step: step, Reservation: reservation})
	if err != nil {
		t.Fatalf("marshal stored workflow data: %v", err)
	}
	for _, forbidden := range []string{"secret narration prompt", "secret subtitle", `C:\private\video.mp4`} {
		if bytes.Contains(stored, []byte(forbidden)) {
			t.Fatalf("stored workflow data contains business content %q: %s", forbidden, stored)
		}
	}
	assertWorkflowBillingCounts(t, app, user.ID, 0, 0, 0)
}

func TestFinalizeProviderSuccessRollsBackOnReservationFailure(t *testing.T) {
	app, user, execution := newProviderFinalizationTestExecution(t, "provider-rollback")
	if err := app.db.Exec(`
		CREATE TRIGGER fail_provider_finalization_reservation
		BEFORE INSERT ON credit_reservations
		BEGIN
			SELECT RAISE(ABORT, 'simulated reservation failure');
		END;
	`).Error; err != nil {
		t.Fatalf("create reservation failure trigger: %v", err)
	}
	providerRequestID := "provider-request-rollback"

	err := app.finalizeProviderSuccess(context.Background(), execution, providerSuccessFinalization{
		ProviderRequestID: &providerRequestID,
		Consumption:       providerFinalizationTestConsumption(user.ID),
	})
	if err == nil || !strings.Contains(err.Error(), "simulated reservation failure") {
		t.Fatalf("finalize error = %v, want simulated reservation failure", err)
	}

	step := loadProviderFinalizationStep(t, app, execution.Step.ID)
	if step.Status != workflowRunStatusRunning {
		t.Fatalf("step status = %q, want running after rollback", step.Status)
	}
	if step.ProviderRequestID != nil || !bytes.Equal(step.Usage, datatypes.JSON([]byte("{}"))) || step.CompletedAt != nil {
		t.Fatalf("provider success metadata escaped rollback: %#v", step)
	}
	var reservationCount int64
	if err := app.db.Model(&models.CreditReservation{}).Where("workflow_run_id = ?", execution.Run.ID).Count(&reservationCount).Error; err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if reservationCount != 0 {
		t.Fatalf("reservations = %d, want 0", reservationCount)
	}
}

func TestFinalizeProviderBlockedAtomicallyBlocksRunAndStepAndPreservesReservation(t *testing.T) {
	t.Run("blocks and resumes", func(t *testing.T) {
		app, user, execution := newProviderFinalizationTestExecution(t, "provider-blocked-run")
		consumption := execution.EnrichCreditConsumption(providerFinalizationTestConsumption(user.ID))
		if err := app.reserveCredits(consumption); err != nil {
			t.Fatalf("seed pending reservation: %v", err)
		}
		blockedAt := app.workflowRuns.now()
		app.workflowRuns.now = func() time.Time { return blockedAt }

		if err := app.finalizeProviderBlocked(context.Background(), execution, "insufficient_credits", "insufficient credits"); err != nil {
			t.Fatalf("finalize provider blocked: %v", err)
		}

		step := loadProviderFinalizationStep(t, app, execution.Step.ID)
		if step.Status != workflowRunStatusBlocked || step.ErrorCode == nil || *step.ErrorCode != "insufficient_credits" {
			t.Fatalf("blocked step = %#v", step)
		}
		var run models.WorkflowRun
		if err := app.db.First(&run, "id = ?", execution.Run.ID).Error; err != nil {
			t.Fatalf("load blocked run: %v", err)
		}
		if run.Status != workflowRunStatusBlocked || run.CurrentStepID == nil || *run.CurrentStepID != execution.Step.StepID {
			t.Fatalf("blocked run = %#v", run)
		}
		if !run.LastActiveAt.Equal(blockedAt) || !run.ExpiresAt.Equal(blockedAt.Add(workflowRunInactivityDuration)) {
			t.Fatalf("blocked run activity = %#v", run)
		}
		var reservation models.CreditReservation
		if err := app.db.Where("workflow_run_id = ?", run.ID).Take(&reservation).Error; err != nil {
			t.Fatalf("load preserved reservation: %v", err)
		}
		if reservation.Status != creditReservationPending {
			t.Fatalf("reservation status = %q, want pending", reservation.Status)
		}

		resumed, err := app.WorkflowRuns().Resume(context.Background(), user.ID, run.ID, "provider-finalization-resumed-client")
		if err != nil {
			t.Fatalf("resume blocked run: %v", err)
		}
		if resumed.Status != workflowRunStatusRunning {
			t.Fatalf("resumed run status = %q, want running", resumed.Status)
		}
		identity := workflowRequestIdentityForTest(run.ID, execution.Step.StepID, run.InputHash, resumed.ClientInstanceID)
		resumedExecution, err := app.resolveWorkflowRequestIdentity(context.Background(), user.ID, nil, identity)
		if err != nil {
			t.Fatalf("resolve resumed execution: %v", err)
		}
		providerRequestID := "provider-request-after-resume"
		if err := app.finalizeProviderSuccess(context.Background(), resumedExecution, providerSuccessFinalization{
			ProviderRequestID: &providerRequestID,
			Consumption:       providerFinalizationTestConsumption(user.ID),
		}); err != nil {
			t.Fatalf("finalize resumed execution: %v", err)
		}
		if step = loadProviderFinalizationStep(t, app, execution.Step.ID); step.Status != workflowRunStatusSucceeded {
			t.Fatalf("resumed step status = %q, want succeeded", step.Status)
		}
	})

	t.Run("rolls back step when run update fails", func(t *testing.T) {
		app, _, execution := newProviderFinalizationTestExecution(t, "provider-blocked-rollback")
		if err := app.db.Exec(`
			CREATE TRIGGER fail_provider_blocked_run_update
			BEFORE UPDATE ON workflow_runs
			BEGIN
				SELECT RAISE(ABORT, 'simulated blocked run failure');
			END;
		`).Error; err != nil {
			t.Fatalf("create blocked run failure trigger: %v", err)
		}

		err := app.finalizeProviderBlocked(context.Background(), execution, "insufficient_credits", "insufficient credits")
		if err == nil || !strings.Contains(err.Error(), "simulated blocked run failure") {
			t.Fatalf("finalize error = %v, want simulated blocked run failure", err)
		}
		if step := loadProviderFinalizationStep(t, app, execution.Step.ID); step.Status != workflowRunStatusRunning {
			t.Fatalf("step status = %q, want running after rollback", step.Status)
		}
		var run models.WorkflowRun
		if err := app.db.First(&run, "id = ?", execution.Run.ID).Error; err != nil {
			t.Fatalf("load run after rollback: %v", err)
		}
		if run.Status != workflowRunStatusRunning {
			t.Fatalf("run status = %q, want running after rollback", run.Status)
		}
	})
}

func TestFinalizeProviderSuccessRejectsZeroBillableUsageWithoutReservation(t *testing.T) {
	tests := []struct {
		name        string
		consumption func(uuid.UUID) creditConsumption
	}{
		{
			name: "token usage",
			consumption: func(userID uuid.UUID) creditConsumption {
				return creditConsumption{UserID: userID, CreditCount: 1, APIEndpoint: "/tokens", Provider: "openai", ServiceKind: "llm", ModelID: "workflow-model"}
			},
		},
		{
			name: "duration usage",
			consumption: func(userID uuid.UUID) creditConsumption {
				zero := 0.0
				return creditConsumption{UserID: userID, CreditCount: 1, APIEndpoint: "/duration", Provider: "elevenlabs", ServiceKind: "asr", ModelID: "scribe-zero", DurationSeconds: &zero}
			},
		},
		{
			name: "character usage",
			consumption: func(userID uuid.UUID) creditConsumption {
				return creditConsumption{UserID: userID, CreditCount: 1, APIEndpoint: "/characters", Provider: "siliconflow", ServiceKind: "tts", ModelID: "tts-zero"}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, user, execution := newProviderFinalizationTestExecution(t, "provider-zero-"+strings.ReplaceAll(tt.name, " ", "-"))
			if err := app.db.Model(&models.SystemSetting{}).Where("key = ?", "billing_rates").Update("value", jsonMap(map[string]any{
				"llm": []map[string]any{{"provider": "openai", "model_id": "workflow-model", "tokens_per_credit": 1}},
				"asr": []map[string]any{{"provider": "elevenlabs", "model_id": "scribe-zero", "credits_per_minute": 1}},
				"tts": []map[string]any{{"provider": "siliconflow", "model_id": "tts-zero", "credits_per_char": 1}},
			})).Error; err != nil {
				t.Fatalf("update billing rates: %v", err)
			}

			err := app.finalizeProviderSuccess(context.Background(), execution, providerSuccessFinalization{Consumption: tt.consumption(user.ID)})
			requestErr, ok := asWorkflowRequestError(err)
			if !ok || requestErr.Status != http.StatusBadGateway || requestErr.Code != "provider_protocol_error" || !requestErr.Retryable {
				t.Fatalf("zero usage error = %#v, %v", requestErr, err)
			}
			if step := loadProviderFinalizationStep(t, app, execution.Step.ID); step.Status == workflowRunStatusSucceeded {
				t.Fatalf("zero usage marked step succeeded: %#v", step)
			}
			var reservations int64
			if err := app.db.Model(&models.CreditReservation{}).Where("workflow_run_id = ?", execution.Run.ID).Count(&reservations).Error; err != nil {
				t.Fatalf("count reservations: %v", err)
			}
			if reservations != 0 {
				t.Fatalf("reservations = %d, want 0", reservations)
			}
		})
	}
}

func TestFinalizeProviderSuccessSucceededReplayPreservesOriginalFinalization(t *testing.T) {
	app, user, execution := newProviderFinalizationTestExecution(t, "provider-replay")
	providerRequestID := "provider-request-replay"
	input := providerSuccessFinalization{
		ProviderRequestID: &providerRequestID,
		Consumption:       providerFinalizationTestConsumption(user.ID),
	}
	if err := app.finalizeProviderSuccess(context.Background(), execution, input); err != nil {
		t.Fatalf("first finalize: %v", err)
	}
	changedProviderID := "provider-request-changed"
	changedConsumption := input.Consumption
	changedConsumption.CreditCount = 26
	changedConsumption.Usage = providers.ChatUsage{PromptTokens: 17, CompletionTokens: 9, TotalTokens: 26}
	changedConsumption.UTF8Bytes = 52
	changedConsumption.CharacterCount = 18
	err := app.finalizeProviderSuccess(context.Background(), execution, providerSuccessFinalization{
		ProviderRequestID: &changedProviderID,
		Consumption:       changedConsumption,
	})
	if err != nil {
		t.Fatalf("changed replay: %v", err)
	}

	step := loadProviderFinalizationStep(t, app, execution.Step.ID)
	if step.AttemptCount != 1 || step.Status != workflowRunStatusSucceeded {
		t.Fatalf("replayed step = %#v", step)
	}
	if step.ProviderRequestID == nil || *step.ProviderRequestID != providerRequestID {
		t.Fatalf("changed replay mutated provider id: %#v", step.ProviderRequestID)
	}
	wantUsage, err := providerFinalizationUsage(input.Consumption)
	if err != nil {
		t.Fatalf("build expected usage: %v", err)
	}
	if !bytes.Equal(step.Usage, wantUsage) {
		t.Fatalf("changed replay mutated step usage: got %s, want %s", step.Usage, wantUsage)
	}

	var reservations []models.CreditReservation
	if err := app.db.Where("workflow_run_id = ?", execution.Run.ID).Find(&reservations).Error; err != nil {
		t.Fatalf("load reservations: %v", err)
	}
	if len(reservations) != 1 {
		t.Fatalf("reservations = %d, want 1", len(reservations))
	}
	reservation := reservations[0]
	if reservation.Status != creditReservationPending || reservation.CreditCount != input.Consumption.CreditCount {
		t.Fatalf("changed replay mutated reservation: %#v", reservation)
	}
	if reservation.PromptTokens != input.Consumption.Usage.PromptTokens ||
		reservation.CompletionTokens != input.Consumption.Usage.CompletionTokens ||
		reservation.TotalModelTokens != input.Consumption.Usage.TotalTokens {
		t.Fatalf("changed replay mutated reservation usage: %#v", reservation)
	}
	var metadata map[string]any
	if err := json.Unmarshal(reservation.RequestMetadata, &metadata); err != nil {
		t.Fatalf("decode reservation metadata: %v", err)
	}
	if metadata["request_id"] != providerRequestID {
		t.Fatalf("changed replay mutated reservation request metadata: %#v", metadata)
	}
	assertWorkflowBillingCounts(t, app, user.ID, 0, 0, 0)
}

func TestFinalizeProviderSuccessSucceededReplayRequiresUsableOriginalReservation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *App, workflowRequestExecution)
	}{
		{
			name: "missing reservation",
			mutate: func(t *testing.T, app *App, execution workflowRequestExecution) {
				t.Helper()
				if err := app.db.Where("workflow_run_id = ?", execution.Run.ID).Delete(&models.CreditReservation{}).Error; err != nil {
					t.Fatalf("delete reservation: %v", err)
				}
			},
		},
		{
			name: "released reservation",
			mutate: func(t *testing.T, app *App, execution workflowRequestExecution) {
				t.Helper()
				if err := app.db.Model(&models.CreditReservation{}).
					Where("workflow_run_id = ?", execution.Run.ID).
					Update("status", creditReservationReleased).Error; err != nil {
					t.Fatalf("release reservation: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, user, execution := newProviderFinalizationTestExecution(t, "provider-replay-reservation-"+uuid.NewString())
			providerRequestID := "provider-request-original"
			input := providerSuccessFinalization{
				ProviderRequestID: &providerRequestID,
				Consumption:       providerFinalizationTestConsumption(user.ID),
			}
			if err := app.finalizeProviderSuccess(context.Background(), execution, input); err != nil {
				t.Fatalf("first finalize: %v", err)
			}
			tt.mutate(t, app, execution)

			changedProviderID := "provider-request-replay"
			err := app.finalizeProviderSuccess(context.Background(), execution, providerSuccessFinalization{
				ProviderRequestID: &changedProviderID,
				Consumption:       input.Consumption,
			})
			if err == nil {
				t.Fatal("replay succeeded without a usable original reservation")
			}
		})
	}
}

func TestFinalizeProviderSuccessRejectsMetadataAndStateConflicts(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*testing.T, *App, workflowRequestExecution, *providerSuccessFinalization)
		wantErr error
	}{
		{
			name: "invalid provider request id",
			mutate: func(_ *testing.T, _ *App, _ workflowRequestExecution, input *providerSuccessFinalization) {
				value := "secret subtitle with spaces"
				input.ProviderRequestID = &value
			},
			wantErr: ErrWorkflowMetadataInvalid,
		},
		{
			name: "negative numeric usage",
			mutate: func(_ *testing.T, _ *App, _ workflowRequestExecution, input *providerSuccessFinalization) {
				input.Consumption.Usage.TotalTokens = -1
			},
			wantErr: ErrWorkflowMetadataInvalid,
		},
		{
			name: "run lease owner changed",
			mutate: func(t *testing.T, app *App, execution workflowRequestExecution, _ *providerSuccessFinalization) {
				t.Helper()
				future := time.Now().UTC().Add(time.Minute)
				if err := app.db.Model(&models.WorkflowRun{}).Where("id = ?", execution.Run.ID).Updates(map[string]any{
					"client_instance_id": "other-client",
					"lease_expires_at":   future,
				}).Error; err != nil {
					t.Fatalf("change run lease: %v", err)
				}
			},
			wantErr: ErrWorkflowRunLeaseConflict,
		},
		{
			name: "step lease expired",
			mutate: func(t *testing.T, app *App, execution workflowRequestExecution, _ *providerSuccessFinalization) {
				t.Helper()
				past := time.Now().UTC().Add(-time.Minute)
				if err := app.db.Model(&models.WorkflowStepExecution{}).Where("id = ?", execution.Step.ID).Update("lease_expires_at", past).Error; err != nil {
					t.Fatalf("expire step lease: %v", err)
				}
			},
			wantErr: ErrWorkflowStepLeaseConflict,
		},
		{
			name: "step state changed",
			mutate: func(t *testing.T, app *App, execution workflowRequestExecution, _ *providerSuccessFinalization) {
				t.Helper()
				if err := app.db.Model(&models.WorkflowStepExecution{}).Where("id = ?", execution.Step.ID).Updates(map[string]any{
					"status":           workflowStepStatusFailed,
					"lease_owner":      nil,
					"lease_expires_at": nil,
				}).Error; err != nil {
					t.Fatalf("change step state: %v", err)
				}
			},
			wantErr: ErrWorkflowStepTransitionInvalid,
		},
		{
			name: "run became terminal",
			mutate: func(t *testing.T, app *App, execution workflowRequestExecution, _ *providerSuccessFinalization) {
				t.Helper()
				if err := app.db.Model(&models.WorkflowRun{}).Where("id = ?", execution.Run.ID).Update("status", workflowRunStatusCancelled).Error; err != nil {
					t.Fatalf("cancel run: %v", err)
				}
			},
			wantErr: ErrWorkflowRunTerminal,
		},
		{
			name: "billing user mismatch",
			mutate: func(_ *testing.T, _ *App, _ workflowRequestExecution, input *providerSuccessFinalization) {
				input.Consumption.UserID = uuid.New()
			},
			wantErr: ErrWorkflowIdentityInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, user, execution := newProviderFinalizationTestExecution(t, "provider-conflict-"+strings.ReplaceAll(tt.name, " ", "-"))
			providerRequestID := "provider-request-conflict"
			input := providerSuccessFinalization{
				ProviderRequestID: &providerRequestID,
				Consumption:       providerFinalizationTestConsumption(user.ID),
			}
			tt.mutate(t, app, execution, &input)

			err := app.finalizeProviderSuccess(context.Background(), execution, input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			var reservationCount int64
			if countErr := app.db.Model(&models.CreditReservation{}).Where("workflow_run_id = ?", execution.Run.ID).Count(&reservationCount).Error; countErr != nil {
				t.Fatalf("count reservations: %v", countErr)
			}
			if reservationCount != 0 {
				t.Fatalf("reservations = %d, want 0", reservationCount)
			}
		})
	}
}

func TestWorkflowProviderFinalizationDetachesCanceledRequestContext(t *testing.T) {
	tests := []struct {
		name       string
		finalize   func(context.Context, *App, models.User, workflowRequestExecution) error
		wantStatus string
		wantCode   string
		wantCount  int64
	}{
		{
			name: "success",
			finalize: func(ctx context.Context, app *App, user models.User, execution workflowRequestExecution) error {
				providerRequestID := "provider-request-canceled-success"
				return app.finalizeProviderSuccess(ctx, execution, providerSuccessFinalization{
					ProviderRequestID: &providerRequestID,
					Consumption:       providerFinalizationTestConsumption(user.ID),
				})
			},
			wantStatus: workflowRunStatusSucceeded,
			wantCount:  1,
		},
		{
			name: "failure",
			finalize: func(ctx context.Context, app *App, _ models.User, execution workflowRequestExecution) error {
				return app.finalizeProviderFailure(ctx, execution, errors.New("provider failed"))
			},
			wantStatus: workflowStepStatusFailed,
			wantCode:   "provider_unavailable",
		},
		{
			name: "blocked",
			finalize: func(ctx context.Context, app *App, _ models.User, execution workflowRequestExecution) error {
				return app.finalizeProviderBlocked(ctx, execution, "insufficient_credits", "insufficient credits")
			},
			wantStatus: workflowRunStatusBlocked,
			wantCode:   "insufficient_credits",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, user, execution := newProviderFinalizationTestExecution(t, "provider-canceled-"+tt.name)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			if err := tt.finalize(ctx, app, user, execution); err != nil {
				t.Fatalf("finalize with canceled request context: %v", err)
			}

			step := loadProviderFinalizationStep(t, app, execution.Step.ID)
			if step.Status != tt.wantStatus {
				t.Fatalf("step status = %q, want %q", step.Status, tt.wantStatus)
			}
			if tt.wantCode != "" && (step.ErrorCode == nil || *step.ErrorCode != tt.wantCode) {
				t.Fatalf("step error code = %v, want %q", step.ErrorCode, tt.wantCode)
			}
			var reservationCount int64
			if err := app.db.Model(&models.CreditReservation{}).Where("workflow_run_id = ?", execution.Run.ID).Count(&reservationCount).Error; err != nil {
				t.Fatalf("count reservations: %v", err)
			}
			if reservationCount != tt.wantCount {
				t.Fatalf("reservations = %d, want %d", reservationCount, tt.wantCount)
			}
		})
	}
}

func TestFinalizeProviderFailureStoresSafeDiagnosticsWithoutReservation(t *testing.T) {
	tests := []struct {
		name        string
		providerErr error
		wantStatus  string
		wantCode    string
		wantMessage string
	}{
		{
			name:        "provider failure",
			providerErr: errors.New("provider failed with secret subtitle and C:\\private\\video.mp4"),
			wantStatus:  workflowStepStatusFailed,
			wantCode:    "provider_unavailable",
			wantMessage: "provider unavailable",
		},
		{
			name: "temporary provider failure",
			providerErr: &providers.DashScopeHTTPError{
				StatusCode: http.StatusBadGateway,
				RequestID:  "request-safe-1",
			},
			wantStatus:  workflowStepStatusFailed,
			wantCode:    "provider_unavailable",
			wantMessage: "provider temporarily unavailable",
		},
		{
			name: "provider rejected request",
			providerErr: &providers.DashScopeHTTPError{
				StatusCode: http.StatusBadRequest,
				RequestID:  "request-safe-2",
			},
			wantStatus:  workflowStepStatusFailed,
			wantCode:    "provider_request_rejected",
			wantMessage: "provider rejected the request",
		},
		{
			name:        "interrupted request",
			providerErr: context.Canceled,
			wantStatus:  workflowRunStatusInterrupted,
			wantCode:    "operation_interrupted",
			wantMessage: "operation interrupted",
		},
		{
			name:        "timed out request",
			providerErr: context.DeadlineExceeded,
			wantStatus:  workflowRunStatusInterrupted,
			wantCode:    "request_timed_out",
			wantMessage: "request timed out",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, _, execution := newProviderFinalizationTestExecution(t, "provider-failure-"+strings.ReplaceAll(tt.name, " ", "-"))
			if err := app.finalizeProviderFailure(context.Background(), execution, tt.providerErr); err != nil {
				t.Fatalf("finalize provider failure: %v", err)
			}

			step := loadProviderFinalizationStep(t, app, execution.Step.ID)
			if step.Status != tt.wantStatus || step.ErrorCode == nil || *step.ErrorCode != tt.wantCode || step.ErrorMessage == nil || *step.ErrorMessage != tt.wantMessage {
				t.Fatalf("failed step = %#v", step)
			}
			if step.ProviderRequestID != nil || !bytes.Equal(step.Usage, datatypes.JSON([]byte("{}"))) || step.LeaseOwner != nil || step.LeaseExpiresAt != nil {
				t.Fatalf("failure retained provider success metadata: %#v", step)
			}
			raw, err := json.Marshal(step)
			if err != nil {
				t.Fatalf("marshal step: %v", err)
			}
			for _, forbidden := range []string{"secret subtitle", `C:\private\video.mp4`} {
				if bytes.Contains(raw, []byte(forbidden)) {
					t.Fatalf("failed step contains business content %q: %s", forbidden, raw)
				}
			}
			var reservationCount int64
			if err := app.db.Model(&models.CreditReservation{}).Where("workflow_run_id = ?", execution.Run.ID).Count(&reservationCount).Error; err != nil {
				t.Fatalf("count reservations: %v", err)
			}
			if reservationCount != 0 {
				t.Fatalf("reservations = %d, want 0", reservationCount)
			}
		})
	}
}

func TestFinalizeProviderFailureReleasesClaimOnlyWhenTheClientCancelled(t *testing.T) {
	for _, tt := range []struct {
		name      string
		err       error
		keepClaim bool
	}{{"cancelled by the client", context.Canceled, false}, {"timed out upstream", context.DeadlineExceeded, true}} {
		t.Run(tt.name, func(t *testing.T) {
			app, _, execution := newProviderFinalizationTestExecution(t, "provider-claim-"+strings.ReplaceAll(tt.name, " ", "-"))
			claim, now := uuid.New(), time.Now()
			if err := app.db.Model(&models.WorkflowStepExecution{}).Where("id = ?", execution.Step.ID).
				Updates(map[string]any{"result_version": 1, "provider_submission_claim_id": claim, "provider_submission_claimed_at": now}).Error; err != nil {
				t.Fatalf("claim step: %v", err)
			}
			execution.Step.ResultVersion = 1
			if err := app.finalizeProviderFailure(context.Background(), execution, tt.err); err != nil {
				t.Fatalf("finalize: %v", err)
			}
			step := loadProviderFinalizationStep(t, app, execution.Step.ID)
			if kept := step.ProviderSubmissionClaimID != nil; kept != tt.keepClaim {
				t.Fatalf("claim kept = %v, want %v (a cancelled stream stopped the provider call and may be retried)", kept, tt.keepClaim)
			}
		})
	}
}

func TestFinalizeProviderNonSuccessStoresSafeFallbackForLocalizedDiagnostics(t *testing.T) {
	app, _, execution := newProviderFinalizationTestExecution(t, "provider-usage-localized")
	message := "网络不稳定导致模型用量统计缺失，请点击重试继续（已完成的步骤不受影响，也不影响剪辑效果）"
	if err := app.finalizeProviderNonSuccess(context.Background(), execution, workflowStepStatusFailed, "provider_usage_missing", message); err != nil {
		t.Fatalf("finalize localized provider usage failure: %v", err)
	}

	step := loadProviderFinalizationStep(t, app, execution.Step.ID)
	if step.Status != workflowStepStatusFailed || step.ErrorCode == nil || *step.ErrorCode != "provider_usage_missing" || step.ErrorMessage == nil || *step.ErrorMessage != "provider returned content but omitted token usage" {
		t.Fatalf("failed step = %#v", step)
	}
	raw, err := json.Marshal(step)
	if err != nil {
		t.Fatalf("marshal step: %v", err)
	}
	if bytes.Contains(raw, []byte(message)) {
		t.Fatalf("failed step retained localized caller message: %s", raw)
	}
}

func TestFinalizeProviderSuccessLegacyBillsImmediately(t *testing.T) {
	app := newTestApp(t)
	seedWorkflowRunBillingRate(t, app)
	token := registerAndLoginWithCredits(t, app, "provider-finalizer-legacy@example.com", 100)
	_ = token
	user := lookupUserByEmail(t, app, "provider-finalizer-legacy@example.com")

	if err := app.finalizeProviderSuccess(context.Background(), workflowRequestExecution{}, providerSuccessFinalization{
		Consumption: providerFinalizationTestConsumption(user.ID),
	}); err != nil {
		t.Fatalf("legacy finalize: %v", err)
	}
	assertWorkflowBillingCounts(t, app, user.ID, 12, 1, 1)
}

func newProviderFinalizationTestExecution(t *testing.T, suffix string) (*App, models.User, workflowRequestExecution) {
	t.Helper()
	app := newTestApp(t)
	seedWorkflowRunBillingRate(t, app)
	email := fmt.Sprintf("%s-%s@example.com", suffix, uuid.NewString())
	registerAndLoginWithCredits(t, app, email, 1000)
	user := lookupUserByEmail(t, app, email)
	run := createWorkflowIdentityTestRun(t, app, user.ID, "provider-finalization-input", "provider-finalization-client")
	identity := workflowRequestIdentityForTest(run.ID, "provider.finalize", run.InputHash, run.ClientInstanceID)
	execution, err := app.resolveWorkflowRequestIdentity(context.Background(), user.ID, nil, identity)
	if err != nil {
		t.Fatalf("resolve workflow execution: %v", err)
	}
	return app, user, execution
}

func providerFinalizationTestConsumption(userID uuid.UUID) creditConsumption {
	duration := 3.5
	return creditConsumption{
		UserID:          userID,
		CreditCount:     12,
		APIEndpoint:     "/api/v1/tools/provider-finalization-test",
		Provider:        "openai",
		ServiceKind:     "llm",
		ModelID:         "workflow-model",
		Usage:           providers.ChatUsage{PromptTokens: 7, CompletionTokens: 5, TotalTokens: 12},
		UTF8Bytes:       24,
		CharacterCount:  8,
		DurationSeconds: &duration,
	}
}

func loadProviderFinalizationStep(t *testing.T, app *App, stepID uuid.UUID) models.WorkflowStepExecution {
	t.Helper()
	var step models.WorkflowStepExecution
	if err := app.db.First(&step, "id = ?", stepID).Error; err != nil {
		t.Fatalf("load workflow step: %v", err)
	}
	return step
}
