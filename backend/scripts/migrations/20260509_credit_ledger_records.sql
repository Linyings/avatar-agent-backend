-- Store immutable Credits balance changes for the user-facing consumption record page.

CREATE TABLE IF NOT EXISTS credit_ledger_records (
  id uuid PRIMARY KEY,
  created_at timestamptz,
  updated_at timestamptz,
  deleted_at timestamptz,
  user_id uuid NOT NULL,
  project_id uuid,
  project_name varchar(200) NOT NULL DEFAULT '',
  event_type varchar(32) NOT NULL,
  item varchar(160) NOT NULL DEFAULT '',
  change_amount bigint NOT NULL,
  balance_after bigint NOT NULL,
  reason varchar(255) NOT NULL DEFAULT '',
  request_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  recorded_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_credit_ledger_records_deleted_at ON credit_ledger_records (deleted_at);
CREATE INDEX IF NOT EXISTS idx_credit_ledger_records_user_id ON credit_ledger_records (user_id);
CREATE INDEX IF NOT EXISTS idx_credit_ledger_records_project_id ON credit_ledger_records (project_id);
CREATE INDEX IF NOT EXISTS idx_credit_ledger_records_event_type ON credit_ledger_records (event_type);
CREATE INDEX IF NOT EXISTS idx_credit_ledger_records_change_amount ON credit_ledger_records (change_amount);
CREATE INDEX IF NOT EXISTS idx_credit_ledger_records_recorded_at ON credit_ledger_records (recorded_at);

WITH should_backfill AS (
  SELECT NOT EXISTS (SELECT 1 FROM credit_ledger_records) AS enabled
),
legacy_events AS (
  SELECT
    u.id,
    u.recorded_at AS created_at,
    u.recorded_at AS updated_at,
    u.user_id,
    u.project_id,
    COALESCE(p.name, '') AS project_name,
    'consume'::varchar(32) AS event_type,
    CASE COALESCE(NULLIF(lower(u.service_kind), ''), '')
      WHEN 'llm' THEN 'LLM 消费'
      WHEN 'vision' THEN 'Vision 消费'
      WHEN 'asr' THEN 'ASR 消费'
      WHEN 'tts' THEN 'TTS 消费'
      ELSE 'Credits 消费'
    END::varchar(160) AS item,
    -COALESCE(NULLIF(u.credit_count, 0), u.token_count, 0) AS change_amount,
    COALESCE(u.api_endpoint, '')::varchar(255) AS reason,
    COALESCE(u.request_metadata, '{}'::jsonb) AS request_metadata,
    u.recorded_at
  FROM token_usage_records u
  LEFT JOIN projects p ON p.id = u.project_id
  UNION ALL
  SELECT
    r.id,
    r.created_at,
    r.updated_at,
    r.user_id,
    NULL::uuid AS project_id,
    ''::varchar(200) AS project_name,
    'recharge'::varchar(32) AS event_type,
    COALESCE(tp.display_name, 'Credits 充值')::varchar(160) AS item,
    COALESCE(r.token_amount, 0) + COALESCE(r.bonus_tokens, 0) AS change_amount,
    '微信充值'::varchar(255) AS reason,
    jsonb_build_object(
      'payment_id', r.payment_id,
      'package_id', r.package_id,
      'payment_amount', r.price,
      'backfilled', true
    ) AS request_metadata,
    COALESCE(r.created_at, now()) AS recorded_at
  FROM token_recharge_records r
  LEFT JOIN token_packages tp ON tp.id = r.package_id
),
ordered_events AS (
  SELECT
    e.*,
    COALESCE(a.total_tokens, 0) - COALESCE(a.used_tokens, 0)
      - SUM(e.change_amount) OVER (PARTITION BY e.user_id)
      + SUM(e.change_amount) OVER (
          PARTITION BY e.user_id
          ORDER BY e.recorded_at ASC, e.created_at ASC, e.id ASC
          ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
        ) AS balance_after
  FROM legacy_events e
  JOIN should_backfill b ON b.enabled
  LEFT JOIN user_token_accounts a ON a.user_id = e.user_id
  WHERE e.change_amount <> 0
)
INSERT INTO credit_ledger_records (
  id,
  created_at,
  updated_at,
  user_id,
  project_id,
  project_name,
  event_type,
  item,
  change_amount,
  balance_after,
  reason,
  request_metadata,
  recorded_at
)
SELECT
  id,
  created_at,
  updated_at,
  user_id,
  project_id,
  project_name,
  event_type,
  item,
  change_amount,
  balance_after,
  reason,
  request_metadata,
  recorded_at
FROM ordered_events
ON CONFLICT (id) DO NOTHING;
