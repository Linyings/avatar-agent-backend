package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
)

func TestAvatarProviderSwitchNeverRetriesOldOrderOnNewVendor(t *testing.T) {
	a := newTestApp(t)
	owner := uuid.New()
	state := seedAvatarTask(t, a, owner, "failed")
	state.Provider = "runninghub"
	state.Model = "seedance-2.5-token"
	state.VideoConfigID = a.avatarMVP.options.VideoConfigID()
	if err := a.avatarMVP.save(state); err != nil {
		t.Fatal(err)
	}
	original := a.avatarMVP.options
	a.avatarMVP.options = avatar.Options{Provider: "minimax"}.VideoDefaults()
	if _, err := a.avatarMVP.claimRetry(context.Background(), owner, state.ID); err == nil || !strings.Contains(err.Error(), "配置已切换") {
		t.Fatalf("cross-provider retry allowed: %v", err)
	}
	if a.avatarMVP.sameVideoConfig(state) {
		t.Fatal("new provider must get a separate baseline")
	}
	a.avatarMVP.options = original
	a.avatarMVP.options.APIKey = "rotated"
	if !a.avatarMVP.sameVideoConfig(state) {
		t.Fatal("key rotation must preserve task identity")
	}
	a.avatarMVP.options.GenerationPath = "/different"
	if a.avatarMVP.sameVideoConfig(state) {
		t.Fatal("endpoint change must not reuse old baseline")
	}
}

func TestAvatarConfigReportsSelectedModelWithoutSecrets(t *testing.T) {
	a := newTestApp(t)
	a.avatarMVP.options = avatar.Options{Provider: "minimax", APIKey: "private-key", Enabled: true}.VideoDefaults()
	a.cfg.DashScopeAPIKey = "tts-key"
	a.cfg.QwenTTSModel = "tts-model"
	r := gin.New()
	r.GET("/config", a.avatarConfig)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/config", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"model":"MiniMax-H3"`) || !strings.Contains(w.Body.String(), `"provider":"minimax"`) || strings.Contains(w.Body.String(), "private-key") {
		t.Fatalf("unexpected config: %s", w.Body.String())
	}
}
