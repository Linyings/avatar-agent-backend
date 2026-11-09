package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/agent"
	"github.com/narratoai/backend-go/internal/agentpackages"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type avatarPackageHost struct{ app *App }

var packageProjectLocks sync.Map

func lockPackageProject(id string) func() {
	v, _ := packageProjectLocks.LoadOrStore(id, &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

type packageProposal struct {
	Node            string               `json:"node,omitempty"`
	TargetNode      string               `json:"target_node,omitempty"`
	ID              string               `json:"id"`
	Revision        int                  `json:"revision"`
	Package         string               `json:"package"`
	Lines           []string             `json:"lines"`
	Edits           avatar.WorkflowEdits `json:"edits"`
	Voice           string               `json:"voice,omitempty"`
	BaseFingerprint string               `json:"baseFingerprint"`
	Applied         bool                 `json:"applied"`
	RunID           string               `json:"runId,omitempty"`
	CreatedAt       time.Time            `json:"createdAt"`
}

func (a *App) latestAgentRun(ctx context.Context, owner, project uuid.UUID) (avatar.WorkflowState, error) {
	return latestAgentRunFrom(a.db.WithContext(ctx), owner, project)
}

func latestAgentRunFrom(db *gorm.DB, owner, project uuid.UUID) (avatar.WorkflowState, error) {
	var row models.WorkflowStepExecution
	err := db.Table("workflow_step_executions AS s").Select("s.*").Joins("JOIN workflow_runs AS r ON r.id=s.workflow_run_id").Joins("JOIN projects AS p ON p.id=r.project_id AND p.deleted_at IS NULL").Where("r.project_id=? AND r.user_id=? AND p.owner_id=? AND s.step_id=?", project, owner, owner, workflowMarker).Order("s.created_at DESC").First(&row).Error
	var state avatar.WorkflowState
	if err != nil {
		return state, err
	}
	err = json.Unmarshal(row.Result, &state)
	return state, err
}

func (h avatarPackageHost) project(ctx context.Context, owner, project string) (models.Project, avatar.WorkflowInput, error) {
	uid, e1 := uuid.Parse(owner)
	pid, e2 := uuid.Parse(project)
	var p models.Project
	if e1 != nil || e2 != nil || uid == uuid.Nil {
		return p, avatar.WorkflowInput{}, gorm.ErrRecordNotFound
	}
	if err := h.app.db.WithContext(ctx).Where("id=? AND owner_id=?", pid, uid).First(&p).Error; err != nil {
		return p, avatar.WorkflowInput{}, err
	}
	in, err := workflowInput(p)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		in = avatar.WorkflowInput{Title: p.Name, Ratio: "9:16", Style: defaultWorkflowStyle(h.app.avatarMVP.options.RuntimeRoot), Voice: sVoice(h.app), Assets: []avatar.SourceAsset{}}
		if p.Description != nil {
			in.Topic = *p.Description
		}
		return p, in, nil
	}
	return p, in, err
}
func proposalOf(p models.Project) *packageProposal {
	var meta struct {
		Proposal *packageProposal `json:"agent_package_proposal"`
	}
	_ = json.Unmarshal(p.ProjectMetadata, &meta)
	return meta.Proposal
}
func (h avatarPackageHost) snapshot(ctx context.Context, owner, project string) (any, error) {
	p, in, err := h.project(ctx, owner, project)
	if err != nil {
		return nil, err
	}
	state, err := h.app.latestAgentRun(ctx, p.OwnerID, p.ID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var run any
	var planRun *avatar.WorkflowState
	if err == nil {
		run = state
		planRun = &state
	}
	styles, err := loadWorkflowStyles(h.app.avatarMVP.options.RuntimeRoot)
	if err != nil {
		return nil, err
	}
	return gin.H{"project": gin.H{"id": p.ID, "name": p.Name, "input": in}, "run": run, "material_drafts": visibleMaterialDrafts(p, state), "audio_timing": workflowAudioTiming(state), "testing_mode": workflowTestingMode(), "workflow_plan": avatar.PlanWorkflow(in, planRun), "agent_activity": agent.ProjectActivity(project), "proposal": proposalOf(p), "styles": styles, "voices": h.app.avatarWorkflow.voices(), "packages": h.catalog(), "skills": agent.DefaultSkills(), "agent_configured": h.app.cfg.AgentConfigured() || h.app.cfg.OpenAIProConfigured() || h.app.cfg.OpenAIFlashConfigured()}, nil
}
func (h avatarPackageHost) catalog() []map[string]any {
	manifests, _ := agentpackages.Load()
	out := []map[string]any{}
	for _, p := range manifests {
		raw, _ := json.Marshal(p)
		var item map[string]any
		_ = json.Unmarshal(raw, &item)
		reason := ""
		if p.Node == "F01.2" || p.Node == "F01.6" || p.Node == "F02" || p.Node == "F03" || p.Node == "F05" || p.Node == "F10" {
			if !h.app.cfg.OpenAIFlashConfigured() && !h.app.cfg.AgentConfigured() {
				reason = "文本模型未配置"
			}
		}
		if p.Node == "F01.4" && !h.app.avatarWorkflow.visionConfigured() {
			reason = "视频理解模型未配置；没有上传视频时自动跳过"
		}
		if p.Node == "F04" {
			if err := h.app.avatarWorkflow.speechConfigured(); err != nil {
				reason = err.Error()
			}
		}
		if p.Node == "F06" {
			if err := h.app.avatarMVP.options.ValidateVideo(); err != nil {
				reason = "数字人视频服务未配置"
			}
		}
		item["ready"] = reason == ""
		item["reason"] = reason
		out = append(out, item)
	}
	return out
}

func (h avatarPackageHost) Invoke(ctx context.Context, owner, project, id string, args map[string]any, request string) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, ok := agentpackages.Find(id)
	if !ok {
		return nil, fmt.Errorf("package 不存在")
	}
	if args == nil {
		args = map[string]any{}
	}
	if err := agentpackages.Validate(p.InputSchema, args); err != nil {
		return nil, err
	}
	unlock := lockPackageProject(project)
	defer unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	record, in, err := h.project(ctx, owner, project)
	if err != nil {
		return nil, err
	}
	switch p.Entrypoint {
	case "inspect":
		return h.snapshot(ctx, owner, project)
	case "configure":
		return h.configure(ctx, record, in, args)
	case "run":
		if key, ok := args["request_id"].(string); ok && key != "" {
			request = p.ID + "/" + key
		}
		return h.run(ctx, record, in, p.Node, request)
	case "control":
		return h.control(ctx, record, args, request)
	case "rerun", "edit_node":
		return h.draftNode(ctx, record, in, p, args)
	case "edit_script", "edit_voice", "edit_material", "edit_shot":
		return h.draft(ctx, record, in, p, args)
	}
	return nil, fmt.Errorf("package 执行入口未注册")
}

func (h avatarPackageHost) configure(ctx context.Context, p models.Project, in avatar.WorkflowInput, args map[string]any) (any, error) {
	a := h.app
	a.avatarMVP.mu.Lock()
	defer a.avatarMVP.mu.Unlock()
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := avatarTransactionLock(tx); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND owner_id=?", p.ID, p.OwnerID).First(&p).Error; err != nil {
			return err
		}
		old, e := workflowInput(p)
		if e == nil {
			in = old
		}
		if revision, ok := args["revision"].(float64); ok && int(revision) != in.Revision {
			return fmt.Errorf("项目已更新，请刷新后再保存")
		}
		raw, _ := json.Marshal(args)
		if _, present := args["segment_voice_speeds"]; present {
			in.SegmentVoiceSpeeds = nil
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return err
		}
		if err := a.avatarWorkflow.validateInput(&in, false); err != nil {
			return err
		}
		in.Revision++
		if in.Title == "" {
			in.Title = p.Name
		}
		meta := map[string]any{}
		_ = json.Unmarshal(p.ProjectMetadata, &meta)
		meta["avatar_v2"] = in
		meta["kind"] = "avatar_mvp"
		meta["inference_mode"] = "avatar"
		return tx.Model(&p).Updates(map[string]any{"project_metadata": jsonMap(meta), "name": in.Title}).Error
	})
	if err != nil {
		return nil, err
	}
	return gin.H{"saved": true, "input": in}, nil
}

func (h avatarPackageHost) run(ctx context.Context, p models.Project, in avatar.WorkflowInput, target, request string) (any, error) {
	if in.Revision == 0 {
		return nil, fmt.Errorf("请先用 avatar_configure 保存制作需求")
	}
	if err := h.app.avatarWorkflow.validatePackageInput(&in, target); err != nil {
		return nil, err
	}
	s, err := h.app.latestAgentRun(ctx, p.OwnerID, p.ID)
	if err == nil && s.Status == "awaiting_confirmation" {
		return workflowControlResult(s), nil
	}
	if err == nil && workflowTestingMode() && (s.Status == "failed" || s.Status == "interrupted" || s.Status == "submission_unknown") {
		next, e := h.app.avatarWorkflow.retryRun(ctx, p.OwnerID, s.ID, target, request)
		if e != nil {
			return nil, e
		}
		if next.Status == "running" {
			h.app.avatarWorkflow.launch(next, p.OwnerID)
		}
		return gin.H{"submitted": next.Status == "running", "run_id": next.ID, "status": next.Status, "target": next.TargetNode}, nil
	}
	if err == nil && s.Input.Revision == in.Revision {
		if s.Status == "succeeded" {
			return gin.H{"submitted": false, "run_id": s.ID, "status": s.Status, "note": "本版本已完成。若要重新制作请调用 avatar_rerun，或 plan_project 同时传 rerunFrom 起点与 target 终点；修改内容使用 avatar_edit_node。"}, nil
		}
		if s.Status == "running" || s.Status == "stopping" {
			return gin.H{"submitted": false, "run_id": s.ID, "status": s.Status, "note": "已有运行，请等待或先暂停"}, nil
		}
		if s.Status == "paused" {
			err = h.app.avatarWorkflow.updateTransaction(ctx, s.ID, func(tx *gorm.DB, current *avatar.WorkflowState) error {
				if current.Status != "paused" {
					return fmt.Errorf("运行状态已变化")
				}
				if e := h.app.avatarMVP.ensureIdle(tx); e != nil {
					return e
				}
				current.TargetNode = target
				current.PauseRequested = false
				current.Status = "running"
				return nil
			})
			if err != nil {
				return nil, err
			}
			s, err = h.app.avatarWorkflow.load(ctx, p.OwnerID, s.ID)
			if err != nil {
				return nil, err
			}
			h.app.avatarWorkflow.launch(s, p.OwnerID)
			return gin.H{"submitted": true, "run_id": s.ID, "status": s.Status, "target": target}, nil
		}
		if s.Status == "failed" || s.Status == "interrupted" || s.Status == "submission_unknown" {
			return nil, fmt.Errorf("已有失败或待核对的运行，请先用运行控制重试，禁止绕过原订单新建")
		}
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if len(request) < 4 {
		request = uuid.NewString()
	}
	if len(request) > 100 {
		request = avatar.Digest(request)
	}
	s, err = h.app.avatarWorkflow.createTo(ctx, p.OwnerID, p.ID, request, in.Revision, target)
	if err != nil {
		return nil, err
	}
	if s.Status == "running" {
		h.app.avatarWorkflow.launch(s, p.OwnerID)
	}
	return gin.H{"submitted": s.Status == "running", "run_id": s.ID, "status": s.Status, "target": target}, nil
}

func (h avatarPackageHost) control(ctx context.Context, p models.Project, args map[string]any, requests ...string) (any, error) {
	s, err := h.app.latestAgentRun(ctx, p.OwnerID, p.ID)
	if err != nil {
		return nil, fmt.Errorf("还没有制作运行")
	}
	action, _ := args["action"].(string)
	if action == "resume" && workflowLegacyNarrationLimit(s) {
		action = "retry"
	}
	w := h.app.avatarWorkflow
	if workflowControlReadOnly(s, action) {
		return workflowControlResult(s), nil
	}
	if workflowTestingMode() && (action == "retry" || (action == "resume" && (s.Status == "failed" || s.Status == "interrupted" || s.Status == "submission_unknown"))) {
		request := ""
		if len(requests) > 0 {
			request = requests[0]
		}
		next, e := w.retryRun(ctx, p.OwnerID, s.ID, "", request)
		if e != nil {
			if current, readErr := h.app.latestAgentRun(ctx, p.OwnerID, p.ID); readErr == nil && workflowControlReadOnly(current, action) {
				return workflowControlResult(current), nil
			}
			return nil, e
		}
		if next.Status == "awaiting_confirmation" {
			return workflowControlResult(next), nil
		}
		if next.Status == "running" {
			w.launch(next, p.OwnerID)
		}
		return gin.H{"submitted": next.Status == "running", "run_id": next.ID, "status": next.Status, "target": next.TargetNode}, nil
	}
	switch action {
	case "stop":
		err = w.stop(ctx, p.OwnerID, s.ID)
	case "pause":
		err = w.updateContext(ctx, s.ID, func(current *avatar.WorkflowState) error {
			if current.Status != "running" && current.Status != "paused" {
				return fmt.Errorf("当前状态不能暂停")
			}
			current.PauseRequested = true
			return nil
		})
	case "resume":
		if err = w.validatePackageInput(&s.Input, "F12"); err != nil {
			return nil, err
		}
		err = w.updateTransaction(ctx, s.ID, func(tx *gorm.DB, current *avatar.WorkflowState) error {
			if current.Status != "paused" {
				return fmt.Errorf("仅已暂停的运行可以继续；审核节点需要先确认")
			}
			if e := h.app.avatarMVP.ensureIdle(tx); e != nil {
				return e
			}
			current.PauseRequested = false
			current.TargetNode = "F12"
			current.Status = "running"
			return nil
		})
	case "retry":
		err = w.retry(ctx, p.OwnerID, s.ID)
	default:
		return nil, fmt.Errorf("不支持的控制操作")
	}
	if err != nil {
		if current, readErr := h.app.latestAgentRun(ctx, p.OwnerID, p.ID); readErr == nil && workflowControlReadOnly(current, action) {
			return workflowControlResult(current), nil
		}
		return nil, err
	}
	s, err = w.load(ctx, p.OwnerID, s.ID)
	if err != nil {
		return nil, err
	}
	if action == "resume" || action == "retry" {
		w.launch(s, p.OwnerID)
	}
	agent.PublishNodeStatus(p.ID.String(), workflowNodeOf(s.Stage), s.Status)
	return gin.H{"action": action, "run_id": s.ID, "status": s.Status, "pause_requested": s.PauseRequested}, nil
}

func (a *App) registerPackageRoutes(r *gin.RouterGroup) {
	a.registerMaterialNodeRoutes(r)
	h := avatarPackageHost{a}
	r.GET("/runs/:runId/segments/:segmentId/audio", func(c *gin.Context) {
		s, err := a.avatarWorkflow.load(c.Request.Context(), getUser(c).ID, c.Param("runId"))
		if err != nil {
			workflowError(c, err)
			return
		}
		for _, seg := range s.Segments {
			if seg.ID == c.Param("segmentId") && seg.Audio != "" {
				path, err := safeWorkflowFile(a.avatarWorkflow.dir(s.ID), seg.Audio)
				if err != nil {
					fail(c, 404, "音频不存在")
					return
				}
				c.Header("Cache-Control", "private, no-cache")
				c.File(path)
				return
			}
		}
		fail(c, 404, "配音尚未生成")
	})
	r.GET("/packages", func(c *gin.Context) { ok(c, gin.H{"packages": h.catalog()}) })
	r.GET("/project/:projectId", func(c *gin.Context) {
		out, err := h.snapshot(c.Request.Context(), getUser(c).ID.String(), c.Param("projectId"))
		if err != nil {
			workflowError(c, err)
			return
		}
		ok(c, out)
	})
	r.POST("/packages/:packageId/invoke", func(c *gin.Context) {
		var req struct {
			ProjectID string         `json:"projectId"`
			Input     map[string]any `json:"input"`
			RequestID string         `json:"requestId"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, 400, "插件参数无效")
			return
		}
		out, err := h.Invoke(c.Request.Context(), getUser(c).ID.String(), req.ProjectID, c.Param("packageId"), req.Input, req.RequestID)
		if err != nil {
			workflowError(c, err)
			return
		}
		ok(c, out)
	})
}

func packageFingerprint(s avatar.WorkflowState) string {
	return avatar.Digest([]any{s.ID, s.Input, s.Script, s.Segments, s.Descriptions, s.Needs, s.Materials, s.Shots})
}

// mergePackageRows only changes existing IDs and leaves paths/timing outside schemas.
func mergePackageRows[T any](rows []T, patches any) ([]T, error) {
	raw, _ := json.Marshal(rows)
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	list, ok := patches.([]any)
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("必须提供至少一个目标")
	}
	seen := map[string]bool{}
	for _, v := range list {
		patch, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("修改格式无效")
		}
		id, _ := patch["id"].(string)
		if id == "" || seen[id] {
			return nil, fmt.Errorf("目标 ID 缺失或重复")
		}
		seen[id] = true
		found := false
		for _, row := range out {
			if row["id"] == id {
				for k, value := range patch {
					row[k] = value
				}
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("目标 %s 不存在，请先检索项目", id)
		}
	}
	raw, _ = json.Marshal(out)
	var result []T
	err := json.Unmarshal(raw, &result)
	return result, err
}
