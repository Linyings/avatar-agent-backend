package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const avatarMarker = "avatar.task"
const avatarOwner = "avatar-server"

type avatarAttempt struct {
	TaskID      string          `json:"task_id"`
	Status      string          `json:"status"`
	SubmittedAt time.Time       `json:"submitted_at"`
	Usage       json.RawMessage `json:"usage,omitempty"`
}
type avatarShot struct {
	avatar.Shot
	Status         string          `json:"status"`
	Submission     string          `json:"submission"`
	ProviderTaskID string          `json:"provider_task_id,omitempty"`
	Attempts       []avatarAttempt `json:"attempts"`
	Clip           avatar.Clip     `json:"clip"`
	Error          string          `json:"error,omitempty"`
	StartedAt      *time.Time      `json:"started_at,omitempty"`
	CompletedAt    *time.Time      `json:"completed_at,omitempty"`
}
type avatarTask struct {
	ID                uuid.UUID          `json:"id"`
	ProjectID         uuid.UUID          `json:"project_id"`
	Status            string             `json:"status"`
	Stage             string             `json:"stage"`
	Title             string             `json:"title"`
	Script            string             `json:"script"`
	Shots             []avatarShot       `json:"shots"`
	Baseline          bool               `json:"baseline"`
	BaselineRequestID string             `json:"baseline_request_id,omitempty"`
	ReviewRequired    bool               `json:"review_required"`
	Provider          string             `json:"provider"`
	Model             string             `json:"model"`
	VideoConfigID     string             `json:"video_config_id,omitempty"`
	Voice             string             `json:"voice"`
	Duration          float64            `json:"duration"`
	Error             string             `json:"error,omitempty"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
	CompletedAt       *time.Time         `json:"completed_at,omitempty"`
	StageSeconds      map[string]float64 `json:"stage_seconds"`
	TTSCharacters     int64              `json:"tts_characters"`
	LLMUsage          any                `json:"llm_usage,omitempty"`
	Operations        []avatar.Operation `json:"operations,omitempty"`
}
type avatarMediaTools interface {
	Check(context.Context) error
	Prepare(context.Context, string) ([]avatar.Clip, error)
	Annotate(context.Context, string) error
	Verify(context.Context, string, string) (avatar.Clip, error)
	Shotbook(context.Context, string) error
	Render(context.Context, string) error
}
type avatarProvider = avatar.Provider
type avatarMVPService struct {
	app         *App
	options     avatar.Options
	provider    avatarProvider
	providerErr error
	tools       avatarMediaTools
	mu          sync.Mutex
	speech      func(context.Context, string) ([]byte, int64, error)
	plan        func(context.Context, []string) ([]avatar.Shot, any, error)
}

func newAvatarMVP(a *App) *avatarMVPService {
	options := a.cfg.Avatar.VideoDefaults()
	if options.DataRoot == "" {
		options.DataRoot = "data/avatar-mvp"
	}
	if options.RuntimeRoot == "" {
		options.RuntimeRoot = "avatar-runtime"
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 5 * time.Second
	}
	if options.JobTimeout <= 0 {
		options.JobTimeout = time.Hour
	}
	options = options.ResolvePaths()
	provider, providerErr := avatar.NewVideoProvider(options)
	s := &avatarMVPService{app: a, options: options, provider: provider, providerErr: providerErr, tools: avatar.Tools{Options: options}}
	s.speech = func(ctx context.Context, text string) ([]byte, int64, error) {
		if a.cfg.TTSModel == "doubao-seed-tts-2.0" || strings.HasPrefix(a.cfg.TTSModel, "minimax/") {
			client := avatar.NewRunningHub(s.options.BaseURL, s.options.APIKey)
			return client.Synthesize(ctx, avatar.SpeechRequest{Model: a.cfg.TTSModel, Speaker: sVoice(a), Language: a.cfg.TTSLanguage, Format: a.cfg.TTSFormat, Text: text})
		}
		return a.dashscope.DubbingTextToSpeech(ctx, text, "qwen3-tts-instruct-flash:Cherry", "", 1)
	}
	s.plan = func(ctx context.Context, sentences []string) ([]avatar.Shot, any, error) {
		shots, usage, err := a.openai.AvatarStoryboard(ctx, sentences)
		return shots, usage, err
	}
	return s
}
func (a *App) purgeAvatarProjectData(projectID uuid.UUID) {
	if a == nil || a.avatarMVP == nil || a.db == nil || projectID == uuid.Nil {
		return
	}
	var runIDs []uuid.UUID
	if err := a.db.Model(&models.WorkflowRun{}).Where("project_id = ?", projectID).Pluck("id", &runIDs).Error; err != nil || len(runIDs) == 0 {
		return
	}
	for _, id := range runIDs {
		_ = os.RemoveAll(a.avatarMVP.taskDir(id))
	}
	_ = a.db.Where("workflow_run_id IN ?", runIDs).Delete(&models.WorkflowStepExecution{}).Error
	_ = a.db.Where("id IN ?", runIDs).Delete(&models.WorkflowRun{}).Error
}

func (s *avatarMVPService) taskDir(id uuid.UUID) string {
	root, _ := filepath.Abs(s.options.DataRoot)
	return filepath.Join(root, id.String())
}
func sVoice(a *App) string {
	voice := strings.TrimSpace(a.cfg.TTSSpeaker)
	if a.cfg.TTSModel != "" && voice != "" && !avatar.SpeechVoiceIncompatible(a.cfg.TTSModel, voice) {
		return voice
	}
	return avatar.DefaultSpeechVoice(a.cfg.TTSModel)
}
func sTTSModel(a *App) string {
	if strings.TrimSpace(a.cfg.TTSModel) != "" {
		return a.cfg.TTSModel
	}
	return a.cfg.QwenTTSModel
}
func (s *avatarMVPService) configured() error {
	if !s.options.Enabled {
		return fmt.Errorf("数字人口播 MVP 尚未启用（AVATAR_MVP_ENABLED）")
	}
	if err := s.options.ValidateVideo(); err != nil {
		return err
	}
	if s.providerErr != nil {
		return s.providerErr
	}
	if s.app.cfg.TTSModel == "doubao-seed-tts-2.0" || strings.HasPrefix(s.app.cfg.TTSModel, "minimax/") {
		if s.options.APIKey == "" || strings.TrimSpace(sVoice(s.app)) == "" {
			return fmt.Errorf("固定音色配音未配置（TTS_MODEL / TTS_SPEAKER）")
		}
	} else if s.app.cfg.DashScopeAPIKey == "" || s.app.cfg.QwenTTSModel == "" {
		return fmt.Errorf("固定音色配音未配置（DASHSCOPE_API_KEY / QWEN_TTS_MODEL）")
	}
	if s.options.BaselineApproved && (s.app.cfg.OpenAIFlashAPIKey == "" || s.app.cfg.OpenAIFlashModel == "" || s.app.cfg.OpenAIFlashAPIBaseURL == "") {
		return fmt.Errorf("自动分镜 Flash LLM 未配置")
	}
	return nil
}
func (a *App) avatarConfig(c *gin.Context) {
	err := a.avatarMVP.configured()
	message := ""
	if err != nil {
		message = err.Error()
	}
	options := a.avatarMVP.options
	ok(c, gin.H{"enabled": a.cfg.Avatar.Enabled, "configured": err == nil, "message": message, "baseline_approved": a.cfg.Avatar.BaselineApproved, "voice": sVoice(a), "tts_model": sTTSModel(a), "provider": options.Provider, "model": options.Model, "protocol": options.Protocol, "resolution": options.Resolution, "angles": avatar.Angles})
}
func (a *App) avatarCreate(c *gin.Context) {
	s := a.avatarMVP
	if err := s.configured(); err != nil {
		fail(c, 503, err.Error())
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 9<<20)
	if err := c.Request.ParseMultipartForm(9 << 20); err != nil {
		fail(c, 400, "请上传不超过 8MB 的 JPG/PNG 图片及口播稿")
		return
	}
	defer c.Request.MultipartForm.RemoveAll()
	script := strings.TrimSpace(c.PostForm("script"))
	title := strings.TrimSpace(c.PostForm("title"))
	sentences := avatar.SplitScript(script)
	if len(sentences) < 3 || utf8.RuneCountInString(script) > 240 || utf8.RuneCountInString(title) > 40 {
		fail(c, 400, "稿件需至少三句且不超过240字，标题不超过40字；成片配音最长30秒")
		return
	}
	requestID := c.PostForm("client_request_id")
	if requestID == "" {
		requestID = uuid.NewString()
	}
	if _, err := uuid.Parse(requestID); err != nil {
		fail(c, 400, "client_request_id 必须为 UUID")
		return
	}
	file, _, err := c.Request.FormFile("photo")
	if err != nil {
		fail(c, 400, "缺少人物照片")
		return
	}
	defer file.Close()
	photo, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	if err != nil || len(photo) > 8<<20 {
		fail(c, 400, "照片读取失败或超过8MB")
		return
	}
	imageInfo, format, err := image.DecodeConfig(bytes.NewReader(photo))
	if err != nil || (format != "jpeg" && format != "png") || imageInfo.Width < 256 || imageInfo.Height < 256 || imageInfo.Width > 4096 || imageInfo.Height > 4096 {
		fail(c, 400, "照片需为 JPG/PNG，宽高在256至4096像素之间")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	if err = s.tools.Check(ctx); err != nil {
		fail(c, 503, err.Error())
		return
	}
	requestedProjectID := uuid.Nil
	if rawProjectID := strings.TrimSpace(c.PostForm("project_id")); rawProjectID != "" {
		parsedProjectID, parseErr := uuid.Parse(rawProjectID)
		if parseErr != nil {
			fail(c, 400, "project_id 必须为 UUID")
			return
		}
		requestedProjectID = parsedProjectID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hash := sha256.Sum256([]byte(getUser(c).ID.String() + ":" + requestID))
	inputHash := hex.EncodeToString(hash[:])
	var state avatarTask
	reused := false
	err = a.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := avatarTransactionLock(tx); err != nil {
			return err
		}
		var prior models.WorkflowRun
		if tx.Where("user_id = ? AND input_hash = ?", getUser(c).ID, inputHash).Take(&prior).Error == nil {
			var marker models.WorkflowStepExecution
			if err := tx.Where("workflow_run_id = ? AND step_id = ?", prior.ID, avatarMarker).Take(&marker).Error; err != nil {
				return err
			}
			reused = true
			return json.Unmarshal(marker.Result, &state)
		}
		var project models.Project
		if requestedProjectID != uuid.Nil {
			if err := tx.Where("id = ? AND owner_id = ?", requestedProjectID, getUser(c).ID).Take(&project).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errAvatarProjectMissing
				}
				return err
			}
			if !isAvatarProjectMetadata(project.ProjectMetadata) {
				return errAvatarProjectKind
			}
			var existing models.WorkflowStepExecution
			findErr := tx.Table("workflow_step_executions AS s").Select("s.*").Joins("JOIN workflow_runs AS r ON r.id = s.workflow_run_id").Where("r.project_id = ? AND s.step_id = ?", project.ID, avatarMarker).Order("s.updated_at DESC").Take(&existing).Error
			if findErr == nil {
				reused = true
				return json.Unmarshal(existing.Result, &state)
			}
			if !errors.Is(findErr, gorm.ErrRecordNotFound) {
				return findErr
			}
		}
		if err := s.ensureIdle(tx); err != nil {
			return err
		}
		if !s.options.BaselineApproved {
			if err := s.checkBaselineRequest(tx, requestID); err != nil {
				return err
			}
		}
		photoHash := sha256.Sum256(photo)
		configuration := map[string]any{"provider": s.options.Provider, "protocol": s.options.Protocol, "model": s.options.Model, "endpoint": s.options.GenerationPath, "provider_base_url": s.options.BaseURL, "video_config_id": s.options.VideoConfigID(), "voice": sVoice(a), "tts_model": sTTSModel(a), "flash_model": a.cfg.OpenAIFlashModel, "asr_backend": s.options.ASRBackend, "baseline_approved": s.options.BaselineApproved, "ratio": "9:16", "resolution": s.options.Resolution, "angles": avatar.Angles, "framings": avatar.Framings, "cards": avatar.Cards}
		if requestedProjectID == uuid.Nil {
			project = models.Project{Name: "数字人口播", OwnerID: getUser(c).ID, Status: "draft", ProjectMetadata: jsonMap(map[string]any{"kind": "avatar_mvp", "inference_mode": "avatar", "script": script, "title": title, "client_request_id": requestID, "provider": s.options.Provider, "model": s.options.Model, "voice": sVoice(a), "avatar_mvp": map[string]any{"configuration": configuration, "photo_sha256": hex.EncodeToString(photoHash[:])}})}
			if title != "" {
				project.Name = title
			}
			if err := tx.Create(&project).Error; err != nil {
				return err
			}
		} else if title != "" && project.Name != title {
			if err := tx.Model(&models.Project{}).Where("id = ?", project.ID).Update("name", title).Error; err != nil {
				return err
			}
		}
		service := NewWorkflowRunStateService(tx, WorkflowRunStateHooks{})
		run, err := service.Create(c.Request.Context(), CreateWorkflowRunInput{UserID: getUser(c).ID, ProjectID: &project.ID, ClientType: "web", InputHash: inputHash, ClientInstanceID: avatarOwner})
		if err != nil {
			return err
		}
		state = avatarTask{ID: run.Run.ID, ProjectID: project.ID, Status: "queued", Stage: "planning", Title: title, Script: script, Baseline: !s.options.BaselineApproved, ReviewRequired: true, Provider: s.options.Provider, Model: s.options.Model, VideoConfigID: s.options.VideoConfigID(), Voice: sVoice(a), CreatedAt: time.Now().UTC(), StageSeconds: map[string]float64{}}
		if state.Baseline && requestID == s.options.BaselineRequestID {
			state.BaselineRequestID = requestID
		}
		for _, shot := range avatar.FixedStoryboard(sentences) {
			state.Shots = append(state.Shots, avatarShot{Shot: shot, Status: "pending", Submission: "none", Attempts: []avatarAttempt{}})
		}
		marker, err := service.UpsertStep(c.Request.Context(), getUser(c).ID, state.ID, WorkflowStepUpdateInput{StepID: avatarMarker, InputHash: inputHash, Status: "running", ClientInstanceID: avatarOwner})
		if err != nil {
			return err
		}
		if err = tx.Model(&marker).Update("result", jsonMap(state)).Error; err != nil {
			return err
		}
		dir := s.taskDir(state.ID)
		if err = os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "photo"), photo, 0600)
	})
	if err != nil {
		switch {
		case errors.Is(err, errAvatarProjectMissing):
			fail(c, 404, err.Error())
		case errors.Is(err, errAvatarProjectKind):
			fail(c, 400, err.Error())
		default:
			fail(c, 409, avatarPublicError(err))
		}
		return
	}
	if !reused {
		go s.execute(state, getUser(c).ID)
	}
	c.JSON(http.StatusAccepted, state)
}

var errAvatarProjectMissing = errors.New("项目不存在")
var errAvatarProjectKind = errors.New("该项目不是数字人口播项目")

func isAvatarProjectMetadata(raw datatypes.JSON) bool {
	var meta map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &meta) != nil {
		return false
	}
	return meta["inference_mode"] == "avatar" || meta["kind"] == "avatar_mvp"
}

func avatarTransactionLock(tx *gorm.DB) error {
	if tx.Dialector.Name() == "postgres" {
		return tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(744834291)).Error
	}
	return nil
}

// Called under the global transaction lock after normal idempotency and busy
// checks. One server-authorized request may create one additional baseline;
// the persisted marker consumes the grant even after failure or a restart.
func (s *avatarMVPService) checkBaselineRequest(tx *gorm.DB, requestID string) error {
	authorized := requestID != "" && requestID == s.options.BaselineRequestID
	var previous []models.WorkflowStepExecution
	err := tx.Table("workflow_step_executions AS s").Select("s.*").Joins("JOIN workflow_runs AS r ON r.id = s.workflow_run_id").Joins("JOIN projects AS p ON p.id = r.project_id AND p.deleted_at IS NULL").Where("s.step_id = ?", avatarMarker).Find(&previous).Error
	if err != nil {
		return err
	}
	blocked := false
	for _, marker := range previous {
		var prior avatarTask
		if err := json.Unmarshal(marker.Result, &prior); err != nil {
			return err
		}
		if authorized && prior.BaselineRequestID == requestID {
			return fmt.Errorf("本次基准测试授权已使用，请查询原任务；不得重复创建")
		}
		blocked = blocked || s.sameVideoConfig(prior)
	}
	if blocked && !authorized {
		return fmt.Errorf("当前视频模型的首条基准任务已创建；请打开原来的数字人口播项目继续测试，人工验收通过后再启用 AVATAR_MVP_BASELINE_APPROVED")
	}
	return nil
}
func (s *avatarMVPService) ensureIdle(tx *gorm.DB) error {
	var count int64
	if err := tx.Model(&models.WorkflowStepExecution{}).Where("step_id IN ? AND status = ?", []string{avatarMarker, workflowMarker}, "running").Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("已有制作任务正在执行，请稍后再试")
	}
	return nil
}
func (s *avatarMVPService) load(ctx context.Context, userID, id uuid.UUID) (avatarTask, error) {
	var row models.WorkflowStepExecution
	err := s.app.db.WithContext(ctx).Table("workflow_step_executions AS s").Select("s.*").Joins("JOIN workflow_runs AS r ON r.id = s.workflow_run_id").Joins("JOIN projects AS p ON p.id = r.project_id AND p.deleted_at IS NULL").Where("s.workflow_run_id = ? AND s.step_id = ? AND r.user_id = ?", id, avatarMarker, userID).Take(&row).Error
	if err != nil {
		return avatarTask{}, err
	}
	var state avatarTask
	err = json.Unmarshal(row.Result, &state)
	if err == nil {
		state.Operations, err = avatar.ReadOperations(filepath.Join(s.taskDir(id), "operations.jsonl"))
	}
	return state, err
}
func (s *avatarMVPService) loadLatestForProject(ctx context.Context, userID, projectID uuid.UUID) (avatarTask, error) {
	var row models.WorkflowStepExecution
	err := s.app.db.WithContext(ctx).Table("workflow_step_executions AS s").Select("s.*").Joins("JOIN workflow_runs AS r ON r.id = s.workflow_run_id").Joins("JOIN projects AS p ON p.id = r.project_id AND p.deleted_at IS NULL").Where("r.project_id = ? AND s.step_id = ? AND r.user_id = ? AND p.owner_id = ?", projectID, avatarMarker, userID, userID).Order("s.updated_at DESC").Take(&row).Error
	if err != nil {
		return avatarTask{}, err
	}
	var state avatarTask
	err = json.Unmarshal(row.Result, &state)
	if err == nil {
		state.Operations, err = avatar.ReadOperations(filepath.Join(s.taskDir(state.ID), "operations.jsonl"))
	}
	return state, err
}
func (a *App) avatarLatestForProject(c *gin.Context) {
	projectID, valid := parseIDParam(c, "projectId")
	if !valid {
		return
	}
	state, err := a.avatarMVP.loadLatestForProject(c.Request.Context(), getUser(c).ID, projectID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, 404, "任务不存在")
			return
		}
		fail(c, 500, "查询项目任务失败")
		return
	}
	ok(c, state)
}
func (a *App) avatarGet(c *gin.Context) {
	id, valid := parseIDParam(c, "id")
	if !valid {
		return
	}
	state, err := a.avatarMVP.load(c.Request.Context(), getUser(c).ID, id)
	if err != nil {
		fail(c, 404, "任务不存在")
		return
	}
	ok(c, state)
}
func (a *App) avatarRetry(c *gin.Context) {
	id, valid := parseIDParam(c, "id")
	if !valid {
		return
	}
	// Check ownership before checking deployment configuration.
	if _, err := a.avatarMVP.load(c.Request.Context(), getUser(c).ID, id); err != nil {
		fail(c, 404, "任务不存在")
		return
	}
	if err := a.avatarMVP.configured(); err != nil {
		fail(c, 503, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	if err := a.avatarMVP.tools.Check(ctx); err != nil {
		fail(c, 503, err.Error())
		return
	}
	state, err := a.avatarMVP.claimRetry(c.Request.Context(), getUser(c).ID, id)
	if err != nil {
		fail(c, 409, avatarPublicError(err))
		return
	}
	go a.avatarMVP.execute(state, getUser(c).ID)
	c.JSON(http.StatusAccepted, state)
}
func (s *avatarMVPService) claimRetry(ctx context.Context, userID, id uuid.UUID) (avatarTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load(ctx, userID, id)
	if err != nil {
		return state, err
	}
	err = s.app.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := avatarTransactionLock(tx); err != nil {
			return err
		}
		var marker models.WorkflowStepExecution
		if err := tx.Where("workflow_run_id = ? AND step_id = ?", id, avatarMarker).Take(&marker).Error; err != nil {
			return err
		}
		if err := json.Unmarshal(marker.Result, &state); err != nil {
			return err
		}
		if state.Status != "failed" && state.Status != "interrupted" {
			return fmt.Errorf("当前任务状态不允许重试")
		}
		if !s.sameVideoConfig(state) {
			return fmt.Errorf("视频供应商或模型配置已切换，请恢复该任务原配置后查单和重试；不会向其他供应商重发订单")
		}
		for _, shot := range state.Shots {
			if (shot.Submission == "submitting" || shot.Submission == "unknown") && shot.ProviderTaskID == "" {
				return fmt.Errorf("镜头提交状态待确认，禁止重复付费提交，请先在原视频供应商核对任务")
			}
		}
		if err := s.ensureIdle(tx); err != nil {
			return err
		}
		service := NewWorkflowRunStateService(tx, WorkflowRunStateHooks{})
		if _, err := service.Resume(ctx, userID, id, avatarOwner); err != nil {
			return err
		}
		expires := time.Now().UTC().Add(workflowRunLeaseDuration)
		state.Status = "queued"
		state.Error = ""
		state.UpdatedAt = time.Now().UTC()
		return tx.Model(&marker).Updates(map[string]any{"status": "running", "result": jsonMap(state), "lease_owner": avatarOwner, "lease_expires_at": expires}).Error
	})
	return state, err
}
func (s *avatarMVPService) sameVideoConfig(state avatarTask) bool {
	if state.VideoConfigID != "" {
		return state.VideoConfigID == s.options.VideoConfigID()
	}
	// Tasks predating provider selection used RunningHub exclusively.
	provider := state.Provider
	if provider == "" {
		provider = "runninghub"
	}
	model := state.Model
	if model == "" {
		model = "seedance-2.5-token"
	}
	return provider == s.options.Provider && model == s.options.Model
}

func (a *App) avatarVideo(c *gin.Context) {
	id, valid := parseIDParam(c, "id")
	if !valid {
		return
	}
	state, err := a.avatarMVP.load(c.Request.Context(), getUser(c).ID, id)
	if err != nil {
		fail(c, 404, "任务不存在")
		return
	}
	if state.Status != "succeeded" {
		fail(c, 409, "成片尚未完成")
		return
	}
	file, err := os.Open(filepath.Join(a.avatarMVP.taskDir(id), "output.mp4"))
	if err != nil {
		fail(c, 404, "成片文件不可用")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		fail(c, 500, "无法读取成片")
		return
	}
	c.Header("Content-Type", "video/mp4")
	c.Header("Content-Disposition", `inline; filename="avatar.mp4"`)
	c.Header("Cache-Control", "private, no-store")
	http.ServeContent(c.Writer, c.Request, "avatar.mp4", info.ModTime(), file)
}
func (s *avatarMVPService) save(state avatarTask) error {
	state.UpdatedAt = time.Now().UTC()
	var err error
	state.Operations, err = avatar.ReadOperations(filepath.Join(s.taskDir(state.ID), "operations.jsonl"))
	if err != nil {
		return err
	}
	result := s.app.db.Model(&models.WorkflowStepExecution{}).Where("workflow_run_id = ? AND step_id = ?", state.ID, avatarMarker).Update("result", jsonMap(state))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("任务状态未保存")
	}
	return nil
}
func (s *avatarMVPService) recoverInterrupted(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.app.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := avatarTransactionLock(tx); err != nil {
			return err
		}
		var rows []models.WorkflowStepExecution
		if err := tx.Where("step_id = ? AND status = ? AND (lease_expires_at IS NULL OR lease_expires_at < ?)", avatarMarker, "running", time.Now().UTC()).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			var state avatarTask
			if err := json.Unmarshal(row.Result, &state); err != nil {
				return err
			}
			state.Status = "interrupted"
			state.Error = "执行器已中断；请手动重试，系统将优先查询已有模型任务"
			state.UpdatedAt = time.Now().UTC()
			if err := tx.Model(&row).Updates(map[string]any{"status": "interrupted", "result": jsonMap(state), "lease_owner": nil, "lease_expires_at": nil}).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.WorkflowRun{}).Where("id = ? AND status = ?", state.ID, "running").Updates(map[string]any{"status": "interrupted", "lease_expires_at": nil}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// StartAvatarMVP must be called once by the single-instance MVP host.
func (a *App) StartAvatarMVP(ctx context.Context) {
	if !a.cfg.Avatar.Enabled {
		return
	}
	if err := a.avatarWorkflow.recover(ctx); err != nil {
		log.Print("workflow recovery failed")
	}
	if err := a.avatarMVP.recoverInterrupted(ctx); err != nil {
		log.Printf("avatar recovery failed")
	}
	a.modelLab.resume()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = a.avatarWorkflow.recover(ctx)
				if err := a.avatarMVP.recoverInterrupted(ctx); err != nil {
					log.Print("avatar recovery failed")
				}
			}
		}
	}()
}
func avatarPublicError(err error) string {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "任务不存在"
	}
	// All external and command errors are converted to bounded, secret-free messages by their adapters.
	message := err.Error()
	if len(message) > 500 {
		return "制作任务失败，请检查本地运行日志"
	}
	return message
}
func (s *avatarMVPService) execute(state avatarTask, userID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), s.options.JobTimeout)
	defer cancel()
	ctx = avatar.WithTelemetry(ctx, filepath.Join(s.taskDir(state.ID), "operations.jsonl"))
	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				step := avatarMarker
				if _, err := s.app.workflowRuns.Heartbeat(ctx, userID, state.ID, WorkflowRunHeartbeatInput{ClientInstanceID: avatarOwner, CurrentStepID: &step}); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	state.Status = "running"
	state.Error = ""
	if state.StageSeconds == nil {
		state.StageSeconds = map[string]float64{}
	}
	if err := s.save(state); err != nil {
		return
	}
	err := s.produce(ctx, &state, userID)
	if err != nil {
		state.Status = "failed"
		state.Error = avatarPublicError(err)
		for i := range state.Shots {
			if state.Shots[i].Status == "running" {
				state.Shots[i].Status = "failed"
				state.Shots[i].Error = state.Error
			}
		}
		_ = s.save(state)
		code, message := "avatar_failed", "operation failed"
		_, _ = s.app.workflowRuns.Interrupt(context.Background(), userID, state.ID, WorkflowRunInterruptInput{ClientInstanceID: avatarOwner, ErrorCode: &code, ErrorMessage: &message})
		s.app.db.Model(&models.WorkflowStepExecution{}).Where("workflow_run_id = ? AND status = ?", state.ID, "running").Updates(map[string]any{"status": "failed", "lease_owner": nil, "lease_expires_at": nil})
		_ = avatar.WriteJSON(filepath.Join(s.taskDir(state.ID), "report.json"), state)
		return
	}
	now := time.Now().UTC()
	state.Status = "succeeded"
	state.Stage = "completed"
	state.CompletedAt = &now
	// The workflow must finalize successfully before exposing a playable result.
	_, err = s.app.workflowRuns.UpsertStep(ctx, userID, state.ID, WorkflowStepUpdateInput{StepID: avatarMarker, InputHash: s.inputHash(state.ID), Status: "succeeded", ClientInstanceID: avatarOwner})
	if err == nil {
		_, err = s.app.workflowRuns.Complete(ctx, userID, state.ID, now)
	}
	if err != nil {
		state.Status = "failed"
		state.Error = "成片已渲染，但任务最终状态保存失败"
	}
	_ = s.save(state)
	_ = avatar.WriteJSON(filepath.Join(s.taskDir(state.ID), "report.json"), state)
	if state.Status == "succeeded" {
		s.app.db.Model(&models.Project{}).Where("id = ?", state.ProjectID).Update("status", "completed")
	}
}
func (s *avatarMVPService) inputHash(id uuid.UUID) string {
	var run models.WorkflowRun
	s.app.db.Select("input_hash").First(&run, "id = ?", id)
	return run.InputHash
}
func (s *avatarMVPService) stage(state *avatarTask, stage string) error {
	state.Stage = stage
	return s.save(*state)
}
func (s *avatarMVPService) produce(ctx context.Context, state *avatarTask, userID uuid.UUID) error {
	dir := s.taskDir(state.ID)
	if state.Baseline == false && state.Stage == "planning" {
		begin := time.Now()
		planCtx, finish := avatar.BeginOperation(ctx, "planning_llm", "")
		shots, usage, err := s.plan(planCtx, avatar.SplitScript(state.Script))
		finish(err)
		if err != nil {
			return fmt.Errorf("自动分镜失败：%s", avatarPublicError(err))
		}
		if err = avatar.ValidateStoryboard(shots, avatar.SplitScript(state.Script)); err != nil {
			return err
		}
		for i := range shots {
			state.Shots[i].Shot = shots[i]
		}
		state.LLMUsage = usage
		state.StageSeconds["planning"] += time.Since(begin).Seconds()
	} else if state.Stage == "planning" {
		_, finish := avatar.BeginOperation(ctx, "planning_fixed", "")
		finish(nil)
	}
	config := map[string]any{"title": state.Title, "script": state.Script, "shots": state.Shots}
	if err := avatar.WriteJSON(filepath.Join(dir, "request.json"), config); err != nil {
		return err
	}
	if err := s.stage(state, "tts"); err != nil {
		return err
	}
	voicePath := filepath.Join(dir, "voice.wav")
	if _, err := os.Stat(voicePath); os.IsNotExist(err) {
		begin := time.Now()
		ttsCtx, finish := avatar.BeginOperation(ctx, "tts", "")
		audio, usage, err := s.speech(ttsCtx, state.Script)
		finish(err)
		if err != nil {
			log.Printf("avatar tts failed: %s", err.Error())
			return fmt.Errorf("固定音色配音失败：%s", err.Error())
		}
		if err = os.WriteFile(voicePath+".part", audio, 0600); err != nil {
			return err
		}
		if err = os.Rename(voicePath+".part", voicePath); err != nil {
			return err
		}
		state.TTSCharacters = usage
		state.StageSeconds["tts"] += time.Since(begin).Seconds()
		if err = s.save(*state); err != nil {
			return err
		}
	}
	if err := s.stage(state, "alignment"); err != nil {
		return err
	}
	begin := time.Now()
	prepareCtx, finishPrepare := avatar.BeginOperation(ctx, "alignment", "")
	clips, err := s.tools.Prepare(prepareCtx, dir)
	finishPrepare(err)
	if err != nil {
		return err
	}
	if len(clips) != 3 {
		return fmt.Errorf("对齐未产生三个镜头")
	}
	for i := range clips {
		clips[i].RequestDuration, err = s.options.GenerationDuration(clips[i].RequestDuration)
		if err != nil {
			return err
		}
	}
	state.Duration = clips[2].End
	state.StageSeconds["alignment"] += time.Since(begin).Seconds()
	if err = s.stage(state, "semantics"); err != nil {
		return err
	}
	semanticsCtx, finishSemantics := avatar.BeginOperation(ctx, "semantics", "")
	err = s.tools.Annotate(semanticsCtx, dir)
	finishSemantics(err)
	if err != nil {
		return err
	}
	for i := range state.Shots {
		shot := &state.Shots[i]
		if shot.Status == "succeeded" {
			continue
		}
		shot.Clip = clips[i]
		shot.Status = "running"
		shot.Error = ""
		now := time.Now().UTC()
		shot.StartedAt = &now
		if err = s.stage(state, "generating"); err != nil {
			return err
		}
		stepID := avatarMarker + "." + shot.ID
		_, err = s.app.workflowRuns.UpsertStep(ctx, userID, state.ID, WorkflowStepUpdateInput{StepID: stepID, InputHash: s.inputHash(state.ID), Status: "running", ClientInstanceID: avatarOwner})
		if err != nil {
			return err
		}
		if shot.Submission == "submitting" || shot.Submission == "unknown" {
			return fmt.Errorf("镜头提交状态待确认，禁止重复生成")
		}
		if shot.Submission == "terminal_failed" {
			shot.ProviderTaskID = ""
			shot.Submission = "none"
		}
		if shot.Submission == "quality_failed" {
			// Only an explicit manual retry reaches this branch. Keep the rejected
			// source and checks for review before creating a fresh paid attempt.
			archive := filepath.Join(dir, shot.ID, fmt.Sprintf("attempt-%d", len(shot.Attempts)))
			if err = os.MkdirAll(archive, 0700); err != nil {
				return err
			}
			for _, name := range []string{"source.mp4", "generated.wav", "verified.json", "audio-check.json", "speech-check.json", "face-zone.json", "qa-first.jpg", "qa-middle.jpg", "qa-last.jpg"} {
				from := filepath.Join(dir, shot.ID, name)
				if _, exists := os.Stat(from); exists == nil {
					if err = os.Rename(from, filepath.Join(archive, name)); err != nil {
						return err
					}
				}
			}
			shot.ProviderTaskID = ""
			shot.Submission = "none"
		}
		if shot.ProviderTaskID == "" {
			// Uploads are not paid generation requests and may be repeated after URL expiry.
			imageCtx, finishImage := avatar.BeginOperation(ctx, "upload_image", shot.ID)
			imageURL, e := s.provider.Upload(imageCtx, filepath.Join(dir, "photo"))
			finishImage(e)
			if e != nil {
				return e
			}
			audioCtx, finishAudio := avatar.BeginOperation(ctx, "upload_audio", shot.ID)
			audioURL, e := s.provider.Upload(audioCtx, filepath.Join(dir, shot.ID, "audio.wav"))
			finishAudio(e)
			if e != nil {
				return e
			}
			shot.Submission = "submitting"
			if err = s.save(*state); err != nil {
				return err
			}
			submitCtx, finishSubmit := avatar.BeginOperation(ctx, "video_submit", shot.ID)
			prompt := avatar.GenerationPrompt(shot.Shot) + fmt.Sprintf(" 参考配音长度为%.3f秒。严格从第0秒按原音频时间轴说话，不拉长对白。视频多出的尾部保持闭嘴静默，不增加声音。", shot.Clip.Duration)
			response, e := s.provider.Submit(submitCtx, avatar.GenerationRequest{Model: s.options.Model, Prompt: prompt, ImageURLs: []string{imageURL}, AudioURLs: []string{audioURL}, Duration: strconv.Itoa(shot.Clip.RequestDuration), Resolution: s.options.Resolution, Ratio: "9:16", GenerateAudio: true, RealPersonMode: true, OutputFormat: "mp4"})
			finishSubmit(e)
			if e != nil {
				shot.Submission = "rejected"
				if avatar.SubmissionUncertain(e) {
					shot.Submission = "unknown"
				}
				if err = s.save(*state); err != nil {
					return err
				}
				return e
			}
			shot.ProviderTaskID = response.TaskID
			shot.Submission = "submitted"
			shot.Attempts = append(shot.Attempts, avatarAttempt{TaskID: response.TaskID, Status: response.Status, SubmittedAt: time.Now().UTC()})
			if err = s.save(*state); err != nil {
				return err
			}
			if err = s.app.db.Model(&models.WorkflowStepExecution{}).Where("workflow_run_id = ? AND step_id = ?", state.ID, stepID).Update("provider_request_id", shot.ProviderTaskID).Error; err != nil {
				return err
			}
		}
		waitCtx, finishWait := avatar.BeginOperation(ctx, "video_wait", shot.ID)
		result, e := s.wait(waitCtx, state, i)
		finishWait(e)
		if e != nil {
			return e
		}
		if len(shot.Attempts) > 0 {
			shot.Attempts[len(shot.Attempts)-1].Status = result.Status
			shot.Attempts[len(shot.Attempts)-1].Usage = result.Usage
		}
		if result.Status != "SUCCESS" {
			shot.Submission = "terminal_failed"
			if err = s.save(*state); err != nil {
				return err
			}
			return fmt.Errorf("镜头 %d 的视频生成失败；请检查原供应商任务记录后手动重试", i+1)
		}
		source := filepath.Join(dir, shot.ID, "source.mp4")
		if _, e = os.Stat(source); os.IsNotExist(e) {
			downloadCtx, finishDownload := avatar.BeginOperation(ctx, "download_video", shot.ID)
			e = s.provider.Download(downloadCtx, result.VideoURL(), source)
			finishDownload(e)
			if e != nil {
				return e
			}
		}
		if err = s.stage(state, "verifying"); err != nil {
			return err
		}
		verifyCtx, finishVerify := avatar.BeginOperation(ctx, "verification", shot.ID)
		verified, e := s.tools.Verify(verifyCtx, dir, shot.ID)
		finishVerify(e)
		if e != nil {
			if errors.Is(e, avatar.ErrQualityRejected) {
				shot.Submission = "quality_failed"
			}
			if err = s.save(*state); err != nil {
				return err
			}
			return fmt.Errorf("镜头 %d 素材检查失败；后续生成已停止，请检查 verify.log 与样片", i+1)
		}
		shot.Clip = verified
		shot.Status = "succeeded"
		completed := time.Now().UTC()
		shot.CompletedAt = &completed
		if err = s.save(*state); err != nil {
			return err
		}
		row, e := s.app.workflowRuns.UpsertStep(ctx, userID, state.ID, WorkflowStepUpdateInput{StepID: stepID, InputHash: s.inputHash(state.ID), Status: "succeeded", ClientInstanceID: avatarOwner})
		if e != nil {
			return e
		}
		if err = s.app.db.Model(&row).Update("result", jsonMap(shot)).Error; err != nil {
			return err
		}
	}
	if err = s.stage(state, "shotbook"); err != nil {
		return err
	}
	shotbookCtx, finishShotbook := avatar.BeginOperation(ctx, "shotbook", "")
	err = s.tools.Shotbook(shotbookCtx, dir)
	finishShotbook(err)
	if err != nil {
		return err
	}
	if err = s.stage(state, "rendering"); err != nil {
		return err
	}
	begin = time.Now()
	if err = avatar.WriteJSON(filepath.Join(dir, "task.json"), state); err != nil {
		return err
	}
	renderCtx, finishRender := avatar.BeginOperation(ctx, "rendering", "")
	err = s.tools.Render(renderCtx, dir)
	finishRender(err)
	if err != nil {
		return err
	}
	state.StageSeconds["rendering"] += time.Since(begin).Seconds()
	info, err := os.Stat(filepath.Join(dir, "output.mp4"))
	if err != nil || info.Size() == 0 {
		return fmt.Errorf("渲染未产出有效成片")
	}
	return nil
}
func (s *avatarMVPService) wait(ctx context.Context, state *avatarTask, index int) (avatar.ProviderResult, error) {
	shot := &state.Shots[index]
	begin := time.Now()
	defer func() { state.StageSeconds["generation"] += time.Since(begin).Seconds() }()
	failures := 0
	for {
		result, err := s.provider.Query(ctx, shot.ProviderTaskID)
		if err == nil {
			failures = 0
			switch result.Status {
			case "SUCCESS", "FAILED", "FAILURE", "CANCELED", "CANCELLED":
				return result, nil
			}
		} else {
			failures++
			if failures >= 6 {
				return result, fmt.Errorf("镜头查询暂不可用；任务 ID 已保存，重试将继续查单")
			}
		}
		timer := time.NewTimer(s.options.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, fmt.Errorf("生成等待已中断，任务 ID 已保存")
		case <-timer.C:
		}
	}
}
