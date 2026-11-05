package prompts

import (
	"encoding/json"
	"strings"
	"testing"
)

func sceneContextFixture() map[string]any {
	req := humanizerRequest("短剧漫剧", "script")
	ctx := mapValue(req, "video_context")
	text := "画面观察：他拿起行李，尚未离开。；声音信息（非逐字字幕）：准备出发。；不确定：身份未确认。"
	ctx["evidence"] = []any{map[string]any{"id": 1, "source": "video", "startMs": 10000, "endMs": 20000, "visual": "他拿起行李，尚未离开。", "audio": "准备出发。", "uncertainty": "身份未确认。", "text": text}}
	ctx["context"] = map[string]any{"narrative_context": "只是假设，不代表事实。", "items": map[string]any{"1": map[string]any{"start_time": 10, "end_time": 20, "visual": text, "subtitle": "需单独保留的文字。"}}}
	return req
}

func TestSceneWritingContextDeduplicatesWithoutLosingFactsOrMutatingRequest(t *testing.T) {
	req := sceneContextFixture()
	before, _ := json.Marshal(req)
	out := generalSceneWritingRequest(req)
	after, _ := json.Marshal(req)
	if string(before) != string(after) {
		t.Fatal("input project evidence was mutated")
	}
	ctx := mapValue(out, "video_context")
	e := ctx["evidence"].([]any)[0].(map[string]any)
	if _, exists := e["text"]; exists {
		t.Fatal("exact duplicate still repeated")
	}
	old := mapValue(req, "video_context")["evidence"].([]any)[0].(map[string]any)
	for key, value := range old {
		if key != "text" && e[key] != value {
			t.Fatalf("evidence %s lost", key)
		}
	}
	i := mapValue(mapValue(mapValue(ctx, "context"), "items"), "1")
	if i["visualEvidenceId"] != 1 || i["subtitle"] != "需单独保留的文字。" || i["start_time"] != 10 || i["end_time"] != 20 {
		t.Fatal("source reference, range or independent subtitle changed")
	}
	if mapValue(ctx, "context")["narrative_context"] != "只是假设，不代表事实。" {
		t.Fatal("whole-story understanding lost")
	}
	// Exercise the real Builder, not just the context helper.
	s, err := NewService("../../prompt")
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewBuilder(s).Build("script_draft", req)
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]any
	const marker = "\nINPUT:\n"
	pos := strings.LastIndex(p.UserPrompt, marker)
	if pos < 0 || json.Unmarshal([]byte(p.UserPrompt[pos+len(marker):]), &actual) != nil {
		t.Fatal("actual input is not JSON")
	}
	actualItem := mapValue(mapValue(mapValue(actual, "context"), "items"), "1")
	actualEvidence := actual["evidence"].([]any)[0].(map[string]any)
	if actualItem["visualEvidenceId"] != float64(1) || actualEvidence["uncertainty"] != "身份未确认。" {
		t.Fatal("reference/uncertainty missing from actual writing prompt", err)
	}
}

func TestSceneWritingContextPreservesDistinctOrUnrecognizedData(t *testing.T) {
	for _, change := range []string{"different-text", "different-time", "structured-visual", "unknown-context", "asr", "downstream", "duplicate-id"} {
		t.Run(change, func(t *testing.T) {
			req := sceneContextFixture()
			ctx := mapValue(req, "video_context")
			e := ctx["evidence"].([]any)[0].(map[string]any)
			i := mapValue(mapValue(mapValue(ctx, "context"), "items"), "1")
			switch change {
			case "different-text":
				e["text"] = "独立补充的信息不能删除。"
				i["visual"] = "另一个版本的观察也必须保留。"
			case "different-time":
				i["end_time"] = 21
			case "structured-visual":
				i["visual"], e["text"] = map[string]any{"extra": true}, map[string]any{"extra": true}
			case "unknown-context":
				ctx["context"] = "未结构化的剧情理解不得清空。"
			case "asr":
				ctx["sourcePolicy"] = ""
			case "downstream":
				ctx["purpose"] = "chapters"
			case "duplicate-id":
				ctx["evidence"] = append(ctx["evidence"].([]any), e)
			}
			out := mapValue(generalSceneWritingRequest(req), "video_context")
			if change == "unknown-context" {
				if out["context"] != ctx["context"] {
					t.Fatal("unknown context changed")
				}
				return
			}
			got := mapValue(mapValue(mapValue(out, "context"), "items"), "1")
			a, _ := json.Marshal(i)
			b, _ := json.Marshal(got)
			if string(a) != string(b) {
				t.Fatal("distinct or unrecognized observation was dropped")
			}
		})
	}
}
