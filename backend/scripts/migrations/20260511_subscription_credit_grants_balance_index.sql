-- Optimize balance and usage-stat subscription credit aggregation.
--
-- The balance path sums active, unexpired grants for a user. INCLUDE keeps the
-- aggregate columns in the PostgreSQL index so the planner can use an
-- index-only scan when visibility permits.
-- Run outside an explicit transaction because PostgreSQL CREATE INDEX
-- CONCURRENTLY cannot run inside a transaction block.

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_subscription_credit_grants_user_active_expiry
ON subscription_credit_grants(user_id, expires_at)
INCLUDE (credit_amount, used_amount)
WHERE status = 'active' AND deleted_at IS NULL;
