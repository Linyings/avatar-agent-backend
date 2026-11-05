CREATE TABLE IF NOT EXISTS auth_email_codes (
  id uuid PRIMARY KEY,
  created_at timestamptz,
  updated_at timestamptz,
  deleted_at timestamptz,
  email varchar(255) NOT NULL,
  purpose varchar(32) NOT NULL,
  code_hash varchar(128) NOT NULL,
  attempts bigint NOT NULL DEFAULT 0,
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz
);

CREATE INDEX IF NOT EXISTS idx_auth_email_codes_deleted_at ON auth_email_codes(deleted_at);
CREATE INDEX IF NOT EXISTS idx_auth_email_codes_expires_at ON auth_email_codes(expires_at);
CREATE INDEX IF NOT EXISTS idx_auth_email_codes_consumed_at ON auth_email_codes(consumed_at);
CREATE INDEX IF NOT EXISTS idx_auth_email_codes_email_purpose_created ON auth_email_codes(email, purpose, created_at);

CREATE TABLE IF NOT EXISTS oauth_accounts (
  id uuid PRIMARY KEY,
  created_at timestamptz,
  updated_at timestamptz,
  deleted_at timestamptz,
  user_id uuid NOT NULL,
  provider varchar(32) NOT NULL,
  provider_user_id varchar(128) NOT NULL,
  provider_email varchar(255),
  provider_email_verified boolean DEFAULT false,
  linked_at timestamptz NOT NULL,
  last_login_at timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_oauth_accounts_provider_user ON oauth_accounts(provider, provider_user_id);
CREATE INDEX IF NOT EXISTS idx_oauth_accounts_deleted_at ON oauth_accounts(deleted_at);
CREATE INDEX IF NOT EXISTS idx_oauth_accounts_user_id ON oauth_accounts(user_id);
CREATE INDEX IF NOT EXISTS idx_oauth_accounts_provider_email ON oauth_accounts(provider_email);

CREATE TABLE IF NOT EXISTS oauth_states (
  id uuid PRIMARY KEY,
  created_at timestamptz,
  updated_at timestamptz,
  deleted_at timestamptz,
  provider varchar(32) NOT NULL,
  state_hash varchar(128) NOT NULL,
  code_verifier varchar(128) NOT NULL,
  redirect_path varchar(1024),
  frontend_base_url varchar(1024),
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_oauth_states_state_hash ON oauth_states(state_hash);
CREATE INDEX IF NOT EXISTS idx_oauth_states_deleted_at ON oauth_states(deleted_at);
CREATE INDEX IF NOT EXISTS idx_oauth_states_provider ON oauth_states(provider);
CREATE INDEX IF NOT EXISTS idx_oauth_states_expires_at ON oauth_states(expires_at);
CREATE INDEX IF NOT EXISTS idx_oauth_states_consumed_at ON oauth_states(consumed_at);

CREATE TABLE IF NOT EXISTS oauth_login_tickets (
  id uuid PRIMARY KEY,
  created_at timestamptz,
  updated_at timestamptz,
  deleted_at timestamptz,
  ticket_hash varchar(128) NOT NULL,
  user_id uuid NOT NULL,
  provider varchar(32) NOT NULL,
  redirect_path varchar(1024),
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_oauth_login_tickets_ticket_hash ON oauth_login_tickets(ticket_hash);
CREATE INDEX IF NOT EXISTS idx_oauth_login_tickets_deleted_at ON oauth_login_tickets(deleted_at);
CREATE INDEX IF NOT EXISTS idx_oauth_login_tickets_user_id ON oauth_login_tickets(user_id);
CREATE INDEX IF NOT EXISTS idx_oauth_login_tickets_provider ON oauth_login_tickets(provider);
CREATE INDEX IF NOT EXISTS idx_oauth_login_tickets_expires_at ON oauth_login_tickets(expires_at);
CREATE INDEX IF NOT EXISTS idx_oauth_login_tickets_consumed_at ON oauth_login_tickets(consumed_at);

ALTER TABLE users
  ADD COLUMN IF NOT EXISTS password_auth_enabled boolean DEFAULT true;

UPDATE users
SET password_auth_enabled = true
WHERE password_auth_enabled IS NULL;

ALTER TABLE users
  DROP COLUMN IF EXISTS invited_by,
  DROP COLUMN IF EXISTS invitation_count;

DROP TABLE IF EXISTS user_invitations;
DROP TABLE IF EXISTS invitation_codes;
