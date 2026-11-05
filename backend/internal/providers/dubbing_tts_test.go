package providers

import (
	"context"
	"encoding/json"
	"github.com/narratoai/backend-go/internal/config"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDubbingTTSAudioHostCompatibility(t *testing.T) {
	for _, tc := range []struct {
		address string
		allowed bool
	}{
		{"http://dashscope-a717.oss-cn-beijing.aliyuncs.com/audio.wav?signature=test", true},
		{"https://dashscope-result-bj.oss-cn-beijing.aliyuncs.com/audio.wav", true},
		{"https://dashscope-result-sh.oss-cn-shanghai.aliyuncs.com/audio.wav", true},
		{"https://example.com/audio.wav", false},
		{"https://dashscope-a717.oss-cn-beijing.aliyuncs.com.evil.example/audio.wav", false},
		{"https://dashscope-a717.evil.aliyuncs.com/audio.wav", false},
		{"https://other-bucket.oss-cn-beijing.aliyuncs.com/audio.wav", false},
		{"https://dashscope-a717.subdomain.oss-cn-beijing.aliyuncs.com/audio.wav", false},
		{"https://user:secret@dashscope-a717.oss-cn-beijing.aliyuncs.com/audio.wav", false},
		{"https://dashscope-a717.oss-cn-beijing.aliyuncs.com:8443/audio.wav", false},
		{"file://dashscope-a717.oss-cn-beijing.aliyuncs.com/audio.wav", false},
		{"http://127.0.0.1/audio.wav", false},
		{"", false},
	} {
		t.Run(tc.address, func(t *testing.T) {
			d := NewDashScopeClient(config.Config{QwenTTSModel: "qwen3-tts-instruct-flash", DashScopeAPIKey: "test"})
			downloads := 0
			d.httpClient.Transport = dubbingTransport(func(r *http.Request) (*http.Response, error) {
				body := "RIFF0000WAVE" + strings.Repeat("a", 100)
				if r.Method == "POST" {
					b, _ := json.Marshal(map[string]any{"output": map[string]any{"audio": map[string]any{"url": tc.address}}, "usage": map[string]any{"characters": 2}})
					body = string(b)
				} else {
					downloads++
					if r.URL.Scheme != "https" || r.Header.Get("Authorization") != "" {
						t.Fatal("download must use HTTPS without provider credentials")
					}
					if strings.Contains(tc.address, "signature=test") && r.URL.RawQuery != "signature=test" {
						t.Fatal("signed URL query was altered")
					}
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			audio, chars, err := d.DubbingTextToSpeech(context.Background(), "你好", "qwen3-tts-instruct-flash:Ethan", "", 1)
			if tc.allowed {
				if err != nil || len(audio) != 112 || chars != 2 || downloads != 1 {
					t.Fatalf("audio=%d usage=%d downloads=%d err=%v", len(audio), chars, downloads, err)
				}
			} else if err == nil || downloads != 0 {
				t.Fatalf("untrusted URL downloaded: downloads=%d err=%v", downloads, err)
			}
		})
	}
}

type dubbingTransport func(*http.Request) (*http.Response, error)

func (f dubbingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestDubbingTTSDownloadsURLAndReportsUsage(t *testing.T) {
	d := NewDashScopeClient(config.Config{QwenTTSModel: "qwen3-tts-instruct-flash", DashScopeAPIKey: "test-key"})
	calls := 0
	d.httpClient.Transport = dubbingTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		body := "RIFF0000WAVE" + strings.Repeat("a", 100)
		if r.Method == "POST" {
			var payload map[string]any
			json.NewDecoder(r.Body).Decode(&payload)
			input := payload["input"].(map[string]any)
			if payload["model"] != d.DubbingTTSModel() || input["voice"] != "Ethan" || !strings.Contains(input["instructions"].(string), "1.20") {
				t.Fatalf("wrong synthesis parameters")
			}
			body = `{"output":{"audio":{"data":"","url":"http://dashscope-result-bj.oss-cn-beijing.aliyuncs.com/audio.wav"}},"usage":{"characters":15}}`
		} else if r.URL.Scheme != "https" || r.Header.Get("Authorization") != "" {
			t.Fatal("unsafe download")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	audio, chars, err := d.DubbingTextToSpeech(context.Background(), "你好", "qwen3-tts-instruct-flash:Ethan", "", 1.2)
	if err != nil || len(audio) != 112 || chars != 15 || calls != 2 {
		t.Fatalf("audio=%d usage=%d calls=%d err=%v", len(audio), chars, calls, err)
	}
}
func TestDubbingTTSRejectsEmptyAndUntrustedMedia(t *testing.T) {
	for _, address := range []string{"", "https://example.com/audio.wav", "https://dashscope-result-bj.oss-cn-beijing.aliyuncs.com/audio.wav"} {
		d := NewDashScopeClient(config.Config{QwenTTSModel: "qwen3-tts-instruct-flash", DashScopeAPIKey: "test"})
		d.httpClient.Transport = dubbingTransport(func(r *http.Request) (*http.Response, error) {
			body := ""
			if r.Method == "POST" {
				b, _ := json.Marshal(map[string]any{"output": map[string]any{"audio": map[string]any{"url": address}}, "usage": map[string]any{"characters": 2}})
				body = string(b)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})
		if _, _, err := d.DubbingTextToSpeech(context.Background(), "啊", "qwen3-tts-instruct-flash:Ethan", "", 1); err == nil {
			t.Fatal("accepted invalid audio")
		}
	}
}

func TestDubbingTTSConfiguredModelAndLegacyVoice(t *testing.T) {
	for _, model := range []string{"qwen3-tts-flash", "custom-tts-snapshot"} {
		t.Run(model, func(t *testing.T) {
			d := NewDashScopeClient(config.Config{DashScopeAPIKey: "test", QwenTTSModel: model})
			d.httpClient.Transport = dubbingTransport(func(r *http.Request) (*http.Response, error) {
				body := "RIFF0000WAVE" + strings.Repeat("a", 100)
				if r.Method == "POST" {
					var payload struct {
						Model string         `json:"model"`
						Input map[string]any `json:"input"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Fatal(err)
					}
					if payload.Model != model || payload.Input["voice"] != "Ethan" {
						t.Fatalf("wrong request: %#v", payload)
					}
					if _, ok := payload.Input["instructions"]; ok {
						t.Fatal("non-Instruct model received instructions")
					}
					if _, ok := payload.Input["optimize_instructions"]; ok {
						t.Fatal("non-Instruct model received optimize_instructions")
					}
					body = `{"output":{"audio":{"url":"https://dashscope-result-bj.oss-cn-beijing.aliyuncs.com/audio.wav"}},"usage":{"characters":2}}`
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			if _, _, err := d.DubbingTextToSpeech(context.Background(), "你好", "qwen3-tts-instruct-flash:Ethan", "自然", 1.2); err != nil {
				t.Fatal(err)
			}
		})
	}
	d := NewDashScopeClient(config.Config{DashScopeAPIKey: "test"})
	if _, _, err := d.DubbingTextToSpeech(context.Background(), "你好", "qwen3-tts-instruct-flash:Ethan", "", 1); err == nil || !strings.Contains(err.Error(), "QWEN_TTS_MODEL") {
		t.Fatalf("missing config: %v", err)
	}
}
