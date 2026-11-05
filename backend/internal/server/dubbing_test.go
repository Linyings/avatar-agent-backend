package server

import (
	"context"
	"encoding/json"
	"github.com/narratoai/backend-go/internal/models"
	"math"
	"testing"
	"time"
)

func TestDubbingPricingMarginAndTTSIsolation(t *testing.T) {
	app, user, run, step := newWorkflowRunBillingFixture(t, "dubbing-margin@example.com", nil)
	app.cfg.DubbingTTSCreditsMultiplier = 1.7
	app.cfg.DubbingASRModel = "paraformer-v2"
	app.cfg.DubbingASRCreditsPerMinute = 2
	app.cfg.DubbingSeparationCreditsPerMinute = 34
	if err := app.db.Model(&models.SystemSetting{}).Where("key = ?", "billing_rates").Update("value", jsonMap(map[string]any{"tts": []map[string]any{
		{"provider": "siliconflow", "model_id": "*", "credits_per_1k_chars": 10, "cost_per_million_chars": 50},
		{"provider": "fishaudio", "model_id": "*", "credits_per_1k_chars": 20.38, "cost_per_million_chars": 101.9},
	}})).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		provider          string
		original, dubbing int64
		cost              float64
	}{
		{"siliconflow", 10, 17, 0.05}, {"fishaudio", 21, 35, 0.1019},
	} {
		usage := creditConsumption{UserID: user.ID, Provider: tc.provider, ServiceKind: "tts", ModelID: "test", CharacterCount: 1000}
		for _, mode := range []struct {
			client, step string
			want         int64
		}{
			{"pc", "dubbing-tts-test", tc.dubbing}, {"pc", "generate-voiceover", tc.original}, {"web", "dubbing-tts-test", tc.original},
		} {
			run.ClientType = mode.client
			step.StepID = mode.step
			execution := workflowRequestExecution{Identity: &workflowRequestIdentity{}, Run: run, Step: step}
			amounts, err := app.billingAmountsForConsumption(execution.EnrichCreditConsumption(usage))
			if err != nil || amounts.CreditCount != mode.want || math.Abs(amounts.CostAmount-tc.cost) > 0.000001 {
				t.Fatalf("%s/%s: %+v %v", tc.provider, mode.step, amounts, err)
			}
		}
	}
	duration := 300.0
	for _, tc := range []struct {
		provider, kind, model string
		want                  int64
	}{
		{"dashscope", "dubbing_asr", "paraformer-v2", 10}, {"aliyunmps", "audio_separation", "MusicDemix", 170},
	} {
		amounts, err := app.billingAmountsForConsumption(creditConsumption{Provider: tc.provider, ServiceKind: tc.kind, ModelID: tc.model, DurationSeconds: &duration})
		if err != nil || amounts.CreditCount != tc.want {
			t.Fatal(amounts, err)
		}
		margin := 1 - amounts.CostAmount/(float64(amounts.CreditCount)*29.9/3000)
		if margin < 0.7 {
			t.Fatal("margin below target", margin)
		}
	}
}

func TestDubbingResultRejectsOtherUserOutput(t *testing.T) {
	build := func(file string) map[string]any {
		data, _ := json.Marshal(map[string]any{"result": []map[string]string{{"file": file}}})
		details, _ := json.Marshal(map[string]any{"Code": "Success", "Duration": 3, "Data": string(data)})
		return map[string]any{"Result": string(details)}
	}
	prefix := "dubbing/user/hash/"
	if d, key, err := parseDubbingResult(build(prefix+"output-accompaniment.wav"), prefix); err != nil || d != 3 || key == "" {
		t.Fatal(d, key, err)
	}
	if _, _, err := parseDubbingResult(build("dubbing/other/hash/output-accompaniment.wav"), prefix); err == nil {
		t.Fatal("accepted another user's output")
	}
	if _, _, err := parseDubbingResult(map[string]any{"Result": "{}"}, prefix); err == nil {
		t.Fatal("accepted missing duration")
	}
}

func TestDubbingSettlementIsIdempotent(t *testing.T) {
	app, user, run, step := newWorkflowRunBillingFixture(t, "dubbing-billing@example.com", nil)
	app.cfg.DubbingSeparationCreditsPerMinute = 20
	duration := 3.0
	usage := creditConsumption{UserID: user.ID, WorkflowRunID: &run.ID, WorkflowStepExecutionID: &step.ID,
		ExecutionKey: step.ExecutionKey, Provider: "aliyun-mps", ServiceKind: "audio_separation", ModelID: "MusicDemix",
		APIEndpoint: "/api/v1/tools/dubbing/separation/query", DurationSeconds: &duration}
	for i := 0; i < 3; i++ {
		if err := app.billCredits(usage); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := app.db.Model(&models.CreditReservation{}).Where("workflow_run_id = ?", run.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal(count, err)
	}
	for i := 0; i < 2; i++ {
		if _, err := app.WorkflowRuns().Complete(context.Background(), user.ID, run.ID, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	assertWorkflowBillingCounts(t, app, user.ID, 1, 1, 1)
}
func TestDubbingBillingUsesSecondsRatherThanMinuteMinimum(t *testing.T) {
	duration := 3.0
	usage := creditConsumption{ServiceKind: "audio_separation", DurationSeconds: &duration}
	rate := billingRate{CreditsPerMinute: 20, CostPerMinute: 0.1}
	credits, err := deriveCreditCount(usage, rate)
	if err != nil || credits != 1 {
		t.Fatalf("credits=%v err=%v", credits, err)
	}
	cost, missing := calculateProviderCost(usage, rate)
	if missing || cost != 0.005 {
		t.Fatalf("cost=%v missing=%v", cost, missing)
	}
	// 语音转写 is also billed by the second now: 3 s × 20 / 60 = 1 credit, not a full minute.
	usage.ServiceKind = "asr"
	credits, err = deriveCreditCount(usage, rate)
	if err != nil || credits != 1 {
		t.Fatal("ASR should bill by the second", credits, err)
	}
}
