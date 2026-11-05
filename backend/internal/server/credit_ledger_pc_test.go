package server

import (
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCreditLedgerPCSummaryFiltersTaskAndUser(t *testing.T) {
	app := newTestApp(t)
	token := registerAndLogin(t, app, "pc-summary@example.com")
	other := registerAndLogin(t, app, "pc-summary-other@example.com")
	run := createWorkflowRunTestPC(t, app, token, "task-a", strings.Repeat("a", 64), "client-a")
	unrelated := createWorkflowRunTestPC(t, app, token, "task-b", strings.Repeat("b", 64), "client-a")
	foreign := createWorkflowRunTestPC(t, app, other, "task-a", strings.Repeat("c", 64), "client-b")
	for i, r := range []workflowRunTestResponse{run, unrelated, foreign} {
		if err := app.createCreditLedgerRecord(nil, creditLedgerEntry{UserID: uuid.MustParse(r.UserID), EventType: creditLedgerEventConsume, Item: creditLedgerConsumeLLMLabel, ChangeAmount: -int64((i + 1) * 10), RecordedAt: time.Now(), RequestMetadata: map[string]any{"workflow_run_id": r.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	resp := request(app, http.MethodGet, "/api/v1/credit-accounts/ledger/records?client_task_id=task-a&event_type=consume&per_page=1", nil, token)
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d: %s", resp.Code, resp.Body.String())
	}
	var got struct {
		Total   int
		Summary struct {
			Total int64  `json:"total_consumed_credits"`
			Task  string `json:"client_task_id"`
		}
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 1 || got.Summary.Total != 10 || got.Summary.Task != "task-a" {
		t.Fatalf("unexpected summary: %+v", got)
	}
}
