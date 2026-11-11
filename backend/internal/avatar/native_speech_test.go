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
		if p.Duration < 4 || p.Duration > 30 || len([]rune(p.Text)) > 92 {
			t.Fatalf("invalid piece: %#v", p)
		}
	}
	if len(pieces) < 2 || joined.String() != text {
		t.Fatal("speech was lost or duplicated")
	}
}

// Seedance fills whatever duration it is given, so the request budget sets the
// speaking pace. Count spoken syllables (an English word is not one unit per
// letter) and budget for a brisk ~1.5x pace instead of the old slow fill.
func TestNativeSpeechPiecesBudgetForBriskPace(t *testing.T) {
	cases := map[string]struct{ old, max int }{
		// Observed: requested 14s/8s/8s/17s and the clips filled them exactly.
		"Anthropic 发布 Claude Fable 5.2 和 Claude Mythos 5.2，":                         {14, 7},
		"两者基于同一底层模型，主打编码、推理和视觉能力。":                                          {8, 5},
		"Fable 5.2 当日起向所有付费计划开放，":                                              {8, 5},
		"并上线 Claude Platform 及 AWS、Google Cloud、Microsoft Azure 等主要云平台；": {17, 9},
	}
	for text, c := range cases {
		pieces := NativeSpeechPieces(text)
		if len(pieces) != 1 {
			t.Fatalf("unexpected split for %q", text)
		}
		got := pieces[0].Duration
		if got > c.max || float64(got) > float64(c.old)/1.5+0.5 {
			t.Fatalf("%q budget %ds is not ~1.5x faster than %ds", text, got, c.old)
		}
		if got < 4 {
			t.Fatalf("%q budget %ds under the Seedance minimum", text, got)
		}
	}
}

// "…上下文 1M。" once split into an 80-char take plus a 4-second take for "1M。".
func TestNativeSpeechPiecesKeepShortTailWithPreviousPiece(t *testing.T) {
	text := "Fable 5.2 定价为每百万输入 token 8 美元、输出 token 40 美元，较 Fable 5.1 的 10/50 美元下降 20%，上下文 1M。"
	pieces := NativeSpeechPieces(text)
	if len(pieces) != 1 || pieces[0].Text != text {
		t.Fatalf("short tail split off: %#v", pieces)
	}
}

func TestSpokenUnitsCountsWordsNotLetters(t *testing.T) {
	if got := spokenUnits("Claude"); got > 3 {
		t.Fatalf("Claude counted as %d units", got)
	}
	if got := spokenUnits("你好"); got != 2 {
		t.Fatalf("CJK chars must count one each, got %d", got)
	}
	if got := spokenUnits("AWS"); got != 3 {
		t.Fatalf("acronyms are spelled out letter by letter, got %d", got)
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
