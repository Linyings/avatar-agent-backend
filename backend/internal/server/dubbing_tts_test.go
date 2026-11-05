package server

import (
	"bytes"
	"encoding/json"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
	"io"
	"net/http"
	"strings"
	"testing"
)

type qwenTestTransport func(*http.Request) (*http.Response, error)

func (f qwenTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestDubbingQwenTTSBillsActualUsageAndPreservesLegacy(t *testing.T) {
	f := newTTSWorkflowFixture(t, "siliconflow")
	run := createTTSWorkflowRun(t, f, "qwen-tts-input", "qwen-client")
	f.app.db.Model(&run).Updates(map[string]any{"client_type": "pc", "project_id": nil, "client_task_id": "qwen-task"})
	f.app.cfg.DubbingTTSCreditsMultiplier = 1.7
	f.app.dashscope = providers.NewDashScopeClient(config.Config{QwenTTSModel: "qwen3-tts-flash", DashScopeAPIKey: "test"})
	original := http.DefaultTransport
	defer func() { http.DefaultTransport = original }()
	audio := ttsWorkflowWAV([]byte(strings.Repeat("a", 80)))
	http.DefaultTransport = qwenTestTransport(func(r *http.Request) (*http.Response, error) {
		body := audio
		if r.Method == "POST" {
			body = []byte(`{"output":{"audio":{"url":"https://dashscope-result-bj.oss-cn-beijing.aliyuncs.com/audio.wav"}},"usage":{"characters":100}}`)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})
	payload := ttsWorkflowPayload(f, run)
	delete(payload, "project_id")
	payload["workflow_step_id"] = "dubbing-tts-test"
	payload["voice_uri"] = "qwen3-tts-instruct-flash:Ethan"
	response := request(f.app, "POST", "/api/v1/tools/dubbing/tts", payload, f.token)
	if response.Code != 200 {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "audio/wav" || !bytes.Equal(audio, response.Body.Bytes()) {
		t.Fatal("wrong audio response")
	}
	var reservation models.CreditReservation
	if err := f.app.db.Where("workflow_run_id = ?", run.ID).First(&reservation).Error; err != nil {
		t.Fatal(err)
	}
	if reservation.CharacterCount != 100 || reservation.CreditCount != 3 || reservation.ModelID != f.app.dashscope.DubbingTTSModel() {
		b, _ := json.Marshal(reservation)
		t.Fatalf("incorrect billing %s", b)
	}
	response = request(f.app, "POST", "/api/v1/tools/dubbing/tts", payload, f.token)
	if response.Code != 200 {
		t.Fatalf("replay %d: %s", response.Code, response.Body.String())
	}
	assertVisionWorkflowReservationCount(t, f.app, run.ID, 1)
	payload["instructions"] = "愤怒"
	if response = request(f.app, "POST", "/api/v1/tools/dubbing/tts", payload, f.token); response.Code != 409 {
		t.Fatalf("changed instruction reused billing: %d", response.Code)
	}
}
