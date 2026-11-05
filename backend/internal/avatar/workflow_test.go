package avatar

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestWorkflowSegmentsPreserveConfirmedScript(t *testing.T) {
	segments := []Segment{{ID: "p1", Text: "第一句。"}, {ID: "p2", Text: "第二句。"}}
	if err := ValidateSegments("第一句。第二句。", segments); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]Segment{{{ID: "p1", Text: "第一句。"}}, {{ID: "p1", Text: "第二句。"}, {ID: "p2", Text: "第一句。"}}, {{ID: "p1", Text: "第一句。"}, {ID: "p1", Text: "第二句。"}}} {
		if ValidateSegments("第一句。第二句。", bad) == nil {
			t.Fatal("accepted omitted/reordered/duplicate paragraphs")
		}
	}
}
func TestWorkflowNeedsUseLockedAudioAndExplicitAngles(t *testing.T) {
	in := WorkflowInput{Style: "flat-vector", Ratio: "9:16", UseAvatar: true}
	segs := []Segment{{ID: "p1", Text: "讲解。", Duration: 6, Audio: "p1/audio.wav"}}
	needs := []MaterialNeed{{ID: "n1", SegmentID: "p1", Kind: "avatar", Duration: 6, Angle: "正面", Content: "讲解"}}
	if err := ValidateNeeds(in, segs, needs); err != nil {
		t.Fatal(err)
	}
	needs[0].Angle = "左前方20度"
	if ValidateNeeds(in, segs, needs) == nil {
		t.Fatal("automatic extra angle allowed")
	}
	in.Angles = []string{"正面", "左前方20度"}
	in.Direction = "请用正面和左前方两个机位"
	needs = append(needs, MaterialNeed{ID: "n2", SegmentID: "p1", Kind: "avatar", Duration: 6, Angle: "正面", Content: "讲解"})
	if err := ValidateNeeds(in, segs, needs); err != nil {
		t.Fatal(err)
	}
	needs[0].Duration = 7
	if ValidateNeeds(in, segs, needs) == nil {
		t.Fatal("ignored locked duration")
	}
}
func TestWorkflowNeedsCoverRequestedAnglesWithoutDuplicatePaidShots(t *testing.T) {
	in := WorkflowInput{UseAvatar: true, Angles: []string{"正面", "左前方20度"}, Direction: "明确需要两个机位"}
	segs := []Segment{{ID: "p1", Duration: 6, Audio: "p1/audio.wav"}}
	needs := []MaterialNeed{{ID: "n1", SegmentID: "p1", Kind: "avatar", Duration: 6, Angle: "正面"}}
	if ValidateNeeds(in, segs, needs) == nil {
		t.Fatal("requested second angle omitted")
	}
	needs = append(needs, MaterialNeed{ID: "n2", SegmentID: "p1", Kind: "avatar", Duration: 6, Angle: "左前方20度"})
	if err := ValidateNeeds(in, segs, needs); err != nil {
		t.Fatal(err)
	}
	needs = append(needs, MaterialNeed{ID: "n3", SegmentID: "p1", Kind: "avatar", Duration: 6, Angle: "正面"})
	if ValidateNeeds(in, segs, needs) == nil {
		t.Fatal("duplicate paid shot of same segment and angle accepted")
	}
}
func TestWorkflowQualityDoesNotGateState(t *testing.T) {
	s := WorkflowState{Status: "running"}
	s.AddWarning("F06/n1", "quality", "嘴型待人工检查")
	s.AddWarning("F06/n1", "quality", "嘴型待人工检查")
	if s.Status != "running" || len(s.Events) != 1 {
		t.Fatal("warning gated execution or duplicated")
	}
}
func TestWorkflowDependencyFingerprint(t *testing.T) {
	a := WorkflowInput{Script: "原稿", Style: "flat-vector", Voice: "speaker-a", Ratio: "9:16"}
	b := a
	b.Style = "line-art"
	if AudioFingerprint(a, "一。") != AudioFingerprint(b, "一。") {
		t.Fatal("style invalidated audio")
	}
	b.Voice = "speaker-b"
	if AudioFingerprint(a, "一。") == AudioFingerprint(b, "一。") {
		t.Fatal("voice reused stale audio")
	}
}

func TestWorkflowShotRejectsShortAndMisalignedSpeech(t *testing.T) {
	segs := []Segment{{ID: "p1", Duration: 8}}
	materials := []Material{{NeedID: "n1", SegmentID: "p1", Kind: "avatar", Duration: 3}}
	shots := []WorkflowShot{{ID: "S1", SegmentID: "p1", Start: 0, End: 8, Primary: "n1", Card: "highlighter-sweep", Layout: "full"}}
	if ValidateWorkflowShots(shots, segs, materials, 8) == nil {
		t.Fatal("short footage accepted")
	}
	materials[0].Duration = 8
	if err := ValidateWorkflowShots(shots, segs, materials, 8); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowShotDecimalBoundaryDoesNotBecomeNegativeSource(t *testing.T) {
	var seg Segment
	var shot WorkflowShot
	json.Unmarshal([]byte(`{"id":"p3","start":21.373834000000002,"duration":7.566792}`), &seg)
	json.Unmarshal([]byte(`{"id":"S4","segment_id":"p3","start":21.373834,"end":28.940626,"primary":"a3","overlay":"a3","card":"chapter-title-card","layout":"pip"}`), &shot)
	if shot.Start-seg.Start >= 0 {
		t.Fatal("fixture must reproduce IEEE-754 boundary subtraction")
	}
	materials := []Material{{NeedID: "a3", SegmentID: "p3", Kind: "avatar", Duration: seg.Duration}}
	shots := []WorkflowShot{shot}
	MapAvatarShotSources(shots, []Segment{seg}, materials)
	if shots[0].PrimaryIn != 0 || shots[0].OverlayIn != 0 {
		t.Fatalf("negative source after mapping: %#v", shots[0])
	}
	if err := validateShotSource(materials[0], seg, shots[0], shots[0].PrimaryIn); err != nil {
		t.Fatal(err)
	}
	// A genuinely early shot remains invalid, rather than silently shifting speech.
	shots[0].Start = seg.Start - .001
	MapAvatarShotSources(shots, []Segment{seg}, materials)
	if validateShotSource(materials[0], seg, shots[0], shots[0].PrimaryIn) == nil {
		t.Fatal("real negative offset accepted")
	}
}

func TestWorkflowShotSourceErrorIdentifiesActualInterval(t *testing.T) {
	m := Material{NeedID: "short", Kind: "animation", Duration: 6}
	shot := WorkflowShot{ID: "S8", Start: 54, End: 62}
	err := validateShotSource(m, Segment{}, shot, 0)
	if err == nil || !strings.Contains(err.Error(), "S8") || !strings.Contains(err.Error(), "short") {
		t.Fatalf("unhelpful range error: %v", err)
	}
	for _, in := range []float64{math.NaN(), math.Inf(1), -.001} {
		if validateShotSource(m, Segment{}, shot, in) == nil {
			t.Fatal("invalid source accepted")
		}
	}
}
