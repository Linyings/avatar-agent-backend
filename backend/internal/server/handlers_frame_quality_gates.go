package server

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/narratoai/backend-go/internal/providers"
)

type frameQualityGateConfig struct {
	kind      string
	stage     string
	resultKey string
	endpoint  string
	message   string
}

var timelineDecisionQualityGateConfig = frameQualityGateConfig{
	kind:      "frame_timeline_decision_quality_gate",
	stage:     "timeline_decision_quality_gate",
	resultKey: "decision_result",
	endpoint:  "/api/v1/tools/frame-workflow/timeline-decisions/review",
	message:   "正在复核时间线决策",
}

var narrativeQualityGateConfig = frameQualityGateConfig{
	kind:      "frame_narrative_quality_gate",
	stage:     "frame_narrative_quality_gate",
	resultKey: "narrative_result",
	endpoint:  "/api/v1/tools/frame-workflow/narrative-understanding/review",
	message:   "正在复核视频理解",
}

func (a *App) frameWorkflowTimelineDecisionReview(c *gin.Context) {
	a.frameWorkflowQualityGate(c, timelineDecisionQualityGateConfig)
}

func (a *App) frameWorkflowNarrativeReview(c *gin.Context) {
	a.frameWorkflowQualityGate(c, narrativeQualityGateConfig)
}

func (a *App) frameWorkflowQualityGateInputFingerprint(config frameQualityGateConfig, req map[string]any) string {
	return workflowInputFingerprint(map[string]any{
		"endpoint":         config.endpoint,
		"profile":          "pro",
		"api_base_url":     strings.TrimRight(strings.TrimSpace(a.cfg.OpenAIProAPIBaseURL), "/"),
		"model":            strings.TrimSpace(a.cfg.OpenAIProModel),
		"thinking_type":    strings.TrimSpace(a.cfg.OpenAIProThinkingType),
		"reasoning_effort": strings.TrimSpace(a.cfg.OpenAIProReasoningEffort),
		"temperature":      a.cfg.OpenAITemperature,
		"request":          req,
	})
}

func (a *App) frameWorkflowQualityGate(c *gin.Context, config frameQualityGateConfig) {
	user := getUser(c)
	var req map[string]any
	if !bindJSON(c, &req) {
		return
	}
	identity, identityOK := extractJSONWorkflowIdentity(c, req)
	if !identityOK {
		return
	}
	if identity != nil {
		workflowRequestID(c)
	}
	projectID, err := projectIDFromRequest(req)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid project ID")
		return
	}
	removeProjectIDFromRequest(req)
	operation, valid := frameQualityGateOperation(req)
	if !valid || !hasFrameQualityGateIntent(req) || !hasValidFrameQualityGateOutput(req, config.resultKey) || (operation == "refine" && !hasFrameQualityGateIssues(req)) {
		fail(c, http.StatusBadRequest, "invalid frame quality gate request")
		return
	}
	req["operation"] = operation
	providerReq := frameQualityGateProviderRequest(req, config.resultKey)
	if identity == nil && !a.openai.ConfiguredFor(config.kind, req) {
		fail(c, http.StatusServiceUnavailable, "OpenAI compatible Pro model is not configured")
		return
	}

	estimatedTokens := estimateNarrativeContextTokens(providerReq)
	model := strings.TrimSpace(a.cfg.OpenAIProModel)
	inputFingerprint := a.frameWorkflowQualityGateInputFingerprint(config, req)
	estimatedConsumption := creditConsumption{
		UserID:      user.ID,
		ProjectID:   projectID,
		CreditCount: estimatedTokens,
		APIEndpoint: config.endpoint,
		Provider:    a.inferOpenAICompatibleProvider(model),
		ServiceKind: "llm",
		ModelID:     model,
		Usage:       providers.ChatUsage{TotalTokens: estimatedTokens},
		RequestMetadata: map[string]any{
			workflowInputFingerprintMetadataKey: inputFingerprint,
		},
	}
	if identity == nil && !a.checkCreditBalance(c, user.ID, estimatedConsumption) {
		return
	}
	execution, executionOK := a.resolveJSONWorkflowExecutionForProject(c, user.ID, projectID, identity)
	if !executionOK {
		return
	}
	if identity != nil && !a.openai.ConfiguredFor(config.kind, req) {
		fail(c, http.StatusServiceUnavailable, "OpenAI compatible Pro model is not configured")
		return
	}
	if identity != nil && execution.Step.Status == workflowRunStatusSucceeded {
		if err := a.finalizeProviderSuccess(c.Request.Context(), execution, providerSuccessFinalization{Consumption: estimatedConsumption}); err != nil {
			if errors.Is(err, ErrCreditReservationConflict) {
				respondWorkflowRequestError(c, newWorkflowRequestError(http.StatusConflict, "workflow_input_mismatch", false, "workflow input changed", err))
			} else {
				respondWorkflowRequestError(c, err)
			}
			return
		}
	} else if identity != nil && !a.checkJSONWorkflowCreditBalance(c, user.ID, estimatedConsumption, execution) {
		return
	}

	emitter := beginSerializedToolEventStream(c, config.stage, config.message)
	stopHeartbeat := startToolStreamHeartbeat(c.Request.Context(), emitter, streamToolHeartbeatInterval)
	defer stopHeartbeat()
	stopWorkflowHeartbeat := a.startWorkflowExecutionHeartbeat(c.Request.Context(), execution, streamToolHeartbeatInterval)
	defer stopWorkflowHeartbeat()
	bufferedEmitter := &bufferedToolEmitter{}
	emit := emitter.emit
	if identity != nil {
		emit = bufferedEmitter.emit
	}
	var completed map[string]any
	err = a.openai.Stream(c.Request.Context(), config.kind, providerReq, func(event string, data map[string]any) error {
		if event == "completed" {
			completed = data
			return nil
		}
		return emit(event, data)
	})
	if err != nil {
		if identity != nil {
			if execution.Step.Status != workflowRunStatusSucceeded {
				if finalizeErr := a.finalizeProviderFailure(c.Request.Context(), execution, err); finalizeErr != nil {
					emitWorkflowToolStreamError(c, emitter, finalizeErr)
					return
				}
			}
			emitWorkflowToolStreamError(c, emitter, newWorkflowProviderStreamError(err))
			return
		}
		_ = emitter.flush()
		emitter.write("error", gin.H{"message": err.Error()})
		return
	}
	if identity == nil {
		if err := emitter.flush(); err != nil {
			return
		}
	}

	content, providerModel, requestID, usage := qualityGateProviderCompletion(completed)
	if identity != nil && execution.Step.Status == workflowRunStatusSucceeded && usage.TotalTokens <= 0 {
		if err := json.Unmarshal(execution.Step.Usage, &usage); err != nil {
			emitWorkflowToolStreamError(c, emitter, newWorkflowRequestError(http.StatusInternalServerError, "stored_provider_usage_invalid", true, "stored provider usage is invalid", err))
			return
		}
	}
	output, outputErr := parseFrameQualityGateProviderOutput(content, operation, config.resultKey, frameQualityGateOriginalResult(req, config.resultKey))
	if outputErr != nil {
		const code = "provider_invalid_output"
		const message = "invalid provider output"
		contentBytes, contentHash, contentShape := frameQualityGateProviderOutputDiagnostic(content, config.resultKey)
		workflowRunID := ""
		workflowStepID := ""
		if identity != nil {
			workflowRunID = identity.WorkflowRunID.String()
			workflowStepID = identity.WorkflowStepID
		}
		log.Printf(
			"frame quality gate provider output invalid kind=%q operation=%q workflow_run_id=%q workflow_step_id=%q model=%q provider_request_id=%q output_bytes=%d output_sha256=%q output_shape=%q parse_error=%q",
			config.kind,
			operation,
			workflowRunID,
			workflowStepID,
			providerModel,
			requestID,
			contentBytes,
			contentHash,
			contentShape,
			outputErr.Error(),
		)
		if identity != nil && execution.Step.Status != workflowRunStatusSucceeded {
			if err := a.finalizeProviderNonSuccess(c.Request.Context(), execution, workflowStepStatusFailed, code, message); err != nil {
				emitWorkflowToolStreamError(c, emitter, err)
				return
			}
		}
		emitWorkflowToolStreamError(c, emitter, newWorkflowRequestError(http.StatusBadGateway, code, true, message, outputErr))
		return
	}
	passed := output.passed
	issues := output.issues
	result := output.result
	if operation == "review" {
		result = req[config.resultKey]
	}
	provider := a.inferOpenAICompatibleProvider(providerModel)
	if identity != nil && usage.TotalTokens <= 0 {
		const code = "provider_usage_missing"
		const message = "invalid provider response"
		if execution.Step.Status != workflowRunStatusSucceeded {
			if err := a.finalizeProviderNonSuccess(c.Request.Context(), execution, workflowStepStatusFailed, code, message); err != nil {
				emitWorkflowToolStreamError(c, emitter, err)
				return
			}
		}
		emitWorkflowToolStreamError(c, emitter, newWorkflowRequestError(http.StatusBadGateway, code, true, message, nil))
		return
	}
	if identity != nil || usage.TotalTokens > 0 {
		requestMetadata := gin.H{"request_id": requestID}
		if identity != nil {
			requestMetadata[workflowInputFingerprintMetadataKey] = inputFingerprint
		}
		if err := a.finalizeProviderSuccess(c.Request.Context(), execution, providerSuccessFinalization{
			ProviderRequestID:        optionalStringPointer(requestID),
			RequireProviderRequestID: identity != nil,
			Consumption: creditConsumption{
				UserID:          user.ID,
				ProjectID:       projectID,
				CreditCount:     usage.TotalTokens,
				APIEndpoint:     config.endpoint,
				Provider:        provider,
				ServiceKind:     "llm",
				ModelID:         providerModel,
				Usage:           usage,
				RequestMetadata: requestMetadata,
			},
		}); err != nil {
			if identity != nil {
				if errors.Is(err, ErrInsufficientCredits) && execution.Step.Status != workflowRunStatusSucceeded {
					if blockErr := a.finalizeProviderBlocked(c.Request.Context(), execution, "insufficient_credits", "insufficient credits"); blockErr != nil {
						emitWorkflowToolStreamError(c, emitter, blockErr)
						return
					}
					emitWorkflowToolStreamError(c, emitter, newWorkflowRequestError(http.StatusPaymentRequired, "insufficient_credits", false, "insufficient credits", err))
					return
				}
				emitWorkflowToolStreamError(c, emitter, err)
				return
			}
			_ = emitter.flush()
			emitter.write("error", gin.H{"message": err.Error()})
			return
		}
	}
	completedPayload := gin.H{
		"success":         true,
		"operation":       operation,
		"provider":        provider,
		"model":           providerModel,
		"content":         content,
		"result":          result,
		"issues":          issues,
		"passed":          passed,
		"usage":           usage,
		"request_id":      requestID,
		"tokens_consumed": usage.TotalTokens,
		"updated_at":      time.Now().UTC(),
	}
	if identity != nil {
		for _, event := range bufferedEmitter.events {
			if err := emitter.emit(event.event, event.data); err != nil {
				return
			}
		}
		_ = emitter.flush()
		emitter.write("completed", completedPayload)
		return
	}
	_ = emitter.flush()
	emitter.write("completed", completedPayload)
}

func frameQualityGateProviderRequest(req map[string]any, resultKey string) map[string]any {
	providerReq := make(map[string]any, len(req))
	for key, value := range req {
		providerReq[key] = value
	}
	result, ok := req[resultKey].(map[string]any)
	if !ok {
		return providerReq
	}
	analysis, ok := result["analysis"].(map[string]any)
	if ok && len(analysis) > 0 {
		providerReq[resultKey] = analysis
	}
	return providerReq
}

func frameQualityGateOperation(req map[string]any) (string, bool) {
	operation := strings.ToLower(strings.TrimSpace(fmt.Sprint(req["operation"])))
	return operation, operation == "review" || operation == "refine"
}

func hasFrameQualityGateIntent(req map[string]any) bool {
	intent, ok := req["intent"].(map[string]any)
	if !ok || len(intent) == 0 {
		return false
	}
	if !hasNonEmptyString(intent, "output_language") {
		return false
	}
	duration, ok := numberValue(intent["output_duration_ms"])
	if !ok || duration <= 0 || math.Trunc(duration) != duration {
		return false
	}
	ratio, ok := numberValue(intent["narration_ratio"])
	if !ok || ratio < 0 || ratio > 1 {
		return false
	}
	wordCount, ok := numberValue(intent["word_count"])
	if !ok || wordCount <= 0 || math.Trunc(wordCount) != wordCount {
		return false
	}
	return hasNonEmptyString(intent, "narration_style_id") || hasNonEmptyString(intent, "narration_style_title")
}

func hasValidFrameQualityGateOutput(req map[string]any, resultKey string) bool {
	result, ok := req[resultKey].(map[string]any)
	return ok && len(result) > 0 && validateFrameQualityGateResult(resultKey, result) == nil
}

func hasFrameQualityGateIssues(req map[string]any) bool {
	issues, ok := req["issues"].([]any)
	return ok && len(issues) > 0
}

func qualityGateProviderCompletion(completed map[string]any) (string, string, string, providers.ChatUsage) {
	content := ""
	model := ""
	requestID := ""
	var usage providers.ChatUsage
	if completed == nil {
		return content, model, requestID, usage
	}
	content = fmt.Sprint(completed["content"])
	model = fmt.Sprint(completed["model"])
	requestID = fmt.Sprint(completed["request_id"])
	if typedUsage, ok := completed["usage"].(providers.ChatUsage); ok {
		usage = typedUsage
	} else if rawUsage, ok := completed["usage"].(map[string]any); ok {
		usage.PromptTokens = int64Value(rawUsage["prompt_tokens"])
		usage.CompletionTokens = int64Value(rawUsage["completion_tokens"])
		usage.TotalTokens = int64Value(rawUsage["total_tokens"])
	}
	return content, model, requestID, usage
}

type frameQualityGateProviderOutput struct {
	passed bool
	issues []any
	result any
}

// frameQualityGateJSONObject extracts the first balanced JSON object from a
// provider response. Models occasionally wrap the requested JSON with a short
// preamble or trailing note; treating the whole response as JSON would make a
// valid result fail validation for the wrong reason.
func frameQualityGateJSONObject(content string) (map[string]any, error) {
	cleaned := stripCodeFence(content)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(cleaned), &parsed); err == nil && parsed != nil {
		return parsed, nil
	}
	searchFrom := 0
	for {
		start := strings.IndexByte(cleaned[searchFrom:], '{')
		if start < 0 {
			break
		}
		start += searchFrom
		end := balancedJSONObjectEnd(cleaned, start)
		if end > start {
			var candidate map[string]any
			if err := json.Unmarshal([]byte(cleaned[start:end+1]), &candidate); err == nil && candidate != nil {
				return candidate, nil
			}
			searchFrom = end + 1
			continue
		}
		searchFrom = start + 1
	}
	return nil, fmt.Errorf("quality gate response must be a JSON object")
}

// balancedJSONObjectEnd returns the index of the closing brace matching the
// opening brace at start, or -1 when no balanced object exists.
func balancedJSONObjectEnd(content string, start int) int {
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(content); i++ {
		ch := content[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func parseFrameQualityGateProviderOutput(content, operation, resultKey string, originalResult map[string]any) (frameQualityGateProviderOutput, error) {
	parsed, err := frameQualityGateJSONObject(content)
	if err != nil {
		return frameQualityGateProviderOutput{}, err
	}
	passed, ok := parsed["passed"].(bool)
	if !ok {
		return frameQualityGateProviderOutput{}, fmt.Errorf("quality gate response has invalid passed")
	}
	issues, ok := parsed["issues"].([]any)
	if !ok {
		return frameQualityGateProviderOutput{}, fmt.Errorf("quality gate response has invalid issues")
	}
	if err := validateFrameQualityGateIssues(issues); err != nil {
		return frameQualityGateProviderOutput{}, err
	}
	if operation == "review" {
		return frameQualityGateProviderOutput{passed: passed, issues: issues}, nil
	}
	if operation != "refine" {
		return frameQualityGateProviderOutput{}, fmt.Errorf("quality gate response has invalid operation")
	}
	result, ok := parsed[resultKey]
	resultObject, ok := result.(map[string]any)
	if !ok || len(resultObject) == 0 {
		return frameQualityGateProviderOutput{}, fmt.Errorf("quality gate response has invalid %s", resultKey)
	}
	// Refine is a sparse correction pass: the model may return only the fields
	// it changed. Fill anything it omitted from the original supplied result so
	// a partial narrative/decision result is still accepted and validated.
	resultObject = mergeFrameQualityGateResult(resultObject, originalResult)
	if err := validateFrameQualityGateResult(resultKey, resultObject); err != nil {
		return frameQualityGateProviderOutput{}, err
	}
	return frameQualityGateProviderOutput{passed: passed, issues: issues, result: resultObject}, nil
}

func mergeFrameQualityGateResult(revised map[string]any, original map[string]any) map[string]any {
	if len(original) == 0 {
		return revised
	}
	merged := make(map[string]any, len(revised)+len(original))
	for key, value := range original {
		merged[key] = value
	}
	for key, value := range revised {
		merged[key] = value
	}
	return merged
}

func frameQualityGateOriginalResult(req map[string]any, resultKey string) map[string]any {
	if result, ok := req[resultKey].(map[string]any); ok {
		return result
	}
	return nil
}

func frameQualityGateProviderOutputDiagnostic(content, resultKey string) (int, string, string) {
	stripped := stripCodeFence(content)
	hash := sha256.Sum256([]byte(stripped))
	shape := "invalid_json"
	if parsed, err := frameQualityGateJSONObject(content); err == nil && parsed != nil {
		knownKeys := []string{"passed", "issues", resultKey}
		known := make(map[string]struct{}, len(knownKeys))
		parts := make([]string, 0, len(knownKeys)+1)
		for _, key := range knownKeys {
			known[key] = struct{}{}
			parts = append(parts, key+"="+frameQualityGateJSONType(parsed[key]))
		}
		unknownCount := 0
		for key := range parsed {
			if _, ok := known[key]; !ok {
				unknownCount++
			}
		}
		parts = append(parts, fmt.Sprintf("unknown_fields=%d", unknownCount))
		shape = "object{" + strings.Join(parts, ",") + "}"
	}
	return len([]byte(stripped)), fmt.Sprintf("%x", hash), shape
}

func frameQualityGateJSONType(value any) string {
	switch value.(type) {
	case nil:
		return "missing"
	case bool:
		return "bool"
	case string:
		return "string"
	case float64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "other"
	}
}

func validateFrameQualityGateIssues(issues []any) error {
	for _, rawIssue := range issues {
		issue, ok := rawIssue.(map[string]any)
		if !ok {
			return fmt.Errorf("quality gate response has invalid issue")
		}
		if !hasNonEmptyString(issue, "code") || !hasNonEmptyString(issue, "message") {
			return fmt.Errorf("quality gate response issue requires code and message")
		}
		severity, ok := issue["severity"].(string)
		if !ok || (severity != "info" && severity != "warning" && severity != "error") {
			return fmt.Errorf("quality gate response issue has invalid severity")
		}
	}
	return nil
}

func validateFrameQualityGateResult(resultKey string, result map[string]any) error {
	switch resultKey {
	case "decision_result":
		return validateTimelineDecisionQualityGateResult(result)
	case "narrative_result":
		return validateNarrativeQualityGateResult(result)
	default:
		return fmt.Errorf("quality gate response has unsupported result key %s", resultKey)
	}
}

func validateTimelineDecisionQualityGateResult(result map[string]any) error {
	if _, hasFullResponse := result["analysis"]; hasFullResponse {
		if !hasBool(result, "success") || !hasString(result, "provider") || !hasString(result, "model") || !hasString(result, "content") || !hasObject(result, "analysis") || !hasObject(result, "usage") || !hasString(result, "request_id") || !hasFiniteNumber(result, "tokens_consumed") || !hasString(result, "updated_at") {
			return fmt.Errorf("quality gate response has invalid decision_result production response")
		}
	} else if !hasNonEmptyString(result, "summary") || !hasBool(result, "needs_timeline_view") || !hasArray(result, "skipped") || !hasNonEmptyString(result, "confidence") || !hasArray(result, "global_notes") {
		return fmt.Errorf("quality gate response has invalid decision_result quality gate response")
	}

	decisions, ok := result["decisions"].([]any)
	if !ok {
		return fmt.Errorf("quality gate response decision_result requires decisions")
	}
	for _, rawDecision := range decisions {
		decision, ok := rawDecision.(map[string]any)
		if !ok || !hasFiniteNumber(decision, "start") || !hasFiniteNumber(decision, "end") || !hasNonEmptyString(decision, "priority") || !hasNonEmptyString(decision, "trigger_type") || !hasNonEmptyString(decision, "reason") {
			return fmt.Errorf("quality gate response has invalid timeline decision")
		}
		start, _ := numberValue(decision["start"])
		end, _ := numberValue(decision["end"])
		if start < 0 || end <= start {
			return fmt.Errorf("quality gate response has invalid timeline decision range")
		}
		if rawFrames, exists := decision["n_frames"]; exists {
			frames, ok := numberValue(rawFrames)
			if !ok || frames <= 0 || math.Trunc(frames) != frames {
				return fmt.Errorf("quality gate response has invalid timeline decision n_frames")
			}
		}
	}
	return nil
}

func validateNarrativeQualityGateResult(result map[string]any) error {
	if _, hasFullResponse := result["analysis"]; hasFullResponse {
		analysis, _ := result["analysis"].(map[string]any)
		if !hasBool(result, "success") || !hasString(result, "provider") || !hasString(result, "model") || !hasString(result, "content") || !hasObject(result, "usage") || !hasString(result, "request_id") || !hasFiniteNumber(result, "tokens_consumed") || !hasString(result, "updated_at") || !hasNonEmptyString(analysis, "summary") {
			return fmt.Errorf("quality gate response has invalid narrative_result production response")
		}
	} else if !hasNonEmptyString(result, "summary") || !hasObject(result, "plot") || !hasArray(result, "open_questions") {
		return fmt.Errorf("quality gate response has invalid narrative_result quality gate response")
	}

	for _, key := range []string{"characters", "relationships", "emotion_curve", "events"} {
		if !hasArray(result, key) {
			return fmt.Errorf("quality gate response narrative_result requires %s", key)
		}
	}
	return nil
}

func hasObject(value map[string]any, key string) bool {
	_, ok := value[key].(map[string]any)
	return ok
}

func hasArray(value map[string]any, key string) bool {
	_, ok := value[key].([]any)
	return ok
}

func hasBool(value map[string]any, key string) bool {
	_, ok := value[key].(bool)
	return ok
}

func hasString(value map[string]any, key string) bool {
	_, ok := value[key].(string)
	return ok
}

func hasNonEmptyString(value map[string]any, key string) bool {
	text, ok := value[key].(string)
	return ok && strings.TrimSpace(text) != ""
}

func hasFiniteNumber(value map[string]any, key string) bool {
	_, ok := numberValue(value[key])
	return ok
}

func numberValue(value any) (float64, bool) {
	number, ok := value.(float64)
	return number, ok && !math.IsNaN(number) && !math.IsInf(number, 0)
}
