package avatar

import "testing"

func TestWorkflowSourceNeedBindsNarrationToUnderstoodSourceInterval(t *testing.T) {
	in := WorkflowInput{Assets: []SourceAsset{{ID: "video1", Kind: "video", Duration: 12}}}
	desc := []SourceDescription{{ID: "clip1", AssetID: "video1", Start: 2, End: 8, Text: "人物在窗边讲解"}}
	needs := []MaterialNeed{{ID: "n1", SegmentID: "p1", Kind: "real", SourceID: "video1", SourceStart: 3, Duration: 4}}
	if err := BindSourceDescriptions(in, desc, needs); err != nil || needs[0].DescriptionID != "clip1" {
		t.Fatalf("missing source association: %#v %v", needs, err)
	}
	needs[0].SourceStart = 7
	if BindSourceDescriptions(in, desc, needs) == nil {
		t.Fatal("unrelated/outside source interval accepted")
	}
}
