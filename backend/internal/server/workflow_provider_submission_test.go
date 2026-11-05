package server

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/database"
	"github.com/narratoai/backend-go/internal/models"
)

const (
	workflowProviderTaskIDPrimary    = "task_01JQ7Y8K9M2N4P6R8T0V3W5X7Z"
	workflowProviderTaskIDAlternate  = "task_01JQ7Y8K9M2N4P6R8T0V3W5X8A"
	workflowProviderTaskIDElevenLabs = "transcription_3XhZ9mK2pQ7vR4tN8wY1cF6sL0aD5gH"
)

func TestSanitizeWorkflowProviderTaskIDAcceptsOnlyOpaqueMachineIDs(t *testing.T) {
	for _, providerTaskID := range []string{
		"a1______________",
		"123e4567-e89b-12d3-a456-426614174000",
		workflowProviderTaskIDElevenLabs,
		strings.Repeat("a1", 64),
	} {
		got, err := sanitizeWorkflowProviderTaskID(providerTaskID)
		if err != nil || got != providerTaskID {
			t.Fatalf("sanitize accepted provider task id %q = %q, err = %v", providerTaskID, got, err)
		}
	}

	for _, providerTaskID := range []string{
		"private-audio.wav",
		"secret-subtitle-line",
		"provider:task:1234567890",
		"https://provider.example/tasks/1234567890",
		`C:\private\audio-1234567890.wav`,
		" provider-task-1234567890",
		"provider-task-1234567890 ",
		"short-task-1",
		"abcdefghijklmnop",
		"1234567890123456",
		strings.Repeat("a1", 65),
	} {
		if got, err := sanitizeWorkflowProviderTaskID(providerTaskID); err == nil {
			t.Fatalf("sanitize rejected provider task id %q = %q, want error", providerTaskID, got)
		}
	}
}

func TestGenericWorkflowProviderRequestIDKeepsSynchronousGrammar(t *testing.T) {
	for _, providerRequestID := range []string{"private-audio.wav", "secret-subtitle-line", "provider:request:123"} {
		got, err := sanitizeWorkflowProviderRequestID(&providerRequestID)
		if err != nil || got == nil || *got != providerRequestID {
			t.Fatalf("sanitize generic provider request id %q = %v, err = %v", providerRequestID, got, err)
		}
	}
}

func TestClaimWorkflowProviderSubmissionSerializesAndRequiresOwnership(t *testing.T) {
	app, _, execution := newWorkflowProviderSubmissionTestExecution(t, "claim-ownership")
	providerInputHash := strings.Repeat("a", 64)

	claim, err := app.claimWorkflowProviderSubmission(context.Background(), execution, providerInputHash)
	if err != nil {
		t.Fatalf("claim provider submission: %v", err)
	}
	if claim.ClaimID == "" || claim.Replayed || claim.Execution.Step.ProviderSubmissionClaimID == nil || *claim.Execution.Step.ProviderSubmissionClaimID != claim.ClaimID {
		t.Fatalf("provider submission claim = %#v", claim)
	}
	if claim.Execution.Step.ProviderInputHash == nil || *claim.Execution.Step.ProviderInputHash != providerInputHash || claim.Execution.Step.ProviderSubmissionClaimedAt == nil {
		t.Fatalf("claimed step metadata = %#v", claim.Execution.Step)
	}

	_, err = app.claimWorkflowProviderSubmission(context.Background(), execution, providerInputHash)
	assertWorkflowProviderSubmissionInProgress(t, err)
	_, err = app.claimWorkflowProviderSubmission(context.Background(), execution, strings.Repeat("b", 64))
	assertWorkflowRequestError(t, err, http.StatusConflict, "workflow_input_mismatch")

	_, err = app.persistWorkflowProviderSubmission(context.Background(), execution, uuid.NewString(), workflowProviderTaskIDPrimary, providerInputHash)
	assertWorkflowRequestError(t, err, http.StatusConflict, "workflow_provider_submission_claim_conflict")
	stored, err := app.persistWorkflowProviderSubmission(context.Background(), execution, claim.ClaimID, workflowProviderTaskIDPrimary, providerInputHash)
	if err != nil {
		t.Fatalf("persist claimed provider submission: %v", err)
	}
	if stored.Step.ProviderRequestID == nil || *stored.Step.ProviderRequestID != workflowProviderTaskIDPrimary || stored.Step.ProviderSubmissionClaimID != nil || stored.Step.ProviderSubmissionClaimedAt != nil {
		t.Fatalf("persisted claimed submission = %#v", stored.Step)
	}
}

func TestClaimWorkflowProviderSubmissionStaleTakeoverReleaseAndReplay(t *testing.T) {
	app, userID, execution := newWorkflowProviderSubmissionTestExecution(t, "claim-stale")
	providerInputHash := strings.Repeat("c", 64)
	first, err := app.claimWorkflowProviderSubmission(context.Background(), execution, providerInputHash)
	if err != nil {
		t.Fatalf("claim provider submission: %v", err)
	}

	atLeaseBoundary := app.workflowRuns.now().Add(workflowRunLeaseDuration)
	app.workflowRuns.now = func() time.Time { return atLeaseBoundary }
	leaseExpiresAt := atLeaseBoundary.Add(workflowRunLeaseDuration)
	if err := app.db.Model(&models.WorkflowRun{}).Where("id = ?", execution.Run.ID).Update("lease_expires_at", leaseExpiresAt).Error; err != nil {
		t.Fatalf("refresh run lease at claim boundary: %v", err)
	}
	if err := app.db.Model(&models.WorkflowStepExecution{}).Where("id = ?", execution.Step.ID).Updates(map[string]any{"lease_expires_at": leaseExpiresAt, "lease_owner": execution.Identity.ClientInstanceID}).Error; err != nil {
		t.Fatalf("refresh step lease at claim boundary: %v", err)
	}
	_, err = app.claimWorkflowProviderSubmission(context.Background(), execution, providerInputHash)
	assertWorkflowProviderSubmissionInProgress(t, err)

	takeoverAt := app.workflowRuns.now().Add(workflowRunLeaseDuration + time.Second)
	app.workflowRuns.now = func() time.Time { return takeoverAt }
	leaseExpiresAt = takeoverAt.Add(workflowRunLeaseDuration)
	if err := app.db.Model(&models.WorkflowRun{}).Where("id = ?", execution.Run.ID).Update("lease_expires_at", leaseExpiresAt).Error; err != nil {
		t.Fatalf("refresh run lease: %v", err)
	}
	if err := app.db.Model(&models.WorkflowStepExecution{}).Where("id = ?", execution.Step.ID).Updates(map[string]any{"lease_expires_at": leaseExpiresAt, "lease_owner": execution.Identity.ClientInstanceID}).Error; err != nil {
		t.Fatalf("refresh step lease: %v", err)
	}

	second, err := app.claimWorkflowProviderSubmission(context.Background(), execution, providerInputHash)
	if err != nil {
		t.Fatalf("take over stale claim: %v", err)
	}
	if second.ClaimID == first.ClaimID || second.Execution.Step.ProviderSubmissionClaimedAt == nil || !second.Execution.Step.ProviderSubmissionClaimedAt.Equal(takeoverAt) {
		t.Fatalf("stale claim takeover = first %#v, second %#v", first, second)
	}

	err = app.releaseWorkflowProviderSubmissionClaim(context.Background(), execution, first.ClaimID)
	assertWorkflowRequestError(t, err, http.StatusConflict, "workflow_provider_submission_claim_conflict")
	if err := app.releaseWorkflowProviderSubmissionClaim(context.Background(), execution, second.ClaimID); err != nil {
		t.Fatalf("release matching claim: %v", err)
	}
	released := loadProviderFinalizationStep(t, app, execution.Step.ID)
	if released.ProviderSubmissionClaimID != nil || released.ProviderSubmissionClaimedAt != nil || released.ProviderInputHash == nil || *released.ProviderInputHash != providerInputHash {
		t.Fatalf("released claim metadata = %#v", released)
	}

	third, err := app.claimWorkflowProviderSubmission(context.Background(), execution, providerInputHash)
	if err != nil {
		t.Fatalf("reclaim released submission: %v", err)
	}
	if _, err := app.persistWorkflowProviderSubmission(context.Background(), execution, third.ClaimID, workflowProviderTaskIDPrimary, providerInputHash); err != nil {
		t.Fatalf("persist reclaimed submission: %v", err)
	}
	replayed, taskID, found, err := app.findWorkflowProviderSubmissionReplay(context.Background(), userID, nil, execution.Identity, providerInputHash)
	if err != nil {
		t.Fatalf("find lost-response replay: %v", err)
	}
	if !found || taskID != workflowProviderTaskIDPrimary || replayed.Step.ProviderSubmissionClaimID != nil {
		t.Fatalf("lost-response replay = %#v, task = %q, found = %v", replayed, taskID, found)
	}
}

func assertWorkflowProviderSubmissionInProgress(t *testing.T, err error) {
	t.Helper()
	requestErr, ok := asWorkflowRequestError(err)
	if !ok || requestErr.Status != http.StatusConflict || requestErr.Code != "workflow_provider_submission_in_progress" || !requestErr.Retryable {
		t.Fatalf("submission in-progress error = %#v, %v", requestErr, err)
	}
}

func TestPersistWorkflowProviderSubmissionStoresSanitizedMetadataAndActivity(t *testing.T) {
	app, _, execution := newWorkflowProviderSubmissionTestExecution(t, "persist")
	submittedAt := app.workflowRuns.now().Add(30 * time.Second)
	app.workflowRuns.now = func() time.Time { return submittedAt }
	providerTaskID := workflowProviderTaskIDPrimary
	providerInputHash := strings.Repeat("A", 64)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stored, err := claimAndPersistWorkflowProviderSubmission(app, ctx, execution, providerTaskID, providerInputHash)
	if err != nil {
		t.Fatalf("persist provider submission: %v", err)
	}
	if stored.Step.Status != workflowRunStatusRunning || stored.Step.CompletedAt != nil {
		t.Fatalf("stored step status = %#v", stored.Step)
	}
	if stored.Step.ProviderRequestID == nil || *stored.Step.ProviderRequestID != providerTaskID {
		t.Fatalf("provider request id = %v, want %q", stored.Step.ProviderRequestID, providerTaskID)
	}
	wantHash := strings.ToLower(providerInputHash)
	if stored.Step.ProviderInputHash == nil || *stored.Step.ProviderInputHash != wantHash {
		t.Fatalf("provider input hash = %v, want %q", stored.Step.ProviderInputHash, wantHash)
	}
	if stored.Step.LeaseExpiresAt == nil || !stored.Step.LeaseExpiresAt.Equal(submittedAt.Add(workflowRunLeaseDuration)) {
		t.Fatalf("step lease expiry = %v", stored.Step.LeaseExpiresAt)
	}

	var run models.WorkflowRun
	if err := app.db.First(&run, "id = ?", execution.Run.ID).Error; err != nil {
		t.Fatalf("load workflow run: %v", err)
	}
	if !run.LastActiveAt.Equal(submittedAt) || !run.ExpiresAt.Equal(submittedAt.Add(workflowRunInactivityDuration)) {
		t.Fatalf("run activity was not refreshed: %#v", run)
	}
	if run.LeaseExpiresAt == nil || !run.LeaseExpiresAt.Equal(submittedAt.Add(workflowRunLeaseDuration)) {
		t.Fatalf("run lease expiry = %v", run.LeaseExpiresAt)
	}

	var reservations int64
	if err := app.db.Model(&models.CreditReservation{}).Where("workflow_run_id = ?", execution.Run.ID).Count(&reservations).Error; err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if reservations != 0 {
		t.Fatalf("reservations = %d, want 0", reservations)
	}

	var persisted models.WorkflowStepExecution
	if err := app.db.First(&persisted, "id = ?", execution.Step.ID).Error; err != nil {
		t.Fatalf("reload workflow step: %v", err)
	}
	if persisted.ProviderRequestID == nil || persisted.ProviderInputHash == nil ||
		*persisted.ProviderRequestID != providerTaskID || *persisted.ProviderInputHash != wantHash {
		t.Fatalf("persisted submission = %#v", persisted)
	}
}

func TestPersistWorkflowProviderSubmissionAllowsExactClaimAfterLeaseTakeover(t *testing.T) {
	app, userID, execution := newWorkflowProviderSubmissionTestExecution(t, "persist-after-takeover")
	providerInputHash := strings.Repeat("9", 64)
	claim, err := app.claimWorkflowProviderSubmission(context.Background(), execution, providerInputHash)
	if err != nil {
		t.Fatalf("claim provider submission: %v", err)
	}
	newClientID := "resumed-provider-client"
	resumed, err := app.WorkflowRuns().Resume(context.Background(), userID, execution.Run.ID, newClientID)
	if err != nil {
		t.Fatalf("resume workflow run: %v", err)
	}

	stored, err := app.persistWorkflowProviderSubmission(context.Background(), claim.Execution, claim.ClaimID, workflowProviderTaskIDPrimary, providerInputHash)
	if err != nil {
		t.Fatalf("persist exact claim after takeover: %v", err)
	}
	if stored.Run.ClientInstanceID != newClientID || stored.Step.LeaseOwner == nil || *stored.Step.LeaseOwner != newClientID {
		t.Fatalf("persist transferred ownership back: run=%#v step=%#v", stored.Run, stored.Step)
	}
	if resumed.ClientInstanceID != newClientID || stored.Step.ProviderRequestID == nil || *stored.Step.ProviderRequestID != workflowProviderTaskIDPrimary {
		t.Fatalf("persisted takeover submission = %#v", stored)
	}
}

func TestPersistWorkflowProviderSubmissionAfterTakeoverRejectsReplacedClaimAndCancelledRun(t *testing.T) {
	t.Run("replaced claim", func(t *testing.T) {
		app, userID, execution := newWorkflowProviderSubmissionTestExecution(t, "persist-replaced-claim")
		providerInputHash := strings.Repeat("8", 64)
		claim, err := app.claimWorkflowProviderSubmission(context.Background(), execution, providerInputHash)
		if err != nil {
			t.Fatalf("claim provider submission: %v", err)
		}
		if _, err := app.WorkflowRuns().Resume(context.Background(), userID, execution.Run.ID, "replacement-client"); err != nil {
			t.Fatalf("resume workflow run: %v", err)
		}
		replacementClaimID := uuid.NewString()
		if err := app.db.Model(&models.WorkflowStepExecution{}).Where("id = ?", execution.Step.ID).Update("provider_submission_claim_id", replacementClaimID).Error; err != nil {
			t.Fatalf("replace provider claim: %v", err)
		}

		_, err = app.persistWorkflowProviderSubmission(context.Background(), claim.Execution, claim.ClaimID, workflowProviderTaskIDPrimary, providerInputHash)
		assertWorkflowRequestError(t, err, http.StatusConflict, "workflow_provider_submission_claim_conflict")
		if step := loadProviderFinalizationStep(t, app, execution.Step.ID); step.ProviderRequestID != nil || step.ProviderSubmissionClaimID == nil || *step.ProviderSubmissionClaimID != replacementClaimID {
			t.Fatalf("stale claim mutated submission: %#v", step)
		}
	})

	t.Run("cancelled run", func(t *testing.T) {
		app, userID, execution := newWorkflowProviderSubmissionTestExecution(t, "persist-cancelled-run")
		providerInputHash := strings.Repeat("7", 64)
		claim, err := app.claimWorkflowProviderSubmission(context.Background(), execution, providerInputHash)
		if err != nil {
			t.Fatalf("claim provider submission: %v", err)
		}
		if _, err := app.WorkflowRuns().Cancel(context.Background(), userID, execution.Run.ID); err != nil {
			t.Fatalf("cancel workflow run: %v", err)
		}

		_, err = app.persistWorkflowProviderSubmission(context.Background(), claim.Execution, claim.ClaimID, workflowProviderTaskIDPrimary, providerInputHash)
		assertWorkflowRequestError(t, err, http.StatusConflict, "workflow_run_terminal")
		if step := loadProviderFinalizationStep(t, app, execution.Step.ID); step.ProviderRequestID != nil {
			t.Fatalf("cancelled run persisted provider task id: %#v", step)
		}
	})
}

func TestPersistWorkflowProviderSubmissionRejectsBusinessContentAndInvalidHash(t *testing.T) {
	app, _, execution := newWorkflowProviderSubmissionTestExecution(t, "sanitize")
	validHash := strings.Repeat("b", 64)
	for _, providerTaskID := range []string{
		"https://provider.example/tasks/secret.wav",
		`C:\private\audio.wav`,
		"secret subtitle text",
	} {
		_, err := app.persistWorkflowProviderSubmission(context.Background(), execution, uuid.NewString(), providerTaskID, validHash)
		assertWorkflowRequestError(t, err, http.StatusBadRequest, "invalid_provider_submission")
	}
	for _, providerInputHash := range []string{
		strings.Repeat("c", 63),
		strings.Repeat("g", 64),
		"secret narration prompt",
	} {
		_, err := app.persistWorkflowProviderSubmission(context.Background(), execution, uuid.NewString(), workflowProviderTaskIDPrimary, providerInputHash)
		assertWorkflowRequestError(t, err, http.StatusBadRequest, "invalid_provider_submission")
	}

	step := loadProviderFinalizationStep(t, app, execution.Step.ID)
	if step.ProviderRequestID != nil || step.ProviderInputHash != nil {
		t.Fatalf("invalid submission metadata was stored: %#v", step)
	}
}

func TestPersistWorkflowProviderSubmissionIsIdempotentAcrossRegeneratedInputAndRejectsTaskConflicts(t *testing.T) {
	app, _, execution := newWorkflowProviderSubmissionTestExecution(t, "idempotent")
	providerInputHash := strings.Repeat("d", 64)
	if _, err := claimAndPersistWorkflowProviderSubmission(app, context.Background(), execution, workflowProviderTaskIDPrimary, providerInputHash); err != nil {
		t.Fatalf("persist original submission: %v", err)
	}
	if _, err := claimAndPersistWorkflowProviderSubmission(app, context.Background(), execution, workflowProviderTaskIDPrimary, providerInputHash); err != nil {
		t.Fatalf("replay identical submission: %v", err)
	}

	_, err := claimAndPersistWorkflowProviderSubmission(app, context.Background(), execution, workflowProviderTaskIDAlternate, providerInputHash)
	assertWorkflowRequestError(t, err, http.StatusConflict, "workflow_provider_submission_conflict")
	_, err = claimAndPersistWorkflowProviderSubmission(app, context.Background(), execution, workflowProviderTaskIDPrimary, strings.Repeat("e", 64))
	if err != nil {
		t.Fatalf("replay persisted task after regenerated provider input: %v", err)
	}

	step := loadProviderFinalizationStep(t, app, execution.Step.ID)
	if step.ProviderRequestID == nil || *step.ProviderRequestID != workflowProviderTaskIDPrimary ||
		step.ProviderInputHash == nil || *step.ProviderInputHash != providerInputHash {
		t.Fatalf("conflicting replay mutated submission: %#v", step)
	}
}

func TestClaimWorkflowProviderSubmissionRejectsOrphanedHashMismatch(t *testing.T) {
	app, _, execution := newWorkflowProviderSubmissionTestExecution(t, "orphaned-hash")
	oldHash := strings.Repeat("c", 64)
	if err := app.db.Model(&models.WorkflowStepExecution{}).
		Where("id = ?", execution.Step.ID).
		Update("provider_input_hash", oldHash).Error; err != nil {
		t.Fatalf("seed orphaned provider input hash: %v", err)
	}

	newHash := strings.Repeat("d", 64)
	_, err := app.claimWorkflowProviderSubmission(context.Background(), execution, newHash)
	assertWorkflowRequestError(t, err, http.StatusConflict, "workflow_input_mismatch")
	step := loadProviderFinalizationStep(t, app, execution.Step.ID)
	if step.ProviderRequestID != nil || step.ProviderInputHash == nil || *step.ProviderInputHash != oldHash {
		t.Fatalf("orphaned hash was mutated: %#v", step)
	}
}

func TestPersistWorkflowProviderSubmissionConcurrentTaskIDsHaveOneWinner(t *testing.T) {
	app, _, execution := newWorkflowProviderSubmissionTestExecution(t, "concurrent")
	providerInputHash := strings.Repeat("f", 64)
	claim, err := app.claimWorkflowProviderSubmission(context.Background(), execution, providerInputHash)
	if err != nil {
		t.Fatalf("claim provider submission: %v", err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, providerTaskID := range []string{workflowProviderTaskIDPrimary, workflowProviderTaskIDAlternate} {
		providerTaskID := providerTaskID
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := app.persistWorkflowProviderSubmission(context.Background(), execution, claim.ClaimID, providerTaskID, providerInputHash)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	succeeded := 0
	conflicted := 0
	for err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		requestErr, ok := asWorkflowRequestError(err)
		if ok && requestErr.Status == http.StatusConflict && requestErr.Code == "workflow_provider_submission_claim_conflict" {
			conflicted++
			continue
		}
		t.Fatalf("unexpected concurrent persist error: %v", err)
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent results: succeeded=%d conflicted=%d", succeeded, conflicted)
	}

	step := loadProviderFinalizationStep(t, app, execution.Step.ID)
	if step.ProviderRequestID == nil || (*step.ProviderRequestID != workflowProviderTaskIDPrimary && *step.ProviderRequestID != workflowProviderTaskIDAlternate) {
		t.Fatalf("stored provider task id = %v", step.ProviderRequestID)
	}
	if step.ProviderInputHash == nil || *step.ProviderInputHash != providerInputHash {
		t.Fatalf("stored provider input hash = %v", step.ProviderInputHash)
	}
}

func TestFindWorkflowProviderSubmissionReplayReusesRunningSubmission(t *testing.T) {
	app, userID, execution := newWorkflowProviderSubmissionTestExecution(t, "replay")
	providerTaskID := workflowProviderTaskIDPrimary
	providerInputHash := strings.Repeat("1", 64)
	if _, err := claimAndPersistWorkflowProviderSubmission(app, context.Background(), execution, providerTaskID, providerInputHash); err != nil {
		t.Fatalf("persist provider submission: %v", err)
	}

	_, err := app.resolveWorkflowRequestIdentity(context.Background(), userID, nil, execution.Identity)
	assertWorkflowRequestError(t, err, http.StatusConflict, "step_execution_in_progress")
	replayAt := app.workflowRuns.now().Add(30 * time.Second)
	app.workflowRuns.now = func() time.Time { return replayAt }
	replayed, gotTaskID, found, err := app.findWorkflowProviderSubmissionReplay(
		context.Background(),
		userID,
		nil,
		execution.Identity,
		providerInputHash,
	)
	if err != nil {
		t.Fatalf("find provider submission replay: %v", err)
	}
	if !found || gotTaskID != providerTaskID || replayed.Step.ID != execution.Step.ID {
		t.Fatalf("replay = %#v, task = %q, found = %v", replayed, gotTaskID, found)
	}

	var steps int64
	if err := app.db.Model(&models.WorkflowStepExecution{}).Where("workflow_run_id = ?", execution.Run.ID).Count(&steps).Error; err != nil {
		t.Fatalf("count workflow steps: %v", err)
	}
	if steps != 1 || replayed.Step.AttemptCount != 1 {
		t.Fatalf("replay created another attempt: steps=%d execution=%#v", steps, replayed.Step)
	}
	if replayed.Step.LeaseExpiresAt == nil || !replayed.Step.LeaseExpiresAt.Equal(replayAt.Add(workflowRunLeaseDuration)) {
		t.Fatalf("replay did not refresh step lease: %v", replayed.Step.LeaseExpiresAt)
	}
}

func TestFindWorkflowProviderSubmissionReplayReturnsSucceededSubmission(t *testing.T) {
	app, userID, execution := newWorkflowProviderSubmissionTestExecution(t, "replay-succeeded")
	providerTaskID := workflowProviderTaskIDElevenLabs
	providerInputHash := strings.Repeat("0", 64)
	if _, err := claimAndPersistWorkflowProviderSubmission(app, context.Background(), execution, providerTaskID, providerInputHash); err != nil {
		t.Fatalf("persist provider submission: %v", err)
	}
	if err := app.db.Model(&models.WorkflowStepExecution{}).Where("id = ?", execution.Step.ID).Updates(map[string]any{
		"status":           workflowRunStatusSucceeded,
		"completed_at":     app.workflowRuns.now(),
		"lease_owner":      nil,
		"lease_expires_at": nil,
	}).Error; err != nil {
		t.Fatalf("complete workflow step: %v", err)
	}

	replayed, gotTaskID, found, err := app.findWorkflowProviderSubmissionReplay(
		context.Background(),
		userID,
		nil,
		execution.Identity,
		providerInputHash,
	)
	if err != nil {
		t.Fatalf("find succeeded provider replay: %v", err)
	}
	if !found || gotTaskID != providerTaskID || replayed.Step.Status != workflowRunStatusSucceeded {
		t.Fatalf("succeeded replay = %#v, task = %q, found = %v", replayed, gotTaskID, found)
	}
}

func TestFindWorkflowProviderSubmissionReplayHandlesMissingAndMismatchedInput(t *testing.T) {
	t.Run("missing submission is not found", func(t *testing.T) {
		app, userID, execution := newWorkflowProviderSubmissionTestExecution(t, "replay-missing")
		got, taskID, found, err := app.findWorkflowProviderSubmissionReplay(
			context.Background(),
			userID,
			nil,
			execution.Identity,
			strings.Repeat("2", 64),
		)
		if err != nil || found || taskID != "" || got.Identity != nil {
			t.Fatalf("missing replay = %#v, task = %q, found = %v, err = %v", got, taskID, found, err)
		}
		if err := app.db.Model(&models.WorkflowStepExecution{}).
			Where("id = ?", execution.Step.ID).
			Update("provider_input_hash", strings.Repeat("2", 64)).Error; err != nil {
			t.Fatalf("seed orphaned provider input hash: %v", err)
		}
		got, taskID, found, err = app.findWorkflowProviderSubmissionReplay(
			context.Background(),
			userID,
			nil,
			execution.Identity,
			strings.Repeat("2", 64),
		)
		if err != nil || found || taskID != "" || got.Identity != nil {
			t.Fatalf("orphaned hash replay = %#v, task = %q, found = %v, err = %v", got, taskID, found, err)
		}
		_, _, _, err = app.findWorkflowProviderSubmissionReplay(
			context.Background(),
			userID,
			nil,
			execution.Identity,
			strings.Repeat("9", 64),
		)
		assertWorkflowRequestError(t, err, http.StatusConflict, "workflow_input_mismatch")
	})

	t.Run("persisted provider task survives regenerated input", func(t *testing.T) {
		app, userID, execution := newWorkflowProviderSubmissionTestExecution(t, "replay-hash")
		if _, err := claimAndPersistWorkflowProviderSubmission(app, context.Background(), execution, workflowProviderTaskIDPrimary, strings.Repeat("3", 64)); err != nil {
			t.Fatalf("persist provider submission: %v", err)
		}
		replayed, taskID, found, err := app.findWorkflowProviderSubmissionReplay(
			context.Background(),
			userID,
			nil,
			execution.Identity,
			strings.Repeat("4", 64),
		)
		if err != nil || !found || taskID != workflowProviderTaskIDPrimary || replayed.Step.ProviderInputHash == nil || *replayed.Step.ProviderInputHash != strings.Repeat("3", 64) {
			t.Fatalf("regenerated input replay = %#v, task = %q, found = %v, err = %v", replayed, taskID, found, err)
		}
	})

	t.Run("workflow input hash mismatch is structured", func(t *testing.T) {
		app, userID, execution := newWorkflowProviderSubmissionTestExecution(t, "replay-workflow-hash")
		identity := *execution.Identity
		identity.WorkflowInputHash = "different-workflow-input"
		_, _, _, err := app.findWorkflowProviderSubmissionReplay(
			context.Background(),
			userID,
			nil,
			&identity,
			strings.Repeat("5", 64),
		)
		assertWorkflowRequestError(t, err, http.StatusConflict, "workflow_input_mismatch")
	})
}

func TestFindWorkflowProviderSubmissionReplayEnforcesOwnershipProjectLeaseAndRunState(t *testing.T) {
	app := newTestApp(t)
	baseTime := time.Now().UTC()
	app.workflowRuns.now = func() time.Time { return baseTime }
	userID := uuid.New()
	projectID := uuid.New()
	run := createWorkflowIdentityTestWebRun(t, app, userID, projectID, "provider-replay-security", "provider-client")
	identity := workflowRequestIdentityForTest(run.ID, "provider.submit", run.InputHash, run.ClientInstanceID)
	execution, err := app.resolveWorkflowRequestIdentity(context.Background(), userID, &projectID, identity)
	if err != nil {
		t.Fatalf("resolve workflow execution: %v", err)
	}
	providerInputHash := strings.Repeat("6", 64)
	if _, err := claimAndPersistWorkflowProviderSubmission(app, context.Background(), execution, workflowProviderTaskIDPrimary, providerInputHash); err != nil {
		t.Fatalf("persist provider submission: %v", err)
	}

	_, _, _, err = app.findWorkflowProviderSubmissionReplay(context.Background(), uuid.New(), &projectID, identity, providerInputHash)
	assertWorkflowRequestError(t, err, http.StatusNotFound, "workflow_run_not_found")
	wrongProjectID := uuid.New()
	_, _, _, err = app.findWorkflowProviderSubmissionReplay(context.Background(), userID, &wrongProjectID, identity, providerInputHash)
	assertWorkflowRequestError(t, err, http.StatusConflict, "workflow_project_mismatch")
	wrongClientIdentity := *identity
	wrongClientIdentity.ClientInstanceID = "other-client"
	_, _, _, err = app.findWorkflowProviderSubmissionReplay(context.Background(), userID, &projectID, &wrongClientIdentity, providerInputHash)
	assertWorkflowRequestError(t, err, http.StatusConflict, "workflow_run_lease_conflict")

	if err := app.db.Model(&models.WorkflowRun{}).Where("id = ?", run.ID).Update("status", workflowRunStatusSucceeded).Error; err != nil {
		t.Fatalf("complete workflow run: %v", err)
	}
	_, _, _, err = app.findWorkflowProviderSubmissionReplay(context.Background(), userID, &projectID, identity, providerInputHash)
	assertWorkflowRequestError(t, err, http.StatusConflict, "workflow_run_terminal")
}

func TestResolveWorkflowProviderQueryLoadsRunningAndSucceededSubmissions(t *testing.T) {
	t.Run("running refreshes lease and activity", func(t *testing.T) {
		app, userID, execution := newWorkflowProviderSubmissionTestExecution(t, "query-running")
		providerTaskID := workflowProviderTaskIDPrimary
		providerInputHash := strings.Repeat("7", 64)
		if _, err := claimAndPersistWorkflowProviderSubmission(app, context.Background(), execution, providerTaskID, providerInputHash); err != nil {
			t.Fatalf("persist provider submission: %v", err)
		}
		queryAt := app.workflowRuns.now().Add(45 * time.Second)
		app.workflowRuns.now = func() time.Time { return queryAt }

		queried, err := app.resolveWorkflowProviderQuery(
			context.Background(),
			userID,
			nil,
			execution.Identity,
			providerTaskID,
			providerInputHash,
		)
		if err != nil {
			t.Fatalf("resolve provider query: %v", err)
		}
		if queried.Step.ID != execution.Step.ID || queried.Step.Status != workflowRunStatusRunning {
			t.Fatalf("queried execution = %#v", queried)
		}
		if queried.Step.LeaseExpiresAt == nil || !queried.Step.LeaseExpiresAt.Equal(queryAt.Add(workflowRunLeaseDuration)) {
			t.Fatalf("query did not refresh step lease: %v", queried.Step.LeaseExpiresAt)
		}
		if !queried.Run.LastActiveAt.Equal(queryAt) || !queried.Run.ExpiresAt.Equal(queryAt.Add(workflowRunInactivityDuration)) {
			t.Fatalf("query did not refresh run activity: %#v", queried.Run)
		}
	})

	t.Run("succeeded remains queryable without a step lease", func(t *testing.T) {
		app, userID, execution := newWorkflowProviderSubmissionTestExecution(t, "query-succeeded")
		providerTaskID := workflowProviderTaskIDElevenLabs
		providerInputHash := strings.Repeat("8", 64)
		if _, err := claimAndPersistWorkflowProviderSubmission(app, context.Background(), execution, providerTaskID, providerInputHash); err != nil {
			t.Fatalf("persist provider submission: %v", err)
		}
		completedAt := app.workflowRuns.now()
		if err := app.db.Model(&models.WorkflowStepExecution{}).Where("id = ?", execution.Step.ID).Updates(map[string]any{
			"status":           workflowRunStatusSucceeded,
			"completed_at":     completedAt,
			"lease_owner":      nil,
			"lease_expires_at": nil,
		}).Error; err != nil {
			t.Fatalf("complete workflow step: %v", err)
		}

		queried, err := app.resolveWorkflowProviderQuery(
			context.Background(),
			userID,
			nil,
			execution.Identity,
			providerTaskID,
			providerInputHash,
		)
		if err != nil {
			t.Fatalf("resolve succeeded provider query: %v", err)
		}
		if queried.Step.Status != workflowRunStatusSucceeded || queried.Step.LeaseOwner != nil || queried.Step.LeaseExpiresAt != nil {
			t.Fatalf("queried succeeded step = %#v", queried.Step)
		}
	})
}

func TestResolveWorkflowProviderQueryRejectsTaskHashAndLeaseMismatch(t *testing.T) {
	app, userID, execution := newWorkflowProviderSubmissionTestExecution(t, "query-mismatch")
	providerTaskID := workflowProviderTaskIDPrimary
	providerInputHash := strings.Repeat("9", 64)
	if _, err := claimAndPersistWorkflowProviderSubmission(app, context.Background(), execution, providerTaskID, providerInputHash); err != nil {
		t.Fatalf("persist provider submission: %v", err)
	}

	_, err := app.resolveWorkflowProviderQuery(context.Background(), userID, nil, execution.Identity, workflowProviderTaskIDAlternate, providerInputHash)
	assertWorkflowRequestError(t, err, http.StatusConflict, "workflow_provider_task_mismatch")
	_, err = app.resolveWorkflowProviderQuery(context.Background(), userID, nil, execution.Identity, providerTaskID, strings.Repeat("a", 64))
	assertWorkflowRequestError(t, err, http.StatusConflict, "workflow_input_mismatch")

	otherClient := "other-client"
	leaseExpiresAt := app.workflowRuns.now().Add(workflowRunLeaseDuration)
	if err := app.db.Model(&models.WorkflowStepExecution{}).Where("id = ?", execution.Step.ID).Updates(map[string]any{
		"lease_owner":      otherClient,
		"lease_expires_at": leaseExpiresAt,
	}).Error; err != nil {
		t.Fatalf("transfer step lease: %v", err)
	}
	_, err = app.resolveWorkflowProviderQuery(context.Background(), userID, nil, execution.Identity, providerTaskID, providerInputHash)
	assertWorkflowRequestError(t, err, http.StatusConflict, "step_execution_in_progress")
}

func TestResolveWorkflowProviderQueryRejectsUnsubmittedStep(t *testing.T) {
	app, userID, execution := newWorkflowProviderSubmissionTestExecution(t, "query-unsubmitted")
	_, err := app.resolveWorkflowProviderQuery(
		context.Background(),
		userID,
		nil,
		execution.Identity,
		workflowProviderTaskIDPrimary,
		strings.Repeat("a", 64),
	)
	assertWorkflowRequestError(t, err, http.StatusNotFound, "workflow_provider_submission_not_found")
}

func TestResolveWorkflowProviderQuerySurvivesDatabaseRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workflow-provider-submission.db")
	cfg := workflowProviderSubmissionTestConfig("sqlite://" + filepath.ToSlash(databasePath))
	app := newWorkflowProviderSubmissionTestApp(t, cfg)
	baseTime := time.Date(2026, time.July, 15, 10, 0, 0, 0, time.UTC)
	app.workflowRuns.now = func() time.Time { return baseTime }
	userID := uuid.New()
	run := createWorkflowIdentityTestRun(t, app, userID, "provider-restart", "provider-client")
	identity := workflowRequestIdentityForTest(run.ID, "provider.submit", run.InputHash, run.ClientInstanceID)
	execution, err := app.resolveWorkflowRequestIdentity(context.Background(), userID, nil, identity)
	if err != nil {
		t.Fatalf("resolve workflow execution: %v", err)
	}
	providerTaskID := workflowProviderTaskIDElevenLabs
	providerInputHash := strings.Repeat("b", 64)
	if _, err := claimAndPersistWorkflowProviderSubmission(app, context.Background(), execution, providerTaskID, providerInputHash); err != nil {
		t.Fatalf("persist provider submission: %v", err)
	}
	sqlDB, err := app.db.DB()
	if err != nil {
		t.Fatalf("unwrap database: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	restarted := newWorkflowProviderSubmissionTestApp(t, cfg)
	restarted.workflowRuns.now = func() time.Time { return baseTime.Add(30 * time.Second) }
	queried, err := restarted.resolveWorkflowProviderQuery(
		context.Background(),
		userID,
		nil,
		identity,
		providerTaskID,
		providerInputHash,
	)
	if err != nil {
		t.Fatalf("resolve provider query after restart: %v", err)
	}
	if queried.Step.ProviderRequestID == nil || *queried.Step.ProviderRequestID != providerTaskID ||
		queried.Step.ProviderInputHash == nil || *queried.Step.ProviderInputHash != providerInputHash {
		t.Fatalf("restarted provider submission = %#v", queried.Step)
	}
}

func newWorkflowProviderSubmissionTestExecution(t *testing.T, suffix string) (*App, uuid.UUID, workflowRequestExecution) {
	t.Helper()
	app := newTestApp(t)
	baseTime := time.Date(2026, time.July, 15, 8, 0, 0, 0, time.UTC)
	app.workflowRuns.now = func() time.Time { return baseTime }
	userID := uuid.New()
	run := createWorkflowIdentityTestRun(t, app, userID, "provider-submission-"+suffix, "provider-client")
	identity := workflowRequestIdentityForTest(run.ID, "provider.submit", run.InputHash, run.ClientInstanceID)
	execution, err := app.resolveWorkflowRequestIdentity(context.Background(), userID, nil, identity)
	if err != nil {
		t.Fatalf("resolve workflow execution: %v", err)
	}
	return app, userID, execution
}

func claimAndPersistWorkflowProviderSubmission(
	app *App,
	ctx context.Context,
	execution workflowRequestExecution,
	providerTaskID string,
	providerInputHash string,
) (workflowRequestExecution, error) {
	claim, err := app.claimWorkflowProviderSubmission(ctx, execution, providerInputHash)
	if err != nil {
		return workflowRequestExecution{}, err
	}
	if claim.Replayed {
		if claim.ProviderTaskID != providerTaskID {
			return workflowRequestExecution{}, workflowProviderSubmissionConflictError()
		}
		return claim.Execution, nil
	}
	return app.persistWorkflowProviderSubmission(ctx, execution, claim.ClaimID, providerTaskID, providerInputHash)
}

func workflowProviderSubmissionTestConfig(databaseURL string) config.Config {
	return config.Config{
		Debug:             true,
		Environment:       "testing",
		Host:              "127.0.0.1",
		Port:              0,
		APIv1Prefix:       "/api/v1",
		OpenAPIURL:        "/api/v1/openapi.json",
		DocsURL:           "/docs",
		RedocURL:          "/redoc",
		DatabaseURL:       databaseURL,
		AutoMigrate:       true,
		SecretKey:         "test-secret-key-test-secret-key-1234",
		CORSOrigins:       []string{"*"},
		AccessTTL:         30 * time.Minute,
		RefreshTTL:        24 * time.Hour,
		PromptTemplateDir: "../../prompt",
	}
}

func newWorkflowProviderSubmissionTestApp(t *testing.T, cfg config.Config) *App {
	t.Helper()
	db, err := database.Open(cfg)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := database.AutoMigrate(db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	return New(cfg, db)
}
