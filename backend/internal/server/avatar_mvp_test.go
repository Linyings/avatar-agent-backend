package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/models"
)

func seedAvatarTask(t *testing.T, a *App, owner uuid.UUID, status string) avatarTask {
	t.Helper()
	project := models.Project{Name: "avatar test", OwnerID: owner}
	if err := a.db.Create(&project).Error; err != nil {
		t.Fatal(err)
	}
	run, err := a.workflowRuns.Create(context.Background(), CreateWorkflowRunInput{UserID: owner, ProjectID: &project.ID, ClientType: "web", InputHash: strings.Repeat("a", 64), ClientInstanceID: "avatar-server"})
	if err != nil {
		t.Fatal(err)
	}
	state := avatarTask{ID: run.Run.ID, ProjectID: project.ID, Status: status, Stage: "prepare", CreatedAt: time.Now().UTC(), Shots: []avatarShot{}}
	step := models.WorkflowStepExecution{WorkflowRunID: state.ID, StepID: avatarMarker, InputHash: run.Run.InputHash, ExecutionKey: workflowStepExecutionKey(state.ID, avatarMarker, run.Run.InputHash), Status: status, Result: jsonMap(state)}
	if err = a.db.Create(&step).Error; err != nil {
		t.Fatal(err)
	}
	return state
}
func TestAvatarOwnershipAndAuthenticatedRange(t *testing.T) {
	a := newTestApp(t)
	owner := uuid.New()
	state := seedAvatarTask(t, a, owner, "succeeded")
	a.avatarMVP.options.DataRoot = t.TempDir()
	dir := a.avatarMVP.taskDir(state.ID)
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, "output.mp4"), []byte("0123456789"), 0600)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("user", models.User{Base: models.Base{ID: owner}}) })
	router.GET("/:id", a.avatarVideo)
	req := httptest.NewRequest("GET", "/"+state.ID.String(), nil)
	req.Header.Set("Range", "bytes=2-4")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusPartialContent || w.Body.String() != "234" {
		t.Fatalf("range: %d %s", w.Code, w.Body.String())
	}
	router = gin.New()
	router.Use(func(c *gin.Context) { c.Set("user", models.User{Base: models.Base{ID: uuid.New()}}) })
	router.GET("/:id", a.avatarGet)
	router.GET("/:id/video", a.avatarVideo)
	router.POST("/:id/retry", a.avatarRetry)
	for _, path := range []string{"/" + state.ID.String(), "/" + state.ID.String() + "/video", "/" + state.ID.String() + "/retry"} {
		method := "GET"
		if strings.HasSuffix(path, "retry") {
			method = "POST"
		}
		w = httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != 404 {
			t.Fatalf("foreign access: %s %d", path, w.Code)
		}
	}
}
func TestAvatarRejectsUnknownSubmissionAndConcurrentRetry(t *testing.T) {
	a := newTestApp(t)
	owner := uuid.New()
	state := seedAvatarTask(t, a, owner, "failed")
	state.Shots = []avatarShot{{Shot: avatar.FixedStoryboard([]string{"一。", "二。", "三。"})[0], Submission: "submitting", Status: "failed"}}
	if err := a.avatarMVP.save(state); err != nil {
		t.Fatal(err)
	}
	_, err := a.avatarMVP.claimRetry(context.Background(), owner, state.ID)
	if err == nil || !strings.Contains(err.Error(), "确认") {
		t.Fatalf("unknown submission allowed: %v", err)
	}
	state.Shots = nil
	state.Status = "running"
	a.avatarMVP.save(state)
	_, err = a.avatarMVP.claimRetry(context.Background(), owner, state.ID)
	if err == nil {
		t.Fatal("accepted concurrent retry")
	}
}
func TestAvatarInterruptedTaskPreservesProviderID(t *testing.T) {
	a := newTestApp(t)
	state := seedAvatarTask(t, a, uuid.New(), "running")
	state.Shots = []avatarShot{{Status: "running", Submission: "submitted", ProviderTaskID: "paid-job"}}
	a.avatarMVP.save(state)
	expired := time.Now().UTC().Add(-time.Minute)
	a.db.Model(&models.WorkflowStepExecution{}).Where("workflow_run_id = ? AND step_id = ?", state.ID, avatarMarker).Updates(map[string]any{"lease_expires_at": expired})
	if err := a.avatarMVP.recoverInterrupted(context.Background()); err != nil {
		t.Fatal(err)
	}
	var row models.WorkflowStepExecution
	a.db.Where("workflow_run_id = ? AND step_id = ?", state.ID, avatarMarker).First(&row)
	var got avatarTask
	json.Unmarshal(row.Result, &got)
	if got.Status != "interrupted" || got.Shots[0].ProviderTaskID != "paid-job" {
		t.Fatalf("lost job on restart: %#v", got)
	}
}
func TestAvatarUnconfiguredServiceDoesNotAcceptPaidTask(t *testing.T) {
	a := newTestApp(t)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user", models.User{}) })
	r.POST("/tasks", a.avatarCreate)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/tasks", nil))
	if w.Code != 503 {
		t.Fatalf("unconfigured service accepted: %d", w.Code)
	}
}

func TestAvatarHTTPIdempotencyGlobalBusyAndBaselineGate(t *testing.T) {
	a := newTestApp(t)
	owner := uuid.New()
	state := seedAvatarTask(t, a, owner, "running")
	a.avatarMVP.options.Enabled = true
	a.avatarMVP.options.APIKey = "test-key"
	a.cfg.DashScopeAPIKey = "test-key"
	a.cfg.QwenTTSModel = "qwen3-tts-flash"
	a.avatarMVP.tools = &avatarFakeTools{}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user", models.User{Base: models.Base{ID: owner}}) })
	r.POST("/tasks", a.avatarCreate)
	requestID := uuid.NewString()
	submit := func(id string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, _ := form.CreateFormFile("photo", "test.png")
		if err := png.Encode(part, image.NewRGBA(image.Rect(0, 0, 256, 256))); err != nil {
			t.Fatal(err)
		}
		form.WriteField("script", "第一句话。第二句话。第三句话。")
		form.WriteField("client_request_id", id)
		form.Close()
		req := httptest.NewRequest("POST", "/tasks", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := submit(requestID); w.Code != 409 {
		t.Fatalf("busy submission=%d %s", w.Code, w.Body.String())
	}
	hash := sha256.Sum256([]byte(owner.String() + ":" + requestID))
	a.db.Model(&models.WorkflowRun{}).Where("id = ?", state.ID).Update("input_hash", hex.EncodeToString(hash[:]))
	if w := submit(requestID); w.Code != 202 || !strings.Contains(w.Body.String(), state.ID.String()) {
		t.Fatalf("duplicate did not reuse original: %d %s", w.Code, w.Body.String())
	}
	a.db.Model(&models.WorkflowStepExecution{}).Where("workflow_run_id = ?", state.ID).Update("status", "failed")
	if w := submit(uuid.NewString()); w.Code != 409 || !strings.Contains(w.Body.String(), "基准") {
		t.Fatalf("baseline gate=%d %s", w.Code, w.Body.String())
	}
	var projects int64
	a.db.Model(&models.Project{}).Count(&projects)
	if projects != 1 {
		t.Fatalf("rejected/duplicate request created projects: %d", projects)
	}
}

func TestAvatarRoutesRequireLogin(t *testing.T) {
	a := newTestApp(t)
	for _, path := range []string{"/api/v1/avatar-mvp/config", "/api/v1/avatar-mvp/tasks/" + uuid.NewString(), "/api/v1/avatar-mvp/tasks/" + uuid.NewString() + "/video"} {
		w := httptest.NewRecorder()
		a.Router().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatalf("unauthenticated route %s returned %d", path, w.Code)
		}
	}
}

func TestDeletedAvatarProjectReleasesBaseline(t *testing.T) {
	a := newTestApp(t)
	owner := uuid.New()
	state := seedAvatarTask(t, a, owner, "failed")
	a.avatarMVP.options.DataRoot = t.TempDir()
	dir := a.avatarMVP.taskDir(state.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "photo"), []byte("photo"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.db.Delete(&models.Project{}, "id = ?", state.ProjectID).Error; err != nil {
		t.Fatal(err)
	}
	a.purgeAvatarProjectData(state.ProjectID)
	if err := a.avatarMVP.checkBaselineRequest(a.db, uuid.NewString()); err != nil {
		t.Fatalf("deleted project still blocks a new task: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("task directory remains: %v", err)
	}
}

func TestAvatarOneRequestBaselineAuthorization(t *testing.T) {
	a := newTestApp(t)
	s := a.avatarMVP
	owner := uuid.New()
	prior := seedAvatarTask(t, a, owner, "failed")
	requestID := uuid.NewString()
	if err := s.checkBaselineRequest(a.db, requestID); err == nil {
		t.Fatal("existing baseline must require explicit authorization")
	}
	s.options.BaselineRequestID = requestID
	if err := s.checkBaselineRequest(a.db, uuid.NewString()); err == nil {
		t.Fatal("authorization must not open other requests")
	}
	if err := s.checkBaselineRequest(a.db, requestID); err != nil {
		t.Fatalf("authorized request rejected: %v", err)
	}
	prior.BaselineRequestID = requestID
	if err := s.save(prior); err != nil {
		t.Fatal(err)
	}
	if err := s.checkBaselineRequest(a.db, requestID); err == nil {
		t.Fatal("consumed authorization must not create a second task or cross-user task")
	}
	s.options.Model = "another-model"
	if err := s.checkBaselineRequest(a.db, requestID); err == nil {
		t.Fatal("changing models must not reuse a consumed authorization")
	}
	var count int64
	a.db.Model(&models.WorkflowStepExecution{}).Where("step_id = ?", avatarMarker).Count(&count)
	if count != 1 {
		t.Fatal("baseline authorization must preserve prior records")
	}
}
