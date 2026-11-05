package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
)

func movieHarnessResultURL(run models.WorkflowRun, stepID string) string {
	return "/api/v1/workflow-runs/" + run.ID.String() + "/steps/" + url.PathEscape(stepID) + "/result?" + url.Values{
		"client_instance_id": {run.ClientInstanceID}, "input_hash": {run.InputHash},
	}.Encode()
}

func TestMovieHarnessCreditBlockedResumesAfterTopUp(t *testing.T) {
	f := newStreamToolWorkflowFixture(t, false, false)
	tc := streamToolWorkflowEndpointCases()[1]
	run := createWorkflowIdentityTestRun(t, f.app, f.user.ID, "movie-credit-retry", "movie-client")
	body := withStreamToolWorkflowIdentity(tc.payload, run, tc.stepID)
	account := f.app.db.Model(&models.UserTokenAccount{}).Where("user_id = ?", f.user.ID)
	if err := account.Updates(map[string]any{"total_tokens": 0, "used_tokens": 0}).Error; err != nil {
		t.Fatal(err)
	}
	first := request(f.app, http.MethodPost, tc.path, body, f.token)
	if first.Code != http.StatusPaymentRequired || f.calls.Load() != 0 {
		t.Fatalf("preflight: %d %s calls=%d", first.Code, first.Body.String(), f.calls.Load())
	}
	lookup := request(f.app, http.MethodGet, movieHarnessResultURL(run, tc.stepID), nil, f.token)
	if !strings.Contains(lookup.Body.String(), `"status":"not_started"`) {
		t.Fatalf("blocked lookup: %s", lookup.Body.String())
	}
	// A resume opportunity does not bypass the normal credit check.
	request(f.app, http.MethodPost, "/api/v1/workflow-runs/"+run.ID.String()+"/resume", map[string]any{"client_instance_id": run.ClientInstanceID}, f.token)
	stillEmpty := request(f.app, http.MethodPost, tc.path, body, f.token)
	if stillEmpty.Code != http.StatusPaymentRequired || f.calls.Load() != 0 {
		t.Fatalf("empty retry: %d calls=%d", stillEmpty.Code, f.calls.Load())
	}
	if err := account.Update("total_tokens", 10000).Error; err != nil {
		t.Fatal(err)
	}
	request(f.app, http.MethodPost, "/api/v1/workflow-runs/"+run.ID.String()+"/resume", map[string]any{"client_instance_id": run.ClientInstanceID}, f.token)
	retry := request(f.app, http.MethodPost, tc.path, body, f.token)
	if !strings.Contains(retry.Body.String(), "event:completed") || f.calls.Load() != 1 {
		t.Fatalf("funded retry: %d %s calls=%d", retry.Code, retry.Body.String(), f.calls.Load())
	}
	replay := request(f.app, http.MethodPost, tc.path, body, f.token)
	if !strings.Contains(replay.Body.String(), "event:completed") || f.calls.Load() != 1 {
		t.Fatal("completed result was not reused")
	}
}

func TestMovieHarnessCreditBlockedWithProviderEvidenceIsNotRestarted(t *testing.T) {
	for _, field := range []string{"provider_request_id", "provider_submission_claim_id", "result", "reservation"} {
		t.Run(field, func(t *testing.T) {
			f := newStreamToolWorkflowFixture(t, false, false)
			tc := streamToolWorkflowEndpointCases()[1]
			run := createWorkflowIdentityTestRun(t, f.app, f.user.ID, "movie-credit-evidence", "movie-client")
			code, message := "insufficient_credits", "insufficient credits"
			step := models.WorkflowStepExecution{WorkflowRunID: run.ID, StepID: tc.stepID, InputHash: run.InputHash, ExecutionKey: workflowStepExecutionKey(run.ID, tc.stepID, run.InputHash), Status: workflowRunStatusBlocked, ErrorCode: &code, ErrorMessage: &message}
			value := "provider-evidence"
			switch field {
			case "provider_request_id":
				step.ProviderRequestID = &value
			case "provider_submission_claim_id":
				step.ProviderSubmissionClaimID = &value
			case "result":
				step.Result = []byte(`{"content":"saved answer"}`)
			}
			if err := f.app.db.Create(&step).Error; err != nil {
				t.Fatal(err)
			}
			if field == "reservation" {
				reservation := models.CreditReservation{UserID: f.user.ID, WorkflowRunID: &run.ID, WorkflowStepExecutionID: &step.ID, CreditCount: 10, Status: creditReservationPending, IdempotencyKey: "guard-" + step.ID.String(), ExpiresAt: time.Now().Add(time.Hour)}
				if err := f.app.db.Create(&reservation).Error; err != nil {
					t.Fatal(err)
				}
			}
			lookup := request(f.app, http.MethodGet, movieHarnessResultURL(run, tc.stepID), nil, f.token)
			if strings.Contains(lookup.Body.String(), `"status":"not_started"`) || f.calls.Load() != 0 {
				t.Fatalf("unsafe restart: %s", lookup.Body.String())
			}
		})
	}
}

func TestMovieHarnessCompletedResultReplaysWithoutProviderOrSecondReservation(t *testing.T) {
	cases := append(streamToolWorkflowEndpointCases(), narrativeWorkflowEndpoint)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newStreamToolWorkflowFixture(t, false, false)
			run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "movie-result-"+tc.name, "movie-result-client")
			payload := tc.payload
			if tc.name == "narrative" {
				payload = narrativeWorkflowPayload()
			}
			body := withStreamToolWorkflowIdentity(payload, run, tc.stepID)
			first := request(fixture.app, http.MethodPost, tc.path, body, fixture.token)
			if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "event:completed") {
				t.Fatalf("first: %d %s", first.Code, first.Body.String())
			}
			fixture.fail = true
			replay := request(fixture.app, http.MethodPost, tc.path, body, fixture.token)
			if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), "event:completed") {
				t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
			}
			if fixture.calls.Load() != 1 {
				t.Fatalf("provider called %d times", fixture.calls.Load())
			}
			assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 1)
			result := request(fixture.app, http.MethodGet, movieHarnessResultURL(run, tc.stepID), nil, fixture.token)
			var data map[string]any
			if result.Code != http.StatusOK || json.Unmarshal(result.Body.Bytes(), &data) != nil || data["status"] != "succeeded" || data["result"] == nil {
				t.Fatalf("result: %d %s", result.Code, result.Body.String())
			}
			listing := request(fixture.app, http.MethodGet, "/api/v1/workflow-runs/"+run.ID.String(), nil, fixture.token)
			if strings.Contains(listing.Body.String(), "approved script") {
				t.Fatal("large provider output leaked into ordinary run list")
			}
		})
	}
}

func TestMovieHarnessResultOwnershipAndTerminalRunAccess(t *testing.T) {
	fixture := newStreamToolWorkflowFixture(t, false, false)
	tc := streamToolWorkflowEndpointCases()[0]
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "movie-owned", "movie-client")
	missing := request(fixture.app, http.MethodGet, movieHarnessResultURL(run, tc.stepID), nil, fixture.token)
	if !strings.Contains(missing.Body.String(), `"status":"not_started"`) {
		t.Fatalf("missing: %s", missing.Body.String())
	}
	request(fixture.app, http.MethodPost, tc.path, withStreamToolWorkflowIdentity(tc.payload, run, tc.stepID), fixture.token)
	otherToken := registerAndLoginWithCredits(t, fixture.app, "movie-other-"+uuid.NewString()+"@example.com", 10000)
	for _, access := range []struct {
		token, address string
		status         int
	}{
		{otherToken, movieHarnessResultURL(run, tc.stepID), http.StatusNotFound},
		{fixture.token, strings.Replace(movieHarnessResultURL(run, tc.stepID), "movie-client", "other-client", 1), http.StatusForbidden},
		{fixture.token, strings.Replace(movieHarnessResultURL(run, tc.stepID), "movie-owned", "different-hash", 1), http.StatusConflict},
	} {
		response := request(fixture.app, http.MethodGet, access.address, nil, access.token)
		if response.Code != access.status || strings.Contains(response.Body.String(), "approved script") {
			t.Fatalf("private result: %d %s", response.Code, response.Body.String())
		}
	}
	for _, status := range []string{workflowRunStatusInterrupted, workflowRunStatusExpired} {
		if err := fixture.app.db.Model(&models.WorkflowRun{}).Where("id = ?", run.ID).Update("status", status).Error; err != nil {
			t.Fatal(err)
		}
		response := request(fixture.app, http.MethodGet, movieHarnessResultURL(run, tc.stepID), nil, fixture.token)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"succeeded"`) {
			t.Fatalf("terminal result: %d %s", response.Code, response.Body.String())
		}
	}
}

func TestMovieHarnessResultSaveFailureRollsBackBillingAndCannotResubmit(t *testing.T) {
	fixture := newStreamToolWorkflowFixture(t, false, false)
	if err := fixture.app.db.Exec(`CREATE TRIGGER reject_harness_result BEFORE UPDATE ON workflow_step_executions WHEN NEW.result IS NOT NULL BEGIN SELECT RAISE(ABORT, 'simulated result write failure'); END;`).Error; err != nil {
		t.Fatal(err)
	}
	tc := streamToolWorkflowEndpointCases()[0]
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "movie-save-failed", "movie-client")
	body := withStreamToolWorkflowIdentity(tc.payload, run, tc.stepID)
	response := request(fixture.app, http.MethodPost, tc.path, body, fixture.token)
	if strings.Contains(response.Body.String(), "event:completed") || !strings.Contains(response.Body.String(), "workflow_result_unknown") {
		t.Fatalf("failed persistence: %s", response.Body.String())
	}
	assertVisionWorkflowReservationCount(t, fixture.app, run.ID, 0)
	result := request(fixture.app, http.MethodGet, movieHarnessResultURL(run, tc.stepID), nil, fixture.token)
	if strings.Contains(result.Body.String(), "approved script") || !strings.Contains(result.Body.String(), `"status":"unknown"`) || !strings.Contains(result.Body.String(), `"retryable":false`) {
		t.Fatalf("failed result query: %s", result.Body.String())
	}
	// Billing rolled back with the failed write, so the client may tell this apart from a charged call.
	if !strings.Contains(result.Body.String(), `"billed":false`) {
		t.Fatalf("unknown result should report that nothing was billed: %s", result.Body.String())
	}
	expired := time.Now().Add(-time.Hour)
	if err := fixture.app.db.Model(&models.WorkflowStepExecution{}).Where("workflow_run_id = ?", run.ID).Update("lease_expires_at", expired).Error; err != nil {
		t.Fatal(err)
	}
	retry := request(fixture.app, http.MethodPost, tc.path, body, fixture.token)
	assertVisionWorkflowErrorResponse(t, retry, http.StatusConflict, "workflow_result_unknown")
	if fixture.calls.Load() != 1 {
		t.Fatalf("unconfirmed provider was repeated %d times", fixture.calls.Load())
	}
}

func TestMovieHarnessConcurrentRequestCannotStartSecondProvider(t *testing.T) {
	fixture := newStreamToolWorkflowFixture(t, false, false)
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	defer finish()
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		content := `{"items":[{"_id":"1","text":"approved script","start":0,"end":1,"org":""}]}`
		fmt.Fprintf(w, "data: {\"id\":\"movie-one\",\"model\":\"stream-tool-model\",\"choices\":[{\"delta\":{\"content\":%q}}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":8,\"total_tokens\":20}}\n\ndata: [DONE]\n\n", content)
	}))
	defer provider.Close()
	fixture.app.cfg.OpenAIProAPIBaseURL = provider.URL
	fixture.app.openai = providers.NewOpenAIClient(fixture.app.cfg, fixture.app.promptBuilder)
	tc := streamToolWorkflowEndpointCases()[0]
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "movie-concurrent", "movie-client")
	body := withStreamToolWorkflowIdentity(tc.payload, run, tc.stepID)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- request(fixture.app, http.MethodPost, tc.path, body, fixture.token) }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("provider did not start")
	}
	repeated := request(fixture.app, http.MethodPost, tc.path, body, fixture.token)
	assertVisionWorkflowErrorResponse(t, repeated, http.StatusConflict, "workflow_step_running")
	result := request(fixture.app, http.MethodGet, movieHarnessResultURL(run, tc.stepID), nil, fixture.token)
	if !strings.Contains(result.Body.String(), `"status":"running"`) {
		t.Fatalf("running result: %s", result.Body.String())
	}
	finish()
	select {
	case response := <-done:
		if !strings.Contains(response.Body.String(), "event:completed") {
			t.Fatalf("first call failed: %s", response.Body.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("provider did not finish")
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d", calls.Load())
	}
}

func TestMovieHarnessKnownFailureAllowsRetryAndUnknownClaimDoesNot(t *testing.T) {
	fixture := newStreamToolWorkflowFixture(t, true, false)
	tc := streamToolWorkflowEndpointCases()[0]
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "movie-retry", "movie-client")
	body := withStreamToolWorkflowIdentity(tc.payload, run, tc.stepID)
	request(fixture.app, http.MethodPost, tc.path, body, fixture.token)
	fixture.fail = false
	retry := request(fixture.app, http.MethodPost, tc.path, body, fixture.token)
	if !strings.Contains(retry.Body.String(), "event:completed") || fixture.calls.Load() != 2 {
		t.Fatalf("known failure retry: %s calls=%d", retry.Body.String(), fixture.calls.Load())
	}
	// Simulate an older successful step without its stored completion body.
	if err := fixture.app.db.Model(&models.WorkflowStepExecution{}).Where("workflow_run_id = ?", run.ID).Update("result", nil).Error; err != nil {
		t.Fatal(err)
	}
	unknown := request(fixture.app, http.MethodPost, tc.path, body, fixture.token)
	assertVisionWorkflowErrorResponse(t, unknown, http.StatusConflict, "workflow_result_unknown")
	if fixture.calls.Load() != 2 {
		t.Fatal("missing stored result caused another paid call")
	}
}

func TestMovieHarnessStoredStreamCanReplayWhenProviderIsNoLongerConfigured(t *testing.T) {
	fixture := newStreamToolWorkflowFixture(t, false, false)
	tc := streamToolWorkflowEndpointCases()[0]
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "movie-offline-replay", "movie-client")
	body := withStreamToolWorkflowIdentity(tc.payload, run, tc.stepID)
	first := request(fixture.app, http.MethodPost, tc.path, body, fixture.token)
	if !strings.Contains(first.Body.String(), "event:completed") {
		t.Fatalf("first: %s", first.Body.String())
	}
	fixture.app.openai = providers.NewOpenAIClient(config.Config{}, fixture.app.promptBuilder)
	replay := request(fixture.app, http.MethodPost, tc.path, body, fixture.token)
	if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), "event:completed") || fixture.calls.Load() != 1 {
		t.Fatalf("offline replay: %d %s", replay.Code, replay.Body.String())
	}
}

func TestMovieHarnessAbandonedStreamIsReportedAsUnbilled(t *testing.T) {
	fixture := newStreamToolWorkflowFixture(t, false, false)
	tc := streamToolWorkflowEndpointCases()[0]
	run := createWorkflowIdentityTestRun(t, fixture.app, fixture.user.ID, "movie-abandoned", "movie-client")
	claim, now := "abandoned-claim", time.Now()
	expired := now.Add(-time.Hour)
	fingerprint := "fingerprint"
	step := models.WorkflowStepExecution{WorkflowRunID: run.ID, StepID: tc.stepID, InputHash: run.InputHash,
		ExecutionKey: workflowStepExecutionKey(run.ID, tc.stepID, run.InputHash), Status: workflowRunStatusRunning,
		LeaseOwner: &run.ClientInstanceID, LeaseExpiresAt: &expired, ResultVersion: 1,
		ProviderInputHash: &fingerprint, ProviderSubmissionClaimID: &claim, ProviderSubmissionClaimedAt: &now}
	if err := fixture.app.db.Create(&step).Error; err != nil {
		t.Fatal(err)
	}
	result := request(fixture.app, http.MethodGet, movieHarnessResultURL(run, tc.stepID), nil, fixture.token)
	body := result.Body.String()
	if !strings.Contains(body, `"status":"unknown"`) || !strings.Contains(body, `"billed":false`) || !strings.Contains(body, `"provider_request_recorded":false`) {
		t.Fatalf("abandoned stream lookup: %s", body)
	}
	if fixture.calls.Load() != 0 {
		t.Fatalf("lookup called the provider %d times", fixture.calls.Load())
	}
}
