package prompts

import (
	"strings"
	"testing"
)

func humanizerRequest(style, purpose string) map[string]any {
	return map[string]any{
		"model_operation": "narration_outline",
		"video_config":    map[string]any{"style_mode": style, "target_language": "zh-CN", "person_view": "third_person"},
		"video_context":   map[string]any{"contract": "general-narration-v3", "purpose": purpose, "sourcePolicy": "video-events-v1", "narrationMixPolicy": "duration-ratio-v1", "narrationRatio": 0.5},
	}
}

func TestGeneralHumanizerSelectedTemplatesReachWriter(t *testing.T) {
	s, err := NewService("../../prompt")
	if err != nil {
		t.Fatal(err)
	}
	b := NewBuilder(s)
	count := 0
	for _, meta := range s.templates {
		if !strings.Contains(strings.ReplaceAll(meta.JSONPath, "\\", "/"), "TwoPhases/step1/") {
			continue
		}
		count++
		t.Run(meta.Title, func(t *testing.T) {
			req := humanizerRequest(meta.ID, "script")
			p, err := b.Build("script_draft", req)
			if err != nil {
				t.Fatal(err)
			}
			if p.Meta.ID != meta.ID {
				t.Fatalf("wrong style: %s", p.Meta.ID)
			}
			rendered, err := s.Render(meta.ID, b.templateContext(mapValue(req, "video_config"), "zh-CN"))
			if err != nil {
				t.Fatal(err)
			}
			style := generalSelectedStyle(rendered.UserPrompt)
			if style == "" || !strings.Contains(style, "创作风格") || !strings.Contains(p.UserPrompt, style) {
				t.Fatal("selected style missing")
			}
			for _, forbidden := range []string{"## 创作流程", "## 禁止事项", "## 文案输出要求"} {
				if strings.Contains(style, forbidden) {
					t.Fatalf("legacy output rules leaked: %s", forbidden)
				}
			}
			if !strings.Contains(p.UserPrompt, generalHumanizerSkill) || !strings.Contains(p.UserPrompt, `"originalClips"`) || !strings.Contains(p.UserPrompt, "[[ORIGINAL:id]]") {
				t.Fatal("skill or original contract missing")
			}
			if !strings.Contains(p.SystemPrompt, "JSON 契约和局部修复范围 >") || !strings.Contains(p.SystemPrompt, "所选风格与原片比例 > 去 AI 味要求") {
				t.Fatal("style/schema priority missing")
			}
		})
	}
	if count < 21 {
		t.Fatalf("only %d styles tested", count)
	}
}

func TestGeneralHumanizerDoesNotRewriteDownstreamOrMovie(t *testing.T) {
	s, err := NewService("../../prompt")
	if err != nil {
		t.Fatal(err)
	}
	b := NewBuilder(s)
	for _, purpose := range []string{"chapters", "retrieve", "match", "match-review", "original-layout", "editorial-review"} {
		req := humanizerRequest("短剧漫剧", purpose)
		p, err := b.Build("script_draft", req)
		if err != nil {
			t.Fatal(err)
		}
		system, user, _ := generalEditingOperationPrompt("script_draft", req)
		if p.SystemPrompt != system || p.UserPrompt != user {
			t.Fatalf("%s changed", purpose)
		}
		if b.GeneralEditingPromptFingerprint("script_draft", req) != GeneralEditingPromptFingerprint("script_draft", req) {
			t.Fatalf("%s identity changed", purpose)
		}
	}
	for _, operation := range []string{"narration_outline", "movie_director_script"} {
		req := map[string]any{"model_operation": operation, "video_config": map[string]any{"style_mode": "电影电视剧"}, "video_context": map[string]any{}}
		p, err := b.Build("script_draft", req)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(p.UserPrompt, "HUMANIZER-ZH-CN SKILL") {
			t.Fatal("skill leaked outside general workflow")
		}
	}
}

func TestGeneralHumanizerRepairsPreserveLocalAndRatioContracts(t *testing.T) {
	s, err := NewService("../../prompt")
	if err != nil {
		t.Fatal(err)
	}
	b := NewBuilder(s)
	for _, scope := range []string{"", "single-chapter-v1", "mix-continuity-v1"} {
		for _, ratio := range []float64{0, 0.5, 1} {
			req := humanizerRequest("温情治愈", "script")
			ctx := mapValue(req, "video_context")
			ctx["repairScope"], ctx["narrationRatio"], ctx["expectedText"] = scope, ratio, "必须逐字保留的原文。"
			p, err := b.Build("script_draft", req)
			if err != nil {
				t.Fatal(err)
			}
			_, original, _ := generalEditingOperationPrompt("script_draft", req)
			if !strings.HasSuffix(p.UserPrompt, original) {
				t.Fatal("task contract or data was dropped")
			}
			for _, required := range []string{"expectedText 存在时逐字恢复", "0% 解说仍只输出原片计划", "局部修复不得扩展到整稿", "不输出初稿、审校意见"} {
				if !strings.Contains(p.SystemPrompt, required) {
					t.Fatalf("repair safety missing: %s", required)
				}
			}
		}
	}
}

func TestGeneralHumanizerFingerprintTracksTemplateAndSkill(t *testing.T) {
	s, err := NewService("../../prompt")
	if err != nil {
		t.Fatal(err)
	}
	b := NewBuilder(s)
	req := humanizerRequest("温情治愈", "script")
	first := b.GeneralEditingPromptFingerprint("script_draft", req)
	if first == "" || first != b.GeneralEditingPromptFingerprint("script_draft", req) {
		t.Fatal("unstable identity")
	}
	if first == b.GeneralEditingPromptFingerprint("script_draft", humanizerRequest("幽默搞笑", "script")) {
		t.Fatal("style identity collided")
	}
	meta := b.resolveTemplate("script_draft", "温情治愈")
	meta.Messages = []TemplateMessage{{Role: "user", Content: "## 创作风格\n新的风格规则\n## 创作流程\n不应入稿"}}
	s.templates[meta.ID] = meta
	second := b.GeneralEditingPromptFingerprint("script_draft", req)
	if first == second {
		t.Fatal("template content update replays old draft")
	}
	old := generalHumanizerSkill
	t.Cleanup(func() { generalHumanizerSkill = old })
	generalHumanizerSkill += "\nUpdated editorial rule."
	if second == b.GeneralEditingPromptFingerprint("script_draft", req) {
		t.Fatal("skill update replays old draft")
	}
}
