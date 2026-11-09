package providers

import (
	"context"
	"encoding/json"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAvatarContentShotBudgetAndLocalPreflight(t *testing.T) {
	t.Setenv("AVATAR_SHOT_MAX_TOKENS", "7000")
	t.Setenv("AVATAR_MODEL_MAX_OUTPUT_TOKENS", "4096")
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["max_tokens"] != float64(4096) {
			t.Error("server output cap ignored")
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"shots\":[]}"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	client := NewOpenAIClient(config.Config{OpenAIFlashAPIKey: "test", OpenAIFlashAPIBaseURL: srv.URL, OpenAIFlashModel: "model"}, nil)
	ctx := avatar.WithShotPlanning(context.Background())
	if _, err := client.AvatarContent(ctx, "plan", strings.Repeat("x", 25000), nil); err == nil {
		t.Fatal("oversized request allowed")
	}
	if calls != 0 {
		t.Fatal("preflight reached provider")
	}
	if _, err := client.AvatarContent(ctx, "plan", "small", nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("unexpected paid retry")
	}
}

func TestAvatarContentMotionCodeContractIsContextScoped(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		prompt := body["messages"].([]any)[0].(map[string]any)["content"].(string)
		if calls == 1 {
			format, _ := body["response_format"].(map[string]any)
			if !strings.Contains(prompt, "禁止输出代码") || format["type"] != "json_object" {
				t.Error("ordinary request enabled code")
			}
		} else {
			format, _ := body["response_format"].(map[string]any)
			if strings.Contains(prompt, "禁止输出代码") || format["type"] != "json_object" {
				t.Error("motion code must have a JSON response contract")
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "{\"ok\":true}"}}}})
	}))
	defer srv.Close()
	client := NewOpenAIClient(config.Config{OpenAIFlashAPIKey: "test", OpenAIFlashAPIBaseURL: srv.URL, OpenAIFlashModel: "model"}, nil)
	for _, ctx := range []context.Context{context.Background(), avatar.WithMotionCode(context.Background())} {
		if _, err := client.AvatarContent(ctx, "design", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAvatarContentBoundedProtocolAndVisionProfile(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != false {
			t.Error("unbounded streaming")
		}
		if body["reasoning_effort"] != nil || body["thinking"] != nil {
			t.Error("must not inject unsupported provider-specific reasoning parameters by default")
		}
		if calls == 2 && body["model"] != "vision-model" {
			t.Error("vision profile ignored")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"ok\":true}"}}],"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}}`))
	}))
	defer srv.Close()
	cfg := config.Config{OpenAIFlashAPIKey: "private", OpenAIFlashAPIBaseURL: srv.URL, OpenAIFlashModel: "text-model", MovieVisionAPIKey: "vision-private", MovieVisionAPIBaseURL: srv.URL, MovieVisionModel: "vision-model"}
	client := NewOpenAIClient(cfg, nil)
	for _, frames := range [][]string{nil, {"data:image/jpeg;base64,test"}} {
		raw, err := client.AvatarContent(context.Background(), "return JSON", map[string]string{"script": "test"}, frames)
		if err != nil || string(raw) != `{"ok":true}` {
			t.Fatalf("%s %v", raw, err)
		}
	}
	if calls != 2 {
		t.Fatal("unexpected extra model calls")
	}
}
