package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type alipayCheckoutInput struct {
	PlanID       uuid.UUID `json:"plan_id" binding:"required"`
	BillingCycle string    `json:"billing_cycle"`
}

func (a *App) createAlipaySubscriptionCheckout(c *gin.Context) {
	user, exists := currentUserFromContext(c)
	if !exists {
		return
	}
	if !strings.EqualFold(a.cfg.SubscriptionPaymentProvider, "alipay") {
		paymentChannelError(c, http.StatusConflict, "payment_channel_disabled", "Alipay subscription checkout is disabled")
		return
	}
	if a.alipay == nil || !a.alipay.Configured() {
		paymentChannelError(c, http.StatusServiceUnavailable, "payment_channel_unavailable", "Alipay subscription is not configured")
		return
	}
	var input alipayCheckoutInput
	if err := c.ShouldBindJSON(&input); err != nil {
		fail(c, http.StatusBadRequest, "invalid checkout request")
		return
	}
	cycle := strings.ToLower(strings.TrimSpace(input.BillingCycle))
	if cycle == "" {
		cycle = billingCycleMonthly
	}
	if cycle != billingCycleMonthly {
		paymentChannelError(c, http.StatusBadRequest, "billing_cycle_unsupported", "Only monthly subscriptions are available")
		return
	}
	var plan models.SubscriptionPlan
	if err := a.db.First(&plan, "id = ? AND is_active = ?", input.PlanID, true).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, "subscription plan not found")
			return
		}
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	if !plan.SubscriptionProviderEnabled || strings.TrimSpace(plan.AlipayPlanID) == "" || strings.TrimSpace(plan.AlipayProductID) == "" {
		paymentChannelError(c, http.StatusConflict, "subscription_plan_unavailable", "subscription plan is not available through Alipay")
		return
	}
	if _, found, err := a.currentUserSubscription(user.ID); err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	} else if found {
		paymentChannelError(c, http.StatusConflict, "subscription_already_active", "user already has an effective subscription")
		return
	}

	requestID := uuid.NewString()
	checkout, err := a.alipay.CreateSubscription(c.Request.Context(), providers.AlipaySubscriptionRequest{
		RequestID: requestID, PlanID: plan.AlipayPlanID, ProductID: plan.AlipayProductID,
		Amount: fmt.Sprintf("%.2f", plan.PriceMonthly), UserID: user.ID.String(),
	})
	if err != nil {
		if errors.Is(err, providers.ErrAlipayCapabilityUnavailable) {
			paymentChannelError(c, http.StatusServiceUnavailable, "payment_channel_pending_approval", "Alipay subscription capability is pending approval")
			return
		}
		fail(c, http.StatusBadGateway, "Alipay checkout request failed")
		return
	}
	if checkout.RequestID != "" {
		requestID = checkout.RequestID
	}
	session := models.AlipaySubscriptionSession{
		UserID: user.ID, PlanID: plan.ID, RequestID: requestID, CheckoutURL: checkout.CheckoutURL,
		AlipayAgreementID: checkout.AgreementID, Status: "pending", Amount: plan.PriceMonthly, Currency: plan.Currency,
	}
	if err := a.db.Create(&session).Error; err != nil {
		fail(c, http.StatusInternalServerError, "could not persist Alipay checkout")
		return
	}
	ok(c, gin.H{"checkout_url": checkout.CheckoutURL, "request_id": requestID})
}

func (a *App) alipaySubscriptionWebhook(c *gin.Context) {
	if a.alipay == nil || !a.alipay.Configured() {
		c.String(http.StatusServiceUnavailable, "failure")
		return
	}
	if err := c.Request.ParseForm(); err != nil {
		c.String(http.StatusBadRequest, "failure")
		return
	}
	if err := a.alipay.VerifyNotification(c.Request.Form); err != nil {
		c.String(http.StatusBadRequest, "failure")
		return
	}
	eventID := firstFormValue(c, "notify_id", "event_id", "trade_no", "out_trade_no")
	if eventID == "" {
		c.String(http.StatusBadRequest, "failure")
		return
	}
	payload, _ := json.Marshal(c.Request.Form)
	event := models.PaymentWebhookEvent{
		Provider: "alipay", EventID: eventID, EventType: firstFormValue(c, "notify_type", "event_type"),
		ObjectID: firstFormValue(c, "agreement_id", "subscription_id", "trade_no"), SignatureValid: true,
		Payload: datatypes.JSON(payload), ProcessingStatus: "pending",
	}
	if event.EventType == "" {
		event.EventType = "unknown"
	}
	if err := a.db.Where("provider = ? AND event_id = ?", event.Provider, event.EventID).FirstOrCreate(&event).Error; err != nil {
		c.String(http.StatusInternalServerError, "failure")
		return
	}
	c.String(http.StatusOK, "success")
}

func (a *App) syncAlipaySubscription(c *gin.Context) {
	paymentChannelError(c, http.StatusServiceUnavailable, "payment_channel_pending_approval", "Alipay subscription capability is pending approval")
}

func (a *App) createAlipaySubscriptionPortal(c *gin.Context) {
	user, exists := currentUserFromContext(c)
	if !exists {
		return
	}
	sub, found, err := a.currentUserSubscription(user.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	if !found || sub.PaymentProvider != "alipay" || sub.AlipayAgreementID == "" {
		fail(c, http.StatusNotFound, "Alipay subscription not found")
		return
	}
	portalURL, err := a.alipay.CreatePortal(c.Request.Context(), sub.AlipayAgreementID)
	if err != nil {
		fail(c, http.StatusBadGateway, "Alipay portal request failed")
		return
	}
	ok(c, gin.H{"portal_url": portalURL})
}

func (a *App) cancelAlipaySubscription(c *gin.Context) {
	user, exists := currentUserFromContext(c)
	if !exists {
		return
	}
	sub, found, err := a.currentUserSubscription(user.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	if !found || sub.PaymentProvider != "alipay" || sub.AlipayAgreementID == "" {
		fail(c, http.StatusNotFound, "Alipay subscription not found")
		return
	}
	if err := a.alipay.CancelSubscription(c.Request.Context(), sub.AlipayAgreementID); err != nil {
		fail(c, http.StatusBadGateway, "Alipay cancellation request failed")
		return
	}
	now := time.Now().UTC()
	if err := a.db.Model(&sub).Updates(map[string]any{"cancel_at_period_end": true, "canceled_at": &now}).Error; err != nil {
		fail(c, http.StatusInternalServerError, err.Error())
		return
	}
	ok(c, gin.H{"status": "cancellation_scheduled"})
}

func (a *App) alipaySubscriptionReturn(c *gin.Context) {
	returnURL := strings.TrimRight(a.cfg.FrontendBaseURL, "/") + "/subscription?payment=alipay&status=pending"
	c.Redirect(http.StatusFound, returnURL)
}

func paymentChannelError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"detail": message, "code": code})
}

func firstFormValue(c *gin.Context, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(c.Request.Form.Get(key)); value != "" {
			return value
		}
	}
	return ""
}
