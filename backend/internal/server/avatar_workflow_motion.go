package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/narratoai/backend-go/internal/avatar"
)

const motionDesignPrompt = `你是当前口播的动效导演。仅把风格参考中的配色、材质、线条、运动语法用于当前内容，不能复制参考片的品牌、固定故事、人物、数字或成片。
观察传入的本段真实数字人画面，简述背景明暗、人物位置与配色，在 tone 选择 warm/cool/neutral 使动效与人物协调。人物素材由后续分镜分层合成，不在动效里重新绘制人物；保持图形完整、边缘留白并为字幕预留空间。没有人物画面时明确说明。
根据当前 segment.text 重新设计 1 到 5 个按口播顺序展开的场景。narration_window 指定此素材负责的台词区间；有 quote 时仅设计并完整覆盖该引用，没有 quote 时按窗口内 words 选择对应完整语句，不重复整段其他素材内容。用内容适配的图形表达信息，不把整段文字搬上画面。每场选 1 到 4 个短标签，数字和单位必须原样保留，不画虚构的趋势或比例。
输出 JSON：{"version":"content-motion/1","tone":"warm|cool|neutral","visual_context":"观察到的本段人物画面与搭配理由","scenes":[{"quote":"本场在当前段落中连续出现的原文，按顺序且不重叠","title":"quote中的主题短语，优先12字以内，最多24字符含英文","layout":"highlight|data|compare|steps|relationship|summary","elements":[{"label":"quote中的连续短语，最多60字","kind":"coin|chart|chip|cloud|people|document|shield|globe|clock|code|spark|box|leaf|link"}]}]}。
title 选简短的主体或主题，不复制完整句子或罗列多个数值；数值在 elements 中强调。配色判断以 style_profile.palette 为准，不要求风格配置之外的颜色。
quote 覆盖要表达的口播内容，title 和 label 必须逐字摘自同场 quote；icon 仅为示意，不是事实证据。kind 根据本段含义选取，layout 决定真实构图；不输出代码、素材URL、演示视频或模型估计的秒数。时间由已有配音字词对齐自动计算。优先使用简洁、可视化的实体和关系。`

func motionAvatarMaterials(s *avatar.WorkflowState, segment string) []avatar.Material {
	var out []avatar.Material
	for _, m := range s.Materials {
		if m.Kind == "avatar" && m.SegmentID == segment {
			out = append(out, m)
		}
	}
	return out
}

func (w *avatarWorkflowService) motionFingerprint(s *avatar.WorkflowState, need avatar.MaterialNeed, seg avatar.Segment) string {
	sources := map[string]string{}
	if avatar.DirectorWorkflow(s) {
		data, _ := os.ReadFile(filepath.Join(w.app.avatarMVP.options.RuntimeRoot, "director-motion.mjs"))
		var scenes []avatar.DirectorScene
		for _, scene := range motionDirectorScenes(s, seg) {
			scene.Narration = ""
			scene.SegmentIDs = nil
			scene.Start = 0
			scene.End = 0
			scenes = append(scenes, scene)
		}
		sources["director-code"] = avatar.Digest([]any{string(data), motionSceneCodePrompt, "motion-phases/2", scenes})
	}
	for _, name := range []string{"src/content-motion.tsx", "src/motion-objects.tsx", "src/workflow-animation.tsx", "content-motion.mjs", "motion-checks.mjs", "workflow-render.mjs", "workflow.py", "native-styles.json", "templates/" + s.Input.Style + "/original-prompt.md"} {
		data, err := os.ReadFile(filepath.Join(w.app.avatarMVP.options.RuntimeRoot, filepath.FromSlash(name)))
		if err != nil {
			sources[name] = "missing"
		} else {
			sources[name] = avatar.Digest(string(data))
		}
	}
	return avatar.Digest([]any{avatar.MotionPlanVersion, motionDesignPrompt, need, seg.Text, seg.Fingerprint, s.Input.Topic, s.Input.Style, s.Input.Ratio, s.Input.Direction, s.ProductionNotes, s.Input.Photo, motionAvatarMaterials(s, seg.ID), sources, workflowStyleProductionContext(w.app.avatarMVP.options.RuntimeRoot, s.Input.Style)})
}

func (w *avatarWorkflowService) planMotion(ctx context.Context, s *avatar.WorkflowState, step *avatar.WorkflowStep, need avatar.MaterialNeed, seg avatar.Segment, fingerprint string) (avatar.MotionPlan, error) {
	var plan avatar.MotionPlan
	style, ok := findWorkflowStyle(w.app.avatarMVP.options.RuntimeRoot, s.Input.Style)
	if ok && style.Legacy && avatar.DirectorWorkflow(s) {
		fallback := "mg-flat-vector"
		if s.Input.Style == "line-art" {
			fallback = "mg-line-art"
		}
		style, ok = findWorkflowStyle(w.app.avatarMVP.options.RuntimeRoot, fallback)
	}
	if !ok || len(style.Design) == 0 {
		return plan, fmt.Errorf("当前动效风格尚未配置内容渲染，请选择已发布的动效风格")
	}
	file := filepath.Join(w.dir(s.ID), "materials", need.ID+"-motion-plan.json")
	var cached struct {
		Fingerprint string            `json:"fingerprint"`
		Plan        avatar.MotionPlan `json:"plan"`
	}
	if data, err := os.ReadFile(file); err == nil && json.Unmarshal(data, &cached) == nil && cached.Fingerprint == fingerprint && avatar.ValidateMotionPlan(cached.Plan, seg.Text) == nil && !avatar.DirectorWorkflow(s) {
		return cached.Plan, nil
	}
	if s.ReusedFrom != "" && s.Edits.MaterialOnly != need.ID && !workflowRerunsNode(s, "F07") {
		if reused, ok := w.reusableMotionDesign(ctx, s, need, seg); ok {
			cached.Fingerprint, cached.Plan = fingerprint, reused
			if err := avatar.WriteJSON(file, cached); err != nil {
				return plan, err
			}
			step.ProductionPhase = "复用动效设计并重新校准时间"
			return reused, nil
		}
	}
	var contextResult struct {
		Frames    []string `json:"frames"`
		Source    string   `json:"source"`
		AvatarIDs []string `json:"avatar_ids"`
	}
	if err := w.mediaStep(ctx, s, "motion_context", map[string]any{"segment": seg, "need": need}, &contextResult); err != nil {
		return plan, err
	}
	if s.Input.UseAvatar && len(contextResult.Frames) == 0 {
		return plan, fmt.Errorf("本段人物素材尚未就绪，动效需读取实际人物画面后再制作")
	}
	// Extract style features, production method, and the complete original prompt
	// to give the model full context about the intended visual style and technique.
	styleReference := fmt.Sprint(style.Design["features"])
	if method, ok := workflowStyleProductionContext(w.app.avatarMVP.options.RuntimeRoot, s.Input.Style)["method"].(string); ok {
		styleReference += "\n\n制作方法：\n" + method
	}
	// Read the complete original prompt for full style context
	if data, err := os.ReadFile(filepath.Join(w.app.avatarMVP.options.RuntimeRoot, "templates", s.Input.Style, "original-prompt.md")); err == nil {
		text := string(data)
		// Extract the Prompt section which contains the original creative brief
		if start := strings.Index(text, "## Prompt"); start >= 0 {
			end := len(text)
			// Find the next section marker to bound the prompt
			if nextSection := strings.Index(text[start+10:], "\n## "); nextSection > 0 {
				end = start + 10 + nextSection
			}
			promptSection := strings.TrimSpace(text[start:end])
			// Remove the markdown code fence if present
			promptSection = strings.TrimPrefix(promptSection, "## Prompt\n\n```text\n")
			promptSection = strings.TrimSuffix(promptSection, "\n```")
			if len(promptSection) > 0 && len(promptSection) < 3000 {
				styleReference += "\n\n原始风格提示词（参考画面风格、UI布局和视觉处理手法，根据当前文案内容改编）：\n" + promptSection
			}
		}
	}
	// Bound the assembled reference. The template brief plus production method
	// can otherwise carry several KB into every design request for the segment.
	styleReference = truncateRunes(styleReference, designStyleReferenceBudget)
	direction := s.Input.Direction
	if len(s.ProductionNotes) > 0 {
		direction += "\n" + strings.Join(s.ProductionNotes, "\n")
	}
	input := map[string]any{"segment": seg, "narration_window": w.motionScope(s, need, seg), "topic": s.Input.Topic, "direction": direction, "style": s.Input.Style, "style_profile": style.Design, "style_reference": styleReference, "ratio": s.Input.Ratio, "visual_source": contextResult.Source, "avatar_ids": contextResult.AvatarIDs, "material_content": need.Content, "requested_layout": need.Template, "requested_items": need.Items}
	// All paths use two-phase generation (design → per-scene code) so each
	// model call has a bounded maximum output and is never truncated by a
	// token ceiling regardless of scene count or segment length.
	if avatar.DirectorWorkflow(s) {
		input["director"] = motionDirectorScenes(s, seg)
		input["material_design"] = need.Prompt
	}
	return w.planMotionPhases(ctx, s, step.ID, input, contextResult.Frames, need, seg, fingerprint, file)
}

const directorMotionPrompt = `
新版制作必须在同一个 JSON 增加 code 字符串字段，编写此镜头专用的 Canvas 2D 绘制函数体，不使用 Markdown。函数参数 ctx,t,w,h,scene,palette；t 为当前场景从0开始的秒数，scene 包含 title、quote、elements（label,kind,at）、start、end；palette 为风格颜色数组，0背景、1表面、2文字、3强调。按导演稿和当前口播含义绘制独特的结构、关系、运动与反馈，可用路径、贝塞尔曲线、渐变、圆角矩形和适当文字，避免整屏堆砌卡片。每帧由t确定性绘制；文字仅使用scene.title和scene.elements[].label，必要时可截取这些原文，不自行添加任何文字或数字。画布上下左右留白约8%，底部15%预留字幕。原理图为主要内容，数字人由合成器叠加，不重画人物。ctx是CanvasRenderingContext2D；仅使用其绘制方法、Math确定性函数、普通变量及有界for循环，最多100次迭代。严禁浏览器全局、DOM、网络、导入、文件、计时器、Date、随机数、while、构造函数或ctx.canvas。代码不得读取外部数据。使用内置字体 sans-serif，中文文字应清楚，正文字号不低于min(w,h)*0.038；动态用缓入缓出和分步出现解释信息，不能机械复制示例。代码控制在6000字符内，避免长篇注释和重复绘图代码；code必须是正确转义换行、反斜杠和双引号的JSON字符串。若导演稿安排圆形人物窗，为其所在角落预留直径约短边28%的空白，不能把标题或信息画在该区域。代码和场景一起输出，不能省略code。`
