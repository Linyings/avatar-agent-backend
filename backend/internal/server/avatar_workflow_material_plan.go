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

// Bound model output by paragraph count, independently of final video duration.
// Each completed batch is durable; an explicit retry resumes at the failed batch.
func (w *avatarWorkflowService) planMaterialNeeds(ctx context.Context, s *avatar.WorkflowState, parent *avatar.WorkflowStep, prompt string, input avatar.WorkflowInput) ([]avatar.MaterialNeed, error) {
	const batchSize = 2
	prompt += " 本次只规划 segments 中的段落，不重复其他段落。content 简述素材含义（不超过80字），prompt 只写制作要点（不超过300字），不要复述整段导演稿；items 最多4项。不输出解释、Markdown、代码或无关字段。required_angles 是本批必须覆盖的用户机位，其余机位可按内容选择。"
	var all []avatar.MaterialNeed
	for start := 0; start < len(s.Segments); start += batchSize {
		end := min(start+batchSize, len(s.Segments))
		segments := s.Segments[start:end]
		var director *avatar.DirectorPlan
		if s.Director != nil {
			director = &avatar.DirectorPlan{Summary: s.Director.Summary, Timing: s.Director.Timing}
			for _, scene := range s.Director.Scenes {
				if len(scene.SegmentIDs) == 0 || slices.ContainsFunc(segments, func(seg avatar.Segment) bool { return slices.Contains(scene.SegmentIDs, seg.ID) }) {
					director.Scenes = append(director.Scenes, scene)
				}
			}
		}
		var requiredAngles []string
		if end == len(s.Segments) {
			for _, angle := range input.Angles {
				if !slices.ContainsFunc(all, func(n avatar.MaterialNeed) bool { return n.Kind == "avatar" && n.Angle == angle }) {
					requiredAngles = append(requiredAngles, angle)
				}
			}
		}
		data := map[string]any{"segments": segments, "style": input.Style, "ratio": input.Ratio, "use_avatar": input.UseAvatar, "angles": input.Angles, "required_angles": requiredAngles, "direction": input.Direction, "descriptions": s.Descriptions, "assets": input.Assets, "director": director, "batch_index": start/batchSize + 1, "batch_count": (len(s.Segments) + batchSize - 1) / batchSize}
		data["selected_style"] = workflowStyleProductionContext(w.app.avatarMVP.options.RuntimeRoot, input.Style)
		var out struct {
			Needs []avatar.MaterialNeed `json:"needs"`
		}
		if len(s.Segments) <= batchSize {
			if err := w.model(ctx, s, parent, prompt, data, nil, &out); err != nil {
				return nil, err
			}
			return avatar.NativeMaterialNeeds(input, segments, out.Needs), nil
		}
		fingerprint := []any{"material-plan-batches/v1", prompt, data, s.ConfigID}
		cache := filepath.Join(w.dir(s.ID), "material-plans", avatar.Digest(fingerprint)+".json")
		validate := func() error {
			out.Needs = avatar.NativeMaterialNeeds(input, segments, out.Needs)
			// Global animation and angle coverage are checked after merging. Local
			// validation still rejects foreign segments, gaps and invalid durations.
			partial := input
			partial.Angles = nil
			partial.Direction = ""
			if partial.UseAnimation != nil && *partial.UseAnimation {
				partial.UseAnimation = nil
			}
			// Keep the angle allowlist without requiring every angle in every batch.
			for _, need := range out.Needs {
				if need.Kind == "avatar" && len(input.Angles) > 0 && input.Direction != "" {
					if !slices.Contains(input.Angles, need.Angle) {
						return fmt.Errorf("素材规划包含未授权机位")
					}
				}
			}
			if len(input.Angles) > 0 && input.Direction != "" {
				partial.Direction = input.Direction
				partial.Angles = append([]string(nil), requiredAngles...)
				for _, n := range out.Needs {
					if n.Kind == "avatar" && !slices.Contains(partial.Angles, n.Angle) {
						partial.Angles = append(partial.Angles, n.Angle)
					}
				}
			}
			if err := avatar.ValidateNeeds(partial, segments, out.Needs); err != nil {
				return err
			}
			if end == len(s.Segments) && input.UseAnimation != nil && *input.UseAnimation {
				has := func(kind string) bool {
					matches := func(n avatar.MaterialNeed) bool { return n.Kind == kind }
					return slices.ContainsFunc(all, matches) || slices.ContainsFunc(out.Needs, matches)
				}
				if !has("animation") {
					return fmt.Errorf("已启用动效，但素材规划遗漏动效画面")
				}
				if input.UseAvatar && !has("avatar") {
					return fmt.Errorf("已启用人物与动效融合，但素材规划遗漏人物口播")
				}
			}
			return avatar.BindSourceDescriptions(input, s.Descriptions, out.Needs)
		}
		err := w.step(ctx, s, "F05/plan-"+segments[0].ID, fingerprint, func(ctx context.Context, step *avatar.WorkflowStep) error {
			if raw, err := os.ReadFile(cache); err == nil && json.Unmarshal(raw, &out) == nil && validate() == nil {
				step.CostStatus = "reused"
				return nil
			}
			if err := w.model(ctx, s, step, prompt, data, nil, &out); err != nil {
				return err
			}
			if err := validate(); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
				return err
			}
			return avatar.WriteJSON(cache, out)
		})
		if err != nil {
			return nil, fmt.Errorf("素材规划第 %d/%d 批失败：%w", start/batchSize+1, (len(s.Segments)+batchSize-1)/batchSize, err)
		}
		// w.step skips a successful batch on retry; load its validated result.
		raw, err := os.ReadFile(cache)
		if err != nil || json.Unmarshal(raw, &out) != nil {
			return nil, fmt.Errorf("已完成素材规划的本地结果不可用")
		}
		if err := validate(); err != nil {
			return nil, err
		}
		for _, need := range out.Needs {
			need.ID = fmt.Sprintf("n%d", len(all)+1)
			all = append(all, need)
		}
	}
	return all, avatar.ValidateNeeds(input, s.Segments, all)
}

func (w *avatarWorkflowService) materialPlanningPrompt(s *avatar.WorkflowState) string {
	prompt := `按已锁定配音时长列出素材需求，不拼接、不组合、不确定最终切镜。输出 {"needs":[{"id":"n1","segment_id":"p1","kind":"avatar|animation|real","duration":实际秒,"angle":"正面","content":"素材内容","template":"steps|compare|relationship|highlight|summary|data","items":["本段原文中的精简要点，最多4项"],"prompt":"生成要求","source_id":"上传文件ID","source_start":0,"description_id":"对应片段文案ID","query":"后续检索建议"}]}。不需要的字段省略。每段至少一种实际可制作素材。使用数字人时每段安排覆盖整段声音的人物需求；有解释内容安排动画，时长不得超过本段。未明确多角度只允许正面；多角度仅用用户指定清单，且全片必须覆盖每一个指定机位；可以把不同机位安排在不同段落。同一段落同一机位只列一条人物需求，禁止重复生成。无上传文件禁止real，不把query当已找到素材。真实视频必须选择descriptions中的相关片段，填写description_id和source_id，source_start与duration的区间必须完全在该片段范围内；content引用对应解说的内容。`
	if !avatar.AnimationEnabled(s.Input) {
		prompt += " 本项目只需要人物口播，不安排 animation。默认风格不是添加动效的授权；没有视频素材时只安排 avatar。"
	}
	prompt += " 每个 animation 必须提供 narration_quote：该素材负责的连续完整台词，逐字摘录 segment.text，不跨其他素材的台词；按完整语句和数据含义拆分，不拆开金额与单位。程序按已有字词时间戳确定实际起止和时长，不需要估算 narration_start。多条动画按台词顺序列出。"
	if avatar.DirectorWorkflow(s) {
		prompt += " 按 director 已设计的镜头目的、画面与动效制作素材，把 motion 和 visual 的具体设计写入对应 need.prompt；配音已实测，素材覆盖真实时长。保留已确认的台词与主要创意。"
	}
	if style, found := findWorkflowStyle(w.app.avatarMVP.options.RuntimeRoot, s.Input.Style); found && !style.Legacy && avatar.AnimationEnabled(s.Input) {
		prompt += " 遵循 selected_style.method 中的叙事、镜头和素材安排要求；素材时间来自实际原声。"
		prompt += " 用户已选择动效风格，至少安排一段animation。风格仅提供配色、材质与运动参考；必须根据本段文案重新设计内容、图形和构图，禁止复制演示视频、示例品牌、固定图形故事或原片台词。content写清楚本段要解释的含义，items摘自本段原文，template按含义选择，duration覆盖该段实际配音。动效制作节点会结合已经生成的人物画面单独设计场景，再由分镜安排分层融合。风格特征：" + fmt.Sprint(style.Design["features"]) + "。不编造数据或证据。"
	}
	prompt += " 人物 avatar 的 prompt 只描述干净普通矩形口播素材的外观、背景、固定机位和自然表演，完整保留头顶与肩部，固定焦距和拍摄距离，不推进、不拉近、不变焦；不能要求人物生成模型制作圆窗、圆角、分屏、字幕、边框、重复小人像或其他合成图层。导演稿的窗口形状、方位与叠加动效只留给后期镜头规划和渲染。"
	if avatar.NativeSpeech(s.Input) {
		prompt += " 本项目人物已在 F04 由 Seedance 以绿幕背景同时生成画面和原声。F06 会自动对人物视频做绿幕抠像，输出带 alpha 通道的透明人物素材。动效（animation）素材作为人物背景层，在分镜合成时人物叠在动效上方；animation 素材不需要为人物预留空间，全画幅铺满设计，由分镜决定两层的遮挡关系。根据各段真实时长规划 animation 和实拍素材。"
	}
	return prompt
}
