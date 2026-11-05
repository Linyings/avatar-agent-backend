package main

import (
	"context"
	"log"

	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/database"
	"github.com/narratoai/backend-go/internal/server"
)

func main() {
	cfg := config.Load()

	db, err := database.Open(cfg)
	if err != nil {
		log.Fatalf("database open failed: %v", err)
	}

	if cfg.AutoMigrate {
		if err := database.AutoMigrate(db); err != nil {
			log.Fatalf("database migrate failed: %v", err)
		}
	}

	app := server.New(cfg, db)
	app.StartAvatarMVP(context.Background())
	go server.RunWorkflowRunExpirationWorker(context.Background(), app.WorkflowRuns())
	if err := app.Router().Run(cfg.Addr()); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}
