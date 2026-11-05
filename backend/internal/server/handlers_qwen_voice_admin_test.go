package server

import (
	"encoding/json"
	"github.com/narratoai/backend-go/internal/providers"
	"testing"
)

func TestQwenVoiceAdminSelection(t *testing.T) {
	f := newTTSWorkflowFixture(t, "siliconflow")
	path := "/api/v1/admin/tts/qwen/voices"
	for _, method := range []string{"GET", "PUT"} {
		if r := request(f.app, method, path, map[string]any{}, f.token); r.Code != 403 {
			t.Fatalf("non-admin %s: %d", method, r.Code)
		}
	}
	if r := request(f.app, "POST", "/api/v1/admin/tts/qwen/preview", map[string]any{}, f.token); r.Code != 403 {
		t.Fatalf("preview permission: %d", r.Code)
	}
	if err := f.app.db.Model(&f.user).Update("is_admin", true).Error; err != nil {
		t.Fatal(err)
	}
	r := request(f.app, "GET", path, nil, f.token)
	var result struct {
		Voices  []providers.DubbingTTSVoice `json:"voices"`
		Enabled []string                    `json:"enabled_voices"`
	}
	if r.Code != 200 {
		t.Fatalf("catalog: %d %s", r.Code, r.Body.String())
	}
	if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Voices) != 24 || len(result.Enabled) != 10 {
		t.Fatalf("unexpected catalog/defaults: %d/%d", len(result.Voices), len(result.Enabled))
	}
	ids := []string{"qwen3-tts-instruct-flash:Eldric Sage", "qwen3-tts-instruct-flash:Stella"}
	r = request(f.app, "PUT", path, map[string]any{"enabled_voices": ids}, f.token)
	if r.Code != 200 {
		t.Fatalf("save: %d %s", r.Code, r.Body.String())
	}
	for _, bad := range [][]string{nil, {ids[0]}, {ids[0], ids[0]}, {ids[0], "qwen3-tts-instruct-flash:Ryan"}} {
		if r := request(f.app, "PUT", path, map[string]any{"enabled_voices": bad}, f.token); r.Code != 400 {
			t.Fatalf("invalid selection: %d", r.Code)
		}
	}
	voices, err := f.app.enabledQwenVoices()
	if err != nil || len(voices) != 2 || voices[0].Value != ids[0] || voices[1].Value != ids[1] {
		t.Fatalf("persisted voices: %v %v", voices, err)
	}
	// Previously selected voices remain usable in existing projects.
	r = request(f.app, "GET", "/api/v1/tools/dubbing/capabilities", nil, f.token)
	var caps struct {
		Voices []providers.DubbingTTSVoice `json:"tts_voices"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &caps); err != nil || r.Code != 200 || len(caps.Voices) != 2 {
		t.Fatalf("PC capabilities do not reflect selection: %d %s", r.Code, r.Body.String())
	}
	if !providers.ValidDubbingTTSVoice("qwen3-tts-instruct-flash:Ethan") {
		t.Fatal("legacy project voice rejected")
	}
}
