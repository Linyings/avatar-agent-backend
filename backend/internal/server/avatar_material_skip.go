package server

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/narratoai/backend-go/internal/avatar"
)

// Skipping is a reviewed material-plan edit. It never marks a failed render
// successful or leaves its old shot references in the new production version.
func (w *avatarWorkflowService) skipMaterialNeeds(s avatar.WorkflowState, node string, value any) ([]avatar.MaterialNeed, error) {
	var ids []string
	raw, err := json.Marshal(value)
	if err != nil || json.Unmarshal(raw, &ids) != nil || len(ids) == 0 {
		return nil, fmt.Errorf("请提供要跳过的素材 ID")
	}
	remove, affected := map[string]bool{}, map[string]bool{}
	for _, id := range ids {
		n, err := materialNeed(s, id)
		if err != nil || remove[id] {
			return nil, fmt.Errorf("跳过素材不存在或重复：%s", id)
		}
		if node != "F05" && materialNodeID(n) != node+"/"+id {
			return nil, fmt.Errorf("%s 不能跳过其他类型的素材", node)
		}
		remove[id], affected[n.SegmentID] = true, true
	}
	remaining := []avatar.MaterialNeed{}
	for _, n := range s.Needs {
		if !remove[n.ID] {
			remaining = append(remaining, n)
		}
	}
	if err := avatar.ValidateNeeds(s.Input, s.Segments, remaining); err != nil {
		return nil, fmt.Errorf("不能直接跳过：%w；请先调整素材或制作要求", err)
	}
	for _, seg := range s.Segments {
		if !affected[seg.ID] {
			continue
		}
		covered := false
		for _, n := range remaining {
			if n.SegmentID != seg.ID || n.Duration+.02 < seg.Duration || (n.NarrationStart != nil && *n.NarrationStart > .02) {
				continue
			}
			for _, m := range s.Materials {
				if m.NeedID != n.ID || m.Duration+.02 < seg.Duration {
					continue
				}
				path, err := safeWorkflowFile(w.dir(s.ID), m.Path)
				if err != nil {
					continue
				}
				if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
					covered = true
				}
			}
		}
		if !covered {
			return nil, fmt.Errorf("段落 %s 尚无覆盖完整配音的可用替代画面，请先补充素材或调整方案", seg.ID)
		}
	}
	return remaining, nil
}
