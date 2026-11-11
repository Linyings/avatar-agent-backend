package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/narratoai/backend-go/internal/avatar"
)

func floatPtr(v float64) *float64 { return &v }

// Concurrent media stages once wrote a second record for the same need with
// another need's narration start. F10 indexes materials by need ID, so the
// stray copy produced a negative source in-point. Keep the record that agrees
// with the planned need.
func TestSanitizeMaterialsDropsDuplicateWithWrongNarrationStart(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	need := avatar.MaterialNeed{ID: "n_a_1", SegmentID: "p1", Kind: "animation", Duration: 14, NarrationStart: floatPtr(0)}
	s.Needs = []avatar.MaterialNeed{need}
	s.Steps = []avatar.WorkflowStep{{ID: "F07/n_a_1", Status: "succeeded"}}
	s.Materials = []avatar.Material{
		{NeedID: "n_a_1", SegmentID: "p1", Kind: "animation", Path: "materials/n_a_1.mp4", Duration: 14, NarrationStart: floatPtr(0)},
		{NeedID: "n_a_1", SegmentID: "p1", Kind: "animation", Path: "materials/n_a_1.mp4", Duration: 14, NarrationStart: floatPtr(2.96)},
	}

	w.sanitizeMaterials(&s)

	if len(s.Materials) != 1 {
		t.Fatalf("duplicate kept: %#v", s.Materials)
	}
	if got := *s.Materials[0].NarrationStart; got != 0 {
		t.Fatalf("kept the stray narration start %v", got)
	}
	if s.Steps[0].Status != "succeeded" {
		t.Fatal("a valid survivor must not reopen its step")
	}
}

// A record pointing at another need's file is not this need's output. Drop it
// and reopen the step so the clip is produced again (locally, no model call).
func TestSanitizeMaterialsDropsCrossLinkedRecordAndReopensStep(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	s.Needs = []avatar.MaterialNeed{
		{ID: "n_p1", SegmentID: "p1", Kind: "avatar", Duration: 14},
		{ID: "n_p2", SegmentID: "p2", Kind: "avatar", Duration: 8},
	}
	s.Steps = []avatar.WorkflowStep{{ID: "F06/n_p1", Status: "succeeded"}, {ID: "F06/n_p2", Status: "succeeded"}}
	s.Materials = []avatar.Material{
		{NeedID: "n_p1", SegmentID: "p1", Kind: "avatar", Path: "materials/n_p1-alpha.webm", Duration: 14},
		{NeedID: "n_p2", SegmentID: "p2", Kind: "avatar", Path: "materials/n_p1-alpha.webm", Duration: 8},
	}

	w.sanitizeMaterials(&s)

	if len(s.Materials) != 1 || s.Materials[0].NeedID != "n_p1" {
		t.Fatalf("cross-linked record survived: %#v", s.Materials)
	}
	if s.Steps[1].Status != "pending" {
		t.Fatalf("step of the dropped record not reopened: %s", s.Steps[1].Status)
	}
	if s.Steps[0].Status != "succeeded" {
		t.Fatal("unaffected step reopened")
	}
}

// Records rebuilt from disk must carry the planned narration start, otherwise
// F10 treats an animation as starting at the segment origin.
func TestRecoveredAnimationKeepsNarrationStart(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	need := avatar.MaterialNeed{ID: "n_late", SegmentID: "p1", Kind: "animation", Duration: 3, NarrationStart: floatPtr(2.5)}
	s.Needs = []avatar.MaterialNeed{need}
	writeMaterialFile(t, w, s.ID, need.ID+".mp4")

	if err := w.recoverMaterialFromCache(&s, need); err != nil {
		t.Fatal(err)
	}
	if s.Materials[0].NarrationStart == nil || *s.Materials[0].NarrationStart != 2.5 {
		t.Fatalf("narration start lost: %#v", s.Materials[0].NarrationStart)
	}
}

func writeMaterialFile(t *testing.T, w *avatarWorkflowService, runID, name string) {
	t.Helper()
	dir := filepath.Join(w.dir(runID), "materials")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("clip"), 0600); err != nil {
		t.Fatal(err)
	}
}

// Media stages share control files in the run directory, so two stages of the
// same run must never overlap. Different runs may.
func TestMediaStagesOfOneRunNeverOverlap(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	if err := os.MkdirAll(w.dir(s.ID), 0700); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	active, peak := 0, 0
	w.media = func(context.Context, string, string) (json.RawMessage, error) {
		mu.Lock()
		active++
		if active > peak {
			peak = active
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return json.RawMessage(`{}`), nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			copyState := s
			if err := w.mediaStep(context.Background(), &copyState, "material", map[string]any{}, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if peak != 1 {
		t.Fatalf("media stages of one run overlapped: peak %d", peak)
	}
}
