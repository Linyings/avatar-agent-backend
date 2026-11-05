package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
	"gorm.io/gorm"
)

type streamToolWorkflowEndpointCase struct {
	name    string
	kind    string
	path    string
	stepID  string
	payload map[string]any
}

type streamToolWorkflowFixture struct {
	app       *App
	token     string
	user      models.User
	calls     atomic.Int32
	leaked    atomic.Bool
	omitUsage bool
	fail      bool
	requestID string
	usage     providers.ChatUsage
}

var narrativeWorkflowEndpoint = streamToolWorkflowEndpointCase{
	name:   "narrative",
	path:   "/api/v1/tools/frame-workflow/narrative-analyze",
	stepID: "frame.narrative.analyze",
}

func narrativeWorkflowPayload() map[string]any {
	return map[string]any{
		"transcript": "narrative workflow source",
		"segments":   []map[string]any{{"start": 0.0, "end": 1.0, "text": "narrative workflow source"}},
	}
}

func TestFrameWorkflowNarrativeIdentityKeepsStreamAliveWithoutEarlyReplay(t *testing.T) {
	providerStarted := make(chan struct{})
	providerDeltaSent := make(chan struct{})
	releaseProvider := make(chan struct{})
	var providerStartedOnce sync.Once
	var providerDeltaSentOnce sync.Once
	var releaseProviderOnce sync.Once
	release := func() { releaseProviderOnce.Do(func() { close(releaseProvider) }) }
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerStartedOnce.Do(func() { close(providerStarted) })
		w.Header().Set("Content-Type", "text/event-stream")
		content := `{"summary":"narrative heartbeat result","events":[{"start":0,"end":1,"description":"narrative workflow source"}]}`
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"stream-tool-request-1\",\"model\":\"stream-tool-model\",\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", content)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		providerDeltaSentOnce.Do(func() { close(providerDeltaSent) })
		select {
		case <-releaseProvider:
		case <-r.Context().Done():
			return
		}

		_, _ = io.WriteString(w, "data: {\"id\":\"stream-tool-request-1\",\"model\":\"stream-tool-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":8,\"total_tokens\":20}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(providerServer.Close)

	fixture := newStreamToolWorkflowFixture(t, false, false)
	fixture.app.openai = providers.NewOpenAIClient(config.Config{
		OpenAIProAPIKey:     "openai-test-key",
		OpenAIProAPIBaseURL: providerServer.URL,
		OpenAIProModel:      "stream-tool-model",
		OpenAITemperature:   0.1,
	}, fixture.app.promptBuilder)
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "narrative-heartbeat", "narrative-heartbeat-client")
	payload := withStreamToolWorkflowIdentity(narrativeWorkflowPayload(), run, narrativeWorkflowEndpoint.stepID)
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal narrative payload: %v", err)
	}
	testContext, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	recorder := &workflowStreamingEventGateRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		onProviderResult: func() error {
			return verifyStreamToolWorkflowFinalized(fixture.app, run.ID, narrativeWorkflowEndpoint, fixture.usage)
		},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/frame-workflow/narrative-analyze", bytes.NewReader(raw)).WithContext(testContext)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+fixture.token)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		fixture.app.Router().ServeHTTP(recorder, req)
	}()
	t.Cleanup(func() {
		release()
		select {
		case <-requestDone:
		case <-time.After(time.Second):
		}
	})

	select {
	case <-providerStarted:
	case <-testContext.Done():
		t.Fatalf("narrative provider did not start: %v", testContext.Err())
	}
	if !waitForStreamBody(recorder, ": connected\n\n", time.Second) {
		t.Fatalf("narrative stream did not open: %s", recorder.Snapshot())
	}
	connectedFlushes := recorder.FlushCount()
	if connectedFlushes == 0 {
		t.Fatal("narrative connection became visible without a flush")
	}
	select {
	case <-providerDeltaSent:
	case <-testContext.Done():
		t.Fatalf("narrative provider did not emit a delta: %v", testContext.Err())
	}
	var initialStep models.WorkflowStepExecution
	if err := fixture.app.db.Where("workflow_run_id = ? AND step_id = ?", run.ID, narrativeWorkflowEndpoint.stepID).Take(&initialStep).Error; err != nil {
		t.Fatalf("load initial narrative step lease: %v", err)
	}
	select {
	case <-time.After(streamToolHeartbeatInterval):
	case <-testContext.Done():
		t.Fatalf("narrative stream did not reach heartbeat interval: %v", testContext.Err())
	}
	if !waitForStreamBody(recorder, ": heartbeat\n\n", time.Second) {
		t.Fatalf("narrative stream did not emit a heartbeat: %s", recorder.Snapshot())
	}
	if heartbeatFlushes := recorder.FlushCount(); heartbeatFlushes <= connectedFlushes {
		t.Fatalf("heartbeat did not cross a new flush boundary: connected=%d heartbeat=%d", connectedFlushes, heartbeatFlushes)
	}
	var refreshedStep models.WorkflowStepExecution
	leaseRefreshDeadline := time.Now().Add(time.Second)
	for time.Now().Before(leaseRefreshDeadline) {
		// The heartbeat updates SQLite concurrently with this observation. Retry
		// transient table locks instead of failing before the renewal is visible.
		if err := retryWorkflowWrite(testContext, func() error {
			return fixture.app.db.Where("id = ?", initialStep.ID).Take(&refreshedStep).Error
		}); err != nil {
			t.Fatalf("load refreshed narrative step lease: %v", err)
		}
		if initialStep.LeaseExpiresAt != nil && refreshedStep.LeaseExpiresAt != nil && refreshedStep.LeaseExpiresAt.After(*initialStep.LeaseExpiresAt) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if initialStep.LeaseExpiresAt == nil || refreshedStep.LeaseExpiresAt == nil || !refreshedStep.LeaseExpiresAt.After(*initialStep.LeaseExpiresAt) {
		t.Fatalf("provider step lease was not renewed: initial=%v refreshed=%v", initialStep.LeaseExpiresAt, refreshedStep.LeaseExpiresAt)
	}
	bodyBeforeProviderRelease := recorder.Snapshot()
	for _, forbidden := range []string{"event:thinking_delta", "event:answer_delta", "event:completed"} {
		if strings.Contains(bodyBeforeProviderRelease, forbidden) {
			t.Fatalf("provider output escaped before finalization: %s", bodyBeforeProviderRelease)
		}
	}

	release()
	select {
	case <-requestDone:
	case <-testContext.Done():
		t.Fatalf("narrative stream did not finish: %v", testContext.Err())
	}
	if checked, checkErr := recorder.ResultCheck(); !checked || checkErr != nil {
		t.Fatalf("workflow was not finalized before provider output: checked=%t err=%v body=%s", checked, checkErr, recorder.Snapshot())
	}
}

func TestFrameWorkflowNarrativeIdentityProviderFailureEmitsStructuredSSEError(t *testing.T) {
	fixture := newStreamToolWorkflowFixture(t, true, false)
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "narrative-provider-failure", "narrative-provider-failure-client")
	resp := &committedHeaderRecorder{ResponseRecorder: httptest.NewRecorder()}

	serveStreamToolWorkflowRequest(fixture.app, resp, narrativeWorkflowEndpoint.path, withStreamToolWorkflowIdentity(narrativeWorkflowPayload(), run, narrativeWorkflowEndpoint.stepID), fixture.token)

	assertStreamToolWorkflowSSEError(t, resp.ResponseRecorder, "provider_unavailable")
	if got := resp.Result().Header.Get("X-Request-ID"); got == "" {
		t.Fatalf("committed narrative SSE error response has no request id header: %#v", resp.Result().Header)
	}
	if strings.Contains(resp.Body.String(), "simulated stream provider failure") {
		t.Fatalf("provider diagnostics leaked into narrative stream: %s", resp.Body.String())
	}
	assertStreamToolWorkflowStep(t, fixture.app, run.ID, narrativeWorkflowEndpoint.stepID, workflowStepStatusFailed, "provider_unavailable")
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
}

func TestFrameWorkflowNarrativeIdentityFinalizationFailureDoesNotReplayProviderOutput(t *testing.T) {
	fixture := newStreamToolWorkflowFixture(t, false, false)
	if err := fixture.app.db.Exec(`
		CREATE TRIGGER fail_narrative_finalization_reservation
		BEFORE INSERT ON credit_reservations
		BEGIN
			SELECT RAISE(ABORT, 'simulated narrative reservation failure');
		END;
	`).Error; err != nil {
		t.Fatalf("create narrative finalization failure trigger: %v", err)
	}
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "narrative-finalization-failure", "narrative-finalization-failure-client")

	resp := request(fixture.app, http.MethodPost, narrativeWorkflowEndpoint.path, withStreamToolWorkflowIdentity(narrativeWorkflowPayload(), run, narrativeWorkflowEndpoint.stepID), fixture.token)

	assertStreamToolWorkflowSSEError(t, resp, "workflow_result_unknown")
	for _, forbidden := range []string{"event:thinking_delta", "event:answer_delta", "event:completed"} {
		if strings.Contains(resp.Body.String(), forbidden) {
			t.Fatalf("provider output replayed after finalization failure: %s", resp.Body.String())
		}
	}
	assertStreamToolWorkflowStep(t, fixture.app, run.ID, narrativeWorkflowEndpoint.stepID, workflowRunStatusRunning, "workflow_result_unknown")
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
}

func TestFrameWorkflowNarrativeIdentitySucceededReplayKeepsOneReservation(t *testing.T) {
	fixture := newStreamToolWorkflowFixture(t, false, false)
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "narrative-replay", "narrative-replay-client")
	payload := withStreamToolWorkflowIdentity(narrativeWorkflowPayload(), run, narrativeWorkflowEndpoint.stepID)

	first := request(fixture.app, http.MethodPost, narrativeWorkflowEndpoint.path, cloneAnyMap(payload), fixture.token)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "event:completed") {
		t.Fatalf("initial narrative response = status %d body %s", first.Code, first.Body.String())
	}
	assertNarrativeWorkflowReservationFingerprint(t, fixture.app, run.ID)
	if err := fixture.app.db.Model(&models.UserTokenAccount{}).Where("user_id = ?", fixture.user.ID).Updates(map[string]any{
		"total_tokens": fixture.usage.TotalTokens,
		"used_tokens":  fixture.usage.TotalTokens,
	}).Error; err != nil {
		t.Fatalf("reduce replay balance below estimate: %v", err)
	}
	replay := request(fixture.app, http.MethodPost, narrativeWorkflowEndpoint.path, cloneAnyMap(payload), fixture.token)
	if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), "event:completed") {
		t.Fatalf("replayed narrative response = status %d body %s", replay.Code, replay.Body.String())
	}
	if fixture.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1 with saved-result replay", fixture.calls.Load())
	}
	assertStreamToolWorkflowStep(t, fixture.app, run.ID, narrativeWorkflowEndpoint.stepID, workflowRunStatusSucceeded, "")
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 1)
}

func TestFrameWorkflowNarrativeIdentitySucceededReplayRejectsChangedProviderInputBeforeProviderCall(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*streamToolWorkflowFixture, map[string]any)
	}{
		{
			name: "narrative payload",
			mutate: func(_ *streamToolWorkflowFixture, payload map[string]any) {
				payload["transcript"] = "changed narrative workflow source"
				payload["segments"] = []map[string]any{{"start": 0.0, "end": 2.0, "text": "changed narrative workflow source"}}
			},
		},
		{
			name: "effective pro profile",
			mutate: func(fixture *streamToolWorkflowFixture, _ map[string]any) {
				fixture.app.cfg.OpenAIProReasoningEffort = "high"
				fixture.app.openai = providers.NewOpenAIClient(fixture.app.cfg, fixture.app.promptBuilder)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newStreamToolWorkflowFixture(t, false, false)
			run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "narrative-replay-input-"+uuid.NewString(), "narrative-replay-input-client")
			payload := withStreamToolWorkflowIdentity(narrativeWorkflowPayload(), run, narrativeWorkflowEndpoint.stepID)
			first := request(fixture.app, http.MethodPost, narrativeWorkflowEndpoint.path, cloneAnyMap(payload), fixture.token)
			if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "event:completed") {
				t.Fatalf("initial narrative response = status %d body %s", first.Code, first.Body.String())
			}

			changed := cloneAnyMap(payload)
			tt.mutate(fixture, changed)
			replay := request(fixture.app, http.MethodPost, narrativeWorkflowEndpoint.path, changed, fixture.token)

			assertVisionWorkflowErrorResponse(t, replay, http.StatusConflict, "workflow_input_mismatch")
			if fixture.calls.Load() != 1 {
				t.Fatalf("provider calls = %d, want only initial call", fixture.calls.Load())
			}
			assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 1)
		})
	}
}

func TestFrameWorkflowNarrativeIdentitySucceededReplayRejectsUnusableReservationBeforeProviderCall(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *App, uuid.UUID)
	}{
		{
			name: "missing",
			mutate: func(t *testing.T, app *App, runID uuid.UUID) {
				t.Helper()
				if err := app.db.Where("workflow_run_id = ?", runID).Delete(&models.CreditReservation{}).Error; err != nil {
					t.Fatalf("delete narrative reservation: %v", err)
				}
			},
		},
		{
			name: "released",
			mutate: func(t *testing.T, app *App, runID uuid.UUID) {
				t.Helper()
				if err := app.db.Model(&models.CreditReservation{}).Where("workflow_run_id = ?", runID).Update("status", creditReservationReleased).Error; err != nil {
					t.Fatalf("release narrative reservation: %v", err)
				}
			},
		},
		{
			name: "corrupt metadata",
			mutate: func(t *testing.T, app *App, runID uuid.UUID) {
				t.Helper()
				if err := app.db.Model(&models.CreditReservation{}).Where("workflow_run_id = ?", runID).Update("request_metadata", "{").Error; err != nil {
					t.Fatalf("corrupt narrative reservation metadata: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newStreamToolWorkflowFixture(t, false, false)
			run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "narrative-replay-reservation-"+uuid.NewString(), "narrative-replay-reservation-client")
			payload := withStreamToolWorkflowIdentity(narrativeWorkflowPayload(), run, narrativeWorkflowEndpoint.stepID)
			first := request(fixture.app, http.MethodPost, narrativeWorkflowEndpoint.path, cloneAnyMap(payload), fixture.token)
			if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "event:completed") {
				t.Fatalf("initial narrative response = status %d body %s", first.Code, first.Body.String())
			}
			tt.mutate(t, fixture.app, run.ID)

			replay := request(fixture.app, http.MethodPost, narrativeWorkflowEndpoint.path, cloneAnyMap(payload), fixture.token)

			assertVisionWorkflowErrorResponse(t, replay, http.StatusInternalServerError, "workflow_request_error")
			if fixture.calls.Load() != 1 {
				t.Fatalf("provider calls = %d, want only initial call", fixture.calls.Load())
			}
		})
	}
}

func TestFrameWorkflowNarrativeLegacyStreamAndBillingRemainImmediate(t *testing.T) {
	fixture := newStreamToolWorkflowFixture(t, false, false)

	resp := request(fixture.app, http.MethodPost, narrativeWorkflowEndpoint.path, narrativeWorkflowPayload(), fixture.token)

	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "event:answer_delta") || !strings.Contains(resp.Body.String(), "event:completed") {
		t.Fatalf("legacy narrative response = status %d body %s", resp.Code, resp.Body.String())
	}
	var account models.UserTokenAccount
	if err := fixture.app.db.First(&account, "user_id = ?", fixture.user.ID).Error; err != nil {
		t.Fatalf("load legacy account: %v", err)
	}
	if account.UsedTokens != fixture.usage.TotalTokens {
		t.Fatalf("used credits = %d, want %d", account.UsedTokens, fixture.usage.TotalTokens)
	}
	var reservationCount int64
	if err := fixture.app.db.Model(&models.CreditReservation{}).Where("user_id = ?", fixture.user.ID).Count(&reservationCount).Error; err != nil {
		t.Fatalf("count legacy reservations: %v", err)
	}
	if reservationCount != 0 {
		t.Fatalf("legacy reservations = %d, want 0", reservationCount)
	}
}

func TestFrameWorkflowNarrativeLegacyBillingFailureFlushesProviderTailBeforeSSEError(t *testing.T) {
	fixture := newStreamToolWorkflowFixture(t, false, false)
	if err := fixture.app.db.Exec(`
		CREATE TRIGGER fail_legacy_narrative_billing
		BEFORE INSERT ON token_usage_records
		BEGIN
			SELECT RAISE(ABORT, 'simulated legacy narrative billing failure');
		END;
	`).Error; err != nil {
		t.Fatalf("create legacy narrative billing failure trigger: %v", err)
	}

	resp := request(fixture.app, http.MethodPost, narrativeWorkflowEndpoint.path, narrativeWorkflowPayload(), fixture.token)
	body := resp.Body.String()
	providerOutputAt := strings.Index(body, "event:answer_delta")
	errorAt := strings.Index(body, "event:error")
	if providerOutputAt < 0 || errorAt < 0 || providerOutputAt > errorAt {
		t.Fatalf("legacy narrative provider tail must precede SSE error: %s", body)
	}
}

func TestFrameWorkflowNarrativeLegacyFlushesProviderTailBeforeBillingCompletes(t *testing.T) {
	fixture := newStreamToolWorkflowFixture(t, false, false)
	billingStarted := make(chan struct{})
	releaseBilling := make(chan struct{})
	var billingStartedOnce sync.Once
	var releaseBillingOnce sync.Once
	release := func() { releaseBillingOnce.Do(func() { close(releaseBilling) }) }
	callbackName := "test:block-narrative-legacy-billing-" + uuid.NewString()
	if err := fixture.app.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "token_usage_records" {
			return
		}
		billingStartedOnce.Do(func() { close(billingStarted) })
		select {
		case <-releaseBilling:
		case <-tx.Statement.Context.Done():
		}
	}); err != nil {
		t.Fatalf("register billing blocker: %v", err)
	}

	testContext, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	recorder := &workflowStreamingEventGateRecorder{ResponseRecorder: httptest.NewRecorder()}
	raw, err := json.Marshal(narrativeWorkflowPayload())
	if err != nil {
		t.Fatalf("marshal legacy narrative payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, narrativeWorkflowEndpoint.path, bytes.NewReader(raw)).WithContext(testContext)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+fixture.token)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		fixture.app.Router().ServeHTTP(recorder, req)
	}()
	t.Cleanup(func() {
		release()
		select {
		case <-requestDone:
		case <-time.After(time.Second):
		}
	})

	select {
	case <-billingStarted:
	case <-testContext.Done():
		t.Fatalf("legacy narrative billing did not start: %v", testContext.Err())
	}
	if !waitForStreamBody(recorder, "event:answer_delta", time.Second) {
		t.Fatalf("provider tail was not flushed before billing completed: %s", recorder.Snapshot())
	}
	select {
	case <-requestDone:
		t.Fatal("legacy narrative request completed while billing was blocked")
	default:
	}

	release()
	select {
	case <-requestDone:
	case <-testContext.Done():
		t.Fatalf("legacy narrative request did not finish: %v", testContext.Err())
	}
}

func TestStreamToolWorkflowSuccessFinalizesBeforeProviderResultSSEWrite(t *testing.T) {
	for _, tc := range streamToolWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newStreamToolWorkflowFixture(t, false, false)
			run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "stream-success-"+tc.name, "stream-client-"+tc.name)
			recorder := &workflowStreamingEventGateRecorder{
				ResponseRecorder: httptest.NewRecorder(),
				onProviderResult: func() error {
					return verifyStreamToolWorkflowFinalized(fixture.app, run.ID, tc, fixture.usage)
				},
			}

			serveStreamToolWorkflowRequest(fixture.app, recorder, tc.path, withStreamToolWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if checked, checkErr := recorder.ResultCheck(); !checked || checkErr != nil {
				t.Fatalf("workflow was not finalized before provider output: checked=%t err=%v", checked, checkErr)
			}
			if fixture.calls.Load() != 1 {
				t.Fatalf("provider calls = %d, want 1", fixture.calls.Load())
			}
			if fixture.leaked.Load() {
				t.Fatal("workflow identity leaked into provider prompt payload")
			}
			if got := recorder.Header().Get("X-Request-ID"); got == "" {
				t.Fatal("workflow stream did not expose a stable request id")
			}
			if completedAt := strings.LastIndex(recorder.Body.String(), "event:completed"); completedAt < 0 {
				t.Fatalf("stream did not emit completed: %s", recorder.Body.String())
			}
		})
	}
}

func TestStreamToolWorkflowRejectsPartialIdentityBeforeProviderCall(t *testing.T) {
	for _, tc := range streamToolWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newStreamToolWorkflowFixture(t, false, false)
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

func TestStreamToolWorkflowUnavailableProviderDoesNotStartStep(t *testing.T) {
	fixture := newStreamToolWorkflowFixture(t, false, false)
	fixture.app.openai = providers.NewOpenAIClient(config.Config{}, fixture.app.promptBuilder)
	tc := streamToolWorkflowEndpointCases()[0]
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "stream-unconfigured", "stream-unconfigured-client")

	resp := request(fixture.app, http.MethodPost, tc.path, withStreamToolWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body = %s", resp.Code, http.StatusServiceUnavailable, resp.Body.String())
	}
	var step models.WorkflowStepExecution
	if err := fixture.app.db.Where("workflow_run_id = ? AND step_id = ?", run.ID, tc.stepID).First(&step).Error; err == nil {
		t.Fatalf("unconfigured provider created workflow step with status %q", step.Status)
	}
	if fixture.calls.Load() != 0 {
		t.Fatalf("provider calls = %d, want 0", fixture.calls.Load())
	}
}

func TestStreamToolWorkflowMissingUsageFailsWithoutReservation(t *testing.T) {
	fixture := newStreamToolWorkflowFixture(t, false, true)
	tc := streamToolWorkflowEndpointCases()[0]
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "stream-missing-usage", "stream-missing-usage-client")

	resp := request(fixture.app, http.MethodPost, tc.path, withStreamToolWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

	assertStreamToolWorkflowSSEError(t, resp, "provider_usage_missing")
	if !strings.Contains(resp.Body.String(), "provider returned content but omitted token usage") {
		t.Fatalf("missing-usage error is not actionable: %s", resp.Body.String())
	}
	assertStreamToolWorkflowStep(t, fixture.app, run.ID, tc.stepID, workflowStepStatusFailed, "provider_usage_missing")
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
}

func TestNarrationSegmentMissingStreamUsageFallsBackAndBillsOnlyFallback(t *testing.T) {
	fixture := newStreamToolWorkflowFixture(t, false, false)
	var calls atomic.Int32
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		if streaming, _ := payload["stream"].(bool); streaming {
			w.Header().Set("Content-Type", "text/event-stream")
			content := `{"items":[{"_id":1,"text":"stream draft"}],"summary":"stream"}`
			_, _ = fmt.Fprintf(w, "data: {\"id\":\"stream-without-usage\",\"model\":\"stream-tool-model\",\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", content)
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "stream-tool-request-1",
			"model": "stream-tool-model",
			"choices": []map[string]any{{
				"message": map[string]any{"content": `{"items":[{"_id":1,"text":"fallback draft"}],"summary":"fallback"}`},
			}},
			"usage": map[string]any{
				"prompt_tokens":     fixture.usage.PromptTokens,
				"completion_tokens": fixture.usage.CompletionTokens,
				"total_tokens":      fixture.usage.TotalTokens,
			},
		})
	}))
	defer providerServer.Close()
	fixture.app.cfg.OpenAIProAPIBaseURL = providerServer.URL
	fixture.app.openai = providers.NewOpenAIClient(fixture.app.cfg, fixture.app.promptBuilder)

	tc := streamToolWorkflowEndpointCases()[1]
	payload := cloneAnyMap(tc.payload)
	payload["model_operation"] = "narration_segment"
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "segment-fallback", "segment-fallback-client")
	resp := request(fixture.app, http.MethodPost, tc.path, withStreamToolWorkflowIdentity(payload, run, tc.stepID), fixture.token)

	if calls.Load() != 2 {
		t.Fatalf("provider calls = %d, want 2", calls.Load())
	}
	if resp.Code != http.StatusOK || strings.Contains(resp.Body.String(), "event:error") {
		t.Fatalf("fallback response = status %d body %s", resp.Code, resp.Body.String())
	}
	if strings.Count(resp.Body.String(), "event:completed") != 1 {
		t.Fatalf("completed event count is not one: %s", resp.Body.String())
	}
	if err := verifyStreamToolWorkflowFinalized(fixture.app, run.ID, tc, fixture.usage); err != nil {
		t.Fatal(err)
	}
}

func TestStreamToolWorkflowMissingProviderRequestIDFailsWithoutReservationOrCompletedEvent(t *testing.T) {
	fixture := newStreamToolWorkflowFixture(t, false, false)
	fixture.requestID = ""
	tc := streamToolWorkflowEndpointCases()[0]
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "stream-missing-request-id", "stream-missing-request-id-client")

	resp := request(fixture.app, http.MethodPost, tc.path, withStreamToolWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

	assertStreamToolWorkflowSSEError(t, resp, "provider_protocol_error")
	if !strings.Contains(resp.Body.String(), `"retryable":true`) {
		t.Fatalf("provider protocol error is not retryable: %s", resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "event:completed") {
		t.Fatalf("completed event was replayed after provider protocol error: %s", resp.Body.String())
	}
	assertStreamToolWorkflowStep(t, fixture.app, run.ID, tc.stepID, workflowStepStatusFailed, "provider_protocol_error")
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
}

func TestStreamToolWorkflowProviderFailureFinalizesAndEmitsStructuredSSEError(t *testing.T) {
	fixture := newStreamToolWorkflowFixture(t, true, false)
	tc := streamToolWorkflowEndpointCases()[0]
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "stream-provider-failure", "stream-provider-failure-client")

	resp := request(fixture.app, http.MethodPost, tc.path, withStreamToolWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

	assertStreamToolWorkflowSSEError(t, resp, "provider_unavailable")
	if strings.Contains(resp.Body.String(), "simulated stream provider failure") {
		t.Fatalf("provider diagnostics leaked into stream: %s", resp.Body.String())
	}
	assertStreamToolWorkflowStep(t, fixture.app, run.ID, tc.stepID, workflowStepStatusFailed, "provider_unavailable")
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
}

func TestStreamToolWorkflowSucceededReplayFailurePreservesReservation(t *testing.T) {
	fixture := newStreamToolWorkflowFixture(t, true, false)
	tc := streamToolWorkflowEndpointCases()[0]
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "stream-replay", "stream-replay-client")
	identity := workflowRequestIdentityForTest(run.ID, tc.stepID, run.InputHash, run.ClientInstanceID)
	execution, err := fixture.app.resolveWorkflowRequestIdentity(t.Context(), fixture.user.ID, nil, identity)
	if err != nil {
		t.Fatalf("resolve initial workflow request: %v", err)
	}
	if err := fixture.app.finalizeProviderSuccess(t.Context(), execution, providerSuccessFinalization{
		ProviderRequestID: optionalStringPointer("stream-tool-request-1"),
		Consumption: creditConsumption{
			UserID:          fixture.user.ID,
			CreditCount:     fixture.usage.TotalTokens,
			APIEndpoint:     tc.path,
			Provider:        "openai",
			ServiceKind:     "llm",
			ModelID:         "stream-tool-model",
			Usage:           fixture.usage,
			RequestMetadata: map[string]any{"request_id": "stream-tool-request-1"},
		},
	}); err != nil {
		t.Fatalf("seed succeeded workflow request: %v", err)
	}

	resp := request(fixture.app, http.MethodPost, tc.path, withStreamToolWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

	assertVisionWorkflowErrorResponse(t, resp, http.StatusConflict, "workflow_result_unknown")
	if fixture.calls.Load() != 0 {
		t.Fatalf("successful legacy step retried provider: %d", fixture.calls.Load())
	}
	assertStreamToolWorkflowStep(t, fixture.app, run.ID, tc.stepID, workflowRunStatusSucceeded, "")
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 1)
}

func TestStreamToolWorkflowFinalInsufficientCreditsBlocksWithoutReservation(t *testing.T) {
	fixture := newStreamToolWorkflowFixture(t, false, false)
	fixture.usage = providers.ChatUsage{PromptTokens: 3000, CompletionTokens: 2001, TotalTokens: 5001}
	tc := streamToolWorkflowEndpointCases()[0]
	if err := fixture.app.db.Model(&models.UserTokenAccount{}).Where("user_id = ?", fixture.user.ID).Updates(map[string]any{"total_tokens": 4500, "used_tokens": 0}).Error; err != nil {
		t.Fatalf("set final balance: %v", err)
	}
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "stream-final-insufficient", "stream-final-insufficient-client")

	resp := request(fixture.app, http.MethodPost, tc.path, withStreamToolWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

	assertStreamToolWorkflowSSEError(t, resp, "insufficient_credits")
	assertStreamToolWorkflowStep(t, fixture.app, run.ID, tc.stepID, workflowRunStatusBlocked, "insufficient_credits")
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
}

func TestStreamToolLegacyRequestsKeepImmediateBilling(t *testing.T) {
	for _, tc := range streamToolWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newStreamToolWorkflowFixture(t, false, false)

			resp := request(fixture.app, http.MethodPost, tc.path, cloneAnyMap(tc.payload), fixture.token)

			if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "event:completed") {
				t.Fatalf("legacy stream response = status %d body %s", resp.Code, resp.Body.String())
			}
			var account models.UserTokenAccount
			if err := fixture.app.db.First(&account, "user_id = ?", fixture.user.ID).Error; err != nil {
				t.Fatalf("load legacy account: %v", err)
			}
			if account.UsedTokens != fixture.usage.TotalTokens {
				t.Fatalf("used credits = %d, want %d", account.UsedTokens, fixture.usage.TotalTokens)
			}
		})
	}
}

func TestStreamToolEmitterSerializesConcurrentHeartbeatAndProviderWrites(t *testing.T) {
	recorder := &concurrentSSERecorder{ResponseRecorder: httptest.NewRecorder()}
	context, _ := gin.CreateTestContext(recorder)
	emitter := newStreamToolEmitter(context, recorder)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_ = emitter.emit("answer_delta", map[string]any{"delta": "provider"})
			_ = emitter.flush()
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = emitter.emit("heartbeat", nil)
		}()
	}
	close(start)
	wg.Wait()

	if recorder.concurrentWrites.Load() != 0 {
		t.Fatalf("concurrent SSE writes = %d", recorder.concurrentWrites.Load())
	}
	if body := recorder.Body.String(); !strings.Contains(body, ": heartbeat\n\n") || strings.Contains(body, "event:heartbeat") {
		t.Fatalf("heartbeat must be an SSE comment: %s", body)
	}
}

func TestToolStreamHeartbeatStopsAfterCompletionOrCancellation(t *testing.T) {
	for _, stopWithCancel := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", stopWithCancel), func(t *testing.T) {
			recorder := &synchronizedSSERecorder{ResponseRecorder: httptest.NewRecorder()}
			ginContext, _ := gin.CreateTestContext(recorder)
			emitter := newStreamToolEmitter(ginContext, recorder)
			ctx, cancel := context.WithCancel(context.Background())
			stop := startToolStreamHeartbeat(ctx, emitter, time.Millisecond)
			t.Cleanup(func() {
				cancel()
				stop()
			})

			waitForStreamHeartbeat(t, recorder)
			if stopWithCancel {
				cancel()
			}
			// context cancellation is asynchronous; stop joins the heartbeat goroutine.
			stop()
			count := strings.Count(recorder.Snapshot(), ": heartbeat\n\n")
			time.Sleep(10 * time.Millisecond)
			if got := strings.Count(recorder.Snapshot(), ": heartbeat\n\n"); got != count {
				t.Fatalf("heartbeat count grew after stop: got %d, want %d", got, count)
			}
		})
	}
}

func waitForStreamHeartbeat(t *testing.T, recorder streamBodySnapshotter) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(recorder.Snapshot(), ": heartbeat\n\n") {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("heartbeat was not emitted: %s", recorder.Snapshot())
}

type streamBodySnapshotter interface {
	Snapshot() string
}

type synchronizedSSERecorder struct {
	*httptest.ResponseRecorder
	mu sync.Mutex
}

func (r *synchronizedSSERecorder) WriteHeader(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.WriteHeader(code)
}

func (r *synchronizedSSERecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Write(data)
}

func (r *synchronizedSSERecorder) WriteString(data string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.WriteString(data)
}

func (r *synchronizedSSERecorder) Flush() {}

func (r *synchronizedSSERecorder) Snapshot() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Body.String()
}

func waitForStreamBody(recorder streamBodySnapshotter, expected string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(recorder.Snapshot(), expected) {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

type concurrentSSERecorder struct {
	*httptest.ResponseRecorder
	activeWrites     atomic.Int32
	concurrentWrites atomic.Int32
}

func (r *concurrentSSERecorder) Write(data []byte) (int, error) {
	if r.activeWrites.Add(1) != 1 {
		r.concurrentWrites.Add(1)
	}
	time.Sleep(time.Millisecond)
	n, err := r.ResponseRecorder.Write(data)
	r.activeWrites.Add(-1)
	return n, err
}

func (r *concurrentSSERecorder) Flush() {}

type committedHeaderRecorder struct {
	*httptest.ResponseRecorder
	once sync.Once
}

func (r *committedHeaderRecorder) WriteHeader(code int) {
	r.ResponseRecorder.WriteHeader(code)
	r.snapshotHeaders()
}

func (r *committedHeaderRecorder) Write(data []byte) (int, error) {
	r.snapshotHeaders()
	return r.ResponseRecorder.Write(data)
}

func (r *committedHeaderRecorder) WriteString(data string) (int, error) {
	r.snapshotHeaders()
	return r.ResponseRecorder.WriteString(data)
}

func (r *committedHeaderRecorder) Flush() {}

func (r *committedHeaderRecorder) snapshotHeaders() {
	r.once.Do(func() { _ = r.ResponseRecorder.Result() })
}

type workflowStreamingEventGateRecorder struct {
	*httptest.ResponseRecorder
	mu               sync.Mutex
	pending          bytes.Buffer
	flushCount       int
	onProviderResult func() error
	once             sync.Once
	checkErr         error
	checked          bool
}

func TestWorkflowStreamingEventGateRecorderRequiresFlushForVisibility(t *testing.T) {
	recorder := &workflowStreamingEventGateRecorder{ResponseRecorder: httptest.NewRecorder()}
	if _, err := recorder.WriteString(": connected\n\n"); err != nil {
		t.Fatalf("write pending stream data: %v", err)
	}
	if body := recorder.Snapshot(); body != "" {
		t.Fatalf("unflushed stream data became visible: %q", body)
	}
	recorder.Flush()
	if body := recorder.Snapshot(); body != ": connected\n\n" {
		t.Fatalf("flushed stream data = %q", body)
	}
	if recorder.FlushCount() != 1 {
		t.Fatalf("flush count = %d, want 1", recorder.FlushCount())
	}
}

func (r *workflowStreamingEventGateRecorder) WriteHeader(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.WriteHeader(code)
}

func (r *workflowStreamingEventGateRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pending.Write(data)
}

func (r *workflowStreamingEventGateRecorder) WriteString(data string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pending.WriteString(data)
}

func (r *workflowStreamingEventGateRecorder) observeProviderResult() {
	r.mu.Lock()
	body := r.Body.Bytes()
	isProviderResult := bytes.Contains(body, []byte("event:thinking_delta")) ||
		bytes.Contains(body, []byte("event:answer_delta")) ||
		bytes.Contains(body, []byte("event:completed"))
	r.mu.Unlock()
	if isProviderResult {
		r.once.Do(func() {
			var checkErr error
			if r.onProviderResult != nil {
				checkErr = r.onProviderResult()
			}
			r.mu.Lock()
			r.checked = true
			r.checkErr = checkErr
			r.mu.Unlock()
		})
	}
}

func (r *workflowStreamingEventGateRecorder) Flush() {
	r.mu.Lock()
	if r.pending.Len() > 0 {
		_, _ = r.ResponseRecorder.Write(r.pending.Bytes())
		r.pending.Reset()
	}
	r.ResponseRecorder.Flush()
	r.flushCount++
	r.mu.Unlock()
	r.observeProviderResult()
}

func (r *workflowStreamingEventGateRecorder) FlushCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flushCount
}

func (r *workflowStreamingEventGateRecorder) Snapshot() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Body.String()
}

func (r *workflowStreamingEventGateRecorder) ResultCheck() (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.checked, r.checkErr
}

func newStreamToolWorkflowFixture(t *testing.T, fail, omitUsage bool) *streamToolWorkflowFixture {
	t.Helper()
	fixture := &streamToolWorkflowFixture{
		fail:      fail,
		omitUsage: omitUsage,
		requestID: "stream-tool-request-1",
		usage:     providers.ChatUsage{PromptTokens: 12, CompletionTokens: 8, TotalTokens: 20},
	}
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		for _, key := range workflowRequestIdentityKeys {
			if bytes.Contains(body, []byte(key)) {
				fixture.leaked.Store(true)
			}
		}
		if fixture.fail {
			http.Error(w, "simulated stream provider failure with prompt content", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		content := `{"items":[{"_id":"1","text":"approved script","start":0,"end":1,"org":""}],"summary":"narrative approved","events":[{"start":0,"end":1,"description":"narrative workflow source"}]}`
		_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"model\":\"stream-tool-model\",\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", fixture.requestID, content)
		if !fixture.omitUsage {
			_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"model\":\"stream-tool-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d,\"total_tokens\":%d}}\n\n", fixture.requestID, fixture.usage.PromptTokens, fixture.usage.CompletionTokens, fixture.usage.TotalTokens)
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(providerServer.Close)

	app := newTestApp(t)
	app.cfg.OpenAIProAPIKey = "openai-test-key"
	app.cfg.OpenAIProAPIBaseURL = providerServer.URL
	app.cfg.OpenAIProModel = "stream-tool-model"
	app.cfg.OpenAITemperature = 0.1
	app.openai = providers.NewOpenAIClient(app.cfg, app.promptBuilder)
	seedBillingRates(t, app, map[string]any{"llm": []map[string]any{{
		"provider": "openai", "model_id": "stream-tool-model", "tokens_per_credit": 1,
	}}})
	email := "stream-tool-workflow-" + uuid.NewString() + "@example.com"
	fixture.app = app
	fixture.token = registerAndLoginWithCredits(t, app, email, 10000)
	fixture.user = lookupUserByEmail(t, app, email)
	return fixture
}

func assertNarrativeWorkflowReservationFingerprint(t *testing.T, app *App, runID uuid.UUID) {
	t.Helper()
	var reservation models.CreditReservation
	if err := app.db.Where("workflow_run_id = ?", runID).Take(&reservation).Error; err != nil {
		t.Fatalf("load narrative reservation: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(reservation.RequestMetadata, &metadata); err != nil {
		t.Fatalf("decode narrative reservation metadata: %v", err)
	}
	if fingerprint := workflowInputFingerprintFromMetadata(metadata); fingerprint == "" {
		t.Fatalf("narrative reservation has no valid input fingerprint: %#v", metadata)
	}
}

func streamToolWorkflowEndpointCases() []streamToolWorkflowEndpointCase {
	return []streamToolWorkflowEndpointCase{
		{name: "script", kind: "script", path: "/api/v1/tools/generate-script-stream", stepID: "script.generate", payload: map[string]any{"video_context": "workflow script source"}},
		{name: "draft", kind: "script_draft", path: "/api/v1/tools/generate-narration-draft-stream", stepID: "script.draft", payload: map[string]any{"video_context": "workflow draft source"}},
		{name: "match", kind: "script_match", path: "/api/v1/tools/match-narration-visuals-stream", stepID: "script.match", payload: map[string]any{"video_context": "workflow match source"}},
	}
}

func withStreamToolWorkflowIdentity(payload map[string]any, run models.WorkflowRun, stepID string) map[string]any {
	requestPayload := cloneAnyMap(payload)
	requestPayload["workflow_run_id"] = run.ID.String()
	requestPayload["workflow_step_id"] = stepID
	requestPayload["workflow_input_hash"] = run.InputHash
	requestPayload["client_instance_id"] = run.ClientInstanceID
	return requestPayload
}

func serveStreamToolWorkflowRequest(app *App, writer http.ResponseWriter, path string, body any, token string) {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	app.Router().ServeHTTP(writer, req)
}

func verifyStreamToolWorkflowFinalized(app *App, runID uuid.UUID, tc streamToolWorkflowEndpointCase, usage providers.ChatUsage) error {
	var step models.WorkflowStepExecution
	if err := app.db.Where("workflow_run_id = ? AND step_id = ?", runID, tc.stepID).Take(&step).Error; err != nil {
		return fmt.Errorf("load step: %w", err)
	}
	if step.Status != workflowRunStatusSucceeded {
		return fmt.Errorf("step status = %q", step.Status)
	}
	if step.ProviderRequestID == nil || *step.ProviderRequestID != "stream-tool-request-1" {
		return fmt.Errorf("provider request id = %v", step.ProviderRequestID)
	}
	var storedUsage providers.ChatUsage
	if err := json.Unmarshal(step.Usage, &storedUsage); err != nil || storedUsage != usage {
		return fmt.Errorf("step usage = %#v err=%v", storedUsage, err)
	}
	var reservations []models.CreditReservation
	if err := app.db.Where("workflow_run_id = ?", runID).Find(&reservations).Error; err != nil {
		return fmt.Errorf("load reservations: %w", err)
	}
	if len(reservations) != 1 || reservations[0].CreditCount != usage.TotalTokens || reservations[0].APIEndpoint != tc.path {
		return fmt.Errorf("reservations = %#v", reservations)
	}
	return nil
}

func assertStreamToolWorkflowSSEError(t *testing.T, resp *httptest.ResponseRecorder, code string) {
	t.Helper()
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want opened SSE status 200, body = %s", resp.Code, resp.Body.String())
	}
	if resp.Header().Get("X-Request-ID") == "" || !strings.Contains(resp.Body.String(), "event:error") ||
		!strings.Contains(resp.Body.String(), `"code":"`+code+`"`) || !strings.Contains(resp.Body.String(), `"retryable":`) {
		t.Fatalf("structured SSE error = headers %#v body %s", resp.Header(), resp.Body.String())
	}
}

func assertStreamToolWorkflowStep(t *testing.T, app *App, runID uuid.UUID, stepID, wantStatus, wantCode string) {
	t.Helper()
	var step models.WorkflowStepExecution
	if err := app.db.Where("workflow_run_id = ? AND step_id = ?", runID, stepID).Take(&step).Error; err != nil {
		t.Fatalf("load workflow step: %v", err)
	}
	if step.Status != wantStatus {
		t.Fatalf("step status = %q, want %q", step.Status, wantStatus)
	}
	if wantCode == "" {
		return
	}
	if step.ErrorCode == nil || *step.ErrorCode != wantCode {
		t.Fatalf("step error code = %v, want %q", step.ErrorCode, wantCode)
	}
}
