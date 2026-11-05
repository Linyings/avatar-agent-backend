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

func TestMovieDirectorStructuredBudgetAndTruncation(t *testing.T) {
	for _, reason := range []string{"length", "stop", ""} {
		t.Run(reason, func(t *testing.T) {
			content := `{"items":[`
			if reason == "stop" {
				content = `{"items":[{"_id":"1","text":"旁白","start":0,"end":10,"org":""}]}`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if payload["max_tokens"] != float64(32768) {
					t.Errorf("max_tokens = %v", payload["max_tokens"])
				}
				format, _ := payload["response_format"].(map[string]any)
				if format["type"] != "json_object" {
					t.Errorf("response_format = %v", format)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"id\":\"director-test\",\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":%q}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":8192,\"total_tokens\":8292}}\n\ndata: [DONE]\n\n", content, reason)
			}))
			defer server.Close()
			client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "test", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "deepseek-test"}, nil)
			var completed map[string]any
			err := client.Stream(context.Background(), "script", map[string]any{
				"model_operation": "movie_director_script", "movie_prompt_version": 1,
				"cache_context": map[string]any{"rules": "return JSON"}, "video_context": "chapter",
			}, func(event string, data map[string]any) error {
				if event == "completed" {
					completed = data
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if completed["content"] != content || completed["finish_reason"] != reason || completed["request_id"] != "director-test" {
				t.Fatalf("completion lost raw output or metadata: %v", completed)
			}
			if usage, ok := completed["usage"].(ChatUsage); !ok || usage.TotalTokens != 8292 {
				t.Fatalf("completion lost usage: %v", completed["usage"])
			}
		})
	}
}
