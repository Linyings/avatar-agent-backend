package server

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/narratoai/backend-go/internal/models"
	"math"
	"strings"
)

func (a *App) billingModeAvailability(c *gin.Context) {
	result := map[string]any{}
	for _, mode := range []string{"general", "dubbing", "movie"} {
		free, _ := a.billingModeConfig(mode)["free_experience"].(bool)
		result[mode] = gin.H{"free_experience": free}
	}
	ok(c, result)
}

// Mode is resolved from the authenticated workflow/operation, never a free flag
// supplied by the renderer. Unregistered operations retain normal billing.
// Desktop movie projects tag every paid call with their project id; those calls use
// 电影模式 rates, which fall back to 通用模式 rates until the admin sets their own.
func consumptionMode(c creditConsumption) string {
	if c.BillingMode != "" {
		return c.BillingMode
	}
	if c.DubbingTTS || c.ServiceKind == "audio_separation" || c.ServiceKind == "dubbing_asr" {
		return "dubbing"
	}
	if id, _ := c.RequestMetadata["movie_project_id"].(string); id != "" {
		return "movie"
	}
	return "general"
}

func (a *App) billingModeConfig(mode string) map[string]any {
	var setting models.SystemSetting
	if a.db.First(&setting, "key = ?", "billing_rates").Error != nil {
		return nil
	}
	var root map[string]any
	if json.Unmarshal(setting.Value, &root) != nil {
		return nil
	}
	if mode == "general" {
		return root
	}
	modes, _ := root["modes"].(map[string]any)
	config, _ := modes[mode].(map[string]any)
	return config
}

func (a *App) modeBillingRate(mode, provider, kind, model string) (billingRate, bool, bool) {
	rate, _, found, free := a.matchModeBillingRate(mode, provider, kind, model)
	return rate, found, free
}

// matchModeBillingRate also reports where the rule came from: the mode name for a
// mode-specific rule, "general" for a stored general rule, or "builtin".
func (a *App) matchModeBillingRate(mode, provider, kind, model string) (rate billingRate, source string, found bool, free bool) {
	config := a.billingModeConfig(mode)
	free, _ = config["free_experience"].(bool)
	if mode != "general" && config != nil {
		if rate, found := modeConfigBillingRate(config, provider, kind, model, true); found {
			return rate, mode, true, free
		}
	}
	// 电影模式 can narrate with 搞笑配音 voices; until it has its own rule they keep the 配音 price.
	if mode == "movie" {
		if dubbing := a.billingModeConfig("dubbing"); dubbing != nil {
			if rate, found := modeConfigBillingRate(dubbing, provider, kind, model, false); found {
				return rate, "dubbing", true, free
			}
		}
	}
	if rate, found := a.findStoredBillingRate(provider, kind, model); found {
		return rate, "general", true, free
	}
	if rate, found := a.builtinBillingRate(provider, kind, model); found {
		return rate, "builtin", true, free
	}
	return billingRate{}, "", false, free
}

func modeConfigBillingRate(config map[string]any, provider, kind, model string, wildcard bool) (billingRate, bool) {
	var rates []billingRate
	collectBillingRates(config, "", &rates)
	provider = normalizeBillingProvider(provider)
	for _, rate := range rates {
		if rate.ServiceKind == kind && (rate.Provider == "" || rate.Provider == provider) && billingModelIDMatches(rate.ModelID, model) {
			return rate, true
		}
	}
	if wildcard {
		for _, rate := range rates {
			if rate.ServiceKind == kind && (rate.Provider == "" || rate.Provider == provider) && (rate.ModelID == "" || rate.ModelID == "*") {
				return rate, true
			}
		}
	}
	return billingRate{}, false
}

func validateModeBillingConfig(value any) error {
	root, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("价格配置必须为对象")
	}
	var validate func(map[string]any) error
	validate = func(config map[string]any) error {
		if flag, exists := config["free_experience"]; exists {
			if _, ok := flag.(bool); !ok {
				return fmt.Errorf("免费体验开关必须为布尔值")
			}
		}
		if p, ok := config["pricing_model"].(map[string]any); ok {
			for _, key := range []string{"creditCostCny", "creditSalePriceCny", "exchangeRate"} {
				if n, exists := p[key]; exists {
					v, ok := n.(float64)
					if !ok || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
						return fmt.Errorf("%s 必须大于零", key)
					}
				}
			}
		}
		var rates []billingRate
		collectBillingRates(config, "", &rates)
		for _, rate := range rates {
			if strings.TrimSpace(rate.Provider) == "" && rate.ModelID != "*" && rate.ModelID != "" {
				return fmt.Errorf("价格规则必须填写供应商")
			}
			for _, v := range []float64{rate.InputCostPerMillion, rate.OutputCostPerMillion, rate.CostPerMinute, rate.CostPerMillionChars, rate.CostPerMillionUTF8, rate.InputTokensPerCredit, rate.OutputTokensPerCredit, rate.CreditsPerMinute, rate.CreditsPerChar, rate.CreditsPerUTF8Byte} {
				if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
					return fmt.Errorf("价格和费率不能为负数")
				}
			}
		}
		return nil
	}
	if err := validate(root); err != nil {
		return err
	}
	if modes, exists := root["modes"]; exists {
		items, ok := modes.(map[string]any)
		if !ok {
			return fmt.Errorf("模式配置必须为对象")
		}
		for mode, item := range items {
			config, ok := item.(map[string]any)
			if !ok || mode == "" || mode == "general" {
				return fmt.Errorf("模式配置无效")
			}
			if err := validate(config); err != nil {
				return err
			}
		}
	}
	return nil
}
