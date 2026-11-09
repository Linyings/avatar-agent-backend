package avatar

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
)

const ShotPlanningVersion = "shot-batches/1"

type ShotPlanningMaterial struct {
	NarrationStart *float64      `json:"narration_start,omitempty"`
	NeedID         string        `json:"need_id"`
	Kind           string        `json:"kind"`
	Duration       float64       `json:"duration"`
	Content        string        `json:"content,omitempty"`
	Template       string        `json:"template,omitempty"`
	Items          []string      `json:"items,omitempty"`
	FaceZone       []float64     `json:"face_zone,omitempty"`
	Scenes         []MotionScene `json:"scenes,omitempty"`
}

type ShotPlanningBatch struct {
	Segment Segment `json:"segment"`
	Window  struct {
		Start float64 `json:"start"`
		End   float64 `json:"end"`
	} `json:"window"`
	Materials    []ShotPlanningMaterial `json:"materials"`
	Director     []DirectorScene        `json:"director,omitempty"`
	Summary      string                 `json:"summary,omitempty"`
	Topic        string                 `json:"topic,omitempty"`
	Direction    string                 `json:"direction,omitempty"`
	Style        string                 `json:"style"`
	Ratio        string                 `json:"ratio"`
	UseAvatar    bool                   `json:"use_avatar"`
	UseAnimation bool                   `json:"use_animation"`
	MaxShots     int                    `json:"max_shots"`
	// Full source identities invalidate the local cache without sending code,
	// paths, prompts or hashes to the model.
	SourceFingerprint string `json:"-"`
}

func planningExcerpt(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func BuildShotPlanningBatches(s *WorkflowState) ([]ShotPlanningBatch, error) {
	var batches []ShotPlanningBatch
	for _, seg := range s.Segments {
		if !SafeID.MatchString(seg.ID) || seg.Duration <= 0 || math.IsNaN(seg.Duration) || math.IsInf(seg.Duration, 0) || math.IsNaN(seg.Start) || math.IsInf(seg.Start, 0) {
			return nil, fmt.Errorf("分段镜头规划缺少有效的配音时间")
		}
		b := ShotPlanningBatch{Segment: seg, Topic: s.Input.Topic, Direction: s.Input.Direction, Style: s.Input.Style, Ratio: s.Input.Ratio, UseAvatar: s.Input.UseAvatar, UseAnimation: AnimationEnabled(s.Input), MaxShots: 4}
		if len(s.ProductionNotes) > 0 {
			b.Direction += "\n" + strings.Join(s.ProductionNotes, "\n")
		}
		b.Segment.Audio, b.Segment.Fingerprint = "", ""
		var sources []Material
		var needs []MaterialNeed
		var directorSources []DirectorScene
		directorSummary := ""
		for _, m := range s.Materials {
			if m.SegmentID != seg.ID {
				continue
			}
			sources = append(sources, m)
			p := ShotPlanningMaterial{NeedID: m.NeedID, Kind: m.Kind, Duration: m.Duration, FaceZone: m.FaceZone, NarrationStart: m.NarrationStart}
			for _, n := range s.Needs {
				if n.ID != m.NeedID {
					continue
				}
				needs = append(needs, n)
				p.Content, p.Template = planningExcerpt(n.Content, 300), n.Template
				for _, item := range n.Items[:min(len(n.Items), 4)] {
					p.Items = append(p.Items, planningExcerpt(item, 60))
				}
			}
			if m.MotionPlan != nil {
				scenes := m.MotionPlan.Scenes
				if len(m.RenderedScenes) > 0 {
					scenes = m.RenderedScenes
				}
				for _, scene := range scenes {
					// Explicit projection: never copy top-level or per-scene code.
					v := MotionScene{Title: planningExcerpt(scene.Title, 60), Quote: planningExcerpt(scene.Quote, 300), Layout: scene.Layout}
					for _, e := range scene.Elements[:min(len(scene.Elements), 4)] {
						v.Elements = append(v.Elements, MotionElement{Label: planningExcerpt(e.Label, 60), Kind: e.Kind})
					}
					p.Scenes = append(p.Scenes, v)
				}
			}
			b.Materials = append(b.Materials, p)
		}
		if len(b.Materials) == 0 {
			return nil, fmt.Errorf("段落 %s 缺少可组装素材", seg.ID)
		}
		if s.Director != nil {
			directorSummary = s.Director.Summary
			b.Summary = planningExcerpt(s.Director.Summary, 600)
			for _, scene := range s.Director.Scenes {
				linked := scene.ID == seg.DirectorSceneID || slices.Contains(scene.SegmentIDs, seg.ID)
				// Old plans may only have measured times, with no segment links.
				if !linked && (len(scene.SegmentIDs) > 0 || seg.DirectorSceneID != "" || scene.End <= seg.Start || scene.Start >= seg.Start+seg.Duration) {
					continue
				}
				directorSources = append(directorSources, scene)
				scene.Narration = "" // the exact narration is already in Segment.Text
				b.Director = append(b.Director, scene)
			}
		}
		b.SourceFingerprint = Digest([]any{seg, sources, needs, directorSources, directorSummary})
		for start := 0.0; start < seg.Duration; start += 20 {
			b.Window.Start, b.Window.End = start, math.Min(start+20, seg.Duration)
			raw, err := json.Marshal(b)
			if err != nil || len(raw) > 16000 {
				return nil, fmt.Errorf("段落 %s 的精简输入仍超过 16000 字节预算，请拆分段落或缩短制作要求；未提交镜头规划请求", seg.ID)
			}
			batches = append(batches, b)
			if len(batches) > 160 {
				return nil, fmt.Errorf("分段规划超过全片 160 镜上限，请拆分为多个视频")
			}
		}
	}
	if len(batches) == 0 {
		return nil, fmt.Errorf("缺少已校准配音段落")
	}
	// Allocate the existing global 160-shot limit before spending on any batch.
	for i := range batches {
		batches[i].MaxShots = min(4, 160/len(batches))
	}
	return batches, nil
}

func NormalizeShotPlanningBatch(b ShotPlanningBatch, input []WorkflowShot) ([]WorkflowShot, error) {
	if len(input) == 0 || len(input) > b.MaxShots || len(input) > 4 {
		return nil, fmt.Errorf("当前分段必须返回 1–%d 个镜头", b.MaxShots)
	}
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > 12000 {
		return nil, fmt.Errorf("当前分段镜头结果超过 12000 字节，请精简后重试当前段")
	}
	shots := append([]WorkflowShot(nil), input...)
	var materials []Material
	for _, m := range b.Materials {
		materials = append(materials, Material{NeedID: m.NeedID, SegmentID: b.Segment.ID, Kind: m.Kind, Duration: m.Duration, NarrationStart: m.NarrationStart})
	}
	for i := range shots {
		v := &shots[i]
		if len(v.Items) > 4 || len(v.Semantic) > 4 || len([]rune(v.Intent)) > 120 || len([]rune(v.SelectionReason)) > 120 || len([]rune(v.Keyword)) > 60 {
			return nil, fmt.Errorf("当前分段解释或标题过长，请精简后重试当前段")
		}
		for _, item := range v.Items {
			if len([]rune(item)) > 60 {
				return nil, fmt.Errorf("镜头要点超过 60 字，请精简当前段")
			}
		}
		v.ID = fmt.Sprintf("S-%s-%d-%d", Digest(b.Segment.ID)[:12], int(math.Round(b.Window.Start*1000)), i+1)
		v.SegmentID = b.Segment.ID
		v.Start += b.Segment.Start
		v.End += b.Segment.Start
	}
	MapAvatarShotSources(shots, []Segment{b.Segment}, materials)
	NormalizeShotPresentation(shots)
	// Validate a zero-origin copy while preserving the original source offsets.
	local := append([]WorkflowShot(nil), shots...)
	for i := range local {
		local[i].Start -= b.Segment.Start + b.Window.Start
		local[i].End -= b.Segment.Start + b.Window.Start
	}
	seg := b.Segment
	seg.Start = -b.Window.Start
	if err := ValidateWorkflowShots(local, []Segment{seg}, materials, b.Window.End-b.Window.Start); err != nil {
		return nil, err
	}
	return shots, nil
}
