package avatar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSpeechQueryAcceptsAudioAndPreservesOriginalOrderUsage(t *testing.T) {
	queries := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries++
		if r.URL.Path != "/openapi/v2/query" {
			t.Fatalf("must only query original order: %s", r.URL.Path)
		}
		w.Write([]byte(`{"taskId":"tts-original","status":"SUCCESS","results":[{"url":"https://cdn.example.com/voice.wav","outputType":"wav"}],"thirdPartyConsumeMoney":"0.001"}`))
	}))
	defer server.Close()
	result, err := NewRunningHub(server.URL, "test").QuerySpeech(context.Background(), "tts-original")
	if err != nil || result.AudioURL() == "" || len(result.Usage) == 0 || queries != 1 {
		t.Fatalf("successful audio order must finish without resubmission: %#v %v queries=%d", result, err, queries)
	}
}

func TestSpeechQueryRejectsSuccessWithVideoOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"taskId":"tts-original","status":"SUCCESS","results":[{"url":"https://cdn.example.com/out.mp4","outputType":"mp4"}]}`))
	}))
	defer server.Close()
	if _, err := NewRunningHub(server.URL, "test").QuerySpeech(context.Background(), "tts-original"); err == nil {
		t.Fatal("video-only result cannot supply narration")
	}
}
