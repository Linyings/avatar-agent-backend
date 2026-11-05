package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/prompts"
	"gorm.io/gorm"
)

var generalWorkflowStep = regexp.MustCompile(`^(generate-narration-script|match-narration-visuals|generate-voiceover)\.general-v2\.(plan-[0-2]|ground-[0-9]+-[0-2]|repair-ground-[0-9]+|(fit-text|review-text)-[0-9]+(-timing-[12])?(-1)?)$`)

var generalNarrationStep = regexp.MustCompile(`^(generate-narration-script\.general-v3\.(script|chapters|editorial-review|retrieve)|generate-voiceover\.general-v3\.(chapters|retrieve)|match-narration-visuals\.general-v3\.(retrieve|match|match-review|original-layout|editorial-review))$`)

var generalFitVoiceStep = regexp.MustCompile(`^generate-voiceover\.general-v2\.fit-voice-[0-9]+(-timing-[12])?$`)

// A reviewed timing rewrite changes the TTS input without changing its logical
// unit. Bind its execution to the actual text/voice/model fingerprint, while
// retaining run ownership and the shared in-flight/unknown-result guard.
func (a *App) generalFitVoiceIdentity(ctx context.Context, userID uuid.UUID, projectID *uuid.UUID, identity *workflowRequestIdentity, fingerprint string) (*workflowRequestIdentity, error) {
	if identity == nil || !generalFitVoiceStep.MatchString(identity.WorkflowStepID) {
		return identity, nil
	}
	run, _, err := a.loadMovieHarnessResult(ctx, userID, projectID, identity)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if run.ID == uuid.Nil {
		return nil, workflowRequestErrorForState(ErrWorkflowRunNotFound)
	}
	if run.ClientType != "pc" {
		return identity, nil
	}
	next := *identity
	next.GeneralStepBase = identity.WorkflowStepID
	next.WorkflowStepID += ".r." + fingerprint[:32]
	if len(next.WorkflowStepID) > 128 {
		return nil, newWorkflowRequestError(http.StatusBadRequest, "invalid_workflow_identity", false, "通用模式配音步骤编号过长", nil)
	}
	return &next, nil
}

// Version only the PC general-editing subrequests. The run and parent step stay
// stable, preserving upstream checkpoints, leases and reservation ownership.
func (a *App) generalWorkflowIdentity(ctx context.Context, userID uuid.UUID, projectID *uuid.UUID, kind string, req map[string]any, identity *workflowRequestIdentity, fingerprint string) (*workflowRequestIdentity, string, error) {
	videoContext, _ := req["video_context"].(map[string]any)
	if identity == nil || kind != "script_draft" || req["model_operation"] != "narration_outline" || !(videoContext["contract"] == "general-editing-v2" && generalWorkflowStep.MatchString(identity.WorkflowStepID) || videoContext["contract"] == "general-narration-v3" && generalNarrationStep.MatchString(identity.WorkflowStepID)) {
		return identity, fingerprint, nil
	}
	// Validate ownership before inspecting old versions or creating any identity.
	run, _, err := a.loadMovieHarnessResult(ctx, userID, projectID, identity)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "", err
	}
	if run.ID == uuid.Nil {
		return nil, "", workflowRequestErrorForState(ErrWorkflowRunNotFound)
	}
	if run.ClientType != "pc" {
		return identity, fingerprint, nil
	}
	promptFingerprint := prompts.GeneralEditingPromptFingerprint(kind, req)
	if a.promptBuilder != nil {
		promptFingerprint = a.promptBuilder.GeneralEditingPromptFingerprint(kind, req)
	}
	fingerprint = workflowInputFingerprint(map[string]any{
		"version": "general-request-v1", "request": req, "kind": kind,
		"profile": a.openai.WorkflowProfileFingerprint(kind, req),
		"prompt":  promptFingerprint,
	})
	next := *identity
	next.GeneralStepBase = identity.WorkflowStepID
	next.WorkflowStepID += ".r." + fingerprint[:32]
	if len(next.WorkflowStepID) > 128 {
		return nil, "", newWorkflowRequestError(http.StatusBadRequest, "invalid_workflow_identity", false, "通用模式步骤编号过长", nil)
	}
	return &next, fingerprint, nil
}

// Called while resolveWorkflowRequestIdentity holds the workflow-run row lock.
// Competing versions therefore cannot both pass this check and start paid work.
func checkGeneralPriorExecutions(tx *gorm.DB, runID uuid.UUID, identity *workflowRequestIdentity, now time.Time) error {
	if identity.GeneralStepBase == "" {
		return nil
	}
	// A new version must not hide an in-flight or uncertain paid operation.
	// Use a literal prefix comparison (SQL LIKE would treat '_' as a wildcard).
	prefix := identity.GeneralStepBase + ".r."
	var previous []models.WorkflowStepExecution
	err := tx.Where("workflow_run_id = ? AND step_id <> ?", runID, identity.WorkflowStepID).
		Where("step_id = ? OR substr(step_id, 1, ?) = ?", identity.GeneralStepBase, len(prefix), prefix).
		Find(&previous).Error
	if err != nil {
		return err
	}
	for _, step := range previous {
		if valueOrEmpty(step.ErrorCode) == "workflow_result_unknown" {
			return movieHarnessUnknownResult()
		}
		if step.Status == workflowRunStatusRunning {
			if step.LeaseExpiresAt != nil && step.LeaseExpiresAt.After(now) {
				return movieHarnessRunningResult()
			}
			return movieHarnessUnknownResult()
		}
		if step.ProviderSubmissionClaimID != nil && step.Status != workflowRunStatusSucceeded && step.Status != workflowStepStatusFailed {
			return movieHarnessUnknownResult()
		}
	}
	return nil
}
