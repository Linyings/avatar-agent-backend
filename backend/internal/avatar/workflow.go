package avatar

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const WorkflowVersion = "avatar-workflow/3.0"

var SafeID = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)
var WorkflowNodes = []string{"F01.2", "F01.4", "F01.6", "F02", "F03", "F04", "F05", "F06", "F07", "F08", "F09", "F10", "F11", "F11.1", "F11.2", "F12"}

type WorkflowInput struct {
	SpeechMode         string             `json:"speech_mode,omitempty"`
	VoiceReference     string             `json:"voice_reference,omitempty"`
	Revision           int                `json:"revision"`
	Title              string             `json:"title"`
	Topic              string             `json:"topic"`
	Script             string             `json:"script"`
	Direction          string             `json:"direction"`
	Style              string             `json:"style"`
	Voice              string             `json:"voice"`
	VoiceModel         string             `json:"voice_model,omitempty"` // RunningHub TTS model; empty = deployment TTS_MODEL
	VoiceSpeed         float64            `json:"voice_speed,omitempty"`
	SegmentVoiceSpeeds map[string]float64 `json:"segment_voice_speeds,omitempty"`
	Ratio              string             `json:"ratio"`
	UseAvatar          bool               `json:"use_avatar"`
	UseAnimation       *bool              `json:"use_animation,omitempty"`
	Angles             []string           `json:"angles"`
	Photo              string             `json:"photo,omitempty"`
	Assets             []SourceAsset      `json:"assets"`
	ReferenceLinks     []string           `json:"reference_links,omitempty"`
	SubtitleColor      string             `json:"subtitle_color,omitempty"`
	SubtitleScale      float64            `json:"subtitle_scale,omitempty"`
	SubtitleEdits      map[string]string  `json:"subtitle_edits,omitempty"`
	AvatarPosition     string             `json:"avatar_position,omitempty"`
}
type SourceAsset struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Kind     string  `json:"kind"`
	Path     string  `json:"path"`
	SHA256   string  `json:"sha256"`
	Duration float64 `json:"duration"`
	Excluded bool    `json:"excluded"`
}
type SourceDescription struct {
	ID       string  `json:"id"`
	AssetID  string  `json:"asset_id"`
	Start    float64 `json:"start"`
	End      float64 `json:"end"`
	Text     string  `json:"text"`
	Coverage string  `json:"coverage"`
}
type Segment struct {
	NativePrompt    string  `json:"native_prompt,omitempty"`
	NativeRevision  string  `json:"native_revision,omitempty"`
	NativeVideo     string  `json:"native_video,omitempty"`
	DirectorSceneID string  `json:"director_scene_id,omitempty"`
	ID              string  `json:"id"`
	Chapter         string  `json:"chapter"`
	Text            string  `json:"text"`
	Audio           string  `json:"audio,omitempty"`
	Fingerprint     string  `json:"fingerprint,omitempty"`
	Start           float64 `json:"start"`
	Duration        float64 `json:"duration"`
}
type MaterialNeed struct {
	NarrationStart *float64 `json:"narration_start,omitempty"`
	NarrationQuote string   `json:"narration_quote,omitempty"`
	ID             string   `json:"id"`
	SegmentID      string   `json:"segment_id"`
	Kind           string   `json:"kind"`
	Duration       float64  `json:"duration"`
	Angle          string   `json:"angle,omitempty"`
	Content        string   `json:"content"`
	Template       string   `json:"template,omitempty"`
	Items          []string `json:"items,omitempty"`
	Prompt         string   `json:"prompt,omitempty"`
	SourceID       string   `json:"source_id,omitempty"`
	SourceStart    float64  `json:"source_start,omitempty"`
	Query          string   `json:"query,omitempty"`
	DescriptionID  string   `json:"description_id,omitempty"`
}
type Material struct {
	RenderedScenes  []MotionScene `json:"rendered_scenes,omitempty"`
	NarrationStart  *float64      `json:"narration_start,omitempty"`
	MotionPlan      *MotionPlan   `json:"motion_plan,omitempty"`
	BaseFingerprint string        `json:"base_fingerprint,omitempty"`
	NeedID          string        `json:"need_id"`
	SegmentID       string        `json:"segment_id"`
	Kind            string        `json:"kind"`
	Path            string        `json:"path"`
	Duration        float64       `json:"duration"`
	Fingerprint     string        `json:"fingerprint"`
	FaceZone        []float64     `json:"face_zone,omitempty"`
	SourceID        string        `json:"source_id,omitempty"`
	SourceStart     float64       `json:"source_start,omitempty"`
	DescriptionID   string        `json:"description_id,omitempty"`
}
type WorkflowShot struct {
	AvatarPosition  string   `json:"avatar_position,omitempty"`
	AvatarShape     string   `json:"avatar_shape,omitempty"`
	ID              string   `json:"id"`
	SegmentID       string   `json:"segment_id"`
	Start           float64  `json:"start"`
	End             float64  `json:"end"`
	Primary         string   `json:"primary"`
	Overlay         string   `json:"overlay,omitempty"`
	Keyword         string   `json:"keyword"`
	Card            string   `json:"card"`
	Layout          string   `json:"layout"`
	Recipe          string   `json:"recipe,omitempty"`
	Items           []string `json:"items,omitempty"`
	PrimaryIn       float64  `json:"primary_in"`
	OverlayIn       float64  `json:"overlay_in"`
	Semantic        []string `json:"semantic,omitempty"`
	Intent          string   `json:"intent,omitempty"`
	SelectionReason string   `json:"selection_reason,omitempty"`
}

type WorkflowOrder struct {
	Attempt    int             `json:"attempt"`
	Submission string          `json:"submission"`
	TaskID     string          `json:"provider_task_id,omitempty"`
	Usage      json.RawMessage `json:"usage,omitempty"`
	RecordedAt time.Time       `json:"recorded_at"`
}
type WorkflowEvent struct {
	ID        string    `json:"id"`
	Node      string    `json:"node"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
	Read      bool      `json:"read"`
}
type WorkflowStep struct {
	Retry            *WorkflowRetry `json:"retry,omitempty"`
	ProductionPhase  string         `json:"production_phase,omitempty"`
	GenerationPrompt string         `json:"generation_prompt,omitempty"`
	// Model is the provider model the order was placed on; recovery queries
	// the same model's API even after the default changes.
	Model          string            `json:"model,omitempty"`
	PromptInput    json.RawMessage   `json:"prompt_input,omitempty"`
	ID             string            `json:"id"`
	Status         string            `json:"status"`
	Attempt        int               `json:"attempt"`
	StartedAt      *time.Time        `json:"started_at,omitempty"`
	CompletedAt    *time.Time        `json:"completed_at,omitempty"`
	Seconds        float64           `json:"seconds"`
	InputHash      string            `json:"input_hash"`
	Submission     string            `json:"submission,omitempty"`
	LocalRecovery  string            `json:"local_recovery,omitempty"`
	ProviderTaskID string            `json:"provider_task_id,omitempty"`
	Error          string            `json:"error,omitempty"`
	ErrorType      string            `json:"error_type,omitempty"`
	Usage          json.RawMessage   `json:"usage,omitempty"`
	CostStatus     string            `json:"cost_status"`
	Orders         []WorkflowOrder   `json:"orders,omitempty"`
	Attempts       []WorkflowAttempt `json:"attempts,omitempty"`
}
type WorkflowAttempt struct {
	Error       string          `json:"error,omitempty"`
	Number      int             `json:"number"`
	StartedAt   time.Time       `json:"started_at"`
	CompletedAt time.Time       `json:"completed_at"`
	Seconds     float64         `json:"seconds"`
	Status      string          `json:"status"`
	CostStatus  string          `json:"cost_status"`
	Usage       json.RawMessage `json:"usage,omitempty"`
	TaskID      string          `json:"provider_task_id,omitempty"`
}
type WorkflowRetry struct {
	LastError        string `json:"last_error"`
	Strategy         string `json:"strategy"`
	AutomaticRetries int    `json:"automatic_retries"`
	Exhausted        bool   `json:"exhausted,omitempty"`
	// PreviousError is the diagnostic of the attempt before LastError's; an
	// identical pair after a model repair means the repair is not progressing.
	PreviousError string `json:"previous_error,omitempty"`
	LastAttempt   int    `json:"last_attempt,omitempty"`
}
type WorkflowArtifact struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
}
type WorkflowState struct {
	AudioQuality        json.RawMessage       `json:"audio_quality,omitempty"`
	QualityReview       json.RawMessage       `json:"quality_review,omitempty"`
	ReferencesAnalyzed  bool                  `json:"references_analyzed,omitempty"`
	Requirements        *RequirementsAnalysis `json:"requirements,omitempty"`
	References          []ReferenceAnalysis   `json:"references,omitempty"`
	Director            *DirectorPlan         `json:"director,omitempty"`
	ShotsConfirmed      string                `json:"shots_confirmed,omitempty"`
	FinalConfirmed      string                `json:"final_confirmed,omitempty"`
	ID                  string                `json:"id"`
	ProjectID           string                `json:"project_id"`
	Version             string                `json:"version"`
	Input               WorkflowInput         `json:"input"`
	ConfigID            string                `json:"config_id"`
	Status              string                `json:"status"`
	Stage               string                `json:"stage"`
	Confirmation        string                `json:"confirmation,omitempty"`
	ConfirmationVersion string                `json:"confirmation_version,omitempty"`
	ScriptConfirmed     bool                  `json:"script_confirmed"`
	AudioConfirmed      bool                  `json:"audio_confirmed"`
	Script              string                `json:"script"`
	ProductionNotes     []string              `json:"production_notes,omitempty"`
	ResolvedAngles      []string              `json:"resolved_angles,omitempty"`
	Segments            []Segment             `json:"segments"`
	Needs               []MaterialNeed        `json:"needs"`
	Materials           []Material            `json:"materials"`
	Descriptions        []SourceDescription   `json:"descriptions"`
	Shots               []WorkflowShot        `json:"shots"`
	Steps               []WorkflowStep        `json:"steps"`
	Events              []WorkflowEvent       `json:"events"`
	Artifacts           []WorkflowArtifact    `json:"artifacts"`
	Operations          []Operation           `json:"operations,omitempty"`
	StopRequested       bool                  `json:"stop_requested"`
	PauseRequested      bool                  `json:"pause_requested"`
	TargetNode          string                `json:"target_node,omitempty"`
	RecoveryRequests    []string              `json:"recovery_requests,omitempty"`
	StoppedAt           *time.Time            `json:"stopped_at,omitempty"`
	CreatedAt           time.Time             `json:"created_at"`
	UpdatedAt           time.Time             `json:"updated_at"`
	CompletedAt         *time.Time            `json:"completed_at,omitempty"`
	Duration            float64               `json:"duration"`
	Error               string                `json:"error,omitempty"`
	ErrorType           string                `json:"error_type,omitempty"`
	ErrorStepID         string                `json:"error_step_id,omitempty"`
	ReusedFrom          string                `json:"reused_from,omitempty"`
	// Need@speech-fingerprint pairs whose reused motion design failed the
	// render check; those needs are designed fresh for the current speech.
	RejectedMotionReuse []string `json:"rejected_motion_reuse,omitempty"`
	ConfigSnapshot      map[string]string     `json:"config_snapshot"`
	Edits               WorkflowEdits         `json:"edits,omitempty"`
	AwaitingSince       *time.Time            `json:"awaiting_since,omitempty"`
	ConfirmationSeconds float64               `json:"confirmation_seconds"`
	Billing             []WorkflowBill        `json:"billing"`
}
type WorkflowBill struct {
	Key        string          `json:"key"`
	Step       string          `json:"step"`
	TaskID     string          `json:"provider_task_id,omitempty"`
	CostStatus string          `json:"cost_status"`
	Usage      json.RawMessage `json:"usage,omitempty"`
	Source     string          `json:"source"`
}

func (s *WorkflowState) RefreshBilling() {
	s.Billing = []WorkflowBill{}
	seen := map[string]int{}
	add := func(step string, attempt int, task, status string, usage json.RawMessage) {
		if status == "local_compute" || status == "reused" || (task == "" && status == "") {
			return
		}
		key := fmt.Sprintf("%s/attempt-%d", step, attempt)
		if task != "" {
			key = "provider-task/" + task
		}
		bill := WorkflowBill{Key: key, Step: step, TaskID: task, CostStatus: status, Usage: usage, Source: "supplier response; original units retained; no currency assumed"}
		if i, ok := seen[key]; ok {
			if len(bill.Usage) == 0 {
				bill.Usage = s.Billing[i].Usage
			}
			if s.Billing[i].CostStatus == "reported" && bill.CostStatus != "reported" {
				bill.CostStatus = "reported"
			}
			s.Billing[i] = bill
		} else {
			seen[key] = len(s.Billing)
			s.Billing = append(s.Billing, bill)
		}
	}
	for _, step := range s.Steps {
		for _, a := range step.Attempts {
			add(step.ID, a.Number, a.TaskID, a.CostStatus, a.Usage)
		}
		for _, o := range step.Orders {
			status := "unknown"
			if len(o.Usage) > 0 {
				status = "reported"
			}
			add(step.ID, o.Attempt, o.TaskID, status, o.Usage)
		}
		if step.Attempt > 0 {
			add(step.ID, step.Attempt, step.ProviderTaskID, step.CostStatus, step.Usage)
		}
	}
}

// Edits create a new immutable run. They cannot redirect the fixed workflow.
type WorkflowEdits struct {
	NativePrompts      map[string]string   `json:"native_prompts,omitempty"`
	MaterialOnly       string              `json:"material_only,omitempty"`
	Director           *DirectorPlan       `json:"director,omitempty"`
	RerunFrom          string              `json:"rerun_from,omitempty"`
	Script             *string             `json:"script,omitempty"`
	Presentation       *PresentationEdits  `json:"presentation,omitempty"`
	Subtitles          map[string]string   `json:"subtitles,omitempty"`
	Voice              string              `json:"voice,omitempty"`
	VoiceModel         string              `json:"voice_model,omitempty"`
	VoiceSpeed         *float64            `json:"voice_speed,omitempty"`
	SegmentVoiceSpeeds map[string]float64  `json:"segment_voice_speeds,omitempty"`
	BaseRunID          string              `json:"base_run_id,omitempty"`
	Segments           []Segment           `json:"segments,omitempty"`
	Needs              []MaterialNeed      `json:"needs,omitempty"`
	Shots              []WorkflowShot      `json:"shots,omitempty"`
	Descriptions       []SourceDescription `json:"descriptions,omitempty"`
	RegenerateSegments []string            `json:"regenerate_segments,omitempty"`
}

type PresentationEdits struct {
	SubtitleColor  *string  `json:"subtitle_color,omitempty"`
	SubtitleScale  *float64 `json:"subtitle_scale,omitempty"`
	AvatarPosition *string  `json:"avatar_position,omitempty"`
}

func (e WorkflowEdits) ApplyPresentation(in *WorkflowInput) {
	if p := e.Presentation; p != nil {
		if p.SubtitleColor != nil {
			in.SubtitleColor = *p.SubtitleColor
		}
		if p.SubtitleScale != nil {
			in.SubtitleScale = *p.SubtitleScale
		}
		if p.AvatarPosition != nil {
			in.AvatarPosition = *p.AvatarPosition
		}
	}
	if e.Subtitles != nil {
		merged := map[string]string{}
		for id, text := range in.SubtitleEdits {
			merged[id] = text
		}
		for id, text := range e.Subtitles {
			merged[id] = text
		}
		in.SubtitleEdits = merged
	}
}

func (step *WorkflowStep) PreserveOrder() {
	// Local F10 recovery retains the original uncertain order in Orders; later
	// local rendering attempts are not new supplier submissions.
	if step.Submission == "" || step.LocalRecovery != "" {
		return
	}
	for i := range step.Orders {
		if (step.ProviderTaskID != "" && step.Orders[i].TaskID == step.ProviderTaskID) || (step.Orders[i].Attempt == step.Attempt && step.Orders[i].TaskID == "") {
			step.Orders[i].Submission = step.Submission
			step.Orders[i].TaskID = step.ProviderTaskID
			step.Orders[i].Usage = step.Usage
			return
		}
	}
	step.Orders = append(step.Orders, WorkflowOrder{Attempt: step.Attempt, Submission: step.Submission, TaskID: step.ProviderTaskID, Usage: step.Usage, RecordedAt: time.Now().UTC()})
}

func Digest(v any) string { b, _ := json.Marshal(v); return fmt.Sprintf("%x", sha256.Sum256(b)) }
func AudioFingerprint(in WorkflowInput, text string) string {
	if in.VoiceSpeed == 0 || in.VoiceSpeed == 1 {
		return Digest([]string{in.Voice, text})
	}
	return Digest([]any{in.Voice, text, in.VoiceSpeed})
}
func (s *WorkflowState) AddWarning(node, kind, message string) {
	id := "evt-" + Digest([]string{node, kind, message})[:24]
	for _, e := range s.Events {
		if e.ID == id {
			return
		}
	}
	s.Events = append(s.Events, WorkflowEvent{ID: id, Node: node, Type: kind, Message: message, CreatedAt: time.Now().UTC()})
}
func spokenWhitespace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}
func ValidateSegments(script string, segments []Segment) error {
	if len(segments) == 0 || len(segments) > 40 {
		return fmt.Errorf("内容段数量需为1–40段")
	}
	ids := map[string]bool{}
	var body strings.Builder
	for _, s := range segments {
		if !SafeID.MatchString(s.ID) || ids[s.ID] || strings.TrimSpace(s.Text) == "" {
			return fmt.Errorf("段落ID重复或内容为空")
		}
		ids[s.ID] = true
		body.WriteString(s.Text)
	}
	if spokenWhitespace(body.String()) != spokenWhitespace(script) {
		return fmt.Errorf("内容拆解遗漏、重复或改写了原稿")
	}
	return nil
}
func ValidateNeeds(in WorkflowInput, segs []Segment, needs []MaterialNeed) error {
	if NativeSpeech(in) {
		in.Angles = []string{"正面"}
	}
	ss := map[string]Segment{}
	for _, s := range segs {
		ss[s.ID] = s
	}
	if len(needs) == 0 || len(needs) > 120 {
		return fmt.Errorf("素材需求数量无效")
	}
	ids := map[string]bool{}
	covered := map[string]bool{}
	avatarShots := map[string]bool{}
	hasAnimation := false
	usedAngles := map[string]bool{}
	for _, n := range needs {
		s, ok := ss[n.SegmentID]
		if !ok || s.Duration <= 0 || s.Audio == "" {
			return fmt.Errorf("需求缺少锁定声音")
		}
		if !SafeID.MatchString(n.ID) || ids[n.ID] || n.Duration <= 0 || math.IsNaN(n.Duration) || math.IsInf(n.Duration, 0) || n.Duration > s.Duration+.02 {
			return fmt.Errorf("素材需求ID或实际时长无效")
		}
		ids[n.ID] = true
		covered[n.SegmentID] = true
		switch n.Kind {
		case "avatar":
			if !in.UseAvatar {
				return fmt.Errorf("本项目未启用数字人")
			}
			allowed := n.Angle == "正面" || n.Angle == ""
			if len(in.Angles) > 0 && strings.TrimSpace(in.Direction) != "" {
				allowed = false
				for _, a := range in.Angles {
					if n.Angle == a {
						allowed = true
					}
				}
			}
			if !allowed {
				return fmt.Errorf("多角度必须来自用户明确的制作要求")
			}
			angle := n.Angle
			if angle == "" {
				angle = "正面"
			}
			key := n.SegmentID + "/" + angle
			if avatarShots[key] {
				return fmt.Errorf("同一段落同一机位不得重复生成；请复用同一人物素材")
			}
			avatarShots[key] = true
			usedAngles[angle] = true
			if math.Abs(n.Duration-s.Duration) > .02 {
				return fmt.Errorf("人物素材须覆盖本段实际驱动音频")
			}
		case "animation":
			if in.UseAnimation != nil && !*in.UseAnimation {
				return fmt.Errorf("本项目未启用动效画面，请按用户选择规划素材")
			}
			if n.Template != "steps" && n.Template != "compare" && n.Template != "relationship" && n.Template != "highlight" && n.Template != "summary" && n.Template != "data" {
				return fmt.Errorf("动画模板不在已发布清单")
			}
			hasAnimation = true
		case "real":
			found := false
			for _, a := range in.Assets {
				if a.ID == n.SourceID && !a.Excluded && a.Kind != "file" {
					found = true
					if n.SourceStart < 0 || (a.Duration > 0 && n.SourceStart+n.Duration > a.Duration+.03) {
						return fmt.Errorf("真实素材源区间不足")
					}
				}
			}
			if !found {
				return fmt.Errorf("真实素材尚未上传或未选用")
			}
		default:
			return fmt.Errorf("未知素材需求类型")
		}
	}
	// Explicit animation selection is a production requirement. Leave legacy
	// inputs without this flag unchanged when recovering historical runs.
	if in.UseAnimation != nil && *in.UseAnimation {
		if !hasAnimation {
			return fmt.Errorf("已启用动效，但素材需求遗漏动效画面，请重新规划素材")
		}
		if in.UseAvatar && len(avatarShots) == 0 {
			return fmt.Errorf("已启用人物与动效融合，但素材需求遗漏人物口播，请重新规划素材")
		}
	}
	for _, s := range segs {
		if !covered[s.ID] {
			return fmt.Errorf("某段配音未分配素材需求")
		}
	}
	if in.UseAvatar && strings.TrimSpace(in.Direction) != "" {
		for _, angle := range in.Angles {
			if !usedAngles[angle] {
				return fmt.Errorf("素材需求遗漏用户指定机位：%s", angle)
			}
		}
	}
	return nil
}
func ValidateWorkflowShots(shots []WorkflowShot, segs []Segment, materials []Material, total float64) error {
	if len(shots) == 0 || len(shots) > 160 {
		return fmt.Errorf("分镜数量无效")
	}
	mm := map[string]Material{}
	for _, m := range materials {
		mm[m.NeedID] = m
	}
	ss := map[string]Segment{}
	for _, s := range segs {
		ss[s.ID] = s
	}
	edge := 0.0
	ids := map[string]bool{}
	for _, s := range shots {
		seg, ok := ss[s.SegmentID]
		m, exists := mm[s.Primary]
		if !SafeID.MatchString(s.ID) || ids[s.ID] || !ok || !exists || s.End <= s.Start || math.Abs(s.Start-edge) > .02 || s.Start < seg.Start-.02 || s.End > seg.Start+seg.Duration+.02 || m.SegmentID != s.SegmentID {
			return fmt.Errorf("分镜缺少真实素材或时间覆盖无效")
		}
		if s.Overlay != "" {
			o, ok := mm[s.Overlay]
			if !ok || o.SegmentID != s.SegmentID {
				return fmt.Errorf("分镜叠加素材无效")
			}
			if err := validateShotSource(o, seg, s, s.OverlayIn); err != nil {
				return err
			}
		}
		if err := validateShotSource(m, seg, s, s.PrimaryIn); err != nil {
			return err
		}
		if !ValidPresentationCard(s.Card) {
			return fmt.Errorf("分镜组件不在允许清单")
		}
		if s.Layout != "full" && s.Layout != "pip" && s.Layout != "split" {
			return fmt.Errorf("分镜布局不在允许清单")
		}
		if s.AvatarShape != "" && s.AvatarShape != "circle" && s.AvatarShape != "rounded" && s.AvatarShape != "rectangle" {
			return fmt.Errorf("人物小窗形状无效")
		}
		if s.AvatarPosition != "" && !strings.Contains("|left|right|top-left|top-right|bottom-left|bottom-right|", "|"+s.AvatarPosition+"|") {
			return fmt.Errorf("人物小窗位置无效")
		}
		if s.Recipe != "" && !ValidPresentationRecipe(s.Recipe) {
			return fmt.Errorf("分镜配方不在允许清单")
		}
		ids[s.ID] = true
		edge = s.End
	}
	if math.Abs(edge-total) > .03 {
		return fmt.Errorf("分镜未覆盖整条锁定配音")
	}
	return nil
}

func validateShotSource(m Material, seg Segment, shot WorkflowShot, in float64) error {
	if math.IsNaN(in) || math.IsInf(in, 0) || in < 0 || in+shot.End-shot.Start > m.Duration+.045 {
		return fmt.Errorf("分镜使用区间超出真实素材，不能冻结或循环补时长（镜头 %s，素材 %s，源入点 %.9f 秒，使用 %.9f 秒，素材 %.9f 秒）", shot.ID, m.NeedID, in, shot.End-shot.Start, m.Duration)
	}
	if m.Kind == "avatar" && math.Abs(in-(shot.Start-seg.Start)) > .02 {
		return fmt.Errorf("人物源区间没有对应本段驱动配音")
	}
	if m.Kind == "animation" && m.NarrationStart != nil && math.Abs(in-(shot.Start-seg.Start-*m.NarrationStart)) > .045 {
		return fmt.Errorf("动效源区间没有对应本段台词时间（镜头 %s，素材 %s）", shot.ID, m.NeedID)
	}
	return nil
}

func MapAvatarShotSources(shots []WorkflowShot, segs []Segment, materials []Material) {
	for i := range shots {
		for _, seg := range segs {
			if seg.ID != shots[i].SegmentID {
				continue
			}
			for _, m := range materials {
				if m.Kind != "avatar" && (m.Kind != "animation" || m.NarrationStart == nil) {
					continue
				}
				base := seg.Start
				if m.Kind == "animation" {
					base += *m.NarrationStart
				}
				if m.NeedID == shots[i].Primary {
					shots[i].PrimaryIn = avatarSourceOffset(shots[i].Start, base)
				}
				if m.NeedID == shots[i].Overlay {
					shots[i].OverlayIn = avatarSourceOffset(shots[i].Start, base)
				}
			}
		}
	}
}

// JSON decimal round trips can put the same boundary on adjacent float64s.
// Snap only sub-nanosecond subtraction noise; real negative offsets still fail.
func avatarSourceOffset(start, segmentStart float64) float64 {
	offset := start - segmentStart
	if offset < 0 && offset >= -1e-9 {
		return 0
	}
	return offset
}
