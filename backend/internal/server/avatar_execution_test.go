package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/models"
)

type avatarFakeTools struct {
	verifyFailure         string
	infrastructureFailure bool
}

func (f *avatarFakeTools) Check(context.Context) error            { return nil }
func (f *avatarFakeTools) Annotate(context.Context, string) error { return nil }
func (f *avatarFakeTools) Shotbook(_ context.Context, dir string) error {
	return os.WriteFile(filepath.Join(dir, "SHOTBOOK.md"), []byte("# SHOTBOOK\n"), 0600)
}
func (f *avatarFakeTools) Prepare(_ context.Context, dir string) ([]avatar.Clip, error) {
	var clips []avatar.Clip
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("shot-%d", i+1)
		os.MkdirAll(filepath.Join(dir, id), 0700)
		os.WriteFile(filepath.Join(dir, id, "audio.wav"), []byte("test audio"), 0600)
		clips = append(clips, avatar.Clip{ID: id, Start: float64(i * 5), End: float64((i + 1) * 5), Duration: 5, RequestDuration: 5})
	}
	return clips, nil
}
func (f *avatarFakeTools) Verify(_ context.Context, _ string, id string) (avatar.Clip, error) {
	if id == f.verifyFailure {
		if f.infrastructureFailure {
			return avatar.Clip{}, fmt.Errorf("test runtime unavailable")
		}
		return avatar.Clip{}, avatar.ErrQualityRejected
	}
	var n int
	fmt.Sscanf(id, "shot-%d", &n)
	return avatar.Clip{ID: id, Start: float64((n - 1) * 5), End: float64(n * 5), Duration: 5, FPS: 25}, nil
}
func (f *avatarFakeTools) Render(_ context.Context, dir string) error {
	return os.WriteFile(filepath.Join(dir, "output.mp4"), []byte("unit-test fixture, not a real generated video"), 0600)
}

type avatarFakeProvider struct {
	submits, queries int
	failQuery        bool
}

func (f *avatarFakeProvider) Upload(context.Context, string) (string, error) {
	return "https://example.com/input", nil
}
func (f *avatarFakeProvider) Submit(context.Context, avatar.GenerationRequest) (avatar.ProviderResult, error) {
	f.submits++
	return avatar.ProviderResult{TaskID: fmt.Sprintf("job-%d", f.submits), Status: "RUNNING"}, nil
}
func (f *avatarFakeProvider) Query(_ context.Context, id string) (avatar.ProviderResult, error) {
	f.queries++
	if f.failQuery {
		return avatar.ProviderResult{}, fmt.Errorf("network down")
	}
	r := avatar.ProviderResult{TaskID: id, Status: "SUCCESS"}
	r.Results = append(r.Results, struct {
		URL        string `json:"url"`
		OutputType string `json:"outputType"`
	}{URL: "https://example.com/result.mp4", OutputType: "mp4"})
	return r, nil
}
func (f *avatarFakeProvider) Download(_ context.Context, _ string, path string) error {
	return os.WriteFile(path, []byte("test fixture"), 0600)
}

func executionFixture(t *testing.T) (*App, avatarTask, uuid.UUID, *avatarFakeProvider) {
	a := newTestApp(t)
	owner := uuid.New()
	state := seedAvatarTask(t, a, owner, "queued")
	state.Baseline = true
	state.Script = "开场。讲解。总结。"
	state.Stage = "planning"
	for _, shot := range avatar.FixedStoryboard(avatar.SplitScript(state.Script)) {
		state.Shots = append(state.Shots, avatarShot{Shot: shot, Status: "pending", Submission: "none"})
	}
	a.avatarMVP.options.DataRoot = t.TempDir()
	a.avatarMVP.options.PollInterval = time.Millisecond
	a.avatarMVP.tools = &avatarFakeTools{}
	provider := &avatarFakeProvider{}
	a.avatarMVP.provider = provider
	a.avatarMVP.speech = func(context.Context, string) ([]byte, int64, error) { return []byte("unit-test voice"), 9, nil }
	os.MkdirAll(a.avatarMVP.taskDir(state.ID), 0700)
	os.WriteFile(filepath.Join(a.avatarMVP.taskDir(state.ID), "photo"), []byte("photo"), 0600)
	// The execution marker's DB state is running even while its public state is queued.
	a.db.Model(&models.WorkflowStepExecution{}).Where("workflow_run_id = ? AND step_id = ?", state.ID, avatarMarker).Update("status", "running")
	expires := time.Now().UTC().Add(workflowRunLeaseDuration)
	a.db.Model(&models.WorkflowStepExecution{}).Where("workflow_run_id = ? AND step_id = ?", state.ID, avatarMarker).Updates(map[string]any{"lease_owner": avatarOwner, "lease_expires_at": expires})
	if err := a.avatarMVP.save(state); err != nil {
		t.Fatal(err)
	}
	return a, state, owner, provider
}
func TestAvatarExecutionCompletesThreeShotsWithoutBilling(t *testing.T) {
	a, state, owner, provider := executionFixture(t)
	a.avatarMVP.execute(state, owner)
	got, err := a.avatarMVP.load(context.Background(), owner, state.ID)
	if err != nil || got.Status != "succeeded" {
		t.Fatalf("execution failed: %#v %v", got, err)
	}
	if provider.submits != 3 {
		t.Fatalf("paid submissions=%d", provider.submits)
	}
	var count int64
	a.db.Model(&models.CreditReservation{}).Count(&count)
	if count != 0 {
		t.Fatal("MVP reserved user credits")
	}
	for _, shot := range got.Shots {
		if shot.Status != "succeeded" || shot.ProviderTaskID == "" {
			t.Fatalf("lost shot result: %#v", shot)
		}
	}
	seen := map[string]bool{}
	for _, op := range got.Operations {
		if op.Scope == "step" && op.Status == "succeeded" && op.Seconds >= 0 {
			seen[op.Step+":"+op.ShotID] = true
		}
	}
	for _, step := range []string{"planning_fixed:", "tts:", "alignment:", "semantics:", "shotbook:", "rendering:",
		"upload_image:shot-1", "upload_audio:shot-1", "video_submit:shot-1", "video_wait:shot-1", "download_video:shot-1", "verification:shot-1",
		"upload_image:shot-2", "upload_audio:shot-2", "video_submit:shot-2", "video_wait:shot-2", "download_video:shot-2", "verification:shot-2",
		"upload_image:shot-3", "upload_audio:shot-3", "video_submit:shot-3", "video_wait:shot-3", "download_video:shot-3", "verification:shot-3"} {
		if !seen[step] {
			t.Fatalf("execution lost measured step %s", step)
		}
	}
}
func TestAvatarQualityFailureStopsSubsequentPaidShots(t *testing.T) {
	a, state, owner, provider := executionFixture(t)
	a.avatarMVP.tools = &avatarFakeTools{verifyFailure: "shot-1"}
	a.avatarMVP.execute(state, owner)
	got, _ := a.avatarMVP.load(context.Background(), owner, state.ID)
	if got.Status != "failed" || provider.submits != 1 || got.Shots[1].Status != "pending" {
		t.Fatalf("did not stop at failed gate: status=%s submits=%d", got.Status, provider.submits)
	}
}
func TestAvatarRetryQueriesExistingJobWithoutResubmission(t *testing.T) {
	a, state, owner, provider := executionFixture(t)
	state.Shots[0].ProviderTaskID = "existing-paid-job"
	state.Shots[0].Submission = "submitted"
	state.Shots[0].Status = "failed"
	a.avatarMVP.execute(state, owner)
	got, _ := a.avatarMVP.load(context.Background(), owner, state.ID)
	if got.Status != "succeeded" || provider.submits != 2 || got.Shots[0].ProviderTaskID != "existing-paid-job" {
		t.Fatalf("re-submitted an existing paid job: %#v submits=%d", got, provider.submits)
	}
}

func TestAvatarExplicitQualityRetryArchivesRejectedClipAndReusesSuccess(t *testing.T) {
	a, state, owner, provider := executionFixture(t)
	a.avatarMVP.tools = &avatarFakeTools{verifyFailure: "shot-2"}
	a.avatarMVP.execute(state, owner)
	failed, _ := a.avatarMVP.load(context.Background(), owner, state.ID)
	if failed.Status != "failed" || provider.submits != 2 || failed.Shots[1].Submission != "quality_failed" {
		t.Fatalf("unexpected gate result: %#v", failed)
	}
	claimed, err := a.avatarMVP.claimRetry(context.Background(), owner, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.avatarMVP.tools = &avatarFakeTools{}
	a.avatarMVP.execute(claimed, owner)
	got, _ := a.avatarMVP.load(context.Background(), owner, state.ID)
	if got.Status != "succeeded" || provider.submits != 4 || got.Shots[0].ProviderTaskID != failed.Shots[0].ProviderTaskID || len(got.Shots[1].Attempts) != 2 {
		t.Fatalf("retry regenerated successful media or lost attempts: %#v", got)
	}
	if _, err := os.Stat(filepath.Join(a.avatarMVP.taskDir(state.ID), "shot-2", "attempt-1", "source.mp4")); err != nil {
		t.Fatalf("rejected clip not preserved: %v", err)
	}
}

func TestAvatarInfrastructureRetryRechecksDownloadedSourceWithoutPayingAgain(t *testing.T) {
	a, state, owner, provider := executionFixture(t)
	a.avatarMVP.tools = &avatarFakeTools{verifyFailure: "shot-2", infrastructureFailure: true}
	a.avatarMVP.execute(state, owner)
	claimed, err := a.avatarMVP.claimRetry(context.Background(), owner, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.avatarMVP.tools = &avatarFakeTools{}
	a.avatarMVP.execute(claimed, owner)
	got, _ := a.avatarMVP.load(context.Background(), owner, state.ID)
	if got.Status != "succeeded" || provider.submits != 3 || got.Shots[1].ProviderTaskID != claimed.Shots[1].ProviderTaskID {
		t.Fatalf("infrastructure retry paid for an existing source: %#v", got)
	}
}
