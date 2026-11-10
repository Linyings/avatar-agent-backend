package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
)

// This server-only switch permits an explicit user retry during local testing.
// It never starts a worker on reads, on deployment, or in a retry loop.
func workflowTestingMode() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("AVATAR_WORKFLOW_TEST_MODE")), "true")
}

func unknownWithoutTask(step avatar.WorkflowStep) bool {
	return step.Status != "succeeded" && step.LocalRecovery == "" && step.ProviderTaskID == "" && (step.Submission == "unknown" || step.Submission == "submitting")
}

func pendingKnownTask(s avatar.WorkflowState) bool {
	for _, step := range s.Steps {
		if step.Status != "succeeded" && step.ProviderTaskID != "" && step.LocalRecovery == "" && step.Submission != "terminal_failed" {
			return true
		}
	}
	return false
}

func (w *avatarWorkflowService) retryRun(ctx context.Context, owner uuid.UUID, id, target, request string) (avatar.WorkflowState, error) {
	s, err := w.load(ctx, owner, id)
	if err != nil {
		return s, err
	}
	if target == "" {
		target = s.TargetNode
	}
	if target == "" {
		target = "F12"
	}
	if !isKnownWorkflowNode(target) {
		return s, fmt.Errorf("制作节点不存在")
	}
	if s.Status == "awaiting_confirmation" {
		return s, nil
	}
	recoveryKey := ""
	if workflowTestingMode() {
		if request == "" {
			request = "run-" + id
		}
		request = "test-retry/" + avatar.Digest(request)
		recoveryKey = request
		if slices.Contains(s.RecoveryRequests, recoveryKey) {
			return s, nil
		}
		project := uuid.MustParse(s.ProjectID)
		hash := avatar.Digest([]any{owner.String(), s.ProjectID, request})
		var prior models.WorkflowRun
		e := w.app.db.WithContext(ctx).Where("user_id=? AND project_id=? AND input_hash=?", owner, project, hash).First(&prior).Error
		if e == nil {
			return w.load(ctx, owner, prior.ID.String())
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return s, e
		}
		if s.Status != "failed" && s.Status != "interrupted" && s.Status != "submission_unknown" {
			return s, fmt.Errorf("当前运行无需重试，请查看最新制作状态")
		}
		_, input, e := w.project(ctx, owner, project)
		if e != nil {
			return s, e
		}
		restart := input.Revision != s.Input.Revision || s.ConfigID != workflowConfigID(w.configSnapshot())
		for _, step := range s.Steps {
			restart = restart || unknownWithoutTask(step)
		}
		if restart && !pendingKnownTask(s) {
			if e = w.validatePackageInput(&input, target); e != nil {
				return s, e
			}
			// A new run uses the current draft/default voice and inherits usable
			// approved content. The original uncertain attempt remains immutable.
			if s.Edits.MaterialOnly != "" && input.Revision == s.Input.Revision {
				edits := s.Edits
				edits.BaseRunID = s.ID
				edits.Needs = append([]avatar.MaterialNeed(nil), s.Needs...)
				return w.createTo(ctx, owner, project, request, input.Revision, target, edits)
			}
			return w.createTo(ctx, owner, project, request, input.Revision, target)
		}
	}
	if err = w.retryWithRequest(ctx, owner, id, target, recoveryKey); err != nil {
		return s, err
	}
	return w.load(ctx, owner, id)
}
