package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/providers"
)

type completedModelOutputFixture struct{}

func (completedModelOutputFixture) Error() string                { return "private response" }
func (completedModelOutputFixture) ProviderHTTPStatus() int      { return 200 }
func (completedModelOutputFixture) ProviderOutputReceived() bool { return true }
func (completedModelOutputFixture) ProviderDiagnosticFields() map[string]string {
	return map[string]string{"error_code": "model_output_truncated"}
}

func TestAvatarMaterialPlanningOutputFailureIsRetryable(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	calls := 0
	a.avatarWorkflow.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		calls++
		return nil, completedModelOutputFixture{}
	}
	s.Steps = []avatar.WorkflowStep{{ID: "F05"}}
	var out any
	err := a.avatarWorkflow.model(context.Background(), &s, &s.Steps[0], "plan", nil, nil, &out)
	if err == nil || s.Steps[0].Submission != "completed" || s.Steps[0].ErrorType != "model_output" || calls != 1 || !strings.Contains(err.Error(), "输出被截断") || strings.Contains(err.Error(), "待确认") {
		t.Fatalf("wrong output failure: %v %+v calls=%d", err, s.Steps[0], calls)
	}
}

func TestAvatarMaterialPlanningBatchesAndResumesCompletedParts(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	for i := 0; i < 7; i++ {
		s.Segments = append(s.Segments, avatar.Segment{ID: fmt.Sprintf("p%d", i+1), Text: "解说", Duration: 30, Audio: fmt.Sprintf("p%d.wav", i+1)})
	}
	s.Director = &avatar.DirectorPlan{Summary: "全片主线", Scenes: []avatar.DirectorScene{{ID: "scene1", SegmentIDs: []string{"p1"}, Visual: "开场"}, {ID: "scene7", SegmentIDs: []string{"p7"}, Visual: "结尾"}}}
	s.Input.UseAvatar = false
	if err := w.save(&s); err != nil {
		t.Fatal(err)
	}
	calls := 0
	fail := true
	w.content = func(_ context.Context, _ string, input any, _ []string) (json.RawMessage, error) {
		calls++
		data := input.(map[string]any)
		segs := data["segments"].([]avatar.Segment)
		if len(segs) > 2 {
			t.Fatalf("unbounded planning batch: %d", len(segs))
		}
		if segs[0].ID == "p3" && fail {
			return nil, completedModelOutputFixture{}
		}
		var needs []avatar.MaterialNeed
		for i, seg := range segs {
			needs = append(needs, avatar.MaterialNeed{ID: fmt.Sprintf("n%d", i+1), SegmentID: seg.ID, Kind: "animation", Duration: seg.Duration, Template: "steps", Content: "解说"})
		}
		return json.Marshal(map[string]any{"needs": needs})
	}
	run := func() error {
		return w.step(context.Background(), &s, "F05", "fixture", func(ctx context.Context, step *avatar.WorkflowStep) error {
			needs, err := w.planMaterialNeeds(ctx, &s, step, "列出素材需求", s.Input)
			if err == nil {
				s.Needs = needs
			}
			return err
		})
	}
	if err := run(); err == nil || calls != 2 || len(s.Needs) != 0 {
		t.Fatalf("partial plan escaped: %v calls=%d", err, calls)
	}
	fail = false
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if calls != 5 || len(s.Needs) != 7 {
		t.Fatalf("completed batch resubmitted or missing plan: calls=%d needs=%d", calls, len(s.Needs))
	}
	if err := avatar.ValidateNeeds(s.Input, s.Segments, s.Needs); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil || calls != 5 {
		t.Fatal("completed plan was billed again")
	}
	if _, err := os.Stat(filepath.Join(w.dir(s.ID), "material-plans")); err != nil {
		t.Fatal("missing persisted batch cache")
	}
}

func TestAvatarMaterialPlanningRejectsForeignSegments(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	for i := 0; i < 3; i++ {
		s.Segments = append(s.Segments, avatar.Segment{ID: fmt.Sprintf("p%d", i+1), Audio: "audio.wav", Duration: 5})
	}
	a.avatarWorkflow.save(&s)
	calls := 0
	a.avatarWorkflow.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"needs":[{"id":"n1","segment_id":"p3","kind":"animation","duration":5,"template":"steps"}]}`), nil
	}
	err := a.avatarWorkflow.step(context.Background(), &s, "F05", "fixture", func(ctx context.Context, step *avatar.WorkflowStep) error {
		_, err := a.avatarWorkflow.planMaterialNeeds(ctx, &s, step, "plan", s.Input)
		return err
	})
	if err == nil || calls != 1 {
		t.Fatalf("foreign batch output accepted: %v %d", err, calls)
	}
}

func TestAvatarMaterialPlanningRejectsInvalidSourceBeforeCaching(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	s.Input.Assets = []avatar.SourceAsset{{ID: "v1", Kind: "video", Duration: 20}}
	s.Descriptions = []avatar.SourceDescription{{ID: "clip1", AssetID: "v1", Start: 0, End: 5}}
	for i := 0; i < 3; i++ {
		s.Segments = append(s.Segments, avatar.Segment{ID: fmt.Sprintf("p%d", i+1), Audio: "audio.wav", Duration: 5})
	}
	w.save(&s)
	calls := 0
	bad := true
	w.content = func(_ context.Context, _ string, input any, _ []string) (json.RawMessage, error) {
		calls++
		var needs []avatar.MaterialNeed
		for i, seg := range input.(map[string]any)["segments"].([]avatar.Segment) {
			start := 0.0
			if bad {
				start = 10
			}
			needs = append(needs, avatar.MaterialNeed{ID: fmt.Sprintf("n%d", i+1), SegmentID: seg.ID, Kind: "real", SourceID: "v1", SourceStart: start, Duration: 5})
		}
		return json.Marshal(map[string]any{"needs": needs})
	}
	run := func() error {
		return w.step(context.Background(), &s, "F05", "source-fixture", func(ctx context.Context, step *avatar.WorkflowStep) error {
			needs, err := w.planMaterialNeeds(ctx, &s, step, "plan", s.Input)
			if err != nil {
				return err
			}
			return avatar.BindSourceDescriptions(s.Input, s.Descriptions, needs)
		})
	}
	if err := run(); err == nil || calls != 1 {
		t.Fatalf("invalid source binding cached or later batch billed: err=%v calls=%d", err, calls)
	}
	bad = false
	if err := run(); err != nil || calls != 3 {
		t.Fatalf("explicit retry cannot replace invalid output: err=%v calls=%d", err, calls)
	}
}

func TestAvatarMaterialPlanningRetriesMissingGlobalRequirements(t *testing.T) {
	for _, kind := range []string{"animation", "angle"} {
		t.Run(kind, func(t *testing.T) {
			a, _, _, s := workflowFixture(t)
			w := a.avatarWorkflow
			s.Input.UseAvatar = true
			on := kind == "animation"
			s.Input.UseAnimation = &on
			if kind == "angle" {
				s.Input.Direction = "使用正面和侧面"
				s.Input.Angles = []string{"正面", "侧面"}
			}
			for i := 0; i < 3; i++ {
				s.Segments = append(s.Segments, avatar.Segment{ID: fmt.Sprintf("p%d", i+1), Audio: "audio.wav", Duration: 5})
			}
			w.save(&s)
			calls := 0
			bad := true
			w.content = func(_ context.Context, _ string, input any, _ []string) (json.RawMessage, error) {
				calls++
				var needs []avatar.MaterialNeed
				for i, seg := range input.(map[string]any)["segments"].([]avatar.Segment) {
					angle := "正面"
					if seg.ID == "p3" && !bad && kind == "angle" {
						angle = "侧面"
					}
					needs = append(needs, avatar.MaterialNeed{ID: fmt.Sprintf("n%d", i+1), SegmentID: seg.ID, Kind: "avatar", Angle: angle, Duration: 5})
					if seg.ID == "p3" && !bad && kind == "animation" {
						needs = append(needs, avatar.MaterialNeed{ID: "motion", SegmentID: seg.ID, Kind: "animation", Template: "steps", Duration: 5})
					}
				}
				return json.Marshal(map[string]any{"needs": needs})
			}
			run := func() error {
				return w.step(context.Background(), &s, "F05", "requirements", func(ctx context.Context, step *avatar.WorkflowStep) error {
					_, err := w.planMaterialNeeds(ctx, &s, step, "plan", s.Input)
					return err
				})
			}
			if err := run(); err == nil || calls != 2 {
				t.Fatalf("missing requirement accepted: %v calls=%d", err, calls)
			}
			bad = false
			if err := run(); err != nil || calls != 3 {
				t.Fatalf("invalid last batch cannot be replaced without repeating valid batches: %v calls=%d", err, calls)
			}
		})
	}
}

// Explicit, text-only acceptance using an isolated fixture DB. The real task is
// read from a snapshot; no real run state, audio or video jobs are changed.
func TestAvatarMaterialPlanningLive(t *testing.T) {
	file := os.Getenv("AVATAR_MATERIAL_PLAN_LIVE_INPUT")
	if file == "" {
		t.Skip("live material planning is opt-in")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var original avatar.WorkflowState
	if err = json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	s.Version, s.Input, s.Segments, s.Director, s.Descriptions = original.Version, original.Input, original.Segments, original.Director, original.Descriptions
	s.ProductionNotes, s.ResolvedAngles = original.ProductionNotes, original.ResolvedAngles
	a.avatarMVP.options.RuntimeRoot = os.Getenv("AVATAR_MATERIAL_PLAN_RUNTIME")
	output := os.Getenv("AVATAR_MATERIAL_PLAN_LIVE_OUTPUT")
	if err = os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(w.dir(s.ID), 0700); err != nil {
		t.Fatal(err)
	}
	if err = w.save(&s); err != nil {
		t.Fatal(err)
	}
	client := providers.NewOpenAIClient(config.Load(), nil)
	calls := 0
	w.content = func(ctx context.Context, prompt string, input any, frames []string) (json.RawMessage, error) {
		calls++
		result, err := client.AvatarContent(ctx, prompt, input, frames)
		t.Logf("material planning batch %d: returned=%t valid_json=%t", calls, err == nil, json.Valid(result))
		return result, err
	}
	w.media = func(context.Context, string, string) (json.RawMessage, error) {
		return nil, fmt.Errorf("media generation is forbidden in this diagnostic")
	}
	production := s.Input
	if len(s.ProductionNotes) > 0 {
		production.Direction += "\n" + strings.Join(s.ProductionNotes, "\n")
		if len(production.Angles) == 0 {
			production.Angles = s.ResolvedAngles
		}
	}
	ctx := avatar.WithTelemetry(context.Background(), output+".operations.jsonl")
	err = w.step(ctx, &s, "F05", "live-plan-check", func(ctx context.Context, step *avatar.WorkflowStep) error {
		needs, err := w.planMaterialNeeds(ctx, &s, step, w.materialPlanningPrompt(&s), production)
		if err != nil {
			return err
		}
		if err = avatar.ValidateNeeds(production, s.Segments, needs); err != nil {
			return err
		}
		if err = avatar.BindSourceDescriptions(s.Input, s.Descriptions, needs); err != nil {
			return err
		}
		s.Needs = needs
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = avatar.WriteJSON(output, map[string]any{"needs": s.Needs, "segments": len(s.Segments), "requests": calls, "video_requests": 0, "total_audio_seconds": original.Duration}); err != nil {
		t.Fatal(err)
	}
	before := calls
	if err = w.step(ctx, &s, "F05", "live-plan-check", func(context.Context, *avatar.WorkflowStep) error { return fmt.Errorf("successful plan must be reused") }); err != nil || calls != before {
		t.Fatal("retry did not reuse completed plan", err)
	}
	t.Logf("validated %d needs over %d segments; %d text requests, zero video requests", len(s.Needs), len(s.Segments), calls)
}
