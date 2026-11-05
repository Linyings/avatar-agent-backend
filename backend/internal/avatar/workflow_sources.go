package avatar

import (
	"fmt"
	"unicode"
)

// BindSourceDescriptions keeps narration paragraph -> description -> source interval
// explicit. Still images have no source clock; uploaded videos require understanding.
func BindSourceDescriptions(in WorkflowInput, descriptions []SourceDescription, needs []MaterialNeed) error {
	for i := range needs {
		n := &needs[i]
		if n.Kind != "real" {
			continue
		}
		isVideo := false
		for _, a := range in.Assets {
			if a.ID == n.SourceID && a.Kind == "video" && !a.Excluded {
				isVideo = true
			}
		}
		if !isVideo {
			continue
		}
		found := false
		for _, d := range descriptions {
			if d.AssetID == n.SourceID && (n.DescriptionID == "" || n.DescriptionID == d.ID) && n.SourceStart >= d.Start-.02 && n.SourceStart+n.Duration <= d.End+.03 {
				n.DescriptionID = d.ID
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("真实视频需求 %s 没有对应已理解片段文案及源区间", n.ID)
		}
	}
	return nil
}

func ValidateSubtitleEdits(in WorkflowInput, segments []Segment) error {
	count := func(s string) int {
		n := 0
		for _, c := range s {
			if unicode.IsLetter(c) || unicode.IsDigit(c) {
				n++
			}
		}
		return n
	}
	for id, text := range in.SubtitleEdits {
		found := false
		for _, p := range segments {
			if p.ID == id {
				found = true
				if count(text) != count(p.Text) {
					return fmt.Errorf("字幕更正须保持朗读字数；改变解说请使用局部文案修改")
				}
			}
		}
		if !found {
			return fmt.Errorf("字幕更正段落不存在")
		}
	}
	return nil
}
