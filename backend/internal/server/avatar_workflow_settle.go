package server

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/models"
)

// settleStoppedOrders closes supplier orders left open in runs that are no
// longer executing. Stopping a run while a video order is being polled leaves
// the order "submitted"; nothing queries it again, so ensureProjectOrdersSettled
// refuses every new run on the project. This asks the provider for the order's
// actual state (read-only, never resubmits) and records a terminal answer.
// Orders still rendering, unreachable, or without a task id stay open.
func (w *avatarWorkflowService) settleStoppedOrders(ctx context.Context, owner, project uuid.UUID) error {
	provider := w.app.avatarMVP.provider
	if provider == nil {
		return nil
	}
	var runs []models.WorkflowRun
	if err := w.app.db.WithContext(ctx).Select("id").Where("project_id=? AND user_id=?", project, owner).Find(&runs).Error; err != nil {
		return err
	}
	for _, run := range runs {
		s, err := w.load(ctx, owner, run.ID.String())
		if err != nil {
			continue
		}
		// A live run settles its own orders by polling.
		if s.Status == "running" || s.Status == "stopping" {
			continue
		}
		settled := map[string]avatar.ProviderResult{}
		for _, step := range s.Steps {
			if step.Status == "succeeded" || step.ProviderTaskID == "" || step.Submission != "submitted" {
				continue
			}
			result, err := provider.Query(ctx, step.ProviderTaskID)
			if err != nil {
				continue
			}
			switch result.Status {
			case "SUCCESS", "FAILED", "FAILURE", "CANCELLED", "CANCELED":
				settled[step.ID] = result
			}
		}
		if len(settled) == 0 {
			continue
		}
		if err := w.update(s.ID, func(current *avatar.WorkflowState) error {
			for i := range current.Steps {
				step := &current.Steps[i]
				result, ok := settled[step.ID]
				if !ok || step.Submission != "submitted" || (result.TaskID != "" && result.TaskID != step.ProviderTaskID) {
					continue
				}
				if len(result.Usage) > 0 {
					step.Usage = result.Usage
					var usage map[string]any
					_ = json.Unmarshal(result.Usage, &usage)
					for _, name := range []string{"thirdPartyConsumeMoney", "consumeMoney", "consumeCoins"} {
						if usage[name] != nil {
							step.CostStatus = "reported"
						}
					}
				}
				if result.Status == "SUCCESS" {
					step.Submission = "completed"
					current.AddWarning(step.ID, "provider_notice", "运行停止时该视频订单仍在生成，现已查单确认完成并计费；视频未下载到本次运行，如需使用请恢复该运行")
				} else {
					step.Submission = "terminal_failed"
					current.AddWarning(step.ID, "provider_notice", "运行停止后查单确认该视频订单失败，已结清")
				}
				step.PreserveOrder()
			}
			current.RefreshBilling()
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
