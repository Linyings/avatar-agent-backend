package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/config"
)

func TestMovieStablePrefixPreservesEvidenceAndCacheDisabledSemantics(t *testing.T) {
	for _, mode := range []string{"auto", "off", "openai", "dashscope_explicit"} {
		t.Run(mode, func(t *testing.T) {
			var prefixes []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				messages := payload["messages"].([]any)
				content := messages[1].(map[string]any)["content"]
				var text string
				if blocks, ok := content.([]any); ok {
					first := blocks[0].(map[string]any)
					if first["cache_control"] == nil {
						t.Fatal("missing explicit cache control")
					}
					text = first["text"].(string) + blocks[1].(map[string]any)["text"].(string)
				} else {
					text = content.(string)
				}
				prefix, suffix, ok := strings.Cut(text, "## 当前任务（动态后缀）")
				if !ok || strings.Count(text, "stable-rules") != 1 || strings.Count(text, "roster-marker") != 1 {
					t.Fatalf("duplicated or absent prefix: %s", text)
				}
				if !strings.Contains(suffix, "dynamic-evidence") || !strings.Contains(suffix, "validation_feedback") || strings.Contains(suffix, "cache_context") {
					t.Fatalf("lost dynamic evidence: %s", suffix)
				}
				prefixes = append(prefixes, prefix)
				_, key := payload["prompt_cache_key"]
				if key != (mode == "openai") {
					t.Fatalf("cache controls for mode %s: %v", mode, payload)
				}
				if payload["max_tokens"] != float64(32768) {
					t.Fatalf("movie output budget = %v", payload["max_tokens"])
				}
				w.Header().Set("Content-Type", "text/event-stream")
				data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": `{"summary":"ok","events":[]}`}}}})
				_, _ = w.Write(append(append([]byte("data: "), data...), []byte("\n\ndata: [DONE]\n\n")...))
			}))
			defer server.Close()
			client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "test", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "test", LLMPromptCacheMode: mode, OpenAIPromptCacheKey: true}, nil)
			for _, feedback := range []string{"first", "repair"} {
				err := client.Stream(context.Background(), "frame_narrative_understanding", map[string]any{
					"movie_prompt_version": 1, "cache_context": map[string]any{"rules": "stable-rules", "shared_context": "roster-marker"},
					"prompt": "stable-rules", "material": "dynamic-evidence", "validation_feedback": feedback,
				}, func(string, map[string]any) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
			}
			if prefixes[0] != prefixes[1] {
				t.Fatal("retry changed stable prefix")
			}
		})
	}
}
