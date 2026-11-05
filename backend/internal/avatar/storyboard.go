package avatar

import (
	"fmt"
	"strings"
	"unicode"
)

type Shot struct {
	ID          string `json:"id"`
	SentenceIDs []int  `json:"sentence_ids"`
	Text        string `json:"text"`
	Angle       string `json:"angle"`
	Framing     string `json:"framing"`
	Prompt      string `json:"prompt"`
	Keyword     string `json:"keyword"`
	Card        string `json:"card"`
	Layout      string `json:"layout"`
}

var Angles = []string{"正面", "左前方20度", "右前方20度"}
var Framings = []string{"中景", "半身", "近景"}
var Cards = []string{"impact-open-title", "highlighter-sweep", "chapter-title-card"}

func SplitScript(text string) []string {
	var out []string
	var current strings.Builder
	for _, r := range strings.TrimSpace(text) {
		current.WriteRune(r)
		if strings.ContainsRune("。！？!?\n", r) {
			s := strings.TrimSpace(current.String())
			if s != "" {
				out = append(out, s)
			}
			current.Reset()
		}
	}
	if s := strings.TrimSpace(current.String()); s != "" {
		out = append(out, s)
	}
	return out
}
func FixedStoryboard(sentences []string) []Shot {
	result := make([]Shot, 3)
	for i := 0; i < 3; i++ {
		shot := Shot{ID: fmt.Sprintf("shot-%d", i+1), Angle: Angles[i], Framing: Framings[i], Card: Cards[i], Layout: "full"}
		if i == 1 {
			shot.Layout = "split"
		}
		for j := i * len(sentences) / 3; j < (i+1)*len(sentences)/3; j++ {
			shot.SentenceIDs = append(shot.SentenceIDs, j)
			shot.Text += sentences[j]
		}
		key := []rune(strings.TrimFunc(shot.Text, func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSpace(r) }))
		if len(key) > 8 {
			key = key[:8]
		}
		shot.Keyword = string(key)
		result[i] = shot
	}
	return result
}
func ValidateStoryboard(shots []Shot, sentences []string) error {
	if len(shots) != 3 || len(sentences) < 3 {
		return fmt.Errorf("稿件需至少三句，分镜必须为三镜")
	}
	next := 0
	for i, s := range shots {
		if s.ID != fmt.Sprintf("shot-%d", i+1) || s.Angle != Angles[i] || s.Framing != Framings[i] || s.Card != Cards[i] || (s.Layout != "full" && s.Layout != "split") {
			return fmt.Errorf("分镜包含不支持的镜头或组件")
		}
		var text strings.Builder
		if len(s.SentenceIDs) == 0 {
			return fmt.Errorf("镜头没有分配稿件")
		}
		for _, id := range s.SentenceIDs {
			if id != next || id >= len(sentences) {
				return fmt.Errorf("稿件分配遗漏、重复或顺序错误")
			}
			text.WriteString(sentences[id])
			next++
		}
		if s.Text != text.String() {
			return fmt.Errorf("分镜改写了口播原文")
		}
		if s.Keyword == "" || !strings.Contains(s.Text, s.Keyword) || len([]rune(s.Keyword)) > 18 {
			return fmt.Errorf("关键词必须引用本镜原文且不超过18字")
		}
		if len([]rune(s.Prompt)) > 1000 {
			return fmt.Errorf("镜头提示词过长")
		}
	}
	if next != len(sentences) {
		return fmt.Errorf("分镜未覆盖全部稿件")
	}
	return nil
}
func GenerationPrompt(s Shot) string {
	return fmt.Sprintf("以@图片1中的同一位人物为人物参考，保持脸部、发型、衣服、场景和光线一致。单镜头连续口播，无字幕、无文字、无背景音乐。相机位于人物%s，%s，机位稳定，仅轻微自然动作，人物看向镜头。严格使用@音频1作为该人物的全部对白及口型驱动，保留原录音的音色、发音、停顿、语速和逐字时间轴，嘴部准确同步；不要添加或删改任何字句。台词：%s。补充场景要求：%s", s.Angle, s.Framing, s.Text, s.Prompt)
}
