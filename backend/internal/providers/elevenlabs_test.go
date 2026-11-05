package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/config"
)

func TestElevenLabsScribeTranscribePacksWordsAndEvents(t *testing.T) {
	scribeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/speech-to-text" {
			t.Fatalf("unexpected elevenlabs request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("xi-api-key"); got != "eleven" {
			t.Fatalf("xi-api-key = %q", got)
		}
		if err := r.ParseMultipartForm(1024 * 1024); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		for key, want := range map[string]string{
			"model_id":               "scribe_v2",
			"diarize":                "true",
			"tag_audio_events":       "true",
			"timestamps_granularity": "word",
			"language_code":          "zh",
		} {
			if got := r.FormValue(key); got != want {
				t.Fatalf("%s = %q, want %q", key, got, want)
			}
		}
		if _, _, err := r.FormFile("file"); err != nil {
			t.Fatalf("missing file: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"language_code": "zh",
			"text":          "你好 (laughs) 世界",
			"words": []map[string]any{
				{"type": "word", "text": "你好", "start": 0.0, "end": 0.2, "speaker_id": "speaker_0"},
				{"type": "spacing", "text": " ", "start": 0.2, "end": 0.3},
				{"type": "audio_event", "text": "laughs", "start": 0.3, "end": 0.6, "speaker_id": "speaker_0"},
				{"type": "word", "text": "世界", "start": 1.2, "end": 1.5, "speaker_id": "speaker_1"},
			},
		})
	}))
	defer scribeServer.Close()

	client := NewElevenLabsClient(config.Config{
		ElevenLabsAPIKey:      "eleven",
		ElevenLabsBaseURL:     scribeServer.URL,
		ElevenLabsScribeModel: "scribe_v2",
	})
	result, err := client.TranscribeScribe(context.Background(), []byte("audio"), "take.wav", "zh")
	if err != nil {
		t.Fatalf("TranscribeScribe: %v", err)
	}
	if len(result.Phrases) != 2 {
		t.Fatalf("phrases = %#v, want 2 phrases", result.Phrases)
	}
	if result.Phrases[0].Text != "你好 (laughs)" || result.Phrases[0].SpeakerID != "speaker_0" {
		t.Fatalf("first phrase = %#v", result.Phrases[0])
	}
	if result.Phrases[1].Text != "世界" || result.Phrases[1].SpeakerID != "speaker_1" {
		t.Fatalf("second phrase = %#v", result.Phrases[1])
	}
	if len(result.AudioEvents) != 1 {
		t.Fatalf("audio events = %#v, want 1", result.AudioEvents)
	}
	if result.Tokens != 22 {
		t.Fatalf("tokens = %d, want 22", result.Tokens)
	}
	if result.PackedTranscript == "" || result.RawResult["text"] == nil {
		t.Fatalf("missing packed or raw result: %#v", result)
	}
	if !strings.Contains(result.PackedTranscript, "## take") {
		t.Fatalf("packed transcript should use uploaded filename: %s", result.PackedTranscript)
	}
}

func TestElevenLabsScribeAsyncSubmitAndGetTranscript(t *testing.T) {
	var sawWebhook bool
	scribeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/speech-to-text":
			if err := r.ParseMultipartForm(1024 * 1024); err != nil {
				t.Fatalf("parse multipart: %v", err)
			}
			if got := r.FormValue("webhook"); got != "true" {
				t.Fatalf("webhook = %q, want true", got)
			}
			if got := r.FormValue("webhook_metadata"); got != `{"project_id":"project-1"}` {
				t.Fatalf("webhook_metadata = %q", got)
			}
			sawWebhook = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"transcription_id": "transcript-1",
				"request_id":       "request-1",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/speech-to-text/transcripts/transcript-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"language_code": "en",
				"text":          "Hello world",
				"words": []map[string]any{
					{"type": "word", "text": "Hello", "start": 0.0, "end": 0.5, "speaker_id": "speaker_0"},
					{"type": "word", "text": "world", "start": 0.6, "end": 1.0, "speaker_id": "speaker_0"},
				},
			})
		default:
			t.Fatalf("unexpected elevenlabs request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer scribeServer.Close()

	client := NewElevenLabsClient(config.Config{
		ElevenLabsAPIKey:      "eleven",
		ElevenLabsBaseURL:     scribeServer.URL,
		ElevenLabsScribeModel: "scribe_v2",
	})
	submission, err := client.SubmitScribeTranscription(context.Background(), []byte("audio"), "take.wav", "en", `{"project_id":"project-1"}`)
	if err != nil {
		t.Fatalf("SubmitScribeTranscription: %v", err)
	}
	if !sawWebhook || submission.TranscriptionID != "transcript-1" || submission.RequestID != "request-1" {
		t.Fatalf("submission = %#v, sawWebhook = %v", submission, sawWebhook)
	}

	result, err := client.GetScribeTranscript(context.Background(), submission.TranscriptionID, "take.wav")
	if err != nil {
		t.Fatalf("GetScribeTranscript: %v", err)
	}
	if result.Text != "Hello world" || len(result.Words) != 2 || result.PackedTranscript == "" {
		t.Fatalf("result = %#v", result)
	}
}
