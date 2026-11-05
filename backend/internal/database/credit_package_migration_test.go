package database

import (
	"fmt"
	"testing"
	"time"

	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
)

func TestMigrateCreditPackagesToCNYRunsExactlyOnce(t *testing.T) {
	db := openCreditPackageMigrationTestDB(t)
	pkg := models.TokenPackage{Name: "starter", DisplayName: "Starter", TokenAmount: 1000, Price: 10, Currency: "USD", ValidityHours: 720}
	if err := db.Create(&pkg).Error; err != nil {
		t.Fatalf("seed package: %v", err)
	}

	if err := migrateCreditPackagesToCNY(db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	if err := migrateCreditPackagesToCNY(db); err != nil {
		t.Fatalf("second migration: %v", err)
	}

	var got models.TokenPackage
	if err := db.Where("name = ?", "starter").First(&got).Error; err != nil {
		t.Fatalf("load package: %v", err)
	}
	if got.Price != 72.00 {
		t.Fatalf("price = %.2f, want 72.00", got.Price)
	}
	if got.Currency != "CNY" || got.ValidityHours != 720 {
		t.Fatalf("currency/validity = %s/%d, want CNY/720", got.Currency, got.ValidityHours)
	}
}

func TestMigrateCreditPackagesToCNYSeedsTrialPackage(t *testing.T) {
	db := openCreditPackageMigrationTestDB(t)
	if err := migrateCreditPackagesToCNY(db); err != nil {
		t.Fatalf("migration: %v", err)
	}

	var got models.TokenPackage
	if err := db.Where("name = ?", "trial_500").First(&got).Error; err != nil {
		t.Fatalf("load trial package: %v", err)
	}
	if got.TokenAmount+got.BonusTokens != 500 || got.Price != 1.90 || !got.IsTrial || got.PerAccountPurchaseLimit != 1 {
		t.Fatalf("unexpected trial package: %+v", got)
	}
}

func openCreditPackageMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("sqlite://file:credit-package-migration-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := Open(config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&models.TokenPackage{}, &models.SystemSetting{}); err != nil {
		t.Fatalf("auto migrate test schema: %v", err)
	}
	return db
}
