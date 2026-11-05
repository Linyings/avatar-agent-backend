package prompts

import (
	"strings"
	"testing"
)

func TestGeneralJointAllocationPolicy(t *testing.T) {
	for _, source := range []string{"", "video-events-v1"} {
		for _, purpose := range []string{"retrieve", "match", "original-layout"} {
			_, prompt, ok := generalNarrationPrompt(map[string]any{"purpose": purpose, "sourcePolicy": source, "allocationPolicy": "joint-candidates-v1"})
			if !ok || !strings.Contains(prompt, "JOINT") {
				t.Fatalf("missing joint allocation rules: %s %s", source, purpose)
			}
			if purpose == "retrieve" && !strings.Contains(prompt, "even when its pictures are currently reserved") {
				t.Fatal("reserved events must not be replaced with unrelated actions")
			}
			if purpose == "original-layout" && !strings.Contains(prompt, "narrationCandidatesWithoutReservations") {
				t.Fatal("repair must read actual candidate demands")
			}
		}
	}
	if generalJointAllocationRules(map[string]any{"purpose": "script"}) != "" {
		t.Fatal("legacy prompts changed")
	}
}

func TestGeneralMatchingRepairKeepsItsSchemaAndWritingStyle(t *testing.T) {
	s, err := NewService("../../prompt")
	if err != nil {
		t.Fatal(err)
	}
	b := NewBuilder(s)
	req := humanizerRequest("短剧漫剧", "original-layout")
	ctx := mapValue(req, "video_context")
	ctx["allocationPolicy"] = "joint-candidates-v1"
	ctx["allowNarrationRepair"] = true
	p, err := b.Build("script_draft", req)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"SCOPED NARRATION RECOVERY", "repairableChapterIds", "narrationRepairs", "HUMANIZER-ZH-CN SKILL", "SELECTED STYLE TEMPLATE"} {
		if !strings.Contains(p.UserPrompt, required) {
			t.Fatalf("missing %s", required)
		}
	}
	if strings.Contains(p.UserPrompt, "FULL SCRIPT CONTRACT:") {
		t.Fatal("full-script schema leaked into layout repair")
	}
	for _, purpose := range []string{"retrieve", "match", "original-layout"} {
		rules := generalNarrationMixRules(map[string]any{"purpose": purpose, "narrationMixPolicy": "duration-ratio-v1"})
		if strings.Contains(rules, "FULL SCRIPT CONTRACT:") {
			t.Fatalf("wrong schema for %s", purpose)
		}
	}
}
