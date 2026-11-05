package config

import "testing"

func TestAvatarBaselineAuthorizationLoadsFromServerEnvironment(t *testing.T) {
	t.Setenv("AVATAR_MVP_BASELINE_APPROVED", "false")
	t.Setenv("AVATAR_MVP_BASELINE_REQUEST_ID", "bac6746a-234f-48cf-ae50-b367c0dbca4a")
	got := Load().Avatar
	if got.BaselineApproved || got.BaselineRequestID != "bac6746a-234f-48cf-ae50-b367c0dbca4a" {
		t.Fatal("one-request baseline authorization must not approve the baseline globally")
	}
}

func TestAvatarVideoProviderDoesNotReuseRunningHubKeyForOtherVendors(t *testing.T) {
	t.Setenv("VIDEO_PROVIDER", "minimax")
	t.Setenv("VIDEO_API_KEY", "")
	t.Setenv("VIDEO_MODEL", "")
	t.Setenv("VIDEO_API_BASE_URL", "")
	t.Setenv("MINIMAX_API_KEY", "")
	t.Setenv("RUNNINGHUB_API_KEY", "rh-secret")
	got := Load().Avatar
	if got.Provider != "minimax" || got.Model != "MiniMax-H3" || got.BaseURL != "https://api.minimax.cn" || got.APIKey != "" {
		t.Fatal("MiniMax must use its own defaults and credentials")
	}
	t.Setenv("VIDEO_API_KEY", "mini-secret")
	t.Setenv("VIDEO_MODEL", "MiniMax-H3-Lite")
	got = Load().Avatar
	if got.APIKey != "mini-secret" || got.Model != "MiniMax-H3-Lite" {
		t.Fatal("generic video overrides not applied")
	}
}

func TestRunningHubLLMUsesSecondBaseURLAndSharedKey(t *testing.T) {
	t.Setenv("VIDEO_PROVIDER", "runninghub")
	t.Setenv("VIDEO_API_KEY", "shared-key")
	t.Setenv("VIDEO_API_BASE_URL", "https://www.runninghub.cn")
	t.Setenv("RUNNINGHUB_LLM_BASE_URL", "https://llm.runninghub.cn/v1")
	t.Setenv("RUNNINGHUB_LLM_MODEL", "google/gemini-3.1-flash-lite-preview")
	t.Setenv("OPENAI_FLASH_API_KEY", "")
	t.Setenv("OPENAI_FLASH_API_BASE_URL", "https://api.openai.com/v1")
	t.Setenv("OPENAI_FLASH_MODEL", "gpt-4.1-mini")

	cfg := Load()
	if cfg.Avatar.BaseURL != "https://www.runninghub.cn" || cfg.Avatar.APIKey != "shared-key" {
		t.Fatalf("video config = %#v", cfg.Avatar)
	}
	if cfg.OpenAIFlashAPIKey != "shared-key" || cfg.OpenAIFlashAPIBaseURL != "https://llm.runninghub.cn/v1" || cfg.OpenAIFlashModel != "google/gemini-3.1-flash-lite-preview" {
		t.Fatalf("llm config key=%q url=%q model=%q", cfg.OpenAIFlashAPIKey, cfg.OpenAIFlashAPIBaseURL, cfg.OpenAIFlashModel)
	}
}
