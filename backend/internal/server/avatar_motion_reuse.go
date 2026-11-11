package server

import (
	"os"
	"path/filepath"

	"github.com/narratoai/backend-go/internal/avatar"
)

// A motion design reused from an earlier run was timed against that run's
// speech. When the segment's speech was regenerated the reused code can fail
// the render check on every attempt, and since reuse returns the same plan the
// retry loops on one error. The rejection is recorded per need and speech
// fingerprint, so the next attempt designs fresh for the current timing.

func motionReuseKey(need avatar.MaterialNeed, seg avatar.Segment) string {
	return need.ID + "@" + seg.Fingerprint
}

func (w *avatarWorkflowService) motionReuseRejected(s *avatar.WorkflowState, need avatar.MaterialNeed, seg avatar.Segment) bool {
	key := motionReuseKey(need, seg)
	rejected := false
	w.withState(func() {
		for _, k := range s.RejectedMotionReuse {
			if k == key {
				rejected = true
				return
			}
		}
	})
	return rejected
}

// rejectMotionReuse remembers the rejection and drops the cached plan so the
// next attempt cannot short-circuit to the same code.
func (w *avatarWorkflowService) rejectMotionReuse(s *avatar.WorkflowState, need avatar.MaterialNeed, seg avatar.Segment, reason string) error {
	key := motionReuseKey(need, seg)
	w.withState(func() {
		for _, k := range s.RejectedMotionReuse {
			if k == key {
				return
			}
		}
		s.RejectedMotionReuse = append(s.RejectedMotionReuse, key)
		s.AddWarning("F07/"+need.ID, "repair", "复用的上一版动效与本次口播时间不符（"+reason+"），改为按当前配音重新设计")
	})
	err := os.Remove(filepath.Join(w.dir(s.ID), "materials", need.ID+"-motion-plan.json"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
