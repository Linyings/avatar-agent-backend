package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/prompts"
)

// The skills must reach the writing model in the real HTTP request, and the result must say so.
func TestMovieWritingSendsSceneWriterSkillsToTheWritingModel(t *testing.T) {
	service, err := prompts.NewService("../../prompt")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		purpose string
		skills  bool
	}{{"movie_screenplay_full_draft", true}, {"movie_screenplay_user_rewrite", true}, {"movie_screenplay_full_review", false}} {
		t.Run(tc.purpose, func(t *testing.T) {
			var sentModel, sentText string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				sentModel, _ = payload["model"].(string)
				raw, _ := json.Marshal(payload["messages"])
				sentText = string(raw)
				w.Header().Set("Content-Type", "text/event-stream")
				data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": `{"text":"正文"}`}}}})
				_, _ = w.Write(append(append([]byte("data: "), data...), []byte("\n\ndata: [DONE]\n\n")...))
			}))
			defer server.Close()
			cfg := config.Config{OpenAIProAPIKey: "pro", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "pro", OpenAIFlashAPIKey: "flash", OpenAIFlashAPIBaseURL: server.URL, OpenAIFlashModel: "flash",
				MovieNarrationAPIKey: "writer", MovieNarrationAPIBaseURL: server.URL, MovieNarrationModel: "writer"}
			var completed map[string]any
			err := NewOpenAIClient(cfg, prompts.NewBuilder(service)).Stream(context.Background(), "script_draft", map[string]any{
				"movie_prompt_version": 1, "model_operation": "narration_outline", "model_profile": "pro", "custom_prompt": `只返回 {"text":""}。`,
				"video_context": map[string]any{"purpose": tc.purpose, "materials": []any{"e1"}},
			}, func(event string, data map[string]any) error {
				if event == "completed" {
					completed = data
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(sentText, "SCENE WRITING SKILLS movie-scene-writer-v1"); got != tc.skills {
				t.Fatalf("skills sent=%v want %v", got, tc.skills)
			}
			metadata, _ := completed["metadata"].(map[string]any)
			policy, _ := metadata["writing_policy"].(map[string]any)
			if (policy != nil) != tc.skills {
				t.Fatalf("writing_policy = %#v", metadata["writing_policy"])
			}
			if tc.skills && (policy["version"] != "movie-scene-writer-v1" || len(policy["messages_sha256"].(string)) != 64 || sentModel != "writer") {
				t.Fatalf("policy=%#v model=%s", policy, sentModel)
			}
		})
	}
}
