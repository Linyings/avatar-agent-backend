package server

import "testing"

func TestMovieBackgroundResearchSummaryContract(t *testing.T) {
	if !isMovieBackgroundResearchSummaryRequest(map[string]any{"purpose": "MOVIE_BACKGROUND_RESEARCH_SUMMARY"}) {
		t.Fatal("purpose should select the public research summary contract")
	}
	parsed, usable := normalizeMovieBackgroundResearchSummary(map[string]any{
		"summary": "作品公开背景概览",
		"facts":   []any{map[string]any{"title": "作品定位", "description": "公开资料中的精炼事实", "source_ids": []any{1}}},
	})
	if usable != 1 || parsed["overview"] != "作品公开背景概览" {
		t.Fatalf("summary normalization failed: usable=%d parsed=%#v", usable, parsed)
	}
	if _, usable := normalizeMovieBackgroundResearchSummary(map[string]any{"overview": "无事实"}); usable != 0 {
		t.Fatal("summary without facts must remain invalid")
	}
}
