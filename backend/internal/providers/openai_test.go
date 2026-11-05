package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/prompts"
)

func TestOpenAIStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "openai-key", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "openai-test", OpenAITemperature: 0.1}, nil)
	var events []string
	err := client.Stream(context.Background(), "script", map[string]any{"video_context": "x"}, func(event string, data map[string]any) error {
		if delta, _ := data["delta"].(string); delta != "" {
			events = append(events, delta)
		}
		if content, _ := data["content"].(string); content != "" {
			events = append(events, content)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if !strings.Contains(strings.Join(events, ""), "hello world") {
		t.Fatalf("events = %#v", events)
	}
}

func TestOpenAIStreamStopsWhenProviderOnlySendsKeepAlive(t *testing.T) {
	previous := streamIdleTimeout
	streamIdleTimeout = 150 * time.Millisecond
	defer func() { streamIdleTimeout = previous }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; i < 40; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
			_, _ = w.Write([]byte(": keep-alive\n\n"))
			flusher.Flush()
		}
	}))
	defer server.Close()
	client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "k", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "m"}, nil)
	started := time.Now()
	err := client.Stream(context.Background(), "script", map[string]any{"video_context": "x"}, func(string, map[string]any) error { return nil })
	if err != errProviderStalled {
		t.Fatalf("a stream of keep-alives must fail as provider busy, got %v", err)
	}
	if time.Since(started) > 700*time.Millisecond {
		t.Fatalf("watchdog should stop the stream promptly, took %v", time.Since(started))
	}
}

func TestOpenAIStreamWithoutAnyDataIsProviderBusy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": keep-alive\n\n"))
	}))
	defer server.Close()
	client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "k", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "m"}, nil)
	if err := client.Stream(context.Background(), "script", map[string]any{"video_context": "x"}, func(string, map[string]any) error { return nil }); err != errProviderNoContent {
		t.Fatalf("an empty stream must fail as provider busy, got %v", err)
	}
}

func TestOpenAIStreamBlockedByContentReviewIsARejectedRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"r1\",\"choices\":[{\"index\":0,\"finish_reason\":\"sensitive\",\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "k", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "m"}, nil)
	err := client.Stream(context.Background(), "script_draft", map[string]any{"model_operation": "narration_outline"}, func(string, map[string]any) error { return nil })
	if err != errProviderContentBlocked || errProviderContentBlocked.ProviderHTTPStatus() != http.StatusBadRequest {
		t.Fatalf("a content-review stop must fail as a rejected request, not missing usage: %v", err)
	}
}

func TestOpenAINarrationSegmentFallsBackToNonStreamWhenStreamUsageIsMissing(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if streaming, _ := payload["stream"].(bool); streaming {
			w.Header().Set("Content-Type", "text/event-stream")
			content := `{"items":[{"_id":1,"text":"stream draft"}],"summary":"stream"}`
			_, _ = fmt.Fprintf(w, "data: {\"id\":\"req-stream\",\"model\":\"mimo-test\",\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", content)
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}

		if _, exists := payload["stream_options"]; exists {
			t.Fatalf("non-stream fallback leaked stream_options: %#v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "req-fallback",
			"model": "mimo-test",
			"choices": []map[string]any{{
				"message": map[string]any{"content": `{"items":[{"_id":1,"text":"fallback draft"}],"summary":"fallback"}`},
			}},
			"usage": map[string]any{"prompt_tokens": 30, "completion_tokens": 12, "total_tokens": 42},
		})
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{
		OpenAIFlashAPIKey:     "flash-key",
		OpenAIFlashAPIBaseURL: server.URL,
		OpenAIFlashModel:      "mimo-test",
		OpenAITemperature:     0.1,
	}, nil)
	var completed map[string]any
	err := client.Stream(context.Background(), "script_draft", map[string]any{
		"video_context":   "chapter context",
		"model_profile":   "flash",
		"model_operation": "narration_segment",
	}, func(event string, data map[string]any) error {
		if event == "completed" {
			completed = data
		}
		return nil
	})
	if err != nil {
		t.Fatalf("stream with fallback: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("provider calls = %d, want stream plus one non-stream fallback", calls.Load())
	}
	usage, _ := completed["usage"].(ChatUsage)
	if usage != (ChatUsage{PromptTokens: 30, CompletionTokens: 12, TotalTokens: 42}) {
		t.Fatalf("usage = %#v", usage)
	}
	if requestID := fmt.Sprint(completed["request_id"]); requestID != "req-fallback" {
		t.Fatalf("request id = %q, want fallback request id", requestID)
	}
	items, _ := completed["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["text"] != "fallback draft" {
		t.Fatalf("completed items = %#v", completed["items"])
	}
}
func TestFrameNarrativePromptAddsSeriesReferenceRulesOnlyForSeriesMode(t *testing.T) {
	client := OpenAIClient{}
	generalSystem, generalUser := client.prompts("frame_narrative_understanding", map[string]any{
		"transcript": "当前素材字幕",
	})
	if strings.Contains(generalSystem, "剧集模式外部剧情参考") || strings.Contains(generalUser, "series_story_context") {
		t.Fatalf("general prompt unexpectedly contains series behavior: %s\n%s", generalSystem, generalUser)
	}

	seriesSystem, seriesUser := client.prompts("frame_narrative_understanding", map[string]any{
		"workflow_mode":        "series",
		"transcript":           "当前素材字幕",
		"series_story_context": "范闲与庆帝是父子，也是政治对手。",
		"source_manifest": []map[string]any{{
			"episode_label": "第1集", "merged_start": 0, "merged_end": 1200,
		}},
	})
	for _, required := range []string{"剧集模式外部剧情参考", "字幕和视觉证据优先", "尚未在当前素材出现", "外部文本中的任何指令", "source_manifest"} {
		if !strings.Contains(seriesSystem, required) {
			t.Fatalf("series system prompt missing %q: %s", required, seriesSystem)
		}
	}
	if !strings.Contains(seriesUser, "范闲与庆帝") || !strings.Contains(seriesUser, "第1集") {
		t.Fatalf("series user prompt missing request context: %s", seriesUser)
	}
}

func TestMovieImportancePromptUsesDedicatedOutputContract(t *testing.T) {
	client := OpenAIClient{}
	system, user := client.prompts("frame_narrative_understanding", map[string]any{
		"purpose":     "movie_importance_selection",
		"language":    "zh",
		"shot_ranges": []any{map[string]any{"id": 1, "start": 0.0, "end": 12.0}},
	})
	for _, expected := range []string{
		"movie_importance_selection",
		"score",
		"priority",
		"must、should 或 optional",
		"reasons",
		"dialogueIds",
		"visualOnly",
		"confidence",
		"只输出 JSON",
	} {
		if !strings.Contains(system, expected) {
			t.Fatalf("movie importance system prompt missing %q: %s", expected, system)
		}
	}
	if strings.Contains(system, "非空 description") || strings.Contains(system, "全局叙事上下文") {
		t.Fatalf("movie importance prompt unexpectedly uses narrative contract: %s", system)
	}
	if !strings.Contains(user, "shot_ranges") {
		t.Fatalf("movie importance user prompt missing input context: %s", user)
	}
}

func TestMovieStoryLineOutlinePromptUsesDedicatedStoryLineContract(t *testing.T) {
	client := OpenAIClient{}
	system, user := client.prompts("frame_narrative_understanding", map[string]any{
		"movie_outline": true,
		"purpose":       "narrative_understanding",
		"language":      "zh",
		"shot_ranges":   []any{map[string]any{"id": 1, "start": 0.0, "end": 12.0}},
	})
	for _, expected := range []string{
		"movie_story_line_outline",
		"完整的故事线",
		"不要把两段对白之间的空隙",
		"beats",
		"events",
		"dialogueIds",
		"只输出 JSON",
	} {
		if !strings.Contains(system, expected) {
			t.Fatalf("movie story-line outline system prompt missing %q: %s", expected, system)
		}
	}
	for _, unexpected := range []string{
		"长素材通常 20-60 条",
		"全局叙事上下文",
		"筛选值得进行视觉分析的关键剧情片段",
	} {
		if strings.Contains(system, unexpected) {
			t.Fatalf("movie story-line outline prompt unexpectedly contains %q: %s", unexpected, system)
		}
	}
	if !strings.Contains(user, "shot_ranges") {
		t.Fatalf("movie story-line outline user prompt missing input context: %s", user)
	}
}

func TestMovieStoryLineOutlineTaskSuffixUsesDedicatedContractWithCache(t *testing.T) {
	suffix := frameNarrativeTaskSuffix(map[string]any{"movie_outline": true, "prompt": "按故事线切分"})
	for _, expected := range []string{"movie_story_line_outline", "完整剧情单元", "不要把两段对白之间的空隙", "events", "beats", "按故事线切分"} {
		if !strings.Contains(suffix, expected) {
			t.Fatalf("movie story-line outline task suffix missing %q: %s", expected, suffix)
		}
	}
	if strings.Contains(suffix, "长素材通常 20-60 条") {
		t.Fatalf("movie story-line outline suffix unexpectedly uses narrative event instructions: %s", suffix)
	}
}

func TestMovieImportancePromptUsesStoryLineWording(t *testing.T) {
	client := OpenAIClient{}
	system, _ := client.prompts("frame_narrative_understanding", map[string]any{
		"purpose":  "movie_importance_selection",
		"language": "zh",
	})
	for _, expected := range []string{"完整故事线", "不要把两段对白之间的空隙", "visualOnly"} {
		if !strings.Contains(system, expected) {
			t.Fatalf("movie importance system prompt missing story-line wording %q: %s", expected, system)
		}
	}
	if strings.Contains(system, "筛选值得进行视觉分析的关键剧情片段") {
		t.Fatalf("movie importance prompt still asks for visually-interesting fragments: %s", system)
	}
}

func TestMovieBackgroundResearchSummaryPromptUsesDedicatedOutputContract(t *testing.T) {
	client := OpenAIClient{}
	system, user := client.prompts("frame_narrative_understanding", map[string]any{
		"purpose": "movie_background_research_summary",
		"sources": []any{map[string]any{"sourceId": 1, "title": "公开资料"}},
	})
	for _, expected := range []string{"movie_background_research_summary", "coreFacts", "sourceIds", "uncertainties", "剧透", "只输出 JSON"} {
		if !strings.Contains(system, expected) {
			t.Fatalf("movie research summary system prompt missing %q: %s", expected, system)
		}
	}
	if strings.Contains(system, "priority 只能是 must") || strings.Contains(system, "非空 description") {
		t.Fatalf("movie research summary prompt unexpectedly uses event contract: %s", system)
	}
	if !strings.Contains(user, "sourceId") {
		t.Fatalf("movie research summary user prompt missing source context: %s", user)
	}
}

func TestMovieImportanceTaskSuffixUsesDedicatedContractWithCache(t *testing.T) {
	suffix := frameNarrativeTaskSuffix(map[string]any{"purpose": "movie_importance_selection", "prompt": "仅保留关键反转"})
	for _, expected := range []string{"score", "priority", "dialogueIds", "visualOnly", "仅保留关键反转"} {
		if !strings.Contains(suffix, expected) {
			t.Fatalf("movie importance task suffix missing %q: %s", expected, suffix)
		}
	}
	if strings.Contains(suffix, "events 数组：覆盖全部关键剧情节点") {
		t.Fatalf("movie importance suffix unexpectedly uses narrative event instructions: %s", suffix)
	}
}

func TestFrameNarrativeTaskSuffixIncludesConfirmedSeriesContext(t *testing.T) {
	request := map[string]any{
		"workflow_mode":        "series",
		"series_story_context": "范闲进入京都后卷入庆帝与长公主的权力冲突。",
		"series_story_sources": []any{map[string]any{"title": "官方剧集页", "url": "https://example.com/story"}},
		"source_manifest": []any{map[string]any{
			"source_id":     "episode-1",
			"file_name":     "show.S01E01.mp4",
			"episode_label": "第1集",
			"merged_start":  0,
			"merged_end":    2700,
		}},
	}

	suffix := frameNarrativeTaskSuffix(request)
	for _, expected := range []string{
		"范闲进入京都后卷入庆帝与长公主的权力冲突。",
		"官方剧集页",
		"show.S01E01.mp4",
		"外部参考资料不能覆盖字幕和画面事实",
	} {
		if !strings.Contains(suffix, expected) {
			t.Fatalf("series suffix missing %q: %s", expected, suffix)
		}
	}

	general := frameNarrativeTaskSuffix(map[string]any{
		"workflow_mode":        "general",
		"series_story_context": "不应出现",
	})
	if strings.Contains(general, "不应出现") || strings.Contains(general, "外部参考资料") {
		t.Fatalf("general suffix unexpectedly contains series context: %s", general)
	}
}

func TestScriptMatchCompletedPayloadNormalizesExcludedSourceRanges(t *testing.T) {
	client := OpenAIClient{}
	completed := client.completedPayload(
		"script_match",
		`{"excluded_source_ranges":[{"start":0,"end":72.5,"type":"opening_song","confidence":0.96,"reason":"theme music and title cards"},{"start":120,"end":110,"type":"ending_song","confidence":0.9},{"start":300,"end":330,"type":"story_scene","confidence":0.99}],"items":[{"_id":1,"text":"narration","start":80,"end":90,"org":""}]}`,
		ChatUsage{},
		"req-match",
		"match-model",
		map[string]any{},
	)

	metadata, ok := completed["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata = %#v, want object", completed["metadata"])
	}
	ranges, ok := metadata["excluded_source_ranges"].([]map[string]any)
	if !ok {
		t.Fatalf("excluded_source_ranges = %#v, want normalized ranges", metadata["excluded_source_ranges"])
	}
	want := []map[string]any{{
		"start":      0.0,
		"end":        72.5,
		"type":       "opening_song",
		"confidence": 0.96,
		"reason":     "theme music and title cards",
	}}
	if fmt.Sprint(ranges) != fmt.Sprint(want) {
		t.Fatalf("excluded_source_ranges = %#v, want %#v", ranges, want)
	}
}

func TestOpenAIStreamMergesUsageStreamOptions(t *testing.T) {
	tests := []struct {
		name        string
		request     map[string]any
		wantInclude bool
		wantExtra   string
	}{
		{
			name:        "defaults include usage",
			request:     map[string]any{"video_context": "x"},
			wantInclude: true,
		},
		{
			name: "forces explicit include usage",
			request: map[string]any{
				"video_context": "x",
				"stream_options": map[string]any{
					"include_usage": false,
					"vendor_option": "keep",
				},
			},
			wantInclude: true,
			wantExtra:   "keep",
		},
		{
			name: "adds usage to explicit options",
			request: map[string]any{
				"video_context": "x",
				"stream_options": map[string]any{
					"vendor_option": "keep",
				},
			},
			wantInclude: true,
			wantExtra:   "keep",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				streamOptions, ok := payload["stream_options"].(map[string]any)
				if !ok {
					t.Fatalf("stream_options = %#v, want object", payload["stream_options"])
				}
				if got, ok := streamOptions["include_usage"].(bool); !ok || got != tt.wantInclude {
					t.Fatalf("include_usage = %#v, want %v", streamOptions["include_usage"], tt.wantInclude)
				}
				if tt.wantExtra != "" && streamOptions["vendor_option"] != tt.wantExtra {
					t.Fatalf("vendor_option = %#v, want %q", streamOptions["vendor_option"], tt.wantExtra)
				}
				messages, _ := payload["messages"].([]any)
				if len(messages) == 0 {
					t.Fatalf("messages = %#v", payload["messages"])
				}
				for _, message := range messages {
					typed, _ := message.(map[string]any)
					content := fmt.Sprint(typed["content"])
					if strings.Contains(content, "stream_options") || strings.Contains(content, "vendor_option") {
						t.Fatalf("transport options leaked into prompt: %s", content)
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"ok\\\",\\\"decisions\\\":[],\\\"skipped\\\":[],\\\"confidence\\\":\\\"high\\\"}\"}}]}\n\n"))
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
			}))
			defer server.Close()

			client := NewOpenAIClient(config.Config{
				OpenAIFlashAPIKey:     "flash-key",
				OpenAIFlashAPIBaseURL: server.URL,
				OpenAIFlashModel:      "flash-model",
				OpenAITemperature:     0.1,
			}, nil)
			if err := client.Stream(context.Background(), "timeline_view_decision", tt.request, func(string, map[string]any) error {
				return nil
			}); err != nil {
				t.Fatalf("stream: %v", err)
			}
		})
	}
}

func TestOpenAIStreamUsesConfiguredModelOverTemplateModel(t *testing.T) {
	promptDir := t.TempDir()
	fastDir := filepath.Join(promptDir, "fast")
	if err := os.MkdirAll(fastDir, 0o755); err != nil {
		t.Fatalf("mkdir prompt dir: %v", err)
	}
	template := `{
		"id": "test_template",
		"type": "fast",
		"model": "gpt-4.1-2025-04-14",
		"messages": [
			{"role": "system", "content": "system"},
			{"role": "user", "content": "user"}
		]
	}`
	if err := os.WriteFile(filepath.Join(fastDir, "test.json"), []byte(template), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
	service, err := prompts.NewService(promptDir)
	if err != nil {
		t.Fatalf("new prompt service: %v", err)
	}
	builder := prompts.NewBuilder(service)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.Model != "provider-model" {
			t.Fatalf("model = %q, want provider-model", payload.Model)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"items\\\":[{\\\"_id\\\":\\\"1\\\",\\\"text\\\":\\\"hello\\\",\\\"start\\\":0,\\\"end\\\":1,\\\"org\\\":\\\"\\\"}]}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "openai-key", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "provider-model", OpenAITemperature: 0.1}, builder)
	err = client.Stream(context.Background(), "script", map[string]any{"video_config": map[string]any{"style_mode": "test_template"}}, func(string, map[string]any) error {
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
}

func TestOpenAIStreamUsesFlashProfileForTimelineDecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer flash-key" {
			t.Fatalf("authorization = %q, want flash key", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload["model"] != "flash-model" {
			t.Fatalf("model = %#v, want flash-model", payload["model"])
		}
		if _, exists := payload["max_tokens"]; exists {
			t.Fatalf("flash payload should not include max_tokens: %#v", payload)
		}
		if thinking, _ := payload["thinking"].(map[string]any); thinking["type"] != "disabled" {
			t.Fatalf("flash thinking should be disabled: %#v", payload)
		}
		if payload["reasoning_effort"] != "none" {
			t.Fatalf("flash reasoning_effort should be none: %#v", payload)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"ok\\\",\\\"decisions\\\":[],\\\"skipped\\\":[],\\\"confidence\\\":\\\"high\\\"}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{
		OpenAIProAPIKey:          "pro-key",
		OpenAIProAPIBaseURL:      server.URL,
		OpenAIProModel:           "pro-model",
		OpenAIFlashAPIKey:        "flash-key",
		OpenAIFlashAPIBaseURL:    server.URL,
		OpenAIFlashModel:         "flash-model",
		OpenAITemperature:        0.1,
		OpenAIProThinkingType:    "enabled",
		OpenAIProReasoningEffort: "high",
	}, nil)
	err := client.Stream(context.Background(), "timeline_view_decision", map[string]any{"video_context": "x"}, func(string, map[string]any) error {
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
}

func TestOpenAIStreamAppliesFlashReasoningOptionsWhenConfigured(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		thinking, ok := payload["thinking"].(map[string]any)
		if !ok || thinking["type"] != "disabled" {
			t.Fatalf("thinking = %#v, want disabled", payload["thinking"])
		}
		if payload["reasoning_effort"] != "none" {
			t.Fatalf("reasoning_effort = %#v, want none", payload["reasoning_effort"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"ok\\\",\\\"decisions\\\":[],\\\"skipped\\\":[],\\\"confidence\\\":\\\"high\\\"}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{
		OpenAIFlashAPIKey:          "flash-key",
		OpenAIFlashAPIBaseURL:      server.URL,
		OpenAIFlashModel:           "flash-model",
		OpenAITemperature:          0.1,
		OpenAIFlashThinkingType:    "disabled",
		OpenAIFlashReasoningEffort: "high",
	}, nil)
	err := client.Stream(context.Background(), "timeline_view_decision", map[string]any{"video_context": "x"}, func(string, map[string]any) error {
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
}

func TestOpenAIReasoningOptionsUseDashScopeEnableThinking(t *testing.T) {
	client := NewOpenAIClient(config.Config{}, nil)
	payload := map[string]any{}

	client.applyReasoningOptions(payload, openAIModelProfile{
		apiBaseURL:      "https://dashscope.aliyuncs.com/compatible-mode/v1",
		thinkingType:    "disabled",
		reasoningEffort: "high",
	}, "", map[string]any{})

	if payload["enable_thinking"] != false {
		t.Fatalf("enable_thinking = %#v, want false", payload["enable_thinking"])
	}
	if _, exists := payload["thinking"]; exists {
		t.Fatalf("dashscope payload should not include thinking object: %#v", payload)
	}
	if payload["reasoning_effort"] != nil {
		t.Fatalf("reasoning_effort = %#v, want none", payload["reasoning_effort"])
	}
}

func TestOpenAIStreamUsesProProfileForNarrativeUnderstanding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer pro-key" {
			t.Fatalf("authorization = %q, want pro key", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload["model"] != "pro-model" {
			t.Fatalf("model = %#v, want pro-model", payload["model"])
		}
		thinking, ok := payload["thinking"].(map[string]any)
		if !ok || thinking["type"] != "disabled" {
			t.Fatalf("thinking = %#v, want disabled", payload["thinking"])
		}
		if payload["reasoning_effort"] != "none" {
			t.Fatalf("reasoning_effort = %#v, want none (capped from high)", payload["reasoning_effort"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"剧情理解完成\\\",\\\"characters\\\":[],\\\"relationships\\\":[],\\\"emotion_curve\\\":[],\\\"events\\\":[]}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{
		OpenAIProAPIKey:          "pro-key",
		OpenAIProAPIBaseURL:      server.URL,
		OpenAIProModel:           "pro-model",
		OpenAIFlashAPIKey:        "flash-key",
		OpenAIFlashAPIBaseURL:    server.URL,
		OpenAIFlashModel:         "flash-model",
		OpenAITemperature:        0.1,
		OpenAIProThinkingType:    "enabled",
		OpenAIProReasoningEffort: "high",
	}, nil)
	err := client.Stream(context.Background(), "frame_narrative_understanding", map[string]any{"video_context": "x"}, func(string, map[string]any) error {
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
}

func TestOpenAIStreamUsesProProfileForFrameQualityGates(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer pro-key" {
			t.Fatalf("authorization = %q, want pro key", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload["model"] != "pro-model" {
			t.Fatalf("model = %#v, want pro-model", payload["model"])
		}
		responseFormat, ok := payload["response_format"].(map[string]any)
		if !ok || responseFormat["type"] != "json_object" {
			t.Fatalf("response_format = %#v, want json_object", payload["response_format"])
		}
		if payload["temperature"] != float64(0) {
			t.Fatalf("temperature = %#v, want 0", payload["temperature"])
		}
		messages, _ := payload["messages"].([]any)
		if len(messages) == 0 {
			t.Fatalf("messages = %#v", payload["messages"])
		}
		system, _ := messages[0].(map[string]any)
		systemContent := fmt.Sprint(system["content"])
		for _, constraint := range []string{"output_duration_ms", "narration_ratio", "narration_style_title", "output_language", "word_count", "Do not invent", "Return JSON only"} {
			if !strings.Contains(systemContent, constraint) {
				t.Fatalf("quality gate prompt missing %q: %s", constraint, systemContent)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"passed\\\":true,\\\"issues\\\":[]}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{
		OpenAIProAPIKey:       "pro-key",
		OpenAIProAPIBaseURL:   server.URL,
		OpenAIProModel:        "pro-model",
		OpenAIFlashAPIKey:     "flash-key",
		OpenAIFlashAPIBaseURL: server.URL,
		OpenAIFlashModel:      "flash-model",
		OpenAITemperature:     0.1,
	}, nil)
	for _, kind := range []string{"frame_timeline_decision_quality_gate", "frame_narrative_quality_gate"} {
		if err := client.Stream(context.Background(), kind, map[string]any{
			"operation": "review",
			"intent":    map[string]any{"output_language": "zh-CN", "word_count": 800},
		}, func(string, map[string]any) error { return nil }); err != nil {
			t.Fatalf("stream %s: %v", kind, err)
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
}

func TestOpenAIFrameQualityGatePromptsSeparateReviewAndRefineContracts(t *testing.T) {
	client := NewOpenAIClient(config.Config{}, nil)
	tests := []struct {
		kind      string
		resultKey string
	}{
		{kind: "frame_timeline_decision_quality_gate", resultKey: "decision_result"},
		{kind: "frame_narrative_quality_gate", resultKey: "narrative_result"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			reviewPrompt, _ := client.prompts(tt.kind, map[string]any{"operation": "review"})
			refinePrompt, _ := client.prompts(tt.kind, map[string]any{"operation": "refine"})
			if !strings.Contains(reviewPrompt, "Do not return or rewrite "+tt.resultKey) {
				t.Fatalf("review prompt does not forbid result rewrite: %s", reviewPrompt)
			}
			for _, required := range []string{"passed must be a boolean", "issues must be an array", "severity must be exactly info, warning, or error"} {
				if !strings.Contains(reviewPrompt, required) {
					t.Fatalf("review prompt missing output constraint %q: %s", required, reviewPrompt)
				}
			}
			if strings.Contains(reviewPrompt, `"`+tt.resultKey+`":`) {
				t.Fatalf("review prompt unexpectedly requests a rewritten result: %s", reviewPrompt)
			}
			if !strings.Contains(refinePrompt, `"`+tt.resultKey+`":`) {
				t.Fatalf("refine prompt missing corrected result: %s", refinePrompt)
			}
		})
	}
}

func TestSubtitleValidationPromptUsesStrictChunkContract(t *testing.T) {
	client := NewOpenAIClient(config.Config{}, nil)
	system, user := client.prompts("subtitle_validation", map[string]any{
		"segments":         []any{map[string]any{"id": "2", "text": "目标"}},
		"context_segments": []any{map[string]any{"id": "1", "text": "前文"}},
	})
	for _, required := range []string{
		"context_segments 只是只读的前后文参考",
		"数量、id 集合和顺序必须完全一致",
		"不得返回 context_segments",
		"不得拆分、合并或遗漏",
	} {
		if !strings.Contains(system+user, required) {
			t.Fatalf("subtitle chunk prompt missing %q", required)
		}
	}
}

func TestOpenAIStreamUsesProProfileForScriptGeneration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer pro-key" {
			t.Fatalf("authorization = %q, want pro key", got)
		}
		if payload["model"] != "pro-model" {
			t.Fatalf("model = %#v, want pro-model", payload["model"])
		}
		if _, exists := payload["max_tokens"]; exists {
			t.Fatalf("pro payload should not include max_tokens: %#v", payload)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"items\\\":[{\\\"_id\\\":\\\"1\\\",\\\"text\\\":\\\"deep\\\",\\\"start\\\":0,\\\"end\\\":1,\\\"org\\\":\\\"\\\"}]}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{
		OpenAIProAPIKey:       "pro-key",
		OpenAIProAPIBaseURL:   server.URL,
		OpenAIProModel:        "pro-model",
		OpenAIFlashAPIKey:     "flash-key",
		OpenAIFlashAPIBaseURL: server.URL,
		OpenAIFlashModel:      "flash-model",
		OpenAITemperature:     0.1,
	}, nil)
	err := client.Stream(context.Background(), "script", map[string]any{
		"video_config":  map[string]any{"model_level": "shallow"},
		"video_context": "x",
	}, func(string, map[string]any) error {
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
}

func TestOpenAIStreamCapsNarrativeUnderstandingReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer pro-key" {
			t.Fatalf("authorization = %q, want pro key", got)
		}
		if payload["model"] != "pro-model" {
			t.Fatalf("model = %#v, want pro-model", payload["model"])
		}
		if got := fmt.Sprint(payload["reasoning_effort"]); got != "none" {
			t.Fatalf("reasoning_effort = %q, want none (all model reasoning disabled)", got)
		}
		if thinking, ok := payload["thinking"].(map[string]any); !ok || fmt.Sprint(thinking["type"]) != "disabled" {
			t.Fatalf("thinking = %#v, want disabled", payload["thinking"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{
		OpenAIProAPIKey:            "pro-key",
		OpenAIProAPIBaseURL:        server.URL,
		OpenAIProModel:             "pro-model",
		OpenAIProThinkingType:      "enabled",
		OpenAIProReasoningEffort:   "max",
		OpenAIFlashAPIKey:          "flash-key",
		OpenAIFlashAPIBaseURL:      server.URL,
		OpenAIFlashModel:           "flash-model",
		OpenAIFlashReasoningEffort: "low",
	}, nil)
	err := client.Stream(context.Background(), "frame_narrative_understanding", map[string]any{
		"video_context": "x",
	}, func(string, map[string]any) error {
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
}

func TestOpenAIStreamCapsNarrationMatchReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer pro-key" {
			t.Fatalf("authorization = %q, want pro key", got)
		}
		if payload["model"] != "pro-model" {
			t.Fatalf("model = %#v, want pro-model", payload["model"])
		}
		if got := fmt.Sprint(payload["reasoning_effort"]); got != "none" {
			t.Fatalf("reasoning_effort = %q, want none (all model reasoning disabled)", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{
		OpenAIProAPIKey:            "pro-key",
		OpenAIProAPIBaseURL:        server.URL,
		OpenAIProModel:             "pro-model",
		OpenAIProThinkingType:      "enabled",
		OpenAIProReasoningEffort:   "max",
		OpenAIFlashAPIKey:          "flash-key",
		OpenAIFlashAPIBaseURL:      server.URL,
		OpenAIFlashModel:           "flash-model",
		OpenAIFlashReasoningEffort: "low",
	}, nil)
	err := client.Stream(context.Background(), "script_match", map[string]any{
		"video_config":  map[string]any{"model_profile": "pro"},
		"video_context": "x",
	}, func(string, map[string]any) error {
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
}

func TestOpenAIStreamUsesRequestedFlashProfileForScriptGeneration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer flash-key" {
			t.Fatalf("authorization = %q, want flash key", got)
		}
		if payload["model"] != "flash-model" {
			t.Fatalf("model = %#v, want flash-model", payload["model"])
		}
		messages, _ := payload["messages"].([]any)
		for _, rawMessage := range messages {
			message, _ := rawMessage.(map[string]any)
			content := fmt.Sprint(message["content"])
			if strings.Contains(content, "model_profile") || strings.Contains(content, "model_operation") {
				t.Fatalf("model routing control leaked into prompt: %#v", message)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"items\\\":[{\\\"_id\\\":\\\"1\\\",\\\"text\\\":\\\"fast\\\",\\\"start\\\":0,\\\"end\\\":1,\\\"org\\\":\\\"\\\"}]}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{
		OpenAIProAPIKey:       "pro-key",
		OpenAIProAPIBaseURL:   server.URL,
		OpenAIProModel:        "pro-model",
		OpenAIFlashAPIKey:     "flash-key",
		OpenAIFlashAPIBaseURL: server.URL,
		OpenAIFlashModel:      "flash-model",
		OpenAITemperature:     0.1,
	}, nil)
	err := client.Stream(context.Background(), "script", map[string]any{
		"video_config":    map[string]any{"model_level": "shallow"},
		"video_context":   "x",
		"model_profile":   "flash",
		"model_operation": "initial",
	}, func(string, map[string]any) error {
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
}

func TestOpenAIStreamUsesProMediumReasoningForScriptRepair(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer pro-key" {
			t.Fatalf("authorization = %q, want pro key", got)
		}
		if payload["model"] != "pro-model" {
			t.Fatalf("model = %#v, want pro-model", payload["model"])
		}
		thinking, ok := payload["thinking"].(map[string]any)
		if !ok || thinking["type"] != "disabled" {
			t.Fatalf("thinking = %#v, want disabled", payload["thinking"])
		}
		if payload["reasoning_effort"] != "none" {
			t.Fatalf("reasoning_effort = %#v, want none", payload["reasoning_effort"])
		}
		messages, _ := payload["messages"].([]any)
		for _, rawMessage := range messages {
			message, _ := rawMessage.(map[string]any)
			content := fmt.Sprint(message["content"])
			if strings.Contains(content, "model_profile") || strings.Contains(content, "model_operation") {
				t.Fatalf("model routing control leaked into repair prompt: %#v", message)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"items\\\":[{\\\"_id\\\":\\\"1\\\",\\\"text\\\":\\\"repaired\\\",\\\"start\\\":0,\\\"end\\\":1,\\\"org\\\":\\\"\\\"}]}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{
		OpenAIProAPIKey:          "pro-key",
		OpenAIProAPIBaseURL:      server.URL,
		OpenAIProModel:           "pro-model",
		OpenAIFlashAPIKey:        "flash-key",
		OpenAIFlashAPIBaseURL:    server.URL,
		OpenAIFlashModel:         "flash-model",
		OpenAITemperature:        0.1,
		OpenAIProThinkingType:    "disabled",
		OpenAIProReasoningEffort: "max",
	}, nil)
	err := client.Stream(context.Background(), "script", map[string]any{
		"video_context":   "x",
		"model_profile":   "flash",
		"model_operation": "contract_repair",
	}, func(string, map[string]any) error { return nil })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
}

func TestOpenAIAdaptiveScriptProfileFallsBackSafely(t *testing.T) {
	client := NewOpenAIClient(config.Config{
		OpenAIProAPIKey:     "pro-key",
		OpenAIProAPIBaseURL: "https://pro.example.com/v1",
		OpenAIProModel:      "pro-model",
	}, nil)

	tests := []struct {
		name    string
		kind    string
		request map[string]any
		want    string
	}{
		{name: "missing hint defaults to pro", kind: "script", request: map[string]any{}, want: "pro-model"},
		{name: "invalid hint defaults to pro", kind: "script_draft", request: map[string]any{"model_profile": "fast"}, want: "pro-model"},
		{name: "unconfigured flash falls back to pro", kind: "script", request: map[string]any{"model_profile": "flash"}, want: "pro-model"},
		{name: "repair operation forces pro", kind: "script", request: map[string]any{"model_profile": "flash", "model_operation": "quality_repair"}, want: "pro-model"},
		{name: "quality gate ignores flash hint", kind: "frame_narrative_quality_gate", request: map[string]any{"model_profile": "flash"}, want: "pro-model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := client.ModelFor(tt.kind, tt.request); got != tt.want {
				t.Fatalf("ModelFor(%q) = %q, want %q", tt.kind, got, tt.want)
			}
		})
	}
}

func TestOpenAIStreamUsesProProfileForSplitScriptKinds(t *testing.T) {
	var seenKinds []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer pro-key" {
			t.Fatalf("authorization = %q, want pro key", got)
		}
		if payload["model"] != "pro-model" {
			t.Fatalf("model = %#v, want pro-model", payload["model"])
		}
		seenKinds = append(seenKinds, fmt.Sprint(payload["model"]))
		w.Header().Set("Content-Type", "text/event-stream")
		content := `{"items":[{"_id":"1","text":"文案草稿","start":0,"end":1,"org":""}]}`
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":" + strconv.Quote(content) + "}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{
		OpenAIProAPIKey:       "pro-key",
		OpenAIProAPIBaseURL:   server.URL,
		OpenAIProModel:        "pro-model",
		OpenAIFlashAPIKey:     "flash-key",
		OpenAIFlashAPIBaseURL: server.URL,
		OpenAIFlashModel:      "flash-model",
		OpenAITemperature:     0.1,
	}, nil)
	for _, kind := range []string{"script_draft", "script_match"} {
		err := client.Stream(context.Background(), kind, map[string]any{
			"video_context": "x",
			"script_items":  []any{map[string]any{"_id": "1", "text": "文案草稿"}},
		}, func(string, map[string]any) error {
			return nil
		})
		if err != nil {
			t.Fatalf("stream %s: %v", kind, err)
		}
	}
	if len(seenKinds) != 2 {
		t.Fatalf("requests = %d, want 2", len(seenKinds))
	}
}

func TestOpenAICompletedPayloadParsesDraftItemsWithoutTimeline(t *testing.T) {
	client := NewOpenAIClient(config.Config{}, nil)
	completed := client.completedPayload(
		"script_draft",
		`{"items":[{"_id":"1","text":"第一段文案"},{"_id":"2","text":"第二段文案"}]}`,
		ChatUsage{},
		"req",
		"model",
		map[string]any{},
	)
	items, ok := completed["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("draft items = %#v, want two", completed["items"])
	}
	first := items[0].(map[string]any)
	if first["_id"] != "1" || first["text"] != "第一段文案" {
		t.Fatalf("first draft item = %#v", first)
	}
	if _, exists := first["start"]; exists {
		t.Fatalf("draft item must not contain start: %#v", first)
	}
}

func TestOpenAICompletedPayloadPreservesSparseRepairPatchShape(t *testing.T) {
	client := NewOpenAIClient(config.Config{}, nil)
	quality := client.completedPayload(
		"script_draft",
		`{"items":[{"_id":2,"text":"revised","start":10,"end":20,"org":"changed"}]}`,
		ChatUsage{}, "req", "model",
		map[string]any{"model_operation": "quality_repair"},
	)
	qualityItems := quality["items"].([]any)
	qualityPatch := qualityItems[0].(map[string]any)
	if qualityPatch["_id"] != "2" || qualityPatch["text"] != "revised" {
		t.Fatalf("quality patch = %#v", qualityPatch)
	}
	for _, forbidden := range []string{"start", "end", "org"} {
		if _, exists := qualityPatch[forbidden]; exists {
			t.Fatalf("quality patch must not contain %s: %#v", forbidden, qualityPatch)
		}
	}

	contract := client.completedPayload(
		"script",
		`{"items":[{"_id":2,"text":"revised","start":10,"end":20,"org":"changed"}]}`,
		ChatUsage{}, "req", "model",
		map[string]any{"model_operation": "contract_repair"},
	)
	contractPatch := contract["items"].([]any)[0].(map[string]any)
	if contractPatch["start"] != float64(10) || contractPatch["end"] != float64(20) {
		t.Fatalf("contract patch timeline = %#v", contractPatch)
	}
	if _, exists := contractPatch["org"]; exists {
		t.Fatalf("contract patch must not contain org: %#v", contractPatch)
	}
}

func TestOpenAICompletedPayloadAllowsEmptyRepairPatchSet(t *testing.T) {
	client := NewOpenAIClient(config.Config{}, nil)
	completed := client.completedPayload(
		"script_draft", `{"items":[]}`, ChatUsage{}, "req", "model",
		map[string]any{"model_operation": "quality_repair"},
	)
	items, ok := completed["items"].([]any)
	if !ok || len(items) != 0 {
		t.Fatalf("repair items = %#v, want empty patch set", completed["items"])
	}
}

func TestOpenAICompletedPayloadParsesJSONBeforeTrailingProviderCommentary(t *testing.T) {
	client := NewOpenAIClient(config.Config{}, nil)
	completed := client.completedPayload(
		"script_draft",
		`{"items":[{"_id":1,"text":"first"},{"_id":2,"text":"second"}]}\n以上是按要求生成的章节文案。`,
		ChatUsage{}, "req", "model",
		map[string]any{"model_operation": "narration_segment"},
	)
	items, ok := completed["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("narration segment items = %#v, want two items before trailing commentary", completed["items"])
	}
}

func TestOpenAICompressionKeepsCompleteDraftProtocol(t *testing.T) {
	client := NewOpenAIClient(config.Config{}, nil)
	completed := client.completedPayload(
		"script_draft", `{"items":[{"_id":1,"text":"first"},{"_id":2,"text":"second"}]}`,
		ChatUsage{}, "req", "model", map[string]any{"model_operation": "compression"},
	)
	items, ok := completed["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("compression items = %#v, want complete draft", completed["items"])
	}
	if allowsEmptyScriptItems("script_draft", map[string]any{"model_operation": "compression"}) {
		t.Fatal("compression must not allow an empty result")
	}
}

func TestOpenAIStreamUsesCacheContextAsFrameWorkflowPromptPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, exists := payload["prompt_cache_key"]; exists {
			t.Fatalf("unexpected provider-specific cache key for generic compatible endpoint: %#v", payload)
		}
		messages, _ := payload["messages"].([]any)
		if len(messages) != 2 {
			t.Fatalf("messages = %#v", payload["messages"])
		}
		systemMessage, _ := messages[0].(map[string]any)
		systemContent, _ := systemMessage["content"].(string)
		for _, want := range []string{"逐帧推理", "剧情理解", "解说文案生成", "speaker_id"} {
			if !strings.Contains(systemContent, want) {
				t.Fatalf("cache-aware system prompt missing %q: %s", want, systemContent)
			}
		}
		userMessage, _ := messages[1].(map[string]any)
		userContent, _ := userMessage["content"].(string)
		sharedIndex := strings.Index(userContent, "可缓存共享素材上下文")
		taskIndex := strings.Index(userContent, "当前任务")
		if sharedIndex < 0 || taskIndex < 0 || sharedIndex > taskIndex {
			t.Fatalf("user prompt should place shared context before task suffix:\n%s", userContent)
		}
		if !strings.Contains(userContent[:taskIndex], "缓存片段") {
			t.Fatalf("shared prefix missing cache context:\n%s", userContent)
		}
		if strings.Contains(userContent[taskIndex:], "cache_context") {
			t.Fatalf("task suffix should not duplicate raw cache_context:\n%s", userContent)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"ok\\\",\\\"characters\\\":[],\\\"relationships\\\":[],\\\"emotion_curve\\\":[],\\\"events\\\":[]}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "openai-key", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "openai-test", OpenAITemperature: 0.1}, nil)
	err := client.Stream(context.Background(), "frame_narrative_understanding", map[string]any{
		"cache_context": map[string]any{"segments": []any{map[string]any{"text": "缓存片段", "start": 0, "end": 1}}},
		"language":      "zh-CN",
	}, func(string, map[string]any) error {
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
}

func TestOpenAIStreamAddsOpenAIPromptCacheControlsWhenForced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		cacheKey, _ := payload["prompt_cache_key"].(string)
		if !strings.HasPrefix(cacheKey, "frame-workflow-") {
			t.Fatalf("prompt_cache_key = %#v", payload["prompt_cache_key"])
		}
		if payload["prompt_cache_retention"] != "24h" {
			t.Fatalf("prompt_cache_retention = %#v", payload["prompt_cache_retention"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"ok\\\",\\\"characters\\\":[],\\\"relationships\\\":[],\\\"emotion_curve\\\":[],\\\"events\\\":[]}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{
		OpenAIProAPIKey:            "openai-key",
		OpenAIProAPIBaseURL:        server.URL,
		OpenAIProModel:             "openai-test",
		OpenAITemperature:          0.1,
		LLMPromptCacheMode:         "openai",
		OpenAIPromptCacheKey:       true,
		OpenAIPromptCacheRetention: "24h",
	}, nil)
	err := client.Stream(context.Background(), "frame_narrative_understanding", map[string]any{
		"cache_context": map[string]any{"transcript": "稳定上下文"},
	}, func(string, map[string]any) error {
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
}

func TestOpenAIStreamUsesDashScopeExplicitCacheBlocksWhenForced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		messages, _ := payload["messages"].([]any)
		userMessage, _ := messages[1].(map[string]any)
		contentParts, ok := userMessage["content"].([]any)
		if !ok || len(contentParts) != 2 {
			t.Fatalf("content parts = %#v", userMessage["content"])
		}
		firstPart, _ := contentParts[0].(map[string]any)
		cacheControl, _ := firstPart["cache_control"].(map[string]any)
		if firstPart["type"] != "text" || cacheControl["type"] != "ephemeral" {
			t.Fatalf("first content part = %#v", firstPart)
		}
		if !strings.Contains(fmt.Sprint(firstPart["text"]), "稳定上下文") {
			t.Fatalf("first content part missing shared context: %#v", firstPart)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"ok\\\",\\\"characters\\\":[],\\\"relationships\\\":[],\\\"emotion_curve\\\":[],\\\"events\\\":[]}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{
		OpenAIProAPIKey:     "dash-key",
		OpenAIProAPIBaseURL: server.URL,
		OpenAIProModel:      "qwen3.6-plus",
		OpenAITemperature:   0.1,
		LLMPromptCacheMode:  "dashscope_explicit",
	}, nil)
	err := client.Stream(context.Background(), "frame_narrative_understanding", map[string]any{
		"cache_context": map[string]any{"transcript": "稳定上下文"},
	}, func(string, map[string]any) error {
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
}

func TestOpenAIUsesDashScopeExplicitCacheByDefaultInAutoMode(t *testing.T) {
	client := NewOpenAIClient(config.Config{
		LLMPromptCacheMode:     "auto",
		DashScopeExplicitCache: true,
	}, nil)

	if !client.shouldUseDashScopeExplicitCache(openAIModelProfile{apiBaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1"}) {
		t.Fatalf("auto mode should enable DashScope explicit cache for DashScope base URL")
	}
	if client.shouldUseDashScopeExplicitCache(openAIModelProfile{apiBaseURL: "https://api.openai.com/v1"}) {
		t.Fatalf("auto mode should not enable DashScope explicit cache for non-DashScope base URL")
	}
}

func TestOpenAIStreamSendsThinkingConfigAndEmitsReasoningDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		thinking, ok := payload["thinking"].(map[string]any)
		if !ok {
			t.Fatalf("thinking = %#v, want object", payload["thinking"])
		}
		if thinking["type"] != "disabled" {
			t.Fatalf("thinking.type = %#v, want disabled", thinking["type"])
		}
		if payload["reasoning_effort"] != "none" {
			t.Fatalf("reasoning_effort = %#v, want none", payload["reasoning_effort"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"先分析\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"答案\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{
		OpenAIProAPIKey:          "openai-key",
		OpenAIProAPIBaseURL:      server.URL,
		OpenAIProModel:           "openai-test",
		OpenAITemperature:        0.1,
		OpenAIProThinkingType:    "enabled",
		OpenAIProReasoningEffort: "high",
	}, nil)
	var thinkingEvents []string
	var answerEvents []string
	err := client.Stream(context.Background(), "script", map[string]any{"video_context": "x", "video_config": map[string]any{"model_level": "deep"}}, func(event string, data map[string]any) error {
		delta, _ := data["delta"].(string)
		switch event {
		case "thinking_delta":
			thinkingEvents = append(thinkingEvents, delta)
		case "answer_delta":
			answerEvents = append(answerEvents, delta)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if strings.Join(thinkingEvents, "") != "先分析" {
		t.Fatalf("thinking events = %#v", thinkingEvents)
	}
	if strings.Join(answerEvents, "") != "答案" {
		t.Fatalf("answer events = %#v", answerEvents)
	}
}

func TestOpenAIStreamErrorsWhenScriptItemsEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"items\\\":[]}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "openai-key", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "openai-test", OpenAITemperature: 0.1}, nil)
	err := client.Stream(context.Background(), "script", map[string]any{"video_context": "x"}, func(string, map[string]any) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected empty script items error")
	}
	if !strings.Contains(err.Error(), "模型未返回有效脚本片段") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenAIStreamNormalizesAlternateScriptShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		content := "模型结果如下：\n```json\n{\"script\":[{\"id\":1,\"narration\":\"hello\",\"start_time\":\"00:00:00\",\"end_time\":\"00:00:01.5\",\"subtitle\":\"\"}]}\n```"
		payload, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{"delta": map[string]any{"content": content}},
			},
		})
		_, _ = w.Write([]byte("data: " + string(payload) + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "openai-key", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "openai-test", OpenAITemperature: 0.1}, nil)
	var completed map[string]any
	err := client.Stream(context.Background(), "script", map[string]any{"video_context": "x"}, func(event string, data map[string]any) error {
		if event == "completed" {
			completed = data
		}
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	items, ok := completed["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %#v, want one normalized item", completed["items"])
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("item = %#v, want object", items[0])
	}
	if item["_id"] != "1" || item["text"] != "hello" || item["start"] != float64(0) || item["end"] != 1.5 || item["org"] != "" {
		t.Fatalf("normalized item = %#v", item)
	}
}

func TestOpenAIStreamFallsBackTextToTimelineItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		content := "第一段解说。第二段解说。"
		payload, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{"delta": map[string]any{"content": content}},
			},
		})
		_, _ = w.Write([]byte("data: " + string(payload) + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "openai-key", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "openai-test", OpenAITemperature: 0.1}, nil)
	var completed map[string]any
	err := client.Stream(context.Background(), "script", map[string]any{
		"video_context": map[string]any{
			"2": map[string]any{"start_time": 3, "end_time": 6, "subtitle": "二"},
			"1": map[string]any{"start_time": 0, "end_time": 3, "subtitle": "一"},
		},
	}, func(event string, data map[string]any) error {
		if event == "completed" {
			completed = data
		}
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	items, ok := completed["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("items = %#v, want two fallback items", completed["items"])
	}
	first := items[0].(map[string]any)
	second := items[1].(map[string]any)
	if first["text"] != "第一段解说。" || first["start"] != float64(0) || first["end"] != float64(3) {
		t.Fatalf("first fallback item = %#v", first)
	}
	if second["text"] != "第二段解说。" || second["start"] != float64(3) || second["end"] != float64(6) {
		t.Fatalf("second fallback item = %#v", second)
	}
}

func TestOpenAIStreamHandlesLargeSSELines(t *testing.T) {
	largeContent := strings.Repeat("长", 70*1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		payload, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{
				{"delta": map[string]any{"content": largeContent}},
			},
		})
		_, _ = w.Write([]byte("data: " + string(payload) + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "openai-key", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "openai-test", OpenAITemperature: 0.1}, nil)
	var streamed strings.Builder
	err := client.Stream(context.Background(), "script", map[string]any{"video_context": "x"}, func(event string, data map[string]any) error {
		if event == "answer_delta" {
			delta, _ := data["delta"].(string)
			streamed.WriteString(delta)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if streamed.Len() != len(largeContent) {
		t.Fatalf("streamed length = %d, want %d", streamed.Len(), len(largeContent))
	}
}

func TestOpenAIClientDoesNotCapStreamBodyReadsWithClientTimeout(t *testing.T) {
	client := NewOpenAIClient(config.Config{}, nil)
	if client.httpClient.Timeout != 0 {
		t.Fatalf("http client timeout = %v, want 0 so stream body reads are not capped by Client.Timeout", client.httpClient.Timeout)
	}
}

func TestNarrationPipelineOperationRouting(t *testing.T) {
	client := OpenAIClient{}
	for _, operation := range []string{"narration_outline", "narration_global_review", "narration_global_repair"} {
		if !client.shouldUsePro("script_draft", map[string]any{"model_profile": "flash", "model_operation": operation}) {
			t.Fatalf("operation %s must force Pro", operation)
		}
	}
	if client.shouldUsePro("script_draft", map[string]any{"model_profile": "flash", "model_operation": "narration_segment"}) {
		t.Fatal("narration_segment must honor the requested Flash profile")
	}
	if client.shouldUsePro("script_draft", map[string]any{"model_profile": "flash", "model_operation": "narration_humanize"}) {
		t.Fatal("narration_humanize must honor the requested Flash profile")
	}
	if !isNarrationStructuredOperation(map[string]any{"model_operation": "narration_humanize"}) {
		t.Fatal("narration_humanize must request structured JSON output")
	}
	formatRepair := map[string]any{"model_profile": "flash", "model_operation": "narration_outline", "video_context": map[string]any{"purpose": "movie_format_repair"}}
	if client.shouldUsePro("script_draft", formatRepair) {
		t.Fatal("movie format repair with an explicit Flash profile must use Flash")
	}
	formatRepair["model_profile"] = "pro"
	if !client.shouldUsePro("script_draft", formatRepair) {
		t.Fatal("movie format repair without an explicit Flash profile must stay on Pro")
	}
	if !client.shouldUsePro("script_draft", map[string]any{"model_profile": "flash", "model_operation": "narration_outline", "video_context": map[string]any{"purpose": "movie_agent_chat"}}) {
		t.Fatal("other movie purposes on narration_outline must still force Pro")
	}
	// Desktop movie mode (movie_prompt_version 1) picks the profile per step; an explicit Flash is honoured everywhere but vision.
	for _, kind := range []string{"script_draft", "script", "frame_narrative_understanding"} {
		movieFlash := map[string]any{"model_profile": "flash", "model_operation": "narration_outline", "movie_prompt_version": 1}
		if client.shouldUsePro(kind, movieFlash) {
			t.Fatalf("%s: a movie step asking for Flash must use Flash", kind)
		}
		movieFlash["model_profile"] = "pro"
		if !client.shouldUsePro(kind, movieFlash) {
			t.Fatalf("%s: a movie step asking for Pro must use Pro", kind)
		}
	}
	if !client.shouldUsePro("movie_vision", map[string]any{"model_profile": "flash", "movie_prompt_version": 1}) {
		t.Fatal("movie vision keeps its own vision profile")
	}
	unconfigured := NewOpenAIClient(config.Config{OpenAIProAPIKey: "pro-key", OpenAIProAPIBaseURL: "https://pro.test", OpenAIProModel: "pro-model"}, nil)
	if got := unconfigured.profileFor("frame_narrative_understanding", map[string]any{"model_profile": "flash", "movie_prompt_version": 1}); got.name != openAIProfilePro {
		t.Fatalf("without a Flash profile the story step falls back to Pro, got %s", got.name)
	}
}

func TestNarrationOutlineDisablesMiniMaxThinking(t *testing.T) {
	client := OpenAIClient{}
	payload := map[string]any{}
	profile := openAIModelProfile{
		apiBaseURL:      "https://api.minimaxi.com/v1",
		thinkingType:    "disabled",
		reasoningEffort: "medium",
	}

	client.applyReasoningOptions(payload, profile, "script_draft", map[string]any{
		"model_operation": "narration_outline",
	})

	thinking, ok := payload["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Fatalf("thinking = %#v, want disabled", payload["thinking"])
	}
	if payload["reasoning_effort"] != "none" {
		t.Fatalf("reasoning_effort = %#v, want none", payload["reasoning_effort"])
	}
}

func TestParseJSONContentSkipsMiniMaxThinkBlock(t *testing.T) {
	content := `<think>The response schema could look like {"analysis":"draft only"}.</think>
{"summary":"final story","chapters":[{"chapter_id":"chapter-01"}]}`

	parsed := parseJSONContent(content)
	if parsed["summary"] != "final story" {
		t.Fatalf("parsed = %#v, want final JSON after think block", parsed)
	}
	chapters, _ := parsed["chapters"].([]any)
	if len(chapters) != 1 {
		t.Fatalf("chapters = %#v, want one final chapter", parsed["chapters"])
	}
}

func TestNarrationPipelineCompletedPayloads(t *testing.T) {
	client := OpenAIClient{}
	outline := client.completedPayload("script_draft", `{"summary":"story","chapters":[{"chapter_id":"chapter-01"}]}`, ChatUsage{}, "req-1", "pro", map[string]any{"model_operation": "narration_outline"})
	metadata, _ := outline["metadata"].(map[string]any)
	structured, _ := metadata["structured_result"].(map[string]any)
	if structured["summary"] != "story" || !allowsEmptyScriptItems("script_draft", map[string]any{"model_operation": "narration_outline"}) {
		t.Fatalf("outline structured payload = %#v", outline)
	}

	segment := client.completedPayload("script_draft", `{"items":[{"_id":"1","text":"copy","evidence_refs":["subtitle-1"]}],"summary":"chapter"}`, ChatUsage{}, "req-2", "flash", map[string]any{"model_operation": "narration_segment"})
	segmentMetadata, _ := segment["metadata"].(map[string]any)
	if _, ok := segmentMetadata["structured_result"].(map[string]any); !ok {
		t.Fatalf("segment must retain structured metadata: %#v", segment)
	}

	repair := client.completedPayload("script_draft", `{"items":[{"_id":2,"text":"fixed","org":"forbidden"}]}`, ChatUsage{}, "req-3", "pro", map[string]any{"model_operation": "narration_global_repair"})
	items, _ := repair["items"].([]any)
	patch, _ := items[0].(map[string]any)
	if patch["_id"] != "2" || patch["text"] != "fixed" {
		t.Fatalf("repair patch = %#v", patch)
	}
	if _, exists := patch["org"]; exists {
		t.Fatalf("repair patch leaked org: %#v", patch)
	}
}

func TestOpenAIStreamUsesDeepSeekProfileWhenRequested(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer deepseek-key" {
			t.Fatalf("authorization = %q, want deepseek key", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload["model"] != "deepseek-chat" {
			t.Fatalf("model = %#v, want deepseek-chat", payload["model"])
		}
		if _, leaked := payload["model_provider"]; leaked {
			t.Fatalf("model_provider must not be forwarded upstream: %#v", payload)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"summary\\\":\\\"ok\\\",\\\"characters\\\":[],\\\"relationships\\\":[],\\\"emotion_curve\\\":[],\\\"events\\\":[]}\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	cfg := config.Config{
		OpenAIProAPIKey: "pro-key", OpenAIProAPIBaseURL: server.URL, OpenAIProModel: "pro-model",
		DeepSeekAPIKey: "deepseek-key", DeepSeekAPIBaseURL: server.URL, DeepSeekModel: "deepseek-chat",
		OpenAITemperature: 0.1,
	}
	client := NewOpenAIClient(cfg, nil)
	request := map[string]any{"video_context": "x", "model_provider": "deepseek"}
	if got := client.ModelFor("frame_narrative_understanding", request); got != "deepseek-chat" {
		t.Fatalf("ModelFor = %q, want deepseek-chat", got)
	}
	if err := client.Stream(context.Background(), "frame_narrative_understanding", request, func(string, map[string]any) error { return nil }); err != nil {
		t.Fatalf("stream: %v", err)
	}
}

func TestOpenAIDeepSeekRequestFallsBackToProWhenNotConfigured(t *testing.T) {
	client := NewOpenAIClient(config.Config{OpenAIProAPIKey: "pro-key", OpenAIProAPIBaseURL: "https://example.test", OpenAIProModel: "pro-model"}, nil)
	if got := client.ModelFor("frame_narrative_understanding", map[string]any{"model_provider": "deepseek"}); got != "pro-model" {
		t.Fatalf("ModelFor = %q, want pro-model fallback", got)
	}
}

func TestOpenAIMovieDirectorPlanningDisablesThinkingOnDashScope(t *testing.T) {
	client := NewOpenAIClient(config.Config{}, nil)
	request := map[string]any{"model_operation": "movie_director_script"}
	payload := map[string]any{}
	client.applyStructuredOutputOptions(payload, "script", request)
	client.applyReasoningOptions(payload, openAIModelProfile{
		apiBaseURL:   "https://dashscope.aliyuncs.com/compatible-mode/v1",
		thinkingType: "disabled",
	}, "script", request)
	dropReasoningEffortWithoutThinking(payload)

	if payload["enable_thinking"] != false {
		t.Fatalf("enable_thinking = %#v, want false", payload["enable_thinking"])
	}
	if payload["reasoning_effort"] != nil {
		t.Fatalf("reasoning_effort = %#v, want none", payload["reasoning_effort"])
	}
	if payload["max_tokens"] != 65536 {
		t.Fatalf("max_tokens = %#v, want 65536", payload["max_tokens"])
	}

	other := map[string]any{}
	client.applyStructuredOutputOptions(other, "script", map[string]any{"model_operation": "narration_segment"})
	client.applyReasoningOptions(other, openAIModelProfile{
		apiBaseURL:   "https://dashscope.aliyuncs.com/compatible-mode/v1",
		thinkingType: "disabled",
	}, "script", map[string]any{"model_operation": "narration_segment"})
	if other["enable_thinking"] != false {
		t.Fatalf("other script operations keep the configured thinking switch, got %#v", other["enable_thinking"])
	}
}

func TestOpenAIMovieConfirmationCandidatesAnswerWithoutThinking(t *testing.T) {
	client := NewOpenAIClient(config.Config{}, nil)
	for _, purpose := range []string{"movie_story_question_plan", "movie_confirmation_plan"} {
		request := map[string]any{"model_operation": "narration_outline", "video_context": map[string]any{"purpose": purpose}}
		for _, base := range []string{"https://dashscope.aliyuncs.com/compatible-mode/v1", "https://api.xiaomimimo.com/v1"} {
			payload := map[string]any{}
			client.applyReasoningOptions(payload, openAIModelProfile{apiBaseURL: base, thinkingType: "enabled", reasoningEffort: "medium"}, "script_draft", request)
			dropReasoningEffortWithoutThinking(payload)
			if base == "https://api.xiaomimimo.com/v1" {
				thinking, _ := payload["thinking"].(map[string]any)
				if thinking["type"] != "disabled" {
					t.Fatalf("%s on %s: thinking = %#v, want disabled", purpose, base, payload["thinking"])
				}
			} else if payload["enable_thinking"] != false {
				t.Fatalf("%s on %s: enable_thinking = %#v, want false", purpose, base, payload["enable_thinking"])
			}
			if base == "https://api.xiaomimimo.com/v1" && payload["reasoning_effort"] != "none" || base != "https://api.xiaomimimo.com/v1" && payload["reasoning_effort"] != nil {
				t.Fatalf("%s on %s kept reasoning_effort", purpose, base)
			}
		}
	}
	// The story design shares the channel and also answers without thinking.
	payload := map[string]any{}
	client.applyReasoningOptions(payload, openAIModelProfile{apiBaseURL: "https://api.xiaomimimo.com/v1", thinkingType: "enabled"}, "script_draft",
		map[string]any{"model_operation": "narration_outline", "video_context": map[string]any{"purpose": "movie_story_design"}})
	if thinking, _ := payload["thinking"].(map[string]any); thinking["type"] != "disabled" {
		t.Fatalf("story design thinking = %#v, want disabled", payload["thinking"])
	}
}

func TestOpenAIReasoningModeOffOverridesForcedPlanningThinking(t *testing.T) {
	client := NewOpenAIClient(config.Config{}, nil)
	request := map[string]any{"model_operation": "movie_director_script", "reasoning_mode": "off"}
	payload := map[string]any{}
	client.applyStructuredOutputOptions(payload, "script", request)
	client.applyReasoningOptions(payload, openAIModelProfile{apiBaseURL: "https://api.xiaomimimo.com/v1", thinkingType: "enabled", reasoningEffort: "medium"}, "script", request)
	if thinking, _ := payload["thinking"].(map[string]any); thinking["type"] != "disabled" {
		t.Fatalf("thinking = %#v, want disabled", payload["thinking"])
	}
	if payload["reasoning_effort"] != "none" {
		t.Fatal("reasoning_effort kept with thinking off")
	}
}

func TestOpenAIMovieAgentChatReasonsOnlyWhenAsked(t *testing.T) {
	client := NewOpenAIClient(config.Config{}, nil)
	profile := openAIModelProfile{apiBaseURL: "https://api.deepseek.com"}
	call := func(mode string, purpose string) map[string]any {
		payload := map[string]any{"temperature": 0.7}
		request := map[string]any{"model_operation": "narration_outline", "reasoning_mode": mode, "video_context": map[string]any{"purpose": purpose}}
		client.applyStructuredOutputOptions(payload, "script_draft", request)
		client.applyReasoningOptions(payload, profile, "script_draft", request)
		dropReasoningEffortWithoutThinking(payload)
		return payload
	}
	thinking := func(p map[string]any) any { v, _ := p["thinking"].(map[string]any); return v["type"] }
	if p := call("on", "movie_agent_chat"); thinking(p) != "enabled" {
		t.Fatalf("agent chat asking for reasoning: %#v", p)
	}
	// Writing reasons and samples like the reference stage.
	if p := call("on", "movie_screenplay_full_draft"); thinking(p) != "enabled" || p["temperature"] != 1.0 || p["top_p"] != 0.95 {
		t.Fatalf("full draft: %#v", p)
	}
	// Picture matching reasons but keeps the structured temperature.
	if p := call("on", "movie_storyboard_match"); thinking(p) != "enabled" || p["temperature"] != 0 {
		t.Fatalf("storyboard match: %#v", p)
	}
	// Not asked, or not a listed purpose: reasoning stays off.
	for _, p := range []map[string]any{call("off", "movie_screenplay_full_draft"), call("on", "movie_format_repair"), call("on", "movie_screenplay_materials")} {
		if thinking(p) != "disabled" || p["reasoning_effort"] != "none" || p["temperature"] != 0 {
			t.Fatalf("other calls keep reasoning off and temperature 0, got %#v", p)
		}
	}
	dash := map[string]any{"temperature": 0.7}
	request := map[string]any{"model_operation": "narration_outline", "reasoning_mode": "on", "video_context": map[string]any{"purpose": "movie_screenplay_full_draft"}}
	client.applyStructuredOutputOptions(dash, "script_draft", request)
	client.applyReasoningOptions(dash, openAIModelProfile{apiBaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1"}, "script_draft", request)
	if dash["enable_thinking"] != true || dash["temperature"] != 1.0 || dash["max_tokens"] != movieReasoningOutputTokens {
		t.Fatalf("dashscope writing: %#v", dash)
	}
	// Reasoning shares max_tokens with the answer: a reasoning call always gets the larger cap.
	if p := call("on", "movie_screenplay_full_review"); p["max_tokens"] != movieReasoningOutputTokens || p["reasoning_effort"] != "medium" {
		t.Fatalf("batch fact check needs room for thinking plus JSON at moderate effort: %#v", p)
	}
	if p := call("on", "movie_screenplay_full_draft"); p["max_tokens"] != movieReasoningOutputTokens || p["reasoning_effort"] != "high" {
		t.Fatalf("writing keeps high effort with the larger cap: %#v", p)
	}
	if p := call("off", "movie_screenplay_full_review"); p["max_tokens"] == movieReasoningOutputTokens {
		t.Fatalf("calls without reasoning keep their own cap: %#v", p)
	}
}

func TestBigModelAlwaysThinkingMapsUnsupportedEfforts(t *testing.T) {
	glm := openAIModelProfile{apiBaseURL: "https://open.bigmodel.cn/api/paas/v4"}
	for _, tc := range []struct{ thinking, effort, want string }{
		{"disabled", "none", "low"}, {"enabled", "medium", "high"}, {"enabled", "high", "high"}, {"enabled", "low", "low"},
	} {
		payload := map[string]any{"thinking": map[string]any{"type": tc.thinking}, "reasoning_effort": tc.effort}
		applyAlwaysThinkingReasoning(payload, glm)
		if payload["thinking"].(map[string]any)["type"] != "enabled" || payload["reasoning_effort"] != tc.want {
			t.Fatalf("%s/%s: got %#v, want enabled/%s", tc.thinking, tc.effort, payload, tc.want)
		}
	}
	other := map[string]any{"thinking": map[string]any{"type": "disabled"}, "reasoning_effort": "none"}
	applyAlwaysThinkingReasoning(other, openAIModelProfile{apiBaseURL: "https://api.deepseek.com"})
	if other["thinking"].(map[string]any)["type"] != "disabled" || other["reasoning_effort"] != "none" {
		t.Fatalf("other providers keep their settings: %#v", other)
	}
}
