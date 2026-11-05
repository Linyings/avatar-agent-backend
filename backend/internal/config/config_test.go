package config

import (
	"testing"
	"time"
)

func TestLoadTavilySearchDefaultsAndOverrides(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "tavily-key")
	t.Setenv("TAVILY_API_BASE_URL", "https://search.example/")
	t.Setenv("TAVILY_SEARCH_DEPTH", "advanced")
	t.Setenv("TAVILY_MAX_RESULTS", "7")
	t.Setenv("TAVILY_REQUEST_TIMEOUT_SECONDS", "12")

	cfg := Load()

	if cfg.TavilyAPIKey != "tavily-key" || cfg.TavilyAPIBaseURL != "https://search.example" {
		t.Fatalf("Tavily endpoint configuration = %#v", cfg)
	}
	if cfg.TavilySearchDepth != "advanced" || cfg.TavilyMaxResults != 7 || cfg.TavilyTimeout != 12*time.Second {
		t.Fatalf("Tavily search configuration = %#v", cfg)
	}
	if !cfg.TavilyConfigured() {
		t.Fatal("Tavily should be configured when the API key is present")
	}
}

func TestLoadPaymentChannelDefaults(t *testing.T) {
	t.Setenv("SUBSCRIPTION_PAYMENT_PROVIDER", "")
	t.Setenv("CREDIT_PAYMENT_PROVIDER", "")
	t.Setenv("CREEM_CHECKOUT_ENABLED", "")

	cfg := Load()

	if cfg.SubscriptionPaymentProvider != "alipay" {
		t.Fatalf("SubscriptionPaymentProvider = %q, want alipay", cfg.SubscriptionPaymentProvider)
	}
	if cfg.CreditPaymentProvider != "wechat" {
		t.Fatalf("CreditPaymentProvider = %q, want wechat", cfg.CreditPaymentProvider)
	}
	if cfg.CreemCheckoutEnabled {
		t.Fatal("CreemCheckoutEnabled = true, want false")
	}
}

func TestAlipayConfiguredRequiresAllSigningAndCallbackFields(t *testing.T) {
	cfg := Config{
		AlipayAppID:      "app-id",
		AlipayMerchantID: "merchant-id",
		AlipayPrivateKey: "private-key",
		AlipayPublicKey:  "public-key",
		AlipayNotifyURL:  "https://api.example.com/alipay/webhook",
		AlipayReturnURL:  "https://app.example.com/subscription",
	}
	if !cfg.AlipayConfigured() {
		t.Fatal("complete Alipay configuration should be configured")
	}

	cfg.AlipayNotifyURL = ""
	if cfg.AlipayConfigured() {
		t.Fatal("configuration without notify URL should not be configured")
	}
}

func TestLoadDesktopUpdateRoot(t *testing.T) {
	t.Setenv("DESKTOP_UPDATE_ROOT", "/srv/narratoai-updates")

	cfg := Load()

	if cfg.DesktopUpdateRoot != "/srv/narratoai-updates" {
		t.Fatalf("DesktopUpdateRoot = %q", cfg.DesktopUpdateRoot)
	}
}

func TestLoadOpenAIProfilesPreferSpecificEnv(t *testing.T) {
	t.Setenv("OPENAI_PRO_API_KEY", "pro-key")
	t.Setenv("OPENAI_PRO_API_BASE_URL", "https://pro.example/v1")
	t.Setenv("OPENAI_PRO_MODEL", "pro-model")
	t.Setenv("OPENAI_PRO_THINKING_TYPE", "enabled")
	t.Setenv("OPENAI_PRO_REASONING_EFFORT", "high")
	t.Setenv("OPENAI_FLASH_API_KEY", "flash-key")
	t.Setenv("OPENAI_FLASH_API_BASE_URL", "https://flash.example/v1")
	t.Setenv("OPENAI_FLASH_MODEL", "flash-model")
	t.Setenv("OPENAI_FLASH_THINKING_TYPE", "disabled")
	t.Setenv("OPENAI_FLASH_REASONING_EFFORT", "high")

	cfg := Load()

	if cfg.OpenAIProAPIKey != "pro-key" || cfg.OpenAIProAPIBaseURL != "https://pro.example/v1" || cfg.OpenAIProModel != "pro-model" {
		t.Fatalf("pro profile = %#v", cfg)
	}
	if cfg.OpenAIProThinkingType != "enabled" || cfg.OpenAIProReasoningEffort != "high" {
		t.Fatalf("pro reasoning options = %#v", cfg)
	}
	if cfg.OpenAIFlashAPIKey != "flash-key" || cfg.OpenAIFlashAPIBaseURL != "https://flash.example/v1" || cfg.OpenAIFlashModel != "flash-model" {
		t.Fatalf("flash profile = %#v", cfg)
	}
	if cfg.OpenAIFlashThinkingType != "disabled" || cfg.OpenAIFlashReasoningEffort != "high" {
		t.Fatalf("flash reasoning options = %#v", cfg)
	}
	if !cfg.OpenAIProConfigured() || !cfg.OpenAIFlashConfigured() || !cfg.OpenAIConfigured() {
		t.Fatalf("expected both OpenAI profiles to be configured")
	}
}

func TestLoadPromptCacheConfig(t *testing.T) {
	t.Setenv("LLM_PROMPT_CACHE_MODE", "openai")
	t.Setenv("OPENAI_PROMPT_CACHE_KEY_ENABLED", "false")
	t.Setenv("OPENAI_PROMPT_CACHE_RETENTION", "24h")
	t.Setenv("DASHSCOPE_EXPLICIT_CACHE_ENABLED", "true")

	cfg := Load()

	if cfg.LLMPromptCacheMode != "openai" {
		t.Fatalf("prompt cache mode = %q", cfg.LLMPromptCacheMode)
	}
	if cfg.OpenAIPromptCacheKey {
		t.Fatalf("OpenAIPromptCacheKey = true, want false")
	}
	if cfg.OpenAIPromptCacheRetention != "24h" {
		t.Fatalf("OpenAIPromptCacheRetention = %q", cfg.OpenAIPromptCacheRetention)
	}
	if !cfg.DashScopeExplicitCache {
		t.Fatalf("DashScopeExplicitCache = false, want true")
	}
}

func TestLoadPromptCacheDefaultsFavorDashScopeExplicitCache(t *testing.T) {
	cfg := Load()

	if cfg.LLMPromptCacheMode != "auto" {
		t.Fatalf("prompt cache mode = %q, want auto", cfg.LLMPromptCacheMode)
	}
	if !cfg.OpenAIPromptCacheKey {
		t.Fatalf("OpenAIPromptCacheKey = false, want true")
	}
	if cfg.OpenAIPromptCacheRetention != "" {
		t.Fatalf("OpenAIPromptCacheRetention = %q, want empty", cfg.OpenAIPromptCacheRetention)
	}
	if !cfg.DashScopeExplicitCache {
		t.Fatalf("DashScopeExplicitCache = false, want true")
	}
}

func TestLoadOpenAIProfilesRequireExplicitProfileKeys(t *testing.T) {
	t.Setenv("OPENAI_PRO_API_KEY", "")
	t.Setenv("OPENAI_PRO_API_BASE_URL", "")
	t.Setenv("OPENAI_PRO_MODEL", "")
	t.Setenv("OPENAI_FLASH_API_KEY", "")
	t.Setenv("OPENAI_FLASH_API_BASE_URL", "")
	t.Setenv("OPENAI_FLASH_MODEL", "")

	cfg := Load()

	if cfg.OpenAIProAPIKey != "" || cfg.OpenAIFlashAPIKey != "" {
		t.Fatalf("empty profile keys should not configure OpenAI profiles: %#v", cfg)
	}
	if cfg.OpenAIProConfigured() || cfg.OpenAIFlashConfigured() || cfg.OpenAIConfigured() {
		t.Fatalf("empty profile keys should not mark OpenAI profiles configured")
	}
}

func TestLoadOpenAIProfilesIgnoreLegacyOpenAIEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "legacy-key")
	t.Setenv("OPENAI_API_BASE_URL", "https://legacy.example/v1")
	t.Setenv("OPENAI_TEXT_MODEL", "legacy-model")
	t.Setenv("OPENAI_THINKING_TYPE", "enabled")
	t.Setenv("OPENAI_REASONING_EFFORT", "high")
	t.Setenv("OPENAI_MAX_TOKENS", "4096")
	t.Setenv("OPENAI_PRO_API_KEY", "")
	t.Setenv("OPENAI_FLASH_API_KEY", "")

	cfg := Load()

	if cfg.OpenAIProAPIKey != "" || cfg.OpenAIFlashAPIKey != "" {
		t.Fatalf("legacy OpenAI env should not populate profile keys: %#v", cfg)
	}
	if cfg.OpenAIConfigured() {
		t.Fatalf("legacy OpenAI env should not configure OpenAI profiles")
	}
}

func TestLoadFrontendBaseURLSupportsAdditionalCSVValues(t *testing.T) {
	t.Setenv("FRONTEND_BASE_URL", "https://www.narratoai.cn,https://narratoai.cn")
	t.Setenv("PUBLIC_API_BASE_URL", "https://api.narratoai.cn,https://backup-api.narratoai.cn")

	cfg := Load()

	if cfg.FrontendBaseURL != "https://www.narratoai.cn" {
		t.Fatalf("FrontendBaseURL = %q, want first URL only", cfg.FrontendBaseURL)
	}
	if len(cfg.FrontendBaseURLs) != 2 || cfg.FrontendBaseURLs[0] != "https://www.narratoai.cn" || cfg.FrontendBaseURLs[1] != "https://narratoai.cn" {
		t.Fatalf("FrontendBaseURLs = %#v, want both configured frontend URLs", cfg.FrontendBaseURLs)
	}
	if cfg.PublicAPIBaseURL != "https://api.narratoai.cn" {
		t.Fatalf("PublicAPIBaseURL = %q, want first URL only", cfg.PublicAPIBaseURL)
	}
}

func TestLoadFrontendBaseURLsPreferExplicitList(t *testing.T) {
	t.Setenv("FRONTEND_BASE_URL", "https://default.example")
	t.Setenv("FRONTEND_BASE_URLS", "https://one.example/, https://two.example")

	cfg := Load()

	if cfg.FrontendBaseURL != "https://one.example" {
		t.Fatalf("FrontendBaseURL = %q, want first explicit URL", cfg.FrontendBaseURL)
	}
	if len(cfg.FrontendBaseURLs) != 2 || cfg.FrontendBaseURLs[0] != "https://one.example" || cfg.FrontendBaseURLs[1] != "https://two.example" {
		t.Fatalf("FrontendBaseURLs = %#v, want explicit URL list", cfg.FrontendBaseURLs)
	}
}

func TestCampaignSMTPRequiresExplicitDedicatedConfiguration(t *testing.T) {
	cfg := Config{
		CampaignSMTPHost:           "smtpdm.aliyun.com",
		CampaignSMTPPort:           465,
		CampaignSMTPUsername:       "campaign-user",
		CampaignSMTPPassword:       "campaign-password",
		CampaignSMTPFromEmail:      "updates@example.com",
		CampaignSMTPReplyTo:        "support@example.com",
		CampaignUnsubscribeBaseURL: "https://api.example.com",
	}
	if !cfg.CampaignSMTPConfigured() {
		t.Fatal("complete campaign SMTP configuration should be enabled")
	}

	cfg.CampaignUnsubscribeBaseURL = ""
	if cfg.CampaignSMTPConfigured() {
		t.Fatal("campaign SMTP must require a public unsubscribe URL")
	}
	cfg.CampaignUnsubscribeBaseURL = "https://api.example.com"
	cfg.CampaignSMTPReplyTo = ""
	if cfg.CampaignSMTPConfigured() {
		t.Fatal("campaign SMTP must require an explicit reply address")
	}

	cfg.CampaignSMTPReplyTo = "support@example.com"
	cfg.SMTPFromEmail = cfg.CampaignSMTPFromEmail
	if cfg.CampaignSMTPConfigured() {
		t.Fatal("campaign SMTP must not reuse the transactional sender address")
	}
}

func TestLoadMovieVisionThinkingBudget(t *testing.T) {
	t.Setenv("MOVIE_VISION_THINKING_BUDGET", "4060")
	if got := Load().MovieVisionThinkingBudget; got != 4060 {
		t.Fatalf("MovieVisionThinkingBudget = %d, want 4060", got)
	}
	for _, value := range []string{"", "abc", "-5"} {
		t.Setenv("MOVIE_VISION_THINKING_BUDGET", value)
		if got := Load().MovieVisionThinkingBudget; got != 0 {
			t.Fatalf("MOVIE_VISION_THINKING_BUDGET=%q loaded %d, want 0 (thinking off)", value, got)
		}
	}
}

func TestQwenTTSModelConfiguredOnlyByEnvironment(t *testing.T) {
	t.Setenv("QWEN_TTS_MODEL", "qwen3-tts-flash")
	if got := Load().QwenTTSModel; got != "qwen3-tts-flash" {
		t.Fatalf("model = %q", got)
	}
	t.Setenv("QWEN_TTS_MODEL", "custom-tts-snapshot")
	if got := Load().QwenTTSModel; got != "custom-tts-snapshot" {
		t.Fatalf("model = %q", got)
	}
	t.Setenv("QWEN_TTS_MODEL", "")
	if got := Load().QwenTTSModel; got != "" {
		t.Fatalf("unexpected hardcoded model %q", got)
	}
}
