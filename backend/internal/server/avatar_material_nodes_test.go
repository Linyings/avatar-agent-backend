package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
)

func TestMaterialNodeDraftPersistsWithoutChangingProduction(t *testing.T) {
	a, s := completedNodeFixture(t)
	h := avatarPackageHost{a}
	ctx := context.Background()
	owner := a.avatarWorkflow.ownerID(&s)
	a.avatarWorkflow.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		t.Fatal("save must not call model")
		return nil, nil
	}
	req := materialNodeRequest{Action: "save", BaseRunID: s.ID, Prompt: "只在关键词出现时展开图形"}
	out, err := h.materialNodeAction(ctx, owner, s.ProjectID, "n1", req)
	if err != nil {
		t.Fatal(err)
	}
	p, _, _ := h.project(ctx, owner, s.ProjectID)
	if materialDrafts(p)["n1"].Prompt != req.Prompt || out.Draft.Version == "" {
		t.Fatal("draft was not persisted")
	}
	old, _ := a.avatarWorkflow.load(ctx, uuid.MustParse(owner), s.ID)
	if old.Needs[0].Prompt != "" || old.Status != "succeeded" {
		t.Fatal("draft mutated production")
	}
	if _, err = h.materialNodeAction(ctx, owner, s.ProjectID, "n1", req); err == nil {
		t.Fatal("stale draft overwrote newer draft")
	}
	if _, err = h.materialNodeAction(ctx, uuid.NewString(), s.ProjectID, "n1", req); err == nil {
		t.Fatal("other owner edited draft")
	}
	req.Version = out.Draft.Version
	req.BaseRunID = uuid.NewString()
	if _, err = h.materialNodeAction(ctx, owner, s.ProjectID, "n1", req); err == nil {
		t.Fatal("stale run accepted")
	}
}

func TestMaterialNodeAIOnlyRewritesPrompt(t *testing.T) {
	a, s := completedNodeFixture(t)
	h := avatarPackageHost{a}
	owner := a.avatarWorkflow.ownerID(&s)
	calls := 0
	a.avatarWorkflow.content = func(_ context.Context, _ string, input any, _ []string) (json.RawMessage, error) {
		calls++
		if input.(map[string]any)["prompt"] != "旧草稿" {
			t.Fatal("model missed draft")
		}
		return json.RawMessage(`{"prompt":"逐句揭示原理，保留字幕区域"}`), nil
	}
	out, err := h.materialNodeAction(context.Background(), owner, s.ProjectID, "n1", materialNodeRequest{Action: "improve", BaseRunID: s.ID, Prompt: "旧草稿", Instruction: "图形更简洁"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || out.Draft.Prompt != "逐句揭示原理，保留字幕区域" || out.RunID != "" {
		t.Fatal("AI rewrite did not remain a draft")
	}
}

func TestMaterialPromptImprovementReceivesCurrentFailureHistory(t *testing.T) {
	a, s := completedNodeFixture(t)
	s.Status = "failed"
	s.Steps = []avatar.WorkflowStep{{ID: "F07/n1/scene-2", Status: "failed", Error: "缩水未展示", Retry: &avatar.WorkflowRetry{LastError: "缩水未展示", Strategy: "调整入场时间", AutomaticRetries: 3, Exhausted: true}, Attempts: []avatar.WorkflowAttempt{{Number: 1, Error: "场景时长不足"}}}, {ID: "F07/other/scene-1", Status: "failed", Error: "无关错误"}}
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	a.avatarWorkflow.content = func(_ context.Context, _ string, input any, _ []string) (json.RawMessage, error) {
		raw, _ := json.Marshal(input)
		for _, want := range []string{"failure_context", "缩水未展示", "调整入场时间", "场景时长不足"} {
			if !strings.Contains(string(raw), want) {
				t.Fatalf("missing failure evidence %s: %s", want, raw)
			}
		}
		if strings.Contains(string(raw), "无关错误") {
			t.Fatal("included another material's failure")
		}
		return json.RawMessage(`{"prompt":"按实际时长提前展示缩水图块"}`), nil
	}
	h := avatarPackageHost{a}
	out, err := h.materialNodeAction(context.Background(), a.avatarWorkflow.ownerID(&s), s.ProjectID, "n1", materialNodeRequest{Action: "improve", BaseRunID: s.ID, Prompt: "旧草稿"})
	if err != nil || out.RunID != "" || out.Draft == nil {
		t.Fatalf("rewrite must only save a draft: %+v %v", out, err)
	}
}

func TestMaterialNodeGenerationIsScopedAndIdempotent(t *testing.T) {
	a, s := completedNodeFixture(t)
	h := avatarPackageHost{a}
	ctx := context.Background()
	owner := a.avatarWorkflow.ownerID(&s)
	s.Needs = append(s.Needs, avatar.MaterialNeed{ID: "n2", Kind: "animation", SegmentID: "p1", Content: s.Script, Duration: 5, Template: "highlight"})
	s.Materials = append(s.Materials, avatar.Material{NeedID: "n2", SegmentID: "p1", Kind: "animation", Path: "materials/n2.mp4", Duration: 5, Fingerprint: "retained"})
	path := filepath.Join(a.avatarWorkflow.dir(s.ID), "materials/n2.mp4")
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, []byte("existing material"), 0600)
	s.Artifacts = append(s.Artifacts, avatar.WorkflowArtifact{ID: "material-n2", Path: "materials/n2.mp4", MediaType: "video/mp4"})
	a.avatarWorkflow.save(&s)
	req := materialNodeRequest{Action: "generate", BaseRunID: s.ID, Prompt: "重新设计动效", RequestID: "single-video-test"}
	out, err := h.materialNodeAction(ctx, owner, s.ProjectID, "n1", req)
	if err != nil {
		t.Fatal(err)
	}
	next, _ := a.avatarWorkflow.load(ctx, uuid.MustParse(owner), out.RunID)
	if next.Edits.MaterialOnly != "n1" || next.TargetNode != "F08" || len(next.Materials) != 1 || next.Materials[0].NeedID != "n2" || next.Needs[0].Prompt != req.Prompt || !next.AudioConfirmed {
		t.Fatalf("wrong scope: %#v", next.Edits)
	}
	if data, err := os.ReadFile(filepath.Join(a.avatarWorkflow.dir(next.ID), "materials/n2.mp4")); err != nil || string(data) != "existing material" {
		t.Fatal("unaffected video not retained")
	}
	again, err := h.materialNodeAction(ctx, owner, s.ProjectID, "n1", req)
	if err != nil || again.RunID != out.RunID {
		t.Fatalf("request replay: %v", err)
	}
	if err := a.avatarWorkflow.validateMaterialAssembly(&next); err == nil {
		t.Fatal("incomplete material set allowed to merge")
	}
	stages := []string{}
	a.avatarWorkflow.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		t.Fatal("single material reran unrelated planning")
		return nil, nil
	}
	a.avatarWorkflow.media = func(_ context.Context, stage, dir string) (json.RawMessage, error) {
		stages = append(stages, stage)
		if stage != "material" {
			t.Fatalf("unexpected stage: %s", stage)
		}
		path := filepath.Join(dir, "materials/n1.mp4")
		os.MkdirAll(filepath.Dir(path), 0700)
		os.WriteFile(path, []byte("new video"), 0600)
		return json.RawMessage(`{"need_id":"n1","segment_id":"p1","kind":"animation","path":"materials/n1.mp4","duration":5}`), nil
	}
	if err := a.avatarWorkflow.produceWorkflow(ctx, &next, uuid.MustParse(owner)); !errors.Is(err, errWorkflowPaused) {
		t.Fatalf("single generation failed: %v", err)
	}
	if len(stages) != 1 || len(next.Materials) != 2 {
		t.Fatalf("wrong single generation: %#v", stages)
	}
	if err := a.avatarWorkflow.validateMaterialAssembly(&next); err != nil {
		t.Fatal(err)
	}
	if err := a.avatarWorkflow.produceMaterial(ctx, &next, next.Needs[1], "F07"); err != nil {
		t.Fatal(err)
	}
	if len(stages) != 1 {
		t.Fatal("merge regenerated independent historical video")
	}
	req.Prompt = "different"
	if _, err = h.materialNodeAction(ctx, owner, s.ProjectID, "n1", req); err == nil {
		t.Fatal("same request accepted different prompt")
	}
}

func TestMaterialNodePersonChangeInvalidatesOnlyDependentMotion(t *testing.T) {
	a, previous := completedNodeFixture(t)
	previous.Needs = []avatar.MaterialNeed{
		{ID: "person", Kind: "avatar", SegmentID: "p1", Prompt: "正面"},
		{ID: "motion", Kind: "animation", SegmentID: "p1"},
		{ID: "other", Kind: "animation", SegmentID: "p2"},
	}
	previous.Materials = nil
	for _, n := range previous.Needs {
		rel := "materials/" + n.ID + ".mp4"
		path := filepath.Join(a.avatarWorkflow.dir(previous.ID), rel)
		os.MkdirAll(filepath.Dir(path), 0700)
		os.WriteFile(path, []byte("video"), 0600)
		previous.Materials = append(previous.Materials, avatar.Material{NeedID: n.ID, Kind: n.Kind, SegmentID: n.SegmentID, Path: rel, Duration: 5})
	}
	next := avatar.WorkflowState{ID: uuid.NewString(), AudioConfirmed: true, Needs: append([]avatar.MaterialNeed(nil), previous.Needs...), Edits: avatar.WorkflowEdits{MaterialOnly: "person"}}
	if err := a.avatarWorkflow.inheritMaterialNodes(&next, previous); err != nil {
		t.Fatal(err)
	}
	if len(next.Materials) != 1 || next.Materials[0].NeedID != "other" {
		t.Fatalf("wrong dependencies retained: %#v", next.Materials)
	}
}
