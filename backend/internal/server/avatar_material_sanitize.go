package server

import (
	"math"
	"path"
	"strings"

	"github.com/narratoai/backend-go/internal/avatar"
)

// sanitizeMaterials repairs records corrupted by earlier concurrent media
// stages: one record per need, every record pointing at its own need's file,
// and animation timing taken from the planned need. A dropped record whose
// need has no survivor reopens that need's step, so the clip is produced again
// from the cached plan and code instead of blocking assembly.
func (w *avatarWorkflowService) sanitizeMaterials(s *avatar.WorkflowState) {
	w.withState(func() {
		needs := map[string]avatar.MaterialNeed{}
		for _, n := range s.Needs {
			needs[n.ID] = n
		}
		kept := make([]avatar.Material, 0, len(s.Materials))
		index := map[string]int{}
		dropped := map[string]bool{}
		for _, m := range s.Materials {
			need, planned := needs[m.NeedID]
			if planned && pointsAtOtherNeed(m, needs) {
				dropped[m.NeedID] = true
				continue
			}
			if planned && need.Kind == "animation" {
				// Timing is owned by the plan; a stray copy may carry another
				// need's start.
				if i, seen := index[m.NeedID]; seen {
					if !narrationMatches(kept[i], need) && narrationMatches(m, need) {
						kept[i] = m
					}
					continue
				}
			} else if i, seen := index[m.NeedID]; seen {
				kept[i] = m
				continue
			}
			index[m.NeedID] = len(kept)
			kept = append(kept, m)
		}
		for i := range kept {
			if need, ok := needs[kept[i].NeedID]; ok && need.Kind == "animation" && need.NarrationStart != nil {
				start := *need.NarrationStart
				kept[i].NarrationStart = &start
			}
		}
		s.Materials = kept
		for id := range dropped {
			if _, survived := index[id]; survived {
				continue
			}
			need := needs[id]
			for i := range s.Steps {
				if s.Steps[i].ID == materialNodeID(need) && s.Steps[i].Status == "succeeded" {
					s.Steps[i].Status = "pending"
					s.Steps[i].ProductionPhase = "素材记录错位，重新生成"
					s.AddWarning(s.Steps[i].ID, "repair", "素材记录指向了其他节点的文件，已丢弃并重新生成本节点")
				}
			}
		}
	})
}

// pointsAtOtherNeed reports whether a record's file is named after a different
// planned need. Materials are written as materials/<need>[-alpha|-composite].*;
// files with any other naming (older conventions) are left alone.
func pointsAtOtherNeed(m avatar.Material, needs map[string]avatar.MaterialNeed) bool {
	base := path.Base(m.Path)
	if materialFileNamedAfter(base, m.NeedID) {
		return false
	}
	for id := range needs {
		if id != m.NeedID && materialFileNamedAfter(base, id) {
			return true
		}
	}
	return false
}

func materialFileNamedAfter(base, id string) bool {
	return base == id+path.Ext(base) || strings.HasPrefix(base, id+"-")
}

func narrationMatches(m avatar.Material, need avatar.MaterialNeed) bool {
	if need.NarrationStart == nil || m.NarrationStart == nil {
		return need.NarrationStart == nil && m.NarrationStart == nil
	}
	return math.Abs(*m.NarrationStart-*need.NarrationStart) < 1e-6
}
