package server

import (
	"fmt"
	"math"
	"strings"

	"github.com/narratoai/backend-go/internal/avatar"
)

type workflowNarrationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *workflowNarrationError) Error() string { return e.Message }

type workflowAudioTimingSummary struct {
	Total           int                     `json:"total"`
	Completed       int                     `json:"completed"`
	Seconds         float64                 `json:"seconds"`
	LongestSeconds  float64                 `json:"longest_seconds"`
	ResumeAvailable bool                    `json:"resume_available,omitempty"`
	Issue           *workflowNarrationError `json:"issue,omitempty"`
}

func measureWorkflowAudio(s avatar.WorkflowState) workflowAudioTimingSummary {
	t := workflowAudioTimingSummary{Total: len(s.Segments)}
	for _, segment := range s.Segments {
		if segment.Audio == "" || segment.Duration <= 0 || math.IsNaN(segment.Duration) || math.IsInf(segment.Duration, 0) {
			continue
		}
		t.Completed++
		t.Seconds += segment.Duration
		t.LongestSeconds = math.Max(t.LongestSeconds, segment.Duration)
	}
	return t
}

func (t workflowAudioTimingSummary) validationError() *workflowNarrationError {
	if t.Total == 0 || t.Completed != t.Total {
		return &workflowNarrationError{Code: "narration_duration_invalid", Message: fmt.Sprintf("配音时长尚未有效取得（%d / %d 段可用），无法校准整片时长。请检查缺失或时长无效的配音文件后重试。", t.Completed, t.Total)}
	}
	return nil
}

func validateWorkflowNarration(s avatar.WorkflowState) *workflowNarrationError {
	return measureWorkflowAudio(s).validationError()
}

// Derive diagnostics on read, including old failures, without rewriting history.
func workflowAudioTiming(s avatar.WorkflowState) workflowAudioTimingSummary {
	t := measureWorkflowAudio(s)
	legacy := workflowLegacyNarrationLimit(s)
	if legacy || (s.Status == "failed" && strings.Split(s.Stage, "/")[0] == "F04" && s.ErrorType == "narration_duration_invalid") {
		t.Issue = t.validationError()
	}
	t.ResumeAvailable = legacy && t.Issue == nil
	return t
}

// Recognize persisted failures from the retired policy without changing history
// or automatically launching production when a project is opened.
func workflowLegacyNarrationLimit(s avatar.WorkflowState) bool {
	return s.Status == "failed" && strings.Split(s.Stage, "/")[0] == "F04" &&
		(s.ErrorType == "narration_duration_exceeded" || s.Error == "实测配音超过90秒，请缩短文案后新建运行；不会截断原稿")
}
