package prompts

import (
	"strings"
	"testing"
)

func TestMovieCompleteStoryPromptsCarrySkillsWithoutLegacyBudgets(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatal(err)
	}
	builder := NewBuilder(service)
	for _, operation := range []string{"movie_director_script", "narration_segment", "narration_global_review", "narration_global_repair"} {
		t.Run(operation, func(t *testing.T) {
			kind := "script_draft"
			if operation == "movie_director_script" {
				kind = "script"
			}
			parts, err := builder.Build(kind, map[string]any{
				"model_operation": operation,
				"video_config":    map[string]any{"style_mode": "general", "target_language": "zh-CN", "narration_mode": "complete_story", "word_count": 126, "mix_ratio": 30, "output_duration_ms": 60000},
				"custom_prompt":   "<skill-reference>COMPLETE_STORY_SKILL_MARKER</skill-reference>",
				"video_context":   map[string]any{"key_story_lines": []any{map[string]any{"id": 18, "summary": "ENDING_PAYOFF"}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, required := range []string{"COMPLETE_STORY_SKILL_MARKER", "ENDING_PAYOFF"} {
				if !strings.Contains(parts.UserPrompt, required) {
					t.Fatalf("missing %s", required)
				}
			}
			if !strings.Contains(parts.SystemPrompt, "No output runtime") {
				t.Fatal("complete-story policy missing")
			}
			for _, forbidden := range []string{"70%-130%", "target_word_count", "60000", "126"} {
				if strings.Contains(parts.UserPrompt, forbidden) {
					t.Fatalf("legacy budget leaked: %s", forbidden)
				}
			}
		})
	}
}

func TestMovieCompleteStoryRequiresExplicitModeAndSupportedOperation(t *testing.T) {
	for _, req := range []map[string]any{
		{"model_operation": "narration_segment"},
		{"model_operation": "narration_outline", "video_config": map[string]any{"narration_mode": "complete_story"}},
	} {
		if _, _, ok := movieNarrationOperationPrompt(req); ok {
			t.Fatal("regular workflow prompt was replaced")
		}
	}
}

func TestMovieRepairExactEditsAreOptIn(t *testing.T) {
	for _, mode := range []string{"exact_edits", "focused_exact_edits"} {
		_, prompt, ok := movieNarrationOperationPrompt(map[string]any{
			"model_operation": "narration_global_repair",
			"video_config":    map[string]any{"narration_mode": "complete_story"},
			"video_context":   map[string]any{"repair_mode": mode},
		})
		if !ok || !strings.Contains(prompt, `"find"`) || !strings.Contains(prompt, "must match exactly once") || !strings.Contains(prompt, "previous_feedback") {
			t.Fatal("exact edit contract missing")
		}
		if strings.Contains(prompt, "Return changed allowed narration items only as") {
			t.Fatal("conflicting whole-item output requirement remains")
		}
	}
	_, legacy, _ := movieNarrationOperationPrompt(map[string]any{
		"model_operation": "narration_global_repair", "video_config": map[string]any{"narration_mode": "complete_story"},
	})
	if !strings.Contains(legacy, "Return changed allowed narration items only as") {
		t.Fatal("older movie clients lost their items contract")
	}
}

func TestMovieNarrationKeepsOneStoryAcrossScenes(t *testing.T) {
	required := map[string][]string{
		"movie_director_script":   {"story_role", "new_information", "transition", "Introduce background, goals and stakes once"},
		"narration_segment":       {"ALREADY HEARD", "reserved for later scenes", "Only the opening scene", "no character or duration budget"},
		"narration_global_review": {"repetitions", "keep_item_id", "duplicate_item_ids", "adjacent AND distant", "not duplication"},
		"narration_global_repair": {"remove its retelling", "Preserve every unique event", "No character, runtime or mix quota"},
	}
	for operation, markers := range required {
		t.Run(operation, func(t *testing.T) {
			_, prompt, ok := movieNarrationOperationPrompt(map[string]any{
				"model_operation": operation, "video_config": map[string]any{"narration_mode": "complete_story"},
			})
			if !ok {
				t.Fatal("movie operation was not handled")
			}
			for _, marker := range markers {
				if !strings.Contains(prompt, marker) {
					t.Fatalf("missing continuity instruction: %s", marker)
				}
			}
		})
	}
}

func TestMovieNarrationSegmentWritesAChapterBatch(t *testing.T) {
	single := map[string]any{"model_operation": "narration_segment", "video_config": map[string]any{"narration_mode": "complete_story"}}
	_, prompt, _ := movieNarrationOperationPrompt(single)
	if !strings.Contains(prompt, "Return exactly one item") {
		t.Fatal("a single scene request must still return exactly one item")
	}
	batch := map[string]any{"model_operation": "narration_segment", "video_config": map[string]any{"narration_mode": "complete_story"},
		"video_context": map[string]any{"chapter_batch": map[string]any{"chapter_id": "chapter-1", "scene_ids": []any{"3", "4"}}}}
	_, prompt, _ = movieNarrationOperationPrompt(batch)
	if strings.Contains(prompt, "Return exactly one item") || !strings.Contains(prompt, "Return one item per scene") {
		t.Fatal("a chapter batch must return one item per scene")
	}
}

func TestMovieOwnContractTasksDoNotReceiveTheChapterOutlineShape(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatal(err)
	}
	for _, purpose := range []string{"movie_story_design", "movie_story_question_plan", "movie_agent_chat"} {
		parts, err := NewBuilder(service).Build("script_draft", map[string]any{
			"model_operation": "narration_outline", "model_profile": "pro",
			"video_config":  map[string]any{"target_language": "zh-CN", "style_mode": "general"},
			"video_context": map[string]any{"purpose": purpose, "events": []any{map[string]any{"id": 1}}},
			"custom_prompt": `只返回JSON {throughline,cards:[]}`,
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(parts.UserPrompt, `"chapters"`) || strings.Contains(parts.UserPrompt, "chapter_id") {
			t.Fatalf("%s still receives the chapter outline shape:\n%s", purpose, parts.UserPrompt)
		}
		if !strings.Contains(parts.UserPrompt, "{throughline,cards:[]}") || !strings.Contains(parts.UserPrompt, `"purpose": "`+purpose+`"`) {
			t.Fatalf("%s lost its own contract or input:\n%s", purpose, parts.UserPrompt)
		}
	}
	// The regular narration workflow keeps its chapter outline.
	parts, err := NewBuilder(service).Build("script_draft", map[string]any{
		"model_operation": "narration_outline",
		"video_config":    map[string]any{"target_language": "zh-CN", "style_mode": "general"},
		"video_context":   map[string]any{"operation": "narration_outline"},
		"custom_prompt":   "风格要求",
	})
	if err != nil || !strings.Contains(parts.UserPrompt, `"chapters"`) {
		t.Fatalf("regular outline changed: %v", err)
	}
}
