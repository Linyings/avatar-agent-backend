package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const (
	workflowStepVideoSource = "video-source"
	workflowStepSubtitles   = "subtitles"
	workflowStepConfig      = "config"
	workflowStepScript      = "script"
	workflowStepVoiceover   = "voiceover"
)

func (a *App) getProjectWorkflow(c *gin.Context) {
	project, okProject := a.ownedProject(c)
	if !okProject {
		return
	}
	workflow := normalizeFrameWorkflow(project.Workflow, project.Status)
	legacy := a.projectWorkflowResponse(project)
	for key, value := range legacy {
		workflow[key] = value
	}
	workflow["workflow_revision"] = a.currentProjectWorkflowRevision(project.ID)
	workflow["workflow_root"] = a.projectWorkflowRootValue(project.ID)
	ok(c, workflow)
}

func (a *App) putProjectWorkflowStep(step string) gin.HandlerFunc {
	return func(c *gin.Context) {
		project, okProject := a.ownedProject(c)
		if !okProject {
			return
		}
		var req map[string]any
		if !bindJSON(c, &req) {
			return
		}
		if len(req) == 0 {
			fail(c, http.StatusBadRequest, "工作流数据不能为空")
			return
		}
		input, okInput := workflowSaveInput(project.ID, step, req)
		if !okInput {
			fail(c, http.StatusNotFound, "工作流步骤不存在")
			return
		}
		if input.ExpectedWorkflowRevision == nil {
			fail(c, http.StatusBadRequest, "workflow_revision不能为空")
			return
		}
		result, err := a.saveWorkflowStep(c, step, input)
		if err != nil {
			if IsWorkflowConflict(err) {
				_ = a.recordWorkflowEvent(project.ID, routeWorkflowStepOrUnknown(step), uuid.Nil, 0, "workflow_conflict", err.Error(), gin.H{"route_step": step})
				fail(c, http.StatusConflict, err.Error())
				return
			}
			fail(c, http.StatusInternalServerError, "保存工作流步骤失败")
			return
		}
		if err := a.db.First(&project, "id = ?", project.ID).Error; err != nil {
			fail(c, http.StatusInternalServerError, "读取工作流步骤失败")
			return
		}
		response := a.projectWorkflowStepResponse(project, step)
		response["workflow_revision"] = a.currentProjectWorkflowRevision(project.ID)
		response["content_hash"] = result.ContentHash
		response["dependency_hash"] = result.DependencyHash
		response["payload_hash"] = result.InputHash
		response["input_hash"] = result.InputHash
		response["step_id"] = result.StepID.String()
		response["idempotent"] = result.Idempotent
		ok(c, response)
	}
}

func (a *App) putProjectWorkflowStepParam(c *gin.Context) {
	step := c.Param("step")
	if _, ok := routeWorkflowStep(step); !ok {
		fail(c, http.StatusNotFound, "工作流步骤不存在")
		return
	}
	a.putProjectWorkflowStep(step)(c)
}

func workflowSaveInput(projectID uuid.UUID, step string, req map[string]any) (WorkflowSaveInput, bool) {
	payload := workflowStepPayload(req, step)
	input := WorkflowSaveInput{
		ProjectID:                projectID,
		ExpectedWorkflowRevision: optionalInt(req["workflow_revision"], req["revision"]),
		ExpectedDependencyHash:   stringValue(req["dependency_hash"]),
		Payload:                  payload,
		Status:                   stringValue(req["status"]),
	}
	switch step {
	case workflowStepVideoSource:
		if url, ok := firstOptionalStringFromAny(payload, "video_url", "url", "source_url"); ok {
			input.LegacyCache.VideoURL = url
			input.LegacyCache.VideoURLSet = true
		}
	case workflowStepVoiceover:
		if url, ok := firstOptionalStringFromAny(payload, "audio_url", "mp3_url", "voiceover_url", "url"); ok {
			input.LegacyCache.AudioURL = url
			input.LegacyCache.AudioURLSet = true
		}
	case workflowStepSubtitles, workflowStepConfig, workflowStepScript:
	default:
		return WorkflowSaveInput{}, false
	}
	return input, true
}

func workflowStepPayload(req map[string]any, step string) any {
	if data, exists := req["data"]; exists {
		return data
	}
	switch step {
	case workflowStepVideoSource:
		return workflowWrappedPayload(req, "video_source", "video_context")
	case workflowStepSubtitles:
		return workflowWrappedPayload(req, "subtitles", "subtitle")
	case workflowStepConfig:
		return workflowWrappedPayload(req, "config", "video_config")
	case workflowStepScript:
		return workflowWrappedPayload(req, "script", "ai_analysis_script")
	case workflowStepVoiceover:
		return workflowWrappedPayload(req, "voiceover", "narration")
	default:
		return req
	}
}

func workflowWrappedPayload(req map[string]any, wrapperKeys ...string) any {
	for _, key := range wrapperKeys {
		if value, exists := req[key]; exists && len(req) == 1 {
			return value
		}
	}
	payload := make(map[string]any, len(req))
	for key, value := range req {
		if key == "workflow_revision" || key == "revision" || key == "dependency_hash" || key == "status" {
			continue
		}
		payload[key] = value
	}
	return payload
}

func (a *App) saveWorkflowStep(c *gin.Context, step string, input WorkflowSaveInput) (WorkflowSaveResult, error) {
	workflowStep, ok := routeWorkflowStep(step)
	if !ok {
		return WorkflowSaveResult{}, gorm.ErrRecordNotFound
	}
	return a.workflowState().saveStep(c.Request.Context(), workflowStep, input)
}

func routeWorkflowStep(step string) (WorkflowStep, bool) {
	switch step {
	case workflowStepVideoSource:
		return WorkflowStepVideoSource, true
	case workflowStepSubtitles:
		return WorkflowStepSubtitle, true
	case workflowStepConfig:
		return WorkflowStepConfig, true
	case workflowStepScript:
		return WorkflowStepScript, true
	case workflowStepVoiceover:
		return WorkflowStepVoiceover, true
	default:
		return "", false
	}
}

func routeWorkflowStepOrUnknown(step string) WorkflowStep {
	workflowStep, ok := routeWorkflowStep(step)
	if !ok {
		return ""
	}
	return workflowStep
}

func (a *App) projectWorkflowResponse(project models.Project) gin.H {
	return gin.H{
		"project_id":   project.ID.String(),
		"video_source": a.workflowVideoSourceValue(project),
		"subtitles":    a.workflowJSONValueWithMeta(project.ID, WorkflowStepSubtitle, project.Subtitle),
		"config":       a.workflowJSONValueWithMeta(project.ID, WorkflowStepConfig, project.VideoConfig),
		"script":       a.workflowJSONValueWithMeta(project.ID, WorkflowStepScript, project.AIAnalysisScript),
		"voiceover":    a.workflowVoiceoverValue(project),
		"updated_at":   project.UpdatedAt,
	}
}

func (a *App) projectWorkflowStepResponse(project models.Project, step string) gin.H {
	base := gin.H{"project_id": project.ID.String(), "step": step, "updated_at": project.UpdatedAt}
	switch step {
	case workflowStepVideoSource:
		base["video_source"] = a.workflowVideoSourceValue(project)
	case workflowStepSubtitles:
		base["subtitles"] = a.workflowJSONValueWithMeta(project.ID, WorkflowStepSubtitle, project.Subtitle)
	case workflowStepConfig:
		base["config"] = a.workflowJSONValueWithMeta(project.ID, WorkflowStepConfig, project.VideoConfig)
	case workflowStepScript:
		base["script"] = a.workflowJSONValueWithMeta(project.ID, WorkflowStepScript, project.AIAnalysisScript)
	case workflowStepVoiceover:
		base["voiceover"] = a.workflowVoiceoverValue(project)
	}
	return base
}

func (a *App) rejectLegacyProjectWorkflowUpdates(c *gin.Context, projectID uuid.UUID, req map[string]any) bool {
	fields := legacyProjectWorkflowWriteFields(req)
	if len(fields) == 0 {
		return false
	}
	a.legacyWorkflowRejectedCount.Add(1)
	c.Header("X-NarratoAI-Deprecated-Workflow-Write", "rejected")
	_ = a.recordWorkflowEvent(projectID, "", uuid.Nil, 0, "legacy_workflow_write_rejected", "legacy project update workflow fields rejected", gin.H{
		"path":               c.FullPath(),
		"fields":             fields,
		"canonical_endpoint": "/api/v1/projects/:project_id/workflow/steps/:step",
	})
	fail(c, http.StatusGone, "workflow 字段写入已迁移，请使用 /api/v1/projects/:project_id/workflow/steps/:step")
	return true
}

func legacyProjectWorkflowWriteFields(req map[string]any) []string {
	var fields []string
	for _, key := range []string{"video_url", "video_context", "subtitle", "video_config", "ai_analysis_script", "audio_url", "mp3_url"} {
		if _, exists := req[key]; exists {
			fields = append(fields, key)
		}
	}
	sort.Strings(fields)
	return fields
}

func (a *App) workflowDependencyHashFor(projectID uuid.UUID, routeStep string) (string, error) {
	step, ok := routeWorkflowStep(routeStep)
	if !ok {
		return "", gorm.ErrRecordNotFound
	}
	spec, ok := workflowSpec(step)
	if !ok {
		return "", gorm.ErrRecordNotFound
	}
	return currentDependencyHash(a.db, spec, projectID)
}

func (a *App) activeWorkflowSnapshot(projectID uuid.UUID, step WorkflowStep) (datatypes.JSON, bool) {
	row, ok := a.activeWorkflowSnapshotRow(projectID, step)
	if !ok {
		return nil, false
	}
	return row.RawSnapshot, true
}

type workflowSnapshotRow struct {
	ID               uuid.UUID
	RawSnapshot      datatypes.JSON
	InputHash        string
	DependencyHash   string
	ContentHash      string
	WorkflowRevision int
}

func (a *App) activeWorkflowSnapshotRow(projectID uuid.UUID, step WorkflowStep) (workflowSnapshotRow, bool) {
	spec, ok := workflowSpec(step)
	if !ok {
		return workflowSnapshotRow{}, false
	}
	var row workflowSnapshotRow
	result := a.db.Table(spec.table).
		Select(fmt.Sprintf("id, raw_snapshot, input_hash, dependency_hash, %s AS content_hash, workflow_revision", spec.contentHashColumn)).
		Where("project_id = ? AND cleared_at IS NULL AND deleted_at IS NULL", projectID).
		Order("workflow_revision DESC, updated_at DESC").
		Limit(1).
		Find(&row)
	if result.Error != nil || result.RowsAffected == 0 || len(row.RawSnapshot) == 0 {
		return workflowSnapshotRow{}, false
	}
	return row, true
}

func (a *App) workflowJSONValueWithMeta(projectID uuid.UUID, step WorkflowStep, fallback datatypes.JSON) any {
	if row, ok := a.activeWorkflowSnapshotRow(projectID, step); ok {
		return workflowValueWithMeta(jsonValue(row.RawSnapshot), row)
	}
	return jsonValue(fallback)
}

func (a *App) workflowVideoSourceValue(project models.Project) gin.H {
	out := gin.H{}
	if row, ok := a.activeWorkflowSnapshotRow(project.ID, WorkflowStepVideoSource); ok {
		if value, ok := workflowValueWithMeta(jsonValue(row.RawSnapshot), row).(map[string]any); ok {
			for k, v := range value {
				out[k] = v
			}
		}
	} else {
		out["video_context"] = jsonValue(project.VideoContext)
	}
	if project.VideoURL != nil {
		out["video_url"] = *project.VideoURL
	}
	return out
}

func (a *App) workflowVoiceoverValue(project models.Project) gin.H {
	out := gin.H{}
	if row, ok := a.activeWorkflowSnapshotRow(project.ID, WorkflowStepVoiceover); ok {
		if value, ok := workflowValueWithMeta(jsonValue(row.RawSnapshot), row).(map[string]any); ok {
			for k, v := range value {
				out[k] = v
			}
		}
	}
	if project.AudioURL != nil {
		out["audio_url"] = *project.AudioURL
	}
	return out
}

func workflowValueWithMeta(value any, row workflowSnapshotRow) any {
	meta := gin.H{
		"step_id":           row.ID.String(),
		"content_hash":      row.ContentHash,
		"dependency_hash":   row.DependencyHash,
		"input_hash":        row.InputHash,
		"payload_hash":      row.InputHash,
		"workflow_revision": row.WorkflowRevision,
	}
	if obj, ok := value.(map[string]any); ok {
		out := gin.H{}
		for k, v := range obj {
			out[k] = v
		}
		for k, v := range meta {
			out[k] = v
		}
		return out
	}
	meta["data"] = value
	return meta
}

func (a *App) currentProjectWorkflowRevision(projectID uuid.UUID) int {
	var workflow struct {
		Revision int
	}
	result := a.db.Table("project_workflows").Select("revision").Where("project_id = ? AND deleted_at IS NULL", projectID).Limit(1).Find(&workflow)
	if result.Error == nil && result.RowsAffected > 0 && workflow.Revision > 0 {
		return workflow.Revision
	}
	var project struct {
		WorkflowSchemaVersion *int
	}
	if err := a.db.Table("projects").Select("workflow_schema_version").Where("id = ?", projectID).Take(&project).Error; err == nil && project.WorkflowSchemaVersion != nil && *project.WorkflowSchemaVersion > 0 {
		return *project.WorkflowSchemaVersion
	}
	return 1
}

func (a *App) projectWorkflowRootValue(projectID uuid.UUID) gin.H {
	var workflow models.ProjectWorkflow
	result := a.db.Where("project_id = ?", projectID).Limit(1).Find(&workflow)
	if result.Error == nil && result.RowsAffected > 0 {
		return gin.H{
			"workflow_id":    workflow.ID.String(),
			"project_id":     workflow.ProjectID.String(),
			"current_step":   workflow.CurrentStep,
			"revision":       workflow.Revision,
			"status":         workflow.Status,
			"failure_reason": workflow.FailureReason,
			"last_step_type": workflow.LastStepType,
			"last_step_id":   workflow.LastStepID,
			"updated_at":     workflow.UpdatedAt,
		}
	}
	revision := a.currentProjectWorkflowRevision(projectID)
	return gin.H{"project_id": projectID.String(), "revision": revision, "status": "active"}
}

func (a *App) recordWorkflowEvent(projectID uuid.UUID, step WorkflowStep, stepID uuid.UUID, revision int, eventType string, message string, metadata gin.H) error {
	var workflow models.ProjectWorkflow
	var workflowID *uuid.UUID
	if err := a.db.Where("project_id = ? AND deleted_at IS NULL", projectID).First(&workflow).Error; err == nil {
		workflowID = &workflow.ID
	}
	var stepType *string
	if step != "" {
		value := string(step)
		stepType = &value
	}
	var stepIDPtr *uuid.UUID
	if stepID != uuid.Nil {
		stepIDPtr = &stepID
	}
	var revisionPtr *int
	if revision > 0 {
		revisionPtr = &revision
	}
	event := models.ProjectWorkflowEvent{
		ProjectID:        projectID,
		WorkflowID:       workflowID,
		StepType:         stepType,
		StepID:           stepIDPtr,
		EventType:        eventType,
		WorkflowRevision: revisionPtr,
		ActorType:        "system",
		Message:          &message,
		Metadata:         jsonMap(metadata),
	}
	return a.db.Create(&event).Error
}

func jsonValue(raw datatypes.JSON) any {
	if len(raw) == 0 {
		return gin.H{}
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return string(raw)
	}
	if out == nil {
		return gin.H{}
	}
	return out
}

func firstString(values map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if value, exists := values[key]; exists {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text), true
			}
		}
	}
	return "", false
}

func firstStringFromAny(value any, keys ...string) (string, bool) {
	values, ok := value.(map[string]any)
	if !ok {
		return "", false
	}
	return firstString(values, keys...)
}

func firstOptionalString(values map[string]any, keys ...string) (*string, bool) {
	for _, key := range keys {
		value, exists := values[key]
		if !exists {
			continue
		}
		if value == nil {
			return nil, true
		}
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return nil, true
		}
		return &trimmed, true
	}
	return nil, false
}

func firstOptionalStringFromAny(value any, keys ...string) (*string, bool) {
	values, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	return firstOptionalString(values, keys...)
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func optionalInt(values ...any) *int {
	for _, value := range values {
		switch typed := value.(type) {
		case int:
			return &typed
		case int64:
			v := int(typed)
			return &v
		case float64:
			v := int(typed)
			return &v
		case json.Number:
			if parsed, err := typed.Int64(); err == nil {
				v := int(parsed)
				return &v
			}
		}
	}
	return nil
}
