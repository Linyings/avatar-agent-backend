package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
)

func TestMovieStoryEventsNormalizationRetainsInvalidPositionsAndNestedMetadata(t *testing.T) {
	var parsed map[string]any
	raw := `{"analysis":{"overview":"父子发生争执","plot":{"premise":"家庭矛盾"},"characters":[{"name":"父亲"}],"open_questions":["去向不明"],"events":[{"id":"event-7","start_time":"00:05:45.37","endTime":"00:06:00.5","text":"父亲追问儿子的去向"},{"event_id":"event-12","id":"other-id","description":"缺少时间","dialogueIds":[18]},null,{"start_ms":400000,"end_ms":410500,"summary":"儿子离开"}]}}`
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatal(err)
	}
	normalized, usable := normalizeMovieStoryEvents(parsed)
	if usable != 2 || normalized["summary"] != "父子发生争执" || !reflect.DeepEqual(normalized["open_questions"], []any{"去向不明"}) {
		t.Fatalf("normalized = %#v, usable = %d", normalized, usable)
	}
	events := normalized["events"].([]any)
	if len(events) != 4 || events[2] != nil {
		t.Fatalf("event positions changed: %#v", events)
	}
	first := events[0].(map[string]any)
	last := events[3].(map[string]any)
	if first["start"] != 345.37 || first["end"] != 360.5 || first["description"] != "父亲追问儿子的去向" || first["start_time"] != "00:05:45.37" {
		t.Fatalf("first = %#v", first)
	}
	if last["start"] != 400.0 || last["end"] != 410.5 || last["description"] != "儿子离开" {
		t.Fatalf("last = %#v", last)
	}
	invalid := normalized["invalid_events"].([]any)
	if len(invalid) != 2 || invalid[0].(map[string]any)["index"] != 1 || invalid[0].(map[string]any)["id"] != "event-12" || invalid[1].(map[string]any)["index"] != 2 {
		t.Fatalf("invalid = %#v", invalid)
	}
	if _, mutated := parsed["summary"]; mutated {
		t.Fatal("normalization mutated source root")
	}
	originalFirst := parsed["analysis"].(map[string]any)["events"].([]any)[0].(map[string]any)
	if _, mutated := originalFirst["start"]; mutated {
		t.Fatal("normalization mutated nested source event")
	}
}

func TestMovieStoryEventsNormalizationDoesNotInventSummaryTimesOrEvents(t *testing.T) {
	cases := []struct {
		name   string
		events any
		usable int
	}{
		{"missing events", nil, 0},
		{"wrong array shape", "bad", 0},
		{"empty array", []any{}, 0},
		{"plain seconds remain seconds", []any{map[string]any{"start": 345000.0, "end": 346000.0, "description": "时间不得猜测单位"}}, 1},
		{"invalid canonical field cannot be hidden", []any{map[string]any{"start": "unknown", "start_ms": 1000.0, "end": 2.0, "description": "保留错误"}}, 0},
		{"unknown aliases remain invalid", []any{map[string]any{"from": 1.0, "to": 2.0, "title": "不是事件描述"}}, 0},
		{"invalid clock remains invalid", []any{map[string]any{"start": "01:99", "end": "02:10", "description": "非法时钟"}}, 0},
		{"clock in milliseconds remains invalid", []any{map[string]any{"start_ms": "00:00:05", "end_ms": "00:00:06", "description": "时钟不能当作毫秒"}}, 0},
		{"short clock is not interpreted", []any{map[string]any{"start": "00:01", "end": "00:02", "description": "时钟必须完整"}}, 0},
		{"unpadded clock is not interpreted", []any{map[string]any{"start": "1:2:3", "end": "1:2:4", "description": "分秒必须两位"}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := map[string]any{"events": tc.events}
			normalized, usable := normalizeMovieStoryEvents(input)
			if usable != tc.usable {
				t.Fatalf("usable = %d, want %d; normalized = %#v", usable, tc.usable, normalized)
			}
			if _, exists := normalized["summary"]; exists {
				t.Fatal("a summary must be derived only after evidence validation on PC")
			}
			if list, ok := tc.events.([]any); ok {
				if len(normalized["events"].([]any)) != len(list) {
					t.Fatal("invalid event was dropped")
				}
			} else if normalized["events"] != tc.events {
				t.Fatal("wrong-shaped events were overwritten")
			}
		})
	}
}

func TestMovieStoryEventsNormalizationMatchesDesktopNestedFallback(t *testing.T) {
	nested := []any{map[string]any{"start": 1.0, "end": 2.0, "description": "", "text": "保留有效嵌套事件"}}
	for _, topLevel := range []any{nil, "invalid", map[string]any{"wrong": "shape"}} {
		value, usable := normalizeMovieStoryEvents(map[string]any{"events": topLevel, "summary": "", "analysis": map[string]any{
			"events": nested, "overview": "使用嵌套概述", "custom_metadata": "preserved",
		}})
		if usable != 1 || len(anySlice(value["events"])) != 1 || value["summary"] != "使用嵌套概述" || value["custom_metadata"] != "preserved" {
			t.Fatalf("nested fallback differs from PC: %#v, usable=%d", value, usable)
		}
	}
	value, usable := normalizeMovieStoryEvents(map[string]any{"events": []any{}, "analysis": map[string]any{"events": nested}})
	if usable != 0 || len(anySlice(value["events"])) != 0 {
		t.Fatal("an explicit empty array must not be replaced by nested events")
	}
}

func TestMovieStoryEventsExtractionRejectsPartialOrWrongRoot(t *testing.T) {
	for _, content := range []string{
		`{"events":[{"start":1,"end":2,"description":"已写完"},{"start":3`,
		`{"analysis":{"events":[{"start":1,"end":2,"description":"已写完"}]}`,
		`[{"events":[{"start":1,"end":2,"description":"错根对象"}]}]`,
		`{"analysis":{"events":[]},broken}`,
		`{"events":[{"start":1,"end":2,"description":"完整前缀"}]} trailing-invalid-json`,
		`"prefix {"events":[{"start":1,"end":2,"description":"引号内不是根对象"}]}"`,
	} {
		parsed := extractMovieStoryEvents(content)
		if parsed["normalization_error"] != "invalid_json" || parsed["events"] != nil {
			t.Fatalf("partial/wrong root was accepted: %#v for %s", parsed, content)
		}
	}
	content := "<think>分析中</think>\n```json\n" + `{"analysis":{"events":[null]}}` + "\n```"
	if parsed := extractMovieStoryEvents(content); parsed["analysis"] == nil {
		t.Fatalf("complete wrapped object rejected: %#v", parsed)
	}
	for _, complete := range []string{
		"说明文字\n" + `{"events":[null]}` + "\n说明结束",
		"<THINK>分析中</THINK>\n```JSON\n" + `{"events":[null]}` + "\n```",
		`{"events":[{"description":"保留字面值 <think>内容</think>","start":1,"end":2}]}`,
	} {
		if parsed := extractMovieStoryEvents(complete); parsed["events"] == nil {
			t.Fatalf("complete supported output rejected: %#v", parsed)
		}
	}
}

func TestMovieStoryEventsStreamPreservesRepairEvidenceAndBillingOutcome(t *testing.T) {
	for _, tc := range []struct {
		name        string
		purpose     string
		content     string
		finish      string
		wantEvent   string
		wantCount   int
		wantCredits int64
	}{
		{"mixed", "movie_story_events", `{"analysis":{"events":[{"start":"00:00:01","end":"00:00:02","text":"有效"},{"description":"缺少时间"},null]}}`, "stop", "completed", 3, 13},
		{"nested_fallback_billed", "movie_story_events", `{"events":"wrong-shaped root","analysis":{"events":[{"start":1,"end":2,"text":"有效嵌套事件"}]}}`, "stop", "completed", 1, 13},
		{"trailing_garbage", "movie_story_events", `{"events":[{"start":1,"end":2,"description":"完整前缀"}]} invalid`, "stop", "error", 0, 0},
		{"all_invalid", "movie_story_events", `{"events":[{"description":"缺少时间"},null]}`, "stop", "error", 2, 0},
		{"truncated", "movie_story_events", `{"events":[{"start":1,"end":2,"description":"写完部分"}],"summary":"未完成`, "length", "error", 0, 0},
		{"length_with_closed_json", "movie_story_events", `{"events":[{"start":1,"end":2,"description":"碰巧写完 JSON 但章节截断"}]}`, "length", "error", 1, 0},
		{"regular_unchanged", "", `{"events":[{"start":1,"end":2,"description":"有效"},{"description":"缺少时间"},null]}`, "stop", "completed", 1, 13},
	} {
		t.Run(tc.name, func(t *testing.T) {
			providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: {\"id\":\"story-contract-request\",\"model\":\"openai-text-test\",\"choices\":[{\"delta\":{\"content\":%q},\"finish_reason\":%q}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":6,\"total_tokens\":13}}\n\ndata: [DONE]\n\n", tc.content, tc.finish)
			}))
			defer providerServer.Close()
			app := newTestApp(t)
			app.openai = providers.NewOpenAIClient(config.Config{OpenAIProAPIKey: "test-key", OpenAIProAPIBaseURL: providerServer.URL, OpenAIProModel: "openai-text-test"}, app.promptBuilder)
			seedOpenAIWildcardBillingRate(t, app)
			email := tc.name + "-story-contract@example.com"
			token := registerAndLoginWithCredits(t, app, email, 10000)
			resp := request(app, http.MethodPost, "/api/v1/tools/frame-workflow/narrative-analyze", map[string]any{
				"purpose": tc.purpose, "movie_prompt_version": 1, "cache_context": map[string]any{"rules": "按事件契约输出 JSON"},
				"dialogue": []any{map[string]any{"id": 1, "start": 1, "end": 2, "text": "有效"}},
			}, token)
			var payload map[string]any
			for _, block := range strings.Split(resp.Body.String(), "\n\n") {
				if !strings.HasPrefix(block, "event:"+tc.wantEvent+"\n") {
					continue
				}
				for _, line := range strings.Split(block, "\n") {
					if strings.HasPrefix(line, "data:") {
						if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data:")), &payload); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if payload == nil {
				t.Fatalf("missing %s payload: %s", tc.wantEvent, resp.Body.String())
			}
			if tc.wantEvent == "error" {
				if payload["code"] != "provider_invalid_output" || strings.Contains(resp.Body.String(), "event:completed\n") {
					t.Fatalf("invalid output became success: %#v", payload)
				}
				payload = payload["result"].(map[string]any)
			}
			if payload["content"] != tc.content || payload["finish_reason"] != tc.finish {
				t.Fatalf("raw evidence was changed or finish reason lost: %#v", payload)
			}
			if got := len(anySlice(payload["analysis"].(map[string]any)["events"])); got != tc.wantCount {
				t.Fatalf("event count = %d, want %d", got, tc.wantCount)
			}
			if tc.purpose != "" && payload["normalization_contract"] != movieStoryEventsContract {
				t.Fatalf("missing movie contract: %#v", payload)
			}
			if tc.purpose == "" && payload["normalization_contract"] != nil {
				t.Fatal("movie contract leaked into the regular narrative workflow")
			}
			var user models.User
			if err := app.db.First(&user, "email = ?", email).Error; err != nil {
				t.Fatal(err)
			}
			var account models.UserTokenAccount
			if err := app.db.First(&account, "user_id = ?", user.ID).Error; err != nil {
				t.Fatal(err)
			}
			if account.UsedTokens != tc.wantCredits {
				t.Fatalf("used credits = %d, want %d", account.UsedTokens, tc.wantCredits)
			}
		})
	}
}
