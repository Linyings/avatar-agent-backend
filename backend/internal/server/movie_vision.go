package server

import (
	"github.com/gin-gonic/gin"
	"net/http"
)

// movieVisionCapability tells the desktop client whether 画面分析 is available and
// which input it takes: "image" (a contact sheet) or "video" (a short clip).
func (a *App) movieVisionCapability(c *gin.Context) {
	input := "image"
	if a.cfg.MovieVisionVideoInput() {
		input = "video"
	}
	c.JSON(http.StatusOK, gin.H{"supports_vision": a.cfg.MovieVisionConfigured() || a.cfg.OpenAIProSupportsVision,
		"available": a.openai.ConfiguredFor("movie_vision", nil), "input": input, "model": a.movieVisionModelName()})
}

// The client needs the selected model name for actionable diagnostics, never credentials or endpoint configuration.
func (a *App) movieVisionModelName() string {
	if a.cfg.MovieVisionConfigured() {
		return a.cfg.MovieVisionModel
	}
	return a.cfg.OpenAIProModel
}
