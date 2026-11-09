// tools.go — Agent 工具注册与三道闸
// 对应 NarratoAI-PC movie-agent-pi-plugin.ts
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// ─────────────────────────────────────────────
// 工具接口
// ─────────────────────────────────────────────

// ToolCall 单次工具调用
type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ToolResult 工具执行结果
type ToolResult struct {
	CallID  string `json:"callId"`
	Content string `json:"content"`
	Error   string `json:"error,omitempty"`
}

// Tool 工具定义
type Tool struct {
	Name        string         // 工具名（英文，snake_case）
	Description string         // 工具描述（供 LLM 理解）
	Parameters  map[string]any // JSON Schema parameters
	// Once=true: 本轮次只能调用一次（如 plan_project）
	Once bool
	// Noop identifies an explicit non-admission result. Only these successful
	// responses release Once; execution errors remain locked against duplication.
	Noop func(result string) bool
	// Check 独立校验函数（校验失败时 block，原因回灌模型）
	Check func(args map[string]any) *Issue
	// Execute 执行函数
	Execute func(ctx context.Context, args map[string]any, emit EventEmitter) (string, error)
}

// EventEmitter 用于工具执行时推送 SSE 事件
type EventEmitter func(event any)

// ─────────────────────────────────────────────
// 工具注册表与三道闸
// ─────────────────────────────────────────────

// ToolRegistry 工具注册表（每个 AgentSession 独立实例）
type ToolRegistry struct {
	mu       sync.Mutex
	tools    map[string]*Tool
	callCnt  map[string]int // 本轮调用次数
	onceDone map[string]bool
}

const MaxToolCallsPerTurn = 12 // 单轮最大工具调用次数

// NewToolRegistry 创建工具注册表
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools:    make(map[string]*Tool),
		callCnt:  make(map[string]int),
		onceDone: make(map[string]bool),
	}
}

// Register 注册工具
func (r *ToolRegistry) Register(t *Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name] = t
}

// ResetTurn 每轮对话开始时重置调用计数
func (r *ToolRegistry) ResetTurn() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.callCnt = make(map[string]int)
	r.onceDone = make(map[string]bool)
}

// Invoke 三道闸 + 执行工具
// 返回 (result, blocked, issue)
func (r *ToolRegistry) Invoke(
	ctx context.Context, call ToolCall, turnID string, emit EventEmitter,
) (result string, blocked bool, issue *Issue) {
	r.mu.Lock()
	t, exists := r.tools[call.Name]
	if !exists {
		r.mu.Unlock()
		i := NewIssue(IssueKindFormat, call.Name, "unknown_tool",
			fmt.Sprintf("工具 %q 未注册", call.Name), true, ResolutionRepairContent)
		return "", true, &i
	}

	// 闸 1：once 工具重复调用
	if t.Once && r.onceDone[call.Name] {
		r.mu.Unlock()
		i := NewIssue(IssueKindFormat, call.Name, "once_tool_repeated",
			fmt.Sprintf("工具 %q 本轮只能调用一次，已拒绝重复调用", call.Name), true, ResolutionRepairContent)
		return "", true, &i
	}

	// 闸 2：超过最大调用次数
	total := 0
	for _, cnt := range r.callCnt {
		total += cnt
	}
	if total >= MaxToolCallsPerTurn {
		r.mu.Unlock()
		i := NewIssue(IssueKindService, call.Name, "max_tool_calls_exceeded",
			fmt.Sprintf("本轮工具调用已达上限 %d，拒绝继续", MaxToolCallsPerTurn), true, ResolutionRetry)
		return "", true, &i
	}

	r.mu.Unlock()

	// 闸 3：独立校验（在锁外执行，校验失败不计入 callCnt / onceDone）
	if t.Check != nil {
		if iss := t.Check(call.Arguments); iss != nil {
			return "", true, iss
		}
	}

	// 校验通过后才计数 / 标记 once（避免 H-02 问题）
	r.mu.Lock()
	r.callCnt[call.Name]++
	if t.Once {
		r.onceDone[call.Name] = true
	}
	r.mu.Unlock()

	// 推送 running 事件（带 turnID，H-04 修复）
	emit(ToolEvent{
		Type:   "tool",
		TurnID: turnID,
		CallID: call.ID,
		Tool:   call.Name,
		Status: "running",
	})

	// 执行
	if err := ctx.Err(); err != nil {
		emit(ToolEvent{Type: "tool", TurnID: turnID, CallID: call.ID, Tool: call.Name, Status: "error", Detail: "本轮已停止"})
		return `{"error":"本轮已停止"}`, false, nil
	}
	out, err := t.Execute(ctx, call.Arguments, emit)
	if err != nil {
		emit(ToolEvent{Type: "tool", TurnID: turnID, CallID: call.ID, Tool: call.Name, Status: "error", Detail: err.Error()})
		data, _ := json.Marshal(map[string]string{"error": err.Error()})
		return string(data), false, nil
	}
	if t.Once && t.Noop != nil && t.Noop(out) {
		r.mu.Lock()
		delete(r.onceDone, call.Name)
		r.mu.Unlock()
	}

	emit(ToolEvent{Type: "tool", TurnID: turnID, CallID: call.ID, Tool: call.Name, Status: "done"})
	return out, false, nil
}

// GetDefinitions 返回所有工具的 schema（供 LLM 使用）
func (r *ToolRegistry) GetDefinitions() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]map[string]any, 0, len(r.tools))
	for _, t := range r.tools {
		result = append(result, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"parameters":  t.Parameters,
		})
	}
	return result
}

// ─────────────────────────────────────────────
// 内置工具注册
// ─────────────────────────────────────────────

// RegisterAvatarTools 注册数字人口播场景的工具集
// 按项目阶段动态裁剪：projectPhase 决定哪些工具可用。
// state 提供只读项目数据（evidence 工具的数据源）；
// mem 提供跨对话记忆读写，可为 nil（此时 memory 工具降级为不播记忆）；
// projectID 用于按项目隔离记忆；
// runner 是工作流执行通道，为 nil 时 plan_project 只能记录计划不能真正执行。
func RegisterAvatarTools(reg *ToolRegistry, projectPhase ProjectPhase, state ProjectState, mem *memoryStore, projectID string, runner ProjectRunner) {
	// advice — 总是可用，纯文字建议，不触发节点
	reg.Register(&Tool{
		Name:        "advice",
		Description: "提供建议或解答用户疑问，不触发任何 Workflow 节点，不消费额度",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reply": map[string]any{"type": "string", "description": "建议内容"},
			},
			"required": []string{"reply"},
		},
		Execute: func(ctx context.Context, args map[string]any, emit EventEmitter) (string, error) {
			reply, _ := args["reply"].(string)
			return fmt.Sprintf(`{"reply":%q}`, reply), nil
		},
	})

	// plan_project — 总是可用，once:true
	reg.Register(&Tool{
		Name:        "plan_project",
		Description: "提交制作计划并启动执行。目标节点是需要执行到的位置（如 F02、F05、F12），执行在后台异步进行，本工具返回的是「已提交」而不是「已完成」。",
		Once:        true,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target":    map[string]any{"type": "string", "description": "目标节点，如 F01.4、F02、F12"},
				"reason":    map[string]any{"type": "string", "description": "为什么需要执行到此节点"},
				"rerunFrom": map[string]any{"type": "string", "description": "从哪个节点重新执行（可选）"},
			},
			"required": []string{"target", "reason"},
		},
		Execute: func(ctx context.Context, args map[string]any, emit EventEmitter) (string, error) {
			target := normalizeNodeID(stringArg(args, "target"))
			reason := stringArg(args, "reason")
			rerunFrom := normalizeNodeID(stringArg(args, "rerunFrom"))

			// 计划需要落成记忆：否则下一轮对话模型不知道已经规划到哪
			if mem != nil {
				summary := "计划执行到 " + target + "，原因：" + reason
				if rerunFrom != "" {
					summary += "，从 " + rerunFrom + " 重跑"
				}
				mem.Remember(projectID, MemoryCheckpoint, summary)
			}

			// 没有注入执行通道时，明确告知不能执行——不要谎报已提交
			if runner == nil {
				out, _ := json.Marshal(map[string]any{
					"submitted": false,
					"target":    target,
					"note":      "执行通道不可用，计划已记录但未启动执行。请如实告知用户当前无法自动执行节点。",
				})
				return string(out), nil
			}

			// 从更早的节点重跑时，以 rerunFrom 为起点
			start := target
			if rerunFrom != "" {
				start = rerunFrom
			}

			if err := runner.Run(ctx, projectID, start); err != nil {
				// 把拒绝原因回灌给模型，让它向用户解释，而不是静默失败
				out, _ := json.Marshal(map[string]any{
					"submitted": false,
					"target":    start,
					"error":     err.Error(),
					"note":      "执行被拒绝，请把原因如实告诉用户，不要声称已经开始。",
				})
				return string(out), nil
			}

			// 已提交：先广播一次占位状态，真实进度由执行引擎后续推送
			emit(map[string]any{
				"type":      "plan",
				"target":    start,
				"reason":    reason,
				"rerunFrom": rerunFrom,
			})
			emit(map[string]any{
				"type":   "node_status",
				"node":   start,
				"status": "running",
			})

			out, _ := json.Marshal(map[string]any{
				"submitted": true,
				"target":    start,
				"note":      "执行已在后台启动，尚未完成。不要声称节点已完成，进度会由系统推送。",
			})
			return string(out), nil
		},
	})

	// evidence — 总是可用，读取项目数据
	reg.Register(&Tool{
		Name:        "evidence",
		Description: "检索项目数据：脚本段落、素材需求、分镜概要等。只读，不修改数据。query 为空表示取全部。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":    map[string]any{"type": "string", "description": "检索关键词，留空则返回全部"},
				"dataType": map[string]any{"type": "string", "enum": []string{"script", "material", "shot", "all"}},
			},
			"required": []string{"query"},
		},
		Execute: func(ctx context.Context, args map[string]any, emit EventEmitter) (string, error) {
			if state == nil {
				return `{"error":"项目数据不可用"}`, nil
			}
			query := stringArg(args, "query")
			dataType := stringArg(args, "dataType")
			if dataType == "" {
				dataType = "all"
			}

			result := map[string]any{}
			if dataType == "script" || dataType == "all" {
				segments, err := state.GetScriptSegments(query)
				if err != nil {
					return "", fmt.Errorf("检索脚本段落失败: %w", err)
				}
				result["segments"] = segments
			}
			if dataType == "material" || dataType == "all" {
				needs, err := state.GetMaterialNeeds(query)
				if err != nil {
					return "", fmt.Errorf("检索素材需求失败: %w", err)
				}
				result["materialNeeds"] = needs
			}
			if dataType == "shot" || dataType == "all" {
				shots, err := state.GetShots(query)
				if err != nil {
					return "", fmt.Errorf("检索分镜失败: %w", err)
				}
				result["shots"] = shots
			}

			out, err := json.Marshal(result)
			if err != nil {
				return "", err
			}
			return string(out), nil
		},
	})

	// memory — 总是可用
	reg.Register(&Tool{
		Name:        "memory",
		Description: "读写长期记忆（preference/fact/decision/checkpoint），跨对话保留用户偏好和关键决策",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":  map[string]any{"type": "string", "enum": []string{"remember", "forget", "list"}},
				"kind":    map[string]any{"type": "string", "enum": []string{"preference", "fact", "decision", "checkpoint"}},
				"content": map[string]any{"type": "string"},
			},
			"required": []string{"action"},
		},
		Execute: func(ctx context.Context, args map[string]any, emit EventEmitter) (string, error) {
			if mem == nil {
				return `{"ok":false,"note":"记忆存储未启用"}`, nil
			}
			action := stringArg(args, "action")
			rawKind := stringArg(args, "kind")
			kind := parseMemoryKind(rawKind)

			switch action {
			case "remember":
				content := stringArg(args, "content")
				if content == "" {
					return `{"ok":false,"note":"remember 需要 content"}`, nil
				}
				entry := mem.Remember(projectID, kind, content)
				out, _ := json.Marshal(map[string]any{
					"ok": true, "id": entry.ID, "kind": entry.Kind,
					"total": mem.Count(projectID),
				})
				return string(out), nil

			case "forget":
				// 未指定 kind 表示清空该项目全部记忆
				if rawKind == "" {
					kind = ""
				}
				removed := mem.Forget(projectID, kind)
				out, _ := json.Marshal(map[string]any{
					"ok": true, "removed": removed, "total": mem.Count(projectID),
				})
				return string(out), nil

			case "list":
				out, _ := json.Marshal(map[string]any{
					"ok": true, "memories": mem.All(projectID),
				})
				return string(out), nil

			default:
				return `{"ok":false,"note":"action 必须是 remember / forget / list"}`, nil
			}
		},
	})

	// edit_script — 有脚本段落时可用
	if projectPhase >= PhaseHasScript {
		reg.Register(&Tool{
			Name:        "edit_script",
			Description: "修改文案段落，生成提案。不直接写入数据，需用户确认后执行。rowIds 是段落 ID（如 p1、p2）。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"rowIds":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "要修改的段落 ID"},
					"changes": map[string]any{"type": "string", "description": "拟修改内容（自然语言描述）"},
					"reason":  map[string]any{"type": "string"},
				},
				"required": []string{"rowIds", "changes"},
			},
			Execute: buildProposalTool(state, "edit_script", "文案修改提案："),
		})
	}

	// edit_voice — 有配音段时可用
	if projectPhase >= PhaseHasVoice {
		reg.Register(&Tool{
			Name:        "edit_voice",
			Description: "调整配音参数（音色/语速/语调），重新生成对应段 TTS。需用户确认。segmentIds 是段落 ID。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"segmentIds": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "要重新配音的段落 ID"},
					"changes":    map[string]any{"type": "string"},
				},
				"required": []string{"segmentIds", "changes"},
			},
			Execute: buildProposalTool(state, "edit_voice", "配音修改提案："),
		})
	}

	// edit_material — 有素材需求时可用
	if projectPhase >= PhaseHasMaterial {
		reg.Register(&Tool{
			Name:        "edit_material",
			Description: "调整 F05 素材需求（类型/时长/内容要求）。需用户确认，确认后重做对应 F06-F08。needIds 是素材需求 ID。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"needIds": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "要修改的素材需求 ID"},
					"changes": map[string]any{"type": "string"},
				},
				"required": []string{"needIds", "changes"},
			},
			Execute: buildProposalTool(state, "edit_material", "素材需求修改提案："),
		})
	}

	// edit_shot — 有分镜时可用
	if projectPhase >= PhaseHasShot {
		reg.Register(&Tool{
			Name:        "edit_shot",
			Description: "调整 F10 分镜方案（布局/素材/时长）。需用户确认，确认后只重渲受影响镜头。shotIds 是分镜 ID。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"shotIds": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "要修改的分镜 ID"},
					"changes": map[string]any{"type": "string"},
				},
				"required": []string{"shotIds", "changes"},
			},
			Execute: buildProposalTool(state, "edit_shot", "分镜修改提案："),
		})
	}

	// run_control — 有活跃运行时可用
	if projectPhase >= PhaseRunning {
		reg.Register(&Tool{
			Name:        "run_control",
			Description: "控制当前运行的工作流：暂停/继续/停止/批准确认节点",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{"type": "string", "enum": []string{"pause", "resume", "stop", "approve"}},
					"reason": map[string]any{"type": "string"},
				},
				"required": []string{"action"},
			},
			Execute: func(ctx context.Context, args map[string]any, emit EventEmitter) (string, error) {
				// run_control 会真实驱动工作流运行，需付费与状态机配合，
				// 在 Runner 接通前只记录意图并如实告知，不谎报已执行。
				action := stringArg(args, "action")
				emit(map[string]any{
					"type":   "run_control",
					"action": action,
					"reason": stringArg(args, "reason"),
				})
				out, _ := json.Marshal(map[string]any{
					"recorded": true,
					"action":   action,
					"note":     "运行控制请求已发给界面，尚未确认执行结果",
				})
				return string(out), nil
			},
		})
	}
}

// ─────────────────────────────────────────────
// 工具实现辅助
// ─────────────────────────────────────────────

// buildProposalTool 生成「只出提案、不写数据」的工具实现。
//
// 四个 edit_* 工具行为一致：校验目标 ID 是否存在，组装提案描述，
// 交给 Session 层写入 proposal 表并让用户确认。
// 这里不做写入，是为了守住「修改必须经用户确认」的规则。
func buildProposalTool(state ProjectState, toolName, summaryPrefix string) func(context.Context, map[string]any, EventEmitter) (string, error) {
	return func(ctx context.Context, args map[string]any, emit EventEmitter) (string, error) {
		if state == nil {
			return `{"proposal":"failed","note":"项目数据不可用，无法生成提案"}`, nil
		}

		targetIDs := stringSliceArg(args, "rowIds", "segmentIds", "needIds", "shotIds")
		if len(targetIDs) == 0 {
			return `{"proposal":"failed","note":"未提供要修改的目标 ID"}`, nil
		}
		changes := stringArg(args, "changes")
		if changes == "" {
			return `{"proposal":"failed","note":"未提供修改内容"}`, nil
		}

		// 校验目标确实存在：让模型拿到明确反馈，而不是生成一个无法应用的提案
		known, err := knownTargetIDs(state, toolName)
		if err != nil {
			return "", fmt.Errorf("读取项目数据失败: %w", err)
		}
		valid := make([]string, 0, len(targetIDs))
		unknown := make([]string, 0)
		for _, id := range targetIDs {
			if known[id] {
				valid = append(valid, id)
			} else {
				unknown = append(unknown, id)
			}
		}
		if len(valid) == 0 {
			out, _ := json.Marshal(map[string]any{
				"proposal": "failed",
				"note":     "目标 ID 在项目中不存在，请先用 evidence 检索确认 ID",
				"unknown":  unknown,
			})
			return string(out), nil
		}

		lines := make([]string, 0, len(valid)+1)
		lines = append(lines, summaryPrefix+changes)
		for _, id := range valid {
			lines = append(lines, "· 影响 "+id)
		}

		emit(map[string]any{
			"type":   "proposal_draft",
			"tool":   toolName,
			"rowIds": valid,
			"lines":  lines,
		})

		out, _ := json.Marshal(map[string]any{
			"proposal": "pending",
			"rowIds":   valid,
			"lines":    lines,
			"unknown":  unknown,
			"note":     "提案已生成，需用户确认后才能写入；现在还没有修改任何数据",
		})
		return string(out), nil
	}
}

// knownTargetIDs 返回该工具可操作的目标 ID 集合
func knownTargetIDs(state ProjectState, toolName string) (map[string]bool, error) {
	known := map[string]bool{}
	switch toolName {
	case "edit_script", "edit_voice":
		segments, err := state.GetScriptSegments("")
		if err != nil {
			return nil, err
		}
		for _, s := range segments {
			known[s.ID] = true
		}
	case "edit_material":
		needs, err := state.GetMaterialNeeds("")
		if err != nil {
			return nil, err
		}
		for _, n := range needs {
			known[n.ID] = true
		}
	case "edit_shot":
		shots, err := state.GetShots("")
		if err != nil {
			return nil, err
		}
		for _, s := range shots {
			known[s.ID] = true
		}
	}
	return known, nil
}

// stringArg 读取字符串参数（缺失或类型不符时返回空串）
func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// normalizeNodeID 把模型输出的节点名归一成引擎认识的格式。
// 模型可能写成 "f2"、"F2"、"F02"、"F02 "，引擎只接受 "F02" 这种两位补零大写形式。
// 无法识别时原样返回，交给执行引擎报错——静默改写成别的节点更危险。
func normalizeNodeID(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	// 已经是 F02 / F01.4 这类合法形式
	if len(s) >= 2 && s[0] == 'F' {
		rest := s[1:]
		// 支持 F01.4 这种带子步骤的写法，只归一主节点号
		main := rest
		suffix := ""
		if idx := strings.IndexByte(rest, '.'); idx >= 0 {
			main, suffix = rest[:idx], rest[idx:]
		}
		if main != "" && isAllDigits(main) {
			if n := len(main); n == 1 {
				return "F0" + main + suffix
			}
		}
	}
	return s
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// stringSliceArg 依次尝试多个键，返回第一个非空字符串数组。
// 工具名不同导致目标 ID 的参数名不同（rowIds/segmentIds/needIds/shotIds），
// 统一在这里兼容，避免每个工具各写一遍解析。
func stringSliceArg(args map[string]any, keys ...string) []string {
	for _, key := range keys {
		raw, ok := args[key]
		if !ok {
			continue
		}
		list, ok := raw.([]any)
		if !ok {
			continue
		}
		out := make([]string, 0, len(list))
		for _, v := range list {
			s := strings.TrimSpace(fmt.Sprint(v))
			if s != "" && s != "<nil>" {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// ProjectPhase 项目阶段（决定哪些工具可用）
type ProjectPhase int

const (
	PhaseInitial     ProjectPhase = 0  // 新项目，只有基础工具
	PhaseHasScript   ProjectPhase = 10 // F02 文案已生成
	PhaseHasVoice    ProjectPhase = 20 // F04 配音已生成
	PhaseHasMaterial ProjectPhase = 30 // F05 素材需求已生成
	PhaseHasShot     ProjectPhase = 40 // F10 分镜已生成
	PhaseRunning     ProjectPhase = 50 // 有活跃 Workflow 运行
)
