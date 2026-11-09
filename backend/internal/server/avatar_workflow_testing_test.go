package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/narratoai/backend-go/internal/avatar"
)

func TestTestingRecoveryCreatesNewRunWithCurrentVoiceAndKeepsHistory(t *testing.T) {
	t.Setenv("AVATAR_WORKFLOW_TEST_MODE", "true")
	a, owner, p, old := workflowFixture(t)
	a.cfg.TTSModel = "minimax/speech-2.8-hd"
	t.Setenv("RUNNINGHUB_API_KEY", "local-test")
	reviewTextConfig(a)
	old.Status = "submission_unknown"
	old.Input.Voice = "zh_male_shaonianzixin_uranus_bigtts"
	old.ScriptConfirmed = true
	old.Script = old.Input.Script
	old.Segments = []avatar.Segment{{ID: "p1", Text: old.Script}}
	old.Steps = []avatar.WorkflowStep{{ID: "F04/p1", Status: "failed", Submission: "unknown", Attempt: 1}}
	if err := a.avatarWorkflow.save(&old); err != nil {
		t.Fatal(err)
	}
	baseline, _ := a.avatarWorkflow.load(context.Background(), owner, old.ID)
	if _, err := (avatarPackageHost{a}).Invoke(context.Background(), owner.String(), p.ID.String(), "avatar.configure", map[string]any{"voice": "Wise_Woman"}, "fix-voice"); err != nil {
		t.Fatal(err)
	}
	next, err := a.avatarWorkflow.retryRun(context.Background(), owner, old.ID, "F04", "user-retry-1")
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == old.ID || next.Input.Voice != "Wise_Woman" || !next.ScriptConfirmed || next.Script != old.Script || next.TargetNode != "F04" || next.Status != "running" {
		t.Fatalf("wrong recovery: %#v", next)
	}
	after, _ := a.avatarWorkflow.load(context.Background(), owner, old.ID)
	if avatar.Digest(after) != avatar.Digest(baseline) {
		t.Fatal("original unknown order was rewritten")
	}
	// Simulate another supplier failure. Replaying the SAME user action must not
	// create a third run, even if the caller now supplies the latest run ID.
	next.Status = "submission_unknown"
	next.Steps = []avatar.WorkflowStep{{ID: "F04/p1", Status: "failed", Submission: "unknown", Attempt: 1}}
	if err = a.avatarWorkflow.save(&next); err != nil {
		t.Fatal(err)
	}
	replayed, err := a.avatarWorkflow.retryRun(context.Background(), owner, next.ID, "F04", "user-retry-1")
	if err != nil || replayed.ID != next.ID || replayed.Status != "submission_unknown" {
		t.Fatalf("retry replay duplicated work: %#v %v", replayed, err)
	}
}

func TestTestingRecoveryPreservesKnownTaskAndReleasesOnlyMissingTask(t *testing.T) {
	t.Setenv("AVATAR_WORKFLOW_TEST_MODE", "true")
	a, owner, _, s := workflowFixture(t)
	s.Status = "submission_unknown"
	s.Steps = []avatar.WorkflowStep{
		{ID: "F04/p1", Status: "failed", Submission: "submitted", ProviderTaskID: "original-order", Attempt: 1},
		{ID: "F04/p2", Status: "failed", Submission: "unknown", Attempt: 1},
	}
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	next, err := a.avatarWorkflow.retryRun(context.Background(), owner, s.ID, "F04", "mixed-retry")
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != s.ID || next.Steps[0].ProviderTaskID != "original-order" || next.Steps[0].Submission != "submitted" {
		t.Fatal("known task was replaced")
	}
	if next.Steps[1].Submission != "" || len(next.Steps[1].Orders) != 1 || next.Steps[1].Orders[0].Submission != "unknown" {
		t.Fatal("unknown attempt was not preserved before retry")
	}
	next.Status = "failed"
	if err := a.avatarWorkflow.save(&next); err != nil {
		t.Fatal(err)
	}
	replayed, err := a.avatarWorkflow.retryRun(context.Background(), owner, s.ID, "F04", "mixed-retry")
	if err != nil || replayed.Status != "failed" {
		t.Fatalf("same action restarted an in-place retry: status=%s err=%v", replayed.Status, err)
	}
}

func TestTestingRecoveryPackageActuallyRunsOnceAndExposesMode(t *testing.T) {
	t.Setenv("AVATAR_WORKFLOW_TEST_MODE", "true")
	a, owner, p, s := workflowFixture(t)
	pool, _ := a.db.DB()
	pool.SetMaxOpenConns(1)
	var submissions atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submissions.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer provider.Close()
	t.Setenv("RUNNINGHUB_BASE_URL", provider.URL)
	t.Setenv("RUNNINGHUB_API_KEY", "local-test")
	a.cfg.TTSModel = "minimax/speech-2.8-hd"
	reviewTextConfig(a)
	s.Status, s.TargetNode = "submission_unknown", "F04"
	s.Input.Voice = "Wise_Woman"
	s.ScriptConfirmed = true
	s.Segments = []avatar.Segment{{ID: "p1", Text: s.Input.Script}}
	s.Steps = []avatar.WorkflowStep{{ID: "F04/p1", Status: "failed", Submission: "unknown"}}
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	h := avatarPackageHost{a}
	ctx := context.Background()
	snapshot, err := h.snapshot(ctx, owner.String(), p.ID.String())
	if err != nil || snapshot.(gin.H)["testing_mode"] != true {
		t.Fatalf("mode missing: %v", err)
	}
	if submissions.Load() != 0 {
		t.Fatal("reading snapshot submitted work")
	}
	_, err = h.Invoke(ctx, owner.String(), p.ID.String(), "avatar.configure", map[string]any{"voice": "Wise_Woman"}, "configure")
	if err != nil {
		t.Fatal(err)
	}
	out, err := h.Invoke(ctx, owner.String(), p.ID.String(), "avatar.control", map[string]any{"action": "retry"}, "one-user-action")
	if err != nil {
		t.Fatal(err)
	}
	id := out.(gin.H)["run_id"].(string)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, active := workflowLaunchLocks.Load(id)
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	next, err := a.avatarWorkflow.load(ctx, owner, id)
	if err != nil || next.Status != "failed" || submissions.Load() != 1 {
		t.Fatalf("retry did not execute: status=%s calls=%d err=%v", next.Status, submissions.Load(), err)
	}
	out, err = h.Invoke(ctx, owner.String(), p.ID.String(), "avatar.control", map[string]any{"action": "retry"}, "one-user-action")
	if err != nil || out.(gin.H)["run_id"] != id || out.(gin.H)["submitted"] != false || submissions.Load() != 1 {
		t.Fatalf("replay repeated submission: %v %v", out, err)
	}
}

func TestTestingRecoveryRequiresServerModeAndDoesNotBypassActiveRun(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	s.Status = "submission_unknown"
	s.Steps = []avatar.WorkflowStep{{ID: "F04/p1", Status: "failed", Submission: "unknown", Attempt: 1}}
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AVATAR_WORKFLOW_TEST_MODE", "false")
	if _, err := a.avatarWorkflow.retryRun(context.Background(), owner, s.ID, "F04", "strict"); err == nil {
		t.Fatal("strict mode bypassed")
	}
	t.Setenv("AVATAR_WORKFLOW_TEST_MODE", "true")
	s.Status = "running"
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	if _, err := a.avatarWorkflow.retryRun(context.Background(), owner, s.ID, "F04", "active"); err == nil {
		t.Fatal("active run restarted")
	}
}
