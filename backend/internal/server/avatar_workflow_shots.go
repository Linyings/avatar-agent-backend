package server

import (
	"encoding/json"
	"fmt"
	"github.com/narratoai/backend-go/internal/avatar"
	"os"
	"path/filepath"
)

// A completed F10 response is paid evidence. Revalidate it locally on explicit
// retry instead of making another model request for a source-range error.
func (w *avatarWorkflowService) restoreCompletedShots(s *avatar.WorkflowState, step avatar.WorkflowStep) error {
	raw, err := os.ReadFile(filepath.Join(w.dir(s.ID), "model-results", fmt.Sprintf("%s-attempt-%d.json", avatar.Digest(step.ID)[:16], step.Attempt)))
	if err != nil {
		return fmt.Errorf("已完成的分镜结果文件不可用；未重新调用模型，请检查服务端任务目录")
	}
	var out struct {
		Shots []avatar.WorkflowShot `json:"shots"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("已保存分镜结果无法解析；未重新调用模型")
	}
	avatar.MapAvatarShotSources(out.Shots, s.Segments, s.Materials)
	avatar.NormalizeShotPresentation(out.Shots)
	if err = avatar.ValidateWorkflowShots(out.Shots, s.Segments, s.Materials, s.Duration); err != nil {
		return fmt.Errorf("已保存分镜仍无法使用，未重新调用模型：%w", err)
	}
	s.Shots = out.Shots
	s.AddWarning("F10", "quality", "已复用上次模型返回的分镜并重新校验源区间；没有重新生成素材或调用分镜模型，请检查画面设计")
	return nil
}
