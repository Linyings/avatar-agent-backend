package avatar

import (
	"encoding/json"
	"testing"
)

func TestWorkflowBillingKeepsInFlightAndHistoricalOrdersExactlyOnce(t *testing.T) {
	s := WorkflowState{Steps: []WorkflowStep{{ID: "F04/p1", Attempt: 2, Status: "running", ProviderTaskID: "old-order", CostStatus: "reported", Usage: json.RawMessage(`{"thirdPartyConsumeMoney":"0.001"}`),
		Orders:   []WorkflowOrder{{Attempt: 1, TaskID: "old-order", Usage: json.RawMessage(`{"thirdPartyConsumeMoney":"0.001"}`)}},
		Attempts: []WorkflowAttempt{{Number: 1, TaskID: "old-order", CostStatus: "unknown"}},
	}}}
	s.RefreshBilling()
	if len(s.Billing) != 1 || len(s.Billing[0].Usage) == 0 {
		t.Fatalf("recovery queries must neither hide nor double count an order: %#v", s.Billing)
	}
	s.Steps[0].ProviderTaskID = "new-order"
	s.Steps[0].Usage = nil
	s.Steps[0].CostStatus = "unknown"
	s.RefreshBilling()
	if len(s.Billing) != 2 {
		t.Fatalf("both real orders must remain: %#v", s.Billing)
	}
}

func TestWorkflowBillingDoesNotChargeLocalOrReusedWork(t *testing.T) {
	s := WorkflowState{Steps: []WorkflowStep{{ID: "F12", Attempt: 1, CostStatus: "local_compute"}, {ID: "F04/p1", Attempt: 1, CostStatus: "reused"}}}
	s.RefreshBilling()
	if len(s.Billing) != 0 {
		t.Fatal("local/cache rows are not new supplier charges")
	}
}
