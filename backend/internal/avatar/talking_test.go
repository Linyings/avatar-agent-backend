package avatar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/media"
)

func TestTalkingAvatarDrivesTheRegisteredModelWithTheGivenVoice(t *testing.T) {
	var sent map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi/v2/kling-v2-ai-avatar-pro/image-audio-to-video":
			_ = json.NewDecoder(r.Body).Decode(&sent)
			_, _ = w.Write([]byte(`{"taskId":"k-1","status":"RUNNING"}`))
		case "/openapi/v2/query":
			_, _ = w.Write([]byte(`{"taskId":"k-1","status":"SUCCESS","results":[{"url":"https://cdn.example.com/a.mp4","outputType":"mp4"}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	model, ok := media.Find("kling-v2-ai-avatar-pro/image-audio-to-video")
	if !ok {
		t.Fatal("kling avatar pro missing from registry")
	}
	p, err := NewTalkingAvatar(NewRunningHub(server.URL, "key"), model)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Submit(context.Background(), GenerationRequest{Prompt: TalkingPrompt("暖色光线"), ImageURLs: []string{"https://x/face.png"}, AudioURLs: []string{"https://x/voice.wav"}, Duration: "4", GenerateAudio: true})
	if err != nil || res.TaskID != "k-1" {
		t.Fatalf("%#v %v", res, err)
	}
	if sent["imageUrl"] != "https://x/face.png" || sent["audioUrl"] != "https://x/voice.wav" || !strings.Contains(sent["prompt"].(string), "直视镜头") || !strings.Contains(sent["prompt"].(string), "暖色光线") {
		t.Fatalf("talking avatar body: %#v", sent)
	}
	if _, ok := sent["duration"]; ok {
		t.Fatal("talking avatar length follows the voice; no duration may be sent")
	}
	done, err := p.Query(context.Background(), "k-1")
	if err != nil || done.VideoURL() != "https://cdn.example.com/a.mp4" {
		t.Fatalf("%#v %v", done, err)
	}
	if _, err := p.Submit(context.Background(), GenerationRequest{ImageURLs: []string{"https://x/face.png"}}); err == nil {
		t.Fatal("submitted without a driving voice")
	}
	if video, _ := media.Find("kling-v3.0-pro/image-to-video"); true {
		if _, err := NewTalkingAvatar(NewRunningHub(server.URL, "key"), video); err == nil {
			t.Fatal("a non-avatar model was accepted as a talking avatar")
		}
	}
}

func TestTalkingPromptKeepsCompositionOutOfTheProviderRequest(t *testing.T) {
	prompt := TalkingPrompt("右下角圆形小窗口，镜头缓慢推近。保持黑色衣服")
	if strings.Contains(prompt, "圆形小窗口") || strings.Contains(prompt, "推近") || !strings.Contains(prompt, "保持黑色衣服") || !strings.Contains(prompt, "正对镜头") {
		t.Fatalf("prompt: %s", prompt)
	}
}
