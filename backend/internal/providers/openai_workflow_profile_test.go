package providers

import (
	"github.com/narratoai/backend-go/internal/config"
	"testing"
)

func TestWorkflowProfileFingerprintUsesResolvedModelNotCredentials(t *testing.T) {
	cfg := config.Config{OpenAIProAPIBaseURL: "https://provider.test/v1", OpenAIProAPIKey: "first-secret", OpenAIProModel: "deepseek-test", OpenAIFlashModel: "flash-test"}
	req := map[string]any{"model_profile": "pro", "model_operation": "narration_outline"}
	fingerprint := func(c config.Config) string {
		return NewOpenAIClient(c, nil).WorkflowProfileFingerprint("script_draft", req)
	}
	base := fingerprint(cfg)
	cfg.OpenAIProAPIKey = "rotated-secret"
	if fingerprint(cfg) != base {
		t.Fatal("credential rotation invalidated paid work")
	}
	cfg.OpenAIFlashModel = "unused-model"
	if fingerprint(cfg) != base {
		t.Fatal("unselected profile invalidated paid work")
	}
	for _, change := range []func(*config.Config){
		func(c *config.Config) { c.OpenAIProModel = "glm-test" },
		func(c *config.Config) { c.OpenAIProAPIBaseURL = "https://another.test/v1" },
		func(c *config.Config) { c.OpenAIProThinkingType = "enabled" },
		func(c *config.Config) { c.OpenAITemperature = .8 },
	} {
		changed := cfg
		change(&changed)
		if fingerprint(changed) == base {
			t.Fatal("effective execution change was ignored")
		}
	}
}
