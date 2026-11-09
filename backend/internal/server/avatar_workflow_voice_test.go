package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/config"
)

func TestWorkflowDefaultVoiceMatchesModel(t *testing.T) {
	for _, tc := range []struct{ name, model, configured, want string }{
		{"minimax empty", "minimax/speech-2.8-hd", "", "Wise_Woman"},
		{"minimax legacy doubao", "minimax/speech-2.8-hd", "zh_male_shaonianzixin_uranus_bigtts", "Wise_Woman"},
		{"minimax explicit voice", "minimax/speech-2.8-hd", " Friendly_Person ", "Friendly_Person"},
		{"doubao empty", "doubao-seed-tts-2.0", "", "zh_male_shaonianzixin_uranus_bigtts"},
		{"qwen existing default", "", "", "Cherry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{cfg: config.Config{TTSModel: tc.model, TTSSpeaker: tc.configured}}
			if got := sVoice(a); got != tc.want {
				t.Fatalf("voice = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWorkflowMiniMaxVoiceCatalogFiltersLegacyAndKeepsDefault(t *testing.T) {
	a := newTestApp(t)
	a.cfg.TTSModel = "minimax/speech-2.8-hd"
	a.cfg.TTSSpeaker = "zh_male_shaonianzixin_uranus_bigtts"
	t.Setenv("AVATAR_TTS_VOICES", "zh_male_shaonianzixin_uranus_bigtts,Friendly_Person,Friendly_Person")
	voices := a.avatarWorkflow.voices()
	if len(voices) != 17 || voices[0].ID != "Wise_Woman" || voices[1].ID != "Friendly_Person" {
		t.Fatalf("catalog retained another model's voice or lost its default: %#v", voices)
	}
	for _, voice := range voices {
		if voice.Model != a.cfg.TTSModel {
			t.Fatal("voice is not bound to the current model")
		}
	}
}

func TestWorkflowMiniMaxCatalogOffersAllSystemVoicesWithSamples(t *testing.T) {
	a := newTestApp(t)
	a.avatarMVP.options.DataRoot = t.TempDir()
	a.cfg.TTSModel = "minimax/speech-2.8-hd"
	t.Setenv("AVATAR_TTS_VOICES", "")
	sample := a.avatarWorkflow.voiceSamplePath("Calm_Woman")
	if err := os.MkdirAll(filepath.Dir(sample), 0o700); err != nil {
		t.Fatal(err)
	}
	mp3 := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 64)...)
	if err := os.WriteFile(sample, mp3, 0o600); err != nil {
		t.Fatal(err)
	}
	voices := a.avatarWorkflow.voices()
	ids := map[string]workflowVoice{}
	for _, v := range voices {
		ids[v.ID] = v
	}
	for _, v := range avatar.SystemSpeechVoices(a.cfg.TTSModel) {
		got, found := ids[v.ID]
		if !found || got.Name != v.Name {
			t.Fatalf("system voice %s missing or unnamed: %#v", v.ID, got)
		}
	}
	if len(voices) != 17 || ids["Calm_Woman"].Sample == "" || ids["Elegant_Man"].Sample != "" {
		t.Fatalf("samples must only be advertised when cached: %#v", voices)
	}
	in := avatar.WorkflowInput{Voice: "Deep_Voice_Man", Style: "", Ratio: "9:16"}
	if err := a.avatarWorkflow.validateInput(&in, false); err != nil && strings.Contains(err.Error(), "音色") {
		t.Fatalf("system voice rejected: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "voice", Value: "Calm_Woman"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/avatar-mvp/catalog/voices/Calm_Woman/sample", nil)
	a.workflowVoiceSample(c)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("mp3 sample served as %d %q", w.Code, w.Header().Get("Content-Type"))
	}
}

func TestWorkflowMiniMaxDraftRepairsLegacyVoiceWithoutChangingUnknownOrder(t *testing.T) {
	a, owner, project, run := workflowFixture(t)
	a.cfg.TTSModel = "minimax/speech-2.8-hd"
	a.cfg.TTSSpeaker = "zh_male_shaonianzixin_uranus_bigtts"
	t.Setenv("AVATAR_TTS_VOICES", "")
	run.Input.Voice = a.cfg.TTSSpeaker
	run.ConfigSnapshot = a.avatarWorkflow.configSnapshot()
	run.ConfigID = avatar.Digest(run.ConfigSnapshot)
	run.Status = "submission_unknown"
	run.Steps = []avatar.WorkflowStep{{ID: "F04/p1", Status: "failed", Submission: "unknown", Attempt: 1}}
	if err := a.avatarWorkflow.save(&run); err != nil {
		t.Fatal(err)
	}
	baseline, err := a.avatarWorkflow.load(context.Background(), owner, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := avatar.Digest(baseline)
	_, err = (avatarPackageHost{a}).Invoke(context.Background(), owner.String(), project.ID.String(), "avatar.configure", map[string]any{
		"revision": float64(1), "voice": a.cfg.TTSSpeaker,
	}, "voice-default-repair")
	if err != nil {
		t.Fatal(err)
	}
	_, input, err := a.avatarWorkflow.project(context.Background(), owner, project.ID)
	if err != nil || input.Voice != "Wise_Woman" || input.Revision != 2 {
		t.Fatalf("draft voice was not migrated: %#v %v", input, err)
	}
	stored, err := a.avatarWorkflow.load(context.Background(), owner, run.ID)
	if err != nil || avatar.Digest(stored) != before {
		t.Fatal("changing draft voice rewrote an unresolved order")
	}
	if err = a.avatarWorkflow.retry(context.Background(), owner, run.ID); err == nil || !strings.Contains(err.Error(), "提交状态待确认") {
		t.Fatal("voice correction bypassed the unresolved-order guard")
	}
	bad := input
	bad.Voice = "unlisted-voice"
	if err = a.avatarWorkflow.validateInput(&bad, false); err == nil {
		t.Fatal("unknown user voice was silently accepted")
	}
}
