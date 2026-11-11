package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
)

func fileExists(t *testing.T, w *avatarWorkflowService, runID, name string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(w.dir(runID), "materials", name))
	return err == nil
}

// A motion design reused from an earlier run carries that run's timing. When
// the segment's speech was regenerated, the reused code can fail the render
// check every time. Once rejected for the current speech, reuse must be
// skipped so the next attempt designs fresh instead of looping on one error.
func TestRejectedMotionReuseIsSkippedForSameSpeech(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	need := avatar.MaterialNeed{ID: "n_m_1", SegmentID: "p1", Kind: "animation", Duration: 4}
	seg := avatar.Segment{ID: "p1", Fingerprint: "speech-v2"}
	writeMaterialFile(t, w, s.ID, need.ID+"-motion-plan.json")

	if w.motionReuseRejected(&s, need, seg) {
		t.Fatal("reuse rejected before any failure")
	}
	if err := w.rejectMotionReuse(&s, need, seg, "场景 3 关键内容未完整展示"); err != nil {
		t.Fatal(err)
	}
	if !w.motionReuseRejected(&s, need, seg) {
		t.Fatal("rejection not remembered")
	}
	if fileExists(t, w, s.ID, need.ID+"-motion-plan.json") {
		t.Fatal("rejected plan still cached; the next attempt would render it again")
	}
}

// New speech means new timing; an older rejection must not block reuse.
func TestMotionReuseRejectionIsScopedToSpeech(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	need := avatar.MaterialNeed{ID: "n_m_2", SegmentID: "p1", Kind: "animation", Duration: 4}
	if err := w.rejectMotionReuse(&s, need, avatar.Segment{ID: "p1", Fingerprint: "speech-v1"}, "x"); err != nil {
		t.Fatal(err)
	}
	if w.motionReuseRejected(&s, need, avatar.Segment{ID: "p1", Fingerprint: "speech-v2"}) {
		t.Fatal("rejection leaked to different speech")
	}
}
