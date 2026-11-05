package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
)

const generalPlanStep = "generate-narration-script.general-v2.plan-0"
const generalDraftEndpoint = "/api/v1/tools/generate-narration-draft-stream"

func TestGeneralLayoutRepairIdentity(t *testing.T) {
	for _, purpose := range []string{"original-layout", "editorial-review"} {
		if !generalNarrationStep.MatchString("match-narration-visuals.general-v3." + purpose) {
			t.Fatal("layout repair must have input-versioned execution identity")
		}
		if generalNarrationStep.MatchString("generate-voiceover.general-v3." + purpose) {
			t.Fatal("layout repair leaked to another stage")
		}
	}
}

func TestGeneralWorkflowTimingRepairIdentityIsBounded(t *testing.T) {
	for _, phase := range []string{"fit-text", "review-text"} {
		for _, suffix := range []string{"", "-1", "-timing-1", "-timing-2-1"} {
			if !generalWorkflowStep.MatchString("generate-voiceover.general-v2." + phase + "-12" + suffix) {
				t.Fatal("timing rewrite must retain versioned retry identity")
			}
		}
		for _, suffix := range []string{"-2", "-1-extra", "-timing-3", "-timing-2-2"} {
			if generalWorkflowStep.MatchString("generate-voiceover.general-v2." + phase + "-12" + suffix) {
				t.Fatal("unbounded timing retry accepted")
			}
		}
	}
	for _, suffix := range []string{"", "-timing-1", "-timing-2"} {
		if !generalFitVoiceStep.MatchString("generate-voiceover.general-v2.fit-voice-12" + suffix) {
			t.Fatal("valid voice calibration missing")
		}
	}
	if generalFitVoiceStep.MatchString("generate-voiceover.general-v2.fit-voice-12-timing-3") {
		t.Fatal("unbounded voice calibration")
	}
}

func TestGeneralWorkflowDoesNotReplayResultFromObsoletePrompt(t *testing.T) {
	f := newStreamToolWorkflowFixture(t, false, false)
	run := createWorkflowIdentityTestRun(t, f.app, f.user.ID, "general-prompt", "general-client")
	req := generalIdentityPayload()
	ctx := req["video_context"].(map[string]any)
	ctx["purpose"] = "ground"
	ctx["unit"] = map[string]any{"id": 4}
	base := "match-narration-visuals.general-v2.ground-4-2"
	oldHash := workflowInputFingerprint(map[string]any{
		"version": "general-request-v1", "request": req, "kind": "script_draft",
		"profile": f.app.openai.WorkflowProfileFingerprint("script_draft", req),
	})
	oldID := base + ".r." + oldHash[:32]
	old := models.WorkflowStepExecution{WorkflowRunID: run.ID, StepID: oldID, InputHash: run.InputHash,
		ExecutionKey: workflowStepExecutionKey(run.ID, oldID, run.InputHash), Status: "succeeded", ProviderInputHash: &oldHash,
		Result: jsonMap(map[string]any{"metadata": map[string]any{"structured_result": map[string]any{"id": 1, "verified": true, "issues": []any{}}}})}
	if err := f.app.db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	payload := withStreamToolWorkflowIdentity(req, run, base)
	for i := 0; i < 2; i++ {
		resp := request(f.app, http.MethodPost, generalDraftEndpoint, payload, f.token)
		if resp.Code != 200 || !strings.Contains(resp.Body.String(), "event:completed") {
			t.Fatalf("%d %s", resp.Code, resp.Body.String())
		}
	}
	if f.calls.Load() != 1 {
		t.Fatalf("new prompt must call once and then replay its own result, calls=%d", f.calls.Load())
	}
	var retained models.WorkflowStepExecution
	if err := f.app.db.First(&retained, "id = ?", old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if string(retained.Result) != string(old.Result) || retained.Status != old.Status {
		t.Fatal("migration overwrote the historical response")
	}
}

func generalIdentityPayload() map[string]any {
	return map[string]any{
		"model_profile": "pro", "model_operation": "narration_outline",
		"video_context": map[string]any{"contract": "general-editing-v2", "revision": "20261018.5", "purpose": "plan", "evidence": "same source"},
	}
}

func TestGeneralNarrationV3VersionsAndReplays(t *testing.T) {
	f := newStreamToolWorkflowFixture(t, false, false)
	run := createWorkflowIdentityTestRun(t, f.app, f.user.ID, "general-v3", "general-client")
	payload := withStreamToolWorkflowIdentity(map[string]any{
		"model_profile": "pro", "model_operation": "narration_outline",
		"video_context": map[string]any{"contract": "general-narration-v3", "purpose": "script", "context": "first"},
	}, run, "generate-narration-script.general-v3.script")
	for i := 0; i < 4; i++ {
		if i == 2 {
			payload["video_context"].(map[string]any)["context"] = "changed"
		}
		resp := request(f.app, http.MethodPost, generalDraftEndpoint, payload, f.token)
		if resp.Code != 200 || !strings.Contains(resp.Body.String(), "event:completed") {
			t.Fatalf("%d %s", resp.Code, resp.Body.String())
		}
	}
	if f.calls.Load() != 2 {
		t.Fatalf("provider calls=%d, expected one per distinct input", f.calls.Load())
	}
}

func TestGeneralEvidenceSubstepsUseVersionedIdentity(t *testing.T) {
	for _, step := range []string{"generate-voiceover.general-v3.chapters", "match-narration-visuals.general-v3.match-review", "match-narration-visuals.general-v3.retrieve"} {
		if !generalNarrationStep.MatchString(step) {
			t.Fatalf("unversioned evidence request %s", step)
		}
	}
}

func TestGeneralWorkflowVersionsRequestAndModelWithoutDuplicateBilling(t *testing.T) {
	f := newStreamToolWorkflowFixture(t, false, false)
	if err := f.app.db.Model(&models.SystemSetting{}).Where("key = ?", "billing_rates").Update("value", jsonMap(map[string]any{"llm": []map[string]any{
		{"provider": "openai", "model_id": "stream-tool-model", "tokens_per_credit": 1},
		{"provider": "openai", "model_id": "glm-test-model", "tokens_per_credit": 1},
	}})).Error; err != nil {
		t.Fatal(err)
	}
	run := createWorkflowIdentityTestRun(t, f.app, f.user.ID, "general-versions", "general-client")
	payload := withStreamToolWorkflowIdentity(generalIdentityPayload(), run, generalPlanStep)
	send := func() {
		t.Helper()
		resp := request(f.app, http.MethodPost, generalDraftEndpoint, payload, f.token)
		if resp.Code != 200 || !strings.Contains(resp.Body.String(), "event:completed") {
			t.Fatalf("%d %s", resp.Code, resp.Body.String())
		}
	}
	send()
	send()
	if f.calls.Load() != 1 {
		t.Fatal("identical retry called provider again")
	}
	payload["video_context"].(map[string]any)["revision"] = "next-contract"
	send()
	send()
	if f.calls.Load() != 2 {
		t.Fatal("changed contract was not isolated/deduplicated")
	}
	payload["video_context"].(map[string]any)["evidence"] = "updated evidence"
	send()
	send()
	if f.calls.Load() != 3 {
		t.Fatal("changed input was not isolated/deduplicated")
	}
	// Actual backend-selected model changes while the PC still asks for 'pro'.
	f.app.cfg.OpenAIProModel = "glm-test-model"
	f.app.openai = providers.NewOpenAIClient(f.app.cfg, f.app.promptBuilder)
	send()
	send()
	if f.calls.Load() != 4 {
		t.Fatal("model switch reused previous model or retry was duplicated")
	}
	var reservations int64
	f.app.db.Model(&models.CreditReservation{}).Where("workflow_run_id = ?", run.ID).Count(&reservations)
	if reservations != 4 {
		t.Fatalf("reservations=%d want 4", reservations)
	}
	var steps []models.WorkflowStepExecution
	f.app.db.Where("workflow_run_id = ?", run.ID).Find(&steps)
	if len(steps) != 4 {
		t.Fatalf("steps=%d want 4", len(steps))
	}
	for _, step := range steps {
		if !strings.HasPrefix(step.StepID, generalPlanStep+".r.") || len(step.StepID) > 128 || step.Status != "succeeded" {
			t.Fatalf("invalid derived step: %s %s", step.StepID, step.Status)
		}
	}
	if f.leaked.Load() {
		t.Fatal("workflow identity leaked into model prompt")
	}
}

func TestGeneralWorkflowMigratesLegacyCompletedStepButBlocksUncertainWork(t *testing.T) {
	for _, state := range []string{"succeeded", "running", "expired-running", "unknown"} {
		t.Run(state, func(t *testing.T) {
			f := newStreamToolWorkflowFixture(t, false, false)
			run := createWorkflowIdentityTestRun(t, f.app, f.user.ID, "general-old", "general-client")
			if err := f.app.db.Model(&run).Update("client_type", "pc").Error; err != nil {
				t.Fatal(err)
			}
			hash := strings.Repeat("a", 64)
			step := models.WorkflowStepExecution{WorkflowRunID: run.ID, StepID: generalPlanStep, InputHash: run.InputHash, ExecutionKey: workflowStepExecutionKey(run.ID, generalPlanStep, run.InputHash), Status: state, ProviderInputHash: &hash}
			if state == "running" || state == "expired-running" {
				step.Status = "running"
				lease := time.Now().Add(time.Minute)
				if state == "expired-running" {
					lease = time.Now().Add(-time.Minute)
				}
				step.LeaseExpiresAt = &lease
			}
			if state == "unknown" {
				step.Status = "failed"
				code := "workflow_result_unknown"
				step.ErrorCode = &code
			}
			if err := f.app.db.Create(&step).Error; err != nil {
				t.Fatal(err)
			}
			payload := withStreamToolWorkflowIdentity(generalIdentityPayload(), run, generalPlanStep)
			resp := request(f.app, http.MethodPost, generalDraftEndpoint, payload, f.token)
			if state == "succeeded" {
				if resp.Code != 200 || f.calls.Load() != 1 {
					t.Fatalf("legacy migration failed: %d %s", resp.Code, resp.Body.String())
				}
			} else {
				code := "workflow_result_unknown"
				if state == "running" {
					code = "workflow_step_running"
				}
				assertVisionWorkflowErrorResponse(t, resp, http.StatusConflict, code)
				if f.calls.Load() != 0 {
					t.Fatal("uncertain old call was duplicated")
				}
			}
			var unchanged models.WorkflowStepExecution
			if err := f.app.db.First(&unchanged, "id = ?", step.ID).Error; err != nil {
				t.Fatal(err)
			}
			if unchanged.Status != step.Status || *unchanged.ProviderInputHash != hash {
				t.Fatal("old execution was modified")
			}
		})
	}
}

func TestGeneralWorkflowIdentityRejectsWrongOwnerAndLeavesOtherModesAlone(t *testing.T) {
	f := newStreamToolWorkflowFixture(t, false, false)
	run := createWorkflowIdentityTestRun(t, f.app, f.user.ID, "general-owner", "general-client")
	id := &workflowRequestIdentity{WorkflowRunID: run.ID, WorkflowStepID: generalPlanStep, WorkflowInputHash: run.InputHash, ClientInstanceID: run.ClientInstanceID}
	req := generalIdentityPayload()
	if _, _, err := f.app.generalWorkflowIdentity(t.Context(), uuid.New(), nil, "script_draft", req, id, "old"); err == nil {
		t.Fatal("wrong owner accepted")
	}
	// Web remains on its original strict request-identity path.
	if err := f.app.db.Model(&run).Update("client_type", "web").Error; err != nil {
		t.Fatal(err)
	}
	got, fp, err := f.app.generalWorkflowIdentity(t.Context(), f.user.ID, nil, "script_draft", req, id, "old")
	if err != nil || got != id || fp != "old" {
		t.Fatalf("web changed: %v", err)
	}
	req["video_context"].(map[string]any)["contract"] = "movie"
	got, fp, err = f.app.generalWorkflowIdentity(t.Context(), f.user.ID, nil, "script_draft", req, id, "old")
	if err != nil || got != id || fp != "old" {
		t.Fatal("other contract changed")
	}
}

func TestGeneralMixedEditorialReviewIdentity(t *testing.T) {
	if !generalNarrationStep.MatchString("generate-narration-script.general-v3.editorial-review") {
		t.Fatal("editorial review identity rejected")
	}
	if generalNarrationStep.MatchString("generate-voiceover.general-v3.editorial-review") {
		t.Fatal("review accepted on wrong stage")
	}
}
