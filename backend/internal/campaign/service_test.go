package campaign

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/database"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
)

type recordingMailer struct {
	mu       sync.Mutex
	messages []Message
	err      error
}

func (m *recordingMailer) Send(_ context.Context, message Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, message)
	return m.err
}

func (m *recordingMailer) Messages() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	messages := make([]Message, len(m.messages))
	copy(messages, m.messages)
	return messages
}

func TestCreateSnapshotDeduplicatesSuppressesAndAssignsWaves(t *testing.T) {
	service, db := newCampaignTestService(t, nil)
	createdAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

	createCampaignTestUser(t, db, "alpha-first", "Alpha@QQ.COM ", true, true, createdAt)
	createCampaignTestUser(t, db, "alpha-duplicate", "alpha@qq.com", true, true, createdAt.Add(time.Second))
	createCampaignTestUser(t, db, "bravo", "bravo@qq.com", true, true, createdAt.Add(2*time.Second))
	createCampaignTestUser(t, db, "charlie", "charlie@qq.com", true, true, createdAt.Add(3*time.Second))
	createCampaignTestUser(t, db, "delta", "delta@qq.com", true, true, createdAt.Add(4*time.Second))
	createCampaignTestUser(t, db, "inactive", "inactive@qq.com", false, true, createdAt.Add(5*time.Second))
	createCampaignTestUser(t, db, "unverified", "unverified@qq.com", true, false, createdAt.Add(6*time.Second))
	createCampaignTestUser(t, db, "other-domain", "other@example.com", true, true, createdAt.Add(7*time.Second))

	if err := db.Create(&models.EmailSuppression{
		Email:  "CHARLIE@qq.com ",
		Status: models.EmailSuppressionStatusActive,
		Reason: models.EmailSuppressionReasonManual,
	}).Error; err != nil {
		t.Fatalf("create suppression: %v", err)
	}

	preview, err := service.PreviewSnapshot(context.Background(), "qq.com", 2)
	if err != nil {
		t.Fatalf("preview snapshot: %v", err)
	}
	if preview.EligibleCount != 3 || preview.SuppressedCount != 1 || preview.WaveCount != 2 || preview.WaveSize != 2 {
		t.Fatalf("unexpected preview: %#v", preview)
	}

	result, err := service.CreateSnapshot(context.Background(), campaignSnapshotSpec("snapshot-dedup", 2))
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	if result.RecipientCount != 3 || result.SuppressedCount != 1 || result.WaveCount != 2 || result.Existing {
		t.Fatalf("unexpected snapshot result: %#v", result)
	}

	var campaign models.EmailCampaign
	if err := db.Where("id = ?", result.CampaignID).First(&campaign).Error; err != nil {
		t.Fatalf("load campaign: %v", err)
	}
	if campaign.RecipientDomain != "qq.com" {
		t.Fatalf("recipient domain = %q, want qq.com", campaign.RecipientDomain)
	}

	var recipients []models.EmailCampaignRecipient
	if err := db.Where("campaign_id = ?", result.CampaignID).Order("wave ASC, created_at ASC, id ASC").Find(&recipients).Error; err != nil {
		t.Fatalf("load recipients: %v", err)
	}
	if len(recipients) != 3 {
		t.Fatalf("recipient count = %d, want 3", len(recipients))
	}

	gotWaves := make(map[string]int, len(recipients))
	for _, recipient := range recipients {
		if recipient.Status != models.EmailCampaignRecipientStatusPending {
			t.Fatalf("recipient %q status = %q, want pending", recipient.NormalizedEmail, recipient.Status)
		}
		if recipient.UnsubscribeTokenHash == "" {
			t.Fatalf("recipient %q is missing an unsubscribe token hash", recipient.NormalizedEmail)
		}
		gotWaves[recipient.NormalizedEmail] = recipient.Wave
	}
	wantWaves := map[string]int{"alpha@qq.com": 1, "bravo@qq.com": 1, "delta@qq.com": 2}
	if len(gotWaves) != len(wantWaves) {
		t.Fatalf("recipient mailbox set = %#v, want %#v", gotWaves, wantWaves)
	}
	for email, wave := range wantWaves {
		if gotWaves[email] != wave {
			t.Fatalf("recipient %q assigned wave %d, want %d", email, gotWaves[email], wave)
		}
	}

	existing, err := service.CreateSnapshot(context.Background(), campaignSnapshotSpec("snapshot-dedup", 2))
	if err != nil {
		t.Fatalf("repeat snapshot: %v", err)
	}
	if !existing.Existing || existing.CampaignID != result.CampaignID || existing.RecipientCount != 3 {
		t.Fatalf("repeat snapshot should return frozen campaign, got %#v", existing)
	}
	var count int64
	if err := db.Model(&models.EmailCampaignRecipient{}).Where("campaign_id = ?", result.CampaignID).Count(&count).Error; err != nil {
		t.Fatalf("count frozen recipients: %v", err)
	}
	if count != 3 {
		t.Fatalf("repeat snapshot inserted recipients: got %d, want 3", count)
	}
}

func TestSendWaveAcceptsEachRecipientOnlyOnce(t *testing.T) {
	mailer := &recordingMailer{}
	service, db := newCampaignTestService(t, mailer)
	createdAt := time.Date(2026, time.January, 3, 3, 4, 5, 0, time.UTC)
	createCampaignTestUser(t, db, "alpha", "alpha@qq.com", true, true, createdAt)
	createCampaignTestUser(t, db, "bravo", "bravo@qq.com", true, true, createdAt.Add(time.Second))
	createCampaignTestUser(t, db, "charlie", "charlie@qq.com", true, true, createdAt.Add(2*time.Second))

	snapshot, err := service.CreateSnapshot(context.Background(), campaignSnapshotSpec("send-once", 2))
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	if snapshot.RecipientCount != 3 || snapshot.WaveCount != 2 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}

	first, err := service.SendWave(context.Background(), "send-once", 1, 2)
	if err != nil {
		t.Fatalf("send first wave: %v", err)
	}
	if first.Claimed != 2 || first.Accepted != 2 || first.Unknown != 0 || first.Remaining != 0 {
		t.Fatalf("unexpected first-wave result: %#v", first)
	}
	assertCampaignMessages(t, mailer.Messages(), 2)

	repeat, err := service.SendWave(context.Background(), "send-once", 1, 2)
	if err != nil {
		t.Fatalf("repeat first wave: %v", err)
	}
	if repeat.Claimed != 0 || repeat.Accepted != 0 || repeat.Unknown != 0 || repeat.Remaining != 0 {
		t.Fatalf("repeat wave should not reclaim recipients: %#v", repeat)
	}
	assertCampaignMessages(t, mailer.Messages(), 2)

	second, err := service.SendWave(context.Background(), "send-once", 2, 2)
	if err != nil {
		t.Fatalf("send second wave: %v", err)
	}
	if second.Claimed != 1 || second.Accepted != 1 || second.Unknown != 0 || second.Remaining != 0 {
		t.Fatalf("unexpected second-wave result: %#v", second)
	}
	assertCampaignMessages(t, mailer.Messages(), 3)

	var recipients []models.EmailCampaignRecipient
	if err := db.Where("campaign_id = ?", snapshot.CampaignID).Find(&recipients).Error; err != nil {
		t.Fatalf("load recipients: %v", err)
	}
	if len(recipients) != 3 {
		t.Fatalf("recipient count = %d, want 3", len(recipients))
	}
	for _, recipient := range recipients {
		if recipient.Status != models.EmailCampaignRecipientStatusSMTPAccepted || recipient.AttemptCount != 1 || recipient.SMTPAcceptedAt == nil {
			t.Fatalf("recipient %q delivery state = %#v", recipient.NormalizedEmail, recipient)
		}
	}
}

func TestSendWaveLeavesUncertainDeliveryUnretryable(t *testing.T) {
	mailer := &recordingMailer{err: errors.New("simulated smtp transport failure")}
	service, db := newCampaignTestService(t, mailer)
	createCampaignTestUser(t, db, "alpha", "alpha@qq.com", true, true, time.Date(2026, time.January, 4, 3, 4, 5, 0, time.UTC))

	snapshot, err := service.CreateSnapshot(context.Background(), campaignSnapshotSpec("unknown-not-retried", 1))
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}

	first, err := service.SendWave(context.Background(), "unknown-not-retried", 1, 1)
	if err == nil {
		t.Fatal("expected uncertain SMTP result to be returned as an error")
	}
	if first.Claimed != 1 || first.Accepted != 0 || first.Unknown != 1 {
		t.Fatalf("unexpected uncertain-delivery result: %#v", first)
	}
	assertCampaignMessages(t, mailer.Messages(), 1)

	mailer.err = nil
	retry, err := service.SendWave(context.Background(), "unknown-not-retried", 1, 1)
	if err != nil {
		t.Fatalf("retry should not attempt an uncertain delivery again: %v", err)
	}
	if retry.Claimed != 0 || retry.Accepted != 0 || retry.Unknown != 0 || retry.Remaining != 0 {
		t.Fatalf("uncertain delivery must remain unclaimed on retry: %#v", retry)
	}
	assertCampaignMessages(t, mailer.Messages(), 1)

	var recipient models.EmailCampaignRecipient
	if err := db.Where("campaign_id = ?", snapshot.CampaignID).First(&recipient).Error; err != nil {
		t.Fatalf("load recipient: %v", err)
	}
	if recipient.Status != models.EmailCampaignRecipientStatusUnknown || recipient.AttemptCount != 1 || !strings.Contains(recipient.LastError, "simulated smtp transport failure") {
		t.Fatalf("uncertain recipient state = %#v", recipient)
	}
}

func newCampaignTestService(t *testing.T, mailer Mailer) (*Service, *gorm.DB) {
	t.Helper()
	db, err := database.Open(config.Config{DatabaseURL: "sqlite://file:campaign-service-" + uuid.NewString() + "?mode=memory&cache=shared"})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.EmailCampaign{}, &models.EmailCampaignRecipient{}, &models.EmailSuppression{}); err != nil {
		t.Fatalf("migrate campaign test database: %v", err)
	}
	service := NewService(db, config.Config{
		SecretKey:                  "campaign-test-secret-not-for-production",
		CampaignSMTPHost:           "smtp.example.test",
		CampaignSMTPPort:           465,
		CampaignSMTPUsername:       "test-user",
		CampaignSMTPPassword:       "test-password",
		CampaignSMTPFromEmail:      "updates@example.test",
		CampaignSMTPReplyTo:        "support@example.test",
		CampaignUnsubscribeBaseURL: "https://api.example.test",
		CampaignSMTPUseTLS:         true,
	}, mailer)
	service.now = func() time.Time { return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC) }
	return service, db
}

func createCampaignTestUser(t *testing.T, db *gorm.DB, username, email string, active, verified bool, createdAt time.Time) models.User {
	t.Helper()
	user := models.User{
		Base:         models.Base{ID: uuid.New(), CreatedAt: createdAt, UpdatedAt: createdAt},
		Username:     username,
		Email:        email,
		PasswordHash: "not-used-in-test",
		IsActive:     active,
		IsVerified:   verified,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user %q: %v", username, err)
	}
	// GORM applies the model's default true value to a false bool on Create.
	// Write both flags explicitly so the fixture covers inactive/unverified users.
	if err := db.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"is_active":   active,
		"is_verified": verified,
	}).Error; err != nil {
		t.Fatalf("set user state for %q: %v", username, err)
	}
	return user
}

func campaignSnapshotSpec(key string, waveSize int) SnapshotSpec {
	return SnapshotSpec{
		Key:          key,
		Name:         "Product update",
		Subject:      "NarratoAI update",
		TextTemplate: "A new NarratoAI version is available. {{unsubscribe_url}}",
		ConsentBasis: "verified test consent",
		Domain:       "qq.com",
		WaveSize:     waveSize,
	}
}

func assertCampaignMessages(t *testing.T, messages []Message, want int) {
	t.Helper()
	if len(messages) != want {
		t.Fatalf("mailer call count = %d, want %d", len(messages), want)
	}
	seenRecipients := make(map[string]struct{}, len(messages))
	seenMessageIDs := make(map[string]struct{}, len(messages))
	for _, message := range messages {
		if message.To == "" || message.MessageID == "" || message.UnsubscribeURL == "" {
			t.Fatalf("message is missing delivery metadata: %#v", message)
		}
		if !strings.Contains(message.Text, message.UnsubscribeURL) {
			t.Fatalf("message does not include its unsubscribe URL: %#v", message)
		}
		if _, duplicate := seenRecipients[message.To]; duplicate {
			t.Fatalf("recipient %q was handed to the mailer more than once", message.To)
		}
		if _, duplicate := seenMessageIDs[message.MessageID]; duplicate {
			t.Fatalf("message id %q was handed to the mailer more than once", message.MessageID)
		}
		seenRecipients[message.To] = struct{}{}
		seenMessageIDs[message.MessageID] = struct{}{}
	}
	addresses := make([]string, 0, len(seenRecipients))
	for address := range seenRecipients {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
}

func TestSendWaveSuppressesRecipientAddedToGlobalListAfterSnapshot(t *testing.T) {
	mailer := &recordingMailer{}
	service, db := newCampaignTestService(t, mailer)
	user := createCampaignTestUser(t, db, "alpha", "alpha@qq.com", true, true, time.Date(2026, time.January, 5, 3, 4, 5, 0, time.UTC))

	snapshot, err := service.CreateSnapshot(context.Background(), campaignSnapshotSpec("suppression-after-snapshot", 1))
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	if err := db.Create(&models.EmailSuppression{
		UserID: &user.ID,
		Email:  "ALPHA@qq.com",
		Status: models.EmailSuppressionStatusActive,
		Reason: models.EmailSuppressionReasonUnsubscribed,
	}).Error; err != nil {
		t.Fatalf("add suppression after snapshot: %v", err)
	}

	result, err := service.SendWave(context.Background(), snapshot.CampaignKey, 1, 1)
	if err != nil {
		t.Fatalf("send wave after suppression: %v", err)
	}
	if result.Claimed != 0 || result.Accepted != 0 || result.Unknown != 0 || result.Remaining != 0 {
		t.Fatalf("suppressed recipient must not be sent: %#v", result)
	}
	assertCampaignMessages(t, mailer.Messages(), 0)

	var recipient models.EmailCampaignRecipient
	if err := db.Where("campaign_id = ?", snapshot.CampaignID).First(&recipient).Error; err != nil {
		t.Fatalf("load recipient: %v", err)
	}
	if recipient.Status != models.EmailCampaignRecipientStatusSuppressed {
		t.Fatalf("recipient status = %q, want suppressed", recipient.Status)
	}
	if recipient.AttemptCount != 0 {
		t.Fatalf("suppressed recipient attempt count = %d, want 0", recipient.AttemptCount)
	}
}

func TestSendWaveRefusesCancelledCampaign(t *testing.T) {
	mailer := &recordingMailer{}
	service, db := newCampaignTestService(t, mailer)
	createCampaignTestUser(t, db, "alpha", "alpha@qq.com", true, true, time.Date(2026, time.January, 6, 3, 4, 5, 0, time.UTC))

	snapshot, err := service.CreateSnapshot(context.Background(), campaignSnapshotSpec("cancelled-campaign", 1))
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	if err := db.Model(&models.EmailCampaign{}).Where("id = ?", snapshot.CampaignID).Update("status", models.EmailCampaignStatusCancelled).Error; err != nil {
		t.Fatalf("cancel campaign: %v", err)
	}

	result, err := service.SendWave(context.Background(), snapshot.CampaignKey, 1, 1)
	if err == nil || !strings.Contains(err.Error(), "cannot be sent") {
		t.Fatalf("cancelled campaign should be rejected, result=%#v err=%v", result, err)
	}
	assertCampaignMessages(t, mailer.Messages(), 0)

	var recipient models.EmailCampaignRecipient
	if err := db.Where("campaign_id = ?", snapshot.CampaignID).First(&recipient).Error; err != nil {
		t.Fatalf("load recipient: %v", err)
	}
	if recipient.Status != models.EmailCampaignRecipientStatusPending || recipient.AttemptCount != 0 {
		t.Fatalf("cancelled campaign changed recipient state: %#v", recipient)
	}
}

func TestCreateSnapshotRejectsWildcardLikeDomainWithoutCreatingCampaign(t *testing.T) {
	service, db := newCampaignTestService(t, nil)
	createCampaignTestUser(t, db, "alpha", "alpha@qq.com", true, true, time.Date(2026, time.January, 7, 3, 4, 5, 0, time.UTC))

	spec := campaignSnapshotSpec("invalid-domain", 1)
	spec.Domain = "qq%.com"
	if _, err := service.CreateSnapshot(context.Background(), spec); err == nil {
		t.Fatal("expected wildcard-like recipient domain to be rejected")
	}

	var campaignCount int64
	if err := db.Model(&models.EmailCampaign{}).Count(&campaignCount).Error; err != nil {
		t.Fatalf("count campaigns: %v", err)
	}
	if campaignCount != 0 {
		t.Fatalf("invalid domain created %d campaign records, want 0", campaignCount)
	}
}
