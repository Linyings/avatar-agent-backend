package server

import (
	"testing"

	"github.com/narratoai/backend-go/internal/config"
)

func TestInferOpenAICompatibleProviderRecognizesMiniMax(t *testing.T) {
	app := &App{cfg: config.Config{
		OpenAIFlashModel:      "MiniMax-M3",
		OpenAIFlashAPIBaseURL: "https://api.minimaxi.com/v1",
	}}

	if got := app.inferOpenAICompatibleProvider("MiniMax-M3"); got != "minimax" {
		t.Fatalf("provider = %q, want minimax", got)
	}
}
