# One-Time Credit Payments Design

## Goal

Replace new automatic subscriptions with one-time Credits packages paid by WeChat Native Pay or Alipay face-to-face QR payment. Every successful purchase creates an independent Credits lot valid for exactly 720 hours. Add a CNY 1.90 trial package containing 500 Credits that each account can successfully purchase only once.

## Scope

This change covers new package sales, payment orders, payment callbacks, Credits grants, expiry, refunds, package administration, price migration, and the customer purchase UI.

Existing Creem and Alipay subscription code, historical subscription records, and provider callbacks remain in the repository for compatibility. New subscription checkout and renewal are not exposed to customers. Historical subscription entitlements remain valid through their recorded periods.

## Product Rules

- All package prices are stored and displayed in CNY.
- Existing package prices are migrated with `new_cny_price = round(old_usd_price * 7.2, 2)`.
- WeChat and Alipay charge the same stored CNY amount.
- Each successful payment creates one independent purchased Credits lot.
- A lot expires exactly 720 hours after the provider-reported successful payment time.
- Credits are consumed from the purchased lot with the earliest expiry first.
- Expiring one lot never changes another lot.
- Historical account balances are not assigned a new expiry and are not cleared by this migration.
- After active purchased lots are consumed, historical subscription grants may be consumed through their existing period rules, followed by legacy non-expiring purchased balance.
- New subscription Credits are no longer sold. Historical subscription Credits follow their existing period rules.

## Trial Package

The system creates or updates a unique trial package with these values:

- Display name: `体验包`
- Price: CNY 1.90
- Credits: 500
- Bonus Credits: 0
- Validity: 720 hours
- Trial flag: true
- Per-account successful-purchase limit: 1
- Featured and sorted before ordinary packages

The limit is shared across WeChat and Alipay. A successful payment permanently creates a `CreditTrialClaim`. Failed, closed, or expired orders do not consume eligibility. Refunds do not restore eligibility.

The trial package remains visible after purchase, but its purchase action is disabled and labeled `已购买`.

## Architecture

### Provider-Neutral Orders

Both payment channels use `CreditPaymentOrder` as the canonical order model. The order snapshots provider, package ID, CNY amount, Credits, package name, and trial status at creation time. Later edits or deletion of a package cannot alter an existing order.

Provider adapters only perform channel-specific operations:

- WeChat Native Pay: create QR order, query transaction, verify/decrypt notification, close, and refund.
- Alipay face-to-face payment: `alipay.trade.precreate`, `alipay.trade.query`, signed asynchronous notification verification, close, and `alipay.trade.refund`.

Both adapters map provider responses into a small internal payment result containing provider transaction ID, merchant order number, paid amount, status, paid time, and verified provider identity.

### Shared Settlement

All asynchronous callbacks and active status queries call one transactional settlement service. The transaction:

1. Locks the canonical payment order.
2. Returns success without changes if the order is already paid.
3. Verifies provider, merchant order number, provider identity, CNY amount, and successful status.
4. Marks the order paid and stores the provider transaction ID and payload.
5. Creates one `PurchasedCreditLot` with a unique constraint on payment order ID.
6. Sets lot expiry to provider paid time plus 720 hours.
7. Creates a trial claim when the order snapshot is a trial package.
8. Writes the recharge record and Credits ledger entry.

This transaction is the only path that grants purchased Credits.

### Trial Concurrency

Trial eligibility is checked before order creation for a useful early error, but settlement enforces the rule authoritatively. A unique database constraint on `CreditTrialClaim.UserID` prevents concurrent callbacks or cross-provider purchases from granting a second trial lot. If another trial payment wins the race, the later paid order is recorded for manual refund and grants no Credits.

### Consumption And Expiry

Active purchased lots are selected with row locking in `expires_at ASC, created_at ASC` order. Consumption updates each lot's remaining Credits atomically. After active lots are exhausted, the system consumes historical subscription grants through their existing period rules, then historical non-expiring purchased balance.

Balance reads exclude expired lots immediately, even if a cleanup job has not run. The cleanup job marks due active lots as expired and writes one expiry ledger record for each lot's discarded remainder. The operation is idempotent.

### Refunds

A successful provider refund marks the order and lot refunded. Only the lot's unspent remaining Credits are removed. Already consumed Credits do not create a negative balance. A refund never deletes the trial claim.

## API Surface

Customer APIs expose:

- Active package list with CNY price, 720-hour validity, trial flag, purchase limit, and current user's trial-purchased state.
- Create payment order for a package and explicit provider (`wechat` or `alipay`).
- Query the authenticated user's canonical order status.
- Balance snapshot including purchased balance, nearest purchased-lot expiry, and trial-purchased state.

Provider callbacks are unauthenticated HTTP endpoints protected by provider signature verification and strict merchant/order/amount validation.

Administrative APIs retain package CRUD and expose CNY currency, fixed 720-hour validity, trial flag, and per-account purchase limit. Only one active trial package is allowed.

## User Interface

The customer payment page becomes a Credits package page. It does not show monthly/yearly switches, automatic renewal, subscription management, cancellation, or Creem checkout.

The header shows available Credits, nearest expiry, and a short `购买后 30 天有效` notice. Package cards display `¥` prices only. Selecting a package opens one payment modal with a WeChat/Alipay segmented channel control. The frontend creates an order only after a channel is selected, shows that channel's QR code, polls every three seconds, and offers a manual `我已完成支付` status check.

After payment, the modal closes, balances refresh, and the UI displays the new lot's expiry. The trial package stays first. Once claimed, it stays visible with a disabled `已购买` action.

The admin package form uses CNY, Credits, bonus Credits, a read-only 720-hour validity, trial status, and purchase limit. Creem and subscription fields remain stored but are hidden from current sales operations.

## Price And Data Migration

The migration is idempotent and records a marker so existing prices are multiplied by 7.2 exactly once. It sets package currency to CNY and disables Creem checkout for packages without deleting Creem identifiers. It upserts the unique trial package.

Existing `WeChatPayment` and recharge records remain immutable historical data. New payment creation uses `CreditPaymentOrder`. Historical subscription grants retain their recorded periods. Historical purchased balances remain non-expiring and are consumed only after active purchased lots and historical subscription grants.

## Error Handling

- The server obtains amount and Credits from the package record; client-supplied values are ignored.
- Inactive or deleted packages reject new orders.
- A claimed trial rejects new trial orders across both providers.
- Invalid signatures, wrong APPID/PID/MchID, wrong amounts, wrong currency, unknown orders, and unsuccessful provider states never grant Credits.
- Duplicate callbacks and repeated queries return the existing paid result without duplicate lots or ledger entries.
- A paid trial order that loses the unique-claim race is flagged for refund and grants no Credits.
- Package edits after order creation cannot change settlement because the order contains immutable snapshots.

## Rollout

1. Run schema and price migration in WSL locally.
2. Test both channels using sandbox or small real payments.
3. Verify create, query, callback, duplicate callback, expiry, refund, amount mismatch, signature failure, and concurrent trial purchase scenarios.
4. Deploy with Alipay package purchase restricted to an internal account first.
5. Verify production callbacks and ledger state, then enable Alipay packages for all users.
6. Keep historical Creem and subscription callbacks active while all new subscription checkout remains disabled.

## Test Requirements

Backend tests cover:

- CNY migration runs exactly once and upserts the trial package.
- WeChat and Alipay create/query/callback flows.
- Signature, provider identity, currency, and amount validation.
- One order grants one lot exactly once.
- Each lot expires at paid time plus exactly 720 hours.
- Multiple lots consume earliest expiry first.
- Expiry removes only the due lot remainder and writes one ledger entry.
- Trial eligibility is shared across providers and safe under concurrent settlement.
- Refund does not restore trial eligibility or create a negative balance.
- Existing non-expiring balance is preserved and consumed after active lots.

Frontend tests cover:

- CNY-only package rendering.
- No automatic-subscription controls or USD conversion.
- WeChat/Alipay channel selection creates only the selected order.
- QR polling, manual status check, successful refresh, and expiry display.
- Claimed trial remains visible and disabled.
- Mobile layout keeps QR code and controls readable without overlap.

## Out Of Scope

- Increasing company registered capital or applying for Alipay merchant deduction.
- New automatic subscriptions through Alipay, WeChat, or Creem.
- Retrospectively expiring historical balances.
- Restoring trial eligibility after refund or account deletion.
- Combining or extending expiry across separate purchases.
