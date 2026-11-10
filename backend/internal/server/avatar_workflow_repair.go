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
	switch {
	case strings.Contains(message, "截断"), strings.Contains(message, "字节"), strings.Contains(message, "JSON"):
		return "压缩当前场景的输出，删除重复代码和长注释；保留必需字段并返回完整、正确转义的 JSON。"
	case strings.Contains(message, "Canvas"), strings.Contains(message, "全局变量"):
		return "根据具体校验位置修复当前场景代码，使用局部变量及确定性的 Canvas 绘制，保留原内容与风格。"
	case strings.Contains(message, "时间"), strings.Contains(message, "展示"), strings.Contains(message, "绘制"):
		if strings.Contains(message, "场景") || strings.Contains(message, "动效") {
			return "根据本场实际 duration、elements.local_at 和 enter_at 修正出场时间；数字和单位原样保留，允许分开排版，关键内容须在结束前完整展示并留出阅读时间。"
		}
		return "根据当前输入的真实时长、可用素材区间和起止时间修复时间映射，保留已经完成的内容。"
	case strings.Contains(message, "渲染"), strings.Contains(message, "render"), strings.Contains(message, "Remotion"):
		return "渲染失败通常由无效属性或超出范围的值引起；检查动效代码中的数值范围、类型匹配和 Remotion API 用法，不要改变内容意图。"
	case strings.Contains(message, "语法"), strings.Contains(message, "SyntaxError"), strings.Contains(message, "parse"):
		return "修复代码语法错误；确保括号、引号、逗号配对，返回合法的 JavaScript 表达式，不要截断输出。"
	case strings.Contains(message, "超时"), strings.Contains(message, "timeout"), strings.Contains(message, "TIMEOUT"):
		return "简化本场景的复杂度：减少元素数量，缩短动画计算，避免深层嵌套；保留核心内容和视觉意图。"
	default:
		return "根据上次后台错误检查当前步骤的输入与输出，仅修复失败部分；保留已经成功的内容，不能原样返回失败结果。"
	}
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
			if len(prior) > 10000 {
				prior = prior[:10000]
			}
			detail["previous_output"] = string(prior)
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
