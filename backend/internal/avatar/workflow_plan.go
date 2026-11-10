package avatar

import "strings"

// WorkflowPlan is a projection of the real inputs and material plan. A package
// being installed does not mean it belongs to every project's execution graph.
type WorkflowPlan struct {
	Stages              []WorkflowStage `json:"stages,omitempty"`
	Nodes               []string        `json:"nodes"`
	MaterialPlanPending bool            `json:"material_plan_pending"`
}

func AnimationEnabled(in WorkflowInput) bool {
	if in.UseAnimation != nil {
		return *in.UseAnimation
	}
	return !in.UseAvatar
}

func PlanWorkflow(in WorkflowInput, run *WorkflowState) WorkflowPlan {
	if run != nil {
		in = run.Input
	}
	video := false
	for _, asset := range in.Assets {
		if asset.Kind == "video" && !asset.Excluded {
			video = true
		}
	}
	branches := map[string]bool{"F06": in.UseAvatar, "F07": AnimationEnabled(in), "F08": video}
	planned := run != nil && len(run.Needs) > 0
	if run != nil {
		for _, step := range run.Steps {
			if step.ID == "F05" && step.Status == "succeeded" {
				planned = true
			}
		}
		if planned {
			branches = map[string]bool{}
			for _, need := range run.Needs {
				branches[map[string]string{"avatar": "F06", "animation": "F07", "real": "F08"}[need.Kind]] = true
			}
		}
		// Keep actual work visible even when an old run has an incomplete plan.
		for _, step := range run.Steps {
			if step.Status != "skipped" {
				node := strings.SplitN(step.ID, "/", 2)[0]
				if node == "F06" || node == "F07" || node == "F08" {
					branches[node] = true
				}
			}
		}
	}
	out := WorkflowPlan{Nodes: []string{"F01"}, MaterialPlanPending: !planned}
	modern := run == nil || DirectorWorkflow(run)
	if modern {
		out.Stages = DirectorStages()
		if NativeSpeech(in) {
			for i := range out.Stages {
				if out.Stages[i].Node == "F04" {
					out.Stages[i].Title = "Seedance 原声口播与时间校准"
				}
			}
		}
	}
	for _, node := range WorkflowNodes {
		if !modern && (node == "F01.2" || node == "F01.6" || node == "F11.1" || node == "F11.2") {
			continue
		}
		if node == "F01.4" && !modern && !video {
			continue
		}
		if (node == "F06" || node == "F07" || node == "F08") && !branches[node] {
			continue
		}
		out.Nodes = append(out.Nodes, node)
	}
	return out
}
