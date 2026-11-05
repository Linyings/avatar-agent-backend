package database

import (
	"testing"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/models"
)

func TestEmailCampaignPersistenceMigrationAndUniqueAudience(t *testing.T) {
	db, err := Open(config.Config{DatabaseURL: "sqlite://file:email-campaign-persistence?mode=memory&cache=shared"})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}

	migrator := db.Migrator()
	for _, model := range []any{
		&models.EmailCampaign{},
		&models.EmailCampaignRecipient{},
		&models.EmailSuppression{},
	} {
		if !migrator.HasTable(model) {
			t.Fatalf("missing migrated table for %T", model)
		}
	}
	for _, index := range []string{
		"uq_email_campaign_recipients_campaign_user",
		"uq_email_campaign_recipients_campaign_normalized_email",
		"uq_email_campaign_recipients_unsubscribe_token_hash",
	} {
		if !migrator.HasIndex(&models.EmailCampaignRecipient{}, index) {
			t.Fatalf("email_campaign_recipients missing %s", index)
		}
	}
	if !migrator.HasIndex(&models.EmailSuppression{}, "uq_email_suppressions_normalized_email") {
		t.Fatal("email_suppressions missing normalized email uniqueness index")
	}

	campaign := models.EmailCampaign{
		Key:          "release-0-2-12",
		Name:         "release announcement",
		Subject:      "NarratoAI latest release",
		TextTemplate: "Download the newest version.",
		ConsentBasis: "registered users opted into product updates",
	}
	if err := db.Create(&campaign).Error; err != nil {
		t.Fatalf("create campaign: %v", err)
	}

	userID := uuid.New()
	first := models.EmailCampaignRecipient{
		CampaignID:           campaign.ID,
		UserID:               userID,
		Email:                "Example@QQ.COM ",
		UnsubscribeTokenHash: "unsubscribe-token-one",
	}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first recipient: %v", err)
	}
	if first.NormalizedEmail != "example@qq.com" || first.Status != models.EmailCampaignRecipientStatusPending {
		t.Fatalf("recipient normalization/status = %q/%q", first.NormalizedEmail, first.Status)
	}

	if err := db.Create(&models.EmailCampaignRecipient{
		CampaignID:           campaign.ID,
		UserID:               userID,
		Email:                "different@qq.com",
		UnsubscribeTokenHash: "unsubscribe-token-two",
	}).Error; err == nil {
		t.Fatal("expected campaign and user uniqueness violation")
	}
	if err := db.Create(&models.EmailCampaignRecipient{
		CampaignID:           campaign.ID,
		UserID:               uuid.New(),
		Email:                "EXAMPLE@qq.com",
		UnsubscribeTokenHash: "unsubscribe-token-three",
	}).Error; err == nil {
		t.Fatal("expected campaign and normalized email uniqueness violation")
	}

	secondCampaign := models.EmailCampaign{
		Key:          "release-0-2-13",
		Name:         "next release announcement",
		Subject:      "NarratoAI next release",
		TextTemplate: "Download the next version.",
		ConsentBasis: "registered users opted into product updates",
	}
	if err := db.Create(&secondCampaign).Error; err != nil {
		t.Fatalf("create second campaign: %v", err)
	}
	if err := db.Create(&models.EmailCampaignRecipient{
		CampaignID:           secondCampaign.ID,
		UserID:               userID,
		Email:                "example@qq.com",
		UnsubscribeTokenHash: "unsubscribe-token-four",
	}).Error; err != nil {
		t.Fatalf("same audience member should be allowed in another campaign: %v", err)
	}

	firstSuppression := models.EmailSuppression{
		Email:  "OptOut@QQ.COM ",
		Reason: models.EmailSuppressionReasonUnsubscribed,
	}
	if err := db.Create(&firstSuppression).Error; err != nil {
		t.Fatalf("create suppression: %v", err)
	}
	if firstSuppression.NormalizedEmail != "optout@qq.com" || firstSuppression.Status != models.EmailSuppressionStatusActive {
		t.Fatalf("suppression normalization/status = %q/%q", firstSuppression.NormalizedEmail, firstSuppression.Status)
	}
	if err := db.Create(&models.EmailSuppression{
		Email:  "OPTOUT@qq.com",
		Reason: models.EmailSuppressionReasonManual,
	}).Error; err == nil {
		t.Fatal("expected normalized suppression email uniqueness violation")
	}
}
