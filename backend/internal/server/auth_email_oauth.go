package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
)

const (
	emailCodePurposeRegister = "register"
	emailCodePurposeVerify   = "verify_email"
	emailCodePurposeReset    = "reset_password"

	emailCodeTTL          = 10 * time.Minute
	emailCodeCooldown     = time.Minute
	emailCodeHourlyLimit  = 5
	emailCodeDailyLimit   = 20
	emailCodeMaxAttempts  = 5
	emailSendTimeout      = 15 * time.Second
	oauthStateTTL         = 10 * time.Minute
	oauthLoginTicketTTL   = 5 * time.Minute
	oauthProviderGoogle   = "google"
	oauthProviderGitHub   = "github"
	oauthCallbackFrontend = "/auth/oauth/callback"

	defaultGoogleTokenURL    = "https://oauth2.googleapis.com/token"
	defaultGoogleUserInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"
)

type smtpMailer struct {
	cfg config.Config
}

func newSMTPMailer(cfg config.Config) mailer {
	return smtpMailer{cfg: cfg}
}

func (m smtpMailer) SendVerificationCode(ctx context.Context, to, code, purpose, locale string) error {
	if !m.cfg.SMTPConfigured() {
		return errors.New("SMTP 未配置")
	}

	subject, body := emailCodeMessage(code, purpose, locale)
	from := mailAddress(m.cfg.SMTPFromName, m.cfg.SMTPFromEmail)
	message := strings.Join([]string{
		"From: " + from,
		"To: " + to,
		"Subject: " + mime.QEncoding.Encode("UTF-8", subject),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}, "\r\n")

	hostPort := fmt.Sprintf("%s:%d", m.cfg.SMTPHost, m.cfg.SMTPPort)
	auth := smtp.PlainAuth("", m.cfg.SMTPUsername, m.cfg.SMTPPassword, m.cfg.SMTPHost)
	if m.cfg.SMTPUsername == "" && m.cfg.SMTPPassword == "" {
		auth = nil
	}

	done := make(chan error, 1)
	go func() {
		if m.cfg.SMTPUseTLS && m.cfg.SMTPPort == 465 {
			done <- sendMailImplicitTLS(hostPort, m.cfg.SMTPHost, auth, m.cfg.SMTPFromEmail, []string{to}, []byte(message))
			return
		}
		if m.cfg.SMTPUseTLS {
			done <- sendMailStartTLS(hostPort, m.cfg.SMTPHost, auth, m.cfg.SMTPFromEmail, []string{to}, []byte(message), true)
			return
		}
		done <- sendMailStartTLS(hostPort, m.cfg.SMTPHost, auth, m.cfg.SMTPFromEmail, []string{to}, []byte(message), false)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

func emailCodeMessage(code, purpose, locale string) (string, string) {
	switch normalizeEmailLocale(locale) {
	case "en":
		action := "verify your email"
		switch purpose {
		case emailCodePurposeRegister:
			action = "complete registration"
		case emailCodePurposeReset:
			action = "reset your password"
		}
		return "NarratoAI email verification code", fmt.Sprintf("Your NarratoAI verification code is: %s\n\nThis code is valid for 10 minutes and is used to %s. If this was not you, please ignore this email.", code, action)
	case "fr":
		action := "vérifier votre adresse e-mail"
		switch purpose {
		case emailCodePurposeRegister:
			action = "finaliser votre inscription"
		case emailCodePurposeReset:
			action = "réinitialiser votre mot de passe"
		}
		return "Code de vérification e-mail NarratoAI", fmt.Sprintf("Votre code de vérification NarratoAI est : %s\n\nCe code est valable pendant 10 minutes et sert à %s. Si vous n’êtes pas à l’origine de cette demande, ignorez cet e-mail.", code, action)
	case "de":
		action := "Ihre E-Mail-Adresse zu verifizieren"
		switch purpose {
		case emailCodePurposeRegister:
			action = "die Registrierung abzuschließen"
		case emailCodePurposeReset:
			action = "Ihr Passwort zurückzusetzen"
		}
		return "NarratoAI E-Mail-Bestätigungscode", fmt.Sprintf("Ihr NarratoAI-Bestätigungscode lautet: %s\n\nDieser Code ist 10 Minuten gültig und dient dazu, %s. Wenn Sie dies nicht angefordert haben, ignorieren Sie diese E-Mail.", code, action)
	default:
		action := "完成邮箱验证"
		switch purpose {
		case emailCodePurposeRegister:
			action = "完成注册"
		case emailCodePurposeReset:
			action = "重置密码"
		}
		return "NarratoAI 邮箱验证码", fmt.Sprintf("您的 NarratoAI 验证码是：%s\n\n该验证码 10 分钟内有效，用于%s。如非本人操作，请忽略本邮件。", code, action)
	}
}

func sendMailImplicitTLS(addr, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: emailSendTimeout}, "tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(emailSendTimeout))
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	return sendSMTPMessage(client, auth, from, to, msg)
}

func sendMailStartTLS(addr, host string, auth smtp.Auth, from string, to []string, msg []byte, requireTLS bool) error {
	conn, err := net.DialTimeout("tcp", addr, emailSendTimeout)
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(emailSendTimeout))
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			_ = client.Close()
			return err
		}
	} else if requireTLS {
		_ = client.Close()
		return errors.New("SMTP 服务器不支持 STARTTLS")
	}
	return sendSMTPMessage(client, auth, from, to, msg)
}

func sendSMTPMessage(client *smtp.Client, auth smtp.Auth, from string, to []string, msg []byte) error {
	defer client.Close()
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	for _, recipient := range to {
		if err := client.Rcpt(recipient); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(msg); err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
}

func mailAddress(name, email string) string {
	if strings.TrimSpace(name) == "" {
		return email
	}
	return fmt.Sprintf("%s <%s>", mime.QEncoding.Encode("UTF-8", name), email)
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func secureHash(secret, value string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func randomURLToken(bytesLen int) (string, error) {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func randomNumericCode() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, value := range buf {
		b.WriteByte('0' + value%10)
	}
	return b.String(), nil
}

func validateEmailCodePurpose(purpose string) string {
	switch purpose {
	case emailCodePurposeVerify, emailCodePurposeReset:
		return purpose
	default:
		return emailCodePurposeRegister
	}
}

func normalizeEmailLocale(locale string) string {
	if normalized, ok := supportedEmailLocale(locale); ok {
		return normalized
	}
	return "en"
}

func supportedEmailLocale(locale string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(locale, "_", "-")))
	switch {
	case strings.HasPrefix(value, "zh"):
		return "zh-CN", true
	case strings.HasPrefix(value, "fr"):
		return "fr", true
	case strings.HasPrefix(value, "de"):
		return "de", true
	case strings.HasPrefix(value, "en"):
		return "en", true
	default:
		return "", false
	}
}

func requestEmailLocale(explicitLocale, acceptLanguage string) string {
	if strings.TrimSpace(explicitLocale) != "" {
		return normalizeEmailLocale(explicitLocale)
	}
	for _, part := range strings.Split(acceptLanguage, ",") {
		tag := strings.TrimSpace(strings.Split(part, ";")[0])
		if normalized, ok := supportedEmailLocale(tag); ok {
			return normalized
		}
	}
	return normalizeEmailLocale("")
}

func (a *App) emailQuota(c *gin.Context) gin.H {
	user := getUser(c)
	used, _ := a.countEmailCodes(user.Email, time.Now().UTC().Add(-time.Hour), emailCodePurposeVerify)
	return gin.H{"limit": emailCodeHourlyLimit, "used": used, "remaining": max(0, emailCodeHourlyLimit-int(used))}
}

func (a *App) emailCode(c *gin.Context) {
	var req struct {
		Email   string `json:"email" binding:"omitempty,email"`
		Purpose string `json:"purpose"`
		Locale  string `json:"locale"`
	}
	if c.Request.ContentLength != 0 && !bindJSON(c, &req) {
		return
	}

	purpose := validateEmailCodePurpose(req.Purpose)
	locale := requestEmailLocale(req.Locale, c.GetHeader("Accept-Language"))
	email := normalizeEmail(req.Email)
	if userValue, exists := c.Get("user"); exists {
		user := userValue.(models.User)
		email = normalizeEmail(user.Email)
		if purpose == emailCodePurposeRegister {
			purpose = emailCodePurposeVerify
		}
	}
	if email == "" {
		fail(c, http.StatusBadRequest, "邮箱不能为空")
		return
	}
	if purpose == emailCodePurposeRegister {
		var count int64
		if err := a.db.Model(&models.User{}).Where("email = ?", email).Count(&count).Error; err != nil {
			fail(c, http.StatusInternalServerError, "检查邮箱失败")
			return
		}
		if count > 0 {
			fail(c, http.StatusConflict, "该邮箱已被注册")
			return
		}
	}

	if err := a.issueEmailCode(c.Request.Context(), email, purpose, locale); err != nil {
		status := emailCodeErrorStatus(err)
		fail(c, status, err.Error())
		return
	}
	ok(c, gin.H{"message": "验证码已发送", "success": true})
}

func (a *App) forgotPassword(c *gin.Context) {
	var req struct {
		Email  string `json:"email" binding:"required,email"`
		Locale string `json:"locale"`
	}
	if !bindJSON(c, &req) {
		return
	}
	email := normalizeEmail(req.Email)
	var user models.User
	if err := a.db.Take(&user, "email = ?", email).Error; err != nil {
		ok(c, gin.H{"message": "如果邮箱存在，验证码将发送到该邮箱", "success": true})
		return
	}
	locale := requestEmailLocale(req.Locale, c.GetHeader("Accept-Language"))
	if err := a.issueEmailCode(c.Request.Context(), email, emailCodePurposeReset, locale); err != nil {
		status := emailCodeErrorStatus(err)
		fail(c, status, err.Error())
		return
	}
	ok(c, gin.H{"message": "验证码已发送", "success": true})
}

var (
	errMailerNotConfigured = errors.New("邮件服务未配置")
	errEmailSendTimeout    = errors.New("邮件发送超时，请检查 SMTP 配置或稍后重试")
	errEmailCodeRateLimit  = errors.New("验证码发送频率受限")
)

func emailCodeErrorStatus(err error) int {
	if errors.Is(err, errEmailSendTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	if errors.Is(err, errEmailCodeRateLimit) {
		return http.StatusTooManyRequests
	}
	if errors.Is(err, errMailerNotConfigured) || strings.Contains(err.Error(), "SMTP") || strings.Contains(err.Error(), "Authentication") {
		return http.StatusServiceUnavailable
	}
	return http.StatusServiceUnavailable
}

func (a *App) issueEmailCode(ctx context.Context, email, purpose, locale string) error {
	now := time.Now().UTC()
	if recent, err := a.countEmailCodes(email, now.Add(-emailCodeCooldown), purpose); err != nil {
		return errors.New("检查发送频率失败")
	} else if recent > 0 {
		return fmt.Errorf("%w：请稍后再试", errEmailCodeRateLimit)
	}
	if hourly, err := a.countEmailCodes(email, now.Add(-time.Hour), purpose); err != nil {
		return errors.New("检查发送配额失败")
	} else if hourly >= emailCodeHourlyLimit {
		return fmt.Errorf("%w：已达小时上限", errEmailCodeRateLimit)
	}
	if daily, err := a.countEmailCodes(email, now.Add(-24*time.Hour), purpose); err != nil {
		return errors.New("检查发送配额失败")
	} else if daily >= emailCodeDailyLimit {
		return fmt.Errorf("%w：已达每日上限", errEmailCodeRateLimit)
	}

	code, err := randomNumericCode()
	if err != nil {
		return errors.New("生成验证码失败")
	}
	if a.mailer == nil {
		return errMailerNotConfigured
	}
	record := models.AuthEmailCode{
		Email:     email,
		Purpose:   purpose,
		CodeHash:  secureHash(a.cfg.SecretKey, email+"|"+purpose+"|"+code),
		ExpiresAt: now.Add(emailCodeTTL),
	}
	if err := a.db.Create(&record).Error; err != nil {
		return errors.New("保存验证码失败")
	}

	sendCtx, cancel := context.WithTimeout(ctx, emailSendTimeout)
	defer cancel()
	if err := a.mailer.SendVerificationCode(sendCtx, email, code, purpose, locale); err != nil {
		_ = a.db.Delete(&record).Error
		if errors.Is(err, context.DeadlineExceeded) {
			return errEmailSendTimeout
		}
		return err
	}
	return nil
}

func (a *App) countEmailCodes(email string, since time.Time, purpose string) (int64, error) {
	var count int64
	err := a.db.Model(&models.AuthEmailCode{}).
		Where("email = ? AND purpose = ? AND created_at >= ?", normalizeEmail(email), purpose, since).
		Count(&count).Error
	return count, err
}

func (a *App) consumeEmailCode(email, purpose, code string) error {
	return a.consumeEmailCodeWithDB(a.db, email, purpose, code)
}

func (a *App) consumeEmailCodeWithDB(db *gorm.DB, email, purpose, code string) error {
	email = normalizeEmail(email)
	code = strings.TrimSpace(code)
	if code == "" {
		return errors.New("请输入邮箱验证码")
	}
	if !emailCodePattern.MatchString(code) {
		return errors.New("邮箱验证码格式不正确")
	}

	var record models.AuthEmailCode
	now := time.Now().UTC()
	err := db.Where("email = ? AND purpose = ? AND consumed_at IS NULL AND expires_at > ?", email, purpose, now).
		Order("created_at DESC").
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return errors.New("验证码不存在或已过期")
	}
	if err != nil {
		return errors.New("验证码校验失败")
	}
	if record.Attempts >= emailCodeMaxAttempts {
		return errors.New("验证码尝试次数已耗尽")
	}
	if !hmac.Equal([]byte(record.CodeHash), []byte(secureHash(a.cfg.SecretKey, email+"|"+purpose+"|"+code))) {
		_ = db.Model(&record).Update("attempts", record.Attempts+1).Error
		return errors.New("验证码不正确")
	}
	result := db.Model(&models.AuthEmailCode{}).
		Where("id = ? AND consumed_at IS NULL", record.ID).
		Updates(map[string]any{"consumed_at": now, "attempts": record.Attempts + 1})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("验证码已被使用")
	}
	return nil
}

type oauthProviderConfig struct {
	Name         string
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	UserInfoURL  string
	EmailsURL    string
	Scopes       []string
}

func (a *App) oauthProvider(name string) (oauthProviderConfig, bool) {
	switch strings.ToLower(name) {
	case oauthProviderGoogle:
		return oauthProviderConfig{
			Name:         oauthProviderGoogle,
			ClientID:     a.cfg.GoogleClientID,
			ClientSecret: a.cfg.GoogleClientSecret,
			AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL:     firstNonEmpty(a.cfg.GoogleTokenURL, defaultGoogleTokenURL),
			UserInfoURL:  firstNonEmpty(a.cfg.GoogleUserInfoURL, defaultGoogleUserInfoURL),
			Scopes:       []string{"openid", "email", "profile"},
		}, a.cfg.GoogleOAuthConfigured()
	case oauthProviderGitHub:
		return oauthProviderConfig{
			Name:         oauthProviderGitHub,
			ClientID:     a.cfg.GitHubClientID,
			ClientSecret: a.cfg.GitHubClientSecret,
			AuthURL:      "https://github.com/login/oauth/authorize",
			TokenURL:     "https://github.com/login/oauth/access_token",
			UserInfoURL:  "https://api.github.com/user",
			EmailsURL:    "https://api.github.com/user/emails",
			Scopes:       []string{"read:user", "user:email"},
		}, a.cfg.GitHubOAuthConfigured()
	default:
		return oauthProviderConfig{}, false
	}
}

func (a *App) oauthStart(c *gin.Context) {
	provider, configured := a.oauthProvider(c.Param("provider"))
	if !configured {
		fail(c, http.StatusNotFound, "OAuth 提供方未配置")
		return
	}
	state, err := randomURLToken(32)
	if err != nil {
		fail(c, http.StatusInternalServerError, "生成 OAuth state 失败")
		return
	}
	verifier, err := randomURLToken(32)
	if err != nil {
		fail(c, http.StatusInternalServerError, "生成 OAuth verifier 失败")
		return
	}
	challenge := sha256.Sum256([]byte(verifier))
	redirectPath := sanitizeRedirectPath(c.Query("redirect"))
	frontendBaseURL := a.resolveOAuthFrontendBaseURL(c)
	if err := a.db.Create(&models.OAuthState{
		Provider:        provider.Name,
		StateHash:       secureHash(a.cfg.SecretKey, provider.Name+"|"+state),
		CodeVerifier:    verifier,
		RedirectPath:    redirectPath,
		FrontendBaseURL: frontendBaseURL,
		ExpiresAt:       time.Now().UTC().Add(oauthStateTTL),
	}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "保存 OAuth state 失败")
		return
	}

	query := url.Values{}
	query.Set("client_id", provider.ClientID)
	query.Set("redirect_uri", a.oauthRedirectURI(c, provider.Name))
	query.Set("response_type", "code")
	query.Set("scope", strings.Join(provider.Scopes, " "))
	query.Set("state", state)
	query.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	query.Set("code_challenge_method", "S256")
	c.Redirect(http.StatusFound, provider.AuthURL+"?"+query.Encode())
}

func (a *App) oauthCallback(c *gin.Context) {
	provider, configured := a.oauthProvider(c.Param("provider"))
	if !configured {
		a.redirectOAuthError(c, "provider_not_configured")
		return
	}
	if errMessage := c.Query("error"); errMessage != "" {
		a.redirectOAuthError(c, errMessage)
		return
	}
	code := strings.TrimSpace(c.Query("code"))
	state := strings.TrimSpace(c.Query("state"))
	if code == "" || state == "" {
		a.redirectOAuthError(c, "missing_code_or_state")
		return
	}

	var stateRecord models.OAuthState
	now := time.Now().UTC()
	err := a.db.Where("provider = ? AND state_hash = ? AND consumed_at IS NULL AND expires_at > ?", provider.Name, secureHash(a.cfg.SecretKey, provider.Name+"|"+state), now).
		First(&stateRecord).Error
	if err != nil {
		a.redirectOAuthError(c, "invalid_state")
		return
	}
	result := a.db.Model(&models.OAuthState{}).
		Where("id = ? AND consumed_at IS NULL", stateRecord.ID).
		Update("consumed_at", now)
	if result.Error != nil || result.RowsAffected == 0 {
		a.redirectOAuthError(c, "state_update_failed")
		return
	}

	token, err := a.exchangeOAuthCode(c.Request.Context(), provider, code, stateRecord.CodeVerifier, a.oauthRedirectURI(c, provider.Name))
	if err != nil {
		a.redirectOAuthError(c, "token_exchange_failed")
		return
	}
	profile, err := a.fetchOAuthProfile(c.Request.Context(), provider, token)
	if err != nil {
		a.redirectOAuthError(c, "profile_fetch_failed")
		return
	}
	user, err := a.resolveOAuthUser(provider.Name, profile)
	if err != nil {
		a.redirectOAuthError(c, "account_link_failed")
		return
	}
	ticket, err := randomURLToken(32)
	if err != nil {
		a.redirectOAuthError(c, "ticket_create_failed")
		return
	}
	if err := a.db.Create(&models.OAuthLoginTicket{
		TicketHash:   secureHash(a.cfg.SecretKey, ticket),
		UserID:       user.ID,
		Provider:     provider.Name,
		RedirectPath: stateRecord.RedirectPath,
		ExpiresAt:    now.Add(oauthLoginTicketTTL),
	}).Error; err != nil {
		a.redirectOAuthError(c, "ticket_save_failed")
		return
	}

	frontendBaseURL := stateRecord.FrontendBaseURL
	if !a.isAllowedFrontendBaseURL(frontendBaseURL) {
		frontendBaseURL = a.cfg.FrontendBaseURL
	}
	redirectURL := a.frontendURLForBase(frontendBaseURL, oauthCallbackFrontend)
	values := redirectURL.Query()
	values.Set("ticket", ticket)
	values.Set("provider", provider.Name)
	redirectURL.RawQuery = values.Encode()
	c.Redirect(http.StatusFound, redirectURL.String())
}

func (a *App) oauthExchange(c *gin.Context) {
	var req struct {
		Ticket string `json:"ticket" binding:"required"`
	}
	if !bindJSON(c, &req) {
		return
	}
	now := time.Now().UTC()
	var ticket models.OAuthLoginTicket
	err := a.db.Where("ticket_hash = ? AND consumed_at IS NULL AND expires_at > ?", secureHash(a.cfg.SecretKey, strings.TrimSpace(req.Ticket)), now).
		First(&ticket).Error
	if err != nil {
		fail(c, http.StatusUnauthorized, "OAuth 登录票据无效或已过期")
		return
	}
	user, err := a.activeUser(ticket.UserID)
	if err != nil {
		fail(c, http.StatusUnauthorized, "用户不存在或已禁用")
		return
	}
	result := a.db.Model(&models.OAuthLoginTicket{}).
		Where("id = ? AND consumed_at IS NULL", ticket.ID).
		Update("consumed_at", now)
	if result.Error != nil {
		fail(c, http.StatusInternalServerError, "OAuth 登录票据消费失败")
		return
	}
	if result.RowsAffected == 0 {
		fail(c, http.StatusUnauthorized, "OAuth 登录票据已被使用")
		return
	}
	tokens, err := a.makeTokens(user)
	if err != nil {
		fail(c, http.StatusInternalServerError, "生成令牌失败")
		return
	}
	user.LastLoginAt = &now
	_ = a.db.Model(&models.User{}).Where("id = ?", user.ID).Update("last_login_at", now).Error
	ok(c, gin.H{"message": "登录成功", "success": true, "user_id": user.ID.String(), "username": user.Username, "email": user.Email, "tokens": tokens, "redirect_path": ticket.RedirectPath})
}

func (a *App) exchangeOAuthCode(ctx context.Context, provider oauthProviderConfig, code, verifier, redirectURI string) (string, error) {
	form := url.Values{}
	form.Set("client_id", provider.ClientID)
	form.Set("client_secret", provider.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", verifier)
	if provider.Name == oauthProviderGoogle {
		form.Set("grant_type", "authorization_code")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := a.oauthHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("oauth token status %d: %s", resp.StatusCode, string(data))
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.AccessToken == "" {
		return "", errors.New("oauth access_token missing")
	}
	return body.AccessToken, nil
}

type oauthUserProfile struct {
	ID            string
	Email         string
	EmailVerified bool
	Name          string
	AvatarURL     string
}

func (a *App) fetchOAuthProfile(ctx context.Context, provider oauthProviderConfig, token string) (oauthUserProfile, error) {
	switch provider.Name {
	case oauthProviderGoogle:
		var body struct {
			Sub           string `json:"sub"`
			Email         string `json:"email"`
			EmailVerified bool   `json:"email_verified"`
			Name          string `json:"name"`
			Picture       string `json:"picture"`
		}
		if err := a.getOAuthJSON(ctx, provider.UserInfoURL, token, &body); err != nil {
			return oauthUserProfile{}, err
		}
		return oauthUserProfile{ID: body.Sub, Email: normalizeEmail(body.Email), EmailVerified: body.EmailVerified, Name: body.Name, AvatarURL: body.Picture}, nil
	case oauthProviderGitHub:
		var userBody struct {
			ID        int64  `json:"id"`
			Login     string `json:"login"`
			Name      string `json:"name"`
			Email     string `json:"email"`
			AvatarURL string `json:"avatar_url"`
		}
		if err := a.getOAuthJSON(ctx, provider.UserInfoURL, token, &userBody); err != nil {
			return oauthUserProfile{}, err
		}
		var emails []struct {
			Email    string `json:"email"`
			Verified bool   `json:"verified"`
			Primary  bool   `json:"primary"`
		}
		if err := a.getOAuthJSON(ctx, provider.EmailsURL, token, &emails); err != nil {
			return oauthUserProfile{}, err
		}
		email := normalizeEmail(userBody.Email)
		verified := false
		for _, item := range emails {
			if item.Primary {
				email = normalizeEmail(item.Email)
				verified = item.Verified
				break
			}
		}
		if email == "" {
			for _, item := range emails {
				if item.Verified {
					email = normalizeEmail(item.Email)
					verified = true
					break
				}
			}
		}
		name := userBody.Name
		if name == "" {
			name = userBody.Login
		}
		return oauthUserProfile{ID: fmt.Sprint(userBody.ID), Email: email, EmailVerified: verified, Name: name, AvatarURL: userBody.AvatarURL}, nil
	default:
		return oauthUserProfile{}, errors.New("unsupported provider")
	}
}

func (a *App) getOAuthJSON(ctx context.Context, endpoint, token string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := a.oauthHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("oauth user status %d: %s", resp.StatusCode, string(data))
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

func (a *App) resolveOAuthUser(provider string, profile oauthUserProfile) (models.User, error) {
	if profile.ID == "" || profile.Email == "" || !profile.EmailVerified {
		return models.User{}, errors.New("provider email is not verified")
	}
	now := time.Now().UTC()
	var account models.OAuthAccount
	if err := a.db.First(&account, "provider = ? AND provider_user_id = ?", provider, profile.ID).Error; err == nil {
		var user models.User
		if err := a.db.First(&user, "id = ?", account.UserID).Error; err != nil {
			return models.User{}, err
		}
		_ = a.db.Model(&account).Updates(map[string]any{"last_login_at": now, "provider_email": profile.Email, "provider_email_verified": true}).Error
		return user, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return models.User{}, err
	}

	var user models.User
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&user, "email = ?", profile.Email).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			username := a.uniqueUsername(tx, profile.Email)
			displayName := strings.TrimSpace(profile.Name)
			user = models.User{
				Username:            username,
				Email:               profile.Email,
				DisplayName:         optionalString(displayName),
				AvatarURL:           optionalString(profile.AvatarURL),
				IsActive:            true,
				IsVerified:          true,
				PasswordAuthEnabled: true,
				EmailVerifiedAt:     &now,
			}
			if err := tx.Create(&user).Error; err != nil {
				return err
			}
		} else {
			if !user.IsVerified || user.EmailVerifiedAt == nil {
				user.IsVerified = true
				user.EmailVerifiedAt = &now
				if err := tx.Model(&user).Updates(map[string]any{"is_verified": true, "email_verified_at": now}).Error; err != nil {
					return err
				}
			}
		}
		account = models.OAuthAccount{
			UserID:                user.ID,
			Provider:              provider,
			ProviderUserID:        profile.ID,
			ProviderEmail:         profile.Email,
			ProviderEmailVerified: true,
			LinkedAt:              now,
			LastLoginAt:           &now,
		}
		if err := tx.Create(&account).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return models.User{}, err
	}
	_, _ = a.ensureProfile(user.ID)
	_, _ = a.ensureTokenAccount(user.ID)
	a.cacheActiveUser(user)
	return user, nil
}

var (
	emailCodePattern  = regexp.MustCompile(`^\d{6}$`)
	usernameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
)

func (a *App) uniqueUsername(tx *gorm.DB, email string) string {
	base := strings.Split(email, "@")[0]
	base = usernameSanitizer.ReplaceAllString(base, "_")
	base = strings.Trim(base, "_-")
	if len(base) < 3 {
		base = "user"
	}
	if len(base) > 36 {
		base = base[:36]
	}
	username := base
	for i := 0; i < 10; i++ {
		var count int64
		_ = tx.Model(&models.User{}).Where("username = ?", username).Count(&count).Error
		if count == 0 {
			return username
		}
		token, _ := randomURLToken(4)
		username = base + "_" + strings.ToLower(strings.ReplaceAll(token, "-", ""))
		if len(username) > 50 {
			username = username[:50]
		}
	}
	return "user_" + uuid.NewString()[:8]
}

func optionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func ptrTime(value time.Time) *time.Time {
	return &value
}

func sanitizeRedirectPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return "/"
	}
	return value
}

func (a *App) oauthRedirectURI(c *gin.Context, provider string) string {
	base := strings.TrimRight(a.cfg.PublicAPIBaseURL, "/")
	if base == "" {
		scheme := c.GetHeader("X-Forwarded-Proto")
		if scheme == "" {
			scheme = "http"
		}
		base = scheme + "://" + c.Request.Host
	}
	return base + a.cfg.APIv1Prefix + "/auth/oauth/" + provider + "/callback"
}

func (a *App) frontendURL(path string) *url.URL {
	return a.frontendURLForBase(a.cfg.FrontendBaseURL, path)
}

func (a *App) frontendURLForBase(baseURL, path string) *url.URL {
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		base, _ = url.Parse("http://localhost:5173")
	}
	base.Path = path
	base.RawQuery = ""
	return base
}

func (a *App) resolveOAuthFrontendBaseURL(c *gin.Context) string {
	candidates := []string{
		c.Query("frontend_origin"),
		c.Query("frontend_base_url"),
		c.GetHeader("Origin"),
		originFromURL(c.GetHeader("Referer")),
		a.cfg.FrontendBaseURL,
	}
	for _, candidate := range candidates {
		if a.isAllowedFrontendBaseURL(candidate) {
			normalized, _ := normalizeFrontendBaseURL(candidate)
			return normalized
		}
	}
	return a.cfg.FrontendBaseURL
}

func (a *App) isAllowedFrontendBaseURL(value string) bool {
	normalized, ok := normalizeFrontendBaseURL(value)
	if !ok {
		return false
	}
	allowed := append([]string{a.cfg.FrontendBaseURL}, a.cfg.FrontendBaseURLs...)
	for _, candidate := range allowed {
		allowedBaseURL, ok := normalizeFrontendBaseURL(candidate)
		if ok && normalized == allowedBaseURL {
			return true
		}
	}
	return false
}

func originFromURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func normalizeFrontendBaseURL(value string) (string, bool) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(value), "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	return parsed.Scheme + "://" + parsed.Host, true
}

func (a *App) redirectOAuthError(c *gin.Context, code string) {
	redirectURL := a.frontendURL(oauthCallbackFrontend)
	values := redirectURL.Query()
	values.Set("error", code)
	redirectURL.RawQuery = values.Encode()
	c.Redirect(http.StatusFound, redirectURL.String())
}

func (a *App) oauthTokenResponse(user models.User) (gin.H, error) {
	tokens, err := a.makeTokens(user)
	if err != nil {
		return nil, err
	}
	return gin.H{"message": "登录成功", "success": true, "user_id": user.ID.String(), "username": user.Username, "email": user.Email, "tokens": tokens}, nil
}
