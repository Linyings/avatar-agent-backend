package avatar

import "testing"

// Motion beside a presenter is planned as a motion-led split; the overlay must
// keep that layout instead of being forced into a picture-in-picture.
func TestVisualSplitIsAValidMotionLedComposition(t *testing.T) {
	if !ValidPresentationRecipe("visual-split") {
		t.Fatal("visual-split not accepted")
	}
	segs := []Segment{{ID: "p1", Text: "原理是这样的。", Duration: 4}}
	media := []Material{{NeedID: "host", SegmentID: "p1", Kind: "avatar", Duration: 4}, {NeedID: "anim", SegmentID: "p1", Kind: "animation", Duration: 4}}
	shots := []WorkflowShot{{ID: "S1", SegmentID: "p1", Start: 0, End: 4, Primary: "anim", Overlay: "host", Recipe: "visual-split", Layout: "split", Card: "none"}}
	NormalizeShotPresentation(shots)
	EnrichShotPresentation(shots, segs, media, nil)
	if shots[0].Recipe != "visual-split" || shots[0].Layout != "split" {
		t.Fatalf("split rewritten: %#v", shots[0])
	}
	MapAvatarShotSources(shots, segs, media)
	if err := ValidateWorkflowShots(shots, segs, media, 4); err != nil {
		t.Fatal(err)
	}
	c := PresentationCandidates(segs, media, nil)[0]
	found := false
	for _, r := range c.Recipes {
		found = found || r == "visual-split"
	}
	if !found {
		t.Fatalf("visual-split not offered when both media exist: %#v", c.Recipes)
	}
}

// A motion-only shot with no presenter is allowed; narration continues.
func TestMotionOnlyShotNeedsNoPresenter(t *testing.T) {
	segs := []Segment{{ID: "p1", Text: "原理是这样的。", Duration: 4}}
	media := []Material{{NeedID: "host", SegmentID: "p1", Kind: "avatar", Duration: 4}, {NeedID: "anim", SegmentID: "p1", Kind: "animation", Duration: 4}}
	shots := []WorkflowShot{{ID: "S1", SegmentID: "p1", Start: 0, End: 4, Primary: "anim", Recipe: "visual-hero", Layout: "full", Card: "none"}}
	EnrichShotPresentation(shots, segs, media, nil)
	if shots[0].Overlay != "" || shots[0].Recipe != "visual-hero" {
		t.Fatalf("presenter forced into a motion-only shot: %#v", shots[0])
	}
	if err := ValidateWorkflowShots(shots, segs, media, 4); err != nil {
		t.Fatal(err)
	}
}
