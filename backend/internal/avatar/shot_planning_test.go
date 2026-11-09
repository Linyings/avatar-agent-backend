package avatar

import (
	"encoding/json"
	"strings"
	"testing"
)

func shotPlanningFixture() WorkflowState {
	return WorkflowState{Input: WorkflowInput{Direction: "保留人物圆形小窗", Ratio: "9:16"}, Duration: 45, Segments: []Segment{{ID: "p1", Text: "第一段。", Duration: 5}, {ID: "p2", Text: "第二段。", Start: 5, Duration: 40}}, Materials: []Material{
		{NeedID: "a1", SegmentID: "p1", Kind: "avatar", Duration: 5, Path: "PRIVATE_PATH", Fingerprint: "PRIVATE_HASH"},
		{NeedID: "a2", SegmentID: "p2", Kind: "avatar", Duration: 40},
		{NeedID: "m2", SegmentID: "p2", Kind: "animation", Duration: 40, MotionPlan: &MotionPlan{Code: strings.Repeat("UNNEEDED_CODE", 10000), Scenes: []MotionScene{{Code: "NESTED_CODE", Quote: "第二段", Title: "第二段"}}}},
	}, Director: &DirectorPlan{Summary: "全片风格", Scenes: []DirectorScene{{ID: "d1", SegmentIDs: []string{"p1"}, Visual: "第一段专属"}, {ID: "d2", SegmentIDs: []string{"p2"}, Visual: "第二段专属"}}}}
}

func TestShotPlanningBoundsAndProjectsOnlyRelevantMedia(t *testing.T) {
	s := shotPlanningFixture()
	batches, err := BuildShotPlanningBatches(&s)
	if err != nil || len(batches) != 3 {
		t.Fatalf("batches=%d err=%v", len(batches), err)
	}
	for _, b := range batches {
		raw, _ := json.Marshal(b)
		for _, secret := range []string{"PRIVATE_PATH", "PRIVATE_HASH", "UNNEEDED_CODE", "NESTED_CODE"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("unneeded generation data leaked: %s", secret)
			}
		}
		if b.Window.End-b.Window.Start > 20 || len(raw) > 24000 {
			t.Fatal("unbounded batch")
		}
		if b.Segment.ID == "p1" && strings.Contains(string(raw), "第二段专属") {
			t.Fatal("unrelated director scene leaked")
		}
	}
	if batches[1].Window.Start != 0 || batches[2].Window.Start != 20 || batches[2].Window.End != 40 {
		t.Fatal("long paragraph lost coverage")
	}
}

func TestShotPlanningMergesLocalTimesAndRejectsInvalidSourceRanges(t *testing.T) {
	s := shotPlanningFixture()
	batches, _ := BuildShotPlanningBatches(&s)
	var all []WorkflowShot
	for _, b := range batches {
		media := "a1"
		if b.Segment.ID == "p2" {
			media = "a2"
		}
		shots, err := NormalizeShotPlanningBatch(b, []WorkflowShot{{ID: "model-id", SegmentID: "wrong", Start: b.Window.Start, End: b.Window.End, Primary: media, Card: "none", Layout: "full", Recipe: "host-hero"}})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, shots...)
	}
	if err := ValidateWorkflowShots(all, s.Segments, s.Materials, s.Duration); err != nil {
		t.Fatal(err)
	}
	if all[2].Start != 25 || all[2].PrimaryIn != 20 || all[0].ID == all[1].ID {
		t.Fatal("wrong local merge")
	}
	bad := []WorkflowShot{{Start: 20, End: 40, Primary: "m2", PrimaryIn: 30, Card: "none", Layout: "full", Recipe: "visual-hero"}}
	if _, err := NormalizeShotPlanningBatch(batches[2], bad); err == nil {
		t.Fatal("out-of-range footage accepted")
	}
	bad[0].PrimaryIn = 0
	bad[0].Start = 21
	if _, err := NormalizeShotPlanningBatch(batches[2], bad); err == nil {
		t.Fatal("timeline gap accepted")
	}
}

func TestShotPlanningRejectsOversizedResponseAndKeepsInputIntact(t *testing.T) {
	s := shotPlanningFixture()
	batches, _ := BuildShotPlanningBatches(&s)
	shots := make([]WorkflowShot, 5)
	if _, err := NormalizeShotPlanningBatch(batches[0], shots); err == nil {
		t.Fatal("unbounded output accepted")
	}
	s.Input.Direction = strings.Repeat("important requirement", 5000)
	if _, err := BuildShotPlanningBatches(&s); err == nil {
		t.Fatal("oversized requirements silently accepted or dropped")
	}
	if len(s.Input.Direction) < 90000 {
		t.Fatal("user requirements changed")
	}
}

func TestShotPlanningBoundsLongIDsAndTotalShotCount(t *testing.T) {
	s := shotPlanningFixture()
	s.Segments = s.Segments[:1]
	s.Materials = s.Materials[:1]
	s.Segments[0].ID = "p" + strings.Repeat("a", 63)
	s.Materials[0].SegmentID = s.Segments[0].ID
	batches, err := BuildShotPlanningBatches(&s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeShotPlanningBatch(batches[0], []WorkflowShot{{Start: 0, End: 5, Primary: "a1", Card: "none", Layout: "full"}}); err != nil {
		t.Fatal(err)
	}
	s.Segments[0].Duration = 900
	s.Materials[0].Duration = 900
	batches, err = BuildShotPlanningBatches(&s)
	if err != nil {
		t.Fatal(err)
	}
	budget := 0
	for _, b := range batches {
		budget += b.MaxShots
	}
	if budget > 160 {
		t.Fatal("batch budgets exceed whole-film shot limit", budget)
	}
}
