package avatar

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// Operation contains measured durations and provider-reported values, never
// credentials, prompts, result URLs, or arbitrary upstream error messages.
type Operation struct {
	Scope          string         `json:"scope"`
	Step           string         `json:"step"`
	ShotID         string         `json:"shot_id,omitempty"`
	Action         string         `json:"action,omitempty"`
	StartedAt      time.Time      `json:"started_at"`
	FinishedAt     time.Time      `json:"finished_at"`
	Seconds        float64        `json:"seconds"`
	Status         string         `json:"status"`
	HTTPStatus     int            `json:"http_status,omitempty"`
	ErrorCode      string         `json:"error_code,omitempty"`
	TransportPhase string         `json:"transport_phase,omitempty"`
	TaskID         string         `json:"provider_task_id,omitempty"`
	RequestID      string         `json:"provider_request_id,omitempty"`
	ProviderCode   string         `json:"provider_code,omitempty"`
	ProviderStatus string         `json:"provider_status,omitempty"`
	ResponseSHA256 string         `json:"response_sha256,omitempty"`
	ResponseBytes  int            `json:"response_bytes,omitempty"`
	Usage          map[string]any `json:"usage,omitempty"`
	CostStatus     string         `json:"cost_status"`
}

type telemetryKey struct{}
type operationKey struct{}
type telemetryWriter struct {
	path string
	mu   sync.Mutex
}
type operationTag struct{ step, shot string }

func WithTelemetry(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, telemetryKey{}, &telemetryWriter{path: path})
}

func BeginOperation(ctx context.Context, step, shot string) (context.Context, func(error)) {
	tag := operationTag{step, shot}
	started := time.Now().UTC()
	ctx = context.WithValue(ctx, operationKey{}, tag)
	return ctx, func(err error) {
		status, cost := "succeeded", "local_compute"
		switch step {
		case "tts", "catalog_voice", "planning_llm", "video_submit", "video_wait":
			cost = "unknown"
		case "upload_image", "upload_audio", "download_video":
			cost = "not_reported"
		}
		if err != nil {
			status = "failed"
		}
		event := Operation{Scope: "step", Step: step, ShotID: shot, StartedAt: started,
			FinishedAt: time.Now().UTC(), Seconds: time.Since(started).Seconds(), Status: status, CostStatus: cost}
		if writer, _ := ctx.Value(telemetryKey{}).(*telemetryWriter); writer != nil {
			previous, _ := ReadOperations(writer.path)
			for _, api := range previous {
				if api.Scope == "api" && api.Step == step && api.ShotID == shot && !api.StartedAt.Before(started) {
					if api.TaskID != "" {
						event.TaskID = api.TaskID
					}
					if len(api.Usage) != 0 {
						event.Usage = api.Usage
					}
					if api.CostStatus == "reported" {
						event.CostStatus = "reported"
					} else if api.CostStatus == "unknown" && event.CostStatus != "reported" {
						event.CostStatus = "unknown"
					}
				}
			}
		}
		appendOperation(ctx, event)
	}
}

func RecordModelUsage(ctx context.Context, usage any) {
	raw, _ := json.Marshal(usage)
	var values map[string]any
	_ = json.Unmarshal(raw, &values)
	tag, _ := ctx.Value(operationKey{}).(operationTag)
	now := time.Now().UTC()
	appendOperation(ctx, Operation{Scope: "api", Step: tag.step, ShotID: tag.shot, Action: "chat/completions",
		StartedAt: now, FinishedAt: now, Status: "received", Usage: numericUsage(values), CostStatus: "unknown"})
}
func RecordContentResult(ctx context.Context, started time.Time, requestID string, usage any, err error) {
	tag, _ := ctx.Value(operationKey{}).(operationTag)
	e := Operation{Scope: "api", Step: tag.step, ShotID: tag.shot, Action: "chat/completions", StartedAt: started, FinishedAt: time.Now().UTC(), Seconds: time.Since(started).Seconds(), Status: "received", CostStatus: "unknown", RequestID: telemetryID(requestID)}
	if err != nil {
		e.Status = "failed"
		if httpError, ok := err.(interface{ ProviderHTTPStatus() int }); ok {
			e.HTTPStatus = httpError.ProviderHTTPStatus()
		}
		if diagnostic, ok := err.(interface{ ProviderDiagnosticFields() map[string]string }); ok {
			fields := diagnostic.ProviderDiagnosticFields()
			e.ErrorCode = telemetryID(fields["error_code"])
			e.TransportPhase = telemetryID(fields["transport_phase"])
			if e.RequestID == "" {
				e.RequestID = telemetryID(fields["request_id"])
			}
		}
	} else {
		raw, _ := json.Marshal(usage)
		var values map[string]any
		json.Unmarshal(raw, &values)
		e.Usage = numericUsage(values)
	}
	appendOperation(ctx, e)
}

func appendOperation(ctx context.Context, event Operation) {
	w, _ := ctx.Value(telemetryKey{}).(*telemetryWriter)
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(event)
	_ = f.Sync()
}

func ReadOperations(path string) ([]Operation, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return []Operation{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	events := []Operation{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event Operation
		if json.Unmarshal(scanner.Bytes(), &event) == nil {
			events = append(events, event)
		}
	}
	return events, scanner.Err()
}

var safeTelemetryID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,100}$`)

func telemetryID(value any) string {
	s, _ := value.(string)
	if safeTelemetryID.MatchString(s) {
		return s
	}
	return ""
}

func numericUsage(source map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"consumeMoney", "consumeCoins", "thirdPartyConsumeMoney", "billingSeconds", "taskCostTime", "prompt_tokens", "completion_tokens", "total_tokens", "input_tokens", "output_tokens"} {
		value, exists := source[key]
		if !exists {
			continue
		}
		if value == nil {
			out[key] = nil
			continue
		}
		s := fmt.Sprint(value)
		if n, err := strconv.ParseFloat(s, 64); err == nil && n >= 0 && n < 1e15 {
			out[key] = s
		}
	}
	for _, key := range []string{"tokenUsage", "usage"} {
		if child, ok := source[key].(map[string]any); ok {
			if clean := numericUsage(child); len(clean) != 0 {
				out[key] = clean
			}
		}
	}
	return out
}

func recordProviderHTTP(ctx context.Context, action string, started time.Time, status int, raw []byte, requestID string, err error) {
	tag, _ := ctx.Value(operationKey{}).(operationTag)
	event := Operation{Scope: "api", Step: tag.step, ShotID: tag.shot, Action: action, StartedAt: started,
		FinishedAt: time.Now().UTC(), Seconds: time.Since(started).Seconds(), HTTPStatus: status,
		Status: "received", CostStatus: "unknown", ResponseBytes: len(raw), RequestID: telemetryID(requestID)}
	if err != nil || status < 200 || status >= 300 {
		event.Status = "failed"
	}
	if len(raw) != 0 {
		event.ResponseSHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) == nil {
		if code, exists := payload["code"]; exists {
			if n, parseErr := strconv.ParseInt(fmt.Sprint(code), 10, 64); parseErr == nil {
				event.ProviderCode = strconv.FormatInt(n, 10)
			}
		}
		if nested, ok := payload["data"].(map[string]any); ok && payload["taskId"] == nil {
			for _, key := range []string{"taskId", "status", "usage", "requestId"} {
				if value, found := nested[key]; found {
					payload[key] = value
				}
			}
		}
		event.TaskID = telemetryID(payload["taskId"])
		event.ProviderStatus = telemetryID(payload["status"])
		if event.RequestID == "" {
			event.RequestID = telemetryID(payload["requestId"])
		}
		if usage, ok := payload["usage"].(map[string]any); ok {
			event.Usage = numericUsage(usage)
			for _, key := range []string{"thirdPartyConsumeMoney", "consumeMoney", "consumeCoins"} {
				if event.Usage[key] != nil {
					event.CostStatus = "reported"
				}
			}
		}
	}
	appendOperation(ctx, event)
}
