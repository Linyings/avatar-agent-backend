package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/providers"
)

func TestDirectorWorkflowPhotoChangeInvalidatesReferenceAndDesign(t *testing.T) {
	a, _, _, prior := workflowFixture(t)
	prior.Version = avatar.WorkflowVersion
	prior.Input.Assets = []avatar.SourceAsset{{ID: "face", Kind: "photo", SHA256: "old"}}
	prior.ReferencesAnalyzed = true
	prior.References = []avatar.ReferenceAnalysis{{ID: "face", Summary: "old photo"}}
	prior.Director = &avatar.DirectorPlan{Summary: "old design"}
	prior.ScriptConfirmed = true
	prior.Needs = []avatar.MaterialNeed{{ID: "host", Kind: "avatar"}}
	prior.Shots = []avatar.WorkflowShot{{ID: "S1"}}
	next := avatar.WorkflowState{Version: avatar.WorkflowVersion, Input: prior.Input, ConfigSnapshot: prior.ConfigSnapshot}
	next.Input.Assets = []avatar.SourceAsset{{ID: "face", Kind: "photo", SHA256: "new"}}
	if err := a.avatarWorkflow.inherit(&next, prior, avatar.WorkflowEdits{}); err != nil {
		t.Fatal(err)
	}
	if next.ReferencesAnalyzed || next.Director != nil || len(next.Needs) > 0 || len(next.Shots) > 0 {
		t.Fatal("photo change retained stale visual design")
	}
	if !next.ScriptConfirmed {
		t.Fatal("photo change unnecessarily discarded approved narration")
	}
}

func TestDirectorWorkflowDesignPrecedesNarration(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	s.Version = avatar.WorkflowVersion
	w := a.avatarWorkflow
	os.MkdirAll(w.dir(s.ID), 0700)
	w.save(&s)
	var prompts []string
	w.content = func(_ context.Context, prompt string, _ any, _ []string) (json.RawMessage, error) {
		prompts = append(prompts, prompt)
		if strings.Contains(prompt, "制作需求分析") {
			return json.RawMessage(`{"goal":"讲清主题","audience":"普通观众","key_points":["原理"]}`), nil
		}
		if strings.Contains(prompt, "统一导演稿") {
			return json.RawMessage(`{"summary":"说明原理","scenes":[{"id":"d1","purpose":"讲清原理","composition":"visual-hero","visual":"演示","motion":"展开","estimated_duration":6}]}`), nil
		}
		return json.RawMessage(`{"segments":[{"id":"p1","text":"第一句。第二句。","director_scene_id":"d1"}]}`), nil
	}
	if err := w.produceWorkflow(context.Background(), &s, owner); err != nil {
		t.Fatal(err)
	}
	if s.Confirmation != "script" || s.Director == nil || len(prompts) != 3 {
		t.Fatalf("wrong design order: %+v %d", s.Director, len(prompts))
	}
	if _, err := os.Stat(filepath.Join(w.dir(s.ID), "director.md")); err != nil {
		t.Fatal(err)
	}
	if err := w.produceWorkflow(context.Background(), &s, owner); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 3 {
		t.Fatal("resuming a gate repeated model calls")
	}
}

func TestDirectorWorkflowReviewRejectsMissingAndStalePreviews(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	s.Version = avatar.WorkflowVersion
	s.Shots = []avatar.WorkflowShot{{ID: "S1", Start: 0, End: 2}}
	s.Status = "awaiting_confirmation"
	s.Confirmation = "shots"
	s.ConfirmationVersion = avatar.ReviewFingerprint(s, "shots")
	w.save(&s)
	if w.confirm(context.Background(), owner, s.ID, "shots", s.ConfirmationVersion, nil) == nil {
		t.Fatal("accepted missing previews")
	}
	s.Artifacts = []avatar.WorkflowArtifact{{ID: "preview-S1", Path: "preview/S1.mp4", MediaType: "video/mp4"}}
	os.MkdirAll(filepath.Join(w.dir(s.ID), "preview"), 0700)
	os.WriteFile(filepath.Join(w.dir(s.ID), "preview/S1.mp4"), []byte("fixture"), 0600)
	s.ConfirmationVersion = avatar.ReviewFingerprint(s, "shots")
	w.save(&s)
	if err := w.confirm(context.Background(), owner, s.ID, "shots", s.ConfirmationVersion, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := w.load(context.Background(), owner, s.ID)
	if got.ShotsConfirmed != s.ConfirmationVersion {
		t.Fatal("approval not persisted")
	}
	if err := w.confirm(context.Background(), owner, s.ID, "shots", s.ConfirmationVersion, nil); err == nil {
		t.Fatal("replayed approval accepted")
	}
}

func TestDirectorWorkflowStopsBeforeRenderAndBeforeDelivery(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	w.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		t.Fatal("review/render/delivery must remain local without whole-film model output")
		return nil, nil
	}
	s.Version = avatar.WorkflowVersion
	s.Shots = []avatar.WorkflowShot{{ID: "S1", Start: 0, End: 2}}
	s.Artifacts = []avatar.WorkflowArtifact{{ID: "preview-S1", Path: "preview/S1.mp4", MediaType: "video/mp4"}}
	os.MkdirAll(filepath.Join(w.dir(s.ID), "preview"), 0700)
	os.WriteFile(filepath.Join(w.dir(s.ID), "preview/S1.mp4"), []byte("fixture"), 0600)
	w.save(&s)
	renders := 0
	w.media = func(_ context.Context, stage, dir string) (json.RawMessage, error) {
		if stage != "render" {
			t.Fatalf("unexpected stage %s", stage)
		}
		renders++
		os.WriteFile(filepath.Join(dir, "output.mp4"), []byte("fixture"), 0600)
		os.WriteFile(filepath.Join(dir, "cover.jpg"), []byte("fixture"), 0600)
		return json.RawMessage(`{"artifacts":[{"id":"video","path":"output.mp4","media_type":"video/mp4"},{"id":"cover","path":"cover.jpg","media_type":"image/jpeg"}]}`), nil
	}
	if err := w.finishDirectorWorkflow(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	if s.Confirmation != "shots" || renders != 0 {
		t.Fatal("rendered before shot approval")
	}
	if err := w.confirm(context.Background(), owner, s.ID, "shots", s.ConfirmationVersion, nil); err != nil {
		t.Fatal(err)
	}
	s, _ = w.load(context.Background(), owner, s.ID)
	if err := w.finishDirectorWorkflow(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	if s.Confirmation != "final" || renders != 1 {
		t.Fatal("did not wait for complete film review")
	}
	for _, step := range s.Steps {
		if step.ID == "F12" {
			t.Fatal("delivered without review")
		}
	}
	if err := w.confirm(context.Background(), owner, s.ID, "final", s.ConfirmationVersion, nil); err != nil {
		t.Fatal(err)
	}
	s, _ = w.load(context.Background(), owner, s.ID)
	if err := w.finishDirectorWorkflow(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	if renders != 1 {
		t.Fatal("confirmation re-rendered identical film")
	}
	if s.Steps[len(s.Steps)-1].ID != "F12" {
		t.Fatal("delivery not executed")
	}
}

func TestDirectorReferenceRejectsPrivateNetwork(t *testing.T) {
	if _, err := readReferenceURL(context.Background(), "http://127.0.0.1:8080/private"); err == nil {
		t.Fatal("reference reader accessed local network")
	}
}

func TestDirectorWorkflowLivePlanning(t *testing.T) {
	input := os.Getenv("AVATAR_DIRECTOR_LIVE_INPUT")
	if input == "" {
		t.Skip("real model acceptance is opt-in")
	}
	var fixture struct {
		Input   avatar.WorkflowInput `json:"input"`
		Segment avatar.Segment       `json:"segment"`
		Need    avatar.MaterialNeed  `json:"need"`
		Context json.RawMessage      `json:"context"`
	}
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	s.Version = avatar.WorkflowVersion
	s.Input = fixture.Input
	s.Segments = []avatar.Segment{fixture.Segment}
	s.Needs = []avatar.MaterialNeed{fixture.Need}
	a.avatarMVP.options.RuntimeRoot = os.Getenv("AVATAR_DIRECTOR_RUNTIME")
	output := os.Getenv("AVATAR_DIRECTOR_LIVE_OUTPUT")
	cfg := config.Load()
	target, err := url.Parse(cfg.MovieVisionAPIBaseURL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(r *http.Request) { director(r); r.Host = target.Host }
	proxy.ModifyResponse = func(r *http.Response) error {
		body, err := io.ReadAll(r.Body)
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		if err == nil && r.StatusCode == 200 {
			var result struct {
				Choices json.RawMessage `json:"choices"`
			}
			if json.Unmarshal(body, &result) == nil {
				_ = avatar.WriteJSON(filepath.Join(output, "motion-response.json"), result)
			}
		}
		return err
	}
	upstream := httptest.NewServer(proxy)
	defer upstream.Close()
	cfg.MovieVisionAPIBaseURL = upstream.URL
	client := providers.NewOpenAIClient(cfg, nil)
	w.content = client.AvatarContent
	if replay := os.Getenv("AVATAR_DIRECTOR_LIVE_REPLAY"); replay != "" {
		var response struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		data, err := os.ReadFile(replay)
		if err != nil || json.Unmarshal(data, &response) != nil || len(response.Choices) == 0 {
			t.Fatal("missing captured response")
		}
		body := strings.TrimSpace(response.Choices[0].Message.Content)
		body = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(body, "```json"), "```"), "```"))
		if !json.Valid([]byte(body)) {
			t.Fatal("captured response is not valid JSON")
		}
		w.content = func(ctx context.Context, prompt string, input any, frames []string) (json.RawMessage, error) {
			if avatar.MotionCodeEnabled(ctx) {
				return json.RawMessage(body), nil
			}
			return nil, fmt.Errorf("replay must reuse all completed planning, no new model calls")
		}
	}
	if data, err := os.ReadFile(filepath.Join(output, "planning-state.json")); err == nil {
		var cached avatar.WorkflowState
		if json.Unmarshal(data, &cached) == nil && avatar.Digest(cached.Input) == avatar.Digest(s.Input) {
			s.Requirements, s.References, s.ReferencesAnalyzed, s.Director = cached.Requirements, cached.References, cached.ReferencesAnalyzed, cached.Director
		}
	}
	os.MkdirAll(filepath.Join(w.dir(s.ID), "materials"), 0700)
	w.save(&s)
	ctx := context.Background()
	if err = w.analyzeRequirements(ctx, &s); err != nil {
		t.Fatal(err)
	}
	if err = w.analyzeReferences(ctx, &s); err != nil {
		t.Fatal(err)
	}
	if err = w.designDirector(ctx, &s); err != nil {
		t.Fatal(err)
	}
	avatar.CalibrateDirector(&s)
	if err = avatar.WriteJSON(filepath.Join(output, "planning-state.json"), s); err != nil {
		t.Fatal(err)
	}
	if err = w.writeDirectorDocuments(&s); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"director.md", "references.md", "requirements.md"} {
		if err = copyWorkflowFile(filepath.Join(w.dir(s.ID), name), filepath.Join(output, name)); err != nil {
			t.Fatal(err)
		}
	}
	w.media = func(context.Context, string, string) (json.RawMessage, error) { return fixture.Context, nil }
	s.Steps = append(s.Steps, avatar.WorkflowStep{ID: "F07/" + fixture.Need.ID, Status: "running", Attempt: 1})
	w.save(&s)
	plan, err := w.planMotion(ctx, &s, &s.Steps[len(s.Steps)-1], fixture.Need, fixture.Segment, "director-live")
	if err != nil {
		t.Fatal(err)
	}
	if err = avatar.WriteJSON(filepath.Join(output, "motion-plan.json"), plan); err != nil {
		t.Fatal(err)
	}
	if err = avatar.WriteJSON(filepath.Join(output, "director-plan.json"), s.Director); err != nil {
		t.Fatal(err)
	}
	if err = w.writeDirectorDocuments(&s); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"director.md", "references.md", "requirements.md"} {
		if err = copyWorkflowFile(filepath.Join(w.dir(s.ID), name), filepath.Join(output, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("real model acceptance: director scenes=%d, motion scenes=%d, source chars=%d", len(s.Director.Scenes), len(plan.Scenes), len(plan.Code))
}
