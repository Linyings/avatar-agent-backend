package avatar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestTelemetryPreservesFailedStepTimeAndRedactsProviderData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.jsonl")
	ctx := WithTelemetry(context.Background(), path)
	ctx, finish := BeginOperation(ctx, "video_submit", "shot-1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":422,"message":"secret-key https://signed.example","requestId":"req-42"}`))
	}))
	defer server.Close()
	_, err := NewRunningHub(server.URL, "secret-key").Submit(ctx, GenerationRequest{Duration: "4"})
	finish(err)
	events, readErr := ReadOperations(path)
	if readErr != nil || len(events) != 2 {
		t.Fatalf("missing persisted request/step diagnostics: %v %d", readErr, len(events))
	}
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), "secret-key") || strings.Contains(string(encoded), "signed.example") {
		t.Fatal("unsafe upstream content persisted")
	}
	if events[0].ProviderCode != "422" || events[0].RequestID != "req-42" || events[1].Status != "failed" || events[1].Seconds < 0 {
		t.Fatalf("missing safe failure diagnostics: %s", encoded)
	}
}

func TestTelemetryDistinguishesLocalAndUnknownCost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.jsonl")
	ctx := WithTelemetry(context.Background(), path)
	_, local := BeginOperation(ctx, "alignment", "")
	local(nil)
	_, remote := BeginOperation(ctx, "tts", "")
	remote(errors.New("timeout"))
	events, err := ReadOperations(path)
	if err != nil || len(events) != 2 || events[0].CostStatus != "local_compute" || events[1].CostStatus != "unknown" {
		t.Fatal("unknown supplier cost must never become zero")
	}
}
func TestTelemetryCatalogVoiceFailureNeverClaimsLocalCompute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.jsonl")
	ctx := WithTelemetry(context.Background(), path)
	_, finish := BeginOperation(ctx, "catalog_voice", "speaker")
	finish(errors.New("connection interrupted"))
	events, _ := ReadOperations(path)
	if len(events) != 1 || events[0].CostStatus != "unknown" {
		t.Fatal("paid catalog request cost is unknown, not local compute")
	}
}
func TestTelemetryWorkflowModelUsageKeepsUnknownPrice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.jsonl")
	ctx := WithTelemetry(context.Background(), path)
	ctx, finish := BeginOperation(ctx, "F02", "")
	RecordModelUsage(ctx, map[string]any{"total_tokens": 20})
	finish(nil)
	events, _ := ReadOperations(path)
	if len(events) != 2 || events[1].CostStatus != "unknown" {
		t.Fatal("LLM token usage without price is not local compute")
	}
}

func TestTelemetryKeepsSupplierUsageAndTaskIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.jsonl")
	ctx := WithTelemetry(context.Background(), path)
	ctx, finish := BeginOperation(ctx, "video_wait", "shot-2")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"taskId":"job-2","status":"SUCCESS","results":[{"url":"https://example.com/a.mp4","outputType":"mp4"}],"usage":{"thirdPartyConsumeMoney":"1.2","consumeMoney":null,"billingSeconds":"4","secret":"key"}}`))
	}))
	defer server.Close()
	_, err := NewRunningHub(server.URL, "key").Query(ctx, "job-2")
	finish(err)
	events, _ := ReadOperations(path)
	data, _ := json.Marshal(events)
	if len(events) != 2 || events[0].TaskID != "job-2" || events[0].CostStatus != "reported" || strings.Contains(string(data), `"secret"`) {
		t.Fatalf("invalid supplier accounting: %s", data)
	}
}
