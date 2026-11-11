package server

import (
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
)

// Each motion scene costs one model request. The design phase distils the look
// into palette, tone and visual context, so per-scene code requests must not
// carry the full style prose again — otherwise a five-scene segment pays for
// the same reference five times.
func TestCompactCodeInputDropsLongStyleAndDirectionProse(t *testing.T) {
	longStyle := strings.Repeat("黑白炭灰编辑式排版、纸张纹理、打字标签。", 120)
	full := map[string]any{
		"style":           "explain-podcast",
		"style_reference": longStyle,
		"direction":       "沿用人物参考图，制作所选风格的动效特效并逐帧渲染成片。",
		"scene":           map[string]any{"title": "结果"},
		"code_max_bytes":  6000,
	}
	compact := compactCodeInput(full, "黑白炭灰、打字标签")

	got, _ := compact["style_reference"].(string)
	if len([]rune(got)) > codeStyleReferenceBudget+1 {
		t.Fatalf("style reference not bounded: %d runes", len([]rune(got)))
	}
	// Whole-film direction was consumed by the design phase.
	if _, exists := compact["direction"]; exists {
		t.Fatal("direction must not be resent to code scenes")
	}
	// The scene payload a code scene actually draws from is untouched.
	if compact["code_max_bytes"] != 6000 {
		t.Fatal("code budget lost")
	}
	if _, exists := compact["scene"]; !exists {
		t.Fatal("scene lost")
	}
	// The caller's map must not be mutated; it is reused across scenes.
	if full["style_reference"] != longStyle {
		t.Fatal("input map was mutated")
	}
}

func TestCompactCodeInputKeepsPaletteProfile(t *testing.T) {
	full := map[string]any{
		"style_profile":   map[string]any{"palette": []any{"#18191C", "#FAF8F2"}},
		"style_reference": strings.Repeat("x", 5000),
	}
	compact := compactCodeInput(full, "digest")
	if compact["style_profile"] == nil {
		t.Fatal("palette profile dropped; code cannot pick colours")
	}
}

func TestTrimDirectorValueKeepsOnlyDrawnFields(t *testing.T) {
	scenes := []avatar.DirectorScene{{
		ID: "d1", Purpose: "展示定价对比", Composition: "evidence",
		Motion: "价格从左滑入", AvatarShape: "circle", AvatarPosition: "bottom-right",
		Narration: strings.Repeat("很长的旁白原文", 200),
		Visual:    strings.Repeat("很长的画面描述", 200),
	}}
	trimmed, ok := trimDirectorValue(scenes, codeDirectorBudget).([]map[string]any)
	if !ok || len(trimmed) != 1 {
		t.Fatalf("unexpected shape: %#v", trimmed)
	}
	if _, exists := trimmed[0]["narration"]; exists {
		t.Fatal("narration prose resent to code scene")
	}
	if _, exists := trimmed[0]["visual"]; exists {
		t.Fatal("visual prose resent to code scene")
	}
	if trimmed[0]["avatar_position"] != "bottom-right" {
		t.Fatal("avatar placement lost; code must reserve that corner")
	}
}

// Byte slicing would split a multi-byte character and produce invalid JSON.
func TestTruncateRepairTextCutsOnRuneBoundary(t *testing.T) {
	text := strings.Repeat("中文", 4000)
	got := truncateRepairText(text, 100)
	if !strings.HasPrefix(got, "中文") {
		t.Fatalf("runes lost: %q", got[:20])
	}
	if len([]rune(got)) > 100+len("…（已截断）") {
		t.Fatalf("not bounded: %d runes", len([]rune(got)))
	}
	for _, r := range got {
		if r == 0xFFFD {
			t.Fatal("invalid rune from byte slicing")
		}
	}
}

func TestStyleDigestForCodeSpellsOutPalette(t *testing.T) {
	digest := styleDigestForCode("黑白编辑式排版", []string{"#18191C", "#FAF8F2"})
	if !strings.Contains(digest, "#18191C") {
		t.Fatalf("palette missing from digest: %s", digest)
	}
}
