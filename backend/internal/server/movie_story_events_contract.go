package server

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const movieStoryEventsContract = "movie_story_events_v1"

func isMovieStoryEventsRequest(req map[string]any) bool {
	return req["purpose"] == "movie_story_events"
}

// A complete event prefix is not a complete chapter. Unlike the generic
// narrative extractor, never salvage a truncated array or promote an inner
// object out of a malformed root; the PC must split/retry that unresolved scope.
func extractMovieStoryEvents(content string) map[string]any {
	cleaned := movieStoryThinkingPrefix.ReplaceAllString(content, "")
	cleaned = movieStoryFencePrefix.ReplaceAllString(cleaned, "")
	cleaned = strings.TrimSpace(movieStoryFenceSuffix.ReplaceAllString(cleaned, ""))
	var parsed map[string]any
	if err := json.Unmarshal([]byte(cleaned), &parsed); err == nil && parsed != nil {
		return parsed
	}
	start := strings.IndexByte(cleaned, '{')
	if start > 0 && cleaned[0] != '[' && cleaned[0] != '"' && cleaned[0] != '{' {
		if end := balancedJSONObjectEnd(cleaned, start); end > start {
			if err := json.Unmarshal([]byte(cleaned[start:end+1]), &parsed); err == nil && parsed != nil {
				return parsed
			}
		}
	}
	return map[string]any{"normalization_error": "invalid_json"}
}

var movieStoryNumericSeconds = regexp.MustCompile(`^-?\d+(?:\.\d+)?$`)
var movieStoryClockSeconds = regexp.MustCompile(`^(\d+):([0-5]\d):([0-5]\d(?:\.\d+)?)$`)
var movieStoryThinkingPrefix = regexp.MustCompile(`(?is)^\s*<think>.*?</think>\s*`)
var movieStoryFencePrefix = regexp.MustCompile("(?i)^\\s*```(?:json)?\\s*")
var movieStoryFenceSuffix = regexp.MustCompile("\\s*```\\s*$")

// Accept only explicit seconds or clock notation. A bare number is never
// guessed to be milliseconds; only a *_ms field performs that conversion.
func movieStorySeconds(value any) (float64, bool) {
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		if movieStoryNumericSeconds.MatchString(text) {
			seconds, err := strconv.ParseFloat(text, 64)
			return seconds, err == nil && seconds >= 0 && !math.IsInf(seconds, 0)
		}
		clock := movieStoryClockSeconds.FindStringSubmatch(text)
		if clock == nil {
			return 0, false
		}
		seconds := 0.0
		for _, part := range clock[1:] {
			number, err := strconv.ParseFloat(part, 64)
			if err != nil || math.IsInf(number, 0) {
				return 0, false
			}
			seconds = seconds*60 + number
		}
		return seconds, !math.IsInf(seconds, 0)
	}
	seconds, ok := parseFlexibleSeconds(value)
	return seconds, ok && seconds >= 0 && !math.IsNaN(seconds) && !math.IsInf(seconds, 0)
}

func movieStoryTime(record map[string]any, canonical string) (float64, bool) {
	for _, key := range []string{canonical, canonical + "_time", canonical + "Time", canonical + "_ms"} {
		value, exists := record[key]
		if !exists {
			continue
		}
		var seconds float64
		var ok bool
		if strings.HasSuffix(key, "_ms") {
			// A clock string already expresses seconds, so interpreting it as
			// milliseconds would silently move the event to unrelated footage.
			if text, textOK := value.(string); textOK && !movieStoryNumericSeconds.MatchString(strings.TrimSpace(text)) {
				return 0, false
			}
			seconds, ok = movieStorySeconds(value)
			seconds /= 1000
		} else {
			seconds, ok = movieStorySeconds(value)
		}
		// A malformed canonical field must remain a visible validation issue.
		return seconds, ok
	}
	return 0, false
}

// normalizeMovieStoryEvents preserves every original array position, including
// malformed entries, so the PC can repair only the rejected events. The normal
// frame workflow keeps its existing filtering contract.
func normalizeMovieStoryEvents(parsed map[string]any) (map[string]any, int) {
	normalized := make(map[string]any, len(parsed)+1)
	nested, _ := parsed["analysis"].(map[string]any)
	for key, value := range nested {
		normalized[key] = value
	}
	for key, value := range parsed {
		normalized[key] = value
	}
	if _, ok := normalized["events"].([]any); !ok {
		if events, nestedOK := nested["events"].([]any); nestedOK {
			normalized["events"] = events
		}
	}
	if summary, _ := normalized["summary"].(string); strings.TrimSpace(summary) == "" {
		if overview, ok := normalized["overview"].(string); ok && strings.TrimSpace(overview) != "" {
			normalized["summary"] = strings.TrimSpace(overview)
		}
	}
	events, ok := normalized["events"].([]any)
	if !ok {
		// Keep the wrong-shaped value as evidence for the desktop's schema
		// diagnostic instead of silently replacing it with an empty array.
		normalized["invalid_events"] = []any{map[string]any{"code": "events_missing_or_not_array", "reasons": []string{"events 必须是非空数组"}}}
		return normalized, 0
	}
	canonical := make([]any, len(events))
	invalid := make([]any, 0)
	usable := 0
	for index, item := range events {
		canonical[index] = item
		reasons := make([]string, 0)
		id := fmt.Sprintf("event-%d", index+1)
		if record, ok := item.(map[string]any); ok && record != nil {
			copy := make(map[string]any, len(record)+3)
			for key, value := range record {
				copy[key] = value
			}
			for _, key := range []string{"event_id", "id"} {
				if value, ok := record[key].(string); ok && strings.TrimSpace(value) != "" {
					id = value
					break
				}
			}
			description := ""
			for _, key := range []string{"description", "summary", "text"} {
				if value, ok := record[key].(string); ok && strings.TrimSpace(value) != "" {
					description = strings.TrimSpace(value)
					break
				}
			}
			if description == "" {
				reasons = append(reasons, "缺少非空事件描述 description")
			} else {
				copy["description"] = description
			}
			start, startOK := movieStoryTime(record, "start")
			end, endOK := movieStoryTime(record, "end")
			if startOK {
				copy["start"] = start
			}
			if endOK {
				copy["end"] = end
			}
			if !startOK || !endOK || end <= start {
				reasons = append(reasons, "事件 start/end 必须是有效秒数且 end > start")
			}
			canonical[index] = copy
		} else {
			reasons = append(reasons, "事件必须是 JSON 对象")
		}
		if len(reasons) == 0 {
			usable++
		} else {
			invalid = append(invalid, map[string]any{"index": index, "id": id, "reasons": reasons})
		}
	}
	if len(events) == 0 {
		invalid = append(invalid, map[string]any{"code": "events_empty", "reasons": []string{"events 不能为空数组"}})
	}
	normalized["events"] = canonical
	normalized["invalid_events"] = invalid
	return normalized, usable
}
