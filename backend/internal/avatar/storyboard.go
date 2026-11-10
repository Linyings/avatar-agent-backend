package avatar

import (
	"fmt"
	"regexp"
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

var avatarDirectionClauses = regexp.MustCompile(`[。！？!?；;\n\r，,]+|\.\s+`)
var avatarCompositionDirective = regexp.MustCompile(`(?i)圆窗|圆形|圆角|矩形|小窗|画中画|分屏|拼[贴接]|叠[加放]|蒙版|遮罩|裁[剪切]|抠[像图]|字幕|边框|图层|右下角|左下角|右上角|左上角|推[近进拉]|拉[近远]|变焦|运镜|摇[镜臂]|镜头.*(?:移动|缩放)|(?:镜头|相机).*(?:旋转|环绕)|zoom|pan(?:ning)?\b|dolly|camera\s+mov|picture.?in.?picture|\bpip\b|split.?screen|overlay|mask|crop|rounded|circular|circle|border|subtitle`)

// Keep scene/appearance direction separate from postproduction. The original
// need remains available for audit; only the provider-facing text is filtered.
func CleanAvatarDirection(value string) string {
	var kept []string
	for _, clause := range avatarDirectionClauses.Split(value, -1) {
		clause = strings.TrimSpace(clause)
		if clause != "" && !avatarCompositionDirective.MatchString(clause) {
			kept = append(kept, clause)
		}
	}
	return strings.Join(kept, "。")
}

func GenerationPrompt(s Shot) string {
	// Postproduction crops this footage into square or round windows; a close-up
	// leaves no room to fill them while keeping the head.
	framing := strings.NewReplacer("特写", "", "近景", "").Replace(CleanAvatarDirection(s.Framing))
	if strings.Trim(framing, "，,。 ") == "" {
		framing = "自然上半身"
	}
	return fmt.Sprintf("以@图片1中的同一位人物为人物参考，保持脸部、发型、衣服、场景和光线一致。补充外观与环境要求：%s。素材交付要求：只生成一位主体人物的干净口播素材，普通矩形全画幅，背景完整连续；不在画面中嵌入额外人物视频、装饰或其他图层。相机位于人物%s，%s，腰部以上中景，人物居中，脸部高度约占画面高度的五分之一，不要近景或特写；完整保留头顶、头发、脸部、下巴、肩部和上半身，头顶上方及左右留有后期取景余量。固定焦距、固定机位、固定拍摄距离，整段人物大小稳定；全程无推拉摇移、无变焦、无自动重新取景、无镜头切换，仅轻微自然表情和动作。无字幕、无文字、无背景音乐。严格使用@音频1作为该人物的全部对白及口型驱动，保留原录音的音色、发音、停顿、语速和逐字时间轴，嘴部准确同步；不要添加或删改任何字句。台词：%s。", CleanAvatarDirection(s.Prompt), s.Angle, framing, s.Text)
}
