package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
	"github.com/shopspring/decimal"
)

type alipayCreditClient interface {
	PagePaymentURL(context.Context, providers.AlipayPagePaymentRequest) (string, error)
	PrecreateCreditPayment(context.Context, providers.AlipayPrecreateRequest) (providers.AlipayPrecreateResponse, error)
	QueryCreditPayment(context.Context, string) (providers.AlipayTransaction, error)
}

func (a *App) prepareAlipayCreditPayment(ctx context.Context, order *models.CreditPaymentOrder) error {
	if !a.alipay.Configured() {
		order.CodeURL = "https://qr.alipay.com/mock/" + order.OutTradeNo
		return a.db.Save(order).Error
	}
	client, ok := a.alipay.(alipayCreditClient)
	if !ok {
		return providers.ErrAlipayCapabilityUnavailable
	}
	if strings.EqualFold(strings.TrimSpace(a.cfg.AlipayPaymentMethod), "precreate") {
		resp, err := client.PrecreateCreditPayment(ctx, providers.AlipayPrecreateRequest{OutTradeNo: order.OutTradeNo, Subject: order.PackageName, TotalAmount: order.AmountCNY.StringFixed(2), ProductCode: "QR_CODE_OFFLINE"})
		if err != nil {
			return err
		}
		order.CodeURL = resp.QRCode
		return a.db.Save(order).Error
	}
	paymentURL, err := client.PagePaymentURL(ctx, providers.AlipayPagePaymentRequest{OutTradeNo: order.OutTradeNo, Subject: order.PackageName, TotalAmount: order.AmountCNY.StringFixed(2)})
	if err != nil {
		return err
	}
	order.CodeURL = paymentURL
	return a.db.Save(order).Error
}

func (a *App) syncAlipayCreditPayment(ctx context.Context, order *models.CreditPaymentOrder) error {
	if !a.alipay.Configured() {
		return nil
	}
	client, ok := a.alipay.(alipayCreditClient)
	if !ok {
		return providers.ErrAlipayCapabilityUnavailable
	}
	tx, err := client.QueryCreditPayment(ctx, order.OutTradeNo)
	if err != nil {
		return err
	}
	if strings.TrimSpace(tx.OutTradeNo) != order.OutTradeNo {
		return fmt.Errorf("%w: Alipay order number mismatch", ErrInvalidPaymentEvidence)
	}
	payload, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	return a.processCreditAlipayTransaction(tx, payload)
}

func parseAlipayPaymentTime(value string) (time.Time, error) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.Time{}, fmt.Errorf("load Asia/Shanghai timezone: %w", err)
	}
	paidAt, err := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(value), location)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: invalid Alipay payment time: %v", ErrInvalidPaymentEvidence, err)
	}
	return paidAt.UTC(), nil
}

func (a *App) processCreditAlipayTransaction(tx providers.AlipayTransaction, payload []byte) error {
	if strings.TrimSpace(tx.AppID) == "" || strings.TrimSpace(a.cfg.AlipayAppID) == "" || tx.AppID != strings.TrimSpace(a.cfg.AlipayAppID) {
		return fmt.Errorf("%w: Alipay app_id mismatch", ErrInvalidPaymentEvidence)
	}
	if strings.TrimSpace(tx.SellerID) == "" || strings.TrimSpace(a.cfg.AlipayMerchantID) == "" || tx.SellerID != strings.TrimSpace(a.cfg.AlipayMerchantID) {
		return fmt.Errorf("%w: Alipay seller_id mismatch", ErrInvalidPaymentEvidence)
	}
	if tx.Status != "TRADE_SUCCESS" && tx.Status != "TRADE_FINISHED" {
		return fmt.Errorf("%w: Alipay trade status is not successful", ErrInvalidPaymentEvidence)
	}
	if strings.TrimSpace(tx.OutTradeNo) == "" || strings.TrimSpace(tx.TransactionID) == "" {
		return fmt.Errorf("%w: Alipay order or transaction id is missing", ErrInvalidPaymentEvidence)
	}
	amount, err := decimal.NewFromString(strings.TrimSpace(tx.Amount))
	if err != nil || !amount.IsPositive() {
		return fmt.Errorf("%w: invalid Alipay paid amount", ErrInvalidPaymentEvidence)
	}
	paidAt, err := parseAlipayPaymentTime(tx.PaymentTime)
	if err != nil {
		return err
	}
	_, err = settleCreditPaymentOrder(a.db, verifiedCreditPayment{
		Provider: "alipay", OutTradeNo: tx.OutTradeNo, TransactionID: tx.TransactionID,
		MerchantID: tx.SellerID, AmountCNY: amount, PaidAt: paidAt, Payload: payload,
	})
	return err
}

func (a *App) alipayCreditPaymentWebhook(c *gin.Context) {
	if err := c.Request.ParseForm(); err != nil {
		c.String(http.StatusBadRequest, "failure")
		return
	}
	values := url.Values(c.Request.PostForm)
	if err := a.alipay.VerifyNotification(values); err != nil {
		c.String(http.StatusUnauthorized, "failure")
		return
	}
	tx := providers.AlipayTransaction{
		AppID: values.Get("app_id"), SellerID: values.Get("seller_id"),
		OutTradeNo: values.Get("out_trade_no"), TransactionID: values.Get("trade_no"),
		Status: values.Get("trade_status"), Amount: values.Get("total_amount"),
		PaymentTime: values.Get("gmt_payment"),
	}
	if err := a.processCreditAlipayTransaction(tx, []byte(values.Encode())); err != nil {
		c.String(creditPaymentErrorHTTPStatus(err), "failure")
		return
	}
	c.String(http.StatusOK, "success")
}
