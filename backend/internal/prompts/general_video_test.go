package prompts

import (
	"strings"
	"testing"
)

func TestGeneralVideoPoliciesReplaceSubtitleAndOriginalRules(t *testing.T) {
	for _, purpose := range []string{"script", "chapters", "retrieve", "match", "match-review", "editorial-review"} {
		system, prompt, ok := generalNarrationPrompt(map[string]any{"purpose": purpose, "sourcePolicy": "video-events-v1", "editingPolicy": "mixed-original-v1", "splitPolicy": "contextual-beats-v1", "layoutPolicy": "release-original-edges-v1"})
		if !ok || !strings.Contains(prompt, "VIDEO EVENT EVIDENCE") || strings.Contains(system, "There is no visual analysis") || strings.Contains(prompt, "RAW subtitle evidence before TTS") {
			t.Fatalf("invalid %s policy", purpose)
		}
		if purpose == "retrieve" && !strings.Contains(prompt, "VIDEO EVENT IDS") {
			t.Fatal("legacy evidence IDs not redefined")
		}
	}
	_, old, _ := generalNarrationPrompt(map[string]any{"purpose": "script"})
	if strings.Contains(old, "VIDEO EVENT EVIDENCE") {
		t.Fatal("changed legacy workflow")
	}
}
