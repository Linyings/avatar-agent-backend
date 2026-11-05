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

type timelineWorkflowEndpointCase struct {
	name              string
	path              string
	stepID            string
	payload           map[string]any
	omitUsage         bool
	providerRequestID string
	provider          string
	serviceKind       string
	model             string
	usage             providers.ChatUsage
}

type timelineWorkflowFixture struct {
	app    *App
	token  string
	user   models.User
	calls  atomic.Int32
	leaked atomic.Bool
}

func TestTimelineToolWorkflowIdentityFinalizesBeforeSuccessResponse(t *testing.T) {
	for _, tc := range timelineWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTimelineWorkflowFixture(t, tc, false)
			run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "timeline-success-input", "timeline-success-client")
			recorder := &workflowResponseGateRecorder{
				ResponseRecorder: httptest.NewRecorder(),
				onFirstWrite: func() error {
					return verifyTimelineWorkflowFinalized(fixture.app, run.ID, tc)
				},
			}

			serveTimelineWorkflowRequest(fixture.app, recorder, tc.path, withTimelineWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if !recorder.checked {
				t.Fatal("handler did not write a response")
			}
			if recorder.checkErr != nil {
				t.Fatalf("workflow state at first response write: %v", recorder.checkErr)
			}
			if fixture.calls.Load() != 1 {
				t.Fatalf("provider calls = %d, want 1", fixture.calls.Load())
			}
			if fixture.leaked.Load() {
				t.Fatal("workflow identity leaked into provider payload")
			}
		})
	}
}

func TestTimelineToolWorkflowIdentityRejectsPartialBeforeProviderCall(t *testing.T) {
	for _, tc := range timelineWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTimelineWorkflowFixture(t, tc, false)
			payload := cloneAnyMap(tc.payload)
			payload["workflow_run_id"] = uuid.NewString()

			resp := request(fixture.app, http.MethodPost, tc.path, payload, fixture.token)

			assertVisionWorkflowErrorResponse(t, resp, http.StatusBadRequest, "partial_workflow_identity")
			if fixture.calls.Load() != 0 {
				t.Fatalf("provider calls = %d, want 0", fixture.calls.Load())
			}
		})
	}
}

func TestTimelineToolWorkflowIdentityRejectsActiveDuplicateBeforeProviderCall(t *testing.T) {
	for _, tc := range timelineWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTimelineWorkflowFixture(t, tc, false)
			run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "timeline-duplicate-input", "timeline-duplicate-client")
			identity := workflowRequestIdentityForTest(run.ID, tc.stepID, run.InputHash, run.ClientInstanceID)
			if _, err := fixture.app.resolveWorkflowRequestIdentity(t.Context(), fixture.user.ID, nil, identity); err != nil {
				t.Fatalf("seed active execution: %v", err)
			}

			resp := request(fixture.app, http.MethodPost, tc.path, withTimelineWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

			assertVisionWorkflowErrorResponse(t, resp, http.StatusConflict, "step_execution_in_progress")
			if fixture.calls.Load() != 0 {
				t.Fatalf("provider calls = %d, want 0", fixture.calls.Load())
			}
			assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
		})
	}
}

func TestTimelineToolWorkflowIdentityInsufficientCreditsIsStructuredAndBlocked(t *testing.T) {
	for _, tc := range timelineWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTimelineWorkflowFixture(t, tc, false)
			run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "timeline-credits-input", "timeline-credits-client")
			if err := fixture.app.db.Model(&models.UserTokenAccount{}).
				Where("user_id = ?", fixture.user.ID).
				Update("used_tokens", 10000).Error; err != nil {
				t.Fatalf("drain credits: %v", err)
			}

			resp := request(fixture.app, http.MethodPost, tc.path, withTimelineWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

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

func TestTimelineToolWorkflowIdentityFinalInsufficientCreditsBlocksRunAndStep(t *testing.T) {
	for _, tc := range timelineWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			tc.usage = providers.ChatUsage{PromptTokens: 6000, CompletionTokens: 6000, TotalTokens: 12000}
			fixture := newTimelineWorkflowFixture(t, tc, false)
			run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "timeline-final-credits-input", "timeline-final-credits-client")

			resp := request(fixture.app, http.MethodPost, tc.path, withTimelineWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

			assertVisionWorkflowErrorResponse(t, resp, http.StatusPaymentRequired, "insufficient_credits")
			if fixture.calls.Load() != 1 {
				t.Fatalf("provider calls = %d, want 1", fixture.calls.Load())
			}
			assertWorkflowProviderBlocked(t, fixture.app, run.ID, tc.stepID)
			assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
		})
	}
}

func TestTimelineToolWorkflowIdentityMissingProviderRequestIDFailsWithoutReservation(t *testing.T) {
	for _, tc := range timelineWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			tc.providerRequestID = ""
			fixture := newTimelineWorkflowFixture(t, tc, false)
			run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "timeline-missing-trace-input", "timeline-missing-trace-client")

			resp := request(fixture.app, http.MethodPost, tc.path, withTimelineWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

			assertVisionWorkflowErrorResponse(t, resp, http.StatusBadGateway, "provider_protocol_error")
			assertWorkflowProviderProtocolFailure(t, fixture.app, run.ID, tc.stepID)
			assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
		})
	}
}

func TestTimelineAnalysisWorkflowIdentityZeroUsageFailsWithoutReservation(t *testing.T) {
	tc := timelineWorkflowEndpointCaseByName(t, "analyze")
	tc.usage = providers.ChatUsage{}
	fixture := newTimelineWorkflowFixture(t, tc, false)
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "timeline-zero-usage-input", "timeline-zero-usage-client")

	resp := request(fixture.app, http.MethodPost, tc.path, withTimelineWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

	assertVisionWorkflowErrorResponse(t, resp, http.StatusBadGateway, "provider_protocol_error")
	assertWorkflowProviderProtocolFailure(t, fixture.app, run.ID, tc.stepID)
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
}

func TestTimelineToolWorkflowIdentityProviderFailureFinalizesWithoutReservation(t *testing.T) {
	for _, tc := range timelineWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTimelineWorkflowFixture(t, tc, true)
			run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "timeline-failure-input", "timeline-failure-client")

			resp := request(fixture.app, http.MethodPost, tc.path, withTimelineWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

			assertVisionWorkflowErrorResponse(t, resp, http.StatusBadGateway, "provider_unavailable")
			if fixture.calls.Load() != 1 {
				t.Fatalf("provider calls = %d, want 1", fixture.calls.Load())
			}
			if strings.Contains(resp.Body.String(), "simulated timeline provider failure") {
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

func TestTimelineDecideWorkflowIdentityMissingUsageFailsWithoutReservation(t *testing.T) {
	tc := timelineWorkflowEndpointCaseByName(t, "decide")
	tc.omitUsage = true
	fixture := newTimelineWorkflowFixture(t, tc, false)
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "timeline-missing-usage-input", "timeline-missing-usage-client")

	resp := request(fixture.app, http.MethodPost, tc.path, withTimelineWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

	assertVisionWorkflowErrorResponse(t, resp, http.StatusBadGateway, "provider_usage_missing")
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
	assertWorkflowBillingCounts(t, fixture.app, fixture.user.ID, 0, 0, 0)
}

func TestTimelineDecideSucceededReplayUsesOriginalUsageWhenProviderOmitsUsage(t *testing.T) {
	tc := timelineWorkflowEndpointCaseByName(t, "decide")
	tc.omitUsage = true
	fixture := newTimelineWorkflowFixture(t, tc, false)
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "timeline-replay-usage-input", "timeline-replay-usage-client")
	identity := workflowRequestIdentityForTest(run.ID, tc.stepID, run.InputHash, run.ClientInstanceID)
	execution, err := fixture.app.resolveWorkflowRequestIdentity(t.Context(), fixture.user.ID, nil, identity)
	if err != nil {
		t.Fatalf("resolve initial execution: %v", err)
	}
	if err := fixture.app.finalizeProviderSuccess(t.Context(), execution, providerSuccessFinalization{
		ProviderRequestID: optionalStringPointer(tc.providerRequestID),
		Consumption: creditConsumption{
			UserID:      fixture.user.ID,
			CreditCount: tc.usage.TotalTokens,
			APIEndpoint: tc.path,
			Provider:    tc.provider,
			ServiceKind: tc.serviceKind,
			ModelID:     tc.model,
			Usage:       tc.usage,
			RequestMetadata: map[string]any{
				"request_id": tc.providerRequestID,
			},
		},
	}); err != nil {
		t.Fatalf("seed successful execution: %v", err)
	}

	resp := request(fixture.app, http.MethodPost, tc.path, withTimelineWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), fmt.Sprintf("\"total_tokens\":%d", tc.usage.TotalTokens)) ||
		!strings.Contains(resp.Body.String(), fmt.Sprintf("\"tokens_consumed\":%d", tc.usage.TotalTokens)) {
		t.Fatalf("replay did not expose original usage: %s", resp.Body.String())
	}
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 1)
}

func TestTimelineToolWorkflowIdentityValidationPrecedesProviderConfiguration(t *testing.T) {
	for _, tc := range timelineWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTimelineWorkflowFixture(t, tc, false)
			switch tc.name {
			case "analyze":
				fixture.app.dashscope = providers.NewDashScopeClient(config.Config{})
			case "decide":
				fixture.app.cfg.OpenAIFlashAPIKey = ""
				fixture.app.cfg.OpenAIFlashAPIBaseURL = ""
				fixture.app.cfg.OpenAIFlashModel = ""
				fixture.app.openai = providers.NewOpenAIClient(config.Config{}, fixture.app.promptBuilder)
			default:
				t.Fatalf("unknown timeline endpoint case %q", tc.name)
			}
			run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "timeline-invalid-identity-input", "timeline-invalid-identity-client")
			payload := withTimelineWorkflowIdentity(tc.payload, run, tc.stepID)
			payload["workflow_input_hash"] = strings.Repeat("f", 64)

			resp := request(fixture.app, http.MethodPost, tc.path, payload, fixture.token)

			assertVisionWorkflowErrorResponse(t, resp, http.StatusConflict, "workflow_input_mismatch")
			if fixture.calls.Load() != 0 {
				t.Fatalf("provider calls = %d, want 0", fixture.calls.Load())
			}
			assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
		})
	}
}

func TestTimelineToolLegacyRequestsStillBillImmediately(t *testing.T) {
	for _, tc := range timelineWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTimelineWorkflowFixture(t, tc, false)

			resp := request(fixture.app, http.MethodPost, tc.path, cloneAnyMap(tc.payload), fixture.token)

			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
			}
			if fixture.calls.Load() != 1 {
				t.Fatalf("provider calls = %d, want 1", fixture.calls.Load())
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

func newTimelineWorkflowFixture(t *testing.T, tc timelineWorkflowEndpointCase, failProvider bool) *timelineWorkflowFixture {
	t.Helper()
	fixture := &timelineWorkflowFixture{}
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		for _, key := range workflowRequestIdentityKeys {
			if bytes.Contains(body, []byte(key)) {
				fixture.leaked.Store(true)
			}
		}
		if failProvider {
			http.Error(w, "simulated timeline provider failure", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch tc.name {
		case "analyze":
			_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"model\":%q,\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"timeline analysis\\\"}\"}}]}\n\n", tc.providerRequestID, tc.model)
			_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"model\":%q,\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d,\"total_tokens\":%d}}\n\n", tc.providerRequestID, tc.model, tc.usage.PromptTokens, tc.usage.CompletionTokens, tc.usage.TotalTokens)
		case "decide":
			if tc.omitUsage {
				_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"model\":%q,\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"timeline decision\\\",\\\"needs_timeline_view\\\":true,\\\"decisions\\\":[],\\\"skipped\\\":[],\\\"confidence\\\":\\\"high\\\",\\\"global_notes\\\":[]}\"}}]}\n\n", tc.providerRequestID, tc.model)
			} else {
				_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"model\":%q,\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"timeline decision\\\",\\\"needs_timeline_view\\\":true,\\\"decisions\\\":[],\\\"skipped\\\":[],\\\"confidence\\\":\\\"high\\\",\\\"global_notes\\\":[]}\"}}],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d,\"total_tokens\":%d}}\n\n", tc.providerRequestID, tc.model, tc.usage.PromptTokens, tc.usage.CompletionTokens, tc.usage.TotalTokens)
			}
		default:
			t.Fatalf("unknown timeline endpoint case %q", tc.name)
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(providerServer.Close)

	app := newTestApp(t)
	switch tc.name {
	case "analyze":
		app.cfg.QwenVisionModel = tc.model
		app.dashscope = providers.NewDashScopeClient(config.Config{
			DashScopeAPIKey:   "dash-test-key",
			QwenVisionModel:   tc.model,
			QwenVisionBaseURL: providerServer.URL,
		})
	case "decide":
		app.cfg.OpenAIFlashAPIBaseURL = providerServer.URL
		app.cfg.OpenAIFlashModel = tc.model
		app.openai = providers.NewOpenAIClient(config.Config{
			OpenAIFlashAPIKey:     "openai-test-key",
			OpenAIFlashAPIBaseURL: providerServer.URL,
			OpenAIFlashModel:      tc.model,
			OpenAITemperature:     0.1,
		}, app.promptBuilder)
	}
	seedBillingRates(t, app, map[string]any{
		tc.serviceKind: []map[string]any{{
			"provider":          tc.provider,
			"model_id":          tc.model,
			"tokens_per_credit": 1,
		}},
	})
	email := "timeline-workflow-" + tc.name + "-" + uuid.NewString() + "@example.com"
	fixture.app = app
	fixture.token = registerAndLoginWithCredits(t, app, email, 10000)
	fixture.user = lookupUserByEmail(t, app, email)
	return fixture
}

func timelineWorkflowEndpointCases() []timelineWorkflowEndpointCase {
	return []timelineWorkflowEndpointCase{
		{
			name:              "analyze",
			path:              "/api/v1/tools/timeline-view/analyze",
			stepID:            "timeline.analyze",
			providerRequestID: "timeline-analyze-request-1",
			provider:          "dashscope",
			serviceKind:       "vision",
			model:             "qwen3.6-plus",
			usage:             providers.ChatUsage{PromptTokens: 12, CompletionTokens: 8, TotalTokens: 20},
			payload: map[string]any{
				"image_oss_url": "oss://dashscope/tmp/timeline.png",
				"start":         1.0,
				"end":           3.5,
				"n_frames":      6,
			},
		},
		{
			name:              "decide",
			path:              "/api/v1/tools/timeline-view/decide",
			stepID:            "timeline.decide",
			providerRequestID: "timeline-decide-request-1",
			provider:          "openai",
			serviceKind:       "llm",
			model:             "openai-timeline-model",
			usage:             providers.ChatUsage{PromptTokens: 5, CompletionTokens: 6, TotalTokens: 11},
			payload: map[string]any{
				"segments":       []map[string]any{{"start": 1, "end": 3, "text": "pause then turn"}},
				"candidate_cuts": []map[string]any{{"start": 1, "end": 3, "reason": "silence"}},
				"max_decisions":  2,
			},
		},
	}
}

func timelineWorkflowEndpointCaseByName(t *testing.T, name string) timelineWorkflowEndpointCase {
	t.Helper()
	for _, tc := range timelineWorkflowEndpointCases() {
		if tc.name == name {
			return tc
		}
	}
	t.Fatalf("unknown timeline endpoint case %q", name)
	return timelineWorkflowEndpointCase{}
}

func withTimelineWorkflowIdentity(payload map[string]any, run models.WorkflowRun, stepID string) map[string]any {
	requestPayload := cloneAnyMap(payload)
	requestPayload["workflow_run_id"] = run.ID.String()
	requestPayload["workflow_step_id"] = stepID
	requestPayload["workflow_input_hash"] = run.InputHash
	requestPayload["client_instance_id"] = run.ClientInstanceID
	return requestPayload
}

func verifyTimelineWorkflowFinalized(app *App, runID uuid.UUID, tc timelineWorkflowEndpointCase) error {
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
	if reservation.APIEndpoint != tc.path || reservation.Provider != tc.provider || reservation.ModelID != tc.model || reservation.CreditCount != tc.usage.TotalTokens {
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
	for _, forbidden := range []string{"image_oss_url", "prompt", "segments", "candidate_cuts", "content", "analysis"} {
		if _, exists := metadata[forbidden]; exists {
			return fmt.Errorf("reservation metadata retained %q: %#v", forbidden, metadata)
		}
	}
	return nil
}

func serveTimelineWorkflowRequest(app *App, writer http.ResponseWriter, path string, body any, token string) {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	app.Router().ServeHTTP(writer, req)
}
