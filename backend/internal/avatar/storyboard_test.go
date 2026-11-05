package avatar

import "testing"

func TestStoryboardRequiresExactOrderedCoverage(t *testing.T) {
	sentences := SplitScript("你好，欢迎体验。这里保留声音！最后生成视频。")
	shots := FixedStoryboard(sentences)
	if err := ValidateStoryboard(shots, sentences); err != nil {
		t.Fatal(err)
	}
	shots[1].SentenceIDs = []int{0}
	if ValidateStoryboard(shots, sentences) == nil {
		t.Fatal("accepted repeated sentence")
	}
}

func TestStoryboardRejectsInventedTextAndUnsafeChoices(t *testing.T) {
	s := SplitScript("开场。讲解。总结。")
	p := FixedStoryboard(s)
	p[1].Text = "虚构台词"
	if ValidateStoryboard(p, s) == nil {
		t.Fatal("accepted rewritten script")
	}
	p = FixedStoryboard(s)
	p[1].Angle = "背面"
	if ValidateStoryboard(p, s) == nil {
		t.Fatal("accepted unsupported camera")
	}
	p = FixedStoryboard(s)
	p[0].Keyword = "不在稿件里"
	if ValidateStoryboard(p, s) == nil {
		t.Fatal("accepted invented anchor")
	}
}
