package server

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
)

func TestSubscriptionEventGrantsOneCreditPeriodAcrossReplays(t *testing.T) {
	app, user, plan := seedSubscriptionEventFixture(t)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	event := subscriptionEvent{
		Provider: "alipay", ProviderEventID: "evt_1", ProviderAgreementID: "agreement_1",
		ProviderSubscriptionID: "subscription_1", Type: subscriptionEventStarted,
		UserID: user.ID, PlanID: plan.ID, PeriodStart: start, PeriodEnd: start.AddDate(0, 1, 0),
		ConfirmedAmount: plan.PriceMonthly, OccurredAt: start,
	}
	if err := app.db.Transaction(func(tx *gorm.DB) error { return processSubscriptionEvent(tx, event) }); err != nil {
		t.Fatal(err)
	}
	if err := app.db.Transaction(func(tx *gorm.DB) error { return processSubscriptionEvent(tx, event) }); err != nil {
		t.Fatalf("same event replay failed: %v", err)
	}
	event.ProviderEventID = "evt_2"
	event.Type = subscriptionEventRenewed
	if err := app.db.Transaction(func(tx *gorm.DB) error { return processSubscriptionEvent(tx, event) }); err != nil {
		t.Fatalf("same period replay failed: %v", err)
	}

	var grants []models.SubscriptionCreditGrant
	if err := app.db.Find(&grants).Error; err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 {
		t.Fatalf("grant count = %d, want 1", len(grants))
	}
	if grants[0].IdempotencyKey != "alipay:agreement_1:2026-08-01" || grants[0].CreditAmount != plan.MonthlyCredits {
		t.Fatalf("unexpected grant: %#v", grants[0])
	}
}

func TestSubscriptionEventRejectsConcurrentEffectiveProvider(t *testing.T) {
	app, user, plan := seedSubscriptionEventFixture(t)
	existing := models.UserSubscription{
		UserID: user.ID, PlanID: plan.ID, BillingCycle: billingCycleMonthly,
		Status: subscriptionStatusActive, PaymentProvider: "creem", CreemSubscriptionID: "creem_sub_1",
	}
	if err := app.db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Truncate(time.Second)
	err := app.db.Transaction(func(tx *gorm.DB) error {
		return processSubscriptionEvent(tx, subscriptionEvent{
			Provider: "alipay", ProviderEventID: "evt_alipay", ProviderAgreementID: "agreement_2",
			Type: subscriptionEventStarted, UserID: user.ID, PlanID: plan.ID,
			PeriodStart: start, PeriodEnd: start.AddDate(0, 1, 0), ConfirmedAmount: plan.PriceMonthly,
		})
	})
	if !errors.Is(err, errConcurrentEffectiveSubscription) {
		t.Fatalf("expected concurrent subscription error, got %v", err)
	}
}

func TestSubscriptionPaymentFailureStartsGraceWithoutCredits(t *testing.T) {
	app, user, plan := seedSubscriptionEventFixture(t)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	activation := subscriptionEvent{
		Provider: "alipay", ProviderEventID: "evt_start", ProviderAgreementID: "agreement_3",
		Type: subscriptionEventStarted, UserID: user.ID, PlanID: plan.ID,
		PeriodStart: start, PeriodEnd: start.AddDate(0, 1, 0), ConfirmedAmount: plan.PriceMonthly, OccurredAt: start,
	}
	if err := app.db.Transaction(func(tx *gorm.DB) error { return processSubscriptionEvent(tx, activation) }); err != nil {
		t.Fatal(err)
	}
	failureAt := start.AddDate(0, 1, 0)
	failure := activation
	failure.ProviderEventID = "evt_failed"
	failure.Type = subscriptionEventPaymentFailed
	failure.PeriodStart = failureAt
	failure.PeriodEnd = failureAt.AddDate(0, 1, 0)
	failure.OccurredAt = failureAt
	if err := app.db.Transaction(func(tx *gorm.DB) error { return processSubscriptionEvent(tx, failure) }); err != nil {
		t.Fatal(err)
	}

	var sub models.UserSubscription
	if err := app.db.First(&sub, "alipay_agreement_id = ?", "agreement_3").Error; err != nil {
		t.Fatal(err)
	}
	if sub.Status != subscriptionStatusPastDue || sub.GracePeriodEndsAt == nil || !sub.GracePeriodEndsAt.Equal(failureAt.Add(72*time.Hour)) {
		t.Fatalf("unexpected grace state: %#v", sub)
	}
	var count int64
	app.db.Model(&models.SubscriptionCreditGrant{}).Count(&count)
	if count != 1 {
		t.Fatalf("grant count = %d, want only activation grant", count)
	}
}

func TestSubscriptionRefundRevokesOnlyUnusedPeriodCredits(t *testing.T) {
	app, user, plan := seedSubscriptionEventFixture(t)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	event := subscriptionEvent{
		Provider: "alipay", ProviderEventID: "evt_start", ProviderAgreementID: "agreement_4",
		Type: subscriptionEventStarted, UserID: user.ID, PlanID: plan.ID,
		PeriodStart: start, PeriodEnd: start.AddDate(0, 1, 0), ConfirmedAmount: plan.PriceMonthly, OccurredAt: start,
	}
	if err := app.db.Transaction(func(tx *gorm.DB) error { return processSubscriptionEvent(tx, event) }); err != nil {
		t.Fatal(err)
	}
	var activeGrant models.SubscriptionCreditGrant
	if err := app.db.First(&activeGrant).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.db.Model(&activeGrant).Update("used_amount", int64(20)).Error; err != nil {
		t.Fatal(err)
	}
	event.ProviderEventID = "evt_refund"
	event.Type = subscriptionEventRefunded
	event.OccurredAt = start.Add(24 * time.Hour)
	if err := app.db.Transaction(func(tx *gorm.DB) error { return processSubscriptionEvent(tx, event) }); err != nil {
		t.Fatal(err)
	}
	var grant models.SubscriptionCreditGrant
	if err := app.db.First(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if grant.Status != "refunded" || grant.CreditAmount != grant.UsedAmount || grant.UsedAmount != 20 {
		t.Fatalf("unexpected refunded grant: %#v", grant)
	}
}

func seedSubscriptionEventFixture(t *testing.T) (*App, models.User, models.SubscriptionPlan) {
	t.Helper()
	app := newTestApp(t)
	user := models.User{Username: "event-" + uuid.NewString(), Email: uuid.NewString() + "@example.com", PasswordHash: "test"}
	if err := app.db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	plan := models.SubscriptionPlan{
		Name: "plan-" + uuid.NewString(), DisplayName: "Monthly", MonthlyCredits: 500,
		PriceMonthly: 29.9, PriceYearly: 299, Currency: "CNY", IsActive: true,
		AlipayPlanID: "alipay_plan", AlipayProductID: "alipay_product", SubscriptionProviderEnabled: true,
	}
	if err := app.db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	return app, user, plan
}
