package avatar

import (
	"slices"
	"testing"
)

func TestWorkflowPlanDependsOnInputAndConfirmedMaterialNeeds(t *testing.T) {
	in := WorkflowInput{UseAvatar: true, Assets: []SourceAsset{{ID: "portrait", Kind: "photo"}}}
	p := PlanWorkflow(in, nil)
	for _, node := range []string{"F07", "F08"} {
		if slices.Contains(p.Nodes, node) {
			t.Fatalf("unneeded %s in %v", node, p.Nodes)
		}
	}
	if !slices.Contains(p.Nodes, "F06") {
		t.Fatal("avatar branch missing")
	}
	in.Assets = append(in.Assets, SourceAsset{ID: "v1", Kind: "video"})
	if !slices.Contains(PlanWorkflow(in, nil).Nodes, "F01.4") {
		t.Fatal("video analysis missing")
	}
	in.Assets[1].Excluded = true
	if !slices.Contains(PlanWorkflow(in, nil).Nodes, "F01.4") {
		t.Fatal("modern reference analysis must include photos and text")
	}
	if slices.Contains(PlanWorkflow(in, &WorkflowState{Input: in, Version: "avatar-workflow/2.2"}).Nodes, "F01.4") {
		t.Fatal("legacy plan retained excluded video")
	}
	in.UseAvatar = false
	if nodes := PlanWorkflow(in, nil).Nodes; slices.Contains(nodes, "F06") || !slices.Contains(nodes, "F07") {
		t.Fatalf("animation route=%v", nodes)
	}
	run := &WorkflowState{Input: in, Steps: []WorkflowStep{{ID: "F05", Status: "succeeded"}}, Needs: []MaterialNeed{{ID: "n1", Kind: "real"}}}
	nodes := PlanWorkflow(in, run).Nodes
	if slices.Contains(nodes, "F07") || !slices.Contains(nodes, "F08") {
		t.Fatalf("confirmed needs ignored: %v", nodes)
	}
}

func TestWorkflowPlanUsesRunInputsUntilNewVersionStarts(t *testing.T) {
	run := &WorkflowState{Input: WorkflowInput{UseAvatar: true, Assets: []SourceAsset{{Kind: "video"}}}}
	nodes := PlanWorkflow(WorkflowInput{}, run).Nodes
	if !slices.Contains(nodes, "F01.4") || !slices.Contains(nodes, "F06") {
		t.Fatalf("draft overwrote running plan: %v", nodes)
	}
}

func TestWorkflowAnimationDisabledDoesNotInventExtraMaterials(t *testing.T) {
	no := false
	in := WorkflowInput{UseAvatar: true, UseAnimation: &no}
	segments := []Segment{{ID: "p1", Text: "原稿。", Duration: 2, Audio: "voice/p1.wav"}}
	needs := []MaterialNeed{{ID: "host", SegmentID: "p1", Kind: "avatar", Duration: 2, Angle: "正面"}}
	if err := ValidateNeeds(in, segments, needs); err != nil {
		t.Fatal(err)
	}
	needs = append(needs, MaterialNeed{ID: "extra", SegmentID: "p1", Kind: "animation", Duration: 2, Template: "summary"})
	if err := ValidateNeeds(in, segments, needs); err == nil {
		t.Fatal("animation added despite an explicit pure-avatar choice")
	}
}

func TestWorkflowFusionRequiresBothMaterialBranches(t *testing.T) {
	yes := true
	in := WorkflowInput{UseAvatar: true, UseAnimation: &yes}
	segments := []Segment{{ID: "p1", Duration: 2, Audio: "voice/p1.wav"}}
	host := MaterialNeed{ID: "host", SegmentID: "p1", Kind: "avatar", Duration: 2}
	motion := MaterialNeed{ID: "motion", SegmentID: "p1", Kind: "animation", Duration: 2, Template: "summary"}
	for _, needs := range [][]MaterialNeed{{host}, {motion}} {
		if err := ValidateNeeds(in, segments, needs); err == nil {
			t.Fatalf("fusion project silently dropped a required branch: %v", needs)
		}
	}
	if err := ValidateNeeds(in, segments, []MaterialNeed{host, motion}); err != nil {
		t.Fatal(err)
	}
	nodes := PlanWorkflow(in, nil).Nodes
	if !slices.Contains(nodes, "F06") || !slices.Contains(nodes, "F07") {
		t.Fatalf("missing fusion branches: %v", nodes)
	}
	// Historical inputs without an explicit animation choice retain their policy.
	in.UseAnimation = nil
	if err := ValidateNeeds(in, segments, []MaterialNeed{host}); err != nil {
		t.Fatal(err)
	}
}
