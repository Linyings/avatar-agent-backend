package server

import (
	"reflect"
	"testing"
)

func TestNormalizeMovieImportanceSelectionCanonicalizesNearValidEvents(t *testing.T) {
	parsed, usable := normalizeMovieImportanceSelection(map[string]any{
		"important_segments": []any{
			map[string]any{
				"start":        "00:12.5",
				"end":          "28.2秒",
				"score":        "90",
				"priority":     "高",
				"reason":       "冲突升级",
				"dialogue_ids": []any{"12", 13.0},
				"visual_only":  "是",
				"confidence":   "高",
			},
		},
	})
	if usable != 1 {
		t.Fatalf("usable event count = %d, want 1 (parsed=%#v)", usable, parsed)
	}
	events, ok := parsed["events"].([]any)
	if !ok || len(events) != 1 {
		t.Fatalf("events = %#v, want one event", parsed["events"])
	}
	event := events[0].(map[string]any)
	if event["start"] != 12.5 || event["end"] != 28.2 || event["score"] != 0.9 || event["priority"] != "must" {
		t.Fatalf("canonical event timing/priority/score = %#v", event)
	}
	if !reflect.DeepEqual(event["reasons"], []string{"冲突升级"}) {
		t.Fatalf("reasons = %#v", event["reasons"])
	}
	if !reflect.DeepEqual(event["dialogueIds"], []any{int64(12), int64(13)}) {
		t.Fatalf("dialogueIds = %#v", event["dialogueIds"])
	}
	if event["visualOnly"] != true || event["confidence"] != "high" {
		t.Fatalf("visualOnly/confidence = %#v/%#v", event["visualOnly"], event["confidence"])
	}
}

func TestNormalizeMovieImportanceSelectionDoesNotRequireDescription(t *testing.T) {
	parsed, usable := normalizeMovieImportanceSelection(map[string]any{
		"events": []any{map[string]any{
			"start": 1.0, "end": 3.0, "score": 0.8, "priority": "should",
			"reasons": []any{"无对白但出现关键动作"}, "dialogueIds": []any{}, "visualOnly": true,
		}},
	})
	if usable != 1 {
		t.Fatalf("usable event count = %d, want 1 (parsed=%#v)", usable, parsed)
	}
	event := parsed["events"].([]any)[0].(map[string]any)
	if _, hasDescription := event["description"]; hasDescription {
		t.Fatalf("movie event unexpectedly gained narrative description: %#v", event)
	}
}

func TestMovieImportanceSelectionRequestDetection(t *testing.T) {
	if !isMovieImportanceSelectionRequest(map[string]any{"purpose": "MOVIE_IMPORTANCE_SELECTION"}) {
		t.Fatal("purpose should identify movie importance request")
	}
	if !isMovieImportanceSelectionRequest(map[string]any{"model_operation": "movie_importance_selection"}) {
		t.Fatal("model_operation should identify movie importance request")
	}
	if isMovieImportanceSelectionRequest(map[string]any{"purpose": "narrative_understanding"}) {
		t.Fatal("regular narrative request must not use movie importance contract")
	}
}
