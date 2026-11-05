package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/database"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
	"github.com/shopspring/decimal"
	"golang.org/x/crypto/bcrypt"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	cfg := config.Config{
		Debug:             true,
		Environment:       "testing",
		Host:              "127.0.0.1",
		Port:              0,
		APIv1Prefix:       "/api/v1",
		OpenAPIURL:        "/api/v1/openapi.json",
		DocsURL:           "/docs",
		RedocURL:          "/redoc",
		DatabaseURL:       "sqlite://file:" + uuid.NewString() + "?mode=memory&cache=shared",
		AutoMigrate:       true,
		SecretKey:         "test-secret-key-test-secret-key-1234",
		CORSOrigins:       []string{"*"},
		AccessTTL:         1800000000000,
		RefreshTTL:        86400000000000,
		PromptTemplateDir: "../../prompt",
	}
	db, err := database.Open(cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.AutoMigrate(db); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	return New(cfg, db)
}

func TestCORSAllowsElectronAppOrigin(t *testing.T) {
	app := New(config.Config{
		Environment: "testing",
		APIv1Prefix: "/api/v1",
		CORSOrigins: []string{"app://narratoai"},
		SecretKey:   "test-secret-key-test-secret-key-1234",
	}, nil)

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/auth/login", nil)
	req.Header.Set("Origin", "app://narratoai")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type,accept-language")
	w := httptest.NewRecorder()

	app.Router().ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("CORS preflight status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "app://narratoai" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
}

func seedBillingRates(t *testing.T, app *App, value map[string]any) {
	t.Helper()
	if err := app.db.Create(&models.SystemSetting{Key: "billing_rates", Value: jsonMap(value)}).Error; err != nil {
		t.Fatalf("billing rates setting: %v", err)
	}
}

func seedOpenAIWildcardBillingRate(t *testing.T, app *App) {
	t.Helper()
	seedBillingRates(t, app, map[string]any{
		"llm": []map[string]any{{
			"provider":          "openai",
			"model_id":          "*",
			"tokens_per_credit": 1,
		}},
	})
}

type fakeMailer struct {
	sends []fakeMailSend
	err   error
}

type fakeMailSend struct {
	to      string
	code    string
	purpose string
	locale  string
}

func (f *fakeMailer) SendVerificationCode(_ context.Context, to, code, purpose, locale string) error {
	if f.err != nil {
		return f.err
	}
	f.sends = append(f.sends, fakeMailSend{to: to, code: code, purpose: purpose, locale: locale})
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonHTTPResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

type fakeWeChatPayClient struct {
	configured bool

	nativeResp providers.WeChatNativeOrderResponse
	nativeErr  error
	nativeReqs []providers.WeChatNativeOrderRequest

	queryTx     providers.WeChatTransaction
	queryErr    error
	queryOrders []string

	decryptedTx providers.WeChatTransaction
	decryptErr  error
	verifyErr   error
}

func (f *fakeWeChatPayClient) Configured() bool {
	return f.configured
}

func (f *fakeWeChatPayClient) CreateNativeOrder(_ context.Context, req providers.WeChatNativeOrderRequest) (providers.WeChatNativeOrderResponse, error) {
	f.nativeReqs = append(f.nativeReqs, req)
	if f.nativeErr != nil {
		return providers.WeChatNativeOrderResponse{}, f.nativeErr
	}
	if f.nativeResp.CodeURL == "" {
		return providers.WeChatNativeOrderResponse{CodeURL: "weixin://wxpay/test"}, nil
	}
	return f.nativeResp, nil
}

func (f *fakeWeChatPayClient) QueryTransactionByOutTradeNo(_ context.Context, outTradeNo string) (providers.WeChatTransaction, error) {
	f.queryOrders = append(f.queryOrders, outTradeNo)
	if f.queryErr != nil {
		return providers.WeChatTransaction{}, f.queryErr
	}
	tx := f.queryTx
	if tx.OutTradeNo == "" {
		tx.OutTradeNo = outTradeNo
	}
	return tx, nil
}

func (f *fakeWeChatPayClient) DecryptNotification(_ []byte) (providers.WeChatTransaction, error) {
	if f.decryptErr != nil {
		return providers.WeChatTransaction{}, f.decryptErr
	}
	return f.decryptedTx, nil
}

func (f *fakeWeChatPayClient) VerifyNotificationSignature(_, _ string, _ []byte, _, _ string) error {
	return f.verifyErr
}

type fakeCreemClient struct {
	configured bool

	checkoutRequests []providers.CreemCheckoutRequest

	upgradeSubscriptionID string
	upgradeProductID      string
	upgradeBehavior       string
	upgradeResponse       map[string]any
	upgradeErr            error

	portalCalls int
}

func (f *fakeCreemClient) Configured() bool {
	return f.configured
}

func (f *fakeCreemClient) VerifyWebhookSignature(_ []byte, _ string) error {
	return nil
}

func (f *fakeCreemClient) CreateCheckout(_ context.Context, req providers.CreemCheckoutRequest) (providers.CreemCheckoutResponse, error) {
	f.checkoutRequests = append(f.checkoutRequests, req)
	return providers.CreemCheckoutResponse{
		ID:          "chk_fake",
		CheckoutURL: "https://checkout.creem.test/fake",
		ProductID:   req.ProductID,
		RequestID:   req.RequestID,
		Metadata:    req.Metadata,
	}, nil
}

func (f *fakeCreemClient) GetCheckout(_ context.Context, _ string) (map[string]any, error) {
	return nil, nil
}

func (f *fakeCreemClient) CancelSubscription(_ context.Context, _ string, _ bool) error {
	return nil
}

func (f *fakeCreemClient) UpgradeSubscription(_ context.Context, subscriptionID, productID, updateBehavior string) (map[string]any, error) {
	f.upgradeSubscriptionID = subscriptionID
	f.upgradeProductID = productID
	f.upgradeBehavior = updateBehavior
	return f.upgradeResponse, f.upgradeErr
}

func (f *fakeCreemClient) CreateCustomerPortal(_ context.Context, _ string) (string, error) {
	f.portalCalls++
	return "https://creem.test/portal", nil
}

func TestParseJSONFloatAcceptsNumericStrings(t *testing.T) {
	value, ok := parseJSONFloat("9.9")
	if !ok || value != 9.9 {
		t.Fatalf("expected numeric string to parse as 9.9, got value=%v ok=%v", value, ok)
	}

	value, ok = parseJSONFloat(float64(99))
	if !ok || value != 99 {
		t.Fatalf("expected numeric JSON value to parse as 99, got value=%v ok=%v", value, ok)
	}

	if _, ok = parseJSONFloat("not-a-price"); ok {
		t.Fatal("expected invalid price string to be rejected")
	}
}

func TestAdminTokenPackageAutoNameSoftAndHardDelete(t *testing.T) {
	app := newTestApp(t)
	token := registerAndMakeAdmin(t, app, "admin-credit-package@example.com")

	create := request(app, http.MethodPost, "/api/v1/credit-packages/packages", map[string]any{
		"display_name": "入门版",
		"description":  "测试",
		"token_amount": 2500,
		"price":        4.9,
		"is_active":    true,
	}, token)
	if create.Code != http.StatusCreated {
		t.Fatalf("create package = %d %s", create.Code, create.Body.String())
	}
	var created struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created package: %v", err)
	}
	if created.ID == "" || !strings.HasPrefix(created.Name, "pkg-") {
		t.Fatalf("expected generated package id/name, got %#v", created)
	}

	softDelete := request(app, http.MethodDelete, "/api/v1/credit-packages/packages/"+created.ID, nil, token)
	if softDelete.Code != http.StatusOK {
		t.Fatalf("soft delete package = %d %s", softDelete.Code, softDelete.Body.String())
	}

	adminList := request(app, http.MethodGet, "/api/v1/credit-packages/admin/packages?per_page=100", nil, token)
	if adminList.Code != http.StatusOK {
		t.Fatalf("admin package list = %d %s", adminList.Code, adminList.Body.String())
	}
	var listed struct {
		Items []struct {
			ID        string     `json:"id"`
			IsDeleted bool       `json:"is_deleted"`
			DeletedAt *time.Time `json:"deleted_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(adminList.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode admin package list: %v", err)
	}
	foundDeleted := false
	for _, item := range listed.Items {
		if item.ID == created.ID {
			foundDeleted = item.IsDeleted && item.DeletedAt != nil
			break
		}
	}
	if !foundDeleted {
		t.Fatalf("soft-deleted package was not visible in admin list: %#v", listed.Items)
	}

	recreate := request(app, http.MethodPost, "/api/v1/credit-packages/packages", map[string]any{
		"display_name": "入门版",
		"token_amount": 2500,
		"price":        4.9,
	}, token)
	if recreate.Code != http.StatusCreated {
		t.Fatalf("recreate package without manual id = %d %s", recreate.Code, recreate.Body.String())
	}

	hardDelete := request(app, http.MethodDelete, "/api/v1/credit-packages/packages/"+created.ID+"?hard=true", nil, token)
	if hardDelete.Code != http.StatusOK {
		t.Fatalf("hard delete package = %d %s", hardDelete.Code, hardDelete.Body.String())
	}
	var count int64
	app.db.Unscoped().Model(&models.TokenPackage{}).Where("id = ?", created.ID).Count(&count)
	if count != 0 {
		t.Fatalf("hard-deleted package still exists, count=%d", count)
	}
}

func TestAdminTokenPackageUpdateAcceptsCreditAliases(t *testing.T) {
	app := newTestApp(t)
	token := registerAndMakeAdmin(t, app, "admin-credit-package-alias@example.com")

	create := request(app, http.MethodPost, "/api/v1/credit-packages/packages", map[string]any{
		"display_name": "入门版",
		"token_amount": 2500,
		"bonus_tokens": 100,
		"price":        4.9,
		"is_active":    true,
	}, token)
	if create.Code != http.StatusCreated {
		t.Fatalf("create package = %d %s", create.Code, create.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created package: %v", err)
	}

	update := request(app, http.MethodPut, "/api/v1/credit-packages/packages/"+created.ID, map[string]any{
		"display_name":  "进阶版",
		"credit_amount": 7500,
		"bonus_credits": 500,
		"price":         14.9,
		"is_active":     true,
		"is_deleted":    false,
	}, token)
	if update.Code != http.StatusOK {
		t.Fatalf("update package with credit aliases = %d %s", update.Code, update.Body.String())
	}

	var updated struct {
		CreditAmount int64 `json:"credit_amount"`
		BonusCredits int64 `json:"bonus_credits"`
	}
	if err := json.Unmarshal(update.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode updated package: %v", err)
	}
	if updated.CreditAmount != 7500 || updated.BonusCredits != 500 {
		t.Fatalf("unexpected response aliases: %#v", updated)
	}

	var pkg models.TokenPackage
	if err := app.db.First(&pkg, "id = ?", created.ID).Error; err != nil {
		t.Fatalf("load package: %v", err)
	}
	if pkg.TokenAmount != 7500 || pkg.BonusTokens != 500 {
		t.Fatalf("expected storage fields to be updated, got token_amount=%d bonus_tokens=%d", pkg.TokenAmount, pkg.BonusTokens)
	}
}

func TestAdminSubscriptionPlanAutoNameSoftAndHardDelete(t *testing.T) {
	app := newTestApp(t)
	token := registerAndMakeAdmin(t, app, "admin-subscription-plan@example.com")

	create := request(app, http.MethodPost, "/api/v1/subscriptions/plans", map[string]any{
		"display_name":             "创作者",
		"monthly_token_limit":      5000,
		"price_monthly":            9,
		"price_yearly":             90,
		"currency":                 "USD",
		"creem_product_id_monthly": "prod_month_auto",
		"creem_product_id_yearly":  "prod_year_auto",
		"features":                 []string{"测试功能"},
		"is_active":                true,
		"sort_order":               1,
	}, token)
	if create.Code != http.StatusCreated {
		t.Fatalf("create plan = %d %s", create.Code, create.Body.String())
	}
	var created struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created plan: %v", err)
	}
	if created.ID == "" || !strings.HasPrefix(created.Name, "plan-") {
		t.Fatalf("expected generated plan id/name, got %#v", created)
	}

	softDelete := request(app, http.MethodDelete, "/api/v1/subscriptions/plans/"+created.ID, nil, token)
	if softDelete.Code != http.StatusOK {
		t.Fatalf("soft delete plan = %d %s", softDelete.Code, softDelete.Body.String())
	}

	adminList := request(app, http.MethodGet, "/api/v1/subscriptions/admin/plans?per_page=100", nil, token)
	if adminList.Code != http.StatusOK {
		t.Fatalf("admin plan list = %d %s", adminList.Code, adminList.Body.String())
	}
	var listed struct {
		Items []struct {
			ID        string     `json:"id"`
			IsDeleted bool       `json:"is_deleted"`
			DeletedAt *time.Time `json:"deleted_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(adminList.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode admin plan list: %v", err)
	}
	foundDeleted := false
	for _, item := range listed.Items {
		if item.ID == created.ID {
			foundDeleted = item.IsDeleted && item.DeletedAt != nil
			break
		}
	}
	if !foundDeleted {
		t.Fatalf("soft-deleted plan was not visible in admin list: %#v", listed.Items)
	}

	hardDelete := request(app, http.MethodDelete, "/api/v1/subscriptions/plans/"+created.ID+"?hard=true", nil, token)
	if hardDelete.Code != http.StatusOK {
		t.Fatalf("hard delete plan = %d %s", hardDelete.Code, hardDelete.Body.String())
	}
	var count int64
	app.db.Unscoped().Model(&models.SubscriptionPlan{}).Where("id = ?", created.ID).Count(&count)
	if count != 0 {
		t.Fatalf("hard-deleted plan still exists, count=%d", count)
	}
}

func TestAdminSubscriptionPlanUpdateAcceptsMonthlyTokenLimitCompat(t *testing.T) {
	app := newTestApp(t)
	token := registerAndMakeAdmin(t, app, "admin-subscription-plan-update@example.com")

	create := request(app, http.MethodPost, "/api/v1/subscriptions/plans", map[string]any{
		"display_name":             "创作者",
		"monthly_token_limit":      5000,
		"price_monthly":            9,
		"price_yearly":             90,
		"currency":                 "USD",
		"creem_product_id_monthly": "prod_month_update",
		"creem_product_id_yearly":  "prod_year_update",
	}, token)
	if create.Code != http.StatusCreated {
		t.Fatalf("create plan = %d %s", create.Code, create.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created plan: %v", err)
	}

	update := request(app, http.MethodPut, "/api/v1/subscriptions/plans/"+created.ID, map[string]any{
		"display_name":             "专业版",
		"monthly_token_limit":      18000,
		"price_monthly":            29.9,
		"price_yearly":             287.04,
		"currency":                 "USD",
		"creem_product_id_monthly": "prod_month_update",
		"creem_product_id_yearly":  "prod_year_update",
	}, token)
	if update.Code != http.StatusOK {
		t.Fatalf("update plan with monthly_token_limit = %d %s", update.Code, update.Body.String())
	}
	var updated struct {
		MonthlyCredits    int64  `json:"monthly_credits"`
		MonthlyTokenLimit int64  `json:"monthly_token_limit"`
		PriceMonthly      string `json:"price_monthly"`
		PriceYearly       string `json:"price_yearly"`
	}
	if err := json.Unmarshal(update.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode updated plan: %v", err)
	}
	if updated.MonthlyCredits != 18000 || updated.MonthlyTokenLimit != 18000 {
		t.Fatalf("expected updated monthly credits to be 18000, got %#v", updated)
	}
	if updated.PriceMonthly != "29.90" || updated.PriceYearly != "287.04" {
		t.Fatalf("expected updated prices to preserve monthly/yearly values, got %#v", updated)
	}

	publicList := request(app, http.MethodGet, "/api/v1/subscriptions/plans?per_page=100", nil, "")
	if publicList.Code != http.StatusOK {
		t.Fatalf("public plan list = %d %s", publicList.Code, publicList.Body.String())
	}
	var listed struct {
		Items []struct {
			ID           string `json:"id"`
			PriceMonthly string `json:"price_monthly"`
			PriceYearly  string `json:"price_yearly"`
		} `json:"items"`
	}
	if err := json.Unmarshal(publicList.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode public plan list: %v", err)
	}
	foundUpdated := false
	for _, item := range listed.Items {
		if item.ID == created.ID {
			foundUpdated = true
			if item.PriceMonthly != "29.90" || item.PriceYearly != "287.04" {
				t.Fatalf("public list prices = %#v, want monthly=29.90 yearly=287.04", item)
			}
		}
	}
	if !foundUpdated {
		t.Fatalf("updated plan %s missing from public list: %#v", created.ID, listed.Items)
	}
}

func TestWorkflowScriptPreservesLocalNarrationMetadata(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "local-narration@example.com")
	project := createTestProject(t, app, token, "local narration metadata")

	script := map[string]any{
		"items": []map[string]any{{
			"id":                  1,
			"content":             "少废话，缴械不杀。",
			"start_time":          210.91,
			"end_time":            218.31,
			"duration":            7.4,
			"local_audio_key":     "project:" + project.ID.String() + ":narration:1",
			"audio_storage":       "local",
			"local_audio_missing": false,
		}},
		"edl": map[string]any{"timeline": []any{}},
	}

	save := request(app, http.MethodPut, "/api/v1/projects/"+project.ID.String()+"/workflow/steps/script", map[string]any{
		"data":              script,
		"workflow_revision": 1,
	}, token)
	if save.Code != http.StatusOK {
		t.Fatalf("save script status = %d, body = %s", save.Code, save.Body.String())
	}

	get := request(app, http.MethodGet, "/api/v1/projects/"+project.ID.String()+"/ai-script", nil, token)
	if get.Code != http.StatusOK {
		t.Fatalf("get ai-script status = %d, body = %s", get.Code, get.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(get.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode ai-script: %v", err)
	}
	items, ok := got["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items missing from ai-script: %#v", got)
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("script item has unexpected type: %#v", items[0])
	}
	if item["local_audio_key"] != "project:"+project.ID.String()+":narration:1" || item["audio_storage"] != "local" || item["local_audio_missing"] != false {
		t.Fatalf("local audio metadata was not preserved: %#v", item)
	}
	if _, exists := item["audio_url"]; exists {
		t.Fatalf("local script metadata should not require backend audio_url: %#v", item)
	}
}

func TestNarrationUploadEndpointIsRemoved(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "removed-narration-upload@example.com")
	project := createTestProject(t, app, token, "removed narration upload")

	resp := request(app, http.MethodPost, "/api/v1/projects/"+project.ID.String()+"/narrations/upload?item_id=1", map[string]any{
		"audio_url": "https://example.com/legacy.mp3",
	}, token)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("removed narration upload status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestProjectPreservesLocalPreviewMetadata(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "local-preview@example.com")
	project := createTestProject(t, app, token, "local preview")

	key := "projects/" + project.ID.String() + "/preview_image/preview.png"
	update := request(app, http.MethodPut, "/api/v1/projects/"+project.ID.String(), map[string]any{
		"artifacts": map[string]any{
			"preview_image": map[string]any{
				"storage":    "opfs",
				"key":        key,
				"name":       "preview.png",
				"type":       "image/png",
				"size":       1024,
				"missing":    false,
				"updated_at": "2026-05-12T00:00:00Z",
			},
		},
	}, token)
	if update.Code != http.StatusOK {
		t.Fatalf("update project status = %d, body = %s", update.Code, update.Body.String())
	}
	var updated struct {
		Artifacts map[string]map[string]any `json:"artifacts"`
	}
	if err := json.Unmarshal(update.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode updated project: %v", err)
	}
	preview := updated.Artifacts["preview_image"]
	if preview["key"] != key {
		t.Fatalf("artifacts.preview_image.key = %v, want %q", preview["key"], key)
	}
	if preview["storage"] != "opfs" {
		t.Fatalf("artifacts.preview_image.storage = %v, want opfs", preview["storage"])
	}
	if preview["missing"] != false {
		t.Fatalf("artifacts.preview_image.missing = %v, want false", preview["missing"])
	}
}

func TestWeChatDevWebhookCreditsTokens(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "pay@example.com")

	var user models.User
	if err := app.db.First(&user, "email = ?", "pay@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	pkg := models.TokenPackage{Name: "test", DisplayName: "测试套餐", TokenAmount: 1000, BonusTokens: 100, Price: 1.23, IsActive: true}
	if err := app.db.Create(&pkg).Error; err != nil {
		t.Fatalf("package: %v", err)
	}
	attach := pkg.ID.String()
	codeURL := "mock"
	payment := models.WeChatPayment{UserID: user.ID, OutTradeNo: "ORDER123", CodeURL: &codeURL, Subject: "测试套餐", TotalAmount: 1.23, Currency: "CNY", Status: "pending", TradeState: "NOTPAY", Attach: &attach}
	if err := app.db.Create(&payment).Error; err != nil {
		t.Fatalf("payment: %v", err)
	}

	webhook := request(app, http.MethodPost, "/api/v1/token-accounts/webhook", map[string]any{
		"out_trade_no":   "ORDER123",
		"transaction_id": "WX123",
		"trade_state":    "SUCCESS",
		"success_time":   time.Now().Format(time.RFC3339),
	}, token)
	if webhook.Code != http.StatusOK {
		t.Fatalf("webhook status = %d, body = %s", webhook.Code, webhook.Body.String())
	}

	var account models.UserTokenAccount
	if err := app.db.First(&account, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.TotalTokens != 1100 {
		t.Fatalf("total tokens = %d, want 1100", account.TotalTokens)
	}
}

func TestWechatRechargeStatusQueriesWechatAndCreditsPaidOrder(t *testing.T) {
	app := newTestApp(t)
	app.cfg.WeChatAppID = "wx-app"
	app.cfg.WeChatMchID = "mch-1"
	token := registerAndLogin(t, app, "pay-query@example.com")

	var user models.User
	if err := app.db.First(&user, "email = ?", "pay-query@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	pkg := models.TokenPackage{Name: "query-pack", DisplayName: "查单套餐", TokenAmount: 1000, BonusTokens: 100, Price: 8.88, IsActive: true}
	if err := app.db.Create(&pkg).Error; err != nil {
		t.Fatalf("package: %v", err)
	}
	attach := pkg.ID.String()
	codeURL := "mock"
	payment := models.WeChatPayment{
		UserID:      user.ID,
		OutTradeNo:  "ORDER_QUERY_SUCCESS",
		CodeURL:     &codeURL,
		Subject:     pkg.DisplayName,
		TotalAmount: 8.88,
		Currency:    "CNY",
		Status:      "pending",
		TradeState:  "NOTPAY",
		Attach:      &attach,
	}
	if err := app.db.Create(&payment).Error; err != nil {
		t.Fatalf("payment: %v", err)
	}
	fakeWechat := &fakeWeChatPayClient{
		configured: true,
		queryTx: providers.WeChatTransaction{
			AppID:          "wx-app",
			MchID:          "mch-1",
			OutTradeNo:     payment.OutTradeNo,
			TransactionID:  "WX_QUERY_SUCCESS",
			TradeState:     "SUCCESS",
			TradeStateDesc: "支付成功",
			SuccessTime:    time.Now().UTC().Format(time.RFC3339),
			Amount: struct {
				Total         int    `json:"total"`
				Currency      string `json:"currency"`
				PayerTotal    int    `json:"payer_total"`
				PayerCurrency string `json:"payer_currency"`
			}{Total: 888, Currency: "CNY", PayerTotal: 888, PayerCurrency: "CNY"},
		},
	}
	app.wechat = fakeWechat

	resp := request(app, http.MethodGet, "/api/v1/credit-accounts/recharge/"+payment.OutTradeNo+"/status", nil, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d %s", resp.Code, resp.Body.String())
	}
	var body struct {
		TradeState string `json:"trade_state"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body.TradeState != "SUCCESS" || body.Status != "paid" {
		t.Fatalf("payment state = %#v, want SUCCESS/paid", body)
	}
	if len(fakeWechat.queryOrders) != 1 || fakeWechat.queryOrders[0] != payment.OutTradeNo {
		t.Fatalf("query orders = %#v", fakeWechat.queryOrders)
	}
	var account models.UserTokenAccount
	if err := app.db.First(&account, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.TotalTokens != 1100 {
		t.Fatalf("total credits = %d, want 1100", account.TotalTokens)
	}
	var rechargeCount int64
	app.db.Model(&models.TokenRechargeRecord{}).Where("payment_id = ?", payment.ID).Count(&rechargeCount)
	if rechargeCount != 1 {
		t.Fatalf("recharge records = %d, want 1", rechargeCount)
	}
	var ledgerCount int64
	app.db.Model(&models.CreditLedgerRecord{}).Where("user_id = ? AND event_type = ?", user.ID, creditLedgerEventRecharge).Count(&ledgerCount)
	if ledgerCount != 1 {
		t.Fatalf("ledger records = %d, want 1", ledgerCount)
	}
}

func TestWechatWebhookIsIdempotentForCredits(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "pay-idempotent@example.com")

	var user models.User
	if err := app.db.First(&user, "email = ?", "pay-idempotent@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	pkg := models.TokenPackage{Name: "idempotent-pack", DisplayName: "幂等套餐", TokenAmount: 1000, BonusTokens: 100, Price: 1.23, IsActive: true}
	if err := app.db.Create(&pkg).Error; err != nil {
		t.Fatalf("package: %v", err)
	}
	attach := pkg.ID.String()
	codeURL := "mock"
	payment := models.WeChatPayment{UserID: user.ID, OutTradeNo: "ORDER_IDEMPOTENT", CodeURL: &codeURL, Subject: pkg.DisplayName, TotalAmount: 1.23, Currency: "CNY", Status: "pending", TradeState: "NOTPAY", Attach: &attach}
	if err := app.db.Create(&payment).Error; err != nil {
		t.Fatalf("payment: %v", err)
	}

	payload := map[string]any{
		"out_trade_no":   payment.OutTradeNo,
		"transaction_id": "WX_IDEMPOTENT",
		"trade_state":    "SUCCESS",
		"success_time":   time.Now().UTC().Format(time.RFC3339),
	}
	for i := 0; i < 2; i++ {
		resp := request(app, http.MethodPost, "/api/v1/credit-accounts/webhook", payload, token)
		if resp.Code != http.StatusOK {
			t.Fatalf("webhook %d = %d %s", i+1, resp.Code, resp.Body.String())
		}
	}

	var account models.UserTokenAccount
	if err := app.db.First(&account, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.TotalTokens != 1100 {
		t.Fatalf("total credits = %d, want 1100", account.TotalTokens)
	}
	var rechargeCount int64
	app.db.Model(&models.TokenRechargeRecord{}).Where("payment_id = ?", payment.ID).Count(&rechargeCount)
	if rechargeCount != 1 {
		t.Fatalf("recharge records = %d, want 1", rechargeCount)
	}
	var ledgerCount int64
	app.db.Model(&models.CreditLedgerRecord{}).Where("user_id = ? AND event_type = ?", user.ID, creditLedgerEventRecharge).Count(&ledgerCount)
	if ledgerCount != 1 {
		t.Fatalf("ledger records = %d, want 1", ledgerCount)
	}
}

func TestWechatWebhookMissingAttachDoesNotMarkPaidWithoutCredits(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "pay-missing-attach@example.com")

	var user models.User
	if err := app.db.First(&user, "email = ?", "pay-missing-attach@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	codeURL := "mock"
	payment := models.WeChatPayment{UserID: user.ID, OutTradeNo: "ORDER_NO_ATTACH", CodeURL: &codeURL, Subject: "坏订单", TotalAmount: 1.23, Currency: "CNY", Status: "pending", TradeState: "NOTPAY"}
	if err := app.db.Create(&payment).Error; err != nil {
		t.Fatalf("payment: %v", err)
	}

	resp := request(app, http.MethodPost, "/api/v1/credit-accounts/webhook", map[string]any{
		"out_trade_no":   payment.OutTradeNo,
		"transaction_id": "WX_NO_ATTACH",
		"trade_state":    "SUCCESS",
		"success_time":   time.Now().UTC().Format(time.RFC3339),
	}, token)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("webhook = %d %s, want 500", resp.Code, resp.Body.String())
	}
	var got models.WeChatPayment
	if err := app.db.First(&got, "id = ?", payment.ID).Error; err != nil {
		t.Fatalf("payment reload: %v", err)
	}
	if got.Status == "paid" || got.TradeState == "SUCCESS" {
		t.Fatalf("payment should not be marked paid without credit grant: %#v", got)
	}
	var account models.UserTokenAccount
	if err := app.db.First(&account, "user_id = ?", user.ID).Error; err == nil && account.TotalTokens != 0 {
		t.Fatalf("total credits = %d, want 0", account.TotalTokens)
	}
}

func TestLegacyRechargeEndpointIsDisabled(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "wechat-fx@example.com")

	pkg := models.TokenPackage{Name: "usd-pack", DisplayName: "USD 套餐", TokenAmount: 2500, Price: 4.9, IsActive: true, CreemCurrency: "USD"}
	if err := app.db.Create(&pkg).Error; err != nil {
		t.Fatalf("package: %v", err)
	}

	resp := request(app, http.MethodPost, "/api/v1/credit-accounts/recharge", map[string]any{
		"package_id": pkg.ID.String(),
	}, token)
	if resp.Code != http.StatusConflict || !strings.Contains(resp.Body.String(), "payment_channel_disabled") {
		t.Fatalf("legacy recharge = %d %s", resp.Code, resp.Body.String())
	}
	return
	var body struct {
		OutTradeNo     string  `json:"out_trade_no"`
		TotalAmount    float64 `json:"total_amount"`
		Currency       string  `json:"currency"`
		SourceAmount   float64 `json:"source_amount"`
		SourceCurrency string  `json:"source_currency"`
		ExchangeRate   float64 `json:"exchange_rate"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode recharge: %v", err)
	}
	if math.Abs(body.TotalAmount-33.37) > 0.001 || body.Currency != "CNY" {
		t.Fatalf("wechat amount = %.2f %s, want 33.37 CNY", body.TotalAmount, body.Currency)
	}
	if body.SourceAmount != 4.9 || body.SourceCurrency != "USD" || body.ExchangeRate != usdToCNYRateForWeChat {
		t.Fatalf("source pricing = %#v", body)
	}

	var payment models.WeChatPayment
	if err := app.db.First(&payment, "out_trade_no = ?", body.OutTradeNo).Error; err != nil {
		t.Fatalf("payment: %v", err)
	}
	if math.Abs(payment.TotalAmount-33.37) > 0.001 || payment.Currency != "CNY" {
		t.Fatalf("stored wechat amount = %.2f %s, want 33.37 CNY", payment.TotalAmount, payment.Currency)
	}
}

func TestCreemWebhookRejectsInvalidSignature(t *testing.T) {
	app := newTestApp(t)
	app.cfg.CreemWebhookSecret = "secret"
	app.creem = providers.NewCreemClient(app.cfg)

	body := []byte(`{"id":"evt_bad","eventType":"checkout.completed","object":{}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/billing/creem/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("creem-signature", "wrong")
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("creem invalid signature status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestCreemWebhookRequiresConfiguredSecret(t *testing.T) {
	app := newTestApp(t)

	payload := map[string]any{"id": "evt_missing_secret", "eventType": "checkout.completed", "object": map[string]any{}}
	resp := creemWebhookRequest(t, app, "secret", payload)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("creem missing secret status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestCreemWebhookCreditsPurchaseIdempotent(t *testing.T) {
	app := newTestApp(t)
	app.cfg.CreemWebhookSecret = "secret"
	app.creem = providers.NewCreemClient(app.cfg)
	token := registerAndLogin(t, app, "creem-credit@example.com")

	var user models.User
	if err := app.db.First(&user, "email = ?", "creem-credit@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	productID := "prod_credit"
	pkg := models.TokenPackage{Name: "creem-pack", DisplayName: "Creem 套餐", TokenAmount: 2000, BonusTokens: 250, Price: 19, IsActive: true, CreemEnabled: true, CreemProductID: &productID, CreemCurrency: "USD"}
	if err := app.db.Create(&pkg).Error; err != nil {
		t.Fatalf("package: %v", err)
	}
	session := models.CreemCheckoutSession{UserID: user.ID, Purpose: creemPurposeCreditPack, RequestID: "req_credit", CreemCheckoutID: "chk_credit", CreemProductID: productID, PackageID: &pkg.ID, Status: "pending", Amount: 19, Currency: "USD"}
	if err := app.db.Create(&session).Error; err != nil {
		t.Fatalf("session: %v", err)
	}

	payload := map[string]any{
		"id":        "evt_credit",
		"eventType": "checkout.completed",
		"object": map[string]any{
			"id":         "chk_credit",
			"request_id": "req_credit",
			"product":    map[string]any{"id": productID},
			"customer":   map[string]any{"id": "cust_1"},
			"order":      map[string]any{"id": "ord_1"},
		},
	}
	for i := 0; i < 2; i++ {
		resp := creemWebhookRequest(t, app, "secret", payload)
		if resp.Code != http.StatusOK {
			t.Fatalf("creem webhook status = %d, body = %s", resp.Code, resp.Body.String())
		}
	}

	var account models.UserTokenAccount
	if err := app.db.First(&account, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.TotalTokens != 2250 {
		t.Fatalf("total credits = %d, want 2250", account.TotalTokens)
	}
	var rechargeCount int64
	app.db.Model(&models.TokenRechargeRecord{}).Where("user_id = ? AND payment_method = ?", user.ID, "creem").Count(&rechargeCount)
	if rechargeCount != 1 {
		t.Fatalf("creem recharge records = %d, want 1", rechargeCount)
	}
	_ = token
}

func TestCreemCheckoutCompletedCreatesSubscriptionAndCredits(t *testing.T) {
	app := newTestApp(t)
	app.cfg.CreemWebhookSecret = "secret"
	app.creem = providers.NewCreemClient(app.cfg)
	token := registerAndLogin(t, app, "creem-checkout-sub@example.com")

	var user models.User
	if err := app.db.First(&user, "email = ?", "creem-checkout-sub@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	plan := models.SubscriptionPlan{Name: "checkout-creator", DisplayName: "Checkout 创作者", MonthlyCredits: 75, PriceMonthly: 12, PriceYearly: 120, Currency: "USD", CreemProductIDMonthly: "prod_checkout_month", CreemProductIDYearly: "prod_checkout_year", IsActive: true}
	if err := app.db.Create(&plan).Error; err != nil {
		t.Fatalf("plan: %v", err)
	}
	session := models.CreemCheckoutSession{
		UserID:          user.ID,
		Purpose:         creemPurposeSubscription,
		RequestID:       "req_sub_checkout",
		CreemCheckoutID: "chk_sub_checkout",
		CreemProductID:  plan.CreemProductIDMonthly,
		PlanID:          &plan.ID,
		BillingCycle:    billingCycleMonthly,
		Status:          "pending",
		Amount:          12,
		Currency:        "USD",
		Metadata: jsonMap(map[string]any{
			"purpose":       creemPurposeSubscription,
			"user_id":       user.ID.String(),
			"plan_id":       plan.ID.String(),
			"billing_cycle": billingCycleMonthly,
		}),
	}
	if err := app.db.Create(&session).Error; err != nil {
		t.Fatalf("session: %v", err)
	}

	start := time.Now().UTC().Truncate(time.Second)
	payload := map[string]any{
		"id":        "evt_sub_checkout",
		"eventType": "checkout.completed",
		"object": map[string]any{
			"id":         "chk_sub_checkout",
			"request_id": "req_sub_checkout",
			"order":      map[string]any{"id": "ord_sub_checkout", "product": plan.CreemProductIDMonthly, "customer": "cust_sub_checkout"},
			"customer":   map[string]any{"id": "cust_sub_checkout"},
			"subscription": map[string]any{
				"id":                        "sub_checkout",
				"product":                   plan.CreemProductIDMonthly,
				"status":                    "active",
				"current_period_start_date": start.Format(time.RFC3339),
				"current_period_end_date":   start.AddDate(0, 1, 0).Format(time.RFC3339),
			},
		},
	}
	resp := creemWebhookRequest(t, app, "secret", payload)
	if resp.Code != http.StatusOK {
		t.Fatalf("checkout completed webhook status = %d, body = %s", resp.Code, resp.Body.String())
	}

	var sub models.UserSubscription
	if err := app.db.First(&sub, "user_id = ? AND creem_subscription_id = ?", user.ID, "sub_checkout").Error; err != nil {
		t.Fatalf("subscription was not created from checkout.completed: %v", err)
	}
	if sub.PlanID != plan.ID || sub.Status != subscriptionStatusActive || sub.CreemCustomerID != "cust_sub_checkout" {
		t.Fatalf("subscription mismatch: %#v", sub)
	}
	var grants []models.SubscriptionCreditGrant
	if err := app.db.Find(&grants, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("grants: %v", err)
	}
	if len(grants) != 1 || grants[0].CreditAmount != plan.MonthlyCredits {
		t.Fatalf("checkout grant = %#v, want one %d-credit grant", grants, plan.MonthlyCredits)
	}

	payload["id"] = "evt_sub_checkout_duplicate"
	resp = creemWebhookRequest(t, app, "secret", payload)
	if resp.Code != http.StatusOK {
		t.Fatalf("duplicate checkout completed webhook status = %d, body = %s", resp.Code, resp.Body.String())
	}
	resp = creemWebhookRequest(t, app, "secret", map[string]any{
		"id":        "evt_sub_checkout_paid",
		"eventType": "subscription.paid",
		"object": map[string]any{
			"id":                        "sub_checkout",
			"product":                   map[string]any{"id": plan.CreemProductIDMonthly},
			"customer":                  map[string]any{"id": "cust_sub_checkout"},
			"current_period_start_date": start.Format(time.RFC3339),
			"current_period_end_date":   start.AddDate(0, 1, 0).Format(time.RFC3339),
			"metadata": map[string]any{
				"user_id":       user.ID.String(),
				"plan_id":       plan.ID.String(),
				"billing_cycle": billingCycleMonthly,
			},
		},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("subscription paid webhook status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var grantCount int64
	app.db.Model(&models.SubscriptionCreditGrant{}).Where("user_id = ?", user.ID).Count(&grantCount)
	if grantCount != 1 {
		t.Fatalf("subscription grants after duplicate checkout and paid webhook = %d, want 1", grantCount)
	}

	current := request(app, http.MethodGet, "/api/v1/subscriptions/current", nil, token)
	if current.Code != http.StatusOK || !bytes.Contains(current.Body.Bytes(), []byte(`"subscription_credits":75`)) {
		t.Fatalf("current subscription = %d %s", current.Code, current.Body.String())
	}
}

func TestCreemCheckoutReturnSyncCompletesCreditPurchase(t *testing.T) {
	app := newTestApp(t)
	app.cfg.CreemAPIKey = "creem_test_redirect_key"
	app.creem = providers.NewCreemClient(app.cfg)
	token := registerAndLogin(t, app, "creem-return-credit@example.com")

	var user models.User
	if err := app.db.First(&user, "email = ?", "creem-return-credit@example.com").Error; err != nil {
		t.Fatal(err)
	}
	var initialAccount models.UserTokenAccount
	if err := app.db.First(&initialAccount, "user_id = ?", user.ID).Error; err != nil {
		t.Fatal(err)
	}
	productID := "prod_return_credit"
	pkg := models.TokenPackage{Name: "creem-return-pack", DisplayName: "Creem Return 套餐", TokenAmount: 1200, BonusTokens: 300, Price: 29, IsActive: true, CreemEnabled: true, CreemProductID: &productID, CreemCurrency: "USD"}
	if err := app.db.Create(&pkg).Error; err != nil {
		t.Fatal(err)
	}
	session := models.CreemCheckoutSession{UserID: user.ID, Purpose: creemPurposeCreditPack, RequestID: "req_return_credit", CreemCheckoutID: "chk_return_credit", CreemProductID: productID, PackageID: &pkg.ID, Status: "pending", Amount: 29, Currency: "USD"}
	if err := app.db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}

	payload := creemRedirectPayload(app.cfg.CreemAPIKey, map[string]string{
		"checkout_id": "chk_return_credit",
		"order_id":    "ord_return_credit",
		"customer_id": "cust_return_credit",
		"product_id":  productID,
		"request_id":  "req_return_credit",
	})
	resp := request(app, http.MethodPost, "/api/v1/billing/creem/checkout/sync", payload, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("creem return sync status = %d, body = %s", resp.Code, resp.Body.String())
	}

	var account models.UserTokenAccount
	if err := app.db.First(&account, "user_id = ?", user.ID).Error; err != nil {
		t.Fatal(err)
	}
	expectedTotal := initialAccount.TotalTokens + 1500
	if got := account.TotalTokens; got != expectedTotal {
		t.Fatalf("credited tokens = %d, want %d", got, expectedTotal)
	}
	var rechargeCount int64
	app.db.Model(&models.TokenRechargeRecord{}).Where("user_id = ? AND payment_method = ?", user.ID, "creem").Count(&rechargeCount)
	if rechargeCount != 1 {
		t.Fatalf("creem recharge records = %d, want 1", rechargeCount)
	}

	resp = request(app, http.MethodPost, "/api/v1/billing/creem/checkout/sync", payload, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("duplicate creem return sync status = %d, body = %s", resp.Code, resp.Body.String())
	}
	app.db.First(&account, "user_id = ?", user.ID)
	if got := account.TotalTokens; got != expectedTotal {
		t.Fatalf("duplicate sync credited tokens = %d, want %d", got, expectedTotal)
	}
	app.db.Model(&models.TokenRechargeRecord{}).Where("user_id = ? AND payment_method = ?", user.ID, "creem").Count(&rechargeCount)
	if rechargeCount != 1 {
		t.Fatalf("duplicate creem recharge records = %d, want 1", rechargeCount)
	}
}

func TestCreemCheckoutReturnSyncCreatesSubscriptionAndCredits(t *testing.T) {
	app := newTestApp(t)
	app.cfg.CreemAPIKey = "creem_test_redirect_key"
	app.creem = providers.NewCreemClient(app.cfg)
	token := registerAndLogin(t, app, "creem-return-sub@example.com")

	var user models.User
	if err := app.db.First(&user, "email = ?", "creem-return-sub@example.com").Error; err != nil {
		t.Fatal(err)
	}
	plan := models.SubscriptionPlan{Name: "return-creator", DisplayName: "Return 创作者", MonthlyCredits: 88, PriceMonthly: 18, PriceYearly: 180, Currency: "USD", CreemProductIDMonthly: "prod_return_month", CreemProductIDYearly: "prod_return_year", IsActive: true}
	if err := app.db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	session := models.CreemCheckoutSession{
		UserID:          user.ID,
		Purpose:         creemPurposeSubscription,
		RequestID:       "req_return_sub",
		CreemCheckoutID: "chk_return_sub",
		CreemProductID:  plan.CreemProductIDMonthly,
		PlanID:          &plan.ID,
		BillingCycle:    billingCycleMonthly,
		Status:          "pending",
		Amount:          18,
		Currency:        "USD",
		Metadata: jsonMap(map[string]any{
			"purpose":       creemPurposeSubscription,
			"user_id":       user.ID.String(),
			"plan_id":       plan.ID.String(),
			"plan_name":     plan.Name,
			"billing_cycle": billingCycleMonthly,
		}),
	}
	if err := app.db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}

	payload := creemRedirectPayload(app.cfg.CreemAPIKey, map[string]string{
		"checkout_id":     "chk_return_sub",
		"customer_id":     "cust_return_sub",
		"product_id":      plan.CreemProductIDMonthly,
		"subscription_id": "sub_return_sub",
		"request_id":      "req_return_sub",
	})
	resp := request(app, http.MethodPost, "/api/v1/billing/creem/checkout/sync", payload, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("creem subscription return sync status = %d, body = %s", resp.Code, resp.Body.String())
	}

	var sub models.UserSubscription
	if err := app.db.First(&sub, "user_id = ? AND creem_subscription_id = ?", user.ID, "sub_return_sub").Error; err != nil {
		t.Fatalf("subscription was not created from redirect sync: %v", err)
	}
	if sub.PlanID != plan.ID || sub.Status != subscriptionStatusActive || sub.CreemCustomerID != "cust_return_sub" {
		t.Fatalf("subscription not synced from redirect: %#v", sub)
	}
	var grants []models.SubscriptionCreditGrant
	if err := app.db.Where("user_id = ? AND subscription_id = ?", user.ID, sub.ID).Find(&grants).Error; err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0].CreditAmount != 88 {
		t.Fatalf("redirect subscription grants = %#v, want one 88-credit grant", grants)
	}
}

func TestCreemRedirectSignatureAcceptsDocumentedVariants(t *testing.T) {
	apiKey := "creem_test_redirect_key"
	params := map[string]string{
		"checkout_id":     "chk_return_sub",
		"customer_id":     "cust_return_sub",
		"product_id":      "prod_return_month",
		"subscription_id": "sub_return_sub",
		"request_id":      "req_return_sub",
	}

	hmacPayload := creemRedirectPayload(apiKey, params)
	hmacParams := map[string]string{}
	for key, value := range hmacPayload {
		hmacParams[key] = value.(string)
	}
	if !verifyCreemRedirectSignature(hmacParams, apiKey) {
		t.Fatal("HMAC redirect signature was rejected")
	}

	legacyParams := map[string]string{
		"request_id":      "req_return_sub",
		"checkout_id":     "chk_return_sub",
		"customer_id":     "cust_return_sub",
		"subscription_id": "sub_return_sub",
		"product_id":      "prod_return_month",
	}
	pairs := []string{
		"request_id=req_return_sub",
		"checkout_id=chk_return_sub",
		"customer_id=cust_return_sub",
		"subscription_id=sub_return_sub",
		"product_id=prod_return_month",
		"salt=" + apiKey,
	}
	sum := sha256.Sum256([]byte(strings.Join(pairs, "|")))
	legacyParams["signature"] = hex.EncodeToString(sum[:])
	if !verifyCreemRedirectSignature(legacyParams, apiKey) {
		t.Fatal("legacy salted redirect signature was rejected")
	}
}

func TestCreemSubscriptionCreditsConsumeBeforePurchased(t *testing.T) {
	app := newTestApp(t)
	app.cfg.CreemWebhookSecret = "secret"
	app.creem = providers.NewCreemClient(app.cfg)
	token := registerAndLogin(t, app, "creem-sub@example.com")

	var user models.User
	if err := app.db.First(&user, "email = ?", "creem-sub@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	plan := models.SubscriptionPlan{Name: "creator", DisplayName: "创作者", MonthlyCredits: 50, PriceMonthly: 9, PriceYearly: 90, Currency: "USD", CreemProductIDMonthly: "prod_month", CreemProductIDYearly: "prod_year", IsActive: true}
	if err := app.db.Create(&plan).Error; err != nil {
		t.Fatalf("plan: %v", err)
	}
	start := time.Now().UTC().Truncate(time.Second)
	payload := map[string]any{
		"id":        "evt_sub_paid",
		"eventType": "subscription.paid",
		"object": map[string]any{
			"id":                   "sub_1",
			"product":              map[string]any{"id": "prod_month"},
			"customer":             map[string]any{"id": "cust_sub"},
			"current_period_start": start.Format(time.RFC3339),
			"current_period_end":   start.AddDate(0, 1, 0).Format(time.RFC3339),
			"metadata": map[string]any{
				"user_id":       user.ID.String(),
				"plan_id":       plan.ID.String(),
				"billing_cycle": "monthly",
			},
		},
	}
	for i := 0; i < 2; i++ {
		resp := creemWebhookRequest(t, app, "secret", payload)
		if resp.Code != http.StatusOK {
			t.Fatalf("subscription paid webhook status = %d, body = %s", resp.Code, resp.Body.String())
		}
	}
	var grants []models.SubscriptionCreditGrant
	if err := app.db.Find(&grants, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("grants: %v", err)
	}
	if len(grants) != 1 || grants[0].CreditAmount != 50 {
		t.Fatalf("grants = %#v, want one 50-credit grant", grants)
	}
	seedBillingRates(t, app, map[string]any{
		"llm": []map[string]any{{
			"provider":          "openai",
			"model_id":          "test",
			"tokens_per_credit": 1,
		}},
	})

	if err := app.consumeCredits(creditConsumption{UserID: user.ID, APIEndpoint: "/api/v1/tools/test", ModelID: "test", ServiceKind: "llm", Usage: providers.ChatUsage{TotalTokens: 50}}); err != nil {
		t.Fatalf("consume credits: %v", err)
	}
	var grant models.SubscriptionCreditGrant
	if err := app.db.First(&grant, "id = ?", grants[0].ID).Error; err != nil {
		t.Fatalf("grant: %v", err)
	}
	if grant.UsedAmount != 50 {
		t.Fatalf("subscription grant used = %d, want 50", grant.UsedAmount)
	}
	var account models.UserTokenAccount
	if err := app.db.First(&account, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.UsedTokens != 0 {
		t.Fatalf("purchased credits used = %d, want 0", account.UsedTokens)
	}

	balance := request(app, http.MethodGet, "/api/v1/credit-accounts/balance", nil, token)
	if balance.Code != http.StatusOK || !bytes.Contains(balance.Body.Bytes(), []byte(`"subscription_credits":0`)) {
		t.Fatalf("balance = %d %s", balance.Code, balance.Body.String())
	}
}

func TestCreditPrecheckIncludesSubscriptionCredits(t *testing.T) {
	app := newTestApp(t)
	registerAndLogin(t, app, "creem-precheck@example.com")

	var user models.User
	if err := app.db.First(&user, "email = ?", "creem-precheck@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	var account models.UserTokenAccount
	if err := app.db.FirstOrCreate(&account, models.UserTokenAccount{UserID: user.ID}).Error; err != nil {
		t.Fatalf("account: %v", err)
	}
	account.TotalTokens = 1000
	account.UsedTokens = 1000
	if err := app.db.Save(&account).Error; err != nil {
		t.Fatalf("save account: %v", err)
	}
	plan := models.SubscriptionPlan{Name: "precheck", DisplayName: "Precheck", MonthlyCredits: 50, PriceMonthly: 9, PriceYearly: 90, Currency: "USD", CreemProductIDMonthly: "prod_precheck_month", CreemProductIDYearly: "prod_precheck_year", IsActive: true}
	if err := app.db.Create(&plan).Error; err != nil {
		t.Fatalf("plan: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	periodEnd := now.AddDate(0, 1, 0)
	sub := models.UserSubscription{
		UserID:                 user.ID,
		PlanID:                 plan.ID,
		BillingCycle:           billingCycleMonthly,
		Status:                 subscriptionStatusActive,
		CreemCustomerID:        "cust_precheck",
		CreemSubscriptionID:    "sub_precheck",
		CreemProductID:         plan.CreemProductIDMonthly,
		CurrentPeriodStart:     &now,
		CurrentPeriodEnd:       &periodEnd,
		MonthlyCreditsSnapshot: plan.MonthlyCredits,
	}
	if err := app.db.Create(&sub).Error; err != nil {
		t.Fatalf("subscription: %v", err)
	}
	grant := models.SubscriptionCreditGrant{UserID: user.ID, SubscriptionID: sub.ID, PlanID: plan.ID, CreditAmount: 50, PeriodStart: now, PeriodEnd: periodEnd, ExpiresAt: periodEnd, IdempotencyKey: "sub_precheck:" + now.Format("2006-01-02"), SourceEventID: "evt_precheck_paid", Status: "active"}
	if err := app.db.Create(&grant).Error; err != nil {
		t.Fatalf("grant: %v", err)
	}
	seedBillingRates(t, app, map[string]any{
		"rates": []map[string]any{{
			"provider":     "openai",
			"service_kind": "custom",
			"model_id":     "unknown",
		}},
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	if ok := app.checkCreditBalance(ctx, user.ID, creditConsumption{UserID: user.ID, CreditCount: 40, ServiceKind: "custom"}); !ok {
		t.Fatalf("subscription credits should satisfy precheck, status = %d body = %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	if ok := app.checkCreditBalance(ctx, user.ID, creditConsumption{UserID: user.ID, CreditCount: 60, ServiceKind: "custom"}); ok {
		t.Fatal("precheck should reject requests above available combined balance")
	}
	if rec.Code != http.StatusPaymentRequired || !bytes.Contains(rec.Body.Bytes(), []byte(`"required_credits":60`)) {
		t.Fatalf("precheck rejection = %d %s", rec.Code, rec.Body.String())
	}

	if err := app.db.Model(&models.SubscriptionCreditGrant{}).Where("id = ?", grant.ID).Update("used_amount", 50).Error; err != nil {
		t.Fatalf("use grant: %v", err)
	}
	if err := app.db.Model(&models.UserTokenAccount{}).Where("user_id = ?", user.ID).Update("used_tokens", 1001).Error; err != nil {
		t.Fatalf("make account negative: %v", err)
	}
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	if ok := app.checkCreditBalance(ctx, user.ID, creditConsumption{UserID: user.ID, CreditCount: 1, ServiceKind: "custom"}); ok {
		t.Fatal("precheck should reject once combined balance is negative")
	}
	if rec.Code != http.StatusPaymentRequired || !bytes.Contains(rec.Body.Bytes(), []byte(`"remaining_credits":-1`)) {
		t.Fatalf("precheck rejection = %d %s", rec.Code, rec.Body.String())
	}
}

func TestCreditReservationDefersChargeUntilProjectCompletion(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLoginWithCredits(t, app, "reservation-complete@example.com", 100)
	user := lookupUserByEmail(t, app, "reservation-complete@example.com")
	project := createTestProject(t, app, token, "预留确认项目")
	seedBillingRates(t, app, map[string]any{
		"llm": []map[string]any{{
			"provider":          "openai",
			"model_id":          "reservation-model",
			"tokens_per_credit": 1,
		}},
	})

	if err := app.reserveCredits(creditConsumption{
		UserID:      user.ID,
		ProjectID:   &project.ID,
		APIEndpoint: "/api/v1/tools/generate-script-stream",
		Provider:    "openai",
		ServiceKind: "llm",
		ModelID:     "reservation-model",
		Usage:       providers.ChatUsage{TotalTokens: 10},
		RequestMetadata: gin.H{
			"request_id": "reservation-request-1",
		},
	}); err != nil {
		t.Fatalf("reserve credits: %v", err)
	}

	var account models.UserTokenAccount
	if err := app.db.First(&account, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.UsedTokens != 0 {
		t.Fatalf("used tokens after reserve = %d, want 0", account.UsedTokens)
	}
	var usageCount int64
	if err := app.db.Model(&models.TokenUsageRecord{}).Where("user_id = ?", user.ID).Count(&usageCount).Error; err != nil {
		t.Fatalf("usage count: %v", err)
	}
	if usageCount != 0 {
		t.Fatalf("usage count after reserve = %d, want 0", usageCount)
	}
	var ledgerCount int64
	if err := app.db.Model(&models.CreditLedgerRecord{}).Where("user_id = ? AND event_type = ?", user.ID, creditLedgerEventConsume).Count(&ledgerCount).Error; err != nil {
		t.Fatalf("ledger count: %v", err)
	}
	if ledgerCount != 0 {
		t.Fatalf("ledger count after reserve = %d, want 0", ledgerCount)
	}

	balance := request(app, http.MethodGet, "/api/v1/credit-accounts/balance", nil, token)
	if balance.Code != http.StatusOK || !bytes.Contains(balance.Body.Bytes(), []byte(`"remaining_credits":90`)) || !bytes.Contains(balance.Body.Bytes(), []byte(`"reserved_credits":10`)) {
		t.Fatalf("balance after reserve = %d %s", balance.Code, balance.Body.String())
	}

	complete := request(app, http.MethodPut, "/api/v1/projects/"+project.ID.String(), map[string]any{
		"status": "completed",
		"artifacts": map[string]any{
			"final_video": map[string]any{"key": "projects/project-1/final.mp4"},
		},
	}, token)
	if complete.Code != http.StatusOK {
		t.Fatalf("complete project = %d %s", complete.Code, complete.Body.String())
	}

	if err := app.db.First(&account, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("account after confirm: %v", err)
	}
	if account.UsedTokens != 10 {
		t.Fatalf("used tokens after confirm = %d, want 10", account.UsedTokens)
	}
	if err := app.db.Model(&models.TokenUsageRecord{}).Where("user_id = ?", user.ID).Count(&usageCount).Error; err != nil {
		t.Fatalf("usage count after confirm: %v", err)
	}
	if usageCount != 1 {
		t.Fatalf("usage count after confirm = %d, want 1", usageCount)
	}
	if err := app.db.Model(&models.CreditLedgerRecord{}).Where("user_id = ? AND event_type = ?", user.ID, creditLedgerEventConsume).Count(&ledgerCount).Error; err != nil {
		t.Fatalf("ledger count after confirm: %v", err)
	}
	if ledgerCount != 1 {
		t.Fatalf("ledger count after confirm = %d, want 1", ledgerCount)
	}
	var ledger models.CreditLedgerRecord
	if err := app.db.First(&ledger, "user_id = ? AND event_type = ?", user.ID, creditLedgerEventConsume).Error; err != nil {
		t.Fatalf("ledger after confirm: %v", err)
	}
	detail := request(app, http.MethodGet, "/api/v1/credit-accounts/ledger/records/"+ledger.ID.String(), nil, token)
	if detail.Code != http.StatusOK {
		t.Fatalf("ledger detail = %d %s", detail.Code, detail.Body.String())
	}
	var detailBody struct {
		Record struct {
			ID              string `json:"id"`
			ProjectName     string `json:"project_name"`
			ChangeAmount    int64  `json:"change_amount"`
			RequestMetadata any    `json:"request_metadata"`
		} `json:"record"`
		UsageRecords []struct {
			ServiceKind      string   `json:"service_kind"`
			APIEndpoint      string   `json:"api_endpoint"`
			CreditCount      int64    `json:"credit_count"`
			CostAmount       *float64 `json:"cost_amount"`
			RequestMetadata  any      `json:"request_metadata"`
			TotalModelTokens int64    `json:"total_model_tokens"`
		} `json:"usage_records"`
		ServiceBreakdown  map[string]int64 `json:"service_breakdown"`
		EndpointBreakdown map[string]int64 `json:"endpoint_breakdown"`
		ReservationCount  int64            `json:"reservation_count"`
		BillingMode       string           `json:"billing_mode"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &detailBody); err != nil {
		t.Fatalf("decode ledger detail: %v", err)
	}
	if detailBody.Record.ID != ledger.ID.String() || detailBody.Record.ProjectName != "预留确认项目" || detailBody.Record.ChangeAmount != -10 {
		t.Fatalf("ledger detail record mismatch: %#v", detailBody.Record)
	}
	if detailBody.Record.RequestMetadata == nil {
		t.Fatalf("consume ledger detail should keep redacted list metadata as empty object, got nil")
	}
	if detailBody.BillingMode != "workflow_reservation_confirm" || detailBody.ReservationCount != 1 {
		t.Fatalf("detail billing metadata mismatch: %#v", detailBody)
	}
	if detailBody.ServiceBreakdown["llm"] != 10 || detailBody.EndpointBreakdown["/api/v1/tools/generate-script-stream"] != 10 {
		t.Fatalf("detail breakdown mismatch: %#v", detailBody)
	}
	if len(detailBody.UsageRecords) != 1 {
		t.Fatalf("usage records = %d, want 1: %#v", len(detailBody.UsageRecords), detailBody.UsageRecords)
	}
	if usage := detailBody.UsageRecords[0]; usage.ServiceKind != "llm" || usage.CreditCount != 10 || usage.TotalModelTokens != 10 || usage.CostAmount != nil || usage.RequestMetadata != nil {
		t.Fatalf("usage detail mismatch or leaked financial metadata: %#v", usage)
	}
	var reservation models.CreditReservation
	if err := app.db.First(&reservation, "project_id = ?", project.ID).Error; err != nil {
		t.Fatalf("reservation after confirm: %v", err)
	}
	if reservation.Status != creditReservationConfirmed {
		t.Fatalf("reservation status = %s, want confirmed", reservation.Status)
	}
	balance = request(app, http.MethodGet, "/api/v1/credit-accounts/balance", nil, token)
	if balance.Code != http.StatusOK || !bytes.Contains(balance.Body.Bytes(), []byte(`"remaining_credits":90`)) || !bytes.Contains(balance.Body.Bytes(), []byte(`"reserved_credits":0`)) {
		t.Fatalf("balance after confirm = %d %s", balance.Code, balance.Body.String())
	}
}

func TestCreditReservationReleaseRestoresAvailableBalance(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLoginWithCredits(t, app, "reservation-release@example.com", 100)
	user := lookupUserByEmail(t, app, "reservation-release@example.com")
	project := createTestProject(t, app, token, "预留释放项目")
	seedBillingRates(t, app, map[string]any{
		"rates": []map[string]any{{
			"provider":          "openai",
			"service_kind":      "llm",
			"model_id":          "reservation-model",
			"tokens_per_credit": 1,
		}},
	})
	if err := app.reserveCredits(creditConsumption{
		UserID:      user.ID,
		ProjectID:   &project.ID,
		APIEndpoint: "/api/v1/tools/frame-workflow/narrative-analyze",
		Provider:    "openai",
		ServiceKind: "llm",
		ModelID:     "reservation-model",
		Usage:       providers.ChatUsage{TotalTokens: 25},
		RequestMetadata: gin.H{
			"request_id": "reservation-release-1",
		},
	}); err != nil {
		t.Fatalf("reserve credits: %v", err)
	}
	release := request(app, http.MethodPost, "/api/v1/projects/"+project.ID.String()+"/billing-reservations/release", map[string]any{"reason": "workflow_failed"}, token)
	if release.Code != http.StatusOK {
		t.Fatalf("release reservations = %d %s", release.Code, release.Body.String())
	}
	balance := request(app, http.MethodGet, "/api/v1/credit-accounts/balance", nil, token)
	if balance.Code != http.StatusOK || !bytes.Contains(balance.Body.Bytes(), []byte(`"remaining_credits":100`)) || !bytes.Contains(balance.Body.Bytes(), []byte(`"reserved_credits":0`)) {
		t.Fatalf("balance after release = %d %s", balance.Code, balance.Body.String())
	}
	var reservation models.CreditReservation
	if err := app.db.First(&reservation, "project_id = ?", project.ID).Error; err != nil {
		t.Fatalf("reservation after release: %v", err)
	}
	if reservation.Status != creditReservationReleased {
		t.Fatalf("reservation status = %s, want released", reservation.Status)
	}
	var usageCount int64
	if err := app.db.Model(&models.TokenUsageRecord{}).Where("user_id = ?", user.ID).Count(&usageCount).Error; err != nil {
		t.Fatalf("usage count after release: %v", err)
	}
	if usageCount != 0 {
		t.Fatalf("usage count after release = %d, want 0", usageCount)
	}
}

func TestCreditReservationIdempotencyAvoidsDuplicateReserve(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLoginWithCredits(t, app, "reservation-idempotent@example.com", 100)
	user := lookupUserByEmail(t, app, "reservation-idempotent@example.com")
	project := createTestProject(t, app, token, "预留幂等项目")
	seedBillingRates(t, app, map[string]any{
		"llm": []map[string]any{{
			"provider":          "openai",
			"model_id":          "reservation-model",
			"tokens_per_credit": 1,
		}},
	})
	consumption := creditConsumption{
		UserID:      user.ID,
		ProjectID:   &project.ID,
		APIEndpoint: "/api/v1/tools/generate-script-stream",
		Provider:    "openai",
		ServiceKind: "llm",
		ModelID:     "reservation-model",
		Usage:       providers.ChatUsage{TotalTokens: 10},
		RequestMetadata: gin.H{
			"request_id": "same-request-id",
		},
	}
	if err := app.reserveCredits(consumption); err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	if err := app.reserveCredits(consumption); err != nil {
		t.Fatalf("second reserve: %v", err)
	}
	var count int64
	if err := app.db.Model(&models.CreditReservation{}).Where("project_id = ?", project.ID).Count(&count).Error; err != nil {
		t.Fatalf("reservation count: %v", err)
	}
	if count != 1 {
		t.Fatalf("reservation count = %d, want 1", count)
	}
}

func TestCreditPrecheckRejectsMissingBillingRate(t *testing.T) {
	app := newTestApp(t)
	registerAndLoginWithCredits(t, app, "missing-rate-precheck@example.com", 100)
	user := lookupUserByEmail(t, app, "missing-rate-precheck@example.com")

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ok := app.checkCreditBalance(ctx, user.ID, creditConsumption{
		UserID:      user.ID,
		APIEndpoint: "/api/v1/tools/timeline-view/analyze",
		Provider:    "dashscope",
		ServiceKind: "vision",
		ModelID:     "qwen3-vl-plus",
		Usage:       providers.ChatUsage{TotalTokens: 10},
	})
	if ok {
		t.Fatal("precheck should reject when billing rate is missing")
	}
	if rec.Code != http.StatusBadRequest || !bytes.Contains(rec.Body.Bytes(), []byte("未配置计费规则")) {
		t.Fatalf("precheck response = %d %s", rec.Code, rec.Body.String())
	}
}

func TestCreditPrecheckRejectsRequestsAboveAvailableBalance(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLoginWithCredits(t, app, "credit-overdraft@example.com", 10)
	user := lookupUserByEmail(t, app, "credit-overdraft@example.com")
	seedBillingRates(t, app, map[string]any{
		"rates": []map[string]any{{
			"provider":     "openai",
			"service_kind": "custom",
			"model_id":     "*",
		}},
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	if ok := app.checkCreditBalance(ctx, user.ID, creditConsumption{UserID: user.ID, CreditCount: 50, ServiceKind: "custom"}); ok {
		t.Fatal("credit precheck should reject requests above available balance")
	}
	if rec.Code != http.StatusPaymentRequired || !bytes.Contains(rec.Body.Bytes(), []byte(`"remaining_credits":10`)) {
		t.Fatalf("credit precheck rejection = %d body = %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	if ok := app.checkTokenBalance(ctx, user.ID, 50); ok {
		t.Fatal("token precheck should reject requests above available balance")
	}

	if err := app.consumeCredits(creditConsumption{
		UserID:      user.ID,
		CreditCount: 15,
		APIEndpoint: "/api/v1/tools/test-overdraft",
		ServiceKind: "custom",
		ModelID:     "test-overdraft",
	}); err == nil || !strings.Contains(err.Error(), "Credits余额不足") {
		t.Fatalf("consume should reject insufficient balance, got %v", err)
	}

	balance := request(app, http.MethodGet, "/api/v1/credit-accounts/balance", nil, token)
	if balance.Code != http.StatusOK || !bytes.Contains(balance.Body.Bytes(), []byte(`"remaining_credits":10`)) {
		t.Fatalf("balance should remain unchanged = %d %s", balance.Code, balance.Body.String())
	}
}

func TestCreemPausedSubscriptionRevokesActiveCredits(t *testing.T) {
	app := newTestApp(t)
	app.cfg.CreemWebhookSecret = "secret"
	app.creem = providers.NewCreemClient(app.cfg)
	registerAndLogin(t, app, "creem-pause@example.com")

	var user models.User
	if err := app.db.First(&user, "email = ?", "creem-pause@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	plan := models.SubscriptionPlan{Name: "pause", DisplayName: "Pause", MonthlyCredits: 50, PriceMonthly: 9, PriceYearly: 90, Currency: "USD", CreemProductIDMonthly: "prod_pause_month", CreemProductIDYearly: "prod_pause_year", IsActive: true}
	if err := app.db.Create(&plan).Error; err != nil {
		t.Fatalf("plan: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	periodEnd := now.AddDate(0, 1, 0)
	sub := models.UserSubscription{UserID: user.ID, PlanID: plan.ID, BillingCycle: billingCycleMonthly, Status: subscriptionStatusActive, CreemCustomerID: "cust_pause", CreemSubscriptionID: "sub_pause", CreemProductID: plan.CreemProductIDMonthly, CurrentPeriodStart: &now, CurrentPeriodEnd: &periodEnd, MonthlyCreditsSnapshot: plan.MonthlyCredits}
	if err := app.db.Create(&sub).Error; err != nil {
		t.Fatalf("subscription: %v", err)
	}
	grant := models.SubscriptionCreditGrant{UserID: user.ID, SubscriptionID: sub.ID, PlanID: plan.ID, CreditAmount: 50, PeriodStart: now, PeriodEnd: periodEnd, ExpiresAt: periodEnd, IdempotencyKey: "sub_pause:" + now.Format("2006-01-02"), SourceEventID: "evt_pause_paid", Status: "active"}
	if err := app.db.Create(&grant).Error; err != nil {
		t.Fatalf("grant: %v", err)
	}

	resp := creemWebhookRequest(t, app, "secret", map[string]any{
		"id":        "evt_sub_paused",
		"eventType": "subscription.paused",
		"object": map[string]any{
			"id":     "sub_pause",
			"status": "paused",
		},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("paused webhook status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if err := app.db.First(&sub, "id = ?", sub.ID).Error; err != nil {
		t.Fatalf("reload subscription: %v", err)
	}
	if sub.Status != subscriptionStatusPaused {
		t.Fatalf("subscription status = %s, want paused", sub.Status)
	}
	if err := app.db.First(&grant, "id = ?", grant.ID).Error; err != nil {
		t.Fatalf("reload grant: %v", err)
	}
	if grant.Status != "revoked" {
		t.Fatalf("grant status = %s, want revoked", grant.Status)
	}
	_, snapshot, err := app.creditBalanceSnapshot(user.ID)
	if err != nil {
		t.Fatalf("credit snapshot: %v", err)
	}
	if snapshot.SubscriptionRemaining != 0 {
		t.Fatalf("subscription remaining = %d, want 0", snapshot.SubscriptionRemaining)
	}
}

func TestImmediateSubscriptionUpgradeTopsUpCurrentPeriod(t *testing.T) {
	app := newTestApp(t)
	registerAndLogin(t, app, "creem-upgrade@example.com")

	var user models.User
	if err := app.db.First(&user, "email = ?", "creem-upgrade@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	basic := models.SubscriptionPlan{Name: "basic", DisplayName: "Basic", MonthlyCredits: 8000, PriceMonthly: 9.9, PriceYearly: 95.04, Currency: "USD", CreemProductIDMonthly: "prod_basic_month", CreemProductIDYearly: "prod_basic_year", IsActive: true}
	pro := models.SubscriptionPlan{Name: "pro", DisplayName: "PRO", MonthlyCredits: 30000, PriceMonthly: 29.9, PriceYearly: 287.04, Currency: "USD", CreemProductIDMonthly: "prod_pro_month", CreemProductIDYearly: "prod_pro_year", IsActive: true}
	if err := app.db.Create(&basic).Error; err != nil {
		t.Fatalf("basic plan: %v", err)
	}
	if err := app.db.Create(&pro).Error; err != nil {
		t.Fatalf("pro plan: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	oldStart := now.AddDate(0, 0, -5)
	oldEnd := oldStart.AddDate(0, 1, 0)
	sub := models.UserSubscription{
		UserID:                 user.ID,
		PlanID:                 basic.ID,
		BillingCycle:           billingCycleMonthly,
		Status:                 subscriptionStatusActive,
		CreemCustomerID:        "cust_upgrade",
		CreemSubscriptionID:    "sub_upgrade",
		CreemProductID:         basic.CreemProductIDMonthly,
		CurrentPeriodStart:     &oldStart,
		CurrentPeriodEnd:       &oldEnd,
		MonthlyCreditsSnapshot: basic.MonthlyCredits,
	}
	if err := app.db.Create(&sub).Error; err != nil {
		t.Fatalf("subscription: %v", err)
	}
	oldGrant := models.SubscriptionCreditGrant{
		UserID:         user.ID,
		SubscriptionID: sub.ID,
		PlanID:         basic.ID,
		CreditAmount:   basic.MonthlyCredits,
		PeriodStart:    oldStart,
		PeriodEnd:      oldEnd,
		ExpiresAt:      oldEnd,
		IdempotencyKey: "sub_upgrade:" + oldStart.Format("2006-01-02"),
		SourceEventID:  "evt_basic_paid",
		Status:         "active",
	}
	if err := app.db.Create(&oldGrant).Error; err != nil {
		t.Fatalf("old grant: %v", err)
	}

	newEnd := now.AddDate(0, 1, 0)
	upgraded, err := app.applyImmediateSubscriptionUpgrade(user.ID, sub, pro, billingCycleMonthly, pro.CreemProductIDMonthly, map[string]any{
		"id":                   "sub_upgrade",
		"product":              map[string]any{"id": pro.CreemProductIDMonthly},
		"customer":             map[string]any{"id": "cust_upgrade"},
		"current_period_start": now.Format(time.RFC3339),
		"current_period_end":   newEnd.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("apply upgrade: %v", err)
	}
	if upgraded.PlanID != pro.ID || upgraded.CreemProductID != pro.CreemProductIDMonthly || upgraded.MonthlyCreditsSnapshot != pro.MonthlyCredits {
		t.Fatalf("upgraded subscription not synced: %#v", upgraded)
	}

	var sums struct{ Total int64 }
	if err := app.db.Model(&models.SubscriptionCreditGrant{}).
		Where("user_id = ? AND subscription_id = ? AND status = ?", user.ID, sub.ID, "active").
		Select("coalesce(sum(credit_amount), 0) as total").
		Scan(&sums).Error; err != nil {
		t.Fatalf("grant sum: %v", err)
	}
	if sums.Total != pro.MonthlyCredits {
		t.Fatalf("active subscription credits = %d, want %d", sums.Total, pro.MonthlyCredits)
	}

	var topUp models.SubscriptionCreditGrant
	if err := app.db.First(&topUp, "idempotency_key = ?", "sub_upgrade:"+now.Format("2006-01-02")).Error; err != nil {
		t.Fatalf("top-up grant: %v", err)
	}
	if topUp.CreditAmount != pro.MonthlyCredits-basic.MonthlyCredits {
		t.Fatalf("top-up credits = %d, want %d", topUp.CreditAmount, pro.MonthlyCredits-basic.MonthlyCredits)
	}

	if _, err := app.applyImmediateSubscriptionUpgrade(user.ID, upgraded, pro, billingCycleMonthly, pro.CreemProductIDMonthly, map[string]any{
		"id":                   "sub_upgrade",
		"product":              map[string]any{"id": pro.CreemProductIDMonthly},
		"customer":             map[string]any{"id": "cust_upgrade"},
		"current_period_start": now.Format(time.RFC3339),
		"current_period_end":   newEnd.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("repeat upgrade sync: %v", err)
	}
	if err := app.db.Model(&models.SubscriptionCreditGrant{}).
		Where("user_id = ? AND subscription_id = ? AND status = ?", user.ID, sub.ID, "active").
		Select("coalesce(sum(credit_amount), 0) as total").
		Scan(&sums).Error; err != nil {
		t.Fatalf("repeat grant sum: %v", err)
	}
	if sums.Total != pro.MonthlyCredits {
		t.Fatalf("repeat upgrade changed active credits to %d, want %d", sums.Total, pro.MonthlyCredits)
	}
}

func TestSubscriptionCheckoutAppliesFirstMonthPromoOnlyToNineNinetyMonthlyPlan(t *testing.T) {
	tests := []struct {
		name         string
		priceMonthly float64
		billingCycle string
		wantDiscount string
	}{
		{name: "nine ninety monthly", priceMonthly: 9.9, billingCycle: billingCycleMonthly, wantDiscount: firstMonthPromoCode},
		{name: "nine ninety yearly", priceMonthly: 9.9, billingCycle: billingCycleYearly, wantDiscount: ""},
		{name: "other monthly", priceMonthly: 29.9, billingCycle: billingCycleMonthly, wantDiscount: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newTestApp(t)
			app.cfg.CreemCheckoutEnabled = true
			app.cfg.CreemAPIKey = "test-creem-key"
			token := registerAndLogin(t, app, "creem-promo-"+strings.ReplaceAll(tt.name, " ", "-")+"@example.com")
			fakeCreem := &fakeCreemClient{configured: true}
			app.creem = fakeCreem

			plan := models.SubscriptionPlan{
				Name:                  "promo-" + strings.ReplaceAll(tt.name, " ", "-"),
				DisplayName:           "Promo Plan",
				MonthlyCredits:        5000,
				PriceMonthly:          tt.priceMonthly,
				PriceYearly:           tt.priceMonthly * 12 * 0.8,
				Currency:              "USD",
				CreemProductIDMonthly: "prod_promo_month",
				CreemProductIDYearly:  "prod_promo_year",
				IsActive:              true,
			}
			if err := app.db.Create(&plan).Error; err != nil {
				t.Fatalf("plan: %v", err)
			}

			resp := request(app, http.MethodPost, "/api/v1/subscriptions/checkout", map[string]any{
				"plan_id":       plan.ID.String(),
				"billing_cycle": tt.billingCycle,
			}, token)
			if resp.Code != http.StatusOK {
				t.Fatalf("checkout status = %d, body = %s", resp.Code, resp.Body.String())
			}
			if len(fakeCreem.checkoutRequests) != 1 {
				t.Fatalf("checkout requests = %d, want 1", len(fakeCreem.checkoutRequests))
			}
			got := fakeCreem.checkoutRequests[0].DiscountCode
			if got != tt.wantDiscount {
				t.Fatalf("discount code = %q, want %q", got, tt.wantDiscount)
			}
			if tt.wantDiscount != "" && fakeCreem.checkoutRequests[0].Metadata["discount_code"] != tt.wantDiscount {
				t.Fatalf("metadata discount_code = %#v, want %q", fakeCreem.checkoutRequests[0].Metadata["discount_code"], tt.wantDiscount)
			}
		})
	}
}

func TestSubscriptionCheckoutExistingPlanChangeUsesCreemUpgradeEndpoint(t *testing.T) {
	app := newTestApp(t)
	app.cfg.CreemCheckoutEnabled = true
	app.cfg.CreemAPIKey = "test-creem-key"
	token := registerAndLogin(t, app, "creem-existing-change@example.com")

	var user models.User
	if err := app.db.First(&user, "email = ?", "creem-existing-change@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	pro := models.SubscriptionPlan{Name: "existing-pro", DisplayName: "PRO", MonthlyCredits: 30000, PriceMonthly: 29.9, PriceYearly: 287.04, Currency: "USD", CreemProductIDMonthly: "prod_existing_pro_month", CreemProductIDYearly: "prod_existing_pro_year", IsActive: true}
	max := models.SubscriptionPlan{Name: "existing-max", DisplayName: "MAX", MonthlyCredits: 90000, PriceMonthly: 89.9, PriceYearly: 863.04, Currency: "USD", CreemProductIDMonthly: "prod_existing_max_month", CreemProductIDYearly: "prod_existing_max_year", IsActive: true}
	if err := app.db.Create(&pro).Error; err != nil {
		t.Fatalf("pro plan: %v", err)
	}
	if err := app.db.Create(&max).Error; err != nil {
		t.Fatalf("max plan: %v", err)
	}

	now := time.Now().UTC()
	end := now.AddDate(0, 1, 0)
	upgradeEnd := now.AddDate(0, 1, 0)
	sub := models.UserSubscription{
		UserID:                 user.ID,
		PlanID:                 pro.ID,
		BillingCycle:           billingCycleMonthly,
		Status:                 subscriptionStatusActive,
		CreemCustomerID:        "cust_existing_change",
		CreemSubscriptionID:    "sub_existing_change",
		CreemProductID:         pro.CreemProductIDMonthly,
		CurrentPeriodStart:     &now,
		CurrentPeriodEnd:       &end,
		MonthlyCreditsSnapshot: pro.MonthlyCredits,
	}
	if err := app.db.Create(&sub).Error; err != nil {
		t.Fatalf("subscription: %v", err)
	}
	if err := app.db.Create(&models.SubscriptionCreditGrant{
		UserID:         user.ID,
		SubscriptionID: sub.ID,
		PlanID:         pro.ID,
		CreditAmount:   pro.MonthlyCredits,
		PeriodStart:    now,
		PeriodEnd:      end,
		ExpiresAt:      end,
		IdempotencyKey: "sub_existing_change:" + now.Format("2006-01-02"),
		SourceEventID:  "evt_existing_paid",
		Status:         "active",
	}).Error; err != nil {
		t.Fatalf("grant: %v", err)
	}
	fakeCreem := &fakeCreemClient{
		configured: true,
		upgradeResponse: map[string]any{
			"id":                   "sub_existing_change",
			"product":              map[string]any{"id": max.CreemProductIDMonthly},
			"customer":             map[string]any{"id": "cust_existing_change"},
			"current_period_start": now.Format(time.RFC3339),
			"current_period_end":   upgradeEnd.Format(time.RFC3339),
		},
	}
	app.creem = fakeCreem

	resp := request(app, http.MethodPost, "/api/v1/subscriptions/checkout", map[string]any{
		"plan_id":       max.ID.String(),
		"billing_cycle": billingCycleMonthly,
	}, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("checkout existing subscription change = %d %s", resp.Code, resp.Body.String())
	}
	if fakeCreem.upgradeSubscriptionID != "sub_existing_change" {
		t.Fatalf("upgrade subscription id = %q", fakeCreem.upgradeSubscriptionID)
	}
	if fakeCreem.upgradeProductID != max.CreemProductIDMonthly {
		t.Fatalf("upgrade product id = %q, want %q", fakeCreem.upgradeProductID, max.CreemProductIDMonthly)
	}
	if fakeCreem.upgradeBehavior != "proration-charge-immediately" {
		t.Fatalf("upgrade behavior = %q", fakeCreem.upgradeBehavior)
	}
	if fakeCreem.portalCalls != 0 {
		t.Fatalf("customer portal was called %d times", fakeCreem.portalCalls)
	}

	var body struct {
		Action         string `json:"action"`
		UpdateBehavior string `json:"update_behavior"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Action != "upgraded" || body.UpdateBehavior != "proration-charge-immediately" {
		t.Fatalf("response = %#v", body)
	}

	var upgraded models.UserSubscription
	if err := app.db.First(&upgraded, "id = ?", sub.ID).Error; err != nil {
		t.Fatalf("reload subscription: %v", err)
	}
	if upgraded.PlanID != max.ID || upgraded.CreemProductID != max.CreemProductIDMonthly || upgraded.MonthlyCreditsSnapshot != max.MonthlyCredits {
		t.Fatalf("existing subscription was not upgraded from Creem response: %#v", upgraded)
	}
	var sums struct{ Total int64 }
	if err := app.db.Model(&models.SubscriptionCreditGrant{}).
		Where("user_id = ? AND subscription_id = ? AND status = ?", user.ID, sub.ID, "active").
		Select("coalesce(sum(credit_amount), 0) as total").
		Scan(&sums).Error; err != nil {
		t.Fatalf("grant sum: %v", err)
	}
	if sums.Total != max.MonthlyCredits {
		t.Fatalf("active subscription credits = %d, want %d", sums.Total, max.MonthlyCredits)
	}
}

func TestAdminUsageAggregations(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "stats@example.com")
	var user models.User
	if err := app.db.First(&user, "email = ?", "stats@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	user.IsAdmin = true
	if err := app.db.Save(&user).Error; err != nil {
		t.Fatalf("save admin: %v", err)
	}
	token = loginOnly(t, app, "stats@example.com")
	usage := models.TokenUsageRecord{UserID: user.ID, TokenCount: 123, APIEndpoint: "/api/v1/tools/ocr-images", ModelName: "model-a", BillingMonth: time.Now().Format("2006-01"), RecordedAt: time.Now()}
	if err := app.db.Create(&usage).Error; err != nil {
		t.Fatalf("usage: %v", err)
	}
	modelUsage := request(app, http.MethodGet, "/api/v1/admin/statistics/models/usage", nil, token)
	if modelUsage.Code != http.StatusOK {
		t.Fatalf("model usage = %d %s", modelUsage.Code, modelUsage.Body.String())
	}
	if !bytes.Contains(modelUsage.Body.Bytes(), []byte("model-a")) {
		t.Fatalf("model usage body = %s", modelUsage.Body.String())
	}
	apiUsage := request(app, http.MethodGet, "/api/v1/admin/statistics/apis/usage", nil, token)
	if apiUsage.Code != http.StatusOK {
		t.Fatalf("api usage = %d %s", apiUsage.Code, apiUsage.Body.String())
	}
	if !bytes.Contains(apiUsage.Body.Bytes(), []byte("/api/v1/tools/ocr-images")) {
		t.Fatalf("api usage body = %s", apiUsage.Body.String())
	}
}

func TestCreditsBillingRecordsCostRevenueAndProfit(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLoginWithCredits(t, app, "credits-billing@example.com", 10000)
	var user models.User
	if err := app.db.First(&user, "email = ?", "credits-billing@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	if err := app.db.Create(&models.SystemSetting{
		Key: "billing_rates",
		Value: jsonMap(map[string]any{
			"llm": []map[string]any{{
				"provider":                "openai",
				"model_id":                "model-billing",
				"input_cost_per_million":  10,
				"output_cost_per_million": 20,
				"tokens_per_credit":       1000,
			}},
			"asr": []map[string]any{{
				"provider":           "dashscope",
				"model_id":           "asr-billing",
				"cost_per_minute":    0.5,
				"credits_per_minute": 15,
			}},
		}),
	}).Error; err != nil {
		t.Fatalf("setting: %v", err)
	}
	var account models.UserTokenAccount
	if err := app.db.First(&account, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("account: %v", err)
	}
	account.TotalRecharged = 100
	if err := app.db.Save(&account).Error; err != nil {
		t.Fatalf("save account: %v", err)
	}

	if err := app.consumeCredits(creditConsumption{
		UserID:      user.ID,
		CreditCount: 100,
		APIEndpoint: "/api/v1/tools/generate-script-stream",
		Provider:    "openai",
		ServiceKind: "llm",
		ModelID:     "model-billing",
		Usage:       providers.ChatUsage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500},
	}); err != nil {
		t.Fatalf("consume llm: %v", err)
	}
	duration := 61.0
	if err := app.consumeCredits(creditConsumption{
		UserID:          user.ID,
		CreditCount:     30,
		APIEndpoint:     "/api/v1/tools/qwen3/asr/query",
		Provider:        "dashscope",
		ServiceKind:     "asr",
		ModelID:         "asr-billing",
		DurationSeconds: &duration,
	}); err != nil {
		t.Fatalf("consume asr: %v", err)
	}

	balance := request(app, http.MethodGet, "/api/v1/credit-accounts/balance", nil, token)
	if balance.Code != http.StatusOK {
		t.Fatalf("credit balance = %d %s", balance.Code, balance.Body.String())
	}
	if !bytes.Contains(balance.Body.Bytes(), []byte("remaining_credits")) {
		t.Fatalf("credit balance missing aliases: %s", balance.Body.String())
	}

	var records []models.TokenUsageRecord
	if err := app.db.Order("recorded_at ASC").Find(&records, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("records: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	if records[0].CreditCount != 2 || records[0].ModelID != "model-billing" || records[0].Provider != "openai" || records[0].ServiceKind != "llm" {
		t.Fatalf("llm record mismatch: %#v", records[0])
	}
	if records[0].CostAmount == nil || math.Abs(*records[0].CostAmount-0.02) > 0.000001 {
		t.Fatalf("llm cost = %v, want 0.02", records[0].CostAmount)
	}
	if records[0].RevenueAmount == nil || math.Abs(*records[0].RevenueAmount-0.02) > 0.000001 {
		t.Fatalf("llm revenue = %v, want 0.02", records[0].RevenueAmount)
	}
	if records[0].ProfitAmount == nil || math.Abs(*records[0].ProfitAmount) > 0.000001 {
		t.Fatalf("llm profit = %v, want 0", records[0].ProfitAmount)
	}
	// 61 seconds billed by the second: 61 × 15 / 60 = 15.25 → 16 credits.
	if records[1].CreditCount != 16 {
		t.Fatalf("asr credits = %d, want 16", records[1].CreditCount)
	}
	if records[1].DurationSeconds == nil || *records[1].DurationSeconds != duration {
		t.Fatalf("asr duration = %v, want %v", records[1].DurationSeconds, duration)
	}
	if records[1].CostAmount == nil || math.Abs(*records[1].CostAmount-0.508333) > 0.000001 {
		t.Fatalf("asr cost = %v, want 0.508333", records[1].CostAmount)
	}

	user.IsAdmin = true
	if err := app.db.Save(&user).Error; err != nil {
		t.Fatalf("save admin: %v", err)
	}
	modelUsage := request(app, http.MethodGet, "/api/v1/admin/statistics/models/usage", nil, token)
	if modelUsage.Code != http.StatusOK {
		t.Fatalf("model usage = %d %s", modelUsage.Code, modelUsage.Body.String())
	}
	for _, want := range []string{"model-billing", "asr-billing", "cost_amount", "profit_amount"} {
		if !bytes.Contains(modelUsage.Body.Bytes(), []byte(want)) {
			t.Fatalf("model usage missing %q: %s", want, modelUsage.Body.String())
		}
	}
}

func TestCreditLedgerRecordsBalanceChanges(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLoginWithCredits(t, app, "credit-ledger@example.com", 100)
	var user models.User
	if err := app.db.First(&user, "email = ?", "credit-ledger@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	project := createTestProject(t, app, token, "流水项目快照")
	seedBillingRates(t, app, map[string]any{
		"llm": []map[string]any{{
			"provider":          "openai",
			"model_id":          "ledger-model",
			"tokens_per_credit": 1,
		}},
	})

	if err := app.consumeCredits(creditConsumption{
		UserID:      user.ID,
		ProjectID:   &project.ID,
		CreditCount: 7,
		APIEndpoint: "/api/v1/tools/generate-script-stream",
		Provider:    "openai",
		ServiceKind: "llm",
		ModelID:     "ledger-model",
		Usage:       providers.ChatUsage{PromptTokens: 4, CompletionTokens: 3, TotalTokens: 7},
	}); err != nil {
		t.Fatalf("consume credits: %v", err)
	}

	pkg := models.TokenPackage{
		Name:          "ledger-pack",
		DisplayName:   "测试充值包",
		TokenAmount:   1000,
		BonusTokens:   500,
		Price:         19.9,
		Currency:      "CNY",
		ValidityHours: 720,
		IsActive:      true,
	}
	if err := app.db.Create(&pkg).Error; err != nil {
		t.Fatalf("package: %v", err)
	}
	order, err := createCreditPaymentOrder(app.db, user.ID, pkg.ID, "wechat", time.Now().UTC())
	if err != nil {
		t.Fatalf("create credit payment: %v", err)
	}
	if _, err := settleCreditPaymentOrder(app.db, verifiedCreditPayment{Provider: "wechat", OutTradeNo: order.OutTradeNo, TransactionID: "ledger-transaction", PaidAt: time.Now().UTC(), AmountCNY: decimal.NewFromFloat(pkg.Price)}); err != nil {
		t.Fatalf("settle credit payment: %v", err)
	}

	user.IsAdmin = true
	if err := app.db.Save(&user).Error; err != nil {
		t.Fatalf("save admin: %v", err)
	}
	adjust := request(app, http.MethodPost, "/api/v1/admin/users/"+user.ID.String()+"/credits", map[string]any{
		"amount": int64(100),
		"reason": "测试补偿",
	}, token)
	if adjust.Code != http.StatusOK {
		t.Fatalf("admin adjust = %d %s", adjust.Code, adjust.Body.String())
	}

	if err := app.db.Delete(&models.Project{}, "id = ?", project.ID).Error; err != nil {
		t.Fatalf("delete project: %v", err)
	}

	records := request(app, http.MethodGet, "/api/v1/credit-accounts/ledger/records?per_page=10", nil, token)
	if records.Code != http.StatusOK {
		t.Fatalf("ledger records = %d %s", records.Code, records.Body.String())
	}
	type ledgerItem struct {
		EventType       string         `json:"event_type"`
		ProjectID       *string        `json:"project_id"`
		ProjectName     string         `json:"project_name"`
		Item            string         `json:"item"`
		ChangeAmount    int64          `json:"change_amount"`
		BalanceAfter    int64          `json:"balance_after"`
		Reason          string         `json:"reason"`
		RequestMetadata map[string]any `json:"request_metadata"`
	}
	var got struct {
		Items      []ledgerItem `json:"items"`
		Total      int64        `json:"total"`
		PerPage    int          `json:"per_page"`
		TotalPages int64        `json:"total_pages"`
	}
	if err := json.Unmarshal(records.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode ledger records: %v", err)
	}
	if got.Total != 3 || got.PerPage != 10 || got.TotalPages != 1 || len(got.Items) != 3 {
		t.Fatalf("pagination/items mismatch: %#v", got)
	}
	byType := make(map[string]ledgerItem)
	for _, item := range got.Items {
		byType[item.EventType] = item
	}
	adminRecord := byType["admin_adjustment"]
	if adminRecord.ChangeAmount != 100 || adminRecord.BalanceAfter != 193 || adminRecord.Reason != "测试补偿" {
		t.Fatalf("admin ledger mismatch: %#v", adminRecord)
	}
	rechargeRecord := byType["recharge"]
	if rechargeRecord.ChangeAmount != 1500 || rechargeRecord.BalanceAfter != 1593 || rechargeRecord.Item != "测试充值包" {
		t.Fatalf("recharge ledger mismatch: %#v", rechargeRecord)
	}
	consumeRecord := byType["consume"]
	if consumeRecord.ChangeAmount != -7 || consumeRecord.BalanceAfter != 93 || consumeRecord.Item != creditLedgerConsumeLLMLabel {
		t.Fatalf("consume ledger mismatch: %#v", consumeRecord)
	}
	if consumeRecord.Reason != "" {
		t.Fatalf("consume ledger reason should be redacted: %#v", consumeRecord)
	}
	if len(consumeRecord.RequestMetadata) != 0 {
		t.Fatalf("consume ledger metadata should be redacted: %#v", consumeRecord)
	}
	if consumeRecord.ProjectID == nil || *consumeRecord.ProjectID != project.ID.String() || consumeRecord.ProjectName != "流水项目快照" {
		t.Fatalf("project snapshot mismatch after delete: %#v", consumeRecord)
	}
}

func TestAdminDeleteUserPermanentlyRemovesAccountAndOwnedData(t *testing.T) {
	app := newTestApp(t)
	adminToken := registerAndMakeAdmin(t, app, "admin-delete-user@example.com")
	userToken := registerAndLogin(t, app, "target-delete-user@example.com")
	target := lookupUserByEmail(t, app, "target-delete-user@example.com")
	project := createTestProject(t, app, userToken, "待彻底删除项目")

	if err := app.db.Create(&models.ProjectWorkflow{ProjectID: project.ID}).Error; err != nil {
		t.Fatalf("seed project workflow: %v", err)
	}
	if err := app.db.Create(&models.ProjectWorkflowEvent{ProjectID: project.ID, EventType: "seed", Metadata: jsonMap(nil)}).Error; err != nil {
		t.Fatalf("seed project workflow event: %v", err)
	}
	if err := app.db.Create(&models.TokenUsageRecord{UserID: target.ID, ProjectID: &project.ID, APIEndpoint: "/seed", ModelName: "seed", RecordedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("seed usage record: %v", err)
	}
	if err := app.db.Create(&models.CreditLedgerRecord{UserID: target.ID, ProjectID: &project.ID, EventType: "seed", ChangeAmount: -1, BalanceAfter: 0, RecordedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("seed ledger record: %v", err)
	}
	if err := app.db.Create(&models.Notification{UserID: target.ID, Type: "system", Title: "seed", Message: "seed", Data: jsonMap(nil)}).Error; err != nil {
		t.Fatalf("seed notification: %v", err)
	}
	if err := app.db.Create(&models.OAuthLoginTicket{UserID: target.ID, TicketHash: "delete-ticket", Provider: "google", ExpiresAt: time.Now().UTC().Add(time.Hour)}).Error; err != nil {
		t.Fatalf("seed oauth ticket: %v", err)
	}
	if err := app.db.Create(&models.UploadRecord{UserID: target.ID, ProjectID: &project.ID, OriginalFilename: "seed.mp4", StorageKey: "seed.mp4", ContentType: "video/mp4"}).Error; err != nil {
		t.Fatalf("seed upload: %v", err)
	}

	resp := request(app, http.MethodDelete, "/api/v1/admin/users/"+target.ID.String(), nil, adminToken)
	if resp.Code != http.StatusOK {
		t.Fatalf("hard delete user = %d %s", resp.Code, resp.Body.String())
	}

	for name, model := range map[string]any{
		"users":                   &models.User{},
		"user_profiles":           &models.UserProfile{},
		"user_token_accounts":     &models.UserTokenAccount{},
		"projects":                &models.Project{},
		"project_workflows":       &models.ProjectWorkflow{},
		"project_workflow_events": &models.ProjectWorkflowEvent{},
		"token_usage_records":     &models.TokenUsageRecord{},
		"credit_ledger_records":   &models.CreditLedgerRecord{},
		"notifications":           &models.Notification{},
		"oauth_login_tickets":     &models.OAuthLoginTicket{},
		"upload_records":          &models.UploadRecord{},
	} {
		var count int64
		query := app.db.Unscoped().Model(model)
		if name == "projects" {
			query = query.Where("owner_id = ?", target.ID)
		} else if strings.HasPrefix(name, "project_") {
			query = query.Where("project_id = ?", project.ID)
		} else if name == "users" {
			query = query.Where("id = ?", target.ID)
		} else {
			query = query.Where("user_id = ?", target.ID)
		}
		if err := query.Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("%s still has %d rows for deleted user", name, count)
		}
	}
}

func TestAdminDeleteUserRejectsCurrentAdmin(t *testing.T) {
	app := newTestApp(t)
	adminToken := registerAndMakeAdmin(t, app, "admin-delete-self@example.com")
	admin := lookupUserByEmail(t, app, "admin-delete-self@example.com")

	resp := request(app, http.MethodDelete, "/api/v1/admin/users/"+admin.ID.String(), nil, adminToken)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("delete self status = %d, body = %s", resp.Code, resp.Body.String())
	}

	var count int64
	if err := app.db.Model(&models.User{}).Where("id = ?", admin.ID).Count(&count).Error; err != nil {
		t.Fatalf("count admin: %v", err)
	}
	if count != 1 {
		t.Fatalf("current admin should not be deleted, count=%d", count)
	}
}

func TestCreditLedgerRecordsFilterByProjectSummaryAndLiveName(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLoginWithCredits(t, app, "credit-ledger-project-filter@example.com", 100)
	var user models.User
	if err := app.db.First(&user, "email = ?", "credit-ledger-project-filter@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	project := createTestProject(t, app, token, "旧项目名")
	otherProject := createTestProject(t, app, token, "其他项目")
	seedBillingRates(t, app, map[string]any{
		"llm": []map[string]any{{
			"provider":          "openai",
			"model_id":          "ledger-filter-model",
			"tokens_per_credit": 1,
		}},
		"vision": []map[string]any{{
			"provider":          "openai",
			"model_id":          "ledger-filter-model",
			"tokens_per_credit": 1,
		}},
		"tts": []map[string]any{{
			"provider":             "openai",
			"model_id":             "ledger-filter-model",
			"credits_per_1k_chars": 1000,
		}},
	})

	for _, consumption := range []struct {
		projectID uuid.UUID
		credits   int64
		kind      string
		endpoint  string
	}{
		{projectID: project.ID, credits: 4, kind: "llm", endpoint: "/api/v1/tools/generate-script-stream"},
		{projectID: project.ID, credits: 5, kind: "tts", endpoint: "/api/v1/tools/text-to-speech"},
		{projectID: otherProject.ID, credits: 11, kind: "vision", endpoint: "/api/v1/tools/timeline-view/analyze"},
	} {
		input := creditConsumption{
			UserID:      user.ID,
			ProjectID:   &consumption.projectID,
			APIEndpoint: consumption.endpoint,
			Provider:    "openai",
			ServiceKind: consumption.kind,
			ModelID:     "ledger-filter-model",
		}
		switch consumption.kind {
		case "llm", "vision":
			input.Usage = providers.ChatUsage{TotalTokens: consumption.credits}
		case "tts":
			input.CharacterCount = consumption.credits
		}
		if err := app.consumeCredits(input); err != nil {
			t.Fatalf("consume credits: %v", err)
		}
	}

	if err := app.db.Model(&models.Project{}).Where("id = ?", project.ID).Update("name", "最新项目名").Error; err != nil {
		t.Fatalf("rename project: %v", err)
	}

	resp := request(app, http.MethodGet, "/api/v1/credit-accounts/ledger/records?project_id="+url.QueryEscape(project.ID.String())+"&per_page=1", nil, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("ledger records = %d %s", resp.Code, resp.Body.String())
	}

	type ledgerItem struct {
		ProjectID    *string `json:"project_id"`
		ProjectName  string  `json:"project_name"`
		ChangeAmount int64   `json:"change_amount"`
	}
	var got struct {
		Items      []ledgerItem `json:"items"`
		Total      int64        `json:"total"`
		PerPage    int          `json:"per_page"`
		TotalPages int64        `json:"total_pages"`
		Summary    struct {
			Income               int64  `json:"income"`
			Expense              int64  `json:"expense"`
			Net                  int64  `json:"net"`
			TotalConsumedCredits int64  `json:"total_consumed_credits"`
			ProjectID            string `json:"project_id"`
			ProjectName          string `json:"project_name"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode ledger records: %v", err)
	}
	if got.Total != 2 || got.PerPage != 1 || got.TotalPages != 2 || len(got.Items) != 1 {
		t.Fatalf("pagination/items mismatch: %#v", got)
	}
	item := got.Items[0]
	if item.ProjectID == nil || *item.ProjectID != project.ID.String() || item.ProjectName != "最新项目名" {
		t.Fatalf("project display mismatch: %#v", item)
	}
	if got.Summary.Income != 0 || got.Summary.Expense != 9 || got.Summary.Net != -9 || got.Summary.TotalConsumedCredits != 9 {
		t.Fatalf("summary mismatch: %#v", got.Summary)
	}
	if got.Summary.ProjectID != project.ID.String() || got.Summary.ProjectName != "最新项目名" {
		t.Fatalf("summary project mismatch: %#v", got.Summary)
	}
}

func TestCreditLedgerRecordsSummaryCountsConsumeOnly(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "credit-ledger-summary@example.com")
	var user models.User
	if err := app.db.First(&user, "email = ?", "credit-ledger-summary@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}

	now := time.Now().UTC()
	for _, entry := range []creditLedgerEntry{
		{
			UserID:       user.ID,
			EventType:    creditLedgerEventConsume,
			Item:         creditLedgerConsumeLLMLabel,
			ChangeAmount: -850,
			BalanceAfter: 1000,
			RecordedAt:   now,
		},
		{
			UserID:       user.ID,
			EventType:    creditLedgerEventAdminAdjustment,
			Item:         "管理员调整",
			ChangeAmount: -99940647,
			BalanceAfter: 0,
			Reason:       "余额校准",
			RecordedAt:   now.Add(time.Second),
		},
		{
			UserID:       user.ID,
			EventType:    creditLedgerEventRecharge,
			Item:         "测试充值",
			ChangeAmount: 30000,
			BalanceAfter: 30000,
			RecordedAt:   now.Add(2 * time.Second),
		},
	} {
		if err := app.createCreditLedgerRecord(nil, entry); err != nil {
			t.Fatalf("create ledger entry: %v", err)
		}
	}

	resp := request(app, http.MethodGet, "/api/v1/credit-accounts/ledger/records?per_page=10", nil, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("ledger records = %d %s", resp.Code, resp.Body.String())
	}

	var got struct {
		Total   int64 `json:"total"`
		Summary struct {
			Income               int64 `json:"income"`
			Expense              int64 `json:"expense"`
			Net                  int64 `json:"net"`
			TotalConsumedCredits int64 `json:"total_consumed_credits"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode ledger records: %v", err)
	}
	if got.Total != 3 {
		t.Fatalf("total = %d, want 3", got.Total)
	}
	if got.Summary.Income != 30000 || got.Summary.Net != -99911497 {
		t.Fatalf("income/net mismatch: %#v", got.Summary)
	}
	if got.Summary.Expense != 850 || got.Summary.TotalConsumedCredits != 850 {
		t.Fatalf("consume-only summary mismatch: %#v", got.Summary)
	}
}

func TestCreditLedgerRecordsClassifyLegacyConsumeItems(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "credit-ledger-legacy@example.com")
	var user models.User
	if err := app.db.First(&user, "email = ?", "credit-ledger-legacy@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}

	now := time.Now().UTC()
	for _, entry := range []creditLedgerEntry{
		{
			UserID:       user.ID,
			EventType:    creditLedgerEventConsume,
			Item:         "qwen3-vl-plus",
			ChangeAmount: -2,
			BalanceAfter: 998,
			Reason:       "/api/v1/tools/timeline-view/analyze",
			RequestMetadata: map[string]any{
				"billing_service_kind": "vision",
				"billing_model_id":     "qwen3-vl-plus",
			},
			RecordedAt: now,
		},
		{
			UserID:       user.ID,
			EventType:    creditLedgerEventConsume,
			Item:         "scribe_v2",
			ChangeAmount: -1,
			BalanceAfter: 997,
			Reason:       "/api/v1/tools/elevenlabs/scribe/transcribe",
			RequestMetadata: map[string]any{
				"billing_model_id": "scribe_v2",
			},
			RecordedAt: now.Add(time.Second),
		},
	} {
		if err := app.createCreditLedgerRecord(nil, entry); err != nil {
			t.Fatalf("create legacy ledger entry: %v", err)
		}
	}

	resp := request(app, http.MethodGet, "/api/v1/credit-accounts/ledger/records?per_page=10", nil, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("ledger records = %d %s", resp.Code, resp.Body.String())
	}

	type ledgerItem struct {
		Item            string         `json:"item"`
		ChangeAmount    int64          `json:"change_amount"`
		Reason          string         `json:"reason"`
		RequestMetadata map[string]any `json:"request_metadata"`
	}
	var body struct {
		Items []ledgerItem `json:"items"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode ledger records: %v", err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("items mismatch: %#v", body.Items)
	}

	byAmount := make(map[int64]ledgerItem)
	for _, item := range body.Items {
		byAmount[item.ChangeAmount] = item
	}
	if got := byAmount[-2]; got.Item != creditLedgerConsumeVisionLabel || got.Reason != "" || len(got.RequestMetadata) != 0 {
		t.Fatalf("vision ledger mismatch: %#v", got)
	}
	if got := byAmount[-1]; got.Item != creditLedgerConsumeASRLabel || got.Reason != "" || len(got.RequestMetadata) != 0 {
		t.Fatalf("asr ledger mismatch: %#v", got)
	}
}

func TestBillingRateTokenCreditRatioParsing(t *testing.T) {
	cases := []struct {
		name       string
		raw        map[string]any
		want       float64
		wantInput  float64
		wantOutput float64
	}{
		{
			name:       "new tokens per credit field",
			raw:        map[string]any{"tokens_per_credit": 1000},
			want:       1000,
			wantInput:  1000,
			wantOutput: 1000,
		},
		{
			name:       "legacy fractional credits per token field",
			raw:        map[string]any{"credits_per_token": 0.001},
			want:       1000,
			wantInput:  1000,
			wantOutput: 1000,
		},
		{
			name:       "legacy admin value entered as tokens per credit",
			raw:        map[string]any{"credits_per_token": 1000},
			want:       1000,
			wantInput:  1000,
			wantOutput: 1000,
		},
		{
			name: "directional token ratios",
			raw: map[string]any{
				"tokens_per_credit":        100,
				"input_tokens_per_credit":  1000,
				"output_tokens_per_credit": 50,
			},
			want:       100,
			wantInput:  1000,
			wantOutput: 50,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rate := parseBillingRate(tc.raw, "llm")
			if math.Abs(rate.TokensPerCredit-tc.want) > 0.000001 {
				t.Fatalf("tokens per credit = %v, want %v", rate.TokensPerCredit, tc.want)
			}
			if math.Abs(rate.InputTokensPerCredit-tc.wantInput) > 0.000001 {
				t.Fatalf("input tokens per credit = %v, want %v", rate.InputTokensPerCredit, tc.wantInput)
			}
			if math.Abs(rate.OutputTokensPerCredit-tc.wantOutput) > 0.000001 {
				t.Fatalf("output tokens per credit = %v, want %v", rate.OutputTokensPerCredit, tc.wantOutput)
			}
		})
	}
}

func TestLLMBillingUsesSeparateInputAndOutputTokenCreditRatios(t *testing.T) {
	credits, err := deriveCreditCount(creditConsumption{
		ServiceKind: "llm",
		Usage: providers.ChatUsage{
			PromptTokens:     1000,
			CompletionTokens: 500,
			TotalTokens:      1500,
		},
	}, billingRate{
		InputTokensPerCredit:  1000,
		OutputTokensPerCredit: 100,
		TokensPerCredit:       1000,
	})
	if err != nil {
		t.Fatalf("derive credit count: %v", err)
	}
	if credits != 6 {
		t.Fatalf("credits = %d, want 6", credits)
	}
}

func TestBillingRateASRMinuteRatioParsing(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
		want float64
	}{
		{
			name: "new credits per minute field",
			raw:  map[string]any{"credits_per_minute": 15},
			want: 15,
		},
		{
			name: "legacy credits per second field",
			raw:  map[string]any{"credits_per_second": 0.25},
			want: 15,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rate := parseBillingRate(tc.raw, "asr")
			if math.Abs(rate.CreditsPerMinute-tc.want) > 0.000001 {
				t.Fatalf("credits per minute = %v, want %v", rate.CreditsPerMinute, tc.want)
			}
		})
	}
}

func TestBillingRateModelIDMatchingNormalizesCaseAndSeparators(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLoginWithCredits(t, app, "scribe-alias-billing@example.com", 100)
	var user models.User
	if err := app.db.First(&user, "email = ?", "scribe-alias-billing@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	if err := app.db.Create(&models.SystemSetting{
		Key: "billing_rates",
		Value: jsonMap(map[string]any{
			"asr": []map[string]any{{
				"provider":           "elevenlabs",
				"model_id":           "Scribe-v2",
				"cost_per_minute":    0.25,
				"credits_per_minute": 1,
			}},
		}),
	}).Error; err != nil {
		t.Fatalf("setting: %v", err)
	}

	duration := 61.0
	if err := app.consumeCredits(creditConsumption{
		UserID:          user.ID,
		CreditCount:     999,
		APIEndpoint:     "/api/v1/tools/elevenlabs/scribe/transcribe",
		Provider:        "elevenlabs",
		ServiceKind:     "asr",
		ModelID:         "scribe_v2",
		DurationSeconds: &duration,
	}); err != nil {
		t.Fatalf("consume scribe alias: %v", err)
	}

	balance := request(app, http.MethodGet, "/api/v1/credit-accounts/balance", nil, token)
	if balance.Code != http.StatusOK {
		t.Fatalf("credit balance = %d %s", balance.Code, balance.Body.String())
	}
	var record models.TokenUsageRecord
	if err := app.db.First(&record, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("record: %v", err)
	}
	// 61 seconds at 1 credit per minute, billed by the second: 61/60 → 2 credits.
	if record.CreditCount != 2 {
		t.Fatalf("credits = %d, want normalized Scribe-v2 rule to bill 2", record.CreditCount)
	}
	if record.MissingPricing {
		t.Fatalf("scribe alias should not be missing pricing")
	}
	if record.CostAmount == nil || math.Abs(*record.CostAmount-0.254167) > 0.000001 {
		t.Fatalf("cost = %v, want 0.254167 (61 s × 0.25/60)", record.CostAmount)
	}
}

func TestBillingRateModelIDMatchingHandlesProviderPathsAndDateVersions(t *testing.T) {
	cases := []struct {
		configured string
		actual     string
		want       bool
	}{
		{configured: "qwen3-vl-flash", actual: "qwen3-vl-flash-2026-01-22", want: true},
		{configured: "qwen3-vl-flash", actual: "dashscope/qwen3-vl-flash-2026-01-22", want: true},
		{configured: "qwen3-vl-plus", actual: "qwen3-vl-flash-2026-01-22", want: false},
		{configured: "qwen3-vl-flash-us", actual: "qwen3-vl-flash-2026-01-22-us", want: true},
	}

	for _, tc := range cases {
		t.Run(tc.configured+" matches "+tc.actual, func(t *testing.T) {
			if got := billingModelIDMatches(tc.configured, tc.actual); got != tc.want {
				t.Fatalf("billingModelIDMatches(%q, %q) = %v, want %v", tc.configured, tc.actual, got, tc.want)
			}
		})
	}
}

func TestVisionBillingUsesConfiguredRateForVersionedDashScopeModel(t *testing.T) {
	app := newTestApp(t)
	registerAndLoginWithCredits(t, app, "vision-versioned-billing@example.com", 10000)
	var user models.User
	if err := app.db.First(&user, "email = ?", "vision-versioned-billing@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	if err := app.db.Create(&models.SystemSetting{
		Key: "billing_rates",
		Value: jsonMap(map[string]any{
			"vision": []map[string]any{{
				"provider":                "dashscope",
				"model_id":                "qwen3-vl-flash",
				"input_cost_per_million":  1.5,
				"output_cost_per_million": 1.5,
				"tokens_per_credit":       3333.333333,
			}},
		}),
	}).Error; err != nil {
		t.Fatalf("setting: %v", err)
	}

	if err := app.consumeCredits(creditConsumption{
		UserID:      user.ID,
		CreditCount: 1922,
		APIEndpoint: "/api/v1/tools/timeline-view/analyze",
		Provider:    "dashscope",
		ServiceKind: "vision",
		ModelID:     "dashscope/qwen3-vl-flash-2026-01-22",
		Usage:       providers.ChatUsage{PromptTokens: 1800, CompletionTokens: 122, TotalTokens: 1922},
	}); err != nil {
		t.Fatalf("consume vision: %v", err)
	}

	var account models.UserTokenAccount
	if err := app.db.First(&account, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.UsedTokens != 1 {
		t.Fatalf("used credits = %d, want versioned qwen3-vl-flash rule to bill 1", account.UsedTokens)
	}
	var record models.TokenUsageRecord
	if err := app.db.First(&record, "user_id = ? AND service_kind = ?", user.ID, "vision").Error; err != nil {
		t.Fatalf("record: %v", err)
	}
	if record.CreditCount != 1 || record.MissingPricing {
		t.Fatalf("vision billing record mismatch: %#v", record)
	}
	if !bytes.Contains(record.RequestMetadata, []byte(`"tokens_per_credit":3333.333333`)) {
		t.Fatalf("metadata should include matched billing rate: %s", string(record.RequestMetadata))
	}
}

func TestBillingRejectsMissingProviderServiceModelRate(t *testing.T) {
	app := newTestApp(t)
	registerAndLoginWithCredits(t, app, "vision-missing-rate-billing@example.com", 10000)
	var user models.User
	if err := app.db.First(&user, "email = ?", "vision-missing-rate-billing@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	if err := app.db.Create(&models.SystemSetting{
		Key: "billing_rates",
		Value: jsonMap(map[string]any{
			"vision": []map[string]any{{
				"provider":                "dashscope",
				"model_id":                "qwen3-vl-flash",
				"input_cost_per_million":  1.5,
				"output_cost_per_million": 1.5,
				"tokens_per_credit":       3333.333333,
			}},
		}),
	}).Error; err != nil {
		t.Fatalf("setting: %v", err)
	}

	err := app.consumeCredits(creditConsumption{
		UserID:      user.ID,
		APIEndpoint: "/api/v1/tools/timeline-view/analyze",
		Provider:    "dashscope",
		ServiceKind: "vision",
		ModelID:     "qwen3-vl-plus",
		Usage:       providers.ChatUsage{PromptTokens: 1236, CompletionTokens: 888, TotalTokens: 2124},
	})
	if err == nil || !strings.Contains(err.Error(), "未配置计费规则") {
		t.Fatalf("consume vision error = %v, want missing billing rate", err)
	}

	var count int64
	if err := app.db.Model(&models.TokenUsageRecord{}).Where("user_id = ? AND service_kind = ?", user.ID, "vision").Count(&count).Error; err != nil {
		t.Fatalf("count records: %v", err)
	}
	if count != 0 {
		t.Fatalf("usage records = %d, want 0 when billing rate is missing", count)
	}
	var account models.UserTokenAccount
	if err := app.db.First(&account, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.UsedTokens != 0 {
		t.Fatalf("used credits = %d, want 0 when billing rate is missing", account.UsedTokens)
	}
}

func TestBillingProviderAliasesMatchConfiguredRates(t *testing.T) {
	app := newTestApp(t)
	registerAndLoginWithCredits(t, app, "provider-alias-billing@example.com", 100)
	var user models.User
	if err := app.db.First(&user, "email = ?", "provider-alias-billing@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	if err := app.db.Create(&models.SystemSetting{
		Key: "billing_rates",
		Value: jsonMap(map[string]any{
			"tts": []map[string]any{{
				"provider":                   "fish-audio",
				"model_id":                   "S2 Pro",
				"cost_per_million_chars":     101.9,
				"credits_per_thousand_chars": 20.38,
			}},
		}),
	}).Error; err != nil {
		t.Fatalf("setting: %v", err)
	}

	duration := 2.0
	if err := app.consumeCredits(creditConsumption{
		UserID:          user.ID,
		CreditCount:     99,
		APIEndpoint:     "/api/v1/tools/text-to-speech-with-uri",
		Provider:        "fishaudio",
		ServiceKind:     "tts",
		ModelID:         "S2 Pro",
		CharacterCount:  12,
		DurationSeconds: &duration,
	}); err != nil {
		t.Fatalf("consume tts: %v", err)
	}

	var record models.TokenUsageRecord
	if err := app.db.First(&record, "user_id = ? AND service_kind = ?", user.ID, "tts").Error; err != nil {
		t.Fatalf("record: %v", err)
	}
	if record.Provider != "fishaudio" || record.CreditCount != 1 || record.MissingPricing {
		t.Fatalf("provider alias billing record mismatch: %#v", record)
	}
}

func TestTTSBillingCostUsesUTF8Bytes(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLoginWithCredits(t, app, "tts-byte-billing@example.com", 100)
	var user models.User
	if err := app.db.First(&user, "email = ?", "tts-byte-billing@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	if err := app.db.Create(&models.SystemSetting{
		Key: "billing_rates",
		Value: jsonMap(map[string]any{
			"tts": []map[string]any{{
				"provider":                    "siliconflow",
				"model_id":                    "tts-byte-model",
				"cost_per_million_utf8_bytes": 15,
				"credits_per_1k_utf8_bytes":   3000,
				"credits_per_second":          10,
			}},
		}),
	}).Error; err != nil {
		t.Fatalf("setting: %v", err)
	}

	text := "你好a"
	utf8Bytes := int64(len([]byte(text)))
	duration := 2.0
	if err := app.consumeCredits(creditConsumption{
		UserID:          user.ID,
		CreditCount:     1,
		APIEndpoint:     "/api/v1/tools/text-to-speech-with-uri",
		Provider:        "siliconflow",
		ServiceKind:     "tts",
		ModelID:         "tts-byte-model",
		UTF8Bytes:       utf8Bytes,
		DurationSeconds: &duration,
		RequestMetadata: map[string]any{"text_length": len([]rune(text))},
	}); err != nil {
		t.Fatalf("consume tts: %v", err)
	}

	var record models.TokenUsageRecord
	if err := app.db.First(&record, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("record: %v", err)
	}
	if record.UTF8Bytes != utf8Bytes {
		t.Fatalf("utf8 bytes = %d, want %d", record.UTF8Bytes, utf8Bytes)
	}
	if record.CreditCount != 21 {
		t.Fatalf("credits = %d, want utf8-byte credits 21", record.CreditCount)
	}
	wantCost := 0.000105
	if record.CostAmount == nil || math.Abs(*record.CostAmount-wantCost) > 0.000001 {
		t.Fatalf("tts cost = %v, want %v", record.CostAmount, wantCost)
	}
	if record.MissingPricing {
		t.Fatalf("tts cost should not be marked missing pricing")
	}

	user.IsAdmin = true
	if err := app.db.Save(&user).Error; err != nil {
		t.Fatalf("save admin: %v", err)
	}
	modelUsage := request(app, http.MethodGet, "/api/v1/admin/statistics/models/usage", nil, token)
	if modelUsage.Code != http.StatusOK {
		t.Fatalf("model usage = %d %s", modelUsage.Code, modelUsage.Body.String())
	}
	for _, want := range []string{"tts-byte-model", "utf8_bytes", "cost_amount", "CNY"} {
		if !bytes.Contains(modelUsage.Body.Bytes(), []byte(want)) {
			t.Fatalf("model usage missing %q: %s", want, modelUsage.Body.String())
		}
	}
}

func TestTTSBillingCostUsesCharacterPricingWhenConfigured(t *testing.T) {
	app := newTestApp(t)
	registerAndLoginWithCredits(t, app, "tts-char-billing@example.com", 100)
	var user models.User
	if err := app.db.First(&user, "email = ?", "tts-char-billing@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	if err := app.db.Create(&models.SystemSetting{
		Key: "billing_rates",
		Value: jsonMap(map[string]any{
			"tts": []map[string]any{{
				"provider":               "siliconflow",
				"model_id":               "tts-char-model",
				"cost_per_million_chars": 30,
				"credits_per_1k_chars":   2000,
			}},
		}),
	}).Error; err != nil {
		t.Fatalf("setting: %v", err)
	}

	text := "你好a"
	duration := 2.0
	if err := app.consumeCredits(creditConsumption{
		UserID:          user.ID,
		CreditCount:     1,
		APIEndpoint:     "/api/v1/tools/text-to-speech-with-uri",
		Provider:        "siliconflow",
		ServiceKind:     "tts",
		ModelID:         "tts-char-model",
		UTF8Bytes:       int64(len([]byte(text))),
		DurationSeconds: &duration,
		RequestMetadata: map[string]any{"text_length": len([]rune(text))},
	}); err != nil {
		t.Fatalf("consume tts: %v", err)
	}

	var record models.TokenUsageRecord
	if err := app.db.First(&record, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("record: %v", err)
	}
	if record.CreditCount != 6 {
		t.Fatalf("credits = %d, want character-based credits 6", record.CreditCount)
	}
	wantCost := 0.00009
	if record.CostAmount == nil || math.Abs(*record.CostAmount-wantCost) > 0.000001 {
		t.Fatalf("tts cost = %v, want %v", record.CostAmount, wantCost)
	}
	if !bytes.Contains(record.RequestMetadata, []byte(`"billing_cost_unit":"million_chars"`)) {
		t.Fatalf("metadata should record character billing unit: %s", string(record.RequestMetadata))
	}
}

func TestSiliconFlowBasicTTSReservesCreditsFromBillingRates(t *testing.T) {
	siliconServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/audio/speech" {
			t.Fatalf("unexpected siliconflow request %s %s", r.Method, r.URL.Path)
		}

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode siliconflow payload: %v", err)
		}
		if got := strings.TrimSpace(payload["model"].(string)); got != "FunAudioLLM/CosyVoice2-0.5B" {
			t.Fatalf("model = %q, want FunAudioLLM/CosyVoice2-0.5B", got)
		}
		if got := strings.TrimSpace(payload["voice"].(string)); got != "speech:test-basic" {
			t.Fatalf("voice = %q, want speech:test-basic", got)
		}

		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("fake-mp3"))
	}))
	defer siliconServer.Close()

	app := newTestApp(t)
	app.silicon = providers.NewSiliconFlowClient(config.Config{
		SiliconFlowAPIKey:   "silicon-test-key",
		SiliconFlowBaseURL:  siliconServer.URL,
		SiliconFlowTTSModel: "FunAudioLLM/CosyVoice2-0.5B",
	})

	token := registerAndLoginWithCredits(t, app, "basic-tts-billing@example.com", 100)
	var user models.User
	if err := app.db.First(&user, "email = ?", "basic-tts-billing@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	project := createTestProject(t, app, token, "TTS 项目")
	if err := app.db.Create(&models.SystemSetting{
		Key: "billing_rates",
		Value: jsonMap(map[string]any{
			"tts": []map[string]any{{
				"provider":                    "siliconflow",
				"model_id":                    "FunAudioLLM/CosyVoice2-0.5B",
				"cost_per_million_utf8_bytes": 15,
				"credits_per_1k_utf8_bytes":   3000,
			}},
		}),
	}).Error; err != nil {
		t.Fatalf("setting: %v", err)
	}

	resp := request(app, http.MethodPost, "/api/v1/tools/text-to-speech-with-uri", map[string]any{
		"text":          "你好a",
		"voice_uri":     "speech:test-basic",
		"service_level": "basic",
		"project_id":    project.ID.String(),
	}, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("basic tts status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if contentType := resp.Header().Get("Content-Type"); !strings.Contains(contentType, "audio/mpeg") {
		t.Fatalf("content-type = %q, want audio/mpeg", contentType)
	}

	var reservation models.CreditReservation
	if err := app.db.First(&reservation, "user_id = ? AND service_kind = ?", user.ID, "tts").Error; err != nil {
		t.Fatalf("reservation: %v", err)
	}
	if reservation.Status != creditReservationPending {
		t.Fatalf("reservation status = %s, want pending", reservation.Status)
	}
	if reservation.Provider != "siliconflow" || reservation.ModelID != "FunAudioLLM/CosyVoice2-0.5B" {
		t.Fatalf("billing reservation provider/model mismatch: %#v", reservation)
	}
	if reservation.ProjectID == nil || *reservation.ProjectID != project.ID {
		t.Fatalf("billing reservation project_id = %v, want %s", reservation.ProjectID, project.ID)
	}
	if reservation.UTF8Bytes != int64(len([]byte("你好a"))) {
		t.Fatalf("utf8 bytes = %d, want %d", reservation.UTF8Bytes, len([]byte("你好a")))
	}
	if reservation.CreditCount != 21 {
		t.Fatalf("credits = %d, want 21", reservation.CreditCount)
	}
	if reservation.MissingPricing {
		t.Fatalf("basic siliconflow tts should not be marked missing pricing")
	}

	var usageCount int64
	if err := app.db.Model(&models.TokenUsageRecord{}).Where("user_id = ? AND service_kind = ?", user.ID, "tts").Count(&usageCount).Error; err != nil {
		t.Fatalf("usage count: %v", err)
	}
	if usageCount != 0 {
		t.Fatalf("usage records = %d, want 0 before project completion", usageCount)
	}
	var ledgerCount int64
	if err := app.db.Model(&models.CreditLedgerRecord{}).Where("user_id = ? AND event_type = ?", user.ID, creditLedgerEventConsume).Count(&ledgerCount).Error; err != nil {
		t.Fatalf("ledger count: %v", err)
	}
	if ledgerCount != 0 {
		t.Fatalf("ledger records = %d, want 0 before project completion", ledgerCount)
	}
}

func TestFishAudioPremiumReferenceVoiceAndDelete(t *testing.T) {
	fishServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/model":
			_ = r.ParseMultipartForm(1024 * 1024)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "fish-voice-1", "title": "Fish Voice"})
		case r.Method == http.MethodDelete && r.URL.Path == "/model/fish-voice-1":
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected fish request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer fishServer.Close()

	app := newTestApp(t)
	app.fish = providers.NewFishAudioClient(config.Config{FishAudioAPIKey: "fish", FishAudioBaseURL: fishServer.URL, FishAudioModel: "s1"})
	token := registerAndLogin(t, app, "fish@example.com")

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "voice.mp3")
	_, _ = io.Copy(part, bytes.NewReader([]byte("audio")))
	_ = writer.WriteField("service_level", "premium")
	_ = writer.WriteField("audio_text", "hello")
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/upload-reference-voice", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("upload voice status = %d, body = %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("fish-voice-1")) {
		t.Fatalf("upload body = %s", w.Body.String())
	}

	deleteResp := request(app, http.MethodDelete, "/api/v1/tools/voice/fish-voice-1?provider=fishaudio", nil, token)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("delete voice status = %d, body = %s", deleteResp.Code, deleteResp.Body.String())
	}
}

func TestElevenLabsScribeTranscribeEndpoint(t *testing.T) {
	scribeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/speech-to-text":
			_ = r.ParseMultipartForm(1024 * 1024)
			for key, want := range map[string]string{
				"model_id":               "scribe_v2",
				"diarize":                "true",
				"tag_audio_events":       "true",
				"timestamps_granularity": "word",
				"webhook":                "true",
			} {
				if got := r.FormValue(key); got != want {
					t.Fatalf("%s = %q, want %q", key, got, want)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"transcription_id": "transcript-1",
				"request_id":       "request-1",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/speech-to-text/transcripts/transcript-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"language_code": "en",
				"text":          "Hello (laughter)",
				"words": []map[string]any{
					{"type": "word", "text": "Hello", "start": 0.0, "end": 0.3, "speaker_id": "speaker_0"},
					{"type": "audio_event", "text": "laughter", "start": 0.4, "end": 0.8, "speaker_id": "speaker_0"},
				},
			})
		default:
			t.Fatalf("unexpected elevenlabs request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer scribeServer.Close()

	app := newTestApp(t)
	app.elevenlabs = providers.NewElevenLabsClient(config.Config{ElevenLabsAPIKey: "eleven", ElevenLabsBaseURL: scribeServer.URL, ElevenLabsScribeModel: "scribe_v2"})
	seedBillingRates(t, app, map[string]any{
		"asr": []map[string]any{{
			"provider":           "elevenlabs",
			"model_id":           "scribe_v2",
			"credits_per_minute": 1,
		}},
	})
	token := registerAndLoginWithCredits(t, app, "scribe@example.com", 100)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "take.mp3")
	_, _ = io.Copy(part, bytes.NewReader([]byte("audio")))
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/elevenlabs/scribe/transcribe", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("scribe status = %d, body = %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("transcript-1")) || !bytes.Contains(w.Body.Bytes(), []byte(`"status":"processing"`)) {
		t.Fatalf("scribe submit body = %s", w.Body.String())
	}

	queryResp := request(app, http.MethodGet, "/api/v1/tools/elevenlabs/scribe/transcribe/transcript-1", nil, token)
	if queryResp.Code != http.StatusOK {
		t.Fatalf("scribe query status = %d, body = %s", queryResp.Code, queryResp.Body.String())
	}
	for _, want := range []string{"raw_result", "packed_transcript", "audio_events", "Hello (laughter)"} {
		if !bytes.Contains(queryResp.Body.Bytes(), []byte(want)) {
			t.Fatalf("scribe query body missing %q: %s", want, queryResp.Body.String())
		}
	}
}

func TestFunASRQueryBillsOnceAndKeepsProjectID(t *testing.T) {
	dashscopeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/task-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"output": map[string]any{
					"task_status": "SUCCEEDED",
					"result": map[string]any{
						"transcription_url": "http://" + r.Host + "/result.json",
					},
				},
				"usage": map[string]any{"duration": 2},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/result.json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"transcripts": []map[string]any{{
					"channel_id": 0,
					"text":       "你好世界",
					"sentences": []map[string]any{{
						"begin_time": 0,
						"end_time":   2000,
						"text":       "你好世界",
						"language":   "zh",
						"speaker_id": "speaker_0",
					}},
				}},
			})
		default:
			t.Fatalf("unexpected dashscope request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer dashscopeServer.Close()
	t.Cleanup(providers.SetDashScopeAPIBaseURLForTest(dashscopeServer.URL))

	app := newTestApp(t)
	app.cfg.FunASRModel = "fun-asr"
	app.dashscope = providers.NewDashScopeClient(config.Config{
		DashScopeAPIKey: "dash",
		FunASRModel:     "fun-asr",
	})
	seedBillingRates(t, app, map[string]any{
		"asr": []map[string]any{{
			"provider":           "dashscope",
			"model_id":           "fun-asr",
			"credits_per_minute": 1,
		}},
	})
	token := registerAndLoginWithCredits(t, app, "funasr-billing@example.com", 100)
	project := createTestProject(t, app, token, "FunASR project")

	path := "/api/v1/tools/funasr/asr/query/task-1?project_id=" + url.QueryEscape(project.ID.String())
	first := request(app, http.MethodGet, path, nil, token)
	if first.Code != http.StatusOK {
		t.Fatalf("first funasr query status = %d, body = %s", first.Code, first.Body.String())
	}
	second := request(app, http.MethodGet, path, nil, token)
	if second.Code != http.StatusOK {
		t.Fatalf("second funasr query status = %d, body = %s", second.Code, second.Body.String())
	}

	var reservations []models.CreditReservation
	if err := app.db.Find(&reservations, "api_endpoint = ? AND user_id = ?", "/api/v1/tools/funasr/asr/query", lookupUserByEmail(t, app, "funasr-billing@example.com").ID).Error; err != nil {
		t.Fatalf("reservations: %v", err)
	}
	if len(reservations) != 1 {
		t.Fatalf("reservations = %d, want 1", len(reservations))
	}
	if reservations[0].ProjectID == nil || *reservations[0].ProjectID != project.ID {
		t.Fatalf("reservation project_id = %v, want %s", reservations[0].ProjectID, project.ID)
	}
	if reservations[0].Provider != "dashscope" || reservations[0].ModelID != "fun-asr" || reservations[0].ServiceKind != "asr" || reservations[0].Status != creditReservationPending {
		t.Fatalf("reservation mismatch: %#v", reservations[0])
	}
}

func TestFunASRSubmitRejectsInsufficientCredits(t *testing.T) {
	dashscopeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("DashScope submit should not be called when credits are insufficient: %s %s", r.Method, r.URL.Path)
	}))
	defer dashscopeServer.Close()
	t.Cleanup(providers.SetDashScopeAPIBaseURLForTest(dashscopeServer.URL))

	app := newTestApp(t)
	app.cfg.FunASRModel = "fun-asr"
	app.dashscope = providers.NewDashScopeClient(config.Config{
		DashScopeAPIKey: "dash",
		FunASRModel:     "fun-asr",
	})
	seedBillingRates(t, app, map[string]any{
		"asr": []map[string]any{{
			"provider":           "dashscope",
			"model_id":           "fun-asr",
			"credits_per_minute": 1,
		}},
	})
	token := registerAndLoginWithCredits(t, app, "funasr-submit-empty@example.com", 0)

	resp := request(app, http.MethodPost, "/api/v1/tools/funasr/asr/submit", map[string]any{
		"file_url": "oss://audio.wav",
	}, token)
	if resp.Code != http.StatusPaymentRequired {
		t.Fatalf("submit status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if !bytes.Contains(resp.Body.Bytes(), []byte(`"required_credits":1`)) || !bytes.Contains(resp.Body.Bytes(), []byte(`"remaining_credits":0`)) {
		t.Fatalf("submit rejection body = %s", resp.Body.String())
	}
}

func TestFunASRQueryRejectsInsufficientCreditsWhenDurationMissing(t *testing.T) {
	dashscopeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/tasks/task-zero-duration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"output": map[string]any{
					"task_status": "SUCCEEDED",
					"result": map[string]any{
						"transcription_url": "http://" + r.Host + "/result-zero-duration.json",
					},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/result-zero-duration.json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"transcripts": []map[string]any{{
					"channel_id": 0,
					"text":       "没有时长信息的转录结果",
				}},
			})
		default:
			t.Fatalf("unexpected dashscope request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer dashscopeServer.Close()
	t.Cleanup(providers.SetDashScopeAPIBaseURLForTest(dashscopeServer.URL))

	app := newTestApp(t)
	app.cfg.FunASRModel = "fun-asr"
	app.dashscope = providers.NewDashScopeClient(config.Config{
		DashScopeAPIKey: "dash",
		FunASRModel:     "fun-asr",
	})
	seedBillingRates(t, app, map[string]any{
		"asr": []map[string]any{{
			"provider":           "dashscope",
			"model_id":           "fun-asr",
			"credits_per_minute": 1,
		}},
	})
	token := registerAndLoginWithCredits(t, app, "funasr-query-empty@example.com", 0)

	resp := request(app, http.MethodGet, "/api/v1/tools/funasr/asr/query/task-zero-duration", nil, token)
	if resp.Code != http.StatusPaymentRequired {
		t.Fatalf("query status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if !bytes.Contains(resp.Body.Bytes(), []byte("Credits余额不足")) {
		t.Fatalf("query rejection body = %s", resp.Body.String())
	}

	var records int64
	if err := app.db.Model(&models.TokenUsageRecord{}).Where("api_endpoint = ?", "/api/v1/tools/funasr/asr/query").Count(&records).Error; err != nil {
		t.Fatalf("usage count: %v", err)
	}
	if records != 0 {
		t.Fatalf("usage records = %d, want 0 for rejected query", records)
	}
}

func TestTimelineViewAnalyzeEndpoint(t *testing.T) {
	var sawVision bool
	dashscopeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/chat/completions":
			sawVision = true
			if got := r.Header.Get("X-DashScope-OssResourceResolve"); got != "enable" {
				t.Fatalf("oss resolve header = %q", got)
			}
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode vision payload: %v", err)
			}
			if payload["model"] != "qwen3.6-plus" {
				t.Fatalf("vision model = %#v", payload["model"])
			}
			if payload["stream"] != true {
				t.Fatalf("vision stream = %#v, want true", payload["stream"])
			}
			streamOptions, _ := payload["stream_options"].(map[string]any)
			if streamOptions["include_usage"] != true {
				t.Fatalf("stream_options = %#v, want include_usage", payload["stream_options"])
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"id\":\"vision-1\",\"model\":\"qwen3.6-plus\",\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"切点安全\\\",\\\"visual_events\\\":[],\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: {\"model\":\"qwen3.6-plus\",\"choices\":[{\"delta\":{\"content\":\"\\\"cut_safety\\\":{\\\"rating\\\":\\\"safe\\\",\\\"reason\\\":\\\"波形平稳\\\"},\\\"concerns\\\":[],\\\"recommendations\\\":[\\\"可在静音段切换\\\"]}\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: {\"id\":\"vision-1\",\"model\":\"qwen3.6-plus\",\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":8,\"total_tokens\":20}}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			t.Fatalf("unexpected dashscope request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer dashscopeServer.Close()

	app := newTestApp(t)
	app.cfg.QwenVisionModel = "qwen3.6-plus"
	app.dashscope = providers.NewDashScopeClient(config.Config{
		DashScopeAPIKey:   "dash",
		QwenVisionModel:   "qwen3.6-plus",
		QwenVisionBaseURL: dashscopeServer.URL,
	})
	seedBillingRates(t, app, map[string]any{
		"vision": []map[string]any{{
			"provider":          "dashscope",
			"model_id":          "qwen3.6-plus",
			"tokens_per_credit": 1,
		}},
	})
	token := registerAndLoginWithCredits(t, app, "timeline@example.com", 100)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/timeline-view/analyze", bytes.NewReader([]byte(`{"image_oss_url":"oss://dashscope/tmp/timeline.png","start":1.0,"end":3.5,"n_frames":6}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("timeline analyze status = %d, body = %s", w.Code, w.Body.String())
	}
	if !sawVision {
		t.Fatalf("expected vision call")
	}
	if got := w.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}
	for _, want := range []string{"event:answer_delta", "timeline_image_url", "切点安全", "tokens_consumed"} {
		if !bytes.Contains(w.Body.Bytes(), []byte(want)) {
			t.Fatalf("timeline body missing %q: %s", want, w.Body.String())
		}
	}
	if !strings.Contains(w.Body.String(), "event:completed") {
		t.Fatalf("timeline analyze should stream completed event: %s", w.Body.String())
	}
}

func TestTimelineViewDecideUsesOpenAIFlashModel(t *testing.T) {
	var sawOpenAI bool
	openAIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected openai request %s %s", r.Method, r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode openai payload: %v", err)
		}
		if payload["model"] != "openai-text-test" {
			t.Fatalf("model = %#v", payload["model"])
		}
		messages, _ := payload["messages"].([]any)
		if len(messages) != 2 {
			t.Fatalf("messages = %#v", payload["messages"])
		}
		systemMessage, _ := messages[0].(map[string]any)
		systemContent, _ := systemMessage["content"].(string)
		for _, want := range []string{"observed_gaps", "不能单独触发 timeline_view", "必须放入 skipped"} {
			if !strings.Contains(systemContent, want) {
				t.Fatalf("system prompt missing %q: %s", want, systemContent)
			}
		}
		if payload["stream"] != true {
			t.Fatalf("stream = %#v", payload["stream"])
		}
		sawOpenAI = true
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"openai-decision-1\",\"model\":\"openai-text-test\",\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"仅明确候选切点需要检查\\\",\\\"needs_timeline_view\\\":true,\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"\\\"decisions\\\":[{\\\"start\\\":10.0,\\\"end\\\":10.5,\\\"priority\\\":\\\"high\\\",\\\"trigger_type\\\":\\\"candidate_cut\\\",\\\"reason\\\":\\\"调用方明确标记为候选切点\\\",\\\"expected_question\\\":\\\"画面是否跳切\\\",\\\"n_frames\\\":10}],\\\"skipped\\\":[{\\\"start\\\":1.2,\\\"end\\\":1.54,\\\"reason\\\":\\\"短停顿但不是明确候选切点\\\"}],\\\"confidence\\\":\\\"high\\\",\\\"global_notes\\\":[]}\"}}],\"usage\":{\"prompt_tokens\":30,\"completion_tokens\":20,\"total_tokens\":50}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer openAIServer.Close()

	app := newTestApp(t)
	app.openai = providers.NewOpenAIClient(config.Config{OpenAIFlashAPIKey: "openai", OpenAIFlashAPIBaseURL: openAIServer.URL, OpenAIFlashModel: "openai-text-test", OpenAITemperature: 0.1}, app.promptBuilder)
	seedOpenAIWildcardBillingRate(t, app)
	token := registerAndLoginWithCredits(t, app, "timeline-decide@example.com", 10000)

	resp := request(app, http.MethodPost, "/api/v1/tools/timeline-view/decide", map[string]any{
		"video_context":  "[1.20-1.55] hello\n[1.82-2.60] world",
		"observed_gaps":  []map[string]any{{"start": 1.20, "end": 1.54, "silence_ms": 340}},
		"candidate_cuts": []map[string]any{{"start": 10.0, "end": 10.5, "reason": "edl_boundary"}},
	}, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("timeline decide status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if !sawOpenAI {
		t.Fatalf("expected openai call")
	}
	for _, want := range []string{"openai-text-test", "decisions", "candidate_cut", "短停顿但不是明确候选切点", "tokens_consumed"} {
		if !bytes.Contains(resp.Body.Bytes(), []byte(want)) {
			t.Fatalf("timeline decide body missing %q: %s", want, resp.Body.String())
		}
	}
}

func TestFrameWorkflowSubtitleValidateUsesOpenAIFlashModel(t *testing.T) {
	var sawOpenAI bool
	openAIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected openai request %s %s", r.Method, r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode openai payload: %v", err)
		}
		if payload["model"] != "openai-flash-test" {
			t.Fatalf("model = %#v", payload["model"])
		}
		messages, _ := payload["messages"].([]any)
		if len(messages) != 2 {
			t.Fatalf("messages = %#v", payload["messages"])
		}
		systemMessage, _ := messages[0].(map[string]any)
		systemContent, _ := systemMessage["content"].(string)
		for _, want := range []string{"语义不通", "context_segments 只是只读的前后文参考", "数量、id 集合和顺序必须完全一致", "不得返回 context_segments", "请求不会提供逐词时间线"} {
			if !strings.Contains(systemContent, want) {
				t.Fatalf("system prompt missing %q: %s", want, systemContent)
			}
		}
		userMessage, _ := messages[1].(map[string]any)
		userContent, _ := userMessage["content"].(string)
		if !strings.Contains(userContent, `"segments"`) {
			t.Fatalf("user prompt missing segments: %s", userContent)
		}
		for _, excluded := range []string{`"transcript"`, `"video_context"`, `"phrases"`, `"words"`} {
			if strings.Contains(userContent, excluded) {
				t.Fatalf("user prompt should strip %s: %s", excluded, userContent)
			}
		}
		if payload["stream"] != true {
			t.Fatalf("stream = %#v", payload["stream"])
		}
		sawOpenAI = true
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"subtitle-validation-1\",\"model\":\"openai-flash-test\",\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"修正一处字幕和说话人\\\",\\\"corrections_count\\\":2,\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"\\\"segments\\\":[{\\\"id\\\":\\\"1\\\",\\\"start\\\":0,\\\"end\\\":1.2,\\\"text\\\":\\\"修正字幕\\\",\\\"speaker_id\\\":\\\"speaker_0\\\"}],\\\"notes\\\":[]}\"}}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":15,\"total_tokens\":35}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer openAIServer.Close()

	app := newTestApp(t)
	app.openai = providers.NewOpenAIClient(config.Config{OpenAIFlashAPIKey: "openai", OpenAIFlashAPIBaseURL: openAIServer.URL, OpenAIFlashModel: "openai-flash-test", OpenAITemperature: 0.1}, app.promptBuilder)
	seedOpenAIWildcardBillingRate(t, app)
	token := registerAndLoginWithCredits(t, app, "subtitle-validate@example.com", 10000)

	resp := request(app, http.MethodPost, "/api/v1/tools/frame-workflow/subtitles/validate", map[string]any{
		"transcript":    "错字幕",
		"video_context": "[0.00-1.20] 错字幕",
		"phrases":       []map[string]any{{"start": 0, "end": 1.2, "text": "错字幕"}},
		"words":         []map[string]any{{"start": 0, "end": 0.2, "text": "错"}},
		"segments": []map[string]any{
			{"id": "1", "start": 0, "end": 1.2, "text": "错字幕", "speaker_id": "speaker_1"},
		},
	}, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("subtitle validate status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if !sawOpenAI {
		t.Fatalf("expected openai call")
	}
	for _, want := range []string{"event:answer_delta", "event:completed", "openai-flash-test", "修正字幕", "speaker_0", "tokens_consumed"} {
		if !bytes.Contains(resp.Body.Bytes(), []byte(want)) {
			t.Fatalf("subtitle validate body missing %q: %s", want, resp.Body.String())
		}
	}

	var usage models.TokenUsageRecord
	if err := app.db.First(&usage, "api_endpoint = ?", "/api/v1/tools/frame-workflow/subtitles/validate").Error; err != nil {
		t.Fatalf("expected subtitle validation usage record: %v", err)
	}
	if usage.ModelName != "openai-flash-test" || usage.ServiceKind != "llm" {
		t.Fatalf("unexpected usage record: %#v", usage)
	}
}

func TestFrameWorkflowNarrativeAnalyzeUsesOpenAIProModel(t *testing.T) {
	var sawOpenAI bool
	openAIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected openai request %s %s", r.Method, r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode openai payload: %v", err)
		}
		if payload["model"] != "openai-text-test" {
			t.Fatalf("model = %#v", payload["model"])
		}
		messages, _ := payload["messages"].([]any)
		if len(messages) != 2 {
			t.Fatalf("messages = %#v", payload["messages"])
		}
		systemMessage, _ := messages[0].(map[string]any)
		systemContent, _ := systemMessage["content"].(string)
		for _, want := range []string{"剧情理解", "人物/关系", "情绪曲线", "事件抽取", "speaker_id"} {
			if !strings.Contains(systemContent, want) {
				t.Fatalf("system prompt missing %q: %s", want, systemContent)
			}
		}
		if payload["stream"] != true {
			t.Fatalf("stream = %#v", payload["stream"])
		}
		sawOpenAI = true
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"openai-narrative-1\",\"model\":\"openai-text-test\",\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"两人因追逃发生冲突\\\",\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"\\\"plot\\\":{\\\"premise\\\":\\\"角色正在逃避追赶\\\",\\\"current_stage\\\":\\\"冲突升级\\\",\\\"stakes\\\":\\\"是否被抓住\\\"},\\\"characters\\\":[{\\\"id\\\":\\\"speaker_0\\\",\\\"role\\\":\\\"逃跑者\\\",\\\"emotional_state\\\":\\\"紧张\\\",\\\"evidence\\\":[\\\"别过来\\\"]}],\\\"relationships\\\":[{\\\"source\\\":\\\"speaker_0\\\",\\\"target\\\":\\\"speaker_1\\\",\\\"relationship\\\":\\\"对立\\\"}],\\\"emotion_curve\\\":[{\\\"start\\\":10,\\\"end\\\":12.5,\\\"character\\\":\\\"speaker_0\\\",\\\"emotion\\\":\\\"紧张\\\"}],\\\"events\\\":[{\\\"start\\\":10,\\\"end\\\":12.5,\\\"type\\\":\\\"conflict\\\",\\\"description\\\":\\\"角色逃跑并回头\\\"}],\\\"conflicts\\\":[\\\"追逃\\\"],\\\"themes\\\":[\\\"紧张反转\\\"],\\\"open_questions\\\":[]}\"}}],\"usage\":{\"prompt_tokens\":60,\"completion_tokens\":40,\"total_tokens\":100}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer openAIServer.Close()

	app := newTestApp(t)
	app.openai = providers.NewOpenAIClient(config.Config{OpenAIProAPIKey: "openai", OpenAIProAPIBaseURL: openAIServer.URL, OpenAIProModel: "openai-text-test", OpenAITemperature: 0.1}, app.promptBuilder)
	seedOpenAIWildcardBillingRate(t, app)
	token := registerAndLoginWithCredits(t, app, "narrative-analyze@example.com", 10000)

	resp := request(app, http.MethodPost, "/api/v1/tools/frame-workflow/narrative-analyze", map[string]any{
		"transcript": "别过来。 我没事。",
		"segments": []map[string]any{
			{"start": 10.0, "end": 12.5, "text": "别过来。", "speaker_id": "speaker_0"},
		},
		"visual_analyses": []map[string]any{
			{"start": 10.0, "end": 12.5, "summary": "人物奔跑后突然转头"},
		},
	}, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("narrative analyze status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if !sawOpenAI {
		t.Fatalf("expected openai call")
	}
	for _, want := range []string{"openai-text-test", "两人因追逃发生冲突", "characters", "relationships", "emotion_curve", "events", "tokens_consumed"} {
		if !bytes.Contains(resp.Body.Bytes(), []byte(want)) {
			t.Fatalf("narrative body missing %q: %s", want, resp.Body.String())
		}
	}
}

func TestFrameWorkflowNarrativeAnalyzePrecheckUsesCreditsNotRawTokens(t *testing.T) {
	var sawOpenAI bool
	openAIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected openai request %s %s", r.Method, r.URL.Path)
		}
		sawOpenAI = true
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"openai-narrative-credit-1\",\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"余额检查通过\\\",\\\"characters\\\":[],\\\"relationships\\\":[],\\\"emotion_curve\\\":[],\\\"events\\\":[{\\\"start\\\":10,\\\"end\\\":12.5,\\\"description\\\":\\\"角色继续逃跑\\\"}]}\"}}],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":500,\"total_tokens\":1500}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer openAIServer.Close()

	app := newTestApp(t)
	app.cfg.OpenAIProModel = "openai-text-test"
	app.cfg.OpenAIProAPIBaseURL = openAIServer.URL
	app.openai = providers.NewOpenAIClient(config.Config{OpenAIProAPIKey: "openai", OpenAIProAPIBaseURL: openAIServer.URL, OpenAIProModel: "openai-text-test", OpenAITemperature: 0.1}, app.promptBuilder)
	token := registerAndLogin(t, app, "narrative-credit-precheck@example.com")
	var user models.User
	if err := app.db.First(&user, "email = ?", "narrative-credit-precheck@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	if err := app.db.Model(&models.UserTokenAccount{}).Where("user_id = ?", user.ID).Updates(map[string]any{"total_tokens": 50, "used_tokens": 0}).Error; err != nil {
		t.Fatalf("shrink account balance: %v", err)
	}
	if err := app.db.Create(&models.SystemSetting{
		Key: "billing_rates",
		Value: jsonMap(map[string]any{
			"llm": []map[string]any{{
				"provider":          "openai",
				"model_id":          "openai-text-test",
				"tokens_per_credit": 1000,
			}},
		}),
	}).Error; err != nil {
		t.Fatalf("setting: %v", err)
	}

	resp := request(app, http.MethodPost, "/api/v1/tools/frame-workflow/narrative-analyze", map[string]any{
		"transcript": "别过来。 我没事。",
		"segments": []map[string]any{
			{"start": 10.0, "end": 12.5, "text": "别过来。", "speaker_id": "speaker_0"},
		},
	}, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("narrative analyze status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if !sawOpenAI {
		t.Fatalf("expected openai call; precheck likely compared raw token estimate against credits")
	}

	var account models.UserTokenAccount
	if err := app.db.First(&account, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.UsedTokens != 2 {
		t.Fatalf("used credits = %d, want 2", account.UsedTokens)
	}
}

func TestFrameWorkflowNarrativeAnalyzeStreamReturnsNarrativePayload(t *testing.T) {
	openAIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected openai request %s %s", r.Method, r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode openai payload: %v", err)
		}
		if payload["stream"] != true {
			t.Fatalf("stream = %#v", payload["stream"])
		}
		if payload["model"] != "openai-text-test" {
			t.Fatalf("model = %#v", payload["model"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"narrative-stream-1\",\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"两人因追逃发生冲突\\\",\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"\\\"characters\\\":[{\\\"id\\\":\\\"speaker_0\\\",\\\"role\\\":\\\"逃跑者\\\"}],\\\"relationships\\\":[],\\\"emotion_curve\\\":[],\\\"events\\\":[{\\\"start\\\":10,\\\"end\\\":12.5,\\\"type\\\":\\\"conflict\\\",\\\"description\\\":\\\"角色逃跑\\\"}]}\"}}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":6,\"total_tokens\":13}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer openAIServer.Close()

	app := newTestApp(t)
	app.openai = providers.NewOpenAIClient(config.Config{OpenAIProAPIKey: "openai", OpenAIProAPIBaseURL: openAIServer.URL, OpenAIProModel: "openai-text-test", OpenAITemperature: 0.1}, app.promptBuilder)
	seedOpenAIWildcardBillingRate(t, app)
	token := registerAndLoginWithCredits(t, app, "narrative-stream@example.com", 10000)

	resp := request(app, http.MethodPost, "/api/v1/tools/frame-workflow/narrative-analyze", map[string]any{
		"transcript": "别过来。 我没事。",
		"segments": []map[string]any{
			{"start": 10.0, "end": 12.5, "text": "别过来。", "speaker_id": "speaker_0"},
		},
	}, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("narrative stream status = %d, body = %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	if !strings.Contains(body, "event:answer_delta") {
		t.Fatalf("stream body missing answer deltas: %s", body)
	}
	if !strings.Contains(body, "event:completed") || !strings.Contains(body, "\"characters\"") || !strings.Contains(body, "两人因追逃发生冲突") {
		t.Fatalf("stream body missing completed narrative payload: %s", body)
	}

	var user models.User
	if err := app.db.First(&user, "email = ?", "narrative-stream@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	var account models.UserTokenAccount
	if err := app.db.First(&account, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.UsedTokens != 13 {
		t.Fatalf("used tokens = %d, want 13", account.UsedTokens)
	}
}

func TestAnalyzeImagesUsesSiliconFlowAndBillsTokens(t *testing.T) {
	siliconServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected silicon request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "sf-req-1",
			"model": "Qwen/Qwen3-VL-32B-Instruct",
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 10, "total_tokens": 20},
			"choices": []map[string]any{{
				"message": map[string]any{"content": `{"summary":"画面总结"}`},
			}},
		})
	}))
	defer siliconServer.Close()

	app := newTestApp(t)
	app.silicon = providers.NewSiliconFlowClient(config.Config{SiliconFlowAPIKey: "sf", SiliconFlowBaseURL: siliconServer.URL})
	seedBillingRates(t, app, map[string]any{
		"vision": []map[string]any{{
			"provider":          "siliconflow",
			"model_id":          "Qwen/Qwen3-VL-32B-Instruct",
			"tokens_per_credit": 5,
		}},
	})
	token := registerAndLoginWithCredits(t, app, "vision@example.com", 100)

	resp := request(app, http.MethodPost, "/api/v1/tools/analyze-images", map[string]any{
		"images": []map[string]any{{"type": "url", "data": "https://example.com/1.png"}},
	}, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("analyze images status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if !bytes.Contains(resp.Body.Bytes(), []byte("画面总结")) {
		t.Fatalf("analyze images body = %s", resp.Body.String())
	}

	var user models.User
	if err := app.db.First(&user, "email = ?", "vision@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	var account models.UserTokenAccount
	if err := app.db.First(&account, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.UsedTokens != 4 {
		t.Fatalf("used tokens = %d, want 4", account.UsedTokens)
	}
}

func TestAnalyzeImagesRequiresConfiguredProvider(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLoginWithCredits(t, app, "no-sf@example.com", 100)
	resp := request(app, http.MethodPost, "/api/v1/tools/analyze-images", map[string]any{
		"images": []map[string]any{{"type": "url", "data": "https://example.com/1.png"}},
	}, token)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestDashScopeUploadPolicyUsesProvider(t *testing.T) {
	requests := 0
	expectedModels := []string{"fun-asr", "qwen3.6-plus"}
	dashscopeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/uploads" {
			t.Fatalf("unexpected dashscope request %s %s", r.Method, r.URL.Path)
		}
		if requests >= len(expectedModels) {
			t.Fatalf("unexpected extra upload policy request")
		}
		if got := r.URL.Query().Get("model"); got != expectedModels[requests] {
			t.Fatalf("model query = %q, want %q", got, expectedModels[requests])
		}
		requests++
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"upload_host":            "https://oss.example.com",
			"upload_dir":             "dashscope/tmp/",
			"oss_access_key_id":      "ak",
			"policy":                 "policy",
			"signature":              "sig",
			"x_oss_object_acl":       "private",
			"x_oss_forbid_overwrite": "true",
		}})
	}))
	defer dashscopeServer.Close()
	t.Cleanup(providers.SetDashScopeAPIBaseURLForTest(dashscopeServer.URL))

	app := newTestApp(t)
	app.dashscope = providers.NewDashScopeClient(config.Config{DashScopeAPIKey: "dash", Qwen3ASRModel: "qwen3", FunASRModel: "fun-asr", QwenVisionModel: "qwen3.6-plus"})
	token := registerAndLogin(t, app, "dash@example.com")
	resp := request(app, http.MethodPost, "/api/v1/tools/dashscope/temp-files/upload-policy", map[string]any{"provider": "funasr"}, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("policy status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if !bytes.Contains(resp.Body.Bytes(), []byte("oss.example.com")) {
		t.Fatalf("policy body = %s", resp.Body.String())
	}
	resp = request(app, http.MethodPost, "/api/v1/tools/dashscope/temp-files/upload-policy", map[string]any{"provider": "qwen-vision"}, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("vision policy status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if requests != len(expectedModels) {
		t.Fatalf("policy requests = %d, want %d", requests, len(expectedModels))
	}
}

func TestStreamToolUsesOpenAIAndBillsTokens(t *testing.T) {
	openAIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected openai request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"openai-1\",\"choices\":[{\"delta\":{\"content\":\"{\\\"items\\\":[\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"_id\\\":\\\"1\\\",\\\"text\\\":\\\"hello\\\",\\\"start\\\":0,\\\"end\\\":1,\\\"org\\\":\\\"\\\"}] }\"}}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer openAIServer.Close()

	app := newTestApp(t)
	app.openai = providers.NewOpenAIClient(config.Config{OpenAIProAPIKey: "openai", OpenAIProAPIBaseURL: openAIServer.URL, OpenAIProModel: "openai-test", OpenAITemperature: 0.1}, app.promptBuilder)
	seedOpenAIWildcardBillingRate(t, app)
	token := registerAndLoginWithCredits(t, app, "openai@example.com", 10000)

	resp := request(app, http.MethodPost, "/api/v1/tools/generate-script-stream", map[string]any{"video_context": "短片段"}, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("stream status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}
	if !bytes.Contains(resp.Body.Bytes(), []byte("completed")) {
		t.Fatalf("stream body = %s", resp.Body.String())
	}

	var user models.User
	if err := app.db.First(&user, "email = ?", "openai@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	var account models.UserTokenAccount
	if err := app.db.First(&account, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.UsedTokens != 7 {
		t.Fatalf("used tokens = %d, want 7", account.UsedTokens)
	}
}

func TestStreamToolUsesOpenAICompatibleProviderForBillingRates(t *testing.T) {
	openAIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected openai-compatible request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"qwen-1\",\"choices\":[{\"delta\":{\"content\":\"{\\\"items\\\":[\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"_id\\\":\\\"1\\\",\\\"text\\\":\\\"hello\\\",\\\"start\\\":0,\\\"end\\\":1,\\\"org\\\":\\\"\\\"}] }\"}}],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":500,\"total_tokens\":1500}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer openAIServer.Close()

	app := newTestApp(t)
	app.cfg.OpenAIProModel = "qwen3.6-plus"
	app.cfg.OpenAIProAPIBaseURL = openAIServer.URL
	app.openai = providers.NewOpenAIClient(config.Config{OpenAIProAPIKey: "openai", OpenAIProAPIBaseURL: openAIServer.URL, OpenAIProModel: "qwen3.6-plus", OpenAITemperature: 0.1}, app.promptBuilder)
	token := registerAndLoginWithCredits(t, app, "qwen-compatible-billing@example.com", 10000)
	if err := app.db.Create(&models.SystemSetting{
		Key: "billing_rates",
		Value: jsonMap(map[string]any{
			"llm": []map[string]any{{
				"provider":                "dashscope",
				"model_id":                "qwen3.6-plus",
				"input_cost_per_million":  2,
				"output_cost_per_million": 12,
				"tokens_per_credit":       1000,
			}},
		}),
	}).Error; err != nil {
		t.Fatalf("setting: %v", err)
	}

	resp := request(app, http.MethodPost, "/api/v1/tools/generate-script-stream", map[string]any{"video_context": "短片段"}, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("stream status = %d, body = %s", resp.Code, resp.Body.String())
	}

	var user models.User
	if err := app.db.First(&user, "email = ?", "qwen-compatible-billing@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	var account models.UserTokenAccount
	if err := app.db.First(&account, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.UsedTokens != 2 {
		t.Fatalf("used credits = %d, want qwen pricing rule to bill 2", account.UsedTokens)
	}
	var record models.TokenUsageRecord
	if err := app.db.First(&record, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("record: %v", err)
	}
	if record.Provider != "dashscope" || record.ServiceKind != "llm" || record.ModelID != "qwen3.6-plus" {
		t.Fatalf("record billing identity mismatch: %#v", record)
	}
	if record.MissingPricing {
		t.Fatalf("qwen compatible billing should not be missing pricing")
	}
}

func TestStreamToolPrecheckDoesNotInflateVideoContextByLegacyMultiplier(t *testing.T) {
	var sawOpenAI bool
	openAIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected openai-compatible request %s %s", r.Method, r.URL.Path)
		}
		sawOpenAI = true
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"openai-script-credit-1\",\"choices\":[{\"delta\":{\"content\":\"{\\\"items\\\":[\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"_id\\\":\\\"1\\\",\\\"text\\\":\\\"hello\\\",\\\"start\\\":0,\\\"end\\\":1,\\\"org\\\":\\\"\\\"}] }\"}}],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":500,\"total_tokens\":1500}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer openAIServer.Close()

	app := newTestApp(t)
	app.cfg.OpenAIProModel = "openai-script-test"
	app.cfg.OpenAIProAPIBaseURL = openAIServer.URL
	app.openai = providers.NewOpenAIClient(config.Config{OpenAIProAPIKey: "openai", OpenAIProAPIBaseURL: openAIServer.URL, OpenAIProModel: "openai-script-test", OpenAITemperature: 0.1}, app.promptBuilder)
	token := registerAndLogin(t, app, "script-credit-precheck@example.com")
	var user models.User
	if err := app.db.First(&user, "email = ?", "script-credit-precheck@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	if err := app.db.Model(&models.UserTokenAccount{}).Where("user_id = ?", user.ID).Updates(map[string]any{"total_tokens": 5711, "used_tokens": 0}).Error; err != nil {
		t.Fatalf("set account balance: %v", err)
	}
	if err := app.db.Create(&models.SystemSetting{
		Key: "billing_rates",
		Value: jsonMap(map[string]any{
			"llm": []map[string]any{{
				"provider":          "openai",
				"model_id":          "openai-script-test",
				"tokens_per_credit": 83.333333,
			}},
		}),
	}).Error; err != nil {
		t.Fatalf("setting: %v", err)
	}

	resp := request(app, http.MethodPost, "/api/v1/tools/generate-script-stream", map[string]any{
		"video_context": strings.Repeat("剧情片段。", 13031/5),
	}, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("stream status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if !sawOpenAI {
		t.Fatalf("expected openai call; precheck likely used legacy video_context * 45 estimate")
	}
}

func TestTimelineViewDecideStreamReturnsDecisionPayload(t *testing.T) {
	openAIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected openai request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"timeline-1\",\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"ok\\\",\\\"needs_timeline_view\\\":true,\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"\\\"decisions\\\":[{\\\"start\\\":1,\\\"end\\\":3,\\\"priority\\\":\\\"high\\\",\\\"trigger_type\\\":\\\"candidate_cut\\\",\\\"reason\\\":\\\"需要复核\\\"}],\\\"skipped\\\":[],\\\"confidence\\\":\\\"high\\\",\\\"global_notes\\\":[]}\"}}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":6,\"total_tokens\":11}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer openAIServer.Close()

	app := newTestApp(t)
	app.openai = providers.NewOpenAIClient(config.Config{OpenAIFlashAPIKey: "openai", OpenAIFlashAPIBaseURL: openAIServer.URL, OpenAIFlashModel: "openai-test", OpenAITemperature: 0.1}, app.promptBuilder)
	seedOpenAIWildcardBillingRate(t, app)
	token := registerAndLoginWithCredits(t, app, "timeline-stream@example.com", 10000)

	resp := request(app, http.MethodPost, "/api/v1/tools/timeline-view/decide", map[string]any{
		"segments":       []map[string]any{{"start": 1, "end": 3, "text": "停顿后回头"}},
		"candidate_cuts": []map[string]any{{"start": 1, "end": 3, "reason": "silence"}},
		"max_decisions":  2,
	}, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("stream status = %d, body = %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	if !strings.Contains(body, "event:answer_delta") {
		t.Fatalf("stream body missing answer deltas: %s", body)
	}
	if !strings.Contains(body, "event:completed") || !strings.Contains(body, "\"decisions\"") || !strings.Contains(body, "需要复核") {
		t.Fatalf("stream body missing completed decision payload: %s", body)
	}

	var user models.User
	if err := app.db.First(&user, "email = ?", "timeline-stream@example.com").Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	var account models.UserTokenAccount
	if err := app.db.First(&account, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("account: %v", err)
	}
	if account.UsedTokens != 11 {
		t.Fatalf("used tokens = %d, want 11", account.UsedTokens)
	}
}

func TestStreamToolCoalescesTinyDeltas(t *testing.T) {
	openAIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected openai request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"openai-1\",\"choices\":[{\"delta\":{\"content\":\"{\\\"items\\\":[\"}}]}\n\n"))
		for i := 0; i < streamDeltaFlushEvents*2; i++ {
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"小\"}}]}\n\n"))
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"_id\\\":\\\"1\\\",\\\"text\\\":\\\"hello\\\",\\\"start\\\":0,\\\"end\\\":1,\\\"org\\\":\\\"\\\"}] }\"}}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer openAIServer.Close()

	app := newTestApp(t)
	app.openai = providers.NewOpenAIClient(config.Config{OpenAIProAPIKey: "openai", OpenAIProAPIBaseURL: openAIServer.URL, OpenAIProModel: "openai-test", OpenAITemperature: 0.1}, app.promptBuilder)
	seedOpenAIWildcardBillingRate(t, app)
	token := registerAndLoginWithCredits(t, app, "openai-coalesce@example.com", 10000)

	resp := request(app, http.MethodPost, "/api/v1/tools/generate-script-stream", map[string]any{"video_context": "短片段"}, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("stream status = %d, body = %s", resp.Code, resp.Body.String())
	}
	body := resp.Body.String()
	if count := strings.Count(body, "event:thinking_delta"); count >= streamDeltaFlushEvents*2 {
		t.Fatalf("thinking_delta event count = %d, want coalesced; body = %s", count, body)
	}
	if !strings.Contains(body, strings.Repeat("小", streamDeltaFlushEvents)) {
		t.Fatalf("stream body does not contain coalesced delta run: %s", body)
	}
	if !strings.Contains(body, "event:completed") {
		t.Fatalf("stream body missing completed event: %s", body)
	}
}

func TestMediaProxyRejectsUntrustedHosts(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "media-proxy@example.com")

	resp := request(app, http.MethodGet, "/api/v1/tools/media-proxy?url="+url.QueryEscape("https://evil.example.com/bgm.mp3"), nil, token)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("media proxy untrusted host status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestAdminNotificationBroadcastCreatesForAllActiveUsers(t *testing.T) {
	app := newTestApp(t)
	adminToken := registerAndMakeAdmin(t, app, "admin-broadcast@example.com")
	registerAndLogin(t, app, "active-a@example.com")
	registerAndLogin(t, app, "active-b@example.com")
	registerAndLogin(t, app, "inactive@example.com")

	if err := app.db.Model(&models.User{}).Where("email = ?", "inactive@example.com").Update("is_active", false).Error; err != nil {
		t.Fatalf("deactivate user: %v", err)
	}

	expiresAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	resp := request(app, http.MethodPost, "/api/v1/admin/notifications/broadcast", map[string]any{
		"title":      "系统维护通知",
		"message":    "今晚 23:00 会有短暂维护。",
		"type":       "system",
		"data":       map[string]any{"category": "maintenance"},
		"expires_at": expiresAt,
	}, adminToken)
	if resp.Code != http.StatusCreated {
		t.Fatalf("broadcast status = %d, body = %s", resp.Code, resp.Body.String())
	}

	var body struct {
		AnnouncementID  string `json:"announcement_id"`
		Success         bool   `json:"success"`
		CreatedCount    int    `json:"created_count"`
		TargetUserCount int    `json:"target_user_count"`
		PublishedAt     string `json:"published_at"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode broadcast response: %v", err)
	}
	if !body.Success || body.CreatedCount != 3 || body.TargetUserCount != 3 {
		t.Fatalf("unexpected broadcast response: %#v", body)
	}
	if _, err := uuid.Parse(body.AnnouncementID); err != nil {
		t.Fatalf("announcement_id parse failed: %v", err)
	}
	if _, err := time.Parse(time.RFC3339, body.PublishedAt); err != nil {
		t.Fatalf("published_at parse failed: %v", err)
	}

	adminUser := lookupUserByEmail(t, app, "admin-broadcast@example.com")
	activeA := lookupUserByEmail(t, app, "active-a@example.com")
	activeB := lookupUserByEmail(t, app, "active-b@example.com")
	inactiveUser := lookupUserByEmail(t, app, "inactive@example.com")

	for _, userID := range []uuid.UUID{adminUser.ID, activeA.ID, activeB.ID} {
		var count int64
		app.db.Model(&models.Notification{}).Where("user_id = ?", userID).Count(&count)
		if count != 1 {
			t.Fatalf("user %s notification count = %d, want 1", userID, count)
		}
	}

	var inactiveCount int64
	app.db.Model(&models.Notification{}).Where("user_id = ?", inactiveUser.ID).Count(&inactiveCount)
	if inactiveCount != 0 {
		t.Fatalf("inactive user notification count = %d, want 0", inactiveCount)
	}

	var item models.Notification
	if err := app.db.First(&item, "user_id = ?", activeA.ID).Error; err != nil {
		t.Fatalf("load created notification: %v", err)
	}
	if item.AnnouncementID == nil || item.AnnouncementID.String() != body.AnnouncementID {
		t.Fatalf("announcement_id = %v, want %s", item.AnnouncementID, body.AnnouncementID)
	}
	if item.ExpiresAt == nil || item.ExpiresAt.UTC().Format(time.RFC3339) != expiresAt.Format(time.RFC3339) {
		t.Fatalf("expires_at = %v, want %s", item.ExpiresAt, expiresAt.Format(time.RFC3339))
	}
	var data map[string]any
	if err := json.Unmarshal(item.Data, &data); err != nil {
		t.Fatalf("decode notification data: %v", err)
	}
	if data["source"] != "admin_announcement" || data["audience"] != "all_active_users" || data["category"] != "maintenance" || data["announcement_id"] != body.AnnouncementID {
		t.Fatalf("unexpected notification data: %#v", data)
	}
}

func TestAdminNotificationBroadcastUsesBatchesForLargeAudience(t *testing.T) {
	app := newTestApp(t)
	adminToken := registerAndMakeAdmin(t, app, "admin-broadcast-batch@example.com")

	emailVerifiedAt := time.Now().UTC()
	users := make([]models.User, 0, 7000)
	for i := 0; i < 7000; i++ {
		users = append(users, models.User{
			Username:        fmt.Sprintf("batch_announcement_%04d", i),
			Email:           fmt.Sprintf("batch-announcement-%04d@example.com", i),
			PasswordHash:    "unused",
			IsActive:        true,
			IsVerified:      true,
			EmailVerifiedAt: &emailVerifiedAt,
		})
	}
	if err := app.db.CreateInBatches(&users, 500).Error; err != nil {
		t.Fatalf("seed batch users: %v", err)
	}

	message := strings.Repeat("本次 Credit 计费升级说明。", 30)
	resp := request(app, http.MethodPost, "/api/v1/admin/notifications/broadcast", map[string]any{
		"title":   "新版本计费方式升级通知",
		"message": message,
		"type":    "system",
	}, adminToken)
	if resp.Code != http.StatusCreated {
		t.Fatalf("broadcast status = %d, body = %s", resp.Code, resp.Body.String())
	}

	var body struct {
		AnnouncementID  string `json:"announcement_id"`
		CreatedCount    int    `json:"created_count"`
		TargetUserCount int    `json:"target_user_count"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode broadcast response: %v", err)
	}
	if body.CreatedCount != 7001 || body.TargetUserCount != 7001 {
		t.Fatalf("unexpected broadcast response: %#v", body)
	}

	var count int64
	if err := app.db.Model(&models.Notification{}).Where("announcement_id = ?", body.AnnouncementID).Count(&count).Error; err != nil {
		t.Fatalf("count broadcast notifications: %v", err)
	}
	if count != 7001 {
		t.Fatalf("broadcast notification count = %d, want 7001", count)
	}
}

func TestAdminAnnouncementsListUpdateAndDelete(t *testing.T) {
	app := newTestApp(t)
	adminToken := registerAndMakeAdmin(t, app, "admin-announcements@example.com")
	registerAndLogin(t, app, "announce-a@example.com")
	registerAndLogin(t, app, "announce-b@example.com")

	broadcastResp := request(app, http.MethodPost, "/api/v1/admin/notifications/broadcast", map[string]any{
		"title":   "版本升级通知",
		"message": "明天上午 10 点发布新版本。",
		"type":    "system",
	}, adminToken)
	if broadcastResp.Code != http.StatusCreated {
		t.Fatalf("broadcast status = %d, body = %s", broadcastResp.Code, broadcastResp.Body.String())
	}

	var broadcastBody struct {
		AnnouncementID string `json:"announcement_id"`
	}
	if err := json.Unmarshal(broadcastResp.Body.Bytes(), &broadcastBody); err != nil {
		t.Fatalf("decode broadcast response: %v", err)
	}

	listResp := request(app, http.MethodGet, "/api/v1/admin/notifications/announcements", nil, adminToken)
	if listResp.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listResp.Code, listResp.Body.String())
	}

	var announcements []struct {
		ID              string  `json:"id"`
		Title           string  `json:"title"`
		Message         string  `json:"message"`
		TargetUserCount int     `json:"target_user_count"`
		ExpiresAt       *string `json:"expires_at"`
	}
	if err := json.Unmarshal(listResp.Body.Bytes(), &announcements); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(announcements) != 1 {
		t.Fatalf("announcement count = %d, want 1", len(announcements))
	}
	if announcements[0].ID != broadcastBody.AnnouncementID || announcements[0].TargetUserCount != 3 {
		t.Fatalf("unexpected announcement summary: %#v", announcements[0])
	}

	expiresAt := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	updateResp := request(app, http.MethodPut, "/api/v1/admin/notifications/announcements/"+broadcastBody.AnnouncementID, map[string]any{
		"title":      "版本升级时间调整",
		"message":    "发布时间改到明天下午 3 点。",
		"expires_at": expiresAt,
	}, adminToken)
	if updateResp.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updateResp.Code, updateResp.Body.String())
	}

	var updated struct {
		ID              string  `json:"id"`
		Title           string  `json:"title"`
		Message         string  `json:"message"`
		TargetUserCount int     `json:"target_user_count"`
		ExpiresAt       *string `json:"expires_at"`
	}
	if err := json.Unmarshal(updateResp.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updated.ID != broadcastBody.AnnouncementID || updated.Title != "版本升级时间调整" || updated.Message != "发布时间改到明天下午 3 点。" || updated.TargetUserCount != 3 {
		t.Fatalf("unexpected update response: %#v", updated)
	}
	if updated.ExpiresAt == nil || *updated.ExpiresAt != expiresAt.Format(time.RFC3339) {
		t.Fatalf("updated expires_at = %v, want %s", updated.ExpiresAt, expiresAt.Format(time.RFC3339))
	}

	var items []models.Notification
	if err := app.db.Where("announcement_id = ?", broadcastBody.AnnouncementID).Find(&items).Error; err != nil {
		t.Fatalf("load updated notifications: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("updated notification count = %d, want 3", len(items))
	}
	for _, item := range items {
		if item.Title != "版本升级时间调整" || item.Message != "发布时间改到明天下午 3 点。" {
			t.Fatalf("updated notification mismatch: %#v", item)
		}
		if item.ExpiresAt == nil || item.ExpiresAt.UTC().Format(time.RFC3339) != expiresAt.Format(time.RFC3339) {
			t.Fatalf("updated expires_at = %v, want %s", item.ExpiresAt, expiresAt.Format(time.RFC3339))
		}
	}

	deleteResp := request(app, http.MethodDelete, "/api/v1/admin/notifications/announcements/"+broadcastBody.AnnouncementID, nil, adminToken)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", deleteResp.Code, deleteResp.Body.String())
	}

	var deleteBody struct {
		Success      bool  `json:"success"`
		DeletedCount int64 `json:"deleted_count"`
	}
	if err := json.Unmarshal(deleteResp.Body.Bytes(), &deleteBody); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if !deleteBody.Success || deleteBody.DeletedCount != 3 {
		t.Fatalf("unexpected delete response: %#v", deleteBody)
	}

	var remaining int64
	if err := app.db.Model(&models.Notification{}).Where("announcement_id = ?", broadcastBody.AnnouncementID).Count(&remaining).Error; err != nil {
		t.Fatalf("count remaining notifications: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("remaining notifications = %d, want 0", remaining)
	}
}

func TestAdminAnnouncementsSupportLegacyAnnouncementManagement(t *testing.T) {
	app := newTestApp(t)
	adminToken := registerAndMakeAdmin(t, app, "admin-legacy-announcements@example.com")
	userA := lookupUserByEmail(t, app, "admin-legacy-announcements@example.com")
	registerAndLogin(t, app, "legacy-a@example.com")
	userB := lookupUserByEmail(t, app, "legacy-a@example.com")

	createdAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	for _, userID := range []uuid.UUID{userA.ID, userB.ID} {
		item := models.Notification{
			Base:    models.Base{CreatedAt: createdAt, UpdatedAt: createdAt},
			UserID:  userID,
			Type:    "system",
			Title:   "旧公告",
			Message: "这是迁移前发布的公告。",
			Data: jsonMap(map[string]any{
				"source":   "admin_announcement",
				"audience": "all_active_users",
			}),
		}
		if err := app.db.Create(&item).Error; err != nil {
			t.Fatalf("create legacy notification: %v", err)
		}
	}

	listResp := request(app, http.MethodGet, "/api/v1/admin/notifications/announcements", nil, adminToken)
	if listResp.Code != http.StatusOK {
		t.Fatalf("legacy list status = %d, body = %s", listResp.Code, listResp.Body.String())
	}

	var announcements []struct {
		ID              string `json:"id"`
		Title           string `json:"title"`
		TargetUserCount int    `json:"target_user_count"`
	}
	if err := json.Unmarshal(listResp.Body.Bytes(), &announcements); err != nil {
		t.Fatalf("decode legacy list response: %v", err)
	}
	if len(announcements) != 1 {
		t.Fatalf("legacy announcement count = %d, want 1", len(announcements))
	}
	if !strings.HasPrefix(announcements[0].ID, "legacy:") || announcements[0].Title != "旧公告" || announcements[0].TargetUserCount != 2 {
		t.Fatalf("unexpected legacy announcement summary: %#v", announcements[0])
	}

	updateResp := request(app, http.MethodPut, "/api/v1/admin/notifications/announcements/"+url.PathEscape(announcements[0].ID), map[string]any{
		"title":   "旧公告（已修订）",
		"message": "这是迁移前公告的修订版。",
	}, adminToken)
	if updateResp.Code != http.StatusOK {
		t.Fatalf("legacy update status = %d, body = %s", updateResp.Code, updateResp.Body.String())
	}

	var updated struct {
		ID              string `json:"id"`
		Title           string `json:"title"`
		Message         string `json:"message"`
		TargetUserCount int    `json:"target_user_count"`
	}
	if err := json.Unmarshal(updateResp.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode legacy update response: %v", err)
	}
	if _, err := uuid.Parse(updated.ID); err != nil {
		t.Fatalf("legacy update should migrate to uuid id, got %q: %v", updated.ID, err)
	}
	if updated.Title != "旧公告（已修订）" || updated.Message != "这是迁移前公告的修订版。" || updated.TargetUserCount != 2 {
		t.Fatalf("unexpected legacy update response: %#v", updated)
	}

	var items []models.Notification
	if err := app.db.Where("announcement_id = ?", updated.ID).Find(&items).Error; err != nil {
		t.Fatalf("load migrated legacy notifications: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("migrated legacy notification count = %d, want 2", len(items))
	}
	for _, item := range items {
		if item.AnnouncementID == nil || item.AnnouncementID.String() != updated.ID {
			t.Fatalf("migrated legacy announcement id = %v, want %s", item.AnnouncementID, updated.ID)
		}
	}
}

func TestAdminNotificationBroadcastRequiresAdmin(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "non-admin-broadcast@example.com")

	resp := request(app, http.MethodPost, "/api/v1/admin/notifications/broadcast", map[string]any{
		"title":   "无权限发布",
		"message": "这条请求应该被拒绝。",
	}, token)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("broadcast without admin status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestListNotificationsSupportsFiltersPaginationAndSkipsExpired(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "notify-list@example.com")
	user := lookupUserByEmail(t, app, "notify-list@example.com")

	now := time.Now().UTC()
	oldest := now.Add(-3 * time.Hour)
	middle := now.Add(-2 * time.Hour)
	newest := now.Add(-1 * time.Hour)
	expiredCreated := now.Add(-30 * time.Minute)
	expiredAt := now.Add(-5 * time.Minute)

	oldestNotification := models.Notification{
		Base:      models.Base{CreatedAt: oldest, UpdatedAt: oldest},
		UserID:    user.ID,
		Type:      "system",
		Title:     "最旧未读系统通知",
		Message:   "oldest",
		Data:      jsonMap(nil),
		ExpiresAt: nil,
	}
	middleNotification := models.Notification{
		Base:    models.Base{CreatedAt: middle, UpdatedAt: middle},
		UserID:  user.ID,
		Type:    "system",
		Title:   "已读系统通知",
		Message: "middle",
		Data:    jsonMap(nil),
		IsRead:  true,
		ReadAt:  &middle,
	}
	newestNotification := models.Notification{
		Base:    models.Base{CreatedAt: newest, UpdatedAt: newest},
		UserID:  user.ID,
		Type:    "project",
		Title:   "最新项目通知",
		Message: "newest",
		Data:    jsonMap(nil),
	}
	expiredNotification := models.Notification{
		Base:      models.Base{CreatedAt: expiredCreated, UpdatedAt: expiredCreated},
		UserID:    user.ID,
		Type:      "system",
		Title:     "已过期通知",
		Message:   "expired",
		Data:      jsonMap(nil),
		ExpiresAt: &expiredAt,
	}
	for _, item := range []*models.Notification{&oldestNotification, &middleNotification, &newestNotification, &expiredNotification} {
		if err := app.db.Create(item).Error; err != nil {
			t.Fatalf("seed notifications: %v", err)
		}
	}

	list := request(app, http.MethodGet, "/api/v1/system/notifications?limit=2&offset=0", nil, token)
	if list.Code != http.StatusOK {
		t.Fatalf("list notifications status = %d, body = %s", list.Code, list.Body.String())
	}
	var items []models.Notification
	if err := json.Unmarshal(list.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode notifications: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("notifications length = %d, want 2", len(items))
	}
	if items[0].ID != newestNotification.ID || items[1].ID != middleNotification.ID {
		t.Fatalf("unexpected notification order: %#v", items)
	}

	filtered := request(app, http.MethodGet, "/api/v1/system/notifications?type=system&is_read=false&limit=10&offset=0", nil, token)
	if filtered.Code != http.StatusOK {
		t.Fatalf("filtered notifications status = %d, body = %s", filtered.Code, filtered.Body.String())
	}
	var filteredItems []models.Notification
	if err := json.Unmarshal(filtered.Body.Bytes(), &filteredItems); err != nil {
		t.Fatalf("decode filtered notifications: %v", err)
	}
	if len(filteredItems) != 1 || filteredItems[0].ID != oldestNotification.ID {
		t.Fatalf("unexpected filtered notifications: %#v", filteredItems)
	}
}

func TestNotificationStatsIncludeCountsByTypeAndLegacyFields(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "notify-stats@example.com")
	user := lookupUserByEmail(t, app, "notify-stats@example.com")

	now := time.Now().UTC()
	expiredAt := now.Add(-10 * time.Minute)
	notifications := []models.Notification{
		{
			UserID:  user.ID,
			Type:    "system",
			Title:   "系统未读",
			Message: "a",
			Data:    jsonMap(nil),
		},
		{
			UserID:  user.ID,
			Type:    "system",
			Title:   "系统已读",
			Message: "b",
			Data:    jsonMap(nil),
			IsRead:  true,
			ReadAt:  &now,
		},
		{
			UserID:  user.ID,
			Type:    "project",
			Title:   "项目未读",
			Message: "c",
			Data:    jsonMap(nil),
		},
		{
			UserID:    user.ID,
			Type:      "security",
			Title:     "已过期安全通知",
			Message:   "d",
			Data:      jsonMap(nil),
			ExpiresAt: &expiredAt,
		},
	}
	for _, item := range notifications {
		if err := app.db.Create(&item).Error; err != nil {
			t.Fatalf("seed notification stats: %v", err)
		}
	}

	resp := request(app, http.MethodGet, "/api/v1/system/notifications/stats", nil, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("notification stats status = %d, body = %s", resp.Code, resp.Body.String())
	}

	var body struct {
		TotalCount  int64            `json:"total_count"`
		UnreadCount int64            `json:"unread_count"`
		ReadCount   int64            `json:"read_count"`
		ByType      map[string]int64 `json:"by_type"`
		Total       int64            `json:"total"`
		Unread      int64            `json:"unread"`
		Read        int64            `json:"read"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode notification stats: %v", err)
	}

	if body.TotalCount != 3 || body.UnreadCount != 2 || body.ReadCount != 1 {
		t.Fatalf("unexpected count fields: %#v", body)
	}
	if body.Total != 3 || body.Unread != 2 || body.Read != 1 {
		t.Fatalf("unexpected legacy count fields: %#v", body)
	}
	if body.ByType["system"] != 2 || body.ByType["project"] != 1 {
		t.Fatalf("unexpected by_type stats: %#v", body.ByType)
	}
	if _, exists := body.ByType["security"]; exists {
		t.Fatalf("expired notification type should be excluded: %#v", body.ByType)
	}
}

func registerAndLogin(t *testing.T, app *App, email string) string {
	t.Helper()
	emailHash := sha256.Sum256([]byte(email))
	username := "u" + hex.EncodeToString(emailHash[:])[:12]
	issueTestEmailCode(t, app, email, emailCodePurposeRegister, "654321")
	register := request(app, http.MethodPost, "/api/v1/auth/register", map[string]any{
		"username": username,
		"email":    email,
		"password": "Pass1234",
		"code":     "654321",
	}, "")
	if register.Code != http.StatusCreated {
		t.Fatalf("register status = %d, body = %s", register.Code, register.Body.String())
	}
	login := request(app, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"email":    email,
		"password": "Pass1234",
	}, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", login.Code, login.Body.String())
	}
	var body struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	_ = json.Unmarshal(login.Body.Bytes(), &body)
	return body.Tokens.AccessToken
}

func issueTestEmailCode(t *testing.T, app *App, email, purpose, code string) {
	t.Helper()
	email = normalizeEmail(email)
	record := models.AuthEmailCode{
		Email:     email,
		Purpose:   purpose,
		CodeHash:  secureHash(app.cfg.SecretKey, email+"|"+purpose+"|"+code),
		ExpiresAt: time.Now().UTC().Add(emailCodeTTL),
	}
	if err := app.db.Create(&record).Error; err != nil {
		t.Fatalf("issue test email code: %v", err)
	}
}

func registerAndMakeAdmin(t *testing.T, app *App, email string) string {
	t.Helper()
	token := registerAndLogin(t, app, email)
	if err := app.db.Model(&models.User{}).Where("email = ?", email).Update("is_admin", true).Error; err != nil {
		t.Fatalf("promote admin user: %v", err)
	}
	return token
}

func lookupUserByEmail(t *testing.T, app *App, email string) models.User {
	t.Helper()
	var user models.User
	if err := app.db.First(&user, "email = ?", email).Error; err != nil {
		t.Fatalf("load user %s: %v", email, err)
	}
	return user
}

func registerAndLoginWithCredits(t *testing.T, app *App, email string, totalCredits int64) string {
	t.Helper()
	token := registerAndLogin(t, app, email)
	user := lookupUserByEmail(t, app, email)
	setTestCredits(t, app, user.ID, totalCredits, 0)
	return token
}

func setTestCredits(t *testing.T, app *App, userID uuid.UUID, totalCredits int64, usedCredits int64) {
	t.Helper()
	account, err := app.ensureTokenAccount(userID)
	if err != nil {
		t.Fatalf("ensure account: %v", err)
	}
	account.TotalTokens = totalCredits
	account.UsedTokens = usedCredits
	if err := app.db.Save(&account).Error; err != nil {
		t.Fatalf("save account credits: %v", err)
	}
}

func loginOnly(t *testing.T, app *App, email string) string {
	t.Helper()
	login := request(app, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"email":    email,
		"password": "Pass1234",
	}, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", login.Code, login.Body.String())
	}
	var body struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	_ = json.Unmarshal(login.Body.Bytes(), &body)
	return body.Tokens.AccessToken
}

func request(app *App, method, path string, body any, token string) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	return w
}

func creemWebhookRequest(t *testing.T, app *App, secret string, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal creem payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/billing/creem/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("creem-signature", hex.EncodeToString(mac.Sum(nil)))
	}
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	return w
}

func creemRedirectPayload(apiKey string, params map[string]string) map[string]any {
	keys := make([]string, 0, len(params))
	for key, value := range params {
		value = strings.TrimSpace(value)
		if value == "" || strings.EqualFold(value, "null") {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	out := make(map[string]any, len(keys)+1)
	for _, key := range keys {
		value := strings.TrimSpace(params[key])
		out[key] = value
		pairs = append(pairs, key+"="+value)
	}
	mac := hmac.New(sha256.New, []byte(apiKey))
	mac.Write([]byte(strings.Join(pairs, "&")))
	out["signature"] = hex.EncodeToString(mac.Sum(nil))
	return out
}

func createTestProject(t *testing.T, app *App, token string, name string) models.Project {
	t.Helper()
	create := request(app, http.MethodPost, "/api/v1/projects/", map[string]any{
		"name": name,
	}, token)
	if create.Code != http.StatusCreated {
		t.Fatalf("create project status = %d, body = %s", create.Code, create.Body.String())
	}
	var project models.Project
	if err := json.Unmarshal(create.Body.Bytes(), &project); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	return project
}

func TestHealth(t *testing.T) {
	app := newTestApp(t)
	w := request(app, http.MethodGet, "/health", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestOpenAPISchema(t *testing.T) {
	app := newTestApp(t)
	w := request(app, http.MethodGet, "/api/v1/openapi.json", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("openapi status = %d, body = %s", w.Code, w.Body.String())
	}
	var spec map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &spec); err != nil {
		t.Fatalf("decode openapi: %v", err)
	}
	if spec["openapi"] != "3.0.3" {
		t.Fatalf("openapi version = %#v", spec["openapi"])
	}
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("missing paths")
	}
	for _, path := range []string{
		"/api/v1/auth/login",
		"/api/v1/projects/{project_id}",
		"/api/v1/tools/elevenlabs/scribe/transcribe",
		"/api/v1/tools/generate-script-stream",
		"/api/v1/tools/timeline-view/decide",
		"/api/v1/tools/timeline-view/analyze",
		"/api/v1/tools/frame-workflow/subtitles/validate",
		"/api/v1/tools/frame-workflow/narrative-analyze",
		"/api/v1/tools/media-proxy",
		"/api/v1/admin/statistics/overview",
	} {
		if _, exists := paths[path]; !exists {
			t.Fatalf("missing path %s", path)
		}
	}
	components := spec["components"].(map[string]any)
	security := components["securitySchemes"].(map[string]any)
	if _, exists := security["BearerAuth"]; !exists {
		t.Fatalf("missing BearerAuth")
	}
}

func TestPromptTemplatesEndpointUsesMigratedTemplates(t *testing.T) {
	app := newTestApp(t)
	w := request(app, http.MethodGet, "/api/v1/tools/prompt-templates", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("prompt templates status = %d, body = %s", w.Code, w.Body.String())
	}
	var body struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode templates: %v", err)
	}
	expectedOrder := readFastPromptTemplateOrder(t)
	if len(body.Items) != len(expectedOrder) {
		t.Fatalf("templates = %d, want %d", len(body.Items), len(expectedOrder))
	}
	for index, id := range expectedOrder {
		if body.Items[index].ID != id {
			t.Fatalf("template order[%d] = %q, want %q", index, body.Items[index].ID, id)
		}
	}
}

func TestPromptTemplatesEndpointHonorsTemplateType(t *testing.T) {
	app := newTestApp(t)
	w := request(app, http.MethodGet, "/api/v1/tools/prompt-templates?type=video_analysis&template_type=fast", nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("prompt templates status = %d, body = %s", w.Code, w.Body.String())
	}
	var body struct {
		Items []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode templates: %v", err)
	}
	preferredOrder := readFastPromptTemplateOrder(t)
	if len(body.Items) != len(preferredOrder) {
		t.Fatalf("templates = %d, want %d; body = %s", len(body.Items), len(preferredOrder), w.Body.String())
	}
	for index, id := range preferredOrder {
		if body.Items[index].ID != id {
			t.Fatalf("template order[%d] = %q, want %q", index, body.Items[index].ID, id)
		}
	}
	for _, item := range body.Items {
		if item.Type != "fast" {
			t.Fatalf("template %s type = %q, want fast", item.ID, item.Type)
		}
	}
}

func readFastPromptTemplateOrder(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile("../../prompt/template_order.json")
	if err != nil {
		t.Fatalf("read prompt template order: %v", err)
	}
	var orderByCategory map[string][]string
	if err := json.Unmarshal(data, &orderByCategory); err != nil {
		t.Fatalf("decode prompt template order: %v", err)
	}
	return orderByCategory["fast"]
}

func TestRegisterLoginAndProjectCRUD(t *testing.T) {
	app := newTestApp(t)

	issueTestEmailCode(t, app, "admin@example.com", emailCodePurposeRegister, "654321")
	register := request(app, http.MethodPost, "/api/v1/auth/register", map[string]any{
		"username": "admin",
		"email":    "admin@example.com",
		"password": "Pass1234",
		"code":     "654321",
	}, "")
	if register.Code != http.StatusCreated {
		t.Fatalf("register status = %d, body = %s", register.Code, register.Body.String())
	}

	login := request(app, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"email":    "admin@example.com",
		"password": "Pass1234",
	}, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", login.Code, login.Body.String())
	}
	var loginBody struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &loginBody); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if loginBody.Tokens.AccessToken == "" {
		t.Fatal("empty access token")
	}

	create := request(app, http.MethodPost, "/api/v1/projects/", map[string]any{
		"name":        "测试项目",
		"description": "Go 迁移回归测试",
	}, loginBody.Tokens.AccessToken)
	if create.Code != http.StatusCreated {
		t.Fatalf("create project status = %d, body = %s", create.Code, create.Body.String())
	}

	list := request(app, http.MethodGet, "/api/v1/projects/", nil, loginBody.Tokens.AccessToken)
	if list.Code != http.StatusOK {
		t.Fatalf("list projects status = %d, body = %s", list.Code, list.Body.String())
	}
	var listBody struct {
		Projects []models.Project `json:"projects"`
		Items    []models.Project `json:"items"`
		Total    int              `json:"total"`
		HasMore  bool             `json:"has_more"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listBody.Total != 1 {
		t.Fatalf("total = %d, want 1", listBody.Total)
	}
	if len(listBody.Projects) != 1 {
		t.Fatalf("projects = %d, want 1; body = %s", len(listBody.Projects), list.Body.String())
	}
	if len(listBody.Items) != 1 {
		t.Fatalf("items = %d, want 1; body = %s", len(listBody.Items), list.Body.String())
	}
	if listBody.HasMore {
		t.Fatal("has_more = true, want false")
	}
}

func TestEmailCodeRequestStoresHashAndEnforcesCooldown(t *testing.T) {
	app := newTestApp(t)
	mailer := &fakeMailer{}
	app.mailer = mailer

	resp := request(app, http.MethodPost, "/api/v1/auth/request-email-code", map[string]any{
		"email":   "NewUser@Example.COM",
		"purpose": "register",
	}, "")
	if resp.Code != http.StatusOK {
		t.Fatalf("request email code = %d, body = %s", resp.Code, resp.Body.String())
	}
	if len(mailer.sends) != 1 || mailer.sends[0].to != "newuser@example.com" || mailer.sends[0].purpose != emailCodePurposeRegister || mailer.sends[0].locale != "en" {
		t.Fatalf("unexpected mail sends: %#v", mailer.sends)
	}

	var record models.AuthEmailCode
	if err := app.db.First(&record, "email = ? AND purpose = ?", "newuser@example.com", emailCodePurposeRegister).Error; err != nil {
		t.Fatalf("load email code: %v", err)
	}
	if record.CodeHash == "" || record.CodeHash == mailer.sends[0].code {
		t.Fatalf("email code should be stored as a hash, got record=%#v mail=%#v", record, mailer.sends[0])
	}

	again := request(app, http.MethodPost, "/api/v1/auth/request-email-code", map[string]any{
		"email": "newuser@example.com",
	}, "")
	if again.Code != http.StatusTooManyRequests {
		t.Fatalf("cooldown response = %d, body = %s", again.Code, again.Body.String())
	}
}

func TestEmailCodeRequestCarriesLocaleToMailer(t *testing.T) {
	app := newTestApp(t)
	mailer := &fakeMailer{}
	app.mailer = mailer

	resp := request(app, http.MethodPost, "/api/v1/auth/request-email-code", map[string]any{
		"email":   "FrenchUser@Example.COM",
		"purpose": "register",
		"locale":  "fr",
	}, "")
	if resp.Code != http.StatusOK {
		t.Fatalf("request email code with locale = %d, body = %s", resp.Code, resp.Body.String())
	}
	if len(mailer.sends) != 1 || mailer.sends[0].locale != "fr" {
		t.Fatalf("unexpected localized mail sends: %#v", mailer.sends)
	}

	app = newTestApp(t)
	mailer = &fakeMailer{}
	app.mailer = mailer
	body, _ := json.Marshal(map[string]any{
		"email":   "GermanUser@Example.COM",
		"purpose": "register",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/request-email-code", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", "de-DE,de;q=0.9,en;q=0.8")
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("request email code with accept-language = %d, body = %s", w.Code, w.Body.String())
	}
	if len(mailer.sends) != 1 || mailer.sends[0].locale != "de" {
		t.Fatalf("unexpected accept-language mail sends: %#v", mailer.sends)
	}
}

func TestEmailCodeMessageIsLocalized(t *testing.T) {
	subject, body := emailCodeMessage("123456", emailCodePurposeRegister, "en")
	if subject != "NarratoAI email verification code" || !strings.Contains(body, "complete registration") || !strings.Contains(body, "123456") {
		t.Fatalf("unexpected English email message: subject=%q body=%q", subject, body)
	}

	subject, body = emailCodeMessage("123456", emailCodePurposeRegister, "")
	if subject != "NarratoAI email verification code" || !strings.Contains(body, "complete registration") || !strings.Contains(body, "123456") {
		t.Fatalf("unexpected default email message: subject=%q body=%q", subject, body)
	}

	subject, body = emailCodeMessage("123456", emailCodePurposeRegister, "fr")
	if subject != "Code de vérification e-mail NarratoAI" || !strings.Contains(body, "finaliser votre inscription") || !strings.Contains(body, "123456") {
		t.Fatalf("unexpected French email message: subject=%q body=%q", subject, body)
	}

	subject, body = emailCodeMessage("123456", emailCodePurposeRegister, "de")
	if subject != "NarratoAI E-Mail-Bestätigungscode" || !strings.Contains(body, "die Registrierung abzuschließen") || !strings.Contains(body, "123456") {
		t.Fatalf("unexpected German email message: subject=%q body=%q", subject, body)
	}

	subject, body = emailCodeMessage("123456", emailCodePurposeRegister, "zh-CN")
	if subject != "NarratoAI 邮箱验证码" || !strings.Contains(body, "完成注册") || !strings.Contains(body, "123456") {
		t.Fatalf("unexpected Chinese email message: subject=%q body=%q", subject, body)
	}
}

func TestRegisterRequiresAndConsumesEmailCode(t *testing.T) {
	app := newTestApp(t)
	payload := map[string]any{
		"username": "email_code_user",
		"email":    "email-code@example.com",
		"password": "Pass1234",
	}

	missing := request(app, http.MethodPost, "/api/v1/auth/register", payload, "")
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("register without code = %d, body = %s", missing.Code, missing.Body.String())
	}

	issueTestEmailCode(t, app, "email-code@example.com", emailCodePurposeRegister, "654321")
	payload["code"] = "000000"
	wrong := request(app, http.MethodPost, "/api/v1/auth/register", payload, "")
	if wrong.Code != http.StatusBadRequest {
		t.Fatalf("register wrong code = %d, body = %s", wrong.Code, wrong.Body.String())
	}

	payload["code"] = "654321"
	okResp := request(app, http.MethodPost, "/api/v1/auth/register", payload, "")
	if okResp.Code != http.StatusCreated {
		t.Fatalf("register correct code = %d, body = %s", okResp.Code, okResp.Body.String())
	}
	user := lookupUserByEmail(t, app, "email-code@example.com")
	if !user.IsVerified || user.EmailVerifiedAt == nil || !user.PasswordAuthEnabled {
		t.Fatalf("registered user was not marked verified/password-enabled: %#v", user)
	}

	payload["username"] = "email_code_user2"
	reuse := request(app, http.MethodPost, "/api/v1/auth/register", payload, "")
	if reuse.Code != http.StatusBadRequest {
		t.Fatalf("reused code response = %d, body = %s", reuse.Code, reuse.Body.String())
	}
}

func TestRegisterReportsDuplicateEmailAndUsernameOnlyForUniqueConflicts(t *testing.T) {
	app := newTestApp(t)

	issueTestEmailCode(t, app, "duplicate@example.com", emailCodePurposeRegister, "654321")
	first := request(app, http.MethodPost, "/api/v1/auth/register", map[string]any{
		"username": "duplicate_user",
		"email":    "duplicate@example.com",
		"password": "Pass1234",
		"code":     "654321",
	}, "")
	if first.Code != http.StatusCreated {
		t.Fatalf("first register status = %d, body = %s", first.Code, first.Body.String())
	}

	issueTestEmailCode(t, app, "duplicate@example.com", emailCodePurposeRegister, "123456")
	duplicateEmail := request(app, http.MethodPost, "/api/v1/auth/register", map[string]any{
		"username": "duplicate_email_user",
		"email":    "duplicate@example.com",
		"password": "Pass1234",
		"code":     "123456",
	}, "")
	if duplicateEmail.Code != http.StatusConflict {
		t.Fatalf("duplicate email status = %d, body = %s", duplicateEmail.Code, duplicateEmail.Body.String())
	}
	if !strings.Contains(duplicateEmail.Body.String(), "邮箱") {
		t.Fatalf("duplicate email message should mention email, body = %s", duplicateEmail.Body.String())
	}

	issueTestEmailCode(t, app, "duplicate-username@example.com", emailCodePurposeRegister, "234567")
	duplicateUsername := request(app, http.MethodPost, "/api/v1/auth/register", map[string]any{
		"username": "duplicate_user",
		"email":    "duplicate-username@example.com",
		"password": "Pass1234",
		"code":     "234567",
	}, "")
	if duplicateUsername.Code != http.StatusConflict {
		t.Fatalf("duplicate username status = %d, body = %s", duplicateUsername.Code, duplicateUsername.Body.String())
	}
	if !strings.Contains(duplicateUsername.Body.String(), "用户名") {
		t.Fatalf("duplicate username message should mention username, body = %s", duplicateUsername.Body.String())
	}
}

func TestRegisterDoesNotReportInternalTransactionFailureAsDuplicateUser(t *testing.T) {
	app := newTestApp(t)
	if err := app.db.Migrator().DropTable(&models.UserProfile{}); err != nil {
		t.Fatalf("drop user_profiles: %v", err)
	}
	issueTestEmailCode(t, app, "profile-failure@example.com", emailCodePurposeRegister, "654321")

	resp := request(app, http.MethodPost, "/api/v1/auth/register", map[string]any{
		"username": "profile_failure",
		"email":    "profile-failure@example.com",
		"password": "Pass1234",
		"code":     "654321",
	}, "")
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("register with profile failure status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "已存在") {
		t.Fatalf("internal failure should not be reported as duplicate user, body = %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "注册失败") {
		t.Fatalf("internal failure should return registration failure message, body = %s", resp.Body.String())
	}
}

func TestRegisterValidatesUsernameAndPasswordPolicy(t *testing.T) {
	app := newTestApp(t)

	invalidUsername := request(app, http.MethodPost, "/api/v1/auth/register", map[string]any{
		"username": "bad username",
		"email":    "bad-username@example.com",
		"password": "Pass1234",
		"code":     "654321",
	}, "")
	if invalidUsername.Code != http.StatusBadRequest {
		t.Fatalf("invalid username response = %d, body = %s", invalidUsername.Code, invalidUsername.Body.String())
	}

	weakPassword := request(app, http.MethodPost, "/api/v1/auth/register", map[string]any{
		"username": "weak_password",
		"email":    "weak-password@example.com",
		"password": "password",
		"code":     "654321",
	}, "")
	if weakPassword.Code != http.StatusBadRequest {
		t.Fatalf("weak password response = %d, body = %s", weakPassword.Code, weakPassword.Body.String())
	}
}

func TestResetPasswordRequiresEmailCodeAndEnablesPasswordAuth(t *testing.T) {
	app := newTestApp(t)
	user := models.User{
		Username:            "oauth_only_reset",
		Email:               "oauth-reset@example.com",
		IsActive:            true,
		IsVerified:          true,
		PasswordAuthEnabled: false,
		EmailVerifiedAt:     ptrTime(time.Now().UTC()),
	}
	if err := app.db.Create(&user).Error; err != nil {
		t.Fatalf("create oauth-only user: %v", err)
	}

	noCode := request(app, http.MethodPost, "/api/v1/auth/reset-password", map[string]any{
		"email":            user.Email,
		"new_password":     "Newpass123",
		"confirm_password": "Newpass123",
	}, "")
	if noCode.Code != http.StatusBadRequest {
		t.Fatalf("reset without code = %d, body = %s", noCode.Code, noCode.Body.String())
	}

	issueTestEmailCode(t, app, user.Email, emailCodePurposeReset, "112233")
	reset := request(app, http.MethodPost, "/api/v1/auth/reset-password", map[string]any{
		"email":            user.Email,
		"code":             "112233",
		"new_password":     "Newpass123",
		"confirm_password": "Newpass123",
	}, "")
	if reset.Code != http.StatusOK {
		t.Fatalf("reset with code = %d, body = %s", reset.Code, reset.Body.String())
	}

	login := request(app, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"email":    user.Email,
		"password": "Newpass123",
	}, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login after reset = %d, body = %s", login.Code, login.Body.String())
	}
}

func TestLoginAllowsExistingPasswordHashWhenPasswordAuthFlagIsFalse(t *testing.T) {
	app := newTestApp(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("Pass1234"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := models.User{
		Username:            "legacy_password_user",
		Email:               "legacy-password@example.com",
		PasswordHash:        string(hash),
		PasswordAuthEnabled: false,
		IsActive:            true,
		IsVerified:          true,
		EmailVerifiedAt:     ptrTime(time.Now().UTC()),
	}
	if err := app.db.Create(&user).Error; err != nil {
		t.Fatalf("create legacy password user: %v", err)
	}
	if err := app.db.Model(&models.User{}).Where("id = ?", user.ID).Update("password_auth_enabled", false).Error; err != nil {
		t.Fatalf("mark legacy password auth disabled: %v", err)
	}

	login := request(app, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"email":    user.Email,
		"password": "Pass1234",
	}, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login with existing hash = %d, body = %s", login.Code, login.Body.String())
	}
	user = lookupUserByEmail(t, app, user.Email)
	if !user.PasswordAuthEnabled {
		t.Fatalf("password auth flag should be repaired after successful login: %#v", user)
	}
}

func TestOAuthCallbackCreatesTicketAndExchangeConsumesOnce(t *testing.T) {
	app := newTestApp(t)
	app.cfg.GoogleClientID = "google-client"
	app.cfg.GoogleClientSecret = "google-secret"
	app.cfg.FrontendBaseURL = "https://app.example.test"
	app.cfg.FrontendBaseURLs = []string{"https://app.example.test", "https://app-alt.example.test"}
	app.cfg.GoogleTokenURL = "https://proxy.example.test/google-oauth/token"
	app.cfg.GoogleUserInfoURL = "https://proxy.example.test/google-openid/v1/userinfo"
	app.oauthHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://proxy.example.test/google-oauth/token":
			return jsonHTTPResponse(req, http.StatusOK, `{"access_token":"provider-token"}`), nil
		case "https://proxy.example.test/google-openid/v1/userinfo":
			return jsonHTTPResponse(req, http.StatusOK, `{"sub":"google-user-1","email":"oauth-new@example.com","email_verified":true,"name":"OAuth New","picture":"https://cdn.example/avatar.png"}`), nil
		default:
			return jsonHTTPResponse(req, http.StatusNotFound, `{}`), nil
		}
	})}

	start := request(app, http.MethodGet, "/api/v1/auth/oauth/google/start?redirect=/projects&frontend_origin=https%3A%2F%2Fapp-alt.example.test", nil, "")
	if start.Code != http.StatusFound {
		t.Fatalf("oauth start = %d, body = %s", start.Code, start.Body.String())
	}
	authURL, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse auth redirect: %v", err)
	}
	state := authURL.Query().Get("state")
	if state == "" || authURL.Query().Get("code_challenge") == "" {
		t.Fatalf("oauth start missing state/challenge: %s", authURL.String())
	}

	callback := request(app, http.MethodGet, "/api/v1/auth/oauth/google/callback?code=auth-code&state="+url.QueryEscape(state), nil, "")
	if callback.Code != http.StatusFound {
		t.Fatalf("oauth callback = %d, body = %s", callback.Code, callback.Body.String())
	}
	callbackURL, err := url.Parse(callback.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse callback redirect: %v", err)
	}
	if callbackURL.Scheme != "https" || callbackURL.Host != "app-alt.example.test" || callbackURL.Path != oauthCallbackFrontend {
		t.Fatalf("unexpected frontend callback url: %s", callbackURL.String())
	}
	ticket := callbackURL.Query().Get("ticket")
	if ticket == "" {
		t.Fatalf("frontend callback missing ticket: %s", callbackURL.String())
	}

	exchange := request(app, http.MethodPost, "/api/v1/auth/oauth/exchange", map[string]any{"ticket": ticket}, "")
	if exchange.Code != http.StatusOK {
		t.Fatalf("oauth exchange = %d, body = %s", exchange.Code, exchange.Body.String())
	}
	if !bytes.Contains(exchange.Body.Bytes(), []byte(`"redirect_path":"/projects"`)) {
		t.Fatalf("oauth exchange did not preserve redirect_path: %s", exchange.Body.String())
	}
	secondExchange := request(app, http.MethodPost, "/api/v1/auth/oauth/exchange", map[string]any{"ticket": ticket}, "")
	if secondExchange.Code != http.StatusUnauthorized {
		t.Fatalf("oauth ticket reuse = %d, body = %s", secondExchange.Code, secondExchange.Body.String())
	}

	user := lookupUserByEmail(t, app, "oauth-new@example.com")
	if !user.PasswordAuthEnabled {
		t.Fatalf("new OAuth user should keep password auth enabled by default: %#v", user)
	}
	var account models.OAuthAccount
	if err := app.db.First(&account, "provider = ? AND provider_user_id = ?", oauthProviderGoogle, "google-user-1").Error; err != nil {
		t.Fatalf("oauth account was not created: %v", err)
	}
}

func TestOAuthVerifiedEmailLinksExistingUserAndRejectsUnverifiedEmail(t *testing.T) {
	app := newTestApp(t)
	registerAndLogin(t, app, "linked@example.com")
	existing := lookupUserByEmail(t, app, "linked@example.com")

	linked, err := app.resolveOAuthUser(oauthProviderGoogle, oauthUserProfile{
		ID:            "google-linked",
		Email:         "linked@example.com",
		EmailVerified: true,
		Name:          "Linked User",
	})
	if err != nil {
		t.Fatalf("link verified oauth email: %v", err)
	}
	if linked.ID != existing.ID {
		t.Fatalf("oauth should link existing user, got %s want %s", linked.ID, existing.ID)
	}

	var account models.OAuthAccount
	if err := app.db.First(&account, "provider = ? AND provider_user_id = ?", oauthProviderGoogle, "google-linked").Error; err != nil {
		t.Fatalf("linked oauth account missing: %v", err)
	}
	_, err = app.resolveOAuthUser(oauthProviderGoogle, oauthUserProfile{
		ID:            "google-unverified",
		Email:         "unverified@example.com",
		EmailVerified: false,
	})
	if err == nil {
		t.Fatal("expected unverified provider email to be rejected")
	}
}

func TestListProjectsMatchesFrontendContract(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "projects@example.com")

	for _, payload := range []map[string]any{
		{"name": "Bravo"},
		{
			"name":             "Alpha",
			"project_metadata": map[string]any{"inference_mode": "deep"},
			"workflow": map[string]any{
				"mode":               "frame",
				"status":             "draft",
				"current_step":       "step_10_generate_narration_script",
				"current_step_index": 10,
				"revision":           7,
				"stages": map[string]any{
					"config": map[string]any{
						"data": map[string]any{"model_level": "frame", "unused_large_config": strings.Repeat("x", 1024)},
					},
					"source_video": map[string]any{
						"data": map[string]any{"video_context": map[string]any{"items": map[string]any{"1": map[string]any{"visual": strings.Repeat("heavy visual ", 128)}}}},
					},
					"script": map[string]any{
						"data": map[string]any{"ai_analysis_script": map[string]any{"items": []any{map[string]any{"text": strings.Repeat("heavy script ", 128)}}}},
					},
				},
			},
		},
		{"name": "Charlie", "project_metadata": map[string]any{"inference_mode": "frame"}},
	} {
		create := request(app, http.MethodPost, "/api/v1/projects/", payload, token)
		if create.Code != http.StatusCreated {
			t.Fatalf("create project %q status = %d, body = %s", payload["name"], create.Code, create.Body.String())
		}
	}

	firstPage := request(app, http.MethodGet, "/api/v1/projects/?limit=1&offset=0&sort_by=name&sort_order=asc", nil, token)
	if firstPage.Code != http.StatusOK {
		t.Fatalf("list first page status = %d, body = %s", firstPage.Code, firstPage.Body.String())
	}
	var firstBody struct {
		Projects []models.Project `json:"projects"`
		Items    []models.Project `json:"items"`
		Total    int              `json:"total"`
		HasMore  bool             `json:"has_more"`
	}
	if err := json.Unmarshal(firstPage.Body.Bytes(), &firstBody); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	if firstBody.Total != 3 {
		t.Fatalf("total = %d, want 3", firstBody.Total)
	}
	if len(firstBody.Projects) != 1 || firstBody.Projects[0].Name != "Alpha" {
		t.Fatalf("first projects = %#v, want Alpha", firstBody.Projects)
	}
	if len(firstBody.Items) != len(firstBody.Projects) {
		t.Fatalf("items length = %d, want projects length %d", len(firstBody.Items), len(firstBody.Projects))
	}
	var metadata map[string]any
	if err := json.Unmarshal(firstBody.Projects[0].ProjectMetadata, &metadata); err != nil {
		t.Fatalf("decode project metadata: %v", err)
	}
	if metadata["inference_mode"] != "deep" {
		t.Fatalf("inference_mode = %v, want deep", metadata["inference_mode"])
	}
	firstPageJSON := firstPage.Body.String()
	for _, excluded := range []string{`"subtitle"`, `"video_context"`, `"ai_analysis_script"`} {
		if strings.Contains(firstPageJSON, excluded) {
			t.Fatalf("list response contains heavy workflow field %s: %s", excluded, firstPageJSON)
		}
	}
	if strings.Contains(firstPageJSON, "unused_large_config") || strings.Contains(firstPageJSON, "heavy visual") || strings.Contains(firstPageJSON, "heavy script") {
		t.Fatalf("list response contains oversized workflow stage data: %s", firstPageJSON)
	}
	for _, included := range []string{`"metadata"`, `"workflow"`, `"artifacts"`} {
		if !strings.Contains(firstPageJSON, included) {
			t.Fatalf("list response missing frontend list field %s: %s", included, firstPageJSON)
		}
	}
	var workflow map[string]any
	if err := json.Unmarshal(firstBody.Projects[0].Workflow, &workflow); err != nil {
		t.Fatalf("decode project workflow: %v", err)
	}
	if workflow["current_step"] == "" || workflow["stages"] == nil {
		t.Fatalf("list workflow summary is incomplete: %#v", workflow)
	}
	if !firstBody.HasMore {
		t.Fatal("first has_more = false, want true")
	}

	secondPage := request(app, http.MethodGet, "/api/v1/projects/?limit=1&offset=1&sort_by=name&sort_order=asc", nil, token)
	if secondPage.Code != http.StatusOK {
		t.Fatalf("list second page status = %d, body = %s", secondPage.Code, secondPage.Body.String())
	}
	var secondBody struct {
		Projects []models.Project `json:"projects"`
		HasMore  bool             `json:"has_more"`
	}
	if err := json.Unmarshal(secondPage.Body.Bytes(), &secondBody); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	if len(secondBody.Projects) != 1 || secondBody.Projects[0].Name != "Bravo" {
		t.Fatalf("second projects = %#v, want Bravo", secondBody.Projects)
	}
	if !secondBody.HasMore {
		t.Fatal("second has_more = false, want true")
	}
}

func TestASRBillsBySecond(t *testing.T) {
	app := newTestApp(t)
	// 0.00033 CNY per second, 1 credit = 0.00002 CNY → 16.5 credits per second (990 per minute).
	seedBillingRates(t, app, map[string]any{"asr": []map[string]any{{
		"provider": "dashscope", "model_id": "fun-asr", "cost_per_minute": 0.0198, "credits_per_minute": 990,
	}}})
	for _, tc := range []struct {
		seconds float64
		credits int64
		cost    float64
	}{
		{1, 17, 0.00033},
		{10, 165, 0.0033},
		{61, 1007, 0.02013},
		{0.5, 9, 0.000165},
	} {
		seconds := tc.seconds
		amounts, err := app.billingAmountsForConsumption(creditConsumption{Provider: "dashscope", ServiceKind: "asr", ModelID: "fun-asr", DurationSeconds: &seconds})
		if err != nil || amounts.CreditCount != tc.credits || math.Abs(amounts.CostAmount-tc.cost) > 0.000001 {
			t.Fatalf("%v s: credits=%d cost=%v err=%v, want %d / %v", tc.seconds, amounts.CreditCount, amounts.CostAmount, err, tc.credits, tc.cost)
		}
	}
}
