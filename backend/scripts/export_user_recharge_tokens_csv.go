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
	defaultOutput := filepath.Join("exports", "user_recharge_tokens_"+time.Now().Format("20060102_150405")+".csv")
	output := flag.String("output", defaultOutput, "CSV output path")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

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
	sqlDB.SetMaxOpenConns(1)

	rows, err := sqlDB.QueryContext(ctx, userRechargeTokensSQL)
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

	headers := []string{
		"user_id",
		"email",
		"cumulative_recharged_cny",
		"account_total_recharged_cny",
		"recharge_records_total_cny",
		"paid_wechat_total_cny",
		"remaining_tokens",
		"total_tokens",
		"used_tokens",
	}
	if err := writer.Write(headers); err != nil {
		log.Fatalf("write csv header: %v", err)
	}

	count := 0
	for rows.Next() {
		values := make([]sql.NullString, len(headers))
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

const userRechargeTokensSQL = `
WITH recharge_records AS (
  SELECT
    user_id,
    COALESCE(SUM(price), 0)::numeric AS recharge_records_total_cny
  FROM token_recharge_records
  GROUP BY user_id
),
paid_wechat AS (
  SELECT
    user_id,
    COALESCE(SUM(total_amount), 0)::numeric AS paid_wechat_total_cny
  FROM wechat_payments
  WHERE status = 'paid'
  GROUP BY user_id
)
SELECT
  u.id::text AS user_id,
  u.email,
  GREATEST(
    COALESCE(a.total_recharged, 0)::numeric,
    COALESCE(rr.recharge_records_total_cny, 0)::numeric,
    COALESCE(pw.paid_wechat_total_cny, 0)::numeric
  )::numeric(20,2)::text AS cumulative_recharged_cny,
  COALESCE(a.total_recharged, 0)::numeric(20,2)::text AS account_total_recharged_cny,
  COALESCE(rr.recharge_records_total_cny, 0)::numeric(20,2)::text AS recharge_records_total_cny,
  COALESCE(pw.paid_wechat_total_cny, 0)::numeric(20,2)::text AS paid_wechat_total_cny,
  GREATEST(COALESCE(a.total_tokens, 0) - COALESCE(a.used_tokens, 0), 0)::text AS remaining_tokens,
  COALESCE(a.total_tokens, 0)::text AS total_tokens,
  COALESCE(a.used_tokens, 0)::text AS used_tokens
FROM users u
LEFT JOIN user_token_accounts a ON a.user_id = u.id
LEFT JOIN recharge_records rr ON rr.user_id = u.id
LEFT JOIN paid_wechat pw ON pw.user_id = u.id
ORDER BY u.email ASC, u.id ASC
`
