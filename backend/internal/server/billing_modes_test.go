package server

import (
	"context"
	"encoding/json"
	"github.com/narratoai/backend-go/internal/models"
	"testing"
	"time"
)

func TestModePromotionPreservesCostsAndDoesNotSpendCredits(t *testing.T) {
	app, user, run, step := newWorkflowRunBillingFixture(t, "mode-free@example.com", nil)
	setting := map[string]any{
		"llm":   []map[string]any{{"provider": "openai", "model_id": "*", "tokens_per_credit": 1, "input_cost_per_million": 2}},
		"modes": map[string]any{"dubbing": map[string]any{"free_experience": true, "tts": []map[string]any{{"provider": "dashscope", "model_id": "qwen3-tts-instruct-flash", "credits_per_1k_chars": 30, "cost_per_million_chars": 90}}}},
	}
	save := func() {
		t.Helper()
		if err := app.db.Model(&models.SystemSetting{}).Where("key = ?", "billing_rates").Update("value", jsonMap(setting)).Error; err != nil {
			t.Fatal(err)
		}
	}
	save()
	c := workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 0)
	c.BillingMode = "dubbing"
	c.Provider = "dashscope"
	c.ServiceKind = "tts"
	c.ModelID = "qwen3-tts-instruct-flash"
	c.CharacterCount = 1000
	amounts, err := app.billingAmountsForConsumption(c)
	if err != nil || amounts.CreditCount != 0 || amounts.CostAmount != 0.09 {
		t.Fatal(amounts, err)
	}
	if err := app.db.Model(&models.UserTokenAccount{}).Where("user_id = ?", user.ID).Updates(map[string]any{"total_tokens": 0, "used_tokens": 0}).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.billCredits(c); err != nil {
		t.Fatal(err)
	}
	// Turning promotion off affects new operations, not a replay of an existing one.
	setting["modes"].(map[string]any)["dubbing"].(map[string]any)["free_experience"] = false
	save()
	paid, err := app.billingAmountsForConsumption(c)
	if err != nil || paid.CreditCount != 30 {
		t.Fatal(paid, err)
	}
	if err := app.billCredits(c); err != nil {
		t.Fatal(err)
	}
	if _, err := app.WorkflowRuns().Complete(context.Background(), user.ID, run.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var records []models.TokenUsageRecord
	app.db.Where("user_id = ?", user.ID).Find(&records)
	if len(records) != 1 || records[0].CreditCount != 0 || floatValueFromPtr(records[0].CostAmount) != 0.09 || floatValueFromPtr(records[0].RevenueAmount) != 0 {
		t.Fatalf("records: %+v", records)
	}
	var metadata map[string]any
	json.Unmarshal(records[0].RequestMetadata, &metadata)
	if metadata["product_mode"] != "dubbing" || metadata["free_experience"] != true {
		t.Fatal(metadata)
	}
	var account models.UserTokenAccount
	app.db.Where("user_id = ?", user.ID).First(&account)
	if account.UsedTokens != 0 {
		t.Fatal("free operation spent credits")
	}
	general := workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10)
	// Exact fixture model/provider: a missing general rule must not inherit free mode.
	general.Provider = "openai"
	general.ModelID = "test"
	a, err := app.billingAmountsForConsumption(general)
	if err != nil || a.Promotion || a.CreditCount != 10 {
		t.Fatal(a, err)
	}
}

func TestModeRatesDoNotLeakAndOverrideLegacyDefaults(t *testing.T) {
	var legacy map[string]any
	json.Unmarshal([]byte(`{"llm":[{"provider":"","model_id":"*","tokens_per_credit":1000}],"free_experience":true}`), &legacy)
	if err := validateModeBillingConfig(legacy); err != nil {
		t.Fatalf("legacy wildcard provider must remain valid: %v", err)
	}
	app, _, _, _ := newWorkflowRunBillingFixture(t, "mode-rates@example.com", nil)
	raw := map[string]any{"tts": []map[string]any{{"provider": "dashscope", "model_id": "qwen3-tts-instruct-flash", "credits_per_1k_chars": 12}}, "modes": map[string]any{"dubbing": map[string]any{"free_experience": true, "tts": []map[string]any{{"provider": "dashscope", "model_id": "qwen3-tts-instruct-flash", "credits_per_1k_chars": 33}}}}}
	app.db.Model(&models.SystemSetting{}).Where("key = ?", "billing_rates").Update("value", jsonMap(raw))
	r, ok := app.findBillingRate("dashscope", "tts", "qwen3-tts-instruct-flash")
	if !ok || r.CreditsPerChar != 0.012 {
		t.Fatal(r)
	}
	r, ok, free := app.modeBillingRate("dubbing", "dashscope", "tts", "qwen3-tts-instruct-flash")
	if !ok || !free || r.CreditsPerChar != 0.033 {
		t.Fatal(r)
	}
	if validateModeBillingConfig(map[string]any{"free_experience": "true"}) == nil {
		t.Fatal("accepted string flag")
	}
}
