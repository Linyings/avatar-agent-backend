package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
)

type fakeAlipaySubscriptionClient struct {
	configured  bool
	checkout    providers.AlipaySubscriptionCheckout
	checkoutErr error
	requests    []providers.AlipaySubscriptionRequest
	verifyErr   error
}

func (f *fakeAlipaySubscriptionClient) Configured() bool { return f.configured }
func (f *fakeAlipaySubscriptionClient) CreateSubscription(_ context.Context, req providers.AlipaySubscriptionRequest) (providers.AlipaySubscriptionCheckout, error) {
	f.requests = append(f.requests, req)
	return f.checkout, f.checkoutErr
}
func (f *fakeAlipaySubscriptionClient) GetSubscription(context.Context, string) (providers.AlipaySubscription, error) {
	return providers.AlipaySubscription{}, nil
}
func (f *fakeAlipaySubscriptionClient) GetTransaction(context.Context, string) (providers.AlipayTransaction, error) {
	return providers.AlipayTransaction{}, nil
}
func (f *fakeAlipaySubscriptionClient) CreatePortal(context.Context, string) (string, error) {
	return "https://cashier.example/portal", nil
}
func (f *fakeAlipaySubscriptionClient) CancelSubscription(context.Context, string) error { return nil }
func (f *fakeAlipaySubscriptionClient) VerifyNotification(url.Values) error              { return f.verifyErr }

func TestAlipaySubscriptionCheckoutIsMonthlyOnly(t *testing.T) {
	app := newTestApp(t)
	app.cfg.SubscriptionPaymentProvider = "alipay"
	fake := &fakeAlipaySubscriptionClient{configured: true, checkout: providers.AlipaySubscriptionCheckout{
		CheckoutURL: "https://cashier.example/checkout", RequestID: "provider_req_1",
	}}
	app.alipay = fake
	token := registerAndLogin(t, app, "alipay-checkout@example.com")
	var user models.User
	if err := app.db.First(&user, "email = ?", "alipay-checkout@example.com").Error; err != nil {
		t.Fatal(err)
	}
	plan := seedAlipayCheckoutPlan(t, app, true)

	response := request(app, http.MethodPost, "/api/v1/subscriptions/alipay/checkout", map[string]any{
		"plan_id": plan.ID, "billing_cycle": "monthly",
	}, token)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "https://cashier.example/checkout") {
		t.Fatalf("checkout status=%d body=%s", response.Code, response.Body.String())
	}
	if len(fake.requests) != 1 || fake.requests[0].UserID != user.ID.String() || fake.requests[0].PlanID != plan.AlipayPlanID {
		t.Fatalf("unexpected provider request: %#v", fake.requests)
	}

	annual := request(app, http.MethodPost, "/api/v1/subscriptions/alipay/checkout", map[string]any{
		"plan_id": plan.ID, "billing_cycle": "yearly",
	}, token)
	if annual.Code != http.StatusBadRequest {
		t.Fatalf("annual checkout status=%d body=%s", annual.Code, annual.Body.String())
	}
}

func TestAlipaySubscriptionCheckoutRejectsUnmappedPlan(t *testing.T) {
	app := newTestApp(t)
	app.cfg.SubscriptionPaymentProvider = "alipay"
	app.alipay = &fakeAlipaySubscriptionClient{configured: true}
	token := registerAndLogin(t, app, "alipay-unmapped@example.com")
	plan := seedAlipayCheckoutPlan(t, app, false)

	response := request(app, http.MethodPost, "/api/v1/subscriptions/alipay/checkout", map[string]any{"plan_id": plan.ID}, token)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "subscription_plan_unavailable") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAlipaySubscriptionWebhookRejectsInvalidSignature(t *testing.T) {
	app := newTestApp(t)
	app.alipay = &fakeAlipaySubscriptionClient{configured: true, verifyErr: errors.New("bad signature")}
	form := url.Values{"notify_id": {"notify_1"}, "sign": {"invalid"}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions/alipay/webhook", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	app.Router().ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest || strings.TrimSpace(response.Body.String()) == "success" {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestCurrentSubscriptionReturnsProviderAwareManagementFields(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "alipay-current@example.com")
	var user models.User
	if err := app.db.First(&user, "email = ?", "alipay-current@example.com").Error; err != nil {
		t.Fatal(err)
	}
	plan := seedAlipayCheckoutPlan(t, app, true)
	nextCharge := time.Now().UTC().AddDate(0, 1, 0)
	sub := models.UserSubscription{
		UserID: user.ID, PlanID: plan.ID, BillingCycle: billingCycleMonthly, Status: subscriptionStatusActive,
		PaymentProvider: "alipay", AlipayAgreementID: "sensitive_agreement", NextChargeAt: &nextCharge,
		MonthlyCreditsSnapshot: plan.MonthlyCredits,
	}
	if err := app.db.Create(&sub).Error; err != nil {
		t.Fatal(err)
	}
	response := request(app, http.MethodGet, "/api/v1/subscriptions/current", nil, token)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"payment_provider":"alipay"`) || !strings.Contains(body, `"management_action":"alipay_portal"`) {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
	if strings.Contains(body, "sensitive_agreement") {
		t.Fatalf("provider agreement ID leaked: %s", body)
	}
}

func seedAlipayCheckoutPlan(t *testing.T, app *App, mapped bool) models.SubscriptionPlan {
	t.Helper()
	plan := models.SubscriptionPlan{
		Name: "checkout-" + uuid.NewString(), DisplayName: "Monthly", MonthlyCredits: 500,
		PriceMonthly: 29.9, PriceYearly: 299, Currency: "CNY", IsActive: true,
		SubscriptionProviderEnabled: mapped,
	}
	if mapped {
		plan.AlipayPlanID = "alipay_plan_1"
		plan.AlipayProductID = "alipay_product_1"
	}
	if err := app.db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	return plan
}
