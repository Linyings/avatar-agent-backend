package server

import (
	"fmt"
	"strings"
)

const movieBackgroundResearchSummaryPurpose = "movie_background_research_summary"

// The movie desktop client asks the same narrative endpoint to curate noisy
// public search material. This is intentionally not a narrative-event result:
// it has no source-video time ranges and must not pass the event normalizer.
func isMovieBackgroundResearchSummaryRequest(req map[string]any) bool {
	for _, key := range []string{"purpose", "model_operation"} {
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(req[key])), movieBackgroundResearchSummaryPurpose) {
			return true
		}
	}
	return false
}

func movieResearchSummaryText(value any) string {
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "<nil>" {
		return ""
	}
	return text
}

func movieResearchSummaryItems(value any) []any {
	if values, ok := value.([]any); ok {
		return values
	}
	return nil
}

// normalizeMovieBackgroundResearchSummary accepts small alias variations while
// retaining the original fields. The desktop parser performs the stricter
// source-id validation against its local source catalogue.
func normalizeMovieBackgroundResearchSummary(parsed map[string]any) (map[string]any, int) {
	if parsed == nil {
		return parsed, 0
	}
	if movieResearchSummaryText(parsed["overview"]) == "" {
		parsed["overview"] = parsed["summary"]
	}
	facts := movieResearchSummaryItems(parsed["coreFacts"])
	if len(facts) == 0 {
		for _, key := range []string{"core_facts", "facts", "key_facts"} {
			if values := movieResearchSummaryItems(parsed[key]); len(values) > 0 {
				facts = values
				parsed["coreFacts"] = values
				break
			}
		}
	}
	if movieResearchSummaryText(parsed["overview"]) == "" || len(facts) == 0 {
		return parsed, 0
	}
	usable := 0
	for _, raw := range facts {
		fact, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		topic := movieResearchSummaryText(fact["topic"])
		if topic == "" {
			topic = movieResearchSummaryText(fact["title"])
		}
		detail := movieResearchSummaryText(fact["detail"])
		if detail == "" {
			detail = movieResearchSummaryText(fact["description"])
		}
		if topic != "" && detail != "" {
			usable++
		}
	}
	return parsed, usable
}
