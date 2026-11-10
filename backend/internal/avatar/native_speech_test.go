package avatar

import (
	"strings"
	"testing"
)

func TestNativeSpeechPiecesPreserveAllTextAndStayWithinBudget(t *testing.T) {
	text := strings.Repeat("这是一段需要完整朗读的中文口播，不能省略内容。", 15)
	pieces := NativeSpeechPieces(text)
	var joined strings.Builder
	for _, p := range pieces {
		joined.WriteString(p.Text)
		if p.Duration < 4 || p.Duration > 30 || len([]rune(p.Text)) > 80 {
			t.Fatalf("invalid piece: %#v", p)
		}
	}
	if len(pieces) < 2 || joined.String() != text {
		t.Fatal("speech was lost or duplicated")
	}
}

func TestNativeSpeechPromptUsesScriptAndOptionalVoiceIdentity(t *testing.T) {
	for _, reference := range []bool{false, true} {
		prompt := NativeSpeechPrompt("请完整朗读这句话。", "暖色演播室", reference, true)
		if !strings.Contains(prompt, "请完整朗读这句话。") || !strings.Contains(prompt, "暖色演播室") || strings.Contains(prompt, "驱动音频") {
			t.Fatal(prompt)
		}
		if reference && !strings.Contains(prompt, "只参考音色") {
			t.Fatal("reference audio must not replace the new script")
		}
	}
}

func TestNativeWorkflowStagesDoNotAdvertiseTTS(t *testing.T) {
	plan := PlanWorkflow(WorkflowInput{SpeechMode: NativeSpeechMode, UseAvatar: true}, nil)
	for _, s := range plan.Stages {
		if s.Node == "F04" && strings.Contains(s.Title, "TTS") {
			t.Fatal(s.Title)
		}
	}
}
