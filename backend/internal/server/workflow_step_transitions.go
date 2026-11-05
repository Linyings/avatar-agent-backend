package server

import (
	"fmt"
	"time"

	"github.com/narratoai/backend-go/internal/models"
)

const workflowStepStatusFailed = "failed"

func validateWorkflowStepTransition(step *models.WorkflowStepExecution, input WorkflowStepUpdateInput, now time.Time) (bool, error) {
	current := step.Status
	next := input.Status
	switch current {
	case workflowRunStatusSucceeded:
		if next == workflowRunStatusSucceeded {
			return true, nil
		}
	case workflowRunStatusRunning:
		if next == workflowRunStatusRunning {
			return false, nil
		}
		if isWorkflowStepRetryableStatus(next) || next == workflowRunStatusSucceeded {
			if workflowStepLeaseOwnedByClient(step, input.ClientInstanceID, now) {
				return false, nil
			}
		}
	case workflowRunStatusInterrupted, workflowStepStatusFailed, workflowRunStatusBlocked:
		if next == current {
			return true, nil
		}
		if next == workflowRunStatusRunning {
			return false, nil
		}
	}
	return false, fmt.Errorf("%w: %s to %s", ErrWorkflowStepTransitionInvalid, current, next)
}

func isWorkflowStepRetryableStatus(status string) bool {
	return status == workflowRunStatusInterrupted || status == workflowStepStatusFailed || status == workflowRunStatusBlocked
}
