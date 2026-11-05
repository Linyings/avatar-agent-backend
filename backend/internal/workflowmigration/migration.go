package workflowmigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

var errDryRunRollback = errors.New("workflow migration dry-run rollback")

var stepOrder = []stepSpec{
	{name: "video-source", table: "project_video_sources", hashColumn: "content_hash"},
	{name: "subtitles", table: "project_subtitles", hashColumn: "content_hash", dependsOn: "video-source"},
	{name: "config", table: "project_generation_configs", hashColumn: "params_hash", dependsOn: "subtitles"},
	{name: "script", table: "project_scripts", hashColumn: "script_hash", dependsOn: "config"},
	{name: "voiceover", table: "project_voiceovers", hashColumn: "content_hash", dependsOn: "script"},
}

type Options struct {
	DryRun bool `json:"dry_run"`
}

type StepReport struct {
	Step             string `json:"step"`
	LegacyCandidates int    `json:"legacy_candidates"`
	ExistingActive   int    `json:"existing_active"`
	PendingInsert    int    `json:"pending_insert"`
	Inserted         int    `json:"inserted"`
	MissingAfter     int    `json:"missing_after"`
}

type Report struct {
	DryRun         bool         `json:"dry_run"`
	StartedAt      time.Time    `json:"started_at"`
	FinishedAt     time.Time    `json:"finished_at"`
	ProjectsMarked int          `json:"projects_marked"`
	Steps          []StepReport `json:"steps"`
}

type stepSpec struct {
	name       string
	table      string
	hashColumn string
	dependsOn  string
}

type activeRow struct {
	ContentHash string
}

type candidate struct {
	project models.Project
	raw     datatypes.JSON
	hash    string
}

func Run(ctx context.Context, db *gorm.DB, opts Options) (Report, error) {
	if db == nil {
		return Report{}, errors.New("db is required")
	}
	report := Report{DryRun: opts.DryRun, StartedAt: time.Now().UTC()}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, spec := range stepOrder {
			stepReport, err := runStep(ctx, tx, spec, opts)
			if err != nil {
				return err
			}
			report.Steps = append(report.Steps, stepReport)
		}
		marked, err := markMigratedProjects(ctx, tx, opts)
		if err != nil {
			return err
		}
		report.ProjectsMarked = marked
		if opts.DryRun {
			return errDryRunRollback
		}
		return nil
	})
	if opts.DryRun && errors.Is(err, errDryRunRollback) {
		err = nil
	}
	report.FinishedAt = time.Now().UTC()
	return report, err
}

func Verify(ctx context.Context, db *gorm.DB) (Report, error) {
	if db == nil {
		return Report{}, errors.New("db is required")
	}
	report := Report{DryRun: true, StartedAt: time.Now().UTC()}
	for _, spec := range stepOrder {
		candidates, err := legacyCandidates(ctx, db, spec)
		if err != nil {
			return report, err
		}
		stepReport := StepReport{Step: spec.name, LegacyCandidates: len(candidates)}
		for _, item := range candidates {
			exists, err := hasActiveRow(ctx, db, spec, item.project.ID)
			if err != nil {
				return report, err
			}
			if exists {
				stepReport.ExistingActive++
			} else {
				stepReport.MissingAfter++
			}
		}
		report.Steps = append(report.Steps, stepReport)
	}
	report.FinishedAt = time.Now().UTC()
	return report, nil
}

func (r Report) HasMissingLegacyRows() bool {
	for _, step := range r.Steps {
		if step.MissingAfter > 0 {
			return true
		}
	}
	return false
}

func runStep(ctx context.Context, tx *gorm.DB, spec stepSpec, opts Options) (StepReport, error) {
	candidates, err := legacyCandidates(ctx, tx, spec)
	if err != nil {
		return StepReport{}, err
	}
	report := StepReport{Step: spec.name, LegacyCandidates: len(candidates)}
	for _, item := range candidates {
		exists, err := hasActiveRow(ctx, tx, spec, item.project.ID)
		if err != nil {
			return report, err
		}
		if exists {
			report.ExistingActive++
			continue
		}
		report.PendingInsert++
		if opts.DryRun {
			continue
		}
		depHash, err := dependencyHash(ctx, tx, spec, item.project.ID)
		if err != nil {
			return report, err
		}
		if err := insertStep(ctx, tx, spec, item, depHash); err != nil {
			return report, err
		}
		report.Inserted++
	}
	return report, nil
}

func legacyCandidates(ctx context.Context, db *gorm.DB, spec stepSpec) ([]candidate, error) {
	var projects []models.Project
	if err := db.WithContext(ctx).Where("deleted_at IS NULL").Find(&projects).Error; err != nil {
		return nil, err
	}
	out := make([]candidate, 0, len(projects))
	for _, project := range projects {
		raw, ok := legacyPayload(project, spec.name)
		if !ok {
			continue
		}
		canonical, hash, err := canonicalJSONAndHash(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, candidate{project: project, raw: datatypes.JSON(canonical), hash: hash})
	}
	return out, nil
}

func legacyPayload(project models.Project, step string) (any, bool) {
	switch step {
	case "video-source":
		videoURL := optionalText(project.VideoURL)
		videoContext := jsonAny(project.VideoContext)
		if videoURL == nil && isEmptyJSONValue(videoContext) {
			return nil, false
		}
		payload := map[string]any{"video_context": videoContext}
		if videoURL != nil {
			payload["video_url"] = *videoURL
		}
		return payload, true
	case "subtitles":
		value := jsonAny(project.Subtitle)
		return value, !isEmptyJSONValue(value)
	case "config":
		value := jsonAny(project.VideoConfig)
		return value, !isEmptyJSONValue(value)
	case "script":
		value := jsonAny(project.AIAnalysisScript)
		return value, !isEmptyJSONValue(value)
	case "voiceover":
		audioURL := optionalText(project.AudioURL)
		if audioURL == nil {
			return nil, false
		}
		return map[string]any{"audio_url": *audioURL}, true
	default:
		return nil, false
	}
}

func insertStep(ctx context.Context, tx *gorm.DB, spec stepSpec, item candidate, depHash string) error {
	hash := item.hash
	switch spec.name {
	case "video-source":
		videoURL := optionalText(item.project.VideoURL)
		originalFilename := "legacy-video-source"
		if videoURL != nil {
			originalFilename = *videoURL
		}
		row := models.ProjectVideoSource{
			ProjectID:        item.project.ID,
			OriginalFilename: originalFilename,
			VideoURL:         videoURL,
			FrontendParams:   item.raw,
			RawSnapshot:      item.raw,
			InputHash:        &hash,
			DependencyHash:   &depHash,
			ContentHash:      &hash,
			Status:           "active",
			WorkflowRevision: 1,
		}
		return tx.WithContext(ctx).Create(&row).Error
	case "subtitles":
		row := models.ProjectSubtitle{
			ProjectID:        item.project.ID,
			Content:          item.raw,
			RawResult:        item.raw,
			RawSnapshot:      item.raw,
			InputHash:        &hash,
			DependencyHash:   &depHash,
			ContentHash:      &hash,
			Status:           "active",
			WorkflowRevision: 1,
		}
		return tx.WithContext(ctx).Create(&row).Error
	case "config":
		raw := item.raw
		row := models.ProjectGenerationConfig{
			ProjectID:        item.project.ID,
			RawParams:        raw,
			RawSnapshot:      raw,
			Style:            jsonString(raw, "style"),
			Language:         jsonString(raw, "language"),
			ClipMode:         jsonString(raw, "clip_mode"),
			OriginalRatio:    jsonString(raw, "original_ratio"),
			NarrationRatio:   jsonString(raw, "narration_ratio"),
			VoiceProvider:    jsonString(raw, "voice_provider"),
			VoiceID:          jsonString(raw, "voice_id"),
			InputHash:        &hash,
			DependencyHash:   &depHash,
			ParamsHash:       &hash,
			Status:           "active",
			WorkflowRevision: 1,
		}
		return tx.WithContext(ctx).Create(&row).Error
	case "script":
		raw := item.raw
		row := models.ProjectScript{
			ProjectID:        item.project.ID,
			ScriptContent:    raw,
			RawResult:        raw,
			RawSnapshot:      raw,
			ModelName:        jsonString(raw, "model_name"),
			InputHash:        &hash,
			DependencyHash:   &depHash,
			ScriptHash:       &hash,
			Status:           "active",
			WorkflowRevision: 1,
		}
		return tx.WithContext(ctx).Create(&row).Error
	case "voiceover":
		raw := item.raw
		row := models.ProjectVoiceover{
			ProjectID:        item.project.ID,
			AudioURL:         optionalText(item.project.AudioURL),
			Segments:         raw,
			Params:           raw,
			RawSnapshot:      raw,
			InputHash:        &hash,
			DependencyHash:   &depHash,
			ContentHash:      &hash,
			Status:           "active",
			WorkflowRevision: 1,
		}
		return tx.WithContext(ctx).Create(&row).Error
	default:
		return fmt.Errorf("unknown workflow step %q", spec.name)
	}
}

func hasActiveRow(ctx context.Context, db *gorm.DB, spec stepSpec, projectID uuid.UUID) (bool, error) {
	var count int64
	err := db.WithContext(ctx).Table(spec.table).
		Where("project_id = ? AND deleted_at IS NULL AND cleared_at IS NULL", projectID).
		Count(&count).Error
	return count > 0, err
}

func dependencyHash(ctx context.Context, db *gorm.DB, spec stepSpec, projectID uuid.UUID) (string, error) {
	if spec.dependsOn == "" {
		return emptyHash, nil
	}
	parent, ok := findSpec(spec.dependsOn)
	if !ok {
		return "", fmt.Errorf("unknown dependency %q", spec.dependsOn)
	}
	var row activeRow
	err := db.WithContext(ctx).Table(parent.table).
		Select(parent.hashColumn+" AS content_hash").
		Where("project_id = ? AND deleted_at IS NULL AND cleared_at IS NULL", projectID).
		Order("workflow_revision DESC, updated_at DESC").
		Limit(1).
		Find(&row).Error
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(row.ContentHash) == "" {
		return emptyHash, nil
	}
	return row.ContentHash, nil
}

func markMigratedProjects(ctx context.Context, tx *gorm.DB, opts Options) (int, error) {
	var projects []models.Project
	if err := tx.WithContext(ctx).Where("deleted_at IS NULL").Find(&projects).Error; err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	count := 0
	for _, project := range projects {
		if !projectHasLegacyWorkflow(project) {
			continue
		}
		count++
		if opts.DryRun {
			continue
		}
		revision := 1
		if project.WorkflowSchemaVersion != nil && *project.WorkflowSchemaVersion > 1 {
			revision = *project.WorkflowSchemaVersion
		}
		updates := map[string]any{
			"workflow_migrated_at":    now,
			"workflow_schema_version": revision,
		}
		if project.WorkflowMigratedAt != nil {
			delete(updates, "workflow_migrated_at")
		}
		if err := tx.WithContext(ctx).Model(&models.Project{}).Where("id = ?", project.ID).Updates(updates).Error; err != nil {
			return count, err
		}
		currentStep, lastStepID, lastStepType, err := latestActiveWorkflowStep(ctx, tx, project.ID)
		if err != nil {
			return count, err
		}
		if currentStep != "" {
			root := models.ProjectWorkflow{
				ProjectID:    project.ID,
				CurrentStep:  &currentStep,
				Revision:     revision,
				Status:       "active",
				LastStepType: &lastStepType,
				LastStepID:   &lastStepID,
			}
			if err := tx.WithContext(ctx).Where("project_id = ?", project.ID).FirstOrCreate(&root, models.ProjectWorkflow{ProjectID: project.ID}).Error; err != nil {
				return count, err
			}
			if err := tx.WithContext(ctx).Model(&models.ProjectWorkflow{}).Where("project_id = ?", project.ID).Updates(map[string]any{
				"current_step":   currentStep,
				"revision":       revision,
				"status":         "active",
				"last_step_type": lastStepType,
				"last_step_id":   lastStepID,
			}).Error; err != nil {
				return count, err
			}
		}
	}
	return count, nil
}

func latestActiveWorkflowStep(ctx context.Context, db *gorm.DB, projectID uuid.UUID) (string, uuid.UUID, string, error) {
	for i := len(stepOrder) - 1; i >= 0; i-- {
		spec := stepOrder[i]
		var row struct {
			ID uuid.UUID
		}
		result := db.WithContext(ctx).Table(spec.table).
			Select("id").
			Where("project_id = ? AND deleted_at IS NULL AND cleared_at IS NULL", projectID).
			Order("workflow_revision DESC, updated_at DESC").
			Limit(1).
			Find(&row)
		if result.Error != nil {
			return "", uuid.Nil, "", result.Error
		}
		if result.RowsAffected > 0 {
			return spec.name, row.ID, spec.name, nil
		}
	}
	return "", uuid.Nil, "", nil
}

func projectHasLegacyWorkflow(project models.Project) bool {
	for _, spec := range stepOrder {
		if _, ok := legacyPayload(project, spec.name); ok {
			return true
		}
	}
	return false
}

func findSpec(name string) (stepSpec, bool) {
	for _, spec := range stepOrder {
		if spec.name == name {
			return spec, true
		}
	}
	return stepSpec{}, false
}

func canonicalJSONAndHash(value any) ([]byte, string, error) {
	normalized := value
	if raw, ok := value.(datatypes.JSON); ok {
		normalized = jsonAny(raw)
	}
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(sum[:]), nil
}

func jsonAny(raw datatypes.JSON) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func isEmptyJSONValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case map[string]any:
		return len(typed) == 0
	case []any:
		return len(typed) == 0
	case string:
		return strings.TrimSpace(typed) == ""
	default:
		return false
	}
}

func optionalText(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func jsonString(raw datatypes.JSON, key string) *string {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	value, ok := obj[key].(string)
	if !ok {
		return nil
	}
	return optionalText(&value)
}
