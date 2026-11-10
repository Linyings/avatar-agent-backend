package avatar

import "testing"

func TestMiniMaxSystemVoicesMatchRunningHubSchema(t *testing.T) {
	// RunningHub minimax/speech-2.8-hd voice_id schema, checked 2026-11-09.
	want := []string{
		"Wise_Woman", "Friendly_Person", "Inspirational_girl", "Deep_Voice_Man", "Calm_Woman", "Casual_Guy",
		"Lively_Girl", "Patient_Man", "Young_Knight", "Determined_Man", "Lovely_Girl", "Decent_Boy",
		"Imposing_Manner", "Elegant_Man", "Abbess", "Sweet_Girl_2", "Exuberant_Girl",
	}
	voices := SystemSpeechVoices("minimax/speech-2.8-hd")
	if len(voices) != len(want) {
		t.Fatalf("voice count = %d, want %d", len(voices), len(want))
	}
	for i, id := range want {
		if voices[i].ID != id || voices[i].Name == "" || voices[i].Name == id {
			t.Fatalf("voice %d = %#v, want %s with a Chinese display name", i, voices[i], id)
		}
	}
	if voices[0].ID != DefaultSpeechVoice("minimax/speech-2.8-hd") {
		t.Fatal("default voice must lead the system catalog")
	}
	for _, other := range []string{"doubao-seed-tts-2.0", "alibaba/qwen3-tts-flash", "kling-lip-sync/tts"} {
		if !SpeechVoiceIncompatible(other, "Wise_Woman") {
			t.Fatalf("MiniMax voices must not be offered for %s", other)
		}
	}
}
