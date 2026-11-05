package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	workflowRunLeaseDuration          = 90 * time.Second
	workflowRunInactivityDuration     = 24 * time.Hour
	workflowRunExpirationInterval     = 5 * time.Minute
	workflowRunStatusRunning          = "running"
	workflowRunStatusInterrupted      = "interrupted"
	workflowRunStatusBlocked          = "blocked"
	workflowRunStatusFinalizing       = "finalizing"
	workflowRunStatusSucceeded        = "succeeded"
	workflowRunStatusCancelled        = "cancelled"
	workflowRunStatusExpired          = "expired"
	workflowRunBillingStatusPending   = "pending"
	workflowRunBillingStatusConfirmed = "confirmed"
	workflowRunBillingStatusReleased  = "released"
)

var (
	ErrWorkflowRunNotFound                   = errors.New("workflow run not found")
	ErrWorkflowRunScopeConflict              = errors.New("active workflow run input hash conflict")
	ErrWorkflowRunLeaseConflict              = errors.New("workflow run lease is owned by another client")
	ErrWorkflowStepLeaseConflict             = errors.New("workflow step lease is owned by another client")
	ErrWorkflowStepTransitionInvalid         = errors.New("workflow step transition is invalid")
	ErrWorkflowIdentityInvalid               = errors.New("workflow identity is invalid")
	ErrWorkflowMetadataInvalid               = errors.New("workflow metadata is invalid")
	ErrWorkflowRunTerminal                   = errors.New("workflow run is terminal")
	ErrWorkflowFinalResultExists             = errors.New("workflow run already has a final result")
	ErrWorkflowReservationInheritanceInvalid = errors.New("workflow reservation inheritance is invalid")
)

var workflowRunActiveStatuses = []string{
	workflowRunStatusRunning,
	workflowRunStatusInterrupted,
	workflowRunStatusBlocked,
	workflowRunStatusFinalizing,
}

type WorkflowRunStateHooks struct {
	AfterComplete func(context.Context, *gorm.DB, *models.WorkflowRun) error
	AfterCancel   func(context.Context, *gorm.DB, *models.WorkflowRun) error
	AfterExpire   func(context.Context, *gorm.DB, *models.WorkflowRun) error
	AfterActivity func(context.Context, *gorm.DB, *models.WorkflowRun) error
}

type WorkflowRunStateService struct {
	db    *gorm.DB
	now   func() time.Time
	hooks WorkflowRunStateHooks
}

type CreateWorkflowRunInput struct {
	UserID                       uuid.UUID
	ProjectID                    *uuid.UUID
	ClientType                   string
	ClientTaskID                 *string
	ClientProjectName            string
	InputHash                    string
	ClientInstanceID             string
	InheritReservationsFromRunID *uuid.UUID
}

type CreateWorkflowRunResult struct {
	Run    models.WorkflowRun
	Reused bool
}

type WorkflowRunHeartbeatInput struct {
	ClientInstanceID string
	CurrentStepID    *string
}

type WorkflowRunInterruptInput struct {
	ClientInstanceID string
	ErrorCode        *string
	ErrorMessage     *string
}

type WorkflowStepUpdateInput struct {
	StepID            string
	InputHash         string
	Status            string
	ClientInstanceID  string
	PreRegistered     bool
	ProviderRequestID *string
	ErrorCode         *string
	ErrorMessage      *string
	Usage             datatypes.JSON
}

func NewWorkflowRunStateService(db *gorm.DB, hooks WorkflowRunStateHooks) *WorkflowRunStateService {
	return &WorkflowRunStateService{
		db:    db,
		now:   func() time.Time { return time.Now().UTC() },
		hooks: hooks,
	}
}

func (s *WorkflowRunStateService) SetHooks(hooks WorkflowRunStateHooks) {
	s.hooks = hooks
}

func (s *WorkflowRunStateService) Create(ctx context.Context, input CreateWorkflowRunInput) (CreateWorkflowRunResult, error) {
	var err error
	input.InputHash, err = sanitizeWorkflowInputHash(input.InputHash)
	if err != nil {
		return CreateWorkflowRunResult{}, err
	}
	input.ClientInstanceID, err = sanitizeWorkflowClientInstanceID(input.ClientInstanceID, false)
	if err != nil {
		return CreateWorkflowRunResult{}, err
	}
	if input.ClientTaskID != nil {
		clientTaskID, err := sanitizeWorkflowClientTaskID(*input.ClientTaskID, input.ClientType == "pc")
		if err != nil {
			return CreateWorkflowRunResult{}, err
		}
		input.ClientTaskID = &clientTaskID
	}
	if input.ClientType == "pc" {
		input.ClientProjectName, err = sanitizeWorkflowClientProjectName(input.ClientProjectName)
		if err != nil {
			return CreateWorkflowRunResult{}, err
		}
	} else if strings.TrimSpace(input.ClientProjectName) != "" {
		return CreateWorkflowRunResult{}, fmt.Errorf("invalid web workflow scope")
	}
	if err := s.ExpireStale(ctx); err != nil {
		return CreateWorkflowRunResult{}, err
	}

	var result CreateWorkflowRunResult
	err = retryWorkflowWrite(ctx, func() error {
		result = CreateWorkflowRunResult{}
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := validateWorkflowRunScope(tx, input); err != nil {
				return err
			}

			var inheritedSource *models.WorkflowRun
			sourceSuperseded := false
			if input.InheritReservationsFromRunID != nil {
				source, err := loadWorkflowRunForReservationInheritance(tx, input)
				if err != nil {
					return err
				}
				if isWorkflowRunTerminal(source.Status) {
					// A terminal source has no pending reservations to inherit; ignore the request.
					inheritedSource = nil
				} else {
					inheritedSource = &source
					if isWorkflowRunActive(source.Status) {
						sourceSuperseded = true
						now := s.now()
						source.Status = workflowRunStatusCancelled
						source.CancelledAt = &now
						source.LeaseExpiresAt = nil
						if err := tx.Save(&source).Error; err != nil {
							return err
						}
					}
				}
			}

			var targetRun models.WorkflowRun

			active, err := loadActiveWorkflowRun(tx, input)
			if err == nil {
				if err := reuseWorkflowRun(tx, active, input, &result); err != nil {
					return err
				}
				targetRun = result.Run
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			} else {
				now := s.now()
				run := models.WorkflowRun{
					UserID:            input.UserID,
					ProjectID:         input.ProjectID,
					ClientType:        input.ClientType,
					ClientTaskID:      input.ClientTaskID,
					ClientProjectName: input.ClientProjectName,
					InputHash:         input.InputHash,
					Status:            workflowRunStatusRunning,
					BillingStatus:     workflowRunBillingStatusPending,
					ClientInstanceID:  input.ClientInstanceID,
					LastActiveAt:      now,
					ExpiresAt:         now.Add(workflowRunInactivityDuration),
				}
				if input.ClientInstanceID != "" {
					leaseExpiresAt := now.Add(workflowRunLeaseDuration)
					run.LeaseExpiresAt = &leaseExpiresAt
				}
				created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&run)
				if created.Error != nil {
					return created.Error
				}
				if created.RowsAffected == 0 {
					active, err = loadActiveWorkflowRun(tx, input)
					if err != nil {
						return err
					}
					if err := reuseWorkflowRun(tx, active, input, &result); err != nil {
						return err
					}
					targetRun = result.Run
				} else {
					targetRun = run
					result = CreateWorkflowRunResult{Run: run}
				}
			}

			if inheritedSource != nil {
				if err := migrateWorkflowRunReservationsTx(tx, inheritedSource.ID, targetRun.ID); err != nil {
					return err
				}
				if sourceSuperseded && s.hooks.AfterCancel != nil {
					if err := s.hooks.AfterCancel(ctx, tx, inheritedSource); err != nil {
						return err
					}
				}
			}
			if input.ClientType == "pc" && input.ClientTaskID != nil {
				if err := reviveReleasedTaskReservationsTx(tx, input.UserID, *input.ClientTaskID, targetRun.ID); err != nil {
					return err
				}
			}
			return nil
		})
	})
	return result, err
}

func loadActiveWorkflowRun(tx *gorm.DB, input CreateWorkflowRunInput) (models.WorkflowRun, error) {
	var active models.WorkflowRun
	query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("status IN ?", workflowRunActiveStatuses)
	if input.ClientType == "web" {
		query = query.Where("client_type = ? AND project_id = ?", "web", *input.ProjectID)
	} else {
		query = query.Where("user_id = ? AND client_type = ? AND client_task_id = ?", input.UserID, "pc", *input.ClientTaskID)
	}
	err := query.Take(&active).Error
	return active, err
}

func reuseWorkflowRun(tx *gorm.DB, active models.WorkflowRun, input CreateWorkflowRunInput, result *CreateWorkflowRunResult) error {
	if active.InputHash != input.InputHash {
		return ErrWorkflowRunScopeConflict
	}
	if active.ClientType == "pc" && active.ClientProjectName == "" && input.ClientProjectName != "" {
		if err := tx.Model(&active).Update("client_project_name", input.ClientProjectName).Error; err != nil {
			return err
		}
		active.ClientProjectName = input.ClientProjectName
	}
	*result = CreateWorkflowRunResult{Run: active, Reused: true}
	return nil
}

func sanitizeWorkflowClientProjectName(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || utf8.RuneCountInString(trimmed) > 200 {
		return "", ErrWorkflowIdentityInvalid
	}
	return trimmed, nil
}

func validateWorkflowRunScope(tx *gorm.DB, input CreateWorkflowRunInput) error {
	switch input.ClientType {
	case "web":
		if input.ProjectID == nil || input.ClientTaskID != nil || input.ClientProjectName != "" {
			return fmt.Errorf("invalid web workflow scope")
		}
		var count int64
		if err := tx.Model(&models.Project{}).Where("id = ? AND owner_id = ?", *input.ProjectID, input.UserID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return ErrWorkflowRunNotFound
		}
	case "pc":
		if input.ProjectID != nil || input.ClientTaskID == nil || *input.ClientTaskID == "" || input.ClientProjectName == "" {
			return fmt.Errorf("invalid pc workflow scope")
		}
	default:
		return fmt.Errorf("invalid workflow client type")
	}
	if input.InputHash == "" {
		return fmt.Errorf("workflow input hash is required")
	}
	return nil
}

func (s *WorkflowRunStateService) Get(ctx context.Context, userID, runID uuid.UUID) (models.WorkflowRun, []models.WorkflowStepExecution, error) {
	if err := s.ExpireStale(ctx); err != nil {
		return models.WorkflowRun{}, nil, err
	}
	var run models.WorkflowRun
	if err := s.db.WithContext(ctx).Where("id = ? AND user_id = ?", runID, userID).Take(&run).Error; err != nil {
		return models.WorkflowRun{}, nil, workflowRunLookupError(err)
	}
	var steps []models.WorkflowStepExecution
	if err := s.db.WithContext(ctx).Where("workflow_run_id = ?", run.ID).Order("created_at ASC").Find(&steps).Error; err != nil {
		return models.WorkflowRun{}, nil, err
	}
	return run, steps, nil
}

func (s *WorkflowRunStateService) Heartbeat(ctx context.Context, userID, runID uuid.UUID, input WorkflowRunHeartbeatInput) (models.WorkflowRun, error) {
	var err error
	input.ClientInstanceID, err = sanitizeWorkflowClientInstanceID(input.ClientInstanceID, true)
	if err != nil {
		return models.WorkflowRun{}, err
	}
	if input.CurrentStepID != nil {
		currentStepID, err := sanitizeWorkflowStepID(*input.CurrentStepID, false)
		if err != nil {
			return models.WorkflowRun{}, err
		}
		if currentStepID == "" {
			input.CurrentStepID = nil
		} else {
			input.CurrentStepID = &currentStepID
		}
	}
	if err := s.ExpireStale(ctx); err != nil {
		return models.WorkflowRun{}, err
	}
	return s.updateOwnedRun(ctx, userID, runID, func(tx *gorm.DB, run *models.WorkflowRun, now time.Time) error {
		if isWorkflowRunTerminal(run.Status) {
			return ErrWorkflowRunTerminal
		}
		if workflowLeaseOwnedByOther(run, input.ClientInstanceID, now) {
			return ErrWorkflowRunLeaseConflict
		}
		run.ClientInstanceID = input.ClientInstanceID
		leaseExpiresAt := now.Add(workflowRunLeaseDuration)
		run.LeaseExpiresAt = timePointer(leaseExpiresAt)
		run.LastActiveAt = now
		run.ExpiresAt = now.Add(workflowRunInactivityDuration)
		if input.CurrentStepID != nil {
			run.CurrentStepID = input.CurrentStepID
			// Clients report the foreground step, while billable requests can run
			// as child steps (for example validate-subtitles.chunk-6.attempt-1).
			// Keep those live, owned leases with the parent. Match the literal dot
			// boundary rather than LIKE, since valid IDs can contain underscores.
			childPrefix := *input.CurrentStepID + "."
			if err := tx.Model(&models.WorkflowStepExecution{}).
				Where("workflow_run_id = ? AND status = ? AND lease_owner = ? AND lease_expires_at > ?", run.ID, workflowRunStatusRunning, input.ClientInstanceID, now).
				Where("(step_id = ? OR substr(step_id, 1, ?) = ?)", *input.CurrentStepID, len(childPrefix), childPrefix).
				Update("lease_expires_at", leaseExpiresAt).Error; err != nil {
				return err
			}
		}
		if err := tx.Save(run).Error; err != nil {
			return err
		}
		return s.runAfterActivityHook(ctx, tx, run)
	})
}

func (s *WorkflowRunStateService) Resume(ctx context.Context, userID, runID uuid.UUID, clientInstanceID string) (models.WorkflowRun, error) {
	if err := s.ExpireStale(ctx); err != nil {
		return models.WorkflowRun{}, err
	}
	clientInstanceID, err := sanitizeWorkflowClientInstanceID(clientInstanceID, true)
	if err != nil {
		return models.WorkflowRun{}, err
	}
	return s.updateOwnedRun(ctx, userID, runID, func(tx *gorm.DB, run *models.WorkflowRun, now time.Time) error {
		if isWorkflowRunTerminal(run.Status) || run.Status == workflowRunStatusFinalizing {
			return ErrWorkflowRunTerminal
		}
		if run.Status == workflowRunStatusInterrupted || run.Status == workflowRunStatusBlocked {
			run.Status = workflowRunStatusRunning
		}
		run.ClientInstanceID = clientInstanceID
		leaseExpiresAt := now.Add(workflowRunLeaseDuration)
		run.LeaseExpiresAt = timePointer(leaseExpiresAt)
		run.LastActiveAt = now
		run.ExpiresAt = now.Add(workflowRunInactivityDuration)
		if err := tx.Model(&models.WorkflowStepExecution{}).
			Where("workflow_run_id = ? AND lease_owner IS NOT NULL AND lease_expires_at > ?", run.ID, now).
			Updates(map[string]any{"lease_owner": clientInstanceID, "lease_expires_at": leaseExpiresAt}).Error; err != nil {
			return err
		}
		if err := tx.Save(run).Error; err != nil {
			return err
		}
		return s.runAfterActivityHook(ctx, tx, run)
	})
}

func (s *WorkflowRunStateService) Interrupt(ctx context.Context, userID, runID uuid.UUID, input WorkflowRunInterruptInput) (models.WorkflowRun, error) {
	var err error
	input.ClientInstanceID, err = sanitizeWorkflowClientInstanceID(input.ClientInstanceID, true)
	if err != nil {
		return models.WorkflowRun{}, err
	}
	return s.updateOwnedRun(ctx, userID, runID, func(tx *gorm.DB, run *models.WorkflowRun, now time.Time) error {
		if isWorkflowRunTerminal(run.Status) {
			return ErrWorkflowRunTerminal
		}
		if workflowLeaseOwnedByOther(run, input.ClientInstanceID, now) {
			return ErrWorkflowRunLeaseConflict
		}
		var err error
		input.ErrorCode, input.ErrorMessage, err = sanitizeWorkflowDiagnostics(input.ErrorCode, input.ErrorMessage)
		if err != nil {
			return err
		}
		run.Status = workflowRunStatusInterrupted
		run.LastErrorCode = input.ErrorCode
		run.LastErrorMessage = input.ErrorMessage
		run.LastActiveAt = now
		run.ExpiresAt = now.Add(workflowRunInactivityDuration)
		run.LeaseExpiresAt = nil
		if err := tx.Save(run).Error; err != nil {
			return err
		}
		return s.runAfterActivityHook(ctx, tx, run)
	})
}

func (s *WorkflowRunStateService) Complete(ctx context.Context, userID, runID uuid.UUID, finalResultAt time.Time) (models.WorkflowRun, error) {
	if err := s.ExpireStale(ctx); err != nil {
		return models.WorkflowRun{}, err
	}
	return s.updateOwnedRun(ctx, userID, runID, func(tx *gorm.DB, run *models.WorkflowRun, now time.Time) error {
		if run.Status == workflowRunStatusSucceeded {
			return nil
		}
		if isWorkflowRunTerminal(run.Status) {
			return ErrWorkflowRunTerminal
		}
		finalResultAt = finalResultAt.UTC()
		run.FinalResultAt = &finalResultAt
		run.CompletedAt = &now
		run.Status = workflowRunStatusSucceeded
		run.LeaseExpiresAt = nil
		if err := tx.Save(run).Error; err != nil {
			return err
		}
		if s.hooks.AfterComplete != nil {
			return s.hooks.AfterComplete(ctx, tx, run)
		}
		return nil
	})
}

func (s *WorkflowRunStateService) Cancel(ctx context.Context, userID, runID uuid.UUID) (models.WorkflowRun, error) {
	if err := s.ExpireStale(ctx); err != nil {
		return models.WorkflowRun{}, err
	}
	return s.updateOwnedRun(ctx, userID, runID, func(tx *gorm.DB, run *models.WorkflowRun, now time.Time) error {
		if run.Status == workflowRunStatusCancelled {
			return nil
		}
		if run.FinalResultAt != nil || run.Status == workflowRunStatusSucceeded {
			return ErrWorkflowFinalResultExists
		}
		if run.Status == workflowRunStatusExpired {
			return ErrWorkflowRunTerminal
		}
		run.Status = workflowRunStatusCancelled
		run.CancelledAt = &now
		run.LeaseExpiresAt = nil
		if err := tx.Save(run).Error; err != nil {
			return err
		}
		if s.hooks.AfterCancel != nil {
			return s.hooks.AfterCancel(ctx, tx, run)
		}
		return nil
	})
}

func (s *WorkflowRunStateService) UpsertStep(ctx context.Context, userID, runID uuid.UUID, input WorkflowStepUpdateInput) (models.WorkflowStepExecution, error) {
	var err error
	input.StepID, err = sanitizeWorkflowStepID(input.StepID, true)
	if err != nil {
		return models.WorkflowStepExecution{}, err
	}
	input.InputHash, err = sanitizeWorkflowInputHash(input.InputHash)
	if err != nil {
		return models.WorkflowStepExecution{}, err
	}
	input.ClientInstanceID, err = sanitizeWorkflowClientInstanceID(input.ClientInstanceID, true)
	if err != nil {
		return models.WorkflowStepExecution{}, err
	}
	if err := s.ExpireStale(ctx); err != nil {
		return models.WorkflowStepExecution{}, err
	}
	executionKey := workflowStepExecutionKey(runID, input.StepID, input.InputHash)
	var result models.WorkflowStepExecution
	err = retryWorkflowWrite(ctx, func() error {
		result = models.WorkflowStepExecution{}
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			run, err := loadOwnedWorkflowRun(tx, userID, runID)
			if err != nil {
				return err
			}
			if isWorkflowRunTerminal(run.Status) {
				return ErrWorkflowRunTerminal
			}
			now := s.now()
			if workflowLeaseOwnedByOther(&run, input.ClientInstanceID, now) {
				return ErrWorkflowRunLeaseConflict
			}
			stepInput, err := sanitizeWorkflowStepUpdate(input)
			if err != nil {
				return err
			}

			var step models.WorkflowStepExecution
			isNew := false
			if stepInput.Status == workflowRunStatusRunning {
				step = models.WorkflowStepExecution{
					WorkflowRunID: runID,
					StepID:        stepInput.StepID,
					InputHash:     stepInput.InputHash,
					ExecutionKey:  executionKey,
					Usage:         datatypes.JSON([]byte("{}")),
				}
				applyWorkflowStepUpdate(&step, stepInput, now, true)
				created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&step)
				if created.Error != nil {
					return created.Error
				}
				isNew = created.RowsAffected == 1
			}
			if !isNew {
				var existing models.WorkflowStepExecution
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("execution_key = ?", executionKey).Take(&existing).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						return ErrWorkflowStepTransitionInvalid
					}
					return err
				}
				step = existing
				if workflowStepLeaseOwnedByOther(&step, stepInput.ClientInstanceID, now) {
					return ErrWorkflowStepLeaseConflict
				}
				noOp, err := validateWorkflowStepTransition(&step, stepInput, now)
				if err != nil {
					return err
				}
				if !noOp {
					applyWorkflowStepUpdate(&step, stepInput, now, false)
					if err := tx.Save(&step).Error; err != nil {
						return err
					}
				}
			}

			run.CurrentStepID = &stepInput.StepID
			run.LastActiveAt = now
			run.ExpiresAt = now.Add(workflowRunInactivityDuration)
			if err := tx.Save(&run).Error; err != nil {
				return err
			}
			if err := s.runAfterActivityHook(ctx, tx, &run); err != nil {
				return err
			}
			result = step
			return nil
		})
	})
	return result, err
}

func (s *WorkflowRunStateService) runAfterActivityHook(ctx context.Context, tx *gorm.DB, run *models.WorkflowRun) error {
	if s.hooks.AfterActivity == nil {
		return nil
	}
	return s.hooks.AfterActivity(ctx, tx, run)
}

func applyWorkflowStepUpdate(step *models.WorkflowStepExecution, input WorkflowStepUpdateInput, now time.Time, isNew bool) {
	if input.Status == workflowRunStatusRunning {
		freshAttempt := isNew || step.Status != workflowRunStatusRunning || step.LeaseExpiresAt == nil || !step.LeaseExpiresAt.After(now)
		if freshAttempt {
			step.AttemptCount++
			step.StartedAt = &now
			step.CompletedAt = nil
		}
		owner := input.ClientInstanceID
		step.LeaseOwner = &owner
		step.LeaseExpiresAt = timePointer(now.Add(workflowRunLeaseDuration))
		step.PreRegistered = input.PreRegistered
	} else {
		step.LeaseOwner = nil
		step.LeaseExpiresAt = nil
		step.PreRegistered = false
		if input.Status == workflowRunStatusSucceeded {
			step.CompletedAt = &now
		}
	}
	step.Status = input.Status
	if input.ProviderRequestID != nil {
		step.ProviderRequestID = input.ProviderRequestID
	}
	if input.ErrorCode != nil {
		step.ErrorCode = input.ErrorCode
	}
	if input.ErrorMessage != nil {
		step.ErrorMessage = input.ErrorMessage
	}
	if len(input.Usage) > 0 {
		step.Usage = input.Usage
	}
}

func (s *WorkflowRunStateService) ExpireStale(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	return retryWorkflowWrite(ctx, func() error {
		now := s.now()
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var runs []models.WorkflowRun
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("status IN ? AND expires_at <= ?", workflowRunActiveStatuses, now).Find(&runs).Error; err != nil {
				return err
			}
			for i := range runs {
				updated := tx.Model(&models.WorkflowRun{}).
					Where("id = ? AND status IN ? AND expires_at <= ?", runs[i].ID, workflowRunActiveStatuses, now).
					Updates(map[string]any{"status": workflowRunStatusExpired, "lease_expires_at": nil})
				if updated.Error != nil {
					return updated.Error
				}
				if updated.RowsAffected == 0 {
					continue
				}
				runs[i].Status = workflowRunStatusExpired
				runs[i].LeaseExpiresAt = nil
				if s.hooks.AfterExpire != nil {
					if err := s.hooks.AfterExpire(ctx, tx, &runs[i]); err != nil {
						return err
					}
				}
			}
			return nil
		})
	})
}

func RunWorkflowRunExpirationWorker(ctx context.Context, service *WorkflowRunStateService) {
	runWorkflowRunExpirationWorker(ctx, service, workflowRunExpirationInterval)
}

func runWorkflowRunExpirationWorker(ctx context.Context, service *WorkflowRunStateService, interval time.Duration) {
	if service == nil {
		return
	}
	if interval <= 0 {
		interval = workflowRunExpirationInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := service.ExpireStale(ctx); err != nil {
				log.Printf("workflow run expiration failed: %v", err)
			}
		}
	}
}

func (s *WorkflowRunStateService) updateOwnedRun(ctx context.Context, userID, runID uuid.UUID, update func(*gorm.DB, *models.WorkflowRun, time.Time) error) (models.WorkflowRun, error) {
	var result models.WorkflowRun
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		run, err := loadOwnedWorkflowRun(tx, userID, runID)
		if err != nil {
			return err
		}
		if err := update(tx, &run, s.now()); err != nil {
			return err
		}
		result = run
		return nil
	})
	return result, err
}

func loadOwnedWorkflowRun(tx *gorm.DB, userID, runID uuid.UUID) (models.WorkflowRun, error) {
	var run models.WorkflowRun
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", runID, userID).Take(&run).Error; err != nil {
		return models.WorkflowRun{}, workflowRunLookupError(err)
	}
	return run, nil
}

func workflowRunLookupError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrWorkflowRunNotFound
	}
	return err
}

func workflowLeaseOwnedByOther(run *models.WorkflowRun, clientInstanceID string, now time.Time) bool {
	return run.ClientInstanceID != "" && run.ClientInstanceID != clientInstanceID && run.LeaseExpiresAt != nil && run.LeaseExpiresAt.After(now)
}

func workflowStepLeaseOwnedByOther(step *models.WorkflowStepExecution, clientInstanceID string, now time.Time) bool {
	return step.LeaseOwner != nil && *step.LeaseOwner != clientInstanceID && step.LeaseExpiresAt != nil && step.LeaseExpiresAt.After(now)
}

func workflowStepLeaseOwnedByClient(step *models.WorkflowStepExecution, clientInstanceID string, now time.Time) bool {
	return step.LeaseOwner != nil && *step.LeaseOwner == clientInstanceID && step.LeaseExpiresAt != nil && step.LeaseExpiresAt.After(now)
}

func retryWorkflowWrite(ctx context.Context, operation func() error) error {
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		err = operation()
		if err == nil || !isRetryableWorkflowWriteError(err) {
			return err
		}
		baseDelay := time.Duration(1<<attempt) * time.Millisecond
		if baseDelay > 50*time.Millisecond {
			baseDelay = 50 * time.Millisecond
		}
		delay := baseDelay + time.Duration(rand.Int64N(int64(baseDelay)+1))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return err
}

func isRetryableWorkflowWriteError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked") ||
		strings.Contains(message, "deadlock detected") ||
		strings.Contains(message, "sqlstate 40001") ||
		strings.Contains(message, "sqlstate 40p01")
}

func workflowStepExecutionKey(runID uuid.UUID, stepID, inputHash string) string {
	sum := sha256.Sum256([]byte(runID.String() + "|" + stepID + "|" + inputHash))
	return hex.EncodeToString(sum[:])
}

func isWorkflowRunActive(status string) bool {
	switch status {
	case workflowRunStatusRunning, workflowRunStatusInterrupted, workflowRunStatusBlocked, workflowRunStatusFinalizing:
		return true
	}
	return false
}

func loadWorkflowRunForReservationInheritance(tx *gorm.DB, input CreateWorkflowRunInput) (models.WorkflowRun, error) {
	var run models.WorkflowRun
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND user_id = ?", *input.InheritReservationsFromRunID, input.UserID).
		Take(&run).Error; err != nil {
		return models.WorkflowRun{}, workflowRunLookupError(err)
	}
	if run.ClientType != input.ClientType {
		return models.WorkflowRun{}, ErrWorkflowReservationInheritanceInvalid
	}
	switch input.ClientType {
	case "pc":
		if strings.TrimSpace(run.ClientProjectName) == "" ||
			strings.TrimSpace(run.ClientProjectName) != strings.TrimSpace(input.ClientProjectName) {
			return models.WorkflowRun{}, ErrWorkflowReservationInheritanceInvalid
		}
	case "web":
		if !uuidPointersEqual(run.ProjectID, input.ProjectID) {
			return models.WorkflowRun{}, ErrWorkflowReservationInheritanceInvalid
		}
	}
	return run, nil
}

func isWorkflowRunTerminal(status string) bool {
	return status == workflowRunStatusSucceeded || status == workflowRunStatusCancelled || status == workflowRunStatusExpired
}

func timePointer(value time.Time) *time.Time { return &value }
