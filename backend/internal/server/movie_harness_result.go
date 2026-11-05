package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
)

const movieHarnessMaxResultBytes = 8 << 20

func movieHarnessUnknownResult() *workflowRequestError {
	return newWorkflowRequestError(http.StatusConflict, "workflow_result_unknown", false, "调用结果尚不能确认，请保留当前进度；不能自动重复提交付费请求。", nil)
}

func movieHarnessRunningResult() *workflowRequestError {
	return newWorkflowRequestError(http.StatusConflict, "workflow_step_running", true, "模型请求仍在执行，请查询原请求结果。", nil)
}

func (a *App) loadMovieHarnessResult(ctx context.Context, userID uuid.UUID, projectID *uuid.UUID, identity *workflowRequestIdentity) (models.WorkflowRun, models.WorkflowStepExecution, error) {
	var run models.WorkflowRun
	if err := a.db.WithContext(ctx).Where("id = ? AND user_id = ?", identity.WorkflowRunID, userID).Take(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return run, models.WorkflowStepExecution{}, workflowRequestErrorForState(ErrWorkflowRunNotFound)
		}
		return run, models.WorkflowStepExecution{}, err
	}
	if run.ClientInstanceID != identity.ClientInstanceID {
		return run, models.WorkflowStepExecution{}, newWorkflowRequestError(http.StatusForbidden, "workflow_client_mismatch", false, "workflow result belongs to another client instance", nil)
	}
	if run.InputHash != identity.WorkflowInputHash || (projectID != nil && !uuidPointersEqual(run.ProjectID, projectID)) {
		return run, models.WorkflowStepExecution{}, newWorkflowRequestError(http.StatusConflict, "workflow_input_mismatch", false, "workflow result identity does not match", nil)
	}
	var step models.WorkflowStepExecution
	err := a.db.WithContext(ctx).Where("workflow_run_id = ? AND step_id = ?", run.ID, identity.WorkflowStepID).Order("created_at DESC").Take(&step).Error
	if err == nil && (step.InputHash != identity.WorkflowInputHash || step.ExecutionKey != workflowStepExecutionKey(run.ID, identity.WorkflowStepID, identity.WorkflowInputHash)) {
		err = newWorkflowRequestError(http.StatusConflict, "workflow_input_mismatch", false, "workflow result identity does not match", nil)
	}
	return run, step, err
}

func decodeMovieHarnessResult(step models.WorkflowStepExecution) (map[string]any, error) {
	var result map[string]any
	if step.Status != workflowRunStatusSucceeded || len(step.Result) == 0 || json.Unmarshal(step.Result, &result) != nil || result == nil {
		return nil, movieHarnessUnknownResult()
	}
	return result, nil
}

func (a *App) validateMovieHarnessReplay(ctx context.Context, run models.WorkflowRun, step models.WorkflowStepExecution, fingerprint string) error {
	var reservation models.CreditReservation
	if err := a.db.WithContext(ctx).Where("workflow_step_execution_id = ? AND workflow_run_id = ?", step.ID, run.ID).Take(&reservation).Error; err != nil {
		return fmt.Errorf("load replay reservation: %w", err)
	}
	if reservation.UserID != run.UserID || !uuidPointersEqual(reservation.ProjectID, run.ProjectID) || (reservation.Status != creditReservationPending && reservation.Status != creditReservationConfirmed) {
		return fmt.Errorf("workflow replay reservation is invalid")
	}
	var metadata map[string]any
	if err := json.Unmarshal(reservation.RequestMetadata, &metadata); err != nil {
		return err
	}
	if workflowInputFingerprintFromMetadata(metadata) != fingerprint {
		return newWorkflowRequestError(http.StatusConflict, "workflow_input_mismatch", false, "workflow request body changed", nil)
	}
	return nil
}

// Check for completed/unknown work before acquiring a new lease or calling a provider.
func (a *App) resolveMovieHarnessExecution(c *gin.Context, userID uuid.UUID, projectID *uuid.UUID, identity *workflowRequestIdentity, fingerprint, kind string, providerConfigured bool) (workflowRequestExecution, bool) {
	if identity == nil {
		if !providerConfigured {
			fail(c, http.StatusServiceUnavailable, "OpenAI 兼容模型配置未配置")
			return workflowRequestExecution{}, false
		}
		return workflowRequestExecution{}, true
	}
	run, step, err := a.loadMovieHarnessResult(c.Request.Context(), userID, projectID, identity)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		respondWorkflowRequestError(c, err)
		return workflowRequestExecution{}, false
	}
	if err == nil {
		if valueOrEmpty(step.ErrorCode) == "workflow_result_unknown" {
			respondWorkflowRequestError(c, movieHarnessUnknownResult())
			return workflowRequestExecution{}, false
		}
		if step.ProviderInputHash != nil && *step.ProviderInputHash != fingerprint {
			respondWorkflowRequestError(c, newWorkflowRequestError(http.StatusConflict, "workflow_input_mismatch", false, "workflow request body changed", nil))
			return workflowRequestExecution{}, false
		}
		if step.Status == workflowRunStatusSucceeded {
			if len(step.Result) > 0 {
				if replayErr := a.validateMovieHarnessReplay(c.Request.Context(), run, step, fingerprint); replayErr != nil {
					respondWorkflowRequestError(c, replayErr)
					return workflowRequestExecution{}, false
				}
			}
			result, resultErr := decodeMovieHarnessResult(step)
			if resultErr != nil {
				respondWorkflowRequestError(c, resultErr)
				return workflowRequestExecution{}, false
			}
			emitter := beginSerializedToolEventStream(c, kind, "正在恢复已完成的结果")
			emitter.write("completed", result)
			return workflowRequestExecution{}, false
		}
		if step.ProviderSubmissionClaimID != nil {
			if step.Status == workflowRunStatusRunning && step.LeaseExpiresAt != nil && step.LeaseExpiresAt.After(a.workflowRuns.now()) {
				respondWorkflowRequestError(c, movieHarnessRunningResult())
			} else {
				respondWorkflowRequestError(c, movieHarnessUnknownResult())
			}
			return workflowRequestExecution{}, false
		}
	}
	if !providerConfigured {
		fail(c, http.StatusServiceUnavailable, "OpenAI 兼容模型配置未配置")
		return workflowRequestExecution{}, false
	}
	execution, err := a.resolveWorkflowRequestIdentity(c.Request.Context(), userID, projectID, identity)
	if err != nil {
		if requestErr, ok := asWorkflowRequestError(err); ok && requestErr.Code == "step_execution_in_progress" {
			err = movieHarnessRunningResult()
		}
		respondWorkflowRequestError(c, err)
		return workflowRequestExecution{}, false
	}
	// A concurrent request can finish between the preflight read and resolution.
	if execution.Step.Status == workflowRunStatusSucceeded {
		if execution.Step.ProviderInputHash != nil && *execution.Step.ProviderInputHash != fingerprint {
			respondWorkflowRequestError(c, newWorkflowRequestError(http.StatusConflict, "workflow_input_mismatch", false, "workflow request body changed", nil))
			return workflowRequestExecution{}, false
		}
		if len(execution.Step.Result) > 0 {
			if replayErr := a.validateMovieHarnessReplay(c.Request.Context(), execution.Run, execution.Step, fingerprint); replayErr != nil {
				respondWorkflowRequestError(c, replayErr)
				return workflowRequestExecution{}, false
			}
		}
		result, resultErr := decodeMovieHarnessResult(execution.Step)
		if resultErr != nil {
			respondWorkflowRequestError(c, resultErr)
			return workflowRequestExecution{}, false
		}
		emitter := beginSerializedToolEventStream(c, kind, "正在恢复已完成的结果")
		emitter.write("completed", result)
		return workflowRequestExecution{}, false
	}
	ctx, cancel := workflowProviderFinalizationContext(c.Request.Context())
	defer cancel()
	err = retryWorkflowWrite(ctx, func() error {
		return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			_, current, err := lockResolvedWorkflowProviderExecution(tx, execution, a.workflowRuns.now())
			if err != nil {
				return err
			}
			if current.ProviderSubmissionClaimID != nil {
				return movieHarnessUnknownResult()
			}
			if current.ProviderInputHash != nil && *current.ProviderInputHash != fingerprint {
				return newWorkflowRequestError(http.StatusConflict, "workflow_input_mismatch", false, "workflow request body changed", nil)
			}
			claim := uuid.NewString()
			now := a.workflowRuns.now()
			current.ResultVersion = 1
			current.ProviderInputHash = &fingerprint
			current.ProviderSubmissionClaimID = &claim
			current.ProviderSubmissionClaimedAt = &now
			if err := tx.Save(&current).Error; err != nil {
				return err
			}
			execution.Step = current
			return nil
		})
	})
	if err != nil {
		respondWorkflowRequestError(c, err)
		return workflowRequestExecution{}, false
	}
	return execution, true
}

// Results remain readable after interruption/expiry without reopening billable work.
func (a *App) getMovieHarnessResult(c *gin.Context) {
	user := getUser(c)
	identity, err := extractWorkflowRequestIdentityFromMap(map[string]any{
		"workflow_run_id": c.Param("run_id"), "workflow_step_id": c.Param("step_id"),
		"workflow_input_hash": c.Query("input_hash"), "client_instance_id": c.Query("client_instance_id"),
	})
	if err != nil {
		respondWorkflowRequestError(c, err)
		return
	}
	_, step, err := a.loadMovieHarnessResult(c.Request.Context(), user.ID, nil, identity)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		ok(c, gin.H{"status": "not_started", "retryable": true})
		return
	}
	if err != nil {
		respondWorkflowRequestError(c, err)
		return
	}
	if step.Status == workflowRunStatusSucceeded {
		result, resultErr := decodeMovieHarnessResult(step)
		if resultErr != nil {
			ok(c, gin.H{"status": "unknown", "code": "workflow_result_unknown", "message": resultErr.Error(), "retryable": false})
			return
		}
		ok(c, gin.H{"status": "succeeded", "result": result, "retryable": false})
		return
	}
	if valueOrEmpty(step.ErrorCode) == "workflow_result_unknown" || (step.ProviderSubmissionClaimID != nil && (step.Status != workflowRunStatusRunning || step.LeaseExpiresAt == nil || !step.LeaseExpiresAt.After(a.workflowRuns.now()))) {
		// Billing is only reserved once the provider reported usage. With no reservation,
		// nothing was charged and no stored result exists, so the client may start over.
		var reservations int64
		if err := a.db.WithContext(c.Request.Context()).Model(&models.CreditReservation{}).Where("workflow_step_execution_id = ?", step.ID).Count(&reservations).Error; err != nil {
			respondWorkflowRequestError(c, err)
			return
		}
		ok(c, gin.H{"status": "unknown", "code": "workflow_result_unknown", "message": movieHarnessUnknownResult().Message, "retryable": false,
			"billed": reservations > 0, "provider_request_recorded": step.ProviderRequestID != nil})
		return
	}
	if step.Status == workflowRunStatusRunning {
		ok(c, gin.H{"status": "running", "code": "workflow_step_running", "retryable": true})
		return
	}
	// A preflight credit rejection never reached the model. Existing desktop clients
	// resume not_started work, but otherwise keep replaying the old blocked error.
	// Require no provider claim, request, stored answer or reservation before retrying.
	if step.Status == workflowRunStatusBlocked && valueOrEmpty(step.ErrorCode) == "insufficient_credits" &&
		step.ProviderSubmissionClaimID == nil && step.ProviderRequestID == nil && len(step.Result) == 0 {
		var reservations int64
		if err := a.db.WithContext(c.Request.Context()).Model(&models.CreditReservation{}).Where("workflow_step_execution_id = ?", step.ID).Count(&reservations).Error; err != nil {
			respondWorkflowRequestError(c, err)
			return
		}
		if reservations == 0 {
			ok(c, gin.H{"status": "not_started", "retryable": true})
			return
		}
	}
	retryable := step.Status == workflowStepStatusFailed || step.Status == workflowRunStatusInterrupted
	code, message := valueOrEmpty(step.ErrorCode), valueOrEmpty(step.ErrorMessage)
	if strings.TrimSpace(message) == "" {
		message = "工作流步骤尚未完成。"
	}
	ok(c, gin.H{"status": step.Status, "code": code, "message": message, "retryable": retryable})
}
