-- Optimize public billing-list reads.
--
-- Public subscription and credit-package pages filter active, non-deleted rows
-- and sort by display order plus price. These partial indexes also give
-- PostgreSQL a small index to scan for the matching count queries.
-- Run outside an explicit transaction because PostgreSQL CREATE INDEX
-- CONCURRENTLY cannot run inside a transaction block.

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_token_packages_active_sort_price
ON token_packages(sort_order, price, id)
WHERE is_active = true AND deleted_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_subscription_plans_active_sort_price
ON subscription_plans(sort_order, price_monthly, id)
WHERE is_active = true AND deleted_at IS NULL;
