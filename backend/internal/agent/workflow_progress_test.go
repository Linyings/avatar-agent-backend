package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
)

type executionPackageHost struct{ calls []string }

func (h *executionPackageHost) Invoke(_ context.Context, _, _, id string, _ map[string]any, _ string) (any, error) {
	h.calls = append(h.calls, id)
	if id == "avatar.render" {
		return map[string]any{"submitted": true, "run_id": "run-1", "status": "running"}, nil
	}
	return map[string]any{"saved": true}, nil
}
func (*executionPackageHost) Confirm(context.Context, string, string, string, int) (any, error) {
	return nil, nil
}

func TestAgentExecutionContinuesPastUnfulfilledPromises(t *testing.T) {
	host := &executionPackageHost{}
	llm := &fakeLLM{responses: []string{
		`{"reply":"我先把需求参数保存下来，文案生成后需要你确认。"}`,
		`{"tool_calls":[{"id":"save","name":"avatar_configure","arguments":{"script":"提供的原稿。"}}]}`,
		`{"reply":"接下来开始制作视频。"}`,
		`{"tool_calls":[{"id":"start","name":"plan_project","arguments":{"target":"F12","reason":"用户要求制作完整视频"}}]}`,
		`{"reply":"制作任务已提交，文案就绪后需要你确认。"}`,
	}}
	s := NewSession(SessionConfig{ProjectID: uuid.NewString(), LLM: llm, Packages: host})
	out, err := s.Chat(context.Background(), ChatInput{Message: "帮我制作数字人口播视频", RequestID: uuid.NewString(), TurnID: uuid.NewString()}, &stubState{})
	if err != nil {
		t.Fatal(err)
	}
	if len(host.calls) != 2 || host.calls[1] != "avatar.render" || !strings.Contains(out.Reply, "已提交") {
		t.Fatalf("silently stopped: calls=%v reply=%s", host.calls, out.Reply)
	}
}

func TestAgentExecutionDoesNotPretendToRunWhenModelNeverCallsTools(t *testing.T) {
	llm := &fakeLLM{responses: []string{`{"reply":"我先保存参数。"}`, `{"reply":"马上开始制作。"}`, `{"reply":"已完成。"}`}}
	host := &executionPackageHost{}
	s := NewSession(SessionConfig{ProjectID: uuid.NewString(), LLM: llm, Packages: host})
	out, err := s.Chat(context.Background(), ChatInput{Message: "帮我制作口播视频", RequestID: uuid.NewString()}, &stubState{})
	if err != nil {
		t.Fatal(err)
	}
	if len(host.calls) != 0 || out.Status != "needs_action" || !strings.Contains(out.Reply, "尚未启动") {
		t.Fatalf("false completion: %#v", out)
	}
}

func TestAgentExecutionRespectsAdviceOnlyRequests(t *testing.T) {
	llm := &fakeLLM{responses: []string{`{"reply":"我先说明制作思路。"}`}}
	host := &executionPackageHost{}
	s := NewSession(SessionConfig{ProjectID: uuid.NewString(), LLM: llm, Packages: host})
	_, err := s.Chat(context.Background(), ChatInput{Message: "只给建议，暂不生成视频", RequestID: uuid.NewString()}, &stubState{})
	if err != nil || len(llm.calls) != 1 || len(host.calls) != 0 {
		t.Fatalf("advice started work: %v", err)
	}
}

func TestAgentWorkflowStateIncludesSucceededSubstepsAndGate(t *testing.T) {
	s := NewWorkflowProjectState(avatar.WorkflowState{Input: avatar.WorkflowInput{Revision: 1}, Status: "awaiting_confirmation", Stage: "F04/p1", Confirmation: "audio", Steps: []avatar.WorkflowStep{{ID: "F02", Status: "succeeded"}, {ID: "F04/p1", Status: "succeeded"}}})
	text := s.GetWorkflowState()
	if !strings.Contains(text, "F02") || !strings.Contains(text, "等待用户确认配音") {
		t.Fatalf("missing gate: %s", text)
	}
	if got := s.GetNodeStatuses()["F04"]; got != "awaiting_confirmation" {
		t.Fatalf("gate status=%s", got)
	}
}

func TestAgentExecutionStatusQuestionDoesNotSuppressContinue(t *testing.T) {
	host := &executionPackageHost{}
	llm := &fakeLLM{responses: []string{`{"tool_calls":[{"id":"start","name":"plan_project","arguments":{"target":"F12","reason":"继续执行"}}]}`, `{"reply":"已提交，后台继续执行"}`}}
	s := NewSession(SessionConfig{ProjectID: uuid.NewString(), LLM: llm, Packages: host})
	_, err := s.Chat(context.Background(), ChatInput{Message: "继续制作并告诉我当前进度", RequestID: uuid.NewString()}, &stubState{})
	if err != nil || len(host.calls) != 1 {
		t.Fatalf("mixed request routed to status only: %v %v", err, host.calls)
	}
}

func TestAgentExecutionRequiresActiveRunOrPendingProposal(t *testing.T) {
	for _, tc := range []struct {
		result string
		want   bool
	}{
		{`{"run":{"id":"old","status":"succeeded"}}`, false},
		{`{"run":{"id":"old","status":"failed"}}`, false},
		{`{"run":{"id":"old","status":"paused"}}`, false},
		{`{"run_id":"old","status":"succeeded","submitted":false}`, false},
		{`{"proposal":{"id":"old","applied":true}}`, false},
		{`{"run":{"id":"active","status":"running"}}`, true},
		{`{"run":{"id":"active","status":"awaiting_confirmation"}}`, true},
		{`{"run_id":"audio-ready","status":"awaiting_confirmation","confirmation":"audio","submitted":false,"requires_user_choice":true}`, true},
		{`{"run_id":"new","status":"running","submitted":true}`, true},
		{`{"proposal":{"id":"new","applied":false}}`, true},
	} {
		if got := executionResolved(tc.result); got != tc.want {
			t.Errorf("resolved(%s)=%v want %v", tc.result, got, tc.want)
		}
	}
}
