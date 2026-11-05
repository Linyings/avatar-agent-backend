package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (a *App) tokenBalance(c *gin.Context) {
	user := getUser(c)
	account, snapshot, err := a.creditBalanceSnapshot(user.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询 Credits 账户失败")
		return
	}
	trialClaimed, err := creditTrialClaimed(a.db, user.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询体验套餐资格失败")
		return
	}
	ok(c, gin.H{
		"user_id":                    user.ID.String(),
		"total_tokens":               snapshot.TotalCredits,
		"used_tokens":                snapshot.UsedCredits,
		"remaining_tokens":           snapshot.RemainingCredits,
		"total_credits":              snapshot.TotalCredits,
		"used_credits":               snapshot.UsedCredits,
		"remaining_credits":          snapshot.RemainingCredits,
		"reserved_credits":           snapshot.ReservedCredits,
		"gross_remaining_credits":    snapshot.GrossRemainingCredits,
		"purchased_credits":          snapshot.PurchasedRemaining,
		"purchased_total_credits":    snapshot.PurchasedTotal,
		"purchased_used_credits":     snapshot.PurchasedUsed,
		"subscription_credits":       snapshot.SubscriptionRemaining,
		"subscription_total_credits": snapshot.SubscriptionTotal,
		"subscription_used_credits":  snapshot.SubscriptionUsed,
		"next_expiry_at":             snapshot.NextExpiryAt,
		"trial_claimed":              trialClaimed,
		"total_recharged":            account.TotalRecharged,
		"last_recharge_at":           account.LastRechargeAt,
		"created_at":                 account.CreatedAt,
		"updated_at":                 account.UpdatedAt,
	})
}

func (a *App) listTokenPackages(c *gin.Context) {
	a.listTokenPackagesWithScope(c, false)
}

func (a *App) listTokenPackagesAdmin(c *gin.Context) {
	a.listTokenPackagesWithScope(c, true)
}

func (a *App) listTokenPackagesWithScope(c *gin.Context, adminScope bool) {
	page, limit := pageLimit(c)
	var total int64
	q := a.db.Model(&models.TokenPackage{})
	if adminScope {
		q = q.Unscoped()
	}
	if !adminScope && c.Query("include_inactive") != "true" {
		q = q.Where("is_active = ?", true)
	}
	if err := q.Count(&total).Error; err != nil {
		fail(c, http.StatusInternalServerError, "查询充值套餐总数失败")
		return
	}
	var items []models.TokenPackage
	order := "sort_order ASC, price ASC"
	if adminScope {
		order = "CASE WHEN deleted_at IS NULL THEN 0 ELSE 1 END ASC, sort_order ASC, price ASC"
	}
	if err := q.Order(order).Offset((page - 1) * limit).Limit(limit).Find(&items).Error; err != nil {
		fail(c, http.StatusInternalServerError, "查询充值套餐失败")
		return
	}
	ok(c, gin.H{"items": tokenPackageResponses(items), "total": total, "page": page, "limit": limit, "pages": (total + int64(limit) - 1) / int64(limit)})
}

func tokenPackageResponses(items []models.TokenPackage) []gin.H {
	out := make([]gin.H, 0, len(items))
	for _, item := range items {
		total := item.TokenAmount + item.BonusTokens
		unit := 0.0
		if item.TokenAmount > 0 {
			unit = item.Price / (float64(item.TokenAmount) / 1000)
		}
		out = append(out, gin.H{
			"id":                          item.ID.String(),
			"name":                        item.Name,
			"display_name":                item.DisplayName,
			"description":                 item.Description,
			"token_amount":                item.TokenAmount,
			"credit_amount":               item.TokenAmount,
			"price":                       item.Price,
			"bonus_tokens":                item.BonusTokens,
			"bonus_credits":               item.BonusTokens,
			"creem_product_id":            item.CreemProductID,
			"creem_enabled":               item.CreemEnabled,
			"creem_currency":              defaultString(item.CreemCurrency, "USD"),
			"currency":                    item.Currency,
			"validity_hours":              item.ValidityHours,
			"is_trial":                    item.IsTrial,
			"per_account_purchase_limit":  item.PerAccountPurchaseLimit,
			"is_active":                   item.IsActive,
			"is_featured":                 item.IsFeatured,
			"sort_order":                  item.SortOrder,
			"is_deleted":                  item.DeletedAt.Valid,
			"deleted_at":                  deletedAtValue(item.DeletedAt),
			"created_at":                  item.CreatedAt,
			"updated_at":                  item.UpdatedAt,
			"total_tokens_with_bonus":     total,
			"total_credits_with_bonus":    total,
			"unit_price":                  unit,
			"unit_price_per_1000_credits": unit,
		})
	}
	return out
}

func normalizeTokenPackageUpdateAliases(req map[string]any) {
	if _, ok := req["token_amount"]; !ok {
		if value, exists := req["credit_amount"]; exists {
			req["token_amount"] = value
		}
	}
	if _, ok := req["bonus_tokens"]; !ok {
		if value, exists := req["bonus_credits"]; exists {
			req["bonus_tokens"] = value
		}
	}
}

func tokenPackageUpdateValues(req map[string]any) map[string]any {
	normalizeTokenPackageUpdateAliases(req)
	allowed := map[string]bool{
		"display_name":               true,
		"description":                true,
		"token_amount":               true,
		"price":                      true,
		"bonus_tokens":               true,
		"creem_product_id":           true,
		"creem_enabled":              true,
		"creem_currency":             true,
		"is_active":                  true,
		"is_featured":                true,
		"sort_order":                 true,
		"is_trial":                   true,
		"per_account_purchase_limit": true,
	}
	values := make(map[string]any, len(req))
	for key, value := range req {
		if allowed[key] {
			values[key] = value
		}
	}
	return values
}

func stringFromMap(req map[string]any, key string) string {
	if value, ok := req[key].(string); ok {
		return value
	}
	return ""
}

func boolFromMap(req map[string]any, key string) bool {
	if value, ok := req[key].(bool); ok {
		return value
	}
	return false
}

func (a *App) getTokenPackage(c *gin.Context) {
	id, okID := parseIDParam(c, "package_id")
	if !okID {
		return
	}
	var pkg models.TokenPackage
	if err := a.db.First(&pkg, "id = ?", id).Error; err != nil {
		fail(c, http.StatusNotFound, "套餐不存在")
		return
	}
	ok(c, tokenPackageResponses([]models.TokenPackage{pkg})[0])
}

func (a *App) createTokenPackage(c *gin.Context) {
	var req map[string]any
	if !bindJSON(c, &req) {
		return
	}
	normalizeTokenPackageUpdateAliases(req)
	name := strings.TrimSpace(stringFromMap(req, "name"))
	if name == "" {
		name = generatedResourceName("pkg")
	}
	pkg := models.TokenPackage{
		Name:                    name,
		DisplayName:             strings.TrimSpace(stringFromMap(req, "display_name")),
		TokenAmount:             int64(floatValue(req["token_amount"])),
		Price:                   floatValue(req["price"]),
		BonusTokens:             int64(floatValue(req["bonus_tokens"])),
		CreemEnabled:            boolFromMap(req, "creem_enabled"),
		CreemCurrency:           defaultString(stringFromMap(req, "creem_currency"), "USD"),
		IsActive:                true,
		IsFeatured:              boolFromMap(req, "is_featured"),
		SortOrder:               int(floatValue(req["sort_order"])),
		Currency:                "CNY",
		ValidityHours:           720,
		IsTrial:                 boolFromMap(req, "is_trial"),
		PerAccountPurchaseLimit: int(floatValue(req["per_account_purchase_limit"])),
	}
	if description := strings.TrimSpace(stringFromMap(req, "description")); description != "" {
		pkg.Description = &description
	}
	if productID := strings.TrimSpace(stringFromMap(req, "creem_product_id")); productID != "" {
		pkg.CreemProductID = &productID
	}
	if value, ok := req["is_active"].(bool); ok {
		pkg.IsActive = value
	}
	if pkg.DisplayName == "" || pkg.TokenAmount <= 0 {
		fail(c, http.StatusBadRequest, "显示名称和 Credits 数量不能为空")
		return
	}
	if err := a.db.Create(&pkg).Error; err != nil {
		fail(c, http.StatusConflict, "创建套餐失败")
		return
	}
	created(c, tokenPackageResponses([]models.TokenPackage{pkg})[0])
}

func (a *App) updateTokenPackage(c *gin.Context) {
	id, okID := parseIDParam(c, "package_id")
	if !okID {
		return
	}
	var pkg models.TokenPackage
	if err := a.db.First(&pkg, "id = ?", id).Error; err != nil {
		fail(c, http.StatusNotFound, "套餐不存在")
		return
	}
	var req map[string]any
	if !bindJSON(c, &req) {
		return
	}
	values := tokenPackageUpdateValues(req)
	if err := a.db.Model(&pkg).Updates(values).Error; err != nil {
		fail(c, http.StatusConflict, "更新套餐失败")
		return
	}
	a.db.First(&pkg, "id = ?", id)
	ok(c, tokenPackageResponses([]models.TokenPackage{pkg})[0])
}

func (a *App) deleteTokenPackage(c *gin.Context) {
	id, okID := parseIDParam(c, "package_id")
	if !okID {
		return
	}
	if c.Query("hard") == "true" {
		if err := a.db.Unscoped().Delete(&models.TokenPackage{}, "id = ?", id).Error; err != nil {
			fail(c, http.StatusInternalServerError, "永久删除套餐失败")
			return
		}
		ok(c, gin.H{"message": "套餐已永久删除", "success": true, "hard_deleted": true})
		return
	}
	if err := a.db.Delete(&models.TokenPackage{}, "id = ?", id).Error; err != nil {
		fail(c, http.StatusInternalServerError, "删除套餐失败")
		return
	}
	ok(c, gin.H{"message": "套餐已删除", "success": true, "hard_deleted": false})
}

func (a *App) createRecharge(c *gin.Context) {
	paymentChannelError(c, http.StatusConflict, "payment_channel_disabled", "Legacy Credits recharge is disabled; use /api/v1/credit-payments")
	return

	user := getUser(c)
	var req struct {
		PackageID string `json:"package_id" binding:"required"`
	}
	if !bindJSON(c, &req) {
		return
	}
	pkgID, err := uuid.Parse(req.PackageID)
	if err != nil {
		fail(c, http.StatusBadRequest, "无效的套餐ID")
		return
	}
	var pkg models.TokenPackage
	if err := a.db.First(&pkg, "id = ? AND is_active = ?", pkgID, true).Error; err != nil {
		fail(c, http.StatusNotFound, "套餐不存在")
		return
	}
	codeURL := "weixin://wxpay/mock/" + randomCode("")
	outTradeNo := fmt.Sprintf("N%s", time.Now().Format("20060102150405")) + randomCode("")
	expire := time.Now().Add(30 * time.Minute)
	wechatAmount := usdToWeChatCNYAmount(pkg.Price)
	payment := models.WeChatPayment{UserID: user.ID, OutTradeNo: outTradeNo[:min(len(outTradeNo), 32)], CodeURL: &codeURL, Subject: pkg.DisplayName, TotalAmount: wechatAmount, Currency: "CNY", TradeType: "NATIVE", TradeState: "NOTPAY", Status: "pending", TimeExpire: &expire}
	payment.Attach = ptrString(pkg.ID.String())
	if a.wechat.Configured() {
		resp, err := a.wechat.CreateNativeOrder(c.Request.Context(), providers.WeChatNativeOrderRequest{
			Description: pkg.DisplayName,
			OutTradeNo:  payment.OutTradeNo,
			TotalAmount: wechatAmount,
			Attach:      pkg.ID.String(),
			TimeExpire:  expire,
		})
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
	ok(c, gin.H{"payment_id": payment.ID.String(), "out_trade_no": payment.OutTradeNo, "code_url": codeURL, "package_info": tokenPackageResponses([]models.TokenPackage{pkg})[0], "total_amount": wechatAmount, "currency": "CNY", "source_amount": pkg.Price, "source_currency": defaultString(pkg.CreemCurrency, "USD"), "exchange_rate": usdToCNYRateForWeChat, "expires_in": 1800})
}

func (a *App) rechargeStatus(c *gin.Context) {
	user := getUser(c)
	var payment models.WeChatPayment
	if err := a.db.First(&payment, "out_trade_no = ? AND user_id = ?", c.Param("out_trade_no"), user.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "订单不存在")
		return
	}
	if payment.Status != "paid" && a.wechat.Configured() {
		if synced, err := a.syncPaymentFromWeChat(c.Request.Context(), payment.OutTradeNo); err != nil {
			fmt.Printf("微信支付查单同步失败 out_trade_no=%s err=%v\n", payment.OutTradeNo, err)
		} else if synced.ID != uuid.Nil {
			payment = synced
		}
	}
	ok(c, gin.H{"payment_id": payment.ID.String(), "out_trade_no": payment.OutTradeNo, "transaction_id": payment.TransactionID, "trade_state": payment.TradeState, "trade_state_desc": payment.TradeStateDesc, "status": payment.Status, "total_amount": payment.TotalAmount, "success_time": payment.SuccessTime, "created_at": payment.CreatedAt, "updated_at": payment.UpdatedAt})
}

func (a *App) syncPaymentFromWeChat(ctx context.Context, outTradeNo string) (models.WeChatPayment, error) {
	tx, err := a.wechat.QueryTransactionByOutTradeNo(ctx, outTradeNo)
	if err != nil {
		return models.WeChatPayment{}, err
	}
	if tx.TradeState == "SUCCESS" {
		if err := a.processPaidTransaction(tx); err != nil {
			return models.WeChatPayment{}, err
		}
	} else if tx.TradeState != "" {
		if err := a.updatePendingPaymentTradeState(outTradeNo, tx); err != nil {
			return models.WeChatPayment{}, err
		}
	}
	var payment models.WeChatPayment
	if err := a.db.First(&payment, "out_trade_no = ?", outTradeNo).Error; err != nil {
		return models.WeChatPayment{}, err
	}
	return payment, nil
}

func (a *App) updatePendingPaymentTradeState(outTradeNo string, tx providers.WeChatTransaction) error {
	if strings.TrimSpace(outTradeNo) == "" || strings.TrimSpace(tx.TradeState) == "" {
		return nil
	}
	updates := map[string]any{
		"trade_state": tx.TradeState,
		"updated_at":  time.Now().UTC(),
	}
	if tx.TradeStateDesc != "" {
		updates["trade_state_desc"] = tx.TradeStateDesc
	}
	if tx.TransactionID != "" {
		updates["transaction_id"] = tx.TransactionID
	}
	switch tx.TradeState {
	case "CLOSED", "REVOKED", "PAYERROR":
		updates["status"] = "failed"
	}
	return a.db.Model(&models.WeChatPayment{}).
		Where("out_trade_no = ? AND status <> ?", outTradeNo, "paid").
		Updates(updates).Error
}

func (a *App) wechatWebhook(c *gin.Context) {
	body, _ := io.ReadAll(c.Request.Body)
	if len(body) == 0 {
		fail(c, http.StatusBadRequest, "空回调请求")
		return
	}

	if a.wechat.Configured() {
		if err := a.wechat.VerifyNotificationSignature(
			c.GetHeader("Wechatpay-Timestamp"),
			c.GetHeader("Wechatpay-Nonce"),
			body,
			c.GetHeader("Wechatpay-Signature"),
			c.GetHeader("Wechatpay-Serial"),
		); err != nil {
			fail(c, http.StatusUnauthorized, err.Error())
			return
		}
		tx, err := a.wechat.DecryptNotification(body)
		if err != nil {
			fail(c, http.StatusBadRequest, "微信回调解密失败: "+err.Error())
			return
		}
		if err := a.processPaidTransaction(tx); err != nil {
			fail(c, http.StatusInternalServerError, err.Error())
			return
		}
		ok(c, gin.H{"code": "SUCCESS", "message": "成功"})
		return
	}

	var tx providers.WeChatTransaction
	if err := json.Unmarshal(body, &tx); err == nil && tx.OutTradeNo != "" {
		if err := a.processPaidTransaction(tx); err != nil {
			fail(c, http.StatusInternalServerError, err.Error())
			return
		}
	}
	ok(c, gin.H{"message": "微信支付回调已接收（未配置验签，开发模式降级）", "success": true})
}

func (a *App) processPaidTransaction(tx providers.WeChatTransaction) error {
	if tx.OutTradeNo == "" {
		return fmt.Errorf("回调缺少 out_trade_no")
	}
	if tx.TradeState != "" && tx.TradeState != "SUCCESS" {
		if err := a.updatePendingPaymentTradeState(tx.OutTradeNo, tx); err != nil {
			return err
		}
		return nil
	}
	if a.cfg.WeChatAppID != "" && tx.AppID != "" && tx.AppID != a.cfg.WeChatAppID {
		return fmt.Errorf("微信回调 appid 不匹配")
	}
	now := time.Now().UTC()
	successTime := now
	if tx.SuccessTime != "" {
		if parsed, err := time.Parse(time.RFC3339, tx.SuccessTime); err == nil {
			successTime = parsed
		}
	}

	return a.db.Transaction(func(db *gorm.DB) error {
		var payment models.WeChatPayment
		if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&payment, "out_trade_no = ?", tx.OutTradeNo).Error; err != nil {
			return fmt.Errorf("支付订单不存在: %w", err)
		}
		if a.cfg.WeChatMchID != "" && tx.MchID != "" && tx.MchID != a.cfg.WeChatMchID {
			return fmt.Errorf("微信回调 mchid 不匹配")
		}
		if tx.Amount.Total > 0 {
			expected := int(math.Round(payment.TotalAmount * 100))
			if tx.Amount.Total != expected {
				return fmt.Errorf("微信支付金额不匹配: got=%d want=%d", tx.Amount.Total, expected)
			}
		}
		if payment.Status == "paid" {
			return nil
		}
		payment.TransactionID = &tx.TransactionID
		payment.TradeState = tx.TradeState
		if payment.TradeState == "" {
			payment.TradeState = "SUCCESS"
		}
		payment.TradeStateDesc = &tx.TradeStateDesc
		payment.Status = "paid"
		payment.SuccessTime = &successTime
		payment.NotifyTime = &now
		payment.NotifyType = ptrString("TRANSACTION.SUCCESS")
		if err := db.Save(&payment).Error; err != nil {
			return err
		}
		if payment.Attach == nil || *payment.Attach == "" {
			return fmt.Errorf("支付订单缺少套餐 attach，无法入账")
		}
		packageID, err := uuid.Parse(*payment.Attach)
		if err != nil {
			return fmt.Errorf("支付订单 attach 不是有效套餐ID: %w", err)
		}
		var pkg models.TokenPackage
		if err := db.First(&pkg, "id = ?", packageID).Error; err != nil {
			return err
		}
		var existing int64
		if err := db.Model(&models.TokenRechargeRecord{}).Where("payment_id = ?", payment.ID).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return nil
		}
		var account models.UserTokenAccount
		if err := db.FirstOrCreate(&account, models.UserTokenAccount{UserID: payment.UserID}).Error; err != nil {
			return err
		}
		totalTokens := pkg.TokenAmount + pkg.BonusTokens
		account.TotalTokens += totalTokens
		account.TotalRecharged += payment.TotalAmount
		account.LastRechargeAt = &successTime
		if err := db.Save(&account).Error; err != nil {
			return err
		}
		record := models.TokenRechargeRecord{UserID: payment.UserID, TokenAmount: pkg.TokenAmount, BonusTokens: pkg.BonusTokens, Price: payment.TotalAmount, PaymentMethod: "wechat", PaymentID: &payment.ID, PackageID: &pkg.ID}
		if err := db.Create(&record).Error; err != nil {
			return err
		}
		return a.createCreditLedgerRecord(db, creditLedgerEntry{
			UserID:       payment.UserID,
			EventType:    creditLedgerEventRecharge,
			Item:         pkg.DisplayName,
			ChangeAmount: totalTokens,
			BalanceAfter: account.RemainingTokens(),
			Reason:       "微信充值",
			RequestMetadata: map[string]any{
				"out_trade_no":   payment.OutTradeNo,
				"payment_id":     payment.ID.String(),
				"package_id":     pkg.ID.String(),
				"package_name":   pkg.DisplayName,
				"payment_amount": payment.TotalAmount,
			},
			RecordedAt: successTime,
		})
	})
}

func (a *App) usageStats(c *gin.Context) {
	user := getUser(c)
	_, snapshot, _ := a.creditBalanceSnapshot(user.ID)
	percent := 0.0
	if snapshot.TotalCredits > 0 {
		percent = float64(snapshot.UsedCredits) / float64(snapshot.TotalCredits) * 100
	}
	ok(c, gin.H{
		"current_month_used":      snapshot.UsedCredits,
		"total_tokens":            snapshot.TotalCredits,
		"remaining_tokens":        snapshot.RemainingCredits,
		"total_credits":           snapshot.TotalCredits,
		"remaining_credits":       snapshot.RemainingCredits,
		"reserved_credits":        snapshot.ReservedCredits,
		"gross_remaining_credits": snapshot.GrossRemainingCredits,
		"used_credits":            snapshot.UsedCredits,
		"purchased_credits":       snapshot.PurchasedRemaining,
		"subscription_credits":    snapshot.SubscriptionRemaining,
		"usage_percentage":        percent,
		"daily_usage":             []gin.H{},
		"api_endpoint_usage":      gin.H{},
		"model_usage":             gin.H{},
		"trend_analysis":          gin.H{},
		"billing_month":           time.Now().Format("2006-01"),
		"last_updated":            time.Now().UTC(),
	})
}

func (a *App) usageRecords(c *gin.Context) {
	user := getUser(c)
	page, limit := pageLimit(c)
	var total int64
	q := a.db.Model(&models.TokenUsageRecord{}).Where("user_id = ?", user.ID)
	q.Count(&total)
	var items []models.TokenUsageRecord
	q.Order("recorded_at DESC").Offset((page - 1) * limit).Limit(limit).Find(&items)
	ok(c, gin.H{"items": items, "total": total, "page": page, "limit": limit})
}

func (a *App) rechargeRecords(c *gin.Context) {
	user := getUser(c)
	page, limit := pageLimit(c)
	var total int64
	q := a.db.Model(&models.TokenRechargeRecord{}).Where("user_id = ?", user.ID)
	q.Count(&total)
	var items []models.TokenRechargeRecord
	q.Order("created_at DESC").Offset((page - 1) * limit).Limit(limit).Find(&items)
	ok(c, gin.H{"items": items, "total": total, "page": page, "limit": limit})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func ptrString(v string) *string { return &v }
