package server

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
)

func deleteProjectRequest(a *App, owner, project uuid.UUID) *httptest.ResponseRecorder {
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user", models.User{Base: models.Base{ID: owner}}) })
	r.DELETE("/:project_id", a.deleteProject)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("DELETE", "/"+project.String(), nil))
	return rec
}

func TestProjectCleanupMediaScratchRemovedOnSuccessAndFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux media runner")
	}
	a := newTestApp(t)
	a.avatarMVP.options.DataRoot = t.TempDir()
	a.avatarMVP.options.RuntimeRoot = t.TempDir()
	a.avatarMVP.options.Python = "/bin/sh"
	task := t.TempDir()
	for _, exit := range []string{"0", "1"} {
		script := "#!/bin/sh\nmkdir -p \"$TMPDIR/render-bundle\"\nprintf '%s' \"$TMPDIR\" > \"$3/scratch-path\"\nprintf '{}' > \"$3/media-result.json\"\nexit " + exit + "\n"
		if err := os.WriteFile(filepath.Join(a.avatarMVP.options.RuntimeRoot, "workflow.py"), []byte(script), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := a.avatarWorkflow.runMedia(context.Background(), "probe", task)
		if (err != nil) != (exit == "1") {
			t.Fatalf("exit=%s err=%v", exit, err)
		}
		path, err := os.ReadFile(filepath.Join(task, "scratch-path"))
		if err != nil {
			t.Fatal(err)
		}
		if len(path) == 0 {
			t.Fatal("media runner has no isolated scratch directory")
		}
		if _, err := os.Stat(string(path)); !os.IsNotExist(err) {
			t.Fatalf("scratch remains: %s", path)
		}
	}
}

func cleanupFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestProjectCleanupRemovesAllOwnedFilesAndKeepsOtherProjects(t *testing.T) {
	a, owner, p, s := workflowFixture(t)
	s.Status = "failed"
	if err := a.avatarWorkflow.save(&s); err != nil {
		t.Fatal(err)
	}
	paths := []string{
		filepath.Join(a.avatarWorkflow.projectDir(p.ID.String()), "assets", "original"),
		filepath.Join(a.avatarWorkflow.projectDir(p.ID.String()), "cache", "render.mp4"),
		filepath.Join(a.avatarWorkflow.dir(s.ID), "output.mp4"),
		filepath.Join(a.avatarMVP.taskDir(uuid.MustParse(s.ID)), "photo"),
	}
	for _, path := range paths {
		cleanupFile(t, path)
	}
	other := filepath.Join(a.avatarWorkflow.projectDir(uuid.NewString()), "assets", "keep")
	cleanupFile(t, other)
	if rec := deleteProjectRequest(a, uuid.New(), p.ID); rec.Code != 404 {
		t.Fatalf("cross-owner: %d", rec.Code)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	if rec := deleteProjectRequest(a, owner, p.ID); rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("file remains: %s (%v)", path, err)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("other project removed", err)
	}
	var count int64
	a.db.Model(&models.WorkflowRun{}).Where("project_id=?", p.ID).Count(&count)
	if count != 0 {
		t.Fatal("run remains")
	}
}

func TestProjectCleanupWithoutRun(t *testing.T) {
	a := newTestApp(t)
	a.avatarMVP.options.DataRoot = t.TempDir()
	p := models.Project{Name: "upload only", OwnerID: uuid.New()}
	a.db.Create(&p)
	path := filepath.Join(a.avatarWorkflow.projectDir(p.ID.String()), "assets", "original")
	cleanupFile(t, path)
	if rec := deleteProjectRequest(a, p.OwnerID, p.ID); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("upload without run remains")
	}
}

func TestProjectCleanupRejectsDelayedLaunch(t *testing.T) {
	a, owner, p, s := workflowFixture(t)
	s.Status = "failed"
	a.avatarWorkflow.save(&s)
	if rec := deleteProjectRequest(a, owner, p.ID); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	s.Status = "running" // stale HTTP handler resumed after deletion committed
	a.avatarWorkflow.launch(s, owner)
	_, launched := workflowLaunchLocks.Load(s.ID)
	if launched {
		time.Sleep(50 * time.Millisecond)
		t.Fatal("deleted workflow launched from stale handler")
	}
	if _, err := os.Stat(a.avatarWorkflow.dir(s.ID)); !os.IsNotExist(err) {
		t.Fatal("deleted run recreated")
	}
}

func TestProjectCleanupReleasesReservationsAndDeletesRecords(t *testing.T) {
	a, owner, p, s := workflowFixture(t)
	s.Status = "failed"
	a.avatarWorkflow.save(&s)
	runID := uuid.MustParse(s.ID)
	reservation := models.CreditReservation{UserID: owner, ProjectID: &p.ID, WorkflowRunID: &runID, Status: creditReservationPending, IdempotencyKey: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := a.db.Create(&reservation).Error; err != nil {
		t.Fatal(err)
	}
	message := models.AgentMessage{ProjectID: p.ID, UserID: owner, Role: "user", Content: "test"}
	if err := a.db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	if rec := deleteProjectRequest(a, owner, p.ID); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	a.db.First(&reservation, "id=?", reservation.ID)
	if reservation.Status != creditReservationReleased {
		t.Fatal("credits remain reserved")
	}
	var count int64
	a.db.Unscoped().Model(&models.AgentMessage{}).Where("project_id=?", p.ID).Count(&count)
	if count != 0 {
		t.Fatal("conversation remains")
	}
}

func TestProjectCleanupBlocksActiveAndFinishingWorker(t *testing.T) {
	a, owner, p, s := workflowFixture(t)
	path := filepath.Join(a.avatarWorkflow.dir(s.ID), "video.mp4")
	cleanupFile(t, path)
	s.Status = "running"
	a.avatarWorkflow.save(&s)
	if rec := deleteProjectRequest(a, owner, p.ID); rec.Code != 409 {
		t.Fatalf("active delete: %d", rec.Code)
	}
	s.Status = "succeeded"
	a.avatarWorkflow.save(&s)
	workflowLaunchLocks.Store(s.ID, &workflowLaunchState{})
	defer workflowLaunchLocks.Delete(s.ID)
	if rec := deleteProjectRequest(a, owner, p.ID); rec.Code != 409 {
		t.Fatalf("finishing delete: %d", rec.Code)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	workflowLaunchLocks.Delete(s.ID)
	if rec := deleteProjectRequest(a, owner, p.ID); rec.Code != 200 {
		t.Fatalf("idle delete: %d %s", rec.Code, rec.Body)
	}
}

func TestProjectCleanupRejectsSymlinkParentAndCanRetry(t *testing.T) {
	a := newTestApp(t)
	a.avatarMVP.options.DataRoot = t.TempDir()
	p := models.Project{Name: "retry", OwnerID: uuid.New()}
	a.db.Create(&p)
	external := t.TempDir()
	keep := filepath.Join(external, "projects", p.ID.String(), "original")
	cleanupFile(t, keep)
	link := filepath.Join(a.avatarMVP.options.DataRoot, "workflow-v2")
	if err := os.Symlink(external, link); err != nil {
		t.Skip(err)
	}
	if rec := deleteProjectRequest(a, p.OwnerID, p.ID); rec.Code != 500 {
		t.Fatalf("symlink delete: %d", rec.Code)
	}
	var count int64
	a.db.Model(&models.Project{}).Where("id=?", p.ID).Count(&count)
	if count != 1 {
		t.Fatal("cleanup failure hid project; cannot retry")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("followed symlink", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if rec := deleteProjectRequest(a, p.OwnerID, p.ID); rec.Code != 200 {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}
}
