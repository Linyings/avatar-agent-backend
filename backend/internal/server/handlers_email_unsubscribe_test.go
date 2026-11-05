package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
)

func TestEmailCampaignUnsubscribeGETAndJSONAreIdempotent(t *testing.T) {
	app := newTestApp(t)
	token := "test-unsubscribe-token-2b7e6a3f-99ef-4d3d-bb50"
	recipient := models.EmailCampaignRecipient{
		CampaignID:           uuid.New(),
		UserID:               uuid.New(),
		Email:                "recipient@qq.com",
		NormalizedEmail:      "recipient@qq.com",
		UnsubscribeTokenHash: unsubscribeTokenHash(token),
		Status:               models.EmailCampaignRecipientStatusPending,
		Wave:                 1,
	}
	if err := app.db.Create(&recipient).Error; err != nil {
		t.Fatalf("create recipient: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/email/unsubscribe?token="+url.QueryEscape(token), nil)
	app.r.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("GET content type = %q", contentType)
	}
	if !strings.Contains(recorder.Body.String(), "已取消订阅") {
		t.Fatalf("GET body missing confirmation: %s", recorder.Body.String())
	}

	var updated models.EmailCampaignRecipient
	if err := app.db.First(&updated, "id = ?", recipient.ID).Error; err != nil {
		t.Fatalf("read recipient: %v", err)
	}
	if updated.Status != models.EmailCampaignRecipientStatusUnsubscribed {
		t.Fatalf("recipient status = %q", updated.Status)
	}
	if updated.UnsubscribedAt == nil {
		t.Fatal("recipient unsubscribed_at was not set")
	}
	var suppression models.EmailSuppression
	if err := app.db.Where("normalized_email = ?", recipient.NormalizedEmail).First(&suppression).Error; err != nil {
		t.Fatalf("read suppression: %v", err)
	}
	if suppression.Status != models.EmailSuppressionStatusActive || suppression.Reason != models.EmailSuppressionReasonUnsubscribed {
		t.Fatalf("suppression = %#v", suppression)
	}
	if suppression.SourceCampaignID == nil || *suppression.SourceCampaignID != recipient.CampaignID {
		t.Fatalf("suppression campaign = %#v", suppression.SourceCampaignID)
	}

	payload, err := json.Marshal(unsubscribeEmailRequest{Token: token})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/email/unsubscribe", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	app.r.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("POST status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("POST content type = %q", contentType)
	}
	var count int64
	if err := app.db.Model(&models.EmailSuppression{}).Where("normalized_email = ?", recipient.NormalizedEmail).Count(&count).Error; err != nil {
		t.Fatalf("count suppressions: %v", err)
	}
	if count != 1 {
		t.Fatalf("suppression count = %d, want 1", count)
	}
}

func TestEmailCampaignUnsubscribeJSONRejectsUnknownToken(t *testing.T) {
	app := newTestApp(t)
	payload := []byte(`{"token":"not-a-valid-token"}`)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/email/unsubscribe", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	app.r.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("content type = %q", contentType)
	}
}
