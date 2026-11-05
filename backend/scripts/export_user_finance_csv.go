//go:build ignore

package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/database"
)

func main() {
	defaultOutput := filepath.Join("exports", "user_finance_"+time.Now().Format("20060102_150405")+".csv")
	output := flag.String("output", defaultOutput, "CSV output path")
	flag.Parse()

	cfg := config.Load()
	db, err := database.Open(cfg)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("database handle: %v", err)
	}
	defer sqlDB.Close()

	rows, err := sqlDB.QueryContext(context.Background(), exportSQL)
	if err != nil {
		log.Fatalf("query users: %v", err)
	}
	defer rows.Close()

	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		log.Fatalf("create output dir: %v", err)
	}
	file, err := os.Create(*output)
	if err != nil {
		log.Fatalf("create csv: %v", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	if err := writer.Write(csvHeaders); err != nil {
		log.Fatalf("write csv header: %v", err)
	}

	count := 0
	for rows.Next() {
		values := make([]sql.NullString, len(csvHeaders))
		scans := make([]any, len(values))
		for i := range values {
			scans[i] = &values[i]
		}
		if err := rows.Scan(scans...); err != nil {
			log.Fatalf("scan row: %v", err)
		}
		record := make([]string, len(values))
		for i, value := range values {
			if value.Valid {
				record[i] = value.String
			}
		}
		if err := writer.Write(record); err != nil {
			log.Fatalf("write csv row: %v", err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("read rows: %v", err)
	}
	if err := writer.Error(); err != nil {
		log.Fatalf("flush csv: %v", err)
	}

	fmt.Printf("exported %d users to %s\n", count, *output)
}

var csvHeaders = []string{
	"user_id",
	"username",
	"email",
	"display_name",
	"avatar_url",
	"is_active",
	"is_verified",
	"is_admin",
	"password_auth_enabled",
	"email_verified_at",
	"last_login_at",
	"user_created_at",
	"user_updated_at",
	"profile_timezone",
	"profile_language",
	"account_total_tokens",
	"account_used_tokens",
	"account_remaining_tokens",
	"account_total_recharged_cny",
	"recharge_records_total_cny",
	"paid_wechat_total_cny",
	"cumulative_recharged_cny",
	"recharge_records_count",
	"paid_wechat_count",
	"recharge_tokens_granted",
	"last_recharge_at",
	"last_paid_at",
	"subscription_remaining_credits",
	"total_remaining_with_subscription",
	"project_count",
}

const exportSQL = `
WITH recharge_records AS (
  SELECT
    user_id,
    COUNT(*)::text AS recharge_records_count,
    COALESCE(SUM(price), 0)::numeric(20,2)::text AS recharge_records_total_cny,
    COALESCE(SUM(token_amount + bonus_tokens), 0)::text AS recharge_tokens_granted,
    MAX(created_at) AS last_recharge_at
  FROM token_recharge_records
  GROUP BY user_id
),
paid_wechat AS (
  SELECT
    user_id,
    COUNT(*)::text AS paid_wechat_count,
    COALESCE(SUM(total_amount), 0)::numeric(20,2)::text AS paid_wechat_total_cny,
    MAX(COALESCE(success_time, notify_time, updated_at, created_at)) AS last_paid_at
  FROM wechat_payments
  WHERE status = 'paid'
  GROUP BY user_id
),
subscription_remaining AS (
  SELECT
    user_id,
    COALESCE(SUM(GREATEST(credit_amount - used_amount, 0)), 0)::text AS subscription_remaining_credits
  FROM subscription_credit_grants
  WHERE status = 'active'
  GROUP BY user_id
),
project_counts AS (
  SELECT owner_id AS user_id, COUNT(*)::text AS project_count
  FROM projects
  GROUP BY owner_id
)
SELECT
  u.id::text AS user_id,
  u.username,
  u.email,
  COALESCE(u.display_name, '') AS display_name,
  COALESCE(u.avatar_url, '') AS avatar_url,
  u.is_active::text AS is_active,
  u.is_verified::text AS is_verified,
  u.is_admin::text AS is_admin,
  u.password_auth_enabled::text AS password_auth_enabled,
  COALESCE(to_char(u.email_verified_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS email_verified_at,
  COALESCE(to_char(u.last_login_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS last_login_at,
  to_char(u.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS user_created_at,
  to_char(u.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS user_updated_at,
  COALESCE(p.timezone, '') AS profile_timezone,
  COALESCE(p.language, '') AS profile_language,
  COALESCE(a.total_tokens, 0)::text AS account_total_tokens,
  COALESCE(a.used_tokens, 0)::text AS account_used_tokens,
  GREATEST(COALESCE(a.total_tokens, 0) - COALESCE(a.used_tokens, 0), 0)::text AS account_remaining_tokens,
  COALESCE(a.total_recharged, 0)::numeric(20,2)::text AS account_total_recharged_cny,
  COALESCE(rr.recharge_records_total_cny, '0.00') AS recharge_records_total_cny,
  COALESCE(pw.paid_wechat_total_cny, '0.00') AS paid_wechat_total_cny,
  GREATEST(
    COALESCE(a.total_recharged, 0)::numeric,
    COALESCE(rr.recharge_records_total_cny, '0')::numeric,
    COALESCE(pw.paid_wechat_total_cny, '0')::numeric
  )::numeric(20,2)::text AS cumulative_recharged_cny,
  COALESCE(rr.recharge_records_count, '0') AS recharge_records_count,
  COALESCE(pw.paid_wechat_count, '0') AS paid_wechat_count,
  COALESCE(rr.recharge_tokens_granted, '0') AS recharge_tokens_granted,
  COALESCE(to_char(rr.last_recharge_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS last_recharge_at,
  COALESCE(to_char(pw.last_paid_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') AS last_paid_at,
  COALESCE(sr.subscription_remaining_credits, '0') AS subscription_remaining_credits,
  (
    GREATEST(COALESCE(a.total_tokens, 0) - COALESCE(a.used_tokens, 0), 0)
    + COALESCE(sr.subscription_remaining_credits, '0')::bigint
  )::text AS total_remaining_with_subscription,
  COALESCE(pc.project_count, '0') AS project_count
FROM users u
LEFT JOIN user_profiles p ON p.user_id = u.id
LEFT JOIN user_token_accounts a ON a.user_id = u.id
LEFT JOIN recharge_records rr ON rr.user_id = u.id
LEFT JOIN paid_wechat pw ON pw.user_id = u.id
LEFT JOIN subscription_remaining sr ON sr.user_id = u.id
LEFT JOIN project_counts pc ON pc.user_id = u.id
ORDER BY u.created_at ASC, u.id ASC
`
