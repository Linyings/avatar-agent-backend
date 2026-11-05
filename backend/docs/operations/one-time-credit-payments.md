# One-Time Credit Payments Runbook

This runbook covers one-time CNY Credit packages paid by WeChat Pay Native QR
or Alipay face-to-face QR. It does not authorize a production release, payment
charge, provider credential change, image publish, tag, push, or server access.

## Configuration And Secrets

The tracked `env.example` documents non-secret names and paths. Copy it to the
ignored `.env` file and set deployment-specific URLs there. Keep merchant
secrets, private keys, provider certificates, and acceptance logs out of Git.
The repository ignores `data/`, including the expected certificate directories.

```dotenv
CREDIT_PAYMENT_PROVIDER=one_time
ALIPAY_APP_ID=
ALIPAY_MERCHANT_ID=
ALIPAY_PRIVATE_KEY_FILE=data/alipay_certs/app_private_key.pem
ALIPAY_PUBLIC_KEY_FILE=data/alipay_certs/alipay_public_key.pem
ALIPAY_NOTIFY_URL=https://api.example.com/api/v1/credit-payments/alipay/webhook
WECHAT_NOTIFY_URL=https://api.example.com/api/v1/credit-payments/wechat/webhook
```

`ALIPAY_MERCHANT_ID` is the seller PID. The running backend reads
`ALIPAY_MERCHANT_ID`, `ALIPAY_PRIVATE_KEY_FILE`, `ALIPAY_PUBLIC_KEY_FILE`,
`ALIPAY_NOTIFY_URL`, and `WECHAT_NOTIFY_URL`; do not substitute undocumented
`*_SELLER_ID`, `*_KEY_PATH`, or `*_CREDIT_NOTIFY_URL` names. Under Docker
Compose, the host directory selected by `ALIPAY_CERT_HOST_DIR` is mounted
read-only at `/app/data/alipay_certs`; use container-visible paths if an
absolute path is configured.

Before enabling an Alipay package, confirm that the enterprise application has
ordinary face-to-face payment permission for `alipay.trade.precreate`. Configure
the provider callbacks to the two routes above. Both callback routes are public
because providers cannot send an application bearer token.

## Operational Queries

Use a read-only database role for investigation. Replace the psql variables
locally; do not place real IDs, order numbers, transaction IDs, or customer data
in tickets or committed files.

```sql
-- psql: \set out_trade_no 'CR...'
SELECT id, out_trade_no, provider, provider_transaction_id, status,
       amount_cny, credits, package_name, is_trial, paid_at, expires_at,
       refunded_at, created_at, updated_at
FROM credit_payment_orders
WHERE out_trade_no = :'out_trade_no';

-- The purchase lot is unique per payment order and holds the spendable remainder.
SELECT o.out_trade_no, l.id AS lot_id, l.granted_credits, l.remaining_credits,
       l.status, l.expires_at, l.refunded_at
FROM purchased_credit_lots l
JOIN credit_payment_orders o ON o.id = l.payment_order_id
WHERE o.out_trade_no = :'out_trade_no';

-- Trial ownership is permanent, including after a refund.
SELECT c.user_id, c.payment_order_id, o.out_trade_no, o.provider,
       o.status, o.paid_at, c.created_at
FROM credit_trial_claims c
JOIN credit_payment_orders o ON o.id = c.payment_order_id
WHERE c.user_id = :'user_id';

-- Ledger records created on successful settlement reference the order ID.
SELECT id, event_type, item, change_amount, balance_after, reason,
       recorded_at, request_metadata
FROM credit_ledger_records
WHERE user_id = :'user_id'
  AND request_metadata ->> 'payment_order_id' = :'payment_order_id'
ORDER BY recorded_at, created_at;
```

Expected lifecycle: `pending` order, then one `paid` order and one active lot.
Duplicate provider notifications or repeated status polling must preserve the
same lot. A successful payment has a `paid_at` and a lot expiry exactly 720
hours later. Consumption spends the earliest expiring active lot first. At or
after expiry, only that lot's remaining Credits become zero and its status is
`expired`.

## Refund Procedure

1. Identify the order and its lot with the queries above. Confirm the provider,
   transaction ID, amount, and remaining Credits.
2. Obtain the required administrator session and call
   `POST /api/v1/credit-payments/{out_trade_no}/refund`.
3. The endpoint calls the original provider before changing local state. Do not
   update `credit_payment_orders` or `purchased_credit_lots` directly.
4. Re-run the order and lot queries. The order must be `refunded`; only the
   unconsumed remainder is revoked and the lot is `refunded`. A repeat request
   is idempotent and returns zero revoked Credits.
5. Keep the `credit_trial_claims` row. A refund does not make a trial package
   claimable again.

If the provider rejects the refund, the API returns an error and the local order
and lot must remain unchanged. Escalate with the provider transaction ID through
the approved support channel; never include key material or full callback bodies.

## Callback Troubleshooting

1. Verify each provider reaches the public HTTPS callback URL and that its
   configured application ID and merchant PID match the backend settings.
2. Inspect API logs by `out_trade_no`; redact request bodies and signatures.
   Confirm that an Alipay notification has a valid RSA2 signature, matching
   `app_id`, matching `seller_id`, matching amount, and a `TRADE_SUCCESS` or
   `TRADE_FINISHED` state. Confirm that a WeChat notification signature decrypts
   and its application ID, merchant ID, amount, and `SUCCESS` state match.
3. Query the order, lot, trial claim, and ledger. A valid paid callback commits
   them together. A duplicate notification must not create a second lot.
4. For a pending order, use the authenticated order status route
   `GET /api/v1/credit-payments/{out_trade_no}`. It performs a provider query
   when that provider is configured.
5. Do not manually reply `success` to a failed callback or manufacture a paid
   database row. Correct the configuration or provider state, then let the
   provider retry or use its approved replay tool in sandbox.

## Rollback

To stop new purchases, deactivate all one-time CNY Credit packages in the
administrator package-management flow. This causes new order creation to reject
the package while preserving the payment order, refund, and callback routes.
Do not remove callback URLs, keys, or the callback handlers during rollback:
delayed paid events, provider queries, and refunds still need reconciliation.

Record the package IDs disabled, operator, UTC time, change request, and the
open pending-order count in an ignored operational log. Re-enable packages only
after investigating the incident and completing the acceptance gate below.

## Local Acceptance Gate

Create an ignored local log such as
`data/acceptance/one-time-credit-payments-YYYYMMDD.md`. For every case, record
the order number, provider transaction ID, observed UTC timestamps, and row
counts for orders, lots, trial claims, and ledger records. Do not commit the log
or customer/payment secrets.

Use provider sandbox credentials or a temporary CNY 0.01 internal package, then
restore the configured package price. The gate requires:

- WeChat: create, QR, paid callback, duplicate callback, exactly one lot.
- Alipay: precreate, QR, paid callback or query, duplicate query, exactly one lot.
- Negative callbacks: invalid signature, wrong merchant, wrong amount, and
  failed status leave zero lots.
- Trial concurrency across providers: one lot and one `refund_required` order.
- Expiry at `paid_at + 720h`: only the due remainder expires, with one ledger
  event for the expiry operation.
- Refund after partial consumption: only the remainder is revoked and the trial
  remains claimed.
- UI: CNY only, a claimed trial is disabled, and the QR remains usable at 360px.

No real charge or phone scan is part of automated local verification. Those
channel end-to-end cases remain explicit manual acceptance items until sandbox
or an approved temporary internal package is available.

## Release Gate

Before asking for separate production deployment authorization, run the complete
backend and frontend suites, frontend lint and build, start the WSL services
with a Windows-side `wsl.exe ... sleep infinity` keepalive, wait 60-90 seconds,
and verify HTTP 200 from `http://127.0.0.1:8000/health` and
`http://127.0.0.1:8080/`. Keep the production deployment as a separate,
explicitly authorized operation.
