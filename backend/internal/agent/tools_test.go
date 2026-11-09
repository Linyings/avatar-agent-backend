// tools_test.go — 工具实现测试
//
// 覆盖 P3 填实的四个关键工具：evidence 真实返回项目数据、
// memory 真读真写、edit_* 生成可用提案、plan_project 落记忆并通知前端。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
)

// avatarStateWith 构造真实工作流快照，用于验证适配器的过滤/映射逻辑
func avatarStateWith(texts ...string) avatar.WorkflowState {
	segments := make([]avatar.Segment, 0, len(texts))
	for i, text := range texts {
		segments = append(segments, avatar.Segment{
			ID:   "p" + strconv.Itoa(i+1),
			Text: text,
		})
	}
	return avatar.WorkflowState{
		ID:       "wf-1",
		Status:   "running",
		Stage:    "F03",
		Segments: segments,
	}
}

// newTestRegistry 构造带状态的注册表
func newTestRegistry(t *testing.T, phase ProjectPhase, state ProjectState, projectID string) (*ToolRegistry, *memoryStore) {
	t.Helper()
	reg := NewToolRegistry()
	mem := newMemoryStore()
	RegisterAvatarTools(reg, phase, state, mem, projectID, nil)
	return reg, mem
}

func fullState() *stubState {
	return &stubState{
		segments: []ScriptSegment{
			{ID: "p1", Content: "开场：介绍主题"},
			{ID: "p2", Content: "主体：展开讲解"},
		},
		needs: []MaterialNeed{
			{ID: "n1", SegmentID: "p1", Type: "avatar", Duration: "8 秒"},
		},
		shots: []Shot{
			{ID: "s1", SegmentID: "p1", Layout: "full", Duration: "4 秒"},
		},
		workflow: "当前节点: F12",
	}
}

// ─────────────────────────────────────────────
// evidence
// ─────────────────────────────────────────────

func TestEvidenceTool_ReturnsRealData(t *testing.T) {
	state := fullState()
	reg, _ := newTestRegistry(t, PhaseHasShot, state, "proj-1")

	out, blocked, issue := reg.Invoke(context.Background(),
		ToolCall{ID: "c1", Name: "evidence", Arguments: map[string]any{"query": "", "dataType": "all"}},
		"t1", func(any) {})
	if blocked {
		t.Fatalf("不应被拦截: %v", issue)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}
	for _, key := range []string{"segments", "materialNeeds", "shots"} {
		list, ok := result[key].([]any)
		if !ok || len(list) == 0 {
			t.Errorf("%s 应返回非空数组，got %v", key, result[key])
		}
	}
}

func TestEvidenceTool_FiltersByDataType(t *testing.T) {
	state := fullState()
	reg, _ := newTestRegistry(t, PhaseHasShot, state, "proj-1")

	out, _, _ := reg.Invoke(context.Background(),
		ToolCall{ID: "c1", Name: "evidence", Arguments: map[string]any{"query": "", "dataType": "script"}},
		"t1", func(any) {})

	var result map[string]any
	_ = json.Unmarshal([]byte(out), &result)
	if _, exists := result["shots"]; exists {
		t.Error("dataType=script 时不应返回分镜")
	}
	if _, exists := result["segments"]; !exists {
		t.Error("dataType=script 时应返回段落")
	}
}

func TestEvidenceTool_QueryMatchesContent(t *testing.T) {
	// 过滤逻辑在 WorkflowProjectState 里，stubState 不做过滤，
	// 因此这里必须用真实适配器验证关键词检索。
	state := NewWorkflowProjectState(avatarStateWith("开场：介绍主题", "主体：展开讲解"))
	reg, _ := newTestRegistry(t, PhaseHasScript, state, "proj-1")

	out, _, _ := reg.Invoke(context.Background(),
		ToolCall{ID: "c1", Name: "evidence", Arguments: map[string]any{"query": "开场", "dataType": "script"}},
		"t1", func(any) {})

	var result struct {
		Segments []ScriptSegment `json:"segments"`
	}
	_ = json.Unmarshal([]byte(out), &result)
	if len(result.Segments) != 1 || result.Segments[0].ID != "p1" {
		t.Errorf("关键词应命中 p1，got %+v", result.Segments)
	}
}

// ─────────────────────────────────────────────
// memory
// ─────────────────────────────────────────────

func TestMemoryTool_RememberAndList(t *testing.T) {
	reg, mem := newTestRegistry(t, PhaseInitial, fullState(), "proj-mem")

	_, _, _ = reg.Invoke(context.Background(),
		ToolCall{ID: "c1", Name: "memory", Arguments: map[string]any{
			"action": "remember", "kind": "preference", "content": "用户喜欢快节奏",
		}}, "t1", func(any) {})

	if mem.Count("proj-mem") != 1 {
		t.Fatalf("应写入 1 条记忆，实际 %d", mem.Count("proj-mem"))
	}

	out, _, _ := reg.Invoke(context.Background(),
		ToolCall{ID: "c2", Name: "memory", Arguments: map[string]any{"action": "list"}},
		"t1", func(any) {})
	if !strings.Contains(out, "快节奏") {
		t.Errorf("list 应返回已存记忆，got %s", out)
	}
}

func TestMemoryTool_ForgetByKind(t *testing.T) {
	reg, mem := newTestRegistry(t, PhaseInitial, fullState(), "proj-f")
	emit := func(any) {}

	_, _, _ = reg.Invoke(context.Background(), ToolCall{ID: "c1", Name: "memory", Arguments: map[string]any{
		"action": "remember", "kind": "preference", "content": "A",
	}}, "t1", emit)
	_, _, _ = reg.Invoke(context.Background(), ToolCall{ID: "c2", Name: "memory", Arguments: map[string]any{
		"action": "remember", "kind": "fact", "content": "B",
	}}, "t1", emit)

	_, _, _ = reg.Invoke(context.Background(), ToolCall{ID: "c3", Name: "memory", Arguments: map[string]any{
		"action": "forget", "kind": "preference",
	}}, "t1", emit)

	if mem.Count("proj-f") != 1 {
		t.Errorf("forget 后应剩 1 条，实际 %d", mem.Count("proj-f"))
	}
}

func TestMemoryTool_DeduplicatesSameContent(t *testing.T) {
	reg, mem := newTestRegistry(t, PhaseInitial, fullState(), "proj-d")
	emit := func(any) {}
	for i := 0; i < 3; i++ {
		_, _, _ = reg.Invoke(context.Background(), ToolCall{ID: "c", Name: "memory", Arguments: map[string]any{
			"action": "remember", "kind": "fact", "content": "重复内容",
		}}, "t1", emit)
	}
	if mem.Count("proj-d") != 1 {
		t.Errorf("相同内容不应重复记录，实际 %d 条", mem.Count("proj-d"))
	}
}

// ─────────────────────────────────────────────
// edit_*
// ─────────────────────────────────────────────

func TestEditScriptTool_GeneratesProposalWithValidIDs(t *testing.T) {
	reg, _ := newTestRegistry(t, PhaseHasScript, fullState(), "proj-e")

	out, blocked, issue := reg.Invoke(context.Background(),
		ToolCall{ID: "c1", Name: "edit_script", Arguments: map[string]any{
			"rowIds": []any{"p1"}, "changes": "把开场改短",
		}}, "t1", func(any) {})
	if blocked {
		t.Fatalf("不应被拦截: %v", issue)
	}

	var result struct {
		Proposal string   `json:"proposal"`
		RowIDs   []string `json:"rowIds"`
		Lines    []string `json:"lines"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("结果不是合法 JSON: %v", err)
	}
	if result.Proposal != "pending" {
		t.Errorf("提案状态应为 pending，got %q", result.Proposal)
	}
	if len(result.RowIDs) != 1 || result.RowIDs[0] != "p1" {
		t.Errorf("rowIds 错误: %v", result.RowIDs)
	}
	// 关键：不能声称已修改
	if !strings.Contains(out, "还没有修改任何数据") {
		t.Errorf("提案结果必须声明未写入数据，got %s", out)
	}
}

func TestEditScriptTool_RejectsUnknownIDs(t *testing.T) {
	reg, _ := newTestRegistry(t, PhaseHasScript, fullState(), "proj-e")

	out, _, _ := reg.Invoke(context.Background(),
		ToolCall{ID: "c1", Name: "edit_script", Arguments: map[string]any{
			"rowIds": []any{"p99"}, "changes": "改一下",
		}}, "t1", func(any) {})

	var result struct {
		Proposal string   `json:"proposal"`
		Unknown  []string `json:"unknown"`
	}
	_ = json.Unmarshal([]byte(out), &result)
	if result.Proposal != "failed" {
		t.Errorf("不存在的 ID 应失败，got %q", result.Proposal)
	}
	if len(result.Unknown) != 1 || result.Unknown[0] != "p99" {
		t.Errorf("应回传未知 ID，got %v", result.Unknown)
	}
}

func TestEditTool_RequiresChanges(t *testing.T) {
	reg, _ := newTestRegistry(t, PhaseHasScript, fullState(), "proj-e")
	out, _, _ := reg.Invoke(context.Background(),
		ToolCall{ID: "c1", Name: "edit_script", Arguments: map[string]any{
			"rowIds": []any{"p1"},
		}}, "t1", func(any) {})
	if !strings.Contains(out, "proposal") {
		t.Errorf("缺少 changes 时应有明确反馈，got %s", out)
	}
}

// ─────────────────────────────────────────────
// plan_project
// ─────────────────────────────────────────────

// fakeRunner 记录 Agent 提交的执行请求，用于验证 plan_project 真的触发了执行通道。
type fakeRunner struct {
	calls   []string // projectID + "|" + target
	err     error
}

func (f *fakeRunner) Run(_ context.Context, projectID, target string) error {
	f.calls = append(f.calls, projectID+"|"+target)
	return f.err
}

func TestPlanProjectTool_NoRunner_ReportsNotSubmitted(t *testing.T) {
	// 未注入执行通道时（runner == nil），必须明确告知无法执行，且不能推送 plan 事件误导前端
	reg, mem := newTestRegistry(t, PhaseHasScript, fullState(), "proj-plan")

	var emitted []any
	out, blocked, issue := reg.Invoke(context.Background(),
		ToolCall{ID: "c1", Name: "plan_project", Arguments: map[string]any{
			"target": "F05", "reason": "需要生成素材",
		}}, "t1", func(e any) { emitted = append(emitted, e) })
	if blocked {
		t.Fatalf("不应被拦截: %v", issue)
	}

	if mem.Count("proj-plan") != 1 {
		t.Errorf("计划应写入记忆，实际 %d 条", mem.Count("proj-plan"))
	}
	for _, e := range emitted {
		if m, ok := e.(map[string]any); ok && m["type"] == "plan" {
			t.Errorf("无执行通道时不应推送 plan 事件，实际 %+v", m)
		}
	}
	if !strings.Contains(out, `"submitted":false`) {
		t.Errorf("应明确标记未提交，got %s", out)
	}
}

func TestPlanProjectTool_WithRunner_SubmitsExecution(t *testing.T) {
	reg := NewToolRegistry()
	mem := newMemoryStore()
	runner := &fakeRunner{}
	RegisterAvatarTools(reg, PhaseHasScript, fullState(), mem, "proj-run", runner)

	var emitted []any
	out, blocked, issue := reg.Invoke(context.Background(),
		ToolCall{ID: "c1", Name: "plan_project", Arguments: map[string]any{
			"target": "f05", "reason": "需要生成素材",
		}}, "t1", func(e any) { emitted = append(emitted, e) })
	if blocked {
		t.Fatalf("不应被拦截: %v", issue)
	}

	// 节点名应被归一成 F05 后提交
	if len(runner.calls) != 1 || runner.calls[0] != "proj-run|F05" {
		t.Fatalf("应提交一次执行到 F05，实际 %+v", runner.calls)
	}
	if !strings.Contains(out, `"submitted":true`) {
		t.Errorf("应标记已提交，got %s", out)
	}

	var plan, nodeRunning bool
	for _, e := range emitted {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "plan" && m["target"] == "F05" {
			plan = true
		}
		if m["type"] == "node_status" && m["status"] == "running" {
			nodeRunning = true
		}
	}
	if !plan {
		t.Errorf("应推送 plan 事件，实际 %+v", emitted)
	}
	if !nodeRunning {
		t.Errorf("应推送 node_status running 事件，实际 %+v", emitted)
	}
}

func TestPlanProjectTool_RunnerRejects_ReportsError(t *testing.T) {
	reg := NewToolRegistry()
	runner := &fakeRunner{err: errors.New("数字人需要上传人物照片")}
	RegisterAvatarTools(reg, PhaseHasScript, fullState(), newMemoryStore(), "proj-rej", runner)

	out, blocked, issue := reg.Invoke(context.Background(),
		ToolCall{ID: "c1", Name: "plan_project", Arguments: map[string]any{
			"target": "F06", "reason": "生成数字人",
		}}, "t1", func(any) {})
	if blocked {
		t.Fatalf("不应被拦截: %v", issue)
	}
	if !strings.Contains(out, `"submitted":false`) || !strings.Contains(out, "上传人物照片") {
		t.Errorf("拒绝原因应回灌给模型，got %s", out)
	}
}

// ─────────────────────────────────────────────
// 阶段裁剪
// ─────────────────────────────────────────────

func TestRegisterAvatarTools_PhaseGating(t *testing.T) {
	initialReg, _ := newTestRegistry(t, PhaseInitial, fullState(), "p")
	defs := initialReg.GetDefinitions()
	names := map[string]bool{}
	for _, d := range defs {
		names[d["name"].(string)] = true
	}
	// 初始阶段没有 edit_* 工具
	for _, banned := range []string{"edit_script", "edit_voice", "edit_material", "edit_shot"} {
		if names[banned] {
			t.Errorf("PhaseInitial 不应注册 %s", banned)
		}
	}
	for _, required := range []string{"advice", "plan_project", "evidence", "memory"} {
		if !names[required] {
			t.Errorf("PhaseInitial 应注册 %s", required)
		}
	}

	shotReg, _ := newTestRegistry(t, PhaseHasShot, fullState(), "p")
	for _, d := range shotReg.GetDefinitions() {
		names[d["name"].(string)] = true
	}
	for _, required := range []string{"edit_script", "edit_voice", "edit_material", "edit_shot"} {
		if !names[required] {
			t.Errorf("PhaseHasShot 应注册 %s", required)
		}
	}
}
