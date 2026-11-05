package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
	"gorm.io/datatypes"
)

func (a *App) createSubscriptionWeChatPayment(c *gin.Context) {
	user, okUser := currentUserFromContext(c)
	if !okUser {
		fail(c, http.StatusUnauthorized, "需要登录")
		return
	}

	var req struct {
		PlanName  string  `json:"plan_name"`
		PackageID string  `json:"package_id"`
		PlanID    string  `json:"plan_id"`
		Amount    float64 `json:"amount"`
	}
	if !bindJSON(c, &req) {
		return
	}

	pkg, hasPkg, err := a.findCompatPackage(req.PackageID, req.PlanID, req.PlanName)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if !hasPkg && req.Amount <= 0 {
		fail(c, http.StatusBadRequest, "缺少有效的 plan_name、package_id 或 amount")
		return
	}

	subject := "Tokenized subscription payment"
	amount := req.Amount
	attach := ""
	var pkgInfo any
	if hasPkg {
		subject = pkg.DisplayName
		amount = pkg.Price
		attach = pkg.ID.String()
		pkgInfo = compatSubscriptionPlanResponse(pkg)
	}
	sourceAmount := amount
	wechatAmount := usdToWeChatCNYAmount(sourceAmount)

	codeURL := "weixin://wxpay/mock/" + randomCode("")
	outTradeNo := fmt.Sprintf("S%s", time.Now().Format("20060102150405")) + randomCode("")
	expire := time.Now().Add(30 * time.Minute)
	payment := models.WeChatPayment{UserID: user.ID, OutTradeNo: outTradeNo[:min(len(outTradeNo), 32)], CodeURL: &codeURL, Subject: subject, TotalAmount: wechatAmount, Currency: "CNY", TradeType: "NATIVE", TradeState: "NOTPAY", Status: "pending", TimeExpire: &expire}
	if attach != "" {
		payment.Attach = &attach
	}
	if a.wechat.Configured() {
		resp, err := a.wechat.CreateNativeOrder(c.Request.Context(), providers.WeChatNativeOrderRequest{Description: subject, OutTradeNo: payment.OutTradeNo, TotalAmount: wechatAmount, Attach: attach, TimeExpire: expire})
		if err != nil {
			fail(c, http.StatusBadGateway, err.Error())
			return
		}
		codeURL = resp.CodeURL
		payment.CodeURL = &codeURL
	}
	if err := a.db.Create(&payment).Error; err != nil {
		fail(c, http.StatusInternalServerError, "创建支付订单失败")
		return
	}
	ok(c, gin.H{"success": true, "mode": "tokenized_subscription", "payment_id": payment.ID.String(), "out_trade_no": payment.OutTradeNo, "code_url": codeURL, "total_amount": wechatAmount, "amount": wechatAmount, "currency": payment.Currency, "source_amount": sourceAmount, "source_currency": "USD", "exchange_rate": usdToCNYRateForWeChat, "status": payment.Status, "trade_state": payment.TradeState, "expires_at": expire, "expires_in": 1800, "plan_info": pkgInfo, "package_info": pkgInfo})
}

func (a *App) subscriptionWeChatPaymentStatus(c *gin.Context) {
	user, hasUser := currentUserFromContext(c)
	q := a.db.Model(&models.WeChatPayment{}).Where("out_trade_no = ?", c.Param("out_trade_no"))
	if hasUser && !user.IsAdmin {
		q = q.Where("user_id = ?", user.ID)
	}
	var payment models.WeChatPayment
	if err := q.First(&payment).Error; err != nil {
		fail(c, http.StatusNotFound, "订单不存在")
		return
	}
	ok(c, compatPaymentResponse(payment))
}

func (a *App) refundSubscriptionPayment(c *gin.Context) {
	a.updateSubscriptionPaymentState(c, "refunded", "REFUND", "支付已标记退款")
}

func (a *App) closeSubscriptionPayment(c *gin.Context) {
	a.updateSubscriptionPaymentState(c, "closed", "CLOSED", "支付已关闭")
}

func (a *App) updateSubscriptionPaymentState(c *gin.Context, status, tradeState, message string) {
	payment, okPayment := a.loadCompatPayment(c)
	if !okPayment {
		return
	}
	now := time.Now().UTC()
	payment.Status = status
	payment.TradeState = tradeState
	payment.TradeStateDesc = &message
	payment.NotifyTime = &now
	if err := a.db.Save(&payment).Error; err != nil {
		fail(c, http.StatusInternalServerError, "更新支付状态失败")
		return
	}
	resp := compatPaymentResponse(payment)
	resp["message"] = message
	resp["success"] = true
	ok(c, resp)
}

func (a *App) listSubscriptionPlans(c *gin.Context) {
	a.listSubscriptionPlansWithScope(c, false)
}

func (a *App) listSubscriptionPlansAdmin(c *gin.Context) {
	a.listSubscriptionPlansWithScope(c, true)
}

func (a *App) listSubscriptionPlansWithScope(c *gin.Context, adminScope bool) {
	page, limit := pageLimit(c)
	q := a.db.Model(&models.SubscriptionPlan{})
	if adminScope {
		q = q.Unscoped()
	}
	if !adminScope && c.Query("include_inactive") != "true" {
		q = q.Where("is_active = ?", true)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		fail(c, http.StatusInternalServerError, "查询订阅计划总数失败")
		return
	}
	var items []models.SubscriptionPlan
	order := "sort_order ASC, price_monthly ASC"
	if adminScope {
		order = "CASE WHEN deleted_at IS NULL THEN 0 ELSE 1 END ASC, sort_order ASC, price_monthly ASC"
	}
	if err := q.Order(order).Offset((page - 1) * limit).Limit(limit).Find(&items).Error; err != nil {
		fail(c, http.StatusInternalServerError, "查询订阅计划失败")
		return
	}
	out := make([]gin.H, 0, len(items))
	for _, item := range items {
		out = append(out, serializeSubscriptionPlan(item))
	}
	ok(c, gin.H{"items": out, "plans": out, "total": total, "page": page, "limit": limit, "per_page": limit, "pages": (total + int64(limit) - 1) / int64(limit), "total_pages": (total + int64(limit) - 1) / int64(limit), "mode": "creem_subscription"})
}

func (a *App) getSubscriptionPlan(c *gin.Context) {
	id := firstParam(c, "plan_id", "package_id", "id")
	plan, okPlan := a.findSubscriptionPlan(c, id, "", false)
	if !okPlan {
		return
	}
	ok(c, serializeSubscriptionPlan(plan))
}

func (a *App) createSubscriptionPlan(c *gin.Context) {
	var req struct {
		Name                  string         `json:"name"`
		PlanName              string         `json:"plan_name"`
		DisplayName           string         `json:"display_name"`
		Description           *string        `json:"description"`
		MonthlyCredits        int64          `json:"monthly_credits"`
		MonthlyTokenLimit     int64          `json:"monthly_token_limit"`
		PriceMonthly          any            `json:"price_monthly"`
		PriceYearly           any            `json:"price_yearly"`
		Currency              string         `json:"currency"`
		CreemProductIDMonthly string         `json:"creem_product_id_monthly"`
		CreemProductIDYearly  string         `json:"creem_product_id_yearly"`
		AlipayPlanID          string         `json:"alipay_plan_id"`
		AlipayProductID       string         `json:"alipay_product_id"`
		ProviderEnabled       bool           `json:"subscription_provider_enabled"`
		Features              []string       `json:"features"`
		Limits                map[string]any `json:"limits"`
		IsActive              *bool          `json:"is_active"`
		IsFeatured            bool           `json:"is_featured"`
		SortOrder             int            `json:"sort_order"`
	}
	if !bindJSON(c, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.PlanName = strings.TrimSpace(req.PlanName)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if req.Name == "" {
		req.Name = req.PlanName
	}
	if req.Name == "" {
		req.Name = generatedResourceName("plan")
	}
	if req.DisplayName == "" {
		req.DisplayName = req.Name
	}
	if req.MonthlyCredits == 0 {
		req.MonthlyCredits = req.MonthlyTokenLimit
	}
	priceMonthly, okMonthlyPrice := parseJSONFloat(req.PriceMonthly)
	priceYearly, okYearlyPrice := parseJSONFloat(req.PriceYearly)
	if req.Name == "" || req.DisplayName == "" || req.MonthlyCredits <= 0 || !okMonthlyPrice || !okYearlyPrice || priceMonthly <= 0 || priceYearly <= 0 {
		fail(c, http.StatusBadRequest, "订阅计划名称、显示名称、月度 Credits 和价格不能为空")
		return
	}
	if req.ProviderEnabled && (strings.TrimSpace(req.AlipayPlanID) == "" || strings.TrimSpace(req.AlipayProductID) == "") {
		fail(c, http.StatusBadRequest, "启用支付宝订阅前必须配置 Plan ID 和 Product ID")
		return
	}
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}
	features, _ := json.Marshal(req.Features)
	limits, _ := json.Marshal(req.Limits)
	plan := models.SubscriptionPlan{
		Name:                        req.Name,
		DisplayName:                 req.DisplayName,
		Description:                 req.Description,
		MonthlyCredits:              req.MonthlyCredits,
		PriceMonthly:                priceMonthly,
		PriceYearly:                 priceYearly,
		Currency:                    defaultString(req.Currency, "USD"),
		CreemProductIDMonthly:       strings.TrimSpace(req.CreemProductIDMonthly),
		CreemProductIDYearly:        strings.TrimSpace(req.CreemProductIDYearly),
		AlipayPlanID:                strings.TrimSpace(req.AlipayPlanID),
		AlipayProductID:             strings.TrimSpace(req.AlipayProductID),
		SubscriptionProviderEnabled: req.ProviderEnabled,
		Features:                    datatypes.JSON(features),
		Limits:                      datatypes.JSON(limits),
		IsActive:                    isActive,
		IsFeatured:                  req.IsFeatured,
		SortOrder:                   req.SortOrder,
	}
	if err := a.db.Create(&plan).Error; err != nil {
		fail(c, http.StatusConflict, "创建订阅计划失败")
		return
	}
	created(c, serializeSubscriptionPlan(plan))
}

func (a *App) updateSubscriptionPlan(c *gin.Context) {
	id := firstParam(c, "plan_id", "id")
	plan, okPlan := a.findSubscriptionPlan(c, id, "", false)
	if !okPlan {
		return
	}
	var req map[string]any
	if !bindJSON(c, &req) {
		return
	}
	if features, ok := req["features"]; ok {
		raw, _ := json.Marshal(features)
		req["features"] = datatypes.JSON(raw)
	}
	if limits, ok := req["limits"]; ok {
		raw, _ := json.Marshal(limits)
		req["limits"] = datatypes.JSON(raw)
	}
	if _, ok := req["monthly_credits"]; !ok {
		if value, exists := req["monthly_token_limit"]; exists {
			req["monthly_credits"] = value
		}
	}
	delete(req, "monthly_token_limit")
	delete(req, "id")
	delete(req, "name")
	delete(req, "plan_name")
	delete(req, "is_deleted")
	delete(req, "deleted_at")
	if err := normalizeJSONFloatField(req, "price_monthly"); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := normalizeJSONFloatField(req, "price_yearly"); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.db.Model(&plan).Updates(req).Error; err != nil {
		fail(c, http.StatusConflict, "更新订阅计划失败")
		return
	}
	a.db.First(&plan, "id = ?", plan.ID)
	ok(c, serializeSubscriptionPlan(plan))
}

func parseJSONFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		parsed, err := v.Float64()
		return parsed, err == nil
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func normalizeJSONFloatField(data map[string]any, field string) error {
	value, ok := data[field]
	if !ok || value == nil {
		return nil
	}
	parsed, valid := parseJSONFloat(value)
	if !valid {
		return fmt.Errorf("%s 必须是数字", field)
	}
	data[field] = parsed
	return nil
}

func (a *App) deleteSubscriptionPlan(c *gin.Context) {
	id := firstParam(c, "plan_id", "id")
	if c.Query("hard") == "true" {
		plan, okPlan := a.findSubscriptionPlanForDelete(c, id, true)
		if !okPlan {
			return
		}
		if err := a.db.Unscoped().Delete(&models.SubscriptionPlan{}, "id = ?", plan.ID).Error; err != nil {
			fail(c, http.StatusInternalServerError, "永久删除订阅计划失败")
			return
		}
		ok(c, gin.H{"message": "订阅计划已永久删除", "success": true, "hard_deleted": true})
		return
	}
	plan, okPlan := a.findSubscriptionPlanForDelete(c, id, false)
	if !okPlan {
		return
	}
	if err := a.db.Delete(&models.SubscriptionPlan{}, "id = ?", plan.ID).Error; err != nil {
		fail(c, http.StatusInternalServerError, "删除订阅计划失败")
		return
	}
	ok(c, gin.H{"message": "订阅计划已删除", "success": true, "hard_deleted": false})
}

func (a *App) findSubscriptionPlanForDelete(c *gin.Context, raw string, includeDeleted bool) (models.SubscriptionPlan, bool) {
	var plan models.SubscriptionPlan
	raw = strings.TrimSpace(raw)
	if raw == "" {
		fail(c, http.StatusBadRequest, "缺少订阅计划ID")
		return plan, false
	}
	q := a.db.Model(&models.SubscriptionPlan{})
	if includeDeleted {
		q = q.Unscoped()
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

func (a *App) currentSubscription(c *gin.Context) {
	user := getUser(c)
	account, snapshot, err := a.creditBalanceSnapshot(user.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询订阅失败")
		return
	}
	sub, hasSub, err := a.currentUserSubscription(user.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询订阅失败")
		return
	}
	var subscription any
	if hasSub {
		subscription = a.serializeUserSubscription(sub)
	}
	ok(c, gin.H{
		"success":                 true,
		"mode":                    "creem_subscription",
		"subscription":            subscription,
		"purchased_credits":       snapshot.PurchasedRemaining,
		"subscription_credits":    snapshot.SubscriptionRemaining,
		"remaining_credits":       snapshot.RemainingCredits,
		"reserved_credits":        snapshot.ReservedCredits,
		"gross_remaining_credits": snapshot.GrossRemainingCredits,
		"token_account": gin.H{
			"user_id":                 user.ID.String(),
			"total_tokens":            account.TotalTokens,
			"used_tokens":             account.UsedTokens,
			"remaining_tokens":        account.RemainingTokens(),
			"total_credits":           snapshot.TotalCredits,
			"used_credits":            snapshot.UsedCredits,
			"remaining_credits":       snapshot.RemainingCredits,
			"reserved_credits":        snapshot.ReservedCredits,
			"gross_remaining_credits": snapshot.GrossRemainingCredits,
			"total_recharged":         account.TotalRecharged,
			"last_recharge_at":        account.LastRechargeAt,
			"updated_at":              account.UpdatedAt,
		},
	})
}
func (a *App) initializeSubscription(c *gin.Context) {
	a.currentSubscription(c)
}
func (a *App) upgradeSubscription(c *gin.Context) {
	a.createSubscriptionCheckout(c)
}
func (a *App) cancelSubscription(c *gin.Context) {
	user := getUser(c)
	sub, hasSub, err := a.currentUserSubscription(user.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询订阅失败")
		return
	}
	if !hasSub {
		fail(c, http.StatusNotFound, "当前用户没有有效订阅")
		return
	}
	if sub.CreemSubscriptionID != "" && a.creem.Configured() {
		if err := a.creem.CancelSubscription(c.Request.Context(), sub.CreemSubscriptionID, true); err != nil {
			fail(c, http.StatusBadGateway, err.Error())
			return
		}
	}
	sub.CancelAtPeriodEnd = true
	sub.Status = "scheduled_cancel"
	now := time.Now().UTC()
	sub.CanceledAt = &now
	if err := a.db.Save(&sub).Error; err != nil {
		fail(c, http.StatusInternalServerError, "更新订阅状态失败")
		return
	}
	ok(c, gin.H{"success": true, "message": "订阅将在当前周期结束时取消", "subscription": a.serializeUserSubscription(sub)})
}
func (a *App) debugUserSubscription(c *gin.Context) {
	id, okID := parseIDParam(c, "user_id")
	if !okID {
		return
	}
	account, snapshot, err := a.creditBalanceSnapshot(id)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询订阅失败")
		return
	}
	var subs []models.UserSubscription
	a.db.Where("user_id = ?", id).Order("updated_at DESC").Find(&subs)
	ok(c, gin.H{"user_id": id, "subscriptions": subs, "token_account": account, "credit_snapshot": snapshot})
}
func (a *App) subscriptionHealth(c *gin.Context) {
	ok(c, gin.H{
		"status": "healthy", "mode": "alipay_subscription", "subscriptions_enabled": true,
		"subscription_payment_provider": a.cfg.SubscriptionPaymentProvider,
		"credit_payment_provider":       a.cfg.CreditPaymentProvider,
		"alipay_configured":             a.alipay != nil && a.alipay.Configured(),
		"creem_configured":              a.creem.Configured(),
		"creem_checkout_enabled":        a.cfg.NewCreemCheckoutEnabled(),
		"token_packages_enabled":        true, "checked_at": time.Now().UTC(),
	})
}
func (a *App) handleExpiredSubscriptions(c *gin.Context) {
	now := time.Now().UTC()
	if err := a.ensureDueSubscriptionCreditsTx(a.db, nil, now); err != nil {
		fail(c, http.StatusInternalServerError, "处理订阅发放失败")
		return
	}
	result := a.db.Model(&models.SubscriptionCreditGrant{}).Where("status = ? AND expires_at <= ?", "active", now).Update("status", "expired")
	if result.Error != nil {
		fail(c, http.StatusInternalServerError, "处理订阅额度过期失败")
		return
	}
	affected := result.RowsAffected
	ok(c, gin.H{"success": true, "mode": "creem_subscription", "affected_count": affected, "message": "订阅额度过期处理完成"})
}

func (a *App) subscriptionStats(c *gin.Context) {
	var packages, activePackages, payments, paidPayments int64
	var revenue float64
	var creemSessions, creemCompleted int64
	var creemRevenue float64
	a.db.Model(&models.SubscriptionPlan{}).Count(&packages)
	a.db.Model(&models.SubscriptionPlan{}).Where("is_active = ?", true).Count(&activePackages)
	a.db.Model(&models.WeChatPayment{}).Count(&payments)
	a.db.Model(&models.WeChatPayment{}).Where("status = ?", "paid").Count(&paidPayments)
	a.db.Model(&models.WeChatPayment{}).Where("status = ?", "paid").Select("coalesce(sum(total_amount), 0)").Scan(&revenue)
	a.db.Model(&models.CreemCheckoutSession{}).Count(&creemSessions)
	a.db.Model(&models.CreemCheckoutSession{}).Where("status = ?", "completed").Count(&creemCompleted)
	a.db.Model(&models.CreemCheckoutSession{}).Where("status = ?", "completed").Select("coalesce(sum(amount), 0)").Scan(&creemRevenue)
	var activeSubscribers int64
	a.db.Model(&models.UserSubscription{}).Where("status IN ?", []string{subscriptionStatusActive, subscriptionStatusTrialing, subscriptionStatusScheduledCancel}).Count(&activeSubscribers)
	ok(c, gin.H{"mode": "creem_subscription", "plans_total": packages, "plans_active": activePackages, "active_subscribers": activeSubscribers, "wechat_payments_total": payments, "wechat_payments_paid": paidPayments, "wechat_revenue": revenue, "creem_checkouts_total": creemSessions, "creem_checkouts_completed": creemCompleted, "creem_revenue": creemRevenue, "total_revenue": revenue + creemRevenue, "last_updated": time.Now().UTC()})
}

func (a *App) resetMonthlyTokens(c *gin.Context) {
	if err := a.ensureDueSubscriptionCreditsTx(a.db, nil, time.Now().UTC()); err != nil {
		fail(c, http.StatusInternalServerError, "处理订阅额度发放失败")
		return
	}
	ok(c, gin.H{"success": true, "mode": "creem_subscription", "message": "订阅周期 Credits 发放任务完成"})
}

func (a *App) consumeSubscriptionTokens(c *gin.Context) {
	user, okUser := currentUserFromContext(c)
	if !okUser {
		fail(c, http.StatusUnauthorized, "需要登录")
		return
	}
	var req struct {
		TokenCount      int64          `json:"token_count" binding:"required"`
		APIEndpoint     string         `json:"api_endpoint" binding:"required"`
		ModelName       string         `json:"model_name" binding:"required"`
		ProjectID       *string        `json:"project_id"`
		RequestMetadata map[string]any `json:"request_metadata"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if req.TokenCount <= 0 {
		fail(c, http.StatusBadRequest, "token_count必须大于0")
		return
	}
	var projectID *uuid.UUID
	if req.ProjectID != nil && strings.TrimSpace(*req.ProjectID) != "" {
		id, err := uuid.Parse(strings.TrimSpace(*req.ProjectID))
		if err != nil {
			fail(c, http.StatusBadRequest, "无效的project_id")
			return
		}
		var count int64
		if err := a.db.Model(&models.Project{}).Where("id = ? AND owner_id = ?", id, user.ID).Count(&count).Error; err != nil {
			fail(c, http.StatusInternalServerError, "校验项目失败")
			return
		}
		if count == 0 {
			fail(c, http.StatusNotFound, "项目不存在")
			return
		}
		projectID = &id
	}
	if err := a.consumeTokens(user.ID, req.TokenCount, req.APIEndpoint, req.ModelName, projectID, req.RequestMetadata); err != nil {
		fail(c, http.StatusPaymentRequired, err.Error())
		return
	}
	account, err := a.ensureTokenAccount(user.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询 Token 账户失败")
		return
	}
	ok(c, gin.H{"success": true, "remaining_tokens": account.RemainingTokens(), "used_tokens": account.UsedTokens, "total_tokens": account.TotalTokens, "message": "Token消费成功"})
}

func (a *App) createNotificationsBatch(c *gin.Context) {
	var req struct {
		Notifications []struct {
			UserID    string         `json:"user_id"`
			Type      string         `json:"type"`
			Title     string         `json:"title"`
			Message   string         `json:"message"`
			Data      map[string]any `json:"data"`
			ExpiresAt *time.Time     `json:"expires_at"`
		} `json:"notifications"`
		UserIDs   []string       `json:"user_ids"`
		Type      string         `json:"type"`
		Title     string         `json:"title"`
		Message   string         `json:"message"`
		Data      map[string]any `json:"data"`
		ExpiresAt *time.Time     `json:"expires_at"`
	}
	if !bindJSON(c, &req) {
		return
	}
	items := make([]models.Notification, 0)
	for _, n := range req.Notifications {
		if n.Type == "" {
			n.Type = req.Type
		}
		if n.Title == "" {
			n.Title = req.Title
		}
		if n.Message == "" {
			n.Message = req.Message
		}
		if n.Data == nil {
			n.Data = req.Data
		}
		if n.ExpiresAt == nil {
			n.ExpiresAt = req.ExpiresAt
		}
		item, okItem := buildNotification(n.UserID, n.Type, n.Title, n.Message, n.Data, n.ExpiresAt)
		if okItem {
			items = append(items, item)
		}
	}
	for _, rawID := range req.UserIDs {
		item, okItem := buildNotification(rawID, req.Type, req.Title, req.Message, req.Data, req.ExpiresAt)
		if okItem {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		fail(c, http.StatusBadRequest, "没有有效通知")
		return
	}
	if err := a.db.Create(&items).Error; err != nil {
		fail(c, http.StatusInternalServerError, "批量创建通知失败")
		return
	}
	created(c, gin.H{"success": true, "created_count": len(items), "items": items})
}

func (a *App) cleanupExpiredSystemData(c *gin.Context) {
	now := time.Now().UTC()
	n := a.db.Where("expires_at IS NOT NULL AND expires_at < ?", now).Delete(&models.Notification{}).RowsAffected
	u := a.db.Where("expires_at IS NOT NULL AND expires_at < ?", now).Delete(&models.UploadRecord{}).RowsAffected
	ok(c, gin.H{"success": true, "affected_count": n + u, "notifications_deleted": n, "uploads_deleted": u})
}

func (a *App) respondTokenizedSubscription(c *gin.Context, action string) {
	user, okUser := currentUserFromContext(c)
	if !okUser {
		fail(c, http.StatusUnauthorized, "需要登录")
		return
	}
	account, err := a.ensureTokenAccount(user.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询 Token 账户失败")
		return
	}
	ok(c, gin.H{"success": true, "mode": "tokenized_subscription", "action": action, "subscription": gin.H{"type": "token_account", "status": "active", "plan_name": "tokenized", "current_period_end": nil, "cancel_at_period_end": false}, "token_account": gin.H{"user_id": user.ID.String(), "total_tokens": account.TotalTokens, "used_tokens": account.UsedTokens, "remaining_tokens": account.RemainingTokens(), "total_recharged": account.TotalRecharged, "last_recharge_at": account.LastRechargeAt, "updated_at": account.UpdatedAt}, "message": "当前系统使用 Token 套餐/充值模型，未启用独立订阅模型"})
}

func currentUserFromContext(c *gin.Context) (models.User, bool) {
	value, exists := c.Get("user")
	if !exists {
		return models.User{}, false
	}
	user, ok := value.(models.User)
	return user, ok && user.ID != uuid.Nil
}

func firstParam(c *gin.Context, names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(c.Param(name)); v != "" {
			return v
		}
	}
	return ""
}

func (a *App) findCompatPackage(packageID, planID, planName string) (models.TokenPackage, bool, error) {
	idText := strings.TrimSpace(packageID)
	if idText == "" {
		idText = strings.TrimSpace(planID)
	}
	var pkg models.TokenPackage
	if idText != "" {
		id, err := uuid.Parse(idText)
		if err != nil {
			return pkg, false, fmt.Errorf("无效套餐ID")
		}
		if err := a.db.First(&pkg, "id = ? AND is_active = ?", id, true).Error; err != nil {
			return pkg, false, fmt.Errorf("套餐不存在")
		}
		return pkg, true, nil
	}
	if strings.TrimSpace(planName) != "" {
		like := strings.TrimSpace(planName)
		if err := a.db.Where("is_active = ? AND (name = ? OR display_name = ?)", true, like, like).First(&pkg).Error; err != nil {
			return pkg, false, fmt.Errorf("套餐不存在")
		}
		return pkg, true, nil
	}
	return pkg, false, nil
}

func (a *App) getCompatPackageByIDOrName(c *gin.Context, raw string) (models.TokenPackage, bool) {
	var pkg models.TokenPackage
	if raw == "" {
		fail(c, http.StatusBadRequest, "缺少套餐ID")
		return pkg, false
	}
	if id, err := uuid.Parse(raw); err == nil {
		if err := a.db.First(&pkg, "id = ?", id).Error; err != nil {
			fail(c, http.StatusNotFound, "套餐不存在")
			return pkg, false
		}
		return pkg, true
	}
	if err := a.db.Where("name = ? OR display_name = ?", raw, raw).First(&pkg).Error; err != nil {
		fail(c, http.StatusNotFound, "套餐不存在")
		return pkg, false
	}
	return pkg, true
}

func (a *App) updateCompatPackage(c *gin.Context) {
	raw := firstParam(c, "plan_id", "package_id", "id")
	pkg, okPkg := a.getCompatPackageByIDOrName(c, raw)
	if !okPkg {
		return
	}
	var req map[string]any
	if !bindJSON(c, &req) {
		return
	}
	a.db.Model(&pkg).Updates(req)
	a.db.First(&pkg, "id = ?", pkg.ID)
	ok(c, compatSubscriptionPlanResponse(pkg))
}

func (a *App) deleteCompatPackage(c *gin.Context) {
	raw := firstParam(c, "plan_id", "package_id", "id")
	pkg, okPkg := a.getCompatPackageByIDOrName(c, raw)
	if !okPkg {
		return
	}
	a.db.Delete(&models.TokenPackage{}, "id = ?", pkg.ID)
	ok(c, gin.H{"message": "订阅套餐已删除", "success": true, "mode": "tokenized_subscription"})
}

func compatSubscriptionPlanResponse(item models.TokenPackage) gin.H {
	total := item.TokenAmount + item.BonusTokens
	unit := 0.0
	if item.TokenAmount > 0 {
		unit = item.Price / (float64(item.TokenAmount) / 1000)
	}
	return gin.H{"id": item.ID.String(), "plan_id": item.ID.String(), "package_id": item.ID.String(), "name": item.Name, "plan_name": item.Name, "display_name": item.DisplayName, "description": item.Description, "token_amount": item.TokenAmount, "bonus_tokens": item.BonusTokens, "total_tokens_with_bonus": total, "price": item.Price, "amount": item.Price, "currency": "CNY", "interval": "one_time", "is_active": item.IsActive, "is_featured": item.IsFeatured, "sort_order": item.SortOrder, "unit_price": unit, "mode": "tokenized_subscription", "created_at": item.CreatedAt, "updated_at": item.UpdatedAt}
}

func (a *App) loadCompatPayment(c *gin.Context) (models.WeChatPayment, bool) {
	raw := firstParam(c, "out_trade_no", "payment_id", "id")
	if raw == "" {
		raw = strings.TrimSpace(c.Query("out_trade_no"))
	}
	if raw == "" {
		raw = strings.TrimSpace(c.Query("payment_id"))
	}
	if raw == "" && c.Request.ContentLength != 0 {
		var req map[string]string
		_ = c.ShouldBindJSON(&req)
		raw = strings.TrimSpace(req["out_trade_no"])
		if raw == "" {
			raw = strings.TrimSpace(req["payment_id"])
		}
	}
	if raw == "" {
		fail(c, http.StatusBadRequest, "缺少订单号")
		return models.WeChatPayment{}, false
	}
	var payment models.WeChatPayment
	q := a.db.Model(&models.WeChatPayment{})
	if id, err := uuid.Parse(raw); err == nil {
		q = q.Where("id = ?", id)
	} else {
		q = q.Where("out_trade_no = ?", raw)
	}
	if user, hasUser := currentUserFromContext(c); hasUser && !user.IsAdmin {
		q = q.Where("user_id = ?", user.ID)
	}
	if err := q.First(&payment).Error; err != nil {
		fail(c, http.StatusNotFound, "订单不存在")
		return payment, false
	}
	return payment, true
}

func compatPaymentResponse(payment models.WeChatPayment) gin.H {
	return gin.H{"success": true, "mode": "tokenized_subscription", "payment_id": payment.ID.String(), "out_trade_no": payment.OutTradeNo, "transaction_id": payment.TransactionID, "trade_state": payment.TradeState, "trade_state_desc": payment.TradeStateDesc, "status": payment.Status, "total_amount": payment.TotalAmount, "amount": payment.TotalAmount, "currency": payment.Currency, "code_url": payment.CodeURL, "subject": payment.Subject, "success_time": payment.SuccessTime, "expires_at": payment.TimeExpire, "created_at": payment.CreatedAt, "updated_at": payment.UpdatedAt}
}

func buildNotification(userIDText, typ, title, message string, data map[string]any, expiresAt *time.Time) (models.Notification, bool) {
	userID, err := uuid.Parse(strings.TrimSpace(userIDText))
	if err != nil || typ == "" || title == "" || message == "" {
		return models.Notification{}, false
	}
	return models.Notification{UserID: userID, Type: typ, Title: title, Message: message, Data: jsonMap(data), ExpiresAt: expiresAt}, true
}
