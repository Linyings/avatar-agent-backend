package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"

	"gorm.io/datatypes"
)

const (
	workflowProviderRequestIDMaxLength = 128
	workflowErrorCodeMaxLength         = 64
	workflowErrorMessageMaxLength      = 128
	workflowUsageMaxValue              = 1_000_000_000_000
)

var (
	workflowProviderRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	workflowErrorCodePattern         = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	workflowUsageIntegerKeys         = map[string]struct{}{
		"prompt_tokens":     {},
		"completion_tokens": {},
		"total_tokens":      {},
		"utf8_bytes":        {},
		"character_count":   {},
	}
	workflowUsageDecimalKeys = map[string]struct{}{
		"duration_seconds": {},
	}
	workflowDiagnosticMessages = map[string]struct{}{
		"素材或生成内容未通过内容审核，请修改或更换素材后重试。":                       {},
		"模型调用失败，请联系客服":                                      {},
		"authentication required":                           {},
		"insufficient credits":                              {},
		"invalid provider output":                           {},
		"invalid provider response":                         {},
		"local resource unavailable":                        {},
		"network unavailable":                               {},
		"operation failed":                                  {},
		"operation interrupted":                             {},
		"provider returned content but omitted token usage": {},
		"provider rate limited":                             {},
		"provider rejected the request":                     {},
		"provider content review blocked the request":       {},
		"provider temporarily unavailable":                  {},
		"provider unavailable":                              {},
		"request timed out":                                 {},
	}
)

func sanitizeWorkflowStepUpdate(input WorkflowStepUpdateInput) (WorkflowStepUpdateInput, error) {
	var err error
	input.ProviderRequestID, err = sanitizeWorkflowProviderRequestID(input.ProviderRequestID)
	if err != nil {
		return WorkflowStepUpdateInput{}, err
	}
	input.ErrorCode, input.ErrorMessage, err = sanitizeWorkflowDiagnostics(input.ErrorCode, input.ErrorMessage)
	if err != nil {
		return WorkflowStepUpdateInput{}, err
	}
	input.Usage, err = sanitizeWorkflowUsage(input.Usage)
	if err != nil {
		return WorkflowStepUpdateInput{}, err
	}
	return input, nil
}

func sanitizeWorkflowProviderRequestID(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, nil
	}
	if len(trimmed) > workflowProviderRequestIDMaxLength || !workflowProviderRequestIDPattern.MatchString(trimmed) {
		return nil, fmt.Errorf("%w: invalid provider_request_id", ErrWorkflowMetadataInvalid)
	}
	return &trimmed, nil
}

func sanitizeWorkflowDiagnostics(code, message *string) (*string, *string, error) {
	var sanitizedCode *string
	if code != nil {
		trimmed := strings.ToLower(strings.TrimSpace(*code))
		if trimmed != "" {
			if len(trimmed) > workflowErrorCodeMaxLength || !workflowErrorCodePattern.MatchString(trimmed) {
				return nil, nil, fmt.Errorf("%w: invalid error_code", ErrWorkflowMetadataInvalid)
			}
			sanitizedCode = &trimmed
		}
	}

	var sanitizedMessage *string
	if message != nil {
		trimmed := strings.ToLower(strings.Join(strings.Fields(*message), " "))
		if trimmed != "" {
			if len(trimmed) > workflowErrorMessageMaxLength {
				return nil, nil, fmt.Errorf("%w: invalid error_message", ErrWorkflowMetadataInvalid)
			}
			if _, ok := workflowDiagnosticMessages[trimmed]; !ok {
				return nil, nil, fmt.Errorf("%w: invalid error_message", ErrWorkflowMetadataInvalid)
			}
			sanitizedMessage = &trimmed
		}
	}
	return sanitizedCode, sanitizedMessage, nil
}

func sanitizeWorkflowUsage(raw datatypes.JSON) (datatypes.JSON, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("%w: invalid usage", ErrWorkflowMetadataInvalid)
	}
	if err := ensureWorkflowJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("%w: invalid usage", ErrWorkflowMetadataInvalid)
	}
	usage, ok := decoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: usage must be an object", ErrWorkflowMetadataInvalid)
	}
	for key, value := range usage {
		number, ok := value.(json.Number)
		if !ok {
			return nil, fmt.Errorf("%w: usage %s must be numeric", ErrWorkflowMetadataInvalid, key)
		}
		if _, ok := workflowUsageIntegerKeys[key]; ok {
			parsed, err := strconv.ParseInt(number.String(), 10, 64)
			if err != nil || parsed < 0 || parsed > workflowUsageMaxValue {
				return nil, fmt.Errorf("%w: invalid usage %s", ErrWorkflowMetadataInvalid, key)
			}
			continue
		}
		if _, ok := workflowUsageDecimalKeys[key]; ok {
			parsed, err := strconv.ParseFloat(number.String(), 64)
			if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 || parsed > workflowUsageMaxValue {
				return nil, fmt.Errorf("%w: invalid usage %s", ErrWorkflowMetadataInvalid, key)
			}
			continue
		}
		return nil, fmt.Errorf("%w: unsupported usage field %s", ErrWorkflowMetadataInvalid, key)
	}
	sanitized, err := json.Marshal(usage)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid usage", ErrWorkflowMetadataInvalid)
	}
	return datatypes.JSON(sanitized), nil
}

func ensureWorkflowJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("unexpected trailing json value")
	}
	return err
}
