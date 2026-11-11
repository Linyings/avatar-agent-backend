package server

import (
	"strings"
	"testing"
)

// Scene code must aim for a striking explainer, not stacked text boxes. The
// prompt carries concrete, sandbox-safe techniques plus a hero action per scene.
func TestMotionSceneCodePromptAsksForRichVisuals(t *testing.T) {
	for _, want := range []string{"主视觉动作", "前景", "背景", "createLinearGradient", "shadowBlur", "镜头", "数字滚动", "不能只是文字框"} {
		if !strings.Contains(motionSceneCodePrompt, want) {
			t.Fatalf("scene code prompt missing %q", want)
		}
	}
	// Richness must never loosen the text and sandbox rules.
	for _, keep := range []string{"禁止DOM", "随机数", "最多100次迭代", "文字只能使用scene.title"} {
		if !strings.Contains(motionSceneCodePrompt, keep) {
			t.Fatalf("scene code prompt lost rule %q", keep)
		}
	}
}
