package providers

import (
	"context"
	"encoding/json"
	"github.com/narratoai/backend-go/internal/config"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
