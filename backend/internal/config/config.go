package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/narratoai/backend-go/internal/avatar"

	"github.com/joho/godotenv"
)

type Config struct {
	Avatar                   avatar.Options
	Debug                    bool
	Environment              string
	Host                     string
	Port                     int
	APIv1Prefix              string
	OpenAPIURL               string
	DocsURL                  string
	RedocURL                 string
	DatabaseURL              string
	AutoMigrate              bool
	SecretKey                string
	CORSOrigins              []string
	AccessTTL                time.Duration
	RefreshTTL               time.Duration
	PromptTemplateDir        string
	FrontendBaseURL          string
	FrontendBaseURLs         []string
	PublicAPIBaseURL         string
	DesktopUpdateRoot        string
	DesktopDownloadOSSBucket string
	DesktopDownloadOSSRegion string
	TavilyAPIKey             string
	TavilyAPIBaseURL         string
	TavilySearchDepth        string
	TavilyMaxResults         int
	TavilyTimeout            time.Duration

	SMTPHost      string
	SMTPPort      int
	SMTPUsername  string
	SMTPPassword  string
	SMTPFromEmail string
	SMTPFromName  string
	SMTPUseTLS    bool

	// Campaign SMTP is intentionally separate from transactional verification mail.
	// An empty campaign configuration keeps promotional delivery disabled.
	CampaignSMTPHost           string
	CampaignSMTPPort           int
	CampaignSMTPUsername       string
	CampaignSMTPPassword       string
	CampaignSMTPFromEmail      string
	CampaignSMTPFromName       string
	CampaignSMTPReplyTo        string
	CampaignSMTPUseTLS         bool
	CampaignUnsubscribeBaseURL string
	GoogleClientID             string
	GoogleClientSecret         string
	GoogleTokenURL             string
	GoogleUserInfoURL          string
	GitHubClientID             string
	GitHubClientSecret         string

	WeChatAppID           string
	WeChatMchID           string
	WeChatAPIV3Key        string
	WeChatPrivateKey      string
	WeChatPrivateKeyFile  string
	WeChatCertSerialNo    string
	WeChatNotifyURL       string
	WeChatCertDir         string
	WeChatVerifySignature bool

	CreemAPIKey                 string
	CreemWebhookSecret          string
	CreemTestMode               bool
	CreemSuccessURL             string
	CreemPortalReturnURL        string
	SubscriptionPaymentProvider string
	CreditPaymentProvider       string
	CreemCheckoutEnabled        bool

	AlipayAppID          string
	AlipayMerchantID     string
	AlipayPrivateKey     string
	AlipayPrivateKeyFile string
	AlipayPublicKey      string
	AlipayPublicKeyFile  string
	AlipayGatewayURL     string
	AlipayNotifyURL      string
	AlipayReturnURL      string
	AlipayPortalURL      string
	AlipayPaymentMethod  string

	DashScopeAPIKey                   string
	DashScopeUseIntl                  bool
	DashScopeTimeout                  time.Duration
	QwenTTSModel                      string
	TTSModel                          string
	TTSSpeaker                        string
	TTSLanguage                       string
	TTSFormat                         string
	Qwen3ASRModel                     string
	FunASRModel                       string
	DubbingASRModel                   string
	DubbingAccessKeyID                string
	DubbingAccessKeySecret            string
	DubbingOSSBucket                  string
	DubbingSeparationCreditsPerMinute float64
	DubbingASRCreditsPerMinute        float64
	DubbingTTSCreditsMultiplier       float64
	QwenVisionModel                   string
	QwenVisionBaseURL                 string
	SiliconFlowAPIKey                 string
	SiliconFlowTTSModel               string
	SiliconFlowTTSVoice               string
	FishAudioAPIKey                   string
	FishAudioBaseURL                  string
	FishAudioModel                    string
	ElevenLabsAPIKey                  string
	ElevenLabsBaseURL                 string
	ElevenLabsScribeModel             string
	SiliconFlowBaseURL                string
	// Dedicated desktop movie narration writer; never inherits Pro/Flash settings.
	MovieNarrationAPIKey          string
	MovieNarrationAPIBaseURL      string
	MovieNarrationModel           string
	MovieNarrationThinkingType    string
	MovieNarrationReasoningEffort string
	OpenAIProAPIKey               string
	OpenAIProAPIBaseURL           string
	OpenAIProModel                string
	OpenAIProSupportsVision       bool
	OpenAIFlashAPIKey             string
	OpenAIFlashAPIBaseURL         string
	OpenAIFlashModel              string
	OpenAIFlashThinkingType       string
	OpenAIFlashReasoningEffort    string
	OpenAITemperature             float64
	OpenAIProThinkingType         string
	OpenAIProReasoningEffort      string
	// 画面分析 model, separate from Pro/Flash. INPUT is "image" (a 3x3 contact sheet)
	// or "video" (a short clip of the segment with its audio); left empty, omni models
	// such as qwen3.8-omni-flash get video and every other model gets images.
	MovieVisionAPIKey          string
	MovieVisionAPIBaseURL      string
	MovieVisionModel           string
	MovieVisionInput           string
	MovieVisionThinkingType    string
	MovieVisionReasoningEffort string
	// 0 keeps thinking off; above 0 turns DashScope thinking on with this reasoning-token budget.
	MovieVisionThinkingBudget int
	// Platform DeepSeek profile, chosen by clients with model_provider=deepseek (billed in credits).
	DeepSeekAPIKey          string
	DeepSeekAPIBaseURL      string
	DeepSeekModel           string
	DeepSeekThinkingType    string
	DeepSeekReasoningEffort string

	LLMPromptCacheMode         string
	OpenAIPromptCacheKey       bool
	OpenAIPromptCacheRetention string
	DashScopeExplicitCache     bool
}

const (
	defaultPromptTemplateDir = "prompt"
)

func Load() Config {
	_ = godotenv.Load()
	frontendBaseURLs := envURLList("FRONTEND_BASE_URLS", env("FRONTEND_BASE_URL", "http://localhost:5173"))

	cfg := Config{
		Avatar:                   loadAvatarOptions(),
		Debug:                    envBool("DEBUG", true),
		Environment:              env("ENVIRONMENT", "development"),
		Host:                     env("HOST", "0.0.0.0"),
		Port:                     envInt("PORT", 8000),
		APIv1Prefix:              env("API_V1_PREFIX", "/api/v1"),
		OpenAPIURL:               env("OPENAPI_URL", "/api/v1/openapi.json"),
		DocsURL:                  env("DOCS_URL", "/docs"),
		RedocURL:                 env("REDOC_URL", "/redoc"),
		DatabaseURL:              env("DATABASE_URL", "postgres://narratoai:narratoai@localhost:5433/narratoai?sslmode=disable"),
		AutoMigrate:              envBool("AUTO_MIGRATE", true),
		SecretKey:                env("SECRET_KEY", "change-me-change-me-change-me-change-me"),
		CORSOrigins:              envCSV("CORS_ORIGINS", "http://localhost:3000,http://localhost:5173"),
		AccessTTL:                30 * time.Minute,
		RefreshTTL:               7 * 24 * time.Hour,
		PromptTemplateDir:        defaultPromptTemplateDir,
		FrontendBaseURL:          firstString(frontendBaseURLs, "http://localhost:5173"),
		FrontendBaseURLs:         frontendBaseURLs,
		PublicAPIBaseURL:         envSingleURL("PUBLIC_API_BASE_URL", ""),
		DesktopUpdateRoot:        env("DESKTOP_UPDATE_ROOT", ""),
		DesktopDownloadOSSBucket: env("DESKTOP_DOWNLOAD_OSS_BUCKET", ""),
		DesktopDownloadOSSRegion: env("DESKTOP_DOWNLOAD_OSS_REGION", "cn-shanghai"),
		TavilyAPIKey:             env("TAVILY_API_KEY", ""),
		TavilyAPIBaseURL:         envSingleURL("TAVILY_API_BASE_URL", "https://api.tavily.com"),
		TavilySearchDepth:        strings.ToLower(env("TAVILY_SEARCH_DEPTH", "basic")),
		TavilyMaxResults:         envInt("TAVILY_MAX_RESULTS", 5),
		TavilyTimeout:            time.Duration(envInt("TAVILY_REQUEST_TIMEOUT_SECONDS", 20)) * time.Second,

		SMTPHost:      env("SMTP_HOST", ""),
		SMTPPort:      envInt("SMTP_PORT", 587),
		SMTPUsername:  env("SMTP_USERNAME", ""),
		SMTPPassword:  env("SMTP_PASSWORD", ""),
		SMTPFromEmail: env("SMTP_FROM_EMAIL", ""),
		SMTPFromName:  env("SMTP_FROM_NAME", "NarratoAI"),
		SMTPUseTLS:    envBool("SMTP_USE_TLS", true),

		GoogleClientID: env("GOOGLE_CLIENT_ID", ""),

		CampaignSMTPHost:           env("CAMPAIGN_SMTP_HOST", ""),
		CampaignSMTPPort:           envInt("CAMPAIGN_SMTP_PORT", 465),
		CampaignSMTPUsername:       env("CAMPAIGN_SMTP_USERNAME", ""),
		CampaignSMTPPassword:       env("CAMPAIGN_SMTP_PASSWORD", ""),
		CampaignSMTPFromEmail:      env("CAMPAIGN_SMTP_FROM_EMAIL", ""),
		CampaignSMTPFromName:       env("CAMPAIGN_SMTP_FROM_NAME", "NarratoAI"),
		CampaignSMTPReplyTo:        env("CAMPAIGN_SMTP_REPLY_TO", ""),
		CampaignSMTPUseTLS:         envBool("CAMPAIGN_SMTP_USE_TLS", true),
		CampaignUnsubscribeBaseURL: envSingleURL("CAMPAIGN_UNSUBSCRIBE_BASE_URL", ""),
		GoogleClientSecret:         env("GOOGLE_CLIENT_SECRET", ""),
		GoogleTokenURL:             env("GOOGLE_TOKEN_URL", "https://oauth2.googleapis.com/token"),
		GoogleUserInfoURL:          env("GOOGLE_USERINFO_URL", "https://openidconnect.googleapis.com/v1/userinfo"),
		GitHubClientID:             env("GITHUB_CLIENT_ID", ""),
		GitHubClientSecret:         env("GITHUB_CLIENT_SECRET", ""),

		WeChatAppID:           env("WECHAT_APP_ID", ""),
		WeChatMchID:           env("WECHAT_MCH_ID", ""),
		WeChatAPIV3Key:        env("WECHAT_API_V3_KEY", ""),
		WeChatPrivateKey:      strings.ReplaceAll(env("WECHAT_PRIVATE_KEY", ""), "\\n", "\n"),
		WeChatPrivateKeyFile:  env("WECHAT_PRIVATE_KEY_FILE", ""),
		WeChatCertSerialNo:    env("WECHAT_CERT_SERIAL_NO", ""),
		WeChatNotifyURL:       env("WECHAT_NOTIFY_URL", ""),
		WeChatCertDir:         env("WECHAT_CERT_DIR", "./data/wechat_certs"),
		WeChatVerifySignature: envBool("WECHAT_VERIFY_SIGNATURE", true),

		CreemAPIKey:                 env("CREEM_API_KEY", ""),
		CreemWebhookSecret:          env("CREEM_WEBHOOK_SECRET", ""),
		CreemTestMode:               envBool("CREEM_TEST_MODE", true),
		CreemSuccessURL:             env("CREEM_SUCCESS_URL", ""),
		CreemPortalReturnURL:        env("CREEM_PORTAL_RETURN_URL", ""),
		SubscriptionPaymentProvider: strings.ToLower(env("SUBSCRIPTION_PAYMENT_PROVIDER", "alipay")),
		CreditPaymentProvider:       strings.ToLower(env("CREDIT_PAYMENT_PROVIDER", "wechat")),
		CreemCheckoutEnabled:        envBool("CREEM_CHECKOUT_ENABLED", false),

		AlipayAppID:          env("ALIPAY_APP_ID", ""),
		AlipayMerchantID:     env("ALIPAY_MERCHANT_ID", ""),
		AlipayPrivateKey:     strings.ReplaceAll(env("ALIPAY_PRIVATE_KEY", ""), "\\n", "\n"),
		AlipayPrivateKeyFile: env("ALIPAY_PRIVATE_KEY_FILE", ""),
		AlipayPublicKey:      strings.ReplaceAll(env("ALIPAY_PUBLIC_KEY", ""), "\\n", "\n"),
		AlipayPublicKeyFile:  env("ALIPAY_PUBLIC_KEY_FILE", ""),
		AlipayGatewayURL:     env("ALIPAY_GATEWAY_URL", "https://openapi.alipay.com/gateway.do"),
		AlipayNotifyURL:      env("ALIPAY_NOTIFY_URL", ""),
		AlipayReturnURL:      env("ALIPAY_RETURN_URL", ""),
		AlipayPortalURL:      env("ALIPAY_PORTAL_URL", ""),
		AlipayPaymentMethod:  strings.ToLower(env("ALIPAY_PAYMENT_METHOD", "page")),

		DashScopeAPIKey:                   env("DASHSCOPE_API_KEY", ""),
		DashScopeUseIntl:                  envBool("DASHSCOPE_USE_INTL", false),
		DashScopeTimeout:                  time.Duration(envInt("DASHSCOPE_REQUEST_TIMEOUT_SECONDS", 60)) * time.Second,
		QwenTTSModel:                      env("QWEN_TTS_MODEL", ""),
		TTSModel:                          env("TTS_MODEL", ""),
		TTSSpeaker:                        env("TTS_SPEAKER", ""),
		TTSLanguage:                       env("TTS_LANGUAGE", "zh-cn"),
		TTSFormat:                         env("TTS_FORMAT", "wav"),
		Qwen3ASRModel:                     env("QWEN3_ASR_MODEL", "qwen3-asr-flash-filetrans"),
		FunASRModel:                       env("FUNASR_MODEL", "fun-asr"),
		DubbingASRModel:                   env("DUBBING_ASR_MODEL", "paraformer-v2"),
		DubbingAccessKeyID:                env("DUBBING_ACCESS_KEY_ID", ""),
		DubbingAccessKeySecret:            env("DUBBING_ACCESS_KEY_SECRET", ""),
		DubbingOSSBucket:                  env("DUBBING_OSS_BUCKET", ""),
		DubbingSeparationCreditsPerMinute: envFloat("DUBBING_SEPARATION_CREDITS_PER_MINUTE", 0),
		DubbingASRCreditsPerMinute:        envFloat("DUBBING_ASR_CREDITS_PER_MINUTE", 0),
		DubbingTTSCreditsMultiplier:       envFloat("DUBBING_TTS_CREDITS_MULTIPLIER", 1),
		QwenVisionModel:                   env("QWEN_VISION_MODEL", "qwen3.6-plus"),
		QwenVisionBaseURL:                 env("QWEN_VISION_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		SiliconFlowAPIKey:                 env("SILICONFLOW_API_KEY", ""),
		SiliconFlowTTSModel:               env("SILICONFLOW_TTS_MODEL", "FunAudioLLM/CosyVoice2-0.5B"),
		SiliconFlowTTSVoice:               env("SILICONFLOW_TTS_VOICE", ""),
		FishAudioAPIKey:                   env("FISH_AUDIO_API_KEY", ""),
		FishAudioBaseURL:                  env("FISH_AUDIO_BASE_URL", "https://api.fish.audio"),
		FishAudioModel:                    env("FISH_AUDIO_MODEL", "s1"),
		ElevenLabsAPIKey:                  env("ELEVENLABS_API_KEY", ""),
		ElevenLabsBaseURL:                 env("ELEVENLABS_BASE_URL", "https://api.elevenlabs.io"),
		ElevenLabsScribeModel:             env("ELEVENLABS_SCRIBE_MODEL", "scribe_v2"),
		SiliconFlowBaseURL:                env("SILICONFLOW_API_BASE_URL", "https://api.siliconflow.cn/v1"),
		MovieNarrationAPIKey:              env("MOVIE_NARRATION_API_KEY", ""),
		MovieNarrationAPIBaseURL:          env("MOVIE_NARRATION_API_BASE_URL", ""),
		MovieNarrationModel:               env("MOVIE_NARRATION_MODEL", ""),
		MovieNarrationThinkingType:        env("MOVIE_NARRATION_THINKING_TYPE", ""),
		MovieNarrationReasoningEffort:     env("MOVIE_NARRATION_REASONING_EFFORT", ""),
		OpenAIProAPIKey:                   env("OPENAI_PRO_API_KEY", ""),
		OpenAIProAPIBaseURL:               env("OPENAI_PRO_API_BASE_URL", "https://api.openai.com/v1"),
		OpenAIProModel:                    env("OPENAI_PRO_MODEL", "gpt-4.1"),
		OpenAIProSupportsVision:           envBool("OPENAI_PRO_SUPPORTS_VISION", true),
		OpenAIFlashAPIKey:                 env("OPENAI_FLASH_API_KEY", ""),
		OpenAIFlashAPIBaseURL:             env("OPENAI_FLASH_API_BASE_URL", "https://api.openai.com/v1"),
		OpenAIFlashModel:                  env("OPENAI_FLASH_MODEL", "gpt-4.1-mini"),
		OpenAIFlashThinkingType:           env("OPENAI_FLASH_THINKING_TYPE", ""),
		OpenAIFlashReasoningEffort:        env("OPENAI_FLASH_REASONING_EFFORT", ""),
		OpenAITemperature:                 envFloat("OPENAI_TEMPERATURE", 0.7),
		OpenAIProThinkingType:             env("OPENAI_PRO_THINKING_TYPE", ""),
		OpenAIProReasoningEffort:          env("OPENAI_PRO_REASONING_EFFORT", ""),
		MovieVisionAPIKey:                 env("DASHSCOPE_API_KEY", ""),
		MovieVisionAPIBaseURL:             env("QWEN_VISION_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		MovieVisionModel:                  env("QWEN_VISION_MODEL", "qwen3.6-plus"),
		MovieVisionThinkingType:           "disabled",
		MovieVisionThinkingBudget:         max(0, envInt("MOVIE_VISION_THINKING_BUDGET", 0)),
		DeepSeekAPIKey:                    env("DEEPSEEK_API_KEY", ""),
		DeepSeekAPIBaseURL:                env("DEEPSEEK_API_BASE_URL", "https://api.deepseek.com"),
		DeepSeekModel:                     env("DEEPSEEK_MODEL", "deepseek-chat"),
		DeepSeekThinkingType:              env("DEEPSEEK_THINKING_TYPE", ""),
		DeepSeekReasoningEffort:           env("DEEPSEEK_REASONING_EFFORT", ""),

		LLMPromptCacheMode:         env("LLM_PROMPT_CACHE_MODE", "auto"),
		OpenAIPromptCacheKey:       envBool("OPENAI_PROMPT_CACHE_KEY_ENABLED", true),
		OpenAIPromptCacheRetention: env("OPENAI_PROMPT_CACHE_RETENTION", ""),
		DashScopeExplicitCache:     envBool("DASHSCOPE_EXPLICIT_CACHE_ENABLED", true),
	}
	applyRunningHubLLM(&cfg)
	return cfg
}

// applyRunningHubLLM fills the flash profile from the RunningHub LLM endpoint
// when that endpoint is configured and the flash profile is still the stock default.
func applyRunningHubLLM(cfg *Config) {
	base := strings.TrimSpace(env("RUNNINGHUB_LLM_BASE_URL", ""))
	model := strings.TrimSpace(env("RUNNINGHUB_LLM_MODEL", ""))
	if base == "" || model == "" || strings.TrimSpace(cfg.OpenAIFlashAPIKey) != "" || cfg.OpenAIFlashModel != "gpt-4.1-mini" {
		return
	}
	key := strings.TrimSpace(env("VIDEO_API_KEY", ""))
	if key == "" {
		key = strings.TrimSpace(env("RUNNINGHUB_API_KEY", ""))
	}
	if key == "" {
		return
	}
	cfg.OpenAIFlashAPIKey = key
	cfg.OpenAIFlashAPIBaseURL = base
	cfg.OpenAIFlashModel = model
}

func loadAvatarOptions() avatar.Options {
	o := avatar.Options{
		Provider: env("VIDEO_PROVIDER", "runninghub"), Protocol: env("VIDEO_API_PROTOCOL", ""),
		Model: env("VIDEO_MODEL", ""), Resolution: env("VIDEO_RESOLUTION", ""),
		BaseURL: env("VIDEO_API_BASE_URL", ""), APIKey: env("VIDEO_API_KEY", ""),
		UploadPath: env("VIDEO_UPLOAD_PATH", ""), GenerationPath: env("VIDEO_GENERATION_PATH", ""), QueryPath: env("VIDEO_QUERY_PATH", ""),
		SupportsReferenceAudio: envBool("VIDEO_REFERENCE_AUDIO_SUPPORTED", false),
		Enabled:                envBool("AVATAR_MVP_ENABLED", false), BaselineApproved: envBool("AVATAR_MVP_BASELINE_APPROVED", false),
		BaselineRequestID: env("AVATAR_MVP_BASELINE_REQUEST_ID", ""),
		DataRoot:          env("AVATAR_MVP_DATA_ROOT", "data/avatar-mvp"), RuntimeRoot: env("AVATAR_MVP_RUNTIME_ROOT", "avatar-runtime"),
		Python: env("AVATAR_MVP_PYTHON", "python"), Node: env("AVATAR_MVP_NODE", "node"), FFmpeg: env("AVATAR_MVP_FFMPEG", "ffmpeg"), FFprobe: env("AVATAR_MVP_FFPROBE", "ffprobe"),
		ASRBackend: env("AVATAR_MVP_ASR_BACKEND", "whisper"), ASRModelDir: env("AVATAR_MVP_ASR_MODEL_DIR", ""),
		PollInterval: 5 * time.Second, JobTimeout: time.Duration(envInt("AVATAR_MVP_TIMEOUT_MINUTES", 60)) * time.Minute,
	}
	if o.Provider == "runninghub" {
		if o.APIKey == "" {
			o.APIKey = env("RUNNINGHUB_API_KEY", "")
		}
		if o.BaseURL == "" {
			o.BaseURL = env("RUNNINGHUB_BASE_URL", "https://www.runninghub.cn")
		}
	} else if o.Provider == "minimax" && o.APIKey == "" {
		o.APIKey = env("MINIMAX_API_KEY", "")
	}
	return o.VideoDefaults()
}

func (c Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}
func (c Config) WeChatConfigured() bool {
	return c.WeChatAppID != "" && c.WeChatMchID != "" && c.WeChatAPIV3Key != "" && c.WeChatCertSerialNo != "" && (c.WeChatPrivateKey != "" || c.WeChatPrivateKeyFile != "")
}

func (c Config) CreemConfigured() bool {
	return c.CreemAPIKey != ""
}

func (c Config) NewCreemCheckoutEnabled() bool {
	return c.CreemCheckoutEnabled && c.CreemConfigured()
}

func (c Config) AlipayConfigured() bool {
	return c.AlipayAppID != "" && c.AlipayMerchantID != "" &&
		(c.AlipayPrivateKey != "" || c.AlipayPrivateKeyFile != "") &&
		(c.AlipayPublicKey != "" || c.AlipayPublicKeyFile != "") &&
		c.AlipayNotifyURL != "" && c.AlipayReturnURL != ""
}

func (c Config) DashScopeConfigured() bool {
	return c.DashScopeAPIKey != ""
}

func (c Config) SiliconFlowConfigured() bool {
	return c.SiliconFlowAPIKey != ""
}

func (c Config) OpenAIConfigured() bool {
	return c.OpenAIProConfigured() || c.OpenAIFlashConfigured()
}

func (c Config) MovieNarrationConfigured() bool {
	return strings.TrimSpace(c.MovieNarrationAPIKey) != "" && strings.TrimSpace(c.MovieNarrationAPIBaseURL) != "" && strings.TrimSpace(c.MovieNarrationModel) != ""
}

func (c Config) OpenAIProConfigured() bool {
	return c.OpenAIProAPIKey != "" && c.OpenAIProAPIBaseURL != "" && c.OpenAIProModel != ""
}

// MovieVisionConfigured reports whether 画面分析 can use the DashScope QWEN_VISION_MODEL;
// without a DashScope key it falls back to the Pro model with contact-sheet images.
func (c Config) MovieVisionConfigured() bool {
	return c.MovieVisionAPIKey != "" && c.MovieVisionAPIBaseURL != "" && c.MovieVisionModel != ""
}

// MovieVisionVideoInput reports whether 画面分析 sends video clips instead of contact sheets.
func (c Config) MovieVisionVideoInput() bool {
	if !c.MovieVisionConfigured() {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(c.MovieVisionInput)) {
	case "video":
		return true
	case "image":
		return false
	default:
		return strings.Contains(strings.ToLower(c.MovieVisionModel), "omni")
	}
}

func (c Config) OpenAIFlashConfigured() bool {
	return c.OpenAIFlashAPIKey != "" && c.OpenAIFlashAPIBaseURL != "" && c.OpenAIFlashModel != ""
}

func (c Config) ElevenLabsConfigured() bool {
	return c.ElevenLabsAPIKey != ""
}

// CampaignSMTPConfigured deliberately requires explicit credentials and a
// public unsubscribe URL, so transactional SMTP settings cannot accidentally
// be reused for a marketing campaign.
func (c Config) CampaignSMTPConfigured() bool {
	return c.CampaignSMTPHost != "" && c.CampaignSMTPPort > 0 &&
		c.CampaignSMTPUsername != "" && c.CampaignSMTPPassword != "" &&
		c.CampaignSMTPFromEmail != "" && c.CampaignSMTPReplyTo != "" &&
		c.CampaignUnsubscribeBaseURL != "" &&
		(c.SMTPFromEmail == "" || !strings.EqualFold(c.CampaignSMTPFromEmail, c.SMTPFromEmail))
}

func (c Config) TavilyConfigured() bool {
	return c.TavilyAPIKey != "" && c.TavilyAPIBaseURL != ""
}

func (c Config) SMTPConfigured() bool {
	return c.SMTPHost != "" && c.SMTPPort > 0 && c.SMTPFromEmail != ""
}

func (c Config) GoogleOAuthConfigured() bool {
	return c.GoogleClientID != "" && c.GoogleClientSecret != ""
}

func (c Config) GitHubOAuthConfigured() bool {
	return c.GitHubClientID != "" && c.GitHubClientSecret != ""
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func envFloat(key string, fallback float64) float64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envCSV(key, fallback string) []string {
	raw := env(key, fallback)
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if item := strings.TrimSpace(part); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func envSingleURL(key, fallback string) string {
	raw := env(key, fallback)
	if comma := strings.Index(raw, ","); comma >= 0 {
		raw = raw[:comma]
	}
	return normalizeBaseURLString(raw)
}

func envURLList(key, fallback string) []string {
	raw := env(key, fallback)
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if item := normalizeBaseURLString(part); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func normalizeBaseURLString(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

func firstString(values []string, fallback string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return fallback
}
