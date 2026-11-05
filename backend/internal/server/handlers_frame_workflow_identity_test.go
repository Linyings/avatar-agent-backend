package server

import (
	"bytes"
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

type frameWorkflowEndpointCase struct {
	name              string
	path              string
	stepID            string
	profile           string
	providerRequestID string
	model             string
	usage             providers.ChatUsage
	payload           map[string]any
	providerContent   string
	omitUsage         bool
}

type frameWorkflowFixture struct {
	app          *App
	token        string
	user         models.User
	project      models.Project
	calls        atomic.Int32
	leaked       atomic.Bool
	includeUsage atomic.Bool
}

func TestFrameWorkflowIdentityFinalizesBeforeSuccessResponse(t *testing.T) {
	for _, tc := range frameWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFrameWorkflowFixture(t, tc, false)
			run := createFrameWorkflowIdentityTestRun(t, fixture, "frame-success-input", "frame-success-client")
			if tc.name == "narrative" {
				recorder := &workflowStreamingEventGateRecorder{
					ResponseRecorder: httptest.NewRecorder(),
					onProviderResult: func() error {
						return verifyFrameWorkflowFinalized(fixture.app, run.ID, tc)
					},
				}
				serveFrameWorkflowRequest(fixture.app, recorder, tc.path, withFrameWorkflowIdentity(tc.payload, fixture.project.ID, run, tc.stepID), fixture.token)

				if recorder.Code != http.StatusOK {
					t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Snapshot())
				}
				if checked, checkErr := recorder.ResultCheck(); !checked {
					t.Fatal("narrative handler did not write a provider result")
				} else if checkErr != nil {
					t.Fatalf("workflow state at provider result write: %v", checkErr)
				}
			} else {
				recorder := &workflowResponseGateRecorder{
					ResponseRecorder: httptest.NewRecorder(),
					onFirstWrite: func() error {
						return verifyFrameWorkflowFinalized(fixture.app, run.ID, tc)
					},
				}
				serveFrameWorkflowRequest(fixture.app, recorder, tc.path, withFrameWorkflowIdentity(tc.payload, fixture.project.ID, run, tc.stepID), fixture.token)

				if recorder.Code != http.StatusOK {
					t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
				}
				if !recorder.checked {
					t.Fatal("handler did not write a response")
				}
				if recorder.checkErr != nil {
					t.Fatalf("workflow state at first response write: %v", recorder.checkErr)
				}
			}
			if fixture.calls.Load() != 1 {
				t.Fatalf("provider calls = %d, want 1", fixture.calls.Load())
			}
			if fixture.leaked.Load() {
				t.Fatal("workflow identity leaked into provider payload")
			}
			if !fixture.includeUsage.Load() {
				t.Fatal("OpenAI request did not force stream_options.include_usage")
			}
		})
	}
}

func TestFrameWorkflowIdentityRejectsPartialBeforeProviderCall(t *testing.T) {
	for _, tc := range frameWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFrameWorkflowFixture(t, tc, false)
			payload := frameWorkflowPayload(tc.payload, fixture.project.ID)
			payload["workflow_run_id"] = uuid.NewString()

			resp := request(fixture.app, http.MethodPost, tc.path, payload, fixture.token)

			assertVisionWorkflowErrorResponse(t, resp, http.StatusBadRequest, "partial_workflow_identity")
			if fixture.calls.Load() != 0 {
				t.Fatalf("provider calls = %d, want 0", fixture.calls.Load())
			}
		})
	}
}

func TestFrameWorkflowIdentityRejectsActiveDuplicateBeforeProviderCall(t *testing.T) {
	for _, tc := range frameWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFrameWorkflowFixture(t, tc, false)
			run := createFrameWorkflowIdentityTestRun(t, fixture, "frame-duplicate-input", "frame-duplicate-client")
			identity := workflowRequestIdentityForTest(run.ID, tc.stepID, run.InputHash, run.ClientInstanceID)
			if _, err := fixture.app.resolveWorkflowRequestIdentity(t.Context(), fixture.user.ID, &fixture.project.ID, identity); err != nil {
				t.Fatalf("seed active execution: %v", err)
			}

			resp := request(fixture.app, http.MethodPost, tc.path, withFrameWorkflowIdentity(tc.payload, fixture.project.ID, run, tc.stepID), fixture.token)

			duplicateCode := "step_execution_in_progress"
			if tc.name == "narrative" {
				duplicateCode = "workflow_step_running"
			}
			assertVisionWorkflowErrorResponse(t, resp, http.StatusConflict, duplicateCode)
			if fixture.calls.Load() != 0 {
				t.Fatalf("provider calls = %d, want 0", fixture.calls.Load())
			}
			assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
		})
	}
}

func TestFrameWorkflowIdentityInsufficientCreditsIsStructuredAndBlocked(t *testing.T) {
	for _, tc := range frameWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFrameWorkflowFixture(t, tc, false)
			run := createFrameWorkflowIdentityTestRun(t, fixture, "frame-credits-input", "frame-credits-client")
			if err := fixture.app.db.Model(&models.UserTokenAccount{}).
				Where("user_id = ?", fixture.user.ID).
				Update("used_tokens", 10000).Error; err != nil {
				t.Fatalf("drain credits: %v", err)
			}

			resp := request(fixture.app, http.MethodPost, tc.path, withFrameWorkflowIdentity(tc.payload, fixture.project.ID, run, tc.stepID), fixture.token)

			assertVisionWorkflowErrorResponse(t, resp, http.StatusPaymentRequired, "insufficient_credits")
			if fixture.calls.Load() != 0 {
				t.Fatalf("provider calls = %d, want 0", fixture.calls.Load())
			}
			var step models.WorkflowStepExecution
			if err := fixture.app.db.Where("workflow_run_id = ? AND step_id = ?", run.ID, tc.stepID).Take(&step).Error; err != nil {
				t.Fatalf("load blocked workflow step: %v", err)
			}
			if step.Status != workflowRunStatusBlocked || step.ErrorCode == nil || *step.ErrorCode != "insufficient_credits" {
				t.Fatalf("blocked workflow step = %#v", step)
			}
			assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
		})
	}
}

func TestFrameWorkflowIdentityBillingConfigurationFailureFinalizesStep(t *testing.T) {
	tc := frameWorkflowEndpointCaseByName(t, "subtitle")
	fixture := newFrameWorkflowFixture(t, tc, false)
	if err := fixture.app.db.Unscoped().Where("key = ?", "billing_rates").Delete(&models.SystemSetting{}).Error; err != nil {
		t.Fatalf("delete seeded billing rates: %v", err)
	}
	seedBillingRates(t, fixture.app, map[string]any{
		"llm": []map[string]any{{
			"provider":          "deepseek",
			"model_id":          "unrelated-model",
			"tokens_per_credit": 1,
		}},
	})
	run := createFrameWorkflowIdentityTestRun(t, fixture, "frame-billing-input", "frame-billing-client")

	resp := request(fixture.app, http.MethodPost, tc.path, withFrameWorkflowIdentity(tc.payload, fixture.project.ID, run, tc.stepID), fixture.token)

	assertVisionWorkflowErrorResponse(t, resp, http.StatusBadRequest, "billing_configuration_invalid")
	if fixture.calls.Load() != 0 {
		t.Fatalf("provider calls = %d, want 0", fixture.calls.Load())
	}
	var step models.WorkflowStepExecution
	if err := fixture.app.db.Where("workflow_run_id = ? AND step_id = ?", run.ID, tc.stepID).Take(&step).Error; err != nil {
		t.Fatalf("load failed workflow step: %v", err)
	}
	if step.Status != workflowStepStatusFailed || step.ErrorCode == nil || *step.ErrorCode != "billing_configuration_invalid" {
		t.Fatalf("billing configuration workflow step = %#v", step)
	}
}

func TestFrameWorkflowIdentityFinalInsufficientCreditsBlocksRunAndStep(t *testing.T) {
	for _, tc := range frameWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			tc.usage = providers.ChatUsage{PromptTokens: 6000, CompletionTokens: 6000, TotalTokens: 12000}
			fixture := newFrameWorkflowFixture(t, tc, false)
			run := createFrameWorkflowIdentityTestRun(t, fixture, "frame-final-credits-input", "frame-final-credits-client")

			resp := request(fixture.app, http.MethodPost, tc.path, withFrameWorkflowIdentity(tc.payload, fixture.project.ID, run, tc.stepID), fixture.token)

			assertFrameWorkflowPostCommitError(t, tc, resp, http.StatusPaymentRequired, "insufficient_credits")
			if fixture.calls.Load() != 1 {
				t.Fatalf("provider calls = %d, want 1", fixture.calls.Load())
			}
			assertWorkflowProviderBlocked(t, fixture.app, run.ID, tc.stepID)
			assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
		})
	}
}

func TestFrameWorkflowIdentityMissingProviderRequestIDFailsWithoutReservation(t *testing.T) {
	for _, tc := range frameWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			tc.providerRequestID = ""
			fixture := newFrameWorkflowFixture(t, tc, false)
			run := createFrameWorkflowIdentityTestRun(t, fixture, "frame-missing-trace-input", "frame-missing-trace-client")

			resp := request(fixture.app, http.MethodPost, tc.path, withFrameWorkflowIdentity(tc.payload, fixture.project.ID, run, tc.stepID), fixture.token)

			assertFrameWorkflowPostCommitError(t, tc, resp, http.StatusBadGateway, "provider_protocol_error")
			assertWorkflowProviderProtocolFailure(t, fixture.app, run.ID, tc.stepID)
			assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
		})
	}
}

func TestFrameWorkflowIdentityProviderFailureFinalizesWithoutReservation(t *testing.T) {
	for _, tc := range frameWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFrameWorkflowFixture(t, tc, true)
			run := createFrameWorkflowIdentityTestRun(t, fixture, "frame-failure-input", "frame-failure-client")

			resp := request(fixture.app, http.MethodPost, tc.path, withFrameWorkflowIdentity(tc.payload, fixture.project.ID, run, tc.stepID), fixture.token)

			assertFrameWorkflowPostCommitError(t, tc, resp, http.StatusBadGateway, "provider_unavailable")
			if fixture.calls.Load() != 1 {
				t.Fatalf("provider calls = %d, want 1", fixture.calls.Load())
			}
			if strings.Contains(resp.Body.String(), "simulated frame provider failure") {
				t.Fatalf("provider diagnostics leaked into response: %s", resp.Body.String())
			}
			var step models.WorkflowStepExecution
			if err := fixture.app.db.Where("workflow_run_id = ? AND step_id = ?", run.ID, tc.stepID).Take(&step).Error; err != nil {
				t.Fatalf("load failed workflow step: %v", err)
			}
			if step.Status != workflowStepStatusFailed || step.ErrorCode == nil || *step.ErrorCode != "provider_unavailable" {
				t.Fatalf("failed workflow step = %#v", step)
			}
			if step.ProviderRequestID != nil || string(step.Usage) != "{}" {
				t.Fatalf("provider failure retained success metadata: %#v", step)
			}
			assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
		})
	}
}

func TestFrameWorkflowSucceededReplayProviderFailurePreservesSuccess(t *testing.T) {
	for _, tc := range frameWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFrameWorkflowFixture(t, tc, true)
			run := createFrameWorkflowIdentityTestRun(t, fixture, "frame-replay-failure-input", "frame-replay-failure-client")
			identity := workflowRequestIdentityForTest(run.ID, tc.stepID, run.InputHash, run.ClientInstanceID)
			execution, err := fixture.app.resolveWorkflowRequestIdentity(t.Context(), fixture.user.ID, &fixture.project.ID, identity)
			if err != nil {
				t.Fatalf("resolve initial execution: %v", err)
			}
			requestMetadata := map[string]any{"request_id": tc.providerRequestID}
			if tc.name == "narrative" {
				narrativeRequest := cloneAnyMap(tc.payload)
				if _, exists := narrativeRequest["language"]; !exists {
					narrativeRequest["language"] = "zh"
				}
				requestMetadata[workflowInputFingerprintMetadataKey] = fixture.app.frameWorkflowNarrativeInputFingerprint(narrativeRequest)
			}
			if err := fixture.app.finalizeProviderSuccess(t.Context(), execution, providerSuccessFinalization{
				ProviderRequestID: optionalStringPointer(tc.providerRequestID),
				Consumption: creditConsumption{
					UserID:          fixture.user.ID,
					ProjectID:       &fixture.project.ID,
					CreditCount:     tc.usage.TotalTokens,
					APIEndpoint:     tc.path,
					Provider:        "openai",
					ServiceKind:     "llm",
					ModelID:         tc.model,
					Usage:           tc.usage,
					RequestMetadata: requestMetadata,
				},
			}); err != nil {
				t.Fatalf("seed successful execution: %v", err)
			}

			resp := request(fixture.app, http.MethodPost, tc.path, withFrameWorkflowIdentity(tc.payload, fixture.project.ID, run, tc.stepID), fixture.token)

			if tc.name == "narrative" {
				assertVisionWorkflowErrorResponse(t, resp, http.StatusConflict, "workflow_result_unknown")
				if fixture.calls.Load() != 0 {
					t.Fatalf("successful legacy result must not invoke provider again: %d", fixture.calls.Load())
				}
			} else {
				assertFrameWorkflowPostCommitError(t, tc, resp, http.StatusBadGateway, "provider_unavailable")
				if fixture.calls.Load() != 1 {
					t.Fatalf("provider calls = %d, want 1 replay attempt", fixture.calls.Load())
				}
			}
			var step models.WorkflowStepExecution
			if err := fixture.app.db.Where("workflow_run_id = ? AND step_id = ?", run.ID, tc.stepID).Take(&step).Error; err != nil {
				t.Fatalf("load replayed step: %v", err)
			}
			if step.Status != workflowRunStatusSucceeded || step.ProviderRequestID == nil || *step.ProviderRequestID != tc.providerRequestID {
				t.Fatalf("provider failure mutated successful replay step: %#v", step)
			}
			assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 1)
		})
	}
}

func TestFrameWorkflowLegacyRequestsStillBillImmediately(t *testing.T) {
	for _, tc := range frameWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFrameWorkflowFixture(t, tc, false)

			resp := request(fixture.app, http.MethodPost, tc.path, cloneAnyMap(tc.payload), fixture.token)

			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
			}
			if fixture.calls.Load() != 1 {
				t.Fatalf("provider calls = %d, want 1", fixture.calls.Load())
			}
			for _, event := range []string{"event:answer_delta", "event:completed"} {
				if !strings.Contains(resp.Body.String(), event) {
					t.Fatalf("legacy response missing %q: %s", event, resp.Body.String())
				}
			}
			var account models.UserTokenAccount
			if err := fixture.app.db.First(&account, "user_id = ?", fixture.user.ID).Error; err != nil {
				t.Fatalf("load account: %v", err)
			}
			if account.UsedTokens != tc.usage.TotalTokens {
				t.Fatalf("used credits = %d, want %d", account.UsedTokens, tc.usage.TotalTokens)
			}
			var reservationCount int64
			if err := fixture.app.db.Model(&models.CreditReservation{}).Count(&reservationCount).Error; err != nil {
				t.Fatalf("count reservations: %v", err)
			}
			if reservationCount != 0 {
				t.Fatalf("reservations = %d, want 0", reservationCount)
			}
		})
	}
}

func TestFrameNarrativeWorkflowIdentityMissingUsageFailsWithoutReservation(t *testing.T) {
	tc := frameWorkflowEndpointCaseByName(t, "narrative")
	tc.omitUsage = true
	fixture := newFrameWorkflowFixture(t, tc, false)
	run := createFrameWorkflowIdentityTestRun(t, fixture, "frame-missing-usage-input", "frame-missing-usage-client")

	resp := request(fixture.app, http.MethodPost, tc.path, withFrameWorkflowIdentity(tc.payload, fixture.project.ID, run, tc.stepID), fixture.token)

	assertFrameWorkflowPostCommitError(t, tc, resp, http.StatusBadGateway, "provider_usage_missing")
	if fixture.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", fixture.calls.Load())
	}
	var step models.WorkflowStepExecution
	if err := fixture.app.db.Where("workflow_run_id = ? AND step_id = ?", run.ID, tc.stepID).Take(&step).Error; err != nil {
		t.Fatalf("load failed workflow step: %v", err)
	}
	if step.Status != workflowStepStatusFailed || step.ErrorCode == nil || *step.ErrorCode != "provider_usage_missing" {
		t.Fatalf("missing usage workflow step = %#v", step)
	}
	if step.ProviderRequestID != nil || string(step.Usage) != "{}" {
		t.Fatalf("missing usage retained success metadata: %#v", step)
	}
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
}

func newFrameWorkflowFixture(t *testing.T, tc frameWorkflowEndpointCase, failProvider bool) *frameWorkflowFixture {
	t.Helper()
	fixture := &frameWorkflowFixture{}
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		for _, key := range workflowRequestIdentityKeys {
			if bytes.Contains(body, []byte(key)) {
				fixture.leaked.Store(true)
			}
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err == nil {
			if options, ok := payload["stream_options"].(map[string]any); ok && options["include_usage"] == true {
				fixture.includeUsage.Store(true)
			}
		}
		if failProvider {
			http.Error(w, "simulated frame provider failure", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"model\":%q,\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", tc.providerRequestID, tc.model, tc.providerContent)
		if !tc.omitUsage {
			_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"model\":%q,\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d,\"total_tokens\":%d}}\n\n", tc.providerRequestID, tc.model, tc.usage.PromptTokens, tc.usage.CompletionTokens, tc.usage.TotalTokens)
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(providerServer.Close)

	app := newTestApp(t)
	providerConfig := config.Config{OpenAITemperature: 0.1}
	switch tc.profile {
	case "flash":
		app.cfg.OpenAIFlashAPIBaseURL = providerServer.URL
		app.cfg.OpenAIFlashModel = tc.model
		providerConfig.OpenAIFlashAPIKey = "openai-test-key"
		providerConfig.OpenAIFlashAPIBaseURL = providerServer.URL
		providerConfig.OpenAIFlashModel = tc.model
	case "pro":
		app.cfg.OpenAIProAPIBaseURL = providerServer.URL
		app.cfg.OpenAIProModel = tc.model
		providerConfig.OpenAIProAPIKey = "openai-test-key"
		providerConfig.OpenAIProAPIBaseURL = providerServer.URL
		providerConfig.OpenAIProModel = tc.model
	default:
		t.Fatalf("unknown OpenAI profile %q", tc.profile)
	}
	app.openai = providers.NewOpenAIClient(providerConfig, app.promptBuilder)
	seedBillingRates(t, app, map[string]any{
		"llm": []map[string]any{{
			"provider":          "openai",
			"model_id":          tc.model,
			"tokens_per_credit": 1,
		}},
	})
	email := "frame-workflow-" + tc.name + "-" + uuid.NewString() + "@example.com"
	fixture.app = app
	fixture.token = registerAndLoginWithCredits(t, app, email, 10000)
	fixture.user = lookupUserByEmail(t, app, email)
	fixture.project = createTestProject(t, app, fixture.token, "Frame workflow "+tc.name)
	return fixture
}

func frameWorkflowEndpointCases() []frameWorkflowEndpointCase {
	return []frameWorkflowEndpointCase{
		{
			name:              "subtitle",
			path:              "/api/v1/tools/frame-workflow/subtitles/validate",
			stepID:            "frame.subtitles.validate",
			profile:           "flash",
			providerRequestID: "frame-subtitle-request-1",
			model:             "openai-frame-flash-model",
			usage:             providers.ChatUsage{PromptTokens: 20, CompletionTokens: 15, TotalTokens: 35},
			payload: map[string]any{
				"segments": []map[string]any{{"id": "1", "start": 0, "end": 1.2, "text": "secret subtitle input", "speaker_id": "speaker_1"}},
				"language": "zh",
			},
			providerContent: `{"summary":"subtitle secret result","corrections_count":1,"segments":[{"id":"1","start":0,"end":1.2,"text":"corrected secret subtitle","speaker_id":"speaker_0"}],"notes":[]}`,
		},
		{
			name:              "narrative",
			path:              "/api/v1/tools/frame-workflow/narrative-analyze",
			stepID:            "frame.narrative.analyze",
			profile:           "pro",
			providerRequestID: "frame-narrative-request-1",
			model:             "openai-frame-pro-model",
			usage:             providers.ChatUsage{PromptTokens: 60, CompletionTokens: 40, TotalTokens: 100},
			payload: map[string]any{
				"transcript":      "secret transcript narrative",
				"segments":        []map[string]any{{"start": 10.0, "end": 12.5, "text": "secret narrative segment", "speaker_id": "speaker_0"}},
				"visual_analyses": []map[string]any{{"start": 10.0, "end": 12.5, "summary": "secret visual analysis"}},
			},
			providerContent: `{"summary":"narrative secret result","characters":[],"relationships":[],"emotion_curve":[],"events":[{"start":10,"end":12.5,"description":"secret narrative event"}]}`,
		},
	}
}

func frameWorkflowEndpointCaseByName(t *testing.T, name string) frameWorkflowEndpointCase {
	t.Helper()
	for _, tc := range frameWorkflowEndpointCases() {
		if tc.name == name {
			return tc
		}
	}
	t.Fatalf("unknown frame endpoint case %q", name)
	return frameWorkflowEndpointCase{}
}

func assertFrameWorkflowPostCommitError(t *testing.T, tc frameWorkflowEndpointCase, resp *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if tc.name != "narrative" {
		assertVisionWorkflowErrorResponse(t, resp, status, code)
		return
	}
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want opened SSE status 200, body = %s", resp.Code, resp.Body.String())
	}
	if resp.Header().Get("X-Request-ID") == "" || !strings.Contains(resp.Body.String(), "event:error") ||
		!strings.Contains(resp.Body.String(), `"code":"`+code+`"`) || !strings.Contains(resp.Body.String(), `"retryable":`) {
		t.Fatalf("structured narrative SSE error = headers %#v body %s", resp.Header(), resp.Body.String())
	}
	for _, event := range []string{"event:thinking_delta", "event:answer_delta", "event:completed"} {
		if strings.Contains(resp.Body.String(), event) {
			t.Fatalf("provider result replayed before finalization: %s", resp.Body.String())
		}
	}
}

func createFrameWorkflowIdentityTestRun(t *testing.T, fixture *frameWorkflowFixture, inputHash, clientID string) models.WorkflowRun {
	t.Helper()
	return createWorkflowIdentityTestWebRun(t, fixture.app, fixture.user.ID, fixture.project.ID, inputHash, clientID)
}

func frameWorkflowPayload(payload map[string]any, projectID uuid.UUID) map[string]any {
	requestPayload := cloneAnyMap(payload)
	requestPayload["project_id"] = projectID.String()
	return requestPayload
}

func withFrameWorkflowIdentity(payload map[string]any, projectID uuid.UUID, run models.WorkflowRun, stepID string) map[string]any {
	requestPayload := frameWorkflowPayload(payload, projectID)
	requestPayload["workflow_run_id"] = run.ID.String()
	requestPayload["workflow_step_id"] = stepID
	requestPayload["workflow_input_hash"] = run.InputHash
	requestPayload["client_instance_id"] = run.ClientInstanceID
	return requestPayload
}

func verifyFrameWorkflowFinalized(app *App, runID uuid.UUID, tc frameWorkflowEndpointCase) error {
	var step models.WorkflowStepExecution
	if err := app.db.Where("workflow_run_id = ? AND step_id = ?", runID, tc.stepID).Take(&step).Error; err != nil {
		return fmt.Errorf("load step: %w", err)
	}
	if step.Status != workflowRunStatusSucceeded {
		return fmt.Errorf("step status = %q", step.Status)
	}
	if step.ProviderRequestID == nil || *step.ProviderRequestID != tc.providerRequestID {
		return fmt.Errorf("provider request id = %v", step.ProviderRequestID)
	}
	var usage providers.ChatUsage
	if err := json.Unmarshal(step.Usage, &usage); err != nil {
		return fmt.Errorf("decode step usage: %w", err)
	}
	if usage != tc.usage {
		return fmt.Errorf("step usage = %#v, want %#v", usage, tc.usage)
	}

	var reservations []models.CreditReservation
	if err := app.db.Where("workflow_run_id = ?", runID).Find(&reservations).Error; err != nil {
		return fmt.Errorf("load reservations: %w", err)
	}
	if len(reservations) != 1 {
		return fmt.Errorf("reservations = %d", len(reservations))
	}
	reservation := reservations[0]
	if reservation.Status != creditReservationPending || reservation.WorkflowStepExecutionID == nil || *reservation.WorkflowStepExecutionID != step.ID {
		return fmt.Errorf("reservation identity/status = %#v", reservation)
	}
	if reservation.APIEndpoint != tc.path || reservation.Provider != "openai" || reservation.ModelID != tc.model || reservation.CreditCount != tc.usage.TotalTokens {
		return fmt.Errorf("reservation billing metadata = %#v", reservation)
	}
	if reservation.PromptTokens != tc.usage.PromptTokens || reservation.CompletionTokens != tc.usage.CompletionTokens || reservation.TotalModelTokens != tc.usage.TotalTokens {
		return fmt.Errorf("reservation usage = %#v", reservation)
	}
	var metadata map[string]any
	if err := json.Unmarshal(reservation.RequestMetadata, &metadata); err != nil {
		return fmt.Errorf("decode reservation metadata: %w", err)
	}
	if metadata["request_id"] != tc.providerRequestID {
		return fmt.Errorf("reservation request metadata = %#v", metadata)
	}
	allowedMetadata := map[string]bool{
		"request_id":                      true,
		"billing_currency":                true,
		"billing_missing_rate":            true,
		"billing_model_id":                true,
		"billing_provider":                true,
		"billing_rate":                    true,
		"product_mode":                    true,
		"free_experience":                 true,
		"billing_service_kind":            true,
		"overdraft_credits_consumed":      true,
		"paid_purchased_credits_consumed": true,
		"purchased_credits_consumed":      true,
		"subscription_credits_consumed":   true,
	}
	if tc.name == "narrative" {
		allowedMetadata[workflowInputFingerprintMetadataKey] = true
		if workflowInputFingerprintFromMetadata(metadata) == "" {
			return fmt.Errorf("narrative reservation input fingerprint = %#v", metadata[workflowInputFingerprintMetadataKey])
		}
	}
	for key := range metadata {
		if !allowedMetadata[key] {
			return fmt.Errorf("reservation metadata retained unsupported key %q: %#v", key, metadata)
		}
	}
	stored, err := json.Marshal(struct {
		Step        models.WorkflowStepExecution
		Reservation models.CreditReservation
	}{Step: step, Reservation: reservation})
	if err != nil {
		return fmt.Errorf("marshal stored workflow data: %w", err)
	}
	for _, forbidden := range []string{
		`"subtitles"`, `"segments"`, `"transcript"`, `"visual_analyses"`, `"prompt"`, `"content"`,
		"secret subtitle input", "corrected secret subtitle", "secret transcript narrative", "secret narrative segment", "secret visual analysis", "narrative secret result",
	} {
		if bytes.Contains(stored, []byte(forbidden)) {
			return fmt.Errorf("stored workflow data contains business content %q: %s", forbidden, stored)
		}
	}
	return nil
}

func serveFrameWorkflowRequest(app *App, writer http.ResponseWriter, path string, body any, token string) {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	app.Router().ServeHTTP(writer, req)
}
