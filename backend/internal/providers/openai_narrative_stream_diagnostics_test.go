package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/narratoai/backend-go/internal/config"
)

func TestNarrativeStreamRetainsTerminationDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name, answer, reason string
		done                 bool
	}{
		{"empty", "", "stop", true}, {"reasoning_exhausted", "", "length", true},
		{"success", "final answer", "stop", true}, {"early_eof", "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if payload["max_tokens"] != float64(32768) || payload["reasoning_effort"] != "none" {
					t.Errorf("movie outline output budget/reasoning = %v/%v", payload["max_tokens"], payload["reasoning_effort"])
				}
				w.Header().Set("Content-Type", "text/event-stream")
				data, _ := json.Marshal(map[string]any{"id": "request-1", "choices": []any{map[string]any{
					"delta": map[string]any{"content": test.answer, "reasoning_content": "private"}, "finish_reason": test.reason,
				}}, "usage": map[string]any{"completion_tokens": 20, "total_tokens": 30}})
				_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
				if test.done {
					_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
				}
			}))
			defer server.Close()
			client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "test", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "test"}, nil)
			var completed map[string]any
			err := client.Stream(context.Background(), "frame_narrative_understanding", map[string]any{"movie_outline": true}, func(event string, data map[string]any) error {
				if event == "completed" {
					completed = data
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if completed["content"] != test.answer || completed["finish_reason"] != test.reason || completed["stream_done"] != test.done || completed["stream_chunks"] != 1 || completed["thinking_characters"] != 7 {
				t.Fatalf("unexpected diagnostics: %#v", completed)
			}
			if _, leaked := completed["reasoning_content"]; leaked {
				t.Fatal("private reasoning leaked")
			}
		})
	}
}
