ALTER TABLE oauth_states ADD COLUMN IF NOT EXISTS frontend_base_url varchar(1024);
