package server

import (
	"encoding/json"
	"testing"
)

func TestParseFlexibleSeconds(t *testing.T) {
	cases := []struct {
		name   string
		value  any
		want   float64
		wantOK bool
	}{
		{name: "float", value: 85.3, want: 85.3, wantOK: true},
		{name: "numeric string", value: "85.3", want: 85.3, wantOK: true},
		{name: "seconds suffix", value: "85秒", want: 85, wantOK: true},
		{name: "mm:ss", value: "1:25", want: 85, wantOK: true},
		{name: "mm:ss.fraction", value: "01:25.5", want: 85.5, wantOK: true},
		{name: "hh:mm:ss", value: "1:02:03", want: 3723, wantOK: true},
		{name: "spaced", value: " 12 秒钟 ", want: 12, wantOK: true},
		{name: "garbage", value: "abc", wantOK: false},
		{name: "clock-like garbage", value: "01:xx", wantOK: false},
		{name: "nil", value: nil, wantOK: false},
		{name: "negative", value: -3, want: -3, wantOK: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := parseFlexibleSeconds(testCase.value)
			if ok != testCase.wantOK {
				t.Fatalf("ok = %v, want %v", ok, testCase.wantOK)
			}
			if ok && got != testCase.want {
				t.Fatalf("got %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestParseNarrativeTimeRange(t *testing.T) {
	cases := []struct {
		name      string
		value     any
		wantStart float64
		wantEnd   float64
		wantOK    bool
	}{
		{name: "object", value: map[string]any{"start": 12.5, "end": 40}, wantStart: 12.5, wantEnd: 40, wantOK: true},
		{name: "object string values", value: map[string]any{"start": "1:25", "end": "2:00"}, wantStart: 85, wantEnd: 120, wantOK: true},
		{name: "array", value: []any{12.5, 40.0}, wantStart: 12.5, wantEnd: 40, wantOK: true},
		{name: "dash string", value: "12.5-40", wantStart: 12.5, wantEnd: 40, wantOK: true},
		{name: "tilde clock string", value: "1:25~2:30", wantStart: 85, wantEnd: 150, wantOK: true},
		{name: "chinese connector", value: "12.5 至 40 秒", wantStart: 12.5, wantEnd: 40, wantOK: true},
		{name: "garbage", value: "no range here", wantOK: false},
		{name: "nil", value: nil, wantOK: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			start, end, ok := parseNarrativeTimeRange(testCase.value)
			if ok != testCase.wantOK {
				t.Fatalf("ok = %v, want %v", ok, testCase.wantOK)
			}
			if ok && (start != testCase.wantStart || end != testCase.wantEnd) {
				t.Fatalf("got (%v, %v), want (%v, %v)", start, end, testCase.wantStart, testCase.wantEnd)
			}
		})
	}
}

func TestNormalizeNarrativeUnderstandingEvents(t *testing.T) {
	t.Run("normalizes mm:ss strings and summary fallback", func(t *testing.T) {
		parsed := map[string]any{
			"events": []any{
				map[string]any{
					"start": "1:25", "end": "2:00", "summary": "福贵自述家世",
					"importance": "high", "participants": []any{"speaker_0"},
				},
				map[string]any{
					"time_range":  map[string]any{"start": "3:00", "end": "3:30"},
					"description": "父子冲突爆发",
				},
			},
		}
		normalized, usable := normalizeNarrativeUnderstandingEvents(parsed)
		if usable != 2 {
			t.Fatalf("usable = %d, want 2", usable)
		}
		events, _ := normalized["events"].([]any)
		first, _ := events[0].(map[string]any)
		if first["start"] != 85.0 || first["end"] != 120.0 {
			t.Fatalf("first event range = %v-%v, want 85-120", first["start"], first["end"])
		}
		if first["description"] != "福贵自述家世" {
			t.Fatalf("first description = %v", first["description"])
		}
		second, _ := events[1].(map[string]any)
		if second["start"] != 180.0 || second["end"] != 210.0 {
			t.Fatalf("second event range = %v-%v, want 180-210", second["start"], second["end"])
		}
		if _, hasTimeRange := second["time_range"]; hasTimeRange {
			t.Fatalf("time_range should be removed after normalization")
		}
	})

	t.Run("drops events without recoverable timestamps", func(t *testing.T) {
		parsed := map[string]any{
			"events": []any{
				map[string]any{"description": "没有时间的事件"},
				map[string]any{"description": "合法事件", "start": 5.0, "end": 9.0},
			},
		}
		_, usable := normalizeNarrativeUnderstandingEvents(parsed)
		if usable != 1 {
			t.Fatalf("usable = %d, want 1", usable)
		}
	})

	t.Run("promotes analysis.events when top level empty", func(t *testing.T) {
		parsed := map[string]any{
			"analysis": map[string]any{
				"events": []any{
					map[string]any{"description": "嵌套事件", "start": 10.0, "end": 20.0},
				},
			},
		}
		normalized, usable := normalizeNarrativeUnderstandingEvents(parsed)
		if usable != 1 {
			t.Fatalf("usable = %d, want 1", usable)
		}
		events, _ := normalized["events"].([]any)
		if len(events) != 1 {
			t.Fatalf("events length = %d, want 1", len(events))
		}
	})

	t.Run("json payload round trip", func(t *testing.T) {
		payload := `{"events":[{"start":"00:01:25","end":"00:02:00","description":"开场"}]}`
		var parsed map[string]any
		if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		_, usable := normalizeNarrativeUnderstandingEvents(parsed)
		if usable != 1 {
			t.Fatalf("usable = %d, want 1", usable)
		}
	})

	t.Run("empty events stays zero", func(t *testing.T) {
		_, usable := normalizeNarrativeUnderstandingEvents(map[string]any{"summary": "nothing"})
		if usable != 0 {
			t.Fatalf("usable = %d, want 0", usable)
		}
	})
}

func TestExtractNarrativeUnderstandingHandlesThinkWrappedOutput(t *testing.T) {
	content := "<think>The user wants a narrative analysis. Plan the events.</think>\n\n```json\n" +
		`{"events":[{"start":"1:25","end":"2:00","summary":"福贵自述家世"}]}` + "\n```"
	parsed := extractNarrativeUnderstanding(content)
	events, ok := parsed["events"].([]any)
	if !ok || len(events) != 1 {
		t.Fatalf("events = %#v, want 1 entry", parsed["events"])
	}
	normalized, usable := normalizeNarrativeUnderstandingEvents(parsed)
	if usable != 1 {
		t.Fatalf("usable = %d, want 1", usable)
	}
	events, _ = normalized["events"].([]any)
	first, _ := events[0].(map[string]any)
	if first["start"] != 85.0 || first["description"] != "福贵自述家世" {
		t.Fatalf("first event = %#v", first)
	}
}

func TestExtractNarrativeUnderstandingHandlesUnterminatedThink(t *testing.T) {
	content := "<think>reasoning without a closing tag\n" +
		`{"events":[{"start":5,"end":9,"description":"合法事件"}]}`
	parsed := extractNarrativeUnderstanding(content)
	_, usable := normalizeNarrativeUnderstandingEvents(parsed)
	if usable != 1 {
		t.Fatalf("usable = %d, want 1", usable)
	}
}

func TestExtractNarrativeUnderstandingSalvagesTruncatedEvents(t *testing.T) {
	// 模拟 40 分钟素材：分析文字写满输出上限，根对象未闭合即被截断
	content := `{"summary":"《福贵》第1集剧情分析","plot":{"premise":"徐家是产贡米的大户","current_stage":"少爷离家出走","stakes":"父子冲突升级"},"characters":[{"id":"speaker_0","name":"","role":"少爷","traits":["骄纵"],"goals":["借米"],"emotional_state":"紧张","evidence":["字幕"]},"events":[{"start":104.64,"end":126.8,"description":"小福贵确认危机","importance":"high"},{"start":140.59,"end":166.56,"description":"清洁任务启动","importance":"medium"`
	parsed := extractNarrativeUnderstanding(content)
	// 第二个事件对象本身被截断（无闭合括号），只能挽救第一个完整事件
	events, ok := parsed["events"].([]any)
	if !ok || len(events) != 1 {
		t.Fatalf("events = %#v, want 1 salvaged event", parsed["events"])
	}
	first, _ := events[0].(map[string]any)
	if first["description"] != "小福贵确认危机" {
		t.Fatalf("first event description = %v", first["description"])
	}
}

func TestExtractNarrativeUnderstandingSalvagesClosedArrayWithTruncatedRoot(t *testing.T) {
	// events 数组完整闭合、但其后的字段被截断
	content := `{"summary":"剧情","events":[{"start":5,"end":9,"description":"开场"}],"conflicts":["父子冲突","米行博弈","第三条没写完的冲突`
	parsed := extractNarrativeUnderstanding(content)
	events, ok := parsed["events"].([]any)
	if !ok || len(events) != 1 {
		t.Fatalf("events = %#v, want 1 salvaged event", parsed["events"])
	}
}

func TestExtractNarrativeUnderstandingSalvageIgnoresArraylessContent(t *testing.T) {
	if salvaged := salvageEventsFromTruncatedPayload(`{"summary":"没有 events 的普通输出"}"`); salvaged != nil {
		t.Fatalf("salvage should return nil without an events array")
	}
}

func TestExtractNarrativeUnderstandingSalvagesPrettyPrintedTruncatedEvents(t *testing.T) {
	// 回归：美化排版的 events 数组含嵌套 participants/evidence 数组，且在
	// 第三个事件中途被截断。旧实现的扫描在遇到嵌套数组的 ']' 时提前放弃。
	content := "{\n" +
		"  \"events\": [\n" +
		"    {\n" +
		"      \"start\": 104.64,\n" +
		"      \"end\": 126.8,\n" +
		"      \"description\": \"小福贵确认危机\",\n" +
		"      \"participants\": [\"speaker_0\"],\n" +
		"      \"evidence\": [\"字幕\"]\n" +
		"    },\n" +
		"    {\n" +
		"      \"start\": 140.59,\n" +
		"      \"end\": 166.56,\n" +
		"      \"description\": \"清洁任务启动\",\n" +
		"      \"participants\": [\"speaker_1\"],\n" +
		"      \"evidence\": [\"字幕2\"]\n" +
		"    },\n" +
		"    {\n" +
		"      \"start\": 180,\n" +
		"      \"description\": \"被截断的事件"
	parsed := extractNarrativeUnderstanding(content)
	events, ok := parsed["events"].([]any)
	if !ok || len(events) != 2 {
		t.Fatalf("events = %#v, want 2 salvaged events", parsed["events"])
	}
}
