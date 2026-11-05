package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/narratoai/backend-go/internal/config"
)

func TestFishAudioTextToSpeech(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tts" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("model") == "" {
			t.Fatalf("missing model header")
		}
		_, _ = w.Write([]byte("fish-mp3"))
	}))
	defer server.Close()

	client := NewFishAudioClient(config.Config{FishAudioAPIKey: "fish", FishAudioBaseURL: server.URL})
	audio, model, tokens, err := client.TextToSpeech(context.Background(), "你好", "voice-id", 1, 1)
	if err != nil {
		t.Fatalf("tts: %v", err)
	}
	if string(audio) != "fish-mp3" {
		t.Fatalf("audio = %q", string(audio))
	}
	if model == "" || tokens == 0 {
		t.Fatalf("model=%q tokens=%d", model, tokens)
	}
}

func TestFishAudioCreateAndDeleteModel(t *testing.T) {
	var sawCreate bool
	var sawDelete bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/model":
			sawCreate = true
			if err := r.ParseMultipartForm(1024 * 1024); err != nil {
				t.Fatalf("multipart: %v", err)
			}
			if r.FormValue("title") == "" {
				t.Fatalf("missing title")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "voice-123", "title": "Voice"})
		case r.Method == http.MethodDelete && r.URL.Path == "/model/voice-123":
			sawDelete = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewFishAudioClient(config.Config{FishAudioAPIKey: "fish", FishAudioBaseURL: server.URL})
	model, err := client.CreateModel(context.Background(), []byte("audio"), "hello", "voice.mp3", "Voice")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if model.ID != "voice-123" {
		t.Fatalf("model id = %s", model.ID)
	}
	if err := client.DeleteModel(context.Background(), model.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !sawCreate || !sawDelete {
		t.Fatalf("sawCreate=%v sawDelete=%v", sawCreate, sawDelete)
	}
}
