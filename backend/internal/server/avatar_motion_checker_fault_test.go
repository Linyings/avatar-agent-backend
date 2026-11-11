package server

import (
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
)

const checkerFaultLog = `AVATAR_MEDIA_ERROR={"code":"motion_text_missing","message":"场景 1 关键内容未完整展示：11 月 9 号；场景只有 8.04 秒，请按本场时长调整出场时间","scene_index":1,"scene_duration":8.04,"missing_labels":["11 月 9 号"],"short_labels":[],"drawn_labels":["11 月 9 号","Anthropic","Claude Fable 5.2"]}`

// The label the checker calls missing was drawn verbatim. Redrawing cannot
// change that, so the scene must not burn model retries on it.
func TestMotionCheckerFaultIsNotAutoRetried(t *testing.T) {
	err := mediaFailure("motion_validate", []byte(checkerFaultLog))
	if !err.checkerFault() {
		t.Fatalf("drawn label not recognised as checker fault: %#v", err)
	}
	step := avatar.WorkflowStep{ID: "F07/n1/scene-1"}
	if motionAutoRetryable(step, err) {
		t.Fatal("checker fault would be retried with the model")
	}
	if !strings.Contains(err.Error(), "校验") {
		t.Fatalf("message should explain it is a check fault: %s", err.Error())
	}
}

func TestGenuinelyMissingLabelIsStillRetried(t *testing.T) {
	log := `AVATAR_MEDIA_ERROR={"code":"motion_text_missing","message":"场景 1 关键内容未完整展示：8 美元","scene_index":1,"missing_labels":["8 美元"],"drawn_labels":["Fable 5.2","定价"]}`
	err := mediaFailure("motion_validate", []byte(log))
	if err.checkerFault() {
		t.Fatal("real omission classified as checker fault")
	}
	if !motionAutoRetryable(avatar.WorkflowStep{}, err) {
		t.Fatal("real omission must stay repairable")
	}
}

// Same diagnostic twice in a row: the repair is not working, stop early.
func TestIdenticalRepeatedFailureStopsAutoRetry(t *testing.T) {
	log := `AVATAR_MEDIA_ERROR={"code":"motion_text_too_brief","message":"场景 2 关键内容展示时间不足：40 美元","scene_index":2,"short_labels":["40 美元"],"drawn_labels":["40 美元"]}`
	err := mediaFailure("motion_validate", []byte(log))
	step := avatar.WorkflowStep{Attempt: 1}
	rememberWorkflowFailure(&step, err.Error())
	// Re-recording the same failure in the same attempt is not a repeat.
	rememberWorkflowFailure(&step, err.Error())
	step.Retry.AutomaticRetries = 1
	if motionRepeatedFailure(step, err) {
		t.Fatal("a single failure counted as a repeat")
	}
	// The model repaired it and the next attempt failed identically.
	step.Attempt = 2
	rememberWorkflowFailure(&step, err.Error())
	if !motionRepeatedFailure(step, err) {
		t.Fatal("identical diagnostic across attempts not matched")
	}
	if motionAutoRetryable(step, err) {
		t.Fatal("identical diagnostic after a repair would be retried again")
	}
	// A different diagnostic is progress and stays retryable.
	other := mediaFailure("motion_validate", []byte(`AVATAR_MEDIA_ERROR={"code":"motion_text_too_brief","message":"场景 2 关键内容展示时间不足：8 美元","scene_index":2,"short_labels":["8 美元"]}`))
	if !motionAutoRetryable(step, other) {
		t.Fatal("changed diagnostic must still be retried")
	}
}
