package avatar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMiniMaxSpeechUsesDocumentedRouteAndVoiceParameters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openapi/v2/rhart-audio/text-to-audio/speech-2.8-hd" {
			t.Errorf("wrong path %s", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["voice_id"] != "Wise_Woman" || body["speed"] != float64(1) || body["speaker"] != nil {
			t.Errorf("wrong audio payload %#v", body)
		}
		if body["english_normalization"] != false || body["enable_base64_output"] != false {
			t.Error("required boolean parameters must be explicitly false, not omitted")
		}
		_, _ = w.Write([]byte(`{"taskId":"audio-1","status":"RUNNING"}`))
	}))
	defer server.Close()
	result, err := NewRunningHub(server.URL, "test").SubmitSpeech(context.Background(), SpeechRequest{Model: "minimax/speech-2.8-hd", Speaker: "Wise_Woman", Text: "测试配音。", Format: "wav"})
	if err != nil || result.TaskID != "audio-1" {
		t.Fatalf("%#v %v", result, err)
	}
}

func TestSpeechSubmissionClassifiesValidationRejectionWithoutLeakingDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name, body string
		uncertain  bool
	}{
		{"numeric_validation", `{"taskId":"","status":"","errorCode":1007,"errorMessage":"private-key signed-url"}`, false},
		{"string_validation", `{"taskId":"","status":"","errorCode":"1007","errorMessage":"private-key signed-url"}`, false},
		{"unrecognized_failure", `{"taskId":"","status":"","errorCode":"9999","errorMessage":"private-key signed-url"}`, true},
		{"invalid_json", `not-json private-key signed-url`, true},
		{"missing_task", `{"status":"RUNNING"}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(test.body)) }))
			defer server.Close()
			_, err := NewRunningHub(server.URL, "private-key").SubmitSpeech(context.Background(), SpeechRequest{Model: "minimax/speech-2.8-hd", Text: "测试。"})
			if err == nil || SubmissionUncertain(err) != test.uncertain || strings.Contains(err.Error(), "private-key") || strings.Contains(err.Error(), "signed-url") {
				t.Fatalf("wrong error classification: %v", err)
			}
			if !test.uncertain && SpeechFailureMessage(err) != "配音请求参数校验失败（供应商错误码 1007）" {
				t.Fatalf("missing safe diagnostic: %s", SpeechFailureMessage(err))
			}
		})
	}
}

func TestMiniMaxSpeechDefaultsEmptyVoiceAndRejectsDoubaoBeforeHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["voice_id"] != "Wise_Woman" {
			t.Errorf("default voice was not sent: %#v", body)
		}
		_, _ = w.Write([]byte(`{"taskId":"default-voice-order","status":"RUNNING"}`))
	}))
	defer server.Close()
	client := NewRunningHub(server.URL, "test")
	_, err := client.SubmitSpeech(context.Background(), SpeechRequest{Model: "minimax/speech-2.8-hd", Text: "测试默认音色。"})
	if err != nil || calls != 1 {
		t.Fatalf("default submission failed: calls=%d err=%v", calls, err)
	}
	for _, voice := range []string{"zh_male_shaonianzixin_uranus_bigtts", "zh_female_vv_uranus_bigtts"} {
		_, err = client.SubmitSpeech(context.Background(), SpeechRequest{Model: "minimax/speech-2.8-hd", Speaker: voice, Text: "不能提交混用的音色。"})
		if err == nil || SubmissionUncertain(err) || calls != 1 {
			t.Fatalf("foreign voice must fail locally without creating an order: calls=%d err=%v", calls, err)
		}
	}
}

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
