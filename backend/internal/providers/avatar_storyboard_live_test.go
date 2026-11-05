package providers

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/config"
)

// Explicit opt-in: this makes one real model request and may incur provider fees.
// Credentials are loaded on the server and never included in test diagnostics.
func TestLiveAvatarStoryboard(t *testing.T) {
	if os.Getenv("AVATAR_LIVE_LLM") != "1" {
		t.Skip("set AVATAR_LIVE_LLM=1 on the configured server to call the real model")
	}
	cfg := config.Load()
	if cfg.OpenAIFlashAPIKey == "" || cfg.OpenAIFlashModel == "" || cfg.OpenAIFlashAPIBaseURL == "" {
		t.Fatal("Flash LLM configuration missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sentences := []string{"你好，欢迎来到数字人口播体验。", "只需一张照片和一段文字，就能让创意变成生动的视频。", "接下来，一起看看不同镜头下的自然表达。"}
	shots, usage, err := NewOpenAIClient(cfg, nil).AvatarStoryboard(ctx, sentences)
	if err != nil {
		t.Fatal(err)
	}
	if err := avatar.ValidateStoryboard(shots, sentences); err != nil {
		t.Fatal(err)
	}
	t.Logf("model=%s validated_shots=%d usage=%+v", cfg.OpenAIFlashModel, len(shots), usage)
}
