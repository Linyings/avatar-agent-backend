package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type createCreditPaymentRequest struct {
	PackageID uuid.UUID `json:"package_id"`
	Provider  string    `json:"provider"`
}

func creditPaymentJSON(order models.CreditPaymentOrder) gin.H {
	return gin.H{"out_trade_no": order.OutTradeNo, "provider": order.Provider, "status": order.Status, "amount_cny": order.AmountCNY.StringFixed(2), "credits": order.Credits, "code_url": order.CodeURL, "paid_at": order.PaidAt, "expires_at": order.ExpiresAt}
}

func (a *App) createCreditPayment(c *gin.Context) {
	var req createCreditPaymentRequest
	if !bindJSON(c, &req) {
		return
	}
	user := getUser(c)
	order, err := createCreditPaymentOrder(a.db, user.ID, req.PackageID, req.Provider, time.Now().UTC())
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if order.Provider == "wechat" {
		codeURL := "weixin://wxpay/mock/" + order.OutTradeNo
		if a.wechat.Configured() {
			amountCNY, _ := order.AmountCNY.Float64()
			resp, err := a.wechat.CreateNativeOrder(c.Request.Context(), providers.WeChatNativeOrderRequest{Description: order.PackageName, OutTradeNo: order.OutTradeNo, TotalAmount: amountCNY, Attach: order.ID.String(), TimeExpire: time.Now().UTC().Add(30 * time.Minute)})
			if err != nil {
				a.db.Model(order).Update("status", "failed")
				fail(c, http.StatusBadGateway, err.Error())
				return
			}
			codeURL = resp.CodeURL
		}
		order.CodeURL = codeURL
		if err := a.db.Save(order).Error; err != nil {
			fail(c, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		if err := a.prepareAlipayCreditPayment(c.Request.Context(), order); err != nil {
			a.db.Model(order).Update("status", "failed")
			fail(c, http.StatusBadGateway, err.Error())
			return
		}
	}
	created(c, creditPaymentJSON(*order))
}

func (a *App) creditPaymentStatus(c *gin.Context) {
	user := getUser(c)
	var order models.CreditPaymentOrder
	if err := a.db.Where("out_trade_no = ? AND user_id = ?", c.Param("out_trade_no"), user.ID).First(&order).Error; err != nil {
		fail(c, http.StatusNotFound, "payment order not found")
		return
	}
	if order.Status == "pending" {
		if order.Provider == "wechat" && a.wechat.Configured() {
			if tx, err := a.wechat.QueryTransactionByOutTradeNo(c.Request.Context(), order.OutTradeNo); err == nil {
				_ = a.processCreditWeChatTransaction(tx)
			}
		} else if order.Provider == "alipay" {
			_ = a.syncAlipayCreditPayment(c.Request.Context(), &order)
		}
		_ = a.db.Where("id = ?", order.ID).First(&order).Error
	}
	ok(c, creditPaymentJSON(order))
}

func (a *App) wechatCreditPaymentWebhook(c *gin.Context) {
	body, _ := io.ReadAll(c.Request.Body)
	if len(body) == 0 {
		fail(c, http.StatusBadRequest, "empty notification")
		return
	}
	var tx providers.WeChatTransaction
	if a.wechat.Configured() {
		if err := a.wechat.VerifyNotificationSignature(c.GetHeader("Wechatpay-Timestamp"), c.GetHeader("Wechatpay-Nonce"), body, c.GetHeader("Wechatpay-Signature"), c.GetHeader("Wechatpay-Serial")); err != nil {
			fail(c, http.StatusUnauthorized, err.Error())
			return
		}
		var err error
		tx, err = a.wechat.DecryptNotification(body)
		if err != nil {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
	} else if err := json.Unmarshal(body, &tx); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.processCreditWeChatTransaction(tx); err != nil {
		fail(c, creditPaymentErrorHTTPStatus(err), err.Error())
		return
	}
	ok(c, gin.H{"code": "SUCCESS", "message": "success"})
}

func creditPaymentErrorHTTPStatus(err error) int {
	if errors.Is(err, ErrInvalidPaymentEvidence) ||
		errors.Is(err, ErrPaymentProviderMismatch) ||
		errors.Is(err, ErrPaymentAmountMismatch) ||
		errors.Is(err, gorm.ErrRecordNotFound) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

func (a *App) processCreditWeChatTransaction(tx providers.WeChatTransaction) error {
	if strings.TrimSpace(tx.TradeState) != "SUCCESS" {
		return fmt.Errorf("%w: WeChat trade state is not SUCCESS", ErrInvalidPaymentEvidence)
	}
	if strings.TrimSpace(tx.AppID) == "" || strings.TrimSpace(a.cfg.WeChatAppID) == "" || tx.AppID != strings.TrimSpace(a.cfg.WeChatAppID) {
		return fmt.Errorf("%w: WeChat appid mismatch", ErrInvalidPaymentEvidence)
	}
	if strings.TrimSpace(tx.MchID) == "" || strings.TrimSpace(a.cfg.WeChatMchID) == "" || tx.MchID != strings.TrimSpace(a.cfg.WeChatMchID) {
		return fmt.Errorf("%w: WeChat merchant mismatch", ErrInvalidPaymentEvidence)
	}
	if strings.TrimSpace(tx.OutTradeNo) == "" {
		return fmt.Errorf("%w: WeChat order number is missing", ErrInvalidPaymentEvidence)
	}
	if strings.TrimSpace(tx.TransactionID) == "" {
		return fmt.Errorf("%w: WeChat transaction id is missing", ErrInvalidPaymentEvidence)
	}
	if tx.Amount.Total <= 0 {
		return fmt.Errorf("%w: WeChat paid amount must be positive", ErrInvalidPaymentEvidence)
	}
	if strings.TrimSpace(tx.Amount.Currency) != "CNY" {
		return fmt.Errorf("%w: WeChat currency must be CNY", ErrInvalidPaymentEvidence)
	}
	paidAt, err := time.Parse(time.RFC3339, strings.TrimSpace(tx.SuccessTime))
	if err != nil {
		return fmt.Errorf("%w: invalid WeChat success_time: %v", ErrInvalidPaymentEvidence, err)
	}
	var order models.CreditPaymentOrder
	if err := a.db.Where("out_trade_no = ? AND provider = ?", tx.OutTradeNo, "wechat").First(&order).Error; err != nil {
		return err
	}
	amount := decimal.NewFromInt(int64(tx.Amount.Total)).Div(decimal.NewFromInt(100))
	payload, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	_, err = settleCreditPaymentOrder(a.db, verifiedCreditPayment{
		Provider: "wechat", OutTradeNo: order.OutTradeNo, TransactionID: tx.TransactionID,
		MerchantID: tx.MchID, AmountCNY: amount, PaidAt: paidAt.UTC(), Payload: payload,
	})
	return err
}
