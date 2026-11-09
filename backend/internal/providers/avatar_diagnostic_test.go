package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/config"
)

func TestAvatarContentNetworkDiagnosticNeverIncludesRawEndpoint(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{&net.DNSError{Err: "private-dns-details", Name: "private-host"}, "network_dns"},
		{context.DeadlineExceeded, "network_timeout"},
		{io.ErrUnexpectedEOF, "connection_interrupted"},
		{errors.New("private-unknown-error"), "request_failed"},
	} {
		d := (&contentRequestDiagnostic{phase: "request_sent"}).wrap(tc.err).(*contentDiagnosticError)
		if d.code != tc.code || strings.Contains(d.Error(), "private") || !errors.Is(d, tc.err) {
			t.Fatalf("incorrect safe classification: %v", d)
		}
	}
}

func TestAvatarContentFailureRecordsSafeResponseDiagnostic(t *testing.T) {
	for _, tc := range []struct{ body, code string }{{"private-invalid-response", "response_invalid_json"}, {`{"choices":[]}`, "response_no_choices"}, {`{"choices":[{"message":{"content":"not-json-private"}}]}`, "model_invalid_json"}} {
		t.Run(tc.code, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Request-Id", "req-diagnostic")
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			client := NewOpenAIClient(config.Config{OpenAIFlashAPIKey: "private-key", OpenAIFlashAPIBaseURL: srv.URL, OpenAIFlashModel: "test"}, nil)
			path := filepath.Join(t.TempDir(), "operations.jsonl")
			ctx, _ := avatar.BeginOperation(avatar.WithTelemetry(context.Background(), path), "F03", "")
			_, err := client.AvatarContent(ctx, "private-script", map[string]string{"script": "private-script"}, nil)
			if err == nil {
				t.Fatal("expected failed content response")
			}
			data, _ := os.ReadFile(path)
			var event map[string]any
			if json.Unmarshal(data, &event) != nil || event["error_code"] != tc.code || event["http_status"] != float64(200) || event["status"] != "failed" {
				t.Fatalf("missing safe failure evidence: %s", data)
			}
			for _, secret := range []string{"private-key", "private-script", tc.body, srv.URL} {
				if strings.Contains(string(data), secret) {
					t.Fatal("sensitive diagnostics persisted")
				}
			}
		})
	}
}

func TestAvatarContentTruncatedOutputIsCompletedAndKeepsUsage(t *testing.T) {
	for _, content := range []string{`{"needs":[{"prompt":"unfinished`, `{"needs":[]}`, ""} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			json.NewEncoder(w).Encode(map[string]any{"id": "req-truncated", "choices": []any{map[string]any{"finish_reason": "length", "message": map[string]string{"content": content}}}, "usage": map[string]int{"completion_tokens": 6000, "prompt_tokens": 6304, "total_tokens": 12304}})
		}))
		client := NewOpenAIClient(config.Config{OpenAIFlashAPIKey: "private", OpenAIFlashAPIBaseURL: srv.URL, OpenAIFlashModel: "test"}, nil)
		path := filepath.Join(t.TempDir(), "operations.jsonl")
		_, err := client.AvatarContent(avatar.WithTelemetry(context.Background(), path), "plan", nil, nil)
		srv.Close()
		if err == nil || calls != 1 {
			t.Fatalf("truncation must fail without implicit retry: %v calls=%d", err, calls)
		}
		diagnostic, ok := err.(interface{ ProviderOutputReceived() bool })
		if !ok || !diagnostic.ProviderOutputReceived() {
			t.Fatal("received output classified as uncertain submission")
		}
		data, _ := os.ReadFile(path)
		var event map[string]any
		_ = json.Unmarshal(data, &event)
		if event["error_code"] != "model_output_truncated" || event["finish_reason"] != "length" || event["provider_request_id"] != "req-truncated" || event["usage"].(map[string]any)["completion_tokens"] != "6000" {
			t.Fatalf("missing truncation evidence: %s", data)
		}
	}
}

func TestMotionOutputBudgetAndReasoningDiagnostics(t *testing.T) {
	t.Setenv("AVATAR_MOTION_MAX_TOKENS", "14000")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		json.NewDecoder(r.Body).Decode(&request)
		if request["max_tokens"] != float64(14000) {
			t.Errorf("wrong per-scene budget: %v", request["max_tokens"])
		}
		w.Write([]byte(`{"id":"req-scene","choices":[{"finish_reason":"stop","message":{"content":"{\"code\":\"example\"}"}}],"usage":{"prompt_tokens":123,"completion_tokens":9000,"total_tokens":9123,"completion_tokens_details":{"reasoning_tokens":8500,"private":"secret"}}}`))
	}))
	defer srv.Close()
	client := NewOpenAIClient(config.Config{OpenAIFlashAPIKey: "test", OpenAIFlashAPIBaseURL: srv.URL, OpenAIFlashModel: "test"}, nil)
	path := filepath.Join(t.TempDir(), "operations.jsonl")
	_, err := client.AvatarContent(avatar.WithMotionCode(avatar.WithTelemetry(context.Background(), path)), "scene", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var event map[string]any
	json.Unmarshal(raw, &event)
	usage := event["usage"].(map[string]any)
	details, ok := usage["completion_tokens_details"].(map[string]any)
	if !ok || details["reasoning_tokens"] != "8500" || event["max_output_tokens"] != float64(14000) || event["output_bytes"] != float64(len(`{"code":"example"}`)) || strings.Contains(string(raw), "secret") {
		t.Fatalf("missing safe output metrics: %s", raw)
	}
}
