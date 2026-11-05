package providers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/prompts"
)

func TestGeneralWritingSkillsReachProviderAndTraceActualMessages(t *testing.T) {
	service, err := prompts.NewService("../../prompt")
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"", "video-events-v1"} {
		for _, scope := range []string{"", "mix-continuity-v1", "single-chapter-v1", "restore"} {
			t.Run(source+"/"+scope, func(t *testing.T) {
				var received, completed map[string]any
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
						t.Error(err)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					chunk, _ := json.Marshal(map[string]any{
						"id": "writing-test", "choices": []any{map[string]any{"delta": map[string]any{"content": `{"script":"有依据的正文。","originalClips":[],"issues":[]}`}}},
						"usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 5, "total_tokens": 25},
					})
					io.WriteString(w, "data: "+string(chunk)+"\n\ndata: [DONE]\n\n")
				}))
				defer server.Close()
				ctx := map[string]any{"contract": "general-narration-v3", "purpose": "script", "sourcePolicy": source, "repairScope": scope,
					"evidence":           []any{map[string]any{"id": 1, "startMs": 1000, "endMs": 2000, "text": "他声称要杀人，尚未发生。"}},
					"narrationMixPolicy": "duration-ratio-v1", "narrationRatio": 1,
				}
				if scope == "restore" {
					ctx["expectedText"] = "逐字恢复。"
				}
				client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "test-only", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "test-model"}, prompts.NewBuilder(service))
				err := client.Stream(context.Background(), "script_draft", map[string]any{
					"model_profile": "pro", "model_operation": "narration_outline", "video_context": ctx,
					"video_config": map[string]any{"style_mode": "短剧漫剧", "target_language": "zh-CN"},
				}, func(event string, payload map[string]any) error {
					if event == "completed" {
						completed = payload
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if calls != 1 {
					t.Fatal("writing skill introduced additional provider calls")
				}
				metadata, _ := completed["metadata"].(map[string]any)
				writing, hasWriting := metadata["writing_policy"].(map[string]any)
				if scope == "restore" {
					if hasWriting {
						t.Fatal("exact restoration incorrectly reported creative writing")
					}
					return
				}
				messagesJSON, _ := json.Marshal(received["messages"])
				if !hasWriting || writing["messages_sha256"] != fmt.Sprintf("%x", sha256.Sum256(messagesJSON)) {
					t.Fatal("trace is not the actual messages delivered to the provider")
				}
				if writing["version"] != "general-scene-writer-v2" || writing["brief_version"] != "source-character-ownership-v1" {
					t.Fatal("wrong skill/brief version")
				}
				for _, required := range []string{"自然语言润色", "writingBrief", "角色自称", "每个信息只交付一次", "SELECTED STYLE TEMPLATE"} {
					if !strings.Contains(string(messagesJSON), required) {
						t.Fatalf("actual provider messages lack %s", required)
					}
				}
				if strings.Contains(string(messagesJSON), "长篇连续性与场景推进") != (scope == "") {
					t.Fatal("upstream planning applied at wrong scope")
				}
				if _, ok := metadata["structured_result"].(map[string]any); !ok {
					t.Fatal("trace replaced the existing structured result")
				}
			})
		}
	}
}
