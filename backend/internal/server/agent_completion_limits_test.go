package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/agent"
	"github.com/narratoai/backend-go/internal/config"
)

func TestAgentCompletionSendsBoundedOutputBudget(t *testing.T) {
	for _, tc := range []struct {
		cap  string
		want int
	}{{"32768", 6000}, {"2048", 2048}} {
		t.Run(tc.cap, func(t *testing.T) {
			t.Setenv("AVATAR_MODEL_MAX_OUTPUT_TOKENS", tc.cap)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if payload["max_tokens"] != float64(tc.want) {
					t.Errorf("max_tokens=%v, want %d", payload["max_tokens"], tc.want)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"分镜建议"}}]}`))
			}))
			defer upstream.Close()
			adapter := newAgentLLMAdapter(config.Config{AgentAPIKey: "test", AgentAPIBaseURL: upstream.URL, AgentModel: "test"})
			if _, err := adapter.Complete(context.Background(), agent.LLMRequest{SystemPrompt: "给出分镜建议"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAgentCompletionRejectsTruncationAndEmptyAnswers(t *testing.T) {
	for _, raw := range []string{
		`{"choices":[{"finish_reason":"length","message":{"content":""}}]}`,
		`{"choices":[{"finish_reason":"length","message":{"content":"未完成的建议"}}]}`,
		`{"choices":[{"finish_reason":"length","message":{"tool_calls":[{"id":"a","function":{"name":"read_skill","arguments":"{}"}}]}}]}`,
		`{"choices":[{"finish_reason":"stop","message":{"content":"   "}}]}`,
	} {
		if _, err := normalizeAgentCompletion([]byte(raw)); err == nil {
			t.Fatalf("incomplete completion accepted as success: %s", raw)
		}
	}
	raw := `{"choices":[{"finish_reason":"tool_calls","message":{"content":"","tool_calls":[{"id":"a","function":{"name":"read_skill","arguments":"{\"name\":\"talking-head-storyboard\"}"}}]}}]}`
	result, err := normalizeAgentCompletion([]byte(raw))
	if err != nil || !strings.Contains(result, "read_skill") {
		t.Fatalf("valid tool-only response rejected: %v", err)
	}
}
