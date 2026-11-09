package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
)

func interruptedShotsFixture(t *testing.T) (*avatarWorkflowService, avatar.WorkflowState) {
	t.Helper()
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	s.Status, s.Stage, s.Duration = "interrupted", "F10", 4
	s.ScriptConfirmed, s.AudioConfirmed = true, true
	s.Segments = []avatar.Segment{{ID: "p1", Text: "第一句。", Duration: 4}}
	s.Materials = []avatar.Material{{NeedID: "host", SegmentID: "p1", Kind: "avatar", Duration: 4, Path: "materials/host.mp4"}, {NeedID: "visual", SegmentID: "p1", Kind: "animation", Duration: 2, Path: "materials/visual.mp4"}}
	now := time.Now().UTC()
	s.Steps = []avatar.WorkflowStep{{ID: "F09", Status: "succeeded"}, {ID: "F10", Status: "interrupted", Submission: "submitting", Attempt: 1, CostStatus: "unknown", Attempts: []avatar.WorkflowAttempt{{Number: 1, StartedAt: now, CompletedAt: now, Status: "interrupted", CostStatus: "unknown"}}}}
	for _, m := range s.Materials {
		p := filepath.Join(w.dir(s.ID), m.Path)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("fixture media"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.save(&s); err != nil {
		t.Fatal(err)
	}
	w.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		t.Fatal("recovery submitted a paid request")
		return nil, nil
	}
	return w, s
}

func TestAvatarWorkflowInterruptedShotsRetryUsesLocalPlanAndPreservesUnknownBill(t *testing.T) {
	w, s := interruptedShotsFixture(t)
	owner := uuid.MustParse(w.ownerID(&s))
	if err := w.retry(context.Background(), owner, s.ID); err != nil {
		t.Fatal(err)
	}
	got, err := w.load(context.Background(), owner, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	step := got.Steps[1]
	if got.Status != "running" || len(got.Shots) != 2 || step.Submission != "submitting" || step.Attempt != 1 || len(step.Orders) != 1 {
		t.Fatalf("lost recovery/evidence: %#v", got)
	}
	if err := avatar.ValidateWorkflowShots(got.Shots, got.Segments, got.Materials, got.Duration); err != nil {
		t.Fatal(err)
	}
	if len(got.Events) == 0 || !strings.Contains(got.Events[len(got.Events)-1].Message, "费用仍待核对") {
		t.Fatal("missing honest billing warning")
	}
	// A local attempt must not invent another supplier order or erase the original.
	if err := w.step(context.Background(), &got, "F10", "fixture", func(_ context.Context, step *avatar.WorkflowStep) error { step.CostStatus = "reused"; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got.Steps[1].Orders) != 1 || got.Steps[1].Orders[0].Submission != "submitting" || len(got.Billing) != 1 || got.Billing[0].Key != "F10/attempt-1" || got.Billing[0].CostStatus != "unknown" {
		t.Fatalf("billing changed: %#v %#v", got.Steps[1], got.Billing)
	}
	// Even a malformed later plan can never fall through to a new paid submission.
	var out any
	if w.model(context.Background(), &got, &got.Steps[1], "", nil, nil, &out) == nil {
		t.Fatal("uncertain request allowed to resubmit")
	}
}

func TestAvatarWorkflowInterruptedShotsRecoveryFailsClosed(t *testing.T) {
	for _, scenario := range []string{"short", "missing", "other-unknown", "stopped", "not-assembled"} {
		t.Run(scenario, func(t *testing.T) {
			w, s := interruptedShotsFixture(t)
			switch scenario {
			case "short":
				s.Materials[0].Duration = 1
			case "missing":
				os.Remove(filepath.Join(w.dir(s.ID), s.Materials[0].Path))
			case "other-unknown":
				s.Steps = append(s.Steps, avatar.WorkflowStep{ID: "F03", Submission: "unknown", CostStatus: "unknown"})
			case "stopped":
				s.Status, s.StopRequested = "stopped", true
			case "not-assembled":
				s.Steps[0].Status = "failed"
			}
			if err := w.save(&s); err != nil {
				t.Fatal(err)
			}
			if w.retry(context.Background(), uuid.MustParse(w.ownerID(&s)), s.ID) == nil {
				t.Fatal("unsafe recovery allowed")
			}
			got, err := w.load(context.Background(), uuid.MustParse(w.ownerID(&s)), s.ID)
			if err != nil || got.Status != s.Status || len(got.Shots) != 0 {
				t.Fatal("failed recovery partially committed")
			}
		})
	}
}

func TestAvatarWorkflowInterruptedShotsPrefersSavedResponse(t *testing.T) {
	w, s := interruptedShotsFixture(t)
	dir := filepath.Join(w.dir(s.ID), "model-results")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"shots":[{"id":"saved","segment_id":"p1","start":0,"end":4,"primary":"host","card":"none","layout":"full"}]}`)
	if err := os.WriteFile(filepath.Join(dir, avatar.Digest("F10")[:16]+"-attempt-1.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	owner := uuid.MustParse(w.ownerID(&s))
	if err := w.retry(context.Background(), owner, s.ID); err != nil {
		t.Fatal(err)
	}
	got, err := w.load(context.Background(), owner, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Shots) != 1 || got.Shots[0].ID != "saved" || got.Steps[1].LocalRecovery != "saved_response" {
		t.Fatal("saved paid evidence was not reused")
	}
}

func TestAvatarWorkflowInterruptedShotsRetryCompletesWithoutProviderCalls(t *testing.T) {
	w, s := interruptedShotsFixture(t)
	owner := uuid.MustParse(w.ownerID(&s))
	// Model a completed upstream pipeline: only F10 and local rendering remain.
	s.Segments[0].Audio = "audio/p1.wav"
	if err := os.MkdirAll(filepath.Join(w.dir(s.ID), "audio"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.dir(s.ID), s.Segments[0].Audio), []byte("fixture audio"), 0600); err != nil {
		t.Fatal(err)
	}
	f10 := s.Steps[1]
	s.Steps = []avatar.WorkflowStep{}
	for _, cached := range []struct {
		id    string
		input any
	}{
		{"F02", []any{s.Input.Script, s.Input.Topic, s.Descriptions}},
		{"F04/p1", w.audioFingerprint(&s, s.Segments[0])},
		{"F05", []any{s.Segments, s.Input.Style, s.Input.Ratio, s.Input.Direction, s.Input.Angles}},
		{"F09", []any{s.Segments, s.Materials, s.Input.SubtitleEdits}},
	} {
		s.Steps = append(s.Steps, avatar.WorkflowStep{ID: cached.id, Status: "succeeded", InputHash: avatar.Digest(cached.input)})
	}
	s.Steps = append(s.Steps, f10)
	if err := w.save(&s); err != nil {
		t.Fatal(err)
	}
	failRender := true
	w.media = func(_ context.Context, stage, _ string) (json.RawMessage, error) {
		if stage == "audio_review" {
			return json.RawMessage(`{"audio_quality":{"issues":[]}}`), nil
		}
		if stage != "shotbook" && stage != "preview" && stage != "render" {
			t.Fatalf("unexpected regeneration: %s", stage)
		}
		if stage == "render" && failRender {
			return nil, fmt.Errorf("fixture render failure")
		}
		return json.RawMessage(`{"artifacts":[]}`), nil
	}
	for _, expected := range []string{"failed", "succeeded"} {
		if err := w.retry(context.Background(), owner, s.ID); err != nil {
			t.Fatal(err)
		}
		s, _ = w.load(context.Background(), owner, s.ID)
		w.execute(s, owner)
		s, _ = w.load(context.Background(), owner, s.ID)
		if s.Status != expected {
			t.Fatalf("status=%s error=%s", s.Status, s.Error)
		}
		if len(s.Billing) != 1 || s.Billing[0].Key != "F10/attempt-1" || s.Billing[0].CostStatus != "unknown" {
			t.Fatalf("lost/duplicated uncertain charge: %#v", s.Billing)
		}
		failRender = false
	}
}
