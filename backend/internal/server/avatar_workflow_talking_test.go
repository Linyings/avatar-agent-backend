package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/narratoai/backend-go/internal/avatar"
)

// recordingSpy keeps every generation request submitted through it. Tests use it
// to count paid orders without caring which model produced them.
type recordingSpy struct {
	workflowProviderSpy
	requests []avatar.GenerationRequest
}

func (p *recordingSpy) Submit(ctx context.Context, in avatar.GenerationRequest) (avatar.ProviderResult, error) {
	p.requests = append(p.requests, in)
	return p.workflowProviderSpy.Submit(ctx, in)
}

// talkingWorkflow prepares a project that has a person photo, so portrait and
// speech tests can start from the same fixture. The returned spy is detached from
// the app: tests that assert on submissions install their own provider.
func talkingWorkflow(t *testing.T) (*avatarWorkflowService, *avatar.WorkflowState, avatar.MaterialNeed, *recordingSpy) {
	t.Helper()
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	spy := &recordingSpy{workflowProviderSpy: workflowProviderSpy{ready: true}}
	a.avatarMVP.options.PollInterval = time.Millisecond
	photo := avatar.SourceAsset{ID: "photo", Kind: "photo", Path: "assets/photo"}
	s.Input.Photo = photo.ID
	s.Input.Assets = []avatar.SourceAsset{photo}
	s.Segments = []avatar.Segment{{ID: "p1", Text: "一。", Audio: "p1/audio.wav", NativeVideo: "p1/native.mp4", Duration: 2.6, Fingerprint: "audio"}}
	s.ConfigSnapshot = w.configSnapshot()
	// Portrait lifecycle is covered separately; this fixture exercises transport.
	delete(s.ConfigSnapshot, "portrait_policy")
	w.save(&s)
	w.media = func(_ context.Context, stage, dir string) (json.RawMessage, error) {
		os.MkdirAll(filepath.Join(dir, "materials"), 0700)
		os.WriteFile(filepath.Join(dir, "materials", "n1.mp4"), []byte("unit-test-video"), 0600)
		return json.RawMessage(`{"need_id":"n1","segment_id":"p1","kind":"avatar","duration":2.6,"path":"materials/n1.mp4"}`), nil
	}
	for _, dir := range []string{w.dir(s.ID), filepath.Join(w.dir(s.ID), "p1"), filepath.Join(w.projectDir(s.ProjectID), "assets")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(w.dir(s.ID), "p1", "native.mp4"), []byte("unit-test-video"), 0600)
	os.WriteFile(filepath.Join(w.dir(s.ID), "p1", "audio.wav"), []byte("unit-test-audio"), 0600)
	os.WriteFile(filepath.Join(w.projectDir(s.ProjectID), "assets", "photo"), []byte("unit-test-photo"), 0600)
	return w, &s, avatar.MaterialNeed{ID: "n1", SegmentID: "p1", Kind: "avatar", Duration: 2.6, Angle: "正面", Prompt: "暖色光线"}, spy
}

// Portrait preparation still uploads the source photo and orders the frontal
// reference through this client. Footage itself now comes from Seedance 2.5, so
// the old Kling talking-avatar tests were removed with that pipeline.
func TestTalkingClientUsesTheConfiguredVideoAccountSite(t *testing.T) {
	a := newTestApp(t)
	a.avatarMVP.options.Protocol, a.avatarMVP.options.BaseURL, a.avatarMVP.options.APIKey = "runninghub", "https://www.runninghub.ai", "video-key"
	t.Setenv("RUNNINGHUB_BASE_URL", "")
	if c := a.avatarWorkflow.talkingClient(); c.BaseURL != "https://www.runninghub.ai" || c.APIKey != "video-key" {
		t.Fatalf("portrait preparation must use the video account: %s", c.BaseURL)
	}
}
