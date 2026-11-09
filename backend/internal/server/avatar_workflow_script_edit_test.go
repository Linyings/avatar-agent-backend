package server

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
)

func TestWorkflowSaveScriptReviewVersionAndContinuation(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	s.Status, s.Stage, s.Confirmation, s.ConfirmationVersion = "awaiting_confirmation", "F03", "script", "v1"
	s.Script = s.Input.Script
	s.Segments = []avatar.Segment{{ID: "p1", Text: "第一句。"}, {ID: "p2", Text: "第二句。"}}
	s.TargetNode = "F03"
	s.Steps = []avatar.WorkflowStep{{ID: "F02", Status: "succeeded"}, {ID: "F03", Status: "succeeded"}}
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	input := []avatar.Segment{{ID: "p1", Chapter: "开场", Text: "手动修改的第一句。", Audio: "../../secret", Duration: 9, Fingerprint: "injected"}, {ID: "p2", Text: "第二句。"}}
	if err := a.avatarWorkflow.saveScriptReview(context.Background(), owner, s.ID, "v1", input); err != nil {
		t.Fatal(err)
	}
	got, err := a.avatarWorkflow.load(context.Background(), owner, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "awaiting_confirmation" || got.ScriptConfirmed || got.Script != "手动修改的第一句。第二句。" || got.ConfirmationVersion == "v1" {
		t.Fatalf("incorrect saved review: %#v", got)
	}
	if got.Segments[0].Audio != "" || got.Segments[0].Duration != 0 || got.Segments[0].Fingerprint != "" {
		t.Fatal("client injected media metadata")
	}
	if err = a.avatarWorkflow.saveScriptReview(context.Background(), owner, s.ID, "v1", input); err == nil {
		t.Fatal("stale save accepted")
	}
	if err = a.avatarWorkflow.confirm(context.Background(), owner, s.ID, "script", "v1", nil); err == nil {
		t.Fatal("stale approval accepted")
	}
	if err = a.avatarWorkflow.saveScriptReview(context.Background(), uuid.New(), s.ID, got.ConfirmationVersion, input); err == nil {
		t.Fatal("another owner saved review")
	}
	if err = a.avatarWorkflow.confirm(context.Background(), owner, s.ID, "script", got.ConfirmationVersion, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = a.avatarWorkflow.load(context.Background(), owner, s.ID)
	a.avatarWorkflow.execute(got, owner)
	got, _ = a.avatarWorkflow.load(context.Background(), owner, s.ID)
	if got.Status != "paused" || !got.ScriptConfirmed || got.Script != "手动修改的第一句。第二句。" {
		t.Fatalf("continuation lost edit: status=%s script=%s error=%s", got.Status, got.Script, got.Error)
	}
}

func TestWorkflowSaveScriptRejectsInvalidOrNonReviewState(t *testing.T) {
	for _, status := range []string{"running", "awaiting_confirmation"} {
		t.Run(status, func(t *testing.T) {
			a, owner, _, s := workflowFixture(t)
			s.Status, s.Confirmation, s.ConfirmationVersion = status, "script", "v1"
			s.Script = s.Input.Script
			s.Segments = []avatar.Segment{{ID: "p1", Text: s.Script}}
			if err := a.avatarWorkflow.save(&s); err != nil {
				t.Fatal(err)
			}
			for _, segments := range [][]avatar.Segment{nil, {{ID: "p1", Text: " "}}, {{ID: "p1", Text: "一"}, {ID: "p1", Text: "二"}}} {
				if err := a.avatarWorkflow.saveScriptReview(context.Background(), owner, s.ID, "v1", segments); err == nil {
					t.Fatal("invalid edit accepted")
				}
			}
			if status == "running" {
				if err := a.avatarWorkflow.saveScriptReview(context.Background(), owner, s.ID, "v1", []avatar.Segment{{ID: "p1", Text: "新稿"}}); err == nil {
					t.Fatal("running task edited")
				}
			}
			got, _ := a.avatarWorkflow.load(context.Background(), owner, s.ID)
			if got.Script != s.Script || got.ConfirmationVersion != "v1" {
				t.Fatal("rejected write changed saved state")
			}
		})
	}
}

func TestWorkflowSaveScriptRejectsSupersededRun(t *testing.T) {
	a, owner, p, s := workflowFixture(t)
	s.Status, s.Confirmation, s.ConfirmationVersion = "awaiting_confirmation", "script", "v1"
	s.Script = s.Input.Script
	s.Segments = []avatar.Segment{{ID: "p1", Text: s.Script}}
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	if _, err := a.avatarWorkflow.create(context.Background(), owner, p.ID, "newer-run", 1); err != nil {
		t.Fatal(err)
	}
	if err := a.avatarWorkflow.saveScriptReview(context.Background(), owner, s.ID, "v1", []avatar.Segment{{ID: "p1", Text: "旧页面的修改"}}); err == nil {
		t.Fatal("superseded run accepted edits")
	}
	got, _ := a.avatarWorkflow.load(context.Background(), owner, s.ID)
	if got.Script != s.Script || got.ConfirmationVersion != "v1" {
		t.Fatal("rejected write changed old run")
	}
}
