package server

import "testing"

func TestSubtitleValidationPromptPayloadKeepsTargetAndReadOnlyContext(t *testing.T) {
	got := subtitleValidationPromptPayload(map[string]any{
		"segments":         []any{map[string]any{"id": "2", "text": "目标"}},
		"context_segments": []any{map[string]any{"id": "1", "text": "前文"}},
		"language":         " zh-CN ",
		"project_id":       "project-1",
	})

	if _, ok := got["segments"]; !ok {
		t.Fatalf("target segments were dropped: %#v", got)
	}
	if _, ok := got["context_segments"]; !ok {
		t.Fatalf("read-only context was dropped: %#v", got)
	}
	if got["language"] != "zh-CN" {
		t.Fatalf("language = %#v, want trimmed language", got["language"])
	}
	if _, ok := got["project_id"]; ok {
		t.Fatalf("project id must not enter the provider prompt: %#v", got)
	}
}
