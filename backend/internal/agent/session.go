// session.go — AgentSession 核心会话管理
// 对应 NarratoAI-PC movie-vision.service.ts agentChat + pi-agent.service.ts
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────
// AgentSession — 单次 Agent 对话轮次
// ─────────────────────────────────────────────

// SessionConfig 会话配置
type SessionConfig struct {
	ProjectID   string
	UserID      string
	LLMEndpoint string // LLM API endpoint（保留：部分部署需按项目覆写）
	LLMModel    string // model name
	LLMAPIKey   string
	MaxTurns    int // 单次会话最大轮次（默认 40）
	// LLM 真实模型客户端，由 server 层注入；为 nil 时退化为本地桩，
	// 保证单元测试与离线开发不需要真实凭据。
	LLM LLMClient
	// MessageStore 对话历史持久化（可选）
	MessageStore MessageStore
	// MemoryBackend 记忆持久化后端（可选，M-03 修复）
	MemoryBackend MemoryPersistence
	// Runner 工作流执行通道；为 nil 时 Agent 只能分析，无法真正执行节点
	Runner   ProjectRunner
	Packages PackageHost
}

// SessionState 会话状态
type SessionState struct {
	mu       sync.Mutex
	turnMu   sync.Mutex // A project has one active turn; other requests receive conflict.
	turnID   string
	abort    *TurnAbort
	history  []ChatMessage
	memory   []Memory
	tools    *ToolRegistry
	phase    ProjectPhase
	activity Activity
	config   SessionConfig
	emitter  func(event any) // SSE 事件推送函数
	store    MessageStore    // 消息持久化，可为 nil
	mem      *memoryStore    // 长期记忆（工具与提示词共用）
}

// memoryStore 返回会话的记忆仓库。
// 不在这里加锁：调用方（doChat / BuildContext）可能已持有 s.mu，
// 而 Go 的 Mutex 不可重入。mem 在 NewSession 中创建后不再替换，
// 因此读取字段本身是安全的。
func (s *SessionState) memoryStore() *memoryStore {
	if s.mem == nil {
		s.mem = newMemoryStore()
	}
	return s.mem
}

// NewSession 创建新会话
func NewSession(config SessionConfig) *SessionState {
	var mem *memoryStore
	if config.MemoryBackend != nil {
		mem = newMemoryStoreWithBackend(config.MemoryBackend)
	} else {
		mem = newMemoryStore()
	}
	return &SessionState{
		turnID:  uuid.New().String(),
		abort:   NewTurnAbort(),
		history: make([]ChatMessage, 0, 24),
		memory:  make([]Memory, 0, 40),
		mem:     mem,
		tools:   NewToolRegistry(),
		phase:   PhaseInitial,
		config:  config,
		store:   config.MessageStore,
	}
}

// ─────────────────────────────────────────────
// 分层上下文构建
// 对应 NarratoAI-PC compactProject + Pi context injection
// ─────────────────────────────────────────────

// LayeredContext 分层上下文
type LayeredContext struct {
	// Layer 0: 规则
	SystemRules string `json:"systemRules"`
	// Layer 1: 预设（技能/风格/写稿规则）
	Presets string `json:"presets"`
	// Layer 2: 索引（项目数量概要，原文不预载）
	ProjectIndex string `json:"projectIndex"`
	// Layer 3: 记忆（preference/fact/decision/checkpoint）
	Memory string `json:"memory"`
	// 实时态：当前工作流状态
	WorkflowState string `json:"workflowState"`
	// 实时态：当前打开的提案
	OpenProposal *Proposal `json:"openProposal,omitempty"`
}

// AVATAR_AGENT_DECISION_GUIDANCE 决策规则（对应 NarratoAI-PC MOVIE_AGENT_DECISION_GUIDANCE）
const AVATAR_AGENT_DECISION_GUIDANCE = `你是 avatarSpeak 数字人口播平台的制作 Agent。
工作方式：理解用户需求 → 拆解任务 → 调用工具执行 → 回答用户。
重要规则：
- 工具返回的是真实执行结果，未调用的工具不能说已执行
- plan_project 由服务端执行，target 是终点；从某步重做要同时传 rerunFrom 起点和 target 终点。例如从第4步分段配音起重做全部后续：rerunFrom="F04", target="F12"。工具名不是节点编号。
- 已完成项目仍可修改所有节点。先 avatar_inspect 读取真实内容、版本、ID；F01 需求用 avatar_configure；F01.4 视频理解、F02 全文、F03 分段、F05–F08 素材需求、F09 字幕、F10 分镜、F11–F12 画面设置用 avatar_edit_node；F04 原声口播修改文案或参考音色后重做音画；仅历史 TTS 模式用 edit_voice。已有 edit_script/edit_material/edit_shot 也可用。需要换图/素材时使用项目已上传的素材 ID。
- 只重做、不改内容用 avatar_rerun（from 起点、target 终点）或 plan_project 的 rerunFrom；结果 pending 表示方案等待用户确认，尚未启动。不要把普通执行到某节点当作重做。
- 工具参数校验失败可以纠正后在同一轮重试；提交成功后的执行失败或订单未知不能自动重复付费。
- 执行重试前必须 avatar_inspect 读取当前 error_step_id、失败步骤 error、retry.last_error/strategy/automatic_retries、attempts 错误记录和已有成功结果，结合具体错误说明本次修复目标。后台最多自动修复重试3次，每一次都必须携带最新错误原因及可用的失败输出，不允许前三次原样盲重试。订单未知、鉴权或本地环境故障不自动重复生成。
- retry.exhausted=true 表示三次自动修复仍失败，必须停止并等待用户接入。用户只要求排查时，只读取证据、解释具体原因和提供处理选项，不调用 retry、resume 或生成工具。让用户决定继续重试、跳过、调整步骤，或手动修改、让 AI 优化提示词。用户明确要求重试时，仅调用一次 avatar_control retry，由后台携带最新错误修复失败部分；不能连续调用工具绕过上限，不能偷偷新建运行以重置次数。
- 用户选择调整步骤或提示词时，先读取当前内容，用 avatar_edit_node 的 needs/prompt 等精确字段形成修改提案；也可引导用户在视频节点直接编辑或让 AI 改写提示词。用户选择跳过素材时，用 avatar_edit_node 的 skip_need_ids 提出跳过方案，检查剩余素材是否完整覆盖该段音频，并重新组装受影响镜头；没有替代画面或仍有依赖时说明阻碍，不能把失败标为成功，也不能静默删除用户要求。提案经用户确认才执行。
- 用户要求继续时，先读取当前运行状态：running 表示后台仍在执行，不重试；awaiting_confirmation 表示正常等待用户选择，不是模型失败，不调用 retry。原声模式会连续生成各段声音与人物视频，通过对齐检查后继续，不需要独立配音确认；仅历史 TTS 模式在全部段落完成后选择试听确认或「直接继续制作」。不要声称必须逐段试听，或不试听就会导致字幕、口型错位。文案仍需查看并确认。
- 如果 avatar_inspect 返回 testing_mode=true，且运行状态是 failed、interrupted 或 submission_unknown，用户明确要求继续或重试时，可以调用 avatar_control 的 retry（或继续执行目标节点），不要再要求用户核对供应商订单。后端会接续已知任务或保留历史后重新执行。每次用户指令只发起一次恢复；若再次失败，说明具体错误并等待下一次指令，不能循环重试。
- "已开始"不等于"已完成"，不要把提交计划当成执行完成
- 修改操作必须先生成提案，用户确认后才写入数据
- 重做节点需要确认修改方案；测试模式下，用户明确说“继续”或“重试”已授权一次失败恢复，不额外要求费用确认。不要循环自动重试。
- 不绕过 Workflow 节点前置条件（如视频原声未测时和对齐不触发 F05）
- advice 意图只提供建议文字，不调用任何付费接口

意图处理（重要）：
- 用户的每条消息都可能是制作意图。能推断出足够信息就直接调用工具执行，不要为了确认而确认。
- 只有当关键信息缺失、且不同取值会显著改变产出时，才提问；一次最多问 1-2 个最关键的问题，其余按合理默认值推进并说明你的假设。
- 所有确认都在当前对话里完成：不要引导用户去别的页面，也不要输出"请前往制作前沟通/确认页"这类指令。
- 你可以从上下文的「项目预设」「项目索引」「工作流状态」中读取当前项目的风格、画幅、配音、段落数量等信息。这些数据已经在上下文里，不要声称自己拿不到项目数据。
- 上下文缺少完整内容时先用 avatar_inspect 或 evidence 读取，不要让用户重新粘贴已有节点内容。
- input.reference_links 是用户添加的参考网页地址。当前工具没有抓取网页正文的能力，不能声称已经阅读链接内容；需要网页事实时请用户提供关键文字或截图。`

// BuildContext 构建分层上下文
func (s *SessionState) BuildContext(projectState ProjectState) LayeredContext {
	// Layer 0: 规则
	rules := AVATAR_AGENT_DECISION_GUIDANCE

	// Layer 1: 预设
	presets := projectState.GetPresets()

	// Layer 2: 索引（只有数量和概要，原文不预载）
	index := projectState.GetIndex()

	// Layer 3: 记忆
	// 优先用 memoryStore（工具写入的长期记忆），它跨会话保留；
	// s.memory 是早期遗留的内存切片，仅在没有 store 时兜底。
	memStore := s.memoryStore()
	memoryStr := memStore.Render(s.config.ProjectID)
	if memoryStr != "" {
		memoryStr = fmt.Sprintf("用户记忆（%d 条）:\n%s", memStore.Count(s.config.ProjectID), memoryStr)
	}

	// 实时态
	workflow := projectState.GetWorkflowState()
	openProposal := globalProposals.GetIfValid(s.config.ProjectID, projectState.Revision())

	return LayeredContext{
		SystemRules:   rules,
		Presets:       presets,
		ProjectIndex:  index,
		Memory:        memoryStr,
		WorkflowState: workflow,
		OpenProposal:  openProposal,
	}
}

// ─────────────────────────────────────────────
// 项目状态接口（供 Session 读取持久化数据）
// ─────────────────────────────────────────────

// ProjectState 项目状态只读接口
// 对应 NarratoAI-PC compactProject 的数据来源
type ProjectState interface {
	// Revision 返回当前项目版本
	Revision() int
	// GetPresets 返回预设内容（技能/风格/写稿规则）
	GetPresets() string
	// GetIndex 返回项目索引（只有数量和概要，不预载原文）
	GetIndex() string
	// GetWorkflowState 返回当前工作流状态
	GetWorkflowState() string
	// GetNodeStatuses 返回 F01-F12 各节点的执行状态，供工作台节点卡片显示
	GetNodeStatuses() map[string]string
	// GetScriptSegments 返回脚本段落（供 evidence 工具检索）
	GetScriptSegments(query string) ([]ScriptSegment, error)
	// GetMaterialNeeds 返回素材需求
	GetMaterialNeeds(query string) ([]MaterialNeed, error)
	// GetShots 返回分镜列表
	GetShots(query string) ([]Shot, error)
	// Fingerprint 返回段落内容指纹
	Fingerprint() string
}

// ScriptSegment 脚本段落
// ID 使用字符串，与 avatar.WorkflowState.Segments 的 "p1"/"p2" 格式保持一致，
// 避免 int 转换在证据检索与提案回写时丢失引用。
type ScriptSegment struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Tag     string `json:"tag,omitempty"`
}

// MaterialNeed 素材需求
type MaterialNeed struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Duration  string `json:"duration"`
	Content   string `json:"content"`
	SegmentID string `json:"segmentId"`
}

// Shot 分镜
type Shot struct {
	ID        string `json:"id"`
	Layout    string `json:"layout"`
	Duration  string `json:"duration"`
	SegmentID string `json:"segmentId"`
	Source    string `json:"source,omitempty"`
}

// ─────────────────────────────────────────────
// 对话执行
// ─────────────────────────────────────────────

// Chat 执行一次对话轮次
// 返回 ChatOutput 或 error
// 使用 inflight 去重：同一 requestId 只执行一次
func (s *SessionState) Chat(
	ctx context.Context, input ChatInput, projectState ProjectState,
) (*ChatOutput, error) {
	// 幂等去重
	inflightKey := fmt.Sprintf("%s:%s", s.config.ProjectID, input.RequestID)
	entry, isOwner := globalInflight.Acquire(inflightKey)
	if !isOwner {
		// 已有执行，等待结果
		select {
		case <-entry.done:
			return entry.out, entry.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// defer 保证 panic 时也能 Release，避免永久阻塞
	defer func() {
		if r := recover(); r != nil {
			globalInflight.Release(inflightKey, nil, fmt.Errorf("agent panic: %v", r))
			panic(r)
		}
	}()
	if !s.turnMu.TryLock() {
		out := &ChatOutput{Reply: "当前项目正在回复，请等待完成或先停止当前回复。", Status: "conflict", TurnID: input.TurnID}
		globalInflight.Release(inflightKey, out, nil)
		return out, nil
	}
	defer s.turnMu.Unlock()
	if err := ctx.Err(); err != nil {
		globalInflight.Release(inflightKey, nil, err)
		return nil, err
	}
	out, err := s.doChat(ctx, input, projectState)
	globalInflight.Release(inflightKey, out, err)
	return out, err
}

// doChat 实际执行对话
func (s *SessionState) doChat(
	ctx context.Context, input ChatInput, projectState ProjectState,
) (out *ChatOutput, err error) {
	s.mu.Lock()
	s.turnID = input.TurnID
	if s.abort != nil {
		s.abort.Abort()
	}
	s.abort = NewTurnAbortWithContext(ctx)
	ctx = s.abort.Context()
	defer s.abort.Abort()
	s.tools = NewToolRegistry()

	// L-02：添加关键日志
	log.Printf("[agent.doChat] projectID=%s userID=%s turnID=%s phase=%v",
		s.config.ProjectID, s.config.UserID, s.turnID, s.phase)

	// projectState 为 nil 时使用空实现，避免 panic
	if projectState == nil {
		projectState = &emptyProjectState{}
	}

	// 根据项目状态更新可用工具
	s.phase = determinePhase(projectState)
	RegisterAvatarTools(s.tools, s.phase, projectState, s.memoryStore(), s.config.ProjectID, s.config.Runner)
	RegisterSkillTools(s.tools)
	RegisterPackageTools(s.tools, s.config.Packages, s.config.UserID, s.config.ProjectID, s.turnID)

	// 添加用户消息到历史
	userMsg := ChatMessage{
		ID:        uuid.New().String(),
		Role:      "user",
		Content:   input.Message,
		CreatedAt: time.Now(),
	}
	// 空消息（纯 accept 请求）不入历史，否则回填时会多出空对话框
	persistUser := input.Message != ""
	if persistUser {
		s.history = append(s.history, userMsg)
	}
	// 归一化历史
	s.history = NormalizeHistory(s.history)
	s.mu.Unlock()
	s.setActivity("running", "thinking", "", "")
	defer func() {
		status, detail := "idle", "本轮对话已结束"
		if ctx.Err() != nil {
			status = "stopped"
			detail = "本轮回复已停止"
		} else if err != nil {
			status = "error"
			detail = err.Error()
		} else if out != nil && (out.Status == "error" || out.Status == "needs_action") {
			status = out.Status
			detail = out.Reply
		}
		s.setActivity(status, "", "", detail)
	}()

	if persistUser {
		s.persist(ctx, userMsg)
	}

	// 快速通道：直接接受提案
	if input.Accept {
		return s.acceptProposal(input, projectState)
	}

	// 快速通道：意图判断。
	//
	// 只有「纯状态查询」和「未实现的撤销」走旁路，其余一律进完整 Agent 循环。
	// 原因：旁路调用不带工具（callLLM 的 tools 传 nil），一旦关键词误判，
	// 模型就完全看不见 plan_project/edit_* 等工具，会回答「我没有这些工具」并
	// 拒绝执行。用户说「给我点建议然后直接开始做」这种混合意图会被 "建议" 命中，
	// 代价是彻底丧失执行能力，不值得为省一次工具调用的 token 冒这个风险。
	intent := s.classifyIntent(input.Message)
	switch intent {
	case IntentStatus:
		if !executionRequested(input.Message) {
			return s.handleStatus(input, projectState)
		}
		return s.runAgentLoop(ctx, input, projectState, intent)
	case IntentUndo:
		return s.handleUndo(input, projectState)
	default:
		// 完整 Agent 轮次：调用 LLM + 工具循环（advice / edit / 执行都走这里）
		return s.runAgentLoop(ctx, input, projectState, intent)
	}
}

// ─────────────────────────────────────────────
// 快速通道处理
// ─────────────────────────────────────────────

// acceptProposal 直接接受上一个提案（C-04 修复：不谎报成功）
func (s *SessionState) acceptProposal(input ChatInput, projectState ProjectState) (*ChatOutput, error) {
	if s.config.Packages != nil {
		return &ChatOutput{Reply: "请在右侧的修改方案卡片中核对具体内容，点击「应用修改并重新制作」。", Status: "ok", TurnID: input.TurnID}, nil
	}
	p := globalProposals.GetIfValid(s.config.ProjectID, projectState.Revision())
	if p == nil {
		return &ChatOutput{
			Reply:  "没有可用的提案，或提案已过期（项目内容已变化）。",
			Status: "error",
			TurnID: input.TurnID,
		}, nil
	}
	// C-04 修复：写入通道尚未实现，明确告知而非谎报已应用
	// TODO: 接入真实 projectState 写入后，在写入成功后再调用 MarkApplied
	return &ChatOutput{
		Reply:  "提案已生成，但写入通道尚未接通。后端集成完成后即可自动应用。",
		Status: "ok",
		TurnID: input.TurnID,
	}, nil
}

// handleStatus 处理状态查询
func (s *SessionState) handleStatus(input ChatInput, projectState ProjectState) (*ChatOutput, error) {
	state := projectState.GetWorkflowState()
	reply := fmt.Sprintf("当前制作状态：\n%s", state)
	return &ChatOutput{Reply: reply, Status: "ok", TurnID: input.TurnID}, nil
}

// handleUndo 处理撤销
func (s *SessionState) handleUndo(input ChatInput, projectState ProjectState) (*ChatOutput, error) {
	// M-05 修复：撤销逻辑尚未实现，明确告知用户而非谎报成功
	return &ChatOutput{
		Reply:  "撤销功能暂未实现。如需回退修改，请在产物卡片中点击「让 Agent 修改」重新生成。",
		Status: "ok",
		TurnID: input.TurnID,
	}, nil
}

// ─────────────────────────────────────────────
// Agent 工具循环（感知→规划→执行→反馈）
// ─────────────────────────────────────────────

// runAgentLoop 完整 Agent 循环
func (s *SessionState) runAgentLoop(
	ctx context.Context, input ChatInput, projectState ProjectState, intent Intent,
) (*ChatOutput, error) {
	// 构建上下文
	layered := s.BuildContext(projectState)

	// 构建系统 prompt
	systemPrompt := s.buildSystemPrompt(layered, intent)
	if s.config.Packages != nil {
		systemPrompt += "\n你可以调用版本化 avatar package。先 avatar_inspect 读取真实输入、workflow_plan 和可用风格音色，用 avatar_configure 保存需求。用户明确说帮我制作、开始、继续等就是执行要求，信息足够时必须实际调用 plan_project，完整视频 target=F12；不能只说先保存或马上开始就结束。用户已提供完整口播稿时保存到 script，不要把制作指令当稿件。尊重项目已保存的 use_avatar、use_animation、style 和 direction；首页新项目默认人物口播与动效融合，两项均为 true，人物由参考图生成。用户选定的动效风格和融合要求必须保留，不能因为本轮只提口播或没有再次提动效就关闭 use_animation 或覆盖风格。只有用户明确要求纯人物、不要动效时才关闭动效；同理不能无故关闭人物。历史项目仍按原配置执行，不自动改成融合项目。项目 input.speech_mode=native-video 时：F04 用 Seedance 2.5 一次生成声音与人物，再从视频原声测时和对齐；F06 仅复用人物画面，不调用可灵，不调用 TTS，不要求音色 ID 或配音审核。可选 voice_reference 是已上传 audio 的素材 ID，缺省由模型配声；修改声音必须连同人物一起重做。历史项目按已存流程恢复。新版按 workflow_plan.stages 的11个阶段执行：需求整理→需求分析F01.2→参考分析F01.4→导演稿F01.6→文案F02/F03→原声口播与时间校准F04→镜头与专用动效F05–F10→逐镜审阅F11→正式渲染F11.1→成片审阅F11.2→交付F12。参考分析处理图片、视频、文案、可访问网页；没有参考素材自动跳过，无法读取的资料明确标记。导演稿会随文案、实际配音和素材校准。只规划或咨询时不启动制作。缺少必要资料或配置时明确说尚未启动并指出缺什么。submitted 仅表示提交，说明后台会继续执行、到文案、镜头和成片审核时等待用户确认；历史 TTS 模式还保留配音审核。所有确认、编辑提案批准由用户在界面操作，不得替用户确认。编辑工具需给出精确的新字段，不能只提供修改意图。"
	}

	// 准备历史
	history := s.getNormalizedHistory()

	// LLM 调用 + 工具循环
	maxIter := 12 // 最大迭代次数
	var lastReply string
	outputStatus := "ok"
	wantsExecution := s.config.Packages != nil && executionRequested(input.Message)
	resolved := false
	continuations := 0
	lastToolError := ""
	finished := false

	for i := 0; i < maxIter; i++ {
		if ctx.Err() != nil {
			return &ChatOutput{Reply: "已中止。", Status: "ok", TurnID: input.TurnID}, nil
		}

		// 调用 LLM
		s.setActivity("running", "thinking", "", "正在理解需求并规划下一步")
		response, err := s.callLLMWithTools(ctx, systemPrompt, history, s.tools.GetDefinitions())
		if ctx.Err() != nil {
			return &ChatOutput{Reply: "已中止。", Status: "ok", TurnID: input.TurnID}, nil
		}
		if err != nil {
			return &ChatOutput{Reply: "Agent 执行出错。", Status: "error", TurnID: input.TurnID}, err
		}

		// 解析 LLM 响应
		parsed := parseLLMResponse(response)
		lastReply = parsed.Reply
		if len(parsed.ToolCalls) == 0 && wantsExecution && !resolved {
			if lastToolError != "" {
				lastReply = "本次操作未能启动或继续：" + lastToolError + "。请先处理此问题，再重试。"
				outputStatus = "needs_action"
			} else if !needsClarification(lastReply) {
				if continuations < 2 {
					continuations++
					history = append(history, ChatMessage{Role: "assistant", Content: lastReply}, ChatMessage{Role: "system", Content: "用户要求实际执行，但你尚未提交制作任务或可确认的方案。继续使用工具完成本轮要求；只保存需求不等于启动。若已有运行或需等待审核，先读取真实状态并说明；若缺必要信息，明确说明尚未启动及缺少什么。不要只承诺下一步，也不要重复提交已接受的任务。"})
					continue
				}
				lastReply = "制作任务尚未启动：Agent 本轮没有提交有效的执行请求。可以点击「让 Agent 开始制作」继续处理；已上传素材和已保存的需求会保留。"
				outputStatus = "needs_action"
			} else {
				lastReply = "制作任务尚未启动或继续，需要先处理以下事项：\n" + lastReply
				outputStatus = "needs_action"
			}
		}

		// 逐轮推送回复文本，让工作台对话窗口边执行边显示。
		// 每次 LLM 调用是一次完整返回（非 token 流），因此按轮次推送整段文本；
		// 前端以 turnId 归并同一轮的多个 delta。
		if text := strings.TrimSpace(lastReply); text != "" && len(parsed.ToolCalls) == 0 {
			s.emit(map[string]any{
				"type":   "thinking",
				"turnId": s.turnID,
				"delta":  lastReply,
			})
		}

		// 无工具调用 → 结束
		if len(parsed.ToolCalls) == 0 {
			finished = true
			break
		}

		// OpenAI-compatible tool results must follow the matching assistant calls.
		history = append(history, ChatMessage{Role: "assistant", Content: parsed.Reply, ToolCalls: parsed.ToolCalls})
		// 执行工具调用，注入 turnID（H-04 修复）
		for _, tc := range parsed.ToolCalls {
			if ctx.Err() != nil {
				return &ChatOutput{Reply: "已中止。", Status: "ok", TurnID: input.TurnID}, nil
			}
			result, blocked, issue := s.tools.Invoke(ctx, tc, s.turnID, s.emit)
			if ctx.Err() != nil {
				return &ChatOutput{Reply: "已中止。", Status: "ok", TurnID: input.TurnID}, nil
			}
			if blocked {
				lastToolError = issue.Message
				s.emit(ToolEvent{Type: "tool", TurnID: s.turnID, CallID: tc.ID, Tool: tc.Name, Status: "error", Detail: issue.Message})
				// 被闸门拦截，将原因回灌模型
				history = append(history, ChatMessage{
					Role:     "tool",
					Content:  fmt.Sprintf("工具 %s 被拦截：%s", tc.Name, issue.Message),
					ToolName: tc.Name,
					ToolID:   tc.ID,
				})
				continue
			}
			if message := toolError(result); message != "" {
				lastToolError = message
			} else {
				lastToolError = ""
				if executionResolved(result) {
					resolved = true
				}
			}
			// 工具结果加入历史
			history = append(history, ChatMessage{
				Role:     "tool",
				Content:  result,
				ToolName: tc.Name,
				ToolID:   tc.ID,
			})

			// 如果工具生成了提案，提取并存储
			if s.config.Packages == nil && (tc.Name == "edit_script" || tc.Name == "edit_voice" || tc.Name == "edit_material" || tc.Name == "edit_shot") {
				s.extractProposal(tc, result, projectState.Revision())
			}
		}
	}
	if !finished {
		outputStatus = "needs_action"
		if resolved {
			lastReply = "本轮工具处理已达上限。已有任务或方案已保留，请查看制作状态；不会重复提交。"
		} else {
			lastReply = "本轮工具处理已达上限，制作任务尚未启动。请查看工具错误并继续处理。"
		}
	}

	// 保存历史
	s.mu.Lock()
	assistantMsg := ChatMessage{
		ID:        uuid.New().String(),
		Role:      "assistant",
		Content:   lastReply,
		CreatedAt: time.Now(),
	}
	s.history = append(s.history, assistantMsg)
	s.history = NormalizeHistory(s.history)
	openProposal := globalProposals.Get(s.config.ProjectID)
	s.mu.Unlock()

	// 落库（空回复不写，避免产生无意义消息）
	if strings.TrimSpace(lastReply) != "" {
		s.persist(ctx, assistantMsg)
	}

	return &ChatOutput{
		Reply:    lastReply,
		Status:   outputStatus,
		TurnID:   input.TurnID,
		Proposal: openProposal,
	}, nil
}

// extractProposal 从工具结果中提取提案并保存
func (s *SessionState) extractProposal(tc ToolCall, result string, revision int) {
	// 解析工具结果中的提案信息
	p := &Proposal{
		ID:        uuid.New().String(),
		Revision:  revision,
		Lines:     []string{fmt.Sprintf("工具 %s 生成修改提案", tc.Name)},
		CreatedAt: time.Now(),
	}
	// 从参数中提取 rowIds
	if ids, ok := tc.Arguments["rowIds"].([]any); ok {
		for _, v := range ids {
			if f, ok := v.(float64); ok {
				p.RowIDs = append(p.RowIDs, int(f))
			}
		}
	}
	// 设置受影响的下游节点
	p.AffectedSteps = determineAffectedSteps(tc.Name)
	globalProposals.Save(s.config.ProjectID, p)
}

// determineAffectedSteps 根据工具名推断受影响的下游节点
func determineAffectedSteps(toolName string) []string {
	switch toolName {
	case "edit_script":
		return []string{"F03", "F04", "F05", "F06", "F07", "F08", "F09", "F10", "F11", "F12"}
	case "edit_voice":
		return []string{"F05", "F06", "F07", "F08", "F09", "F10", "F11", "F12"}
	case "edit_material":
		return []string{"F06", "F07", "F08", "F09", "F10", "F11", "F12"}
	case "edit_shot":
		return []string{"F11", "F12"}
	default:
		return nil
	}
}

// ─────────────────────────────────────────────
// emptyProjectState — nil 安全占位实现
// 当 handler 尚未注入真实 projectState 时使用
// ─────────────────────────────────────────────

type emptyProjectState struct{}

func (e *emptyProjectState) Revision() int                                       { return 0 }
func (e *emptyProjectState) GetPresets() string                                  { return "" }
func (e *emptyProjectState) GetIndex() string                                    { return "（项目数据加载中）" }
func (e *emptyProjectState) GetWorkflowState() string                            { return "待开始" }
func (e *emptyProjectState) GetNodeStatuses() map[string]string                  { return nil }
func (e *emptyProjectState) GetScriptSegments(_ string) ([]ScriptSegment, error) { return nil, nil }
func (e *emptyProjectState) GetMaterialNeeds(_ string) ([]MaterialNeed, error)   { return nil, nil }
func (e *emptyProjectState) GetShots(_ string) ([]Shot, error)                   { return nil, nil }
func (e *emptyProjectState) Fingerprint() string                                 { return "" }

// ─────────────────────────────────────────────
// 意图分类（简单关键词路由）
// ─────────────────────────────────────────────

func (s *SessionState) classifyIntent(message string) Intent {
	msg := toLower(message)
	if contains(msg, "建议", "怎么选", "哪种好", "推荐") {
		return IntentAdvice
	}
	if contains(msg, "进度", "状态", "到哪了", "怎么样了") {
		return IntentStatus
	}
	if contains(msg, "撤销", "回退", "undo", "撤销上一步") {
		return IntentUndo
	}
	if contains(msg, "文案", "改稿", "重写", "段落") {
		return IntentEditScript
	}
	if contains(msg, "配音", "声音", "语速", "音色") {
		return IntentEditVoice
	}
	if contains(msg, "素材", "画面", "动画类型") {
		return IntentEditMaterial
	}
	if contains(msg, "分镜", "镜头", "布局", "构图") {
		return IntentEditShot
	}
	return IntentInput
}

// ─────────────────────────────────────────────
// LLM 调用（JSON 信封协议）
// 对应 NarratoAI-PC pi-agent.service.ts
// ─────────────────────────────────────────────

// LLMResponse LLM 响应（JSON 信封）
type LLMResponse struct {
	Reply     string     `json:"reply"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// AgentPromptKind 提示词模板类型标识，供 providers 侧路由到对应模板与模型档位
const AgentPromptKind = "avatar_agent"

// callLLM 简单 LLM 调用（无工具，用于建议/状态解释等纯文字场景）
func (s *SessionState) callLLM(ctx context.Context, message string, tools []map[string]any) (string, error) {
	client := s.config.LLM
	if client == nil {
		// 未注入客户端时退化为本地桩：单元测试与离线开发无需真实凭据
		return "（LLM 响应占位）", nil
	}
	raw, err := client.Complete(ctx, LLMRequest{
		Kind:         AgentPromptKind,
		SystemPrompt: AVATAR_AGENT_DECISION_GUIDANCE,
		History:      []ChatMessage{{Role: "user", Content: message}},
		Tools:        tools,
		ProjectID:    s.config.ProjectID,
	})
	if err != nil {
		return "", err
	}
	return parseLLMResponse(raw).Reply, nil
}

// callLLMWithTools 带工具的 LLM 调用
func (s *SessionState) callLLMWithTools(
	ctx context.Context, systemPrompt string, history []ChatMessage, tools []map[string]any,
) (string, error) {
	client := s.config.LLM
	if client == nil {
		return `{"reply":"（示例回复）当前未接入模型服务，只演示流程。","tool_calls":[]}`, nil
	}
	return client.Complete(ctx, LLMRequest{
		Kind:         AgentPromptKind,
		SystemPrompt: systemPrompt,
		History:      history,
		Tools:        tools,
		ProjectID:    s.config.ProjectID,
	})
}

// parseLLMResponse 解析 LLM JSON 信封响应
// M-04 修复：JSON 解析失败时尝试提取 reply 字段；若完全无法解析才降级，
// 并在降级回复中标注 [原始响应]，避免把裸 JSON 结构直接展示给用户。
func parseLLMResponse(raw string) LLMResponse {
	var resp LLMResponse
	if err := json.Unmarshal([]byte(raw), &resp); err == nil {
		return resp
	}
	// 尝试从非标准 JSON 中提取 reply 字段（部分 LLM 可能返回截断的 JSON）
	var partial map[string]any
	if json.Unmarshal([]byte(raw), &partial) == nil {
		if reply, ok := partial["reply"].(string); ok && reply != "" {
			return LLMResponse{Reply: reply}
		}
	}
	// 完全无法解析：截断超长原始响应并标注，防止把 JSON 乱码直接暴露给用户
	fallback := raw
	if len(fallback) > 300 {
		fallback = fallback[:300] + "…[已截断]"
	}
	return LLMResponse{Reply: "[原始响应] " + fallback}
}

// buildSystemPrompt 构建系统 prompt
func (s *SessionState) buildSystemPrompt(ctx LayeredContext, intent Intent) string {
	parts := []string{
		ctx.SystemRules,
		"",
		defaultSkillGuidance(),
		"",
		"## 项目预设",
		ctx.Presets,
		"",
		"## 项目索引",
		ctx.ProjectIndex,
	}
	if ctx.Memory != "" {
		parts = append(parts, "", "## 用户记忆", ctx.Memory)
	}
	parts = append(parts, "", "## 工作流状态", ctx.WorkflowState)
	if ctx.OpenProposal != nil {
		parts = append(parts, "", "## 待确认提案",
			fmt.Sprintf("存在未确认提案，revision=%d", ctx.OpenProposal.Revision))
	}
	parts = append(parts, "", fmt.Sprintf("## 当前意图: %s", intent))
	return joinStrings(parts, "\n")
}

// ─────────────────────────────────────────────
// 辅助函数
// ─────────────────────────────────────────────

func (s *SessionState) getNormalizedHistory() []ChatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return NormalizeHistory(s.history)
}

func (s *SessionState) emit(event any) {
	if e, ok := event.(ToolEvent); ok {
		s.setActivity("running", "tool", e.Tool, e.Detail)
	}
	s.mu.Lock()
	emitter := s.emitter
	s.mu.Unlock()
	if emitter != nil {
		emitter(event)
	}
}

func (s *SessionState) SetEmitter(fn func(event any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitter = fn
}

// SetStore 注入消息持久化实现
func (s *SessionState) SetStore(store MessageStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store = store
}

// hydrate 从持久层回填历史。
// 仅在内存历史为空时执行，避免覆盖本进程内已进行的对话；
// 读取失败不阻断对话——历史是可选的增强，不是执行前提。
func (s *SessionState) hydrate(ctx context.Context, projectID string) {
	s.mu.Lock()
	store, hasHistory := s.store, len(s.history) > 0
	s.mu.Unlock()
	if store == nil || hasHistory {
		return
	}
	loaded, err := store.Load(ctx, projectID)
	if err != nil || len(loaded) == 0 {
		return
	}
	s.mu.Lock()
	if len(s.history) == 0 {
		s.history = NormalizeHistory(loaded)
	}
	s.mu.Unlock()
}

// persist 把消息写入持久层（失败仅忽略：内存历史仍然可用）
func (s *SessionState) persist(ctx context.Context, msg ChatMessage) {
	s.mu.Lock()
	store := s.store
	projectID, userID := s.config.ProjectID, s.config.UserID
	s.mu.Unlock()
	if store == nil {
		return
	}
	_ = store.Append(ctx, projectID, userID, msg)
}

func (s *SessionState) Abort() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.abort != nil {
		s.abort.Abort()
	}
}

func determinePhase(projectState ProjectState) ProjectPhase {
	// H-01 修复：使用精确节点名匹配，避免宽泛 contains 误匹配
	// GetWorkflowState 返回形如 "当前节点: F05\n状态: running" 的结构化字符串
	// 通过标准化后用精确前缀/分词匹配，不再用 strings.Contains 拼接节点编号
	state := projectState.GetWorkflowState()
	node := extractCurrentNode(state)
	switch {
	case node >= 10: // F10, F11, F12
		return PhaseHasShot
	case node >= 5: // F05-F09
		return PhaseHasMaterial
	case node == 4: // F04
		return PhaseHasVoice
	case node >= 2: // F02, F03
		return PhaseHasScript
	default:
		return PhaseInitial
	}
}

// extractCurrentNode 从工作流状态字符串中提取当前节点编号
// 支持 "当前节点: F05"、"node:F05" 等显式标记；无标记时才回退到扫描最大节点号。
// 优先解析显式标记，是因为状态文本可能同时列出已完成节点的历史（F01..F04），
// 直接取最大值会把"当前节点 F05"误判成 F04 之后的最远节点。
func extractCurrentNode(state string) int {
	for _, marker := range []string{"当前节点:", "当前节点：", "node:", "node："} {
		if idx := strings.Index(state, marker); idx >= 0 {
			rest := state[idx+len(marker):]
			// 允许标记与节点名之间有空白
			rest = strings.TrimLeft(rest, " \t")
			if n := parseNodeNumber(rest); n > 0 {
				return n
			}
		}
	}
	return maxNodeNumber(state)
}

// parseNodeNumber 解析紧跟在前缀位置的节点号（如 "F05 ..." → 5）
func parseNodeNumber(s string) int {
	if len(s) < 2 || (s[0] != 'F' && s[0] != 'f') {
		return 0
	}
	n := 0
	j := 1
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		n = n*10 + int(s[j]-'0')
		j++
	}
	if n >= 1 && n <= 12 {
		return n
	}
	return 0
}

// maxNodeNumber 扫描文本中出现过的最大节点号（无显式标记时的回退策略）
func maxNodeNumber(state string) int {
	max := 0
	for i := 0; i < len(state)-1; i++ {
		if state[i] != 'F' && state[i] != 'f' {
			continue
		}
		// 前一个字符是字母时不视为节点名前缀（避免匹配 "before" 中的 f）
		if i > 0 && isASCIILetter(state[i-1]) {
			continue
		}
		if n := parseNodeNumber(state[i:]); n > max {
			max = n
		}
	}
	return max
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func toLower(s string) string {
	return strings.ToLower(s)
}

func contains(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func joinStrings(parts []string, sep string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += sep
		}
		result += p
	}
	return result
}
