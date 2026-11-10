package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/narratoai/backend-go/internal/avatar"
)

type talkingSpy struct {
	workflowProviderSpy
	requests []avatar.GenerationRequest
}

func (p *talkingSpy) Submit(ctx context.Context, in avatar.GenerationRequest) (avatar.ProviderResult, error) {
	p.requests = append(p.requests, in)
	return p.workflowProviderSpy.Submit(ctx, in)
}

func talkingWorkflow(t *testing.T) (*avatarWorkflowService, *avatar.WorkflowState, avatar.MaterialNeed, *talkingSpy) {
	t.Helper()
	w, s, need, spy, _ := talkingWorkflowPieces(t)
	return w, s, need, spy
}

// talkingWorkflowPieces also returns the pieces handed to avatar assembly.
func talkingWorkflowPieces(t *testing.T) (*avatarWorkflowService, *avatar.WorkflowState, avatar.MaterialNeed, *talkingSpy, *[]map[string]any) {
	t.Helper()
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	spy := &talkingSpy{workflowProviderSpy: workflowProviderSpy{ready: true}}
	w.talking = func() (avatarTalkingProvider, error) {
		return avatarTalkingProvider{Provider: spy, ModelID: "kling-v2-ai-avatar-pro/image-audio-to-video"}, nil
	}
	a.avatarMVP.provider = &workflowProviderSpy{ready: true}
	a.avatarMVP.providerErr = nil
	a.avatarMVP.options.PollInterval = time.Millisecond
	photo := avatar.SourceAsset{ID: "photo", Kind: "photo", Path: "assets/photo"}
	s.Input.Photo = photo.ID
	s.Input.Assets = []avatar.SourceAsset{photo}
	s.Segments = []avatar.Segment{{ID: "p1", Text: "一。", Audio: "p1/audio.wav", Duration: 2.6, Fingerprint: "audio"}}
	s.ConfigSnapshot = w.configSnapshot()
	// Portrait lifecycle is covered separately; this fixture exercises transport.
	delete(s.ConfigSnapshot, "portrait_policy")
	w.save(&s)
	var seenPieces []map[string]any
	w.media = func(_ context.Context, stage, dir string) (json.RawMessage, error) {
		if stage == "pieces" {
			return json.RawMessage(`{"pieces":[{"id":"c1","start":0,"duration":2.6,"audio":"pieces/n1-c1.wav","text":"一。"}]}`), nil
		}
		var in struct {
			Pieces []map[string]any `json:"pieces"`
		}
		data, _ := os.ReadFile(filepath.Join(dir, "media-input.json"))
		_ = json.Unmarshal(data, &in)
		seenPieces = in.Pieces
		os.WriteFile(filepath.Join(dir, "materials", "n1.mp4"), []byte("unit-test-video"), 0600)
		return json.RawMessage(`{"need_id":"n1","segment_id":"p1","kind":"avatar","duration":2.6,"path":"materials/n1.mp4"}`), nil
	}
	os.MkdirAll(w.dir(s.ID), 0700)
	os.MkdirAll(filepath.Join(w.projectDir(s.ProjectID), "assets"), 0700)
	os.WriteFile(filepath.Join(w.projectDir(s.ProjectID), "assets", "photo"), []byte("unit-test-photo"), 0600)
	return w, &s, avatar.MaterialNeed{ID: "n1", SegmentID: "p1", Kind: "avatar", Duration: 2.6, Angle: "正面", Prompt: "暖色光线"}, spy, &seenPieces
}

func TestTalkingAvatarDrivesF06WithTheNarrationVoice(t *testing.T) {
	w, s, need, spy, pieces := talkingWorkflowPieces(t)
	if err := w.produceMaterial(context.Background(), s, need, "F06"); err != nil {
		t.Fatal(err)
	}
	if len(*pieces) != 1 || (*pieces)[0]["lip_sync"] != "talking-avatar" {
		t.Fatalf("assembly must know the clip is voice-driven: %#v", *pieces)
	}
	if len(spy.requests) != 1 {
		t.Fatalf("submissions: %d", len(spy.requests))
	}
	got := spy.requests[0]
	if len(got.AudioURLs) != 1 || len(got.ImageURLs) != 1 || !strings.Contains(got.Prompt, "直视镜头") || !strings.Contains(got.Prompt, "暖色光线") {
		t.Fatalf("talking request: %#v", got)
	}
	var order *avatar.WorkflowStep
	for i := range s.Steps {
		if s.Steps[i].ID == "F06/n1-c1" {
			order = &s.Steps[i]
		}
	}
	if order == nil || order.Model != "kling-v2-ai-avatar-pro/image-audio-to-video" || order.ProviderTaskID != "original-order" {
		t.Fatalf("order must record the model it was paid on: %#v", order)
	}
	if n := w.app.avatarMVP.provider.(*workflowProviderSpy).submit; n != 0 {
		t.Fatalf("legacy video model was also charged %d times", n)
	}
}

func TestTalkingModelDefaultsToKlingProAndCanBeSwitchedOff(t *testing.T) {
	for env, want := range map[string]string{"": defaultTalkingModel, "off": "", "kling-v2-ai-avatar-standard/image-audio-to-video": "kling-v2-ai-avatar-standard/image-audio-to-video"} {
		t.Setenv("AVATAR_TALKING_MODEL", env)
		if got := talkingModelID(); got != want {
			t.Fatalf("%q -> %q, want %q", env, got, want)
		}
	}
	a := newTestApp(t)
	t.Setenv("AVATAR_TALKING_MODEL", "kling-v3.0-pro/image-to-video")
	if _, err := a.avatarWorkflow.defaultTalking(); err == nil {
		t.Fatal("a non-avatar model must be rejected")
	}
	t.Setenv("AVATAR_TALKING_MODEL", "")
	if p, err := a.avatarWorkflow.defaultTalking(); err != nil || p.ModelID != defaultTalkingModel {
		t.Fatalf("%#v %v", p, err)
	}
}

func TestTalkingAvatarUsesTheConfiguredVideoAccountSite(t *testing.T) {
	a := newTestApp(t)
	a.avatarMVP.options.Protocol, a.avatarMVP.options.BaseURL, a.avatarMVP.options.APIKey = "runninghub", "https://www.runninghub.ai", "video-key"
	t.Setenv("RUNNINGHUB_BASE_URL", "")
	if c := a.avatarWorkflow.talkingClient(); c.BaseURL != "https://www.runninghub.ai" || c.APIKey != "video-key" {
		t.Fatalf("talking avatar must use the video account: %s", c.BaseURL)
	}
}

func TestPersistedOrderIsQueriedOnTheModelItWasPlacedOn(t *testing.T) {
	w, _, _, _ := talkingWorkflow(t)
	w.talking = nil
	_, model, err := w.avatarOrderProvider(&avatar.WorkflowState{}, &avatar.WorkflowStep{Model: "kling-v2-ai-avatar-pro/image-audio-to-video", ProviderTaskID: "k-1"})
	if err != nil || model != "kling-v2-ai-avatar-pro/image-audio-to-video" {
		t.Fatalf("an existing talking order must keep its model after the default changes: %s %v", model, err)
	}
	if _, _, err := w.avatarOrderProvider(&avatar.WorkflowState{}, &avatar.WorkflowStep{Model: "retired/model", ProviderTaskID: "k-2"}); err == nil {
		t.Fatal("unknown order model must not fall back to another API")
	}
}

func TestTalkingAvatarChangesTheFootageIdentity(t *testing.T) {
	w, _, _, _ := talkingWorkflow(t)
	withTalking := w.avatarFootageConfig()
	w.talking = nil
	if w.avatarFootageConfig() == withTalking {
		t.Fatal("switching the avatar model must not reuse footage generated by another model")
	}
}

// A run keeps the footage model it was created with, so a deploy that changes
// the default neither breaks resuming its paid orders nor mixes two models.
func TestRunKeepsTheAvatarModelItWasCreatedWith(t *testing.T) {
	w, s, need, spy := talkingWorkflow(t)
	legacy := w.app.avatarMVP.options.VideoConfigID()
	s.ConfigSnapshot = map[string]string{"video": legacy}
	w.save(s)
	if got := w.runFootageConfig(s); got != legacy {
		t.Fatalf("run created before talking avatars must keep its video identity: %s", got)
	}
	if err := w.produceMaterial(context.Background(), s, need, "F06"); err != nil {
		t.Fatal(err)
	}
	if len(spy.requests) != 0 || w.app.avatarMVP.provider.(*workflowProviderSpy).submit != 1 {
		t.Fatal("an older run must continue on the model it started with")
	}
	if !strings.HasPrefix(w.configSnapshot()["avatar_footage"], "talking:") {
		t.Fatal("new runs must record the talking model")
	}
	snapshot := w.configSnapshot()
	old := map[string]string{}
	for k, v := range snapshot {
		if k != "avatar_footage" {
			old[k] = v
		}
	}
	if workflowConfigID(old) != workflowConfigID(snapshot) || !workflowProviderConfigCompatible(old, snapshot) {
		t.Fatal("recording the avatar model must not restart or block older runs")
	}
}
