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

func TestRunningHubUploadSubmitQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing authentication")
		}
		switch r.URL.Path {
		case "/openapi/v2/media/upload/binary":
			f, header, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
			}
			if f != nil {
				f.Close()
				if header.Filename != "photo.png" {
					t.Errorf("extensionless source did not get its real media extension: %s", header.Filename)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"download_url": "https://cdn.runninghub.cn/input/a.png", "fileName": "not-a-url"}})
		case SeedancePath:
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["audioUrls"] == nil || body["imageUrls"] == nil || body["generateAudio"] != true {
				t.Errorf("invalid body: %#v", body)
			}
			w.Write([]byte(`{"taskId":"job-1","status":"RUNNING"}`))
		case "/openapi/v2/query":
			w.Write([]byte(`{"taskId":"job-1","status":"SUCCESS","results":[{"url":"https://cdn.runninghub.cn/out.mp4","outputType":"mp4"}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	c := NewRunningHub(server.URL, "test-key")
	file := filepath.Join(t.TempDir(), "photo")
	os.WriteFile(file, []byte("\x89PNG\r\n\x1a\n"), 0600)
	uploaded, err := c.Upload(context.Background(), file)
	if err != nil || uploaded != "https://cdn.runninghub.cn/input/a.png" {
		t.Fatalf("upload %q %v", uploaded, err)
	}
	result, err := c.Submit(context.Background(), GenerationRequest{Prompt: "口播", ImageURLs: []string{uploaded}, AudioURLs: []string{"https://cdn.runninghub.cn/a.wav"}, Duration: "5", Resolution: "720p", Ratio: "9:16", GenerateAudio: true})
	if err != nil || result.TaskID != "job-1" {
		t.Fatalf("submit %#v %v", result, err)
	}
	result, err = c.Query(context.Background(), result.TaskID)
	if err != nil || result.VideoURL() == "" {
		t.Fatalf("query %#v %v", result, err)
	}
}

func TestSeedanceShortNarrationUsesAtLeastFourSeconds(t *testing.T) {
	options := Options{Provider: "runninghub"}.VideoDefaults()
	for input, want := range map[int]int{2: 4, 3: 4, 4: 4, 9: 9, 15: 15} {
		got, err := options.GenerationDuration(input)
		if err != nil || got != want {
			t.Fatalf("duration %d -> %d, want %d; err=%v", input, got, want, err)
		}
	}
	options = Options{Provider: "other", Protocol: "http"}.VideoDefaults()
	if got, err := options.GenerationDuration(3); err != nil || got != 3 {
		t.Fatal("Seedance duration policy leaked into another provider")
	}
}

func TestRunningHubUnknownSubmissionMustNotRetry(t *testing.T) {
	for _, body := range []string{`invalid`, `{"status":"RUNNING"}`} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.Write([]byte(body)) }))
		c := NewRunningHub(server.URL, "secret")
		_, err := c.Submit(context.Background(), GenerationRequest{})
		if err == nil || !SubmissionUncertain(err) || calls != 1 {
			t.Errorf("must preserve unknown submission: calls=%d err=%v", calls, err)
		}
		server.Close()
	}
}

func TestUploadRetriesDroppedConnectionWithoutTreatingItAsSubmission(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Fatal(err)
			}
			conn.Close()
			return
		}
		w.Write([]byte(`{"code":0,"data":{"download_url":"https://cdn.example.com/a.png"}}`))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "photo.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := NewRunningHub(server.URL, "secret").Upload(context.Background(), path)
	if err != nil || got != "https://cdn.example.com/a.png" || calls != 2 || SubmissionUncertain(err) {
		t.Fatalf("upload=%s calls=%d err=%v", got, calls, err)
	}
}

func TestSubmitTransportFailureStaysUncertain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
	}))
	defer server.Close()
	_, err := NewRunningHub(server.URL, "secret-key").Submit(context.Background(), GenerationRequest{})
	if err == nil || !SubmissionUncertain(err) || strings.Contains(err.Error(), "secret-key") || strings.Contains(err.Error(), "可以重试") {
		t.Fatalf("submit error = %v", err)
	}
}

func TestRunningHubErrorsDoNotExposeSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"error":"secret-key https://signed.url"}`))
	}))
	defer server.Close()
	_, err := NewRunningHub(server.URL, "secret-key").Submit(context.Background(), GenerationRequest{})
	if err == nil || strings.Contains(err.Error(), "secret-key") || SubmissionUncertain(err) {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestDoubaoSpeechUsesConfiguredModelAndReturnsAudio(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing authentication")
		}
		switch r.URL.Path {
		case "/openapi/v2/bytedance/doubao-seed-tts-2.0":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["speaker"] != "zh_male_shaonianzixin_uranus_bigtts" || body["explicit_language"] != "zh-cn" || body["format"] != "wav" || body["text"] != "你好" {
				t.Errorf("unexpected speech request: %#v", body)
			}
			w.Write([]byte(`{"taskId":"tts-1","status":"RUNNING"}`))
		case "/openapi/v2/query":
			w.Write([]byte(`{"taskId":"tts-1","status":"SUCCESS","results":[{"url":"https://cdn.runninghub.cn/voice.wav","outputType":"wav"}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := NewRunningHub(server.URL, "test-key")
	client.fetch = func(_ context.Context, raw string) ([]byte, error) {
		if raw != "https://cdn.runninghub.cn/voice.wav" {
			t.Errorf("audio url = %s", raw)
		}
		return []byte("RIFF"), nil
	}
	audio, chars, err := client.Synthesize(context.Background(), SpeechRequest{Model: doubaoTTSModel, Speaker: "zh_male_shaonianzixin_uranus_bigtts", Language: "zh-cn", Text: "你好"})
	if err != nil || string(audio) != "RIFF" || chars != 2 {
		t.Fatalf("audio=%q chars=%d err=%v", audio, chars, err)
	}
}

func TestDoubaoSpeechRetriesTransientQuery(t *testing.T) {
	queries := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi/v2/bytedance/doubao-seed-tts-2.0":
			w.Write([]byte(`{"taskId":"tts-2","status":"QUEUED"}`))
		case "/openapi/v2/query":
			queries++
			if queries == 1 {
				w.WriteHeader(500)
				return
			}
			w.Write([]byte(`{"taskId":"tts-2","status":"SUCCESS","results":[{"url":"https://cdn.runninghub.cn/voice.wav","outputType":"wav"}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := NewRunningHub(server.URL, "test-key")
	client.fetch = func(context.Context, string) ([]byte, error) { return []byte("RIFF"), nil }
	audio, _, err := client.Synthesize(context.Background(), SpeechRequest{Model: doubaoTTSModel, Speaker: "zh_male_shaonianzixin_uranus_bigtts", Text: "你好"})
	if err != nil || string(audio) != "RIFF" || queries < 2 {
		t.Fatalf("audio=%q queries=%d err=%v", audio, queries, err)
	}
}

func TestRunningHubRejectsBusinessErrorAndEmptySuccess(t *testing.T) {
	for _, body := range []string{`{"code":123,"message":"no","data":{}}`, `{"taskId":"a","status":"SUCCESS","results":[]}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		_, err := NewRunningHub(server.URL, "key").Query(context.Background(), "a")
		if err == nil {
			t.Errorf("accepted invalid result: %s", body)
		}
		server.Close()
	}
}
