-- Store browser-local project preview metadata.
--
-- Generated preview images are now cached in the user's browser, matching the
-- narration-audio local storage path. The backend keeps only lookup metadata so
-- project cards can restore the cached Blob when the same browser is used.

ALTER TABLE projects
  ADD COLUMN IF NOT EXISTS local_preview_image_key varchar(255),
  ADD COLUMN IF NOT EXISTS preview_image_storage varchar(20),
  ADD COLUMN IF NOT EXISTS local_preview_image_missing boolean;
