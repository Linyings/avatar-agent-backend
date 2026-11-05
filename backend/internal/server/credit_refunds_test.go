package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/shopspring/decimal"
)

type fakeWeChatCreditRefunder struct {
	*fakeWeChatPayClient
	refundCalls int
	refundErr   error
}

func (f *fakeWeChatCreditRefunder) RefundCreditPayment(context.Context, string, string, float64) error {
	f.refundCalls++
	return f.refundErr
}

func TestRefundRevokesOnlyOriginalLotRemainder(t *testing.T) {
	app := newTestApp(t)
	userID := uuid.New()
	now := time.Now().UTC()
	original := seedPurchasedCreditLot(t, app, userID, 120, now.Add(time.Hour))
	original.GrantedCredits = 500
	if err := app.db.Save(&original).Error; err != nil {
		t.Fatalf("mark original lot partially spent: %v", err)
	}
	other := seedPurchasedCreditLot(t, app, userID, 1000, now.Add(2*time.Hour))
	if err := app.db.Create(&models.UserTokenAccount{UserID: userID, TotalTokens: 100}).Error; err != nil {
		t.Fatalf("seed legacy balance: %v", err)
	}
	if err := app.db.Create(&models.CreditTrialClaim{UserID: userID, PaymentOrderID: original.PaymentOrderID}).Error; err != nil {
		t.Fatalf("seed trial claim: %v", err)
	}
	var revoked int64
	for i := 0; i < 2; i++ {
		var err error
		revoked, err = revokePurchasedLotRemainder(app.db, original.PaymentOrderID, now)
		if err != nil {
			t.Fatalf("refund attempt %d: %v", i+1, err)
		}
		want := int64(0)
		if i == 0 {
			want = 120
		}
		if revoked != want {
			t.Fatalf("refund attempt %d revoked = %d, want %d", i+1, revoked, want)
		}
	}
	if reloadPurchasedCreditLot(t, app, original.ID).RemainingCredits != 0 {
		t.Fatalf("original lot not revoked")
	}
	if reloadPurchasedCreditLot(t, app, other.ID).RemainingCredits != 1000 {
		t.Fatalf("other lot changed")
	}
	var ledgers []models.CreditLedgerRecord
	if err := app.db.Where("user_id = ? AND event_type = ?", userID, "refund").Find(&ledgers).Error; err != nil {
		t.Fatalf("load refund ledger: %v", err)
	}
	if len(ledgers) != 1 {
		t.Fatalf("refund ledger count = %d, want 1", len(ledgers))
	}
	if ledgers[0].ChangeAmount != -120 || ledgers[0].BalanceAfter != 1100 {
		t.Fatalf("refund ledger = change %d balance %d, want -120/1100", ledgers[0].ChangeAmount, ledgers[0].BalanceAfter)
	}
	var claimCount int64
	if err := app.db.Model(&models.CreditTrialClaim{}).Where("user_id = ?", userID).Count(&claimCount).Error; err != nil {
		t.Fatalf("count trial claim: %v", err)
	}
	if claimCount != 1 {
		t.Fatalf("trial claim count = %d, want 1", claimCount)
	}
}

func TestRefundRequiredTrialLoserRefundsWithoutLotAndAcknowledgesDuplicates(t *testing.T) {
	app := newTestApp(t)
	fake := &fakeWeChatCreditRefunder{fakeWeChatPayClient: &fakeWeChatPayClient{configured: true}}
	app.wechat = fake
	userID := uuid.New()
	winnerOrderID := uuid.New()
	if err := app.db.Create(&models.CreditTrialClaim{UserID: userID, PaymentOrderID: winnerOrderID}).Error; err != nil {
		t.Fatalf("seed winner claim: %v", err)
	}
	loser := models.CreditPaymentOrder{
		UserID: userID, PackageID: uuid.New(), Provider: "wechat", OutTradeNo: "trial-loser-" + uuid.NewString(),
		ProviderTransactionID: "wx-loser", Status: "refund_required", AmountCNY: decimal.RequireFromString("1.90"),
		Credits: 500, PackageName: "Trial", IsTrial: true,
	}
	if err := app.db.Create(&loser).Error; err != nil {
		t.Fatalf("seed paid trial loser: %v", err)
	}

	for i := 0; i < 2; i++ {
		response := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(response)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/credit-payments/"+loser.OutTradeNo+"/refund", nil)
		c.Params = gin.Params{{Key: "out_trade_no", Value: loser.OutTradeNo}}
		app.refundCreditPayment(c)
		if response.Code != http.StatusOK {
			t.Fatalf("refund attempt %d status = %d body = %s", i+1, response.Code, response.Body.String())
		}
	}
	if fake.refundCalls != 1 {
		t.Fatalf("provider refund calls = %d, want 1", fake.refundCalls)
	}
	var refunded models.CreditPaymentOrder
	if err := app.db.First(&refunded, "id = ?", loser.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refunded.Status != "refunded" || refunded.RefundedAt == nil {
		t.Fatalf("loser refund state = status %q refunded_at %v", refunded.Status, refunded.RefundedAt)
	}
	if _, err := settleCreditPaymentOrder(app.db, verifiedCreditPayment{Provider: "wechat", OutTradeNo: loser.OutTradeNo, TransactionID: "wx-loser", PaidAt: time.Now().UTC(), AmountCNY: loser.AmountCNY, Payload: []byte(`{"duplicate":true}`)}); err != nil {
		t.Fatalf("duplicate settlement after loser refund: %v", err)
	}
	var lotCount, claimCount, refundLedgerCount int64
	app.db.Model(&models.PurchasedCreditLot{}).Where("payment_order_id = ?", loser.ID).Count(&lotCount)
	app.db.Model(&models.CreditTrialClaim{}).Where("user_id = ?", userID).Count(&claimCount)
	app.db.Model(&models.CreditLedgerRecord{}).Where("user_id = ? AND event_type = ?", userID, "refund").Count(&refundLedgerCount)
	if lotCount != 0 || claimCount != 1 || refundLedgerCount != 0 {
		t.Fatalf("loser accounting = lots %d claims %d refund_ledgers %d, want 0/1/0", lotCount, claimCount, refundLedgerCount)
	}
}
