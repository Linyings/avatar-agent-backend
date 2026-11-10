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

func TestWorkflowVoiceModelIsPerProjectAndScopesVoices(t *testing.T) {
	a := newTestApp(t)
	a.avatarMVP.options.DataRoot = t.TempDir()
	a.cfg.TTSModel = "minimax/speech-2.8-hd"
	t.Setenv("AVATAR_TTS_VOICES", "")

	legacy := avatar.WorkflowInput{Ratio: "9:16", Voice: "Calm_Woman"}
	if err := a.avatarWorkflow.validateInput(&legacy, false); err != nil && strings.Contains(err.Error(), "音色") {
		t.Fatalf("legacy project lost its MiniMax voice: %v", err)
	}
	if legacy.VoiceModel != "" || a.avatarWorkflow.voiceModel(legacy) != "minimax/speech-2.8-hd" {
		t.Fatal("projects without a model must keep the deployment model")
	}

	qwen := avatar.WorkflowInput{Ratio: "9:16", VoiceModel: "alibaba/qwen3-tts-flash"}
	if err := a.avatarWorkflow.validateInput(&qwen, false); err != nil && strings.Contains(err.Error(), "音色") {
		t.Fatal(err)
	}
	if qwen.Voice != "Cherry" {
		t.Fatalf("empty voice must take the chosen model default, got %q", qwen.Voice)
	}
	mixed := avatar.WorkflowInput{Ratio: "9:16", VoiceModel: "kling-lip-sync/tts", Voice: "Wise_Woman"}
	if err := a.avatarWorkflow.validateInput(&mixed, false); err == nil || !strings.Contains(err.Error(), "音色") {
		t.Fatalf("voice from another model must be rejected, got %v", err)
	}
	unknown := avatar.WorkflowInput{Ratio: "9:16", VoiceModel: "fish-audio/s2"}
	if err := a.avatarWorkflow.validateInput(&unknown, false); err == nil || !strings.Contains(err.Error(), "配音模型") {
		t.Fatalf("unregistered model must be rejected, got %v", err)
	}
	speed := avatar.WorkflowInput{Ratio: "9:16", VoiceModel: "alibaba/qwen3-tts-flash", Voice: "Cherry", VoiceSpeed: 1.1}
	if err := a.avatarWorkflow.validateInput(&speed, false); err == nil || !strings.Contains(err.Error(), "语速") {
		t.Fatalf("Qwen speed must be rejected before paying, got %v", err)
	}

	kling := a.avatarWorkflow.voicesFor("kling-lip-sync/tts")
	if len(kling) != 35 || kling[0].Model != "kling-lip-sync/tts" {
		t.Fatalf("model-scoped catalog wrong: %d", len(kling))
	}
	if a.avatarWorkflow.voiceSamplePathFor("kling-lip-sync/tts", "genshin_vindi2") == a.avatarWorkflow.voiceSamplePathFor("minimax/speech-2.8-hd", "genshin_vindi2") {
		t.Fatal("samples of different models must not share a cache file")
	}
	if a.avatarWorkflow.audioFingerprint(&avatar.WorkflowState{Input: qwen}, avatar.Segment{ID: "p1", Text: "x"}) ==
		a.avatarWorkflow.audioFingerprint(&avatar.WorkflowState{Input: avatar.WorkflowInput{Voice: "Cherry"}}, avatar.Segment{ID: "p1", Text: "x"}) {
		t.Fatal("audio cache must be keyed by the project's model")
	}
}

func TestWorkflowCatalogListsEveryModelWithSampleRoute(t *testing.T) {
	a := newTestApp(t)
	a.avatarMVP.options.DataRoot = t.TempDir()
	a.cfg.TTSModel = "minimax/speech-2.8-hd"
	sample := a.avatarWorkflow.voiceSamplePathFor("alibaba/qwen3-tts-flash", "Cherry")
	if err := os.MkdirAll(filepath.Dir(sample), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sample, append([]byte("RIFF\x00\x00\x00\x00WAVE"), make([]byte, 32)...), 0o600); err != nil {
		t.Fatal(err)
	}
	models := a.avatarWorkflow.voiceModels()
	if len(models) != len(avatar.SpeechModels()) || models[0].ID != "minimax/speech-2.8-hd" {
		t.Fatalf("deployment model must lead the list: %#v", models[0])
	}
	var qwen workflowVoiceModel
	for _, m := range models {
		if m.ID == "alibaba/qwen3-tts-flash" {
			qwen = m
		}
	}
	if qwen.DefaultVoice != "Cherry" || len(qwen.Voices) != 48 || qwen.Voices[0].Sample != "/api/v1/avatar-mvp/catalog/voices/Cherry/sample?model=alibaba%2Fqwen3-tts-flash" || qwen.SupportsSpeed {
		t.Fatalf("qwen catalog wrong: %#v", qwen.Voices[0])
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "voice", Value: "Cherry"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/avatar-mvp/catalog/voices/Cherry/sample?model=alibaba%2Fqwen3-tts-flash", nil)
	a.workflowVoiceSample(c)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "audio/wav" {
		t.Fatalf("model-scoped sample: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "voice", Value: "Cherry"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/avatar-mvp/catalog/voices/Cherry/sample?model=kling-lip-sync%2Ftts", nil)
	a.workflowVoiceSample(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("voice outside the model must 404, got %d", w.Code)
	}
}

func TestWorkflowVoiceSamplesAreSharedWithinAModelFamily(t *testing.T) {
	a := newTestApp(t)
	a.avatarMVP.options.DataRoot = t.TempDir()
	a.cfg.TTSModel = "minimax/speech-2.8-hd"
	shared := a.avatarWorkflow.voiceSamplePathFor("alibaba/qwen3-tts-flash", "Neil")
	if err := os.MkdirAll(filepath.Dir(shared), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shared, append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 32)...), 0o600); err != nil {
		t.Fatal(err)
	}
	var instructNeil workflowVoice
	for _, v := range a.avatarWorkflow.voicesFor("alibaba/qwen3-tts-instruct-flash") {
		if v.ID == "Neil" {
			instructNeil = v
		}
	}
	if instructNeil.Sample == "" {
		t.Fatal("Instruct must reuse the Flash sample of the same preset voice")
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "voice", Value: "Neil"}}
	c.Request = httptest.NewRequest(http.MethodGet, instructNeil.Sample, nil)
	a.workflowVoiceSample(c)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("shared sample not served: %d", w.Code)
	}
	// Different providers never share, even if an ID collided.
	for _, v := range a.avatarWorkflow.voicesFor("kling-lip-sync/tts") {
		if v.Sample != "" {
			t.Fatalf("kling borrowed a sample: %#v", v)
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
	run.ConfigID = workflowConfigID(run.ConfigSnapshot)
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
