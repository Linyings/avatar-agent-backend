package avatar

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

const MotionPlanVersion = "content-motion/1"

type MotionElement struct {
	Label string `json:"label"`
	Kind  string `json:"kind"`
}
type MotionScene struct {
	Code     string          `json:"code,omitempty"`
	Quote    string          `json:"quote"`
	Title    string          `json:"title"`
	Layout   string          `json:"layout"`
	Elements []MotionElement `json:"elements"`
}

// Some providers place code on each scene. Preserve that observed design and
// dispatch on the validated quote without paying for a second generation.
func NormalizeMotionCode(p *MotionPlan) error {
	if strings.TrimSpace(p.Code) == "" {
		var code strings.Builder
		for _, scene := range p.Scenes {
			if strings.TrimSpace(scene.Code) == "" {
				return fmt.Errorf("动效模型缺少场景绘制源码")
			}
			quote, _ := json.Marshal(scene.Quote)
			fmt.Fprintf(&code, "if (scene.quote === %s) { (() => {\n%s\n})(); }\n", quote, scene.Code)
		}
		p.Code = code.String()
	}
	if len(p.Code) < 30 || len(p.Code) > 18000 {
		return fmt.Errorf("动效模型未返回有效的专用绘制源码")
	}
	for i := range p.Scenes {
		p.Scenes[i].Code = ""
	}
	return nil
}

type MotionPlan struct {
	Code          string        `json:"code,omitempty"`
	Version       string        `json:"version"`
	Tone          string        `json:"tone"`
	VisualContext string        `json:"visual_context"`
	Scenes        []MotionScene `json:"scenes"`
}

// Models sometimes paraphrase a headline despite the exact-quote contract.
// Use a short, supported label already selected for that scene instead. All
// scene quotes and labels still undergo full validation; facts are never fixed
// by guessing, and this typography repair needs no second provider request.
func GroundMotionTitles(p *MotionPlan) {
	for i := range p.Scenes {
		scene := &p.Scenes[i]
		if strings.TrimSpace(scene.Title) != "" && len([]rune(scene.Title)) <= 24 && strings.Contains(scene.Quote, scene.Title) {
			continue
		}
		for _, item := range scene.Elements {
			if strings.TrimSpace(item.Label) != "" && len([]rune(item.Label)) <= 24 && strings.Contains(scene.Quote, item.Label) {
				scene.Title = item.Label
				break
			}
		}
	}
}

// Only bounded declarative graphics reach the renderer. Every visible phrase
// must be supported by its spoken excerpt, including all numbers and units.
func ValidateMotionPlan(p MotionPlan, text string) error {
	if p.Version != MotionPlanVersion || len(p.Scenes) < 1 || len(p.Scenes) > 5 || !slices.Contains([]string{"warm", "cool", "neutral"}, p.Tone) || len([]rune(p.VisualContext)) > 800 {
		return fmt.Errorf("动效方案版本或场景数量无效")
	}
	cursor := 0
	for _, scene := range p.Scenes {
		quote := scene.Quote
		if strings.TrimSpace(quote) == "" || len([]rune(quote)) > 600 {
			return fmt.Errorf("动效场景缺少原文依据")
		}
		at := strings.Index(text[cursor:], quote)
		if at < 0 {
			return fmt.Errorf("动效场景必须按口播顺序引用当前原文")
		}
		cursor += at + len(quote)
		if scene.Title == "" || len([]rune(scene.Title)) > 24 || !strings.Contains(quote, scene.Title) || !slices.Contains([]string{"highlight", "data", "compare", "steps", "relationship", "summary"}, scene.Layout) || len(scene.Elements) < 1 || len(scene.Elements) > 4 {
			return fmt.Errorf("动效标题或构图缺少原文依据")
		}
		for _, item := range scene.Elements {
			if item.Label == "" || len([]rune(item.Label)) > 60 || !strings.Contains(quote, item.Label) || !slices.Contains([]string{"coin", "chart", "chip", "cloud", "people", "document", "shield", "globe", "clock", "code", "spark", "box", "leaf", "link"}, item.Kind) {
				return fmt.Errorf("动效元素必须使用本句原文及支持的图形，不能复制示例或编造数据")
			}
		}
	}
	return nil
}
