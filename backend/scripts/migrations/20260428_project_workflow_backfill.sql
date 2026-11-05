-- Idempotent project workflow backfill for the expand phase.
--
-- Run after 20260428_project_workflow_expand.sql and before cutting reads over
-- to the new workflow tables. The script is intentionally additive/idempotent:
-- it inserts only when a project has no active row for that step. Re-running it
-- is safe and reports remaining legacy-only rows at the end.
--
-- Rollback posture:
--   * No existing project columns are dropped or rewritten except nullable
--     workflow_migrated_at/workflow_schema_version metadata.
--   * To roll back the read path, point the app back at projects.* JSON caches.
--   * To undo only this backfill, clear rows created by this script by
--     matching cleared_at IS NULL and cleared_reason IS NULL after confirming no
--     new writes occurred, or mark them cleared instead of deleting.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

WITH legacy_video AS (
  SELECT
    p.id AS project_id,
    COALESCE(NULLIF(p.video_url, ''), 'legacy-video-source') AS original_filename,
    NULLIF(p.video_url, '') AS video_url,
    jsonb_strip_nulls(jsonb_build_object(
      'video_url', p.video_url,
      'video_context', COALESCE(p.video_context::jsonb, '{}'::jsonb)
    )) AS snapshot
  FROM projects p
  WHERE p.deleted_at IS NULL
    AND (
      NULLIF(p.video_url, '') IS NOT NULL
      OR COALESCE(p.video_context::jsonb, '{}'::jsonb) <> '{}'::jsonb
    )
    AND NOT EXISTS (
      SELECT 1 FROM project_video_sources s
      WHERE s.project_id = p.id AND s.deleted_at IS NULL AND s.cleared_at IS NULL
    )
)
INSERT INTO project_video_sources (
  project_id, original_filename, video_url, frontend_params, raw_snapshot,
  input_hash, dependency_hash, content_hash, status, workflow_revision
)
SELECT
  project_id,
  original_filename,
  video_url,
  snapshot,
  snapshot,
  encode(digest(snapshot::text, 'sha256'), 'hex'),
  'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
  encode(digest(snapshot::text, 'sha256'), 'hex'),
  'active',
  1
FROM legacy_video;

WITH legacy_subtitles AS (
  SELECT p.id AS project_id, COALESCE(p.subtitle::jsonb, '{}'::jsonb) AS snapshot
  FROM projects p
  WHERE p.deleted_at IS NULL
    AND COALESCE(p.subtitle::jsonb, '{}'::jsonb) <> '{}'::jsonb
    AND NOT EXISTS (
      SELECT 1 FROM project_subtitles s
      WHERE s.project_id = p.id AND s.deleted_at IS NULL AND s.cleared_at IS NULL
    )
), video_hashes AS (
  SELECT DISTINCT ON (project_id) project_id, content_hash
  FROM project_video_sources
  WHERE deleted_at IS NULL AND cleared_at IS NULL
  ORDER BY project_id, workflow_revision DESC, updated_at DESC
)
INSERT INTO project_subtitles (
  project_id, content, raw_result, raw_snapshot,
  input_hash, dependency_hash, content_hash, status, workflow_revision
)
SELECT
  s.project_id,
  s.snapshot,
  s.snapshot,
  s.snapshot,
  encode(digest(s.snapshot::text, 'sha256'), 'hex'),
  COALESCE(v.content_hash, 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'),
  encode(digest(s.snapshot::text, 'sha256'), 'hex'),
  'active',
  1
FROM legacy_subtitles s
LEFT JOIN video_hashes v ON v.project_id = s.project_id;

WITH legacy_configs AS (
  SELECT p.id AS project_id, COALESCE(p.video_config::jsonb, '{}'::jsonb) AS snapshot
  FROM projects p
  WHERE p.deleted_at IS NULL
    AND COALESCE(p.video_config::jsonb, '{}'::jsonb) <> '{}'::jsonb
    AND NOT EXISTS (
      SELECT 1 FROM project_generation_configs s
      WHERE s.project_id = p.id AND s.deleted_at IS NULL AND s.cleared_at IS NULL
    )
), subtitle_hashes AS (
  SELECT DISTINCT ON (project_id) project_id, content_hash
  FROM project_subtitles
  WHERE deleted_at IS NULL AND cleared_at IS NULL
  ORDER BY project_id, workflow_revision DESC, updated_at DESC
)
INSERT INTO project_generation_configs (
  project_id, raw_params, raw_snapshot,
  style, language, clip_mode, original_ratio, narration_ratio, voice_provider, voice_id,
  input_hash, dependency_hash, params_hash, status, workflow_revision
)
SELECT
  c.project_id,
  c.snapshot,
  c.snapshot,
  NULLIF(c.snapshot->>'style', ''),
  NULLIF(c.snapshot->>'language', ''),
  NULLIF(c.snapshot->>'clip_mode', ''),
  NULLIF(c.snapshot->>'original_ratio', ''),
  NULLIF(c.snapshot->>'narration_ratio', ''),
  NULLIF(c.snapshot->>'voice_provider', ''),
  NULLIF(c.snapshot->>'voice_id', ''),
  encode(digest(c.snapshot::text, 'sha256'), 'hex'),
  COALESCE(s.content_hash, 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'),
  encode(digest(c.snapshot::text, 'sha256'), 'hex'),
  'active',
  1
FROM legacy_configs c
LEFT JOIN subtitle_hashes s ON s.project_id = c.project_id;

WITH legacy_scripts AS (
  SELECT p.id AS project_id, COALESCE(p.ai_analysis_script::jsonb, '{}'::jsonb) AS snapshot
  FROM projects p
  WHERE p.deleted_at IS NULL
    AND COALESCE(p.ai_analysis_script::jsonb, '{}'::jsonb) <> '{}'::jsonb
    AND NOT EXISTS (
      SELECT 1 FROM project_scripts s
      WHERE s.project_id = p.id AND s.deleted_at IS NULL AND s.cleared_at IS NULL
    )
), config_hashes AS (
  SELECT DISTINCT ON (project_id) project_id, params_hash
  FROM project_generation_configs
  WHERE deleted_at IS NULL AND cleared_at IS NULL
  ORDER BY project_id, workflow_revision DESC, updated_at DESC
)
INSERT INTO project_scripts (
  project_id, script_content, raw_result, raw_snapshot,
  model_name, input_hash, dependency_hash, script_hash, status, workflow_revision
)
SELECT
  s.project_id,
  s.snapshot,
  s.snapshot,
  s.snapshot,
  NULLIF(s.snapshot->>'model_name', ''),
  encode(digest(s.snapshot::text, 'sha256'), 'hex'),
  COALESCE(c.params_hash, 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'),
  encode(digest(s.snapshot::text, 'sha256'), 'hex'),
  'active',
  1
FROM legacy_scripts s
LEFT JOIN config_hashes c ON c.project_id = s.project_id;

WITH legacy_voiceovers AS (
  SELECT
    p.id AS project_id,
    jsonb_strip_nulls(jsonb_build_object('audio_url', p.audio_url)) AS snapshot,
    NULLIF(p.audio_url, '') AS audio_url
  FROM projects p
  WHERE p.deleted_at IS NULL
    AND NULLIF(p.audio_url, '') IS NOT NULL
    AND NOT EXISTS (
      SELECT 1 FROM project_voiceovers s
      WHERE s.project_id = p.id AND s.deleted_at IS NULL AND s.cleared_at IS NULL
    )
), script_hashes AS (
  SELECT DISTINCT ON (project_id) project_id, script_hash
  FROM project_scripts
  WHERE deleted_at IS NULL AND cleared_at IS NULL
  ORDER BY project_id, workflow_revision DESC, updated_at DESC
)
INSERT INTO project_voiceovers (
  project_id, audio_url, segments, params, raw_snapshot,
  input_hash, dependency_hash, content_hash, status, workflow_revision
)
SELECT
  v.project_id,
  v.audio_url,
  v.snapshot,
  v.snapshot,
  v.snapshot,
  encode(digest(v.snapshot::text, 'sha256'), 'hex'),
  COALESCE(s.script_hash, 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'),
  encode(digest(v.snapshot::text, 'sha256'), 'hex'),
  'active',
  1
FROM legacy_voiceovers v
LEFT JOIN script_hashes s ON s.project_id = v.project_id;

UPDATE projects p
SET
  workflow_migrated_at = COALESCE(p.workflow_migrated_at, now()),
  workflow_schema_version = GREATEST(COALESCE(p.workflow_schema_version, 1), 1)
WHERE p.deleted_at IS NULL
  AND (
    NULLIF(p.video_url, '') IS NOT NULL
    OR NULLIF(p.audio_url, '') IS NOT NULL
    OR COALESCE(p.video_context::jsonb, '{}'::jsonb) <> '{}'::jsonb
    OR COALESCE(p.subtitle::jsonb, '{}'::jsonb) <> '{}'::jsonb
    OR COALESCE(p.video_config::jsonb, '{}'::jsonb) <> '{}'::jsonb
    OR COALESCE(p.ai_analysis_script::jsonb, '{}'::jsonb) <> '{}'::jsonb
  );

WITH active_steps AS (
  SELECT project_id, id AS step_id, 'video-source' AS step_type, 'video_source' AS current_step, workflow_revision, 1 AS rank FROM project_video_sources WHERE deleted_at IS NULL AND cleared_at IS NULL
  UNION ALL
  SELECT project_id, id, 'subtitles', 'subtitle', workflow_revision, 2 FROM project_subtitles WHERE deleted_at IS NULL AND cleared_at IS NULL
  UNION ALL
  SELECT project_id, id, 'config', 'config', workflow_revision, 3 FROM project_generation_configs WHERE deleted_at IS NULL AND cleared_at IS NULL
  UNION ALL
  SELECT project_id, id, 'script', 'script', workflow_revision, 4 FROM project_scripts WHERE deleted_at IS NULL AND cleared_at IS NULL
  UNION ALL
  SELECT project_id, id, 'voiceover', 'voiceover', workflow_revision, 5 FROM project_voiceovers WHERE deleted_at IS NULL AND cleared_at IS NULL
), latest AS (
  SELECT DISTINCT ON (project_id)
    project_id, step_id, step_type, current_step, GREATEST(workflow_revision, 1) AS revision
  FROM active_steps
  ORDER BY project_id, rank DESC, workflow_revision DESC
)
INSERT INTO project_workflows (
  project_id, current_step, revision, status, last_step_type, last_step_id
)
SELECT
  l.project_id, l.current_step, l.revision, 'active', l.step_type, l.step_id
FROM latest l
WHERE NOT EXISTS (
  SELECT 1 FROM project_workflows w
  WHERE w.project_id = l.project_id AND w.deleted_at IS NULL
);

-- Backfill report: these counts should be zero before read-cutover.
SELECT 'legacy_video_without_step' AS check_name, count(*) AS remaining
FROM projects p
WHERE p.deleted_at IS NULL
  AND (NULLIF(p.video_url, '') IS NOT NULL OR COALESCE(p.video_context::jsonb, '{}'::jsonb) <> '{}'::jsonb)
  AND NOT EXISTS (SELECT 1 FROM project_video_sources s WHERE s.project_id = p.id AND s.deleted_at IS NULL AND s.cleared_at IS NULL)
UNION ALL
SELECT 'legacy_subtitle_without_step', count(*)
FROM projects p
WHERE p.deleted_at IS NULL
  AND COALESCE(p.subtitle::jsonb, '{}'::jsonb) <> '{}'::jsonb
  AND NOT EXISTS (SELECT 1 FROM project_subtitles s WHERE s.project_id = p.id AND s.deleted_at IS NULL AND s.cleared_at IS NULL)
UNION ALL
SELECT 'legacy_config_without_step', count(*)
FROM projects p
WHERE p.deleted_at IS NULL
  AND COALESCE(p.video_config::jsonb, '{}'::jsonb) <> '{}'::jsonb
  AND NOT EXISTS (SELECT 1 FROM project_generation_configs s WHERE s.project_id = p.id AND s.deleted_at IS NULL AND s.cleared_at IS NULL)
UNION ALL
SELECT 'legacy_script_without_step', count(*)
FROM projects p
WHERE p.deleted_at IS NULL
  AND COALESCE(p.ai_analysis_script::jsonb, '{}'::jsonb) <> '{}'::jsonb
  AND NOT EXISTS (SELECT 1 FROM project_scripts s WHERE s.project_id = p.id AND s.deleted_at IS NULL AND s.cleared_at IS NULL)
UNION ALL
SELECT 'legacy_voiceover_without_step', count(*)
FROM projects p
WHERE p.deleted_at IS NULL
  AND NULLIF(p.audio_url, '') IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM project_voiceovers s WHERE s.project_id = p.id AND s.deleted_at IS NULL AND s.cleared_at IS NULL);
