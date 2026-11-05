package prompts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTemplateServiceLoadsAndRenders(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	items := service.List("fast")
	if len(items) == 0 {
		t.Fatalf("templates = 0, want configured fast templates")
	}
	movieTemplate, ok := service.Get("电影电视剧")
	if !ok {
		t.Fatal("missing 电影电视剧 template")
	}
	if movieTemplate.TitleI18n["en"] != "Film and TV Drama" {
		t.Fatalf("english title = %q", movieTemplate.TitleI18n["en"])
	}
	if !strings.Contains(movieTemplate.DescriptionI18n["en"], "single-pass script generation template") {
		t.Fatalf("english description = %q", movieTemplate.DescriptionI18n["en"])
	}
	rendered, err := service.Render("电影电视剧", map[string]string{
		"narration_perspective": "第三人称",
		"output_language":       "中文",
		"target_word_count":     "1000-3500",
		"original_ratio":        "60",
		"narration_ratio":       "40",
		"audio_speed":           "5",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(rendered.UserPrompt, "第三人称") {
		t.Fatalf("rendered user prompt does not contain substituted variable")
	}
}

func TestNarrationPipelineOperationsUseDedicatedSchemas(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	builder := NewBuilder(service)
	cases := []struct {
		operation string
		contains  string
	}{
		{operation: "narration_outline", contains: `"chapters"`},
		{operation: "narration_segment", contains: `"rolling_memory"`},
		{operation: "narration_segment", contains: "Do not recite"},
		{operation: "narration_segment", contains: "Return exactly the number of narration items requested by item_count_range"},
		{operation: "narration_segment", contains: "allocate each selected event to exactly one item"},
		{operation: "narration_humanize", contains: "Return every input item exactly once"},
		{operation: "narration_global_review", contains: `"coverage"`},
		{operation: "narration_global_repair", contains: "allowed_item_ids"},
	}
	for _, tc := range cases {
		parts, buildErr := builder.Build("script_draft", map[string]any{
			"model_operation": tc.operation,
			"video_config":    map[string]any{"style_mode": "电影电视剧", "target_language": "zh-CN"},
			"video_context":   map[string]any{"operation": tc.operation},
		})
		if buildErr != nil {
			t.Fatalf("build %s: %v", tc.operation, buildErr)
		}
		if !strings.Contains(parts.UserPrompt, tc.contains) {
			t.Fatalf("%s prompt missing %q:\n%s", tc.operation, tc.contains, parts.UserPrompt)
		}
	}
}

func TestNarrationWritingOperationsApplyHumanizerGuidance(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	templates := service.List("fast")
	if len(templates) == 0 {
		t.Fatal("missing fast templates")
	}

	operation := "narration_humanize"
	parts, buildErr := NewBuilder(service).Build("script_draft", map[string]any{
		"model_operation": operation,
		"video_config": map[string]any{
			"style_mode": templates[0].ID, "target_language": "zh-CN",
		},
		"video_context": map[string]any{"operation": operation},
	})
	if buildErr != nil {
		t.Fatalf("build %s: %v", operation, buildErr)
	}
	for _, required := range []string{
		"Humanizer-zh natural-writing rules",
		"Remove filler openers",
		"Avoid formulaic contrasts",
		"Vary sentence lengths",
		"never licenses invented facts",
	} {
		if !strings.Contains(parts.UserPrompt, required) {
			t.Fatalf("%s prompt missing %q", operation, required)
		}
	}
}

func TestNarrationSegmentLeavesHumanizerToSecondStage(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	templates := service.List("fast")
	if len(templates) == 0 {
		t.Fatal("missing fast templates")
	}
	parts, buildErr := NewBuilder(service).Build("script_draft", map[string]any{
		"model_operation": "narration_segment",
		"video_config": map[string]any{
			"style_mode": templates[0].ID, "target_language": "zh-CN",
		},
		"video_context": map[string]any{"operation": "narration_segment"},
	})
	if buildErr != nil {
		t.Fatalf("build narration_segment: %v", buildErr)
	}
	if strings.Contains(parts.UserPrompt, "Humanizer-zh natural-writing rules") {
		t.Fatal("narration_segment must leave Humanizer work to the second stage")
	}
}

func TestNarrationPromptsEnforceBoundedLengthWithoutWeakeningEvidence(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	builder := NewBuilder(service)
	cases := []struct {
		operation string
		contains  []string
		excludes  []string
	}{
		{
			operation: "narration_segment",
			contains:  []string{"70%-130%", "hard acceptance range", "evidence alignment", "chapter.target_characters defines the real acceptance range", "generation_constraints overrides video_config.word_count", "previous_rejected_draft", "strict_final_retry"},
			excludes:  []string{"rough planning guidance"},
		},
		{
			operation: "narration_humanize",
			contains:  []string{"must remain inside target_character_range", "Modify only text", "Never cut evidence-backed facts", "per_item_maximum_characters", "required_reduction_characters"},
			excludes:  []string{"character range is advisory"},
		},
		{
			operation: "narration_global_review",
			contains:  []string{"Do not fail solely because of character count", "high-severity issue must identify", "omitted events are intentionally out of scope", "event_ids as authoritative local coverage proof"},
		},
		{
			operation: "narration_global_repair",
			contains:  []string{"exact, adjacent, and callback evidence", "Never optimize for character count or runtime"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.operation, func(t *testing.T) {
			parts, buildErr := builder.Build("script_draft", map[string]any{
				"model_operation": tc.operation,
				"video_config": map[string]any{
					"style_mode": "电影电视剧", "target_language": "zh-CN",
				},
				"video_context": map[string]any{"operation": tc.operation},
			})
			if buildErr != nil {
				t.Fatalf("build %s: %v", tc.operation, buildErr)
			}
			for _, required := range tc.contains {
				if !strings.Contains(parts.UserPrompt, required) {
					t.Fatalf("%s prompt missing %q", tc.operation, required)
				}
			}
			for _, forbidden := range tc.excludes {
				if strings.Contains(parts.UserPrompt, forbidden) {
					t.Fatalf("%s prompt must not contain %q", tc.operation, forbidden)
				}
			}
		})
	}
}

func TestNarrationOutlinePromptRequiresSourceTimelineSeconds(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	templates := service.List("fast")
	if len(templates) == 0 {
		t.Fatal("missing fast templates")
	}
	parts, err := NewBuilder(service).Build("script_draft", map[string]any{
		"model_operation": "narration_outline",
		"video_config": map[string]any{
			"style_mode": templates[0].ID, "target_language": "en-US",
		},
		"video_context": map[string]any{
			"operation": "narration_outline",
			"contract": map[string]any{
				"source_duration_seconds": 1200.204,
				"chapter_range_unit":      "source_timeline_seconds",
				"valid_chapter_range":     []float64{0, 1200.204},
			},
		},
	})
	if err != nil {
		t.Fatalf("build narration outline: %v", err)
	}
	for _, required := range []string{"source timeline seconds", "contract.source_duration_seconds", "Never use milliseconds"} {
		if !strings.Contains(parts.UserPrompt, required) {
			t.Fatalf("narration outline prompt missing %q", required)
		}
	}
}

func TestScriptMatchPromptRequiresBlockEditing(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	builder := NewBuilder(service)
	parts, err := builder.Build("script_match", map[string]any{
		"video_config":  map[string]any{"style_mode": "电影电视剧", "target_language": "zh-CN"},
		"video_context": map[string]any{"operation": "script_match"},
	})
	if err != nil {
		t.Fatalf("build script_match: %v", err)
	}
	if !strings.Contains(parts.UserPrompt, "原片块必须是连续") {
		t.Fatalf("script_match prompt missing block editing rule:\n%s", parts.UserPrompt)
	}
	if !strings.Contains(parts.UserPrompt, "20–40 秒") {
		t.Fatalf("script_match prompt missing block duration guidance:\n%s", parts.UserPrompt)
	}
}

func TestScriptMatchPromptExposesActualMixedPlaybackRatio(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	parts, err := NewBuilder(service).Build("script_match", map[string]any{
		"video_config": map[string]any{
			"style_mode":         "电影电视剧",
			"target_language":    "zh-CN",
			"org_ratio":          50,
			"mix_ratio":          50,
			"narration_ratio":    0.5,
			"output_duration_ms": 120000,
		},
		"video_context": map[string]any{"operation": "script_match"},
	})
	if err != nil {
		t.Fatalf("build script_match: %v", err)
	}
	for _, required := range []string{
		"目标原片占比：50%",
		"目标解说占比：50%",
		"目标原片播放量（软估算）：约 60.0 秒",
		"必须至少输出一个 text 为空且 org 非空的原片段",
		"必须先播放原片块，再播放对该原片块的解说块",
		"原片段可以使用新的临时 _id",
		"不得按 _id 排序时间线",
	} {
		if !strings.Contains(parts.UserPrompt, required) {
			t.Fatalf("script_match prompt missing %q:\n%s", required, parts.UserPrompt)
		}
	}
}

func TestScriptMatchPromptExcludesDetectedOpeningAndEndingSongs(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	parts, err := NewBuilder(service).Build("script_match", map[string]any{
		"video_config":  map[string]any{"style_mode": "电影电视剧", "target_language": "en-US"},
		"video_context": map[string]any{"transcript": "(music)", "visual": "title cards"},
	})
	if err != nil {
		t.Fatalf("build script_match: %v", err)
	}
	for _, required := range []string{"excluded_source_ranges", "opening_song", "ending_song", "must not overlap"} {
		if !strings.Contains(parts.UserPrompt, required) {
			t.Fatalf("script_match prompt missing %q:\n%s", required, parts.UserPrompt)
		}
	}
}

func TestScriptMatchPromptExcludesAdvertisements(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	parts, err := NewBuilder(service).Build("script_match", map[string]any{
		"video_config":  map[string]any{"style_mode": "电影电视剧", "target_language": "en-US"},
		"video_context": map[string]any{"transcript": "scan the QR code", "visual": "casino website promotion"},
	})
	if err != nil {
		t.Fatalf("build script_match: %v", err)
	}
	for _, required := range []string{"advertisement", "gambling_ad", "website_promotion", "must not overlap"} {
		if !strings.Contains(parts.UserPrompt, required) {
			t.Fatalf("script_match prompt missing %q:\n%s", required, parts.UserPrompt)
		}
	}
}

func TestTemplateServiceListsConfiguredNarrationStyles(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	items := service.List("fast")
	want := readTemplateOrder(t, "../../prompt", "fast")
	if len(items) != len(want) {
		t.Fatalf("templates = %d, want %d", len(items), len(want))
	}
	for index, id := range want {
		if items[index].ID != id {
			t.Fatalf("templates[%d] = %q, want %q", index, items[index].ID, id)
		}
	}
}

func TestTemplateServiceResolvesTwoPhaseTemplatesForEveryFastTemplate(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	for _, fast := range service.List("fast") {
		step1, ok := service.ResolveTwoPhaseTemplate(fast.ID, "step1")
		if !ok {
			t.Fatalf("missing step1 template for %q", fast.ID)
		}
		wantStep1Path := filepath.ToSlash(filepath.Join("TwoPhases", "step1", fast.ID+".json"))
		if step1.JSONPath != wantStep1Path {
			t.Fatalf("step1 path for %q = %q, want %q", fast.ID, step1.JSONPath, wantStep1Path)
		}
		step2, ok := service.ResolveTwoPhaseTemplate(fast.ID, "step2")
		if !ok {
			t.Fatalf("missing step2 template for %q", fast.ID)
		}
		wantStep2Path := filepath.ToSlash(filepath.Join("TwoPhases", "step2", fast.ID+".json"))
		if step2.JSONPath != wantStep2Path {
			t.Fatalf("step2 path for %q = %q, want %q", fast.ID, step2.JSONPath, wantStep2Path)
		}
	}
}

func readTemplateOrder(t *testing.T, baseDir string, category string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(baseDir, templateOrderConfigFile))
	if err != nil {
		t.Fatalf("read sort config: %v", err)
	}
	var orderByCategory map[string][]string
	if err := json.Unmarshal(data, &orderByCategory); err != nil {
		t.Fatalf("decode sort config: %v", err)
	}
	return orderByCategory[category]
}

func TestTemplateServiceUsesTemplateOrderConfig(t *testing.T) {
	baseDir := t.TempDir()
	fastDir := filepath.Join(baseDir, "fast")
	if err := os.Mkdir(fastDir, 0o755); err != nil {
		t.Fatalf("mkdir fast dir: %v", err)
	}
	for _, id := range []string{"短剧漫剧", "白话高能", "直播带货"} {
		data, err := os.ReadFile(filepath.Join("../../prompt/fast", id+".json"))
		if err != nil {
			t.Fatalf("read fixture %s: %v", id, err)
		}
		if err := os.WriteFile(filepath.Join(fastDir, id+".json"), data, 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", id, err)
		}
	}
	if err := os.WriteFile(filepath.Join(baseDir, templateOrderConfigFile), []byte(`{"fast":["白话高能","短剧漫剧"]}`), 0o644); err != nil {
		t.Fatalf("write sort config: %v", err)
	}

	service, err := NewService(baseDir)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	items := service.List("fast")
	want := []string{"白话高能", "短剧漫剧"}
	if len(items) != len(want) {
		t.Fatalf("templates = %d, want %d", len(items), len(want))
	}
	for index, id := range want {
		if items[index].ID != id {
			t.Fatalf("templates[%d] = %q, want %q", index, items[index].ID, id)
		}
	}
}

func TestPromptBuilderBuildsEnglishRuntimePrompt(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	builder := NewBuilder(service)
	parts, err := builder.Build("script", map[string]any{
		"video_context": []any{
			map[string]any{
				"start_time": 1,
				"end_time":   2.5,
				"subtitle":   "hello",
				"visual":     "close-up",
			},
		},
		"video_config": map[string]any{
			"style_mode":      "电影电视剧",
			"target_language": "en-US",
			"person_view":     "first_person",
			"clip_mode":       "narration_only",
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(parts.UserPrompt, "## Actual task input for this run (high priority)") {
		t.Fatalf("missing english runtime marker:\n%s", parts.UserPrompt)
	}
	if !strings.Contains(parts.UserPrompt, "- Editing mode: narration only") {
		t.Fatalf("missing english clip mode:\n%s", parts.UserPrompt)
	}
	if !strings.Contains(parts.UserPrompt, "Generate all narration copy in English") {
		t.Fatalf("missing english output language guard:\n%s", parts.UserPrompt)
	}
	if !strings.Contains(parts.UserPrompt, "[1 -> 2.5] hello (visual) close-up") {
		t.Fatalf("missing english video context:\n%s", parts.UserPrompt)
	}
	if !strings.Contains(parts.UserPrompt, "first person") || !strings.Contains(parts.UserPrompt, "English") {
		t.Fatalf("template variables were not localized:\n%s", parts.UserPrompt)
	}
}

func TestPromptBuilderBuildsRuntimePrompt(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	builder := NewBuilder(service)
	parts, err := builder.Build("script", map[string]any{
		"video_context": "片段一\n片段二",
		"video_config": map[string]any{
			"style_mode":      "电影电视剧",
			"target_language": "zh-CN",
			"person_view":     "third_person",
			"word_count":      "1000-3500",
			"org_ratio":       20,
			"is_org":          true,
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if parts.Meta.ID != "电影电视剧" {
		t.Fatalf("template id = %s", parts.Meta.ID)
	}
	if !strings.Contains(parts.UserPrompt, runtimeMarker) {
		t.Fatalf("missing runtime marker")
	}
	if !strings.Contains(parts.UserPrompt, "片段一") {
		t.Fatalf("missing video context")
	}
}

func TestPromptBuilderAddsEvidenceBoundedEngagementRulesToNarrationOnly(t *testing.T) {
	builder := &Builder{}
	videoConfig := map[string]any{"target_language": "zh-CN"}

	for _, kind := range []string{"script", "script_draft"} {
		prompt := builder.userPrompt(kind, map[string]any{}, videoConfig, "", "zh-CN")
		for _, want := range []string{
			"## 观众留存与互动要求（高优先级）",
			"成片前 5 秒",
			"不得为制造争议",
			"不得输出标题、封面文案、标签",
			"绝不是虚构情节",
		} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("%s prompt missing %q:\n%s", kind, want, prompt)
			}
		}
		if strings.Count(prompt, "## 观众留存与互动要求（高优先级）") != 1 {
			t.Fatalf("%s prompt should contain the engagement section exactly once", kind)
		}
	}

	matchPrompt := builder.userPrompt("script_match", map[string]any{}, videoConfig, "", "zh-CN")
	if strings.Contains(matchPrompt, "## 观众留存与互动要求（高优先级）") {
		t.Fatalf("script_match prompt must not rewrite approved narration:\n%s", matchPrompt)
	}

	english := narrationEngagementPrompt("en")
	for _, want := range []string{
		"Audience retention and engagement requirements",
		"first five seconds",
		"Never manufacture outrage",
		"Do not output titles, cover copy, hashtags, tags",
		"never permission to fabricate plot",
	} {
		if !strings.Contains(english, want) {
			t.Fatalf("english engagement prompt missing %q:\n%s", want, english)
		}
	}
}

func TestPromptBuilderDefaultsOriginalNarrationRatio(t *testing.T) {
	builder := &Builder{}
	ctx := builder.templateContext(map[string]any{}, "zh-CN")

	if ctx["original_ratio"] != "60" {
		t.Fatalf("original_ratio default = %q, want 60", ctx["original_ratio"])
	}
	if ctx["narration_ratio"] != "40" {
		t.Fatalf("narration_ratio default = %q, want 40", ctx["narration_ratio"])
	}
}

func TestPromptBuilderUsesShortDramaTwoPhaseTemplates(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	builder := NewBuilder(service)
	req := map[string]any{
		"video_context": "片段一\n片段二",
		"video_config": map[string]any{
			"style_mode":      "短剧漫剧",
			"target_language": "zh-CN",
			"word_count":      "1200",
		},
	}

	draft, err := builder.Build("script_draft", req)
	if err != nil {
		t.Fatalf("build draft: %v", err)
	}
	if draft.Meta.JSONPath != "TwoPhases/step1/短剧漫剧.json" {
		t.Fatalf("draft template path = %q", draft.Meta.JSONPath)
	}
	if strings.Contains(draft.UserPrompt, "智能分割文案") || strings.Contains(draft.UserPrompt, "已确认解说文案") {
		t.Fatalf("draft prompt should not include visual matching template:\n%s", draft.UserPrompt)
	}
	if !strings.Contains(draft.UserPrompt, "本次只完成第一阶段") || !strings.Contains(draft.UserPrompt, "_id、text") {
		t.Fatalf("draft prompt missing draft runtime guard:\n%s", draft.UserPrompt)
	}
	if strings.Contains(draft.UserPrompt, "start、end、org") && !strings.Contains(draft.UserPrompt, "严禁输出 start、end、org") {
		t.Fatalf("draft prompt should not request timeline fields:\n%s", draft.UserPrompt)
	}

	matchReq := map[string]any{
		"video_context": "片段一\n片段二",
		"script_items":  []any{map[string]any{"_id": "1", "text": "审核后的文案"}},
		"video_config": map[string]any{
			"style_mode":      "短剧漫剧",
			"target_language": "zh-CN",
		},
	}
	match, err := builder.Build("script_match", matchReq)
	if err != nil {
		t.Fatalf("build match: %v", err)
	}
	if match.Meta.JSONPath != "TwoPhases/step2/短剧漫剧.json" {
		t.Fatalf("match template path = %q", match.Meta.JSONPath)
	}
	if strings.Contains(match.UserPrompt, "爆款开头方法") || strings.Contains(match.UserPrompt, "目标总字数") {
		t.Fatalf("match prompt should not include copywriting template:\n%s", match.UserPrompt)
	}
	if !strings.Contains(match.UserPrompt, "已确认解说文案") || !strings.Contains(match.UserPrompt, "审核后的文案") {
		t.Fatalf("match prompt missing approved script:\n%s", match.UserPrompt)
	}
	if !strings.Contains(match.UserPrompt, "_id、text、start、end、org") {
		t.Fatalf("match prompt missing timeline output fields:\n%s", match.UserPrompt)
	}
}

func TestPromptBuilderFormatsObjectVideoContext(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	builder := NewBuilder(service)
	parts, err := builder.Build("script", map[string]any{
		"video_context": map[string]any{
			"2": map[string]any{
				"start_time": 3.1,
				"end_time":   4.36,
				"subtitle":   "我就给你吃了个嘎嘎",
				"visual":     "人物近景",
			},
			"1": map[string]any{
				"start_time": 0.56,
				"end_time":   1.08,
				"subtitle":   "宝妹儿",
			},
		},
		"video_config": map[string]any{
			"style_mode":      "电影电视剧",
			"target_language": "zh-CN",
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(parts.UserPrompt, "Total Segments: 2") {
		t.Fatalf("missing segment count in prompt:\n%s", parts.UserPrompt)
	}
	if !strings.Contains(parts.UserPrompt, "[0.56 -> 1.08] 宝妹儿") {
		t.Fatalf("missing sorted first segment in prompt:\n%s", parts.UserPrompt)
	}
	if !strings.Contains(parts.UserPrompt, "[3.1 -> 4.36] 我就给你吃了个嘎嘎 (视觉)人物近景") {
		t.Fatalf("missing visual segment in prompt:\n%s", parts.UserPrompt)
	}
}

func TestPromptBuilderFormatsWrappedVideoContextItems(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	builder := NewBuilder(service)
	parts, err := builder.Build("script", map[string]any{
		"video_context": map[string]any{
			"items": map[string]any{
				"1": map[string]any{
					"start_time": 0.5,
					"end_time":   1.5,
					"subtitle":   "包装结构里的字幕",
				},
			},
			"total_segments": 1,
		},
		"video_config": map[string]any{
			"style_mode": "电影电视剧",
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(parts.UserPrompt, "Total Segments: 1") || !strings.Contains(parts.UserPrompt, "包装结构里的字幕") {
		t.Fatalf("wrapped video context was not formatted:\n%s", parts.UserPrompt)
	}
}

func TestPromptBuilderFormatsFrameWorkflowNarrativeContextAndSpeakers(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	builder := NewBuilder(service)
	parts, err := builder.Build("script", map[string]any{
		"video_context": map[string]any{
			"items": map[string]any{
				"1": map[string]any{
					"start_time": 0.5,
					"end_time":   1.5,
					"subtitle":   "别过来。",
					"visual":     "人物奔跑后转头",
					"speaker_id": "speaker_0",
				},
			},
			"narrative_context": map[string]any{
				"summary": "两人因追逃发生冲突",
				"relationships": []any{
					map[string]any{"source": "speaker_0", "target": "speaker_1", "relationship": "对立"},
				},
				"emotion_curve": []any{
					map[string]any{"character": "speaker_0", "emotion": "紧张", "intensity": 4},
				},
			},
		},
		"video_config": map[string]any{
			"style_mode":      "电影电视剧",
			"target_language": "zh-CN",
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, want := range []string{"全局剧情理解", "两人因追逃发生冲突", "relationships", "(说话人 speaker_0)", "人物奔跑后转头"} {
		if !strings.Contains(parts.UserPrompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, parts.UserPrompt)
		}
	}
}

func TestPromptBuilderAvoidsRepeatingSharedContextWhenCacheContextIsPresent(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	builder := NewBuilder(service)
	parts, err := builder.Build("script", map[string]any{
		"cache_context": map[string]any{
			"segments": []any{map[string]any{"text": "这段应只在共享缓存上下文中出现"}},
		},
		"video_context": map[string]any{
			"items": map[string]any{
				"1": map[string]any{
					"start_time": 0.5,
					"end_time":   1.5,
					"subtitle":   "这段应只在共享缓存上下文中出现",
					"visual":     "视觉摘要",
				},
			},
			"narrative_context": map[string]any{
				"summary": "剧情冲突升级",
			},
		},
		"video_config": map[string]any{
			"style_mode":      "电影电视剧",
			"target_language": "zh-CN",
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(parts.UserPrompt, "可缓存共享素材上下文") {
		t.Fatalf("missing shared context pointer:\n%s", parts.UserPrompt)
	}
	if strings.Contains(parts.UserPrompt, "Total Segments") || strings.Contains(parts.UserPrompt, "视觉摘要") {
		t.Fatalf("prompt duplicated segment context despite cache_context:\n%s", parts.UserPrompt)
	}
	if !strings.Contains(parts.UserPrompt, "剧情冲突升级") {
		t.Fatalf("prompt should keep narrative context in task suffix:\n%s", parts.UserPrompt)
	}
}

func TestNarrationOutlineUsesOnlySelectedEvents(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	parts, err := NewBuilder(service).Build("script_draft", map[string]any{
		"model_operation": "narration_outline",
		"video_config":    map[string]any{"style_mode": "电影电视剧", "target_language": "zh-CN"},
		"video_context": map[string]any{
			"operation":         "narration_outline",
			"story_event_index": []any{map[string]any{"event_id": "event-001"}},
			"selection_policy":  map[string]any{"omitted_event_count": 30},
		},
	})
	if err != nil {
		t.Fatalf("build narration outline: %v", err)
	}
	for _, required := range []string{"Only use story_event_index", "Omitted events are intentionally excluded"} {
		if !strings.Contains(parts.UserPrompt, required) {
			t.Fatalf("narration outline prompt missing %q", required)
		}
	}
}

func TestScriptMatchPromptLocksEveryNarrationItemToItsOwnAnchor(t *testing.T) {
	service, err := NewService("../../prompt")
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	parts, err := NewBuilder(service).Build("script_match", map[string]any{
		"video_config":  map[string]any{"style_mode": "电影电视剧", "target_language": "en-US"},
		"video_context": map[string]any{"operation": "script_match"},
		"script_items": []any{map[string]any{
			"_id": 1, "text": "narration", "source_start": 12, "source_end": 18,
			"event_ids": []string{"event-001"}, "evidence_refs": []string{"subtitle-1"},
		}},
	})
	if err != nil {
		t.Fatalf("build script_match: %v", err)
	}
	for _, required := range []string{"source_start", "source_end", "own evidence window", "must not modify anchor fields"} {
		if !strings.Contains(parts.UserPrompt, required) {
			t.Fatalf("script_match prompt missing %q:\n%s", required, parts.UserPrompt)
		}
	}
}
