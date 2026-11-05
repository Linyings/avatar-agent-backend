package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
)

type billingModelsResponse struct {
	Modes   map[string][]billingModelEntry `json:"modes"`
	History []billingModelHistoryEntry     `json:"history"`
}

func configureBillingModelsApp(app *App) {
	cfg := app.cfg
	cfg.OpenAIProAPIKey = "pro-key"
	cfg.OpenAIProAPIBaseURL = "https://open.bigmodel.cn/api/paas/v4"
	cfg.OpenAIProModel = "glm-5.3-flashx"
	cfg.OpenAIFlashAPIKey = "flash-key"
	cfg.OpenAIFlashAPIBaseURL = "https://llm.example.com/v1"
	cfg.OpenAIFlashModel = "house-flash"
	cfg.DashScopeAPIKey = "dashscope-key"
	cfg.QwenTTSModel = "qwen3-tts-flash"
	cfg.QwenVisionModel = "qwen3-vl-flash"
	cfg.Qwen3ASRModel = "qwen3-asr-flash-filetrans"
	cfg.FunASRModel = "fun-asr"
	cfg.DubbingASRModel = "paraformer-v2"
	cfg.DubbingAccessKeyID = "ak"
	cfg.DubbingAccessKeySecret = "sk"
	cfg.DubbingOSSBucket = "narrato-dubbing"
	app.cfg = cfg
	app.dashscope = providers.NewDashScopeClient(cfg)
	app.silicon = providers.NewSiliconFlowClient(cfg)
	app.fish = providers.NewFishAudioClient(cfg)
	app.elevenlabs = providers.NewElevenLabsClient(cfg)
	dubbing := providers.NewDubbingClient(cfg)
	app.dubbing = &dubbing
}

func findBillingModel(t *testing.T, entries []billingModelEntry, kind, model string) billingModelEntry {
	t.Helper()
	for _, entry := range entries {
		if entry.ServiceKind == kind && entry.ModelID == model {
			return entry
		}
	}
	t.Fatalf("missing %s/%s in %+v", kind, model, entries)
	return billingModelEntry{}
}

func TestAdminBillingModelsReportsMatchedRules(t *testing.T) {
	app := newTestApp(t)
	configureBillingModelsApp(app)
	seedBillingRates(t, app, map[string]any{
		"llm": []map[string]any{
			{"provider": "", "model_id": "*", "tokens_per_credit": 1000},
			{"provider": "zhipu", "model_id": "glm-5.3-flashx", "input_tokens_per_credit": 100, "output_tokens_per_credit": 100, "hidden": true},
		},
		"dubbing_asr": []map[string]any{{"provider": "dashscope", "model_id": "paraformer-v2", "credits_per_minute": 2}},
		"modes": map[string]any{"dubbing": map[string]any{
			"audio_separation": []map[string]any{{"provider": "aliyun-mps", "model_id": "MusicDemix", "credits_per_minute": 3}},
		}},
	})
	path := "/api/v1/admin/billing/models"
	userToken := registerAndLogin(t, app, "billing-models-user@example.com")
	if r := request(app, http.MethodGet, path, nil, userToken); r.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d", r.Code)
	}
	adminToken := registerAndMakeAdmin(t, app, "billing-models-admin@example.com")
	admin := lookupUserByEmail(t, app, "billing-models-admin@example.com")
	now := time.Now().UTC()
	for _, record := range []models.TokenUsageRecord{
		{UserID: admin.ID, ServiceKind: "llm", Provider: "deepseek", ModelID: "deepseek-v4-pro", RecordedAt: now.Add(-3 * time.Hour)},
		{UserID: admin.ID, ServiceKind: "asr", Provider: "dashscope", ModelID: "fun-asr", RecordedAt: now.Add(-2 * time.Hour)},
		{UserID: admin.ID, ServiceKind: "llm", Provider: "deepseek", ModelID: "deepseek-v4-pro", RecordedAt: now.Add(-1 * time.Hour)},
		{UserID: admin.ID, ServiceKind: "custom", Provider: "openai", ModelID: "", RecordedAt: now},
	} {
		if err := app.db.Create(&record).Error; err != nil {
			t.Fatal(err)
		}
	}

	r := request(app, http.MethodGet, path, nil, adminToken)
	if r.Code != http.StatusOK {
		t.Fatalf("admin status = %d body = %s", r.Code, r.Body.String())
	}
	var body billingModelsResponse
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	general, dubbing := body.Modes["general"], body.Modes["dubbing"]

	pro := findBillingModel(t, general, "llm", "glm-5.3-flashx")
	if pro.Provider != "zhipu" || len(pro.Labels) != 1 || pro.Labels[0] != "Pro 模型" || pro.Rule == nil || pro.Rule.Mode != "general" || pro.Rule.ModelID != "glm-5.3-flashx" || pro.Rule.Provider != "zhipu" {
		t.Fatalf("pro entry: %+v rule=%+v", pro, pro.Rule)
	}
	flash := findBillingModel(t, general, "llm", "house-flash")
	if flash.Rule == nil || flash.Rule.Mode != "general" || flash.Rule.ModelID != "*" || flash.Rule.Provider != "" || flash.Builtin {
		t.Fatalf("flash entry: %+v rule=%+v", flash, flash.Rule)
	}
	asr := findBillingModel(t, general, "asr", "qwen3-asr-flash-filetrans")
	if asr.Rule != nil || asr.Builtin {
		t.Fatalf("unpriced asr entry: %+v rule=%+v", asr, asr.Rule)
	}
	tts := findBillingModel(t, dubbing, "tts", app.dashscope.DubbingTTSModel())
	if tts.Rule != nil || !tts.Builtin || tts.Provider != "dashscope" {
		t.Fatalf("builtin dubbing tts: %+v rule=%+v", tts, tts.Rule)
	}
	separation := findBillingModel(t, dubbing, "audio_separation", "MusicDemix")
	if separation.Rule == nil || separation.Rule.Mode != "dubbing" || separation.Provider != "aliyun-mps" || separation.Rule.Provider != "aliyunmps" {
		t.Fatalf("dubbing separation: %+v rule=%+v", separation, separation.Rule)
	}
	dubbingASR := findBillingModel(t, dubbing, "dubbing_asr", "paraformer-v2")
	if dubbingASR.Rule == nil || dubbingASR.Rule.Mode != "general" || dubbingASR.Rule.ModelID != "paraformer-v2" {
		t.Fatalf("dubbing asr general fallback: %+v rule=%+v", dubbingASR, dubbingASR.Rule)
	}
	for _, entry := range general {
		if entry.ServiceKind == "tts" {
			t.Fatalf("unconfigured tts provider listed: %+v", entry)
		}
	}

	if len(body.History) != 2 || body.History[0].ModelID != "deepseek-v4-pro" || body.History[0].Provider != "deepseek" || body.History[1].ModelID != "fun-asr" {
		t.Fatalf("history: %+v", body.History)
	}
}

func TestAdminBillingModelsEmptyArrays(t *testing.T) {
	app := newTestApp(t)
	token := registerAndMakeAdmin(t, app, "billing-models-empty@example.com")
	r := request(app, http.MethodGet, "/api/v1/admin/billing/models", nil, token)
	if r.Code != http.StatusOK {
		t.Fatalf("status = %d", r.Code)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(r.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["history"]) != "[]" {
		t.Fatalf("history = %s", raw["history"])
	}
	var modes map[string]json.RawMessage
	json.Unmarshal(raw["modes"], &modes)
	if string(modes["general"]) != "[]" || string(modes["dubbing"]) != "[]" || string(modes["movie"]) != "[]" {
		t.Fatalf("modes = %s", raw["modes"])
	}
}

func TestBillingHiddenRuleStillBillsAndCostEditsApply(t *testing.T) {
	app := newTestApp(t)
	configureBillingModelsApp(app)
	rule := map[string]any{"provider": "zhipu", "model_id": "glm-5.3-flashx", "input_tokens_per_credit": 100, "output_tokens_per_credit": 100}
	setting := map[string]any{"llm": []map[string]any{rule}}
	seedBillingRates(t, app, setting)
	save := func() {
		t.Helper()
		if err := validateModeBillingConfig(roundTripJSON(t, setting)); err != nil {
			t.Fatal(err)
		}
		if err := app.db.Model(&models.SystemSetting{}).Where("key = ?", "billing_rates").Update("value", jsonMap(setting)).Error; err != nil {
			t.Fatal(err)
		}
	}
	consumption := creditConsumption{Provider: "zhipu", ServiceKind: "llm", ModelID: "glm-5.3-flashx", Usage: providers.ChatUsage{PromptTokens: 1000, CompletionTokens: 500}}
	credits := func() int64 {
		t.Helper()
		amounts, err := app.billingAmountsForConsumption(consumption)
		if err != nil {
			t.Fatal(err)
		}
		return amounts.CreditCount
	}
	visible := credits()
	if visible != 15 {
		t.Fatalf("visible credits = %d", visible)
	}
	rule["hidden"] = true
	save()
	if hidden := credits(); hidden != visible {
		t.Fatalf("hidden rule billed %d, want %d", hidden, visible)
	}
	rule["input_tokens_per_credit"] = 50
	save()
	if edited := credits(); edited != 25 {
		t.Fatalf("edited credits = %d, want 25", edited)
	}
}

func roundTripJSON(t *testing.T, value any) any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMovieModeBillingUsesItsOwnRatesAndFallsBack(t *testing.T) {
	app := newTestApp(t)
	configureBillingModelsApp(app)
	setting := map[string]any{
		"llm": []map[string]any{{"provider": "zhipu", "model_id": "glm-5.3-flashx", "input_tokens_per_credit": 100, "output_tokens_per_credit": 100}},
		"modes": map[string]any{"dubbing": map[string]any{
			"tts": []map[string]any{{"provider": "dashscope", "model_id": app.dashscope.DubbingTTSModel(), "credits_per_1k_chars": 30}},
		}},
	}
	seedBillingRates(t, app, setting)
	save := func() {
		t.Helper()
		if err := validateModeBillingConfig(roundTripJSON(t, setting)); err != nil {
			t.Fatal(err)
		}
		if err := app.db.Model(&models.SystemSetting{}).Where("key = ?", "billing_rates").Update("value", jsonMap(setting)).Error; err != nil {
			t.Fatal(err)
		}
	}
	llm := func(metadata map[string]any) billingAmounts {
		t.Helper()
		amounts, err := app.billingAmountsForConsumption(creditConsumption{Provider: "zhipu", ServiceKind: "llm", ModelID: "glm-5.3-flashx",
			Usage: providers.ChatUsage{PromptTokens: 1000, CompletionTokens: 500}, RequestMetadata: metadata})
		if err != nil {
			t.Fatal(err)
		}
		return amounts
	}
	movieTag := map[string]any{"movie_project_id": "4f6b7a52-8f3e-4b8f-9a8a-1c2d3e4f5a6b"}
	if general := llm(nil); general.Mode != "general" || general.CreditCount != 15 {
		t.Fatalf("general = %+v", general)
	}
	// Until 电影模式 has its own rule, movie calls bill at the general price but are recorded as movie.
	if inherited := llm(movieTag); inherited.Mode != "movie" || inherited.CreditCount != 15 {
		t.Fatalf("movie fallback = %+v", inherited)
	}
	setting["modes"].(map[string]any)["movie"] = map[string]any{
		"llm": []map[string]any{{"provider": "zhipu", "model_id": "glm-5.3-flashx", "input_tokens_per_credit": 50, "output_tokens_per_credit": 50}},
	}
	save()
	if own := llm(movieTag); own.Mode != "movie" || own.CreditCount != 30 {
		t.Fatalf("movie own rate = %+v", own)
	}
	if general := llm(nil); general.CreditCount != 15 {
		t.Fatalf("general changed by movie rate: %+v", general)
	}

	// A movie narrated with a 搞笑配音 voice is a movie step: it keeps the 配音 price until 电影模式 prices it.
	tts := func() billingAmounts {
		t.Helper()
		amounts, err := app.billingAmountsForConsumption(creditConsumption{Provider: "dashscope", ServiceKind: "tts", ModelID: app.dashscope.DubbingTTSModel(),
			CharacterCount: 1000, DubbingTTS: true, BillingMode: "movie", RequestMetadata: movieTag})
		if err != nil {
			t.Fatal(err)
		}
		return amounts
	}
	if voice := tts(); voice.Mode != "movie" || voice.CreditCount != 30 {
		t.Fatalf("movie dubbing voice fallback = %+v", voice)
	}
	setting["modes"].(map[string]any)["movie"].(map[string]any)["tts"] = []map[string]any{{"provider": "dashscope", "model_id": app.dashscope.DubbingTTSModel(), "credits_per_1k_chars": 10}}
	save()
	if voice := tts(); voice.CreditCount != 10 {
		t.Fatalf("movie dubbing voice own rate = %+v", voice)
	}

	setting["modes"].(map[string]any)["movie"].(map[string]any)["free_experience"] = true
	save()
	if free := llm(movieTag); free.CreditCount != 0 || !free.Promotion {
		t.Fatalf("movie free experience = %+v", free)
	}
	if general := llm(nil); general.CreditCount != 15 || general.Promotion {
		t.Fatalf("general affected by movie promotion = %+v", general)
	}
}

func TestMovieDubbingVoiceStepBillsAsMovie(t *testing.T) {
	execution := workflowRequestExecution{Identity: &workflowRequestIdentity{}, Run: models.WorkflowRun{ClientType: "pc"}}
	execution.Step.StepID = "dubbing-tts-movie-abc"
	movie := execution.EnrichCreditConsumption(creditConsumption{ServiceKind: "tts"})
	if movie.BillingMode != "movie" || !movie.DubbingTTS {
		t.Fatalf("movie voice step = %+v", movie)
	}
	execution.Step.StepID = "dubbing-tts-line-1"
	dubbing := execution.EnrichCreditConsumption(creditConsumption{ServiceKind: "tts"})
	if dubbing.BillingMode != "dubbing" {
		t.Fatalf("dubbing step = %+v", dubbing)
	}
}

func TestAdminBillingModelsListsMovieModels(t *testing.T) {
	app := newTestApp(t)
	configureBillingModelsApp(app)
	app.cfg.MovieNarrationAPIKey = "writer-key"
	app.cfg.MovieNarrationAPIBaseURL = "https://api.deepseek.com/v1"
	app.cfg.MovieNarrationModel = "dedicated-writer"
	seedBillingRates(t, app, map[string]any{
		"llm": []map[string]any{{"provider": "zhipu", "model_id": "glm-5.3-flashx", "tokens_per_credit": 100}},
		"modes": map[string]any{"movie": map[string]any{
			"asr": []map[string]any{{"provider": "dashscope", "model_id": "fun-asr", "credits_per_minute": 2}},
		}},
	})
	token := registerAndMakeAdmin(t, app, "billing-models-movie@example.com")
	r := request(app, http.MethodGet, "/api/v1/admin/billing/models", nil, token)
	var body billingModelsResponse
	if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &body) != nil {
		t.Fatalf("status = %d body = %s", r.Code, r.Body.String())
	}
	movie := body.Modes["movie"]
	writer := findBillingModel(t, movie, "llm", "dedicated-writer")
	if writer.Provider != "deepseek" {
		t.Fatalf("writer provider = %s", writer.Provider)
	}
	for _, entry := range body.Modes["general"] {
		if entry.ModelID == "dedicated-writer" {
			t.Fatal("movie writer leaked into general mode model list")
		}
	}
	pro := findBillingModel(t, movie, "llm", "glm-5.3-flashx")
	if pro.Rule == nil || pro.Rule.Mode != "general" {
		t.Fatalf("movie pro falls back to general: %+v rule=%+v", pro, pro.Rule)
	}
	asr := findBillingModel(t, movie, "asr", "fun-asr")
	if asr.Rule == nil || asr.Rule.Mode != "movie" {
		t.Fatalf("movie asr own rule: %+v rule=%+v", asr, asr.Rule)
	}
	findBillingModel(t, movie, "tts", app.dashscope.DubbingTTSModel())
	for _, entry := range movie {
		if entry.ModelID == "qwen3-asr-flash-filetrans" || entry.ServiceKind == "vision" {
			t.Fatalf("model movie mode does not use: %+v", entry)
		}
	}
}
