//go:build ignore

package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/database"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	db, err := database.Open(config.Load())
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("database handle: %v", err)
	}
	defer sqlDB.Close()

	rows, err := sqlDB.QueryContext(ctx, `
SELECT
  pid::text,
  usename,
  state,
  COALESCE(wait_event_type, '') AS wait_event_type,
  COALESCE(wait_event, '') AS wait_event,
  now() - query_start AS query_age,
  left(regexp_replace(query, '\s+', ' ', 'g'), 220) AS query
FROM pg_stat_activity
WHERE datname = current_database()
ORDER BY query_start NULLS LAST
LIMIT 20
`)
	if err != nil {
		log.Fatalf("query pg_stat_activity: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var pid, user, state, waitType, wait, age, query string
		if err := rows.Scan(&pid, &user, &state, &waitType, &wait, &age, &query); err != nil {
			log.Fatalf("scan: %v", err)
		}
		fmt.Printf("pid=%s user=%s state=%s wait=%s/%s age=%s query=%s\n", pid, user, state, waitType, wait, age, query)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("rows: %v", err)
	}
}
