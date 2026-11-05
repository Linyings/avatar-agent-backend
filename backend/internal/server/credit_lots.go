package server

import (
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type purchasedCreditLotSummary struct {
	Total      int64
	Remaining  int64
	NextExpiry *time.Time
}

func expirePurchasedCreditLots(tx *gorm.DB, userID uuid.UUID, now time.Time) error {
	return tx.Transaction(func(expiryTx *gorm.DB) error {
		var lots []models.PurchasedCreditLot
		if err := expiryTx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND status = ? AND expires_at <= ?", userID, "active", now).
			Order("expires_at ASC, created_at ASC").
			Find(&lots).Error; err != nil {
			return err
		}
		for i := range lots {
			discarded := lots[i].RemainingCredits
			lots[i].RemainingCredits = 0
			lots[i].Status = "expired"
			lots[i].UpdatedAt = now
			if err := expiryTx.Save(&lots[i]).Error; err != nil {
				return err
			}
			if discarded > 0 {
				if err := createLotLedgerOnce(expiryTx, lots[i], creditLedgerEventExpiry, -discarded, now); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func createLotLedgerOnce(tx *gorm.DB, lot models.PurchasedCreditLot, eventType string, change int64, recordedAt time.Time) error {
	balanceAfter, err := grossRemainingCreditsAfterPurchasedLotCleanupTx(tx, lot.UserID, recordedAt)
	if err != nil {
		return err
	}
	item := "Purchased Credits expiry"
	reason := "Purchased credit lot expired"
	if eventType == creditLedgerEventRefund {
		item = "Purchased Credits refund"
		reason = "Unused purchased credits refunded"
	}
	record := models.CreditLedgerRecord{
		Base:            models.Base{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("purchased-credit-lot:"+lot.ID.String()+":"+eventType))},
		UserID:          lot.UserID,
		EventType:       eventType,
		Item:            item,
		ChangeAmount:    change,
		BalanceAfter:    balanceAfter,
		Reason:          reason,
		RequestMetadata: jsonMap(map[string]any{"lot_id": lot.ID.String(), "payment_order_id": lot.PaymentOrderID.String()}),
		RecordedAt:      recordedAt,
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoNothing: true,
	}).Create(&record).Error
}

func purchasedCreditSummary(tx *gorm.DB, userID uuid.UUID, now time.Time) (purchasedCreditLotSummary, error) {
	if err := expirePurchasedCreditLots(tx, userID, now); err != nil {
		return purchasedCreditLotSummary{}, err
	}
	var row struct {
		Total     int64
		Remaining int64
	}
	active := tx.Model(&models.PurchasedCreditLot{}).
		Where("user_id = ? AND status IN ? AND expires_at > ?", userID, []string{"active", "consumed"}, now).
		Select("coalesce(sum(granted_credits), 0) as total, coalesce(sum(remaining_credits), 0) as remaining")
	if err := active.Scan(&row).Error; err != nil {
		return purchasedCreditLotSummary{}, err
	}
	var next models.PurchasedCreditLot
	result := tx.Where("user_id = ? AND status = ? AND remaining_credits > 0 AND expires_at > ?", userID, "active", now).
		Order("expires_at ASC, created_at ASC").Limit(1).Find(&next)
	if result.Error != nil {
		return purchasedCreditLotSummary{}, result.Error
	}
	var nextExpiry *time.Time
	if result.RowsAffected > 0 {
		nextExpiry = &next.ExpiresAt
	}
	return purchasedCreditLotSummary{Total: row.Total, Remaining: row.Remaining, NextExpiry: nextExpiry}, nil
}

func purchasedCreditBalance(tx *gorm.DB, userID uuid.UUID, now time.Time) (int64, *time.Time, error) {
	summary, err := purchasedCreditSummary(tx, userID, now)
	if err != nil {
		return 0, nil, err
	}
	return summary.Remaining, summary.NextExpiry, nil
}

func consumePurchasedCreditLots(tx *gorm.DB, userID uuid.UUID, requested int64, now time.Time) (int64, error) {
	if requested <= 0 {
		return 0, nil
	}
	if err := expirePurchasedCreditLots(tx, userID, now); err != nil {
		return 0, err
	}
	var lots []models.PurchasedCreditLot
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ? AND status = ? AND remaining_credits > 0 AND expires_at > ?", userID, "active", now).
		Order("expires_at ASC, created_at ASC").Find(&lots).Error; err != nil {
		return 0, err
	}
	remaining := requested
	for i := range lots {
		take := lots[i].RemainingCredits
		if take > remaining {
			take = remaining
		}
		lots[i].RemainingCredits -= take
		remaining -= take
		if lots[i].RemainingCredits == 0 {
			lots[i].Status = "consumed"
		}
		if err := tx.Save(&lots[i]).Error; err != nil {
			return requested - remaining, err
		}
		if remaining == 0 {
			break
		}
	}
	return requested - remaining, nil
}
