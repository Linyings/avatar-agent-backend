package server

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/narratoai/backend-go/internal/models"
)

type billingModelRule struct {
	Mode        string `json:"mode"`
	ServiceKind string `json:"service_kind"`
	Provider    string `json:"provider"`
	ModelID     string `json:"model_id"`
}

type billingModelEntry struct {
	ServiceKind string            `json:"service_kind"`
	Provider    string            `json:"provider"`
	ModelID     string            `json:"model_id"`
	Labels      []string          `json:"labels"`
	Rule        *billingModelRule `json:"rule"`
	Builtin     bool              `json:"builtin"`
}

type billingModelHistoryEntry struct {
	ServiceKind string `json:"service_kind"`
	Provider    string `json:"provider"`
	ModelID     string `json:"model_id"`
}

type billingModelList struct {
	entries []billingModelEntry
	index   map[string]int
}

func (l *billingModelList) add(kind, provider, model, label string) {
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	key := kind + "\x00" + normalizeBillingProvider(provider) + "\x00" + model
	if l.index == nil {
		l.index = map[string]int{}
	}
	if i, exists := l.index[key]; exists {
		for _, existing := range l.entries[i].Labels {
			if existing == label {
				return
			}
		}
		l.entries[i].Labels = append(l.entries[i].Labels, label)
		return
	}
	l.index[key] = len(l.entries)
	l.entries = append(l.entries, billingModelEntry{ServiceKind: kind, Provider: provider, ModelID: model, Labels: []string{label}})
}

// The provider/model strings mirror what each consumption site passes to billing.
func (a *App) inUseBillingModels() map[string][]billingModelEntry {
	var general, dubbing billingModelList
	cfg := a.cfg
	if cfg.OpenAIProConfigured() {
		general.add("llm", a.inferOpenAICompatibleProvider(cfg.OpenAIProModel), cfg.OpenAIProModel, "Pro 模型")
	}
	if cfg.OpenAIFlashConfigured() {
		general.add("llm", a.inferOpenAICompatibleProvider(cfg.OpenAIFlashModel), cfg.OpenAIFlashModel, "Flash 模型")
	}
	if cfg.DeepSeekAPIKey != "" && cfg.DeepSeekModel != "" {
		general.add("llm", a.inferOpenAICompatibleProvider(cfg.DeepSeekModel), cfg.DeepSeekModel, "DeepSeek")
	}
	if cfg.MovieVisionConfigured() {
		general.add("llm", a.inferOpenAICompatibleProvider(cfg.MovieVisionModel), cfg.MovieVisionModel, "电影画面分析")
	}
	if a.dashscope.Configured() {
		general.add("vision", "dashscope", cfg.QwenVisionModel, "画面理解")
	}
	if a.silicon.Configured() {
		general.add("vision", "siliconflow", "Qwen/Qwen3-VL-32B-Instruct", "图片分析")
	}
	if a.dashscope.Configured() {
		general.add("asr", "dashscope", cfg.Qwen3ASRModel, "Qwen3 语音转写")
		general.add("asr", "dashscope", cfg.FunASRModel, "FunASR 语音转写")
	}
	if a.elevenlabs.Configured() {
		general.add("asr", "elevenlabs", cfg.ElevenLabsScribeModel, "ElevenLabs 转写")
	}
	if a.silicon.Configured() {
		model := strings.TrimSpace(cfg.SiliconFlowTTSModel)
		if model == "" {
			model = "FunAudioLLM/CosyVoice2-0.5B"
		}
		general.add("tts", "siliconflow", model, "SiliconFlow 配音")
	}
	if a.fish.Configured() {
		model := strings.TrimSpace(cfg.FishAudioModel)
		if model == "" {
			model = "s1"
		}
		general.add("tts", "fishaudio", "fish-audio-"+model, "Fish Audio 配音")
	}
	if a.dashscope.Configured() {
		dubbing.add("tts", "dashscope", a.dashscope.DubbingTTSModel(), "搞笑配音 TTS")
	}
	if a.dubbingProvider().Configured() {
		dubbing.add("audio_separation", "aliyun-mps", "MusicDemix", "人声分离")
	}
	if a.dashscope.Configured() {
		dubbing.add("dubbing_asr", "dashscope", cfg.DubbingASRModel, "配音转写")
	}
	movie := a.movieBillingModels()

	result := map[string][]billingModelEntry{}
	for mode, list := range map[string]*billingModelList{"general": &general, "dubbing": &dubbing, "movie": &movie} {
		entries := list.entries
		if entries == nil {
			entries = []billingModelEntry{}
		}
		for i := range entries {
			entry := &entries[i]
			rate, source, found, _ := a.matchModeBillingRate(mode, entry.Provider, entry.ServiceKind, entry.ModelID)
			if !found {
				continue
			}
			if source == "builtin" {
				entry.Builtin = true
				continue
			}
			entry.Rule = &billingModelRule{Mode: source, ServiceKind: entry.ServiceKind, Provider: rate.Provider, ModelID: strings.TrimSpace(rate.ModelID)}
		}
		result[mode] = entries
	}
	return result
}

// Models a desktop movie project is billed for: the Pro/Flash text models, 画面分析
// (its own vision model, or Pro reading contact sheets), hosted FunASR and the narration voices.
func (a *App) movieBillingModels() billingModelList {
	var movie billingModelList
	cfg := a.cfg
	if cfg.OpenAIProConfigured() {
		movie.add("llm", a.inferOpenAICompatibleProvider(cfg.OpenAIProModel), cfg.OpenAIProModel, "Pro 模型")
	}
	if cfg.OpenAIFlashConfigured() {
		movie.add("llm", a.inferOpenAICompatibleProvider(cfg.OpenAIFlashModel), cfg.OpenAIFlashModel, "Flash 模型")
	}
	if cfg.MovieNarrationConfigured() {
		movie.add("llm", a.inferOpenAICompatibleProvider(cfg.MovieNarrationModel), cfg.MovieNarrationModel, "解说稿写作")
	}
	if cfg.MovieVisionConfigured() {
		movie.add("llm", a.inferOpenAICompatibleProvider(cfg.MovieVisionModel), cfg.MovieVisionModel, "画面分析")
	} else if cfg.OpenAIProSupportsVision && cfg.OpenAIProConfigured() {
		movie.add("llm", a.inferOpenAICompatibleProvider(cfg.OpenAIProModel), cfg.OpenAIProModel, "画面分析")
	}
	if a.dashscope.Configured() {
		movie.add("asr", "dashscope", cfg.FunASRModel, "原片对白转写")
	}
	if a.silicon.Configured() {
		model := strings.TrimSpace(cfg.SiliconFlowTTSModel)
		if model == "" {
			model = "FunAudioLLM/CosyVoice2-0.5B"
		}
		movie.add("tts", "siliconflow", model, "解说配音")
	}
	if a.dashscope.Configured() {
		movie.add("tts", "dashscope", a.dashscope.DubbingTTSModel(), "解说配音（搞笑配音音色）")
	}
	return movie
}

func (a *App) billingModelHistory() ([]billingModelHistoryEntry, error) {
	history := []billingModelHistoryEntry{}
	err := a.db.Model(&models.TokenUsageRecord{}).
		Select("service_kind, provider, model_id").
		Where("model_id <> ''").
		Group("service_kind, provider, model_id").
		Order("MAX(recorded_at) DESC").
		Limit(200).
		Scan(&history).Error
	return history, err
}

func (a *App) adminBillingModels(c *gin.Context) {
	history, err := a.billingModelHistory()
	if err != nil {
		fail(c, http.StatusInternalServerError, "读取模型使用记录失败")
		return
	}
	ok(c, gin.H{"modes": a.inUseBillingModels(), "history": history})
}
