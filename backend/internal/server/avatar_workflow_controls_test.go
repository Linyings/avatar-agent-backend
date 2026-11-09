package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/narratoai/backend-go/internal/avatar"
)

func TestAvatarWorkflowCompletedAudioControlsReturnChoiceWithoutRestart(t *testing.T) {
	for _, mode := range []string{"false", "true"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AVATAR_WORKFLOW_TEST_MODE", mode)
			a, owner, p, s := workflowFixture(t)
			s.Status, s.Confirmation, s.ConfirmationVersion = "awaiting_confirmation", "audio", "audio-v1"
			s.Stage = "F04/p3" // Legacy runs retain the last paragraph as their stage.
			s.ScriptConfirmed = true
			s.Segments = []avatar.Segment{{ID: "p1", Audio: "p1/audio.wav", Duration: 2}, {ID: "p2", Audio: "p2/audio.wav", Duration: 3}, {ID: "p3", Audio: "p3/audio.wav", Duration: 4}}
			if err := a.avatarWorkflow.save(&s); err != nil {
				t.Fatal(err)
			}
			before, _ := a.avatarWorkflow.load(context.Background(), owner, s.ID)
			for _, action := range []string{"retry", "resume", "retry"} {
				out, err := (avatarPackageHost{a}).Invoke(context.Background(), owner.String(), p.ID.String(), "avatar.control", map[string]any{"action": action}, "choice-"+action)
				if err != nil {
					t.Fatal(err)
				}
				r := out.(gin.H)
				if r["submitted"] != false || r["status"] != "awaiting_confirmation" || r["confirmation"] != "audio" || !strings.Contains(r["note"].(string), "全部 3 段") {
					t.Fatalf("missing batch choice: %#v", r)
				}
				if _, active := workflowLaunchLocks.Load(s.ID); active {
					t.Fatal("control restarted completed narration")
				}
			}
			if _, err := a.avatarWorkflow.retryRun(context.Background(), owner, s.ID, "", "http-retry"); err != nil {
				t.Fatal(err)
			}
			after, _ := a.avatarWorkflow.load(context.Background(), owner, s.ID)
			if avatar.Digest(before) != avatar.Digest(after) {
				t.Fatal("a control query changed the approval or generated audio")
			}
		})
	}
}

func TestAvatarWorkflowBatchAudioRunsAllBeforeOneChoice(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	s.ScriptConfirmed = true
	s.Segments = []avatar.Segment{{ID: "p1", Text: "第一句。"}, {ID: "p2", Text: "第二句。"}, {ID: "p3", Text: "第三句。"}}
	for _, seg := range s.Segments {
		dir := filepath.Join(w.projectDir(s.ProjectID), "cache", "audio", w.audioFingerprint(&s, seg))
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
			t.Fatalf("unexpected work before batch approval: %s", stage)
		}
		calls++
		current, err := w.load(context.Background(), owner, s.ID)
		if err != nil || current.Status == "awaiting_confirmation" {
			t.Fatalf("paragraph %d interrupted by approval: %s %v", calls, current.Status, err)
		}
		return json.RawMessage(`{"duration":2}`), nil
	}
	if err := w.save(&s); err != nil {
		t.Fatal(err)
	}
	w.execute(s, owner)
	after, err := w.load(context.Background(), owner, s.ID)
	if err != nil || calls != 3 || after.Status != "awaiting_confirmation" || after.Confirmation != "audio" || after.Stage != "F04" {
		t.Fatalf("wrong batch state: calls=%d status=%s stage=%s error=%s err=%v", calls, after.Status, after.Stage, after.Error, err)
	}
	for _, seg := range after.Segments {
		if seg.Audio == "" || seg.Duration <= 0 {
			t.Fatal("batch gate opened before all narration was available")
		}
	}
	if err := w.confirm(context.Background(), owner, after.ID, "audio", after.ConfirmationVersion, nil); err != nil {
		t.Fatal(err)
	}
	continued, err := w.load(context.Background(), owner, after.ID)
	if err != nil || !continued.AudioConfirmed || continued.Status != "running" || calls != 3 {
		t.Fatalf("batch choice failed to continue without repeating audio: %#v %v", continued, err)
	}
}

func TestAvatarWorkflowAudioChoiceRejectsPartialBatch(t *testing.T) {
	a, owner, _, s := workflowFixture(t)
	s.Status, s.Confirmation, s.ConfirmationVersion = "awaiting_confirmation", "audio", "v1"
	s.Segments = []avatar.Segment{{ID: "p1", Audio: "p1/audio.wav", Duration: 2}, {ID: "p2"}}
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	if err := a.avatarWorkflow.confirm(context.Background(), owner, s.ID, "audio", "v1", nil); err == nil {
		t.Fatal("incomplete audio batch approved")
	}
}
