# Alipay Subscription and WeChat Recharge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Alipay AI Subscription the only new monthly recurring-subscription channel, keep WeChat Native as the only one-time Credits channel, and retain Creem only for historical compatibility.

**Architecture:** Add a provider-neutral subscription event processor behind an Alipay client and callback adapter. All entitlement changes flow through database-backed billing-period idempotency; frontend availability mirrors backend-enforced channel configuration. Creem checkout creation is feature-disabled while its webhook and historical records remain operational.

**Tech Stack:** Go 1.26, Gin, GORM/PostgreSQL, React 19, TypeScript 5.8, Mantine 9, Vitest, Go `httptest`, Alipay AI Subscription APIs.

## Global Constraints

- New recurring subscriptions are monthly only and use Alipay AI Subscription.
- One-time Credits purchases use WeChat Pay Native only.
- New annual and Creem checkouts are rejected by the backend, not merely hidden.
- Existing Creem subscriptions and Creem webhooks remain supported.
- Active Creem agreements are not migrated to Alipay.
- A user cannot have concurrent effective Creem and Alipay subscriptions.
- Failed renewals have a three-day grace period and do not grant new-period Credits.
- Every provider callback is signature-verified and actively confirmed before entitlements change.
- Database uniqueness is the final duplicate-period and duplicate-grant guard.
- Migrations are additive and must not delete Creem fields or tables.

---

## File Structure

### Backend

- `internal/config/config.go`: payment-channel and Alipay environment configuration.
- `env.example`: non-secret deployment variable contract.
- `internal/models/models.go`: additive Alipay session, event, and subscription fields.
- `internal/database/database.go`: migrations and required uniqueness validation.
- `internal/providers/alipay.go`: narrow Alipay HTTP/signature client.
- `internal/providers/alipay_test.go`: provider protocol and cryptographic tests.
- `internal/server/subscription_events.go`: provider-neutral state transitions and Credits grants.
- `internal/server/subscription_events_test.go`: idempotency and lifecycle tests.
- `internal/server/handlers_alipay_subscriptions.go`: checkout, return, webhook, sync, portal, and cancel handlers.
- `internal/server/handlers_alipay_subscriptions_test.go`: endpoint and adversarial callback tests.
- `internal/server/handlers_creem_billing.go`: server-side new-checkout gate only.
- `internal/server/handlers_compat_system_subscriptions.go`: provider-aware current/cancel/health responses.
- `internal/server/server.go`: provider wiring and routes.
- `internal/server/server_test.go`: channel integration and WeChat regression tests.

### Frontend

- `src/features/subscriptions/types/subscription.types.ts`: provider-aware response types.
- `src/features/subscriptions/services/alipaySubscriptionService.ts`: Alipay checkout and portal API calls.
- `src/features/subscriptions/services/index.ts`: service export.
- `src/pages/SubscriptionPage.tsx`: monthly Alipay subscription and WeChat-only recharge UI.
- `src/pages/SubscriptionPage.test.tsx`: channel visibility and redirect tests.
- `src/features/subscriptions/components/admin/PlanFormModal.tsx`: Alipay mapping controls.
- `src/features/subscriptions/components/admin/PlanCard.tsx`: provider mapping status.

---

### Task 1: Channel Configuration and Additive Persistence

**Files:**
- Modify: `internal/config/config.go`
- Modify: `env.example`
- Modify: `internal/models/models.go`
- Modify: `internal/database/database.go`
- Test: `internal/config/config_test.go`
- Test: `internal/database/database_test.go`

**Interfaces:**
- Produces: `Config.AlipayConfigured() bool`, `Config.NewCreemCheckoutEnabled() bool`, and additive GORM models used by later tasks.
- Produces: payment provider constants `alipay`, `wechat`, and `creem`.

- [ ] **Step 1: Write failing configuration tests**

Add table tests proving defaults are `SUBSCRIPTION_PAYMENT_PROVIDER=alipay`, `CREDIT_PAYMENT_PROVIDER=wechat`, and `CREEM_CHECKOUT_ENABLED=false`; production Alipay readiness requires app ID, merchant identity, private key, Alipay public key, notify URL, and return URL.

```go
func TestPaymentChannelDefaults(t *testing.T) {
	t.Setenv("SUBSCRIPTION_PAYMENT_PROVIDER", "")
	t.Setenv("CREDIT_PAYMENT_PROVIDER", "")
	t.Setenv("CREEM_CHECKOUT_ENABLED", "")
	cfg := Load()
	if cfg.SubscriptionPaymentProvider != "alipay" || cfg.CreditPaymentProvider != "wechat" || cfg.CreemCheckoutEnabled {
		t.Fatalf("unexpected channel defaults: %#v", cfg)
	}
}
```

- [ ] **Step 2: Run the focused tests and verify failure**

Run: `go test ./internal/config ./internal/database -run 'TestPaymentChannel|TestAlipayModels' -count=1`

Expected: FAIL because the channel fields and Alipay models do not exist.

- [ ] **Step 3: Add configuration fields and helpers**

Add `SubscriptionPaymentProvider`, `CreditPaymentProvider`, `CreemCheckoutEnabled`, `AlipayAppID`, `AlipayMerchantID`, `AlipayPrivateKey`, `AlipayPrivateKeyFile`, `AlipayPublicKey`, `AlipayPublicKeyFile`, `AlipayGatewayURL`, `AlipayNotifyURL`, `AlipayReturnURL`, and `AlipayPortalURL`. Implement strict `AlipayConfigured()` and default the gateway to the official production gateway without embedding credentials.

- [ ] **Step 4: Add persistence models and indexes**

Extend `SubscriptionPlan` with `AlipayPlanID`, `AlipayProductID`, and `SubscriptionProviderEnabled`. Extend `UserSubscription` with `PaymentProvider`, `AlipayAgreementID`, `AlipaySubscriptionID`, `NextChargeAt`, `GracePeriodEndsAt`, `LastProviderSyncAt`, and `ProviderFailureReason`.

Create `AlipaySubscriptionSession` and provider-neutral `PaymentWebhookEvent` models. Use unique indexes on request ID, `(provider,event_id)`, Alipay agreement ID, and Alipay subscription ID. Keep `SubscriptionCreditGrant.IdempotencyKey` unchanged as the grant uniqueness boundary.

- [ ] **Step 5: Register migrations and document environment variables**

Add models to AutoMigrate and database constraint checks. Document only placeholders in `env.example`; do not add real keys.

- [ ] **Step 6: Run tests and commit**

Run: `go test ./internal/config ./internal/database -count=1`

Expected: PASS.

Commit: `git commit -am "feat: add Alipay subscription persistence and channel config"`

---

### Task 2: Alipay Provider Boundary

**Files:**
- Create: `internal/providers/alipay.go`
- Create: `internal/providers/alipay_test.go`

**Interfaces:**
- Consumes: Alipay configuration from Task 1.
- Produces: `AlipayClient` methods `CreateSubscription`, `GetSubscription`, `GetTransaction`, `CreatePortal`, and `CancelSubscription`.
- Produces: `VerifyNotification(values url.Values) error` and typed provider responses.

- [ ] **Step 1: Write failing protocol tests**

Use an `httptest.Server` and generated RSA2 key pair. Assert canonical parameter signing, response verification, notification verification, request timeouts, non-2xx handling, and provider business-code errors. Include a test that modifying `total_amount` after signing fails verification.

- [ ] **Step 2: Run provider tests and verify failure**

Run: `go test ./internal/providers -run Alipay -count=1`

Expected: FAIL because `AlipayClient` does not exist.

- [ ] **Step 3: Implement the narrow client**

Define internal request/response types instead of exposing untyped maps to server handlers. Keep API method names and field mappings in this file so later provider documentation changes have one owner.

```go
type AlipayClient interface {
	Configured() bool
	CreateSubscription(context.Context, AlipaySubscriptionRequest) (AlipaySubscriptionCheckout, error)
	GetSubscription(context.Context, string) (AlipaySubscription, error)
	GetTransaction(context.Context, string) (AlipayTransaction, error)
	CreatePortal(context.Context, string) (string, error)
	CancelSubscription(context.Context, string) error
	VerifyNotification(url.Values) error
}
```

Use RSA2/SHA-256, reject unverified responses, bound response bodies, redact secrets from errors, and use an injected HTTP client.

- [ ] **Step 4: Run tests and commit**

Run: `go test ./internal/providers -run Alipay -count=1`

Expected: PASS.

Commit: `git add internal/providers/alipay.go internal/providers/alipay_test.go && git commit -m "feat: add Alipay subscription provider client"`

---

### Task 3: Provider-Neutral Subscription Event Processor

**Files:**
- Create: `internal/server/subscription_events.go`
- Create: `internal/server/subscription_events_test.go`
- Modify: `internal/server/handlers_creem_billing.go`

**Interfaces:**
- Consumes: confirmed provider data only.
- Produces: `processSubscriptionEvent(tx *gorm.DB, event subscriptionEvent) error`.
- Produces: event types `started`, `renewed`, `cancelled`, `expired`, `payment_failed`, and `refunded`.

- [ ] **Step 1: Write failing lifecycle tests**

Cover first activation, renewal, duplicate event, same period with a different event ID, reordered cancellation, three-day grace, expiry, refund of unused Credits, refund after partial consumption, and rejection of concurrent effective subscriptions.

```go
event := subscriptionEvent{
	Provider: "alipay", ProviderEventID: "evt_1", ProviderAgreementID: "agreement_1",
	Type: subscriptionEventRenewed, UserID: user.ID, PlanID: plan.ID,
	PeriodStart: periodStart, PeriodEnd: periodEnd, ConfirmedAmount: plan.PriceMonthly,
}
```

Assert exactly one `SubscriptionCreditGrant` with idempotency key `alipay:agreement_1:<period-start>` after replaying the event.

- [ ] **Step 2: Run focused tests and verify failure**

Run: `go test ./internal/server -run 'SubscriptionEvent' -count=1`

Expected: FAIL because the processor is undefined.

- [ ] **Step 3: Implement transactional state transitions**

Lock the user subscription row where supported, validate legal transitions, create the billing-period grant, update the token account and ledger in one transaction, and treat unique-key conflicts as idempotent success only after confirming stored values match.

Do not move all Creem parsing into this task. Adapt only the final confirmed Creem state changes to call the processor where behavior is equivalent; retain Creem-specific parsing in `handlers_creem_billing.go`.

- [ ] **Step 4: Run server tests and commit**

Run: `go test ./internal/server -run 'SubscriptionEvent|CreemWebhook' -count=1`

Expected: PASS.

Commit: `git add internal/server/subscription_events.go internal/server/subscription_events_test.go internal/server/handlers_creem_billing.go && git commit -m "refactor: centralize subscription entitlement events"`

---

### Task 4: Alipay Checkout, Callback, Sync, Portal, and Cancellation

**Files:**
- Create: `internal/server/handlers_alipay_subscriptions.go`
- Create: `internal/server/handlers_alipay_subscriptions_test.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/handlers_compat_system_subscriptions.go`

**Interfaces:**
- Consumes: `AlipayClient` and `processSubscriptionEvent`.
- Produces: the Alipay routes defined in the approved design.

- [ ] **Step 1: Write failing endpoint tests**

Test authenticated checkout, inactive or unmapped plan rejection, annual-cycle rejection, concurrent-subscription rejection, signature failure, amount mismatch, product mismatch, duplicate webhook, return sync, portal creation, cancellation, and provider health reporting.

- [ ] **Step 2: Run endpoint tests and verify failure**

Run: `go test ./internal/server -run 'AlipaySubscription' -count=1`

Expected: FAIL because routes and handlers do not exist.

- [ ] **Step 3: Wire the provider and routes**

Add an `alipayClient` interface field to `App` and register:

```go
subscriptions.POST("/alipay/checkout", a.authRequired(), a.createAlipaySubscriptionCheckout)
subscriptions.POST("/alipay/sync", a.authRequired(), a.syncAlipaySubscription)
subscriptions.POST("/alipay/portal", a.authRequired(), a.createAlipaySubscriptionPortal)
subscriptions.POST("/alipay/cancel", a.authRequired(), a.cancelAlipaySubscription)
subscriptions.POST("/alipay/webhook", a.alipaySubscriptionWebhook)
subscriptions.GET("/alipay/return", a.alipaySubscriptionReturn)
```

Store every callback before processing. Acknowledge only after durable event recording. Confirm subscription and transaction with Alipay before calling the event processor.

- [ ] **Step 4: Make current subscription responses provider-aware**

Return `payment_provider`, `management_action`, `next_charge_at`, `grace_period_ends_at`, and provider-safe failure information. Never return agreement credentials.

- [ ] **Step 5: Run tests and commit**

Run: `go test ./internal/server -run 'AlipaySubscription|CurrentSubscription' -count=1`

Expected: PASS.

Commit: `git add internal/server && git commit -m "feat: add Alipay subscription lifecycle endpoints"`

---

### Task 5: Enforce Channel Policy and Preserve WeChat Recharge

**Files:**
- Modify: `internal/server/handlers_creem_billing.go`
- Modify: `internal/server/handlers_business.go`
- Modify: `internal/server/handlers_compat_system_subscriptions.go`
- Modify: `internal/server/server_test.go`

**Interfaces:**
- Consumes: channel configuration from Task 1.
- Produces: backend-enforced disabled responses for new Creem and annual checkout.

- [ ] **Step 1: Add failing policy regression tests**

Assert that new Creem subscription and Credits checkout return `409` with machine code `payment_channel_disabled`, annual subscription checkout returns `400`, historical Creem webhooks still update existing records, and WeChat package payment still grants exactly once.

- [ ] **Step 2: Run tests and verify failure**

Run: `go test ./internal/server -run 'PaymentChannel|WeChat.*Recharge|Creem.*Disabled' -count=1`

Expected: FAIL because checkout gates do not exist.

- [ ] **Step 3: Add server-side gates**

Gate only checkout creation. Do not gate Creem webhook ingestion, checkout lookup needed for historical reconciliation, or current-subscription display. Keep WeChat payment attached to `TokenPackage`; reject attempts to use it as an automatic subscription.

- [ ] **Step 4: Run the complete backend suite and commit**

Run: `go test ./... -count=1`

Expected: PASS.

Commit: `git add internal/server && git commit -m "feat: enforce Alipay subscription and WeChat recharge channels"`

---

### Task 6: Frontend Subscription and Recharge Experience

**Files:**
- Create: `src/features/subscriptions/services/alipaySubscriptionService.ts`
- Modify: `src/features/subscriptions/services/index.ts`
- Modify: `src/features/subscriptions/types/subscription.types.ts`
- Modify: `src/pages/SubscriptionPage.tsx`
- Modify: `src/pages/SubscriptionPage.test.tsx`

**Interfaces:**
- Consumes: Alipay checkout/portal endpoints and provider-aware current subscription.
- Produces: monthly-only Alipay subscription flow and WeChat-only Credits flow.

- [ ] **Step 1: Write failing UI tests**

Assert that the page contains `支付宝开通自动续费` and `微信扫码支付`; does not contain annual toggle, `银行卡支付`, or Creem action; redirects to returned Alipay URL; sends active Alipay users to the hosted portal; and reports pending, success, cancellation, and failure return states.

- [ ] **Step 2: Run the page tests and verify failure**

Run: `npm test -- --run src/pages/SubscriptionPage.test.tsx`

Expected: FAIL on old Creem and annual behavior.

- [ ] **Step 3: Add typed Alipay service methods**

```ts
export interface AlipayCheckoutResponse {
  checkout_url: string
  request_id: string
}

export const createCheckout = (planId: string) =>
  httpClient.post<AlipayCheckoutResponse>('/api/v1/subscriptions/alipay/checkout', { plan_id: planId })
```

Add corresponding `syncReturn`, `createPortal`, and `cancel` methods. Keep `creemPaymentService.ts` unchanged and unused by visible new-purchase UI.

- [ ] **Step 4: Simplify the subscription page**

Remove annual state and Creem checkout calls. Use Alipay for subscription actions and existing WeChat QR modal/service for packages. Do not add explanatory marketing copy beyond required billing and cancellation disclosure.

- [ ] **Step 5: Run frontend verification and commit in the frontend repository**

Run: `npm test -- --run src/pages/SubscriptionPage.test.tsx && npm run build && npm run lint`

Expected: all commands PASS.

Commit: `git add src/features/subscriptions src/pages/SubscriptionPage.tsx src/pages/SubscriptionPage.test.tsx && git commit -m "feat: switch subscriptions to Alipay automatic renewal"`

---

### Task 7: Administration and Reconciliation Operations

**Files:**
- Modify: `src/features/subscriptions/components/admin/PlanFormModal.tsx`
- Modify: `src/features/subscriptions/components/admin/PlanCard.tsx`
- Modify: `src/pages/admin/subscriptions/SubscriptionPlansPage.tsx`
- Create: `internal/server/subscription_reconciliation.go`
- Create: `internal/server/subscription_reconciliation_test.go`
- Modify: `internal/server/server.go`

**Interfaces:**
- Consumes: Alipay plan mappings and provider event store.
- Produces: administrator-triggered reconciliation and operational visibility.

- [ ] **Step 1: Write failing admin and reconciliation tests**

Test required Alipay mappings for active plans, read-only Creem compatibility fields, unresolved callback replay, missing callback recovery, unresolved payment failure, and no duplicate grant after reconciliation.

- [ ] **Step 2: Run focused tests and verify failure**

Run backend: `go test ./internal/server -run Reconciliation -count=1`

Run frontend: `npm test -- --run src/pages/admin/subscriptions`

Expected: FAIL because reconciliation and Alipay mapping UI are absent.

- [ ] **Step 3: Implement reconciliation**

Add an administrator endpoint and callable service function that selects unresolved sessions/events in bounded batches, queries Alipay, and calls the same event processor. Record attempts and errors; never grant directly from the job.

- [ ] **Step 4: Implement admin mapping and status UI**

Require Alipay plan and product IDs when activating a publicly purchasable subscription plan. Display provider, agreement status, current period, next charge, grace end, last sync, and failure reason. Label Creem mappings `停用兼容配置`.

- [ ] **Step 5: Run tests and commit each repository**

Backend: `go test ./internal/server -run 'Reconciliation|SubscriptionPlan' -count=1`

Frontend: `npm test -- --run src/pages/admin/subscriptions && npm run build`

Expected: PASS.

Backend commit: `git commit -am "feat: add subscription reconciliation operations"`

Frontend commit: `git commit -am "feat: manage Alipay subscription mappings"`

---

### Task 8: Qualification, Sandbox Acceptance, and Production Cutover

**Files:**
- Modify: `docs/operations/` with an Alipay subscription runbook.
- Modify: deployment environment outside Git; never commit credentials.

**Interfaces:**
- Consumes: approved Alipay enterprise account, AI Subscription product, product IDs, signing keys, and callback configuration.
- Produces: a verified production channel cutover with an explicit rollback path.

- [ ] **Step 1: Complete external qualification**

Confirm enterprise account, eligible MCC, AI Subscription approval, merchant debit/web payment products, monthly products, hosted portal, agreement text, privacy policy, refund policy, customer support, and the initial 1,000-new-signature capacity.

- [ ] **Step 2: Run full local verification**

Backend: `go test ./... -count=1`

Frontend: `npm test -- --run && npm run lint && npm run build`

Expected: all commands PASS.

- [ ] **Step 3: Execute Alipay sandbox acceptance**

Record evidence for agreement creation, first charge, callback verification, renewal, duplicate callback, failed renewal, grace period, cancellation, refund, portal management, and reconciliation. Confirm each paid period has one grant.

- [ ] **Step 4: Deploy backend in shadow mode**

Keep new Alipay checkout unavailable to general users while production callback and active-query verification are tested. Confirm no secret appears in logs and Creem historical webhooks remain healthy.

- [ ] **Step 5: Cut over channels**

Set production values:

```text
SUBSCRIPTION_PAYMENT_PROVIDER=alipay
CREDIT_PAYMENT_PROVIDER=wechat
CREEM_CHECKOUT_ENABLED=false
```

Deploy the backend through the versioned image workflow, verify `/health`, then push the frontend repository. Confirm the production UI exposes only Alipay monthly subscription and WeChat recharge.

- [ ] **Step 6: Monitor and document rollback**

Monitor checkout conversion, callback backlog, renewal success, duplicate-grant constraints, refund discrepancies, and the Alipay signature limit. If Alipay checkout is unhealthy, disable new agreements while continuing existing agreement callbacks/reconciliation and keeping WeChat recharge available. Do not automatically re-enable Creem.

- [ ] **Step 7: Commit the runbook**

Commit: `git add docs/operations && git commit -m "docs: add Alipay subscription operations runbook"`

---

## Completion Gate

Implementation is complete only when:

- The backend and frontend full suites pass.
- Alipay sandbox lifecycle evidence is recorded.
- New Creem and annual checkout are rejected server-side.
- Existing Creem webhooks and subscriptions remain healthy.
- WeChat one-time Credits purchase still grants exactly once.
- Production credentials and external product approval are present outside Git.
- Production monitoring shows no duplicate periods, duplicate grants, or unresolved callback backlog.

