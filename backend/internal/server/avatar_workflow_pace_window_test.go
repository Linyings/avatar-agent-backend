package server

import (
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
	"os"
	"path/filepath"
	"testing"
)

func TestVoiceSpeedRevisionInvalidatesOnlyChangedAudio(t *testing.T) {
	a, _, _, prior := workflowFixture(t)
	prior.ScriptConfirmed = true
	prior.AudioConfirmed = true
	prior.Script = "第一句。第二句。"
	prior.Segments = []avatar.Segment{{ID: "p1", Text: "第一句。", Audio: "p1/audio.wav", Duration: 2, Fingerprint: "one"}, {ID: "p2", Text: "第二句。", Audio: "p2/audio.wav", Duration: 2, Fingerprint: "two"}}
	for _, seg := range prior.Segments {
		if err := os.MkdirAll(filepath.Join(a.avatarWorkflow.dir(prior.ID), seg.ID), 0755); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{"audio.wav", "timestamps.json", "alignment-cache.json"} {
			if err := avatar.WriteJSON(filepath.Join(a.avatarWorkflow.dir(prior.ID), seg.ID, file), map[string]any{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	next := avatar.WorkflowState{ID: uuid.NewString(), Input: prior.Input, ConfigSnapshot: prior.ConfigSnapshot}
	err := a.avatarWorkflow.inherit(&next, prior, avatar.WorkflowEdits{SegmentVoiceSpeeds: map[string]float64{"p2": 1.1}})
	if err != nil {
		t.Fatal(err)
	}
	if next.Segments[0].Audio == "" || next.Segments[1].Audio != "" || next.AudioConfirmed {
		t.Fatalf("incorrect dependency reuse: %#v", next.Segments)
	}
	if prior.Input.SegmentVoiceSpeeds != nil {
		t.Fatal("mutated historical input")
	}
}

func TestMotionWindowUsesMeasuredQuoteInsteadOfGuessedDuration(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	s.Segments = []avatar.Segment{{ID: "p1", Text: "首笔110亿美元。投后1.25万亿美元。", Duration: 19.5}}
	s.Needs = []avatar.MaterialNeed{{ID: "a", Kind: "animation", SegmentID: "p1", Duration: 11, NarrationQuote: "首笔110亿美元。"}, {ID: "b", Kind: "animation", SegmentID: "p1", Duration: 8.5, NarrationQuote: "投后1.25万亿美元。"}}
	data := map[string]any{"sentences": []any{map[string]any{"words": []narrationWord{{Text: "首笔110亿美元", Start: 0, End: 13}, {Text: "投后1.25万亿美元", Start: 13.5, End: 19.2}}}}}
	if err := os.MkdirAll(filepath.Join(a.avatarWorkflow.dir(s.ID), "p1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := avatar.WriteJSON(filepath.Join(a.avatarWorkflow.dir(s.ID), "p1/timestamps.json"), data); err != nil {
		t.Fatal(err)
	}
	if err := a.avatarWorkflow.bindMotionWindows(&s); err != nil {
		t.Fatal(err)
	}
	if *s.Needs[1].NarrationStart != 13.5 || s.Needs[1].Duration != 6 {
		t.Fatalf("wrong window: %#v", s.Needs[1])
	}
	if s.Needs[0].Duration != 13.5 {
		t.Fatalf("tiny gap would create a flash cut: %v", s.Needs[0].Duration)
	}
}

func TestMotionWindowBindsPendingMaterialWithoutChangingCompletedMaterial(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	s.Segments = []avatar.Segment{{ID: "p1", Duration: 19.5}}
	s.Needs = []avatar.MaterialNeed{{ID: "a", Kind: "animation", SegmentID: "p1", Duration: 11}, {ID: "b", Kind: "animation", SegmentID: "p1", Duration: 8.5}}
	s.Materials = []avatar.Material{{NeedID: "a", Kind: "animation", SegmentID: "p1", Duration: 11}}
	if err := a.avatarWorkflow.bindMotionWindows(&s); err != nil {
		t.Fatal(err)
	}
	if s.Needs[0].NarrationStart != nil || s.Needs[1].NarrationStart == nil || *s.Needs[1].NarrationStart != 11 {
		t.Fatalf("wrong partial recovery windows: %#v", s.Needs)
	}
}
