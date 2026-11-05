package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
)

const (
	ttsWorkflowPath        = "/api/v1/tools/text-to-speech-with-uri"
	ttsWorkflowStepID      = "audio.tts.generate"
	ttsWorkflowText        = "workflow tts secret \u4f60\u597d"
	ttsWorkflowVoice       = "speech:workflow-secret-voice"
	ttsWorkflowLocalPath   = `C:\workflow-secret\speech.mp3`
	ttsWorkflowAudioMarker = "workflow-audio-secret"
)

type ttsWorkflowFixture struct {
	app      *App
	token    string
	user     models.User
	project  models.Project
	provider string
	model    string
	audio    []byte
	calls    atomic.Int32
	fail     atomic.Bool
	drain    atomic.Bool
	leaked   atomic.Bool
}

func TestTTSWorkflowJSONSuccessFinalizesBeforeFirstWrite(t *testing.T) {
	for _, provider := range []string{"siliconflow", "fishaudio"} {
		t.Run(provider, func(t *testing.T) {
			fixture := newTTSWorkflowFixture(t, provider)
			run := createTTSWorkflowRun(t, fixture, "tts-json-success-input", "tts-json-success-client")
			payload := ttsWorkflowPayload(fixture, run)
			recorder := &workflowResponseGateRecorder{
				ResponseRecorder: httptest.NewRecorder(),
				onFirstWrite: func() error {
					return verifyTTSWorkflowFinalized(fixture, run.ID)
				},
			}

			serveTTSJSONRequest(fixture.app, recorder, payload, fixture.token)

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
			if !bytes.Equal(recorder.Body.Bytes(), fixture.audio) {
				t.Fatalf("audio response = %q, want provider audio", recorder.Body.Bytes())
			}

			replay := request(fixture.app, http.MethodPost, ttsWorkflowPath, cloneAnyMap(payload), fixture.token)
			if replay.Code != http.StatusOK {
				t.Fatalf("replay status = %d, body = %s", replay.Code, replay.Body.String())
			}
			if fixture.calls.Load() != 2 {
				t.Fatalf("provider calls after replay = %d, want 2", fixture.calls.Load())
			}
			assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 1)
		})
	}
}

func TestTTSWorkflowLongSegmentKeepsChildLease(t *testing.T) {
	fixture := newTTSWorkflowFixture(t, "siliconflow")
	base := time.Now().UTC()
	var elapsed atomic.Int64
	fixture.app.workflowRuns.now = func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
	run := createTTSWorkflowRun(t, fixture, "tts-long-input", "tts-long-client")
	var renewed atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elapsed.Store(int64(70 * time.Second))
		deadline := time.Now().Add(streamToolHeartbeatInterval + 5*time.Second)
		for time.Now().Before(deadline) {
			var step models.WorkflowStepExecution
			if err := fixture.app.db.Where("workflow_run_id = ? AND step_id = ?", run.ID, ttsWorkflowStepID).Take(&step).Error; err == nil && step.LeaseExpiresAt != nil && step.LeaseExpiresAt.After(base.Add(110*time.Second)) {
				renewed.Store(true)
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		elapsed.Store(int64(110 * time.Second))
		_, _ = w.Write(fixture.audio)
	}))
	defer provider.Close()
	fixture.app.silicon = providers.NewSiliconFlowClient(config.Config{SiliconFlowAPIKey: "test-key", SiliconFlowBaseURL: provider.URL, SiliconFlowTTSModel: fixture.model})
	resp := request(fixture.app, http.MethodPost, ttsWorkflowPath, ttsWorkflowPayload(fixture, run), fixture.token)
	if !renewed.Load() || resp.Code != http.StatusOK {
		t.Fatalf("long TTS lease renewed=%v status=%d body=%s", renewed.Load(), resp.Code, resp.Body.String())
	}
	if err := verifyTTSWorkflowFinalized(fixture, run.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTTSWorkflowFormIdentityParsesAndIsStripped(t *testing.T) {
	for _, contentType := range []string{"form", "multipart"} {
		t.Run(contentType, func(t *testing.T) {
			fixture := newTTSWorkflowFixture(t, "siliconflow")
			run := createTTSWorkflowRun(t, fixture, "tts-form-input-"+contentType, "tts-form-client-"+contentType)
			values := ttsWorkflowFormValues(fixture, run)

			resp, req := serveTTSFormRequest(fixture.app, values, fixture.token, contentType == "multipart")

			if resp.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
			}
			if fixture.calls.Load() != 1 {
				t.Fatalf("provider calls = %d, want 1", fixture.calls.Load())
			}
			if fixture.leaked.Load() {
				t.Fatal("workflow identity leaked into provider payload")
			}
			for _, key := range workflowRequestIdentityKeys {
				if _, exists := req.PostForm[key]; exists {
					t.Fatalf("request PostForm retained workflow identity key %q", key)
				}
				if req.MultipartForm != nil {
					if _, exists := req.MultipartForm.Value[key]; exists {
						t.Fatalf("request MultipartForm retained workflow identity key %q", key)
					}
				}
			}
			if err := verifyTTSWorkflowFinalized(fixture, run.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTTSWorkflowIdentityRejectsPartialAndInvalid(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		code string
	}{
		{
			name: "partial",
			edit: func(payload map[string]any) {
				payload["workflow_run_id"] = uuid.NewString()
			},
			code: "partial_workflow_identity",
		},
		{
			name: "invalid",
			edit: func(payload map[string]any) {
				payload["workflow_run_id"] = "not-a-uuid"
				payload["workflow_step_id"] = ttsWorkflowStepID
				payload["workflow_input_hash"] = "invalid-input-hash"
				payload["client_instance_id"] = "tts-invalid-client"
			},
			code: "invalid_workflow_identity",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTTSWorkflowFixture(t, "siliconflow")
			payload := ttsLegacyPayload(fixture)
			payload["project_id"] = fixture.project.ID.String()
			tc.edit(payload)

			resp := request(fixture.app, http.MethodPost, ttsWorkflowPath, payload, fixture.token)

			assertVisionWorkflowErrorResponse(t, resp, http.StatusBadRequest, tc.code)
			if fixture.calls.Load() != 0 {
				t.Fatalf("provider calls = %d, want 0", fixture.calls.Load())
			}
		})
	}
}

func TestTTSWorkflowActiveDuplicateResolvesBeforeProviderConfiguration(t *testing.T) {
	fixture := newTTSWorkflowFixture(t, "siliconflow")
	run := createTTSWorkflowRun(t, fixture, "tts-duplicate-input", "tts-duplicate-client")
	identity := workflowRequestIdentityForTest(run.ID, ttsWorkflowStepID, run.InputHash, run.ClientInstanceID)
	if _, err := fixture.app.resolveWorkflowRequestIdentity(t.Context(), fixture.user.ID, &fixture.project.ID, identity); err != nil {
		t.Fatalf("seed active execution: %v", err)
	}
	fixture.app.silicon = providers.NewSiliconFlowClient(config.Config{})

	resp := request(fixture.app, http.MethodPost, ttsWorkflowPath, ttsWorkflowPayload(fixture, run), fixture.token)

	assertVisionWorkflowErrorResponse(t, resp, http.StatusConflict, "step_execution_in_progress")
	if fixture.calls.Load() != 0 {
		t.Fatalf("provider calls = %d, want 0", fixture.calls.Load())
	}
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
}

func TestTTSWorkflowInsufficientCreditsBlocksBeforeProviderCall(t *testing.T) {
	fixture := newTTSWorkflowFixture(t, "siliconflow")
	run := createTTSWorkflowRun(t, fixture, "tts-credits-input", "tts-credits-client")
	setTestCredits(t, fixture.app, fixture.user.ID, 5, 5)

	resp := request(fixture.app, http.MethodPost, ttsWorkflowPath, ttsWorkflowPayload(fixture, run), fixture.token)

	assertVisionWorkflowErrorResponse(t, resp, http.StatusPaymentRequired, "insufficient_credits")
	if fixture.calls.Load() != 0 {
		t.Fatalf("provider calls = %d, want 0", fixture.calls.Load())
	}
	var step models.WorkflowStepExecution
	if err := fixture.app.db.Where("workflow_run_id = ? AND step_id = ?", run.ID, ttsWorkflowStepID).Take(&step).Error; err != nil {
		t.Fatalf("load blocked step: %v", err)
	}
	if step.Status != workflowRunStatusBlocked || step.ErrorCode == nil || *step.ErrorCode != "insufficient_credits" {
		t.Fatalf("blocked workflow step = %#v", step)
	}
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
}

func TestTTSWorkflowProviderFailureIsStructuredWithoutReservation(t *testing.T) {
	fixture := newTTSWorkflowFixture(t, "siliconflow")
	fixture.fail.Store(true)
	run := createTTSWorkflowRun(t, fixture, "tts-failure-input", "tts-failure-client")

	resp := request(fixture.app, http.MethodPost, ttsWorkflowPath, ttsWorkflowPayload(fixture, run), fixture.token)

	assertVisionWorkflowErrorResponse(t, resp, http.StatusBadGateway, "provider_unavailable")
	if strings.Contains(resp.Body.String(), "tts provider private failure") {
		t.Fatalf("provider diagnostics leaked into response: %s", resp.Body.String())
	}
	var step models.WorkflowStepExecution
	if err := fixture.app.db.Where("workflow_run_id = ? AND step_id = ?", run.ID, ttsWorkflowStepID).Take(&step).Error; err != nil {
		t.Fatalf("load failed step: %v", err)
	}
	if step.Status != workflowStepStatusFailed || step.ErrorCode == nil || *step.ErrorCode != "provider_unavailable" {
		t.Fatalf("failed workflow step = %#v", step)
	}
	if step.ProviderRequestID != nil || string(step.Usage) != "{}" {
		t.Fatalf("provider failure retained success metadata: %#v", step)
	}
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
}

func TestTTSWorkflowSucceededReplayFailurePreservesOriginalSuccess(t *testing.T) {
	fixture := newTTSWorkflowFixture(t, "fishaudio")
	run := createTTSWorkflowRun(t, fixture, "tts-replay-failure-input", "tts-replay-failure-client")
	payload := ttsWorkflowPayload(fixture, run)
	first := request(fixture.app, http.MethodPost, ttsWorkflowPath, cloneAnyMap(payload), fixture.token)
	if first.Code != http.StatusOK {
		t.Fatalf("initial status = %d, body = %s", first.Code, first.Body.String())
	}
	fixture.fail.Store(true)

	replay := request(fixture.app, http.MethodPost, ttsWorkflowPath, cloneAnyMap(payload), fixture.token)

	assertVisionWorkflowErrorResponse(t, replay, http.StatusBadGateway, "provider_unavailable")
	if fixture.calls.Load() != 2 {
		t.Fatalf("provider calls = %d, want 2", fixture.calls.Load())
	}
	var step models.WorkflowStepExecution
	if err := fixture.app.db.Where("workflow_run_id = ? AND step_id = ?", run.ID, ttsWorkflowStepID).Take(&step).Error; err != nil {
		t.Fatalf("load replayed step: %v", err)
	}
	if step.Status != workflowRunStatusSucceeded {
		t.Fatalf("replay failure changed successful step: %#v", step)
	}
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 1)
}

func TestTTSWorkflowSucceededReplayRejectsChangedInputBeforeProviderCall(t *testing.T) {
	fixture := newTTSWorkflowFixture(t, "siliconflow")
	run := createTTSWorkflowRun(t, fixture, "tts-replay-input-binding", "tts-replay-input-client")
	payload := ttsWorkflowPayload(fixture, run)
	first := request(fixture.app, http.MethodPost, ttsWorkflowPath, cloneAnyMap(payload), fixture.token)
	if first.Code != http.StatusOK {
		t.Fatalf("initial status = %d, body = %s", first.Code, first.Body.String())
	}
	changed := cloneAnyMap(payload)
	changed["text"] = "workflow tts secret \u518d\u89c1"

	replay := request(fixture.app, http.MethodPost, ttsWorkflowPath, changed, fixture.token)

	assertVisionWorkflowErrorResponse(t, replay, http.StatusConflict, "workflow_input_mismatch")
	if fixture.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want only initial call", fixture.calls.Load())
	}
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 1)
}

func TestTTSGeneralFitRevisionsKeepSeparateInputsAndStableBilling(t *testing.T) {
	f := newTTSWorkflowFixture(t, "siliconflow")
	run := createWorkflowIdentityTestRun(t, f.app, f.user.ID, "general-fit-voice", "general-fit-client")
	payload := ttsWorkflowPayload(f, run)
	delete(payload, "project_id")
	payload["workflow_step_id"] = "generate-voiceover.general-v2.fit-voice-1"
	for _, text := range []string{"原稿配音", "审核通过的新稿配音", "审核通过的新稿配音"} {
		payload["text"] = text
		resp := request(f.app, http.MethodPost, ttsWorkflowPath, cloneAnyMap(payload), f.token)
		if resp.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
		}
	}
	var steps []models.WorkflowStepExecution
	if err := f.app.db.Where("workflow_run_id = ?", run.ID).Find(&steps).Error; err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 {
		t.Fatalf("got %d execution versions, want 2", len(steps))
	}
	for _, step := range steps {
		if !strings.HasPrefix(step.StepID, "generate-voiceover.general-v2.fit-voice-1.r.") || step.Status != "succeeded" {
			t.Fatalf("invalid versioned TTS step: %s %s", step.StepID, step.Status)
		}
	}
	assertVisionWorkflowReservationCount(t, f.app, run.ID, 2)
}

func TestTTSGeneralFitUnknownPriorResultPreventsNewPaidVersion(t *testing.T) {
	f := newTTSWorkflowFixture(t, "siliconflow")
	run := createWorkflowIdentityTestRun(t, f.app, f.user.ID, "general-fit-unknown", "general-fit-unknown-client")
	payload := ttsWorkflowPayload(f, run)
	delete(payload, "project_id")
	payload["workflow_step_id"] = "generate-voiceover.general-v2.fit-voice-1"
	resp := request(f.app, http.MethodPost, ttsWorkflowPath, cloneAnyMap(payload), f.token)
	if resp.Code != http.StatusOK {
		t.Fatalf("initial status %d", resp.Code)
	}
	if err := f.app.db.Model(&models.WorkflowStepExecution{}).Where("workflow_run_id = ?", run.ID).Updates(map[string]any{"status": "failed", "error_code": "workflow_result_unknown"}).Error; err != nil {
		t.Fatal(err)
	}
	payload["text"] = "不能覆盖未确认的旧请求"
	resp = request(f.app, http.MethodPost, ttsWorkflowPath, payload, f.token)
	if resp.Code != http.StatusConflict || f.calls.Load() != 1 {
		t.Fatalf("unknown request must block provider call: status=%d calls=%d body=%s", resp.Code, f.calls.Load(), resp.Body.String())
	}
}

func TestTTSWorkflowFinalReservationShortfallBlocksStep(t *testing.T) {
	fixture := newTTSWorkflowFixture(t, "siliconflow")
	run := createTTSWorkflowRun(t, fixture, "tts-final-balance-input", "tts-final-balance-client")
	fixture.drain.Store(true)

	resp := request(fixture.app, http.MethodPost, ttsWorkflowPath, ttsWorkflowPayload(fixture, run), fixture.token)

	assertVisionWorkflowErrorResponse(t, resp, http.StatusPaymentRequired, "insufficient_credits")
	if fixture.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", fixture.calls.Load())
	}
	var step models.WorkflowStepExecution
	if err := fixture.app.db.Where("workflow_run_id = ? AND step_id = ?", run.ID, ttsWorkflowStepID).Take(&step).Error; err != nil {
		t.Fatalf("load blocked step: %v", err)
	}
	if step.Status != workflowRunStatusBlocked || step.ErrorCode == nil || *step.ErrorCode != "insufficient_credits" {
		t.Fatalf("final shortfall step = %#v", step)
	}
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
}

func TestTTSLegacyRequestsStillBillImmediatelyAndKeepRawProviderFailure(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		fixture := newTTSWorkflowFixture(t, "siliconflow")

		resp := request(fixture.app, http.MethodPost, ttsWorkflowPath, ttsLegacyPayload(fixture), fixture.token)

		if resp.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
		}
		var account models.UserTokenAccount
		if err := fixture.app.db.First(&account, "user_id = ?", fixture.user.ID).Error; err != nil {
			t.Fatalf("load account: %v", err)
		}
		wantCredits := int64(math.Ceil(2 * ttsWorkflowAudioDuration(fixture.audio)))
		if account.UsedTokens != wantCredits {
			t.Fatalf("used credits = %d, want %d", account.UsedTokens, wantCredits)
		}
		var reservationCount int64
		if err := fixture.app.db.Model(&models.CreditReservation{}).Count(&reservationCount).Error; err != nil {
			t.Fatalf("count reservations: %v", err)
		}
		if reservationCount != 0 {
			t.Fatalf("reservations = %d, want 0", reservationCount)
		}
	})

	t.Run("provider failure", func(t *testing.T) {
		fixture := newTTSWorkflowFixture(t, "siliconflow")
		fixture.fail.Store(true)

		resp := request(fixture.app, http.MethodPost, ttsWorkflowPath, ttsLegacyPayload(fixture), fixture.token)

		if resp.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
		}
		if !strings.Contains(resp.Body.String(), "tts provider private failure") {
			t.Fatalf("legacy provider error did not preserve raw diagnostics: %s", resp.Body.String())
		}
	})
}

func newTTSWorkflowFixture(t *testing.T, provider string) *ttsWorkflowFixture {
	t.Helper()
	fixture := &ttsWorkflowFixture{
		provider: provider,
		audio:    ttsWorkflowWAV([]byte(ttsWorkflowAudioMarker)),
	}
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		for _, key := range workflowRequestIdentityKeys {
			if bytes.Contains(body, []byte(key)) {
				fixture.leaked.Store(true)
			}
		}
		if fixture.fail.Load() {
			http.Error(w, "tts provider private failure", http.StatusInternalServerError)
			return
		}
		if fixture.drain.Load() {
			setTestCredits(t, fixture.app, fixture.user.ID, 1, 1)
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(fixture.audio)
	}))
	t.Cleanup(providerServer.Close)

	app := newTestApp(t)
	switch provider {
	case "siliconflow":
		fixture.model = "tts-workflow-silicon-model"
		providerConfig := config.Config{
			SiliconFlowAPIKey:   "silicon-workflow-key",
			SiliconFlowBaseURL:  providerServer.URL,
			SiliconFlowTTSModel: fixture.model,
		}
		app.cfg.SiliconFlowAPIKey = providerConfig.SiliconFlowAPIKey
		app.cfg.SiliconFlowBaseURL = providerConfig.SiliconFlowBaseURL
		app.cfg.SiliconFlowTTSModel = providerConfig.SiliconFlowTTSModel
		app.silicon = providers.NewSiliconFlowClient(providerConfig)
		app.fish = providers.NewFishAudioClient(config.Config{})
	case "fishaudio":
		fishModel := "workflow-fish"
		fixture.model = "fish-audio-" + fishModel
		providerConfig := config.Config{
			FishAudioAPIKey:  "fish-workflow-key",
			FishAudioBaseURL: providerServer.URL,
			FishAudioModel:   fishModel,
		}
		app.cfg.FishAudioAPIKey = providerConfig.FishAudioAPIKey
		app.cfg.FishAudioBaseURL = providerConfig.FishAudioBaseURL
		app.cfg.FishAudioModel = providerConfig.FishAudioModel
		app.fish = providers.NewFishAudioClient(providerConfig)
	default:
		t.Fatalf("unknown TTS provider %q", provider)
	}
	seedBillingRates(t, app, map[string]any{
		"tts": []map[string]any{{
			"provider":           fixture.provider,
			"model_id":           fixture.model,
			"credits_per_second": 2,
			"cost_per_minute":    1,
		}},
	})
	email := "tts-workflow-" + provider + "-" + uuid.NewString() + "@example.com"
	fixture.app = app
	fixture.token = registerAndLoginWithCredits(t, app, email, 100)
	fixture.user = lookupUserByEmail(t, app, email)
	fixture.project = createTestProject(t, app, fixture.token, "TTS workflow project")
	return fixture
}

func createTTSWorkflowRun(t *testing.T, fixture *ttsWorkflowFixture, inputHash, clientID string) models.WorkflowRun {
	t.Helper()
	return createWorkflowIdentityTestWebRun(t, fixture.app, fixture.user.ID, fixture.project.ID, inputHash, clientID)
}

func ttsWorkflowPayload(fixture *ttsWorkflowFixture, run models.WorkflowRun) map[string]any {
	payload := ttsLegacyPayload(fixture)
	payload["project_id"] = fixture.project.ID.String()
	payload["workflow_run_id"] = run.ID.String()
	payload["workflow_step_id"] = ttsWorkflowStepID
	payload["workflow_input_hash"] = run.InputHash
	payload["client_instance_id"] = run.ClientInstanceID
	payload["local_path"] = ttsWorkflowLocalPath
	payload["audio"] = ttsWorkflowAudioMarker
	payload["business_content"] = "workflow-business-secret"
	return payload
}

func ttsLegacyPayload(fixture *ttsWorkflowFixture) map[string]any {
	serviceLevel := "basic"
	if fixture.provider == "fishaudio" {
		serviceLevel = "premium"
	}
	return map[string]any{
		"text":          ttsWorkflowText,
		"voice_uri":     ttsWorkflowVoice,
		"service_level": serviceLevel,
		"speed":         1.0,
		"volume":        1.0,
	}
}

func ttsWorkflowFormValues(fixture *ttsWorkflowFixture, run models.WorkflowRun) url.Values {
	payload := ttsWorkflowPayload(fixture, run)
	values := make(url.Values, len(payload))
	for key, value := range payload {
		values.Set(key, fmt.Sprint(value))
	}
	return values
}

func serveTTSJSONRequest(app *App, writer http.ResponseWriter, body any, token string) {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, ttsWorkflowPath, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	app.Router().ServeHTTP(writer, req)
}

func serveTTSFormRequest(app *App, values url.Values, token string, useMultipart bool) (*httptest.ResponseRecorder, *http.Request) {
	var body io.Reader
	contentType := "application/x-www-form-urlencoded"
	if useMultipart {
		var buffer bytes.Buffer
		writer := multipart.NewWriter(&buffer)
		for key, items := range values {
			for _, item := range items {
				_ = writer.WriteField(key, item)
			}
		}
		_ = writer.Close()
		body = &buffer
		contentType = writer.FormDataContentType()
	} else {
		body = strings.NewReader(values.Encode())
	}
	req := httptest.NewRequest(http.MethodPost, ttsWorkflowPath, body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	resp := httptest.NewRecorder()
	app.Router().ServeHTTP(resp, req)
	return resp, req
}

func verifyTTSWorkflowFinalized(fixture *ttsWorkflowFixture, runID uuid.UUID) error {
	var step models.WorkflowStepExecution
	if err := fixture.app.db.Where("workflow_run_id = ? AND step_id = ?", runID, ttsWorkflowStepID).Take(&step).Error; err != nil {
		return fmt.Errorf("load step: %w", err)
	}
	if step.Status != workflowRunStatusSucceeded {
		return fmt.Errorf("step status = %q", step.Status)
	}
	if step.ProviderRequestID != nil {
		return fmt.Errorf("provider request id = %v, want nil", step.ProviderRequestID)
	}
	var usage map[string]float64
	if err := json.Unmarshal(step.Usage, &usage); err != nil {
		return fmt.Errorf("decode step usage: %w", err)
	}
	wantDuration := ttsWorkflowAudioDuration(fixture.audio)
	if usage["utf8_bytes"] != float64(len([]byte(ttsWorkflowText))) ||
		usage["character_count"] != float64(len([]rune(ttsWorkflowText))) ||
		usage["duration_seconds"] != wantDuration {
		return fmt.Errorf("step usage = %#v", usage)
	}

	var reservations []models.CreditReservation
	if err := fixture.app.db.Where("workflow_run_id = ?", runID).Find(&reservations).Error; err != nil {
		return fmt.Errorf("load reservations: %w", err)
	}
	if len(reservations) != 1 {
		return fmt.Errorf("reservations = %d", len(reservations))
	}
	reservation := reservations[0]
	if reservation.Status != creditReservationPending || reservation.WorkflowStepExecutionID == nil || *reservation.WorkflowStepExecutionID != step.ID {
		return fmt.Errorf("reservation identity/status = %#v", reservation)
	}
	if reservation.ProjectID == nil || *reservation.ProjectID != fixture.project.ID || reservation.Provider != fixture.provider || reservation.ModelID != fixture.model {
		return fmt.Errorf("reservation provider/project metadata = %#v", reservation)
	}
	if reservation.UTF8Bytes != int64(len([]byte(ttsWorkflowText))) || reservation.CharacterCount != int64(len([]rune(ttsWorkflowText))) {
		return fmt.Errorf("reservation text usage = %#v", reservation)
	}
	if reservation.DurationSeconds == nil || *reservation.DurationSeconds != wantDuration {
		return fmt.Errorf("reservation duration = %v, want %v", reservation.DurationSeconds, wantDuration)
	}
	wantCredits := int64(math.Ceil(2 * wantDuration))
	if reservation.CreditCount != wantCredits {
		return fmt.Errorf("reservation credits = %d, want %d", reservation.CreditCount, wantCredits)
	}
	var metadata map[string]any
	if err := json.Unmarshal(reservation.RequestMetadata, &metadata); err != nil {
		return fmt.Errorf("decode reservation metadata: %w", err)
	}
	allowedMetadata := map[string]bool{
		"billing_cost_unit":                 true,
		"billing_currency":                  true,
		"billing_missing_rate":              true,
		"billing_model_id":                  true,
		"billing_provider":                  true,
		"billing_rate":                      true,
		"product_mode":                      true,
		"free_experience":                   true,
		"billing_service_kind":              true,
		"character_count":                   true,
		workflowInputFingerprintMetadataKey: true,
		"overdraft_credits_consumed":        true,
		"paid_purchased_credits_consumed":   true,
		"purchased_credits_consumed":        true,
		"subscription_credits_consumed":     true,
		"utf8_bytes":                        true,
	}
	for key := range metadata {
		if !allowedMetadata[key] {
			return fmt.Errorf("reservation metadata retained unsupported key %q: %#v", key, metadata)
		}
	}
	if fingerprint, _ := metadata[workflowInputFingerprintMetadataKey].(string); len(fingerprint) != 64 {
		return fmt.Errorf("reservation input fingerprint = %#v", metadata[workflowInputFingerprintMetadataKey])
	}
	stored, err := json.Marshal(struct {
		Step        models.WorkflowStepExecution
		Reservation models.CreditReservation
	}{Step: step, Reservation: reservation})
	if err != nil {
		return fmt.Errorf("marshal stored workflow data: %w", err)
	}
	for _, forbidden := range []string{
		ttsWorkflowText,
		ttsWorkflowVoice,
		ttsWorkflowLocalPath,
		ttsWorkflowAudioMarker,
		"workflow-business-secret",
		`"text"`,
		`"voice_uri"`,
		`"local_path"`,
		`"audio"`,
		`"business_content"`,
	} {
		if bytes.Contains(stored, []byte(forbidden)) {
			return fmt.Errorf("stored workflow data contains business content %q: %s", forbidden, stored)
		}
	}
	return nil
}

func ttsWorkflowWAV(data []byte) []byte {
	audio := make([]byte, 44+len(data))
	copy(audio[0:4], "RIFF")
	binary.LittleEndian.PutUint32(audio[4:8], uint32(len(audio)-8))
	copy(audio[8:12], "WAVE")
	copy(audio[12:16], "fmt ")
	binary.LittleEndian.PutUint32(audio[16:20], 16)
	binary.LittleEndian.PutUint16(audio[20:22], 1)
	binary.LittleEndian.PutUint16(audio[22:24], 1)
	binary.LittleEndian.PutUint32(audio[24:28], 8)
	binary.LittleEndian.PutUint32(audio[28:32], 8)
	binary.LittleEndian.PutUint16(audio[32:34], 1)
	binary.LittleEndian.PutUint16(audio[34:36], 8)
	copy(audio[36:40], "data")
	binary.LittleEndian.PutUint32(audio[40:44], uint32(len(data)))
	copy(audio[44:], data)
	return audio
}

func ttsWorkflowAudioDuration(audio []byte) float64 {
	duration, ok := wavDuration(audio)
	if !ok {
		panic("test audio is not valid WAV")
	}
	return duration
}
