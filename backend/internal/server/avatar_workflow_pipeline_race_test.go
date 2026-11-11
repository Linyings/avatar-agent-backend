package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
)

// Concurrent segments produce materials into one shared WorkflowState. Without
// a lock an append loses entries, so F09 later reports a node as missing even
// though its F07 step succeeded. This reproduces the shape: many needs, each
// recording a material from its own goroutine.
func TestConcurrentMaterialRecordingKeepsEveryEntry(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	if err := os.MkdirAll(filepath.Join(w.dir(s.ID), "materials"), 0700); err != nil {
		t.Fatal(err)
	}

	const count = 24
	needs := make([]avatar.MaterialNeed, count)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("n_%02d", i)
		needs[i] = avatar.MaterialNeed{ID: id, SegmentID: "p1", Kind: "animation", Duration: 2}
		file := filepath.Join(w.dir(s.ID), "materials", id+".mp4")
		if err := os.WriteFile(file, []byte("unit-test-material"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s.Needs = needs

	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, need := range needs {
		need := need
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Mirror what produceMaterial does when a clip is finished.
			material := avatar.Material{
				NeedID: need.ID, SegmentID: need.SegmentID, Kind: need.Kind,
				Path: "materials/" + need.ID + ".mp4", Duration: need.Duration,
			}
			w.withState(func() {
				found := false
				for i := range s.Materials {
					if s.Materials[i].NeedID == need.ID {
						s.Materials[i] = material
						found = true
					}
				}
				if !found {
					s.Materials = append(s.Materials, material)
				}
			})
			w.addArtifacts(&s, []avatar.WorkflowArtifact{{ID: "material-" + need.ID, Path: material.Path}})
		}()
	}
	close(start)
	wg.Wait()

	if len(s.Materials) != count {
		t.Fatalf("materials lost to concurrent append: got %d want %d", len(s.Materials), count)
	}
	if len(s.Artifacts) != count {
		t.Fatalf("artifacts lost to concurrent append: got %d want %d", len(s.Artifacts), count)
	}
	seen := map[string]bool{}
	for _, m := range s.Materials {
		if seen[m.NeedID] {
			t.Fatalf("duplicate material for %s", m.NeedID)
		}
		seen[m.NeedID] = true
		// A record must point at its own file, never another need's.
		if m.Path != "materials/"+m.NeedID+".mp4" {
			t.Fatalf("material %s points at %s", m.NeedID, m.Path)
		}
	}
	for _, need := range needs {
		if !seen[need.ID] {
			t.Fatalf("material %s missing entirely", need.ID)
		}
	}
}

// A run recorded by an older build may have a succeeded step but no material
// entry. Assembly must rebuild it from the rendered file instead of failing.
func TestAssemblyRecoversMaterialMissingFromState(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	need := avatar.MaterialNeed{ID: "n_lost_1", SegmentID: "p1", Kind: "animation", Duration: 4}
	s.Needs = []avatar.MaterialNeed{need}
	s.Segments = []avatar.Segment{{ID: "p1", Text: "第一句。", Duration: 4}}
	// Step succeeded, but no material record survived.
	s.Steps = []avatar.WorkflowStep{{ID: "F07/" + need.ID, Status: "succeeded", InputHash: "x"}}
	s.Materials = nil

	if err := os.MkdirAll(filepath.Join(w.dir(s.ID), "materials"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(w.dir(s.ID), "materials", need.ID+".mp4")
	if err := os.WriteFile(file, []byte("rendered-clip"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := w.validateMaterialAssembly(&s); err != nil {
		t.Fatalf("assembly blocked on a recoverable material: %v", err)
	}
	if len(s.Materials) != 1 || s.Materials[0].NeedID != need.ID {
		t.Fatalf("material not recovered: %#v", s.Materials)
	}
}

// Recovery must not invent a material when the rendered file is genuinely gone.
func TestAssemblyStillFailsWhenMaterialFileIsGone(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	need := avatar.MaterialNeed{ID: "n_gone_1", SegmentID: "p1", Kind: "animation", Duration: 4}
	s.Needs = []avatar.MaterialNeed{need}
	s.Segments = []avatar.Segment{{ID: "p1", Text: "第一句。", Duration: 4}}
	s.Steps = []avatar.WorkflowStep{{ID: "F07/" + need.ID, Status: "succeeded", InputHash: "x"}}
	if err := w.validateMaterialAssembly(&s); err == nil {
		t.Fatal("assembly accepted a material with no file")
	}
}

// Avatar materials are keyed on the alpha render; recovery must find it.
func TestRecoverMaterialPrefersAlphaForAvatar(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	need := avatar.MaterialNeed{ID: "n_av_1", SegmentID: "p1", Kind: "avatar", Duration: 3}
	s.Needs = []avatar.MaterialNeed{need}
	s.Segments = []avatar.Segment{{ID: "p1", Text: "一。", Duration: 3}}
	s.Steps = []avatar.WorkflowStep{{ID: "F06/" + need.ID, Status: "succeeded", InputHash: "x"}}
	if err := os.MkdirAll(filepath.Join(w.dir(s.ID), "materials"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{need.ID + ".mp4", need.ID + "-alpha.webm"} {
		if err := os.WriteFile(filepath.Join(w.dir(s.ID), "materials", name), []byte("clip"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.validateMaterialAssembly(&s); err != nil {
		t.Fatal(err)
	}
	if len(s.Materials) != 1 {
		t.Fatalf("material not recovered: %#v", s.Materials)
	}
	var decoded map[string]any
	raw, _ := json.Marshal(s.Materials[0])
	_ = json.Unmarshal(raw, &decoded)
	if s.Materials[0].Path != "materials/"+need.ID+"-alpha.webm" {
		t.Fatalf("avatar recovery should prefer alpha render, got %s", s.Materials[0].Path)
	}
}
