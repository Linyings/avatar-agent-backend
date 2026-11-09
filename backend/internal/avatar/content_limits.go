package avatar

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

type shotPlanningContextKey struct{}

func WithShotPlanning(ctx context.Context) context.Context {
	return context.WithValue(ctx, shotPlanningContextKey{}, true)
}

// These are application budgets, not claims about a provider's context window.
// The server cap should be set to the limit supported by the configured endpoint.
func ContentMaxTokens(ctx context.Context) int {
	n := 6000
	if MotionCodeEnabled(ctx) {
		n = contentLimitEnv("AVATAR_MOTION_MAX_TOKENS", 12000, 4096, 32768)
	}
	if enabled, _ := ctx.Value(shotPlanningContextKey{}).(bool); enabled {
		n = contentLimitEnv("AVATAR_SHOT_MAX_TOKENS", 6000, 1024, 32768)
	}
	return min(n, contentLimitEnv("AVATAR_MODEL_MAX_OUTPUT_TOKENS", 32768, 256, 32768))
}

func contentLimitEnv(key string, fallback, low, high int) int {
	n, err := strconv.Atoi(os.Getenv(key))
	if err != nil || n < low || n > high {
		return fallback
	}
	return n
}

type ContentPreflightError struct{ Message string }

func (e *ContentPreflightError) Error() string                { return e.Message }
func (e *ContentPreflightError) ProviderRequestNotSent() bool { return true }

// UTF-8 bytes bound accidental prompt growth. Image tokens and tokenization are
// provider-specific, so this check intentionally does not estimate token counts.
func CheckContentInput(ctx context.Context, prompt string, input any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return &ContentPreflightError{Message: "模型输入无法编码为 JSON，尚未提交请求"}
	}
	limit := contentLimitEnv("AVATAR_CONTENT_MAX_INPUT_BYTES", 128*1024, 4096, 1024*1024)
	if enabled, _ := ctx.Value(shotPlanningContextKey{}).(bool); enabled {
		limit = min(limit, 24000)
	}
	// Reserve room for the provider adapter's fixed system contract.
	if size := len(prompt) + len(raw) + 1024; size > limit {
		return &ContentPreflightError{Message: fmt.Sprintf("模型文本输入 %d 字节超过本步骤预算 %d 字节，请拆分当前段落或缩短制作要求；尚未提交请求（字节预算不等同于模型 token 窗口）", size, limit)}
	}
	return nil
}
