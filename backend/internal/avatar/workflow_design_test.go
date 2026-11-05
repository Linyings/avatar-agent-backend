package avatar

import "testing"

func TestPresentationRecipesValidateWithoutChangingSourceTiming(t *testing.T) {
	segs := []Segment{{ID: "p1", Text: "首先确认声音。然后准备素材。", Duration: 4}}
	media := []Material{{NeedID: "a1", SegmentID: "p1", Kind: "avatar", Duration: 4}}
	for _, recipe := range []string{"host-hero", "host-explain", "visual-hero", "visual-pip", "compare", "steps", "evidence", "summary"} {
		shots := []WorkflowShot{{ID: "S1", SegmentID: "p1", Start: 0, End: 4, Primary: "a1", Card: "none", Layout: "full", Recipe: recipe}}
		if err := ValidateWorkflowShots(shots, segs, media, 4); err != nil {
			t.Fatalf("%s: %v", recipe, err)
		}
	}
	shots := []WorkflowShot{{ID: "S1", SegmentID: "p1", Start: 0, End: 4, Primary: "a1", Card: "none", Layout: "full", Recipe: "steps", Items: []string{"首先确认声音", "凭空编造"}}}
	EnrichShotPresentation(shots, segs, media, nil)
	if len(shots[0].Items) != 2 || shots[0].Items[1] != "然后准备素材" {
		t.Fatalf("unrelated text leaked into graphics: %#v", shots)
	}
	shots[0].PrimaryIn = 1
	if ValidateWorkflowShots(shots, segs, media, 4) == nil {
		t.Fatal("design must not bypass audio source coverage")
	}
}

func TestRecipeCandidatesReflectMeaningAndAvailableMedia(t *testing.T) {
	segments := []Segment{{ID: "p1", Text: "首先确认声音，然后准备素材。"}, {ID: "p2", Text: "旧方案与新方案相比有什么区别？"}}
	media := []Material{{NeedID: "a", SegmentID: "p1", Kind: "avatar"}, {NeedID: "b", SegmentID: "p2", Kind: "animation"}}
	brief := PresentationCandidates(segments, media, nil)
	if brief[0].Recommended != "steps" || brief[1].Semantic != "对比" || brief[1].Recommended != "visual-hero" {
		t.Fatalf("not content driven: %#v", brief)
	}
	for _, c := range brief {
		for _, r := range c.Recipes {
			if r == "visual-pip" {
				t.Fatal("pip requires two ready kinds of media")
			}
		}
	}
}

func TestRepeatedDesignOnlyProducesAdvisory(t *testing.T) {
	shots := []WorkflowShot{{Recipe: "host-hero"}, {Recipe: "host-hero"}, {Recipe: "host-hero"}, {Recipe: "host-hero"}}
	if len(PresentationWarnings(shots)) == 0 {
		t.Fatal("missing design reminder")
	}
}
