package avatar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSpeechModelCatalogCoversRunningHubPresetVoices(t *testing.T) {
	want := map[string]int{
		"minimax/speech-2.8-hd": 17, "minimax/speech-2.8-turbo": 17, "minimax/speech-2.6-hd": 17,
		"minimax/speech-2.6-turbo": 17, "minimax/speech-02-hd": 17, "minimax/speech-02-turbo": 17,
		"doubao-seed-tts-2.0": 35, "alibaba/qwen3-tts-flash": 48, "alibaba/qwen3-tts-instruct-flash": 24,
		"kling-lip-sync/tts": 35,
	}
	models := SpeechModels()
	if len(models) != len(want) {
		t.Fatalf("model count = %d, want %d", len(models), len(want))
	}
	for _, m := range models {
		if want[m.ID] != len(m.Voices) || m.Name == "" {
			t.Fatalf("%s: %d voices, want %d", m.ID, len(m.Voices), want[m.ID])
		}
		seen := map[string]bool{}
		defaultListed := false
		for _, v := range m.Voices {
			if v.ID == "" || v.Name == "" || v.Name == v.ID || seen[v.ID] {
				t.Fatalf("%s: invalid or duplicate voice %#v", m.ID, v)
			}
			seen[v.ID] = true
			defaultListed = defaultListed || v.ID == m.DefaultVoice
		}
		if !defaultListed {
			t.Fatalf("%s default voice %s is not selectable", m.ID, m.DefaultVoice)
		}
		if found, ok := FindSpeechModel(m.ID); !ok || found.ID != m.ID {
			t.Fatalf("%s is not resolvable", m.ID)
		}
	}
	if m, _ := FindSpeechModel("alibaba/qwen3-tts-flash"); m.SupportsSpeed {
		t.Fatal("Qwen preset TTS has no speed parameter")
	}
	if _, ok := FindSpeechModel("fish-audio/s2"); ok {
		t.Fatal("unregistered models must not resolve")
	}
}

func TestSpeechSubmissionUsesEachProviderSchema(t *testing.T) {
	cases := []struct {
		model, voice, path string
		speed              float64
		check              func(map[string]any) bool
	}{
		{"alibaba/qwen3-tts-flash", "Cherry", "/openapi/v2/alibaba/qwen3-tts-flash", 0, func(b map[string]any) bool {
			return b["voice"] == "Cherry" && b["languageType"] == "Auto" && b["speaker"] == nil && b["voice_id"] == nil
		}},
		{"alibaba/qwen3-tts-instruct-flash", "Neil", "/openapi/v2/alibaba/qwen3-tts-instruct-flash", 0, func(b map[string]any) bool {
			return b["voice"] == "Neil" && b["languageType"] == "Auto"
		}},
		{"kling-lip-sync/tts", "genshin_vindi2", "/openapi/v2/kling-lip-sync/tts", 1.15, func(b map[string]any) bool {
			return b["voiceId"] == "genshin_vindi2" && b["voiceLanguage"] == "zh" && b["voiceSpeed"] == 1.2
		}},
		{"doubao-seed-tts-2.0", "zh_female_vv_uranus_bigtts", "/openapi/v2/bytedance/doubao-seed-tts-2.0", 1.1, func(b map[string]any) bool {
			return b["speaker"] == "zh_female_vv_uranus_bigtts" && b["speech_rate"] == float64(10)
		}},
		{"minimax/speech-02-turbo", "Calm_Woman", "/openapi/v2/rhart-audio/text-to-audio/speech-02-turbo", 0, func(b map[string]any) bool {
			return b["voice_id"] == "Calm_Woman" && b["speed"] == float64(1)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if r.URL.Path != tc.path || body["text"] != "测试配音。" || !tc.check(body) {
					t.Errorf("wrong request %s %#v", r.URL.Path, body)
				}
				_, _ = w.Write([]byte(`{"taskId":"order-1","status":"RUNNING"}`))
			}))
			defer server.Close()
			_, err := NewRunningHub(server.URL, "test").SubmitSpeech(context.Background(), SpeechRequest{Model: tc.model, Speaker: tc.voice, Text: "测试配音。", Format: "wav", Speed: tc.speed})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSpeechSubmissionRejectsUnlistedVoiceAndUnsupportedSpeedBeforeHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	client := NewRunningHub(server.URL, "test")
	for _, in := range []SpeechRequest{
		{Model: "alibaba/qwen3-tts-flash", Speaker: "Wise_Woman", Text: "错配音色。"},
		{Model: "kling-lip-sync/tts", Speaker: "Cherry", Text: "错配音色。"},
		{Model: "alibaba/qwen3-tts-flash", Speaker: "Cherry", Text: "千问不支持语速。", Speed: 1.1},
	} {
		if _, err := client.SubmitSpeech(context.Background(), in); err == nil || SubmissionUncertain(err) {
			t.Fatalf("%s/%s must fail locally: %v", in.Model, in.Speaker, err)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid requests created %d paid orders", calls)
	}
}
