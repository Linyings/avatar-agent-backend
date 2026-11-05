# Alipay Subscription and WeChat Recharge Design

## Summary

NarratoAI will use Alipay AI Subscription as the only channel for new recurring subscriptions and WeChat Pay Native as the only channel for one-time Credits purchases. New subscriptions will be monthly only. Existing Creem code, database fields, webhook handling, and historical subscriptions will remain for compatibility, but users will not be able to create new Creem checkouts.

This change does not migrate active Creem subscriptions to Alipay. Existing Creem subscriptions remain valid and continue receiving provider events until they end. Users can start a new Alipay subscription after the prior subscription is no longer active.

## Goals

- Offer monthly subscriptions through Alipay automatic renewal.
- Offer one-time Credits purchases through WeChat Native QR payment.
- Remove annual subscription and visible bank-card checkout entry points.
- Prevent duplicate subscription periods and duplicate Credits grants.
- Preserve Creem implementation and historical data without accepting new Creem orders.
- Support safe rollout, reconciliation, and rollback.

## Non-Goals

- Adding WeChat entrusted recurring debit.
- Supporting annual subscriptions.
- Migrating active Creem agreements into Alipay agreements.
- Deleting Creem code, tables, product mappings, or webhook endpoints.
- Adding Alipay as a one-time Credits payment method in the initial release.

## Channel Boundaries

| Business operation | Active channel | Notes |
| --- | --- | --- |
| New monthly subscription | Alipay AI Subscription | Automatic monthly renewal |
| Subscription management | Alipay hosted customer portal | View, cancel, and manage the agreement |
| One-time Credits purchase | WeChat Pay Native | QR-code payment |
| New annual subscription | Disabled | Hidden in UI and rejected by backend |
| New Creem checkout | Disabled | Code and historical compatibility retained |
| Existing Creem subscription | Creem compatibility path | Readable and updated by historical webhooks |

Backend configuration will make the boundary explicit:

```text
SUBSCRIPTION_PAYMENT_PROVIDER=alipay
CREDIT_PAYMENT_PROVIDER=wechat
CREEM_CHECKOUT_ENABLED=false
```

The backend must enforce these settings. Hiding a frontend button is not sufficient to disable a channel.

## Architecture

Provider-specific callbacks will be normalized into internal subscription events before changing business state:

- `subscription.started`
- `subscription.renewed`
- `subscription.cancelled`
- `subscription.expired`
- `payment.failed`

The normalized event processor owns subscription state transitions, billing-period creation, and Credits grants. Alipay webhook handling, return-page synchronization, and scheduled reconciliation must all call the same idempotent processor.

WeChat payment remains isolated to one-time Credits purchases. A `wechat_payments` record must not represent an automatically renewing subscription.

Creem handlers remain compiled and existing Creem webhooks remain accepted. Checkout creation endpoints reject new requests while `CREEM_CHECKOUT_ENABLED=false`.

## Subscription Lifecycle

### First Subscription

1. The user selects a monthly plan.
2. The backend validates that the plan is active, monthly, and mapped to an Alipay plan and product.
3. The backend rejects the request if the user already has an active, trialing, scheduled-cancel, or past-due subscription.
4. The backend creates a pending Alipay subscription session with a unique request ID.
5. The frontend redirects the user to Alipay's hosted agreement and first-payment flow.
6. Alipay sends an asynchronous notification after agreement authorization and first payment.
7. The backend verifies the notification signature and queries Alipay to confirm agreement status, transaction status, amount, currency, and product mapping.
8. In one database transaction, the backend activates the subscription, creates the first billing period, and grants the plan's monthly Credits.

### Monthly Renewal

1. Alipay performs the recurring charge according to the agreement.
2. A successful renewal is normalized to `subscription.renewed`.
3. The event processor creates the new billing period and grants that period's Credits.
4. The uniqueness key is based on provider, provider agreement or subscription ID, and billing-period identity.
5. Repeated, delayed, or reordered notifications cannot create another period or grant.

Subscription Credits expire at the end of their corresponding billing period. Existing account rules continue to prefer subscription Credits where applicable.

### Cancellation and Expiry

- Cancellation disables future renewal but does not terminate the paid period immediately.
- A cancelled active subscription becomes `scheduled_cancel` and remains usable through `current_period_end`.
- It becomes `expired` when the period ends without a successful renewal.
- The user can manage or cancel through the Alipay hosted portal.
- A user cannot hold concurrent active Creem and Alipay subscriptions.

### Failed Renewal

- A failed renewal changes the subscription to `past_due`.
- The system applies a three-day grace period.
- No new-period Credits are granted until payment succeeds.
- A successful retry restores `active`, creates the period, and grants Credits once.
- Failure after the grace period changes the subscription to `expired`.
- A scheduled reconciliation job queries unresolved agreements and payments to recover from missing callbacks.

### Refunds and Disputes

- A full refund revokes unused Credits granted for the refunded billing period.
- Consumed Credits do not produce a negative user balance.
- Any unrecoverable consumed amount is recorded as a finance discrepancy for administrator review.
- Refund, callback, synchronization, and reconciliation paths share the same idempotency rules.

## Data Model

### Subscription Plans

Extend `subscription_plans` with:

- Alipay monthly plan ID.
- Alipay product ID.
- Provider-channel enablement state.

Keep `creem_product_id_monthly` and `creem_product_id_yearly` unchanged for historical compatibility. Annual pricing may remain stored but is not publicly purchasable.

### User Subscriptions

Extend `user_subscriptions` with:

- `payment_provider`.
- Alipay agreement ID.
- Alipay subscription or membership ID where supplied.
- Current billing-period start and end.
- Next charge time.
- Cancellation and grace-period timestamps.
- Last provider synchronization time and failure reason.

Existing Creem identifiers remain valid. Provider-specific identifiers must use unique indexes where the provider contract guarantees uniqueness.

### Alipay Sessions and Events

Create an `alipay_subscription_sessions` table containing the user, plan, request ID, hosted redirect information, provider identifiers, status, and timestamps.

Create a provider-neutral `payment_webhook_events` table, or equivalently extend an existing event store, containing:

- Provider and provider event ID.
- Event type and provider object identifiers.
- Signature-verification result.
- Processing status, attempt count, and last error.
- Sanitized raw payload for audit and replay.

The raw payload must not contain private keys, authorization tokens, or complete sensitive payment credentials.

### Credits Idempotency

Continue using the Credits ledger. Each subscription grant must have a unique source key derived from the payment provider, provider agreement or subscription ID, and billing-period identity. Database uniqueness, not an in-memory check, is the final duplicate-grant protection.

## API Design

Add or adapt these authenticated subscription endpoints:

```text
POST /api/v1/subscriptions/alipay/checkout
POST /api/v1/subscriptions/alipay/sync
POST /api/v1/subscriptions/alipay/portal
POST /api/v1/subscriptions/alipay/cancel
GET  /api/v1/subscriptions/current
```

Add public provider endpoints with provider verification:

```text
POST /api/v1/subscriptions/alipay/webhook
GET  /api/v1/subscriptions/alipay/return
```

Keep the one-time Credits payment boundary:

```text
POST /api/v1/credit-packages/wechat/create
GET  /api/v1/credit-packages/wechat/:order/status
POST /api/v1/credit-packages/wechat/webhook
```

Existing Creem checkout creation endpoints return a channel-disabled response when new checkout is disabled. Existing Creem webhook endpoints remain operational.

All amounts and product mappings come from server-side plan or package records. The backend ignores client-supplied prices.

## Frontend Design

### Subscription Page

- Remove the monthly/annual selector and show monthly pricing only.
- Use `Alipay automatic renewal` as the primary subscription action.
- Clearly show the billing interval, renewal rule, and ability to cancel.
- Send active subscribers to the Alipay hosted management portal.
- Display pending confirmation, success, cancellation, and failure states after returning from Alipay.
- Do not show bank-card or Creem checkout actions.

### Credits Purchase

- Show WeChat QR payment as the only one-time Credits purchase action.
- Retain payment polling and successful-credit refresh behavior.
- Remove visible one-time Creem actions.

### Administration

- Add Alipay plan and product mappings to subscription-plan management.
- Retain Creem mappings and label them as disabled compatibility settings.
- Display provider, agreement status, billing period, next charge, grace period, and failure details for subscriptions.
- Provide reconciliation visibility across provider events, transactions, billing periods, and Credits grants.

## Security and Compliance

- Store the Alipay application private key only in backend environment secrets or a managed secret store.
- Verify every Alipay callback signature and actively query Alipay before granting entitlements.
- Compare confirmed amount, currency, merchant identity, application identity, and product mapping with server-side records.
- Process the provider event, subscription state, billing period, and Credits grant transactionally.
- Protect webhook and ledger tables with unique constraints.
- Publish automatic-renewal terms, privacy policy, refund rules, cancellation instructions, and customer-support contact before production review.
- Do not log private keys, access tokens, agreement tokens, or full sensitive credentials.

## Alipay Prerequisites

Before production development can be fully validated, the business must:

1. Use an Alipay enterprise account.
2. Apply for the Alipay AI Subscription solution.
3. Confirm an eligible merchant category, such as software development service, internet data service, other online application, or online tool.
4. Apply for the necessary merchant debit and web payment products.
5. Configure monthly products, callback domains, hosted portal, agreement text, and customer-service details.
6. Obtain production application, product, plan, and signing configuration.

The initial Alipay AI Subscription allowance is limited to 1,000 newly signed subscription users unless expansion is approved. Operations must monitor this limit and apply for expansion before reaching it.

## Testing

Backend automated coverage must include:

- Checkout creation and server-side price validation.
- First payment, renewal, cancellation, expiration, and failed renewal.
- Invalid signature, wrong merchant or application, amount mismatch, and unknown agreement.
- Duplicate and reordered callbacks.
- Missing callback followed by successful synchronization or reconciliation.
- Exactly one Credits grant for each paid billing period.
- Rejection of concurrent active subscriptions.
- Disabled annual and Creem checkout creation.
- Existing Creem subscription compatibility.
- Existing WeChat one-time payment creation, status polling, callback handling, and Credits grant.

Frontend tests must verify that users see only monthly Alipay subscription and one-time WeChat recharge, and that no annual or bank-card checkout is visible.

The release requires an Alipay sandbox end-to-end run covering agreement, first payment, renewal simulation, cancellation, refund, and reconciliation before enabling production checkout.

## Rollout

### Phase 1: Qualification

- Apply for Alipay AI Subscription and required products.
- Configure merchant category, products, portal, domains, agreements, policies, and support channel.
- Obtain sandbox and production credentials without storing them in the repository.

### Phase 2: Shadow Integration

- Deploy additive database migrations and Alipay integration while Creem remains the active new-subscription provider.
- Complete sandbox flows and production test events without exposing Alipay to general users.
- Reconcile agreement, transaction, billing-period, and Credits ledger records.

### Phase 3: Cutover

- Set `SUBSCRIPTION_PAYMENT_PROVIDER=alipay`.
- Set `CREDIT_PAYMENT_PROVIDER=wechat`.
- Set `CREEM_CHECKOUT_ENABLED=false`.
- Remove annual and bank-card actions from the production frontend.
- Continue accepting historical Creem webhooks.
- Monitor checkout conversion, renewal success, callback backlog, duplicate-grant prevention, refund handling, and reconciliation discrepancies.

## Rollback

- Disable new Alipay agreements without terminating existing Alipay subscriptions.
- Keep WeChat one-time recharge available as the temporary payment path.
- Do not automatically expose Creem when Alipay is unavailable.
- Use additive migrations only; do not remove Creem fields or tables.
- Preserve all processed Alipay billing periods and Credits ledger entries across application rollback.
- Continue processing or reconciling existing Alipay agreements even while new checkout is disabled.

## Acceptance Criteria

- New users see only Alipay monthly automatic renewal and WeChat one-time recharge.
- Annual and bank-card checkout controls are absent.
- A confirmed first Alipay payment activates one subscription and grants one monthly Credits allocation.
- Each confirmed renewal creates exactly one billing period and one Credits grant.
- Cancellation keeps the current paid period active and prevents future renewal.
- Failed renewal enters the three-day grace period without granting new Credits.
- New Creem checkout creation is disabled while historical Creem subscriptions remain readable and updateable.
- Duplicate, delayed, reordered, or replayed provider events never duplicate subscription periods or Credits.

