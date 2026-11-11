package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/narratoai/backend-go/internal/avatar"
)

const shotBatchPrompt = `只规划当前 segment 的 window 时间区间，输出 {"shots":[{"start":段落内秒,"end":段落内秒,"primary":"素材need_id","overlay":"可选need_id","primary_in":素材源入点秒,"overlay_in":叠加源入点秒,"recipe":"host-hero|host-explain|visual-hero|visual-pip|visual-split|compare|steps|evidence|summary","keyword":"原文短标题","items":["原文短语"],"card":"none|keyword|definition|steps|compare|data|summary|impact-open-title","layout":"full|pip|split","semantic":["钩子|定义|机制|步骤|对比|数据|金句|章节|论点"],"intent":"构图意图","selection_reason":"素材与台词关联","avatar_shape":"circle|rounded|rectangle","avatar_position":"top-left|top-right|bottom-left|bottom-right"}]}。
必须只返回1到max_shots个镜头。start/end相对于本段起点，不是全片时间；从window.start连续覆盖到window.end，无重叠缺口。不要返回其他段落、源码、素材清单或导演全文。items和semantic各最多4项，keyword/items每项最多60字，intent和selection_reason各最多120字。可见文字必须摘录本段原文，不编造数据。尽量用1–2镜说明内容，不为凑数切镜。
primary/overlay只能取本段materials的need_id。源入点+镜头时长不得超过素材duration；人物源入点必须等于段落内start。不得循环、冻结、变速或改变锁定配音。host-hero/host-explain/compare/steps/summary主画面用人物；visual-hero以动画或真实画面全屏为主、不出现人物，只保留解说声；visual-pip以动画/真实画面全屏为primary，人物为角落小窗overlay；visual-split为左右分屏，动画/真实画面为primary占约70%画面，人物为overlay占约25%窄栏，layout=split。动画与人物同框时，动画必须是primary、人物必须是overlay，人物永远是较小的一层；没有两份可用素材不能用pip或split。不是每个镜头都需要人物：讲原理、流程、数据的内容优先visual-hero或visual-pip，人物全屏host-hero主要用于开场、观点转折和结尾。同一段内相邻镜头不要重复同一种构图，在visual-hero、visual-pip、visual-split、host-hero之间交替。动画已有图形和文字时card=none，避免重复叠卡。host-explain、compare、steps、evidence、summary分别对应解释、对比、步骤、证据、收束；按语义选择，允许保持长镜。
遵循direction、summary和当前director的构图要求，在有同段人物和动效素材时，在visual-hero（纯动效）、visual-pip（动效+人物小窗）、visual-split（动效为主+人物窄栏）之间按内容选择，并保留少量人物主讲镜头。人物小窗由后期工具对干净口播素材裁剪合成，窗口位置和大小在镜头内固定，不对人物额外推进、拉伸或缩放。人物小窗形状和方位遵循导演稿，避开字幕、人物面部和动效重点；没有实际跟踪遮罩不要声称完成附着或遮挡。动效带 narration_start 时只在该段落时间起点至 narration_start+duration 之间使用，源入点=镜头段内start-narration_start，不能把后面的数字提前或把前面的数字重复到后面。避免没有语义目的的不足1秒闪切；临近段落结尾应延续原镜头而不是插入极短人物画面。全片时间换算、镜头ID和最终合并由程序完成。`

func (w *avatarWorkflowService) planShotBatches(ctx context.Context, s *avatar.WorkflowState) ([]avatar.WorkflowShot, error) {
	batches, err := avatar.BuildShotPlanningBatches(s)
	if err != nil {
		return nil, err
	}
	ctx = avatar.WithShotPlanning(ctx)
	// Check every batch before creating any paid order.
	for _, b := range batches {
		if err := avatar.CheckContentInput(ctx, shotBatchPrompt, b); err != nil {
			return nil, err
		}
	}
	for i, b := range batches {
		id := fmt.Sprintf("F10/batch-%03d", i+1)
		if !slices.ContainsFunc(s.Steps, func(step avatar.WorkflowStep) bool { return step.ID == id }) {
			s.Steps = append(s.Steps, avatar.WorkflowStep{ID: id, Status: "pending", CostStatus: "local_compute", ProductionPhase: fmt.Sprintf("组装分段 %d/%d · %s %.1f–%.1f 秒", i+1, len(batches), b.Segment.ID, b.Window.Start, b.Window.End)})
		}
	}
	if err := w.save(s); err != nil {
		return nil, err
	}
	var all []avatar.WorkflowShot
	for i, b := range batches {
		id := fmt.Sprintf("F10/batch-%03d", i+1)
		shots, err := w.shotPlanBatch(ctx, s, id, fmt.Sprintf("组装分段 %d/%d · %s %.1f–%.1f 秒", i+1, len(batches), b.Segment.ID, b.Window.Start, b.Window.End), b)
		if err != nil {
			return nil, err
		}
		all = append(all, shots...)
	}
	avatar.EnrichShotPresentation(all, s.Segments, s.Materials, s.Needs)
	if err := avatar.ValidateWorkflowShots(all, s.Segments, s.Materials, s.Duration); err != nil {
		return nil, err
	}
	return all, nil
}

func (w *avatarWorkflowService) shotPlanBatch(ctx context.Context, s *avatar.WorkflowState, id, phase string, b avatar.ShotPlanningBatch) ([]avatar.WorkflowShot, error) {
	for _, step := range s.Steps {
		if step.ID == id && (step.Submission == "unknown" || step.Submission == "submitting") {
			return nil, &workflowStepError{StepID: id, Err: fmt.Errorf("当前分段的模型请求状态待确认，请先核对供应商记录；未重复提交")}
		}
	}
	key := []any{avatar.ShotPlanningVersion, b.SourceFingerprint, b, shotBatchPrompt, s.ConfigID, avatar.ContentMaxTokens(ctx)}
	hash := avatar.Digest(key)
	cache := filepath.Join(w.dir(s.ID), "shot-plans", hash+".json")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		return nil, err
	}
	var output struct {
		Shots []avatar.WorkflowShot `json:"shots"`
	}
	validate := func() error { _, err := avatar.NormalizeShotPlanningBatch(b, output.Shots); return err }
	load := func() bool {
		raw, err := os.ReadFile(cache)
		output.Shots = nil
		return err == nil && json.Unmarshal(raw, &output) == nil && validate() == nil
	}
	if !load() {
		for _, previous := range s.Steps {
			if previous.ID != id || previous.InputHash != hash || previous.Submission != "completed" {
				continue
			}
			file := filepath.Join(w.dir(s.ID), "model-results", fmt.Sprintf("%s-attempt-%d.json", avatar.Digest(id)[:16], previous.Attempt))
			if raw, err := os.ReadFile(file); err == nil && json.Unmarshal(raw, &output) == nil {
				if err := validate(); err == nil {
					if err := avatar.WriteJSON(cache, output); err != nil {
						return nil, err
					}
					break
				}
			}
			if previous.Status == "succeeded" || previous.Status == "running" {
				return nil, &workflowStepError{StepID: id, Err: fmt.Errorf("已完成分段的本地结果不可用，未重复提交模型请求")}
			}
		}
	}
	for i := range s.Steps {
		step := &s.Steps[i]
		if step.ID == id && (step.Status != "succeeded" || step.InputHash != hash) && step.Submission != "unknown" && step.Submission != "submitting" {
			step.PreserveOrder()
			step.Submission, step.Usage, step.CostStatus = "", nil, "local_compute"
		}
	}
	err := w.step(ctx, s, id, key, func(ctx context.Context, step *avatar.WorkflowStep) error {
		step.GenerationPrompt, step.ProductionPhase = shotBatchPrompt, phase
		step.PromptInput, _ = json.Marshal(b)
		if err := w.save(s); err != nil {
			return err
		}
		if load() {
			if err := validate(); err != nil {
				return err
			}
			step.CostStatus = "reused"
			return nil
		}
		if err := w.model(ctx, s, step, shotBatchPrompt, b, nil, &output); err != nil {
			return err
		}
		if err := validate(); err != nil {
			return err
		}
		return avatar.WriteJSON(cache, output)
	})
	if err != nil {
		return nil, err
	}
	if !load() {
		return nil, &workflowStepError{StepID: id, Err: fmt.Errorf("已完成分段的本地结果不可用，未重复提交模型请求")}
	}
	return avatar.NormalizeShotPlanningBatch(b, output.Shots)
}
