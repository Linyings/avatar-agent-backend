package avatar

import (
	"strings"
	"testing"
)

// The presenter is used as shot, never keyed: no green screen may be requested.
// Project direction normally also asks for animation, layering and final
// rendering; if that reaches the video model it draws the effects into the take.
func TestNativeSpeechPromptKeepsPresenterTakeFreeOfEffects(t *testing.T) {
	direction := "沿用人物参考图，由 AI 生成口播视频；制作所选风格的动效特效，按内容将动效与口播人物分层融合，逐帧渲染最终成片。保留人物身份与视频原声，避免动效遮挡人物面部和字幕。保持自然微笑。"
	prompt := NativeSpeechPrompt("这是要朗读的台词。", direction, false, true)

	for _, banned := range []string{"绿幕", "绿色背景", "#00B140", "抠像"} {
		if strings.Contains(prompt, banned) {
			t.Fatalf("presenter take must not request a green screen (%q): %s", banned, prompt)
		}
	}
	for _, banned := range []string{"分层融合", "逐帧渲染", "制作所选风格的动效特效", "避免动效遮挡人物面部和字幕"} {
		if strings.Contains(prompt, banned) {
			t.Fatalf("post-production note %q leaked into the take prompt: %s", banned, prompt)
		}
	}
	if !strings.Contains(prompt, "保持自然微笑") {
		t.Fatalf("performance note was dropped: %s", prompt)
	}
	if !strings.Contains(prompt, "这是要朗读的台词。") {
		t.Fatal("narration text missing from prompt")
	}
}

// Delivery must be brisk and the mouth restrained. "嘴部清楚" made the model
// over-articulate; "正常语速" let it drag the line out.
func TestNativeSpeechPromptAsksForBriskRestrainedDelivery(t *testing.T) {
	prompt := NativeSpeechPrompt("台词。", "", false, true)
	for _, want := range []string{"1.5 倍", "开合幅度小", "喉部", "不夸张"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
	for _, banned := range []string{"嘴部清楚", "正常语速"} {
		if strings.Contains(prompt, banned) {
			t.Fatalf("prompt still contains %q: %s", banned, prompt)
		}
	}
}

// Without a presenter the model paints its own backdrop, so the original
// direction is passed through untouched.
func TestNativeSpeechPromptKeepsDirectionWithoutPresenter(t *testing.T) {
	prompt := NativeSpeechPrompt("台词。", "暖色演播室，加入动效", false, false)
	if !strings.Contains(prompt, "暖色演播室，加入动效") {
		t.Fatalf("direction must pass through when no presenter is used: %s", prompt)
	}
}
