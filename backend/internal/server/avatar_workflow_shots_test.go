package server

import (
	"context"
	"encoding/json"
	"github.com/narratoai/backend-go/internal/avatar"
	"os"
	"path/filepath"
	"testing"
)

func TestAvatarWorkflowRetryRevalidatesCompletedShotsWithoutPaidCall(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	s.Status = "failed"
	s.Error = "分镜使用区间超出真实素材，不能冻结或循环补时长"
	s.Duration = 28.940626
	json.Unmarshal([]byte(`[{"id":"p1","start":0,"duration":21.373834000000002},{"id":"p3","start":21.373834000000002,"duration":7.566792}]`), &s.Segments)
	s.Materials = []avatar.Material{{NeedID: "a1", SegmentID: "p1", Kind: "avatar", Duration: s.Segments[0].Duration}, {NeedID: "a3", SegmentID: "p3", Kind: "avatar", Duration: s.Segments[1].Duration}}
	s.Steps = []avatar.WorkflowStep{{ID: "F10", Status: "failed", Submission: "completed", Attempt: 2, Error: s.Error, Usage: json.RawMessage(`{"total_tokens":"7374"}`), CostStatus: "unknown"}}
	if err := w.save(&s); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(w.dir(s.ID), "model-results")
	os.MkdirAll(dir, 0700)
	file := filepath.Join(dir, avatar.Digest("F10")[:16]+"-attempt-2.json")
	raw := []byte(`{"shots":[{"id":"S1","segment_id":"p1","start":0,"end":21.373834,"primary":"a1","card":"impact-open-title","layout":"full"},{"id":"S4","segment_id":"p3","start":21.373834,"end":28.940626,"primary":"a3","card":"chapter-title-card","layout":"full"}]}`)
	os.WriteFile(file, raw, 0600)
	w.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		t.Fatal("retry submitted paid model request")
		return nil, nil
	}
	if err := w.retry(context.Background(), owner, s.ID); err != nil {
		t.Fatal(err)
	}
	got, err := w.load(context.Background(), owner, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "running" || len(got.Shots) != 2 || got.Shots[1].PrimaryIn != 0 {
		t.Fatalf("saved plan was not recovered: %#v", got.Shots)
	}
	if got.Steps[0].Attempt != 2 || string(got.Steps[0].Usage) != string(s.Steps[0].Usage) {
		t.Fatal("prior billing evidence changed")
	}
	if err := avatar.ValidateWorkflowShots(got.Shots, got.Segments, got.Materials, got.Duration); err != nil {
		t.Fatal(err)
	}
	// Truly insufficient footage must still fail and must not trigger another model request.
	got.Status = "failed"
	got.Shots = nil
	got.Materials[1].Duration = 2
	w.save(&got)
	if w.retry(context.Background(), owner, s.ID) == nil {
		t.Fatal("short material accepted on recovery")
	}
}
