package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
)

func TestMotionRepairCarriesLatestErrorAndStopsAfterThreeAutomaticRetries(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	calls := 0
	w.content = func(_ context.Context, _ string, input any, _ []string) (json.RawMessage, error) {
		calls++
		raw, _ := json.Marshal(input)
		if calls > 1 && !strings.Contains(string(raw), fmt.Sprintf("错误%d", calls-1)) {
			t.Fatalf("missing latest diagnostic: %s", raw)
		}
		if calls > 1 && !strings.Contains(string(raw), "previous_output") {
			t.Fatal("failed code not supplied for repair")
		}
		return json.RawMessage(`{"code":"ctx.fillText(scene.title,20,40);"}`), nil
	}
	var out struct {
		Code string `json:"code"`
	}
	run := func() error {
		return w.motionPhase(context.Background(), &s, "F07/m/scene-1", "场景1", "prompt", map[string]any{"scene": "one"}, nil, "fp", &out, func(context.Context) error {
			return &workflowMediaError{Code: "motion_text_missing", Message: fmt.Sprintf("错误%d：关键内容未完整展示", calls)}
		})
	}
	if err := run(); err == nil {
		t.Fatal("expected failure")
	}
	if calls != 4 {
		t.Fatalf("initial plus three retries expected, got %d", calls)
	}
	var err error
	s, err = w.load(context.Background(), owner, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Steps) != 1 || s.Steps[0].Retry == nil || s.Steps[0].Retry.AutomaticRetries != 3 || !s.Steps[0].Retry.Exhausted {
		t.Fatalf("retry budget not persisted: %+v", s.Steps)
	}
	if err = run(); err == nil {
		t.Fatal("expected manual repair failure")
	}
	if calls != 5 {
		t.Fatalf("manual retry reset automatic budget: %d", calls)
	}
	if len(s.Steps[0].Attempts) != 5 || s.Steps[0].Attempts[0].Error == "" {
		t.Fatal("attempt diagnostics missing")
	}
}

func TestMotionRepairKeepsSuccessAndDoesNotRetryUnknownProviderSubmission(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	calls := 0
	w.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		calls++
		return nil, fmt.Errorf("connection lost")
	}
	var out map[string]any
	err := w.motionPhase(context.Background(), &s, "F07/m/plan", "方案", "prompt", map[string]any{}, nil, "fp", &out, func(context.Context) error { return nil })
	if err == nil || calls != 1 || s.Steps[0].Submission != "unknown" {
		t.Fatalf("uncertain request repeated: calls=%d err=%v", calls, err)
	}
}

func TestManualModelRetryIncludesOriginalErrorOutsideMotionSteps(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	s.Steps = []avatar.WorkflowStep{{ID: "F10/shot-1", Status: "failed", Error: "分镜使用区间超出真实素材", Submission: "completed"}}
	w.save(&s)
	w.content = func(_ context.Context, _ string, input any, _ []string) (json.RawMessage, error) {
		raw, _ := json.Marshal(input)
		if !strings.Contains(string(raw), "分镜使用区间超出真实素材") {
			t.Fatalf("blind retry: %s", raw)
		}
		return json.RawMessage(`{}`), nil
	}
	if err := w.step(context.Background(), &s, "F10/shot-1", "input", func(ctx context.Context, step *avatar.WorkflowStep) error {
		var out map[string]any
		return w.model(ctx, &s, step, "prompt", map[string]any{"duration": 3}, nil, &out)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMediaDiagnosticKeepsCauseAndRedactsCredentials(t *testing.T) {
	log := []byte("AVATAR_MEDIA_ERROR={\"code\":\"motion_code_invalid\",\"message\":\"不允许访问全局变量 top\",\"scene_index\":2}\nTraceback...\n")
	e := mediaFailure("motion_validate", log)
	if e.Code != "motion_code_invalid" || e.SceneIndex != 2 || !strings.Contains(e.Error(), "top") {
		t.Fatalf("cause lost: %+v", e)
	}
	e = mediaFailure("material", []byte("Error: 动效关键数字未实际绘制：300 亿美元\nTraceback..."))
	if !strings.Contains(e.Error(), "300 亿美元") {
		t.Fatal("legacy log unreadable")
	}
	if strings.Contains(safeRepairText("token=private-secret Bearer abc.def.ghi"), "private-secret") || strings.Contains(safeRepairText("Bearer abc.def.ghi"), "abc.def.ghi") {
		t.Fatal("credential leaked")
	}
}

func TestMotionRepairSuccessIsReusedAndRuntimeFailureDoesNotRegenerate(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	calls := 0
	w.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"code":"ctx.fillText(scene.title,20,40);"}`), nil
	}
	var out struct {
		Code string `json:"code"`
	}
	valid := func(context.Context) error {
		if calls == 1 {
			return &workflowMediaError{Code: "motion_code_invalid", Message: "不允许访问全局变量 window"}
		}
		return nil
	}
	run := func() error {
		return w.motionPhase(context.Background(), &s, "F07/m/scene-1", "场景", "prompt", map[string]any{}, nil, "fp", &out, valid)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || s.Steps[0].Status != "succeeded" {
		t.Fatal("success was regenerated")
	}
	calls = 0
	if err := w.motionPhase(context.Background(), &s, "F07/m/scene-2", "场景", "prompt", map[string]any{}, nil, "fp", &out, func(context.Context) error {
		return &workflowMediaError{Code: "media_execution", Message: "Chromium unavailable"}
	}); err == nil {
		t.Fatal("runtime failure ignored")
	}
	if calls != 1 {
		t.Fatal("environment failure retried as model error")
	}
}

func TestRenderedMotionRepairTargetsOnlyFailingScene(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	s.Steps = []avatar.WorkflowStep{{ID: "F07/m/plan", Status: "succeeded"}, {ID: "F07/m/scene-1", Status: "succeeded"}, {ID: "F07/m/scene-2", Status: "succeeded"}}
	w.save(&s)
	again, err := w.repairRenderedMotion(&s, "F07/m", &workflowMediaError{Code: "motion_text_missing", Message: "缩水未展示", SceneIndex: 2})
	if err != nil || !again || s.Steps[0].Status != "succeeded" || s.Steps[1].Status != "succeeded" || s.Steps[2].Status != "failed" || s.Steps[2].Retry.AutomaticRetries != 1 {
		t.Fatalf("wrong repair scope: %+v %v", s.Steps, err)
	}
}

func TestMotionPreviouslyRejectedCodeIsRevalidatedBeforePayingForRegeneration(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	input := map[string]any{"scene": map[string]any{"quote": "原文", "title": "原文"}}
	raw, _ := json.Marshal(input)
	id := "F07/m/scene-1"
	s.Steps = []avatar.WorkflowStep{{ID: id, Status: "failed", Attempt: 1, Submission: "completed", PromptInput: raw, Error: "本地媒体步骤 motion_validate 执行失败，请查看任务日志"}}
	dir := filepath.Join(w.dir(s.ID), "model-results")
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-attempt-1.json", avatar.Digest(id)[:16])), []byte(`{"code":"const top=20;ctx.fillRect(0,top,w,10);"}`), 0600)
	os.WriteFile(filepath.Join(w.dir(s.ID), "motion_validate.log"), []byte("Error: 专用动效源码不符合确定性 Canvas 绘制约定"), 0600)
	w.save(&s)
	w.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		t.Fatal("revalidated code must not incur model call")
		return nil, nil
	}
	var out struct {
		Code string `json:"code"`
	}
	if err := w.motionPhase(context.Background(), &s, id, "场景", "updated prompt", input, nil, "new-renderer", &out, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if s.Steps[0].Status != "succeeded" || s.Steps[0].CostStatus != "reused" || !strings.Contains(out.Code, "top") {
		t.Fatal("old result lost")
	}
}
