-- Optimize authenticated project-list reads.
--
-- The hot list path filters active rows by owner_id and sorts by updated_at,
-- with optional status filtering and secondary sort modes exposed by the UI.
-- Run outside an explicit transaction because PostgreSQL CREATE INDEX
-- CONCURRENTLY cannot run inside a transaction block.

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_projects_owner_active_updated_at
ON projects(owner_id, updated_at DESC)
WHERE deleted_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_projects_owner_status_active_updated_at
ON projects(owner_id, status, updated_at DESC)
WHERE deleted_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_projects_owner_active_created_at
ON projects(owner_id, created_at DESC)
WHERE deleted_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_projects_owner_active_name
ON projects(owner_id, name)
WHERE deleted_at IS NULL;
