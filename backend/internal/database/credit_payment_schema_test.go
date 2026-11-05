package database

import (
	"testing"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
)

func TestAutoMigrateCreatesCreditPaymentConstraints(t *testing.T) {
	db := openMigrationTestDB(t)
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}

	assertIndexExists(t, db, "credit_payment_orders", "uq_credit_payment_orders_out_trade_no")
	assertIndexExists(t, db, "purchased_credit_lots", "uq_purchased_credit_lots_order_id")
	assertIndexExists(t, db, "credit_trial_claims", "uq_credit_trial_claims_user_id")
}

func TestTokenPackageValidityIsFixedAt720Hours(t *testing.T) {
	db := openMigrationTestDB(t)
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}

	pkg := models.TokenPackage{
		Base:          models.Base{ID: uuid.New()},
		Name:          "invalid-validity",
		DisplayName:   "Invalid validity",
		TokenAmount:   500,
		Price:         1.90,
		Currency:      "CNY",
		ValidityHours: 24,
	}
	if err := db.Create(&pkg).Error; err == nil {
		t.Fatal("expected database to reject validity_hours other than 720")
	}
}

func openMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := Open(config.Config{DatabaseURL: "sqlite://file:credit-payment-schema-migration?mode=memory&cache=shared"})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	return db
}

func assertIndexExists(t *testing.T, db *gorm.DB, table, index string) {
	t.Helper()

	if !db.Migrator().HasIndex(table, index) {
		t.Fatalf("%s missing %s", table, index)
	}
}
