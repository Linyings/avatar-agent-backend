package avatar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSpeechSpeedIsSentAndChangesAudioCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["speed"] != 1.1 {
			t.Errorf("speed = %v", body["speed"])
		}
		_, _ = w.Write([]byte(`{"taskId":"speech-speed","status":"RUNNING"}`))
	}))
	defer server.Close()
	_, err := NewRunningHub(server.URL, "test").SubmitSpeech(context.Background(), SpeechRequest{Model: "minimax/speech-2.8-hd", Speaker: "Wise_Woman", Text: "测试配音", Speed: 1.1})
	if err != nil {
		t.Fatal(err)
	}
	before := WorkflowInput{Voice: "Wise_Woman"}
	after := before
	after.VoiceSpeed = 1.1
	if AudioFingerprint(before, "测试") == AudioFingerprint(after, "测试") {
		t.Fatal("speed change reused old audio")
	}
	explicit := before
	explicit.VoiceSpeed = 1
	if AudioFingerprint(before, "测试") != AudioFingerprint(explicit, "测试") {
		t.Fatal("legacy normal speed should remain reusable")
	}
}

func TestSpeechSpeedValidation(t *testing.T) {
	for _, value := range []float64{-1, .1, 4} {
		if ValidateVoiceSpeed(value) == nil {
			t.Fatalf("accepted speed %v", value)
		}
	}
	if EffectiveVoiceSpeed(WorkflowInput{VoiceSpeed: 1.1, SegmentVoiceSpeeds: map[string]float64{"p2": 1.05}}, "p2") != 1.05 {
		t.Fatal("segment override lost")
	}
}
