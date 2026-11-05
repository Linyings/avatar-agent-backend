package prompts

import (
	"strings"
	"testing"
)

func TestGeneralReferenceExecutionDoesNotReintroduceCitationCapOrAudit(t *testing.T) {
	for _, source := range []string{"", "video-events-v1"} {
		for _, purpose := range []string{"script", "chapters", "retrieve", "match"} {
			_, prompt, ok := generalNarrationPrompt(map[string]any{"purpose": purpose, "sourcePolicy": source, "splitPolicy": "contextual-beats-v1", "validationPolicy": "reference-execution-v1", "picturePolicy": "reference-events-v1"})
			if !ok || !strings.Contains(prompt, "no subsequent mandatory editorial-review or match-review") {
				t.Fatalf("missing execution policy for %s/%s", source, purpose)
			}
			if purpose == "chapters" && strings.Contains(prompt, "expects evidence span <=30000ms") {
				t.Fatal("old hard cap still imposed on splitter")
			}
			if !strings.Contains(prompt, "starts must be detected cuts") || !strings.Contains(prompt, "Never invent evidence") {
				t.Fatal("lost execution and grounding constraints")
			}
		}
	}
}
