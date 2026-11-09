package avatar

import (
	"strings"
	"testing"
)

func TestAvatarGenerationSeparatesFootageFromComposition(t *testing.T) {
	input := Shot{Text: "画面圆窗显示的数据是110亿美元。", Angle: "正面", Framing: "上半身", Prompt: "暖色柔和光线。右下角圆形小窗口，镜头缓慢推近。保持黑色衣服。Add picture-in-picture and zoom in.背景保持简洁。"}
	prompt := GenerationPrompt(input)
	for _, forbidden := range []string{"右下角圆形小窗口", "镜头缓慢推近", "Add picture-in-picture", "zoom in"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("composition instruction reached provider: %s", forbidden)
		}
	}
	for _, required := range []string{input.Text, "暖色柔和光线", "保持黑色衣服", "固定焦距", "普通矩形全画幅", "头顶", "嘴部准确同步"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("missing source-footage contract: %s", required)
		}
	}
	if !strings.Contains(input.Prompt, "圆形小窗口") {
		t.Fatal("historical prompt was modified")
	}
}
