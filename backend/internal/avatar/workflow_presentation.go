package avatar

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

const PresentationVersion = "film-design/1"

var PresentationRecipes = []string{"host-hero", "host-explain", "visual-hero", "visual-pip", "compare", "steps", "evidence", "summary"}

func ValidPresentationRecipe(value string) bool {
	for _, recipe := range PresentationRecipes {
		if value == recipe {
			return true
		}
	}
	return false
}
func ValidPresentationCard(value string) bool {
	for _, card := range []string{"none", "keyword", "definition", "steps", "compare", "data", "summary", "impact-open-title", "highlighter-sweep", "chapter-title-card"} {
		if value == card {
			return true
		}
	}
	return false
}

type PresentationCandidate struct {
	SegmentID   string   `json:"segment_id"`
	Semantic    string   `json:"semantic"`
	Recipes     []string `json:"recipes"`
	Recommended string   `json:"recommended"`
	Items       []string `json:"source_phrases"`
}

var phraseSeparator = regexp.MustCompile(`[。；！？\n，、]+`)

func presentationItems(text string) []string {
	items := []string{}
	for _, item := range phraseSeparator.Split(text, -1) {
		if value := strings.TrimSpace(item); value != "" {
			items = append(items, value)
			if len(items) == 4 {
				break
			}
		}
	}
	return items
}
func PresentationCandidates(segments []Segment, materials []Material, needs []MaterialNeed) []PresentationCandidate {
	out := []PresentationCandidate{}
	for _, seg := range segments {
		host, visual, real := false, false, false
		for _, m := range materials {
			if m.SegmentID == seg.ID {
				host = host || m.Kind == "avatar"
				visual = visual || m.Kind != "avatar"
				real = real || m.Kind == "real"
			}
		}
		semantic, recipe := "论点", "host-hero"
		if visual {
			recipe = "visual-hero"
		}
		for _, rule := range []struct {
			words       []string
			sem, recipe string
		}{
			{[]string{"首先", "第一步", "其次", "然后", "步骤"}, "步骤", "steps"},
			{[]string{"相比", "对比", "区别", "优点", "缺点", "而不是"}, "对比", "compare"},
			{[]string{"总结", "总之", "记住", "最后提醒"}, "金句", "summary"},
			{[]string{"什么是", "指的是", "定义", "所谓"}, "定义", "host-explain"},
			{[]string{"数据", "百分之", "%", "％", "统计", "实测", "证据"}, "数据", "evidence"},
		} {
			matched := false
			for _, word := range rule.words {
				matched = matched || strings.Contains(seg.Text, word)
			}
			if matched {
				semantic, recipe = rule.sem, rule.recipe
				break
			}
		}
		for _, n := range needs {
			if n.SegmentID == seg.ID && n.Kind == "animation" && semantic == "论点" {
				switch n.Template {
				case "steps":
					semantic, recipe = "步骤", "steps"
				case "compare":
					semantic, recipe = "对比", "compare"
				case "summary":
					semantic, recipe = "金句", "summary"
				case "data":
					semantic, recipe = "数据", "evidence"
				}
			}
		}
		choices := []string{"visual-hero"}
		if host {
			choices = []string{"host-hero", "host-explain", "compare", "steps", "evidence", "summary"}
		}
		if visual && host {
			choices = append(choices, "visual-hero", "visual-pip")
		}
		if real && !host {
			choices = append(choices, "evidence")
		}
		if !host && recipe != "evidence" {
			recipe = "visual-hero"
		}
		out = append(out, PresentationCandidate{seg.ID, semantic, choices, recipe, presentationItems(seg.Text)})
	}
	return out
}

// Only exact narration excerpts may be rendered as facts, labels or quantities.
func EnrichShotPresentation(shots []WorkflowShot, segments []Segment, materials []Material, needs []MaterialNeed) {
	candidates := PresentationCandidates(segments, materials, needs)
	for i := range shots {
		s := &shots[i]
		var c PresentationCandidate
		var text, kind string
		for j, seg := range segments {
			if seg.ID == s.SegmentID {
				c = candidates[j]
				text = seg.Text
			}
		}
		for _, m := range materials {
			if m.NeedID == s.Primary {
				kind = m.Kind
			}
		}
		if !ValidPresentationRecipe(s.Recipe) {
			s.Recipe = c.Recommended
			if kind != "avatar" {
				s.Recipe = "visual-hero"
				if s.Overlay != "" {
					s.Recipe = "visual-pip"
				}
			}
		}
		if kind != "avatar" && s.Recipe != "visual-pip" {
			if s.Recipe != "evidence" {
				s.Recipe = "visual-hero"
			}
			s.Card = "none"
		}
		if s.Recipe == "visual-pip" && s.Overlay == "" {
			s.Recipe = "visual-hero"
		}
		if len(s.Semantic) == 0 {
			s.Semantic = []string{c.Semantic}
		}
		valid := len(s.Items) > 0 && len(s.Items) <= 4
		for _, item := range s.Items {
			valid = valid && strings.TrimSpace(item) != "" && strings.Contains(text, item)
		}
		if !valid {
			s.Items = c.Items
		}
		if s.Keyword != "" && !strings.Contains(text, s.Keyword) {
			s.Keyword = ""
		}
		if s.Card == "" {
			s.Card = "none"
		}
		if s.Recipe == "host-hero" && s.Card == "highlighter-sweep" {
			s.Card = "keyword"
		}
	}
}

func PresentationWarnings(shots []WorkflowShot) []string {
	for i := 2; i < len(shots); i++ {
		if shots[i].Recipe != "" && shots[i].Recipe == shots[i-1].Recipe && shots[i].Recipe == shots[i-2].Recipe {
			return []string{"连续三个镜头使用相同构图，请在预览中检查节奏；已继续制作，不自动增加镜头或重新生成"}
		}
	}
	return nil
}

// PresentationFallback uses ready media without changing narration timing.
func PresentationFallback(segments []Segment, materials []Material) []WorkflowShot {
	shots := []WorkflowShot{}
	for _, seg := range segments {
		var host, other Material
		for _, m := range materials {
			if m.SegmentID != seg.ID {
				continue
			}
			if m.Kind == "avatar" && host.NeedID == "" {
				host = m
			}
			if m.Kind != "avatar" && other.NeedID == "" {
				other = m
			}
		}
		offset := 0.0
		if other.NeedID != "" {
			duration := math.Min(other.Duration, seg.Duration)
			shot := WorkflowShot{ID: fmt.Sprintf("S%d", len(shots)+1), SegmentID: seg.ID, Start: seg.Start, End: seg.Start + duration, Primary: other.NeedID, Layout: "full", Card: "highlighter-sweep", Intent: "呈现对应知识点的已就绪素材", SelectionReason: seg.Text, Semantic: []string{"论点"}}
			if host.NeedID != "" {
				shot.Overlay = host.NeedID
				if strings.Contains(seg.Text, "画面中") || strings.Contains(seg.Text, "看这段") {
					shot.Layout = "pip"
				} else {
					shot.Overlay = ""
				}
			}
			shots = append(shots, shot)
			offset = duration
		}
		if offset < seg.Duration-.001 && host.NeedID != "" {
			shots = append(shots, WorkflowShot{ID: fmt.Sprintf("S%d", len(shots)+1), SegmentID: seg.ID, Start: seg.Start + offset, End: seg.Start + seg.Duration, Primary: host.NeedID, PrimaryIn: offset, Layout: "full", Card: "highlighter-sweep", Intent: "人物继续本段解说", SelectionReason: seg.Text, Semantic: []string{"论点"}})
		}
	}
	if len(shots) > 0 {
		shots[0].Card = "impact-open-title"
		if len(shots) > 1 {
			shots[len(shots)-1].Card = "chapter-title-card"
		}
	}
	EnrichShotPresentation(shots, segments, materials, nil)
	MapAvatarShotSources(shots, segments, materials)
	return shots
}
func NormalizeShotPresentation(shots []WorkflowShot) bool {
	changed := false
	for i := range shots {
		s := &shots[i]
		if !ValidPresentationCard(s.Card) {
			s.Card = "none"
			changed = true
		}
		if s.Recipe != "" && !ValidPresentationRecipe(s.Recipe) {
			s.Recipe = ""
			changed = true
		}
		if s.Layout != "full" && s.Layout != "pip" && s.Layout != "split" {
			s.Layout = "full"
			changed = true
		}
		if s.Overlay != "" && s.Layout == "full" {
			s.Layout = "pip"
		}
	}
	return changed
}
