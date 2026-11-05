package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrGeneralResponseInvalid = errors.New("通用模式模型响应格式无效")

// Normalize only recognizable issue descriptions. Never drop an issue or turn
// a refusal into an accepted chapter plan, and never touch narration text.
func normalizeGeneralResponse(content string, request map[string]any) (string, error) {
	ctx, enabled := generalResponseContext(request)
	if !enabled {
		return content, nil
	}
	guard := generalResponseGuard{}
	if err := guard.append(content); err != nil {
		return content, err
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(jsonCandidate(content)), &output); err != nil {
		return content, nil
	}
	raw, exists := output["issues"]
	if !exists {
		return content, nil
	}
	issues, ok := raw.([]any)
	if !ok {
		return content, fmt.Errorf("%w: purpose=%v issues 必须是列表（收到 %T）", ErrGeneralResponseInvalid, ctx["purpose"], raw)
	}
	changed := false
	for i, issue := range issues {
		if value, ok := issue.(string); ok && strings.TrimSpace(value) != "" {
			continue
		}
		obj, ok := issue.(map[string]any)
		if !ok {
			return content, fmt.Errorf("%w: purpose=%v issues[%d] 不是有效的问题说明（%T）", ErrGeneralResponseInvalid, ctx["purpose"], i, issue)
		}
		parts := []string{}
		for key, value := range obj {
			switch key {
			case "message", "reason", "description", "detail", "correction", "suggestion", "action":
				if _, ok := value.(string); !ok {
					return content, fmt.Errorf("%w: issues[%d] 说明字段不是文字", ErrGeneralResponseInvalid, i)
				}
			case "chapterId", "chapter_id", "id", "code", "severity":
				if _, ok := value.(string); !ok {
					if _, ok := value.(float64); !ok {
						return content, fmt.Errorf("%w: issues[%d] 定位字段无效", ErrGeneralResponseInvalid, i)
					}
				}
			default:
				return content, fmt.Errorf("%w: issues[%d] 包含未知字段，不能丢弃问题", ErrGeneralResponseInvalid, i)
			}
		}
		for _, key := range []string{"message", "reason", "description", "detail", "correction", "suggestion", "action"} {
			if value, ok := obj[key].(string); ok && strings.TrimSpace(value) != "" {
				parts = append(parts, strings.TrimSpace(value))
			}
		}
		if len(parts) == 0 {
			return content, fmt.Errorf("%w: issues[%d] 缺少问题说明", ErrGeneralResponseInvalid, i)
		}
		prefix := ""
		for _, key := range []string{"chapterId", "chapter_id", "id", "code", "severity"} {
			if value, exists := obj[key]; exists {
				prefix += fmt.Sprintf("%s=%v；", key, value)
			}
		}
		issues[i] = prefix + strings.Join(parts, "；")
		changed = true
	}
	if !changed {
		return content, nil
	}
	output["issues"] = issues
	rawJSON, err := json.Marshal(output)
	return string(rawJSON), err
}

func generalResponseContext(request map[string]any) (map[string]any, bool) {
	ctx, ok := request["video_context"].(map[string]any)
	return ctx, ok && ctx["contract"] == "general-narration-v3"
}

type generalResponseGuard struct{ size, whitespace int }

func (g *generalResponseGuard) append(delta string) error {
	g.size += len(delta)
	if g.size > 512*1024 {
		return fmt.Errorf("%w: 输出超过大小限制", ErrGeneralResponseInvalid)
	}
	for _, c := range delta {
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			g.whitespace++
		} else {
			g.whitespace = 0
		}
		if g.whitespace > 4096 {
			return fmt.Errorf("%w: 连续空白输出异常", ErrGeneralResponseInvalid)
		}
	}
	return nil
}

func validateGeneralResponse(content string, request map[string]any) error {
	ctx, enabled := generalResponseContext(request)
	if !enabled {
		return nil
	}
	guard := generalResponseGuard{}
	if err := guard.append(content); err != nil {
		return err
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(jsonCandidate(content)), &output); err != nil || len(output) == 0 {
		return fmt.Errorf("%w: JSON 未闭合或为空", ErrGeneralResponseInvalid)
	}
	if output["placeholder"] == true {
		return fmt.Errorf("%w: 不允许占位结果", ErrGeneralResponseInvalid)
	}
	if raw, exists := output["issues"]; exists {
		if _, ok := raw.([]any); !ok {
			return fmt.Errorf("%w: issues 必须是列表", ErrGeneralResponseInvalid)
		}
	}
	if issues, ok := output["issues"].([]any); ok && len(issues) > 0 {
		for _, issue := range issues {
			if s, ok := issue.(string); !ok || strings.TrimSpace(s) == "" {
				return fmt.Errorf("%w: issues 包含非文字或空的问题说明", ErrGeneralResponseInvalid)
			}
		}
		return nil // A valid refusal/repair request is not an accepted edit.
	}
	array := func(key string) bool { v, ok := output[key].([]any); return ok && len(v) > 0 }
	valid := false
	switch ctx["purpose"] {
	case "retrieve":
		if rows, ok := output["units"].([]any); ok && len(rows) > 0 {
			valid = true
			seen := map[float64]bool{}
			for _, raw := range rows {
				row, ok := raw.(map[string]any)
				if !ok {
					valid = false
					break
				}
				id, ok := row["chapterId"].(float64)
				if !ok || id < 1 || id != float64(int(id)) || seen[id] {
					valid = false
					break
				}
				seen[id] = true
				candidates, ok := row["candidates"].([]any)
				if !ok || len(candidates) == 0 || len(candidates) > 3 {
					valid = false
					break
				}
			}
		}
	case "original-layout":
		valid = array("originals")
	case "match", "chapters":
		valid = array("chapters")
	case "script":
		text, ok := output["script"].(string)
		valid = ok && strings.TrimSpace(text) != ""
	case "match-review", "editorial-review":
		_, accepted := output["accepted"].(bool)
		_, issues := output["issues"].([]any)
		valid = accepted && issues
	}
	if !valid {
		return fmt.Errorf("%w: 缺少当前步骤必需字段", ErrGeneralResponseInvalid)
	}
	return nil
}
