package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
)

type contentResponseDiagnosticFixture struct{}

func (contentResponseDiagnosticFixture) Error() string           { return "private-upstream-details" }
func (contentResponseDiagnosticFixture) ProviderHTTPStatus() int { return 200 }
func (contentResponseDiagnosticFixture) ProviderDiagnosticFields() map[string]string {
	return map[string]string{"error_code": "response_no_choices"}
}

func TestAvatarWorkflowMalformedSuccessRemainsUnknownWithoutAutomaticResubmit(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	calls := 0
	a.avatarWorkflow.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		calls++
		return nil, contentResponseDiagnosticFixture{}
	}
	step := avatar.WorkflowStep{ID: "F03"}
	var out map[string]any
	err := a.avatarWorkflow.model(context.Background(), &s, &step, "prompt", nil, nil, &out)
	if err == nil || step.Submission != "unknown" || !strings.Contains(err.Error(), "response_no_choices") || strings.Contains(err.Error(), "private") {
		t.Fatalf("invalid failure/recovery state: %s %v", step.Submission, err)
	}
	_ = a.avatarWorkflow.model(context.Background(), &s, &step, "prompt", nil, nil, &out)
	if calls != 1 {
		t.Fatal("uncertain request resubmitted")
	}
}

func TestAvatarWorkflowCachedMaterialRetainsOnlyMatchingQualityWarnings(t *testing.T) {
	m := avatar.Material{NeedID: "host-p1", Kind: "avatar", Fingerprint: "same"}
	prior := avatar.WorkflowState{Materials: []avatar.Material{m}, Events: []avatar.WorkflowEvent{
		{Node: "F06/host-p1-c1", Type: "quality", Message: "检查未完成；继续制作"},
		{Node: "F06/host-p10-c1", Type: "quality", Message: "另一段提醒"},
		{Node: "F12", Type: "review", Message: "旧成片审片"},
	}}
	next := avatar.WorkflowState{}
	replayWorkflowMaterialWarnings(&next, prior, m)
	replayWorkflowMaterialWarnings(&next, prior, m)
	if len(next.Events) != 1 || next.Events[0].Message != prior.Events[0].Message {
		t.Fatalf("lost quality history or duplicated unrelated events: %#v", next.Events)
	}
	m.Fingerprint = "changed"
	changed := avatar.WorkflowState{}
	replayWorkflowMaterialWarnings(&changed, prior, m)
	if len(changed.Events) != 0 {
		t.Fatal("old quality result attached to changed footage")
	}
}
