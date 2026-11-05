package models

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	EmailCampaignStatusDraft     = "draft"
	EmailCampaignStatusReady     = "ready"
	EmailCampaignStatusSending   = "sending"
	EmailCampaignStatusPaused    = "paused"
	EmailCampaignStatusCompleted = "completed"
	EmailCampaignStatusCancelled = "cancelled"
	EmailCampaignStatusFailed    = "failed"

	EmailCampaignRecipientStatusPending      = "pending"
	EmailCampaignRecipientStatusSending      = "sending"
	EmailCampaignRecipientStatusSMTPAccepted = "smtp_accepted"
	EmailCampaignRecipientStatusFailed       = "failed"
	EmailCampaignRecipientStatusUnknown      = "unknown"
	EmailCampaignRecipientStatusSuppressed   = "suppressed"
	EmailCampaignRecipientStatusUnsubscribed = "unsubscribed"

	EmailSuppressionStatusActive   = "active"
	EmailSuppressionStatusReleased = "released"

	EmailSuppressionReasonUnsubscribed = "unsubscribed"
	EmailSuppressionReasonBounced      = "bounced"
	EmailSuppressionReasonComplaint    = "complaint"
	EmailSuppressionReasonManual       = "manual"
)

// EmailCampaign is an immutable campaign definition with a consent record and
// a recipient snapshot. Its content must not be silently changed after a send
// starts, so later waves deliver the same message to the same frozen audience.
type EmailCampaign struct {
	Base
	Key             string     `gorm:"size:100;uniqueIndex:uq_email_campaigns_key;not null" json:"key"`
	Name            string     `gorm:"size:160;not null" json:"name"`
	Subject         string     `gorm:"size:255;not null" json:"subject"`
	TextTemplate    string     `gorm:"type:text;not null" json:"-"`
	ConsentBasis    string     `gorm:"size:255;not null" json:"consent_basis"`
	RecipientDomain string     `gorm:"size:255;not null" json:"recipient_domain"`
	Status          string     `gorm:"size:32;default:draft;index;not null" json:"status"`
	SnapshotAt      *time.Time `gorm:"index" json:"snapshot_at,omitempty"`
	WaveSize        int        `gorm:"not null;default:2000" json:"wave_size"`
	StartedAt       *time.Time `gorm:"index" json:"started_at,omitempty"`
	CompletedAt     *time.Time `gorm:"index" json:"completed_at,omitempty"`
	LastError       string     `gorm:"type:text" json:"last_error,omitempty"`
}

func (EmailCampaign) TableName() string { return "email_campaigns" }

// EmailCampaignRecipient is the durable no-repeat ledger for one campaign
// recipient. The two unique constraints protect both the user identity and a
// case-insensitive mailbox from duplicate enrollment in the same campaign.
type EmailCampaignRecipient struct {
	Base
	CampaignID           uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:uq_email_campaign_recipients_campaign_user,priority:1;uniqueIndex:uq_email_campaign_recipients_campaign_normalized_email,priority:1;index:idx_email_campaign_recipients_campaign_status,priority:1;index:idx_email_campaign_recipients_campaign_wave_status,priority:1" json:"campaign_id"`
	UserID               uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:uq_email_campaign_recipients_campaign_user,priority:2;index" json:"user_id"`
	Email                string     `gorm:"size:255;not null" json:"email"`
	NormalizedEmail      string     `gorm:"size:255;not null;uniqueIndex:uq_email_campaign_recipients_campaign_normalized_email,priority:2;index" json:"-"`
	UnsubscribeTokenHash string     `gorm:"size:128;uniqueIndex:uq_email_campaign_recipients_unsubscribe_token_hash;not null" json:"-"`
	Status               string     `gorm:"size:32;default:pending;not null;index:idx_email_campaign_recipients_campaign_status,priority:2;index:idx_email_campaign_recipients_campaign_wave_status,priority:3" json:"status"`
	Wave                 int        `gorm:"not null;default:1;index:idx_email_campaign_recipients_campaign_wave_status,priority:2" json:"wave"`
	AttemptCount         int        `gorm:"default:0;not null" json:"attempt_count"`
	AttemptedAt          *time.Time `gorm:"index" json:"attempted_at,omitempty"`
	SMTPAcceptedAt       *time.Time `gorm:"index" json:"smtp_accepted_at,omitempty"`
	UnsubscribedAt       *time.Time `gorm:"index" json:"unsubscribed_at,omitempty"`
	LastError            string     `gorm:"type:text" json:"last_error,omitempty"`
	ProviderMessageID    string     `gorm:"size:255;index" json:"provider_message_id,omitempty"`
}

func (EmailCampaignRecipient) TableName() string { return "email_campaign_recipients" }

func (r *EmailCampaignRecipient) BeforeSave(*gorm.DB) error {
	r.Email = strings.TrimSpace(r.Email)
	if r.Email != "" {
		r.NormalizedEmail = normalizeCampaignEmail(r.Email)
	} else {
		r.NormalizedEmail = normalizeCampaignEmail(r.NormalizedEmail)
	}
	return nil
}

// EmailSuppression is global across campaigns. Once a recipient unsubscribes,
// future snapshots exclude the normalized mailbox unless an administrator
// explicitly releases the suppression.
type EmailSuppression struct {
	Base
	UserID           *uuid.UUID `gorm:"type:uuid;index" json:"user_id,omitempty"`
	Email            string     `gorm:"size:255;not null" json:"email"`
	NormalizedEmail  string     `gorm:"size:255;uniqueIndex:uq_email_suppressions_normalized_email;not null" json:"-"`
	Status           string     `gorm:"size:32;default:active;index;not null" json:"status"`
	Reason           string     `gorm:"size:32;not null;index" json:"reason"`
	SourceCampaignID *uuid.UUID `gorm:"type:uuid;index" json:"source_campaign_id,omitempty"`
	Notes            string     `gorm:"type:text" json:"notes,omitempty"`
}

func (EmailSuppression) TableName() string { return "email_suppressions" }

func (s *EmailSuppression) BeforeSave(*gorm.DB) error {
	s.Email = strings.TrimSpace(s.Email)
	if s.Email != "" {
		s.NormalizedEmail = normalizeCampaignEmail(s.Email)
	} else {
		s.NormalizedEmail = normalizeCampaignEmail(s.NormalizedEmail)
	}
	return nil
}

func normalizeCampaignEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
