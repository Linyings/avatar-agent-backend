package prompts

import (
	"strings"
	"testing"
)

func TestContextualSplitReplacesConflictingPicturePlanning(t *testing.T) {
	ctx := map[string]any{"purpose": "chapters", "splitPolicy": "contextual-beats-v1", "editingPolicy": "mixed-original-v1", "evidencePolicy": "event-evidence-v1", "picturePolicy": "reference-events-v1", "layoutPolicy": "release-original-edges-v1", "matchingPolicy": "typed-partial-repair-v1", "knowledgePolicy": "evidence-rag-v1"}
	_, prompt, ok := generalNarrationPrompt(ctx)
	if !ok {
		t.Fatal("missing prompt")
	}
	rules := strings.Split(prompt, "\nINPUT:\n")[0]
	for _, want := range []string{"Hard stops", "Smart comma stops", "DIFFERENT characters, actions, scenes or viewpoints", "tightly connected modifiers", "FACTUAL CITATIONS", "after measured TTS", "every character", "ALL chapters", "originalClipId", "never invent", "oral-causal-v1"} {
		if !strings.Contains(strings.ToLower(rules), strings.ToLower(want)) {
			t.Errorf("missing %s", want)
		}
	}
	for _, conflict := range []string{"merge adjacent SAME-EVENT", "Windows should contain enough relevant footage", "never borrow or overlap reserved source intervals", "not broad plot chapters or mechanical punctuation", "then choose worthwhile original passages"} {
		if strings.Contains(rules, conflict) {
			t.Errorf("conflicting rule: %s", conflict)
		}
	}
	// A stale flag must not replace writing/matching rules or modify movie mode.
	ctx["purpose"] = "match"
	_, prompt, _ = generalNarrationPrompt(ctx)
	if strings.Contains(strings.Split(prompt, "\nINPUT:\n")[0], "CONTEXTUAL STORYBOARD") {
		t.Fatal("split rules leaked into matching")
	}
}
