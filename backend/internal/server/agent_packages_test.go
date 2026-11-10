package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/agent"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/models"
	"testing"
	"time"
)

func TestPackageProjectScopeAndPersistentProposal(t *testing.T) {
	a, owner, p, s := workflowFixture(t)
	h := avatarPackageHost{a}
	if _, err := h.Invoke(context.Background(), uuid.NewString(), p.ID.String(), "avatar.inspect", map[string]any{}, "read"); err == nil {
		t.Fatal("cross-owner package read allowed")
	}
	s.Status = "awaiting_confirmation"
	s.Confirmation = "script"
	s.Segments = []avatar.Segment{{ID: "p1", Text: "第一句。", Chapter: "开头"}, {ID: "p2", Text: "第二句。", Chapter: "结尾"}}
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"changes": "让开头更自然", "segments": []any{map[string]any{"id": "p1", "text": "修改后的开场。"}}}
	result, err := h.Invoke(context.Background(), owner.String(), p.ID.String(), "avatar.edit-script", args, "proposal-request")
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("missing proposal")
	}
	var stored models.Project
	if err = a.db.First(&stored, "id=?", p.ID).Error; err != nil {
		t.Fatal(err)
	}
	proposal := proposalOf(stored)
	if proposal == nil || proposal.Applied || proposal.Edits.Segments[0].Text != "修改后的开场。" || proposal.Edits.Segments[1].Text != "第二句。" {
		t.Fatalf("proposal not persisted: %#v", proposal)
	}
	unchanged, err := a.avatarWorkflow.load(context.Background(), owner, s.ID)
	if err != nil || unchanged.Segments[0].Text != "第一句。" {
		t.Fatal("draft changed production before confirmation")
	}
	if _, err = h.Confirm(context.Background(), owner.String(), p.ID.String(), proposal.ID, proposal.Revision+1); err == nil {
		t.Fatal("stale revision accepted")
	}
	if _, err = h.Invoke(context.Background(), owner.String(), p.ID.String(), "avatar.edit-script", map[string]any{"changes": "invalid", "segments": []any{map[string]any{"id": "missing", "text": "bad"}}}, "bad"); err == nil {
		t.Fatal("nonexistent target accepted")
	}
	state, err := (agentProjectResolver{a}).Resolve(context.Background(), owner, p.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	segments, err := state.GetScriptSegments("")
	if err != nil || len(segments) != 2 {
		t.Fatalf("project resolver did not load latest RUN: %v %#v", err, segments)
	}
}

func TestPackageConfigurationDoesNotAcceptFilesystemPaths(t *testing.T) {
	a, owner, p, _ := workflowFixture(t)
	h := avatarPackageHost{a}
	if _, err := h.Invoke(context.Background(), owner.String(), p.ID.String(), "avatar.configure", map[string]any{"photo": "/etc/passwd"}, "bad"); err == nil {
		t.Fatal("accepted path injection")
	}
	if _, err := h.Invoke(context.Background(), owner.String(), p.ID.String(), "avatar.configure", map[string]any{"revision": float64(0), "topic": "new"}, "stale"); err == nil {
		t.Fatal("accepted stale draft")
	}
	if _, err := h.Invoke(context.Background(), owner.String(), p.ID.String(), "avatar.configure", map[string]any{"revision": float64(1), "topic": "new"}, "save"); err != nil {
		t.Fatal(err)
	}
	_, in, err := h.project(context.Background(), owner.String(), p.ID.String())
	if err != nil || in.Revision != 2 || in.Topic != "new" {
		t.Fatal("configuration not saved")
	}
}

func TestPackageAdapterPreservesOpenAIToolCallProtocol(t *testing.T) {
	adapter := &agentLLMAdapter{}
	messages := adapter.buildMessages(agent.LLMRequest{History: []agent.ChatMessage{
		{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "call-1", Name: "avatar_inspect", Arguments: map[string]any{}}}},
		{Role: "tool", ToolID: "call-1", Content: `{"ok":true}`},
	}})
	raw, _ := json.Marshal(messages)
	var decoded []map[string]any
	_ = json.Unmarshal(raw, &decoded)
	calls, ok := decoded[0]["tool_calls"].([]any)
	if !ok || len(calls) != 1 || calls[0].(map[string]any)["id"] != "call-1" || decoded[1]["tool_call_id"] != "call-1" {
		t.Fatalf("invalid protocol: %s", raw)
	}
}

func TestPackageHTTPRejectsUnauthorizedAndCrossOwner(t *testing.T) {
	a := newTestApp(t)
	if res := request(a, "GET", "/api/v1/agent/packages", nil, ""); res.Code != 401 {
		t.Fatalf("unauthenticated package catalog: %d", res.Code)
	}
	first := registerAndLogin(t, a, "packages-first@example.com")
	second := registerAndLogin(t, a, "packages-second@example.com")
	res := request(a, "POST", "/api/v1/avatar-mvp/projects", map[string]any{"title": "package-test", "topic": "test", "style": "flat-vector"}, first)
	if res.Code != 200 {
		t.Fatalf("create: %d %s", res.Code, res.Body.String())
	}
	var payload map[string]any
	_ = json.Unmarshal(res.Body.Bytes(), &payload)
	if data, ok := payload["data"].(map[string]any); ok {
		payload = data
	}
	id := payload["id"].(string)
	for _, token := range []string{"", second} {
		res = request(a, "POST", "/api/v1/agent/packages/avatar.configure/invoke", map[string]any{"projectId": id, "input": map[string]any{"topic": "hijack"}}, token)
		if res.Code == 200 {
			t.Fatal("unauthorized mutation allowed")
		}
	}
}

func TestPackageConfirmedEditCreatesOneRunAndWaitsForReview(t *testing.T) {
	a, owner, p, s := workflowFixture(t)
	// SQLite's shared in-memory fixture has table locks, unlike production PostgreSQL.
	pool, err := a.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	h := avatarPackageHost{a}
	ctx := context.Background()
	a.cfg.OpenAIFlashAPIKey = "test"
	a.cfg.OpenAIFlashModel = "test"
	a.cfg.OpenAIFlashAPIBaseURL = "http://unused"
	a.cfg.DashScopeAPIKey = "test"
	a.cfg.QwenTTSModel = "test"
	s.Status = "awaiting_confirmation"
	s.Confirmation = "script"
	s.Segments = []avatar.Segment{{ID: "p1", Text: "第一句。"}, {ID: "p2", Text: "第二句。"}}
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	a.avatarWorkflow.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		return nil, errors.New("unexpected paid call")
	}
	_, err = h.Invoke(ctx, owner.String(), p.ID.String(), "avatar.edit-script", map[string]any{"changes": "修改开头", "segments": []any{map[string]any{"id": "p1", "text": "新的开头。"}}}, "draft")
	if err != nil {
		t.Fatal(err)
	}
	stored, _, _ := h.project(ctx, owner.String(), p.ID.String())
	proposal := proposalOf(stored)
	if _, err = h.Confirm(ctx, owner.String(), p.ID.String(), proposal.ID, proposal.Revision); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		current, e := a.latestAgentRun(ctx, owner, p.ID)
		if e != nil {
			t.Fatal(e)
		}
		if current.Status == "awaiting_confirmation" && current.ID != s.ID {
			if current.Segments[0].Text != "新的开头。" || current.Segments[1].Text != "第二句。" {
				t.Fatal("edits or untouched rows lost")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not reach review: %s %s", current.Status, current.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = h.Confirm(ctx, owner.String(), p.ID.String(), proposal.ID, proposal.Revision); err != nil {
		t.Fatal(err)
	}
	var count int64
	a.db.Model(&models.WorkflowRun{}).Where("project_id=?", p.ID).Count(&count)
	if count != 2 {
		t.Fatalf("duplicate confirmation created %d runs", count)
	}
}

func TestPackageTargetStopsBeforeNextPaidStep(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	s.TargetNode = "F02"
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	called := false
	err := a.avatarWorkflow.step(context.Background(), &s, "F03", nil, func(context.Context, *avatar.WorkflowStep) error { called = true; return nil })
	if !errors.Is(err, errWorkflowPaused) || called {
		t.Fatalf("target ignored: %v called=%v", err, called)
	}
}

func TestPackagePauseSurvivesWorkerSave(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	if err := a.avatarWorkflow.update(s.ID, func(current *avatar.WorkflowState) error { current.PauseRequested = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	current, err := a.avatarWorkflow.load(context.Background(), uuid.MustParse(a.avatarWorkflow.ownerID(&s)), s.ID)
	if err != nil || !current.PauseRequested {
		t.Fatalf("pause lost: %v", err)
	}
}

func TestPackageEmptyTitlePreservesProjectName(t *testing.T) {
	a, owner, p, _ := workflowFixture(t)
	h := avatarPackageHost{a}
	if _, err := h.Invoke(context.Background(), owner.String(), p.ID.String(), "avatar.configure", map[string]any{"title": ""}, "title-update"); err != nil {
		t.Fatal(err)
	}
	_, in, err := h.project(context.Background(), owner.String(), p.ID.String())
	if err != nil || in.Title != p.Name {
		t.Fatalf("empty title was stored: %q, %v", in.Title, err)
	}
}
