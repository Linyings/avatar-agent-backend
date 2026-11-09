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

// SystemSpeechVoices returns the provider's built-in voices for a model family.
func SystemSpeechVoices(model string) []SpeechVoice {
	if strings.HasPrefix(model, "minimax/") {
		return append([]SpeechVoice(nil), miniMaxSystemVoices...)
	}
	return nil
}

// DefaultSpeechVoice keeps the default paired with the selected model family.
// RunningHub's MiniMax speech-2.8-hd schema defaults voice_id to Wise_Woman.
func DefaultSpeechVoice(model string) string {
	switch {
	case strings.HasPrefix(model, "minimax/"):
		return "Wise_Woman"
	case strings.HasPrefix(model, "doubao-") || strings.HasPrefix(model, "bytedance/doubao-"):
		return "zh_male_shaonianzixin_uranus_bigtts"
	default:
		return "Cherry"
	}
}

// SpeechVoiceIncompatible recognizes old Doubao IDs left in MiniMax configs.
// Other custom IDs are still checked by the configured catalog and provider;
// this is not a complete provider voice registry.
func SpeechVoiceIncompatible(model, voice string) bool {
	return strings.HasPrefix(model, "minimax/") && strings.HasSuffix(strings.ToLower(strings.TrimSpace(voice)), "_bigtts")
}
