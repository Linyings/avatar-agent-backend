package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
)

func TestPortraitAssessmentRejectsMissingUnknownAndSideGaze(t *testing.T) {
	for _, raw := range []string{`{}`, `{"frontal":"pass","gaze":"unknown","identity":"pass","detail":"pass","framing":"pass"}`, `{"frontal":"fail","gaze":"pass","identity":"pass","detail":"pass","framing":"pass"}`} {
		var q portraitAssessment
		if err := json.Unmarshal([]byte(raw), &q); err != nil {
			t.Fatal(err)
		}
		if q.accepted() {
			t.Fatal("incomplete or bad portrait accepted")
		}
	}
}

func TestPortraitUnknownOrderIsNeverSubmittedTwice(t *testing.T) {
	w, s, _, _ := talkingWorkflow(t)
	s.ConfigSnapshot["portrait_policy"] = encodePortraitPolicy(currentPortraitPolicy())
	w.save(s)
	w.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		return json.RawMessage(`{"frontal":"fail","gaze":"unknown","identity":"pass","detail":"pass","framing":"pass"}`), nil
	}
	w.media = func(context.Context, string, string) (json.RawMessage, error) {
		return json.RawMessage(`{"frames":["data:image/jpeg;base64,eA=="]}`), nil
	}
	submits := 0
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "upload") {
			rw.Write([]byte(`{"code":0,"data":{"download_url":"https://example.com/photo.png"}}`))
			return
		}
		submits++
		rw.WriteHeader(502)
	}))
	defer server.Close()
	w.app.avatarMVP.options.Protocol = "runninghub"
	w.app.avatarMVP.options.BaseURL = server.URL
	w.app.avatarMVP.options.APIKey = "test"
	t.Setenv("RUNNINGHUB_BASE_URL", "")
	source := filepath.Join(w.projectDir(s.ProjectID), "assets", "photo")
	if _, err := w.preparePortrait(context.Background(), s, source); err == nil {
		t.Fatal("uncertain order accepted")
	}
	if _, err := w.preparePortrait(context.Background(), s, source); err == nil {
		t.Fatal("uncertain order retried")
	}
	if submits != 1 {
		t.Fatalf("paid submissions=%d", submits)
	}
}

func TestF06InjectsSkillUsesPreparedReferenceAndReviewsVideo(t *testing.T) {
	w, s, need, spy := talkingWorkflow(t)
	s.ConfigSnapshot["portrait_policy"] = encodePortraitPolicy(currentPortraitPolicy())
	w.save(s)
	original := w.media
	w.media = func(ctx context.Context, stage, dir string) (json.RawMessage, error) {
		if stage == "portrait-frames" {
			return json.RawMessage(`{"frames":["data:image/jpeg;base64,eA=="]}`), nil
		}
		return original(ctx, stage, dir)
	}
	calls := 0
	w.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"frontal":"pass","gaze":"pass","identity":"pass","detail":"pass","framing":"pass"}`), nil
	}
	need.Prompt = "面部略朝右。暖色光线"
	if err := w.produceMaterial(context.Background(), s, need, "F06"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(spy.requests) != 1 {
		t.Fatalf("reviews=%d requests=%d", calls, len(spy.requests))
	}
	if !strings.Contains(spy.requests[0].Prompt, "毛孔") || strings.Contains(spy.requests[0].Prompt, "略朝右") {
		t.Fatal(spy.requests[0].Prompt)
	}
	for _, step := range s.Steps {
		if step.ID == "F06/n1-c1" && !strings.Contains(string(step.PromptInput), "skill_hash") {
			t.Fatal("skill evidence absent")
		}
	}
}

func TestPortraitPolicyPinsSkillAndModelAndChangesFootageIdentity(t *testing.T) {
	p := currentPortraitPolicy()
	if !strings.Contains(p.SkillText, "毛孔") || p.SkillHash == "" || p.Model == "" {
		t.Fatal(p)
	}
	w, s, _, _ := talkingWorkflow(t)
	s.ConfigSnapshot["portrait_policy"] = encodePortraitPolicy(p)
	a := w.avatarQualityFingerprint(s)
	p.SkillText += "test"
	p.SkillHash = avatar.Digest(p.SkillText)
	s.ConfigSnapshot["portrait_policy"] = encodePortraitPolicy(p)
	if a == w.avatarQualityFingerprint(s) {
		t.Fatal("skill changes reused old footage")
	}
}

func TestFrontalPortraitIsCheckedOnceAndReusedWithoutImageOrder(t *testing.T) {
	w, s, _, _ := talkingWorkflow(t)
	s.ConfigSnapshot["portrait_policy"] = encodePortraitPolicy(currentPortraitPolicy())
	w.save(s)
	calls := 0
	w.content = func(_ context.Context, system string, input any, frames []string) (json.RawMessage, error) {
		calls++
		if len(frames) != 1 {
			t.Fatalf("frames: %d", len(frames))
		}
		return json.RawMessage(`{"frontal":"pass","gaze":"pass","identity":"pass","detail":"pass","framing":"pass"}`), nil
	}
	w.media = func(_ context.Context, stage, dir string) (json.RawMessage, error) {
		return json.RawMessage(`{"frames":["data:image/jpeg;base64,eA=="]}`), nil
	}
	source := filepath.Join(w.projectDir(s.ProjectID), "assets", "photo")
	first, err := w.preparePortrait(context.Background(), s, source)
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.preparePortrait(context.Background(), s, source)
	if err != nil || first != second || calls != 1 {
		t.Fatalf("%s %s calls=%d err=%v", first, second, calls, err)
	}
	if _, err = os.Stat(first); err != nil {
		t.Fatal(err)
	}
	for _, step := range s.Steps {
		if step.ProviderTaskID != "" {
			t.Fatal("unnecessary image order")
		}
	}
}
