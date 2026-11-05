package server

import (
	"strings"
	"testing"
)

func subtitleValidationTestSegments(ids ...any) []any {
	segments := make([]any, 0, len(ids))
	for _, id := range ids {
		segments = append(segments, map[string]any{
			"id":    id,
			"start": 0,
			"end":   1,
			"text":  "test",
		})
	}
	return segments
}

func TestParseSubtitleValidationResponseExtractsWrappedJSONObject(t *testing.T) {
	content := `校对完成，结果如下：
{"summary":"ok","corrections_count":0,"segments":[{"id":"1","start":0,"end":1,"text":"test"}]}
请查收。`

	analysis, segments, err := parseSubtitleValidationResponse(
		content,
		subtitleValidationTestSegments("1"),
	)
	if err != nil {
		t.Fatalf("parseSubtitleValidationResponse returned error: %v", err)
	}
	if analysis["summary"] != "ok" {
		t.Fatalf("unexpected analysis: %#v", analysis)
	}
	if len(segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segments))
	}
}

func TestParseSubtitleValidationResponseOrdersSubtitleRecordByExpectedIDs(t *testing.T) {
	content := `{
		"summary":"ok",
		"subtitles": {
			"2":{"id":2,"start":1,"end":2,"text":"second"},
			"1":{"id":1,"start":0,"end":1,"text":"first"}
		}
	}`

	_, segments, err := parseSubtitleValidationResponse(
		content,
		subtitleValidationTestSegments(1, 2),
	)
	if err != nil {
		t.Fatalf("parseSubtitleValidationResponse returned error: %v", err)
	}
	if got := subtitleValidationSegmentID(segments[0]); got != "1" {
		t.Fatalf("expected first id 1, got %q", got)
	}
	if got := subtitleValidationSegmentID(segments[1]); got != "2" {
		t.Fatalf("expected second id 2, got %q", got)
	}
}

func TestParseSubtitleValidationResponseRejectsMissingTargetSegments(t *testing.T) {
	content := `{"summary":"partial","segments":[{"id":"1","text":"first"}]}`

	_, _, err := parseSubtitleValidationResponse(
		content,
		subtitleValidationTestSegments("1", "2"),
	)
	if err == nil {
		t.Fatal("expected invalid response error")
	}
	if !strings.Contains(err.Error(), "expected 2 target segments, received 1") {
		t.Fatalf("unexpected error: %v", err)
	}
}
