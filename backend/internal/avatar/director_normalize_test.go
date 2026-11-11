package avatar

import "testing"

// Full-screen presenter and presenter-free shots have no window. The model
// naturally writes "center"/"none"/"rectangle" there; that must not fail the
// whole director plan. Real windows keep a valid corner and shape.
func TestNormalizeDirectorClearsWindowFieldsForWindowlessShots(t *testing.T) {
	p := DirectorPlan{Summary: "全片", Scenes: []DirectorScene{
		{ID: "d1", Purpose: "开场", Visual: "人物", Composition: "host-hero", AvatarPosition: "center", AvatarShape: "rectangle", EstimatedDuration: 6},
		{ID: "d2", Purpose: "原理", Visual: "动效", Composition: "visual-hero", AvatarPosition: "none", AvatarShape: "none", EstimatedDuration: 8},
		{ID: "d3", Purpose: "数据", Visual: "动效", Composition: "visual-pip", AvatarPosition: "bottom-right", AvatarShape: "circle", EstimatedDuration: 8},
		{ID: "d4", Purpose: "对比", Visual: "动效", Composition: "visual-split", AvatarPosition: "右侧", AvatarShape: "竖条", EstimatedDuration: 8},
		{ID: "d5", Purpose: "步骤", Visual: "动效", Composition: "visual-pip", AvatarPosition: "center", AvatarShape: "square", EstimatedDuration: 8},
	}}
	NormalizeDirector(&p)
	if err := ValidateDirector(p); err != nil {
		t.Fatalf("normalized plan still invalid: %v", err)
	}
	if p.Scenes[0].AvatarPosition != "" || p.Scenes[1].AvatarPosition != "" || p.Scenes[1].AvatarShape != "" {
		t.Fatalf("windowless shot kept window fields: %+v %+v", p.Scenes[0], p.Scenes[1])
	}
	if p.Scenes[2].AvatarPosition != "bottom-right" || p.Scenes[2].AvatarShape != "circle" {
		t.Fatalf("valid window changed: %+v", p.Scenes[2])
	}
	if p.Scenes[3].AvatarPosition != "right" || p.Scenes[3].AvatarShape != "" {
		t.Fatalf("split side not recovered: %+v", p.Scenes[3])
	}
	if p.Scenes[4].AvatarPosition != "bottom-right" || p.Scenes[4].AvatarShape != "rounded" {
		t.Fatalf("invalid pip window not defaulted: %+v", p.Scenes[4])
	}
}
