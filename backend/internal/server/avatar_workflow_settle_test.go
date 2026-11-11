package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
	"gorm.io/gorm"
)

type settleSpy struct {
	status string
	query  int
	submit int
}

func (p *settleSpy) Upload(context.Context, string) (string, error) { return "", nil }
func (p *settleSpy) Submit(context.Context, avatar.GenerationRequest) (avatar.ProviderResult, error) {
	p.submit++
	return avatar.ProviderResult{}, fmt.Errorf("must not resubmit")
}
func (p *settleSpy) Query(_ context.Context, id string) (avatar.ProviderResult, error) {
	p.query++
	if p.status == "" {
		return avatar.ProviderResult{}, fmt.Errorf("network unavailable")
	}
	var r avatar.ProviderResult
	json.Unmarshal([]byte(`{"taskId":"`+id+`","status":"`+p.status+`","usage":{"thirdPartyConsumeMoney":"14.49"}}`), &r)
	return r, nil
}
func (p *settleSpy) Download(context.Context, string, string) error { return nil }

// A stop during polling left the order "submitted" in a stopped run. Nothing
// would ever query it again, so every new run on the project was refused.
func stoppedRunWithOpenOrder(t *testing.T) (*App, uuid.UUID, avatar.WorkflowState) {
	a, owner, _, s := workflowFixture(t)
	s.Status, s.StopRequested = "stopped", true
	s.Steps = []avatar.WorkflowStep{{ID: "F04/p1-c1", Status: "failed", Error: "用户已停止后续制作", Submission: "submitted", ProviderTaskID: "task-1", Model: avatar.NativeSpeechModel, CostStatus: "unknown", Attempt: 1}}
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	return a, owner, s
}

func TestSettleStoppedRunOrdersQueriesProviderWithoutResubmitting(t *testing.T) {
	a, owner, s := stoppedRunWithOpenOrder(t)
	spy := &settleSpy{status: "SUCCESS"}
	a.avatarMVP.provider = spy

	if err := a.avatarWorkflow.settleStoppedOrders(context.Background(), owner, uuid.MustParse(s.ProjectID)); err != nil {
		t.Fatal(err)
	}
	if spy.query != 1 || spy.submit != 0 {
		t.Fatalf("query=%d submit=%d", spy.query, spy.submit)
	}
	got, err := a.avatarWorkflow.load(context.Background(), owner, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	step := got.Steps[0]
	if step.Submission != "completed" || step.CostStatus != "reported" || step.ProviderTaskID != "task-1" {
		t.Fatalf("order not settled: %#v", step)
	}
	if err := a.db.Transaction(func(tx *gorm.DB) error {
		return a.avatarWorkflow.ensureProjectOrdersSettled(tx, owner, uuid.MustParse(s.ProjectID))
	}); err != nil {
		t.Fatalf("settled order still blocks new runs: %v", err)
	}
}

func TestSettleStoppedRunOrdersMarksProviderFailure(t *testing.T) {
	a, owner, s := stoppedRunWithOpenOrder(t)
	a.avatarMVP.provider = &settleSpy{status: "FAILED"}
	if err := a.avatarWorkflow.settleStoppedOrders(context.Background(), owner, uuid.MustParse(s.ProjectID)); err != nil {
		t.Fatal(err)
	}
	got, _ := a.avatarWorkflow.load(context.Background(), owner, s.ID)
	if got.Steps[0].Submission != "terminal_failed" {
		t.Fatalf("failed order not settled: %#v", got.Steps[0])
	}
}

// Still rendering or unreachable: keep the order open, never guess.
func TestSettleStoppedRunOrdersKeepsUnfinishedOrderOpen(t *testing.T) {
	for _, status := range []string{"RUNNING", ""} {
		a, owner, s := stoppedRunWithOpenOrder(t)
		a.avatarMVP.provider = &settleSpy{status: status}
		_ = a.avatarWorkflow.settleStoppedOrders(context.Background(), owner, uuid.MustParse(s.ProjectID))
		got, _ := a.avatarWorkflow.load(context.Background(), owner, s.ID)
		if got.Steps[0].Submission != "submitted" {
			t.Fatalf("status %q: unfinished order closed: %#v", status, got.Steps[0])
		}
	}
}

// Orders without a task id may or may not exist at the provider; they are not
// guessed closed by this path.
func TestSettleStoppedRunOrdersIgnoresOrdersWithoutTaskID(t *testing.T) {
	a, owner, s := stoppedRunWithOpenOrder(t)
	s.Steps[0].ProviderTaskID, s.Steps[0].Submission = "", "unknown"
	a.avatarWorkflow.save(&s)
	spy := &settleSpy{status: "SUCCESS"}
	a.avatarMVP.provider = spy
	_ = a.avatarWorkflow.settleStoppedOrders(context.Background(), owner, uuid.MustParse(s.ProjectID))
	if spy.query != 0 {
		t.Fatal("queried an order without task id")
	}
}
