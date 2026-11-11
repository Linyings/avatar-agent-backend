package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/narratoai/backend-go/internal/avatar"
)

const motionSceneCodePrompt = `只为输入 scene 编写一个场景的 Canvas 2D 绘制函数体，输出 {"code":"..."}，不输出场景方案、其他镜头或解释。函数参数 ctx,t,w,h,scene,palette；t 为当前场景从0开始的秒数。scene.duration 是本场真实时长，不能套用整段或固定8秒/10秒时长。scene.start/end 和 elements.at 使用素材时间轴；elements.local_at 是本场台词时间，elements.enter_at 是预留阅读时间后的建议入场时刻；动画必须用 t 与 local_at/enter_at 比较，不能直接用 t 与 at 比较。全部关键图形及 elements 标签应在 duration-hold_seconds 前完成，最后保留 hold_seconds 秒稳定展示；短场景应缩短动作或同时展示，不得把内容安排到场景结束之后。palette 为0背景、1表面、2文字、3强调。根据当前导演镜头和素材要求绘制结构、关系与分步出现的图形。每帧只由t确定性绘制，使用Canvas绘图方法、Math确定性函数、普通变量和最多100次迭代的有界for循环。禁止DOM、浏览器全局、网络、导入、文件、计时器、Date、随机数、while、构造函数或ctx.canvas。文字只能使用scene.title和scene.elements[].label及它们的原文子串，不增加任何文字或数字；数值和单位保持完整，名称可分开排版，但必须在同一画面完整出现。用sans-serif字体，正文字号不低于min(w,h)*0.038，四周留白8%，底部15%留给字幕；人物由合成器叠加，若当前导演镜头有圆形人物窗，为该角落保留短边28%的空白。code不得超过code_max_bytes个UTF-8字节，使用简洁函数避免重复和长注释，JSON中正确转义换行、引号和反斜杠。`

func motionDirectorScenes(s *avatar.WorkflowState, seg avatar.Segment) []avatar.DirectorScene {
	var scenes []avatar.DirectorScene
	if s.Director != nil {
		for _, scene := range s.Director.Scenes {
			if scene.ID == seg.DirectorSceneID || slices.Contains(scene.SegmentIDs, seg.ID) {
				scenes = append(scenes, scene)
			}
		}
	}
	return scenes
}

// Some old videos were copied from the project cache without copying their
// design plan. Completed work in this immutable run remains authoritative;
// edits create a new run, and inherited steps have attempt 0.
func (w *avatarWorkflowService) legacyMotionVideoReady(s *avatar.WorkflowState, need avatar.MaterialNeed) bool {
	if !avatar.DirectorWorkflow(s) || need.Kind != "animation" || !slices.ContainsFunc(s.Needs, func(n avatar.MaterialNeed) bool { return avatar.Digest(n) == avatar.Digest(need) }) {
		return false
	}
	id := "F07/" + need.ID
	for _, step := range s.Steps {
		if step.ID != id || step.Status != "succeeded" || step.Attempt == 0 {
			continue
		}
		for _, m := range s.Materials {
			if m.NeedID != need.ID || m.Kind != need.Kind || m.SegmentID != need.SegmentID || m.Duration != need.Duration || m.Fingerprint == "" || step.InputHash != avatar.Digest(m.Fingerprint) {
				continue
			}
			if file, err := safeWorkflowFile(w.dir(s.ID), m.Path); err == nil {
				if info, err := os.Stat(file); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
					return true
				}
			}
		}
	}
	return false
}

// Same-run retries may reuse a validated pre-upgrade plan even when the local
// renderer changed. Compare its recorded input, and never migrate an inherited
// step into an unrelated production request. Explicit regeneration creates a
// new run with no local plan; retries inside that new run may reuse its own plan.
func (w *avatarWorkflowService) legacyMotionFingerprint(s *avatar.WorkflowState, need avatar.MaterialNeed, seg avatar.Segment) string {
	if !avatar.DirectorWorkflow(s) {
		return ""
	}
	var cached struct {
		Fingerprint string            `json:"fingerprint"`
		Plan        avatar.MotionPlan `json:"plan"`
	}
	raw, err := os.ReadFile(filepath.Join(w.dir(s.ID), "materials", need.ID+"-motion-plan.json"))
	if err != nil || json.Unmarshal(raw, &cached) != nil || cached.Fingerprint == "" || avatar.ValidateMotionPlan(cached.Plan, seg.Text) != nil || avatar.NormalizeMotionCode(&cached.Plan) != nil {
		return ""
	}
	for _, step := range s.Steps {
		if step.ID != "F07/"+need.ID || step.Attempt == 0 || step.InputHash != avatar.Digest(cached.Fingerprint) {
			continue
		}
		if step.GenerationPrompt == "" && len(step.PromptInput) == 0 {
			// Runs created before prompt recording still pin their generation to
			// this run ID and parent hash. Prompt/style edits create new runs;
			// inherited steps have attempt 0. Never apply this path to new phases.
			if slices.ContainsFunc(s.Steps, func(child avatar.WorkflowStep) bool { return strings.HasPrefix(child.ID, step.ID+"/") }) {
				return ""
			}
			knownNeed := slices.ContainsFunc(s.Needs, func(n avatar.MaterialNeed) bool { return avatar.Digest(n) == avatar.Digest(need) })
			knownSegment := slices.ContainsFunc(s.Segments, func(p avatar.Segment) bool { return avatar.Digest(p) == avatar.Digest(seg) })
			if knownNeed && knownSegment {
				return cached.Fingerprint
			}
			return ""
		}
		if !strings.HasSuffix(step.GenerationPrompt, directorMotionPrompt) {
			return ""
		}
		var input map[string]any
		if json.Unmarshal(step.PromptInput, &input) != nil {
			return ""
		}
		direction := s.Input.Direction
		if len(s.ProductionNotes) > 0 {
			direction += "\n" + strings.Join(s.ProductionNotes, "\n")
		}
		expected := map[string]any{"segment": seg, "topic": s.Input.Topic, "direction": direction, "style": s.Input.Style, "ratio": s.Input.Ratio, "material_content": need.Content, "requested_layout": need.Template, "requested_items": need.Items, "material_design": need.Prompt, "director": s.Director}
		// Compare canonical JSON objects, not struct field-order serialization.
		encoded, _ := json.Marshal(expected)
		var canonical map[string]any
		_ = json.Unmarshal(encoded, &canonical)
		for key, value := range canonical {
			if avatar.Digest(input[key]) != avatar.Digest(value) {
				return ""
			}
		}
		return cached.Fingerprint
	}
	return ""
}

// The cache key includes the production input and prompt contract. A successful
// model response can repair a missing local cache without another paid request.
func (w *avatarWorkflowService) motionPhase(ctx context.Context, s *avatar.WorkflowState, id, phase, prompt string, input any, frames []string, fingerprint string, out any, validate func(context.Context) error) error {
	key := []any{"motion-phases/2", id, fingerprint, prompt, input, s.ConfigID}
	cache := filepath.Join(w.dir(s.ID), "motion-phases", avatar.Digest(key)+".json")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		return err
	}
	load := func(file string) bool {
		raw, err := os.ReadFile(file)
		return err == nil && json.Unmarshal(raw, out) == nil
	}
	candidate := load(cache)
	if !candidate {
		for _, previous := range s.Steps {
			if previous.ID == id && previous.Attempt > 0 && previous.Submission == "completed" && (previous.InputHash == avatar.Digest(key) || motionPhaseInputEqual(previous.PromptInput, input)) {
				candidate = load(filepath.Join(w.dir(s.ID), "model-results", fmt.Sprintf("%s-attempt-%d.json", avatar.Digest(id)[:16], previous.Attempt)))
			}
		}
	}
	for {
		err := w.step(ctx, s, id, key, func(ctx context.Context, step *avatar.WorkflowStep) error {
			step.GenerationPrompt, step.ProductionPhase = prompt, phase
			step.PromptInput, _ = json.Marshal(input)
			if step.Retry != nil && step.Retry.LastError != "" {
				step.ProductionPhase = fmt.Sprintf("根据报错修复 · 自动重试 %d/3", step.Retry.AutomaticRetries)
			}
			if err := w.save(s); err != nil {
				return err
			}
			if candidate {
				candidate = false
				if err := validate(ctx); err == nil {
					step.CostStatus = "reused"
					return avatar.WriteJSON(cache, out)
				} else {
					// A renderer upgrade may fix a false alarm without another model
					// call. Otherwise repair this candidate with its current diagnostic.
					rememberWorkflowFailure(step, err.Error())
					if ctx.Err() != nil || !motionAutoRetryable(*step, err) {
						return err
					}
				}
			}
			reflect.ValueOf(out).Elem().SetZero()
			if err := w.model(ctx, s, step, prompt, input, frames, out); err != nil {
				return err
			}
			if err := validate(ctx); err != nil {
				return err
			}
			return avatar.WriteJSON(cache, out)
		})
		if err == nil {
			if !load(cache) {
				if candidate {
					if validationErr := validate(ctx); validationErr == nil {
						return avatar.WriteJSON(cache, out)
					} else {
						for i := range s.Steps {
							if s.Steps[i].ID == id {
								s.Steps[i].Status = "failed"
								s.Steps[i].Error = validationErr.Error()
								rememberWorkflowFailure(&s.Steps[i], validationErr.Error())
							}
						}
						if saveErr := w.save(s); saveErr != nil {
							return saveErr
						}
						continue
					}
				}
				return &workflowStepError{StepID: id, Err: fmt.Errorf("已完成场景的本地结果不可用，未重复提交模型请求")}
			}
			return nil
		}
		if ctx.Err() != nil || errors.Is(err, errWorkflowPaused) || errors.Is(err, errWorkflowStopped) {
			return err
		}
		idx := slices.IndexFunc(s.Steps, func(step avatar.WorkflowStep) bool { return step.ID == id })
		if idx < 0 || !motionAutoRetryable(s.Steps[idx], err) {
			return err
		}
		step := &s.Steps[idx]
		if step.Retry == nil {
			rememberWorkflowFailure(step, err.Error())
		}
		if step.Retry.AutomaticRetries >= motionAutomaticRetryLimit {
			step.Retry.Exhausted = true
			_ = w.save(s)
			return err
		}
		step.Retry.AutomaticRetries++
		s.AddWarning(id, "repair", fmt.Sprintf("自动修复 %d/3：%s", step.Retry.AutomaticRetries, step.Retry.LastError))
		if saveErr := w.save(s); saveErr != nil {
			return saveErr
		}
		if step.ErrorType == "transient_provider" {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(1<<uint(step.Retry.AutomaticRetries-1)) * time.Second):
			}
		}
	}
}

// Same-run source records survive renderer/prompt upgrades. Timing is always
// revalidated locally; edited content and new runs cannot inherit this shortcut.
func motionPhaseInputEqual(previous json.RawMessage, input any) bool {
	normalize := func(raw []byte) map[string]any {
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil {
			return nil
		}
		delete(m, "retry_context")
		if scene, ok := m["scene"].(map[string]any); ok {
			for _, k := range []string{"start", "end", "duration", "hold_seconds", "timing"} {
				delete(scene, k)
			}
			if elements, ok := scene["elements"].([]any); ok {
				for _, item := range elements {
					if e, ok := item.(map[string]any); ok {
						for _, k := range []string{"at", "local_at", "enter_at"} {
							delete(e, k)
						}
					}
				}
			}
		}
		return m
	}
	raw, _ := json.Marshal(input)
	a, b := normalize(previous), normalize(raw)
	return a != nil && b != nil && avatar.Digest(a) == avatar.Digest(b)
}

func (w *avatarWorkflowService) planMotionPhases(ctx context.Context, s *avatar.WorkflowState, parentID string, input map[string]any, frames []string, need avatar.MaterialNeed, seg avatar.Segment, fingerprint, file string) (avatar.MotionPlan, error) {
	var plan avatar.MotionPlan
	var timing struct {
		Scenes    []json.RawMessage `json:"scenes"`
		Animation json.RawMessage   `json:"animation"`
	}
	align := func(ctx context.Context) error {
		return w.mediaStep(ctx, s, "motion_timing", map[string]any{"segment": seg, "need": need, "motion_plan": plan}, &timing)
	}
	err := w.motionPhase(ctx, s, parentID+"/plan", "设计场景方案", motionDesignPrompt, input, frames, fingerprint, &plan, func(ctx context.Context) error {
		avatar.GroundMotionTitles(&plan)
		plan.Code = ""
		for i := range plan.Scenes {
			plan.Scenes[i].Code = ""
		}
		if err := avatar.ValidateMotionPlan(plan, seg.Text); err != nil {
			return &workflowMediaError{Code: "motion_plan_invalid", Message: err.Error()}
		}
		return align(ctx)
	})
	if err != nil {
		return plan, err
	}
	if err := avatar.ValidateMotionPlan(plan, seg.Text); err != nil {
		return plan, err
	}
	if len(timing.Scenes) == 0 {
		if err := align(ctx); err != nil {
			return plan, &workflowStepError{StepID: parentID + "/plan", Err: err}
		}
	}
	if len(timing.Scenes) != len(plan.Scenes) || len(timing.Animation) == 0 {
		return plan, fmt.Errorf("动效场景缺少实际配音时间，不能生成任意时长的代码")
	}
	// Reserve dispatch/JSON quoting overhead within the existing 18KB renderer limit.
	overhead := 0
	for _, scene := range plan.Scenes {
		quote, _ := json.Marshal(scene.Quote)
		overhead += len(quote) + 70
	}
	budget := min(6000, (18000-overhead)/len(plan.Scenes))
	// The design phase already resolved palette, tone and visual context. Per
	// scene code requests get a bounded digest instead of the full style prose,
	// so a long segment no longer pays for the same reference once per scene.
	var palette []string
	if raw, ok := input["style_profile"].(map[string]any); ok {
		if values, ok := raw["palette"].([]any); ok {
			for _, v := range values {
				palette = append(palette, fmt.Sprint(v))
			}
		}
	}
	styleDigest := styleDigestForCode(fmt.Sprint(input["style_reference"]), palette)
	for i := range plan.Scenes {
		codeInput := map[string]any{"scene": timing.Scenes[i], "scene_index": i + 1, "scene_count": len(plan.Scenes), "code_max_bytes": budget, "tone": plan.Tone, "visual_context": plan.VisualContext}
		for _, key := range []string{"style", "style_profile", "style_reference", "ratio", "director", "material_design", "direction"} {
			codeInput[key] = input[key]
		}
		codeInput = compactCodeInput(codeInput, styleDigest)
		var output struct {
			Code string `json:"code"`
		}
		err = w.motionPhase(avatar.WithMotionCode(ctx), s, fmt.Sprintf("%s/scene-%d", parentID, i+1), fmt.Sprintf("编写场景 %d/%d 绘图代码", i+1, len(plan.Scenes)), motionSceneCodePrompt, codeInput, nil, fingerprint, &output, func(ctx context.Context) error {
			if len(strings.TrimSpace(output.Code)) < 30 || len(output.Code) > budget {
				return &workflowMediaError{Code: "motion_code_invalid", Message: fmt.Sprintf("当前场景绘图代码为空或超过 %d 字节，请压缩当前场景", budget)}
			}
			return w.mediaStep(ctx, s, "motion_validate", map[string]any{"code": output.Code, "animation": timing.Animation, "scene_index": i}, nil)
		})
		if err != nil {
			return plan, err
		}
		plan.Scenes[i].Code = output.Code
	}
	if err := avatar.NormalizeMotionCode(&plan); err != nil {
		return plan, err
	}
	err = avatar.WriteJSON(file, map[string]any{"fingerprint": fingerprint, "plan": plan})
	return plan, err
}
