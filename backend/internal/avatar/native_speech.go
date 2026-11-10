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

// Request budgets only: downstream timing comes from the returned video.
func NativeSpeechPieces(text string) []NativeSpeechPiece {
	remaining := []rune(text)
	var out []NativeSpeechPiece
	for len(remaining) > 0 {
		end := min(80, len(remaining))
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
		units := 0
		for _, r := range part {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				units++
			}
		}
		out = append(out, NativeSpeechPiece{ID: fmt.Sprintf("c%d", len(out)+1), Text: part, Duration: max(4, min(30, int(math.Ceil(float64(units)/3.5))+2))})
	}
	return out
}

func NativeSpeechPrompt(text, direction string, voiceReference, avatar bool) string {
	visual := "单人正脸中景口播，直视镜头，完整头肩，固定机位和焦距。参考图片只用于人物身份与外观。自然手势，嘴部清楚，保持真实皮肤与跨帧稳定性。"
	if !avatar {
		visual = "生成简洁稳定的背景画面，用一位自然的中文讲述者说出下方台词。"
	}
	voice := "使用模型默认的自然中文音色。"
	if voiceReference {
		voice = "参考音频只参考音色、声线和说话风格，不复述参考音频的内容。必须朗读下方新台词。"
	}
	return visual + "\n" + voice + "声音与画面在同一次生成中同步输出。清晰普通话，正常语速，尽早开始说话，完整读完再结束，不漏字、不改写、不重复、不增加开场或结尾。只保留这一位讲述者的人声，无配乐、无音效、无字幕、无水印。\n画面与表演要求：" + direction + "\n必须逐字朗读的台词：\n「" + text + "」"
}
