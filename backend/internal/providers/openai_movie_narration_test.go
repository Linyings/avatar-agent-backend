package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/prompts"
)

func TestCompleteStoryMoviePromptSurvivesProviderRouting(t *testing.T) {
	service, err := prompts.NewService("../../prompt")
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"movie_director_script", "narration_segment", "narration_global_review", "narration_global_repair"} {
		t.Run(operation, func(t *testing.T) {
			var received map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				chunk, _ := json.Marshal(map[string]any{
					"id":      "movie-test",
					"choices": []any{map[string]any{"delta": map[string]any{"content": `{"items":[{"_id":1,"text":"完整故事解说","start":100,"end":118,"org":"","evidence_refs":["subtitle-1"]}]}`}}},
					"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12},
				})
				io.WriteString(w, "data: "+string(chunk)+"\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "test-key", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "test-model"}, prompts.NewBuilder(service))
			kind := "script_draft"
			if operation == "movie_director_script" {
				kind = "script"
			}
			err := client.Stream(context.Background(), kind, map[string]any{
				"model_profile": "pro", "model_operation": operation,
				"video_config":  map[string]any{"style_mode": "general", "narration_mode": "complete_story", "word_count": 126, "mix_ratio": 30},
				"custom_prompt": "INSTALLED_STORY_SKILL", "video_context": "ENTIRE_STORY_EVIDENCE",
			}, func(string, map[string]any) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(received["messages"])
			for _, text := range []string{"No output runtime", "INSTALLED_STORY_SKILL", "ENTIRE_STORY_EVIDENCE"} {
				if !strings.Contains(string(encoded), text) {
					t.Fatalf("provider prompt missing %s: %s", text, encoded)
				}
			}
			if strings.Contains(string(encoded), "70%-130%") {
				t.Fatal("legacy budget leaked")
			}
			if _, limited := received["max_tokens"]; limited {
				t.Fatal("movie request added a product output cap")
			}
		})
	}
}
