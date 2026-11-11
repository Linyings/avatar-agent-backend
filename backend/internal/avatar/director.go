package avatar

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
)

type motionCodeContextKey struct{}

// Only the server's dedicated motion stage can enable the code response contract.
func WithMotionCode(ctx context.Context) context.Context {
	return context.WithValue(ctx, motionCodeContextKey{}, true)
}
func MotionCodeEnabled(ctx context.Context) bool {
	enabled, _ := ctx.Value(motionCodeContextKey{}).(bool)
	return enabled
}

type RequirementsAnalysis struct {
	Goal        string   `json:"goal"`
	Audience    string   `json:"audience"`
	KeyPoints   []string `json:"key_points"`
	Constraints []string `json:"constraints"`
	Assumptions []string `json:"assumptions"`
}
type ReferenceAnalysis struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Summary     string `json:"summary"`
	Limitations string `json:"limitations,omitempty"`
}
type DirectorScene struct {
	ID                string   `json:"id"`
	Purpose           string   `json:"purpose"`
	Composition       string   `json:"composition"`
	Visual            string   `json:"visual"`
	Motion            string   `json:"motion"`
	Transition        string   `json:"transition"`
	Sound             string   `json:"sound"`
	EstimatedDuration float64  `json:"estimated_duration"`
	AvatarPosition    string   `json:"avatar_position,omitempty"`
	AvatarShape       string   `json:"avatar_shape,omitempty"`
	SegmentIDs        []string `json:"segment_ids"`
	Narration         string   `json:"narration"`
	Start             float64  `json:"start"`
	End               float64  `json:"end"`
}
type DirectorPlan struct {
	Summary string          `json:"summary"`
	Timing  string          `json:"timing"`
	Scenes  []DirectorScene `json:"scenes"`
	Shots   []WorkflowShot  `json:"shots,omitempty"`
}
type WorkflowStage struct {
	Node    string   `json:"node"`
	Title   string   `json:"title"`
	Members []string `json:"members"`
}

func DirectorWorkflow(s *WorkflowState) bool { return s != nil && s.Version == WorkflowVersion }
func DirectorStages() []WorkflowStage {
	return []WorkflowStage{
		{"F01", "用户需求整理", []string{"F01"}},
		{"F01.2", "需求分析", []string{"F01.2"}},
		{"F01.4", "参考素材分析", []string{"F01.4"}},
		{"F01.6", "导演稿设计", []string{"F01.6"}},
		{"F03", "解说文案撰写与确认", []string{"F02", "F03"}},
		{"F04", "TTS 配音与时长校准", []string{"F04"}},
		{"F10", "镜头与专用动效制作", []string{"F05", "F06", "F07", "F08", "F09", "F10"}},
		{"F11", "镜头审阅与修改", []string{"F11"}},
		{"F11.1", "分层融合与逐帧渲染", []string{"F11.1"}},
		{"F11.2", "成片预览与修订", []string{"F11.2"}},
		{"F12", "成片交付", []string{"F12"}},
	}
}
func ConfirmationNode(kind string) string {
	return map[string]string{"script": "F03", "audio": "F04", "shots": "F11", "final": "F11.2"}[kind]
}
func ReviewFingerprint(s WorkflowState, kind string) string {
	if kind == "script" || kind == "audio" {
		return Digest(s.Segments)
	}
	artifacts := []WorkflowArtifact{}
	for _, a := range s.Artifacts {
		if (kind == "shots" && strings.HasPrefix(a.ID, "preview-")) || (kind == "final" && (a.ID == "video" || a.ID == "cover")) {
			artifacts = append(artifacts, a)
		}
	}
	return Digest([]any{kind, s.Shots, s.Materials, s.Segments, s.Input.SubtitleColor, s.Input.SubtitleScale, s.Input.SubtitleEdits, s.Input.AvatarPosition, artifacts})
}
// NormalizeDirector repairs presentation-only fields the model fills loosely.
// Windowless compositions (full-screen presenter, motion only, cards) have no
// avatar window, so values like "center" or "none" are cleared rather than
// failing the whole plan; a windowed composition gets a valid default.
func NormalizeDirector(p *DirectorPlan) {
	positions := []string{"left", "right", "top-left", "top-right", "bottom-left", "bottom-right"}
	sideOf := func(v string) string {
		v = strings.ToLower(strings.TrimSpace(v))
		switch {
		case strings.Contains(v, "left") || strings.Contains(v, "左"):
			return "left"
		case strings.Contains(v, "right") || strings.Contains(v, "右"):
			return "right"
		}
		return ""
	}
	for i := range p.Scenes {
		sc := &p.Scenes[i]
		switch sc.Composition {
		case "visual-pip":
			if !slices.Contains(positions, sc.AvatarPosition) {
				sc.AvatarPosition = "bottom-" + cmp.Or(sideOf(sc.AvatarPosition), "right")
			}
			if !slices.Contains([]string{"circle", "rounded", "rectangle"}, sc.AvatarShape) {
				sc.AvatarShape = "rounded"
			}
		case "visual-split":
			// A side column: only left/right matter, the shape is fixed.
			sc.AvatarPosition, sc.AvatarShape = cmp.Or(sideOf(sc.AvatarPosition), "right"), ""
		default:
			sc.AvatarPosition, sc.AvatarShape = "", ""
		}
	}
}

func ValidateDirector(p DirectorPlan) error {
	if strings.TrimSpace(p.Summary) == "" || len(p.Scenes) == 0 || len(p.Scenes) > 40 {
		return fmt.Errorf("导演稿缺少全片结构或分镜")
	}
	ids := map[string]bool{}
	for _, sc := range p.Scenes {
		if !SafeID.MatchString(sc.ID) || ids[sc.ID] || strings.TrimSpace(sc.Purpose) == "" || strings.TrimSpace(sc.Visual) == "" || math.IsNaN(sc.EstimatedDuration) || math.IsInf(sc.EstimatedDuration, 0) || sc.EstimatedDuration <= 0 || sc.EstimatedDuration > 90 {
			return fmt.Errorf("导演稿镜头编号、内容或预计时长无效")
		}
		if !slices.Contains([]string{"host-hero", "host-explain", "visual-hero", "visual-pip", "visual-split", "compare", "steps", "evidence", "summary"}, sc.Composition) {
			return fmt.Errorf("导演稿包含当前无法渲染的构图")
		}
		if sc.AvatarPosition != "" && !slices.Contains([]string{"left", "right", "top-left", "top-right", "bottom-left", "bottom-right"}, sc.AvatarPosition) {
			return fmt.Errorf("人物小窗位置无效")
		}
		if sc.AvatarShape != "" && !slices.Contains([]string{"circle", "rounded", "rectangle"}, sc.AvatarShape) {
			return fmt.Errorf("人物小窗形状无效")
		}
		ids[sc.ID] = true
	}
	return nil
}

// One shared plan is calibrated locally; this never rewrites approved speech.
func CalibrateDirector(s *WorkflowState) {
	if s.Director == nil || len(s.Director.Scenes) == 0 {
		return
	}
	measured := len(s.Segments) > 0
	for i := range s.Director.Scenes {
		s.Director.Scenes[i].SegmentIDs = nil
		s.Director.Scenes[i].Narration = ""
		s.Director.Scenes[i].Start = 0
		s.Director.Scenes[i].End = 0
	}
	for i := range s.Segments {
		seg := &s.Segments[i]
		idx := -1
		for j, sc := range s.Director.Scenes {
			if sc.ID == seg.DirectorSceneID {
				idx = j
				break
			}
		}
		if idx < 0 {
			idx = min(i*len(s.Director.Scenes)/max(1, len(s.Segments)), len(s.Director.Scenes)-1)
			seg.DirectorSceneID = s.Director.Scenes[idx].ID
		}
		sc := &s.Director.Scenes[idx]
		if len(sc.SegmentIDs) == 0 {
			sc.Start = seg.Start
		}
		sc.End = seg.Start + seg.Duration
		sc.SegmentIDs = append(sc.SegmentIDs, seg.ID)
		sc.Narration += seg.Text
		if seg.Duration <= 0 {
			measured = false
		}
	}
	s.Director.Timing = "estimated"
	if measured {
		s.Director.Timing = "measured"
	}
	s.Director.Shots = append([]WorkflowShot(nil), s.Shots...)
}
