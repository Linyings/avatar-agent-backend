package avatar

import (
	"strings"
	"testing"
)

// A green-screen take must stay a bare performance. Project direction normally
// also asks for animation, layering and final rendering; if that reaches the
// video model it draws the effects into the take and the footage can no longer
// be keyed or composited downstream.
func TestNativeSpeechPromptKeepsGreenScreenTakeFreeOfEffects(t *testing.T) {
	direction := "沿用人物参考图，由 AI 生成口播视频；制作所选风格的动效特效，按内容将动效与口播人物分层融合，逐帧渲染最终成片。保留人物身份与视频原声，避免动效遮挡人物面部和字幕。保持自然微笑。"
	prompt := NativeSpeechPrompt("这是要朗读的台词。", direction, false, true)

	if !strings.Contains(prompt, "纯绿色背景") {
		t.Fatalf("green-screen requirement missing: %s", prompt)
	}
	// Project-level post-production requests must not reach the video model.
	for _, banned := range []string{"分层融合", "逐帧渲染", "制作所选风格的动效特效", "避免动效遮挡人物面部和字幕"} {
		if strings.Contains(prompt, banned) {
			t.Fatalf("post-production note %q leaked into the take prompt: %s", banned, prompt)
		}
	}
	// Pure performance notes survive.
	if !strings.Contains(prompt, "保持自然微笑") {
		t.Fatalf("performance note was dropped: %s", prompt)
	}
	if !strings.Contains(prompt, "这是要朗读的台词。") {
		t.Fatal("narration text missing from prompt")
	}
}

// Without a presenter the model paints its own backdrop, so the original
// direction is passed through untouched.
func TestNativeSpeechPromptKeepsDirectionWithoutPresenter(t *testing.T) {
	prompt := NativeSpeechPrompt("台词。", "暖色演播室，加入动效", false, false)
	if !strings.Contains(prompt, "暖色演播室，加入动效") {
		t.Fatalf("direction must pass through when no presenter is keyed: %s", prompt)
	}
}
