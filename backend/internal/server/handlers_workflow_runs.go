package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type workflowRunAPIResponse struct {
	models.WorkflowRun
	Steps            []models.WorkflowStepExecution `json:"steps,omitempty"`
	CanResume        bool                           `json:"can_resume"`
	ExpiresInSeconds int64                          `json:"expires_in_seconds"`
}

func (a *App) createWorkflowRun(c *gin.Context) {
	user := getUser(c)
	var req struct {
		ClientType                 string  `json:"client_type"`
		ProjectID                  *string `json:"project_id"`
		ClientTaskID               *string `json:"client_task_id"`
		ClientProjectName          *string `json:"client_project_name"`
		InputHash                  string  `json:"input_hash"`
		ClientInstanceID           string  `json:"client_instance_id"`
		InheritReservationsFromRun *string `json:"inherit_reservations_from_run_id"`
	}
	if !bindJSON(c, &req) {
		return
	}

	clientType := strings.ToLower(strings.TrimSpace(req.ClientType))
	inputHash, err := sanitizeWorkflowInputHash(req.InputHash)
	if errors.Is(err, ErrWorkflowIdentityInvalid) && req.InputHash == "" {
		workflowRunFail(c, http.StatusBadRequest, "input_hash_required", "input_hash is required")
		return
	}
	if err != nil {
		a.respondWorkflowRunError(c, err)
		return
	}
	clientInstanceID, err := sanitizeWorkflowClientInstanceID(req.ClientInstanceID, false)
	if err != nil {
		a.respondWorkflowRunError(c, err)
		return
	}

	input := CreateWorkflowRunInput{
		UserID:           user.ID,
		ClientType:       clientType,
		InputHash:        inputHash,
		ClientInstanceID: clientInstanceID,
	}
	switch clientType {
	case "web":
		if req.ClientProjectName != nil && strings.TrimSpace(*req.ClientProjectName) != "" {
			workflowRunFail(c, http.StatusBadRequest, "invalid_web_scope", "web workflow runs cannot use client_project_name")
			return
		}
		if req.ProjectID == nil || strings.TrimSpace(*req.ProjectID) == "" {
			workflowRunFail(c, http.StatusBadRequest, "project_id_required", "web workflow runs require project_id")
			return
		}
		projectID, err := uuid.Parse(strings.TrimSpace(*req.ProjectID))
		if err != nil {
			workflowRunFail(c, http.StatusBadRequest, "invalid_project_id", "invalid project_id")
			return
		}
		if req.ClientTaskID != nil && strings.TrimSpace(*req.ClientTaskID) != "" {
			workflowRunFail(c, http.StatusBadRequest, "invalid_web_scope", "web workflow runs cannot use client_task_id")
			return
		}
		if req.InheritReservationsFromRun != nil && strings.TrimSpace(*req.InheritReservationsFromRun) != "" {
			workflowRunFail(c, http.StatusBadRequest, "invalid_web_scope", "web workflow runs cannot inherit reservations")
			return
		}
		input.ProjectID = &projectID
	case "pc":
		if req.ProjectID != nil && strings.TrimSpace(*req.ProjectID) != "" {
			workflowRunFail(c, http.StatusBadRequest, "invalid_pc_scope", "pc workflow runs cannot use project_id")
			return
		}
		if req.ClientTaskID == nil || *req.ClientTaskID == "" {
			workflowRunFail(c, http.StatusBadRequest, "client_task_id_required", "pc workflow runs require client_task_id")
			return
		}
		clientTaskID, err := sanitizeWorkflowClientTaskID(*req.ClientTaskID, true)
		if err != nil {
			a.respondWorkflowRunError(c, err)
			return
		}
		input.ClientTaskID = &clientTaskID
		if req.ClientProjectName == nil {
			workflowRunFail(c, http.StatusBadRequest, "client_project_name_required", "pc workflow runs require client_project_name")
			return
		}
		clientProjectName, err := sanitizeWorkflowClientProjectName(*req.ClientProjectName)
		if err != nil {
			a.respondWorkflowRunError(c, err)
			return
		}
		input.ClientProjectName = clientProjectName
		if req.InheritReservationsFromRun != nil && strings.TrimSpace(*req.InheritReservationsFromRun) != "" {
			inheritedRunID, err := uuid.Parse(strings.TrimSpace(*req.InheritReservationsFromRun))
			if err != nil {
				workflowRunFail(c, http.StatusBadRequest, "invalid_inherit_reservations_from_run_id", "invalid inherit_reservations_from_run_id")
				return
			}
			input.InheritReservationsFromRunID = &inheritedRunID
		}
	default:
		workflowRunFail(c, http.StatusBadRequest, "invalid_client_type", "client_type must be web or pc")
		return
	}

	result, err := a.workflowRuns.Create(c.Request.Context(), input)
	if err != nil {
		a.respondWorkflowRunError(c, err)
		return
	}
	response := newWorkflowRunAPIResponse(result.Run, nil)
	if result.Reused {
		ok(c, response)
		return
	}
	created(c, response)
}

type pcWorkflowProjectNameSyncResult struct {
	Matched              int64 `json:"matched"`
	WorkflowRunsUpdated  int64 `json:"workflow_runs_updated"`
	LedgerRecordsUpdated int64 `json:"ledger_records_updated"`
}

func (a *App) syncPCWorkflowProjectNames(c *gin.Context) {
	user := getUser(c)
	var req struct {
		Items []struct {
			ClientTaskID string `json:"client_task_id"`
			ProjectName  string `json:"project_name"`
		} `json:"items"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if len(req.Items) > 100 {
		workflowRunFail(c, http.StatusBadRequest, "invalid_pc_project_name_sync", "items must contain at most 100 mappings")
		return
	}

	projectNameByTaskID := make(map[string]string, len(req.Items))
	taskIDs := make([]string, 0, len(req.Items))
	for _, item := range req.Items {
		clientTaskID, err := sanitizeWorkflowClientTaskID(item.ClientTaskID, true)
		if err != nil {
			a.respondWorkflowRunError(c, err)
			return
		}
		projectName, err := sanitizeWorkflowClientProjectName(item.ProjectName)
		if err != nil {
			a.respondWorkflowRunError(c, err)
			return
		}
		if _, duplicate := projectNameByTaskID[clientTaskID]; duplicate {
			workflowRunFail(c, http.StatusBadRequest, "invalid_pc_project_name_sync", "client_task_id mappings must be unique")
			return
		}
		projectNameByTaskID[clientTaskID] = projectName
		taskIDs = append(taskIDs, clientTaskID)
	}

	result := pcWorkflowProjectNameSyncResult{}
	err := a.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if len(taskIDs) == 0 {
			return nil
		}
		var runs []models.WorkflowRun
		if err := tx.Where("user_id = ? AND client_type = ? AND client_task_id IN ?", user.ID, "pc", taskIDs).Find(&runs).Error; err != nil {
			return err
		}
		matchedTaskIDs := make(map[string]struct{}, len(runs))
		projectNameByRunID := make(map[string]string, len(runs))
		for index := range runs {
			if runs[index].ClientTaskID == nil {
				continue
			}
			requestedName, requested := projectNameByTaskID[*runs[index].ClientTaskID]
			if !requested {
				continue
			}
			matchedTaskIDs[*runs[index].ClientTaskID] = struct{}{}
			projectName := runs[index].ClientProjectName
			if projectName == "" {
				updated := tx.Model(&models.WorkflowRun{}).
					Where("id = ? AND client_project_name = ?", runs[index].ID, "").
					Update("client_project_name", requestedName)
				if updated.Error != nil {
					return updated.Error
				}
				result.WorkflowRunsUpdated += updated.RowsAffected
				projectName = requestedName
			}
			projectNameByRunID[runs[index].ID.String()] = projectName
		}
		result.Matched = int64(len(matchedTaskIDs))
		if len(projectNameByRunID) == 0 {
			return nil
		}

		var ledgers []models.CreditLedgerRecord
		if err := tx.Where("user_id = ? AND event_type = ? AND project_name = ?", user.ID, creditLedgerEventConsume, "").Find(&ledgers).Error; err != nil {
			return err
		}
		for index := range ledgers {
			var metadata struct {
				WorkflowRunID string `json:"workflow_run_id"`
			}
			if err := json.Unmarshal(ledgers[index].RequestMetadata, &metadata); err != nil || metadata.WorkflowRunID == "" {
				continue
			}
			projectName, matched := projectNameByRunID[metadata.WorkflowRunID]
			if !matched || projectName == "" {
				continue
			}
			updated := tx.Model(&models.CreditLedgerRecord{}).
				Where("id = ? AND project_name = ?", ledgers[index].ID, "").
				Update("project_name", projectName)
			if updated.Error != nil {
				return updated.Error
			}
			result.LedgerRecordsUpdated += updated.RowsAffected
		}
		return nil
	})
	if err != nil {
		workflowRunFail(c, http.StatusInternalServerError, "pc_project_name_sync_failed", "failed to synchronize PC project names")
		return
	}
	ok(c, result)
}

func (a *App) getWorkflowRun(c *gin.Context) {
	user := getUser(c)
	runID, okID := workflowRunIDParam(c)
	if !okID {
		return
	}
	run, steps, err := a.workflowRuns.Get(c.Request.Context(), user.ID, runID)
	if err != nil {
		a.respondWorkflowRunError(c, err)
		return
	}
	ok(c, newWorkflowRunAPIResponse(run, steps))
}

func (a *App) heartbeatWorkflowRun(c *gin.Context) {
	user := getUser(c)
	runID, okID := workflowRunIDParam(c)
	if !okID {
		return
	}
	var req struct {
		ClientInstanceID string  `json:"client_instance_id"`
		CurrentStepID    *string `json:"current_step_id"`
	}
	if !bindJSON(c, &req) {
		return
	}
	clientInstanceID, err := sanitizeWorkflowClientInstanceID(req.ClientInstanceID, true)
	if errors.Is(err, ErrWorkflowIdentityInvalid) && req.ClientInstanceID == "" {
		workflowRunFail(c, http.StatusBadRequest, "client_instance_id_required", "client_instance_id is required")
		return
	}
	if err != nil {
		a.respondWorkflowRunError(c, err)
		return
	}
	var currentStepID *string
	if req.CurrentStepID != nil && *req.CurrentStepID != "" {
		value, err := sanitizeWorkflowStepID(*req.CurrentStepID, false)
		if err != nil {
			a.respondWorkflowRunError(c, err)
			return
		}
		currentStepID = &value
	}
	run, err := a.workflowRuns.Heartbeat(c.Request.Context(), user.ID, runID, WorkflowRunHeartbeatInput{
		ClientInstanceID: clientInstanceID,
		CurrentStepID:    currentStepID,
	})
	if err != nil {
		a.respondWorkflowRunError(c, err)
		return
	}
	ok(c, newWorkflowRunAPIResponse(run, nil))
}

func (a *App) resumeWorkflowRun(c *gin.Context) {
	user := getUser(c)
	runID, okID := workflowRunIDParam(c)
	if !okID {
		return
	}
	var req struct {
		ClientInstanceID string `json:"client_instance_id"`
	}
	if !bindJSON(c, &req) {
		return
	}
	clientInstanceID, err := sanitizeWorkflowClientInstanceID(req.ClientInstanceID, true)
	if errors.Is(err, ErrWorkflowIdentityInvalid) && req.ClientInstanceID == "" {
		workflowRunFail(c, http.StatusBadRequest, "client_instance_id_required", "client_instance_id is required")
		return
	}
	if err != nil {
		a.respondWorkflowRunError(c, err)
		return
	}
	run, err := a.workflowRuns.Resume(c.Request.Context(), user.ID, runID, clientInstanceID)
	if err != nil {
		a.respondWorkflowRunError(c, err)
		return
	}
	ok(c, newWorkflowRunAPIResponse(run, nil))
}

func (a *App) interruptWorkflowRun(c *gin.Context) {
	user := getUser(c)
	runID, okID := workflowRunIDParam(c)
	if !okID {
		return
	}
	var req struct {
		ClientInstanceID string  `json:"client_instance_id"`
		ErrorCode        *string `json:"error_code"`
		ErrorMessage     *string `json:"error_message"`
	}
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		workflowRunFail(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	clientInstanceID, err := sanitizeWorkflowClientInstanceID(req.ClientInstanceID, true)
	if errors.Is(err, ErrWorkflowIdentityInvalid) && req.ClientInstanceID == "" {
		workflowRunFail(c, http.StatusBadRequest, "client_instance_id_required", "client_instance_id is required")
		return
	}
	if err != nil {
		a.respondWorkflowRunError(c, err)
		return
	}
	run, err := a.workflowRuns.Interrupt(c.Request.Context(), user.ID, runID, WorkflowRunInterruptInput{
		ClientInstanceID: clientInstanceID,
		ErrorCode:        trimOptionalString(req.ErrorCode),
		ErrorMessage:     trimOptionalString(req.ErrorMessage),
	})
	if err != nil {
		a.respondWorkflowRunError(c, err)
		return
	}
	ok(c, newWorkflowRunAPIResponse(run, nil))
}

func (a *App) completeWorkflowRun(c *gin.Context) {
	user := getUser(c)
	runID, okID := workflowRunIDParam(c)
	if !okID {
		return
	}
	var req struct {
		FinalResultAt *time.Time `json:"final_result_at"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if req.FinalResultAt == nil || req.FinalResultAt.IsZero() {
		workflowRunFail(c, http.StatusBadRequest, "final_result_at_required", "final_result_at is required")
		return
	}
	run, err := a.workflowRuns.Complete(c.Request.Context(), user.ID, runID, *req.FinalResultAt)
	if err != nil {
		a.respondWorkflowRunError(c, err)
		return
	}
	ok(c, newWorkflowRunAPIResponse(run, nil))
}

func (a *App) cancelWorkflowRun(c *gin.Context) {
	user := getUser(c)
	runID, okID := workflowRunIDParam(c)
	if !okID {
		return
	}
	run, err := a.workflowRuns.Cancel(c.Request.Context(), user.ID, runID)
	if err != nil {
		a.respondWorkflowRunError(c, err)
		return
	}
	ok(c, newWorkflowRunAPIResponse(run, nil))
}

func (a *App) putWorkflowStepExecution(c *gin.Context) {
	user := getUser(c)
	runID, okID := workflowRunIDParam(c)
	if !okID {
		return
	}
	stepID, err := sanitizeWorkflowStepID(c.Param("step_id"), true)
	if errors.Is(err, ErrWorkflowIdentityInvalid) && c.Param("step_id") == "" {
		workflowRunFail(c, http.StatusBadRequest, "step_id_required", "step_id is required")
		return
	}
	if err != nil {
		a.respondWorkflowRunError(c, err)
		return
	}
	var req struct {
		InputHash         string          `json:"input_hash"`
		Status            string          `json:"status"`
		ClientInstanceID  string          `json:"client_instance_id"`
		PreRegistered     bool            `json:"pre_registered"`
		ProviderRequestID *string         `json:"provider_request_id"`
		ErrorCode         *string         `json:"error_code"`
		ErrorMessage      *string         `json:"error_message"`
		Usage             json.RawMessage `json:"usage"`
	}
	if !bindJSON(c, &req) {
		return
	}
	inputHash, err := sanitizeWorkflowInputHash(req.InputHash)
	status := strings.ToLower(strings.TrimSpace(req.Status))
	if errors.Is(err, ErrWorkflowIdentityInvalid) && req.InputHash == "" {
		workflowRunFail(c, http.StatusBadRequest, "input_hash_required", "input_hash is required")
		return
	}
	if err != nil {
		a.respondWorkflowRunError(c, err)
		return
	}
	if !validWorkflowStepStatus(status) {
		workflowRunFail(c, http.StatusBadRequest, "invalid_step_status", "invalid workflow step status")
		return
	}
	clientInstanceID, err := sanitizeWorkflowClientInstanceID(req.ClientInstanceID, true)
	if errors.Is(err, ErrWorkflowIdentityInvalid) && req.ClientInstanceID == "" {
		workflowRunFail(c, http.StatusBadRequest, "client_instance_id_required", "workflow step mutations require client_instance_id")
		return
	}
	if err != nil {
		a.respondWorkflowRunError(c, err)
		return
	}
	step, err := a.workflowRuns.UpsertStep(c.Request.Context(), user.ID, runID, WorkflowStepUpdateInput{
		StepID:            stepID,
		InputHash:         inputHash,
		Status:            status,
		ClientInstanceID:  clientInstanceID,
		PreRegistered:     req.PreRegistered,
		ProviderRequestID: trimOptionalString(req.ProviderRequestID),
		ErrorCode:         trimOptionalString(req.ErrorCode),
		ErrorMessage:      trimOptionalString(req.ErrorMessage),
		Usage:             datatypes.JSON(req.Usage),
	})
	if err != nil {
		a.respondWorkflowRunError(c, err)
		return
	}
	ok(c, step)
}

func newWorkflowRunAPIResponse(run models.WorkflowRun, steps []models.WorkflowStepExecution) workflowRunAPIResponse {
	expiresIn := int64(time.Until(run.ExpiresAt).Seconds())
	if expiresIn < 0 {
		expiresIn = 0
	}
	return workflowRunAPIResponse{
		WorkflowRun:      run,
		Steps:            steps,
		CanResume:        run.Status == workflowRunStatusRunning || run.Status == workflowRunStatusInterrupted || run.Status == workflowRunStatusBlocked,
		ExpiresInSeconds: expiresIn,
	}
}

func workflowRunIDParam(c *gin.Context) (uuid.UUID, bool) {
	runID, err := uuid.Parse(strings.TrimSpace(c.Param("run_id")))
	if err != nil {
		workflowRunFail(c, http.StatusNotFound, "workflow_run_not_found", "workflow run not found")
		return uuid.Nil, false
	}
	return runID, true
}

func (a *App) respondWorkflowRunError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrWorkflowRunNotFound):
		workflowRunFail(c, http.StatusNotFound, "workflow_run_not_found", "workflow run not found")
	case errors.Is(err, ErrWorkflowRunScopeConflict):
		workflowRunFail(c, http.StatusConflict, "active_workflow_input_conflict", "an active workflow run exists with a different input_hash")
	case errors.Is(err, ErrWorkflowRunLeaseConflict):
		workflowRunFail(c, http.StatusConflict, "workflow_run_lease_conflict", "workflow run is active in another client instance")
	case errors.Is(err, ErrWorkflowStepLeaseConflict):
		workflowRunFail(c, http.StatusConflict, "step_execution_in_progress", "workflow step is active in another client instance")
	case errors.Is(err, ErrWorkflowStepTransitionInvalid):
		workflowRunFail(c, http.StatusConflict, "invalid_step_transition", "workflow step transition is not allowed")
	case errors.Is(err, ErrWorkflowIdentityInvalid):
		workflowRunFail(c, http.StatusBadRequest, "invalid_workflow_identity", "workflow identity contains unsupported characters or exceeds its length limit")
	case errors.Is(err, ErrWorkflowMetadataInvalid):
		workflowRunFail(c, http.StatusBadRequest, "invalid_workflow_metadata", "workflow metadata contains unsupported fields or values")
	case errors.Is(err, ErrWorkflowFinalResultExists):
		workflowRunFail(c, http.StatusConflict, "final_result_exists", "workflow run already has a final result")
	case errors.Is(err, ErrWorkflowRunTerminal):
		workflowRunFail(c, http.StatusConflict, "workflow_run_terminal", "workflow run is already terminal")
	case errors.Is(err, ErrWorkflowReservationInheritanceInvalid):
		workflowRunFail(c, http.StatusBadRequest, "invalid_reservation_inheritance", "cannot inherit credit reservations from the given workflow run")
	default:
		workflowRunFail(c, http.StatusInternalServerError, "workflow_run_error", "workflow run operation failed")
	}
}

func workflowRunFail(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{
		"code":    code,
		"error":   http.StatusText(status),
		"message": message,
		"success": false,
	})
}

func trimOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func validWorkflowStepStatus(status string) bool {
	switch status {
	case workflowRunStatusRunning, workflowRunStatusSucceeded, workflowRunStatusInterrupted, "failed", workflowRunStatusBlocked:
		return true
	default:
		return false
	}
}
