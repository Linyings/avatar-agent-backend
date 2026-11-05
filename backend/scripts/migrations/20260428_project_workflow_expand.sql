-- Project workflow split expand-only migration.
--
-- Scope:
--   * Adds nullable workflow metadata to projects.
--   * Adds nullable project/workflow linkage to upload_records.
--   * Creates step-oriented workflow tables for the phase-1 dual-track rollout.
--   * Preserves existing projects JSON compatibility-cache columns; no old columns are dropped.
--
-- Safety notes:
--   * This migration is additive only.
--   * Run outside an explicit transaction because PostgreSQL CREATE INDEX CONCURRENTLY
--     cannot run inside a transaction block.
--   * Foreign keys are limited to new workflow tables. Existing upload_records gets
--     nullable semantic linkage columns without a new FK to avoid scanning/locking
--     existing production rows during the expand phase.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

ALTER TABLE projects
  ADD COLUMN IF NOT EXISTS current_step varchar(50),
  ADD COLUMN IF NOT EXISTS workflow_migrated_at timestamptz,
  ADD COLUMN IF NOT EXISTS workflow_schema_version integer DEFAULT 1;

ALTER TABLE upload_records
  ADD COLUMN IF NOT EXISTS project_id uuid,
  ADD COLUMN IF NOT EXISTS asset_type varchar(32),
  ADD COLUMN IF NOT EXISTS step_type varchar(50),
  ADD COLUMN IF NOT EXISTS step_id uuid;

CREATE TABLE IF NOT EXISTS project_workflows (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  project_id uuid NOT NULL REFERENCES projects(id),
  current_step varchar(50),
  revision integer NOT NULL DEFAULT 1,
  status varchar(20) DEFAULT 'active',
  failure_reason text,
  last_step_type varchar(50),
  last_step_id uuid
);

CREATE TABLE IF NOT EXISTS project_workflow_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  project_id uuid NOT NULL REFERENCES projects(id),
  workflow_id uuid,
  step_type varchar(50),
  step_id uuid,
  event_type varchar(80) NOT NULL,
  workflow_revision integer,
  actor_type varchar(30) DEFAULT 'system',
  message text,
  metadata jsonb
);

CREATE TABLE IF NOT EXISTS project_video_sources (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  project_id uuid NOT NULL REFERENCES projects(id),
  upload_id uuid,
  original_filename varchar(255) NOT NULL,
  video_url varchar(2048),
  storage_key varchar(1024),
  content_type varchar(127),
  file_size bigint,
  duration_ms bigint,
  is_trimmed boolean NOT NULL DEFAULT false,
  trim_start_ms bigint,
  trim_end_ms bigint,
  asr_engine varchar(50),
  frontend_params jsonb,
  raw_snapshot jsonb,
  input_hash varchar(64),
  dependency_hash varchar(64),
  content_hash varchar(64),
  status varchar(20) DEFAULT 'active',
  workflow_revision integer NOT NULL DEFAULT 1,
  cleared_at timestamptz,
  cleared_reason text
);

CREATE TABLE IF NOT EXISTS project_subtitles (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  project_id uuid NOT NULL REFERENCES projects(id),
  video_source_id uuid,
  asr_engine varchar(50),
  language varchar(20),
  subtitle_format varchar(20),
  content jsonb,
  raw_result jsonb,
  raw_snapshot jsonb,
  input_hash varchar(64),
  dependency_hash varchar(64),
  content_hash varchar(64),
  status varchar(20) DEFAULT 'active',
  workflow_revision integer NOT NULL DEFAULT 1,
  cleared_at timestamptz,
  cleared_reason text
);

CREATE TABLE IF NOT EXISTS project_generation_configs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  project_id uuid NOT NULL REFERENCES projects(id),
  subtitle_id uuid,
  style varchar(50),
  language varchar(20),
  clip_mode varchar(50),
  original_ratio varchar(20),
  narration_ratio varchar(20),
  voice_provider varchar(50),
  voice_id varchar(100),
  raw_params jsonb,
  raw_snapshot jsonb,
  input_hash varchar(64),
  dependency_hash varchar(64),
  params_hash varchar(64),
  status varchar(20) DEFAULT 'active',
  workflow_revision integer NOT NULL DEFAULT 1,
  cleared_at timestamptz,
  cleared_reason text
);

CREATE TABLE IF NOT EXISTS project_scripts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  project_id uuid NOT NULL REFERENCES projects(id),
  config_id uuid,
  script_content jsonb,
  model_name varchar(100),
  raw_result jsonb,
  raw_snapshot jsonb,
  input_hash varchar(64),
  dependency_hash varchar(64),
  script_hash varchar(64),
  status varchar(20) DEFAULT 'active',
  workflow_revision integer NOT NULL DEFAULT 1,
  cleared_at timestamptz,
  cleared_reason text
);

CREATE TABLE IF NOT EXISTS project_voiceovers (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  project_id uuid NOT NULL REFERENCES projects(id),
  script_id uuid,
  upload_id uuid,
  voice_provider varchar(50),
  voice_id varchar(100),
  audio_url varchar(2048),
  storage_key varchar(1024),
  segments jsonb,
  params jsonb,
  raw_snapshot jsonb,
  input_hash varchar(64),
  dependency_hash varchar(64),
  content_hash varchar(64),
  status varchar(20) DEFAULT 'active',
  workflow_revision integer NOT NULL DEFAULT 1,
  cleared_at timestamptz,
  cleared_reason text
);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_projects_current_step ON projects(current_step);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_upload_records_project_id ON upload_records(project_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_upload_records_asset_type ON upload_records(asset_type);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_upload_records_step_type ON upload_records(step_type);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_upload_records_step_id ON upload_records(step_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_workflows_deleted_at ON project_workflows(deleted_at);
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ux_project_workflows_project_id ON project_workflows(project_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_workflows_current_step ON project_workflows(current_step);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_workflows_revision ON project_workflows(revision);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_workflows_status ON project_workflows(status);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_workflows_last_step_type ON project_workflows(last_step_type);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_workflows_last_step_id ON project_workflows(last_step_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_workflow_events_deleted_at ON project_workflow_events(deleted_at);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_workflow_events_project_id ON project_workflow_events(project_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_workflow_events_workflow_id ON project_workflow_events(workflow_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_workflow_events_step_type ON project_workflow_events(step_type);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_workflow_events_step_id ON project_workflow_events(step_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_workflow_events_event_type ON project_workflow_events(event_type);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_workflow_events_workflow_revision ON project_workflow_events(workflow_revision);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_workflow_events_actor_type ON project_workflow_events(actor_type);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_video_sources_deleted_at ON project_video_sources(deleted_at);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_video_sources_project_id ON project_video_sources(project_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_video_sources_upload_id ON project_video_sources(upload_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_video_sources_storage_key ON project_video_sources(storage_key);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_video_sources_input_hash ON project_video_sources(input_hash);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_video_sources_dependency_hash ON project_video_sources(dependency_hash);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_video_sources_content_hash ON project_video_sources(content_hash);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_video_sources_status ON project_video_sources(status);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_video_sources_cleared_at ON project_video_sources(cleared_at);
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ux_project_video_sources_one_active ON project_video_sources(project_id) WHERE deleted_at IS NULL AND cleared_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_subtitles_deleted_at ON project_subtitles(deleted_at);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_subtitles_project_id ON project_subtitles(project_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_subtitles_video_source_id ON project_subtitles(video_source_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_subtitles_input_hash ON project_subtitles(input_hash);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_subtitles_dependency_hash ON project_subtitles(dependency_hash);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_subtitles_content_hash ON project_subtitles(content_hash);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_subtitles_status ON project_subtitles(status);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_subtitles_cleared_at ON project_subtitles(cleared_at);
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ux_project_subtitles_one_active ON project_subtitles(project_id) WHERE deleted_at IS NULL AND cleared_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_generation_configs_deleted_at ON project_generation_configs(deleted_at);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_generation_configs_project_id ON project_generation_configs(project_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_generation_configs_subtitle_id ON project_generation_configs(subtitle_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_generation_configs_style ON project_generation_configs(style);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_generation_configs_language ON project_generation_configs(language);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_generation_configs_clip_mode ON project_generation_configs(clip_mode);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_generation_configs_voice_provider ON project_generation_configs(voice_provider);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_generation_configs_voice_id ON project_generation_configs(voice_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_generation_configs_input_hash ON project_generation_configs(input_hash);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_generation_configs_dependency_hash ON project_generation_configs(dependency_hash);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_generation_configs_params_hash ON project_generation_configs(params_hash);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_generation_configs_status ON project_generation_configs(status);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_generation_configs_cleared_at ON project_generation_configs(cleared_at);
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ux_project_generation_configs_one_active ON project_generation_configs(project_id) WHERE deleted_at IS NULL AND cleared_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_scripts_deleted_at ON project_scripts(deleted_at);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_scripts_project_id ON project_scripts(project_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_scripts_config_id ON project_scripts(config_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_scripts_model_name ON project_scripts(model_name);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_scripts_input_hash ON project_scripts(input_hash);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_scripts_dependency_hash ON project_scripts(dependency_hash);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_scripts_script_hash ON project_scripts(script_hash);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_scripts_status ON project_scripts(status);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_scripts_cleared_at ON project_scripts(cleared_at);
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ux_project_scripts_one_active ON project_scripts(project_id) WHERE deleted_at IS NULL AND cleared_at IS NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_voiceovers_deleted_at ON project_voiceovers(deleted_at);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_voiceovers_project_id ON project_voiceovers(project_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_voiceovers_script_id ON project_voiceovers(script_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_voiceovers_upload_id ON project_voiceovers(upload_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_voiceovers_voice_provider ON project_voiceovers(voice_provider);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_voiceovers_voice_id ON project_voiceovers(voice_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_voiceovers_storage_key ON project_voiceovers(storage_key);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_voiceovers_input_hash ON project_voiceovers(input_hash);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_voiceovers_dependency_hash ON project_voiceovers(dependency_hash);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_voiceovers_content_hash ON project_voiceovers(content_hash);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_voiceovers_status ON project_voiceovers(status);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_project_voiceovers_cleared_at ON project_voiceovers(cleared_at);
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS ux_project_voiceovers_one_active ON project_voiceovers(project_id) WHERE deleted_at IS NULL AND cleared_at IS NULL;
