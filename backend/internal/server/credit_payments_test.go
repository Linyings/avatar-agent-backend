package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
	"github.com/shopspring/decimal"
)

type fakeAlipayCreditPaymentClient struct {
	*fakeAlipaySubscriptionClient
	queryTx  providers.AlipayTransaction
	queryErr error
}

func (f *fakeAlipayCreditPaymentClient) PagePaymentURL(context.Context, providers.AlipayPagePaymentRequest) (string, error) {
	return "https://cashier.alipay.test/credit", nil
}

func (f *fakeAlipayCreditPaymentClient) PrecreateCreditPayment(context.Context, providers.AlipayPrecreateRequest) (providers.AlipayPrecreateResponse, error) {
	return providers.AlipayPrecreateResponse{QRCode: "https://qr.alipay.com/test/credit", OutTradeNo: "CR1"}, nil
}

func (f *fakeAlipayCreditPaymentClient) QueryCreditPayment(context.Context, string) (providers.AlipayTransaction, error) {
	return f.queryTx, f.queryErr
}

func seedCreditPaymentOrder(t *testing.T, app *App, provider string) *models.CreditPaymentOrder {
	t.Helper()
	userID := uuid.New()
	pkg := models.TokenPackage{Name: "strict-" + uuid.NewString(), DisplayName: "Strict payment", TokenAmount: 500, Price: 1.90, Currency: "CNY", ValidityHours: 720, IsActive: true}
	if err := app.db.Create(&pkg).Error; err != nil {
		t.Fatal(err)
	}
	order, err := createCreditPaymentOrder(app.db, userID, pkg.ID, provider, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return order
}

func validWeChatCreditTransaction(app *App, order *models.CreditPaymentOrder) providers.WeChatTransaction {
	tx := providers.WeChatTransaction{
		AppID: app.cfg.WeChatAppID, MchID: app.cfg.WeChatMchID, OutTradeNo: order.OutTradeNo,
		TransactionID: "WX-" + uuid.NewString(), TradeState: "SUCCESS", SuccessTime: "2026-08-03T14:05:06+08:00",
	}
	tx.Amount.Total = 190
	tx.Amount.Currency = "CNY"
	return tx
}

func validAlipayCreditTransaction(app *App, order *models.CreditPaymentOrder) providers.AlipayTransaction {
	return providers.AlipayTransaction{
		AppID: app.cfg.AlipayAppID, SellerID: app.cfg.AlipayMerchantID,
		OutTradeNo: order.OutTradeNo, TransactionID: "ALI-" + uuid.NewString(),
		Status: "TRADE_SUCCESS", Amount: "1.90", PaymentTime: "2026-08-03 14:05:06",
	}
}

func TestPrepareAlipayCreditPaymentUsesPrecreateQRCodeWhenConfigured(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AlipayPaymentMethod = "precreate"
	app.alipay = &fakeAlipayCreditPaymentClient{fakeAlipaySubscriptionClient: &fakeAlipaySubscriptionClient{configured: true}}
	order := seedCreditPaymentOrder(t, app, "alipay")
	if err := app.prepareAlipayCreditPayment(context.Background(), order); err != nil {
		t.Fatal(err)
	}
	if order.CodeURL != "https://qr.alipay.com/test/credit" {
		t.Fatalf("code_url = %q", order.CodeURL)
	}
}

func TestPrepareAlipayCreditPaymentUsesPageURLByDefault(t *testing.T) {
	app := newTestApp(t)
	app.alipay = &fakeAlipayCreditPaymentClient{fakeAlipaySubscriptionClient: &fakeAlipaySubscriptionClient{configured: true}}
	order := seedCreditPaymentOrder(t, app, "alipay")
	if err := app.prepareAlipayCreditPayment(context.Background(), order); err != nil {
		t.Fatal(err)
	}
	if order.CodeURL != "https://cashier.alipay.test/credit" {
		t.Fatalf("code_url = %q", order.CodeURL)
	}
}

func TestCreditWeChatSettlementRejectsIncompleteProviderEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*providers.WeChatTransaction)
	}{
		{name: "missing trade state", mutate: func(tx *providers.WeChatTransaction) { tx.TradeState = "" }},
		{name: "unsuccessful trade state", mutate: func(tx *providers.WeChatTransaction) { tx.TradeState = "NOTPAY" }},
		{name: "missing app id", mutate: func(tx *providers.WeChatTransaction) { tx.AppID = "" }},
		{name: "wrong app id", mutate: func(tx *providers.WeChatTransaction) { tx.AppID = "other-app" }},
		{name: "missing merchant id", mutate: func(tx *providers.WeChatTransaction) { tx.MchID = "" }},
		{name: "wrong merchant id", mutate: func(tx *providers.WeChatTransaction) { tx.MchID = "other-mch" }},
		{name: "missing amount", mutate: func(tx *providers.WeChatTransaction) { tx.Amount.Total = 0 }},
		{name: "negative amount", mutate: func(tx *providers.WeChatTransaction) { tx.Amount.Total = -1 }},
		{name: "mismatched amount", mutate: func(tx *providers.WeChatTransaction) { tx.Amount.Total = 191 }},
		{name: "missing currency", mutate: func(tx *providers.WeChatTransaction) { tx.Amount.Currency = "" }},
		{name: "non-CNY currency", mutate: func(tx *providers.WeChatTransaction) { tx.Amount.Currency = "USD" }},
		{name: "missing order number", mutate: func(tx *providers.WeChatTransaction) { tx.OutTradeNo = "" }},
		{name: "wrong order number", mutate: func(tx *providers.WeChatTransaction) { tx.OutTradeNo = "CR-WRONG" }},
		{name: "missing transaction id", mutate: func(tx *providers.WeChatTransaction) { tx.TransactionID = "" }},
		{name: "missing paid time", mutate: func(tx *providers.WeChatTransaction) { tx.SuccessTime = "" }},
		{name: "invalid paid time", mutate: func(tx *providers.WeChatTransaction) { tx.SuccessTime = "not-a-time" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newTestApp(t)
			app.cfg.WeChatAppID = "wx-app"
			app.cfg.WeChatMchID = "wx-mch"
			order := seedCreditPaymentOrder(t, app, "wechat")
			tx := validWeChatCreditTransaction(app, order)
			tt.mutate(&tx)
			if err := app.processCreditWeChatTransaction(tx); err == nil {
				t.Fatal("incomplete WeChat evidence was accepted")
			}
			var lotCount int64
			app.db.Model(&models.PurchasedCreditLot{}).Where("payment_order_id = ?", order.ID).Count(&lotCount)
			var current models.CreditPaymentOrder
			if err := app.db.First(&current, "id = ?", order.ID).Error; err != nil {
				t.Fatal(err)
			}
			if lotCount != 0 || current.Status != "pending" {
				t.Fatalf("invalid evidence changed settlement: lots=%d status=%q", lotCount, current.Status)
			}
		})
	}
}

func TestCreditWeChatSettlementUsesExactProviderPaidTime(t *testing.T) {
	app := newTestApp(t)
	app.cfg.WeChatAppID = "wx-app"
	app.cfg.WeChatMchID = "wx-mch"
	order := seedCreditPaymentOrder(t, app, "wechat")
	tx := validWeChatCreditTransaction(app, order)
	if err := app.processCreditWeChatTransaction(tx); err != nil {
		t.Fatal(err)
	}
	wantPaidAt := time.Date(2026, 8, 3, 6, 5, 6, 0, time.UTC)
	var current models.CreditPaymentOrder
	if err := app.db.First(&current, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.PaidAt == nil || !current.PaidAt.Equal(wantPaidAt) || current.ExpiresAt == nil || !current.ExpiresAt.Equal(wantPaidAt.Add(720*time.Hour)) {
		t.Fatalf("paid_at/expires_at = %v/%v", current.PaidAt, current.ExpiresAt)
	}
}

func TestCreditAlipayQueryRejectsIncompleteProviderEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*providers.AlipayTransaction)
	}{
		{name: "missing app id", mutate: func(tx *providers.AlipayTransaction) { tx.AppID = "" }},
		{name: "wrong app id", mutate: func(tx *providers.AlipayTransaction) { tx.AppID = "other-app" }},
		{name: "missing seller id", mutate: func(tx *providers.AlipayTransaction) { tx.SellerID = "" }},
		{name: "wrong seller id", mutate: func(tx *providers.AlipayTransaction) { tx.SellerID = "other-seller" }},
		{name: "missing status", mutate: func(tx *providers.AlipayTransaction) { tx.Status = "" }},
		{name: "unsuccessful status", mutate: func(tx *providers.AlipayTransaction) { tx.Status = "WAIT_BUYER_PAY" }},
		{name: "missing amount", mutate: func(tx *providers.AlipayTransaction) { tx.Amount = "" }},
		{name: "mismatched amount", mutate: func(tx *providers.AlipayTransaction) { tx.Amount = "1.91" }},
		{name: "missing order number", mutate: func(tx *providers.AlipayTransaction) { tx.OutTradeNo = "" }},
		{name: "wrong order number", mutate: func(tx *providers.AlipayTransaction) { tx.OutTradeNo = "CR-WRONG" }},
		{name: "missing transaction id", mutate: func(tx *providers.AlipayTransaction) { tx.TransactionID = "" }},
		{name: "missing paid time", mutate: func(tx *providers.AlipayTransaction) { tx.PaymentTime = "" }},
		{name: "invalid paid time", mutate: func(tx *providers.AlipayTransaction) { tx.PaymentTime = "not-a-time" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newTestApp(t)
			app.cfg.AlipayAppID = "ali-app"
			app.cfg.AlipayMerchantID = "ali-seller"
			order := seedCreditPaymentOrder(t, app, "alipay")
			tx := validAlipayCreditTransaction(app, order)
			tt.mutate(&tx)
			app.alipay = &fakeAlipayCreditPaymentClient{fakeAlipaySubscriptionClient: &fakeAlipaySubscriptionClient{configured: true}, queryTx: tx}
			if err := app.syncAlipayCreditPayment(context.Background(), order); err == nil {
				t.Fatal("incomplete Alipay query evidence was accepted")
			}
			var lotCount int64
			app.db.Model(&models.PurchasedCreditLot{}).Where("payment_order_id = ?", order.ID).Count(&lotCount)
			if lotCount != 0 {
				t.Fatalf("invalid query created %d lots", lotCount)
			}
		})
	}
}

func TestCreditAlipayQueryUsesShanghaiProviderPaidTime(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AlipayAppID = "ali-app"
	app.cfg.AlipayMerchantID = "ali-seller"
	order := seedCreditPaymentOrder(t, app, "alipay")
	tx := validAlipayCreditTransaction(app, order)
	app.alipay = &fakeAlipayCreditPaymentClient{fakeAlipaySubscriptionClient: &fakeAlipaySubscriptionClient{configured: true}, queryTx: tx}
	if err := app.syncAlipayCreditPayment(context.Background(), order); err != nil {
		t.Fatal(err)
	}
	wantPaidAt := time.Date(2026, 8, 3, 6, 5, 6, 0, time.UTC)
	var current models.CreditPaymentOrder
	if err := app.db.First(&current, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.PaidAt == nil || !current.PaidAt.Equal(wantPaidAt) || current.ExpiresAt == nil || !current.ExpiresAt.Equal(wantPaidAt.Add(720*time.Hour)) {
		t.Fatalf("paid_at/expires_at = %v/%v", current.PaidAt, current.ExpiresAt)
	}
}

func TestCreditAlipayWebhookRejectsIncompleteProviderEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(url.Values)
	}{
		{name: "missing app id", mutate: func(v url.Values) { v.Del("app_id") }},
		{name: "wrong app id", mutate: func(v url.Values) { v.Set("app_id", "other-app") }},
		{name: "missing seller id", mutate: func(v url.Values) { v.Del("seller_id") }},
		{name: "wrong seller id", mutate: func(v url.Values) { v.Set("seller_id", "other-seller") }},
		{name: "missing status", mutate: func(v url.Values) { v.Del("trade_status") }},
		{name: "unsuccessful status", mutate: func(v url.Values) { v.Set("trade_status", "WAIT_BUYER_PAY") }},
		{name: "missing amount", mutate: func(v url.Values) { v.Del("total_amount") }},
		{name: "mismatched amount", mutate: func(v url.Values) { v.Set("total_amount", "1.91") }},
		{name: "missing order number", mutate: func(v url.Values) { v.Del("out_trade_no") }},
		{name: "wrong order number", mutate: func(v url.Values) { v.Set("out_trade_no", "CR-WRONG") }},
		{name: "missing transaction id", mutate: func(v url.Values) { v.Del("trade_no") }},
		{name: "missing paid time", mutate: func(v url.Values) { v.Del("gmt_payment") }},
		{name: "invalid paid time", mutate: func(v url.Values) { v.Set("gmt_payment", "not-a-time") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newTestApp(t)
			app.cfg.AlipayAppID = "ali-app"
			app.cfg.AlipayMerchantID = "ali-seller"
			app.alipay = &fakeAlipaySubscriptionClient{configured: true}
			order := seedCreditPaymentOrder(t, app, "alipay")
			values := url.Values{
				"app_id": {app.cfg.AlipayAppID}, "seller_id": {app.cfg.AlipayMerchantID},
				"trade_status": {"TRADE_SUCCESS"}, "total_amount": {"1.90"},
				"out_trade_no": {order.OutTradeNo}, "trade_no": {"ALI-CALLBACK"},
				"gmt_payment": {"2026-08-03 14:05:06"},
			}
			tt.mutate(values)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/credit-payments/alipay/webhook", strings.NewReader(values.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			app.Router().ServeHTTP(w, req)
			if w.Code >= 200 && w.Code < 300 {
				t.Fatalf("invalid callback status = %d body=%q", w.Code, w.Body.String())
			}
			var lotCount int64
			app.db.Model(&models.PurchasedCreditLot{}).Where("payment_order_id = ?", order.ID).Count(&lotCount)
			if lotCount != 0 {
				t.Fatalf("invalid callback created %d lots", lotCount)
			}
		})
	}
}

func TestCreditAlipayVerifiedDuplicateCallbackIsIdempotent(t *testing.T) {
	app := newTestApp(t)
	app.cfg.AlipayAppID = "ali-app"
	app.cfg.AlipayMerchantID = "ali-seller"
	app.alipay = &fakeAlipaySubscriptionClient{configured: true}
	order := seedCreditPaymentOrder(t, app, "alipay")
	values := url.Values{
		"app_id": {app.cfg.AlipayAppID}, "seller_id": {app.cfg.AlipayMerchantID},
		"trade_status": {"TRADE_SUCCESS"}, "total_amount": {"1.90"},
		"out_trade_no": {order.OutTradeNo}, "trade_no": {"ALI-CALLBACK"},
		"gmt_payment": {"2026-08-03 14:05:06"},
	}
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/credit-payments/alipay/webhook", strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		app.Router().ServeHTTP(w, req)
		if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "success" {
			t.Fatalf("callback %d status=%d body=%q", i, w.Code, w.Body.String())
		}
	}
	wantPaidAt := time.Date(2026, 8, 3, 6, 5, 6, 0, time.UTC)
	var current models.CreditPaymentOrder
	if err := app.db.First(&current, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.PaidAt == nil || !current.PaidAt.Equal(wantPaidAt) || current.ExpiresAt == nil || !current.ExpiresAt.Equal(wantPaidAt.Add(720*time.Hour)) {
		t.Fatalf("paid_at/expires_at = %v/%v", current.PaidAt, current.ExpiresAt)
	}
	var lotCount, rechargeCount, ledgerCount int64
	app.db.Model(&models.PurchasedCreditLot{}).Where("payment_order_id = ?", order.ID).Count(&lotCount)
	app.db.Model(&models.TokenRechargeRecord{}).Where("payment_id = ?", order.ID).Count(&rechargeCount)
	app.db.Model(&models.CreditLedgerRecord{}).Where("user_id = ? AND event_type = ?", order.UserID, creditLedgerEventRecharge).Count(&ledgerCount)
	if lotCount != 1 || rechargeCount != 1 || ledgerCount != 1 {
		t.Fatalf("duplicate accounting = lots %d recharges %d ledgers %d", lotCount, rechargeCount, ledgerCount)
	}
}

func TestRechargeLedgerBalanceUsesSettlementProcessingTime(t *testing.T) {
	app := newTestApp(t)
	order := seedCreditPaymentOrder(t, app, "alipay")
	processingAt := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	paidAt := processingAt.Add(-2 * time.Hour)
	seedPurchasedCreditLot(t, app, order.UserID, 250, processingAt.Add(-time.Hour))
	if _, err := settleCreditPaymentOrderAt(app.db, verifiedCreditPayment{
		Provider: "alipay", OutTradeNo: order.OutTradeNo, TransactionID: "ALI-LEDGER",
		AmountCNY: decimal.RequireFromString("1.90"), PaidAt: paidAt,
	}, processingAt); err != nil {
		t.Fatal(err)
	}
	var ledger models.CreditLedgerRecord
	if err := app.db.Where("user_id = ? AND event_type = ?", order.UserID, creditLedgerEventRecharge).First(&ledger).Error; err != nil {
		t.Fatal(err)
	}
	if ledger.BalanceAfter != 500 {
		t.Fatalf("balance_after = %d, want current post-settlement balance 500", ledger.BalanceAfter)
	}
	if !ledger.RecordedAt.Equal(processingAt) {
		t.Fatalf("recorded_at = %v, want settlement processing time %v", ledger.RecordedAt, processingAt)
	}
}

func TestSettlementRejectsProviderAndAmountMismatch(t *testing.T) {
	app := newTestApp(t)
	userID := uuid.New()
	pkg := models.TokenPackage{Name: "provider-match", DisplayName: "Provider match", TokenAmount: 500, Price: 1.90, Currency: "CNY", ValidityHours: 720, IsActive: true}
	if err := app.db.Create(&pkg).Error; err != nil {
		t.Fatal(err)
	}
	order, err := createCreditPaymentOrder(app.db, userID, pkg.ID, "wechat", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	_, err = settleCreditPaymentOrder(app.db, verifiedCreditPayment{
		Provider: "alipay", OutTradeNo: order.OutTradeNo, TransactionID: "ali-cross-provider",
		AmountCNY: decimal.RequireFromString("1.90"), PaidAt: time.Now().UTC(),
	})
	if !errors.Is(err, ErrPaymentProviderMismatch) {
		t.Fatalf("provider mismatch error = %v", err)
	}
	_, err = settleCreditPaymentOrder(app.db, verifiedCreditPayment{
		Provider: "wechat", OutTradeNo: order.OutTradeNo, TransactionID: "wx-wrong-amount",
		AmountCNY: decimal.RequireFromString("1.91"), PaidAt: time.Now().UTC(),
	})
	if !errors.Is(err, ErrPaymentAmountMismatch) {
		t.Fatalf("amount mismatch error = %v", err)
	}
	var got models.CreditPaymentOrder
	if err := app.db.First(&got, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	var lotCount int64
	app.db.Model(&models.PurchasedCreditLot{}).Where("payment_order_id = ?", order.ID).Count(&lotCount)
	if got.Status != "pending" || lotCount != 0 {
		t.Fatalf("mismatched provider changed order: status %q lots %d", got.Status, lotCount)
	}
}

func TestCreditPaymentSettlementSnapshotsAndIsIdempotent(t *testing.T) {
	app := newTestApp(t)
	userID := uuid.New()
	if err := app.db.Create(&models.UserTokenAccount{UserID: userID, TotalTokens: 100}).Error; err != nil {
		t.Fatalf("seed legacy balance: %v", err)
	}
	seedPurchasedCreditLot(t, app, userID, 250, time.Now().UTC().Add(time.Hour))
	pkg := models.TokenPackage{Name: "pay-test", DisplayName: "Pay test", TokenAmount: 450, BonusTokens: 50, Price: 1.90, Currency: "CNY", ValidityHours: 720, IsActive: true}
	if err := app.db.Create(&pkg).Error; err != nil {
		t.Fatal(err)
	}
	order, err := createCreditPaymentOrder(app.db, userID, pkg.ID, "alipay", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if order.Credits != 500 || !order.AmountCNY.Equal(decimal.RequireFromString("1.90")) {
		t.Fatalf("bad snapshot: %+v", order)
	}
	paidAt := time.Now().UTC().Truncate(time.Second)
	first, err := settleCreditPaymentOrder(app.db, verifiedCreditPayment{Provider: "alipay", OutTradeNo: order.OutTradeNo, TransactionID: "ali-1", PaidAt: paidAt, AmountCNY: decimal.RequireFromString("1.90"), Payload: []byte(`{"ok":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := settleCreditPaymentOrder(app.db, verifiedCreditPayment{Provider: "alipay", OutTradeNo: order.OutTradeNo, TransactionID: "ali-1", PaidAt: paidAt, AmountCNY: decimal.RequireFromString("1.90")})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || !first.ExpiresAt.Equal(paidAt.Add(720*time.Hour)) {
		t.Fatalf("settlement not idempotent: %+v %+v", first, second)
	}
	var ledgers []models.CreditLedgerRecord
	if err := app.db.Model(&models.CreditLedgerRecord{}).
		Where("user_id = ? AND event_type = ?", userID, creditLedgerEventRecharge).
		Find(&ledgers).Error; err != nil {
		t.Fatalf("load recharge ledger records: %v", err)
	}
	if len(ledgers) != 1 {
		t.Fatalf("recharge ledger records = %d, want 1", len(ledgers))
	}
	if ledgers[0].BalanceAfter != 850 {
		t.Fatalf("recharge balance_after = %d, want full balance 850", ledgers[0].BalanceAfter)
	}
}

func TestCreditWeChatSettlementPersistsVerifiedTransactionEvidence(t *testing.T) {
	app := newTestApp(t)
	app.cfg.WeChatAppID = "wx-app"
	app.cfg.WeChatMchID = "mch-id"
	userID := uuid.New()
	pkg := models.TokenPackage{Name: "wechat-evidence", DisplayName: "WeChat evidence", TokenAmount: 500, Price: 1.90, Currency: "CNY", ValidityHours: 720, IsActive: true}
	if err := app.db.Create(&pkg).Error; err != nil {
		t.Fatal(err)
	}
	order, err := createCreditPaymentOrder(app.db, userID, pkg.ID, "wechat", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	tx := providers.WeChatTransaction{
		AppID: "wx-app", MchID: "mch-id", OutTradeNo: order.OutTradeNo,
		TransactionID: "wx-verified", TradeState: "SUCCESS", SuccessTime: time.Now().UTC().Format(time.RFC3339),
	}
	tx.Amount.Total = 190
	tx.Amount.Currency = "CNY"
	if err := app.processCreditWeChatTransaction(tx); err != nil {
		t.Fatalf("process verified WeChat transaction: %v", err)
	}
	var settled models.CreditPaymentOrder
	if err := app.db.First(&settled, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(settled.ProviderPayload) == 0 {
		t.Fatal("verified WeChat transaction evidence was not persisted")
	}
	var evidence providers.WeChatTransaction
	if err := json.Unmarshal(settled.ProviderPayload, &evidence); err != nil {
		t.Fatalf("decode WeChat evidence: %v", err)
	}
	if evidence.OutTradeNo != order.OutTradeNo || evidence.TransactionID != "wx-verified" || evidence.MchID != "mch-id" || evidence.Amount.Total != 190 {
		t.Fatalf("unexpected WeChat evidence: %+v", evidence)
	}
}

func TestCreditTrialClaimed(t *testing.T) {
	app := newTestApp(t)
	userID := uuid.New()
	claimed, err := creditTrialClaimed(app.db, userID)
	if err != nil || claimed {
		t.Fatalf("unclaimed = %v, err = %v", claimed, err)
	}
	if err := app.db.Create(&models.CreditTrialClaim{UserID: userID, PaymentOrderID: uuid.New()}).Error; err != nil {
		t.Fatalf("create trial claim: %v", err)
	}
	claimed, err = creditTrialClaimed(app.db, userID)
	if err != nil || !claimed {
		t.Fatalf("claimed = %v, err = %v", claimed, err)
	}
}

func TestSettlementPersistsRefundRequiredAfterEarlierTrialRefund(t *testing.T) {
	app := newTestApp(t)
	userID := uuid.New()
	pkg := models.TokenPackage{Name: "trial-test", DisplayName: "Trial", TokenAmount: 500, Price: 1.90, Currency: "CNY", ValidityHours: 720, IsTrial: true, PerAccountPurchaseLimit: 1, IsActive: true}
	if err := app.db.Create(&pkg).Error; err != nil {
		t.Fatal(err)
	}
	first, _ := createCreditPaymentOrder(app.db, userID, pkg.ID, "wechat", time.Now().UTC())
	if _, err := settleCreditPaymentOrder(app.db, verifiedCreditPayment{Provider: "wechat", OutTradeNo: first.OutTradeNo, TransactionID: "wx-1", PaidAt: time.Now().UTC(), AmountCNY: decimal.RequireFromString("1.90")}); err != nil {
		t.Fatal(err)
	}
	if err := app.db.Model(&models.CreditPaymentOrder{}).Where("id = ?", first.ID).Update("status", "refunded").Error; err != nil {
		t.Fatal(err)
	}
	refundedWinnerLot, err := settleCreditPaymentOrder(app.db, verifiedCreditPayment{Provider: "wechat", OutTradeNo: first.OutTradeNo, TransactionID: "wx-1", PaidAt: time.Now().UTC(), AmountCNY: decimal.RequireFromString("1.90")})
	if err != nil || refundedWinnerLot == nil || refundedWinnerLot.PaymentOrderID != first.ID {
		t.Fatalf("normal refunded order settlement = lot %+v err %v", refundedWinnerLot, err)
	}
	second := models.CreditPaymentOrder{UserID: userID, PackageID: pkg.ID, Provider: "alipay", OutTradeNo: "trial-second-" + uuid.NewString(), Status: "pending", AmountCNY: decimal.RequireFromString("1.90"), Credits: 500, PackageName: pkg.Name, IsTrial: true}
	if err := app.db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	paidAt := time.Now().UTC().Truncate(time.Second)
	payload := []byte(`{"trade_status":"TRADE_SUCCESS"}`)
	lot, err := settleCreditPaymentOrder(app.db, verifiedCreditPayment{Provider: "alipay", OutTradeNo: second.OutTradeNo, TransactionID: "ali-2", PaidAt: paidAt, AmountCNY: decimal.RequireFromString("1.90"), Payload: payload})
	if err != nil {
		t.Fatalf("settle paid trial loser: %v", err)
	}
	if lot != nil {
		t.Fatalf("paid trial loser lot = %+v, want nil", lot)
	}

	var loser models.CreditPaymentOrder
	if err := app.db.First(&loser, "id = ?", second.ID).Error; err != nil {
		t.Fatalf("reload paid trial loser: %v", err)
	}
	if loser.Status != "refund_required" || loser.ProviderTransactionID != "ali-2" {
		t.Fatalf("paid trial loser = status %q transaction %q", loser.Status, loser.ProviderTransactionID)
	}
	if loser.PaidAt == nil || !loser.PaidAt.Equal(paidAt) || string(loser.ProviderPayload) != string(payload) {
		t.Fatalf("paid trial loser evidence = paid_at %v payload %s", loser.PaidAt, loser.ProviderPayload)
	}

	duplicate, err := settleCreditPaymentOrder(app.db, verifiedCreditPayment{Provider: "alipay", OutTradeNo: second.OutTradeNo, TransactionID: "ali-duplicate", PaidAt: paidAt.Add(time.Minute), AmountCNY: decimal.RequireFromString("1.90"), Payload: []byte(`{"duplicate":true}`)})
	if err != nil {
		t.Fatalf("duplicate paid trial loser settlement: %v", err)
	}
	if duplicate != nil {
		t.Fatalf("duplicate paid trial loser lot = %+v, want nil", duplicate)
	}
	var duplicateLoser models.CreditPaymentOrder
	if err := app.db.First(&duplicateLoser, "id = ?", second.ID).Error; err != nil {
		t.Fatalf("reload duplicate paid trial loser: %v", err)
	}
	if duplicateLoser.Status != "refund_required" || duplicateLoser.ProviderTransactionID != "ali-2" || duplicateLoser.PaidAt == nil || !duplicateLoser.PaidAt.Equal(paidAt) || string(duplicateLoser.ProviderPayload) != string(payload) {
		t.Fatalf("duplicate changed paid trial loser: %+v", duplicateLoser)
	}

	var claimCount, lotCount, rechargeCount, ledgerCount int64
	app.db.Model(&models.CreditTrialClaim{}).Where("user_id = ?", userID).Count(&claimCount)
	app.db.Model(&models.PurchasedCreditLot{}).Where("user_id = ?", userID).Count(&lotCount)
	app.db.Model(&models.TokenRechargeRecord{}).Where("user_id = ?", userID).Count(&rechargeCount)
	app.db.Model(&models.CreditLedgerRecord{}).Where("user_id = ? AND event_type = ?", userID, creditLedgerEventRecharge).Count(&ledgerCount)
	if claimCount != 1 || lotCount != 1 || rechargeCount != 1 || ledgerCount != 1 {
		t.Fatalf("accounting counts = claim %d lot %d recharge %d ledger %d, want 1 each", claimCount, lotCount, rechargeCount, ledgerCount)
	}
}

func TestConcurrentTrialSettlementGrantsOnlyOneLot(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("TEST_POSTGRES_URL"))
	if databaseURL == "" {
		t.Skip("TEST_POSTGRES_URL is not configured")
	}
	if !strings.Contains(strings.ToLower(databaseURL), "workflow") {
		t.Fatalf("TEST_POSTGRES_URL must point to the dedicated workflow test database")
	}

	app := newWorkflowRunBillingPostgresApp(t, databaseURL)
	userID := uuid.New()
	pkg := models.TokenPackage{Name: "trial-race-" + uuid.NewString(), DisplayName: "Trial race", TokenAmount: 500, Price: 1.90, Currency: "CNY", ValidityHours: 720, IsTrial: true, PerAccountPurchaseLimit: 1, IsActive: true}
	if err := app.db.Create(&pkg).Error; err != nil {
		t.Fatalf("create trial package: %v", err)
	}
	wechat := models.CreditPaymentOrder{UserID: userID, PackageID: pkg.ID, Provider: "wechat", OutTradeNo: "trial-wx-" + uuid.NewString(), Status: "pending", AmountCNY: decimal.RequireFromString("1.90"), Credits: 500, PackageName: pkg.Name, IsTrial: true}
	alipay := models.CreditPaymentOrder{UserID: userID, PackageID: pkg.ID, Provider: "alipay", OutTradeNo: "trial-ali-" + uuid.NewString(), Status: "pending", AmountCNY: decimal.RequireFromString("1.90"), Credits: 500, PackageName: pkg.Name, IsTrial: true}
	if err := app.db.Create(&wechat).Error; err != nil {
		t.Fatalf("create WeChat trial order: %v", err)
	}
	if err := app.db.Create(&alipay).Error; err != nil {
		t.Fatalf("create Alipay trial order: %v", err)
	}

	orders := []models.CreditPaymentOrder{wechat, alipay}
	start := make(chan struct{})
	results := make(chan struct {
		lot *models.PurchasedCreditLot
		err error
	}, len(orders))
	var wg sync.WaitGroup
	for i := range orders {
		order := orders[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			lot, err := settleCreditPaymentOrder(app.db, verifiedCreditPayment{Provider: order.Provider, OutTradeNo: order.OutTradeNo, TransactionID: order.Provider + "-transaction", PaidAt: time.Now().UTC(), AmountCNY: decimal.RequireFromString("1.90"), Payload: []byte(`{"provider":"` + order.Provider + `"}`)})
			results <- struct {
				lot *models.PurchasedCreditLot
				err error
			}{lot: lot, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	granted := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent settlement: %v", result.err)
		}
		if result.lot != nil {
			granted++
		}
	}
	if granted != 1 {
		t.Fatalf("settlements returning lots = %d, want 1", granted)
	}

	var claimCount, lotCount, paidCount, refundRequiredCount, rechargeCount, ledgerCount int64
	app.db.Model(&models.CreditTrialClaim{}).Where("user_id = ?", userID).Count(&claimCount)
	app.db.Model(&models.PurchasedCreditLot{}).Where("user_id = ?", userID).Count(&lotCount)
	app.db.Model(&models.CreditPaymentOrder{}).Where("user_id = ? AND status = ?", userID, "paid").Count(&paidCount)
	app.db.Model(&models.CreditPaymentOrder{}).Where("user_id = ? AND status = ?", userID, "refund_required").Count(&refundRequiredCount)
	app.db.Model(&models.TokenRechargeRecord{}).Where("user_id = ?", userID).Count(&rechargeCount)
	app.db.Model(&models.CreditLedgerRecord{}).Where("user_id = ? AND event_type = ?", userID, creditLedgerEventRecharge).Count(&ledgerCount)
	if claimCount != 1 || lotCount != 1 || paidCount != 1 || refundRequiredCount != 1 || rechargeCount != 1 || ledgerCount != 1 {
		t.Fatalf("race counts = claim %d lot %d paid %d refund_required %d recharge %d ledger %d, want 1 each", claimCount, lotCount, paidCount, refundRequiredCount, rechargeCount, ledgerCount)
	}
}
