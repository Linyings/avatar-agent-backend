package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
)

// step() short-circuits when a step is already succeeded. If the rendered
// product never landed, its material record could never be rebuilt and every
// retry failed with "node not ready". The step must reopen so the renderer runs
// again — but only when the file is genuinely absent.
func TestMissingProductReopensSucceededStep(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	need := avatar.MaterialNeed{ID: "n_lost_1", SegmentID: "p1", Kind: "animation", Duration: 3}
	s.Needs = []avatar.MaterialNeed{need}
	s.Steps = []avatar.WorkflowStep{{ID: "F07/" + need.ID, Status: "succeeded", InputHash: "h"}}
	s.Materials = nil

	w.reopenStepWhenProductMissing(&s, "F07/"+need.ID, need)

	if got := s.Steps[0].Status; got != "pending" {
		t.Fatalf("step not reopened for a missing product: %s", got)
	}
	if s.Steps[0].ProductionPhase != "产物缺失，重新渲染" {
		t.Fatalf("unexpected phase: %s", s.Steps[0].ProductionPhase)
	}
}

// The render plan and code are already on disk; reopening must not discard them.
func TestReopenKeepsMotionPlanFiles(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	need := avatar.MaterialNeed{ID: "n_lost_2", SegmentID: "p1", Kind: "animation", Duration: 3}
	s.Needs = []avatar.MaterialNeed{need}
	s.Steps = []avatar.WorkflowStep{{ID: "F07/" + need.ID, Status: "succeeded", InputHash: "h"}}
	materialsDir := filepath.Join(w.dir(s.ID), "materials")
	if err := os.MkdirAll(materialsDir, 0700); err != nil {
		t.Fatal(err)
	}
	planFile := filepath.Join(materialsDir, need.ID+"-motion-plan.json")
	if err := os.WriteFile(planFile, []byte(`{"plan":{"version":"content-motion/1"}}`), 0600); err != nil {
		t.Fatal(err)
	}

	w.reopenStepWhenProductMissing(&s, "F07/"+need.ID, need)

	if _, err := os.Stat(planFile); err != nil {
		t.Fatalf("reopening discarded the cached plan: %v", err)
	}
}

// A present product keeps its step closed; nothing is re-rendered.
func TestExistingProductKeepsStepClosed(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	need := avatar.MaterialNeed{ID: "n_ok_1", SegmentID: "p1", Kind: "animation", Duration: 3}
	s.Needs = []avatar.MaterialNeed{need}
	s.Steps = []avatar.WorkflowStep{{ID: "F07/" + need.ID, Status: "succeeded", InputHash: "h"}}
	materialsDir := filepath.Join(w.dir(s.ID), "materials")
	if err := os.MkdirAll(materialsDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(materialsDir, need.ID+".mp4"), []byte("clip"), 0600); err != nil {
		t.Fatal(err)
	}

	w.reopenStepWhenProductMissing(&s, "F07/"+need.ID, need)

	if got := s.Steps[0].Status; got != "succeeded" {
		t.Fatalf("existing product was reopened: %s", got)
	}
}

// An avatar need whose alpha render exists must stay closed.
func TestAlphaRenderKeepsAvatarStepClosed(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	need := avatar.MaterialNeed{ID: "n_av_2", SegmentID: "p1", Kind: "avatar", Duration: 3}
	s.Needs = []avatar.MaterialNeed{need}
	s.Steps = []avatar.WorkflowStep{{ID: "F06/" + need.ID, Status: "succeeded", InputHash: "h"}}
	materialsDir := filepath.Join(w.dir(s.ID), "materials")
	if err := os.MkdirAll(materialsDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(materialsDir, need.ID+"-alpha.webm"), []byte("webm"), 0600); err != nil {
		t.Fatal(err)
	}

	w.reopenStepWhenProductMissing(&s, "F06/"+need.ID, need)

	if got := s.Steps[0].Status; got != "succeeded" {
		t.Fatalf("avatar step reopened despite alpha render: %s", got)
	}
}

// A recorded material pointing at a live file also keeps the step closed.
func TestRecordedMaterialKeepsStepClosed(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	need := avatar.MaterialNeed{ID: "n_rec_1", SegmentID: "p1", Kind: "animation", Duration: 3}
	s.Needs = []avatar.MaterialNeed{need}
	s.Steps = []avatar.WorkflowStep{{ID: "F07/" + need.ID, Status: "succeeded", InputHash: "h"}}
	materialsDir := filepath.Join(w.dir(s.ID), "materials")
	if err := os.MkdirAll(materialsDir, 0700); err != nil {
		t.Fatal(err)
	}
	custom := "materials/custom-name.mp4"
	if err := os.WriteFile(filepath.Join(w.dir(s.ID), filepath.FromSlash(custom)), []byte("clip"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Materials = []avatar.Material{{NeedID: need.ID, SegmentID: "p1", Kind: "animation", Path: custom, Duration: 3}}

	w.reopenStepWhenProductMissing(&s, "F07/"+need.ID, need)

	if got := s.Steps[0].Status; got != "succeeded" {
		t.Fatalf("step reopened despite a live material record: %s", got)
	}
}
