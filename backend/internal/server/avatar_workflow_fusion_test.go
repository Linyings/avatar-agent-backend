package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
)

// Exercise the real F10 model boundary with cached upstream work and local media
// stubs: the selected style and fusion direction must reach the shot planner.
func TestAvatarWorkflowFusionDirectionReachesShotDesign(t *testing.T) {
	w, s := interruptedShotsFixture(t)
	enabled := true
	s.Input.UseAvatar, s.Input.UseAnimation = true, &enabled
	s.Input.Direction = "人物口播与动效分层融合，不遮挡面部"
	s.Input.Style = "mg-isometric"
	s.Status, s.Stage = "running", "F10"
	s.Segments[0].Audio = "audio/p1.wav"
	s.Steps = nil
	for _, cached := range []struct {
		id    string
		input any
	}{
		{"F02", []any{s.Input.Script, s.Input.Topic, s.Descriptions}},
		{"F04/p1", w.audioFingerprint(&s, s.Segments[0])},
		{"F05", []any{s.Segments, s.Input.Style, s.Input.Ratio, s.Input.Direction, s.Input.Angles, true}},
		{"F09", []any{s.Segments, s.Materials, s.Input.SubtitleEdits}},
	} {
		s.Steps = append(s.Steps, avatar.WorkflowStep{ID: cached.id, Status: "succeeded", InputHash: avatar.Digest(cached.input)})
	}
	if err := w.save(&s); err != nil {
		t.Fatal(err)
	}
	modelCalls := 0
	w.content = func(_ context.Context, _ string, input any, _ []string) (json.RawMessage, error) {
		modelCalls++
		in := input.(avatar.ShotPlanningBatch)
		if in.Direction != s.Input.Direction || in.Style != s.Input.Style || !in.UseAvatar || !in.UseAnimation {
			t.Fatalf("shot planner lost fusion requirements: %v", in)
		}
		return json.RawMessage(`{"shots":[{"id":"S1","segment_id":"p1","start":0,"end":2,"primary":"visual","overlay":"host","recipe":"visual-pip","layout":"pip","card":"none"},{"id":"S2","segment_id":"p1","start":2,"end":4,"primary":"host","primary_in":2,"recipe":"host-hero","layout":"full","card":"none"}]}`), nil
	}
	stages := map[string]bool{}
	w.media = func(_ context.Context, stage, _ string) (json.RawMessage, error) {
		stages[stage] = true
		return json.RawMessage(`{"plan":{},"artifacts":[]}`), nil
	}
	if err := w.produceWorkflow(context.Background(), &s, uuid.MustParse(w.ownerID(&s))); err != nil {
		t.Fatal(err)
	}
	if modelCalls != 1 || !stages["preview"] || !stages["render"] || len(s.Shots) != 2 || s.Shots[0].Overlay != "host" {
		t.Fatalf("fusion did not reach render: calls=%d stages=%v shots=%v", modelCalls, stages, s.Shots)
	}
}

func TestAvatarWorkflowConfigurePreservesHomeFusionSelection(t *testing.T) {
	a, owner, p, s := workflowFixture(t)
	enabled := true
	s.Input.UseAvatar, s.Input.UseAnimation = true, &enabled
	s.Input.Direction = "人物口播与动效分层融合"
	if err := a.db.Model(&p).Update("project_metadata", jsonMap(map[string]any{"kind": "avatar_mvp", "avatar_v2": s.Input})).Error; err != nil {
		t.Fatal(err)
	}
	h := avatarPackageHost{a}
	_, err := h.Invoke(context.Background(), owner.String(), p.ID.String(), "avatar.configure", map[string]any{"topic": "更新口播主题"}, "fusion-topic-edit")
	if err != nil {
		t.Fatal(err)
	}
	_, got, err := h.project(context.Background(), owner.String(), p.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	if got.Topic != "更新口播主题" || !got.UseAvatar || got.UseAnimation == nil || !*got.UseAnimation || got.Style != s.Input.Style || got.Direction != s.Input.Direction {
		t.Fatalf("topic edit replaced saved fusion settings: %#v", got)
	}
}
