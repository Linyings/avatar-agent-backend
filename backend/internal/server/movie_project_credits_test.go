package server

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMovieProjectCreditAttribution(t *testing.T) {
	a := newTestApp(t)
	owner, other, movie, second := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/", nil)
	c.Request.Header.Set("X-Movie-Project-Id", movie.String())
	meta := movieBillingMetadata(c, map[string]any{"request_id": "current"})
	if meta["movie_project_id"] != movie.String() {
		t.Fatal(meta)
	}
	for _, row := range []struct {
		owner   uuid.UUID
		credits int64
		meta    map[string]any
	}{
		{owner, 12, meta}, {owner, 7, meta},
		{owner, 500, map[string]any{"movie_project_id": second.String(), "request_id": "different-project"}},
		{other, 900, meta}, {owner, 4, map[string]any{"request_id": "legacy"}},
		{owner, 300, map[string]any{"request_id": "unrelated"}},
		{owner, 100, map[string]any{"request_id": ""}},
	} {
		if err := a.db.Create(&models.TokenUsageRecord{UserID: row.owner, CreditCount: row.credits, RequestMetadata: jsonMap(row.meta)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, ids := range [][]string{{"legacy"}, {"legacy", "legacy", "different-project", ""}} {
		var total int64
		if err := movieProjectUsageQuery(a.db, owner, movie, ids).Select("SUM(credit_count)").Scan(&total).Error; err != nil {
			t.Fatal(err)
		}
		if total != 23 {
			t.Fatalf("got %d", total)
		}
	}
	c.Request.Header.Set("X-Movie-Project-Id", "not-a-uuid")
	if _, exists := movieBillingMetadata(c, map[string]any{})["movie_project_id"]; exists {
		t.Fatal("invalid tag accepted")
	}
}

func TestMovieProjectCreditsCountUntaggedWorkflowCallsOfTheProjectRun(t *testing.T) {
	a := newTestApp(t)
	owner, other, movie, second := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	run := func(user uuid.UUID, name, requestID string) {
		id := uuid.New()
		if err := a.db.Create(&models.WorkflowRun{ID: id, UserID: user, ClientType: "pc", ClientProjectName: name, Status: "succeeded", BillingStatus: "confirmed",
			InputHash: uuid.NewString(), ClientTaskID: ptrString("task-" + requestID), ClientInstanceID: "instance",
			LastActiveAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
			t.Fatal(err)
		}
		if err := a.db.Create(&models.WorkflowStepExecution{ID: uuid.New(), WorkflowRunID: id, StepID: "movie-model", InputHash: uuid.NewString(), Status: "succeeded", ExecutionKey: uuid.NewString(),
			ProviderRequestID: ptrString(requestID)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	run(owner, movieProjectRunName(movie), "paid-call")
	run(owner, movieProjectRunName(second), "other-project-call")
	run(other, movieProjectRunName(movie), "other-user-call")
	for _, row := range []struct {
		user    uuid.UUID
		credits int64
		meta    map[string]any
	}{
		{owner, 40, map[string]any{"request_id": "paid-call", "input_fingerprint": "f"}},
		{owner, 900, map[string]any{"request_id": "other-project-call"}},
		{other, 800, map[string]any{"request_id": "other-user-call"}},
		{owner, 5, map[string]any{"request_id": "tagged", "movie_project_id": movie.String()}},
	} {
		if err := a.db.Create(&models.TokenUsageRecord{UserID: row.user, CreditCount: row.credits, RequestMetadata: jsonMap(row.meta)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	var total int64
	if err := movieProjectUsageQuery(a.db, owner, movie, nil).Select("SUM(credit_count)").Scan(&total).Error; err != nil {
		t.Fatal(err)
	}
	if total != 45 {
		t.Fatalf("project total = %d, want 45 (tagged 5 + its own workflow call 40)", total)
	}
}

func TestProviderFinalizationKeepsTheMovieProjectTag(t *testing.T) {
	movie := uuid.NewString()
	consumption := providerFinalizationConsumption(workflowRequestExecution{}, creditConsumption{RequestMetadata: map[string]any{
		"movie_project_id": movie, workflowInputFingerprintMetadataKey: "fingerprint", "noise": "dropped"}}, ptrString("request-1"))
	if consumption.RequestMetadata["movie_project_id"] != movie || consumption.RequestMetadata["request_id"] != "request-1" || consumption.RequestMetadata["noise"] != nil {
		t.Fatalf("metadata = %#v", consumption.RequestMetadata)
	}
}
