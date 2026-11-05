package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type wechatCreditRefunder interface {
	RefundCreditPayment(context.Context, string, string, float64) error
}
type alipayCreditRefunder interface {
	RefundCreditPayment(context.Context, string, string, string) error
}

func revokePurchasedLotRemainder(tx *gorm.DB, orderID uuid.UUID, now time.Time) (int64, error) {
	var lot models.PurchasedCreditLot
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("payment_order_id = ?", orderID).First(&lot).Error; err != nil {
		return 0, err
	}
	if lot.Status == "refunded" {
		return 0, nil
	}
	revoked := lot.RemainingCredits
	lot.RemainingCredits, lot.Status, lot.RefundedAt = 0, "refunded", &now
	if err := tx.Save(&lot).Error; err != nil {
		return 0, err
	}
	if revoked > 0 {
		if err := createLotLedgerOnce(tx, lot, creditLedgerEventRefund, -revoked, now); err != nil {
			return 0, err
		}
	}
	return revoked, nil
}

func (a *App) refundCreditPayment(c *gin.Context) {
	var order models.CreditPaymentOrder
	if err := a.db.Where("out_trade_no = ?", c.Param("out_trade_no")).First(&order).Error; err != nil {
		fail(c, http.StatusNotFound, "payment order not found")
		return
	}
	if order.Status == "refunded" {
		ok(c, gin.H{"success": true, "revoked_credits": 0})
		return
	}
	if order.Status != "paid" && order.Status != "refund_required" {
		fail(c, http.StatusConflict, "payment order is not refundable")
		return
	}
	amount, _ := order.AmountCNY.Float64()
	refundNo := "RF" + order.OutTradeNo
	var err error
	if order.Provider == "wechat" {
		client, valid := a.wechat.(wechatCreditRefunder)
		if !valid {
			err = fmt.Errorf("wechat refund unavailable")
		} else {
			err = client.RefundCreditPayment(c.Request.Context(), order.OutTradeNo, refundNo, amount)
		}
	} else {
		client, valid := a.alipay.(alipayCreditRefunder)
		if !valid {
			err = fmt.Errorf("alipay refund unavailable")
		} else {
			err = client.RefundCreditPayment(c.Request.Context(), order.OutTradeNo, order.AmountCNY.StringFixed(2), refundNo)
		}
	}
	if err != nil {
		fail(c, http.StatusBadGateway, err.Error())
		return
	}
	now := time.Now().UTC()
	var revoked int64
	err = a.db.Transaction(func(tx *gorm.DB) error {
		var locked models.CreditPaymentOrder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", order.ID).Error; err != nil {
			return err
		}
		if locked.Status == "refunded" {
			return nil
		}
		if locked.Status == "paid" {
			var err error
			revoked, err = revokePurchasedLotRemainder(tx, locked.ID, now)
			if err != nil {
				return err
			}
		} else if locked.Status != "refund_required" {
			return fmt.Errorf("payment order is %s", locked.Status)
		}
		return tx.Model(&locked).Updates(map[string]any{"status": "refunded", "refunded_at": now}).Error
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	ok(c, gin.H{"success": true, "revoked_credits": revoked})
}
