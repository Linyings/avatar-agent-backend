package server

import (
	"strings"
	"testing"
)

func TestNarrativeInvalidOutputMessage(t *testing.T) {
	for _, test := range []struct{ name, content, reason, purpose, want string }{
		{"empty", "", "stop", "narrative_understanding", "空结果"},
		{"whitespace", "  ", "", "narrative_understanding", "空结果"},
		{"truncated", "partial", "length", "narrative_understanding", "输出上限"},
		{"invalid", "{}", "stop", "narrative_understanding", "有效时间戳"},
		{"importance", "{}", "stop", "movie_importance_selection", "优先级和理由"},
		{"research", "{}", "stop", "movie_background_research_summary", "作品概览"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := narrativeInvalidOutputMessage(map[string]any{"purpose": test.purpose}, test.content, map[string]any{"finish_reason": test.reason})
			if !strings.Contains(got, test.want) {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
	if got := narrativeInvalidOutputMessage(nil, "", nil); !strings.Contains(got, "空结果") {
		t.Fatal(got)
	}
}
