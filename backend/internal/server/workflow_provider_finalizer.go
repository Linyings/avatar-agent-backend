package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const workflowProviderFinalizationTimeout = 10 * time.Second

type providerSuccessFinalization struct {
	ProviderRequestID        *string
	RequireProviderRequestID bool
	Consumption              creditConsumption
	CompletedResult          map[string]any
}

func (a *App) finalizeProviderSuccess(ctx context.Context, execution workflowRequestExecution, input providerSuccessFinalization) error {
	if execution.Identity == nil {
		return a.billCredits(input.Consumption)
	}
	if input.Consumption.UserID != execution.Run.UserID {
		return fmt.Errorf("%w: billing user does not own resolved workflow execution", ErrWorkflowIdentityInvalid)
	}
	ctx, cancel := workflowProviderFinalizationContext(ctx)
	defer cancel()

	providerRequestID, requestIDErr := sanitizeWorkflowProviderRequestID(input.ProviderRequestID)
	if requestIDErr != nil && !input.RequireProviderRequestID {
		return requestIDErr
	}
	if input.RequireProviderRequestID && (requestIDErr != nil || providerRequestID == nil) {
		return a.finalizeProviderProtocolFailure(ctx, execution, "invalid provider response")
	}
	if requestIDErr != nil {
		return requestIDErr
	}
	consumption := providerFinalizationConsumption(execution, input.Consumption, providerRequestID)
	if !hasPositiveProviderBillableUsage(consumption) {
		return a.finalizeProviderProtocolFailure(ctx, execution, "invalid provider response")
	}
	usage, err := providerFinalizationUsage(consumption)
	if err != nil {
		return err
	}
	var completedResult datatypes.JSON
	if input.CompletedResult != nil {
		raw, err := json.Marshal(input.CompletedResult)
		if err != nil || len(raw) > movieHarnessMaxResultBytes {
			return newWorkflowRequestError(http.StatusInternalServerError, "workflow_result_unknown", false, "provider result could not be persisted; do not resubmit", err)
		}
		completedResult = datatypes.JSON(raw)
	}

	finalizationErr := retryWorkflowWrite(ctx, func() error {
		return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			now := a.workflowRuns.now()
			run, step, err := lockResolvedWorkflowProviderExecution(tx, execution, now)
			if err != nil {
				return err
			}
			lockedExecution := workflowRequestExecution{Identity: execution.Identity, Run: run, Step: step}
			consumption = providerFinalizationConsumption(lockedExecution, consumption, providerRequestID)
			locked := &workflowCreditReservationLocks{Run: &run, Step: &step}

			if step.Status == workflowRunStatusSucceeded {
				return validateSucceededProviderReservation(tx, run, step, consumption)
			}
			if step.Status != workflowRunStatusRunning {
				return ErrWorkflowStepTransitionInvalid
			}
			if !workflowStepLeaseOwnedByClient(&step, execution.Identity.ClientInstanceID, now) {
				return ErrWorkflowStepLeaseConflict
			}

			if err := a.reserveCreditsTx(ctx, tx, consumption, locked); err != nil {
				return err
			}
			applyWorkflowStepUpdate(&step, WorkflowStepUpdateInput{
				StepID:            step.StepID,
				InputHash:         step.InputHash,
				Status:            workflowRunStatusSucceeded,
				ClientInstanceID:  execution.Identity.ClientInstanceID,
				ProviderRequestID: providerRequestID,
				Usage:             usage,
			}, now, false)
			step.ErrorCode = nil
			step.ErrorMessage = nil
			if step.ResultVersion == 1 {
				if len(completedResult) == 0 {
					return newWorkflowRequestError(http.StatusInternalServerError, "workflow_result_unknown", false, "provider result could not be persisted; do not resubmit", nil)
				}
				step.Result = completedResult
				step.ProviderSubmissionClaimID = nil
				step.ProviderSubmissionClaimedAt = nil
			}
			if err := tx.Save(&step).Error; err != nil {
				return err
			}
			return a.recordWorkflowProviderActivityTx(ctx, tx, &run, step.StepID, now)
		})
	})
	if finalizationErr != nil && len(completedResult) > 0 && !errors.Is(finalizationErr, ErrInsufficientCredits) {
		// The provider completed, but its accounting/output transaction did not.
		// Preserve the claim even if the lease later expires; never repay blindly.
		_ = a.db.WithContext(ctx).Model(&models.WorkflowStepExecution{}).Where("id = ? AND status <> ?", execution.Step.ID, workflowRunStatusSucceeded).
			Updates(map[string]any{"error_code": "workflow_result_unknown", "error_message": "provider completion could not be finalized; do not resubmit"}).Error
		return movieHarnessUnknownResult()
	}
	return finalizationErr
}

func (a *App) finalizeProviderProtocolFailure(ctx context.Context, execution workflowRequestExecution, message string) error {
	protocolErr := newWorkflowRequestError(http.StatusBadGateway, "provider_protocol_error", true, message, nil)
	if err := a.finalizeProviderNonSuccess(ctx, execution, workflowStepStatusFailed, "provider_protocol_error", message); err != nil {
		return err
	}
	return protocolErr
}

func validateSucceededProviderReservation(tx *gorm.DB, run models.WorkflowRun, step models.WorkflowStepExecution, consumption creditConsumption) error {
	var reservation models.CreditReservation
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("idempotency_key = ?", creditReservationIdempotencyKey(consumption)).
		Take(&reservation).Error; err != nil {
		return fmt.Errorf("load succeeded workflow reservation: %w", err)
	}
	if reservation.UserID != run.UserID ||
		!uuidPointersEqual(reservation.ProjectID, run.ProjectID) ||
		reservation.WorkflowRunID == nil || *reservation.WorkflowRunID != run.ID ||
		reservation.WorkflowStepExecutionID == nil || *reservation.WorkflowStepExecutionID != step.ID {
		return fmt.Errorf("%w: succeeded workflow reservation identity mismatch", ErrCreditReservationConflict)
	}
	if reservation.Status != creditReservationPending && reservation.Status != creditReservationConfirmed {
		return fmt.Errorf("succeeded workflow reservation is %s", reservation.Status)
	}
	currentFingerprint := workflowInputFingerprintFromMetadata(consumption.RequestMetadata)
	var existingMetadata map[string]any
	if err := json.Unmarshal(reservation.RequestMetadata, &existingMetadata); err != nil {
		return fmt.Errorf("decode succeeded workflow reservation metadata: %w", err)
	}
	existingFingerprint := workflowInputFingerprintFromMetadata(existingMetadata)
	if currentFingerprint != existingFingerprint {
		return fmt.Errorf("%w: succeeded workflow input fingerprint mismatch", ErrCreditReservationConflict)
	}
	return nil
}

func (a *App) finalizeProviderFailure(ctx context.Context, execution workflowRequestExecution, providerErr error) error {
	status, code, message := safeProviderFailureDiagnostics(providerErr)
	return a.finalizeProviderNonSuccess(ctx, execution, status, code, message)
}

func (a *App) finalizeProviderBlocked(ctx context.Context, execution workflowRequestExecution, code, message string) error {
	return a.finalizeProviderNonSuccess(ctx, execution, workflowRunStatusBlocked, code, message)
}

func (a *App) finalizeProviderNonSuccess(ctx context.Context, execution workflowRequestExecution, status, code, message string) error {
	if execution.Identity == nil {
		return nil
	}
	ctx, cancel := workflowProviderFinalizationContext(ctx)
	defer cancel()
	message = workflowProviderFailureDiagnostic(code, message)
	stepInput, err := sanitizeWorkflowStepUpdate(WorkflowStepUpdateInput{
		StepID:           execution.Identity.WorkflowStepID,
		InputHash:        execution.Identity.WorkflowInputHash,
		Status:           status,
		ClientInstanceID: execution.Identity.ClientInstanceID,
		ErrorCode:        &code,
		ErrorMessage:     &message,
		Usage:            datatypes.JSON([]byte("{}")),
	})
	if err != nil {
		return err
	}

	return retryWorkflowWrite(ctx, func() error {
		return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			now := a.workflowRuns.now()
			run, step, err := lockResolvedWorkflowProviderExecution(tx, execution, now)
			if err != nil {
				return err
			}
			if step.Status != workflowRunStatusRunning {
				return ErrWorkflowStepTransitionInvalid
			}
			if !workflowStepLeaseOwnedByClient(&step, execution.Identity.ClientInstanceID, now) {
				return ErrWorkflowStepLeaseConflict
			}

			applyWorkflowStepUpdate(&step, stepInput, now, false)
			// Async submissions retain their opaque Provider task ID so a retry can poll the same task.
			if step.ProviderInputHash == nil {
				step.ProviderRequestID = nil
			}
			step.Usage = datatypes.JSON([]byte("{}"))
			// A known rejection/output failure can retry, and so can a stream the client cancelled:
			// the provider call runs on the request context, so it stopped with the connection.
			// A timeout may still be processing paid work upstream, so it keeps its submission claim.
			if step.ResultVersion == 1 && code != "request_timed_out" {
				step.ProviderSubmissionClaimID = nil
				step.ProviderSubmissionClaimedAt = nil
			}
			if err := tx.Save(&step).Error; err != nil {
				return err
			}
			if status == workflowRunStatusBlocked {
				run.Status = workflowRunStatusBlocked
			}
			return a.recordWorkflowProviderActivityTx(ctx, tx, &run, step.StepID, now)
		})
	})
}

// workflowProviderFailureDiagnostic keeps persisted workflow diagnostics free of
// provider or source content while allowing handlers to return richer, localized
// messages to the caller.
func workflowProviderFailureDiagnostic(code, message string) string {
	if _, sanitized, err := sanitizeWorkflowDiagnostics(nil, &message); err == nil && sanitized != nil {
		return *sanitized
	}
	switch code {
	case "provider_usage_missing":
		return "provider returned content but omitted token usage"
	case "provider_invalid_output":
		return "invalid provider output"
	case "provider_protocol_error":
		return "invalid provider response"
	default:
		return "operation failed"
	}
}

func workflowProviderFinalizationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), workflowProviderFinalizationTimeout)
}

func lockResolvedWorkflowProviderExecution(tx *gorm.DB, execution workflowRequestExecution, now time.Time) (models.WorkflowRun, models.WorkflowStepExecution, error) {
	if execution.Identity == nil || execution.Run.UserID == [16]byte{} {
		return models.WorkflowRun{}, models.WorkflowStepExecution{}, ErrWorkflowIdentityInvalid
	}
	run, err := loadOwnedWorkflowRun(tx, execution.Run.UserID, execution.Identity.WorkflowRunID)
	if err != nil {
		return models.WorkflowRun{}, models.WorkflowStepExecution{}, err
	}
	if run.InputHash != execution.Identity.WorkflowInputHash || !uuidPointersEqual(run.ProjectID, execution.Run.ProjectID) {
		return models.WorkflowRun{}, models.WorkflowStepExecution{}, ErrWorkflowIdentityInvalid
	}
	if isWorkflowRunTerminal(run.Status) || run.Status == workflowRunStatusFinalizing {
		return models.WorkflowRun{}, models.WorkflowStepExecution{}, ErrWorkflowRunTerminal
	}
	if run.ClientInstanceID != execution.Identity.ClientInstanceID || run.LeaseExpiresAt == nil || !run.LeaseExpiresAt.After(now) {
		return models.WorkflowRun{}, models.WorkflowStepExecution{}, ErrWorkflowRunLeaseConflict
	}

	var step models.WorkflowStepExecution
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND workflow_run_id = ?", execution.Step.ID, run.ID).
		Take(&step).Error; err != nil {
		return models.WorkflowRun{}, models.WorkflowStepExecution{}, err
	}
	executionKey := workflowStepExecutionKey(run.ID, execution.Identity.WorkflowStepID, execution.Identity.WorkflowInputHash)
	if step.StepID != execution.Identity.WorkflowStepID || step.InputHash != execution.Identity.WorkflowInputHash || step.ExecutionKey != executionKey {
		return models.WorkflowRun{}, models.WorkflowStepExecution{}, ErrWorkflowIdentityInvalid
	}
	return run, step, nil
}

func providerFinalizationConsumption(execution workflowRequestExecution, consumption creditConsumption, providerRequestID *string) creditConsumption {
	inputFingerprint := workflowInputFingerprintFromMetadata(consumption.RequestMetadata)
	// The desktop movie project is how a project's total is summed; it must survive the reset below.
	movieProjectID, _ := consumption.RequestMetadata["movie_project_id"].(string)
	consumption = execution.EnrichCreditConsumption(consumption)
	consumption.RequestMetadata = map[string]any{}
	if providerRequestID != nil {
		consumption.RequestMetadata["request_id"] = *providerRequestID
	}
	if inputFingerprint != "" {
		consumption.RequestMetadata[workflowInputFingerprintMetadataKey] = inputFingerprint
	}
	if movieProjectID != "" {
		consumption.RequestMetadata["movie_project_id"] = movieProjectID
	}
	return normalizeCreditConsumption(consumption)
}

func providerFinalizationUsage(consumption creditConsumption) (datatypes.JSON, error) {
	usage := map[string]any{}
	if consumption.Usage.PromptTokens != 0 {
		usage["prompt_tokens"] = consumption.Usage.PromptTokens
	}
	if consumption.Usage.CompletionTokens != 0 {
		usage["completion_tokens"] = consumption.Usage.CompletionTokens
	}
	if consumption.Usage.TotalTokens != 0 {
		usage["total_tokens"] = consumption.Usage.TotalTokens
	}
	if consumption.UTF8Bytes != 0 {
		usage["utf8_bytes"] = consumption.UTF8Bytes
	}
	if consumption.CharacterCount != 0 {
		usage["character_count"] = consumption.CharacterCount
	}
	if consumption.DurationSeconds != nil {
		usage["duration_seconds"] = *consumption.DurationSeconds
	}
	raw, err := json.Marshal(usage)
	if err != nil {
		return nil, err
	}
	return sanitizeWorkflowUsage(datatypes.JSON(raw))
}

func hasPositiveProviderBillableUsage(consumption creditConsumption) bool {
	return consumption.Usage.TotalTokens > 0 ||
		consumption.CharacterCount > 0 ||
		(consumption.DurationSeconds != nil && *consumption.DurationSeconds > 0)
}

func safeProviderFailureDiagnostics(err error) (status, code, message string) {
	if errors.Is(err, providers.ErrGeneralResponseInvalid) {
		return workflowStepStatusFailed, "provider_invalid_output", "通用模式模型响应格式无效，请重试该步骤"
	}
	var diagnostic interface{ ProviderHTTPStatus() int }
	if errors.As(err, &diagnostic) {
		statusCode := diagnostic.ProviderHTTPStatus()
		fields := map[string]any{}
		var detailed interface{ ProviderDiagnosticFields() map[string]any }
		if errors.As(err, &detailed) {
			fields = detailed.ProviderDiagnosticFields()
		}
		log.Printf("provider failure: http_status=%d error_type=%T request_id=%v provider_code=%v body_length=%v body_sha256=%v", statusCode, err, fields["request_id"], fields["provider_code"], fields["body_length"], fields["body_sha256"])
		// Provider-account billing failures are never a user's credit shortage.
		providerCode := strings.ToLower(fmt.Sprint(fields["provider_code"]))
		if statusCode == http.StatusPaymentRequired || providerCode == "1113" || providerCode == "insufficient_quota" || providerCode == "insufficient_balance" || providerCode == "arrearage" {
			return workflowStepStatusFailed, "provider_account_unavailable", "模型调用失败，请联系客服"
		}
		switch statusCode {
		case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return workflowStepStatusFailed, "provider_unavailable", "provider temporarily unavailable"
		}
		if fields["provider_code"] == "content_blocked" {
			return workflowStepStatusFailed, "provider_content_blocked", providers.ProviderContentReviewMessage
		}
		switch statusCode {
		default:
			return workflowStepStatusFailed, "provider_request_rejected", "provider rejected the request"
		}
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return workflowRunStatusInterrupted, "request_timed_out", "request timed out"
	case errors.Is(err, context.Canceled):
		return workflowRunStatusInterrupted, "operation_interrupted", "operation interrupted"
	default:
		return workflowStepStatusFailed, "provider_unavailable", "provider unavailable"
	}
}

func (a *App) recordWorkflowProviderActivityTx(ctx context.Context, tx *gorm.DB, run *models.WorkflowRun, stepID string, now time.Time) error {
	run.CurrentStepID = &stepID
	run.LastActiveAt = now
	run.ExpiresAt = now.Add(workflowRunInactivityDuration)
	run.LeaseExpiresAt = timePointer(now.Add(workflowRunLeaseDuration))
	if err := tx.Save(run).Error; err != nil {
		return err
	}
	return a.workflowRuns.runAfterActivityHook(ctx, tx, run)
}
