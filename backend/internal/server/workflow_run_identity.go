package server

import (
	"fmt"
	"regexp"
)

const (
	workflowClientTaskIDMaxLength     = 128
	workflowInputHashMaxLength        = 128
	workflowClientInstanceIDMaxLength = 128
	workflowStepIDMaxLength           = 128
)

var workflowMachineIdentityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

func sanitizeWorkflowClientTaskID(value string, required bool) (string, error) {
	return sanitizeWorkflowMachineIdentity("client_task_id", value, workflowClientTaskIDMaxLength, required)
}

func sanitizeWorkflowInputHash(value string) (string, error) {
	return sanitizeWorkflowMachineIdentity("input_hash", value, workflowInputHashMaxLength, true)
}

func sanitizeWorkflowClientInstanceID(value string, required bool) (string, error) {
	return sanitizeWorkflowMachineIdentity("client_instance_id", value, workflowClientInstanceIDMaxLength, required)
}

func sanitizeWorkflowStepID(value string, required bool) (string, error) {
	return sanitizeWorkflowMachineIdentity("step_id", value, workflowStepIDMaxLength, required)
}

func sanitizeWorkflowMachineIdentity(field, value string, maxLength int, required bool) (string, error) {
	if value == "" {
		if required {
			return "", fmt.Errorf("%w: %s is required", ErrWorkflowIdentityInvalid, field)
		}
		return "", nil
	}
	if len(value) > maxLength || !workflowMachineIdentityPattern.MatchString(value) {
		return "", fmt.Errorf("%w: invalid %s", ErrWorkflowIdentityInvalid, field)
	}
	return value, nil
}
