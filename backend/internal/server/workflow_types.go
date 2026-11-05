package server

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// WorkflowStep identifies one canonical project workflow step. The table names
// intentionally mirror the model names expected by the workflow split PRD:
// ProjectVideoSource, ProjectSubtitle, ProjectGenerationConfig, ProjectScript,
// and ProjectVoiceover.
type WorkflowStep string

const (
	WorkflowStepVideoSource WorkflowStep = "video_source"
	WorkflowStepSubtitle    WorkflowStep = "subtitle"
	WorkflowStepConfig      WorkflowStep = "config"
	WorkflowStepScript      WorkflowStep = "script"
	WorkflowStepVoiceover   WorkflowStep = "voiceover"
)

// WorkflowSaveInput is the service-layer DTO used by routes and legacy adapters
// to persist a single workflow step. ExpectedWorkflowRevision and
// ExpectedDependencyHash are optimistic-concurrency guards from the client's
// last observed state.
type WorkflowSaveInput struct {
	ProjectID                uuid.UUID
	ExpectedWorkflowRevision *int
	ExpectedDependencyHash   string
	Payload                  any
	Status                   string
	LegacyCache              WorkflowLegacyCache
}

// WorkflowLegacyCache lets callers pass high-value compatibility fields that
// are not reliably discoverable from an arbitrary JSON payload.
type WorkflowLegacyCache struct {
	VideoURL    *string
	VideoURLSet bool
	AudioURL    *string
	AudioURLSet bool
}

// WorkflowSaveResult describes the persisted active row after a save.
type WorkflowSaveResult struct {
	Step             WorkflowStep `json:"step"`
	StepID           uuid.UUID    `json:"step_id"`
	ProjectID        uuid.UUID    `json:"project_id"`
	InputHash        string       `json:"input_hash"`
	DependencyHash   string       `json:"dependency_hash"`
	ContentHash      string       `json:"content_hash"`
	WorkflowRevision int          `json:"workflow_revision"`
	Idempotent       bool         `json:"idempotent"`
}

// WorkflowConflictError is returned when optimistic-concurrency guards are
// stale. HTTP adapters should translate this to 409 Conflict.
type WorkflowConflictError struct {
	Step     WorkflowStep `json:"step"`
	Reason   string       `json:"reason"`
	Expected any          `json:"expected,omitempty"`
	Actual   any          `json:"actual,omitempty"`
}

func (e *WorkflowConflictError) Error() string {
	if e == nil {
		return "workflow conflict"
	}
	return fmt.Sprintf("workflow %s conflict: %s", e.Step, e.Reason)
}

func IsWorkflowConflict(err error) bool {
	var conflict *WorkflowConflictError
	return errors.As(err, &conflict)
}
