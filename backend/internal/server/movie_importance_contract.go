package server

import (
	"fmt"
	"math"
	"strings"
)

const movieImportanceSelectionPurpose = "movie_importance_selection"

// isMovieImportanceSelectionRequest keeps the movie-mode contract separate from
// the regular frame narrative-understanding contract. The desktop client sends
// purpose because model_operation is treated as transport metadata and removed
// before prompt construction.
func isMovieImportanceSelectionRequest(req map[string]any) bool {
	for _, key := range []string{"purpose", "model_operation"} {
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(req[key])), movieImportanceSelectionPurpose) {
			return true
		}
	}
	return false
}

func movieImportanceSelectionPriority(value any) string {
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(value))) {
	case "must", "critical", "high", "required", "关键", "重点", "必须", "高":
		return "must"
	case "should", "medium", "important", "recommended", "重要", "建议", "中":
		return "should"
	case "optional", "low", "nice_to_have", "可选", "次要", "低":
		return "optional"
	default:
		return ""
	}
}

func movieImportanceSelectionConfidence(value any) string {
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(value))) {
	case "high", "高":
		return "high"
	case "low", "低":
		return "low"
	default:
		return "medium"
	}
}

func movieImportanceSelectionStrings(value any) []string {
	values := make([]string, 0)
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" && text != "<nil>" {
				values = append(values, text)
			}
		}
	case string:
		if text := strings.TrimSpace(typed); text != "" {
			values = append(values, text)
		}
	}
	return values
}

func movieImportanceSelectionIDs(value any) []int64 {
	values := make([]int64, 0)
	appendValue := func(item any) {
		number, ok := parseJSONFloat(item)
		if !ok || number < 0 || math.Trunc(number) != number || number > math.MaxInt64 {
			return
		}
		values = append(values, int64(number))
	}
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			appendValue(item)
		}
	default:
		appendValue(value)
	}
	return values
}

func movieImportanceSelectionBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case float64:
		return typed == 1
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "yes", "1", "是", "无对白":
			return true
		}
	}
	return false
}

func movieImportanceSelectionTime(record map[string]any, keys []string) (float64, bool) {
	for _, key := range keys {
		value, ok := record[key]
		if !ok {
			continue
		}
		seconds, valid := parseFlexibleSeconds(value)
		if !valid {
			continue
		}
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		if strings.HasSuffix(normalizedKey, "_ms") || strings.HasSuffix(normalizedKey, "ms") || strings.Contains(normalizedKey, "millisecond") {
			seconds /= 1000
		}
		return seconds, true
	}
	for _, key := range []string{"timeRange", "time_range", "range", "timestamp"} {
		rangeValue, ok := record[key]
		if !ok {
			continue
		}
		if rangeRecord, ok := rangeValue.(map[string]any); ok {
			start, end, valid := parseNarrativeTimeRange(rangeRecord)
			if !valid {
				continue
			}
			if keys[0] == "start" {
				return start, true
			}
			return end, true
		}
	}
	return 0, false
}

// normalizeMovieImportanceSelection canonicalizes the small structured result
// used by movie-mode key-plot selection. It deliberately does not require a
// narrative description: unlike regular narrative understanding, this result
// describes candidate ranges and their evidence for later visual analysis.
func normalizeMovieImportanceSelection(parsed map[string]any) (map[string]any, int) {
	if parsed == nil {
		return parsed, 0
	}
	events, _ := parsed["events"].([]any)
	if len(events) == 0 {
		for _, key := range []string{"important_segments", "importantScenes", "segments", "items", "results"} {
			if candidate, ok := parsed[key].([]any); ok && len(candidate) > 0 {
				events = candidate
				parsed["events"] = candidate
				break
			}
		}
	}
	if len(events) == 0 {
		parsed["events"] = []any{}
		return parsed, 0
	}
	normalizedEvents := make([]any, 0, len(events))
	for _, item := range events {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		start, startOK := movieImportanceSelectionTime(record, []string{"start", "start_sec", "startSeconds", "start_time", "source_start", "sourceStart", "begin", "from", "startMs", "start_ms", "startMilliseconds", "start_milliseconds", "beginMs", "begin_ms"})
		end, endOK := movieImportanceSelectionTime(record, []string{"end", "end_sec", "endSeconds", "end_time", "source_end", "sourceEnd", "finish", "to", "endMs", "end_ms", "endMilliseconds", "end_milliseconds", "finishMs", "finish_ms"})
		if !startOK || !endOK || start < 0 || end <= start {
			continue
		}
		priority := movieImportanceSelectionPriority(record["priority"])
		if priority == "" {
			priority = movieImportanceSelectionPriority(record["importance"])
		}
		if priority == "" {
			continue
		}
		reasonsValue := record["reasons"]
		if reasonsValue == nil {
			reasonsValue = record["reason"]
		}
		if reasonsValue == nil {
			reasonsValue = record["evidence"]
		}
		reasons := movieImportanceSelectionStrings(reasonsValue)
		if len(reasons) == 0 {
			continue
		}
		score, scoreOK := parseJSONFloat(record["score"])
		if !scoreOK {
			score, scoreOK = parseJSONFloat(record["importance_score"])
		}
		if scoreOK && score > 1 && score <= 100 {
			score /= 100
		}
		if !scoreOK || score < 0 || score > 1 {
			score = map[string]float64{"must": 0.9, "should": 0.7, "optional": 0.4}[priority]
		}
		canonical := make(map[string]any, len(record)+7)
		for key, value := range record {
			canonical[key] = value
		}
		canonical["start"] = start
		canonical["end"] = end
		canonical["score"] = score
		canonical["priority"] = priority
		canonical["reasons"] = reasons
		dialogueValue := record["dialogueIds"]
		if dialogueValue == nil {
			dialogueValue = record["dialogue_ids"]
		}
		dialogueIDs := movieImportanceSelectionIDs(dialogueValue)
		canonicalDialogueIDs := make([]any, 0, len(dialogueIDs))
		for _, id := range dialogueIDs {
			canonicalDialogueIDs = append(canonicalDialogueIDs, id)
		}
		canonical["dialogueIds"] = canonicalDialogueIDs
		visualValue := record["visualOnly"]
		if visualValue == nil {
			visualValue = record["visual_only"]
		}
		if visualValue == nil {
			visualValue = record["silent"]
		}
		canonical["visualOnly"] = movieImportanceSelectionBool(visualValue)
		confidenceValue := record["confidence"]
		if confidenceValue == nil {
			confidenceValue = record["certainty"]
		}
		canonical["confidence"] = movieImportanceSelectionConfidence(confidenceValue)
		normalizedEvents = append(normalizedEvents, canonical)
	}
	parsed["events"] = normalizedEvents
	return parsed, len(normalizedEvents)
}
