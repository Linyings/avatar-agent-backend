package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
)

func TestConsumePurchasedCreditLotsUsesEarliestExpiry(t *testing.T) {
	app := newTestApp(t)
	userID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	early := seedPurchasedCreditLot(t, app, userID, 100, now.Add(time.Hour))
	late := seedPurchasedCreditLot(t, app, userID, 100, now.Add(2*time.Hour))

	consumed, err := consumePurchasedCreditLots(app.db, userID, 150, now)
	if err != nil {
		t.Fatalf("consume lots: %v", err)
	}
	if consumed != 150 {
		t.Fatalf("consumed = %d, want 150", consumed)
	}
	if got := reloadPurchasedCreditLot(t, app, early.ID).RemainingCredits; got != 0 {
		t.Fatalf("early remaining = %d, want 0", got)
	}
	if got := reloadPurchasedCreditLot(t, app, late.ID).RemainingCredits; got != 50 {
		t.Fatalf("late remaining = %d, want 50", got)
	}
	summary, err := purchasedCreditSummary(app.db, userID, now)
	if err != nil {
		t.Fatalf("summarize lots: %v", err)
	}
	if summary.Total != 200 || summary.Remaining != 50 {
		t.Fatalf("summary = total %d remaining %d, want 200/50", summary.Total, summary.Remaining)
	}
}

func TestConsumeCreditsUsesPurchasedLotsBeforeLegacyAccount(t *testing.T) {
	app := newTestApp(t)
	userID := uuid.New()
	expiresAt := time.Now().UTC().Add(time.Hour)
	lot := seedPurchasedCreditLot(t, app, userID, 100, expiresAt)
	seedBillingRates(t, app, map[string]any{
		"llm": []map[string]any{{"provider": "openai", "model_id": "lot-test", "tokens_per_credit": 1}},
	})

	err := app.consumeCredits(creditConsumption{UserID: userID, APIEndpoint: "/test", ModelID: "lot-test", ServiceKind: "llm", Usage: providers.ChatUsage{TotalTokens: 50}})
	if err != nil {
		t.Fatalf("consume credits: %v", err)
	}
	if got := reloadPurchasedCreditLot(t, app, lot.ID).RemainingCredits; got != 50 {
		t.Fatalf("lot remaining = %d, want 50", got)
	}
	var account models.UserTokenAccount
	if err := app.db.First(&account, "user_id = ?", userID).Error; err != nil {
		t.Fatalf("load legacy account: %v", err)
	}
	if account.UsedTokens != 0 {
		t.Fatalf("legacy used tokens = %d, want 0", account.UsedTokens)
	}
}

func TestPurchasedCreditBalanceExcludesExpiredLots(t *testing.T) {
	app := newTestApp(t)
	userID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	seedPurchasedCreditLot(t, app, userID, 80, now.Add(-time.Second))
	seedPurchasedCreditLot(t, app, userID, 20, now.Add(time.Hour))

	balance, nextExpiry, err := purchasedCreditBalance(app.db, userID, now)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != 20 {
		t.Fatalf("balance = %d, want 20", balance)
	}
	if nextExpiry == nil || !nextExpiry.Equal(now.Add(time.Hour)) {
		t.Fatalf("next expiry = %v, want %v", nextExpiry, now.Add(time.Hour))
	}
}

func TestExpiryClearsOnlyDueLotAndWritesLedgerOnce(t *testing.T) {
	app := newTestApp(t)
	userID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	if err := app.db.Create(&models.UserTokenAccount{UserID: userID, TotalTokens: 100}).Error; err != nil {
		t.Fatalf("seed legacy balance: %v", err)
	}
	due := seedPurchasedCreditLot(t, app, userID, 300, now.Add(-time.Second))
	future := seedPurchasedCreditLot(t, app, userID, 500, now.Add(time.Hour))
	zero := seedPurchasedCreditLot(t, app, userID, 0, now.Add(-time.Second))
	consumed := seedPurchasedCreditLot(t, app, userID, 0, now.Add(-time.Second))
	if err := app.db.Model(&consumed).Update("status", "consumed").Error; err != nil {
		t.Fatalf("mark zero-remainder lot consumed: %v", err)
	}

	for i := 0; i < 2; i++ {
		balance, _, err := purchasedCreditBalance(app.db, userID, now)
		if err != nil {
			t.Fatalf("balance read %d: %v", i+1, err)
		}
		if balance != 500 {
			t.Fatalf("balance read %d = %d, want 500", i+1, balance)
		}
	}

	if got := reloadPurchasedCreditLot(t, app, due.ID); got.Status != "expired" || got.RemainingCredits != 0 {
		t.Fatalf("due lot = status %q remaining %d, want expired/0", got.Status, got.RemainingCredits)
	}
	if got := reloadPurchasedCreditLot(t, app, future.ID); got.Status != "active" || got.RemainingCredits != 500 {
		t.Fatalf("future lot = status %q remaining %d, want active/500", got.Status, got.RemainingCredits)
	}
	if got := reloadPurchasedCreditLot(t, app, zero.ID); got.Status != "expired" || got.RemainingCredits != 0 {
		t.Fatalf("zero active lot = status %q remaining %d, want expired/0", got.Status, got.RemainingCredits)
	}
	if got := reloadPurchasedCreditLot(t, app, consumed.ID); got.Status != "consumed" || got.RemainingCredits != 0 {
		t.Fatalf("consumed lot = status %q remaining %d, want consumed/0", got.Status, got.RemainingCredits)
	}

	var records []models.CreditLedgerRecord
	if err := app.db.Where("user_id = ? AND event_type = ?", userID, "expiry").Find(&records).Error; err != nil {
		t.Fatalf("load expiry ledger: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expiry ledger count = %d, want 1", len(records))
	}
	if records[0].ChangeAmount != -300 || records[0].BalanceAfter != 600 {
		t.Fatalf("expiry ledger = change %d balance %d, want -300/600", records[0].ChangeAmount, records[0].BalanceAfter)
	}
	var metadata map[string]any
	if err := json.Unmarshal(records[0].RequestMetadata, &metadata); err != nil {
		t.Fatalf("decode expiry metadata: %v", err)
	}
	if metadata["lot_id"] != due.ID.String() {
		t.Fatalf("expiry ledger lot_id = %v, want %s", metadata["lot_id"], due.ID)
	}
}

func seedPurchasedCreditLot(t *testing.T, app *App, userID uuid.UUID, credits int64, expiresAt time.Time) models.PurchasedCreditLot {
	t.Helper()
	lot := models.PurchasedCreditLot{UserID: userID, PaymentOrderID: uuid.New(), GrantedCredits: credits, RemainingCredits: credits, Status: "active", ExpiresAt: expiresAt}
	if err := app.db.Create(&lot).Error; err != nil {
		t.Fatalf("seed lot: %v", err)
	}
	return lot
}

func reloadPurchasedCreditLot(t *testing.T, app *App, id uuid.UUID) models.PurchasedCreditLot {
	t.Helper()
	var lot models.PurchasedCreditLot
	if err := app.db.First(&lot, "id = ?", id).Error; err != nil {
		t.Fatalf("reload lot: %v", err)
	}
	return lot
}
