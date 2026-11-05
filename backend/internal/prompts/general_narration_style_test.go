package prompts

import (
	"strings"
	"testing"
)

func TestGeneralWritingStyleAcrossMaterialsAndRepair(t *testing.T) {
	for _, style := range []string{"家庭", "悬疑", "纪实"} {
		for _, purpose := range []string{"script", "chapters", "editorial-review"} {
			ctx := map[string]any{"contract": "general-narration-v3", "editingPolicy": "mixed-original-v1", "purpose": purpose, "style": style, "repairAttempt": 2, "originalClips": []any{}}
			req := map[string]any{"model_operation": "narration_outline", "video_context": ctx}
			_, prompt, ok := generalEditingOperationPrompt("script_draft", req)
			if !ok || !strings.Contains(prompt, "oral-causal-v1") {
				t.Fatalf("style absent for %s/%s (including older clients without marker)", style, purpose)
			}
			if purpose == "chapters" && !strings.Contains(prompt, "never silent chapter rewriting") {
				t.Fatal("style must not cause split text drift")
			}
			if purpose == "editorial-review" && (!strings.Contains(prompt, "originalClips is empty") || !strings.Contains(prompt, "suggestions") || !strings.Contains(prompt, "already voiced and immutable")) {
				t.Fatal("missing pure narration review, non-blocking preferences or voiced lock")
			}
			before := GeneralEditingPromptFingerprint("script_draft", req)
			ctx["writingPolicy"] = "oral-causal-v1"
			if before == GeneralEditingPromptFingerprint("script_draft", req) {
				t.Fatal("new style policy must not replay an old request")
			}
		}
	}
}

func TestGeneralWritingStyleDoesNotLeakIntoMatchingOrOtherModes(t *testing.T) {
	for _, purpose := range []string{"retrieve", "match", "match-review", "original-layout"} {
		_, prompt, ok := generalNarrationPrompt(map[string]any{"purpose": purpose})
		if !ok || strings.Contains(prompt, "oral-causal-v1") {
			t.Fatalf("matching stage should not rewrite narration: %s", purpose)
		}
	}
	for _, contract := range []string{"", "movie", "general-editing-v2"} {
		_, prompt, _ := generalEditingOperationPrompt("script_draft", map[string]any{"model_operation": "narration_outline", "video_context": map[string]any{"contract": contract, "purpose": "script"}})
		if strings.Contains(prompt, "oral-causal-v1") {
			t.Fatalf("style leaked into unrelated contract %s", contract)
		}
	}
}

func TestGeneralSingleChapterRepairScope(t *testing.T) {
	_, prompt, ok := generalNarrationPrompt(map[string]any{"purpose": "script", "repairScope": "single-chapter-v1", "targetChapterId": 9, "expectedText": "他没有离开。"})
	for _, rule := range []string{"oral-causal-v1", "Edit ONLY targetChapterId", "read-only context", "return expectedText verbatim", "No originalClips are required"} {
		if !ok || !strings.Contains(prompt, rule) {
			t.Fatalf("missing local rewrite boundary: %s", rule)
		}
	}
	if strings.Contains(prompt, "Return {\"script\":\"complete narration\"}") {
		t.Fatal("full script instruction leaked into local rewrite")
	}
}
