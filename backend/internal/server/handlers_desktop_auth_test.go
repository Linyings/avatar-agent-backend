package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestDesktopAuthorizationExchangesOneTimePKCECode(t *testing.T) {
	app := newTestApp(t)
	app.cfg.FrontendBaseURL = "https://narratoai.cn"
	tokens := createDesktopAuthorizationSession(t, app, "desktop-auth@example.com")
	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		t.Fatalf("expected access and refresh tokens, got %#v", tokens)
	}
}

func TestDesktopSessionRevocationRejectsAccessAndRefreshTokens(t *testing.T) {
	app := newTestApp(t)
	app.cfg.FrontendBaseURL = "https://narratoai.cn"
	tokens := createDesktopAuthorizationSession(t, app, "desktop-revoke@example.com")

	revoke := request(app, http.MethodDelete, "/api/v1/auth/desktop/session", map[string]any{
		"refresh_token": tokens.RefreshToken,
	}, tokens.AccessToken)
	if revoke.Code != http.StatusOK {
		t.Fatalf("revoke desktop session status = %d, body = %s", revoke.Code, revoke.Body.String())
	}

	me := request(app, http.MethodGet, "/api/v1/auth/me", nil, tokens.AccessToken)
	if me.Code != http.StatusUnauthorized {
		t.Fatalf("revoked access token status = %d, body = %s", me.Code, me.Body.String())
	}

	refresh := request(app, http.MethodPost, "/api/v1/auth/desktop/refresh", map[string]any{
		"refresh_token": tokens.RefreshToken,
	}, "")
	if refresh.Code != http.StatusUnauthorized {
		t.Fatalf("revoked refresh token status = %d, body = %s", refresh.Code, refresh.Body.String())
	}
}

type desktopAuthorizationTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func createDesktopAuthorizationSession(t *testing.T, app *App, email string) desktopAuthorizationTokens {
	t.Helper()
	accessToken := registerAndLogin(t, app, email)
	verifier := strings.Repeat("a", 64)

	create := request(app, http.MethodPost, "/api/v1/auth/desktop/requests", map[string]any{
		"state":          strings.Repeat("s", 32),
		"code_challenge": desktopTestPKCEChallenge(verifier),
	}, "")
	if create.Code != http.StatusCreated {
		t.Fatalf("create desktop authorization request status = %d, body = %s", create.Code, create.Body.String())
	}

	var created struct {
		RequestID        string `json:"request_id"`
		AuthorizationURL string `json:"authorization_url"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.RequestID == "" || !strings.HasPrefix(created.AuthorizationURL, "https://narratoai.cn/desktop/authorize?") {
		t.Fatalf("unexpected create response: %#v", created)
	}

	authorize := request(app, http.MethodPost, "/api/v1/auth/desktop/requests/"+created.RequestID+"/authorize", map[string]any{
		"state": strings.Repeat("s", 32),
	}, accessToken)
	if authorize.Code != http.StatusOK {
		t.Fatalf("authorize desktop request status = %d, body = %s", authorize.Code, authorize.Body.String())
	}

	var authorized struct {
		CallbackURL string `json:"callback_url"`
	}
	if err := json.Unmarshal(authorize.Body.Bytes(), &authorized); err != nil {
		t.Fatalf("decode authorize response: %v", err)
	}
	callback, err := url.Parse(authorized.CallbackURL)
	if err != nil {
		t.Fatalf("parse callback URL: %v", err)
	}
	if callback.Scheme != "narratoai" || callback.Host != "auth" || callback.Path != "/callback" {
		t.Fatalf("unexpected callback URL: %q", authorized.CallbackURL)
	}

	exchangeInput := map[string]any{
		"request_id":    created.RequestID,
		"state":         strings.Repeat("s", 32),
		"code":          callback.Query().Get("code"),
		"code_verifier": verifier,
	}
	exchange := request(app, http.MethodPost, "/api/v1/auth/desktop/exchange", exchangeInput, "")
	if exchange.Code != http.StatusOK {
		t.Fatalf("exchange desktop authorization code status = %d, body = %s", exchange.Code, exchange.Body.String())
	}

	var tokens desktopAuthorizationTokens
	if err := json.Unmarshal(exchange.Body.Bytes(), &tokens); err != nil {
		t.Fatalf("decode exchange response: %v", err)
	}
	replay := request(app, http.MethodPost, "/api/v1/auth/desktop/exchange", exchangeInput, "")
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replay exchange status = %d, body = %s", replay.Code, replay.Body.String())
	}
	return tokens
}

func desktopTestPKCEChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
