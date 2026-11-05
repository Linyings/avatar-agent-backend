package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/config"
)

func TestMovieNarrationProfileIsolation(t *testing.T) {
	cfg := config.Config{OpenAIProAPIKey: "pro", OpenAIProAPIBaseURL: "https://pro.test", OpenAIProModel: "pro", OpenAIFlashAPIKey: "flash", OpenAIFlashAPIBaseURL: "https://flash.test", OpenAIFlashModel: "flash", MovieNarrationAPIKey: "writer", MovieNarrationAPIBaseURL: "https://writer.test", MovieNarrationModel: "writer", DeepSeekAPIKey: "ds", DeepSeekAPIBaseURL: "https://ds.test", DeepSeekModel: "ds"}
	cases := []struct {
		name, kind, operation, purpose, profile, provider string
		movie                                             bool
		want                                              string
	}{
		{"chapter writing", "script_draft", "narration_segment", "", "pro", "", true, "writer"},
		{"review correction writes prose", "script_draft", "narration_global_repair", "", "pro", "", true, "writer"},
		{"writing overrides flash", "script_draft", "narration_segment", "", "flash", "", true, "writer"},
		{"writing overrides provider", "script_draft", "narration_segment", "", "pro", "deepseek", true, "writer"},
		{"selected rewrite", "script_draft", "narration_outline", "movie_screenplay_selection_rewrite", "pro", "", true, "writer"},
		{"full rewrite", "script_draft", "narration_outline", "movie_screenplay_user_rewrite", "pro", "", true, "writer"},
		{"planning", "script", "movie_director_script", "", "pro", "", true, "pro"},
		{"review", "script_draft", "narration_global_review", "movie_screenplay_full_review", "flash", "", true, "flash"},
		{"format repair", "script_draft", "narration_outline", "movie_format_repair", "flash", "", true, "flash"},
		{"agent chat", "script_draft", "narration_outline", "movie_agent_chat", "pro", "", true, "pro"},
		{"story", "frame_narrative_understanding", "", "", "pro", "", true, "pro"},
		{"other modes", "script_draft", "narration_segment", "", "pro", "", false, "pro"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := map[string]any{"model_operation": tc.operation, "model_profile": tc.profile, "model_provider": tc.provider, "video_context": map[string]any{"purpose": tc.purpose}}
			if tc.movie {
				req["movie_prompt_version"] = 1
			}
			client := NewOpenAIClient(cfg, nil)
			if got := client.ModelFor(tc.kind, req); got != tc.want {
				t.Fatalf("model=%s want %s", got, tc.want)
			}
			changed := cfg
			changed.MovieNarrationModel = "writer-v2"
			want := tc.want
			if want == "writer" {
				want = "writer-v2"
			}
			if got := NewOpenAIClient(changed, nil).ModelFor(tc.kind, req); got != want {
				t.Fatalf("changed model=%s want %s", got, want)
			}
		})
	}
	req := map[string]any{"movie_prompt_version": 1, "model_operation": "narration_segment"}
	cfg.MovieNarrationAPIKey = ""
	client := NewOpenAIClient(cfg, nil)
	if client.ConfiguredFor("script_draft", req) {
		t.Fatal("missing writer key must not fall back to Pro")
	}
	err := client.Stream(context.Background(), "script_draft", req, func(string, map[string]any) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "MOVIE_NARRATION") {
		t.Fatalf("expected clear config error, got %v", err)
	}
}

func TestMovieNarrationStreamUsesDedicatedEndpointKeyModelAndReasoning(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer writer-secret" {
			t.Errorf("wrong writer transport")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload["model"] != "writer-model" || payload["reasoning_effort"] != "max" {
			t.Errorf("wrong model or effort: %v / %v", payload["model"], payload["reasoning_effort"])
		}
		thinking, _ := payload["thinking"].(map[string]any)
		if thinking["type"] != "enabled" {
			t.Errorf("writer thinking ignored")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"writer output\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client := NewOpenAIClient(config.Config{MovieNarrationAPIKey: "writer-secret", MovieNarrationAPIBaseURL: server.URL, MovieNarrationModel: "writer-model", MovieNarrationThinkingType: "enabled", MovieNarrationReasoningEffort: "max", OpenAIProAPIBaseURL: "http://invalid", OpenAIProAPIKey: "pro", OpenAIProModel: "pro"}, nil)
	err := client.Stream(context.Background(), "script_draft", map[string]any{"movie_prompt_version": 1, "model_operation": "narration_segment", "video_context": map[string]any{"purpose": "movie_screenplay_full_draft"}}, func(string, map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("writer calls=%d", calls)
	}
}

func TestGeneralModeWritingUsesNarrationModel(t *testing.T) {
	cfg := config.Config{OpenAIProAPIKey: "pro", OpenAIProAPIBaseURL: "https://pro.test", OpenAIProModel: "pro", OpenAIFlashAPIKey: "flash", OpenAIFlashAPIBaseURL: "https://flash.test", OpenAIFlashModel: "flash", MovieNarrationAPIKey: "writer", MovieNarrationAPIBaseURL: "https://writer.test", MovieNarrationModel: "writer"}
	general := func(ctx map[string]any, profile string) map[string]any {
		base := map[string]any{"contract": "general-narration-v3", "purpose": "script"}
		for k, v := range ctx {
			base[k] = v
		}
		return map[string]any{"model_operation": "narration_outline", "model_profile": profile, "video_context": base}
	}
	cases := []struct {
		name string
		kind string
		req  map[string]any
		want string
	}{
		{"full draft", "script_draft", general(nil, "pro"), "writer"},
		{"full draft asked for flash", "script_draft", general(nil, "flash"), "writer"},
		{"bridges between original clips", "script_draft", general(map[string]any{"repairScope": "mix-continuity-v1"}, "pro"), "writer"},
		{"single chapter rewrite", "script_draft", general(map[string]any{"repairScope": "single-chapter-v1"}, "pro"), "writer"},
		{"original layout with narration repair", "script_draft", general(map[string]any{"purpose": "original-layout", "allowNarrationRepair": true}, "pro"), "writer"},
		{"original layout only", "script_draft", general(map[string]any{"purpose": "original-layout"}, "pro"), "pro"},
		{"verbatim restore is not writing", "script_draft", general(map[string]any{"expectedText": "原句"}, "pro"), "pro"},
		{"0% narration is not writing", "script_draft", general(map[string]any{"narrationRatio": 0, "narrationMixPolicy": "duration-ratio-v1"}, "pro"), "pro"},
		{"older general contract", "script_draft", map[string]any{"model_operation": "narration_outline", "model_profile": "pro", "video_context": map[string]any{"contract": "general-editing-v2", "purpose": "script"}}, "pro"},
		{"picture matching is not writing", "script_match", general(nil, "pro"), "pro"},
		{"story understanding", "frame_narrative_understanding", general(nil, "pro"), "pro"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewOpenAIClient(cfg, nil).ModelFor(tc.kind, tc.req); got != tc.want {
				t.Fatalf("model=%s want %s", got, tc.want)
			}
		})
	}
	// Without a dedicated writing model the general draft keeps the existing Pro model instead of failing.
	noWriter := cfg
	noWriter.MovieNarrationAPIKey, noWriter.MovieNarrationModel = "", ""
	client := NewOpenAIClient(noWriter, nil)
	if got := client.ModelFor("script_draft", general(nil, "pro")); got != "pro" || !client.ConfiguredFor("script_draft", general(nil, "pro")) {
		t.Fatalf("fallback model=%s configured=%v", got, client.ConfiguredFor("script_draft", general(nil, "pro")))
	}
}
