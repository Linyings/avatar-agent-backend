package server

import (
	"encoding/json"
	"strings"
)

// salvageEventsFromTruncatedPayload 从可能被截断的剧情理解输出中挽救 events
// 数组：按括号配对逐个抽取已完整输出的事件对象。数组未开始时返回 nil。
// 数组深度相对计数：1 = events 数组顶层，2 = 事件对象或嵌套数组内部。
func salvageEventsFromTruncatedPayload(content string) map[string]any {
	idx := strings.Index(content, "events")
	if idx < 0 {
		return nil
	}
	arrStart := strings.Index(content[idx:], "[")
	if arrStart < 0 {
		return nil
	}
	arrStart += idx
	events := []any{}
	depth := 0
	seenArrayOpen := false
	inString := false
	escaped := false
	objStart := -1
	for i := arrStart; i < len(content); i++ {
		ch := content[i]
		if escaped {
			escaped = false
			continue
		}
		if inString {
			if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '[', '{':
			depth++
			if ch == '[' && depth == 1 {
				seenArrayOpen = true
			}
			if ch == '{' && depth == 2 && seenArrayOpen && objStart < 0 {
				objStart = i
			}
		case ']', '}':
			if ch == '}' && depth == 2 && objStart >= 0 {
				var event map[string]any
				if err := json.Unmarshal([]byte(content[objStart:i+1]), &event); err == nil && event != nil {
					events = append(events, event)
				}
				objStart = -1
			}
			depth--
			if ch == ']' && depth == 0 {
				// events 数组自身已闭合（截断发生在其后字段）
				break
			}
			if depth < 0 {
				return nil
			}
		}
	}
	if len(events) > 0 {
		return map[string]any{"events": events}
	}
	return nil
}
