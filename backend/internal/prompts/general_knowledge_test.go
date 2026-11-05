package prompts

import (
	"strings"
	"testing"
)

func TestGeneralKnowledgeRetrievalContract(t *testing.T) {
	for _, purpose := range []string{"chapters", "retrieve", "match", "match-review"} {
		_, user, ok := generalNarrationPrompt(map[string]any{"purpose": purpose, "picturePolicy": "reference-events-v1", "knowledgePolicy": "evidence-rag-v1"})
		if !ok || !strings.Contains(user, "ORIGINAL narration") || !strings.Contains(user, "not causation") || !strings.Contains(user, "actual selected source interval") {
			t.Fatalf("missing grounded retrieval contract for %s", purpose)
		}
	}
	_, old, _ := generalNarrationPrompt(map[string]any{"purpose": "retrieve", "picturePolicy": "reference-events-v1"})
	if strings.Contains(old, "LOCAL RAG") {
		t.Fatal("changed legacy matching contract")
	}
}
