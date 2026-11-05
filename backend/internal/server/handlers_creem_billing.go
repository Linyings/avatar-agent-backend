package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const (
	creemPurposeSubscription = "subscription"
	creemPurposeCreditPack   = "credit_purchase"
	billingCycleMonthly      = "monthly"
	billingCycleYearly       = "yearly"
	firstMonthPromoCode      = "FIRSTMONTH"
	firstMonthPromoPrice     = 9.9

	subscriptionStatusActive          = "active"
	subscriptionStatusTrialing        = "trialing"
	subscriptionStatusScheduledCancel = "scheduled_cancel"
	subscriptionStatusPastDue         = "past_due"
	subscriptionStatusCanceled        = "canceled"
	subscriptionStatusPaused          = "paused"
	subscriptionStatusExpired         = "expired"
	subscriptionStatusRefunded        = "refunded"
	subscriptionStatusDisputed        = "disputed"
)

type creditBalanceSnapshot struct {
	PurchasedTotal        int64
	PurchasedUsed         int64
	PurchasedRemaining    int64
	SubscriptionTotal     int64
	SubscriptionUsed      int64
	SubscriptionRemaining int64
	ReservedCredits       int64
	GrossRemainingCredits int64
	TotalCredits          int64
	UsedCredits           int64
	RemainingCredits      int64
	NextExpiryAt          *time.Time
}

func (a *App) createSubscriptionCheckout(c *gin.Context) {
	if !a.cfg.NewCreemCheckoutEnabled() {
		paymentChannelError(c, http.StatusConflict, "payment_channel_disabled", "Creem subscription checkout is disabled")
		return
	}
	user := getUser(c)
	var req struct {
		PlanID       string `json:"plan_id"`
		PlanName     string `json:"plan_name"`
		BillingCycle string `json:"billing_cycle"`
	}
	if !bindJSON(c, &req) {
		return
	}
	plan, okPlan := a.findSubscriptionPlan(c, req.PlanID, req.PlanName, true)
	if !okPlan {
		return
	}
	cycle := normalizeBillingCycle(req.BillingCycle)
	productID := plan.CreemProductIDMonthly
	amount := plan.PriceMonthly
	if cycle == billingCycleYearly {
		productID = plan.CreemProductIDYearly
		amount = plan.PriceYearly
	}
	if productID == "" {
		fail(c, http.StatusBadRequest, "订阅计划未配置 Creem product_id")
		return
	}
	if !a.creem.Configured() {
		fail(c, http.StatusServiceUnavailable, "Creem 配置不完整")
		return
	}
	if sub, hasSub, err := a.currentUserSubscription(user.ID); err != nil {
		fail(c, http.StatusInternalServerError, "查询当前订阅失败")
		return
	} else if hasSub {
		if sub.CreemProductID == productID && sub.BillingCycle == cycle && sub.PlanID == plan.ID && sub.Status == subscriptionStatusActive {
			ok(c, gin.H{"success": true, "action": "unchanged", "mode": "creem_subscription", "message": "当前已经是该订阅计划", "subscription": a.serializeUserSubscription(sub), "plan": serializeSubscriptionPlan(plan), "billing_cycle": cycle})
			return
		}
		if sub.CreemSubscriptionID == "" {
			fail(c, http.StatusConflict, "当前订阅缺少 Creem subscription_id，无法通过 Creem 升级套餐")
			return
		}
		const updateBehavior = "proration-charge-immediately"
		upgradeObject, err := a.creem.UpgradeSubscription(c.Request.Context(), sub.CreemSubscriptionID, productID, updateBehavior)
		if err != nil {
			fail(c, http.StatusBadGateway, err.Error())
			return
		}
		updated, err := a.applyImmediateSubscriptionUpgrade(user.ID, sub, plan, cycle, productID, objectMap(upgradeObject))
		if err != nil {
			fail(c, http.StatusInternalServerError, "同步 Creem 订阅升级失败: "+err.Error())
			return
		}
		ok(c, gin.H{"success": true, "action": "upgraded", "mode": "creem_subscription", "message": "订阅已升级", "update_behavior": updateBehavior, "subscription": a.serializeUserSubscription(updated), "plan": serializeSubscriptionPlan(plan), "billing_cycle": cycle})
		return
	}

	requestID := "sub_" + uuid.NewString()
	metadata := gin.H{
		"purpose":       creemPurposeSubscription,
		"user_id":       user.ID.String(),
		"plan_id":       plan.ID.String(),
		"plan_name":     plan.Name,
		"billing_cycle": cycle,
	}
	discountCode := subscriptionCheckoutDiscountCode(plan, cycle)
	if discountCode != "" {
		metadata["discount_code"] = discountCode
	}
	checkout, err := a.creem.CreateCheckout(c.Request.Context(), providers.CreemCheckoutRequest{
		ProductID:    productID,
		RequestID:    requestID,
		DiscountCode: discountCode,
		SuccessURL:   a.cfg.CreemSuccessURL,
		Customer:     &providers.CreemCustomer{Email: user.Email},
		Metadata:     metadata,
	})
	if err != nil {
		fail(c, http.StatusBadGateway, err.Error())
		return
	}

	session := models.CreemCheckoutSession{
		UserID:          user.ID,
		Purpose:         creemPurposeSubscription,
		RequestID:       requestID,
		CreemCheckoutID: checkout.ID,
		CheckoutURL:     checkout.CheckoutURL,
		CreemProductID:  productID,
		PlanID:          &plan.ID,
		BillingCycle:    cycle,
		Status:          "pending",
		Amount:          amount,
		Currency:        plan.Currency,
		Metadata:        jsonMap(metadata),
	}
	if err := a.db.Create(&session).Error; err != nil {
		fail(c, http.StatusInternalServerError, "保存 Creem checkout 失败")
		return
	}
	ok(c, gin.H{"success": true, "action": "checkout", "checkout_id": checkout.ID, "checkout_url": checkout.CheckoutURL, "request_id": requestID, "plan": serializeSubscriptionPlan(plan), "billing_cycle": cycle})
}

func subscriptionCheckoutDiscountCode(plan models.SubscriptionPlan, cycle string) string {
	if cycle == billingCycleMonthly && math.Abs(plan.PriceMonthly-firstMonthPromoPrice) < 0.001 {
		return firstMonthPromoCode
	}
	return ""
}

func (a *App) createSubscriptionPortal(c *gin.Context) {
	user := getUser(c)
	sub, hasSub, err := a.currentUserSubscription(user.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询订阅失败")
		return
	}
	if !hasSub || sub.CreemCustomerID == "" {
		fail(c, http.StatusNotFound, "当前用户没有可管理的 Creem 订阅")
		return
	}
	portalURL, err := a.creem.CreateCustomerPortal(c.Request.Context(), sub.CreemCustomerID)
	if err != nil {
		fail(c, http.StatusBadGateway, err.Error())
		return
	}
	ok(c, gin.H{"success": true, "portal_url": portalURL, "return_url": a.cfg.CreemPortalReturnURL})
}

func (a *App) syncCreemCheckout(c *gin.Context) {
	user := getUser(c)
	var req struct {
		CheckoutID     string `json:"checkout_id"`
		OrderID        string `json:"order_id"`
		CustomerID     string `json:"customer_id"`
		ProductID      string `json:"product_id"`
		SubscriptionID string `json:"subscription_id"`
		RequestID      string `json:"request_id"`
		Signature      string `json:"signature"`
	}
	if !bindJSON(c, &req) {
		return
	}
	req.CheckoutID = strings.TrimSpace(req.CheckoutID)
	req.RequestID = strings.TrimSpace(req.RequestID)
	if req.CheckoutID == "" {
		fail(c, http.StatusBadRequest, "缺少 Creem checkout_id")
		return
	}

	var session models.CreemCheckoutSession
	if err := a.db.First(&session, "creem_checkout_id = ?", req.CheckoutID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			fail(c, http.StatusNotFound, "Creem checkout session 不存在")
			return
		}
		fail(c, http.StatusInternalServerError, "查询 Creem checkout session 失败")
		return
	}
	if session.UserID != user.ID {
		fail(c, http.StatusForbidden, "无权同步该 Creem checkout")
		return
	}
	if req.RequestID != "" && session.RequestID != "" && req.RequestID != session.RequestID {
		fail(c, http.StatusBadRequest, "Creem request_id 与本地订单不匹配")
		return
	}
	if session.Status != "completed" {
		if !a.creem.Configured() {
			fail(c, http.StatusServiceUnavailable, "Creem 配置不完整")
			return
		}
		params := creemRedirectParams(req.CheckoutID, req.OrderID, req.CustomerID, req.ProductID, req.SubscriptionID, req.RequestID, req.Signature)
		signatureValid := verifyCreemRedirectSignature(params, a.cfg.CreemAPIKey)

		redirectObject := creemCheckoutObjectFromRedirect(req.CheckoutID, req.OrderID, req.CustomerID, req.ProductID, req.SubscriptionID, req.RequestID)
		object := redirectObject
		needsCheckoutLookup := !signatureValid || req.ProductID == "" || session.Purpose == creemPurposeSubscription && req.SubscriptionID == ""
		if needsCheckoutLookup {
			checkout, err := a.creem.GetCheckout(c.Request.Context(), req.CheckoutID)
			if err != nil {
				fail(c, http.StatusBadGateway, err.Error())
				return
			}
			checkoutObject := creemCheckoutObjectFromAPI(checkout)
			if !signatureValid && !creemCheckoutIsCompleted(checkoutObject) {
				fail(c, http.StatusConflict, "Creem checkout 尚未完成，请稍后刷新")
				return
			}
			object = mergeCreemCheckoutObjects(checkoutObject, redirectObject)
		}
		if err := a.processCreemCheckoutCompleted("redirect:"+req.CheckoutID, gin.H{"object": object}); err != nil {
			fail(c, http.StatusInternalServerError, err.Error())
			return
		}
	}

	account, snapshot, err := a.creditBalanceSnapshot(user.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "刷新 Credits 余额失败")
		return
	}
	var subscriptionPayload any
	if sub, hasSub, err := a.currentUserSubscription(user.ID); err == nil && hasSub {
		subscriptionPayload = a.serializeUserSubscription(sub)
	} else {
		subscriptionPayload = nil
	}
	ok(c, gin.H{
		"success":                 true,
		"synced":                  true,
		"purpose":                 session.Purpose,
		"checkout_id":             req.CheckoutID,
		"remaining_credits":       snapshot.RemainingCredits,
		"reserved_credits":        snapshot.ReservedCredits,
		"gross_remaining_credits": snapshot.GrossRemainingCredits,
		"purchased_credits":       snapshot.PurchasedRemaining,
		"subscription_credits":    snapshot.SubscriptionRemaining,
		"token_account":           account,
		"subscription":            subscriptionPayload,
	})
}

func (a *App) createCreemCreditRecharge(c *gin.Context) {
	if !a.cfg.NewCreemCheckoutEnabled() {
		paymentChannelError(c, http.StatusConflict, "payment_channel_disabled", "Creem Credits checkout is disabled")
		return
	}
	user := getUser(c)
	var req struct {
		PackageID string `json:"package_id" binding:"required"`
	}
	if !bindJSON(c, &req) {
		return
	}
	pkgID, err := uuid.Parse(strings.TrimSpace(req.PackageID))
	if err != nil {
		fail(c, http.StatusBadRequest, "无效的套餐ID")
		return
	}
	var pkg models.TokenPackage
	if err := a.db.First(&pkg, "id = ? AND is_active = ?", pkgID, true).Error; err != nil {
		fail(c, http.StatusNotFound, "套餐不存在")
		return
	}
	if !pkg.CreemEnabled || pkg.CreemProductID == nil || strings.TrimSpace(*pkg.CreemProductID) == "" {
		fail(c, http.StatusBadRequest, "该 Credits 套餐未启用 Creem 支付")
		return
	}
	if !a.creem.Configured() {
		fail(c, http.StatusServiceUnavailable, "Creem 配置不完整")
		return
	}

	requestID := "credit_" + uuid.NewString()
	productID := strings.TrimSpace(*pkg.CreemProductID)
	metadata := gin.H{
		"purpose":      creemPurposeCreditPack,
		"user_id":      user.ID.String(),
		"package_id":   pkg.ID.String(),
		"package_name": pkg.Name,
	}
	checkout, err := a.creem.CreateCheckout(c.Request.Context(), providers.CreemCheckoutRequest{
		ProductID:  productID,
		RequestID:  requestID,
		SuccessURL: a.cfg.CreemSuccessURL,
		Customer:   &providers.CreemCustomer{Email: user.Email},
		Metadata:   metadata,
	})
	if err != nil {
		fail(c, http.StatusBadGateway, err.Error())
		return
	}
	session := models.CreemCheckoutSession{
		UserID:          user.ID,
		Purpose:         creemPurposeCreditPack,
		RequestID:       requestID,
		CreemCheckoutID: checkout.ID,
		CheckoutURL:     checkout.CheckoutURL,
		CreemProductID:  productID,
		PackageID:       &pkg.ID,
		Status:          "pending",
		Amount:          pkg.Price,
		Currency:        defaultString(pkg.CreemCurrency, "USD"),
		Metadata:        jsonMap(metadata),
	}
	if err := a.db.Create(&session).Error; err != nil {
		fail(c, http.StatusInternalServerError, "保存 Creem checkout 失败")
		return
	}
	ok(c, gin.H{"success": true, "checkout_id": checkout.ID, "checkout_url": checkout.CheckoutURL, "request_id": requestID, "package_info": tokenPackageResponses([]models.TokenPackage{pkg})[0], "total_amount": pkg.Price})
}

func (a *App) creemWebhook(c *gin.Context) {
	body, _ := c.GetRawData()
	if len(body) == 0 {
		fail(c, http.StatusBadRequest, "空回调请求")
		return
	}
	if err := a.creem.VerifyWebhookSignature(body, c.GetHeader("creem-signature")); err != nil {
		fail(c, http.StatusUnauthorized, err.Error())
		return
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		fail(c, http.StatusBadRequest, "Creem webhook JSON 无效")
		return
	}
	eventID := creemStringValue(firstPresent(payload, "id", "event_id"))
	if eventID == "" {
		sum := sha256.Sum256(body)
		eventID = "payload_" + hex.EncodeToString(sum[:])
	}
	eventType := creemStringValue(firstPresent(payload, "eventType", "event_type", "type"))
	if eventType == "" {
		fail(c, http.StatusBadRequest, "Creem webhook 缺少事件类型")
		return
	}

	now := time.Now().UTC()
	webhookEvent := models.CreemWebhookEvent{EventID: eventID, EventType: eventType, Payload: datatypes.JSON(body)}
	if err := a.db.Where("event_id = ?", eventID).FirstOrCreate(&webhookEvent).Error; err != nil {
		fail(c, http.StatusInternalServerError, "记录 Creem webhook 失败")
		return
	}
	if webhookEvent.ProcessedAt != nil {
		ok(c, gin.H{"success": true, "duplicate": true})
		return
	}

	processErr := a.processCreemEvent(eventID, eventType, payload)
	if processErr != nil {
		message := processErr.Error()
		a.db.Model(&webhookEvent).Updates(map[string]any{"processing_error": message})
		fail(c, http.StatusInternalServerError, message)
		return
	}
	a.db.Model(&webhookEvent).Updates(map[string]any{"processed_at": now, "processing_error": nil})
	ok(c, gin.H{"success": true})
}

func (a *App) processCreemEvent(eventID, eventType string, payload map[string]any) error {
	switch eventType {
	case "checkout.completed":
		return a.processCreemCheckoutCompleted(eventID, payload)
	case "subscription.paid":
		return a.processCreemSubscriptionPaid(eventID, payload)
	case "subscription.active", "subscription.trialing", "subscription.update", "subscription.updated", "subscription.canceled", "subscription.cancelled", "subscription.scheduled_cancel", "subscription.past_due", "subscription.expired", "subscription.paused":
		return a.syncCreemSubscriptionStatus(eventType, payload)
	case "refund.created", "dispute.created":
		return a.processCreemPaymentReversal(eventID, eventType, payload)
	default:
		return nil
	}
}

func (a *App) processCreemCheckoutCompleted(eventID string, payload map[string]any) error {
	object := objectMap(payload)
	return a.db.Transaction(func(tx *gorm.DB) error {
		session, hasSession, err := a.findCreemCheckoutSession(tx, object)
		if err != nil || !hasSession {
			return err
		}
		if session.Status == "completed" && session.Purpose == creemPurposeCreditPack {
			return nil
		}
		updateCreemSessionFromObject(&session, object)
		if session.Purpose == creemPurposeCreditPack {
			return a.completeCreemCreditPurchase(tx, &session, object, eventID)
		}
		if session.Purpose == creemPurposeSubscription {
			return a.completeCreemSubscriptionCheckout(tx, &session, object, eventID)
		}
		session.Status = "completed"
		return tx.Save(&session).Error
	})
}

func (a *App) processCreemSubscriptionPaid(eventID string, payload map[string]any) error {
	object := objectMap(payload)
	return a.db.Transaction(func(tx *gorm.DB) error {
		sub, plan, err := a.upsertUserSubscriptionFromCreemObject(tx, object, subscriptionStatusActive)
		if err != nil {
			return err
		}
		periodStart, periodEnd := subscriptionPeriodFromObject(object, sub.BillingCycle)
		return a.grantSubscriptionCreditsForPeriod(tx, &sub, plan, periodStart, periodEnd, eventID)
	})
}

func (a *App) syncCreemSubscriptionStatus(eventType string, payload map[string]any) error {
	object := objectMap(payload)
	status := strings.TrimPrefix(eventType, "subscription.")
	if status == "cancelled" {
		status = subscriptionStatusCanceled
	}
	if status == "update" || status == "updated" {
		status = defaultString(creemStringValue(firstPresent(object, "status")), subscriptionStatusActive)
	}
	if status == "cancelled" {
		status = subscriptionStatusCanceled
	}
	return a.db.Transaction(func(tx *gorm.DB) error {
		sub, _, err := a.upsertUserSubscriptionFromCreemObject(tx, object, status)
		if err != nil {
			return err
		}
		if subscriptionStatusRevokesCredits(status) {
			now := time.Now().UTC()
			sub.NextCreditGrantAt = nil
			if err := tx.Save(&sub).Error; err != nil {
				return err
			}
			return revokeSubscriptionCreditsTx(tx, sub.ID, eventType, now)
		}
		return nil
	})
}

func (a *App) processCreemPaymentReversal(eventID, eventType string, payload map[string]any) error {
	object := objectMap(payload)
	status := subscriptionStatusRefunded
	if eventType == "dispute.created" {
		status = subscriptionStatusDisputed
	}
	return a.db.Transaction(func(tx *gorm.DB) error {
		sub, found, err := a.findCreemSubscriptionForReversal(tx, object)
		if err != nil || !found {
			return err
		}
		now := time.Now().UTC()
		sub.Status = status
		sub.CancelAtPeriodEnd = false
		sub.CanceledAt = &now
		sub.NextCreditGrantAt = nil
		if err := tx.Save(&sub).Error; err != nil {
			return err
		}
		return revokeSubscriptionCreditsTx(tx, sub.ID, eventID, now)
	})
}

func (a *App) findCreemSubscriptionForReversal(tx *gorm.DB, object map[string]any) (models.UserSubscription, bool, error) {
	subscriptionID := creemSubscriptionIDFromRelatedObject(object)
	var sub models.UserSubscription
	if subscriptionID != "" {
		if err := tx.First(&sub, "creem_subscription_id = ?", subscriptionID).Error; err != nil {
			if err != gorm.ErrRecordNotFound {
				return sub, false, err
			}
		} else {
			return sub, true, nil
		}
	}

	session, hasSession, err := a.findCreemCheckoutSessionByRelatedID(tx, object)
	if err != nil {
		return sub, false, err
	}
	if hasSession {
		if session.CreemSubscriptionID != "" {
			if err := tx.First(&sub, "creem_subscription_id = ?", session.CreemSubscriptionID).Error; err != nil {
				if err != gorm.ErrRecordNotFound {
					return sub, false, err
				}
			} else {
				return sub, true, nil
			}
		}
		q := tx.Where("user_id = ?", session.UserID)
		if session.PlanID != nil {
			q = q.Where("plan_id = ?", *session.PlanID)
		}
		if session.CreemProductID != "" {
			q = q.Where("creem_product_id = ?", session.CreemProductID)
		}
		if err := q.Order("updated_at DESC").First(&sub).Error; err != nil {
			if err != gorm.ErrRecordNotFound {
				return sub, false, err
			}
		} else {
			return sub, true, nil
		}
	}

	customerID := creemCustomerIDFromObject(object)
	productID := creemProductIDFromObject(object)
	if customerID != "" {
		q := tx.Where("creem_customer_id = ?", customerID)
		if productID != "" {
			q = q.Where("creem_product_id = ?", productID)
		}
		if err := q.Order("updated_at DESC").First(&sub).Error; err != nil {
			if err != gorm.ErrRecordNotFound {
				return sub, false, err
			}
		} else {
			return sub, true, nil
		}
	}
	return sub, false, nil
}

func revokeSubscriptionCreditsTx(tx *gorm.DB, subscriptionID uuid.UUID, sourceEventID string, now time.Time) error {
	return tx.Model(&models.SubscriptionCreditGrant{}).
		Where("subscription_id = ? AND status = ?", subscriptionID, "active").
		Updates(map[string]any{
			"status":          "revoked",
			"expires_at":      now.UTC(),
			"source_event_id": sourceEventID,
		}).Error
}

func subscriptionStatusRevokesCredits(status string) bool {
	switch status {
	case subscriptionStatusCanceled, subscriptionStatusPaused, subscriptionStatusExpired, subscriptionStatusRefunded, subscriptionStatusDisputed:
		return true
	default:
		return false
	}
}

func (a *App) applyImmediateSubscriptionUpgrade(userID uuid.UUID, current models.UserSubscription, plan models.SubscriptionPlan, cycle, productID string, creemObject map[string]any) (models.UserSubscription, error) {
	now := time.Now().UTC()
	periodStart, periodEnd := subscriptionPeriodFromObject(creemObject, cycle)
	if !creemObjectHasPeriodStart(creemObject) && current.CurrentPeriodStart != nil {
		periodStart = current.CurrentPeriodStart.UTC()
	}
	if !creemObjectHasPeriodEnd(creemObject) && current.CurrentPeriodEnd != nil {
		periodEnd = current.CurrentPeriodEnd.UTC()
	}
	customerID := creemCustomerIDFromObject(creemObject)
	returnedProductID := creemProductIDFromObject(creemObject)
	if returnedProductID != "" {
		productID = returnedProductID
	}
	if productID == "" {
		if cycle == billingCycleYearly {
			productID = plan.CreemProductIDYearly
		} else {
			productID = plan.CreemProductIDMonthly
		}
	}

	var updated models.UserSubscription
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&updated, "id = ? AND user_id = ?", current.ID, userID).Error; err != nil {
			return err
		}

		grantEnd := periodEnd
		if cycle == billingCycleYearly {
			monthEnd := periodStart.AddDate(0, 1, 0)
			if monthEnd.Before(periodEnd) {
				grantEnd = monthEnd
			}
		}

		var activeTotals struct {
			Total int64
		}
		if err := tx.Model(&models.SubscriptionCreditGrant{}).
			Where("user_id = ? AND subscription_id = ? AND status = ? AND expires_at > ?", userID, updated.ID, "active", now).
			Select("coalesce(sum(credit_amount), 0) as total").
			Scan(&activeTotals).Error; err != nil {
			return err
		}

		updated.PlanID = plan.ID
		updated.BillingCycle = cycle
		updated.Status = subscriptionStatusActive
		if customerID != "" {
			updated.CreemCustomerID = customerID
		}
		updated.CreemProductID = productID
		updated.CurrentPeriodStart = &periodStart
		updated.CurrentPeriodEnd = &periodEnd
		updated.CancelAtPeriodEnd = false
		updated.CanceledAt = nil
		updated.MonthlyCreditsSnapshot = plan.MonthlyCredits
		if cycle == billingCycleYearly {
			next := grantEnd.UTC()
			if next.Before(periodEnd) {
				updated.NextCreditGrantAt = &next
			} else {
				updated.NextCreditGrantAt = nil
			}
		} else {
			updated.NextCreditGrantAt = nil
		}
		if err := tx.Save(&updated).Error; err != nil {
			return err
		}

		key := fmt.Sprintf("%s:%s", updated.CreemSubscriptionID, periodStart.UTC().Format("2006-01-02"))
		var grant models.SubscriptionCreditGrant
		if err := tx.Where("idempotency_key = ?", key).First(&grant).Error; err == nil {
			otherActiveCredits := activeTotals.Total - grant.CreditAmount
			if otherActiveCredits < 0 {
				otherActiveCredits = 0
			}
			desiredAmount := plan.MonthlyCredits - otherActiveCredits
			if desiredAmount < 0 {
				desiredAmount = 0
			}
			if desiredAmount < grant.UsedAmount {
				desiredAmount = grant.UsedAmount
			}
			grant.UserID = userID
			grant.SubscriptionID = updated.ID
			grant.PlanID = plan.ID
			grant.CreditAmount = desiredAmount
			grant.PeriodStart = periodStart.UTC()
			grant.PeriodEnd = grantEnd.UTC()
			grant.ExpiresAt = grantEnd.UTC()
			grant.SourceEventID = "upgrade:proration-charge-immediately"
			grant.Status = "active"
			return tx.Save(&grant).Error
		} else if err != gorm.ErrRecordNotFound {
			return err
		}

		topUpCredits := plan.MonthlyCredits - activeTotals.Total
		if topUpCredits < 0 {
			topUpCredits = 0
		}
		grant = models.SubscriptionCreditGrant{
			UserID:         userID,
			SubscriptionID: updated.ID,
			PlanID:         plan.ID,
			CreditAmount:   topUpCredits,
			PeriodStart:    periodStart.UTC(),
			PeriodEnd:      grantEnd.UTC(),
			ExpiresAt:      grantEnd.UTC(),
			IdempotencyKey: key,
			SourceEventID:  "upgrade:proration-charge-immediately",
			Status:         "active",
		}
		return tx.Create(&grant).Error
	})
	return updated, err
}

func (a *App) completeCreemCreditPurchase(tx *gorm.DB, session *models.CreemCheckoutSession, object map[string]any, eventID string) error {
	if session.PackageID == nil {
		return fmt.Errorf("Creem checkout 缺少 package_id")
	}
	var pkg models.TokenPackage
	if err := tx.First(&pkg, "id = ?", *session.PackageID).Error; err != nil {
		return err
	}
	if pkg.CreemProductID == nil || strings.TrimSpace(*pkg.CreemProductID) == "" {
		return fmt.Errorf("套餐未配置 Creem product_id")
	}
	if productID := creemProductIDFromObject(object); productID != "" && productID != strings.TrimSpace(*pkg.CreemProductID) {
		return fmt.Errorf("Creem product_id 与套餐不匹配")
	}

	var account models.UserTokenAccount
	if err := tx.FirstOrCreate(&account, models.UserTokenAccount{UserID: session.UserID}).Error; err != nil {
		return err
	}
	totalCredits := pkg.TokenAmount + pkg.BonusTokens
	account.TotalTokens += totalCredits
	account.TotalRecharged += session.Amount
	now := time.Now().UTC()
	account.LastRechargeAt = &now
	if err := tx.Save(&account).Error; err != nil {
		return err
	}
	record := models.TokenRechargeRecord{UserID: session.UserID, TokenAmount: pkg.TokenAmount, BonusTokens: pkg.BonusTokens, Price: session.Amount, PaymentMethod: "creem", PackageID: &pkg.ID}
	if err := tx.Create(&record).Error; err != nil {
		return err
	}
	session.Status = "completed"
	updateCreemSessionFromObject(session, object)
	if err := tx.Save(session).Error; err != nil {
		return err
	}
	return a.createCreditLedgerRecord(tx, creditLedgerEntry{
		UserID:       session.UserID,
		EventType:    creditLedgerEventRecharge,
		Item:         pkg.DisplayName,
		ChangeAmount: totalCredits,
		BalanceAfter: account.RemainingTokens(),
		Reason:       "Creem 充值",
		RequestMetadata: map[string]any{
			"creem_event_id":    eventID,
			"creem_checkout_id": session.CreemCheckoutID,
			"creem_request_id":  session.RequestID,
			"creem_product_id":  session.CreemProductID,
			"package_id":        pkg.ID.String(),
			"package_name":      pkg.DisplayName,
			"payment_amount":    session.Amount,
		},
		RecordedAt: now,
	})
}

func (a *App) completeCreemSubscriptionCheckout(tx *gorm.DB, session *models.CreemCheckoutSession, object map[string]any, eventID string) error {
	if session.PlanID == nil {
		return fmt.Errorf("Creem checkout 缺少 plan_id")
	}
	var plan models.SubscriptionPlan
	if err := tx.First(&plan, "id = ?", *session.PlanID).Error; err != nil {
		return err
	}
	productID := defaultString(creemProductIDFromObject(object), session.CreemProductID)
	if productID == "" {
		return fmt.Errorf("Creem checkout 缺少 product_id")
	}
	if productID != plan.CreemProductIDMonthly && productID != plan.CreemProductIDYearly {
		return fmt.Errorf("Creem product_id 与订阅计划不匹配")
	}

	cycle := normalizeBillingCycle(session.BillingCycle)
	if productID == plan.CreemProductIDYearly {
		cycle = billingCycleYearly
	}
	if productID == plan.CreemProductIDMonthly {
		cycle = billingCycleMonthly
	}

	subscriptionID := session.CreemSubscriptionID
	if subscriptionID == "" {
		if subscription := mapValue(object["subscription"]); subscription != nil {
			subscriptionID = creemStringValue(firstPresent(subscription, "id", "subscription_id"))
		} else {
			subscriptionID = creemStringValue(object["subscription"])
		}
	}
	if subscriptionID == "" {
		return fmt.Errorf("Creem checkout 缺少 subscription_id")
	}

	subscriptionObject := creemSubscriptionObjectFromCheckout(object)
	subscriptionObject["id"] = subscriptionID
	if _, ok := subscriptionObject["product"]; !ok {
		if product := object["product"]; product != nil {
			subscriptionObject["product"] = product
		} else {
			subscriptionObject["product"] = productID
		}
	}
	if _, ok := subscriptionObject["customer"]; !ok {
		if customer := object["customer"]; customer != nil {
			subscriptionObject["customer"] = customer
		} else if session.CreemCustomerID != "" {
			subscriptionObject["customer"] = session.CreemCustomerID
		}
	}
	metadata := mergeCreemMetadata(
		jsonObject(session.Metadata),
		creemMetadata(object),
		creemMetadata(subscriptionObject),
	)
	metadata["purpose"] = creemPurposeSubscription
	metadata["user_id"] = session.UserID.String()
	metadata["plan_id"] = plan.ID.String()
	metadata["plan_name"] = plan.Name
	metadata["billing_cycle"] = cycle
	subscriptionObject["metadata"] = metadata
	if _, ok := subscriptionObject["status"]; !ok {
		subscriptionObject["status"] = subscriptionStatusActive
	}

	sub, resolvedPlan, err := a.upsertUserSubscriptionFromCreemObject(tx, subscriptionObject, subscriptionStatusActive)
	if err != nil {
		return err
	}
	periodStart, periodEnd := subscriptionPeriodFromObject(subscriptionObject, sub.BillingCycle)
	if err := a.grantSubscriptionCreditsForPeriod(tx, &sub, resolvedPlan, periodStart, periodEnd, eventID); err != nil {
		return err
	}

	session.Status = "completed"
	session.CreemProductID = productID
	session.CreemSubscriptionID = subscriptionID
	session.BillingCycle = cycle
	if customerID := creemCustomerIDFromObject(object); customerID != "" {
		session.CreemCustomerID = customerID
	}
	session.Metadata = jsonMap(metadata)
	return tx.Save(session).Error
}

func (a *App) upsertUserSubscriptionFromCreemObject(tx *gorm.DB, object map[string]any, status string) (models.UserSubscription, models.SubscriptionPlan, error) {
	subscriptionID := creemStringValue(firstPresent(object, "id", "subscription_id"))
	if subscriptionID == "" {
		if subObj := mapValue(object["subscription"]); subObj != nil {
			subscriptionID = creemStringValue(firstPresent(subObj, "id"))
		} else {
			subscriptionID = creemStringValue(object["subscription"])
		}
	}
	if subscriptionID == "" {
		return models.UserSubscription{}, models.SubscriptionPlan{}, fmt.Errorf("Creem subscription 事件缺少 subscription id")
	}

	metadata := creemMetadata(object)
	userIDText := creemStringValue(firstPresent(metadata, "user_id", "userId"))
	var userID uuid.UUID
	var err error
	if userIDText != "" {
		userID, err = uuid.Parse(userIDText)
		if err != nil {
			return models.UserSubscription{}, models.SubscriptionPlan{}, fmt.Errorf("Creem metadata user_id 无效")
		}
	}
	var sub models.UserSubscription
	hasExistingSub := false
	if err := tx.First(&sub, "creem_subscription_id = ?", subscriptionID).Error; err != nil {
		if err != gorm.ErrRecordNotFound {
			return sub, models.SubscriptionPlan{}, err
		}
	} else if userID == uuid.Nil {
		userID = sub.UserID
		hasExistingSub = true
	} else {
		hasExistingSub = true
	}
	if !hasExistingSub && userID == uuid.Nil {
		return sub, models.SubscriptionPlan{}, fmt.Errorf("Creem subscription 缺少 user_id")
	}
	if !hasExistingSub {
		sub = models.UserSubscription{UserID: userID, CreemSubscriptionID: subscriptionID}
	}

	productID := creemProductIDFromObject(object)
	if productID == "" {
		productID = sub.CreemProductID
	}
	planIDText := creemStringValue(firstPresent(metadata, "plan_id", "planId"))
	var plan models.SubscriptionPlan
	if planIDText != "" {
		if planID, parseErr := uuid.Parse(planIDText); parseErr == nil {
			err = tx.First(&plan, "id = ?", planID).Error
		}
	}
	if plan.ID == uuid.Nil && productID != "" {
		err = tx.Where("creem_product_id_monthly = ? OR creem_product_id_yearly = ?", productID, productID).First(&plan).Error
	}
	if plan.ID == uuid.Nil && hasExistingSub {
		err = tx.First(&plan, "id = ?", sub.PlanID).Error
	}
	if err != nil || plan.ID == uuid.Nil {
		return sub, models.SubscriptionPlan{}, fmt.Errorf("订阅计划不存在")
	}

	rawBillingCycle := creemStringValue(firstPresent(metadata, "billing_cycle", "billingCycle"))
	billingCycle := normalizeBillingCycle(rawBillingCycle)
	if rawBillingCycle == "" && sub.BillingCycle != "" {
		billingCycle = sub.BillingCycle
	}
	if productID == plan.CreemProductIDYearly {
		billingCycle = billingCycleYearly
	}
	if productID == plan.CreemProductIDMonthly {
		billingCycle = billingCycleMonthly
	}
	customerID := creemCustomerIDFromObject(object)
	if customerID == "" {
		customerID = sub.CreemCustomerID
	}
	periodStart, periodEnd := subscriptionPeriodFromObject(object, billingCycle)
	if !creemObjectHasPeriodStart(object) && sub.CurrentPeriodStart != nil {
		periodStart = sub.CurrentPeriodStart.UTC()
	}
	if !creemObjectHasPeriodEnd(object) && sub.CurrentPeriodEnd != nil {
		periodEnd = sub.CurrentPeriodEnd.UTC()
	}
	cancelAtPeriodEnd := boolValue(firstPresent(object, "cancel_at_period_end", "cancelAtPeriodEnd"))
	if status == subscriptionStatusScheduledCancel {
		cancelAtPeriodEnd = true
	}
	canceledAt := timePtrFromAny(firstPresent(object, "canceled_at", "cancelled_at", "canceledAt", "cancelledAt"))
	if status == subscriptionStatusCanceled && canceledAt == nil {
		now := time.Now().UTC()
		canceledAt = &now
	}

	previousPlanID := sub.PlanID
	sub.UserID = userID
	sub.PlanID = plan.ID
	sub.BillingCycle = billingCycle
	sub.Status = status
	sub.CreemCustomerID = customerID
	sub.CreemSubscriptionID = subscriptionID
	sub.CreemProductID = productID
	sub.CurrentPeriodStart = &periodStart
	sub.CurrentPeriodEnd = &periodEnd
	sub.CancelAtPeriodEnd = cancelAtPeriodEnd
	sub.CanceledAt = canceledAt
	sub.TrialEnd = timePtrFromAny(firstPresent(object, "trial_end", "trialEnd"))
	if sub.MonthlyCreditsSnapshot == 0 || previousPlanID != uuid.Nil && previousPlanID != plan.ID {
		sub.MonthlyCreditsSnapshot = plan.MonthlyCredits
	}
	if subscriptionStatusRevokesCredits(status) {
		sub.NextCreditGrantAt = nil
	}
	if billingCycle != billingCycleYearly {
		sub.NextCreditGrantAt = nil
	}
	if !subscriptionStatusRevokesCredits(status) && sub.NextCreditGrantAt == nil && billingCycle == billingCycleYearly {
		next := periodStart.AddDate(0, 1, 0)
		if next.Before(periodEnd) {
			sub.NextCreditGrantAt = &next
		}
	}
	if err := tx.Save(&sub).Error; err != nil {
		return sub, plan, err
	}
	return sub, plan, nil
}

func (a *App) grantSubscriptionCreditsForPeriod(tx *gorm.DB, sub *models.UserSubscription, plan models.SubscriptionPlan, periodStart, periodEnd time.Time, eventID string) error {
	if periodEnd.Before(periodStart) || periodEnd.Equal(periodStart) {
		periodEnd = periodStart.AddDate(0, 1, 0)
	}
	grantEnd := periodEnd
	if sub.BillingCycle == billingCycleYearly {
		monthEnd := periodStart.AddDate(0, 1, 0)
		if monthEnd.Before(periodEnd) {
			grantEnd = monthEnd
		}
	}
	credits := plan.MonthlyCredits
	if sub.MonthlyCreditsSnapshot > 0 {
		credits = sub.MonthlyCreditsSnapshot
	}
	next := grantEnd.UTC()
	if next.Before(periodEnd) {
		sub.NextCreditGrantAt = &next
	} else {
		sub.NextCreditGrantAt = nil
	}
	var existing models.SubscriptionCreditGrant
	if err := tx.Where("subscription_id = ? AND status = ? AND period_start < ? AND period_end > ?", sub.ID, "active", grantEnd.UTC(), periodStart.UTC()).
		Order("period_start ASC, created_at ASC").
		First(&existing).Error; err == nil {
		return tx.Save(sub).Error
	} else if err != gorm.ErrRecordNotFound {
		return err
	}

	key := fmt.Sprintf("%s:%s", sub.CreemSubscriptionID, periodStart.UTC().Format("2006-01-02"))
	grant := models.SubscriptionCreditGrant{
		UserID:         sub.UserID,
		SubscriptionID: sub.ID,
		PlanID:         plan.ID,
		CreditAmount:   credits,
		PeriodStart:    periodStart.UTC(),
		PeriodEnd:      grantEnd.UTC(),
		ExpiresAt:      grantEnd.UTC(),
		IdempotencyKey: key,
		SourceEventID:  eventID,
		Status:         "active",
	}
	if err := tx.Where("idempotency_key = ?", key).FirstOrCreate(&grant).Error; err != nil {
		return err
	}
	return tx.Save(sub).Error
}

func (a *App) ensureDueSubscriptionCredits(userID uuid.UUID) error {
	now := time.Now().UTC()
	return a.db.Transaction(func(tx *gorm.DB) error {
		return a.ensureDueSubscriptionCreditsTx(tx, &userID, now)
	})
}

func (a *App) ensureDueSubscriptionCreditsTx(tx *gorm.DB, userID *uuid.UUID, now time.Time) error {
	q := tx.Where("status IN ?", []string{subscriptionStatusActive, subscriptionStatusTrialing, subscriptionStatusScheduledCancel}).
		Where("next_credit_grant_at IS NOT NULL AND next_credit_grant_at <= ?", now)
	if userID != nil {
		q = q.Where("user_id = ?", *userID)
	}
	var subs []models.UserSubscription
	if err := q.Find(&subs).Error; err != nil {
		return err
	}
	for i := range subs {
		sub := subs[i]
		if sub.CurrentPeriodEnd == nil || sub.NextCreditGrantAt == nil || !sub.CurrentPeriodEnd.After(*sub.NextCreditGrantAt) {
			continue
		}
		var plan models.SubscriptionPlan
		if err := tx.First(&plan, "id = ?", sub.PlanID).Error; err != nil {
			return err
		}
		start := sub.NextCreditGrantAt.UTC()
		for !start.After(now) && start.Before(*sub.CurrentPeriodEnd) {
			end := start.AddDate(0, 1, 0)
			if end.After(*sub.CurrentPeriodEnd) {
				end = *sub.CurrentPeriodEnd
			}
			if err := a.grantSubscriptionCreditsForPeriod(tx, &sub, plan, start, *sub.CurrentPeriodEnd, "scheduled:"+start.Format("2006-01-02")); err != nil {
				return err
			}
			start = end
			if sub.NextCreditGrantAt == nil {
				break
			}
		}
	}
	return nil
}

func (a *App) currentUserSubscription(userID uuid.UUID) (models.UserSubscription, bool, error) {
	if err := a.ensureDueSubscriptionCredits(userID); err != nil {
		return models.UserSubscription{}, false, err
	}
	var sub models.UserSubscription
	result := a.db.Where("user_id = ? AND status IN ?", userID, []string{subscriptionStatusActive, subscriptionStatusTrialing, subscriptionStatusScheduledCancel, subscriptionStatusPastDue, subscriptionStatusPaused}).
		Order("updated_at DESC").Limit(1).Find(&sub)
	if result.Error != nil {
		return sub, false, result.Error
	}
	if result.RowsAffected == 0 {
		return sub, false, nil
	}
	return sub, true, nil
}

func (a *App) creditBalanceSnapshot(userID uuid.UUID) (models.UserTokenAccount, creditBalanceSnapshot, error) {
	if err := a.ensureDueSubscriptionCredits(userID); err != nil {
		return models.UserTokenAccount{}, creditBalanceSnapshot{}, err
	}
	now := time.Now().UTC()
	if err := a.db.Transaction(func(tx *gorm.DB) error {
		return a.expireCreditReservationsTx(tx, &userID, now)
	}); err != nil {
		return models.UserTokenAccount{}, creditBalanceSnapshot{}, err
	}
	account, err := a.ensureTokenAccount(userID)
	if err != nil {
		return account, creditBalanceSnapshot{}, err
	}
	purchasedLots, err := purchasedCreditSummary(a.db, userID, now)
	if err != nil {
		return account, creditBalanceSnapshot{}, err
	}
	var sums struct {
		Total int64
		Used  int64
	}
	err = a.db.Model(&models.SubscriptionCreditGrant{}).
		Where("user_id = ? AND status = ? AND expires_at > ?", userID, "active", now).
		Select("coalesce(sum(credit_amount), 0) as total, coalesce(sum(used_amount), 0) as used").
		Scan(&sums).Error
	if err != nil {
		return account, creditBalanceSnapshot{}, err
	}
	var reservedCredits int64
	if err := a.db.Model(&models.CreditReservation{}).
		Where("user_id = ? AND status = ? AND expires_at > ?", userID, creditReservationPending, now).
		Select("coalesce(sum(credit_count), 0)").
		Scan(&reservedCredits).Error; err != nil {
		return account, creditBalanceSnapshot{}, err
	}
	grossRemaining := account.RemainingTokens() + purchasedLots.Remaining + maxInt64(0, sums.Total-sums.Used)
	snap := creditBalanceSnapshot{
		PurchasedTotal:        account.TotalTokens + purchasedLots.Total,
		PurchasedUsed:         account.UsedTokens + purchasedLots.Total - purchasedLots.Remaining,
		PurchasedRemaining:    account.RemainingTokens() + purchasedLots.Remaining,
		SubscriptionTotal:     sums.Total,
		SubscriptionUsed:      sums.Used,
		SubscriptionRemaining: maxInt64(0, sums.Total-sums.Used),
		ReservedCredits:       reservedCredits,
		GrossRemainingCredits: grossRemaining,
		NextExpiryAt:          purchasedLots.NextExpiry,
	}
	snap.TotalCredits = snap.PurchasedTotal + snap.SubscriptionTotal
	snap.UsedCredits = snap.PurchasedUsed + snap.SubscriptionUsed
	snap.RemainingCredits = grossRemaining - reservedCredits
	return account, snap, nil
}

func (a *App) consumeSubscriptionCreditsTx(tx *gorm.DB, userID uuid.UUID, amount int64, now time.Time) (int64, error) {
	if amount <= 0 {
		return 0, nil
	}
	if err := a.ensureDueSubscriptionCreditsTx(tx, &userID, now); err != nil {
		return 0, err
	}
	var grants []models.SubscriptionCreditGrant
	if err := tx.Where("user_id = ? AND status = ? AND expires_at > ? AND credit_amount > used_amount", userID, "active", now).
		Order("expires_at ASC, period_start ASC, created_at ASC").
		Find(&grants).Error; err != nil {
		return 0, err
	}
	remaining := amount
	consumed := int64(0)
	for _, grant := range grants {
		available := grant.RemainingCredits()
		if available <= 0 {
			continue
		}
		delta := available
		if delta > remaining {
			delta = remaining
		}
		if err := tx.Model(&models.SubscriptionCreditGrant{}).
			Where("id = ? AND credit_amount - used_amount >= ?", grant.ID, delta).
			UpdateColumn("used_amount", gorm.Expr("used_amount + ?", delta)).Error; err != nil {
			return consumed, err
		}
		consumed += delta
		remaining -= delta
		if remaining == 0 {
			break
		}
	}
	return consumed, nil
}

func (a *App) findCreemCheckoutSession(tx *gorm.DB, object map[string]any) (models.CreemCheckoutSession, bool, error) {
	requestID := creemStringValue(firstPresent(object, "request_id", "requestId"))
	checkoutID := creemStringValue(firstPresent(object, "id", "checkout_id", "checkoutId"))
	var session models.CreemCheckoutSession
	q := tx.Model(&models.CreemCheckoutSession{})
	switch {
	case requestID != "":
		q = q.Where("request_id = ?", requestID)
	case checkoutID != "":
		q = q.Where("creem_checkout_id = ?", checkoutID)
	default:
		return session, false, fmt.Errorf("Creem checkout 缺少 request_id")
	}
	if err := q.First(&session).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return session, false, fmt.Errorf("Creem checkout session 不存在")
		}
		return session, false, err
	}
	return session, true, nil
}

func (a *App) findCreemCheckoutSessionByRelatedID(tx *gorm.DB, object map[string]any) (models.CreemCheckoutSession, bool, error) {
	requestID := creemStringValue(firstPresent(object, "request_id", "requestId"))
	checkoutID := creemStringValue(firstPresent(object, "checkout_id", "checkoutId"))
	if checkoutID == "" {
		if checkout := mapValue(object["checkout"]); checkout != nil {
			checkoutID = creemStringValue(firstPresent(checkout, "id", "checkout_id", "checkoutId"))
		}
	}
	orderID := creemOrderIDFromObject(object)
	subscriptionID := creemSubscriptionIDFromRelatedObject(object)

	var session models.CreemCheckoutSession
	q := tx.Model(&models.CreemCheckoutSession{})
	switch {
	case requestID != "":
		q = q.Where("request_id = ?", requestID)
	case checkoutID != "":
		q = q.Where("creem_checkout_id = ?", checkoutID)
	case orderID != "":
		q = q.Where("creem_order_id = ?", orderID)
	case subscriptionID != "":
		q = q.Where("creem_subscription_id = ?", subscriptionID)
	default:
		return session, false, nil
	}
	if err := q.First(&session).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return session, false, nil
		}
		return session, false, err
	}
	return session, true, nil
}

func (a *App) findSubscriptionPlan(c *gin.Context, idOrName, fallbackName string, requireActive bool) (models.SubscriptionPlan, bool) {
	var plan models.SubscriptionPlan
	raw := strings.TrimSpace(idOrName)
	if raw == "" {
		raw = strings.TrimSpace(fallbackName)
	}
	if raw == "" {
		fail(c, http.StatusBadRequest, "缺少订阅计划ID")
		return plan, false
	}
	q := a.db.Model(&models.SubscriptionPlan{})
	if requireActive {
		q = q.Where("is_active = ?", true)
	}
	if id, err := uuid.Parse(raw); err == nil {
		q = q.Where("id = ?", id)
	} else {
		q = q.Where("name = ? OR display_name = ?", raw, raw)
	}
	if err := q.First(&plan).Error; err != nil {
		fail(c, http.StatusNotFound, "订阅计划不存在")
		return plan, false
	}
	return plan, true
}

func serializeSubscriptionPlan(plan models.SubscriptionPlan) gin.H {
	return gin.H{
		"id":                            plan.ID.String(),
		"name":                          plan.Name,
		"display_name":                  plan.DisplayName,
		"description":                   plan.Description,
		"monthly_credits":               plan.MonthlyCredits,
		"monthly_token_limit":           plan.MonthlyCredits,
		"price_monthly":                 fmt.Sprintf("%.2f", plan.PriceMonthly),
		"price_yearly":                  fmt.Sprintf("%.2f", plan.PriceYearly),
		"currency":                      defaultString(plan.Currency, "USD"),
		"creem_product_id_monthly":      plan.CreemProductIDMonthly,
		"creem_product_id_yearly":       plan.CreemProductIDYearly,
		"alipay_plan_id":                plan.AlipayPlanID,
		"alipay_product_id":             plan.AlipayProductID,
		"subscription_provider_enabled": plan.SubscriptionProviderEnabled,
		"features":                      jsonStringSlice(plan.Features),
		"limits":                        jsonObject(plan.Limits),
		"is_active":                     plan.IsActive,
		"is_featured":                   plan.IsFeatured,
		"sort_order":                    plan.SortOrder,
		"is_deleted":                    plan.DeletedAt.Valid,
		"deleted_at":                    deletedAtValue(plan.DeletedAt),
		"created_at":                    plan.CreatedAt,
		"updated_at":                    plan.UpdatedAt,
	}
}

func (a *App) serializeUserSubscription(sub models.UserSubscription) gin.H {
	var plan models.SubscriptionPlan
	_ = a.db.First(&plan, "id = ?", sub.PlanID).Error
	_, snap, _ := a.creditBalanceSnapshot(sub.UserID)
	limit := sub.MonthlyCreditsSnapshot
	if limit == 0 {
		limit = plan.MonthlyCredits
	}
	percentage := 0.0
	if limit > 0 {
		percentage = float64(snap.SubscriptionUsed) / float64(limit) * 100
	}
	provider := strings.ToLower(strings.TrimSpace(sub.PaymentProvider))
	if provider == "" {
		provider = "creem"
	}
	managementAction := provider + "_portal"
	return gin.H{
		"id":                      sub.ID.String(),
		"user_id":                 sub.UserID.String(),
		"plan":                    serializeSubscriptionPlan(plan),
		"status":                  sub.Status,
		"billing_cycle":           sub.BillingCycle,
		"payment_provider":        provider,
		"management_action":       managementAction,
		"current_period_start":    sub.CurrentPeriodStart,
		"current_period_end":      sub.CurrentPeriodEnd,
		"next_charge_at":          sub.NextChargeAt,
		"grace_period_ends_at":    sub.GracePeriodEndsAt,
		"provider_failure_reason": sub.ProviderFailureReason,
		"cancel_at_period_end":    sub.CancelAtPeriodEnd,
		"cancelled_at":            sub.CanceledAt,
		"canceled_at":             sub.CanceledAt,
		"trial_end":               sub.TrialEnd,
		"monthly_token_used":      snap.SubscriptionUsed,
		"monthly_token_limit":     limit,
		"token_usage_percentage":  percentage,
		"remaining_tokens":        snap.SubscriptionRemaining,
		"subscription_credits":    snap.SubscriptionRemaining,
		"reserved_credits":        snap.ReservedCredits,
		"gross_remaining_credits": snap.GrossRemainingCredits,
		"created_at":              sub.CreatedAt,
		"updated_at":              sub.UpdatedAt,
	}
}

func normalizeBillingCycle(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case billingCycleYearly, "annual", "annually", "year":
		return billingCycleYearly
	default:
		return billingCycleMonthly
	}
}

func objectMap(payload map[string]any) map[string]any {
	if obj := mapValue(payload["object"]); obj != nil {
		return obj
	}
	return payload
}

func creemCheckoutObjectFromAPI(payload map[string]any) map[string]any {
	if checkout := mapValue(payload["checkout"]); checkout != nil {
		return checkout
	}
	if data := mapValue(payload["data"]); data != nil {
		if checkout := mapValue(data["checkout"]); checkout != nil {
			return checkout
		}
		if obj := mapValue(data["object"]); obj != nil {
			return obj
		}
		return data
	}
	return objectMap(payload)
}

func creemCheckoutObjectFromRedirect(checkoutID, orderID, customerID, productID, subscriptionID, requestID string) map[string]any {
	checkoutID = strings.TrimSpace(checkoutID)
	orderID = strings.TrimSpace(orderID)
	customerID = strings.TrimSpace(customerID)
	productID = strings.TrimSpace(productID)
	subscriptionID = strings.TrimSpace(subscriptionID)
	requestID = strings.TrimSpace(requestID)

	out := map[string]any{"id": checkoutID, "checkout_id": checkoutID}
	if requestID != "" {
		out["request_id"] = requestID
	}
	if productID != "" {
		out["product"] = map[string]any{"id": productID}
		out["product_id"] = productID
	}
	if customerID != "" {
		out["customer"] = map[string]any{"id": customerID}
		out["customer_id"] = customerID
	}
	if orderID != "" {
		order := map[string]any{"id": orderID}
		if productID != "" {
			order["product"] = productID
		}
		if customerID != "" {
			order["customer"] = customerID
		}
		out["order"] = order
	}
	if subscriptionID != "" {
		subscription := map[string]any{"id": subscriptionID}
		if productID != "" {
			subscription["product"] = productID
		}
		if customerID != "" {
			subscription["customer"] = customerID
		}
		out["subscription"] = subscription
		out["subscription_id"] = subscriptionID
	}
	return out
}

func mergeCreemCheckoutObjects(primary, fallback map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range primary {
		out[key] = value
	}
	for key, value := range fallback {
		if existing, ok := out[key]; !ok || creemValueEmpty(existing) {
			out[key] = value
			continue
		}
		existingMap := mapValue(out[key])
		valueMap := mapValue(value)
		if existingMap != nil && valueMap != nil {
			out[key] = mergeCreemCheckoutObjects(existingMap, valueMap)
		}
	}
	return out
}

func creemValueEmpty(value any) bool {
	if value == nil {
		return true
	}
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		return text == "" || strings.EqualFold(text, "null")
	}
	return false
}

func creemRedirectParams(checkoutID, orderID, customerID, productID, subscriptionID, requestID, signature string) map[string]string {
	return map[string]string{
		"checkout_id":     strings.TrimSpace(checkoutID),
		"order_id":        strings.TrimSpace(orderID),
		"customer_id":     strings.TrimSpace(customerID),
		"product_id":      strings.TrimSpace(productID),
		"subscription_id": strings.TrimSpace(subscriptionID),
		"request_id":      strings.TrimSpace(requestID),
		"signature":       strings.TrimSuffix(strings.TrimSpace(signature), "?"),
	}
}

func creemCheckoutIsCompleted(object map[string]any) bool {
	status := strings.ToLower(creemStringValue(firstPresent(object, "status", "checkout_status", "checkoutStatus")))
	switch status {
	case "completed", "complete", "paid", "succeeded", "success":
		return true
	default:
		return false
	}
}

func verifyCreemRedirectSignature(params map[string]string, apiKey string) bool {
	signature := strings.TrimSpace(params["signature"])
	apiKey = strings.TrimSpace(apiKey)
	if signature == "" || apiKey == "" {
		return false
	}
	sortedPairs := creemRedirectPairs(params, nil, false)
	signedPayload := strings.Join(sortedPairs, "&")
	mac := hmac.New(sha256.New, []byte(apiKey))
	mac.Write([]byte(signedPayload))
	if creemSignatureEqual(signature, hex.EncodeToString(mac.Sum(nil))) {
		return true
	}

	legacyOrders := [][]string{
		{"request_id", "checkout_id", "order_id", "customer_id", "subscription_id", "product_id"},
		{"checkout_id", "order_id", "customer_id", "subscription_id", "product_id", "request_id"},
		{"checkout_id", "subscription_id", "customer_id", "product_id", "request_id", "order_id"},
	}
	for _, order := range legacyOrders {
		for _, includeNull := range []bool{false, true} {
			if creemVerifyLegacyRedirectSignature(signature, apiKey, creemRedirectPairs(params, order, includeNull)) {
				return true
			}
		}
	}
	if creemVerifyLegacyRedirectSignature(signature, apiKey, sortedPairs) {
		return true
	}
	return creemVerifyLegacyRedirectSignature(signature, apiKey, creemRedirectPairs(params, nil, true))
}

func creemRedirectPairs(params map[string]string, order []string, includeNull bool) []string {
	pairs := []string{}
	used := map[string]bool{"signature": true}
	appendPair := func(key string) {
		if used[key] {
			return
		}
		value, ok := params[key]
		if !ok {
			return
		}
		value = strings.TrimSpace(value)
		if value == "" || !includeNull && strings.EqualFold(value, "null") {
			return
		}
		used[key] = true
		pairs = append(pairs, key+"="+value)
	}
	for _, key := range order {
		appendPair(key)
	}
	keys := make([]string, 0, len(params))
	for key := range params {
		if !used[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		appendPair(key)
	}
	return pairs
}

func creemVerifyLegacyRedirectSignature(signature, apiKey string, pairs []string) bool {
	if len(pairs) == 0 {
		return false
	}
	payload := strings.Join(append(append([]string{}, pairs...), "salt="+apiKey), "|")
	sum := sha256.Sum256([]byte(payload))
	return creemSignatureEqual(signature, hex.EncodeToString(sum[:]))
}

func creemSignatureEqual(got, expected string) bool {
	got = strings.ToLower(strings.TrimSpace(got))
	expected = strings.ToLower(strings.TrimSpace(expected))
	return len(got) == len(expected) && hmac.Equal([]byte(got), []byte(expected))
}

func mapValue(value any) map[string]any {
	if raw, ok := value.(map[string]any); ok {
		return raw
	}
	return nil
}

func creemSubscriptionObjectFromCheckout(object map[string]any) map[string]any {
	out := map[string]any{}
	if subscription := mapValue(object["subscription"]); subscription != nil {
		for key, value := range subscription {
			out[key] = value
		}
	}
	for _, key := range []string{
		"current_period_start",
		"currentPeriodStart",
		"current_period_start_date",
		"currentPeriodStartDate",
		"current_period_end",
		"currentPeriodEnd",
		"current_period_end_date",
		"currentPeriodEndDate",
		"start_date",
		"startDate",
		"end_date",
		"endDate",
		"trial_end",
		"trialEnd",
		"cancel_at_period_end",
		"cancelAtPeriodEnd",
		"canceled_at",
		"cancelled_at",
		"canceledAt",
		"cancelledAt",
	} {
		if value := firstPresent(object, key); value != nil {
			out[key] = value
		}
	}
	return out
}

func mergeCreemMetadata(items ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, item := range items {
		for key, value := range item {
			out[key] = value
		}
	}
	return out
}

func creemMetadata(object map[string]any) map[string]any {
	if metadata := mapValue(object["metadata"]); metadata != nil {
		return metadata
	}
	if checkout := mapValue(object["checkout"]); checkout != nil {
		if metadata := mapValue(checkout["metadata"]); metadata != nil {
			return metadata
		}
	}
	if subscription := mapValue(object["subscription"]); subscription != nil {
		if metadata := mapValue(subscription["metadata"]); metadata != nil {
			return metadata
		}
	}
	return map[string]any{}
}

func creemObjectID(value any, altKey string) string {
	if object := mapValue(value); object != nil {
		return creemStringValue(firstPresent(object, "id", altKey))
	}
	return creemStringValue(value)
}

func creemProductIDFromObject(object map[string]any) string {
	if productID := creemObjectID(object["product"], "product_id"); productID != "" {
		return productID
	}
	if productID := creemStringValue(firstPresent(object, "product_id", "productId")); productID != "" {
		return productID
	}
	if order := mapValue(object["order"]); order != nil {
		if productID := creemObjectID(order["product"], "product_id"); productID != "" {
			return productID
		}
	}
	if subscription := mapValue(object["subscription"]); subscription != nil {
		if productID := creemProductIDFromObject(subscription); productID != "" {
			return productID
		}
	}
	return ""
}

func creemSubscriptionIDFromRelatedObject(object map[string]any) string {
	if subscriptionID := creemStringValue(firstPresent(object, "subscription_id", "subscriptionId")); subscriptionID != "" {
		return subscriptionID
	}
	if subscription := mapValue(object["subscription"]); subscription != nil {
		if subscriptionID := creemStringValue(firstPresent(subscription, "id", "subscription_id", "subscriptionId")); subscriptionID != "" {
			return subscriptionID
		}
	} else if subscriptionID := creemStringValue(object["subscription"]); subscriptionID != "" {
		return subscriptionID
	}
	if order := mapValue(object["order"]); order != nil {
		if subscriptionID := creemSubscriptionIDFromRelatedObject(order); subscriptionID != "" {
			return subscriptionID
		}
	}
	if checkout := mapValue(object["checkout"]); checkout != nil {
		if subscriptionID := creemSubscriptionIDFromRelatedObject(checkout); subscriptionID != "" {
			return subscriptionID
		}
	}
	return ""
}

func creemOrderIDFromObject(object map[string]any) string {
	if orderID := creemStringValue(firstPresent(object, "order_id", "orderId")); orderID != "" {
		return orderID
	}
	if order := mapValue(object["order"]); order != nil {
		if orderID := creemStringValue(firstPresent(order, "id", "order_id", "orderId")); orderID != "" {
			return orderID
		}
	}
	if checkout := mapValue(object["checkout"]); checkout != nil {
		if orderID := creemOrderIDFromObject(checkout); orderID != "" {
			return orderID
		}
	}
	return ""
}

func creemCustomerIDFromObject(object map[string]any) string {
	if customerID := creemObjectID(object["customer"], "customer_id"); customerID != "" {
		return customerID
	}
	if customerID := creemStringValue(firstPresent(object, "customer_id", "customerId")); customerID != "" {
		return customerID
	}
	if order := mapValue(object["order"]); order != nil {
		if customerID := creemObjectID(order["customer"], "customer_id"); customerID != "" {
			return customerID
		}
	}
	if subscription := mapValue(object["subscription"]); subscription != nil {
		if customerID := creemCustomerIDFromObject(subscription); customerID != "" {
			return customerID
		}
	}
	return ""
}

func creemObjectHasPeriodStart(object map[string]any) bool {
	if !timeFromAny(firstPresent(object, "current_period_start", "currentPeriodStart", "current_period_start_date", "currentPeriodStartDate", "start_date", "startDate")).IsZero() {
		return true
	}
	if subscription := mapValue(object["subscription"]); subscription != nil {
		return creemObjectHasPeriodStart(subscription)
	}
	return false
}

func creemObjectHasPeriodEnd(object map[string]any) bool {
	if !timeFromAny(firstPresent(object, "current_period_end", "currentPeriodEnd", "current_period_end_date", "currentPeriodEndDate", "end_date", "endDate")).IsZero() {
		return true
	}
	if subscription := mapValue(object["subscription"]); subscription != nil {
		return creemObjectHasPeriodEnd(subscription)
	}
	return false
}

func subscriptionPeriodFromObject(object map[string]any, cycle string) (time.Time, time.Time) {
	now := time.Now().UTC()
	start := timeFromAny(firstPresent(object, "current_period_start", "currentPeriodStart", "current_period_start_date", "currentPeriodStartDate", "start_date", "startDate"))
	if start.IsZero() {
		start = now
	}
	end := timeFromAny(firstPresent(object, "current_period_end", "currentPeriodEnd", "current_period_end_date", "currentPeriodEndDate", "end_date", "endDate"))
	if end.IsZero() {
		if cycle == billingCycleYearly {
			end = start.AddDate(1, 0, 0)
		} else {
			end = start.AddDate(0, 1, 0)
		}
	}
	return start.UTC(), end.UTC()
}

func updateCreemSessionFromObject(session *models.CreemCheckoutSession, object map[string]any) {
	if checkoutID := creemStringValue(firstPresent(object, "id", "checkout_id", "checkoutId")); checkoutID != "" {
		session.CreemCheckoutID = checkoutID
	}
	if productID := creemProductIDFromObject(object); productID != "" {
		session.CreemProductID = productID
	}
	if customerID := creemCustomerIDFromObject(object); customerID != "" {
		session.CreemCustomerID = customerID
	}
	if order := mapValue(object["order"]); order != nil {
		session.CreemOrderID = creemStringValue(firstPresent(order, "id", "order_id"))
	}
	if subscription := mapValue(object["subscription"]); subscription != nil {
		session.CreemSubscriptionID = creemStringValue(firstPresent(subscription, "id", "subscription_id"))
	} else if subscriptionID := creemStringValue(object["subscription"]); subscriptionID != "" {
		session.CreemSubscriptionID = subscriptionID
	}
}

func creemStringValue(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

func boolValue(value any) bool {
	if v, ok := value.(bool); ok {
		return v
	}
	return false
}

func timePtrFromAny(value any) *time.Time {
	t := timeFromAny(value)
	if t.IsZero() {
		return nil
	}
	return &t
}

func timeFromAny(value any) time.Time {
	switch v := value.(type) {
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return time.Time{}
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, v); err == nil {
				return parsed.UTC()
			}
		}
	case float64:
		if v > 1_000_000_000_000 {
			return time.UnixMilli(int64(v)).UTC()
		}
		if v > 1_000_000_000 {
			return time.Unix(int64(v), 0).UTC()
		}
	}
	return time.Time{}
}

func jsonStringSlice(raw datatypes.JSON) []string {
	if len(raw) == 0 {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err == nil && out != nil {
		return out
	}
	return []string{}
}

func jsonObject(raw datatypes.JSON) map[string]any {
	if len(raw) == 0 {
		return gin.H{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err == nil && out != nil {
		return out
	}
	return gin.H{}
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
