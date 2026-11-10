package avatar

import "strings"

// SpeechVoice is one provider system voice exposed to users.
type SpeechVoice struct {
	ID   string
	Name string
}

// RunningHub minimax/speech-2.8-hd voice_id schema (api-448184545), checked 2026-11-09.
// The order follows the provider schema; Wise_Woman is the provider default.
var miniMaxSystemVoices = []SpeechVoice{
	{"Wise_Woman", "睿智女声 · Wise Woman"},
	{"Friendly_Person", "亲和友善 · Friendly Person"},
	{"Inspirational_girl", "励志少女 · Inspirational Girl"},
	{"Deep_Voice_Man", "低沉男声 · Deep Voice Man"},
	{"Calm_Woman", "沉稳女声 · Calm Woman"},
	{"Casual_Guy", "随性男生 · Casual Guy"},
	{"Lively_Girl", "活泼少女 · Lively Girl"},
	{"Patient_Man", "耐心男声 · Patient Man"},
	{"Young_Knight", "少年骑士 · Young Knight"},
	{"Determined_Man", "坚定男声 · Determined Man"},
	{"Lovely_Girl", "可爱少女 · Lovely Girl"},
	{"Decent_Boy", "端正少年 · Decent Boy"},
	{"Imposing_Manner", "威严气场 · Imposing Manner"},
	{"Elegant_Man", "优雅男声 · Elegant Man"},
	{"Abbess", "庄重长者 · Abbess"},
	{"Sweet_Girl_2", "甜美少女 · Sweet Girl"},
	{"Exuberant_Girl", "元气少女 · Exuberant Girl"},
}

// SystemSpeechVoices returns the provider's built-in voices for a registered model.
func SystemSpeechVoices(model string) []SpeechVoice {
	if m, ok := FindSpeechModel(model); ok {
		return m.Voices
	}
	return nil
}

// DefaultSpeechVoice keeps the default paired with the selected model.
// RunningHub's MiniMax speech-2.8-hd schema defaults voice_id to Wise_Woman.
func DefaultSpeechVoice(model string) string {
	if m, ok := FindSpeechModel(model); ok {
		return m.DefaultVoice
	}
	if strings.HasPrefix(model, "minimax/") {
		return "Wise_Woman"
	}
	return "Cherry"
}

// SpeechVoiceKnown reports whether any registered model offers the voice, or it
// is a legacy Doubao ID; only such voices may be migrated to another model.
func SpeechVoiceKnown(voice string) bool {
	voice = strings.TrimSpace(voice)
	if strings.HasSuffix(strings.ToLower(voice), "_bigtts") {
		return true
	}
	for _, m := range speechModels {
		if m.HasVoice(voice) {
			return true
		}
	}
	return false
}

// SpeechVoiceIncompatible rejects a voice that the registered model does not
// offer. Unregistered models (custom gateways) keep the old Doubao-ID guard.
func SpeechVoiceIncompatible(model, voice string) bool {
	voice = strings.TrimSpace(voice)
	if m, ok := FindSpeechModel(model); ok {
		return voice != "" && !m.HasVoice(voice)
	}
	return strings.HasPrefix(model, "minimax/") && strings.HasSuffix(strings.ToLower(voice), "_bigtts")
}
