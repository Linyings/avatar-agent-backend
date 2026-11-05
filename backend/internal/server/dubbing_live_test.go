package server

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// Real cloud ASR with an isolated SQLite billing fixture, never a production user.
func TestDubbingASRLive(t *testing.T) {
	if os.Getenv("DUBBING_ASR_LIVE_TEST") != "1" {
		t.Skip("live ASR disabled")
	}
	envBytes, err := os.ReadFile(os.Getenv("DUBBING_LIVE_ENV_FILE"))
	if err != nil {
		t.Fatal("configuration unavailable")
	}
	audio, err := os.ReadFile(os.Getenv("DUBBING_LIVE_AUDIO"))
	if err != nil {
		t.Fatal("audio unavailable")
	}
	app, user, run, _ := newWorkflowRunBillingFixture(t, "dubbing-live@example.com", nil)
	for _, line := range strings.Split(string(envBytes), "\n") {
		if strings.HasPrefix(line, "DASHSCOPE_API_KEY=") {
			app.cfg.DashScopeAPIKey = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "DASHSCOPE_API_KEY=")), "\"'")
		}
	}
	app.cfg.DubbingASRModel = "paraformer-v2"
	app.cfg.DubbingASRCreditsPerMinute = 1
	app.dashscope = providers.NewDashScopeClient(app.cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	file, _, err := app.dashscope.UploadTempFile(ctx, "paraformer-v2", "speech.wav", "audio/wav", audio)
	if err != nil {
		t.Fatal("temporary upload failed", err)
	}
	identity := url.Values{"workflow_run_id": {run.ID.String()}, "workflow_step_id": {"dubbing-asr-live"}, "workflow_input_hash": {run.InputHash}, "client_instance_id": {"billing-client"}}
	payload := map[string]any{"file_url": file, "diarization_enabled": true, "channel_id": []int{0}}
	for k, v := range identity {
		payload[k] = v[0]
	}
	raw, _ := json.Marshal(payload)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Set("user", user)
	c.Request = httptest.NewRequest("POST", "/", bytes.NewReader(raw)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	app.submitVendorASR("paraformer")(c)
	if rec.Code != 200 {
		t.Fatal("submit failed", rec.Code)
	}
	var result map[string]any
	json.Unmarshal(rec.Body.Bytes(), &result)
	if data, ok := result["data"].(map[string]any); ok {
		result = data
	}
	task, _ := result["task_id"].(string)
	if task == "" {
		t.Fatal("missing task")
	}
	t.Log("ASR submitted", task)
	for {
		rec = httptest.NewRecorder()
		c, _ = gin.CreateTestContext(rec)
		c.Set("user", user)
		c.Request = httptest.NewRequest("GET", "/?"+identity.Encode(), nil).WithContext(ctx)
		c.Params = gin.Params{{Key: "task_id", Value: task}}
		app.queryVendorASR("paraformer")(c)
		if rec.Code != 200 {
			t.Fatal("query failed", rec.Code)
		}
		json.Unmarshal(rec.Body.Bytes(), &result)
		if data, ok := result["data"].(map[string]any); ok {
			result = data
		}
		if result["status"] == "SUCCEEDED" {
			original, ok := result["raw_result"].(map[string]any)
			if !ok {
				t.Fatal("missing raw result")
			}
			tracks, ok := original["transcripts"].([]any)
			if !ok || len(tracks) == 0 {
				t.Fatal("missing tracks")
			}
			sentences, _ := tracks[0].(map[string]any)["sentences"].([]any)
			if len(sentences) == 0 {
				t.Fatal("missing dialogue")
			}
			var reservations []models.CreditReservation
			app.db.Where("workflow_run_id = ?", run.ID).Find(&reservations)
			if len(reservations) != 1 || reservations[0].CreditCount != 1 {
				t.Fatal("incorrect billing")
			}
			t.Logf("ASR passed: %d sentences, duration=%v, reserved=1 credit", len(sentences), result["duration"])
			return
		}
		if result["status"] != "PENDING" && result["status"] != "RUNNING" {
			t.Fatal("ASR failed")
		}
		select {
		case <-ctx.Done():
			t.Fatal("ASR timed out")
		case <-time.After(5 * time.Second):
		}
	}
}
