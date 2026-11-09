package avatar

import "testing"

func TestMotionPlanRequiresNarrationGrounding(t *testing.T) {
	text := "研发投入110亿元，算力规模扩大。下一步开放接口。"
	valid := MotionPlan{Version: MotionPlanVersion, Tone: "cool", Scenes: []MotionScene{
		{Quote: "研发投入110亿元，算力规模扩大", Title: "研发投入", Layout: "data", Elements: []MotionElement{{Label: "110亿元", Kind: "coin"}, {Label: "算力规模扩大", Kind: "chip"}}},
		{Quote: "下一步开放接口", Title: "开放接口", Layout: "steps", Elements: []MotionElement{{Label: "开放接口", Kind: "code"}}},
	}}
	if err := ValidateMotionPlan(valid, text); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*MotionPlan){
		func(p *MotionPlan) { p.Scenes[0].Elements[0].Label = "999亿元" },
		func(p *MotionPlan) { p.Scenes[0].Quote = "Soft Landing" },
		func(p *MotionPlan) { p.Scenes[0].Elements[0].Kind = "https://demo.mp4" },
		func(p *MotionPlan) { p.Scenes[0], p.Scenes[1] = p.Scenes[1], p.Scenes[0] },
		func(p *MotionPlan) { p.Scenes[0].Layout = "javascript" },
	} {
		p := valid
		p.Scenes = append([]MotionScene(nil), valid.Scenes...)
		p.Scenes[0].Elements = append([]MotionElement(nil), valid.Scenes[0].Elements...)
		mutate(&p)
		if err := ValidateMotionPlan(p, text); err == nil {
			t.Fatal("ungrounded or unsupported plan accepted")
		}
	}
}

func TestMotionPlanGroundsParaphrasedHeadlinesWithoutInventingLabels(t *testing.T) {
	p := MotionPlan{Version: MotionPlanVersion, Tone: "warm", Scenes: []MotionScene{{Quote: "据 Bloomberg 援引知情人士报道", Title: "消息来源", Layout: "relationship", Elements: []MotionElement{{Label: "Bloomberg", Kind: "document"}}}}}
	GroundMotionTitles(&p)
	if p.Scenes[0].Title != "Bloomberg" || ValidateMotionPlan(p, p.Scenes[0].Quote) != nil {
		t.Fatal("headline did not use its supported short label")
	}
	p.Scenes[0].Title = "虚构信息"
	p.Scenes[0].Elements[0].Label = "999亿元"
	GroundMotionTitles(&p)
	if ValidateMotionPlan(p, p.Scenes[0].Quote) == nil {
		t.Fatal("unsupported data was silently accepted")
	}
}
