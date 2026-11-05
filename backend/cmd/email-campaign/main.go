package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/narratoai/backend-go/internal/campaign"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/database"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
)

const defaultCampaignTemplate = `你好，

NarratoAI 最新版已经上线，现在可以直接下载使用。

本次更新重点包括：

- 软件内部模型已升级，推理能力有所提升
- 剪辑流程进一步优化
- 解说文案与画面的匹配更准确，成片观感更自然

欢迎下载最新版体验：

官网与下载入口：https://www.narratoai.cn

如不希望继续收到产品更新邮件，请点击取消订阅：
{{unsubscribe_url}}

NarratoAI 团队`

type campaignStatusRow struct {
	Wave   int
	Status string
	Count  int64
}

func main() {
	action := flag.String("action", "preview", "preview, snapshot, status, or send")
	campaignKey := flag.String("campaign-key", "narratoai-latest-update-2026-09-03", "immutable campaign key")
	campaignName := flag.String("campaign-name", "NarratoAI 最新版本更新通知", "campaign name")
	subject := flag.String("subject", "NarratoAI 最新版已上线，模型与剪辑效果已升级", "email subject")
	bodyFile := flag.String("body-file", "", "optional UTF-8 text template containing {{unsubscribe_url}}")
	consentBasis := flag.String("consent-basis", "", "required record of the audience consent basis")
	domain := flag.String("domain", "qq.com", "recipient email domain")
	waveSize := flag.Int("wave-size", 2000, "recipients per wave")
	wave := flag.Int("wave", 1, "wave to send")
	limit := flag.Int("limit", 2000, "maximum recipients to claim in this send run")
	apply := flag.Bool("apply", false, "allow a snapshot write or SMTP delivery")
	flag.Parse()

	cfg := config.Load()
	db, err := database.Open(cfg)
	if err != nil {
		log.Fatalf("database open failed: %v", err)
	}
	if cfg.AutoMigrate {
		if err := database.AutoMigrate(db); err != nil {
			log.Fatalf("database migration failed: %v", err)
		}
	}
	service := campaign.NewService(db, cfg, nil)
	ctx := context.Background()

	switch strings.ToLower(strings.TrimSpace(*action)) {
	case "preview":
		result, err := service.PreviewSnapshot(ctx, *domain, *waveSize)
		if err != nil {
			log.Fatal(err)
		}
		printJSON(result)
	case "snapshot":
		requireApply(*apply, "snapshot")
		requireConsent(*consentBasis)
		body, err := loadBody(*bodyFile)
		if err != nil {
			log.Fatal(err)
		}
		result, err := service.CreateSnapshot(ctx, campaign.SnapshotSpec{
			Key:          *campaignKey,
			Name:         *campaignName,
			Subject:      *subject,
			TextTemplate: body,
			ConsentBasis: *consentBasis,
			Domain:       *domain,
			WaveSize:     *waveSize,
		})
		if err != nil {
			log.Fatal(err)
		}
		printJSON(result)
	case "status":
		printCampaignStatus(db, *campaignKey)
	case "send":
		requireApply(*apply, "send")
		result, err := service.SendWave(ctx, *campaignKey, *wave, *limit)
		if err != nil {
			printJSON(result)
			log.Fatal(err)
		}
		printJSON(result)
	default:
		log.Fatalf("unsupported action %q", *action)
	}
}

func loadBody(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return defaultCampaignTemplate, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	body := strings.TrimSpace(string(content))
	if body == "" {
		return "", fmt.Errorf("campaign body file is empty")
	}
	return body, nil
}

func printCampaignStatus(db *gorm.DB, campaignKey string) {
	var campaign models.EmailCampaign
	if err := db.Where("key = ?", strings.TrimSpace(campaignKey)).First(&campaign).Error; err != nil {
		log.Fatal(err)
	}

	var rows []campaignStatusRow
	if err := db.Model(&models.EmailCampaignRecipient{}).
		Select("wave, status, COUNT(*) AS count").
		Where("campaign_id = ?", campaign.ID).
		Group("wave, status").
		Order("wave ASC, status ASC").
		Scan(&rows).Error; err != nil {
		log.Fatal(err)
	}

	var total int64
	for _, row := range rows {
		total += row.Count
	}
	printJSON(struct {
		CampaignKey string              `json:"campaign_key"`
		Status      string              `json:"status"`
		Total       int64               `json:"total"`
		Waves       []campaignStatusRow `json:"waves"`
	}{
		CampaignKey: campaign.Key,
		Status:      campaign.Status,
		Total:       total,
		Waves:       rows,
	})
}

func requireApply(apply bool, action string) {
	if !apply {
		log.Fatalf("%s is a state-changing operation; rerun with -apply after reviewing the preview", action)
	}
}

func requireConsent(value string) {
	if strings.TrimSpace(value) == "" {
		log.Fatal("-consent-basis is required to create a recipient snapshot")
	}
}

func printJSON(value any) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(encoded))
}
