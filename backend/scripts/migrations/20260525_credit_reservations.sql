CREATE TABLE IF NOT EXISTS credit_reservations (
  id uuid PRIMARY KEY,
  created_at timestamptz,
  updated_at timestamptz,
  deleted_at timestamptz,
  user_id uuid NOT NULL,
  project_id uuid,
  api_endpoint varchar(100),
  provider varchar(50),
  service_kind varchar(30),
  model_id varchar(128),
  credit_count bigint NOT NULL,
  prompt_tokens bigint DEFAULT 0,
  completion_tokens bigint DEFAULT 0,
  total_model_tokens bigint DEFAULT 0,
  utf8_bytes bigint DEFAULT 0,
  character_count bigint DEFAULT 0,
  duration_seconds numeric(15,6),
  billing_month varchar(7),
  request_metadata jsonb,
  cost_amount numeric(15,6),
  missing_pricing boolean DEFAULT false,
  status varchar(20) NOT NULL DEFAULT 'pending',
  idempotency_key varchar(180) NOT NULL,
  expires_at timestamptz NOT NULL,
  confirmed_at timestamptz,
  released_at timestamptz,
  recorded_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_credit_reservations_deleted_at ON credit_reservations(deleted_at);
CREATE INDEX IF NOT EXISTS idx_credit_reservations_user_id ON credit_reservations(user_id);
CREATE INDEX IF NOT EXISTS idx_credit_reservations_project_id ON credit_reservations(project_id);
CREATE INDEX IF NOT EXISTS idx_credit_reservations_status ON credit_reservations(status);
CREATE INDEX IF NOT EXISTS idx_credit_reservations_expires_at ON credit_reservations(expires_at);
CREATE INDEX IF NOT EXISTS idx_credit_reservations_billing_month ON credit_reservations(billing_month);
CREATE UNIQUE INDEX IF NOT EXISTS idx_credit_reservations_idempotency_key ON credit_reservations(idempotency_key);
