package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestCreemNewCheckoutsAreDisabledByChannelPolicy(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "creem-disabled@example.com")

	for _, endpoint := range []string{
		"/api/v1/subscriptions/checkout",
		"/api/v1/credit-accounts/recharge/creem",
	} {
		response := request(app, http.MethodPost, endpoint, map[string]any{}, token)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "payment_channel_disabled") {
			t.Fatalf("%s status=%d body=%s", endpoint, response.Code, response.Body.String())
		}
	}
}
