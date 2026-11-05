package prompts

import (
	"strings"
	"testing"
)

func TestGeneralContextReviewContract(t *testing.T) {
	_, prompt, ok := generalNarrationPrompt(map[string]any{"purpose": "match-review", "contract": "general-narration-v3", "knowledgePolicy": "evidence-rag-v1", "picturePolicy": "reference-events-v1", "reviewEvidencePolicy": "facts-and-picture-context-v2"})
	if !ok || !strings.Contains(prompt, "factEvidenceIds") || !strings.Contains(prompt, "Never relabel an unsupported visible action as context") || strings.Contains(prompt, "An ID outside that interval makes the claim unsupported.") {
		t.Fatal("inconsistent fact/picture contract")
	}
}
