package agent

import (
	"context"
	"testing"
)

type nodePackageHost struct {
	id     string
	args   map[string]any
	calls  int
	result any
}

func (h *nodePackageHost) Invoke(_ context.Context, _, _, id string, args map[string]any, _ string) (any, error) {
	h.id = id
	h.args = args
	h.calls++
	if h.result != nil {
		return h.result, nil
	}
	return map[string]any{"status": "pending"}, nil
}

func TestPackagePlanCompletedNoopCanBeReplanned(t *testing.T) {
	h := &nodePackageHost{result: map[string]any{"submitted": false, "status": "succeeded"}}
	r := NewToolRegistry()
	RegisterPackageTools(r, h, "owner", "project", "turn")
	_, blocked, _ := r.Invoke(context.Background(), ToolCall{Name: "plan_project", Arguments: map[string]any{"target": "F04", "reason": "继续"}}, "turn", func(any) {})
	if blocked {
		t.Fatal("initial plan blocked")
	}
	h.result = nil
	_, blocked, issue := r.Invoke(context.Background(), ToolCall{Name: "plan_project", Arguments: map[string]any{"target": "F12", "rerunFrom": "F04", "reason": "从配音开始重做"}}, "turn", func(any) {})
	if blocked || issue != nil || h.id != "avatar.rerun" {
		t.Fatalf("non-submission consumed the only plan: %#v", issue)
	}
}
func (*nodePackageHost) Confirm(context.Context, string, string, string, int) (any, error) {
	return nil, nil
}

func TestPackagePlanNodeAliasesAndRerun(t *testing.T) {
	for _, target := range []string{"F04", "f4", "avatar_voice", "avatar.voice", "分段配音"} {
		t.Run(target, func(t *testing.T) {
			h := &nodePackageHost{}
			r := NewToolRegistry()
			RegisterPackageTools(r, h, "owner", "project", "turn")
			out, blocked, issue := r.Invoke(context.Background(), ToolCall{Name: "plan_project", Arguments: map[string]any{"target": "F12", "rerunFrom": target, "reason": "从配音起全部重做"}}, "turn", func(any) {})
			if blocked || issue != nil || h.id != "avatar.rerun" || h.args["from"] != "F04" || h.args["target"] != "F12" {
				t.Fatalf("rerun lost: %s blocked=%v issue=%v host=%#v", out, blocked, issue, h)
			}
		})
	}
}

func TestPackageInvalidPlanCanBeRepairedInSameTurn(t *testing.T) {
	h := &nodePackageHost{}
	r := NewToolRegistry()
	RegisterPackageTools(r, h, "owner", "project", "turn")
	for _, bad := range []string{"missing", "F01", "F04/p1"} {
		_, blocked, issue := r.Invoke(context.Background(), ToolCall{Name: "plan_project", Arguments: map[string]any{"target": bad, "reason": "test"}}, "turn", func(any) {})
		if !blocked || issue == nil || h.calls != 0 {
			t.Fatalf("invalid target consumed execution: %s", bad)
		}
	}
	_, blocked, issue := r.Invoke(context.Background(), ToolCall{Name: "plan_project", Arguments: map[string]any{"target": "avatar_voice", "reason": "test"}}, "turn", func(any) {})
	if blocked || issue != nil || h.id != "avatar.voice" {
		t.Fatalf("corrected call blocked: %v %#v", issue, h)
	}
	_, blocked, _ = r.Invoke(context.Background(), ToolCall{Name: "plan_project", Arguments: map[string]any{"target": "F04", "reason": "test"}}, "turn", func(any) {})
	if !blocked || h.calls != 1 {
		t.Fatal("successful admission must still be once-only")
	}
}
