package agent

import (
	"context"
	"testing"
)

func TestToolResultsHaveAssistantInvocationInHistory(t *testing.T) {
	llm := &fakeLLM{responses: []string{`{"tool_calls":[{"id":"lookup-1","name":"evidence","arguments":{"query":""}}]}`, `{"reply":"已查到"}`}}
	s := NewSession(SessionConfig{ProjectID: "protocol-project", LLM: llm})
	s.SetEmitter(func(any) {})
	if _, err := s.Chat(context.Background(), ChatInput{Message: "检索文案", TurnID: "protocol-turn", RequestID: "protocol-request"}, &stubState{}); err != nil {
		t.Fatal(err)
	}
	history := llm.calls[1].History
	found := false
	for i, m := range history {
		if m.Role == "tool" {
			if i == 0 || history[i-1].Role != "assistant" || len(history[i-1].ToolCalls) == 0 || history[i-1].ToolCalls[0].ID != m.ToolID {
				t.Fatalf("orphan tool result: %#v", history)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no tool result")
	}
}
