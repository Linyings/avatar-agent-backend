-- Persist project-level UI/workflow metadata such as the selected inference mode.

ALTER TABLE projects
  ADD COLUMN IF NOT EXISTS project_metadata jsonb NOT NULL DEFAULT '{}'::jsonb;
