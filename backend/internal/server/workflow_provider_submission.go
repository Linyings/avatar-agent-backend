package server

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
)

type workflowProviderSubmissionClaim struct {
	Execution      workflowRequestExecution
	ClaimID        string
	ProviderTaskID string
	Replayed       bool
}

func (a *App) claimWorkflowProviderSubmission(
	ctx context.Context,
	execution workflowRequestExecution,
	providerInputHash string,
) (workflowProviderSubmissionClaim, error) {
	providerInputHash, err := sanitizeWorkflowProviderInputHash(providerInputHash)
	if err != nil {
		return workflowProviderSubmissionClaim{}, err
	}
	claimID := uuid.NewString()
	ctx, cancel := workflowProviderFinalizationContext(ctx)
	defer cancel()

	var claimed workflowProviderSubmissionClaim
	err = retryWorkflowWrite(ctx, func() error {
		claimed = workflowProviderSubmissionClaim{}
		return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			now := a.workflowRuns.now()
			run, step, err := lockResolvedWorkflowProviderExecution(tx, execution, now)
			if err != nil {
				return workflowProviderSubmissionErrorForState(err)
			}
			if step.Status != workflowRunStatusRunning {
				return workflowProviderSubmissionErrorForState(ErrWorkflowStepTransitionInvalid)
			}
			if !workflowStepLeaseOwnedByClient(&step, execution.Identity.ClientInstanceID, now) {
				return workflowProviderSubmissionErrorForState(ErrWorkflowStepLeaseConflict)
			}
			if step.ProviderRequestID != nil {
				if step.ProviderInputHash == nil {
					return invalidWorkflowProviderSubmissionError(ErrWorkflowMetadataInvalid)
				}
				if err := refreshWorkflowProviderSubmissionStep(tx, &step, execution.Identity.ClientInstanceID, now); err != nil {
					return err
				}
				if err := a.recordWorkflowProviderActivityTx(ctx, tx, &run, step.StepID, now); err != nil {
					return err
				}
				claimed = workflowProviderSubmissionClaim{
					Execution:      workflowRequestExecution{Identity: execution.Identity, Run: run, Step: step},
					ProviderTaskID: *step.ProviderRequestID,
					Replayed:       true,
				}
				return nil
			}
			if step.ProviderInputHash != nil && *step.ProviderInputHash != providerInputHash {
				return workflowProviderInputMismatchError()
			}

			staleBefore := now.Add(-workflowRunLeaseDuration)
			// MPS has no idempotency token. An unconfirmed paid submission must not
			// become eligible for automatic re-submission when its lease expires.
			if strings.HasPrefix(step.StepID, "dubbing-separation-") && step.ProviderSubmissionClaimID != nil {
				return workflowProviderSubmissionInProgressError()
			}
			if workflowProviderSubmissionClaimActive(step, staleBefore) {
				return workflowProviderSubmissionInProgressError()
			}
			leaseExpiresAt := now.Add(workflowRunLeaseDuration)
			query := tx.Model(&models.WorkflowStepExecution{}).
				Where("id = ? AND workflow_run_id = ? AND status = ? AND lease_owner = ? AND lease_expires_at > ? AND provider_request_id IS NULL AND (provider_input_hash IS NULL OR provider_input_hash = ?)", step.ID, run.ID, workflowRunStatusRunning, execution.Identity.ClientInstanceID, now, providerInputHash)
			if step.ProviderSubmissionClaimID == nil {
				query = query.Where("provider_submission_claim_id IS NULL")
			} else {
				query = query.Where("provider_submission_claim_id = ? AND provider_submission_claimed_at <= ?", *step.ProviderSubmissionClaimID, staleBefore)
			}
			result := query.Updates(map[string]any{
				"provider_input_hash":            providerInputHash,
				"provider_submission_claim_id":   claimID,
				"provider_submission_claimed_at": now,
				"lease_expires_at":               leaseExpiresAt,
			})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return workflowProviderSubmissionClaimConflictError()
			}
			step = models.WorkflowStepExecution{}
			if err := tx.Where("id = ?", execution.Step.ID).Take(&step).Error; err != nil {
				return err
			}
			if err := a.recordWorkflowProviderActivityTx(ctx, tx, &run, step.StepID, now); err != nil {
				return err
			}
			claimed = workflowProviderSubmissionClaim{
				Execution: workflowRequestExecution{Identity: execution.Identity, Run: run, Step: step},
				ClaimID:   claimID,
			}
			return nil
		})
	})
	return claimed, err
}

func (a *App) persistWorkflowProviderSubmission(
	ctx context.Context,
	execution workflowRequestExecution,
	claimID string,
	providerTaskID string,
	providerInputHash string,
) (workflowRequestExecution, error) {
	providerTaskID, providerInputHash, err := sanitizeWorkflowProviderSubmission(providerTaskID, providerInputHash)
	if err != nil {
		return workflowRequestExecution{}, err
	}
	claimID, err = sanitizeWorkflowProviderSubmissionClaimID(claimID)
	if err != nil {
		return workflowRequestExecution{}, err
	}
	ctx, cancel := workflowProviderFinalizationContext(ctx)
	defer cancel()

	var stored workflowRequestExecution
	err = retryWorkflowWrite(ctx, func() error {
		stored = workflowRequestExecution{}
		return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			now := a.workflowRuns.now()
			run, step, err := lockWorkflowProviderClaimPersistence(tx, execution)
			if err != nil {
				return workflowProviderSubmissionErrorForState(err)
			}
			if step.Status != workflowRunStatusRunning {
				return workflowProviderSubmissionErrorForState(ErrWorkflowStepTransitionInvalid)
			}

			result := tx.Model(&models.WorkflowStepExecution{}).
				Where(
					"id = ? AND workflow_run_id = ? AND status = ? AND provider_request_id IS NULL AND provider_input_hash = ? AND provider_submission_claim_id = ? AND provider_submission_claimed_at IS NOT NULL",
					step.ID,
					run.ID,
					workflowRunStatusRunning,
					providerInputHash,
					claimID,
				).
				Updates(map[string]any{
					"provider_request_id":            providerTaskID,
					"provider_submission_claim_id":   nil,
					"provider_submission_claimed_at": nil,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return workflowProviderSubmissionClaimConflictError()
			}
			step = models.WorkflowStepExecution{}
			if err := tx.Where("id = ?", execution.Step.ID).Take(&step).Error; err != nil {
				return err
			}
			if err := a.recordWorkflowProviderActivityTx(ctx, tx, &run, step.StepID, now); err != nil {
				return err
			}
			stored = workflowRequestExecution{Identity: execution.Identity, Run: run, Step: step}
			return nil
		})
	})
	return stored, err
}

func lockWorkflowProviderClaimPersistence(tx *gorm.DB, execution workflowRequestExecution) (models.WorkflowRun, models.WorkflowStepExecution, error) {
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
	var step models.WorkflowStepExecution
	if err := tx.Where("id = ? AND workflow_run_id = ?", execution.Step.ID, run.ID).Take(&step).Error; err != nil {
		return models.WorkflowRun{}, models.WorkflowStepExecution{}, err
	}
	executionKey := workflowStepExecutionKey(run.ID, execution.Identity.WorkflowStepID, execution.Identity.WorkflowInputHash)
	if step.StepID != execution.Identity.WorkflowStepID || step.InputHash != execution.Identity.WorkflowInputHash || step.ExecutionKey != executionKey {
		return models.WorkflowRun{}, models.WorkflowStepExecution{}, ErrWorkflowIdentityInvalid
	}
	return run, step, nil
}

func (a *App) releaseWorkflowProviderSubmissionClaim(
	ctx context.Context,
	execution workflowRequestExecution,
	claimID string,
) error {
	claimID, err := sanitizeWorkflowProviderSubmissionClaimID(claimID)
	if err != nil {
		return err
	}
	ctx, cancel := workflowProviderFinalizationContext(ctx)
	defer cancel()

	return retryWorkflowWrite(ctx, func() error {
		return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			now := a.workflowRuns.now()
			run, step, err := lockResolvedWorkflowProviderExecution(tx, execution, now)
			if err != nil {
				return workflowProviderSubmissionErrorForState(err)
			}
			if step.ProviderSubmissionClaimID == nil || *step.ProviderSubmissionClaimID != claimID || step.ProviderRequestID != nil {
				return workflowProviderSubmissionClaimConflictError()
			}
			result := tx.Model(&models.WorkflowStepExecution{}).
				Where("id = ? AND provider_request_id IS NULL AND provider_submission_claim_id = ?", step.ID, claimID).
				Updates(map[string]any{
					"provider_submission_claim_id":   nil,
					"provider_submission_claimed_at": nil,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return workflowProviderSubmissionClaimConflictError()
			}
			return a.recordWorkflowProviderActivityTx(ctx, tx, &run, step.StepID, now)
		})
	})
}

func (a *App) findWorkflowProviderSubmissionReplay(
	ctx context.Context,
	userID uuid.UUID,
	requestProjectID *uuid.UUID,
	identity *workflowRequestIdentity,
	providerInputHash string,
) (workflowRequestExecution, string, bool, error) {
	if identity == nil {
		return workflowRequestExecution{}, "", false, nil
	}
	providerInputHash, err := sanitizeWorkflowProviderInputHash(providerInputHash)
	if err != nil {
		return workflowRequestExecution{}, "", false, err
	}

	var replayed workflowRequestExecution
	var providerTaskID string
	var found bool
	err = retryWorkflowWrite(ctx, func() error {
		replayed = workflowRequestExecution{}
		providerTaskID = ""
		found = false
		return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			now := a.workflowRuns.now()
			run, err := lockWorkflowProviderSubmissionRun(tx, userID, requestProjectID, identity, now)
			if err != nil {
				return err
			}
			step, err := loadWorkflowRequestStep(tx, run.ID, identity.WorkflowStepID)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := validateWorkflowProviderSubmissionStepIdentity(run, step, identity); err != nil {
				return err
			}
			if step.ProviderRequestID == nil {
				if step.ProviderInputHash != nil && *step.ProviderInputHash != providerInputHash {
					return workflowProviderInputMismatchError()
				}
				if workflowProviderSubmissionClaimActive(step, now.Add(-workflowRunLeaseDuration)) {
					return workflowProviderSubmissionInProgressError()
				}
				return nil
			}
			if step.ProviderInputHash == nil {
				return invalidWorkflowProviderSubmissionError(ErrWorkflowMetadataInvalid)
			}
			if err := refreshWorkflowProviderSubmissionStep(tx, &step, identity.ClientInstanceID, now); err != nil {
				return err
			}
			if err := a.recordWorkflowProviderActivityTx(ctx, tx, &run, step.StepID, now); err != nil {
				return err
			}
			replayed = workflowRequestExecution{Identity: identity, Run: run, Step: step}
			providerTaskID = *step.ProviderRequestID
			found = true
			return nil
		})
	})
	return replayed, providerTaskID, found, err
}

func (a *App) resolveWorkflowProviderQuery(
	ctx context.Context,
	userID uuid.UUID,
	requestProjectID *uuid.UUID,
	identity *workflowRequestIdentity,
	providerTaskID string,
	providerInputHash string,
) (workflowRequestExecution, error) {
	if identity == nil {
		return workflowRequestExecution{}, invalidWorkflowRequestIdentityError(ErrWorkflowIdentityInvalid)
	}
	providerTaskID, err := sanitizeWorkflowProviderTaskID(providerTaskID)
	if err != nil {
		return workflowRequestExecution{}, err
	}
	providerInputHash = strings.TrimSpace(providerInputHash)
	if providerInputHash != "" {
		providerInputHash, err = sanitizeWorkflowProviderInputHash(providerInputHash)
		if err != nil {
			return workflowRequestExecution{}, err
		}
	}

	var resolved workflowRequestExecution
	err = retryWorkflowWrite(ctx, func() error {
		resolved = workflowRequestExecution{}
		return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			now := a.workflowRuns.now()
			run, err := lockWorkflowProviderSubmissionRun(tx, userID, requestProjectID, identity, now)
			if err != nil {
				return err
			}
			step, err := loadWorkflowRequestStep(tx, run.ID, identity.WorkflowStepID)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return workflowProviderSubmissionErrorForState(err)
			}
			if err != nil {
				return err
			}
			if err := validateWorkflowProviderSubmissionStepIdentity(run, step, identity); err != nil {
				return err
			}
			if step.ProviderRequestID == nil || step.ProviderInputHash == nil {
				return workflowProviderSubmissionErrorForState(gorm.ErrRecordNotFound)
			}
			storedInputHash, err := sanitizeWorkflowProviderInputHash(*step.ProviderInputHash)
			if err != nil {
				return workflowProviderSubmissionErrorForState(err)
			}
			if *step.ProviderRequestID != providerTaskID {
				return workflowProviderTaskMismatchError()
			}
			if providerInputHash == "" {
				providerInputHash = storedInputHash
			} else if storedInputHash != providerInputHash {
				return workflowProviderInputMismatchError()
			}
			if err := refreshWorkflowProviderSubmissionStep(tx, &step, identity.ClientInstanceID, now); err != nil {
				return err
			}
			if err := a.recordWorkflowProviderActivityTx(ctx, tx, &run, step.StepID, now); err != nil {
				return err
			}
			resolved = workflowRequestExecution{Identity: identity, Run: run, Step: step}
			return nil
		})
	})
	return resolved, err
}

func sanitizeWorkflowProviderSubmission(providerTaskID, providerInputHash string) (string, string, error) {
	providerTaskID, err := sanitizeWorkflowProviderTaskID(providerTaskID)
	if err != nil {
		return "", "", err
	}
	providerInputHash, err = sanitizeWorkflowProviderInputHash(providerInputHash)
	if err != nil {
		return "", "", err
	}
	return providerTaskID, providerInputHash, nil
}

func sanitizeWorkflowProviderSubmissionClaimID(claimID string) (string, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(claimID))
	if err != nil || parsed == uuid.Nil {
		return "", invalidWorkflowProviderSubmissionError(err)
	}
	return parsed.String(), nil
}

func workflowProviderSubmissionClaimActive(step models.WorkflowStepExecution, staleBefore time.Time) bool {
	if step.ProviderSubmissionClaimID == nil {
		return false
	}
	return step.ProviderSubmissionClaimedAt == nil || !step.ProviderSubmissionClaimedAt.Before(staleBefore)
}

func sanitizeWorkflowProviderTaskID(providerTaskID string) (string, error) {
	if len(providerTaskID) < 16 || len(providerTaskID) > 128 {
		return "", invalidWorkflowProviderSubmissionError(ErrWorkflowMetadataInvalid)
	}
	hasLetter := false
	hasDigit := false
	for index := 0; index < len(providerTaskID); index++ {
		character := providerTaskID[index]
		switch {
		case character >= 'A' && character <= 'Z', character >= 'a' && character <= 'z':
			hasLetter = true
		case character >= '0' && character <= '9':
			hasDigit = true
		case character == '_', character == '-':
		default:
			return "", invalidWorkflowProviderSubmissionError(ErrWorkflowMetadataInvalid)
		}
	}
	if !hasLetter || !hasDigit {
		return "", invalidWorkflowProviderSubmissionError(ErrWorkflowMetadataInvalid)
	}
	return providerTaskID, nil
}

func sanitizeWorkflowProviderInputHash(providerInputHash string) (string, error) {
	providerInputHash = strings.ToLower(strings.TrimSpace(providerInputHash))
	decoded, err := hex.DecodeString(providerInputHash)
	if err != nil || len(decoded) != 32 {
		return "", invalidWorkflowProviderSubmissionError(err)
	}
	return providerInputHash, nil
}

func invalidWorkflowProviderSubmissionError(cause error) *workflowRequestError {
	return newWorkflowRequestError(
		http.StatusBadRequest,
		"invalid_provider_submission",
		false,
		"provider task id or provider input hash is invalid",
		cause,
	)
}

func workflowProviderSubmissionConflictError() *workflowRequestError {
	return newWorkflowRequestError(
		http.StatusConflict,
		"workflow_provider_submission_conflict",
		false,
		"workflow step already has a different provider submission",
		nil,
	)
}

func workflowProviderSubmissionInProgressError() *workflowRequestError {
	return newWorkflowRequestError(
		http.StatusConflict,
		"workflow_provider_submission_in_progress",
		true,
		"workflow provider submission is already in progress",
		nil,
	)
}

func workflowProviderSubmissionClaimConflictError() *workflowRequestError {
	return newWorkflowRequestError(
		http.StatusConflict,
		"workflow_provider_submission_claim_conflict",
		false,
		"workflow provider submission claim does not match",
		nil,
	)
}

func workflowProviderInputMismatchError() *workflowRequestError {
	return newWorkflowRequestError(
		http.StatusConflict,
		"workflow_input_mismatch",
		false,
		"workflow input identity does not match the provider submission",
		nil,
	)
}

func workflowProviderTaskMismatchError() *workflowRequestError {
	return newWorkflowRequestError(
		http.StatusConflict,
		"workflow_provider_task_mismatch",
		false,
		"provider task id does not match the workflow submission",
		nil,
	)
}

func lockWorkflowProviderSubmissionRun(
	tx *gorm.DB,
	userID uuid.UUID,
	requestProjectID *uuid.UUID,
	identity *workflowRequestIdentity,
	now time.Time,
) (models.WorkflowRun, error) {
	run, err := loadOwnedWorkflowRun(tx, userID, identity.WorkflowRunID)
	if err != nil {
		return models.WorkflowRun{}, workflowProviderSubmissionErrorForState(err)
	}
	if requestProjectID != nil && !uuidPointersEqual(run.ProjectID, requestProjectID) {
		return models.WorkflowRun{}, newWorkflowRequestError(http.StatusConflict, "workflow_project_mismatch", false, "workflow run does not belong to the requested project", nil)
	}
	if run.InputHash != identity.WorkflowInputHash {
		return models.WorkflowRun{}, workflowProviderInputMismatchError()
	}
	if isWorkflowRunTerminal(run.Status) || run.Status == workflowRunStatusFinalizing {
		return models.WorkflowRun{}, workflowProviderSubmissionErrorForState(ErrWorkflowRunTerminal)
	}
	if run.ClientInstanceID != identity.ClientInstanceID || run.LeaseExpiresAt == nil || !run.LeaseExpiresAt.After(now) {
		return models.WorkflowRun{}, workflowProviderSubmissionErrorForState(ErrWorkflowRunLeaseConflict)
	}
	return run, nil
}

func validateWorkflowProviderSubmissionStepIdentity(
	run models.WorkflowRun,
	step models.WorkflowStepExecution,
	identity *workflowRequestIdentity,
) error {
	executionKey := workflowStepExecutionKey(run.ID, identity.WorkflowStepID, identity.WorkflowInputHash)
	if step.StepID != identity.WorkflowStepID || step.InputHash != identity.WorkflowInputHash || step.ExecutionKey != executionKey {
		return newWorkflowRequestError(http.StatusConflict, "workflow_step_identity_mismatch", false, "workflow step identity was already used with different input", ErrWorkflowIdentityInvalid)
	}
	return nil
}

func refreshWorkflowProviderSubmissionStep(
	tx *gorm.DB,
	step *models.WorkflowStepExecution,
	clientInstanceID string,
	now time.Time,
) error {
	switch step.Status {
	case workflowRunStatusRunning:
		if !workflowStepLeaseOwnedByClient(step, clientInstanceID, now) {
			return workflowProviderSubmissionErrorForState(ErrWorkflowStepLeaseConflict)
		}
		leaseExpiresAt := now.Add(workflowRunLeaseDuration)
		result := tx.Model(&models.WorkflowStepExecution{}).
			Where("id = ? AND status = ? AND lease_owner = ? AND lease_expires_at > ?", step.ID, workflowRunStatusRunning, clientInstanceID, now).
			Update("lease_expires_at", leaseExpiresAt)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return workflowProviderSubmissionErrorForState(ErrWorkflowStepLeaseConflict)
		}
		step.LeaseExpiresAt = &leaseExpiresAt
		return nil
	case workflowRunStatusSucceeded:
		return nil
	case workflowStepStatusFailed, workflowRunStatusInterrupted:
		leaseExpiresAt := now.Add(workflowRunLeaseDuration)
		result := tx.Model(&models.WorkflowStepExecution{}).
			Where("id = ? AND status = ?", step.ID, step.Status).
			Updates(map[string]any{
				"status":           workflowRunStatusRunning,
				"lease_owner":      clientInstanceID,
				"lease_expires_at": leaseExpiresAt,
				"error_code":       nil,
				"error_message":    nil,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return workflowProviderSubmissionErrorForState(ErrWorkflowStepLeaseConflict)
		}
		step.Status = workflowRunStatusRunning
		step.LeaseOwner = &clientInstanceID
		step.LeaseExpiresAt = &leaseExpiresAt
		step.ErrorCode = nil
		step.ErrorMessage = nil
		return nil
	default:
		return workflowProviderSubmissionErrorForState(ErrWorkflowStepTransitionInvalid)
	}
}

func workflowProviderSubmissionErrorForState(err error) error {
	if _, ok := asWorkflowRequestError(err); ok {
		return err
	}
	switch {
	case errors.Is(err, ErrWorkflowStepTransitionInvalid):
		return newWorkflowRequestError(http.StatusConflict, "invalid_step_transition", false, "workflow step cannot accept a provider submission", err)
	case errors.Is(err, ErrWorkflowIdentityInvalid):
		return newWorkflowRequestError(http.StatusConflict, "workflow_step_identity_mismatch", false, "workflow step identity does not match the resolved execution", err)
	case errors.Is(err, gorm.ErrRecordNotFound):
		return newWorkflowRequestError(http.StatusNotFound, "workflow_provider_submission_not_found", false, "workflow provider submission not found", err)
	default:
		return workflowRequestErrorForState(err)
	}
}
