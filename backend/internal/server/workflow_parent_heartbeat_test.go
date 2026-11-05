package server

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/datatypes"
)

func TestWorkflowParentHeartbeatRenewsOnlyLiveOwnedDescendants(t *testing.T) {
	app := newTestApp(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	app.workflowRuns.now = func() time.Time { return now }
	userID := uuid.New()
	run := createWorkflowIdentityTestRun(t, app, userID, "parent-heartbeat", "client-a")
	otherRun := createWorkflowIdentityTestRun(t, app, userID, "other-run", "client-a")
	parent := "validate_subtitles"
	soon := now.Add(10 * time.Second)
	past := now.Add(-time.Second)
	tests := []struct {
		name       string
		stepID     string
		runID      uuid.UUID
		owner      *string
		status     string
		expires    *time.Time
		wantExtend bool
	}{
		{"parent", parent, run.ID, stringPointer("client-a"), "running", &soon, true},
		{"child", parent + ".chunk-1", run.ID, stringPointer("client-a"), "running", &soon, true},
		{"descendant", parent + ".chunk-2.attempt-1", run.ID, stringPointer("client-a"), "running", &soon, true},
		{"prefix lookalike", parent + "-extra.chunk-1", run.ID, stringPointer("client-a"), "running", &soon, false},
		{"underscore is literal", "validateXsubtitles.chunk-1", run.ID, stringPointer("client-a"), "running", &soon, false},
		{"unrelated", "understand-narrative", run.ID, stringPointer("client-a"), "running", &soon, false},
		{"other owner", parent + ".chunk-3", run.ID, stringPointer("client-b"), "running", &soon, false},
		{"expired", parent + ".chunk-4", run.ID, stringPointer("client-a"), "running", &past, false},
		{"expires exactly now", parent + ".chunk-5", run.ID, stringPointer("client-a"), "running", &now, false},
		{"cleared lease", parent + ".chunk-6", run.ID, stringPointer("client-a"), "running", nil, false},
		{"cleared owner", parent + ".chunk-7", run.ID, nil, "running", &soon, false},
		{"succeeded", parent + ".chunk-8", run.ID, stringPointer("client-a"), "succeeded", &soon, false},
		{"interrupted", parent + ".chunk-9", run.ID, stringPointer("client-a"), "interrupted", &soon, false},
		{"other run", parent + ".chunk-1", otherRun.ID, stringPointer("client-a"), "running", &soon, false},
	}
	steps := make([]models.WorkflowStepExecution, len(tests))
	for i, tt := range tests {
		steps[i] = models.WorkflowStepExecution{
			WorkflowRunID: tt.runID,
			StepID:        tt.stepID, InputHash: "input",
			ExecutionKey: workflowStepExecutionKey(tt.runID, tt.stepID, "input"),
			Status:       tt.status, AttemptCount: 1, LeaseOwner: tt.owner, LeaseExpiresAt: tt.expires,
			Usage: datatypes.JSON([]byte("{}")),
		}
		if err := app.db.Create(&steps[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", tt.name, err)
		}
	}
	if _, err := app.workflowRuns.Heartbeat(context.Background(), userID, run.ID, WorkflowRunHeartbeatInput{
		ClientInstanceID: run.ClientInstanceID, CurrentStepID: &parent,
	}); err != nil {
		t.Fatalf("parent heartbeat: %v", err)
	}
	for i, tt := range tests {
		step := loadProviderFinalizationStep(t, app, steps[i].ID)
		want := tt.expires
		if tt.wantExtend {
			want = timePointer(now.Add(workflowRunLeaseDuration))
		}
		if (step.LeaseExpiresAt == nil) != (want == nil) || (want != nil && !step.LeaseExpiresAt.Equal(*want)) {
			t.Errorf("%s: lease = %v, want %v", tt.name, step.LeaseExpiresAt, want)
		}
		if step.Status != tt.status || step.AttemptCount != 1 {
			t.Errorf("%s: heartbeat changed step state: %#v", tt.name, step)
		}
	}
}

func TestWorkflowParentHeartbeatAllowsLongSubtitleCallToFinalizeOnce(t *testing.T) {
	app, user, initial := newProviderFinalizationTestExecution(t, "subtitle-parent-heartbeat")
	base := app.workflowRuns.now()
	current := base
	app.workflowRuns.now = func() time.Time { return current }
	parent := "validate-subtitles"
	identity := workflowRequestIdentityForTest(initial.Run.ID, parent+".chunk-6.attempt-1", initial.Run.InputHash, initial.Run.ClientInstanceID)
	execution, err := app.resolveWorkflowRequestIdentity(context.Background(), user.ID, nil, identity)
	if err != nil {
		t.Fatalf("start subtitle provider call: %v", err)
	}
	for elapsed := 30 * time.Second; elapsed <= 3*time.Minute; elapsed += 30 * time.Second {
		current = base.Add(elapsed)
		if _, err := app.workflowRuns.Heartbeat(context.Background(), user.ID, initial.Run.ID, WorkflowRunHeartbeatInput{
			ClientInstanceID: identity.ClientInstanceID, CurrentStepID: &parent,
		}); err != nil {
			t.Fatalf("parent heartbeat at %s: %v", elapsed, err)
		}
	}
	// A second request must still see the original call in progress after the
	// initial 90-second lease window, instead of starting another paid attempt.
	_, err = app.resolveWorkflowRequestIdentity(context.Background(), user.ID, nil, identity)
	assertWorkflowRequestError(t, err, http.StatusConflict, "step_execution_in_progress")
	input := providerSuccessFinalization{Consumption: providerFinalizationTestConsumption(user.ID)}
	for i := 0; i < 2; i++ {
		if err := app.finalizeProviderSuccess(context.Background(), execution, input); err != nil {
			t.Fatalf("finalize long subtitle call (%d): %v", i, err)
		}
	}
	step := loadProviderFinalizationStep(t, app, execution.Step.ID)
	if step.Status != workflowRunStatusSucceeded || step.AttemptCount != 1 || step.CompletedAt == nil || !step.CompletedAt.Equal(current) {
		t.Fatalf("long call was not finalized on its first attempt: %#v", step)
	}
	var count int64
	if err := app.db.Model(&models.CreditReservation{}).Where("workflow_step_execution_id = ?", step.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("reservation count = %d, err = %v; want exactly one", count, err)
	}
}

func TestWorkflowParentHeartbeatDoesNotRenewCancelledOrForeignRuns(t *testing.T) {
	for _, scenario := range []string{"cancelled", "foreign client", "foreign user"} {
		t.Run(scenario, func(t *testing.T) {
			app, user, execution := newProviderFinalizationTestExecution(t, "parent-heartbeat-rejected")
			parent := "provider"
			userID, clientID := user.ID, execution.Run.ClientInstanceID
			wantErr := ErrWorkflowRunLeaseConflict
			switch scenario {
			case "cancelled":
				if _, err := app.workflowRuns.Cancel(context.Background(), userID, execution.Run.ID); err != nil {
					t.Fatalf("cancel: %v", err)
				}
				wantErr = ErrWorkflowRunTerminal
			case "foreign client":
				clientID = "client-b"
			case "foreign user":
				userID = uuid.New()
				wantErr = ErrWorkflowRunNotFound
			}
			before := loadProviderFinalizationStep(t, app, execution.Step.ID)
			_, err := app.workflowRuns.Heartbeat(context.Background(), userID, execution.Run.ID, WorkflowRunHeartbeatInput{
				ClientInstanceID: clientID, CurrentStepID: &parent,
			})
			if !errors.Is(err, wantErr) {
				t.Fatalf("heartbeat error = %v, want %v", err, wantErr)
			}
			after := loadProviderFinalizationStep(t, app, execution.Step.ID)
			if before.LeaseExpiresAt == nil || after.LeaseExpiresAt == nil || !after.LeaseExpiresAt.Equal(*before.LeaseExpiresAt) {
				t.Fatalf("rejected heartbeat changed child lease: before=%v, after=%v", before.LeaseExpiresAt, after.LeaseExpiresAt)
			}
		})
	}
}
