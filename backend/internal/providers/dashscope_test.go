package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/narratoai/backend-go/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDashScopeNormalizeASRQuery(t *testing.T) {
	resultServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"transcripts": []map[string]any{{
				"channel_id": 0,
				"text":       "你好世界",
				"sentences": []map[string]any{{
					"begin_time": 0,
					"end_time":   2000,
					"text":       "你好世界",
					"language":   "zh",
				}},
			}},
		})
	}))
	defer resultServer.Close()

	client := NewDashScopeClient(config.Config{DashScopeAPIKey: "dash"})
	normalized, tokens, err := client.NormalizeASRQuery(context.Background(), "task-1", "qwen3", map[string]any{
		"output": map[string]any{
			"task_status": "SUCCEEDED",
			"result":      map[string]any{"transcription_url": resultServer.URL},
		},
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if tokens != 30 {
		t.Fatalf("tokens = %d, want 30", tokens)
	}
	if normalized["text_content"] != "你好世界" {
		t.Fatalf("text = %#v", normalized["text_content"])
	}
}

func TestDashScopeSubmitASRRetriesTransientReset(t *testing.T) {
	attempts := 0
	client := NewDashScopeClient(config.Config{
		DashScopeAPIKey: "dash",
	})
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return nil, errors.New("read tcp 192.168.3.110:58588->58.144.195.181:443: read: connection reset by peer")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{
					"output": {
						"task_id": "task-1",
						"task_status": "PENDING"
					}
				}`)),
			}, nil
		}),
	}

	resp, err := client.SubmitASR(context.Background(), "fun-asr", "oss://audio.wav", map[string]any{}, true)
	if err != nil {
		t.Fatalf("SubmitASR returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	output, _ := resp["output"].(map[string]any)
	if output["task_id"] != "task-1" {
		t.Fatalf("task_id = %#v, want task-1", output["task_id"])
	}
}

func TestDashScopeSubmitASRReturnsFriendlyTransientError(t *testing.T) {
	client := NewDashScopeClient(config.Config{
		DashScopeAPIKey: "dash",
	})
	client.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("read tcp 192.168.3.110:58588->58.144.195.181:443: read: connection reset by peer")
		}),
	}

	_, err := client.SubmitASR(context.Background(), "fun-asr", "oss://audio.wav", map[string]any{}, true)
	if err == nil {
		t.Fatal("SubmitASR returned nil error")
	}
	if !strings.Contains(err.Error(), "DashScope 服务网络连接被重置") {
		t.Fatalf("error = %q, want friendly reset message", err.Error())
	}
}

func TestDashScopeHTTPErrorRedactsProviderBody(t *testing.T) {
	header := make(http.Header)
	header.Set("X-DashScope-RequestId", "req-1")
	resp := &http.Response{StatusCode: http.StatusBadGateway, Header: header}
	err, ok := newDashScopeHTTPError(resp, []byte(`{"code":"BadGateway","message":"secret media url"}`)).(*DashScopeHTTPError)
	if !ok {
		t.Fatalf("error type = %T, want *DashScopeHTTPError", err)
	}
	if err.RequestID != "req-1" || err.ProviderCode != "BadGateway" || err.BodyLength == 0 || err.BodySHA256 == "" {
		t.Fatalf("unexpected diagnostics: %#v", err)
	}
	if strings.Contains(err.Error(), "secret media url") {
		t.Fatal("provider body leaked into error")
	}
}

func TestDashScopeClientKeepsNonStreamRequestTimeoutsWithoutCappingVisionStreams(t *testing.T) {
	client := NewDashScopeClient(config.Config{DashScopeAPIKey: "dash"})
	if client.httpClient.Timeout != 0 {
		t.Fatalf("http client timeout = %v, want 0 so long vision requests are not capped by Client.Timeout", client.httpClient.Timeout)
	}
	if got := client.requestTimeout(); got != time.Minute {
		t.Fatalf("request timeout = %v, want 1m", got)
	}

	client = NewDashScopeClient(config.Config{
		DashScopeAPIKey:  "dash",
		DashScopeTimeout: 42 * time.Second,
	})
	if got := client.requestTimeout(); got != 42*time.Second {
		t.Fatalf("configured request timeout = %v", got)
	}
}

func TestDashScopeFriendlyTimeoutError(t *testing.T) {
	err := friendlyDashScopeError(context.DeadlineExceeded)
	if err == nil {
		t.Fatal("friendlyDashScopeError returned nil")
	}
	if !strings.Contains(err.Error(), "DashScope 服务响应超时") {
		t.Fatalf("error = %q, want timeout message", err.Error())
	}
}
