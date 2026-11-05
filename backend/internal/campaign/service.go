package campaign

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
)

const unsubscribePlaceholder = "{{unsubscribe_url}}"

type SnapshotSpec struct {
	Key          string
	Name         string
	Subject      string
	TextTemplate string
	ConsentBasis string
	Domain       string
	WaveSize     int
}

type SnapshotPreview struct {
	EligibleCount   int
	SuppressedCount int
	WaveCount       int
	WaveSize        int
}

type SnapshotResult struct {
	CampaignID      uuid.UUID
	CampaignKey     string
	RecipientCount  int
	SuppressedCount int
	WaveCount       int
	Existing        bool
}

type SendResult struct {
	CampaignKey string
	Wave        int
	Claimed     int
	Accepted    int
	Unknown     int
	Remaining   int
}

type Message struct {
	To             string
	Subject        string
	Text           string
	MessageID      string
	UnsubscribeURL string
}

type Mailer interface {
	Send(context.Context, Message) error
}

type Service struct {
	db     *gorm.DB
	cfg    config.Config
	mailer Mailer
	now    func() time.Time
}

func NewService(db *gorm.DB, cfg config.Config, mailer Mailer) *Service {
	return &Service{db: db, cfg: cfg, mailer: mailer, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) PreviewSnapshot(ctx context.Context, domain string, waveSize int) (SnapshotPreview, error) {
	if waveSize <= 0 {
		waveSize = 2000
	}
	users, suppressed, err := s.eligibleUsers(ctx, domain)
	if err != nil {
		return SnapshotPreview{}, err
	}
	waves := 0
	if len(users) > 0 {
		waves = (len(users) + waveSize - 1) / waveSize
	}
	return SnapshotPreview{EligibleCount: len(users), SuppressedCount: suppressed, WaveCount: waves, WaveSize: waveSize}, nil
}

func (s *Service) CreateSnapshot(ctx context.Context, spec SnapshotSpec) (SnapshotResult, error) {
	if strings.TrimSpace(s.cfg.SecretKey) == "" || s.cfg.SecretKey == "change-me-change-me-change-me-change-me" {
		return SnapshotResult{}, errors.New("a non-default SECRET_KEY is required for campaign unsubscribe tokens")
	}
	spec, err := validateSnapshotSpec(spec)
	if err != nil {
		return SnapshotResult{}, err
	}

	var existing models.EmailCampaign
	err = s.db.WithContext(ctx).Where("key = ?", spec.Key).First(&existing).Error
	if err == nil {
		var count int64
		if countErr := s.db.WithContext(ctx).Model(&models.EmailCampaignRecipient{}).Where("campaign_id = ?", existing.ID).Count(&count).Error; countErr != nil {
			return SnapshotResult{}, countErr
		}
		waves := 0
		if count > 0 {
			waves = int((count + int64(existing.WaveSize) - 1) / int64(existing.WaveSize))
		}
		return SnapshotResult{CampaignID: existing.ID, CampaignKey: existing.Key, RecipientCount: int(count), WaveCount: waves, Existing: true}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return SnapshotResult{}, err
	}

	users, suppressedCount, err := s.eligibleUsers(ctx, spec.Domain)
	if err != nil {
		return SnapshotResult{}, err
	}
	if len(users) == 0 {
		return SnapshotResult{}, errors.New("no eligible recipients for campaign snapshot")
	}

	now := s.now()
	campaign := models.EmailCampaign{
		Key:             spec.Key,
		Name:            spec.Name,
		Subject:         spec.Subject,
		TextTemplate:    spec.TextTemplate,
		ConsentBasis:    spec.ConsentBasis,
		RecipientDomain: spec.Domain,
		Status:          models.EmailCampaignStatusReady,
		SnapshotAt:      &now,
		WaveSize:        spec.WaveSize,
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&campaign).Error; err != nil {
			return err
		}
		for index, user := range users {
			recipientID := uuid.New()
			token, err := s.unsubscribeToken(recipientID)
			if err != nil {
				return err
			}
			recipient := models.EmailCampaignRecipient{
				Base:                 models.Base{ID: recipientID},
				CampaignID:           campaign.ID,
				UserID:               user.ID,
				Email:                user.Email,
				NormalizedEmail:      normalizeEmail(user.Email),
				UnsubscribeTokenHash: hashToken(token),
				Status:               models.EmailCampaignRecipientStatusPending,
				Wave:                 index/spec.WaveSize + 1,
			}
			if err := tx.Create(&recipient).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return SnapshotResult{}, err
	}

	waves := (len(users) + spec.WaveSize - 1) / spec.WaveSize
	return SnapshotResult{
		CampaignID: campaign.ID, CampaignKey: campaign.Key, RecipientCount: len(users),
		SuppressedCount: suppressedCount, WaveCount: waves,
	}, nil
}

func (s *Service) SendWave(ctx context.Context, campaignKey string, wave, limit int) (SendResult, error) {
	campaignKey = strings.TrimSpace(campaignKey)
	if campaignKey == "" || wave < 1 {
		return SendResult{}, errors.New("campaign key and wave are required")
	}
	if !s.cfg.CampaignSMTPConfigured() {
		return SendResult{}, errors.New("campaign SMTP is not configured")
	}
	if strings.TrimSpace(s.cfg.SecretKey) == "" || s.cfg.SecretKey == "change-me-change-me-change-me-change-me" {
		return SendResult{}, errors.New("a non-default SECRET_KEY is required for campaign unsubscribe tokens")
	}

	var campaign models.EmailCampaign
	if err := s.db.WithContext(ctx).Where("key = ?", campaignKey).First(&campaign).Error; err != nil {
		return SendResult{}, err
	}
	if campaign.Status != models.EmailCampaignStatusReady && campaign.Status != models.EmailCampaignStatusSending {
		return SendResult{}, fmt.Errorf("campaign %q is %s and cannot be sent", campaign.Key, campaign.Status)
	}
	if limit <= 0 || limit > campaign.WaveSize {
		limit = campaign.WaveSize
	}
	mailer := s.mailer
	if mailer == nil {
		var err error
		mailer, err = NewSMTPMailer(s.cfg)
		if err != nil {
			return SendResult{}, err
		}
	}

	now := s.now()
	if err := s.db.WithContext(ctx).Model(&models.EmailCampaign{}).Where("id = ? AND status = ?", campaign.ID, models.EmailCampaignStatusReady).Updates(map[string]any{
		"status":     models.EmailCampaignStatusSending,
		"started_at": &now,
	}).Error; err != nil {
		return SendResult{}, err
	}

	result := SendResult{CampaignKey: campaign.Key, Wave: wave}
	for result.Claimed < limit {
		recipient, claimed, err := s.claimNext(ctx, campaign.ID, wave)
		if err != nil {
			return result, err
		}
		if !claimed {
			break
		}
		result.Claimed++

		token, err := s.unsubscribeToken(recipient.ID)
		if err != nil {
			return result, s.markUnknown(ctx, recipient.ID, err)
		}
		unsubURL, err := s.unsubscribeURL(token)
		if err != nil {
			return result, s.markUnknown(ctx, recipient.ID, err)
		}
		message := Message{
			To:             recipient.Email,
			Subject:        campaign.Subject,
			Text:           renderText(campaign.TextTemplate, unsubURL),
			MessageID:      s.messageID(recipient.ID),
			UnsubscribeURL: unsubURL,
		}
		if err := mailer.Send(ctx, message); err != nil {
			result.Unknown++
			return result, s.markUnknown(ctx, recipient.ID, err)
		}
		acceptedAt := s.now()
		if err := s.db.WithContext(ctx).Model(&models.EmailCampaignRecipient{}).Where("id = ? AND status = ?", recipient.ID, models.EmailCampaignRecipientStatusSending).Updates(map[string]any{
			"status":           models.EmailCampaignRecipientStatusSMTPAccepted,
			"smtp_accepted_at": &acceptedAt,
			"last_error":       "",
		}).Error; err != nil {
			return result, err
		}
		result.Accepted++
	}

	var pending int64
	if err := s.db.WithContext(ctx).Model(&models.EmailCampaignRecipient{}).Where("campaign_id = ? AND wave = ? AND status = ?", campaign.ID, wave, models.EmailCampaignRecipientStatusPending).Count(&pending).Error; err != nil {
		return result, err
	}
	result.Remaining = int(pending)
	return result, nil
}

func (s *Service) eligibleUsers(ctx context.Context, domain string) ([]models.User, int, error) {
	domain = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(domain, "@")))
	if domain == "" || !strings.Contains(domain, ".") {
		return nil, 0, errors.New("a valid recipient domain is required")
	}
	var suppressions []models.EmailSuppression
	if err := s.db.WithContext(ctx).Where("status = ?", models.EmailSuppressionStatusActive).Find(&suppressions).Error; err != nil {
		return nil, 0, err
	}
	suppressed := make(map[string]struct{}, len(suppressions))
	for _, item := range suppressions {
		suppressed[normalizeEmail(item.NormalizedEmail)] = struct{}{}
	}

	var users []models.User
	if err := s.db.WithContext(ctx).
		Where("is_active = ? AND is_verified = ?", true, true).
		Where("LOWER(TRIM(email)) LIKE ?", "%@"+domain).
		Order("created_at ASC, id ASC").
		Find(&users).Error; err != nil {
		return nil, 0, err
	}

	seen := make(map[string]struct{}, len(users))
	eligible := make([]models.User, 0, len(users))
	suppressedCount := 0
	for _, user := range users {
		email := normalizeEmail(user.Email)
		if email == "" {
			continue
		}
		if _, isSuppressed := suppressed[email]; isSuppressed {
			suppressedCount++
			continue
		}
		if _, duplicate := seen[email]; duplicate {
			continue
		}
		seen[email] = struct{}{}
		eligible = append(eligible, user)
	}
	return eligible, suppressedCount, nil
}

func (s *Service) claimNext(ctx context.Context, campaignID uuid.UUID, wave int) (models.EmailCampaignRecipient, bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return models.EmailCampaignRecipient{}, false, err
		}
		var recipient models.EmailCampaignRecipient
		err := s.db.WithContext(ctx).
			Where("campaign_id = ? AND wave = ? AND status = ?", campaignID, wave, models.EmailCampaignRecipientStatusPending).
			Order("created_at ASC, id ASC").
			First(&recipient).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.EmailCampaignRecipient{}, false, nil
		}
		if err != nil {
			return models.EmailCampaignRecipient{}, false, err
		}

		var suppression models.EmailSuppression
		suppressionErr := s.db.WithContext(ctx).
			Where("normalized_email = ? AND status = ?", recipient.NormalizedEmail, models.EmailSuppressionStatusActive).
			First(&suppression).Error
		if suppressionErr == nil {
			if err := s.db.WithContext(ctx).Model(&models.EmailCampaignRecipient{}).
				Where("id = ? AND status = ?", recipient.ID, models.EmailCampaignRecipientStatusPending).
				Updates(map[string]any{
					"status":     models.EmailCampaignRecipientStatusSuppressed,
					"last_error": "suppressed before campaign delivery",
				}).Error; err != nil {
				return models.EmailCampaignRecipient{}, false, err
			}
			continue
		}
		if !errors.Is(suppressionErr, gorm.ErrRecordNotFound) {
			return models.EmailCampaignRecipient{}, false, suppressionErr
		}

		now := s.now()
		messageID := s.messageID(recipient.ID)
		result := s.db.WithContext(ctx).Model(&models.EmailCampaignRecipient{}).
			Where("id = ? AND status = ? AND NOT EXISTS (SELECT 1 FROM email_suppressions WHERE normalized_email = ? AND status = ?)", recipient.ID, models.EmailCampaignRecipientStatusPending, recipient.NormalizedEmail, models.EmailSuppressionStatusActive).
			Updates(map[string]any{
				"status":              models.EmailCampaignRecipientStatusSending,
				"attempt_count":       gorm.Expr("attempt_count + ?", 1),
				"attempted_at":        &now,
				"provider_message_id": messageID,
			})
		if result.Error != nil {
			return models.EmailCampaignRecipient{}, false, result.Error
		}
		if result.RowsAffected == 0 {
			continue
		}
		recipient.Status = models.EmailCampaignRecipientStatusSending
		recipient.AttemptCount++
		recipient.AttemptedAt = &now
		recipient.ProviderMessageID = messageID
		return recipient, true, nil
	}
	return models.EmailCampaignRecipient{}, false, errors.New("unable to claim campaign recipient")
}

func (s *Service) markUnknown(ctx context.Context, recipientID uuid.UUID, cause error) error {
	message := strings.TrimSpace(cause.Error())
	if len(message) > 2048 {
		message = message[:2048]
	}
	updateErr := s.db.WithContext(ctx).Model(&models.EmailCampaignRecipient{}).Where("id = ? AND status = ?", recipientID, models.EmailCampaignRecipientStatusSending).Updates(map[string]any{
		"status":     models.EmailCampaignRecipientStatusUnknown,
		"last_error": message,
	}).Error
	if updateErr != nil {
		return fmt.Errorf("campaign delivery result is unknown: %v; failed to persist status: %w", cause, updateErr)
	}
	return fmt.Errorf("campaign delivery result is unknown and will not be retried automatically: %w", cause)
}

func (s *Service) unsubscribeToken(recipientID uuid.UUID) (string, error) {
	secret := strings.TrimSpace(s.cfg.SecretKey)
	if secret == "" {
		return "", errors.New("SECRET_KEY is required for unsubscribe tokens")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	if _, err := mac.Write([]byte("email-campaign-unsubscribe:" + recipientID.String())); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Service) unsubscribeURL(token string) (string, error) {
	base := strings.TrimSpace(s.cfg.CampaignUnsubscribeBaseURL)
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Scheme != "https" {
		return "", errors.New("CAMPAIGN_UNSUBSCRIBE_BASE_URL must be an https URL")
	}
	path := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(path, "/api/v1/email/unsubscribe"):
	case strings.HasSuffix(path, "/api/v1"):
		path += "/email/unsubscribe"
	default:
		path += "/api/v1/email/unsubscribe"
	}
	parsed.Path = path
	query := parsed.Query()
	query.Set("token", token)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (s *Service) messageID(recipientID uuid.UUID) string {
	domain := "narratoai.local"
	if parts := strings.SplitN(strings.TrimSpace(s.cfg.CampaignSMTPFromEmail), "@", 2); len(parts) == 2 && parts[1] != "" {
		domain = strings.ToLower(parts[1])
	}
	return fmt.Sprintf("<campaign-%s@%s>", recipientID.String(), domain)
}

func validateSnapshotSpec(spec SnapshotSpec) (SnapshotSpec, error) {
	spec.Key = strings.TrimSpace(spec.Key)
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Subject = strings.TrimSpace(spec.Subject)
	spec.TextTemplate = strings.TrimSpace(spec.TextTemplate)
	spec.ConsentBasis = strings.TrimSpace(spec.ConsentBasis)
	spec.Domain = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(spec.Domain, "@")))
	if spec.WaveSize <= 0 {
		spec.WaveSize = 2000
	}
	if spec.Key == "" || spec.Name == "" || spec.Subject == "" || spec.TextTemplate == "" || spec.ConsentBasis == "" || spec.Domain == "" {
		return SnapshotSpec{}, errors.New("campaign key, name, subject, text template, consent basis, and recipient domain are required")
	}
	if !strings.Contains(spec.Domain, ".") || strings.HasPrefix(spec.Domain, ".") || strings.HasSuffix(spec.Domain, ".") || strings.ContainsAny(spec.Domain, "%_@/\\") {
		return SnapshotSpec{}, errors.New("a valid recipient domain is required")
	}
	if strings.ContainsAny(spec.Subject, "\r\n") {
		return SnapshotSpec{}, errors.New("campaign subject must not contain line breaks")
	}
	return spec, nil
}

func renderText(template, unsubscribeURL string) string {
	text := strings.ReplaceAll(strings.TrimSpace(template), unsubscribePlaceholder, unsubscribeURL)
	if !strings.Contains(text, unsubscribeURL) {
		text += "\n\n如不希望继续收到产品更新邮件，请点击取消订阅：\n" + unsubscribeURL
	}
	return text
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func normalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
