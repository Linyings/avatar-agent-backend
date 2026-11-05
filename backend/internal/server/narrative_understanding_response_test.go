package server

import "testing"

func TestExtractNarrativeUnderstandingRepairsSingleQuoteStringTerminators(t *testing.T) {
	content := `{"summary":"剧情","events":[{"start":140.41,"end":182.85,"type":"conflict","description":"福贵追打中一路叫喊','participants":["speaker_0"],"importance":"high","evidence":["站住"]},{"start":2435.34,"end":2463.90,"type":"setup","description":"福贵围观自行车、想学骑车、被拒','participants":["speaker_0"],"importance":"low","evidence":["别碰"]}]}`

	parsed := extractNarrativeUnderstanding(content)
	events, ok := parsed["events"].([]any)
	if !ok {
		t.Fatalf("events type = %T, want []any; parsed=%v", parsed["events"], parsed)
	}
	if len(events) != 2 {
		t.Fatalf("events length = %d, want 2", len(events))
	}
	first, ok := events[0].(map[string]any)
	if !ok {
		t.Fatalf("first event type = %T, want map[string]any", events[0])
	}
	if got := first["description"]; got != "福贵追打中一路叫喊" {
		t.Fatalf("description = %v", got)
	}
	if got := first["participants"].([]any); len(got) != 1 || got[0] != "speaker_0" {
		t.Fatalf("participants = %v", got)
	}
}

func TestExtractNarrativeUnderstandingKeepsValidApostrophes(t *testing.T) {
	content := `{"summary":"don't rewrite valid text","events":[{"start":1,"end":2,"description":"don't stop","participants":[]}]}`
	parsed := extractNarrativeUnderstanding(content)
	if got := parsed["summary"]; got != "don't rewrite valid text" {
		t.Fatalf("summary = %v", got)
	}
}
