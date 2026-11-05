package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
)

const (
	asrWorkflowStepID = "audio.asr.transcribe"
	asrWorkflowTaskID = "asrTask_01JQ7Y8K9M2N4P6R8T0V3W5X7Z"
)

type asrWorkflowFixture struct {
	app             *App
	token           string
	user            models.User
	project         models.Project
	submitCalls     atomic.Int32
	queryCalls      atomic.Int32
	pending         atomic.Bool
	failQuery       atomic.Bool
	missingArtifact atomic.Bool
	queryStatus     atomic.Value
	leaked          atomic.Bool
	blockSubmit     atomic.Bool
	submitStarted   chan struct{}
	releaseSubmit   chan struct{}
	startedOnce     sync.Once
}

func TestVendorASRWorkflowSubmissionReplaysPersistedTaskAfterTemporaryMediaURLChanges(t *testing.T) {
	fixture := newASRWorkflowFixture(t)
	run := createASRWorkflowRun(t, fixture, "asr-submit-input", "asr-submit-client")
	payload := asrWorkflowPayload(fixture, run)

	first := request(fixture.app, http.MethodPost, "/api/v1/tools/qwen3/asr/submit", cloneAnyMap(payload), fixture.token)
	if first.Code != http.StatusOK {
		t.Fatalf("initial status = %d, body = %s", first.Code, first.Body.String())
	}
	if fixture.submitCalls.Load() != 1 {
		t.Fatalf("provider submissions = %d, want 1", fixture.submitCalls.Load())
	}
	if fixture.leaked.Load() {
		t.Fatal("workflow identity leaked to provider")
	}
	step := loadASRWorkflowStep(t, fixture, run.ID)
	if step.ProviderRequestID == nil || *step.ProviderRequestID != asrWorkflowTaskID || step.ProviderInputHash == nil {
		t.Fatalf("stored submission = %#v", step)
	}

	replay := request(fixture.app, http.MethodPost, "/api/v1/tools/qwen3/asr/submit", cloneAnyMap(payload), fixture.token)
	if replay.Code != http.StatusOK || !bytes.Contains(replay.Body.Bytes(), []byte(asrWorkflowTaskID)) {
		t.Fatalf("replay = %d %s", replay.Code, replay.Body.String())
	}
	if fixture.submitCalls.Load() != 1 {
		t.Fatalf("provider submissions after replay = %d, want 1", fixture.submitCalls.Load())
	}

	changed := cloneAnyMap(payload)
	changed["file_url"] = "oss://temporary/reuploaded-audio.mp3"
	changedReplay := request(fixture.app, http.MethodPost, "/api/v1/tools/qwen3/asr/submit", changed, fixture.token)
	if changedReplay.Code != http.StatusOK || !bytes.Contains(changedReplay.Body.Bytes(), []byte(asrWorkflowTaskID)) {
		t.Fatalf("changed media URL replay = %d %s", changedReplay.Code, changedReplay.Body.String())
	}
	if fixture.submitCalls.Load() != 1 {
		t.Fatalf("provider submissions after regenerated media URL = %d, want 1", fixture.submitCalls.Load())
	}
}

func TestVendorASRWorkflowSubmissionUsesPreRegisteredStepLease(t *testing.T) {
	fixture := newASRWorkflowFixture(t)
	run := createASRWorkflowRun(t, fixture, "asr-pre-registered-input", "asr-pre-registered-client")
	stepPath := "/api/v1/workflow-runs/" + run.ID.String() + "/steps/" + asrWorkflowStepID

	preRegistered := request(fixture.app, http.MethodPut, stepPath, map[string]any{
		"input_hash":        run.InputHash,
		"status":            "running",
		"client_instance_id": run.ClientInstanceID,
		"pre_registered":    true,
	}, fixture.token)
	if preRegistered.Code != http.StatusOK {
		t.Fatalf("pre-register step = %d %s", preRegistered.Code, preRegistered.Body.String())
	}

	submission := request(fixture.app, http.MethodPost, "/api/v1/tools/funasr/asr/submit", asrWorkflowPayload(fixture, run), fixture.token)
	if submission.Code != http.StatusOK {
		t.Fatalf("submission after pre-register = %d %s", submission.Code, submission.Body.String())
	}
	if fixture.submitCalls.Load() != 1 {
		t.Fatalf("provider submissions = %d, want 1", fixture.submitCalls.Load())
	}
	if step := loadASRWorkflowStep(t, fixture, run.ID); step.PreRegistered {
		t.Fatalf("provider submission did not consume pre-registration: %#v", step)
	}
}

func TestVendorASRWorkflowConcurrentSubmissionUsesDurableClaim(t *testing.T) {
	fixture := newASRWorkflowFixture(t)
	run := createASRWorkflowRun(t, fixture, "asr-concurrent-input", "asr-concurrent-client")
	payload := asrWorkflowPayload(fixture, run)
	fixture.blockSubmit.Store(true)
	firstResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstResult <- request(fixture.app, http.MethodPost, "/api/v1/tools/qwen3/asr/submit", cloneAnyMap(payload), fixture.token)
	}()
	<-fixture.submitStarted

	second := request(fixture.app, http.MethodPost, "/api/v1/tools/qwen3/asr/submit", cloneAnyMap(payload), fixture.token)
	close(fixture.releaseSubmit)
	first := <-firstResult

	assertVisionWorkflowErrorResponse(t, second, http.StatusConflict, "workflow_provider_submission_in_progress")
	if fixture.submitCalls.Load() != 1 {
		t.Fatalf("provider submissions = %d, want 1", fixture.submitCalls.Load())
	}
	if first.Code != http.StatusOK {
		t.Fatalf("first submission = %d %s", first.Code, first.Body.String())
	}
}

func TestVendorASRWorkflowQueryIgnoresLegacyBillingMapAndFinalizesOnce(t *testing.T) {
	fixture := newASRWorkflowFixture(t)
	run := createASRWorkflowRun(t, fixture, "asr-query-input", "asr-query-client")
	payload := asrWorkflowPayload(fixture, run)
	if response := request(fixture.app, http.MethodPost, "/api/v1/tools/funasr/asr/submit", payload, fixture.token); response.Code != http.StatusOK {
		t.Fatalf("submit = %d %s", response.Code, response.Body.String())
	}
	queryPath := asrWorkflowQueryPath("/api/v1/tools/funasr/asr/query/", fixture, run, asrWorkflowTaskID)
	fixture.app.vendorASRBilled = sync.Map{}
	first := request(fixture.app, http.MethodGet, queryPath, nil, fixture.token)
	if first.Code != http.StatusOK || !bytes.Contains(first.Body.Bytes(), []byte(`"status":"SUCCEEDED"`)) {
		t.Fatalf("first query = %d %s", first.Code, first.Body.String())
	}
	assertASRWorkflowFinalized(t, fixture, run.ID)

	second := request(fixture.app, http.MethodGet, queryPath, nil, fixture.token)
	if second.Code != http.StatusOK {
		t.Fatalf("second query = %d %s", second.Code, second.Body.String())
	}
	assertASRWorkflowFinalized(t, fixture, run.ID)
}

func TestVendorASRWorkflowQueryPendingFailureAndTaskMismatch(t *testing.T) {
	fixture := newASRWorkflowFixture(t)
	run := createASRWorkflowRun(t, fixture, "asr-query-edge-input", "asr-query-edge-client")
	if response := request(fixture.app, http.MethodPost, "/api/v1/tools/qwen3/asr/submit", asrWorkflowPayload(fixture, run), fixture.token); response.Code != http.StatusOK {
		t.Fatalf("submit = %d %s", response.Code, response.Body.String())
	}
	queryPath := asrWorkflowQueryPath("/api/v1/tools/qwen3/asr/query/", fixture, run, asrWorkflowTaskID)
	fixture.pending.Store(true)
	pending := request(fixture.app, http.MethodGet, queryPath, nil, fixture.token)
	if pending.Code != http.StatusOK || !bytes.Contains(pending.Body.Bytes(), []byte(`"status":"RUNNING"`)) {
		t.Fatalf("pending query = %d %s", pending.Code, pending.Body.String())
	}
	if step := loadASRWorkflowStep(t, fixture, run.ID); step.Status != workflowRunStatusRunning {
		t.Fatalf("pending step status = %s, want running", step.Status)
	}

	mismatch := request(fixture.app, http.MethodGet, asrWorkflowQueryPath("/api/v1/tools/qwen3/asr/query/", fixture, run, workflowProviderTaskIDAlternate), nil, fixture.token)
	assertVisionWorkflowErrorResponse(t, mismatch, http.StatusConflict, "workflow_provider_task_mismatch")

	fixture.pending.Store(false)
	fixture.failQuery.Store(true)
	failure := request(fixture.app, http.MethodGet, queryPath, nil, fixture.token)
	assertVisionWorkflowErrorResponse(t, failure, http.StatusBadGateway, "provider_unavailable")
	if step := loadASRWorkflowStep(t, fixture, run.ID); step.Status != workflowStepStatusFailed || step.ErrorCode == nil || *step.ErrorCode != "provider_unavailable" || step.ProviderRequestID == nil || *step.ProviderRequestID != asrWorkflowTaskID {
		t.Fatalf("failed workflow step = %#v", step)
	}
	fixture.failQuery.Store(false)
	retry := request(fixture.app, http.MethodGet, queryPath, nil, fixture.token)
	if retry.Code != http.StatusOK {
		t.Fatalf("retry query = %d %s", retry.Code, retry.Body.String())
	}
	if fixture.submitCalls.Load() != 1 {
		t.Fatalf("provider submissions after query retry = %d, want 1", fixture.submitCalls.Load())
	}
}

func TestVendorASRWorkflowNormalizedStatuses(t *testing.T) {
	for _, tc := range []struct {
		name            string
		status          string
		missingArtifact bool
		pending         bool
	}{
		{name: "unknown", status: "UNKNOWN"},
		{name: "canceled", status: "CANCELED"},
		{name: "succeeded missing result", status: "SUCCEEDED", missingArtifact: true},
		{name: "pending", status: "PENDING", pending: true},
		{name: "running", status: "RUNNING", pending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newASRWorkflowFixture(t)
			suffix := strings.ReplaceAll(tc.name, " ", "-")
			run := createASRWorkflowRun(t, fixture, "asr-status-"+suffix, "asr-status-client-"+suffix)
			if response := request(fixture.app, http.MethodPost, "/api/v1/tools/qwen3/asr/submit", asrWorkflowPayload(fixture, run), fixture.token); response.Code != http.StatusOK {
				t.Fatalf("submit = %d %s", response.Code, response.Body.String())
			}
			fixture.queryStatus.Store(tc.status)
			fixture.missingArtifact.Store(tc.missingArtifact)
			queryPath := asrWorkflowQueryPath("/api/v1/tools/qwen3/asr/query/", fixture, run, asrWorkflowTaskID)

			response := request(fixture.app, http.MethodGet, queryPath, nil, fixture.token)

			if tc.pending {
				if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"status":"`+tc.status+`"`)) {
					t.Fatalf("pending response = %d %s", response.Code, response.Body.String())
				}
				if step := loadASRWorkflowStep(t, fixture, run.ID); step.Status != workflowRunStatusRunning {
					t.Fatalf("pending workflow step = %#v", step)
				}
				assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
				return
			}

			assertVisionWorkflowErrorResponse(t, response, http.StatusBadGateway, "provider_unavailable")
			step := loadASRWorkflowStep(t, fixture, run.ID)
			if step.Status != workflowStepStatusFailed || step.ProviderRequestID == nil || *step.ProviderRequestID != asrWorkflowTaskID || step.ProviderInputHash == nil {
				t.Fatalf("failed workflow step = %#v", step)
			}
			assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
		})
	}
}

func TestVendorASRWorkflowIdentityAndCreditFailures(t *testing.T) {
	fixture := newASRWorkflowFixture(t)
	partial := asrLegacyPayload()
	partial["project_id"] = fixture.project.ID.String()
	partial["workflow_run_id"] = uuid.NewString()
	response := request(fixture.app, http.MethodPost, "/api/v1/tools/qwen3/asr/submit", partial, fixture.token)
	assertVisionWorkflowErrorResponse(t, response, http.StatusBadRequest, "partial_workflow_identity")
	if fixture.submitCalls.Load() != 0 {
		t.Fatalf("provider submissions = %d, want 0", fixture.submitCalls.Load())
	}
	invalid := asrLegacyPayload()
	invalid["workflow_run_id"] = "not-a-uuid"
	invalid["workflow_step_id"] = asrWorkflowStepID
	invalid["workflow_input_hash"] = "not-a-valid-hash"
	invalid["client_instance_id"] = "asr-invalid-client"
	response = request(fixture.app, http.MethodPost, "/api/v1/tools/qwen3/asr/submit", invalid, fixture.token)
	assertVisionWorkflowErrorResponse(t, response, http.StatusBadRequest, "invalid_workflow_identity")
	if fixture.submitCalls.Load() != 0 {
		t.Fatalf("provider submissions after invalid identity = %d, want 0", fixture.submitCalls.Load())
	}

	run := createASRWorkflowRun(t, fixture, "asr-credit-input", "asr-credit-client")
	setTestCredits(t, fixture.app, fixture.user.ID, 0, 0)
	response = request(fixture.app, http.MethodPost, "/api/v1/tools/qwen3/asr/submit", asrWorkflowPayload(fixture, run), fixture.token)
	assertVisionWorkflowErrorResponse(t, response, http.StatusPaymentRequired, "insufficient_credits")
	if fixture.submitCalls.Load() != 0 {
		t.Fatalf("provider submissions = %d, want 0", fixture.submitCalls.Load())
	}
	if step := loadASRWorkflowStep(t, fixture, run.ID); step.Status != workflowRunStatusBlocked {
		t.Fatalf("blocked workflow step = %#v", step)
	}
}

func TestVendorASRLegacyRequestRemainsCompatible(t *testing.T) {
	fixture := newASRWorkflowFixture(t)
	response := request(fixture.app, http.MethodPost, "/api/v1/tools/qwen3/asr/submit", asrLegacyPayload(), fixture.token)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(asrWorkflowTaskID)) {
		t.Fatalf("legacy response = %d %s", response.Code, response.Body.String())
	}
	if fixture.submitCalls.Load() != 1 {
		t.Fatalf("provider submissions = %d, want 1", fixture.submitCalls.Load())
	}
}

func newASRWorkflowFixture(t *testing.T) *asrWorkflowFixture {
	t.Helper()
	fixture := &asrWorkflowFixture{submitStarted: make(chan struct{}), releaseSubmit: make(chan struct{})}
	fixture.queryStatus.Store("SUCCEEDED")
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/services/audio/asr/transcription":
			call := fixture.submitCalls.Add(1)
			if fixture.blockSubmit.Load() && call == 1 {
				fixture.startedOnce.Do(func() { close(fixture.submitStarted) })
				<-fixture.releaseSubmit
			}
			body, _ := io.ReadAll(r.Body)
			for _, key := range workflowRequestIdentityKeys {
				if bytes.Contains(body, []byte(key)) {
					fixture.leaked.Store(true)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"output": map[string]any{"task_id": asrWorkflowTaskID, "task_status": "PENDING"}})
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/"+asrWorkflowTaskID:
			fixture.queryCalls.Add(1)
			if fixture.failQuery.Load() {
				http.Error(w, "private provider failure", http.StatusBadGateway)
				return
			}
			status := fixture.queryStatus.Load().(string)
			if fixture.pending.Load() {
				status = "RUNNING"
			}
			output := map[string]any{"task_status": status}
			if status == "SUCCEEDED" && !fixture.missingArtifact.Load() {
				output["result"] = map[string]any{"transcription_url": "http://" + r.Host + "/asr-result.json"}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"output": output, "usage": map[string]any{"duration": 2}})
		case r.Method == http.MethodGet && r.URL.Path == "/asr-result.json":
			_ = json.NewEncoder(w).Encode(map[string]any{"transcripts": []map[string]any{{"channel_id": 0, "text": "provider transcript secret", "sentences": []map[string]any{{"begin_time": 0, "end_time": 2000, "text": "provider transcript secret"}}}}})
		default:
			t.Fatalf("unexpected provider request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(providerServer.Close)
	t.Cleanup(providers.SetDashScopeAPIBaseURLForTest(providerServer.URL))

	app := newTestApp(t)
	app.cfg.Qwen3ASRModel = "qwen-asr-workflow-model"
	app.cfg.FunASRModel = "fun-asr-workflow-model"
	app.dashscope = providers.NewDashScopeClient(config.Config{DashScopeAPIKey: "dash-workflow-key", Qwen3ASRModel: app.cfg.Qwen3ASRModel, FunASRModel: app.cfg.FunASRModel})
	seedBillingRates(t, app, map[string]any{"asr": []map[string]any{{"provider": "dashscope", "model_id": app.cfg.Qwen3ASRModel, "credits_per_minute": 1}, {"provider": "dashscope", "model_id": app.cfg.FunASRModel, "credits_per_minute": 1}}})
	email := "asr-workflow-" + uuid.NewString() + "@example.com"
	fixture.app = app
	fixture.token = registerAndLoginWithCredits(t, app, email, 100)
	fixture.user = lookupUserByEmail(t, app, email)
	fixture.project = createTestProject(t, app, fixture.token, "ASR workflow project")
	return fixture
}

func createASRWorkflowRun(t *testing.T, fixture *asrWorkflowFixture, inputHash, clientID string) models.WorkflowRun {
	t.Helper()
	return createWorkflowIdentityTestWebRun(t, fixture.app, fixture.user.ID, fixture.project.ID, inputHash, clientID)
}

func asrLegacyPayload() map[string]any {
	return map[string]any{"file_url": "oss://private-asr-workflow-audio.wav", "language": "zh", "channel_id": []int{0}, "enable_itn": true}
}

func asrWorkflowPayload(fixture *asrWorkflowFixture, run models.WorkflowRun) map[string]any {
	payload := asrLegacyPayload()
	payload["project_id"] = fixture.project.ID.String()
	payload["workflow_run_id"] = run.ID.String()
	payload["workflow_step_id"] = asrWorkflowStepID
	payload["workflow_input_hash"] = run.InputHash
	payload["client_instance_id"] = run.ClientInstanceID
	return payload
}

func asrWorkflowQueryPath(prefix string, fixture *asrWorkflowFixture, run models.WorkflowRun, taskID string) string {
	values := url.Values{"project_id": {fixture.project.ID.String()}, "workflow_run_id": {run.ID.String()}, "workflow_step_id": {asrWorkflowStepID}, "workflow_input_hash": {run.InputHash}, "client_instance_id": {run.ClientInstanceID}}
	return prefix + taskID + "?" + values.Encode()
}

func loadASRWorkflowStep(t *testing.T, fixture *asrWorkflowFixture, runID uuid.UUID) models.WorkflowStepExecution {
	t.Helper()
	var step models.WorkflowStepExecution
	if err := fixture.app.db.Where("workflow_run_id = ? AND step_id = ?", runID, asrWorkflowStepID).Take(&step).Error; err != nil {
		t.Fatalf("load ASR workflow step: %v", err)
	}
	return step
}

func assertASRWorkflowFinalized(t *testing.T, fixture *asrWorkflowFixture, runID uuid.UUID) {
	t.Helper()
	step := loadASRWorkflowStep(t, fixture, runID)
	if step.Status != workflowRunStatusSucceeded || step.ProviderRequestID == nil || *step.ProviderRequestID != asrWorkflowTaskID {
		t.Fatalf("finalized workflow step = %#v", step)
	}
	var reservations []models.CreditReservation
	if err := fixture.app.db.Where("workflow_run_id = ?", runID).Find(&reservations).Error; err != nil {
		t.Fatalf("load ASR reservations: %v", err)
	}
	if len(reservations) != 1 {
		t.Fatalf("reservations = %d, want 1", len(reservations))
	}
	stored, _ := json.Marshal(struct {
		Step        models.WorkflowStepExecution
		Reservation models.CreditReservation
	}{step, reservations[0]})
	for _, forbidden := range []string{"private-asr-workflow-audio.wav", "provider transcript secret", "file_url", "transcripts"} {
		if bytes.Contains(stored, []byte(forbidden)) {
			t.Fatalf("stored metadata leaked %q: %s", forbidden, stored)
		}
	}
}

const scribeWorkflowModel = "scribe-workflow-model"

type scribeWorkflowFixture struct {
	app           *App
	token         string
	user          models.User
	project       models.Project
	submitCalls   atomic.Int32
	queryCalls    atomic.Int32
	pending       atomic.Bool
	failQuery     atomic.Bool
	zeroDuration  atomic.Bool
	blockSubmit   atomic.Bool
	submitStarted chan struct{}
	releaseSubmit chan struct{}
	startedOnce   sync.Once
}

func TestElevenLabsScribeWorkflowMultipartIdentityPersistsSubmission(t *testing.T) {
	fixture := newScribeWorkflowFixture(t)
	run := createScribeWorkflowRun(t, fixture, "scribe-submit-input", "scribe-submit-client")

	response := submitScribeRequest(t, fixture, &run)

	if response.Code != http.StatusOK {
		t.Fatalf("scribe submit = %d %s", response.Code, response.Body.String())
	}
	if fixture.submitCalls.Load() != 1 {
		t.Fatalf("Scribe provider calls = %d, want 1", fixture.submitCalls.Load())
	}
	step := loadScribeWorkflowStep(t, fixture, run.ID)
	if step.ProviderRequestID == nil || *step.ProviderRequestID != workflowProviderTaskIDElevenLabs || step.ProviderInputHash == nil {
		t.Fatalf("stored Scribe submission = %#v", step)
	}
}

func TestElevenLabsScribeWorkflowConcurrentSubmissionUsesDurableClaim(t *testing.T) {
	fixture := newScribeWorkflowFixture(t)
	run := createScribeWorkflowRun(t, fixture, "scribe-concurrent-input", "scribe-concurrent-client")
	fixture.blockSubmit.Store(true)
	firstResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstResult <- submitScribeRequest(t, fixture, &run)
	}()
	<-fixture.submitStarted

	second := submitScribeRequest(t, fixture, &run)
	close(fixture.releaseSubmit)
	first := <-firstResult

	assertVisionWorkflowErrorResponse(t, second, http.StatusConflict, "workflow_provider_submission_in_progress")
	if fixture.submitCalls.Load() != 1 {
		t.Fatalf("Scribe provider submissions = %d, want 1", fixture.submitCalls.Load())
	}
	if first.Code != http.StatusOK {
		t.Fatalf("first Scribe submission = %d %s", first.Code, first.Body.String())
	}
}

func TestElevenLabsScribeWorkflowPendingUsesConfiguredModelWithoutLegacyJob(t *testing.T) {
	fixture := newScribeWorkflowFixture(t)
	run := createScribeWorkflowRun(t, fixture, "scribe-pending-input", "scribe-pending-client")
	if response := submitScribeRequest(t, fixture, &run); response.Code != http.StatusOK {
		t.Fatalf("scribe submit = %d %s", response.Code, response.Body.String())
	}
	fixture.app.elevenLabsScribeJobs = sync.Map{}
	fixture.app.elevenLabsScribeBilled = sync.Map{}
	fixture.pending.Store(true)

	response := request(fixture.app, http.MethodGet, scribeWorkflowQueryPath(fixture, run), nil, fixture.token)

	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"status":"processing"`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"model":"`+scribeWorkflowModel+`"`)) {
		t.Fatalf("pending workflow query = %d %s", response.Code, response.Body.String())
	}
	if step := loadScribeWorkflowStep(t, fixture, run.ID); step.Status != workflowRunStatusRunning {
		t.Fatalf("pending Scribe step = %#v", step)
	}
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
}

func TestElevenLabsScribeLegacyPendingKeepsStoredJobModel(t *testing.T) {
	fixture := newScribeWorkflowFixture(t)
	if response := submitScribeRequest(t, fixture, nil); response.Code != http.StatusOK {
		t.Fatalf("legacy Scribe submit = %d %s", response.Code, response.Body.String())
	}
	fixture.pending.Store(true)

	response := request(fixture.app, http.MethodGet, "/api/v1/tools/elevenlabs/scribe/transcribe/"+workflowProviderTaskIDElevenLabs, nil, fixture.token)

	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"model":"`+scribeWorkflowModel+`"`)) {
		t.Fatalf("legacy pending query = %d %s", response.Code, response.Body.String())
	}
}

func TestElevenLabsScribeWorkflowQueryFinalizesOnceWithoutLegacyMaps(t *testing.T) {
	fixture := newScribeWorkflowFixture(t)
	run := createScribeWorkflowRun(t, fixture, "scribe-success-input", "scribe-success-client")
	if response := submitScribeRequest(t, fixture, &run); response.Code != http.StatusOK {
		t.Fatalf("scribe submit = %d %s", response.Code, response.Body.String())
	}
	fixture.app.elevenLabsScribeJobs = sync.Map{}
	fixture.app.elevenLabsScribeBilled = sync.Map{}
	queryPath := scribeWorkflowQueryPath(fixture, run)

	first := request(fixture.app, http.MethodGet, queryPath, nil, fixture.token)
	if first.Code != http.StatusOK || !bytes.Contains(first.Body.Bytes(), []byte(`"status":"completed"`)) || !bytes.Contains(first.Body.Bytes(), []byte("Scribe workflow transcript")) {
		t.Fatalf("first workflow query = %d %s", first.Code, first.Body.String())
	}
	assertScribeWorkflowFinalized(t, fixture, run.ID)

	fixture.app.elevenLabsScribeJobs = sync.Map{}
	fixture.app.elevenLabsScribeBilled = sync.Map{}
	second := request(fixture.app, http.MethodGet, queryPath, nil, fixture.token)
	if second.Code != http.StatusOK {
		t.Fatalf("repeated workflow query = %d %s", second.Code, second.Body.String())
	}
	assertScribeWorkflowFinalized(t, fixture, run.ID)
}

func TestElevenLabsScribeWorkflowQueryProviderFailureIsRetryableWithoutLegacyMaps(t *testing.T) {
	fixture := newScribeWorkflowFixture(t)
	run := createScribeWorkflowRun(t, fixture, "scribe-failure-input", "scribe-failure-client")
	if response := submitScribeRequest(t, fixture, &run); response.Code != http.StatusOK {
		t.Fatalf("scribe submit = %d %s", response.Code, response.Body.String())
	}
	fixture.app.elevenLabsScribeJobs = sync.Map{}
	fixture.app.elevenLabsScribeBilled = sync.Map{}
	fixture.failQuery.Store(true)
	queryPath := scribeWorkflowQueryPath(fixture, run)

	failure := request(fixture.app, http.MethodGet, queryPath, nil, fixture.token)

	assertVisionWorkflowErrorResponse(t, failure, http.StatusBadGateway, "provider_unavailable")
	step := loadScribeWorkflowStep(t, fixture, run.ID)
	if step.Status != workflowStepStatusFailed || step.ProviderRequestID == nil || *step.ProviderRequestID != workflowProviderTaskIDElevenLabs || step.ProviderInputHash == nil {
		t.Fatalf("failed Scribe step = %#v", step)
	}
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)

	fixture.failQuery.Store(false)
	retry := request(fixture.app, http.MethodGet, queryPath, nil, fixture.token)
	if retry.Code != http.StatusOK {
		t.Fatalf("Scribe query retry = %d %s", retry.Code, retry.Body.String())
	}
	assertScribeWorkflowFinalized(t, fixture, run.ID)
}

func TestElevenLabsScribeWorkflowZeroDurationFailsWithoutReservation(t *testing.T) {
	fixture := newScribeWorkflowFixture(t)
	run := createScribeWorkflowRun(t, fixture, "scribe-zero-duration-input", "scribe-zero-duration-client")
	if response := submitScribeRequest(t, fixture, &run); response.Code != http.StatusOK {
		t.Fatalf("scribe submit = %d %s", response.Code, response.Body.String())
	}
	fixture.app.elevenLabsScribeJobs = sync.Map{}
	fixture.app.elevenLabsScribeBilled = sync.Map{}
	fixture.zeroDuration.Store(true)

	response := request(fixture.app, http.MethodGet, scribeWorkflowQueryPath(fixture, run), nil, fixture.token)

	assertVisionWorkflowErrorResponse(t, response, http.StatusBadGateway, "provider_protocol_error")
	assertWorkflowProviderProtocolFailure(t, fixture.app, run.ID, asrWorkflowStepID)
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
}

func newScribeWorkflowFixture(t *testing.T) *scribeWorkflowFixture {
	t.Helper()
	fixture := &scribeWorkflowFixture{submitStarted: make(chan struct{}), releaseSubmit: make(chan struct{})}
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/speech-to-text":
			call := fixture.submitCalls.Add(1)
			if fixture.blockSubmit.Load() && call == 1 {
				fixture.startedOnce.Do(func() { close(fixture.submitStarted) })
				<-fixture.releaseSubmit
			}
			body, _ := io.ReadAll(r.Body)
			for _, key := range workflowRequestIdentityKeys {
				if bytes.Contains(body, []byte(key)) {
					t.Fatalf("workflow identity leaked to Scribe provider: %s", body)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"transcription_id": workflowProviderTaskIDElevenLabs, "request_id": "scribeRequest_01JQ7Y8K9M2N4P6R8T0V3W5X7Z"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/speech-to-text/transcripts/"+workflowProviderTaskIDElevenLabs:
			fixture.queryCalls.Add(1)
			if fixture.pending.Load() {
				http.Error(w, "transcription pending", http.StatusNotFound)
				return
			}
			if fixture.failQuery.Load() {
				http.Error(w, "private Scribe provider failure", http.StatusBadGateway)
				return
			}
			result := map[string]any{
				"language_code": "en",
				"text":          "Scribe workflow transcript",
				"words": []map[string]any{
					{"type": "word", "text": "Scribe", "start": 0.0, "end": 1.0, "speaker_id": "speaker_0"},
					{"type": "word", "text": "workflow", "start": 1.0, "end": 2.0, "speaker_id": "speaker_0"},
				},
			}
			if fixture.zeroDuration.Load() {
				result["words"] = []map[string]any{}
			}
			_ = json.NewEncoder(w).Encode(result)
		default:
			t.Fatalf("unexpected ElevenLabs request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(providerServer.Close)

	app := newTestApp(t)
	app.cfg.ElevenLabsScribeModel = scribeWorkflowModel
	app.elevenlabs = providers.NewElevenLabsClient(config.Config{ElevenLabsAPIKey: "scribe-workflow-key", ElevenLabsBaseURL: providerServer.URL, ElevenLabsScribeModel: scribeWorkflowModel})
	seedBillingRates(t, app, map[string]any{"asr": []map[string]any{{"provider": "elevenlabs", "model_id": scribeWorkflowModel, "credits_per_minute": 1}}})
	email := "scribe-workflow-" + uuid.NewString() + "@example.com"
	fixture.app = app
	fixture.token = registerAndLoginWithCredits(t, app, email, 100)
	fixture.user = lookupUserByEmail(t, app, email)
	fixture.project = createTestProject(t, app, fixture.token, "Scribe workflow project")
	return fixture
}

func createScribeWorkflowRun(t *testing.T, fixture *scribeWorkflowFixture, inputHash, clientID string) models.WorkflowRun {
	t.Helper()
	return createWorkflowIdentityTestWebRun(t, fixture.app, fixture.user.ID, fixture.project.ID, inputHash, clientID)
}

func submitScribeRequest(t *testing.T, fixture *scribeWorkflowFixture, run *models.WorkflowRun) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("project_id", fixture.project.ID.String()); err != nil {
		t.Fatalf("write project id: %v", err)
	}
	if run != nil {
		for key, value := range map[string]string{
			"workflow_run_id": run.ID.String(), "workflow_step_id": asrWorkflowStepID,
			"workflow_input_hash": run.InputHash, "client_instance_id": run.ClientInstanceID,
		} {
			if err := writer.WriteField(key, value); err != nil {
				t.Fatalf("write workflow form field: %v", err)
			}
		}
	}
	part, err := writer.CreateFormFile("file", "private-scribe-audio.wav")
	if err != nil {
		t.Fatalf("create audio part: %v", err)
	}
	_, _ = part.Write([]byte("private scribe audio"))
	_ = writer.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/elevenlabs/scribe/transcribe", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+fixture.token)
	response := httptest.NewRecorder()
	fixture.app.Router().ServeHTTP(response, req)
	return response
}

func scribeWorkflowQueryPath(fixture *scribeWorkflowFixture, run models.WorkflowRun) string {
	values := url.Values{"project_id": {fixture.project.ID.String()}, "workflow_run_id": {run.ID.String()}, "workflow_step_id": {asrWorkflowStepID}, "workflow_input_hash": {run.InputHash}, "client_instance_id": {run.ClientInstanceID}}
	return "/api/v1/tools/elevenlabs/scribe/transcribe/" + workflowProviderTaskIDElevenLabs + "?" + values.Encode()
}

func loadScribeWorkflowStep(t *testing.T, fixture *scribeWorkflowFixture, runID uuid.UUID) models.WorkflowStepExecution {
	t.Helper()
	var step models.WorkflowStepExecution
	if err := fixture.app.db.Where("workflow_run_id = ? AND step_id = ?", runID, asrWorkflowStepID).Take(&step).Error; err != nil {
		t.Fatalf("load Scribe workflow step: %v", err)
	}
	return step
}

func assertScribeWorkflowFinalized(t *testing.T, fixture *scribeWorkflowFixture, runID uuid.UUID) {
	t.Helper()
	step := loadScribeWorkflowStep(t, fixture, runID)
	if step.Status != workflowRunStatusSucceeded || step.ProviderRequestID == nil || *step.ProviderRequestID != workflowProviderTaskIDElevenLabs {
		t.Fatalf("finalized Scribe step = %#v", step)
	}
	var reservations []models.CreditReservation
	if err := fixture.app.db.Where("workflow_run_id = ?", runID).Find(&reservations).Error; err != nil {
		t.Fatalf("load Scribe reservations: %v", err)
	}
	if len(reservations) != 1 || reservations[0].ModelID != scribeWorkflowModel || reservations[0].CreditCount != 1 {
		t.Fatalf("Scribe reservations = %#v", reservations)
	}
}
