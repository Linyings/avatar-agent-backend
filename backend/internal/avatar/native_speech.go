package avatar

import (
	"fmt"
	"math"
	"strings"
	"unicode"
)

const NativeSpeechMode = "native-video"
const NativeSpeechModel = "seedance-2.5-token"

func NativeSpeech(in WorkflowInput) bool { return in.SpeechMode == NativeSpeechMode }

// F04 has already produced the person. F05 can only arrange that existing
// footage; it must not invent additional cameras or unexecuted visual prompts.
func NativeMaterialNeeds(in WorkflowInput, segments []Segment, needs []MaterialNeed) []MaterialNeed {
	if !NativeSpeech(in) {
		return needs
	}
	out := make([]MaterialNeed, 0, len(needs)+len(segments))
	for _, need := range needs {
		if need.Kind != "avatar" {
			out = append(out, need)
		}
	}
	if in.UseAvatar {
		for _, seg := range segments {
			out = append(out, MaterialNeed{ID: "native_" + seg.ID, SegmentID: seg.ID, Kind: "avatar", Duration: seg.Duration, Angle: "正面", Content: "复用本段 Seedance 原声人物视频", Prompt: in.Direction + "\n" + seg.NativePrompt})
		}
	}
	return out
}

type NativeSpeechPiece struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Duration int    `json:"duration"`
}

// briskUnitsPerSecond is the spoken pace the request budget is sized for.
// Seedance fills the whole requested duration, so this sets delivery speed:
// observed takes at the old budget ran ~2.6 units/s, which sounded slow.
// 4.0 units/s is about 1.5x that, a brisk but natural broadcast pace.
const briskUnitsPerSecond = 4.0

// spokenUnits approximates syllables: one per CJK character or digit, one per
// letter of an all-caps acronym (spelled out), and about one per 2.5 letters of
// an ordinary Latin word (brand names are read slower than their letter count
// suggests).
func spokenUnits(text string) int {
	units := 0
	word := []rune{}
	prevDigit := false
	flush := func() {
		if len(word) == 0 {
			return
		}
		upper := true
		for _, r := range word {
			if !unicode.IsUpper(r) {
				upper = false
				break
			}
		}
		if upper && len(word) <= 5 {
			units += len(word)
		} else {
			units += max(1, int(math.Ceil(float64(len(word))/2.5)))
		}
		word = word[:0]
	}
	for _, r := range text {
		digit := false
		switch {
		case unicode.Is(unicode.Han, r):
			flush()
			units++
		case unicode.IsDigit(r):
			flush()
			units++
			digit = true
		case unicode.IsLetter(r):
			word = append(word, r)
		default:
			flush()
			// A decimal point is read aloud ("五点二").
			if r == '.' && prevDigit {
				units++
			}
		}
		prevDigit = digit
	}
	flush()
	return units
}

// Request budgets only: downstream timing comes from the returned video.
func NativeSpeechPieces(text string) []NativeSpeechPiece {
	remaining := []rune(text)
	var out []NativeSpeechPiece
	for len(remaining) > 0 {
		end := min(80, len(remaining))
		// A short leftover would become its own 4-second minimum take and be
		// read slowly to fill it; keep it with the current piece instead.
		if len(remaining)-end < 12 && len(remaining) <= 92 {
			end = len(remaining)
		}
		if end < len(remaining) {
			for i := end - 1; i >= end/2; i-- {
				if strings.ContainsRune("。！？；，,.!?; \n", remaining[i]) {
					end = i + 1
					break
				}
			}
		}
		part := string(remaining[:end])
		remaining = remaining[end:]
		// Seedance requires at least 4 seconds.
		seconds := int(math.Round(float64(spokenUnits(part)) / briskUnitsPerSecond))
		out = append(out, NativeSpeechPiece{ID: fmt.Sprintf("c%d", len(out)+1), Text: part, Duration: max(4, min(30, seconds))})
	}
	return out
}

// nativeDirection keeps only the performance notes that apply to the presenter
// take. The project direction also carries whole-film requests ("render the
// final film", "layer animation over the presenter"); handing those to the
// video model makes it draw the effects into the take itself.
func nativeDirection(direction string) string {
	cut := []string{"动效", "特效", "分层", "融合", "逐帧", "渲染", "成片", "字幕", "剪辑", "包装", "转场", "片头"}
	keep := []string{}
	for _, part := range strings.FieldsFunc(direction, func(r rune) bool {
		return r == '\n' || r == '。' || r == '；' || r == ';' || r == '！' || r == '!'
	}) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		blocked := false
		for _, word := range cut {
			if strings.Contains(part, word) {
				blocked = true
				break
			}
		}
		if !blocked {
			keep = append(keep, part)
		}
	}
	return strings.Join(keep, "；")
}

func NativeSpeechPrompt(text, direction string, voiceReference, avatar bool) string {
	// The take is used as shot (no keying), so the backdrop comes from the
	// reference photo. Articulation is deliberately restrained: asking for a
	// "clear mouth" made the model exaggerate jaw and throat movement.
	visual := "人物单人正脸中景口播，直视镜头，完整头肩，固定机位和焦距，背景与参考图片保持一致，干净简洁，不添加任何图形、动效或特效。参考图片只用于人物身份、外观与背景。" +
		"表演克制自然，像资深新闻主播：嘴唇开合幅度小，口型准确但不夸张；下颌和喉部动作轻微，脖子不明显起伏；头部基本稳定，只有轻微点头；表情平和，少量自然手势。保持真实皮肤与跨帧稳定性。"
	if !avatar {
		visual = "生成简洁稳定的背景画面，用一位自然的中文讲述者说出下方台词。"
	}
	voice := "使用模型默认的自然中文音色。"
	if voiceReference {
		voice = "参考音频只参考音色、声线和说话风格，不复述参考音频的内容。必须朗读下方新台词。"
	}
	performance := ""
	// Only on-camera performance notes survive, so the model never bakes
	// effects into the source.
	if avatar {
		if notes := nativeDirection(direction); notes != "" {
			performance = "\n表演要求（仅人物动作、表情、语气；不要绘制任何图形或特效）：" + notes
		}
	} else if direction != "" {
		performance = "\n画面与表演要求：" + direction
	}
	return visual + "\n" + voice + "声音与画面在同一次生成中同步输出。清晰普通话，语速明显偏快，约为日常说话的 1.5 倍，节奏紧凑利落，句间停顿短，开口即说，读完立刻收尾；不拖音、不放慢、不漏字、不改写、不重复、不增加开场或结尾。只保留这一位讲述者的人声，无配乐、无音效、无字幕、无水印。" + performance + "\n必须逐字朗读的台词：\n「" + text + "」"
}
