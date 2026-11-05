package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/narratoai/backend-go/internal/config"
)

func TestSiliconFlowTextToSpeechRejectsTruncatedAudio(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("partial"))
	}))
	defer server.Close()
	client := NewSiliconFlowClient(config.Config{SiliconFlowAPIKey: "test-key", SiliconFlowBaseURL: server.URL})
	audio, _, _, err := client.TextToSpeech(context.Background(), "test", "", 1, 1, "basic")
	if !errors.Is(err, io.ErrUnexpectedEOF) || len(audio) != 0 {
		t.Fatalf("truncated audio must fail without returning playable data: size=%d err=%v", len(audio), err)
	}
}

func TestSiliconFlowAnalyzeImages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sf-key" {
			t.Fatalf("missing auth header")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "req-1",
			"model": "Qwen/Qwen3-VL-32B-Instruct",
			"choices": []map[string]any{{
				"message": map[string]any{"content": `{"summary":"画面里有人正在奔跑"}`},
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))
	defer server.Close()

	client := NewSiliconFlowClient(config.Config{SiliconFlowAPIKey: "sf-key", SiliconFlowBaseURL: server.URL})
	result, err := client.AnalyzeImages(context.Background(), map[string]any{
		"images": []any{map[string]any{"type": "url", "data": "https://example.com/a.png"}},
	})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if result.Usage.TotalTokens != 15 {
		t.Fatalf("tokens = %d", result.Usage.TotalTokens)
	}
	if result.RequestID != "req-1" {
		t.Fatalf("request id = %s", result.RequestID)
	}
}

func TestSiliconFlowTextToSpeech(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/speech" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload["model"] != "FunAudioLLM/CosyVoice2-0.5B" {
			t.Fatalf("model = %v", payload["model"])
		}
		if payload["voice"] != "FunAudioLLM/CosyVoice2-0.5B:alex" {
			t.Fatalf("voice = %v", payload["voice"])
		}
		if _, ok := payload["response_format"]; ok {
			t.Fatalf("unexpected response_format = %v", payload["response_format"])
		}
		if _, ok := payload["stream"]; ok {
			t.Fatalf("unexpected stream = %v", payload["stream"])
		}
		gain, ok := payload["gain"].(float64)
		if !ok || math.Abs(gain+6.020599913279624) > 0.000001 {
			t.Fatalf("gain = %v", payload["gain"])
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("mp3"))
	}))
	defer server.Close()

	client := NewSiliconFlowClient(config.Config{SiliconFlowAPIKey: "sf-key", SiliconFlowBaseURL: server.URL})
	audio, _, _, err := client.TextToSpeech(context.Background(), "你好", "", 1, 0.5, "basic")
	if err != nil {
		t.Fatalf("tts: %v", err)
	}
	if string(audio) != "mp3" {
		t.Fatalf("audio = %q", string(audio))
	}
}
