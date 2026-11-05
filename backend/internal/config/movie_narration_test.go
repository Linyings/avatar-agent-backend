package config

import "testing"

func TestMovieNarrationConfigIndependent(t *testing.T) {
	t.Setenv("MOVIE_NARRATION_API_KEY", "writer-key")
	t.Setenv("MOVIE_NARRATION_API_BASE_URL", "https://writer.test/v1")
	t.Setenv("MOVIE_NARRATION_MODEL", "writer-model")
	t.Setenv("MOVIE_NARRATION_THINKING_TYPE", "enabled")
	t.Setenv("MOVIE_NARRATION_REASONING_EFFORT", "high")
	t.Setenv("OPENAI_PRO_MODEL", "pro-model")
	t.Setenv("OPENAI_FLASH_MODEL", "flash-model")
	cfg := Load()
	if !cfg.MovieNarrationConfigured() || cfg.MovieNarrationModel != "writer-model" || cfg.MovieNarrationThinkingType != "enabled" || cfg.MovieNarrationReasoningEffort != "high" {
		t.Fatal("writer config not loaded")
	}
	if cfg.OpenAIProModel != "pro-model" || cfg.OpenAIFlashModel != "flash-model" {
		t.Fatal("writer config changed other profiles")
	}
	t.Setenv("MOVIE_NARRATION_MODEL", "")
	cfg = Load()
	if cfg.MovieNarrationConfigured() || cfg.MovieNarrationModel != "" {
		t.Fatal("writer must not inherit Pro/Flash")
	}
}
