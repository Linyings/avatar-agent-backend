package providers

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/narratoai/backend-go/internal/config"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTimelineVisionCustomPromptJSONContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		messages := payload["messages"].([]any)
		content := messages[0].(map[string]any)["content"].([]any)
		prompt := content[0].(map[string]any)["text"].(string)
		if !strings.Contains(prompt, "牛倒地") || !strings.Contains(prompt, "JSON") || !strings.Contains(prompt, "不是秒数") {
			t.Errorf("missing question or output contract: %s", prompt)
		}
		if r.Header.Get("X-DashScope-OssResourceResolve") != "enable" {
			t.Error("missing OSS resolution header")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"牛倒地\\\"}\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client := NewDashScopeClient(config.Config{DashScopeAPIKey: "test", QwenVisionBaseURL: server.URL, QwenVisionModel: "test"})
	result, err := client.AnalyzeTimelineImageStream(context.Background(), "oss://test/image.jpg", "根据画面网格核对牛倒地是否真的发生", "", nil)
	if err != nil || !strings.Contains(result.Content, "牛倒地") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestTimelineVisionRejectsWithoutLeakingProviderBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"code":"InvalidParameter","message":"private-media-url"}`)
	}))
	defer server.Close()
	client := NewDashScopeClient(config.Config{DashScopeAPIKey: "test", QwenVisionBaseURL: server.URL})
	_, err := client.AnalyzeTimelineImageStream(context.Background(), "oss://test/image.jpg", "check JSON", "test", nil)
	var upstream *DashScopeHTTPError
	if !errors.As(err, &upstream) || upstream.StatusCode != 400 || upstream.ProviderCode != "InvalidParameter" {
		t.Fatalf("missing safe diagnostics: %v", err)
	}
	if strings.Contains(err.Error(), "private-media-url") {
		t.Fatal("provider body leaked")
	}
}
