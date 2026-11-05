# Alipay Automatic Subscription Operations

The application sells monthly automatic subscriptions through Alipay and one-time credit packages through WeChat Pay. Creem webhook and historical subscription handling remain available for existing records, while new Creem checkout is disabled by default.

## State While Product Approval Is Pending

- Keep `CREEM_CHECKOUT_ENABLED=false`.
- Keep every plan's `subscription_provider_enabled` value false until its Alipay product is approved and mapped.
- Alipay credentials may be installed and callbacks may be reachable, but the API deliberately refuses to create or synchronize agreements until the approved API method and response field contract are wired.
- WeChat one-time credit purchases remain available independently.

This state prevents accidental agreement creation while allowing plan administration, callback storage, event idempotency, entitlement logic, and the frontend channel switch to be deployed and tested.

## Credential Files

Never commit private keys. Put the files on the deployment host under the directory selected by `ALIPAY_CERT_HOST_DIR` (default `./data/alipay_certs`):

```text
data/alipay_certs/app_private_key.pem
data/alipay_certs/alipay_public_key.pem
```

Docker Compose mounts that directory read-only at `/app/data/alipay_certs`. Configure:

```dotenv
ALIPAY_APP_ID=
ALIPAY_MERCHANT_ID=
ALIPAY_PRIVATE_KEY_FILE=/app/data/alipay_certs/app_private_key.pem
ALIPAY_PUBLIC_KEY_FILE=/app/data/alipay_certs/alipay_public_key.pem
ALIPAY_GATEWAY_URL=https://openapi.alipay.com/gateway.do
ALIPAY_NOTIFY_URL=https://api.example.com/api/v1/billing/alipay/webhook
ALIPAY_RETURN_URL=https://app.example.com/subscription
ALIPAY_PORTAL_URL=
CREEM_CHECKOUT_ENABLED=false
```

`ALIPAY_MERCHANT_ID` is the merchant PID, not the application ID.

## After Approval

Before enabling a plan:

1. Obtain the approved Alipay product ID and plan ID.
2. Obtain the official AI subscription API method names, request fields, response fields, callback event names, and sandbox credentials from the approved product documentation.
3. Populate `providers.AlipayMethods` in `internal/server/server.go` and map the official response/callback fields in the provider and subscription handlers. Do not guess these values.
4. In subscription plan administration, enter `alipay_product_id` and `alipay_plan_id`, then enable `subscription_provider_enabled` for that plan.
5. Verify create, return sync, renewal, payment failure and grace period, cancellation, expiry, refund, duplicate callback, and invalid signature scenarios in the sandbox.

Only enable production agreement creation after all sandbox cases pass. Existing callback endpoints must remain active during a rollback so delayed renewals, cancellations, and refunds continue to reconcile.

## Rollback

Disable `subscription_provider_enabled` on all plans to stop new Alipay agreements. Do not disable the webhook or event processor. Leave WeChat credit purchase enabled, and keep Creem webhook processing enabled for historical subscriptions while `CREEM_CHECKOUT_ENABLED=false` prevents new Creem checkouts.
