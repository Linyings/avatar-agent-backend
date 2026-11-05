package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
)

type frameQualityGateEndpointCase struct {
	name      string
	path      string
	stepID    string
	payload   map[string]any
	resultKey string
}

func frameQualityGateEndpointCases() []frameQualityGateEndpointCase {
	intent := map[string]any{
		"output_language":       "zh-CN",
		"narration_style_title": "cinematic",
		"output_duration_ms":    240000,
		"narration_ratio":       0.5,
		"word_count":            800,
	}
	return []frameQualityGateEndpointCase{
		{
			name:      "timeline decisions",
			path:      "/api/v1/tools/frame-workflow/timeline-decisions/review",
			stepID:    "frame.timeline-decisions.review",
			resultKey: "decision_result",
			payload: map[string]any{"operation": "review", "intent": intent, "decision_result": map[string]any{
				"summary": "source", "needs_timeline_view": true, "decisions": []any{}, "skipped": []any{}, "confidence": "medium", "global_notes": []any{},
			}},
		},
		{
			name:      "narrative understanding",
			path:      "/api/v1/tools/frame-workflow/narrative-understanding/review",
			stepID:    "frame.narrative-understanding.review",
			resultKey: "narrative_result",
			payload: map[string]any{"operation": "review", "intent": intent, "narrative_result": map[string]any{
				"summary": "source narrative", "plot": map[string]any{}, "characters": []any{}, "relationships": []any{}, "emotion_curve": []any{}, "events": []any{}, "open_questions": []any{},
			}},
		},
	}
}

func TestFrameQualityGateRoutesStreamStructuredCompletion(t *testing.T) {
	for _, tc := range frameQualityGateEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFrameQualityGateFixture(t, tc, false, false, `{"passed":true,"issues":[]}`)
			resp := request(fixture.app, http.MethodPost, tc.path, tc.payload, fixture.token)
			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
			}
			for _, want := range []string{"event:stage", "event:thinking_delta", "event:answer_delta", "event:completed", `"success":true`, `"operation":"review"`, `"passed":true`, `"tokens_consumed":100`, `"request_id":"quality-gate-request-1"`, `"result":`} {
				if !strings.Contains(resp.Body.String(), want) {
					t.Fatalf("response missing %q: %s", want, resp.Body.String())
				}
			}
			if fixture.calls.Load() != 1 || fixture.model != "quality-gate-pro-model" {
				t.Fatalf("provider calls/model = %d/%q", fixture.calls.Load(), fixture.model)
			}
			originalJSON, _ := json.Marshal(tc.payload[tc.resultKey])
			if !strings.Contains(resp.Body.String(), string(originalJSON)) {
				t.Fatalf("review response did not backfill original result: %s", resp.Body.String())
			}
		})
	}
}

func TestFrameQualityGateRejectsInvalidOriginalResultBeforeProviderCall(t *testing.T) {
	tc := frameQualityGateEndpointCases()[0]
	fixture := newFrameQualityGateFixture(t, tc, false, false, `{"passed":true,"issues":[]}`)
	payload := cloneAnyMap(tc.payload)
	payload[tc.resultKey] = map[string]any{"decisions": []any{}}

	resp := request(fixture.app, http.MethodPost, tc.path, payload, fixture.token)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "invalid frame quality gate request") {
		t.Fatalf("response = %d %s", resp.Code, resp.Body.String())
	}
	if fixture.calls.Load() != 0 {
		t.Fatalf("provider calls = %d, want 0", fixture.calls.Load())
	}
}

func TestFrameQualityGateRefineRoutesReturnCorrectedStructuredResult(t *testing.T) {
	for _, tc := range frameQualityGateEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFrameQualityGateFixture(t, tc, false, false, "")
			payload := cloneAnyMap(tc.payload)
			payload["operation"] = "refine"
			payload["issues"] = []any{map[string]any{"code": "duration_mismatch", "severity": "high", "message": "shorten the result"}}

			resp := request(fixture.app, http.MethodPost, tc.path, payload, fixture.token)
			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
			}
			for _, want := range []string{"\"operation\":\"refine\"", "\"result\":{", "\"summary\":\"reviewed\""} {
				if !strings.Contains(resp.Body.String(), want) {
					t.Fatalf("response missing %q: %s", want, resp.Body.String())
				}
			}
		})
	}
}

func TestFrameQualityGateRefineRequiresIssues(t *testing.T) {
	for _, tc := range frameQualityGateEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFrameQualityGateFixture(t, tc, false, false, "")
			payload := cloneAnyMap(tc.payload)
			payload["operation"] = "refine"

			resp := request(fixture.app, http.MethodPost, tc.path, payload, fixture.token)
			if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "invalid frame quality gate request") {
				t.Fatalf("response = %d %s", resp.Code, resp.Body.String())
			}
			if fixture.calls.Load() != 0 {
				t.Fatalf("provider calls = %d, want 0", fixture.calls.Load())
			}
		})
	}
}

func TestHasFrameQualityGateIntentRejectsInvalidIntent(t *testing.T) {
	base := map[string]any{
		"intent": map[string]any{
			"output_language":       "zh-CN",
			"narration_style_title": "cinematic",
			"output_duration_ms":    float64(240000),
			"narration_ratio":       0.5,
			"word_count":            float64(800),
		},
	}
	if !hasFrameQualityGateIntent(base) {
		t.Fatal("expected valid intent")
	}

	for _, invalid := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "blank output language", mutate: func(intent map[string]any) { intent["output_language"] = " " }},
		{name: "zero duration", mutate: func(intent map[string]any) { intent["output_duration_ms"] = float64(0) }},
		{name: "fractional duration", mutate: func(intent map[string]any) { intent["output_duration_ms"] = 240000.5 }},
		{name: "ratio below zero", mutate: func(intent map[string]any) { intent["narration_ratio"] = -0.1 }},
		{name: "ratio above one", mutate: func(intent map[string]any) { intent["narration_ratio"] = 1.1 }},
		{name: "zero word count", mutate: func(intent map[string]any) { intent["word_count"] = float64(0) }},
		{name: "blank style title", mutate: func(intent map[string]any) {
			delete(intent, "narration_style_id")
			intent["narration_style_title"] = " "
		}},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			payload := cloneAnyMap(base)
			invalid.mutate(payload["intent"].(map[string]any))
			if hasFrameQualityGateIntent(payload) {
				t.Fatal("expected invalid intent")
			}
		})
	}
}

func TestParseFrameQualityGateProviderOutputRejectsInvalidContent(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		resultKey string
	}{
		{name: "non JSON", content: "not JSON", resultKey: "decision_result"},
		{name: "missing passed", content: `{"issues":[],"decision_result":{}}`, resultKey: "decision_result"},
		{name: "non boolean passed", content: `{"passed":"yes","issues":[],"decision_result":{}}`, resultKey: "decision_result"},
		{name: "missing issues", content: `{"passed":true,"decision_result":{}}`, resultKey: "decision_result"},
		{name: "non array issues", content: `{"passed":true,"issues":{},"decision_result":{}}`, resultKey: "decision_result"},
		{name: "missing stage result", content: `{"passed":true,"issues":[]}`, resultKey: "decision_result"},
		{name: "timeline string result", content: `{"passed":true,"issues":[],"decision_result":"invalid"}`, resultKey: "decision_result"},
		{name: "timeline array result", content: `{"passed":true,"issues":[],"decision_result":[]}`, resultKey: "decision_result"},
		{name: "timeline empty result", content: `{"passed":true,"issues":[],"decision_result":{}}`, resultKey: "decision_result"},
		{name: "narrative string result", content: `{"passed":true,"issues":[],"narrative_result":"invalid"}`, resultKey: "narrative_result"},
		{name: "narrative array result", content: `{"passed":true,"issues":[],"narrative_result":[]}`, resultKey: "narrative_result"},
		{name: "narrative empty result", content: `{"passed":true,"issues":[],"narrative_result":{}}`, resultKey: "narrative_result"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseFrameQualityGateProviderOutput(tt.content, "refine", tt.resultKey, nil); err == nil {
				t.Fatal("expected invalid provider output error")
			}
		})
	}
}

func TestParseFrameQualityGateProviderOutputToleratesSurroundingProse(t *testing.T) {
	content := "好的，这是修正后的结果：\n```json\n{\"passed\":true,\"issues\":[],\"narrative_result\":{\"summary\":\"主线\",\"plot\":{},\"characters\":[],\"relationships\":[],\"emotion_curve\":[],\"events\":[],\"open_questions\":[]}}\n```\n以上内容仅供参考，请据此使用。"
	output, err := parseFrameQualityGateProviderOutput(content, "refine", "narrative_result", nil)
	if err != nil {
		t.Fatalf("parse refined output with surrounding prose: %v", err)
	}
	if !output.passed || output.result == nil {
		t.Fatalf("output = %#v", output)
	}
}

func TestParseFrameQualityGateProviderOutputToleratesTrailingExplanation(t *testing.T) {
	content := "{\"passed\":true,\"issues\":[],\"decision_result\":{\"summary\":\"主线\",\"needs_timeline_view\":true,\"decisions\":[{\"start\":1,\"end\":2,\"priority\":\"high\",\"trigger_type\":\"ambiguous_pause\",\"reason\":\"候选切点\"}],\"skipped\":[],\"confidence\":\"medium\",\"global_notes\":[]}} 校验完成。"
	output, err := parseFrameQualityGateProviderOutput(content, "refine", "decision_result", nil)
	if err != nil {
		t.Fatalf("parse refined output with trailing text: %v", err)
	}
	if !output.passed || output.result == nil {
		t.Fatalf("output = %#v", output)
	}
}

func TestParseFrameQualityGateRefineMergesPartialNarrativeResult(t *testing.T) {
	content := `{"passed":true,"issues":[],"narrative_result":{"summary":"修正后的主线","characters":[{"name":"小明"}]}}`
	original := map[string]any{
		"summary":       "原始主线",
		"plot":          map[string]any{},
		"characters":    []any{},
		"relationships": []any{},
		"emotion_curve": []any{},
		"events":        []any{},
		"open_questions": []any{},
	}
	output, err := parseFrameQualityGateProviderOutput(content, "refine", "narrative_result", original)
	if err != nil {
		t.Fatalf("parse partial narrative refine: %v", err)
	}
	result, ok := output.result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v", output.result)
	}
	if result["summary"] != "修正后的主线" {
		t.Fatalf("summary = %#v", result["summary"])
	}
	for _, key := range []string{"plot", "relationships", "emotion_curve", "events", "open_questions"} {
		if _, exists := result[key]; !exists {
			t.Fatalf("merged result is missing %s", key)
		}
	}
}

func TestParseFrameQualityGateRefineMergesPartialDecisionResult(t *testing.T) {
	content := `{"passed":true,"issues":[],"decision_result":{"decisions":[{"start":1,"end":2,"priority":"high","trigger_type":"plot","reason":"conflict"}]}}`
	original := map[string]any{
		"summary":            "原始决策",
		"needs_timeline_view": true,
		"skipped":            []any{},
		"confidence":         "medium",
		"global_notes":       []any{},
	}
	output, err := parseFrameQualityGateProviderOutput(content, "refine", "decision_result", original)
	if err != nil {
		t.Fatalf("parse partial decision refine: %v", err)
	}
	result, ok := output.result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v", output.result)
	}
	if result["summary"] != "原始决策" || result["needs_timeline_view"] != true {
		t.Fatalf("merged result = %#v", result)
	}
}

func TestParseFrameQualityGateReviewAcceptsConclusionWithoutResult(t *testing.T) {
	output, err := parseFrameQualityGateProviderOutput(`{"passed":false,"issues":[{"code":"duration_mismatch","severity":"error","message":"duration differs"}]}`, "review", "decision_result", nil)
	if err != nil {
		t.Fatalf("parse review output: %v", err)
	}
	if output.passed || len(output.issues) != 1 || output.result != nil {
		t.Fatalf("review output = %#v", output)
	}
}

func TestParseFrameQualityGateReviewIgnoresInvalidAttachedResult(t *testing.T) {
	output, err := parseFrameQualityGateProviderOutput(`{"passed":true,"issues":[],"decision_result":{"decisions":[{"start":5,"end":2}]}}`, "review", "decision_result", nil)
	if err != nil {
		t.Fatalf("parse review output: %v", err)
	}
	if !output.passed || output.result != nil {
		t.Fatalf("review output = %#v", output)
	}
}

func TestFrameQualityGateProviderRequestUsesCanonicalAnalysis(t *testing.T) {
	original := map[string]any{
		"success":  true,
		"content":  "duplicated provider output",
		"analysis": map[string]any{"summary": "canonical narrative", "characters": []any{}},
		"usage":    map[string]any{"total_tokens": float64(10)},
	}
	req := map[string]any{
		"operation":        "review",
		"narrative_result": original,
		"intent":           map[string]any{"output_language": "zh-CN"},
	}

	providerReq := frameQualityGateProviderRequest(req, "narrative_result")
	result, ok := providerReq["narrative_result"].(map[string]any)
	if !ok || result["summary"] != "canonical narrative" {
		t.Fatalf("provider narrative_result = %#v", providerReq["narrative_result"])
	}
	if _, exists := result["content"]; exists {
		t.Fatalf("provider result retained duplicated content: %#v", result)
	}
	if original["content"] != "duplicated provider output" {
		t.Fatalf("original result was mutated: %#v", original)
	}
}

func TestFrameQualityGateProviderOutputDiagnosticDoesNotExposeContent(t *testing.T) {
	const secret = "PRIVATE SUBTITLE CONTENT"
	content := `{"passed":true,"issues":[],"decision_result":{},"unexpected":"` + secret + `"}`
	contentBytes, contentHash, shape := frameQualityGateProviderOutputDiagnostic(content, "decision_result")
	if contentBytes == 0 || len(contentHash) != 64 {
		t.Fatalf("diagnostic size/hash = %d/%q", contentBytes, contentHash)
	}
	if strings.Contains(shape, secret) || strings.Contains(contentHash, secret) {
		t.Fatalf("diagnostic leaked content: hash=%q shape=%q", contentHash, shape)
	}
	for _, want := range []string{"passed=bool", "issues=array", "decision_result=object", "unknown_fields=1"} {
		if !strings.Contains(shape, want) {
			t.Fatalf("diagnostic shape missing %q: %s", want, shape)
		}
	}
}

func TestParseFrameQualityGateProviderOutputRejectsMalformedSemanticContent(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		resultKey string
	}{
		{name: "issue must be an object", content: `{"passed":false,"issues":["invalid"],"decision_result":{"summary":"reviewed","needs_timeline_view":true,"decisions":[],"skipped":[],"confidence":"medium","global_notes":[]}}`, resultKey: "decision_result"},
		{name: "issue requires an allowed severity", content: `{"passed":false,"issues":[{"code":"unsupported","severity":"high","message":"not supported"}],"decision_result":{"summary":"reviewed","needs_timeline_view":true,"decisions":[],"skipped":[],"confidence":"medium","global_notes":[]}}`, resultKey: "decision_result"},
		{name: "issue requires a code", content: `{"passed":false,"issues":[{"severity":"warning","message":"not supported"}],"decision_result":{"summary":"reviewed","needs_timeline_view":true,"decisions":[],"skipped":[],"confidence":"medium","global_notes":[]}}`, resultKey: "decision_result"},
		{name: "issue requires a message", content: `{"passed":false,"issues":[{"code":"unsupported","severity":"warning","message":"  "}],"decision_result":{"summary":"reviewed","needs_timeline_view":true,"decisions":[],"skipped":[],"confidence":"medium","global_notes":[]}}`, resultKey: "decision_result"},
		{name: "timeline result requires decisions", content: `{"passed":true,"issues":[],"decision_result":{"summary":"reviewed","needs_timeline_view":true,"skipped":[],"confidence":"medium","global_notes":[]}}`, resultKey: "decision_result"},
		{name: "timeline decision requires a valid range", content: `{"passed":true,"issues":[],"decision_result":{"summary":"reviewed","needs_timeline_view":true,"decisions":[{"start":5,"end":2,"priority":"high","trigger_type":"plot","reason":"conflict"}],"skipped":[],"confidence":"medium","global_notes":[]}}`, resultKey: "decision_result"},
		{name: "timeline decision requires positive frames", content: `{"passed":true,"issues":[],"decision_result":{"summary":"reviewed","needs_timeline_view":true,"decisions":[{"start":2,"end":5,"priority":"high","trigger_type":"plot","reason":"conflict","n_frames":0}],"skipped":[],"confidence":"medium","global_notes":[]}}`, resultKey: "decision_result"},
		{name: "timeline result requires structured metadata", content: `{"passed":true,"issues":[],"decision_result":{"summary":"reviewed","needs_timeline_view":true,"decisions":[],"skipped":[],"confidence":"medium","global_notes":"none"}}`, resultKey: "decision_result"},
		{name: "narrative result requires structured collections", content: `{"passed":true,"issues":[],"narrative_result":{"summary":"narrative","plot":{},"characters":[],"relationships":[],"emotion_curve":[],"events":[]}}`, resultKey: "narrative_result"},
		{name: "narrative result requires a nonempty summary", content: `{"passed":true,"issues":[],"narrative_result":{"summary":"  ","plot":{},"characters":[],"relationships":[],"emotion_curve":[],"events":[],"open_questions":[]}}`, resultKey: "narrative_result"},
		{name: "narrative result requires an object plot", content: `{"passed":true,"issues":[],"narrative_result":{"summary":"narrative","plot":[],"characters":[],"relationships":[],"emotion_curve":[],"events":[],"open_questions":[]}}`, resultKey: "narrative_result"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseFrameQualityGateProviderOutput(tt.content, "refine", tt.resultKey, nil); err == nil {
				t.Fatal("expected malformed semantic provider output error")
			}
		})
	}
}

func TestParseFrameQualityGateProviderOutputAcceptsProductionShapedResults(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		resultKey string
	}{
		{name: "timeline quality gate result", content: `{"passed":true,"issues":[],"decision_result":{"summary":"reviewed","needs_timeline_view":true,"decisions":[{"start":1.5,"end":3,"priority":"high","trigger_type":"plot","reason":"conflict","n_frames":15}],"skipped":[],"confidence":"medium","global_notes":[]}}`, resultKey: "decision_result"},
		{name: "timeline PC result", content: `{"passed":true,"issues":[],"decision_result":{"success":true,"provider":"openai","model":"deepseek-chat","content":"{}","analysis":{"summary":"reviewed","needs_timeline_view":true,"decisions":[]},"decisions":[{"start":1.5,"end":3,"priority":"high","trigger_type":"plot","reason":"conflict","n_frames":15}],"usage":{"total_tokens":10},"request_id":"request-1","tokens_consumed":10,"updated_at":"2026-08-09T00:00:00Z"}}`, resultKey: "decision_result"},
		{name: "narrative quality gate result", content: `{"passed":true,"issues":[],"narrative_result":{"summary":"A conflict escalates.","plot":{},"characters":[],"relationships":[],"emotion_curve":[],"events":[],"open_questions":[]}}`, resultKey: "narrative_result"},
		{name: "narrative PC result", content: `{"passed":true,"issues":[],"narrative_result":{"success":true,"provider":"openai","model":"deepseek-chat","content":"{}","analysis":{"summary":"A conflict escalates."},"characters":[],"relationships":[],"emotion_curve":[],"events":[],"usage":{"total_tokens":10},"request_id":"request-1","tokens_consumed":10,"updated_at":"2026-08-09T00:00:00Z"}}`, resultKey: "narrative_result"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseFrameQualityGateProviderOutput(tt.content, "refine", tt.resultKey, nil); err != nil {
				t.Fatalf("parseFrameQualityGateProviderOutput() error = %v", err)
			}
		})
	}
}

func TestFrameQualityGateWorkflowIdentityMissingUsageFailsWithoutCompletedCharge(t *testing.T) {
	tc := frameQualityGateEndpointCases()[0]
	fixture := newFrameQualityGateFixture(t, tc, false, true, "")
	run := createWorkflowIdentityTestWebRun(t, fixture.app, fixture.user.ID, fixture.project.ID, "quality-gate-missing-usage", "quality-gate-client")
	payload := cloneAnyMap(tc.payload)
	payload["project_id"] = fixture.project.ID.String()
	payload["workflow_run_id"] = run.ID.String()
	payload["workflow_step_id"] = tc.stepID
	payload["workflow_input_hash"] = run.InputHash
	payload["client_instance_id"] = run.ClientInstanceID

	resp := request(fixture.app, http.MethodPost, tc.path, payload, fixture.token)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"code":"provider_usage_missing"`) {
		t.Fatalf("missing usage response = %d %s", resp.Code, resp.Body.String())
	}
	var step models.WorkflowStepExecution
	if err := fixture.app.db.Where("workflow_run_id = ? AND step_id = ?", run.ID, tc.stepID).Take(&step).Error; err != nil {
		t.Fatalf("load workflow step: %v", err)
	}
	if step.Status != workflowStepStatusFailed || step.ErrorCode == nil || *step.ErrorCode != "provider_usage_missing" {
		t.Fatalf("workflow step = %#v", step)
	}
	var reservations int64
	if err := fixture.app.db.Model(&models.CreditReservation{}).Where("workflow_run_id = ?", run.ID).Count(&reservations).Error; err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if reservations != 0 {
		t.Fatalf("completed charge reservations = %d, want 0", reservations)
	}
}

func TestFrameQualityGateWorkflowIdentityProviderFailureFinalizesFailedStep(t *testing.T) {
	tc := frameQualityGateEndpointCases()[1]
	fixture := newFrameQualityGateFixture(t, tc, true, false, "")
	run := createWorkflowIdentityTestWebRun(t, fixture.app, fixture.user.ID, fixture.project.ID, "quality-gate-provider-failure", "quality-gate-client")
	payload := cloneAnyMap(tc.payload)
	payload["project_id"] = fixture.project.ID.String()
	payload["workflow_run_id"] = run.ID.String()
	payload["workflow_step_id"] = tc.stepID
	payload["workflow_input_hash"] = run.InputHash
	payload["client_instance_id"] = run.ClientInstanceID

	resp := request(fixture.app, http.MethodPost, tc.path, payload, fixture.token)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"code":"provider_unavailable"`) {
		t.Fatalf("provider failure response = %d %s", resp.Code, resp.Body.String())
	}
	var step models.WorkflowStepExecution
	if err := fixture.app.db.Where("workflow_run_id = ? AND step_id = ?", run.ID, tc.stepID).Take(&step).Error; err != nil {
		t.Fatalf("load workflow step: %v", err)
	}
	if step.Status != workflowStepStatusFailed || step.ErrorCode == nil || *step.ErrorCode != "provider_unavailable" {
		t.Fatalf("workflow step = %#v", step)
	}
	var reservations int64
	if err := fixture.app.db.Model(&models.CreditReservation{}).Where("workflow_run_id = ?", run.ID).Count(&reservations).Error; err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if reservations != 0 {
		t.Fatalf("completed charge reservations = %d, want 0", reservations)
	}
}

func TestFrameQualityGateWorkflowIdentityInvalidProviderOutputFailsWithoutCompletedCharge(t *testing.T) {
	tc := frameQualityGateEndpointCases()[0]
	fixture := newFrameQualityGateFixture(t, tc, false, false, "not JSON")
	run := createWorkflowIdentityTestWebRun(t, fixture.app, fixture.user.ID, fixture.project.ID, "quality-gate-invalid-output", "quality-gate-client")
	payload := cloneAnyMap(tc.payload)
	payload["project_id"] = fixture.project.ID.String()
	payload["workflow_run_id"] = run.ID.String()
	payload["workflow_step_id"] = tc.stepID
	payload["workflow_input_hash"] = run.InputHash
	payload["client_instance_id"] = run.ClientInstanceID

	resp := request(fixture.app, http.MethodPost, tc.path, payload, fixture.token)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"code":"provider_invalid_output"`) {
		t.Fatalf("invalid output response = %d %s", resp.Code, resp.Body.String())
	}
	var step models.WorkflowStepExecution
	if err := fixture.app.db.Where("workflow_run_id = ? AND step_id = ?", run.ID, tc.stepID).Take(&step).Error; err != nil {
		t.Fatalf("load workflow step: %v", err)
	}
	if step.Status != workflowStepStatusFailed || step.ErrorCode == nil || *step.ErrorCode != "provider_invalid_output" {
		t.Fatalf("workflow step = %#v", step)
	}
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
	assertWorkflowBillingCounts(t, fixture.app, fixture.user.ID, 0, 0, 0)
}

func TestFrameQualityGateNarrativeRefineInvalidProviderOutputFailsWithoutCompletedCharge(t *testing.T) {
	tc := frameQualityGateEndpointCases()[1]
	fixture := newFrameQualityGateFixture(t, tc, false, false, `{"passed":true,"issues":[],"narrative_result":[]}`)
	run := createWorkflowIdentityTestWebRun(t, fixture.app, fixture.user.ID, fixture.project.ID, "quality-gate-narrative-invalid-output", "quality-gate-client")
	payload := cloneAnyMap(tc.payload)
	payload["operation"] = "refine"
	payload["issues"] = []any{map[string]any{"code": "narrative_mismatch", "severity": "error", "message": "refine narrative"}}
	payload["project_id"] = fixture.project.ID.String()
	payload["workflow_run_id"] = run.ID.String()
	payload["workflow_step_id"] = tc.stepID
	payload["workflow_input_hash"] = run.InputHash
	payload["client_instance_id"] = run.ClientInstanceID

	resp := request(fixture.app, http.MethodPost, tc.path, payload, fixture.token)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"code":"provider_invalid_output"`) {
		t.Fatalf("narrative invalid output response = %d %s", resp.Code, resp.Body.String())
	}
	var step models.WorkflowStepExecution
	if err := fixture.app.db.Where("workflow_run_id = ? AND step_id = ?", run.ID, tc.stepID).Take(&step).Error; err != nil {
		t.Fatalf("load narrative workflow step: %v", err)
	}
	if step.Status != workflowStepStatusFailed || step.ErrorCode == nil || *step.ErrorCode != "provider_invalid_output" {
		t.Fatalf("narrative workflow step = %#v", step)
	}
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
	assertWorkflowBillingCounts(t, fixture.app, fixture.user.ID, 0, 0, 0)
}

type frameQualityGateFixture struct {
	app     *App
	token   string
	user    models.User
	project models.Project
	calls   atomic.Int32
	model   string
}

func newFrameQualityGateFixture(t *testing.T, tc frameQualityGateEndpointCase, failProvider, omitUsage bool, contentOverride string) *frameQualityGateFixture {
	t.Helper()
	fixture := &frameQualityGateFixture{model: "quality-gate-pro-model"}
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.calls.Add(1)
		var requestPayload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&requestPayload); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		if requestPayload["model"] != fixture.model {
			t.Fatalf("model = %#v, want %q", requestPayload["model"], fixture.model)
		}
		if failProvider {
			http.Error(w, "simulated quality gate provider failure", http.StatusBadRequest)
			return
		}
		result := contentOverride
		if result == "" {
			result = defaultFrameQualityGateProviderOutput(tc.resultKey)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"quality-gate-request-1\",\"model\":\"quality-gate-pro-model\",\"choices\":[{\"delta\":{\"reasoning_content\":\"checking constraints\"}}]}\n\n")
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"quality-gate-request-1\",\"model\":%q,\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", fixture.model, result)
		if !omitUsage {
			_, _ = io.WriteString(w, "data: {\"id\":\"quality-gate-request-1\",\"model\":\"quality-gate-pro-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":60,\"completion_tokens\":40,\"total_tokens\":100}}\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(providerServer.Close)

	app := newTestApp(t)
	app.cfg.OpenAIProAPIBaseURL = providerServer.URL
	app.cfg.OpenAIProModel = fixture.model
	app.openai = providers.NewOpenAIClient(config.Config{OpenAIProAPIKey: "quality-gate-key", OpenAIProAPIBaseURL: providerServer.URL, OpenAIProModel: fixture.model, OpenAITemperature: 0.1}, app.promptBuilder)
	seedBillingRates(t, app, map[string]any{"llm": []map[string]any{{"provider": "openai", "model_id": fixture.model, "tokens_per_credit": 1}}})
	email := "quality-gate-" + uuid.NewString() + "@example.com"
	fixture.app = app
	fixture.token = registerAndLoginWithCredits(t, app, email, 10000)
	fixture.user = lookupUserByEmail(t, app, email)
	fixture.project = createTestProject(t, app, fixture.token, "Quality gate")
	return fixture
}

func defaultFrameQualityGateProviderOutput(resultKey string) string {
	if resultKey == "decision_result" {
		return `{"passed":true,"issues":[],"decision_result":{"summary":"reviewed","needs_timeline_view":true,"decisions":[],"skipped":[],"confidence":"medium","global_notes":[]}}`
	}
	return `{"passed":true,"issues":[],"narrative_result":{"summary":"reviewed","plot":{},"characters":[],"relationships":[],"emotion_curve":[],"events":[],"open_questions":[]}}`
}
