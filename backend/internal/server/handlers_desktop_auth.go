package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
)

const desktopAuthorizationTTL = time.Minute

type createDesktopAuthorizationRequest struct {
	State         string `json:"state" binding:"required"`
	CodeChallenge string `json:"code_challenge" binding:"required"`
}

type authorizeDesktopAuthorizationRequest struct {
	State string `json:"state" binding:"required"`
}

type exchangeDesktopAuthorizationRequest struct {
	RequestID    string `json:"request_id" binding:"required"`
	State        string `json:"state" binding:"required"`
	Code         string `json:"code" binding:"required"`
	CodeVerifier string `json:"code_verifier" binding:"required"`
}

func desktopAuthorizationStateHash(secret, state string) string {
	return secureHash(secret, "desktop-authorization-state|"+state)
}

func desktopAuthorizationCodeHash(secret, code string) string {
	return secureHash(secret, "desktop-authorization-code|"+code)
}

func validDesktopAuthorizationState(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) >= 32 && len(value) <= 128
}

func validDesktopCodeChallenge(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil
}

func desktopPKCEChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (a *App) createDesktopAuthorizationRequest(c *gin.Context) {
	var req createDesktopAuthorizationRequest
	if !bindJSON(c, &req) {
		return
	}
	req.State = strings.TrimSpace(req.State)
	req.CodeChallenge = strings.TrimSpace(req.CodeChallenge)
	if !validDesktopAuthorizationState(req.State) || !validDesktopCodeChallenge(req.CodeChallenge) {
		fail(c, http.StatusBadRequest, "invalid desktop authorization request")
		return
	}
	if strings.TrimSpace(a.cfg.FrontendBaseURL) == "" {
		fail(c, http.StatusInternalServerError, "desktop authorization frontend URL is not configured")
		return
	}

	now := time.Now().UTC()
	record := models.DesktopAuthorizationRequest{
		StateHash:     desktopAuthorizationStateHash(a.cfg.SecretKey, req.State),
		CodeChallenge: req.CodeChallenge,
		ExpiresAt:     now.Add(desktopAuthorizationTTL),
	}
	if err := a.db.Create(&record).Error; err != nil {
		fail(c, http.StatusInternalServerError, "failed to create desktop authorization request")
		return
	}

	frontendURL, err := url.Parse(a.cfg.FrontendBaseURL)
	if err != nil || frontendURL.Scheme == "" || frontendURL.Host == "" {
		fail(c, http.StatusInternalServerError, "desktop authorization frontend URL is invalid")
		return
	}
	frontendURL.Path = strings.TrimRight(frontendURL.Path, "/") + "/desktop/authorize"
	query := frontendURL.Query()
	query.Set("request_id", record.ID.String())
	query.Set("state", req.State)
	frontendURL.RawQuery = query.Encode()

	created(c, gin.H{
		"request_id":        record.ID.String(),
		"authorization_url": frontendURL.String(),
		"expires_at":        record.ExpiresAt,
	})
}

func (a *App) authorizeDesktopAuthorizationRequest(c *gin.Context) {
	requestID, validID := parseIDParam(c, "request_id")
	if !validID {
		return
	}
	var req authorizeDesktopAuthorizationRequest
	if !bindJSON(c, &req) {
		return
	}
	req.State = strings.TrimSpace(req.State)
	if !validDesktopAuthorizationState(req.State) {
		fail(c, http.StatusBadRequest, "invalid desktop authorization state")
		return
	}

	now := time.Now().UTC()
	code, err := randomURLToken(32)
	if err != nil {
		fail(c, http.StatusInternalServerError, "failed to create desktop authorization code")
		return
	}
	user := getUser(c)
	result := a.db.Model(&models.DesktopAuthorizationRequest{}).
		Where("id = ? AND state_hash = ? AND expires_at > ? AND authorized_at IS NULL AND redeemed_at IS NULL", requestID, desktopAuthorizationStateHash(a.cfg.SecretKey, req.State), now).
		Updates(map[string]any{
			"user_id":                 user.ID,
			"authorized_at":           now,
			"authorization_code_hash": desktopAuthorizationCodeHash(a.cfg.SecretKey, code),
		})
	if result.Error != nil {
		fail(c, http.StatusInternalServerError, "failed to authorize desktop request")
		return
	}
	if result.RowsAffected != 1 {
		fail(c, http.StatusUnauthorized, "desktop authorization request is invalid or expired")
		return
	}

	callback := url.URL{Scheme: "narratoai", Host: "auth", Path: "/callback"}
	query := callback.Query()
	query.Set("request_id", requestID.String())
	query.Set("code", code)
	query.Set("state", req.State)
	callback.RawQuery = query.Encode()
	ok(c, gin.H{"callback_url": callback.String()})
}

func (a *App) exchangeDesktopAuthorization(c *gin.Context) {
	var req exchangeDesktopAuthorizationRequest
	if !bindJSON(c, &req) {
		return
	}
	req.State = strings.TrimSpace(req.State)
	req.Code = strings.TrimSpace(req.Code)
	req.CodeVerifier = strings.TrimSpace(req.CodeVerifier)
	requestID, err := uuid.Parse(strings.TrimSpace(req.RequestID))
	if err != nil || !validDesktopAuthorizationState(req.State) || req.Code == "" || req.CodeVerifier == "" {
		fail(c, http.StatusUnauthorized, "desktop authorization code is invalid or expired")
		return
	}

	now := time.Now().UTC()
	var record models.DesktopAuthorizationRequest
	err = a.db.Where("id = ? AND state_hash = ? AND authorization_code_hash = ? AND redeemed_at IS NULL AND expires_at > ?", requestID, desktopAuthorizationStateHash(a.cfg.SecretKey, req.State), desktopAuthorizationCodeHash(a.cfg.SecretKey, req.Code), now).
		First(&record).Error
	if err != nil || record.UserID == nil || !hmac.Equal([]byte(record.CodeChallenge), []byte(desktopPKCEChallenge(req.CodeVerifier))) {
		fail(c, http.StatusUnauthorized, "desktop authorization code is invalid or expired")
		return
	}

	result := a.db.Model(&models.DesktopAuthorizationRequest{}).
		Where("id = ? AND redeemed_at IS NULL", record.ID).
		Update("redeemed_at", now)
	if result.Error != nil {
		fail(c, http.StatusInternalServerError, "failed to consume desktop authorization code")
		return
	}
	if result.RowsAffected != 1 {
		fail(c, http.StatusUnauthorized, "desktop authorization code has already been used")
		return
	}

	user, err := a.activeUser(*record.UserID)
	if err != nil {
		fail(c, http.StatusUnauthorized, "desktop authorization user is unavailable")
		return
	}
	tokens, err := a.makeTokens(user)
	if err != nil {
		fail(c, http.StatusInternalServerError, "failed to issue desktop authorization tokens")
		return
	}
	ok(c, tokens)
}

func (a *App) refreshDesktopAuthorization(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refresh_token" binding:"required"`
	}
	if !bindJSON(c, &req) {
		return
	}
	tokens, err := a.refreshTokens(req.RefreshToken)
	if err != nil {
		fail(c, http.StatusUnauthorized, "invalid desktop refresh token")
		return
	}
	ok(c, tokens)
}

func (a *App) revokeDesktopAuthorizationSession(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refresh_token" binding:"required"`
	}
	if !bindJSON(c, &req) {
		return
	}
	header := c.GetHeader("Authorization")
	if len(header) < len("Bearer ") {
		fail(c, http.StatusUnauthorized, "invalid desktop session")
		return
	}
	accessClaims, accessUser, accessValid := a.parseToken(strings.TrimSpace(header[len("Bearer "):]), "access")
	refreshClaims, refreshUser, refreshValid := a.parseRefreshToken(strings.TrimSpace(req.RefreshToken))
	if !accessValid || !refreshValid || accessUser.ID != refreshUser.ID {
		fail(c, http.StatusUnauthorized, "invalid desktop session")
		return
	}
	if err := a.db.Transaction(func(tx *gorm.DB) error {
		for _, claims := range []map[string]any{accessClaims, refreshClaims} {
			jti := strings.TrimSpace(fmt.Sprint(claims["jti"]))
			expiresAt, valid := jwtExpiry(claims)
			if !valid {
				return errors.New("invalid desktop session expiration")
			}
			record := models.RevokedAuthToken{JTI: jti, ExpiresAt: expiresAt}
			if err := tx.Where("jti = ?", jti).FirstOrCreate(&record).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		fail(c, http.StatusInternalServerError, "failed to revoke desktop session")
		return
	}
	ok(c, gin.H{"success": true})
}

func jwtExpiry(claims map[string]any) (time.Time, bool) {
	value, ok := claims["exp"]
	if !ok {
		return time.Time{}, false
	}
	switch typed := value.(type) {
	case float64:
		return time.Unix(int64(typed), 0).UTC(), typed > 0
	case json.Number:
		seconds, err := typed.Int64()
		return time.Unix(seconds, 0).UTC(), err == nil && seconds > 0
	default:
		return time.Time{}, false
	}
}
