package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/agent"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const workflowMarker = "avatar.workflow.v2"

var errWorkflowStopped = errors.New("用户已停止后续制作")

type avatarWorkflowService struct {
	app     *App
	content func(context.Context, string, any, []string) (json.RawMessage, error)
	media   func(context.Context, string, string) (json.RawMessage, error)
	// stateMu guards the in-memory WorkflowState when material production runs
	// concurrently across segments. Steps, Materials and Artifacts are appended
	// from several goroutines; without it entries are lost, duplicated or point
	// at another need's file. Zero value is ready to use.
	stateMu sync.Mutex
	// mediaLocks serialises media stages per run. Every stage shares one set of
	// control files (workflow.json, media-input.json, media-result.json) inside
	// the run directory, so two concurrent stages read each other's input and
	// publish each other's result. Keys are run IDs so unrelated projects still
	// render in parallel.
	mediaLocks keyedMutex
}

// keyedMutex hands out one mutex per key and drops it once nobody waits on it.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

// lock blocks until the key is free and returns the matching unlock.
func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*keyedLock{}
	}
	entry, ok := k.locks[key]
	if !ok {
		entry = &keyedLock{}
		k.locks[key] = entry
	}
	entry.refs++
	k.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		k.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}

// withState runs fn while holding the state lock. Every mutation of the shared
// WorkflowState during concurrent material production must go through it.
func (w *avatarWorkflowService) withState(fn func()) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	fn()
}

func newAvatarWorkflow(a *App) *avatarWorkflowService {
	w := &avatarWorkflowService{app: a}
	w.content = a.openai.AvatarContent
	w.media = w.runMedia
	return w
}
func (w *avatarWorkflowService) root() string {
	return filepath.Join(w.app.avatarMVP.options.DataRoot, "workflow-v2")
}
func (w *avatarWorkflowService) projectDir(id string) string {
	return filepath.Join(w.root(), "projects", id)
}
func (w *avatarWorkflowService) dir(id string) string { return filepath.Join(w.root(), "runs", id) }
func workflowInput(p models.Project) (avatar.WorkflowInput, error) {
	var meta struct {
		Input *avatar.WorkflowInput `json:"avatar_v2"`
	}
	if err := json.Unmarshal(p.ProjectMetadata, &meta); err != nil || meta.Input == nil {
		return avatar.WorkflowInput{}, gorm.ErrRecordNotFound
	}
	return *meta.Input, nil
}
func (w *avatarWorkflowService) project(ctx context.Context, owner, id uuid.UUID) (models.Project, avatar.WorkflowInput, error) {
	var p models.Project
	err := w.app.db.WithContext(ctx).Where("id = ? AND owner_id = ?", id, owner).First(&p).Error
	if err != nil {
		return p, avatar.WorkflowInput{}, err
	}
	in, err := workflowInput(p)
	return p, in, err
}
func (w *avatarWorkflowService) load(ctx context.Context, owner uuid.UUID, id string) (avatar.WorkflowState, error) {
	var row models.WorkflowStepExecution
	err := w.app.db.WithContext(ctx).Table("workflow_step_executions AS s").Select("s.*").Joins("JOIN workflow_runs AS r ON r.id=s.workflow_run_id").Joins("JOIN projects AS p ON p.id=r.project_id AND p.deleted_at IS NULL").Where("s.workflow_run_id=? AND s.step_id=? AND r.user_id=? AND p.owner_id=?", id, workflowMarker, owner, owner).Take(&row).Error
	var s avatar.WorkflowState
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(row.Result, &s)
	return s, err
}

// All read/modify/write transitions share the same lock as legacy task admission.
func (w *avatarWorkflowService) update(id string, fn func(*avatar.WorkflowState) error) error {
	return w.updateContext(context.Background(), id, fn)
}

func (w *avatarWorkflowService) updateContext(ctx context.Context, id string, fn func(*avatar.WorkflowState) error) error {
	return w.updateTransaction(ctx, id, func(_ *gorm.DB, state *avatar.WorkflowState) error { return fn(state) })
}

func (w *avatarWorkflowService) updateTransaction(ctx context.Context, id string, fn func(*gorm.DB, *avatar.WorkflowState) error) error {
	w.app.avatarMVP.mu.Lock()
	defer w.app.avatarMVP.mu.Unlock()
	var committed avatar.WorkflowState
	err := w.app.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := avatarTransactionLock(tx); err != nil {
			return err
		}
		var row models.WorkflowStepExecution
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("workflow_run_id=? AND step_id=?", id, workflowMarker).First(&row).Error; err != nil {
			return err
		}
		var s avatar.WorkflowState
		if err := json.Unmarshal(row.Result, &s); err != nil {
			return err
		}
		if err := fn(tx, &s); err != nil {
			return err
		}
		s.UpdatedAt = time.Now().UTC()
		row.Status = s.Status
		if s.Status == "stopping" {
			row.Status = "running"
		}
		row.Result = jsonMap(s)
		if s.Status == "running" {
			expiry := time.Now().UTC().Add(workflowRunLeaseDuration)
			row.LeaseExpiresAt = &expiry
			row.LeaseOwner = strPtr(avatarOwner)
		}
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		committed = s
		return tx.Model(&models.WorkflowRun{}).Where("id=?", id).Updates(map[string]any{"status": s.Status, "last_active_at": s.UpdatedAt, "completed_at": s.CompletedAt}).Error
	})
	if err == nil {
		agent.PublishWorkflowState(committed.ProjectID, committed.ID, committed.Stage, committed.Status)
	}
	return err
}
func (w *avatarWorkflowService) save(s *avatar.WorkflowState) error {
	for i := range s.Steps {
		s.Steps[i].PreserveOrder()
	}
	s.UpdatedAt = time.Now().UTC()
	s.RefreshBilling()
	err := w.update(s.ID, func(current *avatar.WorkflowState) error {
		if current.PauseRequested {
			s.PauseRequested = true
		}
		if current.TargetNode != "" {
			s.TargetNode = current.TargetNode
		}
		if current.StopRequested {
			s.StopRequested = true
			s.StoppedAt = current.StoppedAt
			if s.Status == "running" {
				s.Status = "stopping"
			}
		}
		for i := range s.Events {
			for _, old := range current.Events {
				if old.ID == s.Events[i].ID {
					s.Events[i].Read = old.Read
				}
			}
		}
		*current = *s
		return nil
	})
	if err != nil {
		return err
	}
	for _, step := range s.Steps {
		if err = w.recordStep(s, step); err != nil {
			return err
		}
	}
	return nil
}
func (w *avatarWorkflowService) create(ctx context.Context, owner, projectID uuid.UUID, request string, revision int, editOptions ...avatar.WorkflowEdits) (avatar.WorkflowState, error) {
	return w.createTo(ctx, owner, projectID, request, revision, "", editOptions...)
}

// TargetNode is admitted in the same transaction as the run, including edits.
// A failed write or process restart must not expand a requested partial run.
func (w *avatarWorkflowService) createTo(ctx context.Context, owner, projectID uuid.UUID, request string, revision int, target string, editOptions ...avatar.WorkflowEdits) (avatar.WorkflowState, error) {
	return w.createToChecked(ctx, owner, projectID, request, revision, target, nil, editOptions...)
}

// Proposal checks must observe the same transaction as run admission. The HTTP
// preflight alone cannot protect against edits through other workflow endpoints.
func (w *avatarWorkflowService) createToChecked(ctx context.Context, owner, projectID uuid.UUID, request string, revision int, target string, proposal *packageProposal, editOptions ...avatar.WorkflowEdits) (avatar.WorkflowState, error) {
	var s avatar.WorkflowState
	if target != "" && !isKnownWorkflowNode(target) {
		return s, fmt.Errorf("制作节点不存在")
	}
	if len(request) < 4 || len(request) > 100 {
		return s, fmt.Errorf("需要有效的幂等请求ID")
	}
	w.app.avatarMVP.mu.Lock()
	defer w.app.avatarMVP.mu.Unlock()
	err := w.app.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := avatarTransactionLock(tx); err != nil {
			return err
		}
		var p models.Project
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND owner_id=?", projectID, owner).First(&p).Error; err != nil {
			return err
		}
		in, err := workflowInput(p)
		if err != nil {
			return err
		}
		hash := avatar.Digest([]any{owner.String(), projectID.String(), request})
		var prior models.WorkflowRun
		if err := tx.Where("user_id=? AND project_id=? AND input_hash=?", owner, projectID, hash).First(&prior).Error; err == nil {
			var marker models.WorkflowStepExecution
			if err := tx.Where("workflow_run_id=? AND step_id=?", prior.ID, workflowMarker).First(&marker).Error; err != nil {
				return err
			}
			return json.Unmarshal(marker.Result, &s)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if revision != in.Revision {
			return fmt.Errorf("输入版本已更新，请刷新后再运行")
		}
		if proposal != nil {
			stored := proposalOf(p)
			if stored == nil || stored.ID != proposal.ID || stored.Applied || stored.Revision != in.Revision {
				return fmt.Errorf("提案或项目版本已变化，请重新生成提案")
			}
			current, err := latestAgentRunFrom(tx, owner, projectID)
			if err != nil {
				return err
			}
			if current.ID != proposal.Edits.BaseRunID || packageFingerprint(current) != proposal.BaseFingerprint {
				return fmt.Errorf("产物已变化，请重新生成提案")
			}
			if current.Status == "running" || current.Status == "stopping" || (current.Status == "submission_unknown" && !workflowTestingMode()) {
				return fmt.Errorf("当前运行尚未结束或订单待核对，不能应用修改")
			}
		}
		if err := w.ensureProjectOrdersSettled(tx, owner, projectID); err != nil {
			return err
		}
		if err := w.app.avatarMVP.ensureIdle(tx); err != nil {
			return err
		}
		// Resolve legacy voice defaults before deciding which previous audio can
		// be inherited into this new run.
		defaultNative := in.SpeechMode == "" && len(editOptions) == 0
		if defaultNative {
			in.SpeechMode = avatar.NativeSpeechMode
		}
		if err := w.validateInput(&in, false); err != nil {
			return err
		}
		now := time.Now().UTC()
		id := uuid.New()
		s = avatar.WorkflowState{ID: id.String(), ProjectID: projectID.String(), Version: avatar.WorkflowVersion, Input: in, ConfigID: w.app.avatarMVP.options.VideoConfigID(), Script: in.Script, Status: "running", Stage: "F01", CreatedAt: now, UpdatedAt: now, Steps: []avatar.WorkflowStep{}, Events: []avatar.WorkflowEvent{}, Artifacts: []avatar.WorkflowArtifact{}, Segments: []avatar.Segment{}, Materials: []avatar.Material{}, Needs: []avatar.MaterialNeed{}, Shots: []avatar.WorkflowShot{}}
		s.TargetNode = target
		s.ConfigSnapshot = w.configSnapshot()
		if avatar.NativeSpeech(in) {
			s.ConfigSnapshot["avatar_footage"] = "native:" + avatar.NativeSpeechModel
			delete(s.ConfigSnapshot, "portrait_policy")
		}
		s.ConfigID = workflowConfigID(s.ConfigSnapshot)
		edits := avatar.WorkflowEdits{}
		if len(editOptions) > 0 {
			edits = editOptions[0]
		}
		if edits.RerunFrom != "" && edits.BaseRunID == "" {
			return fmt.Errorf("重做必须指定已有基础运行")
		}
		var last models.WorkflowStepExecution
		priorQuery := tx.Table("workflow_step_executions AS s").Select("s.*").Joins("JOIN workflow_runs AS r ON r.id=s.workflow_run_id").Where("r.project_id=? AND r.user_id=? AND s.step_id=?", projectID, owner, workflowMarker)
		if edits.BaseRunID != "" {
			priorQuery = priorQuery.Where("r.id=?", edits.BaseRunID)
		}
		if err := priorQuery.Order("s.created_at DESC").First(&last).Error; err == nil {
			var previous avatar.WorkflowState
			if json.Unmarshal(last.Result, &previous) == nil {
				// Keep an existing production contract on a local edit. Upgrading
				// an old project is an explicit restart from the new design stage.
				if previous.Version != "" && previous.Version != avatar.WorkflowVersion && edits.RerunFrom != "F01.2" && edits.RerunFrom != "F01.6" && edits.Director == nil {
					s.Version = previous.Version
				}
				if err := w.inherit(&s, previous, edits); err != nil {
					return err
				}
			}
		} else if edits.BaseRunID != "" {
			return fmt.Errorf("局部修改基础运行不存在或不属于本项目")
		}
		if err := avatar.ValidateSubtitleEdits(s.Input, s.Segments); err != nil {
			return err
		}
		if err := w.validateInput(&s.Input, false); err != nil {
			return err
		}
		// Keep an accepted voice change in the draft as well as this run. The
		// revision and run admission commit together, so retries cannot lose it.
		if defaultNative || (edits.Voice != "" && (in.Voice != s.Input.Voice || in.VoiceModel != s.Input.VoiceModel)) || edits.VoiceSpeed != nil || edits.SegmentVoiceSpeeds != nil || edits.Presentation != nil || edits.Subtitles != nil || avatar.Digest(in.SubtitleEdits) != avatar.Digest(s.Input.SubtitleEdits) {
			in.Voice = s.Input.Voice
			in.VoiceModel = s.Input.VoiceModel
			in.VoiceSpeed = s.Input.VoiceSpeed
			in.SegmentVoiceSpeeds = s.Input.SegmentVoiceSpeeds
			edits.ApplyPresentation(&in)
			in.SubtitleEdits = s.Input.SubtitleEdits
			in.Revision++
			s.Input.Revision = in.Revision
			meta := map[string]any{}
			if err := json.Unmarshal(p.ProjectMetadata, &meta); err != nil {
				return err
			}
			meta["avatar_v2"] = in
			if err := tx.Model(&p).Update("project_metadata", jsonMap(meta)).Error; err != nil {
				return err
			}
		}
		run := models.WorkflowRun{ID: id, UserID: owner, ProjectID: &projectID, ClientType: "web", InputHash: hash, Status: "running", BillingStatus: "released", ClientInstanceID: avatarOwner, LastActiveAt: now, ExpiresAt: now.Add(24 * time.Hour)}
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		expiry := now.Add(workflowRunLeaseDuration)
		step := models.WorkflowStepExecution{WorkflowRunID: id, StepID: workflowMarker, InputHash: hash, ExecutionKey: workflowStepExecutionKey(id, workflowMarker, hash), Status: "running", Result: jsonMap(s), LeaseOwner: strPtr(avatarOwner), LeaseExpiresAt: &expiry}
		return tx.Create(&step).Error
	})
	return s, err
}

// Check every run, not just the latest revision: changing a draft or stopping a
// run does not settle an order. Idempotent replays are handled before this gate.
func (w *avatarWorkflowService) ensureProjectOrdersSettled(tx *gorm.DB, owner, project uuid.UUID) error {
	var rows []models.WorkflowStepExecution
	if err := tx.Table("workflow_step_executions AS s").Select("s.result").Joins("JOIN workflow_runs AS r ON r.id=s.workflow_run_id").Where("r.project_id=? AND r.user_id=? AND s.step_id=?", project, owner, workflowMarker).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		var state avatar.WorkflowState
		if err := json.Unmarshal(row.Result, &state); err != nil {
			return fmt.Errorf("历史运行状态无法读取，请先核对订单")
		}
		for _, step := range state.Steps {
			if step.Status == "succeeded" || step.LocalRecovery != "" {
				continue
			}
			if workflowTestingMode() && unknownWithoutTask(step) {
				continue
			}
			switch step.Submission {
			case "submitting", "unknown", "submitted":
				return fmt.Errorf("本项目存在待核对的供应商订单，请恢复原运行并核对订单，不能新建制作")
			}
		}
	}
	return nil
}
func strPtr(s string) *string { return &s }
func (w *avatarWorkflowService) stop(ctx context.Context, owner uuid.UUID, id string) error {
	if _, err := w.load(ctx, owner, id); err != nil {
		return err
	}
	return w.updateContext(ctx, id, func(s *avatar.WorkflowState) error {
		if s.Status == "succeeded" || s.Status == "stopped" {
			return nil
		}
		now := time.Now().UTC()
		s.StopRequested = true
		s.StoppedAt = &now
		if s.Status == "running" || s.Status == "stopping" {
			s.Status = "stopping"
		} else {
			s.Status = "stopped"
		}
		return nil
	})
}
func (w *avatarWorkflowService) beforeSubmit(s *avatar.WorkflowState, step string) error {
	return w.update(s.ID, func(current *avatar.WorkflowState) error {
		if current.StopRequested {
			return errWorkflowStopped
		}
		if current.PauseRequested {
			return errWorkflowPaused
		}
		// The persisted submitting claim is the linearization point shared with stop.
		for i := range s.Steps {
			if s.Steps[i].ID == step {
				s.Steps[i].Submission = "submitting"
				s.Steps[i].CostStatus = "unknown"
				s.Steps[i].PreserveOrder()
			}
		}
		for i := range s.Events {
			for _, old := range current.Events {
				if old.ID == s.Events[i].ID {
					s.Events[i].Read = old.Read
				}
			}
		}
		s.RefreshBilling()
		*current = *s
		return nil
	})
}
func (w *avatarWorkflowService) confirm(ctx context.Context, owner uuid.UUID, id, kind, version string, segments []avatar.Segment) error {
	if _, err := w.load(ctx, owner, id); err != nil {
		return err
	}
	return w.updateTransaction(ctx, id, func(tx *gorm.DB, s *avatar.WorkflowState) error {
		if s.Status != "awaiting_confirmation" || s.Confirmation != kind || s.ConfirmationVersion != version || s.StopRequested {
			return fmt.Errorf("确认版本已过期或任务已停止")
		}
		if err := w.app.avatarMVP.ensureIdle(tx); err != nil {
			return err
		}
		switch kind {
		case "shots", "final":
			if !avatar.DirectorWorkflow(s) || version != avatar.ReviewFingerprint(*s, kind) {
				return fmt.Errorf("审阅版本已变化，请重新查看预览")
			}
			if err := w.validateReview(s, kind); err != nil {
				return err
			}
			if kind == "shots" {
				s.ShotsConfirmed = version
			} else {
				s.FinalConfirmed = version
			}
		case "script":
			if len(segments) > 0 {
				for i := range segments {
					// Confirmation accepts text only, never browser-supplied media paths or timing.
					segments[i] = avatar.Segment{ID: segments[i].ID, Chapter: segments[i].Chapter, Text: segments[i].Text}
					for _, old := range s.Segments {
						if old.ID == segments[i].ID {
							segments[i].DirectorSceneID = old.DirectorSceneID
						}
					}
					for _, old := range s.Segments {
						if old.ID == segments[i].ID && old.Text == segments[i].Text && old.Audio != "" && old.Fingerprint != "" {
							chapter := segments[i].Chapter
							segments[i] = old
							segments[i].Chapter = chapter
							break
						}
					}
				}
				var text strings.Builder
				for _, p := range segments {
					text.WriteString(p.Text)
				}
				if err := avatar.ValidateSegments(text.String(), segments); err != nil {
					return err
				}
				s.Script = text.String()
				s.Segments = segments
			}
			s.ScriptConfirmed = true
		case "audio":
			if len(s.Segments) == 0 {
				return fmt.Errorf("配音尚未全部完成，请等待全部段落生成后继续")
			}
			for _, segment := range s.Segments {
				if segment.Audio == "" || segment.Duration <= 0 {
					return fmt.Errorf("配音尚未全部完成，请等待全部段落生成后继续")
				}
			}
			s.AudioConfirmed = true
		default:
			return fmt.Errorf("未知确认类型")
		}
		s.Status = "running"
		s.Confirmation = ""
		if s.AwaitingSince != nil {
			s.ConfirmationSeconds += time.Since(*s.AwaitingSince).Seconds()
			s.AwaitingSince = nil
		}
		return nil
	})
}
func (w *avatarWorkflowService) retry(ctx context.Context, owner uuid.UUID, id string, targets ...string) error {
	target := ""
	if len(targets) > 0 {
		target = targets[0]
	}
	return w.retryWithRequest(ctx, owner, id, target, "")
}

func (w *avatarWorkflowService) retryWithRequest(ctx context.Context, owner uuid.UUID, id, target, request string) error {
	if _, err := w.load(ctx, owner, id); err != nil {
		return err
	}
	return w.updateTransaction(ctx, id, func(tx *gorm.DB, s *avatar.WorkflowState) error {
		for _, accepted := range s.RecoveryRequests {
			if request != "" && accepted == request {
				return nil
			}
		}
		if s.Status != "failed" && s.Status != "interrupted" && s.Status != "submission_unknown" {
			return fmt.Errorf("当前任务不能重试；停止的任务请另建运行")
		}
		if s.ConfigID != workflowConfigID(w.configSnapshot()) && !workflowProviderConfigCompatible(s.ConfigSnapshot, w.configSnapshot()) {
			return fmt.Errorf("供应商配置已变更，请恢复原配置后查单")
		}
		if err := w.app.avatarMVP.ensureIdle(tx); err != nil {
			return err
		}
		for i := range s.Steps {
			step := &s.Steps[i]
			if (step.Submission == "submitting" || step.Submission == "unknown") && step.ProviderTaskID == "" {
				if workflowTestingMode() {
					step.PreserveOrder()
					step.Submission = ""
					step.Usage = nil
					s.AddWarning(step.ID, "test_retry", "用户已请求重新执行未取得任务号的步骤，原尝试记录保留。")
				} else if step.ID != "F10" {
					return fmt.Errorf("提交状态待确认，禁止重复付费；请先核对供应商订单")
				} else {
					if err := w.restoreInterruptedShots(s, step); err != nil {
						return err
					}
				}
			}
			if step.ID == "F10" && step.Submission == "completed" && strings.HasPrefix(step.Error, "分镜使用区间超出真实素材") && len(s.Shots) == 0 {
				if err := w.restoreCompletedShots(s, *step); err != nil {
					return err
				}
			}
			if step.ID == "F10" && (step.Error == "分镜组件不在允许清单" || step.Error == "分镜布局不在允许清单") && len(s.Shots) == 0 {
				shots := avatar.PresentationFallback(s.Segments, s.Materials)
				if err := avatar.ValidateWorkflowShots(shots, s.Segments, s.Materials, s.Duration); err == nil {
					s.Shots = shots
					s.AddWarning("F10", "quality", "原分镜组件不可用，已用同一批实际素材和锁定时长套用保守布局；没有新增模型调用，请检查画面设计")
				}
			}
		}
		s.Status = "running"
		if target != "" {
			s.TargetNode = target
		}
		if request != "" {
			s.RecoveryRequests = append(s.RecoveryRequests, request)
		}
		s.PauseRequested = false
		s.Error = ""
		s.ErrorType = ""
		s.ErrorStepID = ""
		return nil
	})
}
func (w *avatarWorkflowService) runMedia(ctx context.Context, stage, dir string) (json.RawMessage, error) {
	o := w.app.avatarMVP.options
	// Remotion/Chromium can copy the whole public task directory into /tmp.
	// Isolate each command's temporary data outside that public directory and
	// remove it after either success or failure. Crash leftovers are swept later.
	scratchRoot, err := filepath.Abs(filepath.Join(o.DataRoot, ".scratch"))
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(scratchRoot, 0700); err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp(scratchRoot, "media-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	cmd := exec.CommandContext(ctx, o.Python, filepath.Join(o.RuntimeRoot, "workflow.py"), stage, "--task-dir", dir, "--ffmpeg", o.FFmpeg, "--ffprobe", o.FFprobe, "--node", o.Node, "--asr-backend", o.ASRBackend, "--asr-model-dir", o.ASRModelDir)
	cmd.Dir = o.RuntimeRoot
	cmd.Env = append(os.Environ(), "TMPDIR="+scratch, "TMP="+scratch, "TEMP="+scratch)
	var tail workflowLogTail
	cmd.Stdout = &tail
	cmd.Stderr = &tail
	if err := cmd.Run(); err != nil {
		_ = os.WriteFile(filepath.Join(dir, stage+".log"), tail.Bytes(), 0600)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, mediaFailure(stage, tail.Bytes())
	}
	result, err := os.ReadFile(filepath.Join(dir, "media-result.json"))
	if err != nil {
		return nil, err
	}
	return result, nil
}
func (w *avatarWorkflowService) recover(ctx context.Context) error {
	var rows []models.WorkflowStepExecution
	if err := w.app.db.WithContext(ctx).Where("step_id=? AND status=? AND (lease_expires_at IS NULL OR lease_expires_at < ?)", workflowMarker, "running", time.Now().UTC()).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if err := w.update(row.WorkflowRunID.String(), func(s *avatar.WorkflowState) error {
			now := time.Now().UTC()
			for i := range s.Steps {
				step := &s.Steps[i]
				if step.Status == "running" && step.StartedAt != nil {
					seconds := now.Sub(*step.StartedAt).Seconds()
					step.Seconds += seconds
					step.CompletedAt = &now
					step.Status = "interrupted"
					step.Attempts = append(step.Attempts, avatar.WorkflowAttempt{Number: step.Attempt, StartedAt: *step.StartedAt, CompletedAt: now, Seconds: seconds, Status: "interrupted", CostStatus: step.CostStatus, Usage: step.Usage, TaskID: step.ProviderTaskID})
					step.PreserveOrder()
				}
			}
			s.RefreshBilling()
			if s.StopRequested {
				s.Status = "stopped"
			} else {
				s.Status = "interrupted"
				s.Error = "服务已中断；手动恢复将先查询原订单，不会重新提交已知订单"
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
func (w *avatarWorkflowService) heartbeat(id string) error {
	expiry := time.Now().UTC().Add(workflowRunLeaseDuration)
	return w.app.db.Model(&models.WorkflowStepExecution{}).Where("workflow_run_id=? AND step_id=? AND status=?", id, workflowMarker, "running").Updates(map[string]any{"lease_expires_at": expiry}).Error
}
func workflowError(c *gin.Context, err error) {
	code := 409
	if errors.Is(err, gorm.ErrRecordNotFound) {
		code = 404
	}
	fail(c, code, avatarPublicError(err))
}
func (a *App) workflowProjectCreate(c *gin.Context) {
	var in avatar.WorkflowInput
	if c.ShouldBindJSON(&in) != nil {
		fail(c, 400, "项目输入无效")
		return
	}
	in.Revision = 1
	in.SpeechMode = avatar.NativeSpeechMode
	in.VoiceReference = ""
	in.Photo = ""
	in.Assets = []avatar.SourceAsset{}
	if err := a.avatarWorkflow.validateInput(&in, false); err != nil {
		fail(c, 400, err.Error())
		return
	}
	p := models.Project{Name: in.Title, OwnerID: getUser(c).ID, ProjectMetadata: jsonMap(map[string]any{"kind": "avatar_mvp", "inference_mode": "avatar", "avatar_v2": in})}
	if p.Name == "" {
		p.Name = "数字人口播"
	}
	if err := a.db.Create(&p).Error; err != nil {
		fail(c, 500, "项目保存失败")
		return
	}
	ok(c, gin.H{"id": p.ID, "input": in})
}
func (a *App) workflowProjectGet(c *gin.Context) {
	id, err := uuid.Parse(c.Param("projectId"))
	if err != nil {
		fail(c, 404, "项目不存在")
		return
	}
	p, in, err := a.avatarWorkflow.project(c.Request.Context(), getUser(c).ID, id)
	if err != nil {
		workflowError(c, err)
		return
	}
	var rows []models.WorkflowRun
	a.db.Where("project_id=? AND client_instance_id=?", id, avatarOwner).Order("created_at DESC").Limit(50).Find(&rows)
	runs := []gin.H{}
	for _, r := range rows {
		var count int64
		a.db.Model(&models.WorkflowStepExecution{}).Where("workflow_run_id=? AND step_id=?", r.ID, workflowMarker).Count(&count)
		if count > 0 {
			runs = append(runs, gin.H{"id": r.ID, "status": r.Status, "created_at": r.CreatedAt})
		}
	}
	ok(c, gin.H{"id": p.ID, "input": in, "runs": runs})
}
func (a *App) workflowProjectPatch(c *gin.Context) {
	id, err := uuid.Parse(c.Param("projectId"))
	if err != nil {
		fail(c, 404, "项目不存在")
		return
	}
	var in avatar.WorkflowInput
	if c.ShouldBindJSON(&in) != nil {
		fail(c, 400, "输入无效")
		return
	}
	a.avatarMVP.mu.Lock()
	defer a.avatarMVP.mu.Unlock()
	err = a.db.Transaction(func(tx *gorm.DB) error {
		if err := avatarTransactionLock(tx); err != nil {
			return err
		}
		var p models.Project
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND owner_id=?", id, getUser(c).ID).First(&p).Error; err != nil {
			return err
		}
		old, err := workflowInput(p)
		if err != nil {
			return err
		}
		if old.Revision != in.Revision {
			return fmt.Errorf("草稿版本冲突，请刷新")
		}
		in.Photo = old.Photo
		excluded := map[string]bool{}
		for _, s := range in.Assets {
			excluded[s.ID] = s.Excluded
		}
		in.Assets = old.Assets
		for i := range in.Assets {
			in.Assets[i].Excluded = excluded[in.Assets[i].ID]
		}
		if err := a.avatarWorkflow.validateInput(&in, false); err != nil {
			return err
		}
		in.Revision++
		var meta map[string]any
		json.Unmarshal(p.ProjectMetadata, &meta)
		meta["avatar_v2"] = in
		return tx.Model(&p).Updates(map[string]any{"project_metadata": jsonMap(meta), "name": in.Title}).Error
	})
	if err != nil {
		workflowError(c, err)
		return
	}
	ok(c, gin.H{"id": id, "input": in})
}
func (a *App) workflowRunCreate(c *gin.Context) {
	id, err := uuid.Parse(c.Param("projectId"))
	if err != nil {
		fail(c, 404, "项目不存在")
		return
	}
	var req struct {
		Revision  int                  `json:"revision"`
		RequestID string               `json:"request_id"`
		Edits     avatar.WorkflowEdits `json:"edits"`
	}
	if c.ShouldBindJSON(&req) != nil {
		fail(c, 400, "运行参数无效")
		return
	}
	_, in, err := a.avatarWorkflow.project(c.Request.Context(), getUser(c).ID, id)
	if err != nil {
		workflowError(c, err)
		return
	}
	if err = a.avatarWorkflow.validateInput(&in, true); err != nil {
		fail(c, 422, err.Error())
		return
	}
	s, err := a.avatarWorkflow.create(c.Request.Context(), getUser(c).ID, id, req.RequestID, req.Revision, req.Edits)
	if err != nil {
		workflowError(c, err)
		return
	}
	if s.Status == "running" {
		a.avatarWorkflow.launch(s, getUser(c).ID)
	}
	c.JSON(202, s)
}
func (a *App) workflowRunGet(c *gin.Context) {
	s, err := a.avatarWorkflow.load(c.Request.Context(), getUser(c).ID, c.Param("id"))
	if err != nil {
		workflowError(c, err)
		return
	}
	s.Operations, _ = avatar.ReadOperations(filepath.Join(a.avatarWorkflow.dir(s.ID), "operations.jsonl"))
	ok(c, s)
}
func (a *App) workflowStop(c *gin.Context) {
	if err := a.avatarWorkflow.stop(c.Request.Context(), getUser(c).ID, c.Param("id")); err != nil {
		workflowError(c, err)
		return
	}
	a.workflowRunGet(c)
}
func (a *App) workflowConfirm(c *gin.Context) {
	var r struct {
		Kind     string           `json:"kind"`
		Version  string           `json:"version"`
		Segments []avatar.Segment `json:"segments"`
	}
	if c.ShouldBindJSON(&r) != nil {
		fail(c, 400, "确认参数无效")
		return
	}
	if err := a.avatarWorkflow.confirm(c.Request.Context(), getUser(c).ID, c.Param("id"), r.Kind, r.Version, r.Segments); err != nil {
		workflowError(c, err)
		return
	}
	s, _ := a.avatarWorkflow.load(c.Request.Context(), getUser(c).ID, c.Param("id"))
	a.avatarWorkflow.launch(s, getUser(c).ID)
	c.JSON(202, s)
}
func (a *App) workflowRetry(c *gin.Context) {
	s, err := a.avatarWorkflow.retryRun(c.Request.Context(), getUser(c).ID, c.Param("id"), "", c.GetHeader("Idempotency-Key"))
	if err != nil {
		workflowError(c, err)
		return
	}
	if s.Status == "running" {
		a.avatarWorkflow.launch(s, getUser(c).ID)
	}
	c.JSON(202, s)
}
func (a *App) workflowRead(c *gin.Context) {
	if _, err := a.avatarWorkflow.load(c.Request.Context(), getUser(c).ID, c.Param("id")); err != nil {
		workflowError(c, err)
		return
	}
	err := a.avatarWorkflow.update(c.Param("id"), func(s *avatar.WorkflowState) error {
		for i := range s.Events {
			if s.Events[i].ID == c.Param("eventId") {
				s.Events[i].Read = true
				return nil
			}
		}
		return gorm.ErrRecordNotFound
	})
	if err != nil {
		workflowError(c, err)
		return
	}
	ok(c, gin.H{"read": true})
}
func safeWorkflowFile(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "\\") {
		return "", fmt.Errorf("文件引用无效")
	}
	p := filepath.Join(root, filepath.FromSlash(rel))
	r, err := filepath.Rel(root, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("文件引用越界")
	}
	actual, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	r, err = filepath.Rel(root, actual)
	if err != nil || strings.HasPrefix(r, "..") {
		return "", fmt.Errorf("文件引用越界")
	}
	return p, nil
}
func (a *App) workflowArtifact(c *gin.Context) {
	s, err := a.avatarWorkflow.load(c.Request.Context(), getUser(c).ID, c.Param("id"))
	if err != nil {
		workflowError(c, err)
		return
	}
	for _, item := range s.Artifacts {
		if item.ID == c.Param("artifactId") {
			p, err := safeWorkflowFile(a.avatarWorkflow.dir(s.ID), item.Path)
			if err != nil {
				fail(c, 404, "产物文件不存在")
				return
			}
			f, err := os.Open(p)
			if err != nil {
				fail(c, 404, "产物不存在")
				return
			}
			defer f.Close()
			info, err := f.Stat()
			if err != nil {
				fail(c, 404, "产物不存在")
				return
			}
			c.Header("Content-Type", item.MediaType)
			c.Header("Cache-Control", "private, no-store")
			c.Header("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": item.Name}))
			http.ServeContent(c.Writer, c.Request, item.Name, info.ModTime(), f)
			return
		}
	}
	fail(c, 404, "产物不存在")
}
func (a *App) workflowUpload(c *gin.Context) {
	id, err := uuid.Parse(c.Param("projectId"))
	if err != nil {
		fail(c, 404, "项目不存在")
		return
	}
	unlock := lockPackageProject(id.String())
	defer unlock()
	_, _, err = a.avatarWorkflow.project(c.Request.Context(), getUser(c).ID, id)
	if err != nil {
		workflowError(c, err)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256<<20)
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		fail(c, 400, "文件无效或超过256MB")
		return
	}
	defer file.Close()
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	kind := c.Request.FormValue("kind")
	if kind != "photo" && kind != "video" && kind != "image" && kind != "text" && kind != "audio" {
		fail(c, 400, "素材类型无效")
		return
	}
	assetID := "a-" + uuid.NewString()
	rel := "assets/" + assetID
	root := a.avatarWorkflow.projectDir(id.String())
	dir := filepath.Join(root, "assets")
	if err = os.MkdirAll(dir, 0700); err != nil {
		fail(c, 500, "素材目录不可用")
		return
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		fail(c, 500, "无法保存素材")
		return
	}
	_, err = io.Copy(f, io.LimitReader(file, (256<<20)+1))
	f.Close()
	if err != nil {
		os.Remove(path)
		fail(c, 400, "上传中断")
		return
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(path)
		}
	}()
	probeDir, err := os.MkdirTemp(root, "probe-")
	if err != nil {
		fail(c, 500, "无法校验素材")
		return
	}
	defer os.RemoveAll(probeDir)
	avatar.WriteJSON(filepath.Join(probeDir, "probe-input.json"), map[string]any{"path": path, "kind": kind})
	raw, err := a.avatarWorkflow.media(c.Request.Context(), "probe", probeDir)
	if err != nil {
		fail(c, 400, "文件无法解码或不符合素材要求；参考音色需为 2–30 秒且不超过 20MB 的有效音频")
		return
	}
	var info struct {
		Duration float64 `json:"duration"`
		SHA256   string  `json:"sha256"`
	}
	if json.Unmarshal(raw, &info) != nil {
		fail(c, 400, "素材校验失败")
		return
	}
	source := avatar.SourceAsset{ID: assetID, Name: filepath.Base(header.Filename), Kind: kind, Path: rel, SHA256: info.SHA256, Duration: info.Duration}
	a.avatarMVP.mu.Lock()
	err = a.db.Transaction(func(tx *gorm.DB) error {
		if err := avatarTransactionLock(tx); err != nil {
			return err
		}
		var p models.Project
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND owner_id=?", id, getUser(c).ID).First(&p).Error; err != nil {
			return err
		}
		in, err := workflowInput(p)
		if err != nil {
			return err
		}
		if kind == "photo" {
			in.Photo = assetID
		}
		if kind == "audio" {
			in.VoiceReference = assetID
		}
		in.Assets = append(in.Assets, source)
		in.Revision++
		var meta map[string]any
		json.Unmarshal(p.ProjectMetadata, &meta)
		meta["avatar_v2"] = in
		return tx.Model(&p).Update("project_metadata", jsonMap(meta)).Error
	})
	a.avatarMVP.mu.Unlock()
	if err != nil {
		workflowError(c, err)
		return
	}
	keep = true
	ok(c, source)
}
func (a *App) workflowAsset(c *gin.Context) {
	id, err := uuid.Parse(c.Param("projectId"))
	if err != nil {
		fail(c, 404, "素材不存在")
		return
	}
	_, in, err := a.avatarWorkflow.project(c.Request.Context(), getUser(c).ID, id)
	if err != nil {
		workflowError(c, err)
		return
	}
	for _, asset := range in.Assets {
		if asset.ID != c.Param("assetId") {
			continue
		}
		p, err := safeWorkflowFile(a.avatarWorkflow.projectDir(id.String()), asset.Path)
		if err != nil {
			fail(c, 404, "素材不可用")
			return
		}
		f, err := os.Open(p)
		if err != nil {
			fail(c, 404, "素材不可用")
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			fail(c, 404, "素材不可用")
			return
		}
		c.Header("Cache-Control", "private, no-store")
		http.ServeContent(c.Writer, c.Request, asset.Name, info.ModTime(), f)
		return
	}
	fail(c, 404, "素材不存在")
}
func (a *App) registerAvatarWorkflow(r *gin.RouterGroup) {
	r.GET("/catalog", a.workflowCatalog)
	r.GET("/catalog/:style/preview", a.workflowStylePreview)
	r.GET("/catalog/:style/card-preview", a.workflowStylePreview)
	r.GET("/catalog/:style/card-poster", a.workflowStylePreview)
	r.GET("/catalog/:style/poster", a.workflowStylePreview)
	r.GET("/catalog/:style/reference", a.workflowStylePreview)
	r.GET("/catalog/:style/prompt", a.workflowStylePreview)
	r.GET("/catalog/:style/source", a.workflowStylePreview)
	r.GET("/catalog/voices/:voice/sample", a.workflowVoiceSample)
	r.POST("/projects", a.workflowProjectCreate)
	r.GET("/projects/:projectId", a.workflowProjectGet)
	r.PATCH("/projects/:projectId", a.workflowProjectPatch)
	r.POST("/projects/:projectId/assets", a.workflowUpload)
	r.GET("/projects/:projectId/assets/:assetId", a.workflowAsset)
	r.POST("/projects/:projectId/runs", a.workflowRunCreate)
	r.GET("/runs/:id", a.workflowRunGet)
	r.POST("/runs/:id/stop", a.workflowStop)
	r.POST("/runs/:id/confirm", a.workflowConfirm)
	r.POST("/runs/:id/script-review", a.workflowSaveScriptReview)
	r.POST("/runs/:id/retry", a.workflowRetry)
	r.POST("/runs/:id/events/:eventId/read", a.workflowRead)
	r.GET("/runs/:id/artifacts/:artifactId", a.workflowArtifact)
}

type workflowLogTail struct{ bytes.Buffer }

func (b *workflowLogTail) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < 512<<10 {
		remaining := (512 << 10) - b.Len()
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.Buffer.Write(p)
	}
	return n, nil
}
