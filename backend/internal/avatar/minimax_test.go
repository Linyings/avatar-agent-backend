package avatar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMiniMaxH3UploadSubmitQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mini-key" {
			t.Error("missing auth")
		}
		switch r.URL.Path {
		case "/v1/files/upload":
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			file.Close()
			if r.FormValue("purpose") != "video_generation_input" || header.Filename != "photo.png" {
				t.Error("incorrect media upload")
			}
			w.Write([]byte(`{"file":{"file_id":1234567890123456789},"base_resp":{"status_code":0}}`))
		case "/v2/video_generation":
			var body struct {
				Model      string
				Duration   int
				Resolution string
				Content    []struct {
					Type, Role string
					ImageURL   map[string]string `json:"image_url"`
					AudioURL   map[string]string `json:"audio_url"`
				}
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Model != "MiniMax-H3" || body.Duration != 4 || body.Resolution != "768P" || len(body.Content) != 3 {
				t.Fatalf("wrong request: %+v", body)
			}
			if body.Content[1].Role != "reference_image" || body.Content[2].Role != "reference_audio" || body.Content[1].ImageURL["url"] != "mm_file://1234567890123456789" || body.Content[2].AudioURL["url"] != "mm_file://audio-1" {
				t.Error("must combine reference_image with reference_audio, never first_frame")
			}
			w.Write([]byte(`{"task_id":"mini-job"}`))
		case "/v2/query/video_generation/mini-job":
			if r.Method != "GET" {
				t.Error("query must use GET")
			}
			w.Write([]byte(`{"task":{"id":"mini-job","model":"MiniMax-H3","status":"succeeded","content":{"url":"https://cdn.example.com/video.mp4"},"usage":{"output_seconds":4}}}`))
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer server.Close()
	provider, err := NewVideoProvider(Options{Provider: "minimax", BaseURL: server.URL, APIKey: "mini-key"})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "photo")
	os.WriteFile(file, []byte("\x89PNG\r\n\x1a\n"), 0600)
	ref, err := provider.Upload(context.Background(), file)
	if err != nil || ref != "mm_file://1234567890123456789" {
		t.Fatalf("upload %s %v", ref, err)
	}
	result, err := provider.Submit(context.Background(), GenerationRequest{Prompt: "人物说话", ImageURLs: []string{ref}, AudioURLs: []string{"mm_file://audio-1"}, Duration: "3", Ratio: "9:16"})
	if err != nil || result.TaskID != "mini-job" || result.Status != "RUNNING" {
		t.Fatalf("submit %+v %v", result, err)
	}
	result, err = provider.Query(context.Background(), result.TaskID)
	if err != nil || result.Status != "SUCCESS" || result.VideoURL() == "" || len(result.Usage) == 0 {
		t.Fatalf("query %+v %v", result, err)
	}
}

func TestMiniMaxH3UnknownSubmissionAndInvalidResults(t *testing.T) {
	for _, body := range []string{`invalid`, `{}`, `{"error":{"message":"secret-key signed-url"}}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		provider, _ := NewVideoProvider(Options{Provider: "minimax", BaseURL: server.URL, APIKey: "key"})
		_, err := provider.Submit(context.Background(), GenerationRequest{Prompt: "test", ImageURLs: []string{"https://cdn.example.com/i.png"}, AudioURLs: []string{"https://cdn.example.com/a.wav"}, Duration: "5"})
		if err == nil || !SubmissionUncertain(err) || strings.Contains(err.Error(), "secret-key") {
			t.Errorf("unsafe submission %v", err)
		}
		server.Close()
	}
	for _, body := range []string{`{"task":{"id":"other","status":"running"}}`, `{"task":{"id":"job","status":"succeeded","content":{}}}`, `{"task":{"id":"job","status":"unknown"}}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		provider, _ := NewVideoProvider(Options{Provider: "minimax", BaseURL: server.URL, APIKey: "key"})
		if _, err := provider.Query(context.Background(), "job"); err == nil {
			t.Errorf("accepted invalid result %s", body)
		}
		server.Close()
	}
}

func TestVideoProviderConfiguration(t *testing.T) {
	for _, opts := range []Options{{Provider: "minimax"}, {Provider: "runninghub"}, {Provider: "another-vendor", Protocol: "http", BaseURL: "https://video.example.com", Model: "model-v1", SupportsReferenceAudio: true}} {
		opts = opts.VideoDefaults()
		opts.APIKey = "key"
		if _, err := NewVideoProvider(opts); err != nil {
			t.Errorf("provider %s: %v", opts.Provider, err)
		}
	}
	for _, opts := range []Options{{Provider: "x", Protocol: "unknown", Model: "x", BaseURL: "https://example.com"}, {Provider: "x", Protocol: "http", Model: "x", BaseURL: "https://example.com"}, {Provider: "minimax", Model: "MiniMax-H3", Resolution: "720p"}} {
		opts = opts.VideoDefaults()
		opts.APIKey = "key"
		if err := opts.ValidateVideo(); err == nil {
			t.Errorf("accepted incompatible config: %s", opts.Provider)
		}
	}
	a := Options{Provider: "minimax", APIKey: "first"}.VideoDefaults()
	b := a
	b.APIKey = "rotated"
	if a.VideoConfigID() != b.VideoConfigID() {
		t.Error("key rotation must not change task identity")
	}
	b.Model = "MiniMax-H3-Lite"
	if a.VideoConfigID() == b.VideoConfigID() {
		t.Error("model switch must change identity")
	}
}

func TestMiniMaxHTTPFailuresPreserveUncertainSubmission(t *testing.T) {
	for _, code := range []int{403, 408, 429, 500} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			w.Write([]byte(`{"error":{"message":"private-key signed-media-url"}}`))
		}))
		p, _ := NewVideoProvider(Options{Provider: "minimax", BaseURL: server.URL, APIKey: "private-key"})
		_, err := p.Submit(context.Background(), GenerationRequest{Prompt: "test", ImageURLs: []string{"https://cdn.example.com/i.png"}, AudioURLs: []string{"https://cdn.example.com/a.wav"}, Duration: "5"})
		if err == nil || SubmissionUncertain(err) != (code == 408 || code >= 500) || strings.Contains(err.Error(), "private-key") || strings.Contains(err.Error(), "signed-media-url") {
			t.Errorf("HTTP %d unsafe failure: %v", code, err)
		}
		server.Close()
	}
}

func TestMiniMaxVariantsAndTaskStatuses(t *testing.T) {
	for _, model := range []string{"MiniMax-H3", "MiniMax-H3-Highspeed", "MiniMax-H3-Lite", "MiniMax-H3-Max"} {
		opts := Options{Provider: "minimax", Model: model, APIKey: "key"}.VideoDefaults()
		if err := opts.ValidateVideo(); err != nil {
			t.Fatal(err)
		}
		duration, err := opts.GenerationDuration(3)
		minimum := 4
		if model == "MiniMax-H3-Max" {
			minimum = 5
		}
		if err != nil || duration != minimum {
			t.Errorf("%s duration=%d err=%v", model, duration, err)
		}
	}
	for remote, expected := range map[string]string{"queued": "QUEUED", "running": "RUNNING", "failed": "FAILED", "cancelled": "CANCELLED"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"task": map[string]string{"id": "job", "status": remote}})
		}))
		p, _ := NewVideoProvider(Options{Provider: "minimax", BaseURL: server.URL, APIKey: "key"})
		result, err := p.Query(context.Background(), "job")
		if err != nil || result.Status != expected {
			t.Errorf("%s: %+v %v", remote, result, err)
		}
		server.Close()
	}
}
