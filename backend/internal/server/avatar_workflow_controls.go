package server

import (
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/narratoai/backend-go/internal/avatar"
)

func workflowControlReadOnly(s avatar.WorkflowState, action string) bool {
	if action != "retry" && action != "resume" {
		return false
	}
	switch s.Status {
	case "awaiting_confirmation", "running", "stopping":
		return true
	}
	return false
}

// A stale retry/continue request is a status response, not a provider failure.
// Approval still requires the versioned confirmation endpoint and user choice.
func workflowControlResult(s avatar.WorkflowState) gin.H {
	out := gin.H{"submitted": false, "run_id": s.ID, "status": s.Status, "target": s.TargetNode}
	switch s.Status {
	case "awaiting_confirmation":
		out["confirmation"] = s.Confirmation
		out["requires_user_choice"] = true
		out["note"] = "文案已准备好，请在对话中的确认卡片查看并确认后继续。无需重试。"
		if s.Confirmation == "audio" {
			completed := 0
			for _, seg := range s.Segments {
				if seg.Audio != "" && seg.Duration > 0 {
					completed++
				}
			}
			out["audio_completed"], out["audio_total"] = completed, len(s.Segments)
			out["note"] = "配音内容尚未齐全，请刷新制作状态。"
			if completed > 0 && completed == len(s.Segments) {
				out["note"] = fmt.Sprintf("全部 %d 段配音已完成，无需重试。请在对话中的配音卡片选择先试听确认，或点击「直接继续制作」。这是整批配音的一次选择，不需要逐段确认。", completed)
			}
		}
		if s.Confirmation == "shots" {
			out["note"] = "全部镜头已生成融合预览，请到镜头审阅节点查看，确认后开始正式渲染。"
		}
		if s.Confirmation == "final" {
			out["note"] = "完整成片已生成，请完整审阅后确认交付。"
		}
	case "running":
		out["note"] = "后台正在继续制作，请查看当前步骤进度，无需重复启动。"
	case "stopping":
		out["note"] = "正在保存并停止当前制作，请等待状态更新。"
	}
	return out
}
