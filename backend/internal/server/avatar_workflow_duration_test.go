package server

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
)

func narrationDurationFixture() avatar.WorkflowState {
	s := avatar.WorkflowState{Status: "failed", Stage: "F04/p7", Error: "实测配音超过90秒，请缩短文案后新建运行；不会截断原稿"}
	for i, seconds := range []float64{5.503125, 19.50025, 11.041125, 18.599208, 14.071333, 15.905708, 8.9165} {
		id := string(rune('1' + i))
		s.Segments = append(s.Segments, avatar.Segment{ID: id, Audio: id + "/audio.wav", Duration: seconds})
		s.Steps = append(s.Steps, avatar.WorkflowStep{ID: "F04/" + id, Status: "succeeded", Seconds: 150})
	}
	return s
}

func TestWorkflowNarrationDurationUsesAudioSumAndAllowsLegacyRecovery(t *testing.T) {
	s := narrationDurationFixture()
	timing := workflowAudioTiming(s)
	if timing.Total != 7 || timing.Completed != 7 || math.Abs(timing.Seconds-93.537249) > 1e-6 || timing.LongestSeconds != 19.50025 {
		t.Fatalf("must sum audio durations, not generation time or stale run duration: %+v", timing)
	}
	data, _ := json.Marshal(timing)
	var fields map[string]any
	_ = json.Unmarshal(data, &fields)
	if timing.Issue != nil || fields["resume_available"] != true || fields["limit_seconds"] != nil {
		t.Fatalf("legacy duration failure must be recoverable without a limit: %s", data)
	}
	s.Error, s.ErrorType = "7 段配音累计时长超过旧上限", "narration_duration_exceeded"
	if !workflowAudioTiming(s).ResumeAvailable {
		t.Fatal("typed historical failure is not recoverable")
	}
	for _, status := range []string{"running", "succeeded", "awaiting_confirmation"} {
		s.Status = status
		if current := workflowAudioTiming(s); current.Issue != nil || current.ResumeAvailable {
			t.Fatalf("stale error blocks %s", status)
		}
	}
	s.Status = "failed"
	s.Segments[0].Audio = ""
	if current := workflowAudioTiming(s); current.ResumeAvailable || current.Issue == nil || current.Issue.Code != "narration_duration_invalid" {
		t.Fatal("missing audio was mistaken for recoverable duration policy")
	}
}

func TestWorkflowNarrationDurationValidationBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		durations []float64
		want      string
	}{
		{"empty", nil, "narration_duration_invalid"},
		{"zero", []float64{0}, "narration_duration_invalid"},
		{"invalid segment", []float64{93, 0}, "narration_duration_invalid"},
		{"negative", []float64{-1}, "narration_duration_invalid"},
		{"nan", []float64{math.NaN()}, "narration_duration_invalid"},
		{"infinite", []float64{math.Inf(1)}, "narration_duration_invalid"},
		{"ninety seconds", []float64{30, 30, 30}, ""},
		{"over former limit", []float64{30, 30, 33.54}, ""},
		{"ten minutes", []float64{120, 180, 300}, ""},
		{"several hours", []float64{3600, 7200, 1800}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s avatar.WorkflowState
			for _, d := range tc.durations {
				s.Segments = append(s.Segments, avatar.Segment{Audio: "audio.wav", Duration: d})
			}
			err := validateWorkflowNarration(s)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || err.Code != tc.want {
				t.Fatalf("want %s, got %+v", tc.want, err)
			}
			if tc.want == "narration_duration_invalid" && strings.Contains(err.Message, "超过") {
				t.Fatal("invalid duration must not report an overrun")
			}
		})
	}
}

func TestWorkflowNarrationDurationGeneratedScriptFollowsContentLength(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	s.Input.Script = ""
	s.Input.Topic = "需要完整讲解的长篇主题"
	script := strings.Repeat("这是一句需要保留的完整解说。", 100)
	a.avatarWorkflow.content = func(_ context.Context, prompt string, _ any, _ []string) (json.RawMessage, error) {
		if strings.Contains(prompt, "写中文口播稿") {
			if strings.Contains(prompt, "不超过90秒") || strings.Contains(prompt, "目标20–60秒") {
				t.Fatal("prompt still forces a short video")
			}
			out, err := json.Marshal(map[string]any{"title": "长篇", "script": script})
			return out, err
		}
		out, err := json.Marshal(map[string]any{"segments": []avatar.Segment{{ID: "p1", Text: script}}})
		return out, err
	}
	if err := a.avatarWorkflow.produceWorkflow(context.Background(), &s, owner); err != nil {
		t.Fatal(err)
	}
	if s.Status != "awaiting_confirmation" || s.Confirmation != "script" || s.Script != script {
		t.Fatal("long script was rejected or truncated")
	}
}

func TestWorkflowNarrationDurationRetryPreservesCompletedAudio(t *testing.T) {
	for _, mode := range []string{"false", "true"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AVATAR_WORKFLOW_TEST_MODE", mode)
			a, owner, _, s := workflowFixture(t)
			failed := narrationDurationFixture()
			s.Status, s.Stage, s.Error, s.Segments, s.Steps = failed.Status, failed.Stage, failed.Error, failed.Segments, failed.Steps
			if err := a.avatarWorkflow.save(&s); err != nil {
				t.Fatal(err)
			}
			before, _ := a.avatarWorkflow.load(context.Background(), owner, s.ID)
			if workflowControlReadOnly(s, "retry") || workflowControlReadOnly(s, "resume") {
				t.Fatal("removed duration policy still blocks recovery")
			}
			if _, err := a.avatarWorkflow.retryRun(context.Background(), owner, s.ID, "", "duration-http"); err != nil {
				t.Fatal(err)
			}
			after, _ := a.avatarWorkflow.load(context.Background(), owner, s.ID)
			if after.Status != "running" || after.Error != "" || after.ErrorType != "" {
				t.Fatalf("duration failure cannot recover: %+v", after)
			}
			if avatar.Digest(before.Segments) != avatar.Digest(after.Segments) || avatar.Digest(before.Steps) != avatar.Digest(after.Steps) {
				t.Fatal("retry discarded completed audio or submitted it again")
			}
			if _, active := workflowLaunchLocks.Load(s.ID); active {
				t.Fatal("retry started a worker")
			}
		})
	}
}

func TestWorkflowNarrationDurationPipelineAcceptsLongAudioAndReusesItOnRecovery(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	s.ScriptConfirmed = true
	s.Segments = []avatar.Segment{{ID: "p1", Text: "第一句。"}, {ID: "p2", Text: "第二句。"}, {ID: "p3", Text: "第三句。"}}
	for _, segment := range s.Segments {
		dir := filepath.Join(w.projectDir(s.ProjectID), "cache", "audio", w.audioFingerprint(&s, segment))
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "audio.wav"), []byte("fixture-audio"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	w.media = func(_ context.Context, stage, _ string) (json.RawMessage, error) {
		if stage == "audio_review" {
			return json.RawMessage(`{"audio_quality":{"issues":[]}}`), nil
		}
		if stage != "audio" {
			t.Fatalf("unexpected downstream work: %s", stage)
		}
		calls++
		return json.RawMessage(`{"duration":31.18}`), nil
	}
	w.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
		t.Fatal("unexpected model request")
		return nil, nil
	}
	if err := w.save(&s); err != nil {
		t.Fatal(err)
	}
	w.execute(s, owner)
	after, err := w.load(context.Background(), owner, s.ID)
	if err != nil || calls != 3 || after.Status != "awaiting_confirmation" || after.Stage != "F04" || after.Confirmation != "audio" || after.ErrorType != "" {
		t.Fatalf("unexpected aggregate validation: calls=%d state=%+v err=%v", calls, after, err)
	}
	if math.Abs(after.Duration-93.54) > 1e-6 || math.Abs(after.Segments[2].Start-62.36) > 1e-6 {
		t.Fatal("lost calibrated timing")
	}
	for _, segment := range after.Segments {
		if _, err := os.Stat(filepath.Join(w.dir(s.ID), segment.Audio)); err != nil {
			t.Fatal("completed audio lost", err)
		}
	}
	for _, step := range after.Steps {
		if step.Status != "succeeded" {
			t.Fatalf("successful audio reported as failed: %+v", step)
		}
	}
	// Simulate the persisted failure produced by the retired policy. Recovery
	// must reuse every successful audio step and stop at the batch approval.
	after.Status, after.Confirmation = "failed", ""
	after.ErrorType = "narration_duration_exceeded"
	after.Error = "实测配音超过90秒，请缩短文案后新建运行；不会截断原稿"
	if err := w.save(&after); err != nil {
		t.Fatal(err)
	}
	next, err := w.retryRun(context.Background(), owner, after.ID, "", "resume-long-audio")
	if err != nil {
		t.Fatal(err)
	}
	w.execute(next, owner)
	recovered, err := w.load(context.Background(), owner, after.ID)
	if err != nil || recovered.Status != "awaiting_confirmation" || recovered.Confirmation != "audio" || calls != 3 || recovered.Duration != after.Duration {
		t.Fatalf("recovery repeated audio or bypassed approval: calls=%d state=%+v err=%v", calls, recovered, err)
	}
}
