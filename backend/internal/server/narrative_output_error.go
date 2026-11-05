package server

import "strings"

// A missing final answer is distinct from a schema error. Keep this explanation
// useful even for clients which do not support structured retry metadata yet.
func narrativeInvalidOutputMessage(req map[string]any, content string, completed map[string]any) string {
	if completed["finish_reason"] == "length" {
		return "主模型达到单次输出上限，剧情结果不完整；可重试，已完成的素材预处理不受影响"
	}
	if strings.TrimSpace(content) == "" {
		return "主模型未返回最终正文（空结果），无法生成关键剧情；可重试，已完成的素材预处理不受影响"
	}
	if isMovieImportanceSelectionRequest(req) {
		return "关键剧情筛选未产生带有效时间、优先级和理由的事件，请重试"
	}
	if isMovieBackgroundResearchSummaryRequest(req) {
		return "公开资料整理未产生作品概览和可用核心事实，请重试"
	}
	return "剧情理解未产生带有效时间戳和描述的事件，请重试"
}
