package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type WorkflowRun struct {
	ID                uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	UserID            uuid.UUID  `gorm:"type:uuid;index;not null" json:"user_id"`
	ProjectID         *uuid.UUID `gorm:"type:uuid;index" json:"project_id"`
	ClientType        string     `gorm:"size:16;index;not null" json:"client_type"`
	ClientTaskID      *string    `gorm:"size:255;index" json:"client_task_id"`
	ClientProjectName string     `gorm:"size:200;not null;default:''" json:"client_project_name"`
	InputHash         string     `gorm:"size:128;not null" json:"input_hash"`
	Status            string     `gorm:"size:24;index;not null" json:"status"`
	BillingStatus     string     `gorm:"size:24;index;not null" json:"billing_status"`
	CurrentStepID     *string    `gorm:"size:255;index" json:"current_step_id"`
	LastErrorCode     *string    `gorm:"size:128" json:"last_error_code"`
	LastErrorMessage  *string    `gorm:"size:1024" json:"last_error_message"`
	FinalResultAt     *time.Time `gorm:"index" json:"final_result_at"`
	CompletedAt       *time.Time `gorm:"index" json:"completed_at"`
	CancelledAt       *time.Time `gorm:"index" json:"cancelled_at"`
	ClientInstanceID  string     `gorm:"size:255;not null" json:"client_instance_id"`
	LeaseExpiresAt    *time.Time `gorm:"index" json:"lease_expires_at"`
	LastActiveAt      time.Time  `gorm:"index;not null" json:"last_active_at"`
	ExpiresAt         time.Time  `gorm:"index;not null" json:"expires_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

func (WorkflowRun) TableName() string { return "workflow_runs" }

func (r *WorkflowRun) BeforeCreate(*gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

type WorkflowStepExecution struct {
	ID                          uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	WorkflowRunID               uuid.UUID      `gorm:"type:uuid;index;not null" json:"workflow_run_id"`
	StepID                      string         `gorm:"size:255;index;not null" json:"step_id"`
	InputHash                   string         `gorm:"size:128;not null" json:"input_hash"`
	ExecutionKey                string         `gorm:"size:64;uniqueIndex;not null" json:"execution_key"`
	Status                      string         `gorm:"size:24;index;not null" json:"status"`
	AttemptCount                int            `gorm:"not null;default:0" json:"attempt_count"`
	ProviderRequestID           *string        `gorm:"size:255;index" json:"provider_request_id"`
	ProviderInputHash           *string        `gorm:"size:64" json:"provider_input_hash"`
	ProviderSubmissionClaimID   *string        `gorm:"size:36" json:"-"`
	ProviderSubmissionClaimedAt *time.Time     `json:"-"`
	PreRegistered               bool           `gorm:"not null;default:false" json:"-"`
	ErrorCode                   *string        `gorm:"size:128" json:"error_code"`
	ErrorMessage                *string        `gorm:"size:1024" json:"error_message"`
	Usage                       datatypes.JSON `gorm:"type:json" json:"usage"`
	// Completed provider output is available only through the authenticated result endpoint.
	Result         datatypes.JSON `gorm:"type:json" json:"-"`
	ResultVersion  int            `gorm:"not null;default:0" json:"-"`
	LeaseOwner     *string        `gorm:"size:255;index" json:"lease_owner"`
	LeaseExpiresAt *time.Time     `gorm:"index" json:"lease_expires_at"`
	StartedAt      *time.Time     `json:"started_at"`
	CompletedAt    *time.Time     `gorm:"index" json:"completed_at"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

func (WorkflowStepExecution) TableName() string { return "workflow_step_executions" }

func (e *WorkflowStepExecution) BeforeCreate(*gorm.DB) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	if len(e.Usage) == 0 {
		e.Usage = datatypes.JSON([]byte("{}"))
	}
	return nil
}
