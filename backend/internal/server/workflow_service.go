package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const workflowEmptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

var workflowStepOrder = []WorkflowStep{
	WorkflowStepVideoSource,
	WorkflowStepSubtitle,
	WorkflowStepConfig,
	WorkflowStepScript,
	WorkflowStepVoiceover,
}

type workflowService struct {
	db *gorm.DB
}

type workflowStepSpec struct {
	step              WorkflowStep
	table             string
	contentHashColumn string
	legacyJSONColumn  string
	currentStep       string
	dependsOn         WorkflowStep
}

type workflowActiveRow struct {
	ID               uuid.UUID
	InputHash        string
	DependencyHash   string
	ContentHash      string
	WorkflowRevision int
}

type workflowProjectLock struct {
	ID                    uuid.UUID
	WorkflowSchemaVersion *int
}

func (a *App) workflowState() *workflowService {
	return &workflowService{db: a.db}
}

func newWorkflowService(db *gorm.DB) *workflowService {
	return &workflowService{db: db}
}

func saveProjectVideoSourceWorkflow(ctx context.Context, db *gorm.DB, input WorkflowSaveInput) (WorkflowSaveResult, error) {
	return newWorkflowService(db).SaveVideoSource(ctx, input)
}

func saveProjectSubtitleWorkflow(ctx context.Context, db *gorm.DB, input WorkflowSaveInput) (WorkflowSaveResult, error) {
	return newWorkflowService(db).SaveSubtitle(ctx, input)
}

func saveProjectGenerationConfigWorkflow(ctx context.Context, db *gorm.DB, input WorkflowSaveInput) (WorkflowSaveResult, error) {
	return newWorkflowService(db).SaveConfig(ctx, input)
}

func saveProjectScriptWorkflow(ctx context.Context, db *gorm.DB, input WorkflowSaveInput) (WorkflowSaveResult, error) {
	return newWorkflowService(db).SaveScript(ctx, input)
}

func saveProjectVoiceoverWorkflow(ctx context.Context, db *gorm.DB, input WorkflowSaveInput) (WorkflowSaveResult, error) {
	return newWorkflowService(db).SaveVoiceover(ctx, input)
}

func (s *workflowService) SaveVideoSource(ctx context.Context, input WorkflowSaveInput) (WorkflowSaveResult, error) {
	return s.saveStep(ctx, WorkflowStepVideoSource, input)
}

func (s *workflowService) SaveSubtitle(ctx context.Context, input WorkflowSaveInput) (WorkflowSaveResult, error) {
	return s.saveStep(ctx, WorkflowStepSubtitle, input)
}

func (s *workflowService) SaveConfig(ctx context.Context, input WorkflowSaveInput) (WorkflowSaveResult, error) {
	return s.saveStep(ctx, WorkflowStepConfig, input)
}

func (s *workflowService) SaveScript(ctx context.Context, input WorkflowSaveInput) (WorkflowSaveResult, error) {
	return s.saveStep(ctx, WorkflowStepScript, input)
}

func (s *workflowService) SaveVoiceover(ctx context.Context, input WorkflowSaveInput) (WorkflowSaveResult, error) {
	return s.saveStep(ctx, WorkflowStepVoiceover, input)
}

func (s *workflowService) HardClearDownstream(ctx context.Context, tx *gorm.DB, projectID uuid.UUID, changedStep WorkflowStep, reason string) error {
	if tx == nil {
		tx = s.db.WithContext(ctx)
	}
	return hardClearDownstream(ctx, tx, projectID, changedStep, reason)
}

func (s *workflowService) saveStep(ctx context.Context, step WorkflowStep, input WorkflowSaveInput) (WorkflowSaveResult, error) {
	spec, ok := workflowSpec(step)
	if !ok {
		return WorkflowSaveResult{}, fmt.Errorf("unknown workflow step %q", step)
	}
	if input.ProjectID == uuid.Nil {
		return WorkflowSaveResult{}, errors.New("workflow project id is required")
	}
	canonical, inputHash, err := canonicalJSONAndHash(input.Payload)
	if err != nil {
		return WorkflowSaveResult{}, err
	}
	normalizedPayload, err := normalizeJSONValue(input.Payload)
	if err != nil {
		return WorkflowSaveResult{}, err
	}
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = "active"
	}

	var result WorkflowSaveResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		projectLock, err := lockProjectWorkflow(tx, input.ProjectID)
		if err != nil {
			return err
		}
		projectRevision := workflowProjectRevision(projectLock)
		if input.ExpectedWorkflowRevision != nil && *input.ExpectedWorkflowRevision != projectRevision {
			// Idempotent retry with same active payload is handled after the active row is loaded.
		}
		actualDependencyHash, err := currentDependencyHash(tx, spec, input.ProjectID)
		if err != nil {
			return err
		}
		if spec.dependsOn != "" {
			if input.ExpectedDependencyHash == "" {
				if actualDependencyHash != workflowEmptyHash {
					return &WorkflowConflictError{Step: step, Reason: "dependency_hash_required", Expected: input.ExpectedDependencyHash, Actual: actualDependencyHash}
				}
			} else if input.ExpectedDependencyHash != actualDependencyHash {
				return &WorkflowConflictError{Step: step, Reason: "dependency_hash_mismatch", Expected: input.ExpectedDependencyHash, Actual: actualDependencyHash}
			}
		}
		if spec.dependsOn == "" && input.ExpectedDependencyHash != "" && input.ExpectedDependencyHash != workflowEmptyHash {
			return &WorkflowConflictError{Step: step, Reason: "dependency_hash_mismatch", Expected: input.ExpectedDependencyHash, Actual: workflowEmptyHash}
		}

		active, found, err := activeWorkflowRow(tx, spec, input.ProjectID)
		if err != nil {
			return err
		}
		if input.ExpectedWorkflowRevision != nil && *input.ExpectedWorkflowRevision != projectRevision {
			allowInitialZero := !found && *input.ExpectedWorkflowRevision == 0 && projectRevision == 1
			allowIdempotentRetry := found && active.InputHash == inputHash && active.DependencyHash == actualDependencyHash
			if !allowInitialZero && !allowIdempotentRetry {
				return &WorkflowConflictError{Step: step, Reason: "workflow_revision_mismatch", Expected: *input.ExpectedWorkflowRevision, Actual: projectRevision}
			}
		}

		if found && active.InputHash == inputHash && active.DependencyHash == actualDependencyHash {
			if err := updateLegacyWorkflowCache(tx, spec, input.ProjectID, canonical, input.LegacyCache); err != nil {
				return err
			}
			if err := upsertProjectWorkflowRoot(tx, input.ProjectID, projectRevision, spec.currentStep, active.ID, string(step), "active", ""); err != nil {
				return err
			}
			if err := createWorkflowEvent(tx, input.ProjectID, projectRevision, step, active.ID, "workflow_step_idempotent", "workflow step save was idempotent", map[string]any{"input_hash": inputHash, "dependency_hash": actualDependencyHash}); err != nil {
				return err
			}
			result = WorkflowSaveResult{Step: step, StepID: active.ID, ProjectID: input.ProjectID, InputHash: active.InputHash, DependencyHash: active.DependencyHash, ContentHash: active.ContentHash, WorkflowRevision: projectRevision, Idempotent: true}
			return nil
		}

		nextRevision := projectRevision + 1
		if found {
			if err := clearActiveWorkflowRows(tx, spec, input.ProjectID, fmt.Sprintf("superseded_%s", step)); err != nil {
				return err
			}
		}

		stepID := uuid.New()
		contentHash := inputHash
		row := map[string]any{
			"id":                stepID,
			"project_id":        input.ProjectID,
			"raw_snapshot":      datatypes.JSON(canonical),
			"input_hash":        inputHash,
			"dependency_hash":   actualDependencyHash,
			"status":            status,
			"workflow_revision": nextRevision,
			"created_at":        time.Now().UTC(),
			"updated_at":        time.Now().UTC(),
		}
		row[spec.contentHashColumn] = contentHash
		fillWorkflowStepColumns(step, row, normalizedPayload)
		if err := tx.Table(spec.table).Create(row).Error; err != nil {
			return err
		}
		if err := updateLegacyWorkflowCache(tx, spec, input.ProjectID, canonical, input.LegacyCache); err != nil {
			return err
		}
		if err := updateWorkflowProjectRevision(tx, input.ProjectID, nextRevision, spec.currentStep); err != nil {
			return err
		}
		if err := upsertProjectWorkflowRoot(tx, input.ProjectID, nextRevision, spec.currentStep, stepID, string(step), "active", ""); err != nil {
			return err
		}
		if err := createWorkflowEvent(tx, input.ProjectID, nextRevision, step, stepID, "workflow_step_saved", "workflow step saved", map[string]any{"input_hash": inputHash, "dependency_hash": actualDependencyHash, "content_hash": contentHash}); err != nil {
			return err
		}
		if err := linkWorkflowUpload(tx, input.ProjectID, step, stepID, normalizedPayload); err != nil {
			return err
		}
		if found && (active.InputHash != inputHash || active.DependencyHash != actualDependencyHash) {
			if err := hardClearDownstream(ctx, tx, input.ProjectID, step, fmt.Sprintf("%s_hash_changed", step)); err != nil {
				return err
			}
		}
		result = WorkflowSaveResult{Step: step, StepID: stepID, ProjectID: input.ProjectID, InputHash: inputHash, DependencyHash: actualDependencyHash, ContentHash: contentHash, WorkflowRevision: nextRevision}
		return nil
	})
	return result, err
}

func workflowSpec(step WorkflowStep) (workflowStepSpec, bool) {
	specs := map[WorkflowStep]workflowStepSpec{
		WorkflowStepVideoSource: {step: WorkflowStepVideoSource, table: "project_video_sources", contentHashColumn: "content_hash", legacyJSONColumn: "video_context", currentStep: "video_source"},
		WorkflowStepSubtitle:    {step: WorkflowStepSubtitle, table: "project_subtitles", contentHashColumn: "content_hash", legacyJSONColumn: "subtitle", currentStep: "subtitle", dependsOn: WorkflowStepVideoSource},
		WorkflowStepConfig:      {step: WorkflowStepConfig, table: "project_generation_configs", contentHashColumn: "params_hash", legacyJSONColumn: "video_config", currentStep: "config", dependsOn: WorkflowStepSubtitle},
		WorkflowStepScript:      {step: WorkflowStepScript, table: "project_scripts", contentHashColumn: "script_hash", legacyJSONColumn: "ai_analysis_script", currentStep: "script", dependsOn: WorkflowStepConfig},
		WorkflowStepVoiceover:   {step: WorkflowStepVoiceover, table: "project_voiceovers", contentHashColumn: "content_hash", currentStep: "voiceover", dependsOn: WorkflowStepScript},
	}
	spec, ok := specs[step]
	return spec, ok
}

func activeWorkflowRow(tx *gorm.DB, spec workflowStepSpec, projectID uuid.UUID) (workflowActiveRow, bool, error) {
	var row workflowActiveRow
	result := tx.Table(spec.table).
		Select(fmt.Sprintf("id, input_hash, dependency_hash, %s AS content_hash, workflow_revision", spec.contentHashColumn)).
		Where("project_id = ? AND cleared_at IS NULL AND deleted_at IS NULL", projectID).
		Order("workflow_revision DESC, updated_at DESC").
		Limit(1).
		Find(&row)
	if result.Error != nil {
		return workflowActiveRow{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		return workflowActiveRow{}, false, nil
	}
	return row, true, nil
}

func currentDependencyHash(tx *gorm.DB, spec workflowStepSpec, projectID uuid.UUID) (string, error) {
	if spec.dependsOn == "" {
		return workflowEmptyHash, nil
	}
	dependency, ok := workflowSpec(spec.dependsOn)
	if !ok {
		return "", fmt.Errorf("unknown dependency step %q", spec.dependsOn)
	}
	var row struct{ ContentHash string }
	result := tx.Table(dependency.table).
		Select(fmt.Sprintf("%s AS content_hash", dependency.contentHashColumn)).
		Where("project_id = ? AND cleared_at IS NULL AND deleted_at IS NULL", projectID).
		Order("workflow_revision DESC, updated_at DESC").
		Limit(1).
		Find(&row)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected == 0 {
		return workflowEmptyHash, nil
	}
	return row.ContentHash, nil
}

func lockProjectWorkflow(tx *gorm.DB, projectID uuid.UUID) (workflowProjectLock, error) {
	var locked workflowProjectLock
	err := tx.Table("projects").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id, workflow_schema_version").Where("id = ?", projectID).Take(&locked).Error
	return locked, err
}

func workflowProjectRevision(project workflowProjectLock) int {
	if project.WorkflowSchemaVersion == nil || *project.WorkflowSchemaVersion <= 0 {
		return 1
	}
	return *project.WorkflowSchemaVersion
}

func updateWorkflowProjectRevision(tx *gorm.DB, projectID uuid.UUID, revision int, currentStep string) error {
	updates := map[string]any{"workflow_schema_version": revision, "updated_at": time.Now().UTC()}
	if currentStep != "" {
		updates["current_step"] = currentStep
	}
	return tx.Table("projects").Where("id = ?", projectID).Updates(updates).Error
}

func upsertProjectWorkflowRoot(tx *gorm.DB, projectID uuid.UUID, revision int, currentStep string, stepID uuid.UUID, stepType string, status string, failureReason string) error {
	now := time.Now().UTC()
	if revision <= 0 {
		revision = 1
	}
	if status == "" {
		status = "active"
	}
	updates := map[string]any{
		"revision":       revision,
		"status":         status,
		"updated_at":     now,
		"last_step_type": stepType,
		"last_step_id":   stepID,
	}
	if currentStep != "" {
		updates["current_step"] = currentStep
	}
	if failureReason != "" {
		updates["failure_reason"] = failureReason
	} else {
		updates["failure_reason"] = nil
	}
	result := tx.Table("project_workflows").Where("project_id = ? AND deleted_at IS NULL", projectID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	row := map[string]any{
		"id":             uuid.New(),
		"project_id":     projectID,
		"current_step":   currentStep,
		"revision":       revision,
		"status":         status,
		"last_step_type": stepType,
		"last_step_id":   stepID,
		"created_at":     now,
		"updated_at":     now,
	}
	if failureReason != "" {
		row["failure_reason"] = failureReason
	}
	return tx.Table("project_workflows").Create(row).Error
}

func createWorkflowEvent(tx *gorm.DB, projectID uuid.UUID, revision int, step WorkflowStep, stepID uuid.UUID, eventType string, message string, metadata map[string]any) error {
	var workflow models.ProjectWorkflow
	var workflowID *uuid.UUID
	if err := tx.Where("project_id = ? AND deleted_at IS NULL", projectID).First(&workflow).Error; err == nil {
		workflowID = &workflow.ID
	}
	stepType := string(step)
	revisionValue := revision
	event := models.ProjectWorkflowEvent{
		ProjectID:        projectID,
		WorkflowID:       workflowID,
		StepType:         &stepType,
		StepID:           &stepID,
		EventType:        eventType,
		WorkflowRevision: &revisionValue,
		ActorType:        "system",
		Message:          &message,
		Metadata:         jsonMap(metadata),
	}
	return tx.Create(&event).Error
}

func clearActiveWorkflowRows(tx *gorm.DB, spec workflowStepSpec, projectID uuid.UUID, reason string) error {
	now := time.Now().UTC()
	return tx.Table(spec.table).
		Where("project_id = ? AND cleared_at IS NULL AND deleted_at IS NULL", projectID).
		Updates(map[string]any{"status": "cleared", "cleared_at": now, "cleared_reason": reason, "updated_at": now}).Error
}

func hardClearDownstream(ctx context.Context, tx *gorm.DB, projectID uuid.UUID, changedStep WorkflowStep, reason string) error {
	start := workflowStepIndex(changedStep)
	if start < 0 {
		return fmt.Errorf("unknown workflow step %q", changedStep)
	}
	for _, downstream := range workflowStepOrder[start+1:] {
		spec, _ := workflowSpec(downstream)
		if err := clearActiveWorkflowRows(tx.WithContext(ctx), spec, projectID, reason); err != nil {
			return err
		}
	}
	return resetDownstreamLegacyCache(tx.WithContext(ctx), projectID, changedStep)
}

func workflowStepIndex(step WorkflowStep) int {
	for i, candidate := range workflowStepOrder {
		if candidate == step {
			return i
		}
	}
	return -1
}

func updateLegacyWorkflowCache(tx *gorm.DB, spec workflowStepSpec, projectID uuid.UUID, canonical []byte, cache WorkflowLegacyCache) error {
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if spec.legacyJSONColumn != "" {
		updates[spec.legacyJSONColumn] = datatypes.JSON(canonical)
	}
	if spec.step == WorkflowStepVideoSource && cache.VideoURLSet {
		if cache.VideoURL == nil {
			updates["video_url"] = nil
		} else {
			updates["video_url"] = *cache.VideoURL
		}
	}
	if spec.step == WorkflowStepVoiceover && cache.AudioURLSet {
		if cache.AudioURL == nil {
			updates["audio_url"] = nil
		} else {
			updates["audio_url"] = *cache.AudioURL
		}
	}
	if len(updates) == 1 {
		return nil
	}
	return tx.Table("projects").Where("id = ?", projectID).Updates(updates).Error
}

func resetDownstreamLegacyCache(tx *gorm.DB, projectID uuid.UUID, changedStep WorkflowStep) error {
	updates := map[string]any{"updated_at": time.Now().UTC()}
	for _, downstream := range workflowStepOrder[workflowStepIndex(changedStep)+1:] {
		spec, _ := workflowSpec(downstream)
		if spec.legacyJSONColumn != "" {
			updates[spec.legacyJSONColumn] = datatypes.JSON([]byte("{}"))
		}
		if downstream == WorkflowStepVoiceover {
			updates["audio_url"] = nil
		}
	}
	if len(updates) == 1 {
		return nil
	}
	return tx.Table("projects").Where("id = ?", projectID).Updates(updates).Error
}

func canonicalJSONHash(value any) (string, error) {
	_, hash, err := canonicalJSONAndHash(value)
	return hash, err
}

func canonicalJSONAndHash(value any) ([]byte, string, error) {
	if value == nil {
		value = map[string]any{}
	}
	normalized, err := normalizeJSONValue(value)
	if err != nil {
		return nil, "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, "", err
	}
	h := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(h[:]), nil
}

func normalizeJSONValue(value any) (any, error) {
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case json.RawMessage:
		raw = []byte(v)
	case datatypes.JSON:
		raw = []byte(v)
	case string:
		raw = []byte(v)
	default:
		marshaled, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		raw = marshaled
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("workflow payload contains multiple JSON values")
		}
		return nil, err
	}
	return decoded, nil
}

func fillWorkflowStepColumns(step WorkflowStep, row map[string]any, payload any) {
	m, _ := payload.(map[string]any)
	s := func(keys ...string) *string {
		for _, key := range keys {
			if v, ok := m[key]; ok {
				text := strings.TrimSpace(fmt.Sprint(v))
				if text != "" && text != "<nil>" {
					return &text
				}
			}
		}
		return nil
	}
	i64 := func(keys ...string) *int64 {
		for _, key := range keys {
			if v, ok := m[key]; ok {
				switch x := v.(type) {
				case json.Number:
					if n, err := x.Int64(); err == nil {
						return &n
					}
				case float64:
					n := int64(x)
					return &n
				case int:
					n := int64(x)
					return &n
				case int64:
					return &x
				}
			}
		}
		return nil
	}
	b := func(keys ...string) bool {
		for _, key := range keys {
			if v, ok := m[key]; ok {
				if x, ok := v.(bool); ok {
					return x
				}
			}
		}
		return false
	}
	switch step {
	case WorkflowStepVideoSource:
		if v := s("original_filename", "filename", "name"); v != nil {
			row["original_filename"] = *v
		} else {
			row["original_filename"] = ""
		}
		if v := s("video_url", "url", "source_url"); v != nil {
			row["video_url"] = v
		}
		if v := s("storage_key"); v != nil {
			row["storage_key"] = v
		}
		if v := s("content_type"); v != nil {
			row["content_type"] = v
		}
		if v := i64("file_size"); v != nil {
			row["file_size"] = v
		}
		if v := i64("duration_ms"); v != nil {
			row["duration_ms"] = v
		}
		row["is_trimmed"] = b("is_trimmed", "trimmed") || i64("trim_start_ms", "trim_start") != nil || i64("trim_end_ms", "trim_end") != nil
		if v := i64("trim_start_ms", "trim_start"); v != nil {
			row["trim_start_ms"] = v
		}
		if v := i64("trim_end_ms", "trim_end"); v != nil {
			row["trim_end_ms"] = v
		}
		if v := s("asr_engine"); v != nil {
			row["asr_engine"] = v
		}
		row["frontend_params"] = row["raw_snapshot"]
	case WorkflowStepSubtitle:
		if v := s("asr_engine"); v != nil {
			row["asr_engine"] = v
		}
		if v := s("language"); v != nil {
			row["language"] = v
		}
		if v := s("subtitle_format", "format"); v != nil {
			row["subtitle_format"] = v
		}
		row["content"] = row["raw_snapshot"]
		row["raw_result"] = row["raw_snapshot"]
	case WorkflowStepConfig:
		for col, keys := range map[string][]string{"style": {"style"}, "language": {"language"}, "clip_mode": {"clip_mode"}, "original_ratio": {"original_ratio"}, "narration_ratio": {"narration_ratio"}, "voice_provider": {"voice_provider"}, "voice_id": {"voice_id"}} {
			if v := s(keys...); v != nil {
				row[col] = v
			}
		}
		row["raw_params"] = row["raw_snapshot"]
	case WorkflowStepScript:
		row["script_content"] = row["raw_snapshot"]
		row["raw_result"] = row["raw_snapshot"]
		if v := s("model_name", "model"); v != nil {
			row["model_name"] = v
		}
	case WorkflowStepVoiceover:
		if v := s("voice_provider", "provider"); v != nil {
			row["voice_provider"] = v
		}
		if v := s("voice_id", "voice"); v != nil {
			row["voice_id"] = v
		}
		if v := s("audio_url", "mp3_url", "voiceover_url", "url"); v != nil {
			row["audio_url"] = v
		}
		if v := s("storage_key"); v != nil {
			row["storage_key"] = v
		}
		row["segments"] = row["raw_snapshot"]
		row["params"] = row["raw_snapshot"]
	}
}

func linkWorkflowUpload(tx *gorm.DB, projectID uuid.UUID, step WorkflowStep, stepID uuid.UUID, payload any) error {
	m, ok := payload.(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := m["upload_id"]
	if !ok {
		return nil
	}
	id, err := uuid.Parse(strings.TrimSpace(fmt.Sprint(raw)))
	if err != nil {
		return nil
	}
	assetType := "other"
	if step == WorkflowStepVideoSource {
		assetType = "video"
	}
	if step == WorkflowStepVoiceover {
		assetType = "voiceover"
	}
	return tx.Table("upload_records").Where("id = ?", id).Updates(map[string]any{"project_id": projectID, "asset_type": assetType, "step_type": string(step), "step_id": stepID, "updated_at": time.Now().UTC()}).Error
}
