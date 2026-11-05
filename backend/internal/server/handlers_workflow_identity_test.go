package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
)

type visionWorkflowEndpointCase struct {
	name              string
	path              string
	stepID            string
	providerRequestID string
	usage             providers.ChatUsage
	providerContent   string
	payload           map[string]any
}

type visionWorkflowFixture struct {
	app    *App
	token  string
	user   models.User
	calls  atomic.Int32
	leaked atomic.Bool
}

type workflowResponseGateRecorder struct {
	*httptest.ResponseRecorder
	onFirstWrite func() error
	once         sync.Once
	checkErr     error
	checked      bool
}

func (r *workflowResponseGateRecorder) beforeWrite() {
	r.once.Do(func() {
		r.checked = true
		if r.onFirstWrite != nil {
			r.checkErr = r.onFirstWrite()
		}
	})
}

func (r *workflowResponseGateRecorder) WriteHeader(statusCode int) {
	r.beforeWrite()
	r.ResponseRecorder.WriteHeader(statusCode)
}

func (r *workflowResponseGateRecorder) Write(data []byte) (int, error) {
	r.beforeWrite()
	return r.ResponseRecorder.Write(data)
}

func (r *workflowResponseGateRecorder) Flush() {
	r.beforeWrite()
	r.ResponseRecorder.Flush()
}

func TestVisionToolWorkflowIdentityFinalizesBeforeSuccessResponse(t *testing.T) {
	for _, tc := range visionWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newVisionWorkflowFixture(t, tc, false)
			run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "vision-success-input", "vision-success-client")
			payload := withVisionWorkflowIdentity(tc.payload, run, tc.stepID)
			recorder := &workflowResponseGateRecorder{
				ResponseRecorder: httptest.NewRecorder(),
				onFirstWrite: func() error {
					return verifyVisionWorkflowFinalized(fixture.app, run.ID, tc)
				},
			}

			serveVisionWorkflowRequest(fixture.app, recorder, tc.path, payload, fixture.token)

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

func TestVisionToolWorkflowIdentityRejectsPartialBeforeProviderCall(t *testing.T) {
	for _, tc := range visionWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newVisionWorkflowFixture(t, tc, false)
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

func TestVisionToolWorkflowIdentityRejectsActiveDuplicateBeforeProviderCall(t *testing.T) {
	for _, tc := range visionWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newVisionWorkflowFixture(t, tc, false)
			run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "vision-duplicate-input", "vision-duplicate-client")
			identity := workflowRequestIdentityForTest(run.ID, tc.stepID, run.InputHash, run.ClientInstanceID)
			if _, err := fixture.app.resolveWorkflowRequestIdentity(t.Context(), fixture.user.ID, nil, identity); err != nil {
				t.Fatalf("seed active execution: %v", err)
			}

			resp := request(fixture.app, http.MethodPost, tc.path, withVisionWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

			assertVisionWorkflowErrorResponse(t, resp, http.StatusConflict, "step_execution_in_progress")
			if fixture.calls.Load() != 0 {
				t.Fatalf("provider calls = %d, want 0", fixture.calls.Load())
			}
			assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
		})
	}
}

func TestVisionToolWorkflowIdentityInsufficientCreditsIsStructuredAndBlocked(t *testing.T) {
	for _, tc := range visionWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newVisionWorkflowFixture(t, tc, false)
			run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "vision-credits-input", "vision-credits-client")
			if err := fixture.app.db.Model(&models.UserTokenAccount{}).
				Where("user_id = ?", fixture.user.ID).
				Update("used_tokens", 1000).Error; err != nil {
				t.Fatalf("drain credits: %v", err)
			}

			resp := request(fixture.app, http.MethodPost, tc.path, withVisionWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

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

func TestVisionToolWorkflowIdentityFinalInsufficientCreditsBlocksRunAndStep(t *testing.T) {
	for _, tc := range visionWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			tc.usage = providers.ChatUsage{PromptTokens: 6000, CompletionTokens: 6000, TotalTokens: 12000}
			fixture := newVisionWorkflowFixture(t, tc, false)
			run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "vision-final-credits-input", "vision-final-credits-client")

			resp := request(fixture.app, http.MethodPost, tc.path, withVisionWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

			assertVisionWorkflowErrorResponse(t, resp, http.StatusPaymentRequired, "insufficient_credits")
			if fixture.calls.Load() != 1 {
				t.Fatalf("provider calls = %d, want 1", fixture.calls.Load())
			}
			assertWorkflowProviderBlocked(t, fixture.app, run.ID, tc.stepID)
			assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
		})
	}
}

func TestVisionToolWorkflowIdentityMissingProviderRequestIDFailsWithoutReservation(t *testing.T) {
	for _, tc := range visionWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			tc.providerRequestID = ""
			fixture := newVisionWorkflowFixture(t, tc, false)
			run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "vision-missing-trace-input", "vision-missing-trace-client")

			resp := request(fixture.app, http.MethodPost, tc.path, withVisionWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

			assertVisionWorkflowErrorResponse(t, resp, http.StatusBadGateway, "provider_protocol_error")
			assertWorkflowProviderProtocolFailure(t, fixture.app, run.ID, tc.stepID)
			assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
		})
	}
}

func TestVisionToolWorkflowIdentityZeroUsageFailsWithoutReservation(t *testing.T) {
	for _, tc := range visionWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			tc.usage = providers.ChatUsage{}
			fixture := newVisionWorkflowFixture(t, tc, false)
			run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "vision-zero-usage-input", "vision-zero-usage-client")

			resp := request(fixture.app, http.MethodPost, tc.path, withVisionWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

			assertVisionWorkflowErrorResponse(t, resp, http.StatusBadGateway, "provider_protocol_error")
			assertWorkflowProviderProtocolFailure(t, fixture.app, run.ID, tc.stepID)
			assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
		})
	}
}

func TestVisionToolWorkflowIdentityProviderFailureFinalizesWithoutReservation(t *testing.T) {
	for _, tc := range visionWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newVisionWorkflowFixture(t, tc, true)
			run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "vision-failure-input", "vision-failure-client")

			resp := request(fixture.app, http.MethodPost, tc.path, withVisionWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)

			assertVisionWorkflowErrorResponse(t, resp, http.StatusBadGateway, "provider_unavailable")
			if fixture.calls.Load() != 1 {
				t.Fatalf("provider calls = %d, want 1", fixture.calls.Load())
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

func TestVisionToolLegacyRequestsStillBillImmediately(t *testing.T) {
	for _, tc := range visionWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newVisionWorkflowFixture(t, tc, false)

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
			if account.UsedTokens != 4 {
				t.Fatalf("used credits = %d, want 4", account.UsedTokens)
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

func TestVisionToolLegacyInsufficientCreditsPrecedesProviderConfiguration(t *testing.T) {
	for _, tc := range visionWorkflowEndpointCases() {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestApp(t)
			email := "vision-legacy-credits-" + uuid.NewString() + "@example.com"
			token := registerAndLoginWithCredits(t, app, email, 0)

			resp := request(app, http.MethodPost, tc.path, cloneAnyMap(tc.payload), token)

			if resp.Code != http.StatusPaymentRequired {
				t.Fatalf("status = %d, want 402, body = %s", resp.Code, resp.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["error"] != "Payment Required" {
				t.Fatalf("legacy response = %#v", body)
			}
			if _, exists := body["request_id"]; exists {
				t.Fatalf("legacy response unexpectedly used workflow contract: %#v", body)
			}
		})
	}
}

func newVisionWorkflowFixture(t *testing.T, tc visionWorkflowEndpointCase, failProvider bool) *visionWorkflowFixture {
	t.Helper()
	fixture := &visionWorkflowFixture{}
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		for _, key := range workflowRequestIdentityKeys {
			if bytes.Contains(body, []byte(key)) {
				fixture.leaked.Store(true)
			}
		}
		if failProvider {
			http.Error(w, "simulated siliconflow failure", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    tc.providerRequestID,
			"model": "Qwen/Qwen3-VL-32B-Instruct",
			"usage": map[string]any{"prompt_tokens": tc.usage.PromptTokens, "completion_tokens": tc.usage.CompletionTokens, "total_tokens": tc.usage.TotalTokens},
			"choices": []map[string]any{{
				"message": map[string]any{"content": tc.providerContent},
			}},
		})
	}))
	t.Cleanup(providerServer.Close)

	app := newTestApp(t)
	app.silicon = providers.NewSiliconFlowClient(config.Config{
		SiliconFlowAPIKey:  "silicon-test-key",
		SiliconFlowBaseURL: providerServer.URL,
	})
	seedBillingRates(t, app, map[string]any{
		"vision": []map[string]any{{
			"provider":          "siliconflow",
			"model_id":          "Qwen/Qwen3-VL-32B-Instruct",
			"tokens_per_credit": 5,
		}},
	})
	email := "vision-workflow-" + uuid.NewString() + "@example.com"
	fixture.app = app
	fixture.token = registerAndLoginWithCredits(t, app, email, 1000)
	fixture.user = lookupUserByEmail(t, app, email)
	return fixture
}

func visionWorkflowEndpointCases() []visionWorkflowEndpointCase {
	return []visionWorkflowEndpointCase{
		{
			name:              "analyzeImages",
			path:              "/api/v1/tools/analyze-images",
			stepID:            "images.analyze",
			providerRequestID: "silicon-request-1",
			usage:             providers.ChatUsage{PromptTokens: 10, CompletionTokens: 10, TotalTokens: 20},
			providerContent:   `{"summary":"image analysis"}`,
			payload: map[string]any{
				"images":            []map[string]any{{"type": "url", "data": "https://example.com/image.png"}},
				"previous_subtitle": "before",
			},
		},
		{
			name:              "ocrImages",
			path:              "/api/v1/tools/ocr-images",
			stepID:            "images.ocr",
			providerRequestID: "silicon-request-1",
			usage:             providers.ChatUsage{PromptTokens: 10, CompletionTokens: 10, TotalTokens: 20},
			providerContent:   `{"timestamps":["00:00:01"],"text":["ocr text"]}`,
			payload: map[string]any{
				"images": []map[string]any{{"type": "url", "data": "https://example.com/image.png"}},
			},
		},
	}
}

func withVisionWorkflowIdentity(payload map[string]any, run models.WorkflowRun, stepID string) map[string]any {
	requestPayload := cloneAnyMap(payload)
	requestPayload["workflow_run_id"] = run.ID.String()
	requestPayload["workflow_step_id"] = stepID
	requestPayload["workflow_input_hash"] = run.InputHash
	requestPayload["client_instance_id"] = run.ClientInstanceID
	return requestPayload
}

func verifyVisionWorkflowFinalized(app *App, runID uuid.UUID, tc visionWorkflowEndpointCase) error {
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
		return fmt.Errorf("step usage = %#v", usage)
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
	if reservation.APIEndpoint != tc.path || reservation.Provider != "siliconflow" || reservation.ModelID != "Qwen/Qwen3-VL-32B-Instruct" || reservation.CreditCount != tc.usage.TotalTokens/5 {
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
		return fmt.Errorf("reservation request id = %#v", metadata["request_id"])
	}
	return nil
}

func assertWorkflowProviderBlocked(t *testing.T, app *App, runID uuid.UUID, stepID string) {
	t.Helper()
	var run models.WorkflowRun
	if err := app.db.First(&run, "id = ?", runID).Error; err != nil {
		t.Fatalf("load blocked run: %v", err)
	}
	if run.Status != workflowRunStatusBlocked || run.CurrentStepID == nil || *run.CurrentStepID != stepID {
		t.Fatalf("blocked run = %#v", run)
	}
	var step models.WorkflowStepExecution
	if err := app.db.Where("workflow_run_id = ? AND step_id = ?", runID, stepID).Take(&step).Error; err != nil {
		t.Fatalf("load blocked step: %v", err)
	}
	if step.Status != workflowRunStatusBlocked || step.ErrorCode == nil || *step.ErrorCode != "insufficient_credits" {
		t.Fatalf("blocked step = %#v", step)
	}
}

func assertWorkflowProviderProtocolFailure(t *testing.T, app *App, runID uuid.UUID, stepID string) {
	t.Helper()
	var step models.WorkflowStepExecution
	if err := app.db.Where("workflow_run_id = ? AND step_id = ?", runID, stepID).Take(&step).Error; err != nil {
		t.Fatalf("load protocol-error step: %v", err)
	}
	if step.Status != workflowStepStatusFailed || step.ErrorCode == nil || *step.ErrorCode != "provider_protocol_error" {
		t.Fatalf("protocol-error step = %#v", step)
	}
}

func serveVisionWorkflowRequest(app *App, writer http.ResponseWriter, path string, body any, token string) {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	app.Router().ServeHTTP(writer, req)
}

func assertVisionWorkflowErrorResponse(t *testing.T, resp *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if resp.Code != status {
		t.Fatalf("status = %d, want %d, body = %s", resp.Code, status, resp.Body.String())
	}
	var body workflowRequestErrorResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode workflow error response: %v; body = %s", err, resp.Body.String())
	}
	if body.Code != code || body.RequestID == "" || strings.TrimSpace(body.Message) == "" {
		t.Fatalf("workflow error response = %#v", body)
	}
}

func assertVisionWorkflowReservationCount(t *testing.T, app *App, runID uuid.UUID, want int64) {
	t.Helper()
	var count int64
	if err := app.db.Model(&models.CreditReservation{}).Where("workflow_run_id = ?", runID).Count(&count).Error; err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if count != want {
		t.Fatalf("reservations = %d, want %d", count, want)
	}
}
