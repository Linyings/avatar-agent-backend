package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/datatypes"
)

type workflowTestState struct {
	Revision int
	Hashes   map[string]string
}

func newWorkflowTestState(t *testing.T, app *App, token string, projectID uuid.UUID) workflowTestState {
	t.Helper()
	state := workflowTestState{Hashes: map[string]string{}}
	resp := request(app, http.MethodGet, workflowPath(projectID, ""), nil, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("initial workflow summary status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var body map[string]any
	decodeWorkflowBody(t, resp, &body)
	state.Revision = workflowRevision(body)
	return state
}

func TestWorkflowAPIHappyPathReloadsAllSteps(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-happy@example.com")
	project := createTestProject(t, app, token, "workflow happy path")
	state := newWorkflowTestState(t, app, token, project.ID)

	video := saveWorkflowStep(t, app, token, project.ID, "video-source", state.Revision, "", map[string]any{
		"original_filename": "story.mp4",
		"storage_key":       "videos/story.mp4",
		"video_url":         "https://cdn.example.com/story.mp4",
		"trim_start":        1.25,
		"trim_end":          12.50,
		"asr_engine":        "dashscope",
		"raw":               map[string]any{"source": "test"},
	})
	state.capture("video-source", video)

	subtitles := saveWorkflowStep(t, app, token, project.ID, "subtitles", state.Revision, state.Hashes["video-source"], map[string]any{
		"language": "zh-CN",
		"items": []map[string]any{{
			"start": 1.25,
			"end":   3.50,
			"text":  "第一句字幕",
		}},
		"raw": map[string]any{"provider": "dashscope"},
	})
	state.capture("subtitles", subtitles)

	config := saveWorkflowStep(t, app, token, project.ID, "config", state.Revision, state.Hashes["subtitles"], map[string]any{
		"style":        "dramatic",
		"target_words": 180,
		"params":       map[string]any{"temperature": 0.2, "narration_ratio": 70},
		"raw":          map[string]any{"template_id": "电影电视剧"},
	})
	state.capture("config", config)

	script := saveWorkflowStep(t, app, token, project.ID, "script", state.Revision, state.Hashes["config"], map[string]any{
		"script_text": "第一句解说。第二句解说。",
		"items": []map[string]any{{
			"text":  "第一句解说",
			"start": 1.25,
			"end":   3.50,
		}},
		"raw": map[string]any{"model": "test-model"},
	})
	state.capture("script", script)

	voiceover := saveWorkflowStep(t, app, token, project.ID, "voiceover", state.Revision, state.Hashes["script"], map[string]any{
		"audio_url":        "https://cdn.example.com/voiceover.mp3",
		"storage_key":      "voiceovers/voiceover.mp3",
		"duration_seconds": 4.2,
		"voice":            "test-voice",
		"raw":              map[string]any{"provider": "fishaudio"},
	})
	state.capture("voiceover", voiceover)

	summary := getWorkflow(t, app, token, project.ID, "")
	assertBodyContains(t, summary, "story.mp4", "第一句字幕", "dramatic", "第一句解说", "voiceover.mp3")
}

func TestWorkflowSummaryExposesHashesForReloadedConfigSave(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-reload-config@example.com")
	project := createTestProject(t, app, token, "workflow reload config")
	state := newWorkflowTestState(t, app, token, project.ID)

	video := saveWorkflowStep(t, app, token, project.ID, "video-source", state.Revision, "", map[string]any{
		"original_filename": "reload.mp4",
		"storage_key":       "videos/reload.mp4",
	})
	state.capture("video-source", video)

	subtitles := saveWorkflowStep(t, app, token, project.ID, "subtitles", state.Revision, state.Hashes["video-source"], map[string]any{
		"language": "zh-CN",
		"items":    []map[string]any{{"start": 0.0, "end": 1.0, "text": "重新加载后保存配置"}},
	})
	state.capture("subtitles", subtitles)

	summaryResp := getWorkflow(t, app, token, project.ID, "")
	var summary map[string]any
	decodeWorkflowBody(t, summaryResp, &summary)
	reloadedRevision := workflowRevision(summary)
	reloadedSubtitles, ok := summary["subtitles"].(map[string]any)
	if !ok {
		t.Fatalf("summary subtitles shape = %#v, want object with workflow hashes", summary["subtitles"])
	}
	reloadedSubtitleHash, _ := reloadedSubtitles["content_hash"].(string)
	if reloadedSubtitleHash == "" || reloadedSubtitleHash != state.Hashes["subtitles"] {
		t.Fatalf("summary subtitles content_hash = %q, want %q; body = %s", reloadedSubtitleHash, state.Hashes["subtitles"], summaryResp.Body.String())
	}

	config := request(app, http.MethodPut, workflowPath(project.ID, "config"), workflowTestPayload(reloadedRevision, reloadedSubtitleHash, map[string]any{
		"style": "reload-safe-config",
	}), token)
	if config.Code != http.StatusOK {
		t.Fatalf("reloaded config save status = %d, body = %s", config.Code, config.Body.String())
	}
	assertBodyContains(t, config, "reload-safe-config")
}

func TestWorkflowRootTracksRevisionAndCurrentStep(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-root@example.com")
	project := createTestProject(t, app, token, "workflow root")
	state := newWorkflowTestState(t, app, token, project.ID)

	video := saveWorkflowStep(t, app, token, project.ID, "video-source", state.Revision, "", map[string]any{
		"video_url": "https://cdn.example.com/root.mp4",
	})
	state.capture("video-source", video)

	var root models.ProjectWorkflow
	if err := app.db.First(&root, "project_id = ?", project.ID).Error; err != nil {
		t.Fatalf("workflow root missing: %v", err)
	}
	if root.Revision != state.Revision || root.CurrentStep == nil || *root.CurrentStep != "video_source" {
		t.Fatalf("root revision/current_step = %d/%v, want %d/video_source", root.Revision, root.CurrentStep, state.Revision)
	}

	summary := getWorkflow(t, app, token, project.ID, "")
	assertBodyContains(t, summary, "workflow_root", "video_source")

	var eventCount int64
	if err := app.db.Model(&models.ProjectWorkflowEvent{}).Where("project_id = ? AND event_type = ?", project.ID, "workflow_step_saved").Count(&eventCount).Error; err != nil {
		t.Fatalf("count workflow events: %v", err)
	}
	if eventCount < 1 {
		t.Fatalf("workflow_step_saved event count = %d, want >= 1", eventCount)
	}
}

func TestWorkflowDependencyClearIsIdempotentForSameSubtitle(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-clear@example.com")
	project := createTestProject(t, app, token, "workflow dependency clear")
	state := seedWorkflowChain(t, app, token, project.ID)

	sameSubtitles := map[string]any{
		"language": "zh-CN",
		"items":    []map[string]any{{"start": 0.0, "end": 1.0, "text": "原始字幕"}},
		"raw":      map[string]any{"case": "idempotent"},
	}
	resaved := saveWorkflowStep(t, app, token, project.ID, "subtitles", state.Revision, state.Hashes["video-source"], sameSubtitles)
	state.capture("subtitles", resaved)
	unchangedSummary := getWorkflow(t, app, token, project.ID, "")
	assertBodyContains(t, unchangedSummary, "config-idempotent", "script-idempotent", "voiceover-idempotent.mp3")

	changed := saveWorkflowStep(t, app, token, project.ID, "subtitles", state.Revision, state.Hashes["video-source"], map[string]any{
		"language": "zh-CN",
		"items":    []map[string]any{{"start": 0.0, "end": 1.0, "text": "变更字幕"}},
		"raw":      map[string]any{"case": "changed"},
	})
	state.capture("subtitles", changed)
	clearedSummary := getWorkflow(t, app, token, project.ID, "")
	assertBodyContains(t, clearedSummary, "变更字幕")
	assertBodyNotContains(t, clearedSummary, "config-idempotent", "script-idempotent", "voiceover-idempotent.mp3")
}

func TestWorkflowAllowsFirstDependentStepWhenParentMissing(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-missing-parent@example.com")
	project := createTestProject(t, app, token, "workflow missing parent")
	state := newWorkflowTestState(t, app, token, project.ID)

	subtitles := saveWorkflowStep(t, app, token, project.ID, "subtitles", state.Revision, "", map[string]any{
		"language": "zh-CN",
		"items": []map[string]any{{
			"start": 0.0,
			"end":   2.0,
			"text":  "没有 video-source 也允许首次字幕保存",
		}},
	})
	state.capture("subtitles", subtitles)
	assertBodyContains(t, getWorkflow(t, app, token, project.ID, ""), "没有 video-source 也允许首次字幕保存")
}

func TestWorkflowRequiresDependencyHashWhenParentExists(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-require-dep@example.com")
	project := createTestProject(t, app, token, "workflow requires dependency")
	state := newWorkflowTestState(t, app, token, project.ID)

	video := saveWorkflowStep(t, app, token, project.ID, "video-source", state.Revision, "", map[string]any{
		"video_url": "https://cdn.example.com/parent.mp4",
	})
	state.capture("video-source", video)

	resp := request(app, http.MethodPut, workflowPath(project.ID, "subtitles"), workflowTestPayload(state.Revision, "", map[string]any{
		"items": []map[string]any{{"start": 0.0, "end": 1.0, "text": "缺少依赖 hash"}},
	}), token)
	if resp.Code != http.StatusConflict {
		t.Fatalf("save subtitles without dependency hash status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestWorkflowStaleSaveReturnsConflict(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-stale@example.com")
	project := createTestProject(t, app, token, "workflow stale save")
	state := newWorkflowTestState(t, app, token, project.ID)
	initialRevision := state.Revision

	video := saveWorkflowStep(t, app, token, project.ID, "video-source", initialRevision, "", map[string]any{
		"original_filename": "stale.mp4",
		"storage_key":       "videos/stale.mp4",
		"asr_engine":        "dashscope",
	})
	state.capture("video-source", video)

	stale := request(app, http.MethodPut, workflowPath(project.ID, "subtitles"), workflowTestPayload(initialRevision, state.Hashes["video-source"], map[string]any{
		"language": "zh-CN",
		"items":    []map[string]any{{"start": 0.0, "end": 1.0, "text": "stale client"}},
	}), token)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale save status = %d, want 409, body = %s", stale.Code, stale.Body.String())
	}

	var eventCount int64
	if err := app.db.Model(&models.ProjectWorkflowEvent{}).Where("project_id = ? AND event_type = ?", project.ID, "workflow_conflict").Count(&eventCount).Error; err != nil {
		t.Fatalf("count workflow conflict events: %v", err)
	}
	if eventCount < 1 {
		t.Fatalf("workflow_conflict event count = %d, want >= 1", eventCount)
	}
}

func TestWorkflowSaveRequiresRevision(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-revision-required@example.com")
	project := createTestProject(t, app, token, "workflow revision required")

	missingRevision := request(app, http.MethodPut, workflowPath(project.ID, "video-source"), map[string]any{
		"data": map[string]any{"original_filename": "missing-revision.mp4"},
	}, token)
	if missingRevision.Code != http.StatusBadRequest {
		t.Fatalf("missing revision status = %d, want 400, body = %s", missingRevision.Code, missingRevision.Body.String())
	}
}

func TestWorkflowOldAndNewVisibilityAndRoutes(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-compat@example.com")
	project := createTestProject(t, app, token, "workflow compatibility")
	state := seedWorkflowChain(t, app, token, project.ID)
	_ = state

	legacyDetail := request(app, http.MethodGet, "/api/v1/projects/"+project.ID.String(), nil, token)
	if legacyDetail.Code != http.StatusOK {
		t.Fatalf("legacy project detail status = %d, body = %s", legacyDetail.Code, legacyDetail.Body.String())
	}
	var detailBody map[string]any
	if err := json.Unmarshal(legacyDetail.Body.Bytes(), &detailBody); err != nil {
		t.Fatalf("decode project detail: %v", err)
	}
	for _, removed := range []string{"subtitle", "video_context", "video_config", "ai_analysis_script", "video_url", "audio_url", "current_step", "workflow_schema_version"} {
		if _, exists := detailBody[removed]; exists {
			t.Fatalf("project detail response contains removed top-level field %s: %s", removed, legacyDetail.Body.String())
		}
	}

	legacySubtitle := request(app, http.MethodGet, "/api/v1/projects/"+project.ID.String()+"/subtitle", nil, token)
	if legacySubtitle.Code != http.StatusOK {
		t.Fatalf("legacy subtitle status = %d, body = %s", legacySubtitle.Code, legacySubtitle.Body.String())
	}
	assertBodyContains(t, legacySubtitle, "原始字幕")

	legacyUpdate := request(app, http.MethodPut, "/api/v1/projects/"+project.ID.String(), map[string]any{
		"subtitle": map[string]any{"items": []map[string]any{{"start": 2.0, "end": 3.0, "text": "legacy subtitle visible"}}},
	}, token)
	if legacyUpdate.Code != http.StatusGone {
		t.Fatalf("legacy update status = %d, want 410, body = %s", legacyUpdate.Code, legacyUpdate.Body.String())
	}
	if legacyUpdate.Header().Get("X-NarratoAI-Deprecated-Workflow-Write") != "rejected" {
		t.Fatalf("legacy workflow write rejection header missing")
	}
	newSummary := getWorkflow(t, app, token, project.ID, "")
	assertBodyNotContains(t, newSummary, "legacy subtitle visible")
	assertActiveWorkflowRows(t, app, project.ID, WorkflowStepSubtitle, 1)
	assertActiveWorkflowRows(t, app, project.ID, WorkflowStepConfig, 1)
	assertActiveWorkflowRows(t, app, project.ID, WorkflowStepScript, 1)
	assertActiveWorkflowRows(t, app, project.ID, WorkflowStepVoiceover, 1)

	routeConflict := request(app, http.MethodGet, "/api/v1/projects/"+project.ID.String()+"/workflow", nil, token)
	if routeConflict.Code != http.StatusOK {
		t.Fatalf("workflow route status = %d, body = %s", routeConflict.Code, routeConflict.Body.String())
	}
}

func TestCanonicalWorkflowStepRoutesAndLegacyReadsUseCanonicalRows(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-canonical-routes@example.com")
	project := createTestProject(t, app, token, "workflow canonical routes")
	state := newWorkflowTestState(t, app, token, project.ID)

	video := request(app, http.MethodPut, canonicalWorkflowStepPath(project.ID, "video-source"), workflowTestPayload(state.Revision, "", map[string]any{
		"video_url":   "https://cdn.example.com/canonical.mp4",
		"video_title": "canonical video",
	}), token)
	if video.Code != http.StatusOK {
		t.Fatalf("canonical video-source save status = %d, body = %s", video.Code, video.Body.String())
	}
	var videoBody map[string]any
	decodeWorkflowBody(t, video, &videoBody)
	state.capture("video-source", videoBody)

	subtitles := request(app, http.MethodPut, canonicalWorkflowStepPath(project.ID, "subtitles"), workflowTestPayload(state.Revision, state.Hashes["video-source"], map[string]any{
		"items": []map[string]any{{"start": 0.0, "end": 1.0, "text": "canonical subtitle visible through legacy"}},
	}), token)
	if subtitles.Code != http.StatusOK {
		t.Fatalf("canonical subtitles save status = %d, body = %s", subtitles.Code, subtitles.Body.String())
	}

	canonicalRead := request(app, http.MethodGet, "/api/v1/projects/"+project.ID.String()+"/workflow", nil, token)
	if canonicalRead.Code != http.StatusOK {
		t.Fatalf("canonical workflow read status = %d, body = %s", canonicalRead.Code, canonicalRead.Body.String())
	}
	assertBodyContains(t, canonicalRead, "canonical subtitle visible through legacy", "content_hash")

	legacyRead := request(app, http.MethodGet, "/api/v1/projects/"+project.ID.String()+"/subtitle", nil, token)
	if legacyRead.Code != http.StatusOK {
		t.Fatalf("legacy subtitle read status = %d, body = %s", legacyRead.Code, legacyRead.Body.String())
	}
	assertBodyContains(t, legacyRead, "canonical subtitle visible through legacy", "content_hash")

	invalid := request(app, http.MethodGet, canonicalWorkflowStepPath(project.ID, "unknown-step"), nil, token)
	if invalid.Code != http.StatusNotFound {
		t.Fatalf("invalid canonical step status = %d, body = %s", invalid.Code, invalid.Body.String())
	}
}

func TestWorkflowScriptPersistsFrameIntermediateArtifacts(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-frame-artifacts@example.com")
	project := createTestProject(t, app, token, "workflow frame artifacts")
	state := newWorkflowTestState(t, app, token, project.ID)

	video := saveWorkflowStep(t, app, token, project.ID, "video-source", state.Revision, "", map[string]any{
		"original_filename": "frame-source.mp4",
	})
	state.capture("video-source", video)

	subtitles := saveWorkflowStep(t, app, token, project.ID, "subtitles", state.Revision, state.Hashes["video-source"], map[string]any{
		"items": []map[string]any{{"start": 0.0, "end": 1.0, "text": "关键字幕"}},
	})
	state.capture("subtitles", subtitles)

	config := saveWorkflowStep(t, app, token, project.ID, "config", state.Revision, state.Hashes["subtitles"], map[string]any{
		"style": "frame-debug",
	})
	state.capture("config", config)

	scriptPayload := map[string]any{
		"items": []map[string]any{{"text": "最终解说", "start": 0.0, "end": 1.0}},
		"frame_workflow": map[string]any{
			"tokens": map[string]any{"total": 32},
		},
		"timeline_decision": map[string]any{
			"needs_timeline_view": true,
			"decisions": []map[string]any{{
				"start":  0.0,
				"end":    1.0,
				"reason": "关键视觉判断",
			}},
		},
		"timeline_analyses": []map[string]any{{
			"decisionIndex": 0,
			"response": map[string]any{
				"analysis": map[string]any{"summary": "时间线画面分析结果"},
			},
		}},
		"narrative_understanding": map[string]any{
			"analysis": map[string]any{"summary": "剧情理解结果"},
		},
	}
	saveWorkflowStep(t, app, token, project.ID, "script", state.Revision, state.Hashes["config"], scriptPayload)

	legacyRead := request(app, http.MethodGet, "/api/v1/projects/"+project.ID.String()+"/ai-script", nil, token)
	if legacyRead.Code != http.StatusOK {
		t.Fatalf("legacy ai-script read status = %d, body = %s", legacyRead.Code, legacyRead.Body.String())
	}
	assertBodyContains(t, legacyRead, "timeline_decision", "关键视觉判断", "timeline_analyses", "时间线画面分析结果", "narrative_understanding", "剧情理解结果")

	var stored struct {
		ScriptContent datatypes.JSON
		RawSnapshot   datatypes.JSON
	}
	if err := app.db.Table("project_scripts").
		Select("script_content, raw_snapshot").
		Where("project_id = ? AND cleared_at IS NULL AND deleted_at IS NULL", project.ID).
		Take(&stored).Error; err != nil {
		t.Fatalf("read stored project script: %v", err)
	}
	assertJSONContainsKeys(t, stored.ScriptContent, "timeline_decision", "timeline_analyses", "narrative_understanding")
	assertJSONContainsKeys(t, stored.RawSnapshot, "timeline_decision", "timeline_analyses", "narrative_understanding")
}

func TestWorkflowVideoSourceClearPersistsNullLegacyCache(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "workflow-clear-video-url@example.com")
	project := createTestProject(t, app, token, "workflow clear video url")
	state := newWorkflowTestState(t, app, token, project.ID)

	video := request(app, http.MethodPut, canonicalWorkflowStepPath(project.ID, "video-source"), workflowTestPayload(state.Revision, "", map[string]any{
		"video_url": "https://cdn.example.com/clear-me.mp4",
	}), token)
	if video.Code != http.StatusOK {
		t.Fatalf("initial video-source save status = %d, body = %s", video.Code, video.Body.String())
	}
	var body map[string]any
	decodeWorkflowBody(t, video, &body)
	state.capture("video-source", body)

	cleared := request(app, http.MethodPut, canonicalWorkflowStepPath(project.ID, "video-source"), map[string]any{
		"workflow_revision": state.Revision,
		"data":              map[string]any{"video_url": nil},
	}, token)
	if cleared.Code != http.StatusOK {
		t.Fatalf("clear video-source status = %d, body = %s", cleared.Code, cleared.Body.String())
	}

	var stored models.Project
	if err := app.db.First(&stored, "id = ?", project.ID).Error; err != nil {
		t.Fatalf("reload project: %v", err)
	}
	if stored.VideoURL != nil {
		t.Fatalf("video_url cache = %q, want nil", *stored.VideoURL)
	}
}

func TestOpenAPIExposesWorkflowContract(t *testing.T) {
	app := newTestApp(t)
	resp := request(app, http.MethodGet, "/api/v1/openapi.json", nil, "")
	if resp.Code != http.StatusOK {
		t.Fatalf("openapi status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var spec map[string]any
	decodeWorkflowBody(t, resp, &spec)
	components := spec["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	if _, ok := schemas["WorkflowStepSaveRequest"]; !ok {
		t.Fatalf("WorkflowStepSaveRequest schema missing")
	}
	paths := spec["paths"].(map[string]any)
	stepPath := paths["/api/v1/projects/{project_id}/workflow/steps/{step}"].(map[string]any)
	put := stepPath["put"].(map[string]any)
	responses := put["responses"].(map[string]any)
	if _, ok := responses["409"]; !ok {
		t.Fatalf("workflow put 409 response missing: %#v", responses)
	}
	params := put["parameters"].([]any)
	foundStepEnum := false
	for _, param := range params {
		item := param.(map[string]any)
		if item["name"] == "step" {
			schema := item["schema"].(map[string]any)
			foundStepEnum = schema["$ref"] == "#/components/schemas/WorkflowStepName"
		}
	}
	if !foundStepEnum {
		t.Fatalf("workflow step parameter schema missing enum ref: %#v", params)
	}
}

func TestWorkflowUploadLinkageWhenSchemaExists(t *testing.T) {
	app := newTestApp(t)
	if !app.db.Migrator().HasColumn(&models.UploadRecord{}, "project_id") || !app.db.Migrator().HasColumn(&models.UploadRecord{}, "asset_type") {
		t.Skip("upload_records workflow linkage columns are not present in this build")
	}
	token := registerAndLogin(t, app, "workflow-upload@example.com")
	project := createTestProject(t, app, token, "workflow upload linkage")
	state := newWorkflowTestState(t, app, token, project.ID)

	upload := models.UploadRecord{
		UserID:           project.OwnerID,
		OriginalFilename: "linked.mp4",
		StorageKey:       "videos/linked.mp4",
		ContentType:      "video/mp4",
		Status:           "completed",
		StorageProvider:  "local",
	}
	if err := app.db.Create(&upload).Error; err != nil {
		t.Fatalf("create upload record: %v", err)
	}

	video := saveWorkflowStep(t, app, token, project.ID, "video-source", state.Revision, "", map[string]any{
		"upload_id":         upload.ID.String(),
		"original_filename": upload.OriginalFilename,
		"storage_key":       upload.StorageKey,
		"asr_engine":        "dashscope",
	})
	state.capture("video-source", video)

	var linkedProjectID string
	var assetType string
	row := app.db.Table("upload_records").Select("project_id, asset_type").Where("id = ?", upload.ID).Row()
	if err := row.Scan(&linkedProjectID, &assetType); err != nil {
		t.Fatalf("scan upload linkage: %v", err)
	}
	if linkedProjectID != project.ID.String() || assetType != "video" {
		t.Fatalf("upload linkage project_id=%q asset_type=%q, want %q/video", linkedProjectID, assetType, project.ID.String())
	}
}

func seedWorkflowChain(t *testing.T, app *App, token string, projectID uuid.UUID) workflowTestState {
	t.Helper()
	state := newWorkflowTestState(t, app, token, projectID)
	video := saveWorkflowStep(t, app, token, projectID, "video-source", state.Revision, "", map[string]any{
		"original_filename": "idempotent.mp4",
		"storage_key":       "videos/idempotent.mp4",
		"asr_engine":        "dashscope",
	})
	state.capture("video-source", video)
	subtitles := saveWorkflowStep(t, app, token, projectID, "subtitles", state.Revision, state.Hashes["video-source"], map[string]any{
		"language": "zh-CN",
		"items":    []map[string]any{{"start": 0.0, "end": 1.0, "text": "原始字幕"}},
		"raw":      map[string]any{"case": "idempotent"},
	})
	state.capture("subtitles", subtitles)
	config := saveWorkflowStep(t, app, token, projectID, "config", state.Revision, state.Hashes["subtitles"], map[string]any{
		"style":  "config-idempotent",
		"params": map[string]any{"marker": "config-idempotent"},
	})
	state.capture("config", config)
	script := saveWorkflowStep(t, app, token, projectID, "script", state.Revision, state.Hashes["config"], map[string]any{
		"script_text": "script-idempotent",
		"items":       []map[string]any{{"text": "script-idempotent", "start": 0.0, "end": 1.0}},
	})
	state.capture("script", script)
	voiceover := saveWorkflowStep(t, app, token, projectID, "voiceover", state.Revision, state.Hashes["script"], map[string]any{
		"audio_url":   "https://cdn.example.com/voiceover-idempotent.mp3",
		"storage_key": "voiceovers/voiceover-idempotent.mp3",
	})
	state.capture("voiceover", voiceover)
	return state
}

func saveWorkflowStep(t *testing.T, app *App, token string, projectID uuid.UUID, step string, revision int, dependencyHash string, data map[string]any) map[string]any {
	t.Helper()
	resp := request(app, http.MethodPut, workflowPath(projectID, step), workflowTestPayload(revision, dependencyHash, data), token)
	if resp.Code != http.StatusOK && resp.Code != http.StatusCreated {
		t.Fatalf("save workflow %s status = %d, body = %s", step, resp.Code, resp.Body.String())
	}
	var body map[string]any
	decodeWorkflowBody(t, resp, &body)
	return body
}

func getWorkflow(t *testing.T, app *App, token string, projectID uuid.UUID, step string) *httptest.ResponseRecorder {
	t.Helper()
	resp := request(app, http.MethodGet, workflowPath(projectID, step), nil, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("get workflow %s status = %d, body = %s", step, resp.Code, resp.Body.String())
	}
	return resp
}

func workflowPath(projectID uuid.UUID, step string) string {
	base := "/api/v1/projects/" + projectID.String() + "/workflow"
	if step == "" {
		return base
	}
	return base + "/steps/" + step
}

func canonicalWorkflowStepPath(projectID uuid.UUID, step string) string {
	return "/api/v1/projects/" + projectID.String() + "/workflow/steps/" + step
}

func workflowTestPayload(revision int, dependencyHash string, data map[string]any) map[string]any {
	payload := map[string]any{
		"workflow_revision": revision,
		"data":              data,
	}
	for key, value := range data {
		payload[key] = value
	}
	if dependencyHash != "" {
		payload["dependency_hash"] = dependencyHash
	}
	return payload
}

func (s *workflowTestState) capture(step string, body map[string]any) {
	if revision := workflowRevision(body); revision > 0 {
		s.Revision = revision
	}
	if hash := workflowHash(body); hash != "" {
		s.Hashes[step] = hash
	}
}

func workflowRevision(body map[string]any) int {
	for _, key := range []string{"workflow_revision", "revision"} {
		if value, ok := numberAsInt(body[key]); ok {
			return value
		}
	}
	if step, ok := body["step"].(map[string]any); ok {
		if value, ok := numberAsInt(step["workflow_revision"]); ok {
			return value
		}
		if value, ok := numberAsInt(step["revision"]); ok {
			return value
		}
	}
	return 0
}

func workflowHash(body map[string]any) string {
	for _, key := range []string{"content_hash", "input_hash", "payload_hash", "hash", "dependency_hash"} {
		if value, ok := body[key].(string); ok && value != "" {
			return value
		}
	}
	if step, ok := body["step"].(map[string]any); ok {
		return workflowHash(step)
	}
	return ""
}

func numberAsInt(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case json.Number:
		i, err := v.Int64()
		return int(i), err == nil
	default:
		return 0, false
	}
}

func decodeWorkflowBody(t *testing.T, resp *httptest.ResponseRecorder, target any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(resp.Body.Bytes()))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode workflow body: %v; body = %s", err, resp.Body.String())
	}
}

func assertBodyContains(t *testing.T, resp *httptest.ResponseRecorder, needles ...string) {
	t.Helper()
	body := resp.Body.String()
	for _, needle := range needles {
		if !strings.Contains(body, needle) {
			t.Fatalf("response missing %q: %s", needle, body)
		}
	}
}

func assertJSONContainsKeys(t *testing.T, raw datatypes.JSON, keys ...string) {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode stored json: %v; json = %s", err, string(raw))
	}
	for _, key := range keys {
		if _, exists := decoded[key]; !exists {
			t.Fatalf("stored json missing key %q: %s", key, string(raw))
		}
	}
}

func assertBodyNotContains(t *testing.T, resp *httptest.ResponseRecorder, needles ...string) {
	t.Helper()
	body := resp.Body.String()
	for _, needle := range needles {
		if strings.Contains(body, needle) {
			t.Fatalf("response unexpectedly contains %q: %s", needle, body)
		}
	}
}

func assertActiveWorkflowRows(t *testing.T, app *App, projectID uuid.UUID, step WorkflowStep, want int64) {
	t.Helper()
	spec, ok := workflowSpec(step)
	if !ok {
		t.Fatalf("unknown workflow step %q", step)
	}
	var count int64
	if err := app.db.Table(spec.table).Where("project_id = ? AND deleted_at IS NULL AND cleared_at IS NULL", projectID).Count(&count).Error; err != nil {
		t.Fatalf("count active workflow rows for %s: %v", step, err)
	}
	if count != want {
		t.Fatalf("active workflow rows for %s = %d, want %d", step, count, want)
	}
}
