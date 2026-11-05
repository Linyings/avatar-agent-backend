package server

import (
	"encoding/json"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/datatypes"
)

const (
	frameWorkflowMode             = "frame"
	frameStepCreated              = "step_0_created"
	frameStepMergeVideos          = "step_1_merge_videos"
	frameStepExtractAudio         = "step_2_extract_audio"
	frameStepTranscribe           = "step_3_transcribe"
	frameStepValidateSubtitles    = "step_4_validate_subtitles"
	frameStepDecideTimelineView   = "step_5_decide_timeline_view"
	frameStepGenerateTimelineImgs = "step_6_generate_timeline_images"
	frameStepAnalyzeTimelineImgs  = "step_7_analyze_timeline_images"
	frameStepUnderstandNarrative  = "step_8_understand_narrative"
	frameStepMergeVideoContext    = "step_9_merge_video_context"
	frameStepGenerateScript       = "step_10_generate_narration_script"
	frameStepMatchNarrationVisual = "step_10_match_narration_visuals"
	frameStepGenerateVoiceover    = "step_11_generate_voiceover"
	frameStepComposeFinalVideo    = "step_12_compose_final_video"
	frameStepPersistProject       = "step_13_persist_project"
	frameStepCompleted            = "step_14_completed"
	frameStepLegacyCompleted      = "step_13_completed"
)

var frameWorkflowStepIDs = map[string]string{
	frameStepMergeVideos:          "merge-videos",
	frameStepExtractAudio:         "extract-audio",
	frameStepTranscribe:           "transcribe",
	frameStepValidateSubtitles:    "validate-subtitles",
	frameStepDecideTimelineView:   "decide-timeline-view",
	frameStepGenerateTimelineImgs: "generate-timeline-images",
	frameStepAnalyzeTimelineImgs:  "analyze-timeline-images",
	frameStepUnderstandNarrative:  "understand-narrative",
	frameStepMergeVideoContext:    "merge-video-context",
	frameStepGenerateScript:       "generate-narration-script",
	frameStepMatchNarrationVisual: "match-narration-visuals",
	frameStepGenerateVoiceover:    "generate-voiceover",
	frameStepComposeFinalVideo:    "compose-final-video",
	frameStepPersistProject:       "persist-project",
}

var frameWorkflowStepIndex = map[string]int{
	frameStepCreated:              0,
	frameStepMergeVideos:          1,
	frameStepExtractAudio:         2,
	frameStepTranscribe:           3,
	frameStepValidateSubtitles:    4,
	frameStepDecideTimelineView:   5,
	frameStepGenerateTimelineImgs: 6,
	frameStepAnalyzeTimelineImgs:  7,
	frameStepUnderstandNarrative:  8,
	frameStepMergeVideoContext:    9,
	frameStepGenerateScript:       10,
	frameStepMatchNarrationVisual: 11,
	frameStepGenerateVoiceover:    12,
	frameStepComposeFinalVideo:    13,
	frameStepPersistProject:       14,
	frameStepCompleted:            15,
	frameStepLegacyCompleted:      13,
}

func defaultFrameWorkflow(status string) gin.H {
	if status == "" {
		status = "draft"
	}
	return gin.H{
		"mode":               frameWorkflowMode,
		"status":             status,
		"current_step":       frameStepCreated,
		"current_step_id":    nil,
		"current_step_index": 0,
		"revision":           1,
		"steps": gin.H{
			frameStepCreated:              gin.H{},
			frameStepMergeVideos:          gin.H{},
			frameStepExtractAudio:         gin.H{},
			frameStepTranscribe:           gin.H{},
			frameStepValidateSubtitles:    gin.H{},
			frameStepDecideTimelineView:   gin.H{},
			frameStepGenerateTimelineImgs: gin.H{},
			frameStepAnalyzeTimelineImgs:  gin.H{},
			frameStepUnderstandNarrative:  gin.H{},
			frameStepMergeVideoContext:    gin.H{},
			frameStepGenerateScript:       gin.H{},
			frameStepMatchNarrationVisual: gin.H{},
			frameStepGenerateVoiceover:    gin.H{},
			frameStepComposeFinalVideo:    gin.H{},
			frameStepPersistProject:       gin.H{},
			frameStepCompleted:            gin.H{},
		},
		"stages": gin.H{
			"source_video": gin.H{},
			"subtitles":    gin.H{},
			"config":       gin.H{},
			"script":       gin.H{},
			"voiceover":    gin.H{},
			"final_video":  gin.H{},
		},
	}
}

func normalizeFrameWorkflow(value datatypes.JSON, projectStatus string) gin.H {
	workflow := defaultFrameWorkflow(projectStatus)
	stored := projectJSONObject(value)
	for key, item := range stored {
		workflow[key] = item
	}
	if _, ok := workflow["steps"].(map[string]any); !ok {
		if _, ok := workflow["steps"].(gin.H); !ok {
			workflow["steps"] = defaultFrameWorkflow(projectStatus)["steps"]
		}
	}
	if _, ok := workflow["stages"].(map[string]any); !ok {
		if _, ok := workflow["stages"].(gin.H); !ok {
			workflow["stages"] = defaultFrameWorkflow(projectStatus)["stages"]
		}
	}
	if workflow["mode"] == nil || workflow["mode"] == "" {
		workflow["mode"] = frameWorkflowMode
	}
	if workflow["status"] == nil || workflow["status"] == "" {
		workflow["status"] = projectStatus
	}
	currentStep, _ := workflow["current_step"].(string)
	if currentStep == "" {
		currentStep = frameStepCreated
		workflow["current_step"] = currentStep
	}
	if index, ok := frameWorkflowStepIndex[currentStep]; ok {
		workflow["current_step_index"] = index
	}
	if stepID, ok := frameWorkflowStepIDs[currentStep]; ok {
		workflow["current_step_id"] = stepID
	} else if currentStep == frameStepCreated || currentStep == frameStepCompleted || currentStep == frameStepLegacyCompleted {
		workflow["current_step_id"] = nil
	}
	if _, ok := workflow["revision"].(float64); !ok {
		if _, ok := workflow["revision"].(int); !ok {
			workflow["revision"] = 1
		}
	}
	return workflow
}

func projectPublicResponse(project models.Project) gin.H {
	return gin.H{
		"id":            project.ID.String(),
		"name":          project.Name,
		"description":   project.Description,
		"thumbnail_url": project.ThumbnailURL,
		"owner_id":      project.OwnerID.String(),
		"status":        project.Status,
		"created_at":    project.CreatedAt,
		"updated_at":    project.UpdatedAt,
		"workflow":      normalizeFrameWorkflow(project.Workflow, project.Status),
		"artifacts":     projectJSONObject(project.Artifacts),
		"metadata":      projectJSONObject(project.ProjectMetadata),
	}
}

func projectListPublicResponse(project models.Project) gin.H {
	return gin.H{
		"id":            project.ID.String(),
		"name":          project.Name,
		"description":   project.Description,
		"thumbnail_url": project.ThumbnailURL,
		"owner_id":      project.OwnerID.String(),
		"status":        project.Status,
		"created_at":    project.CreatedAt,
		"updated_at":    project.UpdatedAt,
		"workflow":      projectListWorkflowResponse(project),
		"artifacts":     projectListArtifactsResponse(project),
		"metadata":      projectJSONObject(project.ProjectMetadata),
	}
}

func projectListWorkflowResponse(project models.Project) gin.H {
	currentStep := frameStepCreated
	if project.CurrentStep != nil && *project.CurrentStep != "" {
		currentStep = listWorkflowStepKey(*project.CurrentStep)
	}
	currentStepIndex := 0
	if index, ok := frameWorkflowStepIndex[currentStep]; ok {
		currentStepIndex = index
	}
	revision := 1
	if project.WorkflowSchemaVersion != nil && *project.WorkflowSchemaVersion > 0 {
		revision = *project.WorkflowSchemaVersion
	}
	var currentStepID any
	if stepID, ok := frameWorkflowStepIDs[currentStep]; ok {
		currentStepID = stepID
	}

	return gin.H{
		"mode":               frameWorkflowMode,
		"status":             project.Status,
		"current_step":       currentStep,
		"current_step_id":    currentStepID,
		"current_step_index": currentStepIndex,
		"revision":           revision,
		"steps":              gin.H{},
		"stages": gin.H{
			"source_video": gin.H{},
			"subtitles":    gin.H{},
			"config":       gin.H{},
			"script":       gin.H{},
			"voiceover":    gin.H{},
			"final_video":  gin.H{},
		},
	}
}

func listWorkflowStepKey(step string) string {
	switch step {
	case "video_source":
		return frameStepMergeVideos
	case "subtitle":
		return frameStepValidateSubtitles
	case "config":
		return frameStepDecideTimelineView
	case "script":
		return frameStepGenerateScript
	case "voiceover":
		return frameStepGenerateVoiceover
	default:
		return step
	}
}

func projectListArtifactsResponse(project models.Project) gin.H {
	artifacts := projectJSONObject(project.Artifacts)
	out := gin.H{}
	for _, key := range []string{"source_video", "final_video", "preview_image"} {
		if value, ok := artifacts[key]; ok {
			out[key] = value
		}
	}
	return out
}

func defaultFrameWorkflowJSON(status string) datatypes.JSON {
	return mustJSON(defaultFrameWorkflow(status))
}

func emptyJSONObjectJSON() datatypes.JSON {
	return datatypes.JSON([]byte("{}"))
}

func mustJSON(value any) datatypes.JSON {
	encoded, err := json.Marshal(value)
	if err != nil {
		return datatypes.JSON([]byte("{}"))
	}
	return datatypes.JSON(encoded)
}

func projectJSONObject(value datatypes.JSON) map[string]any {
	if len(value) == 0 {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(value, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func updatedWorkflowJSON(raw datatypes.JSON, projectStatus string, patch any) datatypes.JSON {
	workflow := normalizeFrameWorkflow(raw, projectStatus)
	patchMap, ok := patch.(map[string]any)
	if !ok {
		return mustJSON(workflow)
	}
	for key, value := range patchMap {
		workflow[key] = value
	}
	if revision, ok := workflow["revision"].(float64); ok {
		workflow["revision"] = int(revision)
	}
	workflow["updated_at"] = time.Now().UTC()
	return mustJSON(workflow)
}
