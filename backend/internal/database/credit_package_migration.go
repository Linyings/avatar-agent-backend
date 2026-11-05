package database

import (
	"math"

	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const creditPackageCNYMigrationKey = "credit_packages_cny_v1"

func migrateCreditPackagesToCNY(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		description := "One-time USD to CNY credit package migration"
		marker := models.SystemSetting{
			Key:         creditPackageCNYMigrationKey,
			Value:       datatypes.JSON([]byte(`"completed"`)),
			Description: &description,
		}
		claim := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoNothing: true,
		}).Create(&marker)
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected == 0 {
			return nil
		}

		var packages []models.TokenPackage
		if err := tx.Unscoped().Find(&packages).Error; err != nil {
			return err
		}
		for i := range packages {
			packages[i].Price = math.Round(packages[i].Price*7.2*100) / 100
			packages[i].Currency = "CNY"
			packages[i].ValidityHours = 720
			if err := tx.Unscoped().Save(&packages[i]).Error; err != nil {
				return err
			}
		}

		trial := models.TokenPackage{
			Name:                    "trial_500",
			DisplayName:             "体验包",
			TokenAmount:             500,
			Price:                   1.90,
			Currency:                "CNY",
			ValidityHours:           720,
			IsTrial:                 true,
			PerAccountPurchaseLimit: 1,
			IsActive:                true,
		}
		trialValues := map[string]any{
			"display_name":               trial.DisplayName,
			"token_amount":               trial.TokenAmount,
			"bonus_tokens":               int64(0),
			"price":                      trial.Price,
			"currency":                   trial.Currency,
			"validity_hours":             trial.ValidityHours,
			"is_trial":                   trial.IsTrial,
			"per_account_purchase_limit": trial.PerAccountPurchaseLimit,
			"is_active":                  trial.IsActive,
		}
		if err := tx.Where("name = ?", trial.Name).Assign(trialValues).FirstOrCreate(&trial).Error; err != nil {
			return err
		}

		return nil
	})
}
