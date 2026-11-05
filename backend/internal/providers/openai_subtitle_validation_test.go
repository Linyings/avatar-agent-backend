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

func TestApplyStructuredOutputOptionsForSubtitleValidation(t *testing.T) {
	payload := map[string]any{"temperature": 0.7}

	(OpenAIClient{}).applyStructuredOutputOptions(payload, "subtitle_validation", map[string]any{})

	if payload["temperature"] != 0 {
		t.Fatalf("expected temperature 0, got %#v", payload["temperature"])
	}
	responseFormat, ok := payload["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("expected response_format object, got %#v", payload["response_format"])
	}
	if responseFormat["type"] != "json_object" {
		t.Fatalf("expected json_object response format, got %#v", responseFormat)
	}
}

func TestSubtitleValidationBudgetFitsFullChunkWithoutBudgetingReadOnlyContext(t *testing.T) {
	segments := make([]any, 20)
	for i := range segments {
		segments[i] = map[string]any{
			"id": fmt.Sprint(i + 1), "start": float64(i * 3), "end": float64(i*3 + 2),
			"text": "他带着家人回到村里，却发现家门已经上了锁。", "speaker_id": "speaker_0",
		}
	}
	payload := map[string]any{}
	request := map[string]any{"segments": segments, "context_segments": []any{}}
	(OpenAIClient{}).applyStructuredOutputOptions(payload, "subtitle_validation", request)
	budget := payload["max_tokens"].(int)
	fullResponse, _ := json.Marshal(map[string]any{"summary": "已校对", "segments": segments, "notes": []any{}, "corrections_count": 0})
	// Even a conservative one-token-per-byte estimate fits the unchanged JSON.
	if budget < len(fullResponse) || budget > 8192 {
		t.Fatalf("20-subtitle budget = %d, full response bytes = %d", budget, len(fullResponse))
	}
	request["context_segments"] = []any{map[string]any{"text": strings.Repeat("只读上下文", 2000)}}
	(OpenAIClient{}).applyStructuredOutputOptions(payload, "subtitle_validation", request)
	if payload["max_tokens"] != budget {
		t.Fatalf("read-only context increased output budget: %#v", payload)
	}
	request["segments"] = []any{map[string]any{"text": strings.Repeat("长字幕", 20000)}}
	(OpenAIClient{}).applyStructuredOutputOptions(payload, "subtitle_validation", request)
	if payload["max_tokens"] != 16384 {
		t.Fatalf("oversized request has no bounded output: %#v", payload)
	}
	wholeVideoPayload := map[string]any{}
	delete(request, "context_segments")
	(OpenAIClient{}).applyStructuredOutputOptions(wholeVideoPayload, "subtitle_validation", request)
	if _, capped := wholeVideoPayload["max_tokens"]; capped {
		t.Fatalf("legacy whole-video request was given a chunk-size output limit: %#v", wholeVideoPayload)
	}
}

func TestSubtitleFlashDisablesReasoningThroughAliasRelayAndNarrativeBothStayOff(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests = append(requests, payload)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"req-test\",\"model\":\"deepseek-flash\",\"choices\":[{\"delta\":{\"content\":\"{}\"}}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	client := NewOpenAIClient(config.Config{
		OpenAIFlashAPIKey: "test", OpenAIFlashAPIBaseURL: server.URL, OpenAIFlashModel: "narratoai-llm-flash",
		OpenAIFlashThinkingType: "disabled", OpenAIFlashReasoningEffort: "none",
		OpenAIProAPIKey: "test", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "narratoai-llm-pro",
		OpenAIProThinkingType: "enabled", OpenAIProReasoningEffort: "medium",
	}, nil)
	for _, kind := range []string{"subtitle_validation", "frame_narrative_understanding"} {
		if err := client.Stream(context.Background(), kind, map[string]any{"segments": []any{}, "context_segments": []any{}}, func(string, map[string]any) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if len(requests) != 2 {
		t.Fatalf("provider requests = %d, want one per task", len(requests))
	}
	flash, pro := requests[0], requests[1]
	if flash["model"] != "narratoai-llm-flash" || flash["reasoning_effort"] != "none" || flash["thinking"].(map[string]any)["type"] != "disabled" {
		t.Fatalf("flash reasoning switch missing: %#v", flash)
	}
	// Legacy relays discard unknown `thinking` fields when rewriting aliases;
	// reasoning_effort must independently carry the non-thinking instruction.
	delete(flash, "thinking")
	if flash["reasoning_effort"] != "none" || flash["max_tokens"] == nil {
		t.Fatalf("alias conversion loses bounded proofreading: %#v", flash)
	}
	if pro["model"] != "narratoai-llm-pro" || pro["reasoning_effort"] != "none" || pro["thinking"].(map[string]any)["type"] != "disabled" {
		t.Fatalf("narrative must also keep reasoning off: %#v", pro)
	}
}
