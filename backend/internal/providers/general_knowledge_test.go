package providers

import (
	"strings"
	"testing"
)

func TestGeneralKnowledgeNarrativeScope(t *testing.T) {
	client := OpenAIClient{}
	general, _ := client.prompts("frame_narrative_understanding", map[string]any{"workflow_mode": "general"})
	other, _ := client.prompts("frame_narrative_understanding", map[string]any{})
	if !strings.Contains(general, "通用模式本地事件知识图谱") || !strings.Contains(general, "speaker_0") || !strings.Contains(general, "phase") {
		t.Fatal("missing grounded graph contract")
	}
	if strings.Contains(other, "通用模式本地事件知识图谱") {
		t.Fatal("general-only graph contract leaked")
	}
}
