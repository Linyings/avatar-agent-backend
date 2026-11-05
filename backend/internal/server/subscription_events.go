package server

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type subscriptionEventType string

const (
	subscriptionEventStarted       subscriptionEventType = "started"
	subscriptionEventRenewed       subscriptionEventType = "renewed"
	subscriptionEventCancelled     subscriptionEventType = "cancelled"
	subscriptionEventExpired       subscriptionEventType = "expired"
	subscriptionEventPaymentFailed subscriptionEventType = "payment_failed"
	subscriptionEventRefunded      subscriptionEventType = "refunded"
)

var errConcurrentEffectiveSubscription = errors.New("user already has an effective subscription")

type subscriptionEvent struct {
	Provider               string
	ProviderEventID        string
	ProviderAgreementID    string
	ProviderSubscriptionID string
	Type                   subscriptionEventType
	UserID                 uuid.UUID
	PlanID                 uuid.UUID
	PeriodStart            time.Time
	PeriodEnd              time.Time
	ConfirmedAmount        float64
	FailureReason          string
	OccurredAt             time.Time
}

func processSubscriptionEvent(tx *gorm.DB, event subscriptionEvent) error {
	if err := validateSubscriptionEvent(event); err != nil {
		return err
	}
	event.Provider = strings.ToLower(strings.TrimSpace(event.Provider))
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	} else {
		event.OccurredAt = event.OccurredAt.UTC()
	}

	storedEvent := models.PaymentWebhookEvent{
		Provider: event.Provider, EventID: event.ProviderEventID, EventType: string(event.Type),
		ObjectID: event.ProviderAgreementID, SignatureValid: true, ProcessingStatus: "processing", AttemptCount: 1,
	}
	result := tx.Where("provider = ? AND event_id = ?", event.Provider, event.ProviderEventID).FirstOrCreate(&storedEvent)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 && storedEvent.ProcessingStatus == "processed" {
		return nil
	}

	var plan models.SubscriptionPlan
	if err := tx.First(&plan, "id = ?", event.PlanID).Error; err != nil {
		return err
	}
	if (event.Type == subscriptionEventStarted || event.Type == subscriptionEventRenewed) && math.Abs(event.ConfirmedAmount-plan.PriceMonthly) > 0.005 {
		return fmt.Errorf("confirmed subscription amount does not match plan")
	}

	sub, found, err := findProviderSubscriptionForUpdate(tx, event)
	if err != nil {
		return err
	}
	if !found {
		if err := rejectConcurrentEffectiveSubscription(tx, event); err != nil {
			return err
		}
		sub = models.UserSubscription{
			UserID: event.UserID, PlanID: plan.ID, BillingCycle: billingCycleMonthly,
			PaymentProvider: event.Provider, MonthlyCreditsSnapshot: plan.MonthlyCredits,
		}
		setProviderSubscriptionIDs(&sub, event)
	}

	if sub.LastProviderSyncAt != nil && event.OccurredAt.Before(sub.LastProviderSyncAt.UTC()) {
		return markSubscriptionEventProcessed(tx, &storedEvent, event.OccurredAt)
	}
	if sub.UserID != event.UserID || sub.PlanID != plan.ID {
		return errors.New("provider subscription identity does not match stored subscription")
	}

	switch event.Type {
	case subscriptionEventStarted, subscriptionEventRenewed:
		if !event.PeriodEnd.After(event.PeriodStart) {
			return errors.New("paid subscription event has an invalid billing period")
		}
		sub.Status = subscriptionStatusActive
		sub.CurrentPeriodStart = utcTimePtr(event.PeriodStart)
		sub.CurrentPeriodEnd = utcTimePtr(event.PeriodEnd)
		sub.NextChargeAt = utcTimePtr(event.PeriodEnd)
		sub.GracePeriodEndsAt = nil
		sub.ProviderFailureReason = ""
		sub.CancelAtPeriodEnd = false
		sub.CanceledAt = nil
	case subscriptionEventPaymentFailed:
		sub.Status = subscriptionStatusPastDue
		graceEnd := event.OccurredAt.Add(72 * time.Hour)
		sub.GracePeriodEndsAt = &graceEnd
		sub.ProviderFailureReason = strings.TrimSpace(event.FailureReason)
	case subscriptionEventCancelled:
		sub.Status = subscriptionStatusCanceled
		sub.CancelAtPeriodEnd = true
		sub.CanceledAt = utcTimePtr(event.OccurredAt)
		sub.NextChargeAt = nil
	case subscriptionEventExpired:
		sub.Status = subscriptionStatusExpired
		sub.GracePeriodEndsAt = nil
		sub.NextChargeAt = nil
	case subscriptionEventRefunded:
		sub.Status = subscriptionStatusRefunded
		sub.GracePeriodEndsAt = nil
		sub.NextChargeAt = nil
	default:
		return fmt.Errorf("unsupported subscription event type %q", event.Type)
	}

	setProviderSubscriptionIDs(&sub, event)
	sub.LastProviderSyncAt = utcTimePtr(event.OccurredAt)
	if err := tx.Save(&sub).Error; err != nil {
		return err
	}
	if event.Type == subscriptionEventStarted || event.Type == subscriptionEventRenewed {
		if err := createSubscriptionPeriodGrant(tx, sub, plan, event); err != nil {
			return err
		}
	}
	if event.Type == subscriptionEventRefunded {
		if err := revokeUnusedSubscriptionPeriodCredits(tx, sub, event); err != nil {
			return err
		}
	}
	return markSubscriptionEventProcessed(tx, &storedEvent, event.OccurredAt)
}

func validateSubscriptionEvent(event subscriptionEvent) error {
	if strings.TrimSpace(event.Provider) == "" || strings.TrimSpace(event.ProviderEventID) == "" {
		return errors.New("subscription event provider and event ID are required")
	}
	if strings.TrimSpace(event.ProviderAgreementID) == "" {
		return errors.New("subscription event agreement ID is required")
	}
	if event.UserID == uuid.Nil || event.PlanID == uuid.Nil {
		return errors.New("subscription event user and plan are required")
	}
	return nil
}

func findProviderSubscriptionForUpdate(tx *gorm.DB, event subscriptionEvent) (models.UserSubscription, bool, error) {
	var sub models.UserSubscription
	query := tx.Clauses(clause.Locking{Strength: "UPDATE"})
	if strings.EqualFold(event.Provider, "alipay") {
		query = query.Where("alipay_agreement_id = ?", event.ProviderAgreementID)
	} else {
		query = query.Where("payment_provider = ? AND creem_subscription_id = ?", event.Provider, event.ProviderAgreementID)
	}
	err := query.First(&sub).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.UserSubscription{}, false, nil
	}
	return sub, err == nil, err
}

func rejectConcurrentEffectiveSubscription(tx *gorm.DB, event subscriptionEvent) error {
	var count int64
	err := tx.Model(&models.UserSubscription{}).
		Where("user_id = ? AND status IN ?", event.UserID, []string{
			subscriptionStatusActive, subscriptionStatusTrialing, subscriptionStatusScheduledCancel,
			subscriptionStatusPastDue, subscriptionStatusPaused,
		}).Count(&count).Error
	if err != nil {
		return err
	}
	if count > 0 {
		return errConcurrentEffectiveSubscription
	}
	return nil
}

func setProviderSubscriptionIDs(sub *models.UserSubscription, event subscriptionEvent) {
	sub.PaymentProvider = event.Provider
	if event.Provider == "alipay" {
		sub.AlipayAgreementID = event.ProviderAgreementID
		if event.ProviderSubscriptionID != "" {
			sub.AlipaySubscriptionID = event.ProviderSubscriptionID
		}
	} else if event.Provider == "creem" {
		sub.CreemSubscriptionID = event.ProviderAgreementID
	}
}

func createSubscriptionPeriodGrant(tx *gorm.DB, sub models.UserSubscription, plan models.SubscriptionPlan, event subscriptionEvent) error {
	periodStart := event.PeriodStart.UTC()
	periodEnd := event.PeriodEnd.UTC()
	key := fmt.Sprintf("%s:%s:%s", event.Provider, event.ProviderAgreementID, periodStart.Format("2006-01-02"))
	grant := models.SubscriptionCreditGrant{
		UserID: sub.UserID, SubscriptionID: sub.ID, PlanID: plan.ID,
		CreditAmount: plan.MonthlyCredits, PeriodStart: periodStart, PeriodEnd: periodEnd, ExpiresAt: periodEnd,
		IdempotencyKey: key, SourceEventID: event.ProviderEventID, Status: "active",
	}
	return tx.Where("idempotency_key = ?", key).FirstOrCreate(&grant).Error
}

func revokeUnusedSubscriptionPeriodCredits(tx *gorm.DB, sub models.UserSubscription, event subscriptionEvent) error {
	var grant models.SubscriptionCreditGrant
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("subscription_id = ? AND period_start = ?", sub.ID, event.PeriodStart.UTC()).First(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	grant.CreditAmount = grant.UsedAmount
	grant.Status = "refunded"
	return tx.Save(&grant).Error
}

func markSubscriptionEventProcessed(tx *gorm.DB, event *models.PaymentWebhookEvent, occurredAt time.Time) error {
	now := time.Now().UTC()
	event.ProcessingStatus = "processed"
	event.ProcessedAt = &now
	event.LastError = ""
	return tx.Save(event).Error
}

func utcTimePtr(value time.Time) *time.Time {
	result := value.UTC()
	return &result
}
