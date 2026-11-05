ALTER TABLE token_packages
  ADD COLUMN IF NOT EXISTS creem_product_id varchar(64),
  ADD COLUMN IF NOT EXISTS creem_enabled boolean DEFAULT false,
  ADD COLUMN IF NOT EXISTS creem_currency varchar(3) DEFAULT 'USD';

CREATE TABLE IF NOT EXISTS subscription_plans (
  id uuid PRIMARY KEY,
  created_at timestamptz,
  updated_at timestamptz,
  deleted_at timestamptz,
  name varchar(50) NOT NULL UNIQUE,
  display_name varchar(100) NOT NULL,
  description text,
  monthly_credits bigint NOT NULL,
  price_monthly decimal(10,2) NOT NULL,
  price_yearly decimal(10,2) NOT NULL,
  currency varchar(3) DEFAULT 'USD',
  creem_product_id_monthly varchar(64),
  creem_product_id_yearly varchar(64),
  features jsonb,
  limits jsonb,
  is_active boolean DEFAULT true,
  is_featured boolean DEFAULT false,
  sort_order bigint DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_subscription_plans_deleted_at ON subscription_plans(deleted_at);
CREATE INDEX IF NOT EXISTS idx_subscription_plans_is_active ON subscription_plans(is_active);
CREATE INDEX IF NOT EXISTS idx_subscription_plans_sort_order ON subscription_plans(sort_order);
CREATE INDEX IF NOT EXISTS idx_subscription_plans_creem_monthly ON subscription_plans(creem_product_id_monthly);
CREATE INDEX IF NOT EXISTS idx_subscription_plans_creem_yearly ON subscription_plans(creem_product_id_yearly);

CREATE TABLE IF NOT EXISTS user_subscriptions (
  id uuid PRIMARY KEY,
  created_at timestamptz,
  updated_at timestamptz,
  deleted_at timestamptz,
  user_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  billing_cycle varchar(16) NOT NULL,
  status varchar(32) NOT NULL,
  creem_customer_id varchar(64),
  creem_subscription_id varchar(64) UNIQUE,
  creem_product_id varchar(64),
  current_period_start timestamptz,
  current_period_end timestamptz,
  cancel_at_period_end boolean DEFAULT false,
  canceled_at timestamptz,
  trial_end timestamptz,
  next_credit_grant_at timestamptz,
  monthly_credits_snapshot bigint DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_user_subscriptions_deleted_at ON user_subscriptions(deleted_at);
CREATE INDEX IF NOT EXISTS idx_user_subscriptions_user_id ON user_subscriptions(user_id);
CREATE INDEX IF NOT EXISTS idx_user_subscriptions_plan_id ON user_subscriptions(plan_id);
CREATE INDEX IF NOT EXISTS idx_user_subscriptions_status ON user_subscriptions(status);
CREATE INDEX IF NOT EXISTS idx_user_subscriptions_creem_customer_id ON user_subscriptions(creem_customer_id);
CREATE INDEX IF NOT EXISTS idx_user_subscriptions_creem_product_id ON user_subscriptions(creem_product_id);

CREATE TABLE IF NOT EXISTS subscription_credit_grants (
  id uuid PRIMARY KEY,
  created_at timestamptz,
  updated_at timestamptz,
  deleted_at timestamptz,
  user_id uuid NOT NULL,
  subscription_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  credit_amount bigint NOT NULL,
  used_amount bigint DEFAULT 0,
  period_start timestamptz NOT NULL,
  period_end timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  idempotency_key varchar(160) NOT NULL UNIQUE,
  source_event_id varchar(80),
  status varchar(20) DEFAULT 'active'
);

CREATE INDEX IF NOT EXISTS idx_subscription_credit_grants_deleted_at ON subscription_credit_grants(deleted_at);
CREATE INDEX IF NOT EXISTS idx_subscription_credit_grants_user_id ON subscription_credit_grants(user_id);
CREATE INDEX IF NOT EXISTS idx_subscription_credit_grants_subscription_id ON subscription_credit_grants(subscription_id);
CREATE INDEX IF NOT EXISTS idx_subscription_credit_grants_plan_id ON subscription_credit_grants(plan_id);
CREATE INDEX IF NOT EXISTS idx_subscription_credit_grants_expires_at ON subscription_credit_grants(expires_at);
CREATE INDEX IF NOT EXISTS idx_subscription_credit_grants_status ON subscription_credit_grants(status);

CREATE TABLE IF NOT EXISTS creem_checkout_sessions (
  id uuid PRIMARY KEY,
  created_at timestamptz,
  updated_at timestamptz,
  deleted_at timestamptz,
  user_id uuid NOT NULL,
  purpose varchar(32) NOT NULL,
  request_id varchar(80) NOT NULL UNIQUE,
  creem_checkout_id varchar(64) UNIQUE,
  checkout_url varchar(2048),
  creem_product_id varchar(64),
  creem_customer_id varchar(64),
  creem_subscription_id varchar(64),
  creem_order_id varchar(64),
  plan_id uuid,
  package_id uuid,
  billing_cycle varchar(16),
  status varchar(32) DEFAULT 'pending',
  amount decimal(10,2),
  currency varchar(3) DEFAULT 'USD',
  metadata jsonb
);

CREATE INDEX IF NOT EXISTS idx_creem_checkout_sessions_deleted_at ON creem_checkout_sessions(deleted_at);
CREATE INDEX IF NOT EXISTS idx_creem_checkout_sessions_user_id ON creem_checkout_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_creem_checkout_sessions_purpose ON creem_checkout_sessions(purpose);
CREATE INDEX IF NOT EXISTS idx_creem_checkout_sessions_creem_product_id ON creem_checkout_sessions(creem_product_id);
CREATE INDEX IF NOT EXISTS idx_creem_checkout_sessions_status ON creem_checkout_sessions(status);

CREATE TABLE IF NOT EXISTS creem_webhook_events (
  id uuid PRIMARY KEY,
  created_at timestamptz,
  updated_at timestamptz,
  deleted_at timestamptz,
  event_id varchar(80) NOT NULL UNIQUE,
  event_type varchar(80) NOT NULL,
  payload jsonb,
  processed_at timestamptz,
  processing_error text
);

CREATE INDEX IF NOT EXISTS idx_creem_webhook_events_deleted_at ON creem_webhook_events(deleted_at);
CREATE INDEX IF NOT EXISTS idx_creem_webhook_events_event_type ON creem_webhook_events(event_type);
