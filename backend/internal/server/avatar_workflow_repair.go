package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/narratoai/backend-go/internal/avatar"
)

const motionAutomaticRetryLimit = 3

type workflowMediaError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	SceneIndex int    `json:"scene_index,omitempty"`
	Stage      string `json:"-"`
}

func (e *workflowMediaError) Error() string {
	if e.Stage == "" {
		return e.Message
	}
	return fmt.Sprintf("本地媒体步骤 %s：%s", e.Stage, e.Message)
}

var repairSecret = regexp.MustCompile(`(?i)(api[_ -]?key|authorization|access_token|token|secret|password)\s*[:=]\s*["']?[^\s,"'}]+`)
var repairBearer = regexp.MustCompile(`(?i)bearer\s+[^\s,"'}]+`)

func safeRepairText(text string) string {
	text = repairSecret.ReplaceAllString(text, "$1=[已隐藏]")
	text = repairBearer.ReplaceAllString(text, "Bearer [已隐藏]")
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > 900 {
		runes = runes[:900]
	}
	return string(runes)
}

// truncateRepairText cuts on rune boundaries. Slicing bytes would split a
// multi-byte character and make the JSON unmarshalable for the model.
func truncateRepairText(text string, runes int) string {
	values := []rune(strings.TrimSpace(text))
	if len(values) <= runes {
		return string(values)
	}
	return string(values[:runes]) + "…（已截断）"
}

func mediaFailure(stage string, log []byte) *workflowMediaError {
	failure := &workflowMediaError{Stage: stage, Code: "media_execution", Message: "执行失败，请查看任务日志"}
	for _, line := range strings.Split(string(log), "\n") {
		if strings.HasPrefix(line, "AVATAR_MEDIA_ERROR=") {
			var detail workflowMediaError
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "AVATAR_MEDIA_ERROR=")), &detail) == nil && detail.Message != "" {
				detail.Stage = stage
				detail.Message = safeRepairText(detail.Message)
				return &detail
			}
		}
		for _, prefix := range []string{"Error: ", "ValueError: ", "RuntimeError: "} {
			if strings.HasPrefix(line, prefix) {
				failure.Message = safeRepairText(strings.TrimPrefix(line, prefix))
			}
		}
	}
	return failure
}

func repairStrategy(message string) string {
	// More specific error classification with actionable repair instructions
	lower := strings.ToLower(message)
	
	// JSON truncation or size issues
	if strings.Contains(message, "截断") || strings.Contains(lower, "truncat") || strings.Contains(message, "字节") {
		return "输出被截断，立即压缩：(1) 删除所有注释和空行 (2) 合并重复的绘图逻辑为函数 (3) 简化变量名 (4) 如果场景过多，只保留最重要的 2-3 个关键场景，其他合并为简化版本。确保返回完整、正确转义的 JSON。"
	}
	
	// JSON format errors
	if strings.Contains(lower, "json") && (strings.Contains(lower, "invalid") || strings.Contains(lower, "parse") || strings.Contains(message, "格式") || strings.Contains(message, "无效")) {
		return "JSON 格式错误：(1) 检查所有引号、括号、逗号是否正确配对 (2) 确保字符串中的换行符用 \\n 转义 (3) 确保反斜杠用 \\\\ 转义 (4) 不要在 code 字段中使用未转义的双引号。返回严格的 JSON 格式。"
	}
	
	// Canvas API or global variable errors
	if strings.Contains(message, "Canvas") || strings.Contains(lower, "global") || strings.Contains(message, "全局变量") || strings.Contains(lower, "undefined") {
		return "Canvas 代码错误：(1) 只使用 ctx (CanvasRenderingContext2D) 的标准方法：fillRect, strokeRect, arc, lineTo, fillText 等 (2) 所有变量必须在函数内声明，不能使用全局变量 (3) 不能使用 DOM、window、document、setTimeout、setInterval (4) 只能使用 Math 和标准 JavaScript。检查报错位置，修复具体的 API 调用或变量声明。"
	}
	
	// Timing and duration issues
	if strings.Contains(message, "时间") || strings.Contains(lower, "duration") || strings.Contains(message, "展示") || strings.Contains(lower, "timing") {
		if strings.Contains(message, "scene") || strings.Contains(message, "场景") || strings.Contains(message, "动效") {
			return "场景时间安排错误：(1) 所有元素必须在 scene.duration - scene.hold_seconds 之前完整出现 (2) 使用 elements[i].local_at 或 enter_at 判断元素何时入场，不要用固定的秒数 (3) 如果 duration 只有 2-3 秒，同时展示多个元素而不是顺序动画 (4) 文字、数字、单位必须在同一画面完整可见。立即修正所有 if (t > xxx) 的时间判断。"
		}
		return "时间映射错误：根据报错信息中的具体时间值，检查 (1) 素材的实际起止时间是否在可用区间内 (2) 动画的时间计算是否正确 (3) 是否误用了全局时间而不是本场景的相对时间。"
	}
	
	// Rendering or Remotion errors
	if strings.Contains(lower, "render") || strings.Contains(message, "渲染") || strings.Contains(lower, "remotion") {
		return "渲染失败：(1) 检查是否使用了无效的 CSS 属性值（如负数的 width/height、超出 0-1 范围的 opacity） (2) 确保所有数值类型正确（不要把字符串当数字用） (3) 检查是否误用了 React 组件而不是纯 Canvas 绘图 (4) 确保 fillStyle 和 strokeStyle 是有效的颜色值。根据具体报错信息修复对应的代码行。"
	}
	
	// Syntax errors
	if strings.Contains(lower, "syntax") || strings.Contains(message, "语法") || strings.Contains(lower, "syntaxerror") {
		return "JavaScript 语法错误：(1) 检查所有括号 () [] {} 是否正确配对 (2) 检查字符串引号是否正确闭合 (3) 检查每行结尾是否缺少分号或逗号 (4) 检查是否有拼写错误的关键字。仔细阅读报错信息中的行号和错误位置，精确修复该处的语法问题。"
	}
	
	// Timeout errors
	if strings.Contains(lower, "timeout") || strings.Contains(message, "超时") {
		return "执行超时：(1) 减少场景数量，合并相似内容 (2) 简化每个场景的绘图逻辑，删除复杂的循环和计算 (3) 减少文字数量和图形元素 (4) 避免深层嵌套的循环。优先保留核心信息，次要细节可以省略。"
	}
	
	// Text or element display issues
	if strings.Contains(message, "文字") || strings.Contains(lower, "text") || strings.Contains(message, "标签") || strings.Contains(lower, "label") {
		return "文字显示问题：(1) 只能使用 scene.title、scene.elements[].label 和它们的子串，不能添加额外文字 (2) 数值和单位必须完整保留（如 \"1.5亿\" 不能拆成 \"1.5\" 和 \"亿\"） (3) 字号不能小于 min(w,h)*0.038 (4) 确保所有文字在画面结束前完整可见并留出阅读时间。"
	}
	
	// Default: generic repair instruction with error context emphasis
	return "根据报错信息修复代码：(1) 仔细阅读错误消息中的具体问题描述 (2) 定位到出错的代码行 (3) 针对性地修复该处的具体问题，不要改动其他正常工作的部分 (4) 保持原有的内容意图和视觉风格。如果错误信息不明确，尝试简化代码复杂度。"
}

func rememberWorkflowFailure(step *avatar.WorkflowStep, message string) {
	if step.Retry == nil {
		step.Retry = &avatar.WorkflowRetry{}
	}
	step.Retry.LastError = safeRepairText(message)
	step.Retry.Strategy = repairStrategy(message)
}

// Keep prompt editing tied to the selected video's evidence, without sending
// unrelated materials or unbounded model transcripts to the rewrite model.
func (w *avatarWorkflowService) materialFailureContext(s *avatar.WorkflowState, node string) []map[string]any {
	var failures []map[string]any
	for _, step := range s.Steps {
		if step.Status != "failed" || (step.ID != node && !strings.HasPrefix(step.ID, node+"/") && !strings.HasPrefix(step.ID, node+"-")) {
			continue
		}
		if step.Retry != nil {
			copy := *step.Retry
			step.Retry = &copy
		}
		if step.Retry == nil || step.Retry.LastError == "" || (step.Error != "" && step.Retry.LastError != safeRepairText(step.Error)) {
			w.restoreRetryCause(s, &step)
		}
		if step.Retry == nil || step.Retry.LastError == "" {
			continue
		}
		history := []string{}
		start := max(0, len(step.Attempts)-4)
		for _, attempt := range step.Attempts[start:] {
			if attempt.Error != "" {
				history = append(history, safeRepairText(attempt.Error))
			}
		}
		failures = append(failures, map[string]any{"step_id": step.ID, "last_error": safeRepairText(step.Retry.LastError), "repair_instruction": safeRepairText(step.Retry.Strategy), "automatic_retries": step.Retry.AutomaticRetries, "exhausted": step.Retry.Exhausted, "recent_errors": history})
		if len(failures) == 4 {
			break
		}
	}
	return failures
}

// Old runs contain a generic outer error. Read its recorded subprocess log
// before retry clears it, rather than asking the model to guess the cause.
func (w *avatarWorkflowService) restoreRetryCause(s *avatar.WorkflowState, step *avatar.WorkflowStep) {
	if step.Error == "" {
		return
	}
	message := step.Error
	for _, stage := range []string{"motion_validate", "material", "motion_timing"} {
		if strings.Contains(message, "本地媒体步骤 "+stage+" 执行失败") {
			if log, err := os.ReadFile(filepath.Join(w.dir(s.ID), stage+".log")); err == nil {
				message = mediaFailure(stage, log).Error()
			}
			break
		}
	}
	rememberWorkflowFailure(step, message)
}

func (w *avatarWorkflowService) repairModelInput(s *avatar.WorkflowState, step *avatar.WorkflowStep, input any) any {
	if step.Retry == nil || step.Retry.LastError == "" {
		return input
	}
	request := map[string]any{}
	if original, ok := input.(map[string]any); ok {
		for key, value := range original {
			request[key] = value
		}
	} else {
		raw, _ := json.Marshal(input)
		if json.Unmarshal(raw, &request) != nil || request == nil {
			request = map[string]any{"original_input": input}
		}
	}
	detail := map[string]any{"last_error": step.Retry.LastError, "repair_instruction": step.Retry.Strategy, "automatic_retries": step.Retry.AutomaticRetries, "mode": "diagnose_and_repair"}
	// A bounded previous output is data, not another instruction. Read only the
	// immediately preceding completed attempt, never other materials or logs.
	if step.Attempt > 1 {
		file := filepath.Join(w.dir(s.ID), "model-results", fmt.Sprintf("%s-attempt-%d.json", avatar.Digest(step.ID)[:16], step.Attempt-1))
		if prior, err := os.ReadFile(file); err == nil {
			// Parse and strip any nested retry_context: carrying it forward makes
			// every retry re-embed all earlier retries, so the prompt grows
			// geometrically with attempt count. Only the code body is useful.
			var previous map[string]any
			if json.Unmarshal(prior, &previous) == nil {
				delete(previous, "retry_context")
				if code, ok := previous["code"].(string); ok && len(previous) <= 2 {
					previous = map[string]any{"code": code}
				}
				if encoded, e := json.Marshal(previous); e == nil {
					prior = encoded
				}
			}
			detail["previous_output"] = truncateRepairText(string(prior), 6000)
		}
	}
	request["retry_context"] = detail
	return request
}

func motionAutoRetryable(step avatar.WorkflowStep, err error) bool {
	if step.Submission == "unknown" || step.Submission == "submitting" {
		return false
	}
	if step.ErrorType == "model_output" || step.ErrorType == "transient_provider" || step.ErrorType == "validation" {
		return true
	}
	var media *workflowMediaError
	if errors.As(err, &media) {
		switch media.Code {
		case "motion_code_invalid", "motion_plan_invalid", "motion_text_missing", "motion_text_too_brief", "motion_draw_failed":
			return true
		}
	}
	return false
}

// Render diagnostics identify the exact scene. Mark only that phase for repair;
// its successful siblings and design plan remain reusable on the next pass.
func (w *avatarWorkflowService) repairRenderedMotion(s *avatar.WorkflowState, parent string, err error) (bool, error) {
	var media *workflowMediaError
	if !errors.As(err, &media) || media.SceneIndex < 1 {
		return false, err
	}
	id := fmt.Sprintf("%s/scene-%d", parent, media.SceneIndex)
	for i := range s.Steps {
		step := &s.Steps[i]
		if step.ID != id {
			continue
		}
		step.Status, step.Error, step.ErrorType = "failed", err.Error(), "execution"
		rememberWorkflowFailure(step, err.Error())
		failure := &workflowStepError{StepID: id, Err: err}
		if !motionAutoRetryable(*step, err) || step.Retry.AutomaticRetries >= motionAutomaticRetryLimit {
			step.Retry.Exhausted = step.Retry.AutomaticRetries >= motionAutomaticRetryLimit
			if e := w.save(s); e != nil {
				return false, e
			}
			return false, failure
		}
		step.Retry.AutomaticRetries++
		s.AddWarning(id, "repair", fmt.Sprintf("渲染检查未通过，自动修复 %d/3：%s", step.Retry.AutomaticRetries, step.Retry.LastError))
		if e := w.save(s); e != nil {
			return false, e
		}
		return true, nil
	}
	return false, err
}
