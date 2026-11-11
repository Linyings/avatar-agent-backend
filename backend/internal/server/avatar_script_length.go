package server

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/narratoai/backend-go/internal/avatar"
)

// scriptCharsPerSecond is the spoken pace the native-speech takes are budgeted
// for (avatar.briskUnitsPerSecond); the script length must use the same pace or
// a "two-minute" request turns into a four-minute film.
const scriptCharsPerSecond = 4.0

var durationPattern = regexp.MustCompile(`(\d+(?:\.\d+)?|[一二两三四五六七八九十半]+)\s*(分钟|分|min|秒钟|秒|s\b)(?:\s*(\d+)\s*秒)?`)

func chineseNumber(s string) (float64, bool) {
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v, true
	}
	digits := map[rune]float64{'一': 1, '二': 2, '两': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	switch {
	case s == "半":
		return .5, true
	case s == "十":
		return 10, true
	}
	runes := []rune(s)
	if len(runes) == 1 {
		v, ok := digits[runes[0]]
		return v, ok
	}
	if len(runes) == 2 && runes[0] == '十' {
		v, ok := digits[runes[1]]
		return 10 + v, ok
	}
	if len(runes) >= 2 && runes[1] == '十' {
		tens, ok := digits[runes[0]]
		if !ok {
			return 0, false
		}
		v := tens * 10
		if len(runes) == 3 {
			ones, ok := digits[runes[2]]
			return v + ones, ok
		}
		return v, true
	}
	return 0, false
}

// requestedSeconds finds the first explicit duration in the analysed
// constraints, then the topic. Nothing found means no length target.
func requestedSeconds(req *avatar.RequirementsAnalysis, topic string) (float64, bool) {
	texts := []string{}
	if req != nil {
		texts = append(texts, req.Constraints...)
	}
	texts = append(texts, topic)
	for _, text := range texts {
		for _, m := range durationPattern.FindAllStringSubmatch(text, -1) {
			v, ok := chineseNumber(m[1])
			if !ok || v <= 0 {
				continue
			}
			seconds := v
			if strings.HasPrefix(m[2], "分") || m[2] == "min" {
				seconds = v * 60
				if m[3] != "" {
					extra, _ := strconv.ParseFloat(m[3], 64)
					seconds += extra
				}
			}
			if seconds >= 5 && seconds <= 3600 {
				return seconds, true
			}
		}
	}
	return 0, false
}

// scriptCharBudget converts a requested duration into a spoken-character range
// (±10%) at the native-speech pace.
func scriptCharBudget(req *avatar.RequirementsAnalysis, topic string) (int, int, bool) {
	seconds, ok := requestedSeconds(req, topic)
	if !ok {
		return 0, 0, false
	}
	target := seconds * scriptCharsPerSecond
	return int(math.Round(target * .9)), int(math.Round(target * 1.1)), true
}

func scriptLengthRule(req *avatar.RequirementsAnalysis, topic string) string {
	lo, hi, ok := scriptCharBudget(req, topic)
	if !ok {
		return "篇幅遵循内容需要，不为凑时长重复台词。"
	}
	seconds, _ := requestedSeconds(req, topic)
	return fmt.Sprintf("用户要求成片约 %.0f 秒，口播按每秒约 4 字的语速计算，正文控制在 %d–%d 字（中文字、数字各计 1 字，英文单词约计 2 字）；超出时优先删减次要信息和铺垫，保留核心事实，不要写成更长的版本。", seconds, lo, hi)
}
