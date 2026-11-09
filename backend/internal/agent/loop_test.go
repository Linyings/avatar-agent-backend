// loop_test.go — Agent 工具循环集成测试
//
// 验证完整链路：LLM 返回 tool_calls → 三道闸校验 → 工具执行 →
// 结果回灌模型 → 再次调用 → 无工具调用时结束。
package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────
// 测试替身
// ─────────────────────────────────────────────

// fakeLLM 按预设脚本依次返回响应，并记录每次收到的请求
type fakeLLM struct {
	responses []string
	calls     []LLMRequest
	onCall    func(n int) // 第 n 次调用（从 1 开始）后的钩子，用于模拟中途中止
}

func (f *fakeLLM) Complete(ctx context.Context, req LLMRequest) (string, error) {
	f.calls = append(f.calls, req)
	if f.onCall != nil {
		f.onCall(len(f.calls))
	}
	if len(f.calls) > len(f.responses) {
		return `{"reply":"（脚本已用尽）","tool_calls":[]}`, nil
	}
	return f.responses[len(f.calls)-1], nil
}

// stubState 可控的项目状态
type stubState struct {
	segments    []ScriptSegment
	needs       []MaterialNeed
	shots       []Shot
	workflow    string
	revision    int
	presets     string
	index       string
	fingerprint string
}

func (s *stubState) Revision() int                                       { return s.revision }
func (s *stubState) GetPresets() string                                  { return s.presets }
func (s *stubState) GetIndex() string                                    { return s.index }
func (s *stubState) GetWorkflowState() string                            { return s.workflow }
func (s *stubState) GetNodeStatuses() map[string]string                  { return nil }
func (s *stubState) Fingerprint() string                                 { return s.fingerprint }
func (s *stubState) GetScriptSegments(_ string) ([]ScriptSegment, error) { return s.segments, nil }
func (s *stubState) GetMaterialNeeds(_ string) ([]MaterialNeed, error)   { return s.needs, nil }
func (s *stubState) GetShots(_ string) ([]Shot, error)                   { return s.shots, nil }

// ─────────────────────────────────────────────
// 循环测试
// ─────────────────────────────────────────────

func TestAgentLoop_ExecutesToolCallsThenStops(t *testing.T) {
	llm := &fakeLLM{responses: []string{
		// 第一轮：要求检索证据
		`{"reply":"我先看一下项目内容","tool_calls":[{"id":"c1","name":"evidence","arguments":{"query":"","dataType":"all"}}]}`,
		// 第二轮：不再调用工具，收尾
		`{"reply":"已了解项目内容，可以开始制作","tool_calls":[]}`,
	}}

	session := NewSession(SessionConfig{ProjectID: "p-test", LLM: llm})
	session.SetEmitter(func(any) {})

	state := &stubState{
		segments: []ScriptSegment{{ID: "p1", Content: "开场内容"}},
		workflow: "当前节点: F03",
	}

	out, err := session.Chat(context.Background(), ChatInput{
		ProjectID: "p-test", Message: "帮我看下现在的文案", TurnID: "t1", RequestID: "r1",
	}, state)
	if err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	if !strings.Contains(out.Reply, "可以开始制作") {
		t.Errorf("最终回复应来自第二轮，got %q", out.Reply)
	}
	// 两轮 LLM 调用各有一次，说明工具结果确实被回灌
	if len(llm.calls) != 2 {
		t.Fatalf("期望 2 次 LLM 调用，实际 %d 次", len(llm.calls))
	}
	// 第二轮的历史里必须出现 evidence 的工具结果
	found := false
	for _, m := range llm.calls[1].History {
		if m.Role == "tool" && m.ToolName == "evidence" {
			found = true
			break
		}
	}
	if !found {
		t.Error("第二轮请求的历史中缺少 evidence 工具结果")
	}
	// 工具 schema 应传给模型，否则模型无从选择工具
	if len(llm.calls[0].Tools) == 0 {
		t.Error("首次请求未携带工具定义")
	}
}

func TestAgentLoop_BlockedToolIsReportedBack(t *testing.T) {
	// plan_project 是 once 工具，重复调用应被闸门拦截并把原因回灌
	llm := &fakeLLM{responses: []string{
		`{"reply":"第一次计划","tool_calls":[{"id":"c1","name":"plan_project","arguments":{"target":"F05","reason":"需要素材"}}]}`,
		`{"reply":"重复计划","tool_calls":[{"id":"c2","name":"plan_project","arguments":{"target":"F05","reason":"再来一次"}}]}`,
		`{"reply":"好的，不再重复","tool_calls":[]}`,
	}}

	session := NewSession(SessionConfig{ProjectID: "p-block", LLM: llm})
	session.SetEmitter(func(any) {})

	_, err := session.Chat(context.Background(), ChatInput{
		ProjectID: "p-block", Message: "开始制作", TurnID: "t1", RequestID: "r1",
	}, &stubState{workflow: "当前节点: F03"})
	if err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}

	// 第三轮的历史里应包含"被拦截"的说明
	var blockedSeen bool
	for _, m := range llm.calls[2].History {
		if m.Role == "tool" && strings.Contains(m.Content, "被拦截") {
			blockedSeen = true
			break
		}
	}
	if !blockedSeen {
		t.Error("重复调用 once 工具后，历史中应记录拦截原因")
	}
}

func TestAgentLoop_NoLLMClientFallsBackToStub(t *testing.T) {
	// 未注入 LLM 时不应 panic，也不应发起网络请求
	session := NewSession(SessionConfig{ProjectID: "p-nollm"})
	session.SetEmitter(func(any) {})

	out, err := session.Chat(context.Background(), ChatInput{
		ProjectID: "p-nollm", Message: "随便聊聊", TurnID: "t1", RequestID: "r1",
	}, &stubState{workflow: "当前节点: F01"})
	if err != nil {
		t.Fatalf("无 LLM 时不应报错: %v", err)
	}
	if out == nil {
		t.Fatal("应返回占位输出")
	}
}

func TestAgentLoop_AbortStopsLoop(t *testing.T) {
	// TurnAbort 在每轮对话开始时重建，因此在 Chat 之前调用 Abort 无效；
	// 真实的「停止生成」发生在轮次进行中，这里在第一次模型调用后触发中止。
	session := NewSession(SessionConfig{ProjectID: "p-abort"})
	session.SetEmitter(func(any) {})

	llm := &fakeLLM{
		responses: []string{
			`{"reply":"继续","tool_calls":[{"id":"c1","name":"advice","arguments":{"reply":"x"}}]}`,
		},
		onCall: func(int) { session.Abort() },
	}
	session.config.LLM = llm

	out, err := session.Chat(context.Background(), ChatInput{
		ProjectID: "p-abort", Message: "开始", TurnID: "t1", RequestID: "r1",
	}, &stubState{workflow: "当前节点: F01"})
	if err != nil {
		t.Fatalf("中止不应返回错误: %v", err)
	}
	if !strings.Contains(out.Reply, "中止") {
		t.Errorf("期望中止提示，got %q", out.Reply)
	}
	// 中止后不应继续下一轮模型调用
	if len(llm.calls) != 1 {
		t.Errorf("中止后应停止循环，实际调用 %d 次", len(llm.calls))
	}
}

// ─────────────────────────────────────────────
// 响应归一化测试
// ─────────────────────────────────────────────

func TestLLMResponse_ToolCallParsing(t *testing.T) {
	raw := `{"reply":"处理中","tool_calls":[{"id":"c1","name":"edit_script","arguments":{"rowIds":["p1"],"changes":"改短"}}]}`
	resp := parseLLMResponse(raw)
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("期望 1 个工具调用，got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "edit_script" {
		t.Errorf("工具名错误: %s", resp.ToolCalls[0].Name)
	}
	if resp.ToolCalls[0].Arguments["changes"] != "改短" {
		t.Errorf("参数未正确解析: %v", resp.ToolCalls[0].Arguments)
	}
}

// ─────────────────────────────────────────────
// 工作流适配器测试
// ─────────────────────────────────────────────

func TestWorkflowProjectState_IndexAndWorkflow(t *testing.T) {
	st := &stubState{
		segments: []ScriptSegment{{ID: "p1"}, {ID: "p2"}},
		workflow: "当前节点: F05",
	}
	if got := determinePhase(st); got != PhaseHasMaterial {
		t.Errorf("F05 应判定为 PhaseHasMaterial, got %v", got)
	}
}

func TestWorkflowProjectState_JSONShape(t *testing.T) {
	// 段落的 segmentId 是字符串，前端与工具都依赖这个类型
	need := MaterialNeed{ID: "n1", SegmentID: "p1", Type: "avatar", Duration: "8 秒"}
	raw, err := json.Marshal(need)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"segmentId":"p1"`) {
		t.Errorf("segmentId 应为字符串, got %s", raw)
	}
}
