// Package agent 实现 avatarSpeak Agent 框架
// 架构参考 NarratoAI-PC 电影模式 AI 剪辑助手
// 三层分离：前端 Runner / 后端 AgentSession / 持久化项目数据
package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────
// 核心数据类型
// ─────────────────────────────────────────────

// Intent 意图类型
type Intent string

const (
	IntentInput        Intent = "input"         // 用户描述创作需求，填充 F01
	IntentEditScript   Intent = "edit-script"   // 修改文案段落
	IntentEditVoice    Intent = "edit-voice"    // 调整配音
	IntentEditMaterial Intent = "edit-material" // 修改素材需求
	IntentEditShot     Intent = "edit-shot"     // 修改分镜
	IntentAdvice       Intent = "advice"        // 纯文字建议，不触发节点
	IntentStatus       Intent = "status"        // 查询制作进度
	IntentUndo         Intent = "undo"          // 撤销上一步
	IntentReviewAccept Intent = "review-accept" // 接受审核结论
	IntentReviewFix    Intent = "review-fix"    // 选择修改建议
)

// EditVersion 乐观锁版本（对应 NarratoAI-PC movieAgentEditVersion）
type EditVersion struct {
	Revision    int    `json:"revision"`
	Fingerprint string `json:"fingerprint"` // 段落内容 SHA-256
}

// Proposal 提案（Agent 生成变更但不直接写入，等用户确认）
// 对应 NarratoAI-PC MovieAgentOpenProposal
type Proposal struct {
	ID            string    `json:"id"`
	RowIDs        []int     `json:"rowIds"`
	Revision      int       `json:"revision"`      // 生成时的项目版本
	Lines         []string  `json:"lines"`         // 人话描述变更（非 diff）
	AffectedSteps []string  `json:"affectedSteps"` // 影响的下游节点，如 ["F04","F06"]
	Applied       bool      `json:"applied"`
	CreatedAt     time.Time `json:"createdAt"`
}

// ChatMessage 对话历史消息
type ChatMessage struct {
	ID        string     `json:"id"`
	Role      string     `json:"role"` // "user" | "assistant" | "tool"
	Content   string     `json:"content"`
	ToolName  string     `json:"toolName,omitempty"`
	ToolID    string     `json:"toolId,omitempty"`
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

// ToolEvent 工具调用事件（通过 SSE 推送到前端）
// 对应 NarratoAI-PC MovieAgentStreamEvent{type:'tool'}
type ToolEvent struct {
	Type   string `json:"type"` // "tool"
	TurnID string `json:"turnId"`
	CallID string `json:"callId"`
	Tool   string `json:"tool"`
	Status string `json:"status"` // "running" | "done" | "error"
	Detail string `json:"detail,omitempty"`
}

// ThinkingEvent 思考流事件
type ThinkingEvent struct {
	Type   string `json:"type"` // "thinking"
	TurnID string `json:"turnId"`
	Delta  string `json:"delta"`
}

// QuestionEvent 需要用户回答的问题
type QuestionEvent struct {
	Type              string          `json:"type"` // "question"
	TurnID            string          `json:"turnId"`
	QuestionID        string          `json:"questionId"`
	Title             string          `json:"title"`
	Subtitle          string          `json:"subtitle,omitempty"`
	Body              string          `json:"body,omitempty"`
	Choices           []ConfirmChoice `json:"choices"`
	AllowCustom       bool            `json:"allowCustom"`
	CustomPlaceholder string          `json:"customPlaceholder,omitempty"`
}

// ConfirmChoice 确认选项（禁止"是/否"空选项）
type ConfirmChoice struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
	Danger      bool   `json:"danger,omitempty"`
}

// ChatInput Agent 对话输入
type ChatInput struct {
	ProjectID string `json:"projectId"`
	Message   string `json:"message"`
	Intent    Intent `json:"intent,omitempty"`
	RequestID string `json:"requestId"`        // 幂等 ID
	Accept    bool   `json:"accept,omitempty"` // 直接接受上一个提案
	TurnID    string `json:"turnId"`           // 前端生成，用于 SSE 事件匹配
}

// ChatOutput Agent 对话输出
type ChatOutput struct {
	Reply    string    `json:"reply"`
	Proposal *Proposal `json:"proposal,omitempty"`
	Status   string    `json:"status"` // "ok" | "conflict" | "error"
	TurnID   string    `json:"turnId"`
}

// ─────────────────────────────────────────────
// 乐观锁工具函数
// ─────────────────────────────────────────────

// ComputeFingerprint 计算段落内容指纹（SHA-256 前 16 字节 hex）
func ComputeFingerprint(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h[:16])
}

// SameVersion 比对两个版本是否一致
func SameVersion(a, b EditVersion) bool {
	return a.Revision == b.Revision && a.Fingerprint == b.Fingerprint
}

// ─────────────────────────────────────────────
// inflight 并发去重（同一 requestId 只跑一次）
// 对应 NarratoAI-PC inflight Map
// ─────────────────────────────────────────────

type inflightMap struct {
	mu   sync.Mutex
	runs map[string]*inflightEntry
}

type inflightEntry struct {
	done chan struct{}
	out  *ChatOutput
	err  error
}

var globalInflight = &inflightMap{runs: make(map[string]*inflightEntry)}

// Acquire 获取执行权，返回 (entry, isOwner)
// isOwner=true 表示本 goroutine 负责执行，执行完后调用 Release
// isOwner=false 表示已有执行，等待 entry.done 即可
func (m *inflightMap) Acquire(key string) (*inflightEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.runs[key]; ok {
		return e, false
	}
	e := &inflightEntry{done: make(chan struct{})}
	m.runs[key] = e
	return e, true
}

// Release 标记执行完成并清理
func (m *inflightMap) Release(key string, out *ChatOutput, err error) {
	m.mu.Lock()
	e, ok := m.runs[key]
	if ok {
		e.out = out
		e.err = err
		delete(m.runs, key)
	}
	m.mu.Unlock()
	if ok {
		close(e.done)
	}
}

// ─────────────────────────────────────────────
// 提案仓库（每个项目只保留最后一条未确认提案）
// ─────────────────────────────────────────────

type proposalStore struct {
	mu    sync.Mutex
	store map[string]*Proposal // projectID -> latest proposal
}

var globalProposals = &proposalStore{store: make(map[string]*Proposal)}

// Save 保存提案（覆盖同一项目的旧提案）
func (s *proposalStore) Save(projectID string, p *Proposal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store[projectID] = p
}

// Get 获取项目最新提案
func (s *proposalStore) Get(projectID string) *Proposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store[projectID]
}

// GetIfValid 获取提案并校验版本（version 不符则视为失效）
func (s *proposalStore) GetIfValid(projectID string, currentRevision int) *Proposal {
	p := s.Get(projectID)
	if p == nil || p.Applied || p.Revision != currentRevision {
		return nil
	}
	return p
}

// MarkApplied 标记提案已应用
func (s *proposalStore) MarkApplied(projectID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.store[projectID]; ok {
		p.Applied = true
	}
}

// ─────────────────────────────────────────────
// 对话历史管理（最近 24 条 / 按 token 预算截断）
// 对应 NarratoAI-PC normalizeMovieAgentConversationHistory
// ─────────────────────────────────────────────

const (
	MaxHistoryMessages  = 24
	MaxHistoryChars     = 16000
	FullToolResultTurns = 2 // 只保留最近 2 轮完整工具结果
)

// NormalizeHistory 归一化对话历史（裁剪 + 压缩旧工具结果）
// 返回新切片，不突变入参（M-01 修复）
func NormalizeHistory(messages []ChatMessage) []ChatMessage {
	if len(messages) == 0 {
		return messages
	}
	// 复制一份，避免修改调用方的底层数组
	out := make([]ChatMessage, len(messages))
	copy(out, messages)

	// 截取最近 MaxHistoryMessages 条
	if len(out) > MaxHistoryMessages {
		out = out[len(out)-MaxHistoryMessages:]
	}
	// 按字符数预算截断
	total := 0
	for _, m := range out {
		total += len(m.Content)
	}
	for total > MaxHistoryChars && len(out) > 1 {
		total -= len(out[0].Content)
		out = out[1:]
	}
	// 更早的工具结果只保留结论摘要（前 120 字符）
	cutoff := len(out) - FullToolResultTurns*2
	for i := 0; i < cutoff && i < len(out); i++ {
		if out[i].Role == "tool" && len(out[i].Content) > 120 {
			out[i].Content = out[i].Content[:120] + "…[已压缩]"
		}
	}
	return out
}

// ─────────────────────────────────────────────
// 记忆系统（4 类：preference/fact/decision/checkpoint）
// ─────────────────────────────────────────────

// MemoryKind 记忆类型
type MemoryKind string

const (
	MemoryPreference MemoryKind = "preference"
	MemoryFact       MemoryKind = "fact"
	MemoryDecision   MemoryKind = "decision"
	MemoryCheckpoint MemoryKind = "checkpoint"
)

// Memory 单条记忆
type Memory struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"projectId"`
	Kind      MemoryKind `json:"kind"`
	Content   string     `json:"content"`
	CreatedAt time.Time  `json:"createdAt"`
}

// NewMemory 创建记忆条目
func NewMemory(projectID string, kind MemoryKind, content string) Memory {
	return Memory{
		ID:        uuid.New().String(),
		ProjectID: projectID,
		Kind:      kind,
		Content:   content,
		CreatedAt: time.Now(),
	}
}

// ─────────────────────────────────────────────
// 工具调用 Issue（结构化错误票据）
// 对应 NarratoAI-PC MovieHarnessIssue
// ─────────────────────────────────────────────

// IssueKind 问题类型
type IssueKind string

const (
	IssueKindFormat   IssueKind = "format"
	IssueKindFact     IssueKind = "fact"
	IssueKindCoverage IssueKind = "coverage"
	IssueKindService  IssueKind = "service"
	IssueKindScope    IssueKind = "scope"
)

// ResolutionKind 处置方式
type ResolutionKind string

const (
	ResolutionRepairContent ResolutionKind = "repair-content" // 让模型自动修复
	ResolutionAskUser       ResolutionKind = "ask-user"       // 生成确认选项给用户
	ResolutionRetry         ResolutionKind = "retry"          // 工作流重试（技术错误）
)

// Issue 工具调用问题票据
type Issue struct {
	ID         string         `json:"id"`
	Kind       IssueKind      `json:"kind"`
	Source     string         `json:"source"` // 哪个工具/节点产生
	Code       string         `json:"code"`
	Message    string         `json:"message"`
	ItemIDs    []int          `json:"itemIds,omitempty"`
	Expected   string         `json:"expected,omitempty"`
	Actual     string         `json:"actual,omitempty"`
	Blocking   bool           `json:"blocking"`
	Resolution ResolutionKind `json:"resolution"`
}

// NewIssue 创建 Issue
func NewIssue(kind IssueKind, source, code, message string, blocking bool, resolution ResolutionKind) Issue {
	return Issue{
		ID:         uuid.New().String(),
		Kind:       kind,
		Source:     source,
		Code:       code,
		Message:    message,
		Blocking:   blocking,
		Resolution: resolution,
	}
}

// ─────────────────────────────────────────────
// 中止信号
// ─────────────────────────────────────────────

// TurnAbort 单轮次的中止控制器
type TurnAbort struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// NewTurnAbort 创建带超时的中止控制器（默认 1 小时）
func NewTurnAbort() *TurnAbort {
	return NewTurnAbortWithContext(context.Background())
}

func NewTurnAbortWithContext(parent context.Context) *TurnAbort {
	ctx, cancel := context.WithTimeout(parent, time.Hour)
	return &TurnAbort{ctx: ctx, cancel: cancel}
}

// Context 返回 context（供 HTTP 客户端和工具使用）
func (a *TurnAbort) Context() context.Context { return a.ctx }

// Abort 中止当前轮次
func (a *TurnAbort) Abort() { a.cancel() }

// Done 是否已中止
func (a *TurnAbort) Done() bool {
	select {
	case <-a.ctx.Done():
		return true
	default:
		return false
	}
}
