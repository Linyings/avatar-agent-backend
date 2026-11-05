package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var workflowRequestIdentityKeys = []string{
	"workflow_run_id",
	"workflow_step_id",
	"workflow_input_hash",
	"client_instance_id",
}

type workflowRequestIdentity struct {
	WorkflowRunID     uuid.UUID
	WorkflowStepID    string
	WorkflowInputHash string
	ClientInstanceID  string
	// Server-derived only; serialize competing versions under the run lock.
	GeneralStepBase string
}

type workflowRequestIdentityFields struct {
	WorkflowRunID     string
	WorkflowStepID    string
	WorkflowInputHash string
	ClientInstanceID  string
	present           map[string]bool
}

type workflowRequestExecution struct {
	Identity *workflowRequestIdentity
	Run      models.WorkflowRun
	Step     models.WorkflowStepExecution
}

func (e workflowRequestExecution) EnrichCreditConsumption(consumption creditConsumption) creditConsumption {
	if e.Identity == nil {
		return consumption
	}
	runID := e.Run.ID
	stepExecutionID := e.Step.ID
	consumption.ProjectID = cloneUUIDPointer(e.Run.ProjectID)
	consumption.WorkflowRunID = &runID
	consumption.WorkflowStepExecutionID = &stepExecutionID
	consumption.ExecutionKey = e.Step.ExecutionKey
	consumption.DubbingTTS = e.Run.ClientType == "pc" && strings.HasPrefix(e.Step.StepID, "dubbing-tts-")
	if e.Run.ClientType == "pc" && strings.HasPrefix(e.Step.StepID, "dubbing-tts-movie-") {
		// A movie narrated with a 搞笑配音 voice is still a movie; it bills at 电影模式 rates.
		consumption.BillingMode = "movie"
	} else if e.Run.ClientType == "pc" && strings.HasPrefix(e.Step.StepID, "dubbing-") {
		// Do not accept dubbing labels on unrelated general-model calls.
		if consumption.ServiceKind == "dubbing_asr" || consumption.ServiceKind == "audio_separation" || consumption.DubbingTTS || (consumption.ServiceKind == "vision" && strings.HasPrefix(e.Step.StepID, "dubbing-cast-")) {
			consumption.BillingMode = "dubbing"
		}
	}
	return consumption
}

type workflowRequestError struct {
	Status    int
	Code      string
	Retryable bool
	Message   string
	cause     error
}

func (e *workflowRequestError) Error() string {
	if e == nil {
		return "workflow request failed"
	}
	return e.Message
}

func (e *workflowRequestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type workflowRequestErrorResponse struct {
	RequestID string `json:"request_id"`
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
	Message   string `json:"message"`
}

func newWorkflowRequestError(status int, code string, retryable bool, message string, cause error) *workflowRequestError {
	return &workflowRequestError{
		Status:    status,
		Code:      code,
		Retryable: retryable,
		Message:   message,
		cause:     cause,
	}
}

func asWorkflowRequestError(err error) (*workflowRequestError, bool) {
	var requestErr *workflowRequestError
	ok := errors.As(err, &requestErr)
	return requestErr, ok
}

func respondWorkflowRequestError(c *gin.Context, err error) {
	requestErr, ok := asWorkflowRequestError(err)
	if !ok {
		requestErr = newWorkflowRequestError(
			http.StatusInternalServerError,
			"workflow_request_error",
			true,
			"workflow request failed",
			err,
		)
	}
	requestID := workflowRequestID(c)
	c.AbortWithStatusJSON(requestErr.Status, workflowRequestErrorResponse{
		RequestID: requestID,
		Code:      requestErr.Code,
		Retryable: requestErr.Retryable,
		Message:   requestErr.Message,
	})
}

func workflowRequestID(c *gin.Context) string {
	const contextKey = "workflow_request_id"
	if existing, ok := c.Get(contextKey); ok {
		if requestID, ok := existing.(string); ok && requestID != "" {
			return requestID
		}
	}
	requestID := uuid.NewString()
	c.Set(contextKey, requestID)
	c.Header("X-Request-ID", requestID)
	return requestID
}

func extractWorkflowRequestIdentityFromMap(payload map[string]any) (*workflowRequestIdentity, error) {
	fields := workflowRequestIdentityFields{present: make(map[string]bool, len(workflowRequestIdentityKeys))}
	var invalid error
	for _, key := range workflowRequestIdentityKeys {
		value, exists := payload[key]
		delete(payload, key)
		if !exists {
			continue
		}
		fields.present[key] = true
		text, ok := value.(string)
		if !ok {
			if invalid == nil {
				invalid = fmt.Errorf("%s must be a string", key)
			}
			continue
		}
		setWorkflowRequestIdentityField(&fields, key, text)
	}
	if invalid != nil {
		return nil, invalidWorkflowRequestIdentityError(invalid)
	}
	return parseWorkflowRequestIdentityFields(fields)
}

func extractWorkflowRequestIdentityFromValues(values url.Values) (*workflowRequestIdentity, error) {
	fields := workflowRequestIdentityFields{present: make(map[string]bool, len(workflowRequestIdentityKeys))}
	var invalid error
	for _, key := range workflowRequestIdentityKeys {
		items, exists := values[key]
		delete(values, key)
		if !exists {
			continue
		}
		fields.present[key] = true
		if len(items) != 1 {
			if invalid == nil {
				invalid = fmt.Errorf("%s must have exactly one value", key)
			}
			continue
		}
		setWorkflowRequestIdentityField(&fields, key, items[0])
	}
	if invalid != nil {
		return nil, invalidWorkflowRequestIdentityError(invalid)
	}
	return parseWorkflowRequestIdentityFields(fields)
}

func parseWorkflowRequestIdentityValues(values url.Values) (*workflowRequestIdentity, error) {
	fields := workflowRequestIdentityFields{present: make(map[string]bool, len(workflowRequestIdentityKeys))}
	for _, key := range workflowRequestIdentityKeys {
		items, exists := values[key]
		if !exists {
			continue
		}
		fields.present[key] = true
		if len(items) != 1 {
			return nil, invalidWorkflowRequestIdentityError(fmt.Errorf("%s must have exactly one value", key))
		}
		setWorkflowRequestIdentityField(&fields, key, items[0])
	}
	return parseWorkflowRequestIdentityFields(fields)
}

func setWorkflowRequestIdentityField(fields *workflowRequestIdentityFields, key, value string) {
	value = strings.TrimSpace(value)
	switch key {
	case "workflow_run_id":
		fields.WorkflowRunID = value
	case "workflow_step_id":
		fields.WorkflowStepID = value
	case "workflow_input_hash":
		fields.WorkflowInputHash = value
	case "client_instance_id":
		fields.ClientInstanceID = value
	}
}

func parseWorkflowRequestIdentityFields(fields workflowRequestIdentityFields) (*workflowRequestIdentity, error) {
	provided := len(fields.present)
	if provided == 0 {
		return nil, nil
	}
	if provided != len(workflowRequestIdentityKeys) {
		return nil, newWorkflowRequestError(
			http.StatusBadRequest,
			"partial_workflow_identity",
			false,
			"workflow_run_id, workflow_step_id, workflow_input_hash, and client_instance_id must be provided together",
			ErrWorkflowIdentityInvalid,
		)
	}

	runID, err := uuid.Parse(fields.WorkflowRunID)
	if err != nil {
		return nil, invalidWorkflowRequestIdentityError(err)
	}
	stepID, err := sanitizeWorkflowStepID(fields.WorkflowStepID, true)
	if err != nil {
		return nil, invalidWorkflowRequestIdentityError(err)
	}
	inputHash, err := sanitizeWorkflowInputHash(fields.WorkflowInputHash)
	if err != nil {
		return nil, invalidWorkflowRequestIdentityError(err)
	}
	clientInstanceID, err := sanitizeWorkflowClientInstanceID(fields.ClientInstanceID, true)
	if err != nil {
		return nil, invalidWorkflowRequestIdentityError(err)
	}
	return &workflowRequestIdentity{
		WorkflowRunID:     runID,
		WorkflowStepID:    stepID,
		WorkflowInputHash: inputHash,
		ClientInstanceID:  clientInstanceID,
	}, nil
}

func invalidWorkflowRequestIdentityError(cause error) *workflowRequestError {
	return newWorkflowRequestError(
		http.StatusBadRequest,
		"invalid_workflow_identity",
		false,
		"workflow identity contains an invalid UUID, unsupported characters, or an unsupported value",
		cause,
	)
}

func (a *App) resolveWorkflowRequestIdentity(
	ctx context.Context,
	userID uuid.UUID,
	requestProjectID *uuid.UUID,
	identity *workflowRequestIdentity,
) (workflowRequestExecution, error) {
	if identity == nil {
		return workflowRequestExecution{}, nil
	}

	var execution workflowRequestExecution
	err := retryWorkflowWrite(ctx, func() error {
		execution = workflowRequestExecution{}
		return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			run, err := loadOwnedWorkflowRun(tx, userID, identity.WorkflowRunID)
			if err != nil {
				return workflowRequestErrorForState(err)
			}
			if requestProjectID != nil && !uuidPointersEqual(run.ProjectID, requestProjectID) {
				return newWorkflowRequestError(http.StatusConflict, "workflow_project_mismatch", false, "workflow run does not belong to the requested project", nil)
			}
			if run.InputHash != identity.WorkflowInputHash {
				return newWorkflowRequestError(http.StatusConflict, "workflow_input_mismatch", false, "workflow input identity does not match the run", nil)
			}
			if isWorkflowRunTerminal(run.Status) || run.Status == workflowRunStatusFinalizing {
				return newWorkflowRequestError(http.StatusConflict, "workflow_run_terminal", false, "workflow run cannot accept billable work", ErrWorkflowRunTerminal)
			}

			now := a.workflowRuns.now()
			if run.ClientInstanceID != identity.ClientInstanceID || run.LeaseExpiresAt == nil || !run.LeaseExpiresAt.After(now) {
				return newWorkflowRequestError(http.StatusConflict, "workflow_run_lease_conflict", false, "workflow run is not leased to this client instance", ErrWorkflowRunLeaseConflict)
			}
			if err := checkGeneralPriorExecutions(tx, run.ID, identity, now); err != nil {
				return err
			}

			executionKey := workflowStepExecutionKey(run.ID, identity.WorkflowStepID, identity.WorkflowInputHash)
			step, err := loadWorkflowRequestStep(tx, run.ID, identity.WorkflowStepID)
			switch {
			case errors.Is(err, gorm.ErrRecordNotFound):
				step = models.WorkflowStepExecution{
					WorkflowRunID: run.ID,
					StepID:        identity.WorkflowStepID,
					InputHash:     identity.WorkflowInputHash,
					ExecutionKey:  executionKey,
					Usage:         datatypes.JSON([]byte("{}")),
				}
				applyWorkflowStepUpdate(&step, WorkflowStepUpdateInput{
					StepID:           identity.WorkflowStepID,
					InputHash:        identity.WorkflowInputHash,
					Status:           workflowRunStatusRunning,
					ClientInstanceID: identity.ClientInstanceID,
				}, now, true)
				if err := tx.Create(&step).Error; err != nil {
					return err
				}
			case err != nil:
				return err
			default:
				if step.InputHash != identity.WorkflowInputHash || step.ExecutionKey != executionKey {
					return newWorkflowRequestError(http.StatusConflict, "workflow_step_identity_mismatch", false, "workflow step identity was already used with different input", nil)
				}
				if err := refreshWorkflowRequestStep(&step, identity.ClientInstanceID, now); err != nil {
					return err
				}
				if err := tx.Save(&step).Error; err != nil {
					return err
				}
			}

			stepID := identity.WorkflowStepID
			run.CurrentStepID = &stepID
			run.LastActiveAt = now
			run.ExpiresAt = now.Add(workflowRunInactivityDuration)
			runLeaseExpiresAt := now.Add(workflowRunLeaseDuration)
			run.LeaseExpiresAt = &runLeaseExpiresAt
			if err := tx.Save(&run).Error; err != nil {
				return err
			}
			if err := a.workflowRuns.runAfterActivityHook(ctx, tx, &run); err != nil {
				return err
			}
			execution = workflowRequestExecution{Identity: identity, Run: run, Step: step}
			return nil
		})
	})
	return execution, err
}

// startWorkflowExecutionHeartbeat keeps a provider-owned child step leased while
// a long-running stream is in progress. The client heartbeat only knows its
// foreground step, which can differ from the provider request step.
func (a *App) startWorkflowExecutionHeartbeat(ctx context.Context, execution workflowRequestExecution, interval time.Duration) func() {
	if execution.Identity == nil || interval <= 0 {
		return func() {}
	}
	stepID := execution.Identity.WorkflowStepID
	refresh := func() {
		_, _ = a.workflowRuns.Heartbeat(ctx, execution.Run.UserID, execution.Identity.WorkflowRunID, WorkflowRunHeartbeatInput{
			ClientInstanceID: execution.Identity.ClientInstanceID,
			CurrentStepID:    &stepID,
		})
	}
	refresh()

	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				refresh()
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

func loadWorkflowRequestStep(tx *gorm.DB, runID uuid.UUID, stepID string) (models.WorkflowStepExecution, error) {
	var step models.WorkflowStepExecution
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("workflow_run_id = ? AND step_id = ?", runID, stepID).
		Order("created_at DESC").
		Take(&step).Error
	return step, err
}

func refreshWorkflowRequestStep(step *models.WorkflowStepExecution, clientInstanceID string, now time.Time) error {
	if step.Status == workflowRunStatusSucceeded {
		return nil
	}
	if step.Status == workflowRunStatusRunning && step.LeaseExpiresAt != nil && step.LeaseExpiresAt.After(now) {
		if step.PreRegistered && workflowStepLeaseOwnedByClient(step, clientInstanceID, now) {
			step.PreRegistered = false
			return nil
		}
		return newWorkflowRequestError(http.StatusConflict, "step_execution_in_progress", true, "workflow step execution is already in progress", ErrWorkflowStepLeaseConflict)
	}
	switch step.Status {
	case workflowRunStatusRunning, workflowRunStatusInterrupted, "failed", workflowRunStatusBlocked:
		applyWorkflowStepUpdate(step, WorkflowStepUpdateInput{
			StepID:           step.StepID,
			InputHash:        step.InputHash,
			Status:           workflowRunStatusRunning,
			ClientInstanceID: clientInstanceID,
		}, now, false)
		return nil
	default:
		return newWorkflowRequestError(http.StatusConflict, "invalid_step_transition", false, "workflow step status cannot start billable work", ErrWorkflowStepTransitionInvalid)
	}
}

func workflowRequestErrorForState(err error) error {
	switch {
	case errors.Is(err, ErrWorkflowRunNotFound):
		return newWorkflowRequestError(http.StatusNotFound, "workflow_run_not_found", false, "workflow run not found", err)
	case errors.Is(err, ErrWorkflowRunLeaseConflict):
		return newWorkflowRequestError(http.StatusConflict, "workflow_run_lease_conflict", false, "workflow run is not leased to this client instance", err)
	case errors.Is(err, ErrWorkflowStepLeaseConflict):
		return newWorkflowRequestError(http.StatusConflict, "step_execution_in_progress", true, "workflow step is active in another client instance", err)
	case errors.Is(err, ErrWorkflowRunTerminal):
		return newWorkflowRequestError(http.StatusConflict, "workflow_run_terminal", false, "workflow run cannot accept billable work", err)
	default:
		return err
	}
}

func cloneUUIDPointer(value *uuid.UUID) *uuid.UUID {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
