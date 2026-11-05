# One-Time Credit Payments Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace new recurring subscriptions with CNY-denominated, 30-day credit purchases through WeChat Pay or Alipay QR codes while preserving historical subscription entitlements and payment code.

**Architecture:** A provider-neutral payment order owns immutable package snapshots and is settled exactly once into an expiring purchased-credit lot. WeChat and Alipay are adapters around the same order, settlement, refund, balance, and consumption services; historical Creem and subscription records remain readable and their webhooks remain active, but customer-facing subscription checkout is disabled.

**Tech Stack:** Go 1.24, Echo, GORM/PostgreSQL, shopspring/decimal, existing WeChat Pay SDK, Alipay OpenAPI RSA2 gateway, React 18, TypeScript, Mantine, Vitest/Testing Library, Docker Compose in Ubuntu WSL.

## Global Constraints

- Every successful purchase creates an independent credit lot valid for exactly `720` hours from provider-confirmed payment time.
- Purchased lots are consumed by earliest `expires_at`, then earliest `created_at`; expired or refunded credits can never be consumed.
- The trial package is exactly `CNY 1.90` for `500` Credits and can succeed once per account lifetime across both providers; a refund never restores eligibility.
- Existing package prices are converted exactly once as `old USD price * 7.2`, rounded to two decimals, and stored as CNY. Runtime USD conversion is forbidden after migration.
- New checkout supports only `wechat` and `alipay`; no new Creem or recurring subscription checkout is exposed.
- Existing Creem code, subscription records, active subscription grants, and subscription webhooks are retained for historical entitlement completion.
- A refund revokes only the unconsumed remainder of the purchased lot created by that order; it never creates a negative balance and never touches another lot.
- Package administration exposes CNY price, fixed 720-hour validity, trial status, and per-account purchase limit. Legacy Creem/USD columns remain stored but are hidden from active forms.
- Provider callbacks and query synchronization must share the same idempotent settlement transaction.
- Alipay requires the ordinary face-to-face payment product and `alipay.trade.precreate`; keys remain environment secrets and are never committed.
- Verification happens in WSL locally before any production release.

---

### Task 1: Provider-Neutral Payment and Credit-Lot Schema

**Files:**
- Modify: `internal/models/models.go`
- Modify: `internal/database/database.go`
- Create: `internal/database/credit_payment_schema_test.go`

**Interfaces:**
- Consumes: existing `models.TokenPackage`, `models.User`, PostgreSQL UUID conventions, and `database.AutoMigrate(*gorm.DB) error`.
- Produces: `models.CreditPaymentOrder`, `models.PurchasedCreditLot`, `models.CreditTrialClaim`, and the added `TokenPackage.Currency`, `ValidityHours`, `IsTrial`, and `PerAccountPurchaseLimit` fields.

- [ ] **Step 1: Write the failing schema test**

```go
func TestAutoMigrateCreatesCreditPaymentConstraints(t *testing.T) {
    db := openMigrationTestDB(t)
    require.NoError(t, AutoMigrate(db))
    assertIndexExists(t, db, "credit_payment_orders", "uq_credit_payment_orders_out_trade_no")
    assertIndexExists(t, db, "purchased_credit_lots", "uq_purchased_credit_lots_order_id")
    assertIndexExists(t, db, "credit_trial_claims", "uq_credit_trial_claims_user_id")
}
```

- [ ] **Step 2: Run the test and verify the missing-table failure**

Run: `go test ./internal/database -run TestAutoMigrateCreatesCreditPaymentConstraints -count=1`

Expected: FAIL because the three models and indexes do not exist.

- [ ] **Step 3: Add the exact model contracts**

```go
type CreditPaymentOrder struct {
    ID                    uuid.UUID       `gorm:"type:uuid;primaryKey" json:"id"`
    UserID                uuid.UUID       `gorm:"type:uuid;not null;index" json:"user_id"`
    PackageID             uuid.UUID       `gorm:"type:uuid;not null;index" json:"package_id"`
    Provider              string          `gorm:"size:20;not null;index" json:"provider"`
    OutTradeNo            string          `gorm:"size:64;not null;uniqueIndex:uq_credit_payment_orders_out_trade_no" json:"out_trade_no"`
    ProviderTransactionID string          `gorm:"size:128;index" json:"provider_transaction_id"`
    Status                string          `gorm:"size:20;not null;default:pending;index" json:"status"`
    AmountCNY             decimal.Decimal `gorm:"type:decimal(10,2);not null" json:"amount_cny"`
    Credits               int64           `gorm:"not null" json:"credits"`
    PackageName           string          `gorm:"size:100;not null" json:"package_name"`
    IsTrial               bool            `gorm:"not null;default:false" json:"is_trial"`
    CodeURL               string          `gorm:"type:text" json:"code_url"`
    PaidAt                *time.Time       `json:"paid_at"`
    ExpiresAt             *time.Time       `json:"expires_at"`
    RefundedAt            *time.Time       `json:"refunded_at"`
    ProviderPayload       datatypes.JSON   `json:"provider_payload"`
    CreatedAt             time.Time        `json:"created_at"`
    UpdatedAt             time.Time        `json:"updated_at"`
}

type PurchasedCreditLot struct {
    ID               uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
    UserID           uuid.UUID `gorm:"type:uuid;not null;index:idx_credit_lots_spend,priority:1" json:"user_id"`
    PaymentOrderID   uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:uq_purchased_credit_lots_order_id" json:"payment_order_id"`
    GrantedCredits   int64     `gorm:"not null" json:"granted_credits"`
    RemainingCredits int64     `gorm:"not null" json:"remaining_credits"`
    Status           string    `gorm:"size:20;not null;index:idx_credit_lots_spend,priority:2" json:"status"`
    ExpiresAt        time.Time `gorm:"not null;index:idx_credit_lots_spend,priority:3" json:"expires_at"`
    RefundedAt       *time.Time `json:"refunded_at"`
    CreatedAt        time.Time `json:"created_at"`
    UpdatedAt        time.Time `json:"updated_at"`
}

type CreditTrialClaim struct {
    ID             uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
    UserID         uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:uq_credit_trial_claims_user_id" json:"user_id"`
    PaymentOrderID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"payment_order_id"`
    CreatedAt      time.Time `json:"created_at"`
}
```

Add to `TokenPackage`:

```go
Currency                string `gorm:"size:3;not null;default:CNY" json:"currency"`
ValidityHours           int    `gorm:"not null;default:720" json:"validity_hours"`
IsTrial                 bool   `gorm:"not null;default:false;index" json:"is_trial"`
PerAccountPurchaseLimit int    `gorm:"not null;default:0" json:"per_account_purchase_limit"`
```

Register all three models in `AutoMigrate` and add explicit unique-index checks following the existing `requiredIndexes` convention.

- [ ] **Step 4: Run schema tests**

Run: `go test ./internal/database -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the schema**

```bash
git add internal/models/models.go internal/database/database.go internal/database/credit_payment_schema_test.go
git commit -m "feat: add one-time credit payment schema"
```

---

### Task 2: Idempotent CNY Package Migration and Trial Seed

**Files:**
- Create: `internal/database/credit_package_migration.go`
- Create: `internal/database/credit_package_migration_test.go`
- Modify: `internal/database/database.go`

**Interfaces:**
- Consumes: `models.TokenPackage`, `models.SystemSetting`, `decimal.NewFromFloat(7.2)`, and a transaction created by GORM.
- Produces: `migrateCreditPackagesToCNY(tx *gorm.DB) error`, marker key `credit_packages_cny_v1`, and trial package stable name `trial_500`.

- [ ] **Step 1: Write failing migration tests**

```go
func TestMigrateCreditPackagesToCNYRunsExactlyOnce(t *testing.T) {
    db := openMigrationTestDB(t)
    seedPackage(t, db, "starter", "10.00")
    require.NoError(t, migrateCreditPackagesToCNY(db))
    require.NoError(t, migrateCreditPackagesToCNY(db))

    pkg := loadPackageByName(t, db, "starter")
    assert.Equal(t, "72.00", pkg.Price.StringFixed(2))
    assert.Equal(t, "CNY", pkg.Currency)
    assert.Equal(t, 720, pkg.ValidityHours)
}

func TestMigrateCreditPackagesToCNYSeedsTrialPackage(t *testing.T) {
    db := openMigrationTestDB(t)
    require.NoError(t, migrateCreditPackagesToCNY(db))
    pkg := loadPackageByName(t, db, "trial_500")
    assert.Equal(t, int64(500), pkg.TokenAmount+pkg.BonusTokens)
    assert.Equal(t, "1.90", pkg.Price.StringFixed(2))
    assert.True(t, pkg.IsTrial)
    assert.Equal(t, 1, pkg.PerAccountPurchaseLimit)
}
```

- [ ] **Step 2: Run the focused tests**

Run: `go test ./internal/database -run 'TestMigrateCreditPackagesToCNY' -count=1`

Expected: FAIL because `migrateCreditPackagesToCNY` is undefined.

- [ ] **Step 3: Implement the transaction and durable marker**

```go
const creditPackageCNYMigrationKey = "credit_packages_cny_v1"

func migrateCreditPackagesToCNY(db *gorm.DB) error {
    return db.Transaction(func(tx *gorm.DB) error {
        var marker models.SystemSetting
        if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("key = ?", creditPackageCNYMigrationKey).First(&marker).Error; err == nil {
            return nil
        } else if !errors.Is(err, gorm.ErrRecordNotFound) {
            return err
        }

        var packages []models.TokenPackage
        if err := tx.Find(&packages).Error; err != nil { return err }
        rate := decimal.NewFromFloat(7.2)
        for i := range packages {
            packages[i].Price = packages[i].Price.Mul(rate).Round(2)
            packages[i].Currency = "CNY"
            packages[i].ValidityHours = 720
            if err := tx.Save(&packages[i]).Error; err != nil { return err }
        }

        trial := models.TokenPackage{Name: "trial_500", DisplayName: "体验包", TokenAmount: 500, Price: decimal.RequireFromString("1.90"), Currency: "CNY", ValidityHours: 720, IsTrial: true, PerAccountPurchaseLimit: 1, IsActive: true}
        if err := tx.Where("name = ?", trial.Name).Assign(trial).FirstOrCreate(&trial).Error; err != nil { return err }
        description := "One-time USD to CNY credit package migration"
        return tx.Create(&models.SystemSetting{
            Base: models.Base{ID: uuid.New()},
            Key: creditPackageCNYMigrationKey,
            Value: datatypes.JSON([]byte(`"completed"`)),
            Description: &description,
        }).Error
    })
}
```

Call it after `AutoMigrate` has created the new columns. Preserve all legacy Creem columns unchanged.

- [ ] **Step 4: Run database tests**

Run: `go test ./internal/database -count=1`

Expected: PASS, including a second invocation that leaves `72.00` unchanged.

- [ ] **Step 5: Commit the migration**

```bash
git add internal/database/credit_package_migration.go internal/database/credit_package_migration_test.go internal/database/database.go
git commit -m "feat: migrate credit packages to cny"
```

---

### Task 3: Expiring Lot Balance and Earliest-Expiry Consumption

**Files:**
- Create: `internal/server/credit_lots.go`
- Create: `internal/server/credit_lots_test.go`
- Modify: `internal/server/billing.go`
- Modify: `internal/server/handlers_creem_billing.go`
- Modify: `internal/server/handlers_business.go`

**Interfaces:**
- Consumes: `models.PurchasedCreditLot`, active historical `models.SubscriptionCreditGrant`, and existing credit ledger creation.
- Produces: `purchasedCreditBalance(tx *gorm.DB, userID uuid.UUID, now time.Time) (int64, *time.Time, error)`, `consumePurchasedCreditLots(tx *gorm.DB, userID uuid.UUID, credits int64, now time.Time) (int64, error)`, and `expirePurchasedCreditLots(tx *gorm.DB, userID uuid.UUID, now time.Time) error`.

- [ ] **Step 1: Write failing FIFO-by-expiry tests**

```go
func TestConsumePurchasedCreditLotsUsesEarliestExpiry(t *testing.T) {
    db, userID := creditLotTestDB(t)
    early := seedLot(t, db, userID, 100, now.Add(time.Hour))
    late := seedLot(t, db, userID, 100, now.Add(2*time.Hour))

    consumed, err := consumePurchasedCreditLots(db, userID, 150, now)
    require.NoError(t, err)
    assert.Equal(t, int64(150), consumed)
    assert.Equal(t, int64(0), reloadLot(t, db, early.ID).RemainingCredits)
    assert.Equal(t, int64(50), reloadLot(t, db, late.ID).RemainingCredits)
}

func TestPurchasedCreditBalanceExcludesExpiredLots(t *testing.T) {
    db, userID := creditLotTestDB(t)
    seedLot(t, db, userID, 80, now.Add(-time.Second))
    seedLot(t, db, userID, 20, now.Add(time.Hour))
    balance, nextExpiry, err := purchasedCreditBalance(db, userID, now)
    require.NoError(t, err)
    assert.Equal(t, int64(20), balance)
    assert.Equal(t, now.Add(time.Hour), *nextExpiry)
}
```

- [ ] **Step 2: Verify the new service is absent**

Run: `go test ./internal/server -run 'Test(ConsumePurchased|PurchasedCreditBalance)' -count=1`

Expected: FAIL with undefined lot-service functions.

- [ ] **Step 3: Implement locked lot consumption**

```go
func consumePurchasedCreditLots(tx *gorm.DB, userID uuid.UUID, requested int64, now time.Time) (int64, error) {
    if err := expirePurchasedCreditLots(tx, userID, now); err != nil { return 0, err }
    var lots []models.PurchasedCreditLot
    err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
        Where("user_id = ? AND status = ? AND remaining_credits > 0 AND expires_at > ?", userID, "active", now).
        Order("expires_at ASC, created_at ASC").Find(&lots).Error
    if err != nil { return 0, err }
    remaining := requested
    for i := range lots {
        take := min(remaining, lots[i].RemainingCredits)
        if take == 0 { break }
        lots[i].RemainingCredits -= take
        remaining -= take
        if lots[i].RemainingCredits == 0 { lots[i].Status = "consumed" }
        if err := tx.Save(&lots[i]).Error; err != nil { return 0, err }
    }
    return requested - remaining, nil
}
```

Update `consumeCredits` to retain the current historical subscription-grant policy and use purchased lots for purchased balance. Update `tokenBalance` and `creditBalanceSnapshot` to return `purchased_credits`, `subscription_credits`, `total_credits`, and `next_expiry_at`; stop treating `UserTokenAccount.TotalTokens` as authoritative for new purchases.

- [ ] **Step 4: Run billing and server tests**

Run: `go test ./internal/server -run 'Test(ConsumePurchased|PurchasedCreditBalance|ConsumeCredits|TokenBalance)' -count=1`

Expected: PASS with expired lots excluded and historical subscription grants preserved.

- [ ] **Step 5: Commit lot accounting**

```bash
git add internal/server/credit_lots.go internal/server/credit_lots_test.go internal/server/billing.go internal/server/handlers_creem_billing.go internal/server/handlers_business.go
git commit -m "feat: consume expiring purchased credit lots"
```

---

### Task 4: Shared Order Creation and Idempotent Settlement

**Files:**
- Create: `internal/server/credit_payments.go`
- Create: `internal/server/credit_payments_test.go`
- Modify: `internal/server/handlers_business.go`

**Interfaces:**
- Consumes: models from Task 1 and lot balance from Task 3.
- Produces: `createCreditPaymentOrder(tx *gorm.DB, userID, packageID uuid.UUID, provider string, now time.Time) (*models.CreditPaymentOrder, error)` and `settleCreditPaymentOrder(tx *gorm.DB, outTradeNo, providerTransactionID string, paidAt time.Time, paidAmount decimal.Decimal, payload []byte) (*models.PurchasedCreditLot, error)`.

- [ ] **Step 1: Write failing concurrency and snapshot tests**

```go
func TestCreateCreditPaymentOrderSnapshotsPackage(t *testing.T) {
    order, err := createCreditPaymentOrder(db, user.ID, pkg.ID, "alipay", now)
    require.NoError(t, err)
    assert.Equal(t, "1.90", order.AmountCNY.StringFixed(2))
    assert.Equal(t, int64(500), order.Credits)
    assert.True(t, order.IsTrial)
}

func TestSettleCreditPaymentOrderIsIdempotent(t *testing.T) {
    first, err := settleCreditPaymentOrder(db, order.OutTradeNo, "txn-1", now, order.AmountCNY, nil)
    require.NoError(t, err)
    second, err := settleCreditPaymentOrder(db, order.OutTradeNo, "txn-1", now, order.AmountCNY, nil)
    require.NoError(t, err)
    assert.Equal(t, first.ID, second.ID)
    assert.Equal(t, int64(1), countLotsForOrder(t, db, order.ID))
}

func TestTrialClaimSurvivesRefundAndBlocksSecondSuccess(t *testing.T) {
    settleTrialOrder(t, db, user.ID)
    refundTrialOrder(t, db, user.ID)
    _, err := settleSecondTrialOrder(t, db, user.ID)
    assert.ErrorIs(t, err, ErrTrialAlreadyPurchased)
}
```

- [ ] **Step 2: Run focused tests**

Run: `go test ./internal/server -run 'Test(CreateCreditPaymentOrder|SettleCreditPaymentOrder|TrialClaim)' -count=1`

Expected: FAIL because the shared order service is undefined.

- [ ] **Step 3: Implement provider validation and settlement transaction**

```go
var (
    ErrUnsupportedPaymentProvider = errors.New("unsupported payment provider")
    ErrTrialAlreadyPurchased      = errors.New("trial package already purchased")
    ErrPaymentAmountMismatch      = errors.New("payment amount mismatch")
)

func settleCreditPaymentOrder(db *gorm.DB, outTradeNo, transactionID string, paidAt time.Time, paidAmount decimal.Decimal, payload []byte) (*models.PurchasedCreditLot, error) {
    var lot models.PurchasedCreditLot
    err := db.Transaction(func(tx *gorm.DB) error {
        var order models.CreditPaymentOrder
        if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("out_trade_no = ?", outTradeNo).First(&order).Error; err != nil { return err }
        if order.Status == "paid" || order.Status == "refunded" {
            return tx.Where("payment_order_id = ?", order.ID).First(&lot).Error
        }
        if !order.AmountCNY.Equal(paidAmount) { return ErrPaymentAmountMismatch }
        if order.IsTrial {
            claim := models.CreditTrialClaim{ID: uuid.New(), UserID: order.UserID, PaymentOrderID: order.ID}
            if err := tx.Create(&claim).Error; err != nil { return ErrTrialAlreadyPurchased }
        }
        expiry := paidAt.Add(720 * time.Hour)
        lot = models.PurchasedCreditLot{ID: uuid.New(), UserID: order.UserID, PaymentOrderID: order.ID, GrantedCredits: order.Credits, RemainingCredits: order.Credits, Status: "active", ExpiresAt: expiry}
        if err := tx.Create(&lot).Error; err != nil { return err }
        order.Status, order.ProviderTransactionID, order.PaidAt, order.ExpiresAt = "paid", transactionID, &paidAt, &expiry
        order.ProviderPayload = datatypes.JSON(payload)
        return tx.Save(&order).Error
    })
    return &lot, err
}
```

Order creation must reject inactive packages, non-CNY packages, validity other than 720, providers outside `wechat|alipay`, and trial users who already have a `CreditTrialClaim`. It must snapshot `AmountCNY`, total Credits, package name, and trial flag before calling a provider.

- [ ] **Step 4: Run shared payment tests**

Run: `go test ./internal/server -run 'Test(CreateCreditPaymentOrder|SettleCreditPaymentOrder|TrialClaim)' -count=1`

Expected: PASS, with exactly one lot and one lifetime trial claim.

- [ ] **Step 5: Commit the shared domain service**

```bash
git add internal/server/credit_payments.go internal/server/credit_payments_test.go internal/server/handlers_business.go
git commit -m "feat: add shared credit payment settlement"
```

---

### Task 5: WeChat Adapter and Unified Credit-Payment API

**Files:**
- Create: `internal/server/handlers_credit_payments.go`
- Create: `internal/server/handlers_credit_payments_test.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/handlers_business.go`
- Modify: `internal/server/currency.go`

**Interfaces:**
- Consumes: `createCreditPaymentOrder`, `settleCreditPaymentOrder`, existing WeChat Native create/query/notify client.
- Produces: `POST /api/v1/credit-payments`, `GET /api/v1/credit-payments/:out_trade_no`, and compatibility aliases for existing recharge status routes.

- [ ] **Step 1: Write failing HTTP tests**

```go
func TestCreateCreditPaymentWeChatUsesStoredCNYPrice(t *testing.T) {
    fake := &fakeWeChatPay{CodeURL: "weixin://wxpay/bizpayurl?pr=test"}
    app := newPaymentTestApp(t, fake, nil)
    res := requestJSON(t, app, http.MethodPost, "/api/v1/credit-payments", `{"package_id":"`+pkg.ID.String()+`","provider":"wechat"}`, userToken)
    assert.Equal(t, http.StatusCreated, res.Code)
    assert.Equal(t, int64(190), fake.LastAmountFen)
    assert.NotContains(t, res.Body.String(), "exchange_rate")
}

func TestCreditPaymentStatusSynchronizesAndSettlesWeChatOrder(t *testing.T) {
    fake := &fakeWeChatPay{QueryStatus: "SUCCESS", TransactionID: "wx-1", PaidAmountFen: 190}
    res := request(t, app, http.MethodGet, "/api/v1/credit-payments/"+order.OutTradeNo, userToken)
    assert.Equal(t, http.StatusOK, res.Code)
    assert.Equal(t, int64(1), countLotsForOrder(t, db, order.ID))
}
```

- [ ] **Step 2: Run the handler tests**

Run: `go test ./internal/server -run 'Test(CreateCreditPaymentWeChat|CreditPaymentStatusSynchronizes)' -count=1`

Expected: FAIL with route not found.

- [ ] **Step 3: Add unified request/response and WeChat adapter**

```go
type createCreditPaymentRequest struct {
    PackageID uuid.UUID `json:"package_id" validate:"required"`
    Provider  string    `json:"provider" validate:"required,oneof=wechat alipay"`
}

type creditPaymentResponse struct {
    OutTradeNo string     `json:"out_trade_no"`
    Provider   string     `json:"provider"`
    Status     string     `json:"status"`
    AmountCNY  string     `json:"amount_cny"`
    Credits    int64      `json:"credits"`
    CodeURL    string     `json:"code_url,omitempty"`
    PaidAt     *time.Time `json:"paid_at,omitempty"`
    ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}
```

Register:

```go
creditPayments := v1.Group("/credit-payments", a.authRequired())
creditPayments.POST("", a.createCreditPayment)
creditPayments.GET("/:out_trade_no", a.creditPaymentStatus)
v1.POST("/credit-payments/wechat/webhook", a.wechatCreditPaymentWebhook)
```

Convert stored CNY directly to fen with `AmountCNY.Mul(decimal.NewFromInt(100)).IntPart()`. Remove `usdToWeChatCNYAmount` from all new-credit paths; keep the function only if historical code still references it. Existing `/credit-accounts/recharge` and status routes call these new handlers for a deprecation period.

- [ ] **Step 4: Run WeChat and regression tests**

Run: `go test ./internal/server -run 'Test(CreateCreditPaymentWeChat|CreditPaymentStatusSynchronizes|WeChat)' -count=1`

Expected: PASS and provider amount equals the persisted CNY snapshot.

- [ ] **Step 5: Commit WeChat migration**

```bash
git add internal/server/handlers_credit_payments.go internal/server/handlers_credit_payments_test.go internal/server/server.go internal/server/handlers_business.go internal/server/currency.go
git commit -m "feat: route wechat through shared credit payments"
```

---

### Task 6: Alipay Face-to-Face QR Adapter

**Files:**
- Modify: `internal/providers/alipay.go`
- Modify: `internal/providers/alipay_test.go`
- Create: `internal/server/handlers_alipay_credit_payments.go`
- Create: `internal/server/handlers_alipay_credit_payments_test.go`
- Modify: `internal/server/server.go`

**Interfaces:**
- Consumes: existing Alipay RSA2 signing, gateway configuration, and Task 4 shared settlement.
- Produces: `PrecreateCreditPayment`, `QueryCreditPayment`, `CloseCreditPayment`, `RefundCreditPayment`, and `/api/v1/credit-payments/alipay/webhook`.

- [ ] **Step 1: Write failing signed-request and callback tests**

```go
func TestPrecreateCreditPaymentCallsTradePrecreate(t *testing.T) {
    gateway := newAlipayGatewayRecorder(t, `{"alipay_trade_precreate_response":{"code":"10000","qr_code":"https://qr.alipay.com/test"}}`)
    client := newTestAlipayClient(gateway.URL)
    got, err := client.PrecreateCreditPayment(context.Background(), AlipayPrecreateRequest{OutTradeNo: "CR202608020001", Subject: "NarratoAI 500 Credits", TotalAmount: "1.90"})
    require.NoError(t, err)
    assert.Equal(t, "https://qr.alipay.com/test", got.QRCode)
    assert.Equal(t, "alipay.trade.precreate", gateway.FormValue("method"))
}

func TestAlipayCreditWebhookSettlesVerifiedTradeSuccess(t *testing.T) {
    form := signedAlipayNotification(t, map[string]string{"out_trade_no": order.OutTradeNo, "trade_no": "ali-1", "trade_status": "TRADE_SUCCESS", "total_amount": "1.90"})
    res := postForm(t, app, "/api/v1/credit-payments/alipay/webhook", form)
    assert.Equal(t, http.StatusOK, res.Code)
    assert.Equal(t, "success", res.Body.String())
    assert.Equal(t, int64(1), countLotsForOrder(t, db, order.ID))
}
```

- [ ] **Step 2: Run focused Alipay tests**

Run: `go test ./internal/providers ./internal/server -run 'Test(PrecreateCreditPayment|AlipayCreditWebhook)' -count=1`

Expected: FAIL because face-to-face methods and webhook route are absent.

- [ ] **Step 3: Implement ordinary QR operations**

```go
type AlipayPrecreateRequest struct { OutTradeNo, Subject, TotalAmount string }
type AlipayPrecreateResponse struct { QRCode string }

func (c *AlipayClient) PrecreateCreditPayment(ctx context.Context, req AlipayPrecreateRequest) (*AlipayPrecreateResponse, error) {
    biz, err := json.Marshal(map[string]string{"out_trade_no": req.OutTradeNo, "subject": req.Subject, "total_amount": req.TotalAmount})
    if err != nil { return nil, err }
    var envelope struct {
        Response struct {
            Code    string `json:"code"`
            SubCode string `json:"sub_code"`
            SubMsg  string `json:"sub_msg"`
            QRCode  string `json:"qr_code"`
        } `json:"alipay_trade_precreate_response"`
    }
    if err := c.call(ctx, "alipay.trade.precreate", biz, &envelope); err != nil { return nil, err }
    if envelope.Response.Code != "10000" { return nil, fmt.Errorf("alipay precreate %s: %s", envelope.Response.SubCode, envelope.Response.SubMsg) }
    return &AlipayPrecreateResponse{QRCode: envelope.Response.QRCode}, nil
}
```

Add matching `alipay.trade.query`, `alipay.trade.close`, and `alipay.trade.refund` calls. The webhook must verify RSA2 signature, `app_id`, seller PID, amount, and `TRADE_SUCCESS|TRADE_FINISHED`, then call `settleCreditPaymentOrder`. Return literal `success` only after commit; otherwise return `failure` so Alipay retries.

- [ ] **Step 4: Run provider and handler tests**

Run: `go test ./internal/providers ./internal/server -run 'Test(PrecreateCreditPayment|AlipayCreditWebhook|CreditPayment.*Alipay)' -count=1`

Expected: PASS, including invalid-signature and wrong-amount rejection cases.

- [ ] **Step 5: Commit Alipay QR support**

```bash
git add internal/providers/alipay.go internal/providers/alipay_test.go internal/server/handlers_alipay_credit_payments.go internal/server/handlers_alipay_credit_payments_test.go internal/server/server.go
git commit -m "feat: add alipay qr credit payments"
```

---

### Task 7: Provider-Aware Refunds and Subscription Checkout Shutdown

**Files:**
- Create: `internal/server/credit_refunds.go`
- Create: `internal/server/credit_refunds_test.go`
- Modify: `internal/server/handlers_credit_payments.go`
- Modify: `internal/server/handlers_alipay_subscriptions.go`
- Modify: `internal/server/handlers_creem_billing.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/payment_channel_policy_test.go`

**Interfaces:**
- Consumes: `models.CreditPaymentOrder`, purchased lots, provider refund adapters, and historical subscription handlers.
- Produces: `refundCreditPaymentOrder(db *gorm.DB, outTradeNo string, now time.Time) (revoked int64, err error)` and admin endpoint `POST /api/v1/credit-payments/:out_trade_no/refund`.

- [ ] **Step 1: Write failing refund and policy tests**

```go
func TestRefundRevokesOnlyOriginalLotRemainder(t *testing.T) {
    original := seedPaidLot(t, db, user.ID, 500, 120)
    other := seedPaidLot(t, db, user.ID, 1000, 1000)
    revoked, err := refundCreditPaymentOrder(db, original.Order.OutTradeNo, now)
    require.NoError(t, err)
    assert.Equal(t, int64(120), revoked)
    assert.Equal(t, int64(0), reloadLot(t, db, original.Lot.ID).RemainingCredits)
    assert.Equal(t, int64(1000), reloadLot(t, db, other.Lot.ID).RemainingCredits)
}

func TestNewSubscriptionCheckoutIsGoneButWebhookRemains(t *testing.T) {
    assertRouteMissing(t, app, http.MethodPost, "/api/v1/subscriptions/checkout")
    assertRouteExists(t, app, http.MethodPost, "/api/v1/billing/creem/webhook")
    assertRouteExists(t, app, http.MethodPost, "/api/v1/subscriptions/alipay/webhook")
}
```

- [ ] **Step 2: Run refund and route-policy tests**

Run: `go test ./internal/server -run 'Test(RefundRevokes|NewSubscriptionCheckout)' -count=1`

Expected: FAIL because new checkout routes remain and refund is WeChat/subscription-specific.

- [ ] **Step 3: Implement scoped revocation and disable new recurring checkout**

```go
func revokePurchasedLotRemainder(tx *gorm.DB, orderID uuid.UUID, now time.Time) (int64, error) {
    var lot models.PurchasedCreditLot
    if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("payment_order_id = ?", orderID).First(&lot).Error; err != nil { return 0, err }
    revoked := lot.RemainingCredits
    lot.RemainingCredits = 0
    lot.Status = "refunded"
    lot.RefundedAt = &now
    return revoked, tx.Save(&lot).Error
}
```

Call the order provider's refund API before marking the order refunded, then revoke only that locked lot. Make repeated refund requests idempotently return the first result. Remove registration of `/subscriptions/checkout`, `/subscriptions/alipay/checkout`, and `/credit-accounts/recharge/creem`; retain existing source and all provider webhook routes. Keep historical balance and cancellation endpoints needed by existing subscribers.

- [ ] **Step 4: Run all server tests**

Run: `go test ./internal/server -count=1`

Expected: PASS, with no new recurring checkout route and historical webhook tests unchanged.

- [ ] **Step 5: Commit refund and channel policy**

```bash
git add internal/server/credit_refunds.go internal/server/credit_refunds_test.go internal/server/handlers_credit_payments.go internal/server/handlers_alipay_subscriptions.go internal/server/handlers_creem_billing.go internal/server/server.go internal/server/payment_channel_policy_test.go
git commit -m "feat: finalize one-time credit payment policy"
```

---

### Task 8: Provider-Neutral Frontend Payment Client and QR Modal

**Files:**
- Modify: `src/features/subscriptions/types/token.types.ts`
- Modify: `src/features/subscriptions/types/payment.types.ts`
- Modify: `src/features/subscriptions/services/paymentService.ts`
- Create: `src/features/subscriptions/services/paymentService.test.ts`
- Create: `src/features/subscriptions/components/CreditPaymentModal.tsx`
- Create: `src/features/subscriptions/components/CreditPaymentModal.test.tsx`
- Modify: `src/features/subscriptions/components/index.ts`
- Retain: `src/features/subscriptions/components/WeChatPaymentModal.tsx`

**Interfaces:**
- Consumes: Task 5 unified JSON API and existing authenticated `httpClient`.
- Produces: `PaymentProvider = 'wechat' | 'alipay'`, `paymentService.createCreditPayment(packageId, provider)`, `paymentService.getCreditPayment(outTradeNo)`, and `<CreditPaymentModal package={pkg} opened onClose onPaid />`.

- [ ] **Step 1: Write failing client and modal tests**

```ts
it('creates an Alipay order through the unified endpoint', async () => {
  mockedHttp.post.mockResolvedValueOnce(order)
  await paymentService.createCreditPayment('pkg-1', 'alipay')
  expect(mockedHttp.post).toHaveBeenCalledWith('/api/v1/credit-payments', { package_id: 'pkg-1', provider: 'alipay' })
})

it('switches provider and renders the returned QR URL', async () => {
  render(<CreditPaymentModal package={trialPackage} opened onClose={vi.fn()} onPaid={vi.fn()} />)
  await userEvent.click(screen.getByRole('radio', { name: '支付宝' }))
  await userEvent.click(screen.getByRole('button', { name: '生成支付二维码' }))
  expect(await screen.findByAltText('支付宝支付二维码')).toBeInTheDocument()
})
```

- [ ] **Step 2: Run focused frontend tests**

Run: `npm test -- --run src/features/subscriptions/services/paymentService.test.ts src/features/subscriptions/components/CreditPaymentModal.test.tsx`

Expected: FAIL because the provider-neutral client/modal do not exist.

- [ ] **Step 3: Define exact frontend contracts and modal behavior**

```ts
export type PaymentProvider = 'wechat' | 'alipay'

export interface CreditPaymentOrder {
  out_trade_no: string
  provider: PaymentProvider
  status: 'pending' | 'paid' | 'closed' | 'refunded' | 'failed'
  amount_cny: string
  credits: number
  code_url?: string
  paid_at?: string
  expires_at?: string
}

export const paymentService = {
  createCreditPayment: (packageId: string, provider: PaymentProvider) =>
    httpClient.post<CreditPaymentOrder>('/api/v1/credit-payments', { package_id: packageId, provider }),
  getCreditPayment: (outTradeNo: string) =>
    httpClient.get<CreditPaymentOrder>(`/api/v1/credit-payments/${outTradeNo}`),
}
```

The modal uses a Mantine segmented control for `微信支付` and `支付宝`, generates the order only after provider selection, renders the existing QR component, polls every 2 seconds, stops on terminal status/unmount, calls `onPaid(order)` once, and exposes retry after API failure. Do not add any USD conversion or provider-specific polling branch.

- [ ] **Step 4: Run focused tests and typecheck**

Run: `npm test -- --run src/features/subscriptions/services/paymentService.test.ts src/features/subscriptions/components/CreditPaymentModal.test.tsx && npx tsc -b --pretty false`

Expected: PASS.

- [ ] **Step 5: Commit the payment client and modal**

```bash
git add src/features/subscriptions/types/token.types.ts src/features/subscriptions/types/payment.types.ts src/features/subscriptions/services/paymentService.ts src/features/subscriptions/services/paymentService.test.ts src/features/subscriptions/components/CreditPaymentModal.tsx src/features/subscriptions/components/CreditPaymentModal.test.tsx src/features/subscriptions/components/index.ts
git commit -m "feat: add dual-channel credit payment modal"
```

---

### Task 9: CNY Credit Store, Trial Eligibility, and Package Administration

**Files:**
- Modify: `src/pages/SubscriptionPage.tsx`
- Modify: `src/pages/SubscriptionPage.test.tsx`
- Modify: `src/pages/PricingPage.tsx`
- Modify: `src/pages/PricingPage.test.tsx`
- Modify: `src/pages/HomePage.tsx`
- Modify: `src/pages/HomePage.test.tsx`
- Modify: `src/features/subscriptions/components/admin/PackageCard.tsx`
- Modify: `src/features/subscriptions/components/admin/PackageFormModal.tsx`
- Modify: `src/pages/admin/subscriptions/CreditPackagesPage.tsx`
- Modify: `src/features/subscriptions/services/tokenPackageService.test.ts`
- Modify: `src/shared/i18n/dictionary.ts`

**Interfaces:**
- Consumes: `CreditPaymentModal`, package eligibility fields, and balance `next_expiry_at`.
- Produces: customer-facing one-time Credit store and CNY-only administrator package editor.

- [ ] **Step 1: Rewrite failing page and admin assertions**

```tsx
it('shows one-time CNY packages without subscription or USD copy', () => {
  renderPage({ packages: [trialPackage, standardPackage] })
  expect(screen.getByText('¥1.90')).toBeInTheDocument()
  expect(screen.getByText('500 Credits')).toBeInTheDocument()
  expect(screen.getByText('购买后 30 天有效')).toBeInTheDocument()
  expect(screen.queryByText(/USD|美元|自动续费|月付/)).not.toBeInTheDocument()
})

it('disables the lifetime trial after it has been purchased', () => {
  renderPage({ packages: [{ ...trialPackage, purchase_eligible: false, purchase_ineligible_reason: 'trial_already_purchased' }] })
  expect(screen.getByRole('button', { name: '已购买体验包' })).toBeDisabled()
})

it('admin form fixes currency and validity while hiding Creem fields', () => {
  render(<PackageFormModal opened onClose={vi.fn()} onSubmit={vi.fn()} />)
  expect(screen.getByLabelText('价格（人民币）')).toBeInTheDocument()
  expect(screen.getByText('有效期：30 天')).toBeInTheDocument()
  expect(screen.getByLabelText('体验套餐')).toBeInTheDocument()
  expect(screen.queryByLabelText(/Creem|USD/)).not.toBeInTheDocument()
})
```

- [ ] **Step 2: Run page tests**

Run: `npm test -- --run src/pages/SubscriptionPage.test.tsx src/pages/PricingPage.test.tsx src/pages/HomePage.test.tsx src/features/subscriptions/services/tokenPackageService.test.ts`

Expected: FAIL because the current pages still expose subscriptions and USD-to-WeChat conversion.

- [ ] **Step 3: Implement the CNY-only purchase experience**

Extend `TokenPackage` exactly as follows:

```ts
currency: 'CNY'
validity_hours: 720
is_trial: boolean
per_account_purchase_limit: number
purchase_eligible: boolean
purchase_ineligible_reason?: 'trial_already_purchased' | 'purchase_limit_reached'
```

Make `SubscriptionPage` and `PricingPage` display `¥${Number(pkg.price).toFixed(2)}`, total Credits, `购买后 30 天有效`, and one purchase button opening `CreditPaymentModal`. Show `最早一批将于 {localized next_expiry_at} 到期` beside balance when present. Remove `useSubscription`, subscription checkout cards, `formatUSD`, `usdToWeChatCny`, and WeChat-converted-price copy from active customer pages. Change Home pricing navigation/copy to one-time Credits and remove recurring promotional claims.

The admin form sends `currency: 'CNY'`, `validity_hours: 720`, `is_trial`, and `per_account_purchase_limit` (`1` when trial, otherwise `0`). It displays legacy Creem fields nowhere but does not delete them from TypeScript response compatibility.

- [ ] **Step 4: Run all frontend tests and production build**

Run: `npm test -- --run && npx tsc -b --pretty false && npm run build`

Expected: all tests PASS, TypeScript has zero errors, and Vite build exits 0.

- [ ] **Step 5: Commit the customer and admin UI**

```bash
git add src/pages/SubscriptionPage.tsx src/pages/SubscriptionPage.test.tsx src/pages/PricingPage.tsx src/pages/PricingPage.test.tsx src/pages/HomePage.tsx src/pages/HomePage.test.tsx src/features/subscriptions/components/admin/PackageCard.tsx src/features/subscriptions/components/admin/PackageFormModal.tsx src/pages/admin/subscriptions/CreditPackagesPage.tsx src/features/subscriptions/services/tokenPackageService.test.ts src/shared/i18n/dictionary.ts
git commit -m "feat: replace subscriptions with cny credit store"
```

---

### Task 10: End-to-End Verification in WSL and Release Readiness

**Files:**
- Modify: `README.md`
- Create: `docs/payment-verification.md`
- Modify: `.env.example`

**Interfaces:**
- Consumes: completed backend/frontend, enterprise Alipay environment values, Docker Compose, and WSL deployment instructions in `AGENTS.md`.
- Produces: reproducible local verification evidence and an explicit production readiness checklist; it does not deploy production.

- [ ] **Step 1: Document exact required environment variables**

Add names only, never secret values:

```dotenv
ALIPAY_APP_ID=
ALIPAY_SELLER_ID=
ALIPAY_PRIVATE_KEY=
ALIPAY_PUBLIC_KEY=
ALIPAY_GATEWAY_URL=https://openapi.alipay.com/gateway.do
ALIPAY_NOTIFY_URL=https://api.example.com/api/v1/credit-payments/alipay/webhook
WECHAT_PAY_NOTIFY_URL=https://api.example.com/api/v1/credit-payments/wechat/webhook
```

Document that the enabled Alipay application must belong to the enterprise merchant and have `alipay.trade.precreate` permission.

- [ ] **Step 2: Run backend static checks and tests**

Run in `NarratoAI-SaaS-B-Go`:

```powershell
gofmt -w internal/models/models.go internal/database/credit_package_migration.go internal/server/credit_lots.go internal/server/credit_payments.go internal/server/handlers_credit_payments.go internal/server/handlers_alipay_credit_payments.go internal/server/credit_refunds.go
go vet ./...
go test ./... -count=1
```

Expected: `go vet` exits 0 and every Go package reports `ok` or `[no test files]`.

- [ ] **Step 3: Run frontend verification**

Run in `NarratoAI-SaaS-F`:

```powershell
npm test -- --run
npx tsc -b --pretty false
npm run build
```

Expected: all Vitest suites pass, typecheck exits 0, and Vite produces `dist/`.

- [ ] **Step 4: Start and verify the WSL deployment**

Run from Windows PowerShell:

```powershell
Start-Process -WindowStyle Hidden -FilePath "$env:WINDIR\System32\wsl.exe" -ArgumentList '-d','Ubuntu','--exec','sleep','infinity'
wsl.exe -d Ubuntu -- bash -lc "cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-alipay-subscription && docker compose up -d --build && cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-F-alipay-subscription && docker compose up -d --build"
Start-Sleep -Seconds 90
Invoke-WebRequest -UseBasicParsing http://127.0.0.1:8000/health
Invoke-WebRequest -UseBasicParsing http://127.0.0.1:8080/
```

Expected: both requests return HTTP 200.

- [ ] **Step 5: Execute the manual low-value payment matrix**

Using a dedicated test account in `http://127.0.0.1:8080/`, record in `docs/payment-verification.md`:

```text
[ ] WeChat trial: QR generated, ¥1.90 paid, exactly 500 Credits granted, expiry = paid_at + 720h
[ ] Trial replay via Alipay: blocked before payment because the account already claimed trial
[ ] Alipay standard package: QR generated, exact stored CNY charged, one lot granted
[ ] Callback replay: no second lot and no second ledger credit
[ ] Earliest-expiry consumption: older lot decreases before newer lot
[ ] Refund after partial use: only original lot remainder becomes zero; other lots unchanged
[ ] Historical subscription user: remaining subscription grant still appears and can be consumed
[ ] New recurring checkout routes: unavailable
```

Use a separate account to reverse provider order (Alipay trial, then WeChat replay) so cross-channel enforcement is tested both ways. Do not attempt production deployment until every row is checked.

- [ ] **Step 6: Commit verification documentation**

```bash
git add README.md docs/payment-verification.md .env.example
git commit -m "docs: add dual-channel payment verification runbook"
```

- [ ] **Step 7: Review final diffs without deploying**

Run in each worktree:

```powershell
git status --short
git log --oneline --decorate -12
```

Expected: clean working trees and the ten task commits present. Stop here for explicit production deployment approval.
