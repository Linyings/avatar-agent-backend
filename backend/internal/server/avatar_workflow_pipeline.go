package server

// pipelineMaterialProduction runs F06 / F07 / F08 for every segment after F05
// has completed. The original serial loop processed kinds in order:
//
//	for kind in [avatar, animation, real]:
//	    for need in needs: ...
//
// This version groups needs by segment and runs each segment's materials
// concurrently. Within a segment, F06 (avatar) must finish before F07
// (animation) starts because planMotion samples frames from the presenter
// video. F08 (real) has no such dependency and runs in parallel with both.
//
// The state object is shared; produceMaterial and its helpers already use the
// workflow step/save machinery which is effectively single-writer (each need
// writes to its own step ID). The only cross-goroutine mutations are appends to
// s.Materials, which are serialized by a mutex passed through the service.

import (
	"context"
	"errors"
	"sync"

	"github.com/narratoai/backend-go/internal/avatar"
)

// pipelineMaterialProduction replaces the original F06/F07/F08 serial loop.
// It is called after bindMotionWindows and the initial save, exactly where the
// old loop started.
func (w *avatarWorkflowService) pipelineMaterialProduction(
	ctx context.Context,
	s *avatar.WorkflowState,
) error {
	// Group needs by segment, preserving declaration order within each group.
	type bucket struct {
		avatarNeeds    []avatar.MaterialNeed
		animationNeeds []avatar.MaterialNeed
		realNeeds      []avatar.MaterialNeed
	}

	buckets := make(map[string]*bucket, len(s.Segments))
	var segOrder []string
	for _, seg := range s.Segments {
		buckets[seg.ID] = &bucket{}
		segOrder = append(segOrder, seg.ID)
	}
	for _, need := range s.Needs {
		b := buckets[need.SegmentID]
		if b == nil {
			// Need belongs to an unknown segment — keep it safe and skip.
			continue
		}
		switch need.Kind {
		case "avatar":
			b.avatarNeeds = append(b.avatarNeeds, need)
		case "animation":
			b.animationNeeds = append(b.animationNeeds, need)
		case "real":
			b.realNeeds = append(b.realNeeds, need)
		}
	}

	type result struct {
		segID string
		err   error
	}
	ch := make(chan result, len(segOrder))
	var wg sync.WaitGroup

	for _, segID := range segOrder {
		segID := segID
		b := buckets[segID]
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := w.produceSegmentMaterials(ctx, s, b.avatarNeeds, b.animationNeeds, b.realNeeds)
			ch <- result{segID: segID, err: err}
		}()
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	var firstErr error
	for r := range ch {
		if r.err == nil {
			continue
		}
		if errors.Is(r.err, errWorkflowStopped) {
			return r.err // hard stop — drain is not needed
		}
		if firstErr == nil {
			firstErr = r.err
		}
	}
	return firstErr
}

// produceSegmentMaterials runs the material-production steps for one segment.
//
//   - F08 real materials start immediately (no dependency on avatar video).
//   - F06 avatar materials start immediately and run concurrently with F08.
//   - F07 animation starts only after ALL F06 within this segment finish, because
//     planMotion calls motion_context which samples frames from the presenter.
func (w *avatarWorkflowService) produceSegmentMaterials(
	ctx context.Context,
	s *avatar.WorkflowState,
	avatarNeeds, animationNeeds, realNeeds []avatar.MaterialNeed,
) error {
	// errCh is large enough to absorb one error per need without blocking.
	total := len(avatarNeeds) + len(animationNeeds) + len(realNeeds)
	if total == 0 {
		return nil
	}
	errCh := make(chan error, total)

	var wg sync.WaitGroup

	// F08 — real materials, no dependency.
	for _, need := range realNeeds {
		need := need
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.produceMaterial(ctx, s, need, "F08"); err != nil && !errors.Is(err, errWorkflowStopped) {
				errCh <- err
			}
		}()
	}

	// F06 — avatar materials.  Collect into a separate WaitGroup so we can
	// join it before launching F07.
	var f06wg sync.WaitGroup
	for _, need := range avatarNeeds {
		need := need
		f06wg.Add(1)
		go func() {
			defer f06wg.Done()
			if err := w.produceMaterial(ctx, s, need, "F06"); err != nil {
				if !errors.Is(err, errWorkflowStopped) {
					errCh <- err
				}
			}
		}()
	}

	// Wait for F06 before starting F07 — animation planning needs the
	// presenter frames that only exist after the avatar video is written.
	f06wg.Wait()

	// If any F06 failed, skip F07 for this segment (motion_context will be
	// missing and planMotion would fail anyway).
	select {
	case err := <-errCh:
		wg.Wait()
		return err
	default:
	}

	// F07 — animation materials.
	for _, need := range animationNeeds {
		need := need
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.produceMaterial(ctx, s, need, "F07"); err != nil && !errors.Is(err, errWorkflowStopped) {
				errCh <- err
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}
