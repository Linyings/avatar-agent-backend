package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/narratoai/backend-go/internal/agentpackages"
	"strings"
)

// PackageHost owns authorization, persistent proposals and workflow admission.
// owner is carried through every call, including model-initiated calls.
type PackageHost interface {
	Invoke(context.Context, string, string, string, map[string]any, string) (any, error)
	Confirm(context.Context, string, string, string, int) (any, error)
}

func packagePlanNoop(result string) bool {
	var out struct {
		Submitted *bool           `json:"submitted"`
		Status    string          `json:"status"`
		Proposal  json.RawMessage `json:"proposal"`
	}
	if json.Unmarshal([]byte(result), &out) != nil {
		return false
	}
	return out.Submitted != nil && !*out.Submitted && out.Status != "pending" && len(out.Proposal) == 0
}

func RegisterPackageTools(reg *ToolRegistry, host PackageHost, owner, project, turn string) {
	if host == nil {
		return
	}
	manifests, err := agentpackages.Load()
	if err != nil {
		return
	}
	for _, manifest := range manifests {
		p := manifest
		reg.Register(&Tool{Name: p.Tool, Description: p.Description, Parameters: p.InputSchema,
			Check: func(args map[string]any) *Issue {
				if err := agentpackages.Validate(p.InputSchema, args); err != nil {
					i := NewIssue(IssueKindFormat, p.Tool, "invalid_arguments", err.Error(), true, ResolutionRepairContent)
					return &i
				}
				return nil
			},
			Execute: func(ctx context.Context, args map[string]any, emit EventEmitter) (string, error) {
				result, err := host.Invoke(ctx, owner, project, p.ID, args, turn+"/"+p.ID)
				if err != nil {
					return "", err
				}
				data, err := json.Marshal(result)
				return string(data), err
			},
		})
	}
	nodes := []string{}
	for _, p := range manifests {
		if p.Entrypoint == "run" {
			nodes = append(nodes, p.Node)
		}
	}
	resolve := func(raw string) string {
		for _, p := range manifests {
			if p.Node != "" && (strings.EqualFold(strings.TrimSpace(raw), p.ID) || strings.EqualFold(strings.TrimSpace(raw), p.Tool) || strings.TrimSpace(raw) == p.Title) {
				return p.Node
			}
		}
		return normalizeNodeID(raw)
	}
	check := func(args map[string]any) *Issue {
		invalid := func(message string) *Issue {
			i := NewIssue(IssueKindFormat, "plan_project", "invalid_arguments", message, true, ResolutionRepairContent)
			return &i
		}
		for key := range args {
			if key != "target" && key != "reason" && key != "rerunFrom" {
				return invalid("不支持字段 " + key + "；重做请传 target 和 rerunFrom")
			}
		}
		if stringArg(args, "reason") == "" {
			return invalid("请用 reason 说明执行或重做原因")
		}
		for _, key := range []string{"target", "rerunFrom"} {
			if _, exists := args[key]; !exists && key == "rerunFrom" {
				continue
			}
			node := resolve(stringArg(args, key))
			valid := false
			for _, n := range nodes {
				if node == n {
					valid = true
				}
			}
			if node == "F01" {
				return invalid("F01 是制作需求，请使用 avatar_configure 编辑；不会触发生成。生成从 F01.4 开始。")
			}
			if !valid {
				return invalid(fmt.Sprintf("%s=%q 无效；可选节点：%s。请修正参数后重试，本次未执行。", key, stringArg(args, key), strings.Join(nodes, "、")))
			}
		}
		if from := resolve(stringArg(args, "rerunFrom")); from != "" && from > resolve(stringArg(args, "target")) {
			return invalid("rerunFrom 不得晚于 target")
		}
		return nil
	}
	reg.Register(&Tool{Name: "plan_project", Description: "执行到 target；从已完成步骤重做必须同时传 rerunFrom（起点）和 target（终点，全部后续为 F12），生成待确认的重做方案。F01 用 avatar_configure。修改节点内容用 avatar_edit_node 或 edit_*。", Parameters: map[string]any{"type": "object", "properties": map[string]any{"target": map[string]any{"type": "string", "enum": nodes, "description": "执行终点编号；从 F04 开始全部重做时为 F12"}, "rerunFrom": map[string]any{"type": "string", "enum": nodes, "description": "重做起点编号，例如 F04；省略表示继续执行，不强制重做已完成内容"}, "reason": map[string]any{"type": "string"}}, "required": []string{"target", "reason"}, "additionalProperties": false}, Check: check, Once: true, Noop: packagePlanNoop, Execute: func(ctx context.Context, args map[string]any, emit EventEmitter) (string, error) {
		target := resolve(stringArg(args, "target"))
		if from := resolve(stringArg(args, "rerunFrom")); from != "" {
			result, err := host.Invoke(ctx, owner, project, "avatar.rerun", map[string]any{"from": from, "target": target, "changes": stringArg(args, "reason")}, turn+"/plan")
			if err != nil {
				return "", err
			}
			raw, err := json.Marshal(result)
			return string(raw), err
		}
		for _, p := range manifests {
			if p.Node == target && p.Entrypoint == "run" {
				result, err := host.Invoke(ctx, owner, project, p.ID, map[string]any{}, turn+"/plan")
				if err != nil {
					return "", err
				}
				raw, err := json.Marshal(result)
				return string(raw), err
			}
		}
		return "", fmt.Errorf("节点未注册，请重新读取 avatar_inspect")
	}})
}
