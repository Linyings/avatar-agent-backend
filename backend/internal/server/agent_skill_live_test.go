package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/agent"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/config"
)

type skillTraceTransport struct{ t *testing.T }

func (tr skillTraceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return res, err
	}
	raw, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return nil, err
	}
	res.Body = io.NopCloser(bytes.NewReader(raw))
	var payload struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string            `json:"content"`
				Reasoning string            `json:"reasoning_content"`
				Tools     []json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(raw, &payload) == nil && len(payload.Choices) > 0 {
		c := payload.Choices[0]
		tr.t.Logf("upstream finish=%s content_bytes=%d reasoning_bytes=%d tools=%d usage=%s", c.FinishReason, len(c.Message.Content), len(c.Message.Reasoning), len(c.Message.Tools), payload.Usage)
	}
	return res, nil
}

func TestLiveDefaultSkillDiagnostics(t *testing.T) {
	if os.Getenv("AVATAR_VERIFY_LIVE_AGENT_SKILL") != "1" {
		t.Skip("manual real-model diagnostic")
	}
	cfg := config.Load()
	llm := newAgentLLMAdapter(cfg)
	llm.client.Transport = skillTraceTransport{t}
	id := uuid.NewString()
	session := agent.NewSession(agent.SessionConfig{ProjectID: id, LLM: llm})
	session.SetEmitter(func(event any) {
		if e, ok := event.(agent.ToolEvent); ok {
			t.Logf("tool=%s status=%s", e.Tool, e.Status)
		}
	})
	state := agent.NewWorkflowProjectState(avatar.WorkflowState{Stage: "F01", Status: "pending", Input: avatar.WorkflowInput{Ratio: "9:16", Style: "flat-vector", Script: "先确定目标，再准备材料，最后检查结果。"}})
	out, err := session.Chat(context.Background(), agent.ChatInput{ProjectID: id, TurnID: uuid.NewString(), RequestID: uuid.NewString(), Message: "只分析，不修改项目，不启动制作。请为三句话设计竖版口播分镜：先确定目标，再准备材料，最后检查结果。说明场景切分、人物窗口和图层动效，给一份简短建议。"}, state)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("final status=%s reply_bytes=%d", out.Status, len(out.Reply))
	if out.Reply == "" {
		t.Fatal("empty final reply")
	}
}
