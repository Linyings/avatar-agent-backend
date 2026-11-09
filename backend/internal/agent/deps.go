// deps.go — agent 包的依赖注入契约
//
// 本包刻意不直接依赖 gorm 或 providers：它只声明自己需要什么，
// 由 server 层在装配路由时注入实现。这样测试可以整体替换，
// 也避免 agent → server 的反向依赖。
package agent

import (
	"context"

	"github.com/google/uuid"
)

// LLMRequest 一次模型调用所需的全部输入
type LLMRequest struct {
	// Kind 提示词模板类型，用于 providers 侧选择模板与模型档位
	Kind string
	// SystemPrompt 系统提示（规则 + 预设 + 项目索引 + 记忆 + 工作流状态）
	SystemPrompt string
	// History 归一化后的对话历史
	History []ChatMessage
	// Tools 工具的 JSON Schema 列表，模型据此决定是否调用
	Tools []map[string]any
	// ProjectID 用于计费与追踪
	ProjectID string
}

// LLMClient 模型调用抽象。
// 返回值是模型原始响应文本（约定为 JSON 信封 {"reply":..,"tool_calls":[..]}）。
type LLMClient interface {
	Complete(ctx context.Context, req LLMRequest) (string, error)
}

// ProjectResolver 按项目 ID 加载只读项目状态。
// owner 必须参与校验，否则跨用户读取项目内容。
type ProjectResolver func(ctx context.Context, owner uuid.UUID, projectID string) (ProjectState, error)

// ProjectAuthorizer 校验项目归属。
// 单独于 ProjectResolver 存在，是因为部分接口（history/stop/confirm）
// 只需要"这个项目是不是你的"，不需要加载整份工作流快照。
type ProjectAuthorizer func(ctx context.Context, owner uuid.UUID, projectID string) error

// MessageStore 对话历史持久化。
// 允许为 nil —— 此时 agent 退化为纯内存会话（开发环境或测试）。
type MessageStore interface {
	// Load 读取项目最近的对话历史，按时间正序
	Load(ctx context.Context, projectID string) ([]ChatMessage, error)
	// Append 追加一条消息
	Append(ctx context.Context, projectID, userID string, msg ChatMessage) error
}

// ProjectRunner 把 Agent 的制作计划交给真实工作流引擎执行。
//
// 存在这个接口，是因为执行引擎（TTS / 图像 / 视频生成）住在 server 包，
// agent 包不能反向依赖它。实现方负责：校验节点前置条件、落库、异步启动，
// 并在执行过程中通过事件通道把节点进度推回前端。
//
// Run 必须是"提交即返回"：整个工作流可能跑几分钟，不能让 HTTP 请求等它。
type ProjectRunner interface {
	// Run 从 target 节点开始执行。target 形如 "F02"、"F05"；空字符串表示从当前断点继续。
	// 归属校验由实现方按 projectID 查项目行完成——调用前 handler 已经做过一次
	// authorizeProject，这里再查是为了不把 owner 一路穿过工具层。
	Run(ctx context.Context, projectID, target string) error
}

// Deps 注册路由所需的全部外部依赖
type Deps struct {
	LLM       LLMClient
	Resolve   ProjectResolver
	Authorize ProjectAuthorizer // 校验项目归属；为 nil 时拒绝所有带 projectId 的请求
	Messages  MessageStore      // 对话历史持久化，可为 nil
	Memories  MemoryPersistence // 记忆持久化，可为 nil（M-03 修复）
	Runner    ProjectRunner     // 工作流执行通道；为 nil 时 Agent 只能分析不能执行
	Packages  PackageHost
}
