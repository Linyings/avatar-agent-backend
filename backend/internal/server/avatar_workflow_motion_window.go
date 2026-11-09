package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
)

type narrationWord struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

func (w *avatarWorkflowService) reusableMotionDesign(ctx context.Context, s *avatar.WorkflowState, need avatar.MaterialNeed, seg avatar.Segment) (avatar.MotionPlan, bool) {
	var empty avatar.MotionPlan
	prior, err := w.load(ctx, uuid.MustParse(w.ownerID(s)), s.ReusedFrom)
	if err != nil || prior.Input.Style != s.Input.Style || prior.Input.Ratio != s.Input.Ratio || prior.Input.Direction != s.Input.Direction || prior.Input.Photo != s.Input.Photo {
		return empty, false
	}
	sameText := false
	for _, p := range prior.Segments {
		if p.ID == seg.ID && p.Text == seg.Text {
			sameText = true
		}
	}
	if !sameText {
		return empty, false
	}
	normalize := func(n avatar.MaterialNeed) string { n.Duration = 0; n.NarrationStart = nil; return avatar.Digest(n) }
	sameNeed := false
	for _, n := range prior.Needs {
		if n.ID == need.ID && normalize(n) == normalize(need) {
			sameNeed = true
		}
	}
	if !sameNeed {
		return empty, false
	}
	for _, m := range prior.Materials {
		if m.NeedID == need.ID && m.MotionPlan != nil {
			raw, _ := json.Marshal(m.MotionPlan)
			var plan avatar.MotionPlan
			_ = json.Unmarshal(raw, &plan)
			if avatar.ValidateMotionPlan(plan, seg.Text) == nil && avatar.NormalizeMotionCode(&plan) == nil {
				return plan, true
			}
		}
	}
	return empty, false
}

func (w *avatarWorkflowService) narrationWords(s *avatar.WorkflowState, id string) []narrationWord {
	var data struct {
		Sentences []struct {
			Words []narrationWord `json:"words"`
		} `json:"sentences"`
	}
	raw, err := os.ReadFile(filepath.Join(w.dir(s.ID), id, "timestamps.json"))
	if err != nil || json.Unmarshal(raw, &data) != nil {
		return nil
	}
	var words []narrationWord
	for _, sentence := range data.Sentences {
		words = append(words, sentence.Words...)
	}
	return words
}

// Bind pending materials, including partial recovery. Finished materials retain
// their recorded timing until explicitly replaced.
func (w *avatarWorkflowService) bindMotionWindows(s *avatar.WorkflowState) error {
	ready := map[string]bool{}
	for _, material := range s.Materials {
		ready[material.NeedID] = true
	}
	offsets := map[string]float64{}
	previous := map[string]int{}
	for i := range s.Needs {
		need := &s.Needs[i]
		if need.Kind != "animation" {
			continue
		}
		var seg avatar.Segment
		for _, p := range s.Segments {
			if p.ID == need.SegmentID {
				seg = p
				break
			}
		}
		start := offsets[seg.ID]
		end := start + need.Duration
		if need.NarrationStart != nil {
			start = *need.NarrationStart
			end = start + need.Duration
		}
		if ready[need.ID] || (s.Edits.MaterialOnly != "" && s.Edits.MaterialOnly != need.ID) {
			offsets[seg.ID] = end
			delete(previous, seg.ID)
			continue
		}
		if need.NarrationQuote != "" {
			at := strings.Index(seg.Text, need.NarrationQuote)
			if at < 0 {
				return fmt.Errorf("动效 %s 对应台词必须逐字摘自本段原文", need.ID)
			}
			words := w.narrationWords(s, seg.ID)
			cursor := 0
			first, last := -1.0, -1.0
			for _, word := range words {
				pos := strings.Index(seg.Text[cursor:], word.Text)
				if pos < 0 || word.Text == "" {
					continue
				}
				pos += cursor
				cursor = pos + len(word.Text)
				if cursor > at && pos < at+len(need.NarrationQuote) {
					if first < 0 {
						first = word.Start
					}
					last = word.End
				}
			}
			if first < 0 || last <= first {
				return fmt.Errorf("动效 %s 缺少所引用台词的实测时间，请检查配音对齐", need.ID)
			}
			start, end = first, last
			if strings.TrimSpace(seg.Text[:at]) == "" {
				start = 0
			}
			if strings.Trim(seg.Text[at+len(need.NarrationQuote):], " ，。；！？\n\r\t") == "" {
				end = seg.Duration
			}
		}
		if math.IsNaN(start) || math.IsInf(start, 0) || math.IsNaN(end) || math.IsInf(end, 0) || start < 0 || end > seg.Duration+.045 || end <= start {
			return fmt.Errorf("动效 %s 的台词区间超出本段配音，请修订素材规划", need.ID)
		}
		need.NarrationStart = &start
		need.Duration = end - start
		if index, ok := previous[seg.ID]; ok {
			prior := &s.Needs[index]
			gap := start - (*prior.NarrationStart + prior.Duration)
			if gap >= 0 && gap < 1 {
				prior.Duration = start - *prior.NarrationStart
			}
		}
		previous[seg.ID] = i
		offsets[seg.ID] = end
	}
	return nil
}

func (w *avatarWorkflowService) motionScope(s *avatar.WorkflowState, need avatar.MaterialNeed, seg avatar.Segment) map[string]any {
	start := 0.0
	if need.NarrationStart != nil {
		start = *need.NarrationStart
	}
	var words []narrationWord
	for _, word := range w.narrationWords(s, seg.ID) {
		if word.End > start && word.Start < start+need.Duration {
			words = append(words, word)
		}
	}
	return map[string]any{"start": start, "end": start + need.Duration, "quote": need.NarrationQuote, "words": words}
}
