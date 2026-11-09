package avatar

import (
	"strings"
	"testing"
)

func TestDirectorMotionNormalizesCompletePerSceneCode(t *testing.T) {
	p := MotionPlan{Scenes: []MotionScene{{Quote: "第一句", Code: "ctx.fillRect(0,0,w,h);"}, {Quote: "第二句", Code: "ctx.strokeRect(0,0,w,h);"}}}
	if err := NormalizeMotionCode(&p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Code, "scene.quote ===") || !strings.Contains(p.Code, "ctx.strokeRect") || p.Scenes[0].Code != "" {
		t.Fatal("per-scene code lost or duplicated")
	}
	bad := MotionPlan{Scenes: []MotionScene{{Quote: "第一句", Code: "ctx.fillRect(0,0,w,h);"}, {Quote: "第二句"}}}
	if NormalizeMotionCode(&bad) == nil {
		t.Fatal("accepted incomplete scene code")
	}
}

func TestDirectorCalibrationUsesMeasuredSpeechAndKeepsCreativePlan(t *testing.T) {
	s := WorkflowState{Director: &DirectorPlan{Scenes: []DirectorScene{{ID: "d1", Purpose: "讲清原理", Composition: "visual-pip", Visual: "流程演示", Motion: "展开路径", EstimatedDuration: 6}}}, Segments: []Segment{{ID: "p1", Text: "第一句", DirectorSceneID: "d1", Start: 0, Duration: 2.3}, {ID: "p2", Text: "第二句", DirectorSceneID: "d1", Start: 2.3, Duration: 3.7}}}
	CalibrateDirector(&s)
	if s.Director.Scenes[0].End != 6 || len(s.Director.Scenes[0].SegmentIDs) != 2 || s.Director.Scenes[0].Motion != "展开路径" {
		t.Fatalf("bad calibration: %+v", s.Director)
	}
	s.Segments[0].Duration = 3
	s.Segments[1].Start = 3
	CalibrateDirector(&s)
	if s.Director.Scenes[0].End != 6.7 || s.Director.Timing != "measured" {
		t.Fatal("stale timing")
	}
}

func TestDirectorStagesAndVersionedReview(t *testing.T) {
	s := WorkflowState{Version: WorkflowVersion, Input: WorkflowInput{Revision: 1}, Shots: []WorkflowShot{{ID: "S1", Start: 0, End: 3}}, Artifacts: []WorkflowArtifact{{ID: "preview-S1", Path: "preview/S1.mp4"}}}
	plan := PlanWorkflow(s.Input, &s)
	if len(plan.Stages) != 11 {
		t.Fatalf("want 11 stages, got %d", len(plan.Stages))
	}
	before := ReviewFingerprint(s, "shots")
	s.Shots[0].Keyword = "新文字"
	if before == ReviewFingerprint(s, "shots") {
		t.Fatal("stale preview approval survived edit")
	}
	if ConfirmationNode("shots") != "F11" || ConfirmationNode("final") != "F11.2" {
		t.Fatal("wrong gate")
	}
}
