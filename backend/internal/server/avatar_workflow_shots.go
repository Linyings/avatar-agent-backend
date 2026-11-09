package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/narratoai/backend-go/internal/avatar"
)

// Only an explicit retry may replace an interrupted F10 with a local plan.
// This never resolves the original supplier charge or permits a resubmission.
func (w *avatarWorkflowService) restoreInterruptedShots(s *avatar.WorkflowState, step *avatar.WorkflowStep) error {
	assembled := false
	for _, prior := range s.Steps {
		assembled = assembled || (prior.ID == "F09" && prior.Status == "succeeded")
	}
	if !assembled || !s.AudioConfirmed || s.StopRequested || len(s.Segments) == 0 || len(s.Materials) == 0 {
		return fmt.Errorf("分镜请求状态待确认，且本地素材尚未就绪；未重复调用模型")
	}
	for _, material := range s.Materials {
		path, err := safeWorkflowFile(w.dir(s.ID), material.Path)
		if err != nil {
			return fmt.Errorf("恢复分镜所需素材路径不可用；未重新生成素材")
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("恢复分镜所需素材文件缺失；未重新生成素材")
		}
	}
	recovery := step.LocalRecovery
	if len(s.Shots) == 0 {
		// A crash can happen after the response file is written but before its
		// database commit. Prefer that evidence over a deterministic fallback.
		file := filepath.Join(w.dir(s.ID), "model-results", fmt.Sprintf("%s-attempt-%d.json", avatar.Digest(step.ID)[:16], step.Attempt))
		if _, err := os.Stat(file); err == nil {
			if err := w.restoreCompletedShots(s, *step); err != nil {
				return err
			}
			recovery = "saved_response"
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("无法检查已保存分镜；未重复调用模型")
		} else {
			s.Shots = avatar.PresentationFallback(s.Segments, s.Materials)
			recovery = "conservative_local_plan"
		}
	} else if recovery == "" {
		recovery = "existing_plan"
	}
	avatar.EnrichShotPresentation(s.Shots, s.Segments, s.Materials, s.Needs)
	avatar.MapAvatarShotSources(s.Shots, s.Segments, s.Materials)
	if err := avatar.ValidateWorkflowShots(s.Shots, s.Segments, s.Materials, s.Duration); err != nil {
		return fmt.Errorf("本地恢复分镜校验未通过；未重复调用模型：%w", err)
	}
	step.PreserveOrder()
	step.LocalRecovery = recovery
	s.AddWarning("F10", "quality", "分镜请求被中断，已复用保存结果或按现有素材生成本地保守分镜；没有新增模型调用，原请求费用仍待核对，请检查画面设计")
	return nil
}

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
