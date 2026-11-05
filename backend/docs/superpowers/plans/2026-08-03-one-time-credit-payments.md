# One-Time Credit Payments Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace new subscription checkout with CNY one-time Credits packages paid by WeChat Native Pay or Alipay QR, including independent 720-hour lots and a lifetime one-purchase trial package.

**Architecture:** `CreditPaymentOrder` is the provider-neutral order and `PurchasedCreditLot` is the only new purchased-credit grant. Provider adapters create/query/verify/refund payments, then call one transactional settlement path; balance and billing read lots in expiry order before historical subscription and legacy balances. Existing Creem, subscription, and historical payment data remain for compatibility but are not exposed for new sales.

**Tech Stack:** Go 1.x, Gin, GORM, PostgreSQL, shopspring/decimal, WeChat Pay v3, Alipay RSA2 OpenAPI, React, TypeScript, Mantine, Vitest, Docker Compose/WSL Ubuntu.

## Global Constraints

- Store and display all active package prices in CNY; never perform USD conversion in the new sale path.
- Migrate old numeric prices exactly once with `round(old_price * 7.2, 2)` and preserve historical balances and transactions.
- Every successful purchase creates one independent lot expiring at provider paid time plus exactly 720 hours.
- Consume active purchased lots by `expires_at ASC, created_at ASC`, then historical subscription grants, then legacy non-expiring purchased balance.
- Trial package is CNY 1.90 for 500 Credits, lifetime one successful purchase per account across both providers; refunds never restore eligibility.
- Retain Creem and subscription code and callbacks for compatibility, but hide and disable new subscription sales.
- Provider callbacks grant nothing unless signature, merchant identity, order, successful status, currency, and amount are verified.
- Production deployment is excluded until WSL tests and small real-channel payments pass and the user separately authorizes deployment.

---

### Task 1: Idempotent Package And Schema Migration

**Files:**
- Modify: `internal/models/models.go`
- Modify: `internal/database/database.go`
- Create: `internal/database/one_time_credit_migration.go`
- Create: `internal/database/one_time_credit_migration_test.go`

**Interfaces:**
- Produces: `func ApplyOneTimeCreditMigration(db *gorm.DB) error`
- Produces marker: `one_time_credit_packages_cny_v1`
- Produces one active trial package with `Currency="CNY"`, `ValidityHours=720`, `IsTrial=true`, `PerAccountPurchaseLimit=1`.

- [ ] **Step 1: Write failing migration tests**

```go
func TestApplyOneTimeCreditMigrationConvertsPriceOnce(t *testing.T) {
    db := migrationTestDB(t)
    pkg := models.TokenPackage{Name: "starter", DisplayName: "Starter", Price: 10, Currency: "USD", ValidityHours: 720, IsActive: true}
    require.NoError(t, db.Create(&pkg).Error)

    require.NoError(t, ApplyOneTimeCreditMigration(db))
    require.NoError(t, ApplyOneTimeCreditMigration(db))

    require.NoError(t, db.First(&pkg, "id = ?", pkg.ID).Error)
    require.Equal(t, "CNY", pkg.Currency)
    require.InDelta(t, 72.00, pkg.Price, 0.001)
}

func TestApplyOneTimeCreditMigrationUpsertsUniqueTrialWithoutTouchingBalances(t *testing.T) {
    db := migrationTestDB(t)
    account := models.TokenAccount{UserID: uuid.New(), TotalTokens: 900, UsedTokens: 100}
    require.NoError(t, db.Create(&account).Error)
    require.NoError(t, ApplyOneTimeCreditMigration(db))
    require.NoError(t, ApplyOneTimeCreditMigration(db))

    var trials []models.TokenPackage
    require.NoError(t, db.Where("is_trial = ? AND is_active = ?", true, true).Find(&trials).Error)
    require.Len(t, trials, 1)
    require.Equal(t, 1.90, trials[0].Price)
    require.Equal(t, int64(500), trials[0].TokenAmount)
    require.Equal(t, 720, trials[0].ValidityHours)
    require.NoError(t, db.First(&account, "user_id = ?", account.UserID).Error)
    require.Equal(t, int64(900), account.TotalTokens)
    require.Equal(t, int64(100), account.UsedTokens)
}
```

- [ ] **Step 2: Run tests and confirm the missing migration fails**

Run:
```powershell
wsl.exe -d Ubuntu -- bash -lc "cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-alipay-subscription && CGO_ENABLED=1 go test ./internal/database -run 'TestApplyOneTimeCreditMigration' -count=1"
```
Expected: FAIL with `undefined: ApplyOneTimeCreditMigration`.

- [ ] **Step 3: Implement a transactional marker migration and schema invariants**

```go
const oneTimeCreditMigrationKey = "one_time_credit_packages_cny_v1"

func ApplyOneTimeCreditMigration(db *gorm.DB) error {
    return db.Transaction(func(tx *gorm.DB) error {
        var marker models.SystemSetting
        err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
            Where("key = ?", oneTimeCreditMigrationKey).First(&marker).Error
        if err == nil {
            return nil
        }
        if !errors.Is(err, gorm.ErrRecordNotFound) {
            return err
        }
        if err := tx.Model(&models.TokenPackage{}).
            Where("currency <> ?", "CNY").
            Updates(map[string]any{
                "price": gorm.Expr("ROUND((price * 7.2)::numeric, 2)"),
                "currency": "CNY", "validity_hours": 720,
            }).Error; err != nil {
            return err
        }
        trial := models.TokenPackage{Name: "trial-500", DisplayName: "体验包", Price: 1.90, Currency: "CNY", TokenAmount: 500, BonusTokens: 0, ValidityHours: 720, IsTrial: true, PerAccountPurchaseLimit: 1, IsActive: true, IsFeatured: true, SortOrder: -100}
        if err := tx.Where("name = ?", trial.Name).Assign(trial).FirstOrCreate(&trial).Error; err != nil {
            return err
        }
        return tx.Create(&models.SystemSetting{Key: oneTimeCreditMigrationKey, Value: "applied"}).Error
    })
}
```

Ensure `AutoMigrate` includes `CreditPaymentOrder`, `PurchasedCreditLot`, and `CreditTrialClaim`, with unique indexes on `out_trade_no`, `payment_order_id`, and trial `user_id`. Call `ApplyOneTimeCreditMigration` only after schema migration succeeds.

- [ ] **Step 4: Run migration tests**

Run the command from Step 2. Expected: PASS; second invocation leaves price at `72.00`, creates one trial package, and preserves account balances.

- [ ] **Step 5: Commit**

```bash
git add internal/models/models.go internal/database/database.go internal/database/one_time_credit_migration.go internal/database/one_time_credit_migration_test.go
git commit -m "feat: migrate credit packages to cny lots"
```

### Task 2: Canonical Settlement, Trial Concurrency, Expiry, And Refund Accounting

**Files:**
- Modify: `internal/server/credit_payments.go`
- Modify: `internal/server/credit_payments_test.go`
- Modify: `internal/server/credit_lots.go`
- Create: `internal/server/credit_lots_test.go`
- Modify: `internal/server/credit_refunds.go`
- Create: `internal/server/credit_refunds_test.go`

**Interfaces:**
- Consumes: `CreditPaymentOrder`, `PurchasedCreditLot`, `CreditTrialClaim` from Task 1.
- Produces: `settleCreditPaymentOrder(db, verifiedCreditPayment) (*models.PurchasedCreditLot, error)`.
- Produces: `expirePurchasedCreditLots(tx, userID, now) error` with one expiry ledger event per lot.
- Produces: `persistPaidOrderAndAccounting(tx *gorm.DB, order *models.CreditPaymentOrder, lot *models.PurchasedCreditLot, paid verifiedCreditPayment) error`.
- Produces: `createLotLedgerOnce(tx *gorm.DB, lot models.PurchasedCreditLot, eventType string, change int64, recordedAt time.Time) error`.
- Produces order terminal state `refund_required` when a paid trial loses the claim race.

- [ ] **Step 1: Add failing settlement and lifecycle tests**

```go
type verifiedCreditPayment struct {
    Provider string
    OutTradeNo string
    TransactionID string
    MerchantID string
    AmountCNY decimal.Decimal
    PaidAt time.Time
    Payload []byte
}

func TestSettlementCreatesExactlyOneLotAndLedger(t *testing.T) {
    // Create a CNY 1.90 order, settle the same verified payment twice, then count by order ID.
    // Assert both calls return the same lot ID and counts are lot=1, recharge=1, recharge-ledger=1.
}
func TestSettlementRejectsProviderAndAmountMismatch(t *testing.T) {
    // Settle once with provider=wechat against an Alipay order and once with amount=1.91.
    // Assert both return their typed mismatch errors; order remains pending and lot count is zero.
}
func TestConcurrentTrialSettlementGrantsOnlyOneLot(t *testing.T) {
    // Create one WeChat and one Alipay trial order for the same user and settle through two goroutines.
    // Assert claim count=1, active lot count=1, paid order count=1, refund_required order count=1.
}
func TestExpiryClearsOnlyDueLotAndWritesLedgerOnce(t *testing.T) {
    // Seed a due lot with 300 remaining and a future lot with 500, then expire twice at the same time.
    // Assert due lot is expired/zero, future lot remains active/500, and expiry ledger count/change are 1/-300.
}
func TestRefundRevokesOnlyRemainderAndKeepsTrialClaim(t *testing.T) {
    // Seed a claimed 500-Credit lot with 200 remaining and refund twice.
    // Assert first revoked=200, second revoked=0, balance never becomes negative, claim count=1, refund ledger count=1.
}
```

Use `sync.WaitGroup` and a PostgreSQL-backed test database for the concurrency case; SQLite does not reproduce row/unique-index behavior reliably.

- [ ] **Step 2: Run focused tests and confirm failures**

Run:
```powershell
wsl.exe -d Ubuntu -- bash -lc "cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-alipay-subscription && GIN_MODE=release CGO_ENABLED=1 go test ./internal/server -run 'Test(Settlement|ConcurrentTrial|Expiry|Refund)' -count=1"
```
Expected: FAIL because settlement lacks provider identity input, trial loser handling, expiry ledger idempotency, and refund ledger accounting.

- [ ] **Step 3: Make shared settlement the only grant path**

```go
func settleCreditPaymentOrder(db *gorm.DB, paid verifiedCreditPayment) (*models.PurchasedCreditLot, error) {
    var lot models.PurchasedCreditLot
    err := db.Transaction(func(tx *gorm.DB) error {
        var order models.CreditPaymentOrder
        if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("out_trade_no = ?", paid.OutTradeNo).First(&order).Error; err != nil { return err }
        if order.Status == "paid" || order.Status == "refunded" { return tx.Where("payment_order_id = ?", order.ID).First(&lot).Error }
        if order.Provider != paid.Provider { return ErrPaymentProviderMismatch }
        if !order.AmountCNY.Equal(paid.AmountCNY) { return ErrPaymentAmountMismatch }
        if order.IsTrial {
            claim := models.CreditTrialClaim{UserID: order.UserID, PaymentOrderID: order.ID}
            if err := tx.Create(&claim).Error; err != nil {
                return tx.Model(&order).Updates(map[string]any{"status": "refund_required", "provider_transaction_id": paid.TransactionID, "paid_at": paid.PaidAt, "provider_payload": datatypes.JSON(paid.Payload)}).Error
            }
        }
        expiresAt := paid.PaidAt.UTC().Add(720 * time.Hour)
        lot = models.PurchasedCreditLot{UserID: order.UserID, PaymentOrderID: order.ID, GrantedCredits: order.Credits, RemainingCredits: order.Credits, Status: "active", ExpiresAt: expiresAt}
        if err := tx.Create(&lot).Error; err != nil { return err }
        // Update order, then create exactly one recharge record and one ledger record keyed by order ID.
        return persistPaidOrderAndAccounting(tx, &order, &lot, paid)
    })
    return &lot, err
}
```

Add unique event identity using `payment_order_id`/`lot_id` metadata plus database uniqueness where supported. `BalanceAfter` must be the full balance snapshot after the mutation, not the granted lot amount.

- [ ] **Step 4: Make expiry and refund idempotent accounting mutations**

```go
func expirePurchasedCreditLots(tx *gorm.DB, userID uuid.UUID, now time.Time) error {
    var lots []models.PurchasedCreditLot
    if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND status = 'active' AND expires_at <= ?", userID, now).Find(&lots).Error; err != nil { return err }
    for i := range lots {
        discarded := lots[i].RemainingCredits
        lots[i].RemainingCredits, lots[i].Status = 0, "expired"
        if err := tx.Save(&lots[i]).Error; err != nil { return err }
        if discarded > 0 {
            if err := createLotLedgerOnce(tx, lots[i], creditLedgerEventExpiry, -discarded, now); err != nil { return err }
        }
    }
    return nil
}
```

Refund provider first, then transactionally lock order and lot, set both to `refunded`, zero only `RemainingCredits`, create one negative refund ledger record, and never delete `CreditTrialClaim`.

- [ ] **Step 5: Run focused tests and commit**

Run the Step 2 command. Expected: PASS with exactly one grant/expiry/refund ledger mutation per object.

```bash
git add internal/server/credit_payments.go internal/server/credit_payments_test.go internal/server/credit_lots.go internal/server/credit_lots_test.go internal/server/credit_refunds.go internal/server/credit_refunds_test.go
git commit -m "feat: enforce canonical credit lot settlement"
```

### Task 3: Purchased-First Billing And Reservation Consistency

**Files:**
- Modify: `internal/server/billing.go`
- Modify: `internal/server/server_test.go`
- Modify: `internal/server/workflow_run_billing_test.go`

**Interfaces:**
- Consumes: `consumePurchasedCreditLots(tx, userID, requested, now) (int64, error)`.
- Produces: one shared `consumeCreditSourcesTx` used by immediate billing and reservation finalization.
- Consumption order: purchased lot, subscription period grant, legacy token account.

- [ ] **Step 1: Add failing ordering tests for direct and reserved billing**

```go
func TestConsumeCreditsUsesPurchasedThenSubscriptionThenLegacy(t *testing.T) {
    // Seed: lot=30, subscription=40, legacy=50; consume 80.
    // Assert: lot=0, subscription=0, legacy remaining=40.
}

func TestFinalizeReservationUsesSamePurchasedFirstOrder(t *testing.T) {
    // Reserve and finalize 35 with lot=30 and subscription=20.
    // Assert: lot=0, subscription remaining=15, legacy unchanged.
}

func TestExpiredLotIsUnavailableBeforeCleanupJob(t *testing.T) {
    // Seed an active row whose expires_at is in the past; assert insufficient balance.
}
```

- [ ] **Step 2: Run the focused tests**

Run:
```powershell
wsl.exe -d Ubuntu -- bash -lc "cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-alipay-subscription && GIN_MODE=release CGO_ENABLED=1 go test ./internal/server -run 'Test(ConsumeCreditsUsesPurchased|FinalizeReservationUsesSame|ExpiredLotIsUnavailable)' -count=1"
```
Expected: FAIL because current code consumes subscription Credits before purchased lots and reservation paths are not unified.

- [ ] **Step 3: Centralize source consumption**

```go
type creditSourceConsumption struct { Purchased, Subscription, Legacy int64 }

func consumeCreditSourcesTx(tx *gorm.DB, userID uuid.UUID, requested int64, now time.Time) (creditSourceConsumption, error) {
    var used creditSourceConsumption
    purchased, err := consumePurchasedCreditLots(tx, userID, requested, now)
    if err != nil { return used, err }
    used.Purchased = purchased
    remaining := requested - purchased
    used.Subscription, err = consumeSubscriptionCreditsTx(tx, userID, remaining, now)
    if err != nil { return used, err }
    remaining -= used.Subscription
    used.Legacy, err = consumeLegacyCreditsTx(tx, userID, remaining)
    return used, err
}
```

Use this function from both immediate `consumeCredits` and reservation finalization. Availability checks must sum only unexpired lots, active historical subscription grants, and legacy remaining balance, while reservations continue to reduce the displayed available amount exactly once.

- [ ] **Step 4: Run billing regression tests**

Run:
```powershell
wsl.exe -d Ubuntu -- bash -lc "cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-alipay-subscription && GIN_MODE=release CGO_ENABLED=1 go test ./internal/server -run 'Test(CreditReservation|ConsumeCredits|FinalizeProvider|WorkflowRunBilling|ExpiredLot)' -count=1"
```
Expected: PASS; legacy workflow idempotency and reservation release tests remain green.

- [ ] **Step 5: Commit**

```bash
git add internal/server/billing.go internal/server/server_test.go internal/server/workflow_run_billing_test.go
git commit -m "fix: consume expiring credit lots first"
```

### Task 4: WeChat Native Pay Adapter And Public Route Cutover

**Files:**
- Modify: `internal/providers/wechat.go`
- Modify: `internal/providers/wechat_test.go`
- Modify: `internal/server/handlers_credit_payments.go`
- Create: `internal/server/handlers_credit_payments_test.go`
- Modify: `internal/server/handlers_business.go`
- Modify: `internal/server/server.go`

**Interfaces:**
- `POST /api/v1/credit-payments` body `{ "package_id": "uuid", "provider": "wechat" }`.
- `GET /api/v1/credit-payments/:out_trade_no` returns authenticated user's order.
- `POST /api/v1/credit-payments/wechat/webhook` is provider-authenticated, not user-authenticated.
- Provider creates direct CNY amount in fen: `amountFen = order.AmountCNY.Mul(decimal.NewFromInt(100)).IntPart()`.

- [ ] **Step 1: Add failing handler/provider tests**

```go
func TestCreateWeChatCreditPaymentUsesStoredCNYAmount(t *testing.T) {
    // Seed price=19.90, create via provider=wechat, and assert the fake provider request amount is exactly 1990 fen.
}
func TestCreateCreditPaymentIgnoresClientAmountAndCredits(t *testing.T) {
    // POST amount=0.01/credits=999999 against a 19.90/1000 package and assert the order snapshot remains 19.90/1000.
}
func TestWeChatWebhookRejectsWrongMerchantAmountAndSignature(t *testing.T) {
    // Submit validly encrypted fixtures with wrong MchID and 1989 fen plus an invalid-signature fixture.
    // Assert each response is non-success and no order becomes paid and no lot is created.
}
func TestWeChatDuplicateWebhookCreatesOneLot(t *testing.T) {
    // Submit the same valid signed 1990-fen SUCCESS notification twice and assert paid order=1, lot=1, ledger=1.
}
func TestLegacyRechargeEndpointIsDisabled(t *testing.T) {
    // POST the former customer recharge route and assert HTTP 409 with code payment_channel_disabled.
}
```

- [ ] **Step 2: Run tests and verify failures**

Run:
```powershell
wsl.exe -d Ubuntu -- bash -lc "cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-alipay-subscription && GIN_MODE=release CGO_ENABLED=1 go test ./internal/providers ./internal/server -run 'Test(CreateWeChatCredit|CreateCreditPayment|WeChatWebhook|LegacyRecharge)' -count=1"
```
Expected: FAIL on strict merchant/amount verification or canonical settlement signature.

- [ ] **Step 3: Map verified WeChat transactions to shared settlement**

```go
paid := verifiedCreditPayment{
    Provider: "wechat",
    OutTradeNo: tx.OutTradeNo,
    TransactionID: tx.TransactionID,
    MerchantID: tx.MchID,
    AmountCNY: decimal.NewFromInt(tx.PayerTotalFen).Div(decimal.NewFromInt(100)),
    PaidAt: tx.SuccessTime.UTC(),
    Payload: rawNotification,
}
_, err := settleCreditPaymentOrder(a.db, paid)
```

Before settlement, require configured `MchID`, `AppID`, currency `CNY`, successful trade state, and exact amount. Return WeChat's required success response for duplicate valid callbacks. Keep old data models and callback code, but make old customer recharge creation return `409 payment_channel_disabled`.

- [ ] **Step 4: Run tests and commit**

Run Step 2 command. Expected: PASS and no call to `usdToWeChatCNYAmount` from the new route.

```bash
git add internal/providers/wechat.go internal/providers/wechat_test.go internal/server/handlers_credit_payments.go internal/server/handlers_credit_payments_test.go internal/server/handlers_business.go internal/server/server.go
git commit -m "feat: route wechat purchases through credit orders"
```

### Task 5: Alipay Face-To-Face QR Adapter

**Files:**
- Modify: `internal/providers/alipay.go`
- Modify: `internal/providers/alipay_test.go`
- Modify: `internal/server/handlers_alipay_credit_payments.go`
- Create: `internal/server/handlers_alipay_credit_payments_test.go`

**Interfaces:**
- Uses `alipay.trade.precreate`, `alipay.trade.query`, `alipay.trade.close`, `alipay.trade.refund` with RSA2.
- Async webhook requires verified `app_id`, `seller_id`, `trade_status`, `out_trade_no`, and `total_amount`.
- Produces the same `verifiedCreditPayment` consumed by Task 2.

- [ ] **Step 1: Add failing adapter and handler tests**

```go
func TestAlipayPrecreateUsesFixedMethodAndCNYAmount(t *testing.T) {
    // Capture gateway form and assert method=alipay.trade.precreate and biz_content total_amount="1.90".
}
func TestAlipayNotificationVerificationExcludesSignFieldsAndUsesRSA2(t *testing.T) {
    // Sign sorted form fields with the test RSA private key, include sign/sign_type, and verify with its public key.
    // Mutate total_amount after signing and assert verification fails.
}
func TestAlipayWebhookRejectsWrongAppSellerAmountOrStatus(t *testing.T) {
    // Submit four correctly signed forms changing APPID, seller_id, amount, and trade_status one at a time.
    // Assert every response is failure, the order stays pending, and lot count remains zero.
}
func TestAlipayQuerySettlesPaidOrderIdempotently(t *testing.T) {
    // Return TRADE_SUCCESS twice for one merchant order and assert one paid order, one lot, and one ledger entry.
}
func TestAlipayRefundUsesOriginalOrderAndAmount(t *testing.T) {
    // Refund order CR123 at 1.90 and assert method=alipay.trade.refund, out_trade_no=CR123, refund_amount=1.90.
}
```

- [ ] **Step 2: Run tests and confirm failures**

Run:
```powershell
wsl.exe -d Ubuntu -- bash -lc "cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-alipay-subscription && GIN_MODE=release CGO_ENABLED=1 go test ./internal/providers ./internal/server -run 'TestAlipay(Precreate|Notification|Webhook|Query|Refund)' -count=1"
```
Expected: FAIL until one-time trade methods and strict notification identity checks are complete.

- [ ] **Step 3: Implement explicit one-time Alipay methods**

```go
type AlipayPrecreateRequest struct { OutTradeNo, Subject, TotalAmount string }
type AlipayPrecreateResponse struct { OutTradeNo, QRCode string }

func (c *AlipayClient) PrecreateCreditPayment(ctx context.Context, req AlipayPrecreateRequest) (AlipayPrecreateResponse, error) {
    biz := map[string]string{"out_trade_no": req.OutTradeNo, "subject": req.Subject, "total_amount": req.TotalAmount}
    var response alipayPrecreateEnvelope
    err := c.call(ctx, "alipay.trade.precreate", biz, &response)
    return AlipayPrecreateResponse{OutTradeNo: response.OutTradeNo, QRCode: response.QRCode}, err
}
```

Build the notification signing string from sorted form keys excluding `sign` and `sign_type`, verify with the configured Alipay public key, then validate configured APPID and enterprise PID/seller ID before settlement. Never log private keys or full sensitive callback values.

- [ ] **Step 4: Map status query and callback to canonical settlement**

```go
paid := verifiedCreditPayment{
    Provider: "alipay", OutTradeNo: values.Get("out_trade_no"),
    TransactionID: values.Get("trade_no"), MerchantID: values.Get("seller_id"),
    AmountCNY: decimal.RequireFromString(values.Get("total_amount")),
    PaidAt: parsedGMTDate, Payload: []byte(values.Encode()),
}
_, err := settleCreditPaymentOrder(a.db, paid)
```

Only `TRADE_SUCCESS` and `TRADE_FINISHED` settle. Return literal `success` only after verification and idempotent settlement succeeds; otherwise return `failure` with an error HTTP status.

- [ ] **Step 5: Run tests and commit**

Run Step 2 command. Expected: PASS.

```bash
git add internal/providers/alipay.go internal/providers/alipay_test.go internal/server/handlers_alipay_credit_payments.go internal/server/handlers_alipay_credit_payments_test.go
git commit -m "feat: add alipay qr credit purchases"
```

### Task 6: Customer/Admin API Contract And Subscription Sales Disablement

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/handlers_compat_system_subscriptions.go`
- Modify: `internal/server/handlers_credit_payments.go`
- Modify: `internal/server/server_test.go`
- Modify: `internal/server/openapi.go`

**Interfaces:**
- `GET /api/v1/token-packages/packages`: `price`, `currency`, `validity_hours`, `is_trial`, `per_account_purchase_limit`, `trial_claimed`.
- `GET /api/v1/credit-payments/:out_trade_no`: `status`, `provider`, `code_url`, `credits`, `amount_cny`, `paid_at`, `expires_at`.
- `GET /api/v1/token-accounts/balance`: purchased/subscription/legacy breakdown, `next_expiry_at`, and `trial_claimed`.
- `POST /api/v1/credit-payments/:out_trade_no/refund`: admin only.

- [ ] **Step 1: Add failing API policy tests**

```go
func TestPackageListReturnsCNYTrialEligibilityForAuthenticatedUser(t *testing.T) {
    // Claim the trial, GET packages, and assert it remains present with currency=CNY and trial_claimed=true.
}
func TestCreditPaymentStatusCannotReadAnotherUsersOrder(t *testing.T) {
    // Authenticate user B and request user A's out_trade_no; assert HTTP 404 without order data.
}
func TestOnlyOneActiveTrialPackageCanBeSaved(t *testing.T) {
    // Seed one active trial, submit a second through admin create, and assert HTTP 409 and active trial count=1.
}
func TestSubscriptionCheckoutEndpointsRejectNewSales(t *testing.T) {
    // POST both Creem and Alipay subscription checkout routes and assert 409 subscription_sales_disabled.
}
func TestBalanceExposesNearestPurchasedExpiry(t *testing.T) {
    // Seed active lots expiring at +24h and +48h; assert next_expiry_at equals the +24h timestamp.
}
```

- [ ] **Step 2: Run API tests**

Run:
```powershell
wsl.exe -d Ubuntu -- bash -lc "cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-alipay-subscription && GIN_MODE=release CGO_ENABLED=1 go test ./internal/server -run 'Test(PackageListReturnsCNY|CreditPaymentStatus|OnlyOneActiveTrial|SubscriptionCheckout|BalanceExposes)' -count=1"
```
Expected: FAIL on at least trial eligibility scoping, ownership, or disabled checkout policy.

- [ ] **Step 3: Complete response and policy enforcement**

```go
func creditPaymentJSON(order models.CreditPaymentOrder) gin.H {
    return gin.H{"out_trade_no": order.OutTradeNo, "status": order.Status, "provider": order.Provider, "code_url": order.CodeURL, "credits": order.Credits, "amount_cny": order.AmountCNY.StringFixed(2), "paid_at": order.PaidAt, "expires_at": order.ExpiresAt}
}
```

Scope status query by both `out_trade_no` and authenticated `user_id`. Enforce one active trial in admin create/update transactions. Preserve historical callback routes, but return `409` from all user-facing subscription checkout creation routes while `CreditPaymentProvider` is active.

- [ ] **Step 4: Run tests and commit**

Run Step 2 command, then:
```powershell
wsl.exe -d Ubuntu -- bash -lc "cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-alipay-subscription && GIN_MODE=release CGO_ENABLED=1 go test ./internal/server -count=1"
```
Expected: PASS.

```bash
git add internal/server/server.go internal/server/handlers_compat_system_subscriptions.go internal/server/handlers_credit_payments.go internal/server/server_test.go internal/server/openapi.go
git commit -m "feat: expose one-time credit payment api"
```

### Task 7: CNY Credits Purchase UI And Admin Form

**Files:**
- Modify: `src/features/subscriptions/types/payment.types.ts`
- Modify: `src/features/subscriptions/types/token.types.ts`
- Modify: `src/features/subscriptions/services/paymentService.ts`
- Modify: `src/features/subscriptions/components/CreditPaymentModal.tsx`
- Modify: `src/features/subscriptions/components/CreditPaymentModal.test.tsx`
- Modify: `src/pages/SubscriptionPage.tsx`
- Modify: `src/pages/SubscriptionPage.test.tsx`
- Modify: `src/pages/admin/subscriptions/CreditPackagesPage.tsx`
- Modify: `src/features/admin/components/PackageFormModal.tsx`
- Create: `src/features/admin/components/PackageFormModal.test.tsx`

**Interfaces:**
- Consumes Task 6 API contract.
- `createCreditPayment(packageId: string, provider: 'wechat' | 'alipay'): Promise<CreditPaymentOrder>`.
- Poll interval: 3000 ms; successful status is `paid`; terminal non-success statuses stop polling.

- [ ] **Step 1: Add failing customer UI tests**

```tsx
it('renders CNY packages without subscription or USD controls', async () => {
  render(<SubscriptionPage />)
  expect(await screen.findByText('¥1.90')).toBeInTheDocument()
  expect(screen.queryByText(/monthly|yearly|自动续费|\$/i)).not.toBeInTheDocument()
})

it('creates only the selected Alipay order and polls every three seconds', async () => {
  vi.useFakeTimers()
  render(<CreditPaymentModal opened package={trialPackage} onClose={vi.fn()} onPaid={vi.fn()} />)
  await userEvent.click(screen.getByRole('radio', { name: '支付宝' }))
  await userEvent.click(screen.getByRole('button', { name: '生成支付二维码' }))
  expect(createCreditPayment).toHaveBeenCalledWith(trialPackage.id, 'alipay')
  expect(createCreditPayment).not.toHaveBeenCalledWith(trialPackage.id, 'wechat')
  await vi.advanceTimersByTimeAsync(3000)
  expect(getCreditPayment).toHaveBeenCalledTimes(1)
})

it('keeps claimed trial visible and disables repurchase', async () => {
  render(<SubscriptionPage />)
  expect(await screen.findByText('体验包')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: '已购买' })).toBeDisabled()
})
```

- [ ] **Step 2: Run frontend tests and confirm failures**

Run:
```powershell
npm test -- --run src/pages/SubscriptionPage.test.tsx src/features/subscriptions/components/CreditPaymentModal.test.tsx
```
from `D:\NarratoAI\.worktrees\NarratoAI-SaaS-F-alipay-subscription`.
Expected: FAIL until all selected-channel, polling, disabled-trial, and CNY-only assertions pass.

- [ ] **Step 3: Complete typed service and modal state machine**

```ts
export type CreditPaymentProvider = 'wechat' | 'alipay'
export interface CreditPaymentOrder {
  out_trade_no: string
  status: 'pending' | 'paid' | 'closed' | 'failed' | 'refund_required' | 'refunded'
  provider: CreditPaymentProvider
  code_url: string
  credits: number
  amount_cny: string
  paid_at?: string
  expires_at?: string
}

export async function createCreditPayment(packageId: string, provider: CreditPaymentProvider) {
  return api.post<CreditPaymentOrder>('/credit-payments', { package_id: packageId, provider }).then(r => r.data)
}
```

Do not create an order on modal open or channel hover. Create after explicit channel selection and button click, render QR for only that order, poll every three seconds, include manual `我已完成支付`, stop timers on close/unmount/terminal state, and refresh package plus balance queries after `paid`.

- [ ] **Step 4: Complete package page and admin form**

Render `¥${price.toFixed(2)}`, Credits, and `购买后 30 天有效`. Show balance and nearest expiry without subscription management controls. Trial sorts first and uses disabled `已购买` when `trial_claimed` is true.

Admin form fields are CNY price, Credits, bonus Credits, active/featured/trial toggles, read-only `720` validity, and trial purchase limit `1`. Remove Creem currency/ID and subscription period fields from visible form controls without deleting their TypeScript/backend compatibility fields.

- [ ] **Step 5: Add admin and responsive assertions**

```tsx
it('uses CNY and fixed 720-hour validity without Creem fields', () => {
  render(<PackageFormModal opened package={null} onClose={vi.fn()} onSaved={vi.fn()} />)
  expect(screen.getByLabelText('价格（人民币）')).toBeInTheDocument()
  expect(screen.getByDisplayValue('720')).toBeDisabled()
  expect(screen.queryByText(/Creem|USD/)).not.toBeInTheDocument()
})
```

Set QR container to `width: min(280px, 75vw); aspect-ratio: 1` and modal controls to wrap below 360px. Test at a narrow viewport that QR and actions remain present and do not overflow.

- [ ] **Step 6: Run frontend verification and commit**

Run:
```powershell
npm test -- --run src/pages/SubscriptionPage.test.tsx src/features/subscriptions/components/CreditPaymentModal.test.tsx src/features/admin/components/PackageFormModal.test.tsx
npm run lint
npm run build
```
Expected: all tests PASS, lint exits 0, production build exits 0.

```bash
git add src/features/subscriptions src/features/admin/components/PackageFormModal.tsx src/features/admin/components/PackageFormModal.test.tsx src/pages/SubscriptionPage.tsx src/pages/SubscriptionPage.test.tsx src/pages/admin/subscriptions/CreditPackagesPage.tsx
git commit -m "feat: sell cny credit packages by qr payment"
```

### Task 8: WSL Integration, Payment Runbook, And Release Gate

**Files:**
- Create: `docs/operations/one-time-credit-payments.md`
- Modify: `.env.example`
- Modify: `README.md`

**Interfaces:**
- Requires existing ignored Alipay and WeChat credential paths; no secrets enter Git.
- Local frontend: `http://127.0.0.1:8080/`; backend health: `http://127.0.0.1:8000/health`.
- Production deployment remains a separate, explicitly authorized operation.

- [ ] **Step 1: Document non-secret configuration and callback URLs**

```dotenv
CREDIT_PAYMENT_PROVIDER=one_time
ALIPAY_APP_ID=
ALIPAY_SELLER_ID=
ALIPAY_PRIVATE_KEY_PATH=data/alipay_certs/app_private_key.pem
ALIPAY_PUBLIC_KEY_PATH=data/alipay_certs/alipay_public_key.pem
ALIPAY_CREDIT_NOTIFY_URL=https://api.example.com/api/v1/credit-payments/alipay/webhook
WECHAT_CREDIT_NOTIFY_URL=https://api.example.com/api/v1/credit-payments/wechat/webhook
```

The runbook must state that key files and merchant secrets stay ignored, describe trial/order/lot/ledger queries, refund procedure, callback troubleshooting, and rollback by disabling new order creation while preserving callbacks.

- [ ] **Step 2: Run complete backend and frontend suites**

Run:
```powershell
wsl.exe -d Ubuntu -- bash -lc "cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-alipay-subscription && GIN_MODE=release CGO_ENABLED=1 go test ./... -count=1"
```
Expected: PASS.

Run from the frontend worktree:
```powershell
npm test -- --run
npm run lint
npm run build
```
Expected: all tests PASS, lint exits 0, build exits 0.

- [ ] **Step 3: Start WSL services with Windows-side keepalive**

```powershell
Start-Process -WindowStyle Hidden -FilePath "$env:WINDIR\System32\wsl.exe" -ArgumentList '-d','Ubuntu','--exec','sleep','infinity'
wsl.exe -d Ubuntu -- bash -lc "cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-alipay-subscription && docker compose up -d && cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-F-alipay-subscription && docker compose up -d"
Start-Sleep -Seconds 90
Invoke-WebRequest -UseBasicParsing http://127.0.0.1:8000/health
Invoke-WebRequest -UseBasicParsing http://127.0.0.1:8080/
```
Expected: both responses are HTTP 200.

- [ ] **Step 4: Execute the local acceptance matrix**

Verify with sandbox or CNY 0.01 temporary internal packages before restoring configured prices:

```text
WeChat: create -> QR -> paid callback -> duplicate callback -> one lot
Alipay: precreate -> QR -> paid callback/query -> duplicate query -> one lot
Negative: invalid signature, wrong merchant, wrong amount, failed status -> zero lots
Trial: concurrent cross-provider paid events -> one lot, one refund_required order
Expiry: paid_at + 720h boundary -> only due remainder expires, one ledger event
Refund: partial consumption -> only remainder revoked, trial remains claimed
UI: CNY only, trial disabled after claim, QR usable at 360px width
```

Record order numbers, provider transaction IDs, observed timestamps, and database row counts in a local ignored test log; do not commit customer/payment secrets.

- [ ] **Step 5: Commit operational documentation**

```bash
git add .env.example README.md docs/operations/one-time-credit-payments.md
git commit -m "docs: add one-time payment rollout runbook"
```

- [ ] **Step 6: Stop at the production release gate**

Expected state: WSL health checks, full suites, and the acceptance matrix pass. Report results and request separate production deployment authorization; do not tag, push a release image, SSH, or change production in this task.
