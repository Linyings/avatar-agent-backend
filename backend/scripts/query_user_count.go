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
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
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

	if err := sqlDB.PingContext(ctx); err != nil {
		log.Fatalf("ping database: %v", err)
	}

	var count int64
	if err := sqlDB.QueryRowContext(ctx, "select count(*) from users").Scan(&count); err != nil {
		log.Fatalf("query user count: %v", err)
	}
	fmt.Printf("users=%d\n", count)
}
