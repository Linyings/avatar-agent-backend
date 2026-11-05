package server

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrUnsupportedPaymentProvider = errors.New("unsupported payment provider")
	ErrTrialAlreadyPurchased      = errors.New("trial package already purchased")
	ErrPaymentProviderMismatch    = errors.New("payment provider mismatch")
	ErrPaymentAmountMismatch      = errors.New("payment amount mismatch")
	ErrInvalidPaymentEvidence     = errors.New("invalid payment provider evidence")
)

type verifiedCreditPayment struct {
	Provider      string
	OutTradeNo    string
	TransactionID string
	MerchantID    string
	AmountCNY     decimal.Decimal
	PaidAt        time.Time
	Payload       []byte
}

func creditTrialClaimed(db *gorm.DB, userID uuid.UUID) (bool, error) {
	var count int64
	if err := db.Model(&models.CreditTrialClaim{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func createCreditPaymentOrder(db *gorm.DB, userID, packageID uuid.UUID, provider string, now time.Time) (*models.CreditPaymentOrder, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider != "wechat" && provider != "alipay" {
		return nil, ErrUnsupportedPaymentProvider
	}
	var pkg models.TokenPackage
	if err := db.Where("id = ? AND is_active = ?", packageID, true).First(&pkg).Error; err != nil {
		return nil, err
	}
	if pkg.Currency != "CNY" || pkg.ValidityHours != 720 {
		return nil, fmt.Errorf("credit package must use CNY and 720-hour validity")
	}
	if pkg.IsTrial {
		claimed, err := creditTrialClaimed(db, userID)
		if err != nil {
			return nil, err
		}
		if claimed {
			return nil, ErrTrialAlreadyPurchased
		}
	}
	order := models.CreditPaymentOrder{
		UserID: userID, PackageID: pkg.ID, Provider: provider,
		OutTradeNo: fmt.Sprintf("CR%s%s", now.UTC().Format("20060102150405"), strings.ReplaceAll(uuid.NewString(), "-", "")[:16]),
		Status:     "pending", AmountCNY: decimal.NewFromFloat(pkg.Price).Round(2), Credits: pkg.TokenAmount + pkg.BonusTokens,
		PackageName: pkg.DisplayName, IsTrial: pkg.IsTrial,
	}
	if err := db.Create(&order).Error; err != nil {
		return nil, err
	}
	return &order, nil
}

func settleCreditPaymentOrder(db *gorm.DB, paid verifiedCreditPayment) (*models.PurchasedCreditLot, error) {
	return settleCreditPaymentOrderAt(db, paid, time.Now().UTC())
}

func settleCreditPaymentOrderAt(db *gorm.DB, paid verifiedCreditPayment, processingAt time.Time) (*models.PurchasedCreditLot, error) {
	paid.Provider = strings.ToLower(strings.TrimSpace(paid.Provider))
	paid.OutTradeNo = strings.TrimSpace(paid.OutTradeNo)
	paid.TransactionID = strings.TrimSpace(paid.TransactionID)
	if paid.Provider == "" || paid.OutTradeNo == "" || paid.TransactionID == "" || !paid.AmountCNY.IsPositive() || paid.PaidAt.IsZero() {
		return nil, ErrInvalidPaymentEvidence
	}
	paid.PaidAt = paid.PaidAt.UTC()
	processingAt = processingAt.UTC()
	if processingAt.IsZero() {
		return nil, ErrInvalidPaymentEvidence
	}
	var lot *models.PurchasedCreditLot
	err := db.Transaction(func(tx *gorm.DB) error {
		var order models.CreditPaymentOrder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("out_trade_no = ?", paid.OutTradeNo).First(&order).Error; err != nil {
			return err
		}
		if order.Provider != strings.ToLower(strings.TrimSpace(paid.Provider)) {
			return ErrPaymentProviderMismatch
		}
		if !order.AmountCNY.Equal(paid.AmountCNY) {
			return ErrPaymentAmountMismatch
		}
		if order.Status == "paid" || order.Status == "refunded" {
			var existing models.PurchasedCreditLot
			if err := tx.Where("payment_order_id = ?", order.ID).First(&existing).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) && order.Status == "refunded" {
					loser, lookupErr := refundedTrialLoserWithoutLot(tx, order)
					if lookupErr != nil {
						return lookupErr
					}
					if loser {
						return nil
					}
				}
				return err
			}
			lot = &existing
			return nil
		}
		if order.Status == "refund_required" {
			return nil
		}
		if order.Status != "pending" {
			return fmt.Errorf("payment order is %s", order.Status)
		}
		if order.IsTrial {
			claim := models.CreditTrialClaim{UserID: order.UserID, PaymentOrderID: order.ID}
			result := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "user_id"}},
				DoNothing: true,
			}).Create(&claim)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				updates := map[string]any{
					"status":                  "refund_required",
					"provider_transaction_id": paid.TransactionID,
					"paid_at":                 paid.PaidAt,
					"provider_payload":        datatypes.JSON(paid.Payload),
				}
				return tx.Model(&order).Updates(updates).Error
			}
		}
		expiresAt := paid.PaidAt.UTC().Add(720 * time.Hour)
		createdLot := models.PurchasedCreditLot{UserID: order.UserID, PaymentOrderID: order.ID, GrantedCredits: order.Credits, RemainingCredits: order.Credits, Status: "active", ExpiresAt: expiresAt}
		if err := tx.Create(&createdLot).Error; err != nil {
			return err
		}
		lot = &createdLot
		return persistPaidOrderAndAccounting(tx, &order, &createdLot, paid, processingAt)
	})
	return lot, err
}

func refundedTrialLoserWithoutLot(tx *gorm.DB, order models.CreditPaymentOrder) (bool, error) {
	if !order.IsTrial {
		return false, nil
	}
	var count int64
	if err := tx.Model(&models.CreditTrialClaim{}).
		Where("user_id = ? AND payment_order_id <> ?", order.UserID, order.ID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func persistPaidOrderAndAccounting(tx *gorm.DB, order *models.CreditPaymentOrder, lot *models.PurchasedCreditLot, paid verifiedCreditPayment, processingAt time.Time) error {
	order.Status, order.ProviderTransactionID, order.PaidAt, order.ExpiresAt = "paid", paid.TransactionID, &paid.PaidAt, &lot.ExpiresAt
	if paid.Payload != nil {
		order.ProviderPayload = datatypes.JSON(paid.Payload)
	}
	if err := tx.Save(order).Error; err != nil {
		return err
	}
	price, _ := order.AmountCNY.Float64()
	record := models.TokenRechargeRecord{UserID: order.UserID, TokenAmount: order.Credits, Price: price, PaymentMethod: order.Provider, PaymentID: &order.ID, PackageID: &order.PackageID}
	if err := tx.Create(&record).Error; err != nil {
		return err
	}
	balanceAfter, err := grossRemainingCreditsAfterPurchasedLotCleanupTx(tx, order.UserID, processingAt)
	if err != nil {
		return err
	}
	ledger := models.CreditLedgerRecord{
		UserID: order.UserID, EventType: creditLedgerEventRecharge, Item: order.PackageName,
		ChangeAmount: order.Credits, BalanceAfter: balanceAfter,
		Reason: "Credits 套餐购买", RecordedAt: processingAt,
		RequestMetadata: datatypes.JSON([]byte(fmt.Sprintf(`{"payment_order_id":%q,"provider":%q}`, order.ID.String(), order.Provider))),
	}
	return tx.Create(&ledger).Error
}
