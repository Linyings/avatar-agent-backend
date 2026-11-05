# One-Time Credit Payments Design

## Summary

NarratoAI will stop selling new automatically renewing subscriptions. Users will buy one-time Credit packages through either WeChat Native QR payment or Alipay face-to-face QR payment. Every successful purchase creates an independent Credit lot valid for exactly 30 times 24 hours from the payment success timestamp. Consumption uses the lot that expires first.

Existing Creem and historical subscription records remain supported until they expire or are cancelled, but the product will expose no new subscription checkout. Creem integration code and webhooks remain for compatibility.

## Product Rules

- All package prices are stored, displayed, and charged in CNY.
- Existing package prices migrate once from their stored USD value to CNY using the existing fixed WeChat conversion rate of `7.2` and round to two decimal places.
- No runtime USD-to-CNY conversion remains after migration.
- WeChat and Alipay charge the same stored CNY package price.
- Each successful payment produces an independent Credit lot with an expiry of `paid_at + 720 hours`.
- Credit consumption is earliest-expiry-first.
- Expired Credits cannot be queried as available or consumed, even if the scheduled expiry worker is delayed.

### Trial Package

- Price: `CNY 1.90`.
- Credit amount: `500`.
- One successful purchase per account for the lifetime of the account.
- WeChat and Alipay share the same limit.
- Failed, closed, and unpaid orders do not consume eligibility.
- A successful payment permanently consumes eligibility, including after a refund.
- Concurrent pending trial orders are restricted, and only the first successfully settled order may grant Credits.

## Data Model

### Credit Package

Extend the existing package model with provider-neutral sales rules:

- Currency is fixed to `CNY` for active one-time packages.
- `validity_hours` stores `720`.
- `is_trial` identifies the trial package.
- `per_account_purchase_limit` stores `1` for the trial package and is null for unlimited packages.

Legacy Creem product fields remain in storage but are not exposed as active administration controls.

### Payment Order

Use a provider-neutral payment order as the authoritative purchase record. It records:

- user and package identifiers;
- provider (`wechat` or `alipay`);
- merchant order number and provider transaction number;
- immutable CNY amount and Credit quantity snapshots;
- status (`pending`, `paid`, `closed`, `failed`, or `refunded`);
- payment, expiry, and refund timestamps;
- an idempotent settlement marker.

The frontend submits only the package ID and provider. The backend reads the package price and quantity and creates the snapshots.

### Credit Lot

Each settled order creates one Credit lot containing:

- `total_credits`;
- `used_credits` or equivalent remaining balance representation;
- `paid_at`;
- `expires_at`;
- payment order linkage;
- state (`active`, `depleted`, `expired`, or `refunded`).

The payment order linkage is unique so duplicate callbacks cannot create duplicate lots.

## Payment Architecture

Payment-specific providers own only QR order creation, provider-side query, callback parsing, and signature verification. A shared settlement service owns validation, idempotency, trial eligibility, lot creation, refunds, and ledger entries.

### WeChat

Continue using the existing WeChat Native QR flow. Remove USD conversion and send the stored CNY price directly in fen.

### Alipay

Use ordinary face-to-face payment through `alipay.trade.precreate`; do not use agreement signing or automatic deduction APIs. The backend generates the QR code value, verifies RSA2 asynchronous notifications, and queries provider status when requested by the shared order status endpoint.

Alipay settlement validates the merchant order number, APPID, merchant PID, CNY amount, and successful trade state against the local order before granting Credits.

### Shared State Flow

1. The authenticated user selects a package and provider.
2. The backend validates package availability and trial eligibility.
3. The backend creates a pending immutable order and requests a provider QR code.
4. The frontend displays the QR code and polls the shared order status endpoint.
5. A verified provider callback or trusted provider query invokes settlement.
6. Settlement locks the user trial eligibility and order, validates all snapshots, marks the order paid, creates one expiring Credit lot, and writes the ledger entry in one transaction.
7. Duplicate callbacks return success without granting Credits again.

Pending QR orders expire after 30 minutes. An unpaid expired order does not consume trial eligibility. At most one pending trial order per user is allowed. If provider timing creates multiple successful trial payments, only the first order grants Credits; remaining payments are recorded for operator refund handling and cannot grant Credits.

## Balance, Consumption, and Expiry

Balance queries sum only non-refunded lots with remaining Credits and `expires_at > now`. They also return the next-expiring amount and timestamp for the frontend.

Consumption transactions select eligible lots ordered by `expires_at`, then creation order, lock them, and deduct until the requested amount is satisfied. Insufficient unexpired balance rejects the whole operation.

A scheduled worker marks elapsed active lots as expired and writes one idempotent expiry ledger entry containing the cleared remainder. Correctness does not depend on worker timing because queries and consumption always filter by expiry in real time.

## Refund Rules

- For an unused lot, refund settlement revokes the full remainder.
- For a partially used lot, it revokes only that lot's remainder.
- For a depleted lot, it records the refund and unrecoverable consumed quantity but does not deduct another lot or create a negative balance.
- Refund handling is idempotent and tied to the original payment order.
- A refunded trial purchase remains in successful purchase history and does not restore eligibility.

## User Experience

The payment page becomes a Credit store and removes monthly, annual, automatic renewal, and agreement-signing controls and copy.

It shows:

- current available Credits;
- the amount and timestamp of the next expiry;
- the trial package and active standard packages;
- CNY prices with the `¥` symbol;
- one purchase command per package.

The purchase modal first selects WeChat or Alipay, then displays the selected provider's QR code and payment status. After settlement it refreshes balance, expiry information, and trial eligibility.

The trial card displays the lifetime one-purchase rule. Once purchased it shows an already-purchased state and disables checkout.

## Administration

The existing Credit package administration remains the source of package configuration. It exposes CNY price, Credit quantity, 720-hour validity, trial status, and per-account purchase limit. It no longer exposes USD conversion or active Creem product configuration. Legacy fields remain available to compatibility code.

The migration creates or updates exactly one trial package at `CNY 1.90 / 500 Credits` and migrates existing active package prices exactly once. A durable migration marker prevents repeated multiplication by `7.2`.

## Historical Subscriptions

- New Creem and Alipay subscription checkouts are disabled and removed from the user interface.
- Existing subscription entitlement records continue to affect users until their current rules end them.
- Existing Creem webhooks, reconciliation, cancellation, refund, and entitlement logic remain active for historical records.
- One-time Credit purchases and historical subscription Credits remain separate lots and follow their original ownership and expiry rules.

## Error Handling and Security

- Never trust frontend-supplied price, currency, or Credit quantity.
- Use database uniqueness and row locks for settlement and trial eligibility, not process-local locks.
- Verify WeChat and Alipay callback signatures before parsing them as trusted payment events.
- Reject callbacks with mismatched merchant identity, APPID, amount, currency, order identity, or trade state.
- Store signed but unmappable callbacks for operational review without granting Credits.
- Return provider-required callback acknowledgements only after durable processing.
- Do not log private keys, raw signing material, or full sensitive callback payloads.

## Testing and Release Gates

Automated coverage must include:

- WeChat and Alipay QR creation and provider query;
- Alipay RSA2 signature verification and identity/amount mismatch rejection;
- duplicate callback settlement idempotency for both providers;
- concurrent cross-provider trial settlement allowing one grant only;
- unpaid, failed, and closed orders preserving trial eligibility;
- refunds not restoring trial eligibility;
- independent 720-hour expiry for multiple lots;
- earliest-expiry-first consumption;
- real-time exclusion and scheduled idempotent clearing of expired remainders;
- one-time CNY migration and exact two-decimal values;
- historical subscription compatibility with new checkout disabled;
- frontend CNY-only display and WeChat/Alipay QR paths.

Before production deployment, run the full backend and frontend suites, build the frontend, and deploy both services in WSL. Complete sandbox or low-value live payment tests for both providers, callback replay, expiry simulation, refund behavior, and trial concurrency. Production deployment occurs only after this local payment and ledger loop passes.

## External Prerequisites

- The enterprise Alipay application must have ordinary face-to-face payment permission for `alipay.trade.precreate`.
- The configured Alipay APPID, PID, application private key, and Alipay public key must belong to the same enterprise application and merchant.
- Public HTTPS callback URLs must be configured for both payment providers before live end-to-end testing.
