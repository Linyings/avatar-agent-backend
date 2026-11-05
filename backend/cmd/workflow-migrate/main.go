package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/database"
	"github.com/narratoai/backend-go/internal/workflowmigration"
)

func main() {
	dryRun := flag.Bool("dry-run", true, "只输出将迁移的数据统计，不写入数据库")
	apply := flag.Bool("apply", false, "执行迁移写入；设置后会覆盖 --dry-run")
	verifyOnly := flag.Bool("verify-only", false, "只校验旧字段是否都有 active workflow row")
	autoMigrate := flag.Bool("auto-migrate", false, "先运行 GORM AutoMigrate；生产环境建议使用 SQL expand migration")
	flag.Parse()

	cfg := config.Load()
	db, err := database.Open(cfg)
	if err != nil {
		exitf("open database: %v", err)
	}
	if *autoMigrate {
		if err := database.AutoMigrate(db); err != nil {
			exitf("auto migrate: %v", err)
		}
	}
	sqlDB, err := db.DB()
	if err == nil {
		defer sqlDB.Close()
	}

	ctx := context.Background()
	var report workflowmigration.Report
	if *verifyOnly {
		report, err = workflowmigration.Verify(ctx, db)
	} else {
		report, err = workflowmigration.Run(ctx, db, workflowmigration.Options{DryRun: *dryRun && !*apply})
	}
	if err != nil {
		exitf("workflow migration failed: %v", err)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		exitf("encode report: %v", err)
	}
	fmt.Println(string(encoded))
	if report.HasMissingLegacyRows() {
		os.Exit(2)
	}
}

func exitf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
