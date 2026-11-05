package prompts

import (
	"strings"
	"testing"
)

func TestGeneralOriginalLayoutContract(t *testing.T) {
	_, user, ok := generalNarrationPrompt(map[string]any{"purpose": "original-layout", "editingPolicy": "mixed-original-v1", "layoutPolicy": "release-original-edges-v1"})
	if !ok || !strings.Contains(user, "Only shrink") || !strings.Contains(user, "payoff") || !strings.Contains(user, "never move") {
		t.Fatal("missing safe layout contract")
	}
}

func TestEventEvidenceMatchingContract(t *testing.T) {
	for _, purpose := range []string{"script", "chapters", "match", "match-review"} {
		ctx := map[string]any{"purpose": purpose, "editingPolicy": "mixed-original-v1", "evidencePolicy": "event-evidence-v1"}
		_, user, ok := generalNarrationPrompt(ctx)
		if !ok || !strings.Contains(user, "EVIDENCE POLICY") || !strings.Contains(user, "never invent IDs") {
			t.Fatalf("missing evidence policy: %s", purpose)
		}
		if purpose == "chapters" && !strings.Contains(user, "ONE-EVENT") {
			t.Fatal("missing decomposition")
		}
		if purpose == "match" && !strings.Contains(user, "ACTUAL selected") {
			t.Fatal("broad windows still allowed")
		}
		if purpose == "match-review" && !strings.Contains(user, "ALL concrete claims") {
			t.Fatal("missing semantic review")
		}
	}
}

func TestGeneralNarrationV3RoutesWithoutLegacyEditingRules(t *testing.T) {
	for _, purpose := range []string{"script", "chapters", "match"} {
		req := map[string]any{"model_operation": "narration_outline", "video_context": map[string]any{"contract": "general-narration-v3", "purpose": purpose}}
		system, user, ok := generalEditingOperationPrompt("script_draft", req)
		if !ok || system == "" || !strings.Contains(user, "Return") {
			t.Fatalf("missing v3 operation %s", purpose)
		}
		if strings.Contains(user, "narrationPictureMs") || strings.Contains(user, "originalPictureMs") || strings.Contains(user, "20–40") {
			t.Fatalf("legacy allocation leaked into %s", purpose)
		}
		if GeneralEditingPromptFingerprint("script_draft", req) == "" {
			t.Fatal("missing replay fingerprint")
		}
	}
}

func TestMixedOriginalNarrationPolicy(t *testing.T) {
	for _, purpose := range []string{"script", "chapters", "editorial-review", "match"} {
		req := map[string]any{"model_operation": "narration_outline", "video_context": map[string]any{"contract": "general-narration-v3", "editingPolicy": "mixed-original-v1", "purpose": purpose}}
		_, user, ok := generalEditingOperationPrompt("script_draft", req)
		if !ok || !strings.Contains(user, "SOURCE AUDIO") || !strings.Contains(user, "NOT repeat or paraphrase") || !strings.Contains(user, "Return") {
			t.Fatalf("missing mixed policy for %s", purpose)
		}
		if strings.Contains(user, "Do not arrange original sound") {
			t.Fatal("legacy original ban leaked")
		}
	}
}

func TestPictureCandidatePolicyOverridesLegacyGeometry(t *testing.T) {
	for _, purpose := range []string{"match", "match-review"} {
		ctx := map[string]any{"purpose": purpose, "editingPolicy": "mixed-original-v1", "evidencePolicy": "event-evidence-v1", "picturePolicy": "context-candidates-v1"}
		_, prompt, ok := generalNarrationPrompt(ctx)
		if !ok || !strings.Contains(prompt, "FACTS AND PICTURES ARE DIFFERENT") || !strings.Contains(prompt, "subtitleIds") {
			t.Fatal("missing candidate policy")
		}
		if strings.Contains(prompt, "covers ALL cited evidence") || strings.Contains(prompt, "at most 5 seconds") {
			t.Fatal("contradictory legacy geometry leaked")
		}
	}
}

func TestReferenceMatchingStages(t *testing.T) {
	for _, purpose := range []string{"retrieve", "match", "match-review"} {
		_, prompt, ok := generalNarrationPrompt(map[string]any{"purpose": purpose, "editingPolicy": "mixed-original-v1", "evidencePolicy": "event-evidence-v1", "picturePolicy": "reference-events-v1"})
		if !ok || !strings.Contains(prompt, "REFERENCE MATCHING") || !strings.Contains(prompt, "action STAGE") || !strings.Contains(prompt, "next-episode previews") {
			t.Fatal("missing reference contract", purpose)
		}
		if strings.Contains(prompt, "covers ALL cited evidence") || strings.Contains(prompt, "at most 5 seconds") || strings.Contains(prompt, "Select a chronological non-overlapping path") {
			t.Fatal("obsolete matching policy leaked")
		}
	}
}
