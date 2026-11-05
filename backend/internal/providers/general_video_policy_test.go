package providers

import (
	"github.com/narratoai/backend-go/internal/config"
	"strings"
	"testing"
)

func TestGeneralVideoModelIsolationAndEmptyTranscript(t *testing.T) {
	client := NewOpenAIClient(config.Config{MovieVisionAPIKey: "key", MovieVisionAPIBaseURL: "https://example.invalid/v1", MovieVisionModel: "qwen3-vl-flash"}, nil)
	if client.ModelFor("movie_vision", map[string]any{"general_video_policy": "video-events-v1"}) != "qwen3.8-omni-flash" {
		t.Fatal("general video uses wrong model")
	}
	if client.ModelFor("movie_vision", nil) != "qwen3-vl-flash" {
		t.Fatal("changed movie mode model")
	}
	system, _ := client.prompts("frame_narrative_understanding", map[string]any{"workflow_mode": "general", "visual_analyses": []any{map[string]any{"summary": "画面观察"}}})
	if !strings.Contains(system, "本次是无字幕的视频理解工作流") || strings.Contains(system, "evidence 必须是该事件时间范围内逐字引用的短字幕") {
		t.Fatal("video narrative still requires transcript")
	}
}
