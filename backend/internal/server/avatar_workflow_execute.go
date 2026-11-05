package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm/clause"
)

// launchLocks prevents duplicate HTTP idempotency replays starting another goroutine.
var workflowLaunchLocks sync.Map
var workflowLaunchMu sync.Mutex

type workflowLaunchState struct{ pending bool }

func (w *avatarWorkflowService) launch(s avatar.WorkflowState, owner uuid.UUID) {
	workflowLaunchMu.Lock()
	if active, loaded := workflowLaunchLocks.Load(s.ID); loaded {
		active.(*workflowLaunchState).pending = true
		workflowLaunchMu.Unlock()
		return
	}
	active := &workflowLaunchState{}
	workflowLaunchLocks.Store(s.ID, active)
	workflowLaunchMu.Unlock()
	go func() {
		for {
			w.execute(s, owner)
			workflowLaunchMu.Lock()
			pending := active.pending
			active.pending = false
			if !pending {
				workflowLaunchLocks.Delete(s.ID)
				workflowLaunchMu.Unlock()
				return
			}
			workflowLaunchMu.Unlock()
			next, err := w.load(context.Background(), owner, s.ID)
			if err == nil && next.Status == "running" {
				s = next
				continue
			}
			workflowLaunchMu.Lock()
			workflowLaunchLocks.Delete(s.ID)
			workflowLaunchMu.Unlock()
			return
		}
	}()
}
func (w *avatarWorkflowService) execute(s avatar.WorkflowState, owner uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
	defer cancel()
	if err := os.MkdirAll(w.dir(s.ID), 0700); err != nil {
		s.Status = "failed"
		s.Error = "任务目录不可用"
		w.save(&s)
		return
	}
	_ = w.heartbeat(s.ID)
	ctx = avatar.WithTelemetry(ctx, filepath.Join(w.dir(s.ID), "operations.jsonl"))
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := w.heartbeat(s.ID); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	err := w.produceWorkflow(ctx, &s, owner)
	if err != nil {
		s.Error = err.Error()
		s.ErrorType = "execution"
		if errors.Is(err, errWorkflowStopped) {
			s.Status = "stopped"
			s.Error = ""
		} else {
			s.Status = "failed"
			for _, step := range s.Steps {
				if step.Status == "failed" {
					s.ErrorType = step.ErrorType
				}
				if (step.Submission == "submitting" || step.Submission == "unknown") && step.ProviderTaskID == "" {
					s.Status = "submission_unknown"
				}
			}
		}
	} else if s.Status == "running" {
		now := time.Now().UTC()
		s.CompletedAt = &now
		s.Status = "succeeded"
		s.Stage = "completed"
	}
	// A stop accepted while the final local command was running still wins.
	current, e := w.load(context.Background(), owner, s.ID)
	if e == nil && current.StopRequested {
		s.StopRequested = true
		s.Status = "stopped"
		s.Error = ""
	}
	// await() already committed the gate. A rapid confirmation must not be
	// overwritten by this worker's stale awaiting_confirmation snapshot.
	if s.Status != "awaiting_confirmation" {
		_ = w.save(&s)
	}
	_ = w.app.db.Model(&models.WorkflowStepExecution{}).Where("workflow_run_id=? AND step_id=? AND status=?", s.ID, workflowMarker, s.Status).Updates(map[string]any{"lease_owner": nil, "lease_expires_at": nil}).Error
	s.Operations, _ = avatar.ReadOperations(filepath.Join(w.dir(s.ID), "operations.jsonl"))
	_ = avatar.WriteJSON(filepath.Join(w.dir(s.ID), "report.json"), s)
	if s.Status == "succeeded" {
		w.app.db.Model(&models.Project{}).Where("id=?", s.ProjectID).Update("status", "completed")
	}
}
func (w *avatarWorkflowService) recordStep(s *avatar.WorkflowState, step avatar.WorkflowStep) error {
	id, _ := uuid.Parse(s.ID)
	row := models.WorkflowStepExecution{WorkflowRunID: id, StepID: "v2." + step.ID, InputHash: step.InputHash, ExecutionKey: workflowStepExecutionKey(id, "v2."+step.ID, step.InputHash), Status: step.Status, AttemptCount: step.Attempt, Result: jsonMap(step), StartedAt: step.StartedAt, CompletedAt: step.CompletedAt}
	if step.ProviderTaskID != "" {
		row.ProviderRequestID = &step.ProviderTaskID
	}
	return w.app.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "execution_key"}}, DoUpdates: clause.AssignmentColumns([]string{"status", "attempt_count", "provider_request_id", "result", "started_at", "completed_at", "updated_at"})}).Create(&row).Error
}
func (w *avatarWorkflowService) step(ctx context.Context, s *avatar.WorkflowState, id string, input any, fn func(context.Context, *avatar.WorkflowStep) error) error {
	current, err := w.load(ctx, uuid.MustParse(w.ownerID(s)), s.ID)
	if err != nil {
		return err
	}
	if current.StopRequested {
		return errWorkflowStopped
	}
	hash := avatar.Digest(input)
	idx := -1
	for i := range s.Steps {
		if s.Steps[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.Steps = append(s.Steps, avatar.WorkflowStep{ID: id, InputHash: hash, CostStatus: "local_compute"})
		idx = len(s.Steps) - 1
	}
	step := &s.Steps[idx]
	if step.Status == "succeeded" && step.InputHash == hash {
		return nil
	}
	if step.InputHash != hash && step.ProviderTaskID != "" {
		return fmt.Errorf("已有订单的步骤输入发生变化，请创建新运行")
	}
	now := time.Now().UTC()
	step.InputHash = hash
	step.Attempt++
	step.Status = "running"
	step.StartedAt = &now
	step.CompletedAt = nil
	step.Error = ""
	step.ErrorType = ""
	s.Stage = id
	if err = w.save(s); err != nil {
		return err
	}
	opCtx, finish := avatar.BeginOperation(ctx, id, "")
	err = fn(opCtx, step)
	finish(err)
	step = &s.Steps[idx]
	operations, _ := avatar.ReadOperations(filepath.Join(w.dir(s.ID), "operations.jsonl"))
	for _, op := range operations {
		if op.Scope == "api" && op.Step == id && !op.StartedAt.Before(now) {
			if step.CostStatus != "reported" || op.CostStatus == "reported" {
				step.CostStatus = op.CostStatus
			}
			if len(op.Usage) > 0 {
				step.Usage, _ = json.Marshal(op.Usage)
			}
		}
	}
	end := time.Now().UTC()
	step.CompletedAt = &end
	step.Seconds += end.Sub(now).Seconds()
	step.Status = "succeeded"
	if err != nil {
		step.Status = "failed"
		step.Error = err.Error()
		if step.ErrorType == "" {
			step.ErrorType = "execution"
		}
	}
	step.Attempts = append(step.Attempts, avatar.WorkflowAttempt{Number: step.Attempt, StartedAt: now, CompletedAt: end, Seconds: end.Sub(now).Seconds(), Status: step.Status, CostStatus: step.CostStatus, Usage: step.Usage, TaskID: step.ProviderTaskID})
	if e := w.recordStep(s, *step); e != nil {
		return e
	}
	if e := w.save(s); e != nil {
		return e
	}
	return err
}
func (w *avatarWorkflowService) ownerID(s *avatar.WorkflowState) string {
	var r models.WorkflowRun
	w.app.db.Select("user_id").Where("id=?", s.ID).First(&r)
	return r.UserID.String()
}
func (w *avatarWorkflowService) model(ctx context.Context, s *avatar.WorkflowState, step *avatar.WorkflowStep, prompt string, input any, frames []string, out any) error {
	if step.Submission == "unknown" || step.Submission == "submitting" {
		return fmt.Errorf("文本/理解请求提交状态待确认，请先核对供应商调用记录，禁止盲目重交")
	}
	if err := w.beforeSubmit(s, step.ID); err != nil {
		return err
	}
	step.CostStatus = "unknown"
	raw, err := w.content(ctx, prompt, input, frames)
	if err != nil {
		step.ErrorType = "provider"
		step.Submission = "unknown"
		if e, ok := err.(interface{ ProviderHTTPStatus() int }); ok && e.ProviderHTTPStatus() >= 400 {
			step.Submission = "rejected"
			_ = w.save(s)
			return fmt.Errorf("模型服务调用失败（HTTP %d），请检查该服务的鉴权、额度及运行状态", e.ProviderHTTPStatus())
		}
		_ = w.save(s)
		if diagnostic, ok := err.(interface{ ProviderDiagnosticFields() map[string]string }); ok {
			code := diagnostic.ProviderDiagnosticFields()["error_code"]
			labels := map[string]string{"network_dns": "文本模型域名解析失败", "network_timeout": "文本模型连接或响应超时", "network_connect": "无法连接文本模型服务", "network_tls": "文本模型TLS连接失败", "connection_interrupted": "文本模型连接或响应中断", "response_invalid_json": "文本模型服务返回了无法解析的响应", "response_no_choices": "文本模型服务未返回候选内容", "response_empty_content": "文本模型服务返回空内容", "model_invalid_json": "文本模型输出不是本节点需要的JSON", "request_canceled": "文本模型请求被取消"}
			if label := labels[code]; label != "" {
				return fmt.Errorf("%s（%s）；本次调用状态待确认，未自动重复提交", label, code)
			}
		}
		return fmt.Errorf("模型/网络调用失败，请检查模型服务配置和请求日志；已知订单不会重复提交")
	}
	step.Submission = "completed"
	resultDir := filepath.Join(w.dir(s.ID), "model-results")
	if err := os.MkdirAll(resultDir, 0700); err != nil {
		return err
	}
	if err := avatar.WriteJSON(filepath.Join(resultDir, fmt.Sprintf("%s-attempt-%d.json", avatar.Digest(step.ID)[:16], step.Attempt)), raw); err != nil {
		return err
	}
	if err := w.save(s); err != nil {
		return err
	}
	if err = json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("模型结果缺少本节点必需的数据结构")
	}
	return nil
}
func (w *avatarWorkflowService) mediaStep(ctx context.Context, s *avatar.WorkflowState, stage string, input any, out any) error {
	dir := w.dir(s.ID)
	if err := avatar.WriteJSON(filepath.Join(dir, "workflow.json"), s); err != nil {
		return err
	}
	if err := avatar.WriteJSON(filepath.Join(dir, "media-input.json"), input); err != nil {
		return err
	}
	paths := map[string]string{"__cache": filepath.Join(w.projectDir(s.ProjectID), "cache")}
	for _, a := range s.Input.Assets {
		if !a.Excluded {
			p, err := safeWorkflowFile(w.projectDir(s.ProjectID), a.Path)
			if err != nil {
				return fmt.Errorf("原素材文件不可用")
			}
			paths[a.ID] = p
		}
	}
	if err := avatar.WriteJSON(filepath.Join(dir, "asset-paths.json"), paths); err != nil {
		return err
	}
	raw, err := w.media(ctx, stage, dir)
	if err != nil {
		return err
	}
	var warnings struct {
		Warnings []string `json:"warnings"`
	}
	_ = json.Unmarshal(raw, &warnings)
	for _, msg := range warnings.Warnings {
		s.AddWarning(s.Stage, "quality", msg)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
func (w *avatarWorkflowService) await(s *avatar.WorkflowState, kind string) error {
	now := time.Now().UTC()
	s.AwaitingSince = &now
	s.Status = "awaiting_confirmation"
	s.Confirmation = kind
	if kind == "script" {
		s.ConfirmationVersion = avatar.Digest(s.Segments)
	} else {
		s.ConfirmationVersion = avatar.Digest(s.Segments)
	}
	return w.save(s)
}
func (w *avatarWorkflowService) produceWorkflow(ctx context.Context, s *avatar.WorkflowState, owner uuid.UUID) error {
	// F01.4 only runs for uploaded videos. Analysis is before all final writing.
	var analysisError error
	for _, source := range s.Input.Assets {
		if source.Excluded || source.Kind != "video" {
			continue
		}
		err := w.step(ctx, s, "F01.4/"+source.ID, []any{source.SHA256, s.ConfigSnapshot["vision_model"]}, func(ctx context.Context, step *avatar.WorkflowStep) error {
			var batches struct {
				Batches []struct {
					Start  float64   `json:"start"`
					End    float64   `json:"end"`
					Frames []string  `json:"frames"`
					Times  []float64 `json:"times"`
				} `json:"batches"`
			}
			if err := w.mediaStep(ctx, s, "frames", source, &batches); err != nil {
				return err
			}
			var batchError error
			for index, batch := range batches.Batches {
				if err := w.step(ctx, s, fmt.Sprintf("F01.4/%s-b%d", source.ID, index), []any{source.SHA256, batch.Start, batch.End, s.ConfigSnapshot["vision_model"]}, func(ctx context.Context, batchStep *avatar.WorkflowStep) error {
					var result struct {
						Clips []avatar.SourceDescription `json:"clips"`
					}
					prompt := `你是视频画面理解节点。按带源时间的帧序列描述可见内容、动作和变化，不能只转写声音。输出 {"clips":[{"start":源秒,"end":源秒,"text":"可见事实描述"}]}。时间在给定批次内，不能声称观察到采样间的短动作；不确定标记待核实。`
					if err := w.model(ctx, s, batchStep, prompt, map[string]any{"name": source.Name, "start": batch.Start, "end": batch.End, "frame_times": batch.Times}, batch.Frames, &result); err != nil {
						return err
					}
					if len(result.Clips) == 0 {
						return fmt.Errorf("视频理解未产生片段文案")
					}
					for j := range result.Clips {
						d := &result.Clips[j]
						if d.Start < batch.Start || d.End > batch.End+.1 || d.End <= d.Start || strings.TrimSpace(d.Text) == "" {
							return fmt.Errorf("视频理解时间范围无效")
						}
						d.AssetID = source.ID
						d.ID = fmt.Sprintf("%s-b%d-c%d", source.ID, index, j)
						d.Coverage = "按源时间抽帧分析；未采样动作未核实"
					}
					// Persist each batch independently; retry below reuses analysis by stable IDs.
					existing := map[string]bool{}
					for _, d := range s.Descriptions {
						existing[d.ID] = true
					}
					for _, d := range result.Clips {
						if !existing[d.ID] {
							s.Descriptions = append(s.Descriptions, d)
						}
					}
					return w.save(s)
				}); err != nil {
					if errors.Is(err, errWorkflowStopped) || ctx.Err() != nil {
						return err
					}
					if batchError == nil {
						batchError = err
					}
				}
			}
			return batchError
		})
		if err != nil {
			if errors.Is(err, errWorkflowStopped) || ctx.Err() != nil {
				return err
			}
			if analysisError == nil {
				analysisError = err
			}
		}
	}
	if analysisError != nil {
		return analysisError
	}
	if err := w.step(ctx, s, "F02", []any{s.Input.Script, s.Input.Topic, s.Descriptions}, func(ctx context.Context, step *avatar.WorkflowStep) error {
		if s.ReusedFrom != "" && len(s.Segments) > 0 {
			step.CostStatus = "reused"
			return nil
		}
		if strings.TrimSpace(s.Input.Script) != "" {
			s.Script = s.Input.Script
			if len(s.Descriptions) > 0 {
				s.AddWarning("F02", "notice", "已保留原稿；请在稿件确认时核对上传素材文案与原稿是否一致")
			}
			return nil
		}
		var out struct {
			Title  string `json:"title"`
			Script string `json:"script"`
		}
		if err := w.model(ctx, s, step, `按用户主题和素材事实写中文口播稿，目标20–60秒、总时长不超过90秒。包含开场、解释和结论；不虚构统计/出处。输出 {"title":"标题","script":"纯朗读正文"}。拍摄要求不进入正文。`, map[string]any{"topic": s.Input.Topic, "descriptions": s.Descriptions}, nil, &out); err != nil {
			return err
		}
		if strings.TrimSpace(out.Script) == "" || len([]rune(out.Script)) > 1000 {
			return fmt.Errorf("写稿结果为空或超过首版范围")
		}
		s.Script = out.Script
		if s.Input.Title == "" {
			s.Input.Title = out.Title
		}
		return nil
	}); err != nil {
		return err
	}
	if !s.ScriptConfirmed {
		if err := w.step(ctx, s, "F03", s.Script, func(ctx context.Context, step *avatar.WorkflowStep) error {
			if len(s.Segments) > 0 {
				return avatar.ValidateSegments(s.Script, s.Segments)
			}
			var out struct {
				Segments        []avatar.Segment `json:"segments"`
				ProductionNotes []string         `json:"production_notes"`
				Angles          []string         `json:"angles"`
				Conflict        string           `json:"conflict"`
			}
			if err := w.model(ctx, s, step, `只按内容/剧情/知识点/章节拆解完整口播稿，不规划任何画面。输出 {"segments":[{"id":"p1","chapter":"章节","text":"连续原文"}],"production_notes":["原文中明确给制作者的要求，逐字引用，仅在存在时提取"],"angles":["用户明确要求的机位：正面、左前方20度、右前方20度"],"conflict":"主题、原稿与素材事实存在明显冲突时提示，否则空字符串"}。每段建议1–3个完整句子，约5–12秒；除明确的制作指令外，全部原文必须按原序恰好覆盖一次，不改写、不省略标点。普通讲解镜头知识的台词不属于制作指令。无明确要求angles为空，不输出画面计划。`, map[string]any{"script": s.Script, "topic": s.Input.Topic, "descriptions": s.Descriptions}, nil, &out); err != nil {
				return err
			}
			spoken := s.Script
			for _, note := range out.ProductionNotes {
				if strings.TrimSpace(note) == "" || !strings.Contains(spoken, note) {
					return fmt.Errorf("制作要求不是原稿中的连续引用")
				}
				spoken = strings.Replace(spoken, note, "", 1)
			}
			if len(out.ProductionNotes) > 0 {
				for _, angle := range out.Angles {
					if angle != "正面" && angle != "左前方20度" && angle != "右前方20度" {
						return fmt.Errorf("请在制作要求中明确选择当前支持的机位")
					}
				}
				s.ProductionNotes = out.ProductionNotes
				s.ResolvedAngles = out.Angles
			}
			if out.Conflict != "" {
				s.AddWarning("F03", "content", out.Conflict)
			}
			if err := avatar.ValidateSegments(spoken, out.Segments); err != nil {
				return err
			}
			s.Segments = out.Segments
			s.Script = spoken
			return nil
		}); err != nil {
			return err
		}
		return w.await(s, "script")
	}
	var audioError error
	for i := range s.Segments {
		segment := &s.Segments[i]
		err := w.step(ctx, s, "F04/"+segment.ID, w.audioFingerprint(s, *segment), func(ctx context.Context, step *avatar.WorkflowStep) error {
			return w.produceAudio(ctx, s, segment, step)
		})
		if err != nil {
			if errors.Is(err, errWorkflowStopped) {
				return err
			}
			if audioError == nil {
				audioError = err
			}
		}
	}
	if audioError != nil {
		return audioError
	}
	offset := 0.0
	for i := range s.Segments {
		s.Segments[i].Start = offset
		offset += s.Segments[i].Duration
	}
	s.Duration = offset
	if offset <= 0 || offset > 90.05 {
		return fmt.Errorf("实测配音超过90秒，请缩短文案后新建运行；不会截断原稿")
	}
	if !s.AudioConfirmed {
		return w.await(s, "audio")
	}
	if err := w.step(ctx, s, "F05", []any{s.Segments, s.Input.Style, s.Input.Ratio, s.Input.Direction, s.Input.Angles}, func(ctx context.Context, step *avatar.WorkflowStep) error {
		productionInput := s.Input
		if len(s.ProductionNotes) > 0 {
			productionInput.Direction += "\n" + strings.Join(s.ProductionNotes, "\n")
			if len(productionInput.Angles) == 0 {
				productionInput.Angles = s.ResolvedAngles
			}
		}
		if len(s.Needs) > 0 {
			w.reuseStableNeeds(ctx, s)
			step.CostStatus = "reused"
			if err := avatar.BindSourceDescriptions(s.Input, s.Descriptions, s.Needs); err != nil {
				return err
			}
			return avatar.ValidateNeeds(productionInput, s.Segments, s.Needs)
		}
		var out struct {
			Needs []avatar.MaterialNeed `json:"needs"`
		}
		prompt := `按已锁定配音时长列出素材需求，不拼接、不组合、不确定最终切镜。输出 {"needs":[{"id":"n1","segment_id":"p1","kind":"avatar|animation|real","duration":实际秒,"angle":"正面","content":"素材内容","template":"steps|compare|relationship|highlight|summary|data","items":["本段原文中的精简要点，最多4项"],"prompt":"生成要求","source_id":"上传文件ID","source_start":0,"description_id":"对应片段文案ID","query":"后续检索建议"}]}。不需要的字段省略。每段至少一种实际可制作素材。使用数字人时每段安排覆盖整段声音的人物需求；有解释内容安排动画，时长不得超过本段。未明确多角度只允许正面；多角度仅用用户指定清单，且全片必须覆盖每一个指定机位；可以把不同机位安排在不同段落。同一段落同一机位只列一条人物需求，禁止重复生成。无上传文件禁止real，不把query当已找到素材。真实视频必须选择descriptions中的相关片段，填写description_id和source_id，source_start与duration的区间必须完全在该片段范围内；content引用对应解说的内容。`
		if style, found := findWorkflowStyle(w.app.avatarMVP.options.RuntimeRoot, s.Input.Style); found && !style.Legacy {
			prompt += " 用户已选择动效风格，至少安排一段animation。直接复用所选mg-styles-15原始模板的源码、固定图形、材质、动画节奏及运镜，禁止另画简化图解；template仅记录内容语义标签，不能替换原始模板。需求说明本段文字和适配时长，固定示例图形不代表事实证据。原始模板实现：" + style.Signature + "。人物是否进入画中画由后续分镜决定，不强制全程在场。不编造数据；不要把原片品牌、台词或不相关人物写入需求。"
		}
		if err := w.model(ctx, s, step, prompt, map[string]any{"segments": s.Segments, "style": s.Input.Style, "ratio": s.Input.Ratio, "use_avatar": s.Input.UseAvatar, "angles": productionInput.Angles, "direction": productionInput.Direction, "descriptions": s.Descriptions, "assets": s.Input.Assets}, nil, &out); err != nil {
			return err
		}
		if err := avatar.ValidateNeeds(productionInput, s.Segments, out.Needs); err != nil {
			return err
		}
		if err := avatar.BindSourceDescriptions(s.Input, s.Descriptions, out.Needs); err != nil {
			return err
		}
		s.Needs = out.Needs
		canonicalizeWorkflowNeeds(s.Needs)
		w.reuseStableNeeds(ctx, s)
		if err := avatar.ValidateNeeds(productionInput, s.Segments, s.Needs); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	var materialErr error
	for _, kind := range []string{"avatar", "animation", "real"} {
		node := map[string]string{"avatar": "F06", "animation": "F07", "real": "F08"}[kind]
		for _, need := range s.Needs {
			if need.Kind != kind {
				continue
			}
			err := w.produceMaterial(ctx, s, need, node)
			if err != nil {
				if errors.Is(err, errWorkflowStopped) {
					return err
				}
				if materialErr == nil {
					materialErr = err
				}
			}
		}
	}
	if materialErr != nil {
		return materialErr
	}
	if err := w.step(ctx, s, "F09", []any{s.Segments, s.Materials, s.Input.SubtitleEdits}, func(ctx context.Context, step *avatar.WorkflowStep) error {
		return w.mediaStep(ctx, s, "assemble", nil, nil)
	}); err != nil {
		return err
	}
	designInput := []any{s.Materials, s.Segments, s.Input.Style, s.Input.Ratio}
	// Historical orders retain their original input hash and provider identity.
	if version := s.ConfigSnapshot["presentation_version"]; version != "" {
		designInput = append([]any{version}, designInput...)
	}
	if err := w.step(ctx, s, "F10", designInput, func(ctx context.Context, step *avatar.WorkflowStep) error {
		if len(s.Shots) > 0 {
			avatar.EnrichShotPresentation(s.Shots, s.Segments, s.Materials, s.Needs)
			avatar.MapAvatarShotSources(s.Shots, s.Segments, s.Materials)
			if err := avatar.ValidateWorkflowShots(s.Shots, s.Segments, s.Materials, s.Duration); err == nil {
				step.CostStatus = "reused"
				return w.mediaStep(ctx, s, "shotbook", nil, nil)
			} else if len(s.Edits.Shots) > 0 {
				return err
			}
			s.Shots = nil
		}
		var out struct {
			Shots []avatar.WorkflowShot `json:"shots"`
		}
		var design struct {
			Plan map[string]any `json:"plan"`
		}
		if err := w.mediaStep(ctx, s, "plan", map[string]any{"candidates": avatar.PresentationCandidates(s.Segments, s.Materials, s.Needs)}, &design); err != nil {
			return err
		}
		prompt := `以已有素材和实测配音做talkcraft内容驱动分镜，先依据design的语义和可渲染候选安排全片节奏，再输出 {"shots":[{"id":"S1","segment_id":"p1","start":全局秒,"end":全局秒,"primary":"素材need_id","overlay":"可选素材need_id","recipe":"host-hero|host-explain|visual-hero|visual-pip|compare|steps|evidence|summary","keyword":"本段原文中的短标题","items":["原文短语"],"card":"none|keyword|definition|steps|compare|data|summary|impact-open-title","layout":"full|pip|split","primary_in":源起点秒,"overlay_in":叠加源起点秒,"semantic":["钩子|定义|机制|步骤|对比|数据|金句|章节|论点"],"intent":"观众此时重点看什么，为什么采用该构图","selection_reason":"素材与台词的关联"}]}。
配方：host-hero=人物主讲/不强制信息卡；host-explain=人物+侧边定义或要点；visual-hero=动画/真实画面主导、人物可离场；visual-pip=动画/实拍主画面+人物小窗；compare=人物+对比信息；steps=人物+步骤图；evidence=人物+真实数据摘录或真实证据主导；summary=人物+精简收束。人物主讲配方primary必须avatar。visual-pip的primary为animation或real，overlay为avatar；没有两份真实可用素材不得选pip。动画已经拥有图形与文字，优先visual-hero或visual-pip且card=none，禁止再套标题卡或步骤卡。items最多4项，只能摘录原文，不编造数据，不重复整段字幕。
每段至少一镜，时间轴从0连续覆盖全片，无重叠缺口。primary/overlay必须属于本段，不越段；人物源入点=镜头开始-本段开始；每个素材源入点+镜头时长不得超过真实duration。只用现有素材，不增人物角度，不改声音/时长，不循环/变速。按语义选择配方，不按段落机械交替人物与pip；有理由可保持长镜，不为凑配方切镜。上游108卡候选仅供语义参考，不能输出未实现组件。`
		if err := w.model(ctx, s, step, prompt, map[string]any{"segments": s.Segments, "materials": s.Materials, "needs": s.Needs, "duration": s.Duration, "style": s.Input.Style, "design": design.Plan}, nil, &out); err != nil {
			return err
		}
		avatar.MapAvatarShotSources(out.Shots, s.Segments, s.Materials)
		if avatar.NormalizeShotPresentation(out.Shots) {
			s.AddWarning("F10", "quality", "模型给出的组件/布局不在可用清单，已套用现有强调组件并继续；请检查设计效果")
		}
		avatar.EnrichShotPresentation(out.Shots, s.Segments, s.Materials, s.Needs)
		for _, message := range avatar.PresentationWarnings(out.Shots) {
			s.AddWarning("F10", "quality", message)
		}
		if err := avatar.ValidateWorkflowShots(out.Shots, s.Segments, s.Materials, s.Duration); err != nil {
			return err
		}
		s.Shots = out.Shots
		return w.mediaStep(ctx, s, "shotbook", nil, nil)
	}); err != nil {
		return err
	}
	if err := w.step(ctx, s, "F11", []any{avatar.PresentationVersion, s.Shots, s.Input.Style, s.Input.Ratio, s.Input.SubtitleColor, s.Input.SubtitleScale, s.Input.SubtitleEdits, s.Input.AvatarPosition}, func(ctx context.Context, step *avatar.WorkflowStep) error {
		if err := w.mediaStep(ctx, s, "shotbook", nil, nil); err != nil {
			return err
		}
		var result struct {
			Artifacts []avatar.WorkflowArtifact `json:"artifacts"`
		}
		if err := w.mediaStep(ctx, s, "preview", nil, &result); err != nil {
			return err
		}
		w.addArtifacts(s, result.Artifacts)
		return nil
	}); err != nil {
		return err
	}
	return w.step(ctx, s, "F12", []any{avatar.PresentationVersion, s.Shots, s.Input.Style, s.Input.Ratio, s.Input.SubtitleColor, s.Input.SubtitleScale, s.Input.SubtitleEdits, s.Input.AvatarPosition}, func(ctx context.Context, step *avatar.WorkflowStep) error {
		var result struct {
			Artifacts []avatar.WorkflowArtifact `json:"artifacts"`
		}
		if err := w.mediaStep(ctx, s, "render", nil, &result); err != nil {
			return err
		}
		w.addArtifacts(s, result.Artifacts)
		s.AddWarning("F12", "review", "制作已完成，请检查身份、声音、嘴型、皮肤纹理及画面衔接；未审片不影响下载")
		w.addArtifacts(s, []avatar.WorkflowArtifact{{ID: "report", Name: "成本耗时报告.json", Path: "report.json", MediaType: "application/json"}})
		return nil
	})
}
func (w *avatarWorkflowService) addArtifacts(s *avatar.WorkflowState, items []avatar.WorkflowArtifact) {
	for _, item := range items {
		found := false
		for i := range s.Artifacts {
			if s.Artifacts[i].ID == item.ID {
				s.Artifacts[i] = item
				found = true
			}
		}
		if !found {
			s.Artifacts = append(s.Artifacts, item)
		}
	}
}
func (w *avatarWorkflowService) speechClient() *avatar.RunningHub {
	base := os.Getenv("RUNNINGHUB_BASE_URL")
	if base == "" {
		base = "https://www.runninghub.cn"
	}
	key := os.Getenv("RUNNINGHUB_API_KEY")
	if key == "" && w.app.avatarMVP.options.Protocol == "runninghub" {
		key = w.app.avatarMVP.options.APIKey
	}
	return avatar.NewRunningHub(base, key)
}
func (w *avatarWorkflowService) poll(ctx context.Context, s *avatar.WorkflowState, step *avatar.WorkflowStep, query func(context.Context, string) (avatar.ProviderResult, error)) (avatar.ProviderResult, error) {
	interval := w.app.avatarMVP.options.PollInterval
	if interval < time.Millisecond {
		interval = 5 * time.Second
	}
	for {
		current, err := w.load(ctx, uuid.MustParse(w.ownerID(s)), s.ID)
		if err != nil {
			return avatar.ProviderResult{}, err
		}
		if current.StopRequested {
			return avatar.ProviderResult{}, errWorkflowStopped
		}
		result, err := query(ctx, step.ProviderTaskID)
		if err != nil && result.Status == "SUCCESS" {
			step.Usage = result.Usage
			_ = w.save(s)
			return result, fmt.Errorf("供应商订单已完成但返回产物异常，已保留原订单；恢复时仅查单")
		}
		if err == nil {
			if len(result.Usage) > 0 {
				step.Usage = result.Usage
				var usage map[string]any
				_ = json.Unmarshal(result.Usage, &usage)
				for _, name := range []string{"thirdPartyConsumeMoney", "consumeMoney", "consumeCoins"} {
					if usage[name] != nil {
						step.CostStatus = "reported"
					}
				}
			}
			if err = w.save(s); err != nil {
				return result, err
			}
			switch result.Status {
			case "SUCCESS":
				return result, nil
			case "FAILED", "FAILURE", "CANCELLED", "CANCELED":
				step.Submission = "terminal_failed"
				return result, fmt.Errorf("模型后台任务失败，原订单已保留；可手动重试")
			}
		}
		if err != nil {
			s.AddWarning(step.ID, "provider_notice", "模型查询暂时失败，仍查询原订单，不重复提交")
			_ = w.save(s)
		}
		select {
		case <-ctx.Done():
			return avatar.ProviderResult{}, fmt.Errorf("模型查询超时/中断；已保留原订单，恢复时继续查询")
		case <-time.After(interval):
		}
	}
}
func (w *avatarWorkflowService) produceAudio(ctx context.Context, s *avatar.WorkflowState, seg *avatar.Segment, step *avatar.WorkflowStep) error {
	fingerprint := w.audioFingerprint(s, *seg)
	relative := seg.ID + "/audio.wav"
	dir := filepath.Join(w.dir(s.ID), seg.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	cache := filepath.Join(w.projectDir(s.ProjectID), "cache", "audio", fingerprint)
	voicePath := filepath.Join(dir, "audio.wav")
	if _, err := os.Stat(voicePath); os.IsNotExist(err) {
		if _, err := os.Stat(filepath.Join(cache, "audio.wav")); err == nil {
			if err = copyWorkflowFile(filepath.Join(cache, "audio.wav"), voicePath); err != nil {
				return err
			}
			step.CostStatus = "reused"
		} else {
			if step.Submission == "terminal_failed" {
				step.PreserveOrder()
				step.ProviderTaskID = ""
				step.Submission = ""
			}
			if step.ProviderTaskID == "" {
				if step.Submission == "submitting" || step.Submission == "unknown" {
					return fmt.Errorf("配音提交状态待确认，禁止重复付费")
				}
				if err := w.beforeSubmit(s, step.ID); err != nil {
					return err
				}
				step.ErrorType = "provider"
				step.CostStatus = "unknown"
				if w.app.cfg.TTSModel == "doubao-seed-tts-2.0" {
					result, err := w.speechClient().SubmitSpeech(ctx, avatar.SpeechRequest{Text: seg.Text, Model: w.app.cfg.TTSModel, Speaker: s.Input.Voice, Language: w.app.cfg.TTSLanguage, Format: "wav"})
					if err != nil {
						step.Submission = "rejected"
						if avatar.SubmissionUncertain(err) {
							step.Submission = "unknown"
						}
						_ = w.save(s)
						return fmt.Errorf("配音模型调用失败；提交状态已记录")
					}
					step.ProviderTaskID = result.TaskID
					step.Submission = "submitted"
					if err = w.save(s); err != nil {
						return err
					}
				} else {
					audio, _, err := w.app.dashscope.DubbingTextToSpeech(ctx, seg.Text, w.app.cfg.QwenTTSModel+":"+s.Input.Voice, "", 1)
					if err != nil {
						step.Submission = "unknown"
						_ = w.save(s)
						return fmt.Errorf("配音调用未取得可恢复结果，请先核对供应商用量")
					}
					if err = os.WriteFile(voicePath, audio, 0600); err != nil {
						return err
					}
					step.Submission = "completed"
					if err = w.save(s); err != nil {
						return err
					}
				}
			}
			if step.ProviderTaskID != "" {
				client := w.speechClient()
				result, err := w.poll(ctx, s, step, client.QuerySpeech)
				if err != nil {
					return err
				}
				if result.AudioURL() == "" {
					return fmt.Errorf("配音订单成功但没有声音文件")
				}
				if err = client.Download(ctx, result.AudioURL(), voicePath); err != nil {
					return fmt.Errorf("配音结果下载失败，原订单已保留")
				}
			}
		}
	}
	seg.Audio = relative
	seg.Fingerprint = fingerprint
	var result struct {
		Duration float64 `json:"duration"`
	}
	if err := w.mediaStep(ctx, s, "audio", map[string]any{"segment": seg}, &result); err != nil {
		return err
	}
	seg.Duration = result.Duration
	if seg.Duration <= 0 {
		return fmt.Errorf("配音文件没有可测时长")
	}
	os.MkdirAll(cache, 0700)
	_ = copyWorkflowFile(voicePath, filepath.Join(cache, "audio.wav"))
	if _, err := os.Stat(w.voiceSamplePath(s.Input.Voice)); os.IsNotExist(err) {
		_ = copyWorkflowFile(voicePath, w.voiceSamplePath(s.Input.Voice))
	}
	w.addArtifacts(s, []avatar.WorkflowArtifact{{ID: "audio-" + seg.ID, Name: seg.ID + ".wav", Path: relative, MediaType: "audio/wav"}})
	return nil
}
func copyWorkflowFile(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(to), 0700); err != nil {
		return err
	}
	return os.WriteFile(to, b, 0600)
}
func (w *avatarWorkflowService) produceMaterial(ctx context.Context, s *avatar.WorkflowState, need avatar.MaterialNeed, node string) error {
	var seg avatar.Segment
	for _, p := range s.Segments {
		if p.ID == need.SegmentID {
			seg = p
		}
	}
	fingerprint := avatar.Digest([]any{need, seg.Fingerprint, s.Input.Style, s.Input.Ratio, w.app.avatarMVP.options.VideoConfigID(), s.Input.Photo})
	if need.Kind == "animation" {
		sources := map[string]string{}
		for _, name := range []string{"src/workflow-animation.tsx", "src/semantic-graphics.tsx", "src/film-design.ts", "src/animation-layout.ts", "workflow-render.mjs", "workflow.py", "mg-native.mjs", "mg-shell.js", "native-styles.json", "templates/" + s.Input.Style + "/SOURCE.json"} {
			data, err := os.ReadFile(filepath.Join(w.app.avatarMVP.options.RuntimeRoot, filepath.FromSlash(name)))
			if err != nil {
				sources[name] = "missing"
				continue
			}
			sources[name] = avatar.Digest(string(data))
		}
		fingerprint = avatar.Digest([]any{need, seg.Fingerprint, s.Input.Style, s.Input.Ratio, sources})
	}
	// Style changes must not invalidate talking footage.
	if need.Kind == "avatar" {
		fingerprint = avatar.Digest([]any{need, seg.Fingerprint, w.app.avatarMVP.options.VideoConfigID(), s.Input.Photo})
	}
	return w.step(ctx, s, node+"/"+need.ID, fingerprint, func(ctx context.Context, step *avatar.WorkflowStep) error {
		relative := "materials/" + need.ID + ".mp4"
		cache := filepath.Join(w.projectDir(s.ProjectID), "cache", "materials", fingerprint+".mp4")
		dest := filepath.Join(w.dir(s.ID), filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return err
		}
		var material avatar.Material
		if _, err := os.Stat(cache); err == nil {
			if err = copyWorkflowFile(cache, dest); err != nil {
				return err
			}
			step.CostStatus = "reused"
			material = avatar.Material{NeedID: need.ID, SegmentID: need.SegmentID, Kind: need.Kind, Path: relative, Duration: need.Duration, Fingerprint: fingerprint, SourceID: need.SourceID, SourceStart: need.SourceStart}
			var cached avatar.Material
			if data, e := os.ReadFile(cache + ".json"); e == nil && json.Unmarshal(data, &cached) == nil && cached.Fingerprint == fingerprint {
				material.FaceZone = cached.FaceZone
			}
			if s.ReusedFrom != "" {
				if prior, err := w.load(ctx, uuid.MustParse(w.ownerID(s)), s.ReusedFrom); err == nil {
					replayWorkflowMaterialWarnings(s, prior, material)
				}
			}
		} else if need.Kind == "avatar" {
			// Split on actual aligned word gaps; each suborder is separately persisted.
			var pieces struct {
				Pieces []struct {
					ID       string  `json:"id"`
					Start    float64 `json:"start"`
					Duration float64 `json:"duration"`
					Audio    string  `json:"audio"`
					Text     string  `json:"text"`
				} `json:"pieces"`
			}
			if err := w.mediaStep(ctx, s, "pieces", map[string]any{"segment": seg, "need": need}, &pieces); err != nil {
				return err
			}
			var allError error
			for _, piece := range pieces.Pieces {
				piece := piece
				err := w.step(ctx, s, node+"/"+need.ID+"-"+piece.ID, []any{fingerprint, piece}, func(ctx context.Context, order *avatar.WorkflowStep) error {
					out := filepath.Join(w.dir(s.ID), "pieces", need.ID+"-"+piece.ID+".mp4")
					if _, err := os.Stat(out); err == nil {
						return nil
					}
					if order.Submission == "terminal_failed" {
						order.PreserveOrder()
						order.ProviderTaskID = ""
						order.Submission = ""
					}
					if order.ProviderTaskID == "" {
						if order.Submission == "submitting" || order.Submission == "unknown" {
							return fmt.Errorf("人物提交状态待确认，禁止重复生成")
						}
						var photo string
						for _, a := range s.Input.Assets {
							if a.ID == s.Input.Photo {
								photo = filepath.Join(w.projectDir(s.ProjectID), filepath.FromSlash(a.Path))
							}
						}
						if photo == "" {
							return fmt.Errorf("人物照片丢失")
						}
						provider := w.app.avatarMVP.provider
						if w.app.avatarMVP.providerErr != nil {
							return fmt.Errorf("人物视频服务配置错误")
						}
						imageURL, err := provider.Upload(ctx, photo)
						if err != nil {
							return fmt.Errorf("人物图片上传失败，尚未提交生成")
						}
						audioURL, err := provider.Upload(ctx, filepath.Join(w.dir(s.ID), filepath.FromSlash(piece.Audio)))
						if err != nil {
							return fmt.Errorf("驱动声音上传失败，尚未提交生成")
						}
						duration, err := w.app.avatarMVP.options.GenerationDuration(int(math.Ceil(piece.Duration)))
						if err != nil {
							return err
						}
						if err = w.beforeSubmit(s, order.ID); err != nil {
							return err
						}
						order.ErrorType = "provider"
						order.CostStatus = "unknown"
						angle := need.Angle
						if angle == "" {
							angle = "正面"
						}
						text := piece.Text
						if text == "" {
							text = seg.Text
						}
						prompt := avatar.GenerationPrompt(avatar.Shot{Text: text, Angle: angle, Framing: "上半身，头肩和嘴部完整清楚", Prompt: "保持参考人物年龄、原有肤色和识别特征。半哑光皮肤，鼻翼和邻近脸颊保留细微局部毛孔及受光细绒毛；额头、鼻梁和鼻尖仅有克制的局部反光，脸颊和眼下较哑光。遵循参考照片现有灯光，不增加斑点或衰老；肤色和细微纹理跨帧稳定，高光随动作连续变化，不出现塑料感、蜡感、过度磨皮或均匀油膜。" + need.Prompt}) + fmt.Sprintf(" 音频从第0秒说话，时长%.3f秒；多余尾部闭嘴静默，不延长配音。", piece.Duration)
						result, err := provider.Submit(ctx, avatar.GenerationRequest{Model: w.app.avatarMVP.options.Model, Prompt: prompt, ImageURLs: []string{imageURL}, AudioURLs: []string{audioURL}, Duration: strconv.Itoa(duration), Ratio: s.Input.Ratio, Resolution: w.app.avatarMVP.options.Resolution, GenerateAudio: true, RealPersonMode: true, OutputFormat: "mp4"})
						if err != nil {
							order.Submission = "rejected"
							if avatar.SubmissionUncertain(err) {
								order.Submission = "unknown"
							}
							_ = w.save(s)
							return fmt.Errorf("视频模型提交失败，订单状态已记录，请检查服务商状态")
						}
						order.ProviderTaskID = result.TaskID
						order.Submission = "submitted"
						if err = w.save(s); err != nil {
							return err
						}
					}
					result, err := w.poll(ctx, s, order, w.app.avatarMVP.provider.Query)
					if err != nil {
						return err
					}
					if result.VideoURL() == "" {
						return fmt.Errorf("模型没有返回实际视频")
					}
					if err = os.MkdirAll(filepath.Dir(out), 0700); err != nil {
						return err
					}
					if err = w.app.avatarMVP.provider.Download(ctx, result.VideoURL(), out); err != nil {
						return fmt.Errorf("视频下载失败，恢复时继续查原单")
					}
					return nil
				})
				if err != nil {
					if errors.Is(err, errWorkflowStopped) {
						return err
					}
					if allError == nil {
						allError = err
					}
				}
			}
			if allError != nil {
				return allError
			}
			if err := w.mediaStep(ctx, s, "avatar", map[string]any{"segment": seg, "need": need, "pieces": pieces.Pieces}, &material); err != nil {
				return err
			}
		} else {
			if err := w.mediaStep(ctx, s, "material", map[string]any{"segment": seg, "need": need}, &material); err != nil {
				return err
			}
		}
		material.Fingerprint = fingerprint
		material.DescriptionID = need.DescriptionID
		found := false
		for i := range s.Materials {
			if s.Materials[i].NeedID == need.ID {
				s.Materials[i] = material
				found = true
			}
		}
		if !found {
			s.Materials = append(s.Materials, material)
		}
		os.MkdirAll(filepath.Dir(cache), 0700)
		_ = copyWorkflowFile(dest, cache)
		_ = avatar.WriteJSON(cache+".json", material)
		w.addArtifacts(s, []avatar.WorkflowArtifact{{ID: "material-" + need.ID, Name: need.ID + ".mp4", Path: relative, MediaType: "video/mp4"}})
		return nil
	})
}
