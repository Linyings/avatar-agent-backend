package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
)

func TestWorkflowFailureKeepsInnermostStepAfterLaterIndependentFailure(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	first := w.step(context.Background(), &s, "F07/first", "input", func(ctx context.Context, _ *avatar.WorkflowStep) error {
		return w.step(ctx, &s, "F07/first/scene-2", "scene", func(_ context.Context, step *avatar.WorkflowStep) error {
			step.ErrorType = "model_output"
			return fmt.Errorf("模型输出被截断")
		})
	})
	_ = w.step(context.Background(), &s, "F07/last", "input", func(context.Context, *avatar.WorkflowStep) error { return fmt.Errorf("代码校验失败") })
	setWorkflowFailure(&s, first)
	if s.Stage != "F07/first/scene-2" || s.ErrorStepID != s.Stage || s.ErrorType != "model_output" || s.Error != "模型输出被截断" {
		t.Fatalf("wrong failure attribution: %s %s %s", s.Stage, s.ErrorType, s.Error)
	}
	if !errors.Is(&workflowStepError{StepID: "F07", Err: errWorkflowPaused}, errWorkflowPaused) {
		t.Fatal("pause identity lost")
	}
}

func TestDirectorMotionPhasesResumeWithoutRepeatingCompletedModelCalls(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	root := t.TempDir()
	a.avatarMVP.options.RuntimeRoot = root
	os.WriteFile(filepath.Join(root, "native-styles.json"), []byte(`[{"id":"mg-flat-vector","name":"平面","entry":"demos/01/index.html","design":{"features":"简洁"}}]`), 0600)
	s.Version = avatar.WorkflowVersion
	s.Input.Style = "mg-flat-vector"
	s.Segments = []avatar.Segment{{ID: "p1", Text: "研发投入110亿元。收入增长20%。", Duration: 4, Fingerprint: "speech", DirectorSceneID: "d1"}}
	s.Director = &avatar.DirectorPlan{Scenes: []avatar.DirectorScene{{ID: "d1", Visual: "相关镜头"}, {ID: "d2", Visual: "UNRELATED_SECRET_SCENE"}}}
	need := avatar.MaterialNeed{ID: "motion", SegmentID: "p1", Kind: "animation", Duration: 4, Prompt: "按句呈现"}
	s.Needs = []avatar.MaterialNeed{need}
	w.save(&s)
	calls := map[string]int{}
	w.content = func(ctx context.Context, prompt string, input any, frames []string) (json.RawMessage, error) {
		data, _ := json.Marshal(input)
		if strings.Contains(string(data), "UNRELATED_SECRET_SCENE") {
			t.Fatal("unrelated director context leaked")
		}
		in := input.(map[string]any)
		if !avatar.MotionCodeEnabled(ctx) {
			calls["plan"]++
			return json.RawMessage(`{"version":"content-motion/1","tone":"neutral","visual_context":"无人像","scenes":[{"quote":"研发投入110亿元","title":"研发投入","layout":"data","elements":[{"label":"110亿元","kind":"coin"}]},{"quote":"收入增长20%","title":"收入增长","layout":"data","elements":[{"label":"20%","kind":"chart"}]}]}`), nil
		}
		var scene avatar.MotionScene
		sceneJSON, _ := json.Marshal(in["scene"])
		if json.Unmarshal(sceneJSON, &scene) != nil || scene.Title == "" {
			t.Fatal("code request must contain exactly one scene")
		}
		calls[scene.Title]++
		if scene.Title == "收入增长" && calls[scene.Title] == 1 {
			return nil, completedModelOutputFixture{}
		}
		return json.RawMessage(`{"code":"var kind='document';ctx.fillRect(0,0,w,h);ctx.fillText(scene.title,20,40);"}`), nil
	}
	renders := 0
	w.media = func(_ context.Context, stage, dir string) (json.RawMessage, error) {
		if stage == "motion_context" {
			return json.RawMessage(`{"frames":[],"source":"no-avatar"}`), nil
		}
		if stage == "motion_validate" {
			return json.RawMessage(`{"valid":true}`), nil
		}
		if stage == "motion_timing" {
			return json.RawMessage(`{"scenes":[{"quote":"研发投入110亿元","title":"研发投入","layout":"data","elements":[{"label":"110亿元","kind":"coin","local_at":0.5}],"duration":2},{"quote":"收入增长20%","title":"收入增长","layout":"data","elements":[{"label":"20%","kind":"chart","local_at":0.3}],"duration":2}],"animation":{}}`), nil
		}
		if stage != "material" {
			t.Fatalf("unexpected stage %s", stage)
		}
		renders++
		if renders == 1 {
			return nil, fmt.Errorf("temporary local render failure")
		}
		os.WriteFile(filepath.Join(dir, "materials", "motion.mp4"), []byte("video"), 0600)
		return json.RawMessage(`{"need_id":"motion","segment_id":"p1","kind":"animation","path":"materials/motion.mp4","duration":4}`), nil
	}
	run := func() error { return w.produceMaterial(context.Background(), &s, need, "F07") }
	if err := run(); err == nil || !strings.Contains(err.Error(), "local render") {
		t.Fatalf("expected truncation repaired before local render failure, got %v", err)
	}
	if calls["plan"] != 1 || calls["研发投入"] != 1 || calls["收入增长"] != 2 || renders != 1 {
		t.Fatalf("wrong phase calls: %v render=%d", calls, renders)
	}
	// Simulate a worker restart and eviction of the disposable phase cache.
	files, _ := os.ReadDir(filepath.Join(w.dir(s.ID), "motion-phases"))
	for _, file := range files {
		if err := os.Remove(filepath.Join(w.dir(s.ID), "motion-phases", file.Name())); err != nil {
			t.Fatal(err)
		}
	}
	var loadErr error
	s, loadErr = w.load(context.Background(), owner, s.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if calls["plan"] != 1 || calls["研发投入"] != 1 || calls["收入增长"] != 2 || renders != 2 {
		t.Fatalf("completed work regenerated: %v renders=%d", calls, renders)
	}
	for _, step := range s.Steps {
		if strings.HasPrefix(step.ID, "F07/motion/") && (step.GenerationPrompt == "" || len(step.PromptInput) == 0 || step.Status != "succeeded") {
			t.Fatalf("missing phase record: %+v", step)
		}
		if step.ID == "F07/motion" && step.ProductionPhase != "视频已就绪" {
			t.Fatal("stale parent pointer")
		}
	}
	if len(s.Materials) != 1 || s.Materials[0].MotionPlan == nil || !strings.Contains(s.Materials[0].MotionPlan.Code, "scene.quote ===") {
		t.Fatal("missing assembled plan")
	}
}

func TestLegacyMotionPlanSurvivesRendererUpgradeButNotChangedPrompt(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	s.Version = avatar.WorkflowVersion
	s.Input.Style = "mg-flat-vector"
	seg := avatar.Segment{ID: "p1", Text: "研发投入110亿元", Duration: 3, Fingerprint: "speech"}
	need := avatar.MaterialNeed{ID: "motion", SegmentID: "p1", Kind: "animation", Prompt: "按句呈现"}
	s.Director = &avatar.DirectorPlan{Scenes: []avatar.DirectorScene{{ID: "d1", Visual: "研发"}}}
	input := map[string]any{"segment": seg, "topic": s.Input.Topic, "direction": s.Input.Direction, "style": s.Input.Style, "ratio": s.Input.Ratio, "material_content": need.Content, "requested_layout": need.Template, "requested_items": need.Items, "material_design": need.Prompt, "director": s.Director}
	raw, _ := json.Marshal(input)
	s.Steps = []avatar.WorkflowStep{{ID: "F07/motion", Attempt: 3, InputHash: avatar.Digest("old-renderer"), GenerationPrompt: "design" + directorMotionPrompt, PromptInput: raw}}
	plan := avatar.MotionPlan{Version: avatar.MotionPlanVersion, Tone: "neutral", Code: "var kind='document';ctx.fillRect(0,0,w,h);", Scenes: []avatar.MotionScene{{Quote: seg.Text, Title: "研发投入", Layout: "data", Elements: []avatar.MotionElement{{Label: "110亿元", Kind: "coin"}}}}}
	path := filepath.Join(a.avatarWorkflow.dir(s.ID), "materials", "motion-motion-plan.json")
	os.MkdirAll(filepath.Dir(path), 0700)
	if err := avatar.WriteJSON(path, map[string]any{"fingerprint": "old-renderer", "plan": plan}); err != nil {
		t.Fatal(err)
	}
	if got := a.avatarWorkflow.legacyMotionFingerprint(&s, need, seg); got != "old-renderer" {
		t.Fatalf("lost validated historical plan: %q", got)
	}
	// Early runs predate prompt recording. A plan produced in this same run is
	// still usable; edited needs use a new run and inherited steps have attempt 0.
	s.Needs = []avatar.MaterialNeed{need}
	s.Segments = []avatar.Segment{seg}
	s.Steps[0].GenerationPrompt = ""
	s.Steps[0].PromptInput = nil
	if got := a.avatarWorkflow.legacyMotionFingerprint(&s, need, seg); got != "old-renderer" {
		t.Fatal("old run without prompt records lost its plan")
	}
	s.Edits.MaterialOnly = need.ID
	if got := a.avatarWorkflow.legacyMotionFingerprint(&s, need, seg); got != "old-renderer" {
		t.Fatal("retry inside an explicit material revision lost its own completed plan")
	}
	s.Steps[0].Attempt = 0
	if got := a.avatarWorkflow.legacyMotionFingerprint(&s, need, seg); got != "" {
		t.Fatal("inherited plan treated as same-run legacy generation")
	}
	s.Steps[0].Attempt = 3
	need.Prompt = "重新设计"
	if got := a.avatarWorkflow.legacyMotionFingerprint(&s, need, seg); got != "" {
		t.Fatal("changed prompt reused old plan")
	}
}

func TestLegacyMotionCompletedVideoSurvivesUpgradeWithoutLocalPlan(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	s.Version = avatar.WorkflowVersion
	need := avatar.MaterialNeed{ID: "motion", Kind: "animation", SegmentID: "p1", Duration: 3, Prompt: "原设计"}
	s.Needs = []avatar.MaterialNeed{need}
	s.Segments = []avatar.Segment{{ID: "p1", Text: "已完成视频", Duration: 3}}
	s.Steps = []avatar.WorkflowStep{{ID: "F07/motion", Status: "succeeded", Attempt: 1, InputHash: avatar.Digest("old-renderer")}}
	s.Materials = []avatar.Material{{NeedID: need.ID, SegmentID: need.SegmentID, Kind: "animation", Duration: 3, Path: "materials/motion.mp4", Fingerprint: "old-renderer"}}
	file := filepath.Join(w.dir(s.ID), "materials", "motion.mp4")
	os.MkdirAll(filepath.Dir(file), 0700)
	os.WriteFile(file, []byte("existing-video"), 0600)
	w.save(&s)
	w.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		t.Fatal("completed video requested model again")
		return nil, nil
	}
	w.media = func(context.Context, string, string) (json.RawMessage, error) {
		t.Fatal("completed video rendered again")
		return nil, nil
	}
	if err := w.produceMaterial(context.Background(), &s, need, "F07"); err != nil {
		t.Fatal(err)
	}
	s.Steps = append(s.Steps, avatar.WorkflowStep{ID: "F07/motion/scene-1", Status: "succeeded", Attempt: 1})
	if err := w.produceMaterial(context.Background(), &s, need, "F07"); err != nil {
		t.Fatal("completed phased video lost after renderer upgrade", err)
	}
	need.Prompt = "修改设计"
	if err := w.produceMaterial(context.Background(), &s, need, "F07"); err == nil {
		t.Fatal("different need incorrectly reused completed video")
	}
}
