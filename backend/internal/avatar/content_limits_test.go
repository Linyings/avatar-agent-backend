package avatar

import (
	"context"
	"strings"
	"testing"
)

func TestContentBudgetsRespectStepAndServerLimits(t *testing.T) {
	ctx := context.Background()
	t.Setenv("AVATAR_SHOT_MAX_TOKENS", "7000")
	t.Setenv("AVATAR_MOTION_MAX_TOKENS", "12000")
	t.Setenv("AVATAR_MODEL_MAX_OUTPUT_TOKENS", "8000")
	if ContentMaxTokens(WithShotPlanning(ctx)) != 7000 || ContentMaxTokens(WithMotionCode(ctx)) != 8000 || ContentMaxTokens(ctx) != 6000 {
		t.Fatal("output budgets ignored")
	}
	t.Setenv("AVATAR_CONTENT_MAX_INPUT_BYTES", "8192")
	for _, c := range []context.Context{ctx, WithMotionCode(ctx), WithShotPlanning(ctx)} {
		if CheckContentInput(c, "prompt", strings.Repeat("字", 3000)) == nil {
			t.Fatal("byte preflight ignored")
		}
		if err := CheckContentInput(c, "prompt", "small"); err != nil {
			t.Fatal(err)
		}
	}
}
