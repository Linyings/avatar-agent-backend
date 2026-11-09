package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
)

func TestShotBatchesResumeFromSavedResponsesAndInvalidateChangedInputs(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	s.Duration = 8
	s.Segments = []avatar.Segment{{ID: "p1", Text: "第一句", Duration: 4}, {ID: "p2", Text: "第二句", Start: 4, Duration: 4}}
	s.Materials = []avatar.Material{{NeedID: "a1", SegmentID: "p1", Kind: "avatar", Duration: 4}, {NeedID: "a2", SegmentID: "p2", Kind: "avatar", Duration: 4}}
	w.save(&s)
	calls := map[string]int{}
	w.content = func(_ context.Context, _ string, input any, _ []string) (json.RawMessage, error) {
		raw, _ := json.Marshal(input)
		var b avatar.ShotPlanningBatch
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatal(err)
		}
		calls[b.Segment.ID]++
		if b.Segment.ID == "p2" && calls["p2"] == 2 && !strings.Contains(string(raw), "模型输出被截断") {
			t.Fatal("shot retry omitted the original error")
		}
		if b.Segment.ID == "p2" && calls["p2"] == 1 {
			return nil, completedModelOutputFixture{}
		}
		return json.Marshal(map[string]any{"shots": []avatar.WorkflowShot{{Start: b.Window.Start, End: b.Window.End, Primary: b.Materials[0].NeedID, Card: "none", Layout: "full", Recipe: "host-hero"}}})
	}
	if _, err := w.planShotBatches(context.Background(), &s); err == nil || !strings.Contains(err.Error(), "截断") {
		t.Fatalf("expected truncation: %v", err)
	}
	if calls["p1"] != 1 || calls["p2"] != 1 {
		t.Fatal(calls)
	}
	files, _ := os.ReadDir(filepath.Join(w.dir(s.ID), "shot-plans"))
	for _, f := range files {
		os.Remove(filepath.Join(w.dir(s.ID), "shot-plans", f.Name()))
	}
	s, _ = w.load(context.Background(), owner, s.ID)
	shots, err := w.planShotBatches(context.Background(), &s)
	if err != nil {
		t.Fatal(err)
	}
	if len(shots) != 2 || calls["p1"] != 1 || calls["p2"] != 2 {
		t.Fatalf("completed segment repeated: %v", calls)
	}
	s.Input.Direction = "改变小窗方位"
	w.save(&s)
	if _, err = w.planShotBatches(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	if calls["p1"] != 2 || calls["p2"] != 3 {
		t.Fatal("stale input reused", calls)
	}
	s.Materials[0].Fingerprint = "changed video"
	w.save(&s)
	if _, err = w.planShotBatches(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	if calls["p1"] != 3 || calls["p2"] != 3 {
		t.Fatal("source identity was lost during compression", calls)
	}
	// Losing both durable copies must never resubmit a completed paid batch.
	for _, dir := range []string{"shot-plans", "model-results"} {
		entries, _ := os.ReadDir(filepath.Join(w.dir(s.ID), dir))
		for _, f := range entries {
			os.Remove(filepath.Join(w.dir(s.ID), dir, f.Name()))
		}
	}
	if _, err = w.planShotBatches(context.Background(), &s); err == nil {
		t.Fatal("missing results silently regenerated")
	}
	if calls["p1"] != 3 || calls["p2"] != 3 {
		t.Fatal("completed batch was charged again", calls)
	}
}

func TestShotParentPreservesLegacyChargeBeforeLocalAttempt(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	s.Steps = []avatar.WorkflowStep{{ID: "F10", Status: "failed", Submission: "completed", Attempt: 1, Usage: json.RawMessage(`{"completion_tokens":6000}`), CostStatus: "unknown"}}
	w.save(&s)
	if err := w.step(context.Background(), &s, "F10", "local assembly", func(_ context.Context, step *avatar.WorkflowStep) error {
		if step.Submission != "" || len(step.Usage) > 0 || step.CostStatus != "local_compute" {
			t.Fatal("legacy charge leaked into local attempt")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	step := s.Steps[0]
	if len(step.Orders) != 1 || step.Orders[0].Attempt != 1 || !strings.Contains(string(step.Orders[0].Usage), "6000") {
		t.Fatal("lost legacy cost evidence")
	}
	if step.Attempt != 2 || len(step.Attempts) != 1 || len(step.Attempts[0].Usage) > 0 {
		t.Fatal("local assembly counted old tokens again")
	}
}

func TestShotBatchPreflightDoesNotCreateProviderOrder(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	w.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		t.Fatal("oversized request submitted")
		return nil, nil
	}
	step := avatar.WorkflowStep{ID: "F10/p1"}
	s.Steps = append(s.Steps, step)
	var out any
	err := w.model(avatar.WithShotPlanning(context.Background()), &s, &s.Steps[len(s.Steps)-1], "plan", strings.Repeat("x", 50000), nil, &out)
	if err == nil || s.Steps[len(s.Steps)-1].Submission != "" {
		t.Fatal("preflight recorded uncertain paid order")
	}
}

func TestShotBatchProviderLocalRejectionDoesNotLeaveUnknownOrder(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	w.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		return nil, &avatar.ContentPreflightError{Message: "local input rejected"}
	}
	s.Steps = append(s.Steps, avatar.WorkflowStep{ID: "F10/batch-001", Attempt: 1})
	var out any
	step := &s.Steps[len(s.Steps)-1]
	if err := w.model(avatar.WithShotPlanning(context.Background()), &s, step, "plan", "small", nil, &out); err == nil {
		t.Fatal("local rejection ignored")
	}
	if step.Submission != "" || len(step.Orders) != 0 || step.CostStatus != "local_compute" {
		t.Fatal("invented uncertain charge")
	}
}

func TestShotBatchUnknownRequestIsNeverResubmitted(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	s.Segments = []avatar.Segment{{ID: "p1", Text: "第一句", Duration: 4}}
	s.Duration = 4
	s.Materials = []avatar.Material{{NeedID: "a1", SegmentID: "p1", Kind: "avatar", Duration: 4}}
	w.save(&s)
	calls := 0
	w.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		calls++
		return nil, fmt.Errorf("uncertain network response")
	}
	for i := 0; i < 2; i++ {
		if _, err := w.planShotBatches(context.Background(), &s); err == nil {
			t.Fatal("uncertain request accepted")
		}
	}
	for _, step := range s.Steps {
		if step.ID == "F10/batch-001" && (step.Attempt != 1 || len(step.Orders) != 1) {
			t.Fatal("created another uncertain order")
		}
	}
	if calls != 1 {
		t.Fatal("unknown paid request repeated")
	}
}

// Opt-in, read-only validation against a saved run; never calls a provider.
func TestShotBatchSavedRunPreflight(t *testing.T) {
	path := os.Getenv("AVATAR_SHOT_PREFLIGHT_FILE")
	if path == "" {
		t.Skip("saved-run preflight is opt-in")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s avatar.WorkflowState
	if err = json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	batches, err := avatar.BuildShotPlanningBatches(&s)
	if err != nil {
		t.Fatal(err)
	}
	smallest, largest := int(^uint(0)>>1), 0
	var all []avatar.WorkflowShot
	for _, b := range batches {
		if err := avatar.CheckContentInput(avatar.WithShotPlanning(context.Background()), shotBatchPrompt, b); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(b)
		smallest = min(smallest, len(data))
		largest = max(largest, len(data))
		primary := ""
		for _, m := range b.Materials {
			if m.Kind == "avatar" {
				primary = m.NeedID
				break
			}
		}
		if primary == "" {
			t.Fatal("fixture needs avatar material for local timeline validation")
		}
		shots, err := avatar.NormalizeShotPlanningBatch(b, []avatar.WorkflowShot{{Start: b.Window.Start, End: b.Window.End, Primary: primary, Card: "none", Layout: "full", Recipe: "host-hero"}})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, shots...)
	}
	if err := avatar.ValidateWorkflowShots(all, s.Segments, s.Materials, s.Duration); err != nil {
		t.Fatal(err)
	}
	t.Logf("segments=%d materials=%d duration=%.6fs batches=%d batch_bytes=%d..%d prompt_bytes=%d local_timeline_valid=true provider_calls=0", len(s.Segments), len(s.Materials), s.Duration, len(batches), smallest, largest, len(shotBatchPrompt))
}
