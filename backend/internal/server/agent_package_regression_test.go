// Isolated adversarial review. No production configuration or paid providers.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/models"
)

func reviewTextConfig(a *App) {
	a.cfg.OpenAIFlashAPIKey = "test"
	a.cfg.OpenAIFlashModel = "test"
	a.cfg.OpenAIFlashAPIBaseURL = "http://127.0.0.1:1"
}

func TestAdversarialUnknownOrderAfterConfigure(t *testing.T) {
	a, owner, p, s := workflowFixture(t)
	pool, _ := a.db.DB()
	pool.SetMaxOpenConns(1)
	var submissions atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		submissions.Add(1)
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"isolated review: submission intercepted"}`))
	}))
	defer provider.Close()
	t.Setenv("RUNNINGHUB_BASE_URL", provider.URL)
	t.Setenv("RUNNINGHUB_API_KEY", "test")
	a.cfg.TTSModel = "doubao-seed-tts-2.0"
	s.Input.Voice = sVoice(a)
	if err := a.db.Model(&p).Update("project_metadata", jsonMap(map[string]any{"kind": "avatar_mvp", "avatar_v2": s.Input})).Error; err != nil {
		t.Fatal(err)
	}
	reviewTextConfig(a)
	s.Status = "submission_unknown"
	s.ScriptConfirmed = true
	s.Segments = []avatar.Segment{{ID: "p1", Text: s.Input.Script}}
	s.Steps = []avatar.WorkflowStep{{ID: "F04/p1", Status: "failed", Submission: "unknown", CostStatus: "unknown"}}
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	h := avatarPackageHost{a}
	if _, err := h.Invoke(ctx, owner.String(), p.ID.String(), "avatar.voice", nil, "before-save"); err == nil {
		t.Fatal("fixture: expected unknown order block before configure")
	}
	// Even an empty configuration patch increments revision without settling orders.
	if _, err := h.Invoke(ctx, owner.String(), p.ID.String(), "avatar.configure", map[string]any{}, "noop-save"); err != nil {
		t.Fatal(err)
	}
	result, err := h.Invoke(ctx, owner.String(), p.ID.String(), "avatar.voice", nil, "after-save")
	if err != nil {
		if !strings.Contains(err.Error(), "订单") {
			t.Fatalf("unexpected rejection: %v", err)
		}
		var count int64
		a.db.Model(&models.WorkflowRun{}).Where("project_id=?", p.ID).Count(&count)
		if count != 1 || submissions.Load() != 0 {
			t.Fatalf("unexpected side effect: runs=%d submissions=%d", count, submissions.Load())
		}
		return
	}
	id := result.(gin.H)["run_id"].(string)
	// Wait for the isolated worker to finish before its test database is closed.
	deadline := time.Now().Add(5 * time.Second)
	var next avatar.WorkflowState
	for time.Now().Before(deadline) {
		next, err = a.avatarWorkflow.load(ctx, owner, id)
		if err != nil {
			t.Fatal(err)
		}
		_, active := workflowLaunchLocks.Load(id)
		if !active {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("unresolved order bypassed: new_run=%t new_revision=%d status=%s provider_requests=%d; error=%s", id != s.ID, next.Input.Revision, next.Status, submissions.Load(), next.Error)
}

func TestAdversarialRenameThenSaveSettings(t *testing.T) {
	a, owner, p, s := workflowFixture(t)
	in := s.Input
	in.Title = "旧名称"
	if err := a.db.Model(&p).Updates(map[string]any{"name": in.Title, "project_metadata": jsonMap(map[string]any{"kind": "avatar_mvp", "avatar_v2": in})}).Error; err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	var user models.User
	user.ID = owner
	r.Use(func(c *gin.Context) { c.Set("user", user); c.Next() })
	r.PUT("/p/:project_id", a.updateProject)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/p/"+p.ID.String(), strings.NewReader(`{"name":"新名称"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("rename HTTP %d: %s", w.Code, w.Body.String())
	}
	h := avatarPackageHost{a}
	ctx := context.Background()
	renamed, draft, err := h.project(ctx, owner.String(), p.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "新名称" {
		t.Fatal("rename fixture did not apply")
	}
	if draft.Revision != 2 || draft.Title != "新名称" {
		t.Fatalf("rename did not atomically update draft revision/title: %#v", draft)
	}
	if _, err = h.Invoke(ctx, owner.String(), p.ID.String(), "avatar.configure", map[string]any{"revision": float64(1), "title": "旧名称"}, "stale-settings"); err == nil {
		t.Fatal("a settings form opened before the rename may not overwrite it")
	}
	_, err = h.Invoke(ctx, owner.String(), p.ID.String(), "avatar.configure", map[string]any{"title": draft.Title, "ratio": "16:9"}, "save-settings")
	if err != nil {
		t.Fatal(err)
	}
	after, _, _ := h.project(ctx, owner.String(), p.ID.String())
	if after.Name != "新名称" {
		t.Fatalf("rename reverted when saving ratio: draft title=%q final name=%q", draft.Title, after.Name)
	}
}

func TestAdversarialScriptProposalWithoutVideoPrerequisites(t *testing.T) {
	a, owner, p, s := workflowFixture(t)
	pool, _ := a.db.DB()
	pool.SetMaxOpenConns(1)
	reviewTextConfig(a)
	a.cfg.DashScopeAPIKey = ""
	a.cfg.QwenTTSModel = ""
	a.avatarWorkflow.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		return nil, fmt.Errorf("unexpected model invocation")
	}
	in := s.Input
	in.UseAvatar = true
	if err := a.db.Model(&p).Update("project_metadata", jsonMap(map[string]any{"kind": "avatar_mvp", "avatar_v2": in})).Error; err != nil {
		t.Fatal(err)
	}
	s.Input = in
	s.Status = "awaiting_confirmation"
	s.Confirmation = "script"
	s.Segments = []avatar.Segment{{ID: "p1", Text: in.Script}}
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	h := avatarPackageHost{a}
	ctx := context.Background()
	if err := a.avatarWorkflow.validatePackageInput(&in, "F03"); err != nil {
		t.Fatal(err)
	}
	_, err := h.Invoke(ctx, owner.String(), p.ID.String(), "avatar.edit-script", map[string]any{"changes": "修改开头", "segments": []any{map[string]any{"id": "p1", "text": "新开头。第二句。"}}}, "edit-script")
	if err != nil {
		t.Fatal(err)
	}
	stored, _, _ := h.project(ctx, owner.String(), p.ID.String())
	proposal := proposalOf(stored)
	applied, err := h.Confirm(ctx, owner.String(), p.ID.String(), proposal.ID, proposal.Revision)
	if err != nil {
		t.Fatalf("script-only edit blocked by unrelated video prerequisite: %v", err)
	}
	next := waitForPackageWorker(t, a, owner, applied.(gin.H)["run_id"].(string))
	if next.Status != "awaiting_confirmation" || next.Confirmation != "script" || next.TargetNode != "F03" || next.Segments[0].Text != "新开头。第二句。" {
		t.Fatalf("edit did not stop at script review: %#v", next)
	}
	if err = a.avatarWorkflow.confirm(ctx, owner, next.ID, "script", next.ConfirmationVersion, nil); err != nil {
		t.Fatal(err)
	}
	next, err = a.avatarWorkflow.load(ctx, owner, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.avatarWorkflow.launch(next, owner)
	next = waitForPackageWorker(t, a, owner, next.ID)
	if next.Status != "paused" {
		t.Fatalf("approved script ran beyond target: %s %s", next.Status, next.Error)
	}
	if _, err = h.Invoke(ctx, owner.String(), p.ID.String(), "avatar.control", map[string]any{"action": "resume"}, "resume"); err == nil {
		t.Fatal("full resume without downstream prerequisites was admitted")
	}
}

func waitForPackageWorker(t *testing.T, a *App, owner uuid.UUID, id string) avatar.WorkflowState {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if _, active := workflowLaunchLocks.Load(id); !active {
			state, err := a.avatarWorkflow.load(context.Background(), owner, id)
			if err != nil {
				t.Fatal(err)
			}
			return state
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("package worker did not finish")
	return avatar.WorkflowState{}
}

func TestPackageAdmissionBlocksUnresolvedHistoricalOrders(t *testing.T) {
	for _, status := range []string{"submission_unknown", "interrupted", "stopped"} {
		t.Run(status, func(t *testing.T) {
			a, owner, p, s := workflowFixture(t)
			s.Status = status
			s.Steps = []avatar.WorkflowStep{{ID: "F04/p1", Status: "failed", Submission: "unknown"}}
			if err := a.avatarWorkflow.save(&s); err != nil {
				t.Fatal(err)
			}
			// Bypass package revision checks deliberately: every run admission must guard orders.
			if _, err := a.avatarWorkflow.create(context.Background(), owner, p.ID, "different-request", 1); err == nil {
				t.Fatal("unresolved order accepted by common admission")
			}
			replay, err := a.avatarWorkflow.create(context.Background(), owner, p.ID, "request-1", 1)
			if err != nil || replay.ID != s.ID {
				t.Fatalf("same request must still return original run: %v", err)
			}
			s.Steps[0].Submission = "terminal_failed"
			s.Status = "failed"
			if err = a.avatarWorkflow.save(&s); err != nil {
				t.Fatal(err)
			}
			if _, err = a.avatarWorkflow.create(context.Background(), owner, p.ID, "after-settlement", 1); err != nil {
				t.Fatalf("settled order should allow new run: %v", err)
			}
		})
	}
}

func TestPackageAdmissionChecksOlderRunsAndKnownOrders(t *testing.T) {
	a, owner, p, old := workflowFixture(t)
	ctx := context.Background()
	old.Status = "succeeded"
	if err := a.avatarWorkflow.save(&old); err != nil {
		t.Fatal(err)
	}
	later, err := a.avatarWorkflow.create(ctx, owner, p.ID, "later-run", 1)
	if err != nil {
		t.Fatal(err)
	}
	later.Status = "stopped"
	if err = a.avatarWorkflow.save(&later); err != nil {
		t.Fatal(err)
	}
	// Simulate an already-existing historical run with an outstanding known order.
	old.Status = "stopped"
	old.Steps = []avatar.WorkflowStep{{ID: "F06/n1", Status: "interrupted", Submission: "submitted", ProviderTaskID: "existing-order"}}
	if err = a.avatarWorkflow.save(&old); err != nil {
		t.Fatal(err)
	}
	if _, err = a.avatarWorkflow.create(ctx, owner, p.ID, "new-attempt", 1); err == nil {
		t.Fatal("older outstanding order ignored")
	}
}

func TestPackageCancelledCallDoesNotMutateDraft(t *testing.T) {
	a, owner, p, _ := workflowFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (avatarPackageHost{a}).Invoke(ctx, owner.String(), p.ID.String(), "avatar.configure", map[string]any{"title": "should not save"}, "cancelled"); err == nil {
		t.Fatal("cancelled call accepted")
	}
	_, in, err := a.avatarWorkflow.project(context.Background(), owner, p.ID)
	if err != nil || in.Revision != 1 || in.Title == "should not save" {
		t.Fatalf("cancelled call changed draft: %v %#v", err, in)
	}
}

func TestPackageTargetIsPersistedAtAdmission(t *testing.T) {
	a, owner, p, s := workflowFixture(t)
	s.Status = "stopped"
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	next, err := a.avatarWorkflow.createTo(ctx, owner, p.ID, "partial-run", 1, "F03")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := a.avatarWorkflow.load(ctx, owner, next.ID)
	if err != nil || stored.TargetNode != "F03" {
		t.Fatalf("target missing at admission: %q %v", stored.TargetNode, err)
	}
	replay, err := a.avatarWorkflow.createTo(ctx, owner, p.ID, "partial-run", 1, "F12")
	if err != nil || replay.ID != next.ID || replay.TargetNode != "F03" {
		t.Fatalf("idempotent replay changed scope: %#v %v", replay, err)
	}
}
