package avatar

import "testing"

func TestWorkflowPresentationFallbackUsesActualMaterialCoverageAndAudioOffset(t *testing.T) {
	segments := []Segment{{ID: "p1", Start: 0, Duration: 5}, {ID: "p2", Start: 5, Duration: 4}}
	materials := []Material{{NeedID: "a1", SegmentID: "p1", Kind: "avatar", Duration: 5}, {NeedID: "m1", SegmentID: "p1", Kind: "animation", Duration: 3}, {NeedID: "a2", SegmentID: "p2", Kind: "avatar", Duration: 4}}
	shots := PresentationFallback(segments, materials)
	if err := ValidateWorkflowShots(shots, segments, materials, 9); err != nil {
		t.Fatal(err)
	}
	if len(shots) != 3 || shots[1].PrimaryIn != 3 {
		t.Fatalf("lost actual driven audio source offset: %#v", shots)
	}
}
