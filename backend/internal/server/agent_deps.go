// agent_deps.go — 把 server 层的真实能力接进 agent 包
//
// agent 只声明接口（LLMClient / ProjectResolver / MessageStore），
// 实现放在这里：用 avatar 工作流快照做项目状态，用 OpenAI 兼容接口做模型调用。
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/narratoai/backend-go/internal/agent"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/models"
)

// ─────────────────────────────────────────────
// 项目状态装配
// ─────────────────────────────────────────────

// agentProjectResolver 按 (owner, projectID) 加载工作流快照并适配为 ProjectState。
// owner 参与查询：解析阶段就拦住跨用户读取，不依赖上层记得校验。
type agentProjectResolver struct {
	app *App
}

func (r agentProjectResolver) Resolve(ctx context.Context, owner uuid.UUID, projectID string) (agent.ProjectState, error) {
	if owner == uuid.Nil {
		return nil, fmt.Errorf("missing owner")
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, fmt.Errorf("missing project id")
	}
	// load 内部带 owner 校验，越权与不存在同样返回错误
	state, err := r.app.latestAgentRun(ctx, owner, mustUUID(projectID))
	if err != nil {
		// 工作流可能尚未创建（项目刚建、还没跑过任何节点），
		// 此时退化为只读的空快照，让 Agent 仍能对话而不是直接 404。
		if empty, ok := r.emptyState(ctx, owner, projectID); ok {
			return agent.NewWorkflowProjectState(empty), nil
		}
		return nil, err
	}
	return agent.NewWorkflowProjectState(state), nil
}

// emptyState 项目存在但尚无工作流时，构造一份带项目元信息的最小快照
func (r agentProjectResolver) emptyState(ctx context.Context, owner uuid.UUID, projectID string) (avatar.WorkflowState, bool) {
	project, input, err := r.app.avatarWorkflow.project(ctx, owner, mustUUID(projectID))
	if err != nil {
		return avatar.WorkflowState{}, false
	}
	return avatar.WorkflowState{
		ID:        projectID,
		ProjectID: projectID,
		Version:   avatar.WorkflowVersion,
		Input:     input,
		Status:    "pending",
		Stage:     "F01",
		Script:    input.Script,
		Segments:  []avatar.Segment{},
		Needs:     []avatar.MaterialNeed{},
		Shots:     []avatar.WorkflowShot{},
		Steps:     []avatar.WorkflowStep{},
		UpdatedAt: project.UpdatedAt,
	}, true
}

func mustUUID(raw string) uuid.UUID {
	id, _ := uuid.Parse(raw)
	return id
}

// agentProjectAuthorizer 只校验归属，不加载整份工作流快照。
// 供 history / stop / confirm / pre-production 等接口使用，
// 这些入口不需要项目内容，但同样必须拦住跨用户访问。
type agentProjectAuthorizer struct {
	app *App
}

func (a agentProjectAuthorizer) Authorize(ctx context.Context, owner uuid.UUID, projectID string) error {
	if owner == uuid.Nil {
		return gorm.ErrRecordNotFound
	}
	id, err := uuid.Parse(projectID)
	if err != nil {
		return gorm.ErrRecordNotFound
	}
	var count int64
	if err := a.app.db.WithContext(ctx).Model(&models.Project{}).
		Where("id = ? AND owner_id = ?", id, owner).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ─────────────────────────────────────────────
// LLM 装配
// ─────────────────────────────────────────────

// agentLLMAdapter 走 OpenAI 兼容的 /chat/completions。
//
// 不复用 providers.OpenAIClient.Stream：那条链路是「单轮 system+user + 模板渲染」，
// 而 Agent 需要多轮历史 + tools 协议 + 结构化 tool_calls 回传，语汇不兼容。
type agentLLMAdapter struct {
	cfg    config.Config
	client *http.Client
}

func newAgentLLMAdapter(cfg config.Config) *agentLLMAdapter {
	return &agentLLMAdapter{
		cfg:    cfg,
		client: &http.Client{Timeout: 180 * time.Second},
	}
}

// Complete 调用模型并归一化响应为 agent 约定的 JSON 信封。
func (a *agentLLMAdapter) Complete(ctx context.Context, req agent.LLMRequest) (string, error) {
	baseURL, apiKey, model := a.credentials()
	if apiKey == "" {
		return "", fmt.Errorf("agent LLM 未配置：请设置 OPENAI_PRO_API_KEY 或 OPENAI_FLASH_API_KEY")
	}

	payload := map[string]any{
		"model":       model,
		"messages":    a.buildMessages(req),
		"temperature": a.cfg.OpenAITemperature,
		// Some compatible endpoints default to only 1024 output tokens. Share
		// the workflow's bounded budget, including its configured provider cap.
		"max_tokens": avatar.ContentMaxTokens(ctx),
	}
	if len(req.Tools) > 0 {
		payload["tools"] = a.openAITools(req.Tools)
		payload["tool_choice"] = "auto"
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("agent LLM 返回 %d: %s", resp.StatusCode, truncateBytes(raw, 300))
	}
	return normalizeAgentCompletion(raw)
}

// credentials 返回 Agent 的模型配置
// 优先使用独立的 Agent 配置，未配置时回退到 Pro 档，最后回退 Flash
func (a *agentLLMAdapter) credentials() (baseURL, apiKey, model string) {
	// 优先：独立 Agent 配置
	if a.cfg.AgentConfigured() {
		return a.cfg.AgentAPIBaseURL, a.cfg.AgentAPIKey, a.cfg.AgentModel
	}
	// 回退：Pro 配置
	if a.cfg.OpenAIProConfigured() {
		return a.cfg.OpenAIProAPIBaseURL, a.cfg.OpenAIProAPIKey, a.cfg.OpenAIProModel
	}
	// 最后回退：Flash 配置
	return a.cfg.OpenAIFlashAPIBaseURL, a.cfg.OpenAIFlashAPIKey, a.cfg.OpenAIFlashModel
}

// buildMessages 把分层上下文 + 多轮历史压成 OpenAI messages
func (a *agentLLMAdapter) buildMessages(req agent.LLMRequest) []map[string]any {
	messages := make([]map[string]any, 0, len(req.History)+1)
	if strings.TrimSpace(req.SystemPrompt) != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.SystemPrompt})
	}
	for _, m := range req.History {
		switch m.Role {
		case "tool":
			// OpenAI 协议要求 tool 角色带 tool_call_id
			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": m.ToolID,
				"content":      m.Content,
			})
		default:
			message := map[string]any{"role": m.Role, "content": m.Content}
			if m.Role == "assistant" && len(m.ToolCalls) > 0 {
				calls := []map[string]any{}
				for _, call := range m.ToolCalls {
					args, _ := json.Marshal(call.Arguments)
					calls = append(calls, map[string]any{"id": call.ID, "type": "function", "function": map[string]any{"name": call.Name, "arguments": string(args)}})
				}
				message["tool_calls"] = calls
			}
			messages = append(messages, message)
		}
	}
	return messages
}

// openAITools 把 agent 的裸 schema 包成 OpenAI function 格式。
// agent 侧只关心 name/description/parameters，协议包装属于传输层细节。
func (a *agentLLMAdapter) openAITools(tools []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		fn := map[string]any{"name": t["name"]}
		if desc, ok := t["description"]; ok {
			fn["description"] = desc
		}
		params, ok := t["parameters"]
		if !ok || params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		fn["parameters"] = params
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out
}

// normalizeAgentCompletion 把 OpenAI 响应转成 agent 的信封格式：
// {"reply": "...", "tool_calls": [{"id","name","arguments":{...}}]}
func normalizeAgentCompletion(raw []byte) (string, error) {
	var decoded struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", fmt.Errorf("解析模型响应失败: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("模型响应为空")
	}
	if decoded.Choices[0].FinishReason == "length" {
		return "", fmt.Errorf("模型输出被截断（finish_reason=length）；请缩小本轮输出或在服务端支持范围内调整输出预算")
	}

	msg := decoded.Choices[0].Message
	if strings.TrimSpace(msg.Content) == "" && len(msg.ToolCalls) == 0 {
		return "", fmt.Errorf("模型没有返回文字或工具调用，本轮未得到有效答复")
	}
	envelope := map[string]any{"reply": msg.Content}
	if len(msg.ToolCalls) > 0 {
		calls := make([]map[string]any, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			args := map[string]any{}
			// arguments 是 JSON 字符串；解析失败时保留空参数，
			// 让工具侧的 Check 报参数缺失而不是整轮崩掉
			if strings.TrimSpace(tc.Function.Arguments) != "" {
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			}
			calls = append(calls, map[string]any{
				"id":        tc.ID,
				"name":      tc.Function.Name,
				"arguments": args,
			})
		}
		envelope["tool_calls"] = calls
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func truncateBytes(raw []byte, limit int) string {
	if len(raw) <= limit {
		return string(raw)
	}
	return string(raw[:limit]) + "…"
}
