package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	errUnsubscribeTokenRequired = errors.New("unsubscribe token is required")
	errUnsubscribeTokenInvalid  = errors.New("unsubscribe token is invalid")
)

type unsubscribeEmailRequest struct {
	Token string `json:"token"`
}

// unsubscribeEmailCampaignHTML is intentionally public: a recipient must be
// able to opt out without an existing authenticated product session.
func (a *App) unsubscribeEmailCampaignHTML(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if err := a.unsubscribeEmailCampaign(c.Request.Context(), c.Query("token")); err != nil {
		status, title, message := unsubscribeHTMLError(err)
		c.Data(status, "text/html; charset=utf-8", []byte(unsubscribeConfirmationHTML(title, message)))
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(unsubscribeConfirmationHTML("已取消订阅", "你已取消接收 NarratoAI 的产品更新邮件。后续不会再向该邮箱发送此类推广邮件。")))
}

func (a *App) unsubscribeEmailCampaignJSON(c *gin.Context) {
	var request unsubscribeEmailRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		fail(c, http.StatusBadRequest, "请求格式不正确")
		return
	}
	if err := a.unsubscribeEmailCampaign(c.Request.Context(), request.Token); err != nil {
		status, message := unsubscribeJSONError(err)
		fail(c, status, message)
		return
	}
	ok(c, gin.H{"status": "unsubscribed"})
}

func (a *App) unsubscribeEmailCampaign(ctx context.Context, rawToken string) error {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return errUnsubscribeTokenRequired
	}
	tokenHash := unsubscribeTokenHash(rawToken)
	now := time.Now().UTC()
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var recipient models.EmailCampaignRecipient
		if err := tx.Where("unsubscribe_token_hash = ?", tokenHash).First(&recipient).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errUnsubscribeTokenInvalid
			}
			return err
		}
		if err := tx.Model(&recipient).Updates(map[string]any{
			"status":          models.EmailCampaignRecipientStatusUnsubscribed,
			"unsubscribed_at": &now,
		}).Error; err != nil {
			return err
		}

		normalizedEmail := strings.ToLower(strings.TrimSpace(recipient.NormalizedEmail))
		if normalizedEmail == "" {
			normalizedEmail = strings.ToLower(strings.TrimSpace(recipient.Email))
		}
		if normalizedEmail == "" {
			return errors.New("campaign recipient has no email address")
		}
		suppression := models.EmailSuppression{
			UserID:           &recipient.UserID,
			Email:            recipient.Email,
			NormalizedEmail:  normalizedEmail,
			Status:           models.EmailSuppressionStatusActive,
			Reason:           models.EmailSuppressionReasonUnsubscribed,
			SourceCampaignID: &recipient.CampaignID,
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "normalized_email"}},
			DoUpdates: clause.Assignments(map[string]any{
				"user_id":            recipient.UserID,
				"email":              recipient.Email,
				"status":             models.EmailSuppressionStatusActive,
				"reason":             models.EmailSuppressionReasonUnsubscribed,
				"source_campaign_id": recipient.CampaignID,
				"updated_at":         now,
			}),
		}).Create(&suppression).Error
	})
}

func unsubscribeTokenHash(rawToken string) string {
	digest := sha256.Sum256([]byte(rawToken))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func unsubscribeJSONError(err error) (int, string) {
	switch {
	case errors.Is(err, errUnsubscribeTokenRequired):
		return http.StatusBadRequest, "缺少退订令牌"
	case errors.Is(err, errUnsubscribeTokenInvalid):
		return http.StatusNotFound, "退订链接无效或已过期"
	default:
		return http.StatusInternalServerError, "暂时无法处理退订请求"
	}
}

func unsubscribeHTMLError(err error) (int, string, string) {
	switch {
	case errors.Is(err, errUnsubscribeTokenRequired):
		return http.StatusBadRequest, "缺少退订信息", "请使用邮件中的完整退订链接。"
	case errors.Is(err, errUnsubscribeTokenInvalid):
		return http.StatusNotFound, "退订链接无效或已过期", "请检查链接是否完整，或联系 NarratoAI 支持团队。"
	default:
		return http.StatusInternalServerError, "暂时无法处理退订请求", "请稍后重试，或联系 NarratoAI 支持团队。"
	}
}

func unsubscribeConfirmationHTML(title, message string) string {
	return "<!doctype html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"><title>" + title + " - NarratoAI</title><style>body{margin:0;background:#f6f8fb;color:#172033;font-family:-apple-system,BlinkMacSystemFont,\"Segoe UI\",\"Microsoft YaHei\",sans-serif}.card{box-sizing:border-box;max-width:560px;margin:12vh auto;padding:42px 36px;background:#fff;border:1px solid #e7eaf0;border-radius:12px;box-shadow:0 12px 38px rgba(31,46,72,.09)}h1{margin:0 0 16px;font-size:24px}p{margin:0;color:#536078;line-height:1.75}small{display:block;margin-top:28px;color:#8791a6}</style></head><body><main class=\"card\"><h1>" + title + "</h1><p>" + message + "</p><small>NarratoAI</small></main></body></html>"
}
