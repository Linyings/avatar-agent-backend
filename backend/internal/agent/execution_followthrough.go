package agent

import (
	"encoding/json"
	"strings"
)

func executionRequested(message string) bool {
	// This guard only asks the model to finish the user's existing instruction;
	// it never selects or invokes a generation tool itself.
	if contains(message, "不要制作", "不要生成", "暂不", "先不", "仅建议", "只给建议", "只要建议", "只规划", "仅规划", "先规划", "只分析", "先帮我规划", "不要执行") {
		return false
	}
	return contains(message, "帮我做", "制作", "开始", "继续", "生成", "执行", "重做")
}

func needsClarification(reply string) bool {
	// A future approval ("生成后需要你确认") is not a current blocker.
	return contains(reply, "？", "?", "请提供", "请补充", "请上传", "请选择", "未配置", "无法", "不能", "失败", "缺少")
}

// Accepted run or a concrete proposal is a real stopping point. Saving config
// and reading a snapshot alone do not fulfill a request to produce a video.
func executionResolved(result string) bool {
	var v struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
		Action string `json:"action"`
		Run    *struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"run"`
		Proposal *struct {
			ID      string `json:"id"`
			Applied bool   `json:"applied"`
		} `json:"proposal"`
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(result), &v) != nil {
		return false
	}
	if v.Error != "" {
		return false
	}
	active := func(status string) bool {
		return status == "running" || status == "stopping" || status == "awaiting_confirmation"
	}
	// Merely inspecting a completed/failed/paused run cannot fulfill a new
	// execution request. Explicit stop/pause actions are valid stopping points.
	return (v.RunID != "" && (active(v.Status) || v.Action == "stop" || v.Action == "pause")) ||
		(v.Run != nil && v.Run.ID != "" && active(v.Run.Status)) ||
		(v.Proposal != nil && v.Proposal.ID != "" && !v.Proposal.Applied)
}

func toolError(result string) string {
	var v struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(result), &v) == nil {
		return strings.TrimSpace(v.Error)
	}
	return ""
}
