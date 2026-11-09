// session_test.go — agent 包单元测试
package agent

import (
	"context"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────
// NormalizeHistory 测试（M-01：不突变源数据）
// ─────────────────────────────────────────────

func TestNormalizeHistory_DoesNotMutateSource(t *testing.T) {
	src := make([]ChatMessage, MaxHistoryMessages+5)
	for i := range src {
		src[i] = ChatMessage{Role: "user", Content: strings.Repeat("x", 10)}
	}
	original := make([]ChatMessage, len(src))
	copy(original, src)

	_ = NormalizeHistory(src)

	for i, m := range src {
		if m.Content != original[i].Content {
			t.Errorf("NormalizeHistory mutated src[%d]: got %q, want %q", i, m.Content, original[i].Content)
		}
	}
}

func TestNormalizeHistory_TruncatesLongToolResults(t *testing.T) {
	// 构造 FullToolResultTurns*2 + 6 条消息（user/tool 交替）
	// 使得 cutoff = len(out) - FullToolResultTurns*2 > 0
	// 注意：FullToolResultTurns = 2，所以需要至少 4 + 1 = 5 条使 cutoff>0
	count := FullToolResultTurns*2 + 6 // = 10
	msgs := make([]ChatMessage, count)
	for i := range msgs {
		role := "user"
		if i%2 == 1 {
			role = "tool"
		}
		msgs[i] = ChatMessage{Role: role, Content: strings.Repeat("a", 200)}
	}
	out := NormalizeHistory(msgs)

	// cutoff 计算与 NormalizeHistory 内部一致
	cutoff := len(out) - FullToolResultTurns*2
	if cutoff <= 0 {
		// 如果 cutoff<=0，压缩逻辑不会运行，测试直接跳过
		t.Skip("cutoff <= 0, truncation logic not triggered")
	}

	// cutoff 之前的 tool 条目应已被压缩：
	// "…[已压缩]" 的 UTF-8 编码：… 3B + [ 1B + 已 3B + 压 3B + 缩 3B + ] 1B = 14B
	// 最大长度 = 120 (内容) + 14 (后缀) = 134 字节
	const maxTool = 134
	for i := 0; i < cutoff; i++ {
		m := out[i]
		if m.Role == "tool" && len(m.Content) > maxTool {
			t.Errorf("early tool result at index %d not truncated, len=%d (want <= %d)", i, len(m.Content), maxTool)
		}
	}
}

func TestNormalizeHistory_EmptySlice(t *testing.T) {
	out := NormalizeHistory(nil)
	if out != nil {
		t.Errorf("expected nil for nil input, got %v", out)
	}
	out = NormalizeHistory([]ChatMessage{})
	if len(out) != 0 {
		t.Errorf("expected empty slice, got len=%d", len(out))
	}
}

// ─────────────────────────────────────────────
// classifyIntent 测试（H-01：关键词不过宽）
// ─────────────────────────────────────────────

func TestClassifyIntent(t *testing.T) {
	s := &SessionState{}
	cases := []struct {
		msg    string
		expect Intent
	}{
		{"帮我修改一下文案", IntentEditScript},
		{"这段话重写一下", IntentEditScript},
		{"配音语速太快了", IntentEditVoice},
		{"换一个音色", IntentEditVoice},
		{"素材换成动画类型", IntentEditMaterial},
		{"分镜布局调整", IntentEditShot},
		{"现在进度到哪了", IntentStatus},
		{"当前状态怎么样了", IntentStatus},
		{"撤销上一步", IntentUndo},
		{"建议用哪种风格", IntentAdvice},
		{"开始制作吧", IntentInput},
		{"你好", IntentInput},
	}
	for _, tc := range cases {
		got := s.classifyIntent(tc.msg)
		if got != tc.expect {
			t.Errorf("classifyIntent(%q) = %v, want %v", tc.msg, got, tc.expect)
		}
	}
}

// ─────────────────────────────────────────────
// determinePhase 测试（H-01：精确节点匹配）
// ─────────────────────────────────────────────

type mockProjectState struct {
	workflowState string
}

func (m *mockProjectState) Revision() int                                       { return 0 }
func (m *mockProjectState) GetPresets() string                                  { return "" }
func (m *mockProjectState) GetIndex() string                                    { return "" }
func (m *mockProjectState) GetWorkflowState() string                            { return m.workflowState }
func (m *mockProjectState) GetNodeStatuses() map[string]string                  { return nil }
func (m *mockProjectState) GetScriptSegments(_ string) ([]ScriptSegment, error) { return nil, nil }
func (m *mockProjectState) GetMaterialNeeds(_ string) ([]MaterialNeed, error)   { return nil, nil }
func (m *mockProjectState) GetShots(_ string) ([]Shot, error)                   { return nil, nil }
func (m *mockProjectState) Fingerprint() string                                 { return "" }

func TestDeterminePhase(t *testing.T) {
	cases := []struct {
		state  string
		expect ProjectPhase
	}{
		{"当前节点: F01", PhaseInitial},
		{"当前节点: F02\n状态: running", PhaseHasScript},
		{"当前节点: F03", PhaseHasScript},
		{"当前节点: F04", PhaseHasVoice},
		{"当前节点: F05", PhaseHasMaterial},
		{"当前节点: F09", PhaseHasMaterial},
		{"当前节点: F10", PhaseHasShot},
		{"当前节点: F12", PhaseHasShot},
		{"待开始", PhaseInitial},
		// 确保不被字母前的 f 误匹配（如 "before F05"）
		{"workflow: F05 已完成", PhaseHasMaterial},
	}
	for _, tc := range cases {
		ps := &mockProjectState{workflowState: tc.state}
		got := determinePhase(ps)
		if got != tc.expect {
			t.Errorf("determinePhase(%q) = %v, want %v", tc.state, got, tc.expect)
		}
	}
}

// ─────────────────────────────────────────────
// parseLLMResponse 测试（M-04：静默降级处理）
// ─────────────────────────────────────────────

func TestParseLLMResponse_ValidJSON(t *testing.T) {
	raw := `{"reply":"你好","tool_calls":[]}`
	resp := parseLLMResponse(raw)
	if resp.Reply != "你好" {
		t.Errorf("expected reply '你好', got %q", resp.Reply)
	}
}

func TestParseLLMResponse_PlainText(t *testing.T) {
	raw := "这是纯文字回复"
	resp := parseLLMResponse(raw)
	// 非 JSON 应该降级，reply 包含原始内容
	if !strings.Contains(resp.Reply, "这是纯文字回复") {
		t.Errorf("expected reply to contain original text, got %q", resp.Reply)
	}
}

func TestParseLLMResponse_PartialJSON(t *testing.T) {
	// 包含 reply 字段但结构不完整
	raw := `{"reply":"部分响应"}`
	resp := parseLLMResponse(raw)
	if resp.Reply != "部分响应" {
		t.Errorf("expected reply '部分响应', got %q", resp.Reply)
	}
}

func TestParseLLMResponse_LongFallback(t *testing.T) {
	// 超长非 JSON 应该被截断至 300 字节 + "[原始响应] " 前缀 + "…[已截断]" 后缀
	// "[原始响应] " = 8 字节, "…[已截断]" ≈ 14 字节（UTF-8 多字节字符）
	// 合理上界：300 + 8 + 15 = 323
	raw := strings.Repeat("x", 500)
	resp := parseLLMResponse(raw)
	const maxExpected = 330
	if len(resp.Reply) > maxExpected {
		t.Errorf("fallback reply not truncated enough, len=%d (want <= %d)", len(resp.Reply), maxExpected)
	}
	if !strings.HasPrefix(resp.Reply, "[原始响应]") {
		t.Errorf("fallback reply should start with [原始响应], got %q", resp.Reply[:20])
	}
}

// ─────────────────────────────────────────────
// ToolRegistry 三道闸测试（H-02）
// ─────────────────────────────────────────────

func TestToolRegistry_OnceToolBlocksDuplicate(t *testing.T) {
	reg := NewToolRegistry()
	callCount := 0
	reg.Register(&Tool{
		Name: "plan_project",
		Once: true,
		Execute: func(ctx context.Context, args map[string]any, emit EventEmitter) (string, error) {
			callCount++
			return "ok", nil
		},
	})

	emit := func(event any) {}
	ctx := context.Background()

	// 第一次调用应该成功
	_, blocked1, _ := reg.Invoke(ctx, ToolCall{ID: "1", Name: "plan_project"}, "turn-1", emit)
	if blocked1 {
		t.Fatal("first call should not be blocked")
	}
	if callCount != 1 {
		t.Errorf("expected callCount=1, got %d", callCount)
	}

	// 第二次调用应该被拦截
	_, blocked2, issue := reg.Invoke(ctx, ToolCall{ID: "2", Name: "plan_project"}, "turn-1", emit)
	if !blocked2 {
		t.Fatal("second call to once-tool should be blocked")
	}
	if issue == nil {
		t.Fatal("expected issue for blocked call")
	}
	// execute 不应再次被调用
	if callCount != 1 {
		t.Errorf("execute should not run again, callCount=%d", callCount)
	}
}

func TestToolRegistry_CheckFailureDoesNotCountCall(t *testing.T) {
	reg := NewToolRegistry()
	executeCalled := false
	reg.Register(&Tool{
		Name: "edit_script",
		Once: true,
		Check: func(args map[string]any) *Issue {
			i := NewIssue(IssueKindFormat, "edit_script", "bad_args", "参数缺失", true, ResolutionRepairContent)
			return &i
		},
		Execute: func(ctx context.Context, args map[string]any, emit EventEmitter) (string, error) {
			executeCalled = true
			return "ok", nil
		},
	})

	emit := func(event any) {}
	ctx := context.Background()

	// Check 失败，不应计入 onceDone
	_, blocked, _ := reg.Invoke(ctx, ToolCall{ID: "1", Name: "edit_script"}, "t1", emit)
	if !blocked {
		t.Fatal("should be blocked by Check")
	}
	if executeCalled {
		t.Fatal("execute should not have been called")
	}

	// 再次调用应该仍然走 Check（不被 once 拦截）
	_, blocked2, issue2 := reg.Invoke(ctx, ToolCall{ID: "2", Name: "edit_script"}, "t1", emit)
	if !blocked2 {
		t.Fatal("should still be blocked by Check")
	}
	if issue2 == nil {
		t.Fatal("expected issue")
	}
}

func TestToolRegistry_UnknownTool(t *testing.T) {
	reg := NewToolRegistry()
	emit := func(event any) {}
	_, blocked, issue := reg.Invoke(context.Background(), ToolCall{ID: "1", Name: "nonexistent"}, "t1", emit)
	if !blocked {
		t.Fatal("unknown tool should be blocked")
	}
	if issue == nil || issue.Code != "unknown_tool" {
		t.Errorf("expected unknown_tool issue, got %v", issue)
	}
}

// ─────────────────────────────────────────────
// extractCurrentNode 测试
// ─────────────────────────────────────────────

func TestExtractCurrentNode(t *testing.T) {
	cases := []struct {
		input  string
		expect int
	}{
		{"F01", 1},
		{"F12", 12},
		{"当前节点: F05\n状态: running", 5},
		{"", 0},
		{"无节点信息", 0},
		{"workflow: F05 完成, F07 运行中", 7},
		// 不应被字母前的 f 干扰
		{"before F04 after", 4},
	}
	for _, tc := range cases {
		got := extractCurrentNode(tc.input)
		if got != tc.expect {
			t.Errorf("extractCurrentNode(%q) = %d, want %d", tc.input, got, tc.expect)
		}
	}
}
