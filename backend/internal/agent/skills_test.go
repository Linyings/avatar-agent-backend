package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestDefaultSkillCatalogLoadsDocumentsOnDemand(t *testing.T) {
	skills := DefaultSkills()
	if len(skills) == 0 {
		t.Fatal("default skill catalog is empty")
	}
	var found bool
	for _, skill := range skills {
		if skill.Name != "talking-head-storyboard" {
			continue
		}
		found = true
		if !skill.DefaultEnabled || !skill.AllowImplicitInvocation || skill.Description == "" {
			t.Fatalf("skill is not available for model selection: %#v", skill)
		}
		for _, resource := range append([]string{"SKILL.md"}, skill.Resources...) {
			doc, err := ReadSkill(skill.Name, resource)
			if err != nil || doc.Content == "" || len(doc.Content) > 24*1024 {
				t.Fatalf("invalid resource %s: %v", resource, err)
			}
		}
	}
	if !found {
		t.Fatal("storyboard skill missing")
	}
}

func TestSkillToolsRejectUnknownNamesAndPaths(t *testing.T) {
	reg := NewToolRegistry()
	RegisterSkillTools(reg)
	for _, args := range []map[string]any{
		{},
		{"name": "unknown"},
		{"name": "talking-head-storyboard", "resource": "../SKILL.md"},
		{"name": "talking-head-storyboard", "resource": "/etc/passwd"},
		{"name": "talking-head-storyboard", "resource": `..\SKILL.md`},
		{"name": "talking-head-storyboard", "resource": ".env"},
		{"name": "talking-head-storyboard", "resource": 42},
		{"name": "talking-head-storyboard", "resource": "references/missing.md"},
		{"name": "talking-head-storyboard", "execute": true},
	} {
		_, blocked, issue := reg.Invoke(context.Background(), ToolCall{Name: "read_skill", Arguments: args}, "test", func(any) {})
		if !blocked || issue == nil {
			t.Fatalf("invalid read was accepted: %#v", args)
		}
	}
	if _, err := ReadSkill("talking-head-storyboard", "../SKILL.md"); err == nil {
		t.Fatal("direct resource reader accepted traversal")
	}
}

func TestAgentCanChooseSkillAndReadReferences(t *testing.T) {
	llm := &fakeLLM{responses: []string{
		`{"reply":"先读取分镜方法","tool_calls":[{"id":"s1","name":"read_skill","arguments":{"name":"talking-head-storyboard"}}]}`,
		`{"reply":"再读取交付格式","tool_calls":[{"id":"s2","name":"read_skill","arguments":{"name":"talking-head-storyboard","resource":"references/storyboard-format.md"}}]}`,
		`{"reply":"建议按讲解目标划分场景，人物保持稳定小窗。","tool_calls":[]}`,
	}}
	project := uuid.NewString()
	session := NewSession(SessionConfig{ProjectID: project, LLM: llm})
	out, err := session.Chat(context.Background(), ChatInput{ProjectID: project, Message: "分析这份分镜，不开始制作", TurnID: uuid.NewString(), RequestID: uuid.NewString()}, &stubState{})
	if err != nil || len(llm.calls) != 3 {
		t.Fatalf("skill loop did not finish: %v, calls=%d", err, len(llm.calls))
	}
	if !strings.Contains(out.Reply, "稳定小窗") {
		t.Fatalf("unexpected answer: %#v", out)
	}
	for index, resource := range []string{"SKILL.md", "references/storyboard-format.md"} {
		var found bool
		for _, message := range llm.calls[index+1].History {
			if message.Role != "tool" || message.ToolName != "read_skill" {
				continue
			}
			var doc SkillDocument
			if json.Unmarshal([]byte(message.Content), &doc) == nil && doc.Resource == resource && doc.Content != "" {
				found = true
			}
		}
		if !found {
			t.Fatalf("model did not receive %s", resource)
		}
	}
}

func TestAgentCanSkipDefaultSkillForUnrelatedRequest(t *testing.T) {
	llm := &fakeLLM{responses: []string{`{"reply":"你好。","tool_calls":[]}`}}
	project := uuid.NewString()
	session := NewSession(SessionConfig{ProjectID: project, LLM: llm})
	_, err := session.Chat(context.Background(), ChatInput{ProjectID: project, Message: "你好", TurnID: uuid.NewString(), RequestID: uuid.NewString()}, &stubState{})
	if err != nil || len(llm.calls) != 1 {
		t.Fatalf("unrelated request was forced to load a skill: %v", err)
	}
	req := llm.calls[0]
	if !strings.Contains(req.SystemPrompt, "talking-head-storyboard") || !strings.Contains(req.SystemPrompt, "自行判断") {
		t.Fatal("model did not receive default skill selection guidance")
	}
	if strings.Contains(req.SystemPrompt, "区分五种粒度") || strings.Contains(req.SystemPrompt, "P01：") {
		t.Fatal("skill documents were eagerly injected")
	}
	var available bool
	for _, definition := range req.Tools {
		available = available || definition["name"] == "read_skill"
	}
	if !available {
		t.Fatal("model cannot choose to load the default skill")
	}
}
