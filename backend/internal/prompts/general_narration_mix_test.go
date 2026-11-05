package prompts

import (
	"strings"
	"testing"
)

func TestGeneralNarrationMixPrompt(t *testing.T) {
	for _, source := range []string{"video-events-v1", ""} {
		for _, ratio := range []float64{0, .5, 1} {
			for _, purpose := range []string{"script", "chapters", "retrieve", "match", "original-layout"} {
				_, prompt, ok := generalNarrationPrompt(map[string]any{"sourcePolicy": source, "purpose": purpose, "editingPolicy": "mixed-original-v1", "narrationMixPolicy": "duration-ratio-v1", "narrationRatio": ratio})
				if !ok || !strings.Contains(prompt, "FINAL PLAYBACK DURATION") || !strings.Contains(prompt, "Do NOT narrate, quote, paraphrase or spoil") || !strings.Contains(prompt, "At 0") || !strings.Contains(prompt, "At 1") {
					t.Fatalf("missing mix contract: source=%s purpose=%s ratio=%v", source, purpose, ratio)
				}
				if source == "video-events-v1" && (strings.Contains(prompt, "NARRATION ONLY") || strings.Contains(prompt, "There are NO original markers")) {
					t.Fatal("obsolete video-only prohibition overrides user ratio")
				}
			}
		}
	}
}
