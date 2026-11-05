# Credits Reset Migration Runbook

This runbook resets legacy project/workflow/upload/usage data while preserving user identity, recharge/payment history, and compensated Credits balances.

## Safety Rules

- Run `inspect` and `dry-run` against an RDS clone first.
- Do not run `apply` against production until the clone result is verified.
- Keep the RDS manual backup and the migration archive schema until post-launch support risk is gone.
- Do not commit database passwords or URLs.

## Connection

Use an environment variable outside the repo:

```bash
export CLONE_DATABASE_URL='postgres://USER:PASSWORD@HOST:5432/DBNAME?sslmode=disable'
```

## Inspect

```bash
go run ./cmd/credits-migrate --mode inspect
```

This prints:

- Key table counts.
- Missing profile/account checks.
- Invalid balance checks.
- Recharge/payment consistency checks.
- Balance compensation summary.

## Dry Run

```bash
go run ./cmd/credits-migrate \
  --mode dry-run \
  --token-to-credit-ratio 0.002 \
  --credits-per-cny 0
```

Compensation uses the larger of:

- `legacy_remaining_tokens * token_to_credit_ratio`
- `remaining_paid_value_cny * credits_per_cny`

`remaining_paid_value_cny` uses the larger of the account recharge total and recharge-record total per user, then prorates it by remaining balance.

Current migration policy is `1 credit = 500 legacy tokens`, so `token_to_credit_ratio = 1 / 500 = 0.002`. Compensation is rounded up per user.

## Apply On Clone

Only after confirming the database is a clone:

```bash
go run ./cmd/credits-migrate \
  --mode apply \
  --token-to-credit-ratio 0.002 \
  --credits-per-cny 0 \
  --expected-user-count 5996 \
  --allow-apply \
  --confirm-destroy-project-data \
  --confirm-reset-usage-ledger
```

Apply performs one database transaction:

1. Creates an archive schema like `migration_YYYYMMDD_HHMMSS_credits_reset`.
2. Archives preserved and cleaned tables into that schema.
3. Creates zero-balance accounts for users missing `user_token_accounts`.
4. Creates `user_balance_snapshot`.
5. Rewrites `user_token_accounts` to compensated Credits and resets usage to zero.
6. Truncates project, upload, old usage, and old ledger tables.
7. Creates `credit_ledger_records` if missing.
8. Inserts one migration compensation ledger record per compensated user.

## Verify

Use the archive schema printed during apply:

```bash
go run ./cmd/credits-migrate \
  --mode verify \
  --archive-schema migration_20260510_143432_credits_reset
```

Verification checks:

- Current `users` count matches the snapshot.
- Every user has a `user_token_accounts` row.
- Current `projects`, `upload_records`, and `token_usage_records` are empty.
- Current account Credits total equals snapshot compensation total.
- Current used Credits total is zero.
- Migration ledger row count equals compensated-user count.
- Migration ledger amount equals compensated Credits total.
- Archive schema preserved legacy project/upload/usage rows.

## Current Clone Findings

Last inspected clone showed:

- Users: 5996
- User profiles: 5995
- User token accounts: 5994
- Recharge records: 569
- WeChat payments: 1060
- Projects: 8229
- Upload records: 110881
- Token usage records: 77787
- Existing `credit_ledger_records`: missing
- Existing subscription tables: missing
- Invalid balances: 0
- Legacy remaining tokens: 683320247
- Remaining paid value: about 5882.45 CNY
- Dry-run compensation with `1 credit = 500 legacy tokens`: 1368398 Credits

Clone apply succeeded with archive schema `migration_20260510_143432_credits_reset`.

Post-apply verification:

- Current users: 5996
- Current accounts: 5996
- Current projects: 0
- Current uploads: 0
- Current usage records: 0
- Current account Credits: 1368398
- Current used Credits: 0
- Current migration ledger records: 5993
- Current migration ledger amount: 1368398
- Archive projects: 8229
- Archive uploads: 110881
- Archive usage records: 77787
- Verification status: passed
