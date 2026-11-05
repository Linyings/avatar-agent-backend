package server

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const billingCurrency = "CNY"
const creditReservationTTL = 24 * time.Hour

const (
	creditReservationPending   = "pending"
	creditReservationConfirmed = "confirmed"
	creditReservationReleased  = "released"
	creditReservationExpired   = "expired"
)

var ErrCreditReservationConflict = fmt.Errorf("credit reservation identity conflict")
var ErrInsufficientCredits = fmt.Errorf("insufficient credits")

const workflowInputFingerprintMetadataKey = "input_fingerprint"

type creditConsumption struct {
	BillingMode             string // Server-resolved workflow mode.
	DubbingTTS              bool   // Server-resolved PC dubbing step, never accepted from request JSON.
	UserID                  uuid.UUID
	ProjectID               *uuid.UUID
	WorkflowRunID           *uuid.UUID
	WorkflowStepExecutionID *uuid.UUID
	ExecutionKey            string
	CreditCount             int64
	APIEndpoint             string
	Provider                string
	ServiceKind             string
	ModelID                 string
	Usage                   providers.ChatUsage
	UTF8Bytes               int64
	CharacterCount          int64
	DurationSeconds         *float64
	RequestMetadata         map[string]any
}

type billingRate struct {
	Provider              string
	ServiceKind           string
	ModelID               string
	InputCostPerMillion   float64
	OutputCostPerMillion  float64
	CostPerMinute         float64
	CostPerMillionUTF8    float64
	CostPerMillionChars   float64
	InputTokensPerCredit  float64
	OutputTokensPerCredit float64
	TokensPerCredit       float64
	CreditsPerUTF8Byte    float64
	CreditsPerChar        float64
	CreditsPerMinute      float64
	CreditsPerSecond      float64
}

type billingAmounts struct {
	Mode           string
	Promotion      bool
	CreditCount    int64
	CostAmount     float64
	RevenueAmount  float64
	ProfitAmount   float64
	MissingPricing bool
	Rate           *billingRate
}

type workflowCreditReservationLocks struct {
	Run  *models.WorkflowRun
	Step *models.WorkflowStepExecution
}

func (a *App) billCredits(consumption creditConsumption) error {
	if consumption.ProjectID != nil || consumption.WorkflowRunID != nil {
		return a.reserveCredits(consumption)
	}
	return a.consumeCredits(consumption)
}

func (a *App) consumeCredits(consumption creditConsumption) error {
	consumption = normalizeCreditConsumption(consumption)
	amounts, err := a.billingAmountsForConsumption(consumption)
	if err != nil {
		return err
	}
	if amounts.CreditCount <= 0 && !amounts.Promotion {
		return nil
	}

	return a.db.Transaction(func(tx *gorm.DB) error {
		var account models.UserTokenAccount
		if err := tx.FirstOrCreate(&account, models.UserTokenAccount{UserID: consumption.UserID}).Error; err != nil {
			return err
		}

		recordedAt := time.Now().UTC()
		if err := a.ensureDueSubscriptionCreditsTx(tx, &consumption.UserID, recordedAt); err != nil {
			return err
		}
		var startingSubscriptionSums struct {
			Total int64
			Used  int64
		}
		if err := tx.Model(&models.SubscriptionCreditGrant{}).
			Where("user_id = ? AND status = ? AND expires_at > ?", consumption.UserID, "active", recordedAt).
			Select("coalesce(sum(credit_amount), 0) as total, coalesce(sum(used_amount), 0) as used").
			Scan(&startingSubscriptionSums).Error; err != nil {
			return err
		}
		purchasedLotBalance, _, err := purchasedCreditBalance(tx, consumption.UserID, recordedAt)
		if err != nil {
			return err
		}
		startingBalance := account.RemainingTokens() + purchasedLotBalance + maxInt64(0, startingSubscriptionSums.Total-startingSubscriptionSums.Used)
		if startingBalance < amounts.CreditCount {
			return fmt.Errorf("Credits余额不足（需 %d，剩余 %d）", amounts.CreditCount, startingBalance)
		}

		subscriptionConsumed, err := a.consumeSubscriptionCreditsTx(tx, consumption.UserID, amounts.CreditCount, recordedAt)
		if err != nil {
			return err
		}
		purchasedConsumed := amounts.CreditCount - subscriptionConsumed
		lotConsumed, err := consumePurchasedCreditLots(tx, consumption.UserID, purchasedConsumed, recordedAt)
		if err != nil {
			return err
		}
		legacyPurchasedConsumed := purchasedConsumed - lotConsumed
		paidPurchasedConsumed := purchasedConsumed

		if paidPurchasedConsumed > 0 && account.TotalTokens > 0 {
			amounts.RevenueAmount = float64(paidPurchasedConsumed) * account.TotalRecharged / float64(account.TotalTokens)
		}
		amounts.ProfitAmount = amounts.RevenueAmount - amounts.CostAmount

		if legacyPurchasedConsumed > 0 {
			result := tx.Model(&models.UserTokenAccount{}).
				Where("user_id = ?", consumption.UserID).
				UpdateColumn("used_tokens", gorm.Expr("used_tokens + ?", legacyPurchasedConsumed))
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return fmt.Errorf("Credits账户不存在")
			}
		}

		metadata := cloneMetadata(consumption.RequestMetadata)
		metadata["product_mode"] = amounts.Mode
		metadata["free_experience"] = amounts.Promotion
		metadata["billing_missing_rate"] = amounts.MissingPricing
		metadata["billing_provider"] = consumption.Provider
		metadata["billing_service_kind"] = consumption.ServiceKind
		metadata["billing_model_id"] = consumption.ModelID
		metadata["billing_currency"] = billingCurrency
		metadata["subscription_credits_consumed"] = subscriptionConsumed
		metadata["purchased_credits_consumed"] = purchasedConsumed
		metadata["paid_purchased_credits_consumed"] = paidPurchasedConsumed
		metadata["overdraft_credits_consumed"] = maxInt64(0, purchasedConsumed-paidPurchasedConsumed)
		if consumption.UTF8Bytes > 0 {
			metadata["utf8_bytes"] = consumption.UTF8Bytes
		}
		if consumption.CharacterCount > 0 {
			metadata["character_count"] = consumption.CharacterCount
		}
		if consumption.ServiceKind == "tts" {
			if amounts.Rate != nil && amounts.Rate.CostPerMillionChars > 0 {
				metadata["billing_cost_unit"] = "million_chars"
			} else {
				metadata["billing_cost_unit"] = "million_utf8_bytes"
			}
		}
		if amounts.Rate != nil {
			metadata["billing_rate"] = map[string]any{
				"provider":                    amounts.Rate.Provider,
				"service_kind":                amounts.Rate.ServiceKind,
				"model_id":                    amounts.Rate.ModelID,
				"currency":                    billingCurrency,
				"input_cost_per_million":      amounts.Rate.InputCostPerMillion,
				"output_cost_per_million":     amounts.Rate.OutputCostPerMillion,
				"cost_per_minute":             amounts.Rate.CostPerMinute,
				"cost_per_million_utf8_bytes": amounts.Rate.CostPerMillionUTF8,
				"cost_per_million_chars":      amounts.Rate.CostPerMillionChars,
				"input_tokens_per_credit":     amounts.Rate.InputTokensPerCredit,
				"output_tokens_per_credit":    amounts.Rate.OutputTokensPerCredit,
				"tokens_per_credit":           amounts.Rate.TokensPerCredit,
				"credits_per_1k_utf8_bytes":   amounts.Rate.CreditsPerUTF8Byte * 1000,
				"credits_per_1k_chars":        amounts.Rate.CreditsPerChar * 1000,
				"credits_per_minute":          amounts.Rate.CreditsPerMinute,
				"credits_per_second":          amounts.Rate.CreditsPerSecond,
			}
		}

		record := models.TokenUsageRecord{
			UserID:           consumption.UserID,
			ProjectID:        consumption.ProjectID,
			TokenCount:       amounts.CreditCount,
			CreditCount:      amounts.CreditCount,
			APIEndpoint:      consumption.APIEndpoint,
			ModelName:        consumption.ModelID,
			ModelID:          consumption.ModelID,
			Provider:         consumption.Provider,
			ServiceKind:      consumption.ServiceKind,
			PromptTokens:     consumption.Usage.PromptTokens,
			CompletionTokens: consumption.Usage.CompletionTokens,
			TotalModelTokens: consumption.Usage.TotalTokens,
			UTF8Bytes:        consumption.UTF8Bytes,
			DurationSeconds:  consumption.DurationSeconds,
			BillingMonth:     time.Now().Format("2006-01"),
			RequestMetadata:  jsonMap(metadata),
			CostAmount:       ptrFloat64(amounts.CostAmount),
			RevenueAmount:    ptrFloat64(amounts.RevenueAmount),
			ProfitAmount:     ptrFloat64(amounts.ProfitAmount),
			MissingPricing:   amounts.MissingPricing,
			RecordedAt:       recordedAt,
		}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}

		var updatedAccount models.UserTokenAccount
		if err := tx.First(&updatedAccount, "user_id = ?", consumption.UserID).Error; err != nil {
			return err
		}
		var subscriptionSums struct {
			Total int64
			Used  int64
		}
		if err := tx.Model(&models.SubscriptionCreditGrant{}).
			Where("user_id = ? AND status = ? AND expires_at > ?", consumption.UserID, "active", recordedAt).
			Select("coalesce(sum(credit_amount), 0) as total, coalesce(sum(used_amount), 0) as used").
			Scan(&subscriptionSums).Error; err != nil {
			return err
		}
		balanceAfter := updatedAccount.RemainingTokens() + maxInt64(0, subscriptionSums.Total-subscriptionSums.Used)
		return a.createCreditLedgerRecord(tx, creditLedgerEntry{
			UserID:          consumption.UserID,
			ProjectID:       consumption.ProjectID,
			EventType:       creditLedgerEventConsume,
			Item:            creditLedgerConsumeItemLabelForServiceKind(consumption.ServiceKind),
			ChangeAmount:    -amounts.CreditCount,
			BalanceAfter:    balanceAfter,
			Reason:          consumption.APIEndpoint,
			RequestMetadata: metadata,
			RecordedAt:      recordedAt,
		})
	})
}

func (a *App) reserveCredits(consumption creditConsumption) error {
	if consumption.ProjectID == nil && consumption.WorkflowRunID == nil {
		return a.consumeCredits(consumption)
	}
	return retryWorkflowWrite(context.Background(), func() error {
		return a.db.Transaction(func(tx *gorm.DB) error {
			return a.reserveCreditsTx(context.Background(), tx, consumption, nil)
		})
	})
}

func (a *App) reserveCreditsTx(ctx context.Context, tx *gorm.DB, consumption creditConsumption, locked *workflowCreditReservationLocks) error {
	consumption = normalizeCreditConsumption(consumption)
	if consumption.ProjectID == nil && consumption.WorkflowRunID == nil {
		return fmt.Errorf("transaction-scoped credit reservation requires a project or workflow run")
	}
	if consumption.WorkflowRunID != nil {
		consumption.ExecutionKey = strings.TrimSpace(consumption.ExecutionKey)
		if consumption.ExecutionKey == "" || consumption.WorkflowStepExecutionID == nil {
			return fmt.Errorf("workflow credit reservation requires execution identity")
		}
	}
	amounts, err := a.billingAmountsForConsumption(consumption)
	if err != nil {
		return err
	}
	if amounts.CreditCount <= 0 && !amounts.Promotion {
		return nil
	}

	recordedAt := time.Now().UTC()
	var workflowRun *models.WorkflowRun
	if consumption.WorkflowRunID != nil {
		if locked == nil {
			run, step, err := validateWorkflowCreditConsumptionTx(tx, consumption)
			if err != nil {
				return err
			}
			locked = &workflowCreditReservationLocks{Run: &run, Step: &step}
		} else if err := validateLockedWorkflowCreditConsumption(consumption, locked); err != nil {
			return err
		}
		workflowRun = locked.Run
		if !workflowRunRejectsNewReservations(workflowRun.Status) {
			if err := a.extendWorkflowRunReservationsTx(ctx, tx, workflowRun); err != nil {
				return err
			}
		}
	}
	if err := a.expireCreditReservationsTx(tx, &consumption.UserID, recordedAt); err != nil {
		return err
	}
	if err := a.ensureDueSubscriptionCreditsTx(tx, &consumption.UserID, recordedAt); err != nil {
		return err
	}

	idempotencyKey := creditReservationIdempotencyKey(consumption)
	var existing models.CreditReservation
	result := tx.Where("idempotency_key = ?", idempotencyKey).First(&existing)
	if result.Error == nil {
		if workflowRun != nil && workflowRunRejectsNewReservations(workflowRun.Status) {
			if workflowRun.Status == workflowRunStatusSucceeded && existing.Status == creditReservationConfirmed {
				return existingCreditReservationResult(existing, consumption, amounts)
			}
			return ErrWorkflowRunTerminal
		}
		return existingCreditReservationResult(existing, consumption, amounts)
	}
	if result.Error != nil && result.Error != gorm.ErrRecordNotFound {
		return result.Error
	}
	if workflowRun != nil && workflowRunRejectsNewReservations(workflowRun.Status) {
		return ErrWorkflowRunTerminal
	}

	available, err := a.availableCreditsTx(tx, consumption.UserID, recordedAt)
	if err != nil {
		return err
	}
	if available < amounts.CreditCount {
		return fmt.Errorf("%w: Credits余额不足（需 %d，剩余 %d）", ErrInsufficientCredits, amounts.CreditCount, available)
	}

	metadata := billingMetadataForConsumption(consumption, amounts, 0, 0, 0)
	expiresAt := recordedAt.Add(creditReservationTTL)
	if workflowRun != nil && workflowRun.ExpiresAt.After(expiresAt) {
		expiresAt = workflowRun.ExpiresAt
	}
	reservation := models.CreditReservation{
		UserID:                  consumption.UserID,
		ProjectID:               consumption.ProjectID,
		WorkflowRunID:           consumption.WorkflowRunID,
		WorkflowStepExecutionID: consumption.WorkflowStepExecutionID,
		APIEndpoint:             consumption.APIEndpoint,
		Provider:                consumption.Provider,
		ServiceKind:             consumption.ServiceKind,
		ModelID:                 consumption.ModelID,
		CreditCount:             amounts.CreditCount,
		PromptTokens:            consumption.Usage.PromptTokens,
		CompletionTokens:        consumption.Usage.CompletionTokens,
		TotalModelTokens:        consumption.Usage.TotalTokens,
		UTF8Bytes:               consumption.UTF8Bytes,
		CharacterCount:          consumption.CharacterCount,
		DurationSeconds:         consumption.DurationSeconds,
		BillingMonth:            recordedAt.Format("2006-01"),
		RequestMetadata:         jsonMap(metadata),
		CostAmount:              ptrFloat64(amounts.CostAmount),
		MissingPricing:          amounts.MissingPricing,
		Status:                  creditReservationPending,
		IdempotencyKey:          idempotencyKey,
		ExpiresAt:               expiresAt,
		RecordedAt:              recordedAt,
	}
	created := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "idempotency_key"}},
		DoNothing: true,
	}).Create(&reservation)
	if created.Error != nil {
		return created.Error
	}
	if created.RowsAffected > 0 {
		return nil
	}
	if err := tx.Where("idempotency_key = ?", idempotencyKey).First(&existing).Error; err != nil {
		return err
	}
	return existingCreditReservationResult(existing, consumption, amounts)
}

func (a *App) confirmProjectReservations(projectID uuid.UUID) error {
	return a.db.Transaction(func(tx *gorm.DB) error {
		return a.confirmProjectReservationsTx(tx, projectID)
	})
}

func (a *App) confirmProjectReservationsTx(tx *gorm.DB, projectID uuid.UUID) error {
	return a.confirmCreditReservationsTx(tx, creditReservationConfirmationScope{
		where:           "project_id = ? AND workflow_run_id IS NULL AND status = ?",
		args:            []any{projectID, creditReservationPending},
		ledgerProjectID: &projectID,
		billingMode:     "workflow_reservation_confirm",
		reason:          "workflow_completed",
	})
}

type creditReservationConfirmationScope struct {
	where             string
	args              []any
	ledgerProjectID   *uuid.UUID
	ledgerProjectName string
	workflowRunID     *uuid.UUID
	billingMode       string
	reason            string
}

func (a *App) confirmCreditReservationsTx(tx *gorm.DB, scope creditReservationConfirmationScope) error {
	now := time.Now().UTC()
	if err := a.expireCreditReservationsTx(tx, nil, now); err != nil {
		return err
	}

	var reservations []models.CreditReservation
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where(scope.where, scope.args...).
		Order("recorded_at ASC, created_at ASC").
		Find(&reservations).Error; err != nil {
		return err
	}
	if len(reservations) == 0 {
		return nil
	}

	userID := reservations[0].UserID
	totalCredits := int64(0)
	totalCost := 0.0
	serviceBreakdown := map[string]int64{}
	endpointBreakdown := map[string]int64{}
	reservationIDs := make([]string, 0, len(reservations))
	for _, reservation := range reservations {
		if reservation.UserID != userID {
			return fmt.Errorf("项目存在跨用户 Credits 预留，无法确认扣费")
		}
		totalCredits += reservation.CreditCount
		if reservation.CostAmount != nil {
			totalCost += *reservation.CostAmount
		}
		serviceBreakdown[reservation.ServiceKind] += reservation.CreditCount
		endpointBreakdown[reservation.APIEndpoint] += reservation.CreditCount
		reservationIDs = append(reservationIDs, reservation.ID.String())
	}

	var account models.UserTokenAccount
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).FirstOrCreate(&account, models.UserTokenAccount{UserID: userID}).Error; err != nil {
		return err
	}
	if err := a.ensureDueSubscriptionCreditsTx(tx, &userID, now); err != nil {
		return err
	}
	gross, err := a.grossRemainingCreditsTx(tx, userID, now)
	if err != nil {
		return err
	}
	if gross < totalCredits {
		return fmt.Errorf("Credits余额不足（需 %d，剩余 %d）", totalCredits, gross)
	}

	subscriptionConsumed, err := a.consumeSubscriptionCreditsTx(tx, userID, totalCredits, now)
	if err != nil {
		return err
	}
	purchasedConsumed := totalCredits - subscriptionConsumed
	lotConsumed, err := consumePurchasedCreditLots(tx, userID, purchasedConsumed, now)
	if err != nil {
		return err
	}
	legacyPurchasedConsumed := purchasedConsumed - lotConsumed
	paidPurchasedConsumed := purchasedConsumed
	revenueAmount := 0.0
	if paidPurchasedConsumed > 0 && account.TotalTokens > 0 {
		revenueAmount = float64(paidPurchasedConsumed) * account.TotalRecharged / float64(account.TotalTokens)
	}
	if legacyPurchasedConsumed > 0 {
		result := tx.Model(&models.UserTokenAccount{}).
			Where("user_id = ?", userID).
			UpdateColumn("used_tokens", gorm.Expr("used_tokens + ?", legacyPurchasedConsumed))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("Credits账户不存在")
		}
	}

	for _, reservation := range reservations {
		share := 0.0
		if totalCredits > 0 {
			share = float64(reservation.CreditCount) / float64(totalCredits)
		}
		record := models.TokenUsageRecord{
			UserID:           reservation.UserID,
			ProjectID:        reservation.ProjectID,
			TokenCount:       reservation.CreditCount,
			CreditCount:      reservation.CreditCount,
			APIEndpoint:      reservation.APIEndpoint,
			ModelName:        reservation.ModelID,
			ModelID:          reservation.ModelID,
			Provider:         reservation.Provider,
			ServiceKind:      reservation.ServiceKind,
			PromptTokens:     reservation.PromptTokens,
			CompletionTokens: reservation.CompletionTokens,
			TotalModelTokens: reservation.TotalModelTokens,
			UTF8Bytes:        reservation.UTF8Bytes,
			DurationSeconds:  reservation.DurationSeconds,
			BillingMonth:     reservation.BillingMonth,
			RequestMetadata:  reservation.RequestMetadata,
			CostAmount:       reservation.CostAmount,
			RevenueAmount:    ptrFloat64(roundMoney(revenueAmount * share)),
			ProfitAmount:     ptrFloat64(roundMoney(revenueAmount*share - floatValueFromPtr(reservation.CostAmount))),
			MissingPricing:   reservation.MissingPricing,
			RecordedAt:       reservation.RecordedAt,
		}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
	}

	var updatedAccount models.UserTokenAccount
	if err := tx.First(&updatedAccount, "user_id = ?", userID).Error; err != nil {
		return err
	}
	balanceAfter, err := a.grossRemainingCreditsTx(tx, userID, now)
	if err != nil {
		return err
	}
	metadata := map[string]any{
		"billing_mode":                    scope.billingMode,
		"reservation_ids":                 reservationIDs,
		"reservation_count":               len(reservations),
		"service_breakdown":               serviceBreakdown,
		"endpoint_breakdown":              endpointBreakdown,
		"billing_currency":                billingCurrency,
		"subscription_credits_consumed":   subscriptionConsumed,
		"purchased_credits_consumed":      purchasedConsumed,
		"paid_purchased_credits_consumed": paidPurchasedConsumed,
		"overdraft_credits_consumed":      maxInt64(0, purchasedConsumed-paidPurchasedConsumed),
		"cost_amount":                     roundMoney(totalCost),
		"revenue_amount":                  roundMoney(revenueAmount),
		"profit_amount":                   roundMoney(revenueAmount - totalCost),
		"account_used_tokens":             updatedAccount.UsedTokens,
	}
	if scope.workflowRunID != nil {
		metadata["workflow_run_id"] = scope.workflowRunID.String()
	}
	if err := a.createCreditLedgerRecord(tx, creditLedgerEntry{
		UserID:          userID,
		ProjectID:       scope.ledgerProjectID,
		ProjectName:     scope.ledgerProjectName,
		EventType:       creditLedgerEventConsume,
		Item:            creditLedgerConsumeItemLabel,
		ChangeAmount:    -totalCredits,
		BalanceAfter:    balanceAfter,
		Reason:          scope.reason,
		RequestMetadata: metadata,
		RecordedAt:      now,
	}); err != nil {
		return err
	}

	return tx.Model(&models.CreditReservation{}).
		Where(scope.where, scope.args...).
		Updates(map[string]any{"status": creditReservationConfirmed, "confirmed_at": now}).Error
}

func (a *App) confirmWorkflowRunReservationsTx(_ context.Context, tx *gorm.DB, run *models.WorkflowRun) error {
	if err := a.confirmCreditReservationsTx(tx, creditReservationConfirmationScope{
		where:             "workflow_run_id = ? AND status = ?",
		args:              []any{run.ID, creditReservationPending},
		ledgerProjectID:   run.ProjectID,
		ledgerProjectName: run.ClientProjectName,
		workflowRunID:     &run.ID,
		billingMode:       "workflow_run_reservation_confirm",
		reason:            "workflow_run_completed",
	}); err != nil {
		return err
	}
	run.BillingStatus = workflowRunBillingStatusConfirmed
	return tx.Model(&models.WorkflowRun{}).Where("id = ?", run.ID).Update("billing_status", run.BillingStatus).Error
}

func (a *App) releaseWorkflowRunReservationsTx(_ context.Context, tx *gorm.DB, run *models.WorkflowRun) error {
	now := time.Now().UTC()
	if err := tx.Model(&models.CreditReservation{}).
		Where("workflow_run_id = ? AND status = ?", run.ID, creditReservationPending).
		Updates(map[string]any{"status": creditReservationReleased, "released_at": now, "updated_at": now}).Error; err != nil {
		return err
	}
	run.BillingStatus = workflowRunBillingStatusReleased
	return tx.Model(&models.WorkflowRun{}).Where("id = ?", run.ID).Update("billing_status", run.BillingStatus).Error
}

func migrateWorkflowRunReservationsTx(tx *gorm.DB, sourceRunID, targetRunID uuid.UUID) error {
	return tx.Model(&models.CreditReservation{}).
		Where("workflow_run_id = ? AND status = ?", sourceRunID, creditReservationPending).
		Update("workflow_run_id", targetRunID).Error
}

func reviveReleasedTaskReservationsTx(tx *gorm.DB, userID uuid.UUID, clientTaskID string, targetRunID uuid.UUID) error {
	now := time.Now().UTC()
	cutoff := now.Add(-creditReservationTTL)
	return tx.Model(&models.CreditReservation{}).
		Where(`user_id = ? AND status = ? AND released_at > ? AND workflow_run_id IN (
			SELECT id FROM workflow_runs
			WHERE user_id = ? AND client_type = ? AND client_task_id = ? AND status = ?
		)`, userID, creditReservationReleased, cutoff, userID, "pc", clientTaskID, workflowRunStatusCancelled).
		Updates(map[string]any{
			"workflow_run_id": targetRunID,
			"status":          creditReservationPending,
			"released_at":     nil,
			"updated_at":      now,
		}).Error
}

func (a *App) extendWorkflowRunReservationsTx(_ context.Context, tx *gorm.DB, run *models.WorkflowRun) error {
	if run == nil || run.ID == uuid.Nil || run.ExpiresAt.IsZero() {
		return nil
	}
	return tx.Model(&models.CreditReservation{}).
		Where("workflow_run_id = ? AND status = ? AND expires_at < ?", run.ID, creditReservationPending, run.ExpiresAt).
		Update("expires_at", run.ExpiresAt).Error
}

func (a *App) releaseProjectReservations(projectID uuid.UUID, reason string) error {
	return a.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		_ = strings.TrimSpace(reason)
		return tx.Model(&models.CreditReservation{}).
			Where("project_id = ? AND workflow_run_id IS NULL AND status = ?", projectID, creditReservationPending).
			Updates(map[string]any{"status": creditReservationReleased, "released_at": now, "updated_at": now}).
			Error
	})
}

func (a *App) expireCreditReservationsTx(tx *gorm.DB, userID *uuid.UUID, now time.Time) error {
	q := tx.Model(&models.CreditReservation{}).
		Where("status = ? AND expires_at <= ?", creditReservationPending, now)
	if userID != nil {
		q = q.Where("user_id = ?", *userID)
	}
	return q.Updates(map[string]any{"status": creditReservationExpired, "released_at": now, "updated_at": now}).Error
}

func (a *App) availableCreditsTx(tx *gorm.DB, userID uuid.UUID, now time.Time) (int64, error) {
	gross, err := a.grossRemainingCreditsTx(tx, userID, now)
	if err != nil {
		return 0, err
	}
	var reserved int64
	if err := tx.Model(&models.CreditReservation{}).
		Where("user_id = ? AND status = ? AND expires_at > ?", userID, creditReservationPending, now).
		Select("coalesce(sum(credit_count), 0)").
		Scan(&reserved).Error; err != nil {
		return 0, err
	}
	return gross - reserved, nil
}

func (a *App) grossRemainingCreditsTx(tx *gorm.DB, userID uuid.UUID, now time.Time) (int64, error) {
	if err := expirePurchasedCreditLots(tx, userID, now); err != nil {
		return 0, err
	}
	return grossRemainingCreditsAfterPurchasedLotCleanupTx(tx, userID, now)
}

func grossRemainingCreditsAfterPurchasedLotCleanupTx(tx *gorm.DB, userID uuid.UUID, now time.Time) (int64, error) {
	var account models.UserTokenAccount
	if err := tx.FirstOrCreate(&account, models.UserTokenAccount{UserID: userID}).Error; err != nil {
		return 0, err
	}
	var subscriptionSums struct {
		Total int64
		Used  int64
	}
	if err := tx.Model(&models.SubscriptionCreditGrant{}).
		Where("user_id = ? AND status = ? AND expires_at > ?", userID, "active", now).
		Select("coalesce(sum(credit_amount), 0) as total, coalesce(sum(used_amount), 0) as used").
		Scan(&subscriptionSums).Error; err != nil {
		return 0, err
	}
	var purchasedLotBalance int64
	if err := tx.Model(&models.PurchasedCreditLot{}).
		Where("user_id = ? AND status IN ? AND expires_at > ?", userID, []string{"active", "consumed"}, now).
		Select("coalesce(sum(remaining_credits), 0)").
		Scan(&purchasedLotBalance).Error; err != nil {
		return 0, err
	}
	return account.RemainingTokens() + purchasedLotBalance + maxInt64(0, subscriptionSums.Total-subscriptionSums.Used), nil
}

func billingMetadataForConsumption(consumption creditConsumption, amounts billingAmounts, subscriptionConsumed, purchasedConsumed, paidPurchasedConsumed int64) map[string]any {
	metadata := cloneMetadata(consumption.RequestMetadata)
	metadata["product_mode"] = amounts.Mode
	metadata["free_experience"] = amounts.Promotion
	metadata["billing_missing_rate"] = amounts.MissingPricing
	metadata["billing_provider"] = consumption.Provider
	metadata["billing_service_kind"] = consumption.ServiceKind
	metadata["billing_model_id"] = consumption.ModelID
	metadata["billing_currency"] = billingCurrency
	metadata["subscription_credits_consumed"] = subscriptionConsumed
	metadata["purchased_credits_consumed"] = purchasedConsumed
	metadata["paid_purchased_credits_consumed"] = paidPurchasedConsumed
	metadata["overdraft_credits_consumed"] = maxInt64(0, purchasedConsumed-paidPurchasedConsumed)
	if consumption.UTF8Bytes > 0 {
		metadata["utf8_bytes"] = consumption.UTF8Bytes
	}
	if consumption.CharacterCount > 0 {
		metadata["character_count"] = consumption.CharacterCount
	}
	if consumption.ServiceKind == "tts" {
		if amounts.Rate != nil && amounts.Rate.CostPerMillionChars > 0 {
			metadata["billing_cost_unit"] = "million_chars"
		} else {
			metadata["billing_cost_unit"] = "million_utf8_bytes"
		}
	}
	if amounts.Rate != nil {
		metadata["billing_rate"] = map[string]any{
			"provider":                    amounts.Rate.Provider,
			"service_kind":                amounts.Rate.ServiceKind,
			"model_id":                    amounts.Rate.ModelID,
			"currency":                    billingCurrency,
			"input_cost_per_million":      amounts.Rate.InputCostPerMillion,
			"output_cost_per_million":     amounts.Rate.OutputCostPerMillion,
			"cost_per_minute":             amounts.Rate.CostPerMinute,
			"cost_per_million_utf8_bytes": amounts.Rate.CostPerMillionUTF8,
			"cost_per_million_chars":      amounts.Rate.CostPerMillionChars,
			"input_tokens_per_credit":     amounts.Rate.InputTokensPerCredit,
			"output_tokens_per_credit":    amounts.Rate.OutputTokensPerCredit,
			"tokens_per_credit":           amounts.Rate.TokensPerCredit,
			"credits_per_1k_utf8_bytes":   amounts.Rate.CreditsPerUTF8Byte * 1000,
			"credits_per_1k_chars":        amounts.Rate.CreditsPerChar * 1000,
			"credits_per_minute":          amounts.Rate.CreditsPerMinute,
			"credits_per_second":          amounts.Rate.CreditsPerSecond,
		}
	}
	return metadata
}

func creditReservationIdempotencyKey(consumption creditConsumption) string {
	if consumption.WorkflowRunID != nil {
		rawKey := consumption.WorkflowRunID.String() + "|" + strings.TrimSpace(consumption.ExecutionKey)
		sum := sha256.Sum256([]byte(rawKey))
		return "workflow-credit-reservation:" + hex.EncodeToString(sum[:])
	}
	metadata := cloneMetadata(consumption.RequestMetadata)
	projectID := ""
	if consumption.ProjectID != nil {
		projectID = consumption.ProjectID.String()
	}
	for _, key := range []string{"request_id", "task_id", "transcription_id", "tts_segment_id", "segment_id"} {
		if value := strings.TrimSpace(fmt.Sprint(metadata[key])); value != "" && value != "<nil>" {
			rawKey := fmt.Sprintf("%s|%s|%s|%s|%s|%s",
				consumption.UserID.String(),
				projectID,
				consumption.APIEndpoint,
				consumption.Provider,
				key,
				value,
			)
			sum := sha256.Sum256([]byte(rawKey))
			return "credit-reservation:" + hex.EncodeToString(sum[:])
		}
	}
	raw := fmt.Sprintf("%s|%s|%s|%s|%s|%d|%d|%d|%d|%d|%s",
		consumption.UserID.String(),
		projectID,
		consumption.APIEndpoint,
		consumption.Provider,
		consumption.ModelID,
		consumption.CreditCount,
		consumption.Usage.PromptTokens,
		consumption.Usage.CompletionTokens,
		consumption.Usage.TotalTokens,
		consumption.UTF8Bytes,
		string(jsonMap(metadata)),
	)
	sum := sha256.Sum256([]byte(raw))
	return "credit-reservation:" + hex.EncodeToString(sum[:])
}

func workflowInputFingerprint(input any) string {
	raw, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func workflowInputFingerprintFromMetadata(metadata map[string]any) string {
	value := strings.ToLower(strings.TrimSpace(fmt.Sprint(metadata[workflowInputFingerprintMetadataKey])))
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return ""
	}
	return value
}

func validateWorkflowCreditConsumptionTx(tx *gorm.DB, consumption creditConsumption) (models.WorkflowRun, models.WorkflowStepExecution, error) {
	if consumption.WorkflowRunID == nil {
		return models.WorkflowRun{}, models.WorkflowStepExecution{}, nil
	}
	var run models.WorkflowRun
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND user_id = ?", *consumption.WorkflowRunID, consumption.UserID).
		Take(&run).Error; err != nil {
		return models.WorkflowRun{}, models.WorkflowStepExecution{}, err
	}
	if !uuidPointersEqual(run.ProjectID, consumption.ProjectID) {
		return models.WorkflowRun{}, models.WorkflowStepExecution{}, fmt.Errorf("workflow credit reservation project mismatch")
	}
	var step models.WorkflowStepExecution
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND workflow_run_id = ?", *consumption.WorkflowStepExecutionID, run.ID).
		Take(&step).Error; err != nil {
		return models.WorkflowRun{}, models.WorkflowStepExecution{}, err
	}
	if step.ExecutionKey != consumption.ExecutionKey {
		return models.WorkflowRun{}, models.WorkflowStepExecution{}, fmt.Errorf("workflow credit reservation execution key mismatch")
	}
	return run, step, nil
}

func validateLockedWorkflowCreditConsumption(consumption creditConsumption, locked *workflowCreditReservationLocks) error {
	if locked == nil || locked.Run == nil || locked.Step == nil {
		return fmt.Errorf("workflow credit reservation requires locked run and step")
	}
	if consumption.WorkflowRunID == nil || *consumption.WorkflowRunID != locked.Run.ID || locked.Run.UserID != consumption.UserID {
		return fmt.Errorf("workflow credit reservation run mismatch")
	}
	if !uuidPointersEqual(locked.Run.ProjectID, consumption.ProjectID) {
		return fmt.Errorf("workflow credit reservation project mismatch")
	}
	if consumption.WorkflowStepExecutionID == nil || *consumption.WorkflowStepExecutionID != locked.Step.ID || locked.Step.WorkflowRunID != locked.Run.ID {
		return fmt.Errorf("workflow credit reservation step mismatch")
	}
	if locked.Step.ExecutionKey != consumption.ExecutionKey {
		return fmt.Errorf("workflow credit reservation execution key mismatch")
	}
	return nil
}

func workflowRunRejectsNewReservations(status string) bool {
	return isWorkflowRunTerminal(status) || status == workflowRunStatusFinalizing
}

func existingCreditReservationResult(existing models.CreditReservation, consumption creditConsumption, amounts billingAmounts) error {
	// Price changes apply to new provider operations, not an already recorded one.
	var metadata map[string]any
	if json.Unmarshal(existing.RequestMetadata, &metadata) == nil && metadata["product_mode"] == amounts.Mode {
		amounts.CreditCount = existing.CreditCount
		amounts.CostAmount = floatValueFromPtr(existing.CostAmount)
		amounts.MissingPricing = existing.MissingPricing
	}
	if !creditReservationMatches(existing, consumption, amounts) {
		return fmt.Errorf("%w: workflow execution identity was reused with different billing data", ErrCreditReservationConflict)
	}
	if existing.Status == creditReservationPending || existing.Status == creditReservationConfirmed {
		return nil
	}
	return fmt.Errorf("Credits reservation is %s and cannot reuse the same request", existing.Status)
}

func creditReservationMatches(existing models.CreditReservation, consumption creditConsumption, amounts billingAmounts) bool {
	return existing.UserID == consumption.UserID &&
		uuidPointersEqual(existing.ProjectID, consumption.ProjectID) &&
		uuidPointersEqual(existing.WorkflowRunID, consumption.WorkflowRunID) &&
		uuidPointersEqual(existing.WorkflowStepExecutionID, consumption.WorkflowStepExecutionID) &&
		existing.APIEndpoint == consumption.APIEndpoint &&
		existing.Provider == consumption.Provider &&
		existing.ServiceKind == consumption.ServiceKind &&
		existing.ModelID == consumption.ModelID &&
		existing.CreditCount == amounts.CreditCount &&
		existing.PromptTokens == consumption.Usage.PromptTokens &&
		existing.CompletionTokens == consumption.Usage.CompletionTokens &&
		existing.TotalModelTokens == consumption.Usage.TotalTokens &&
		existing.UTF8Bytes == consumption.UTF8Bytes &&
		existing.CharacterCount == consumption.CharacterCount &&
		floatPointersEqual(existing.DurationSeconds, consumption.DurationSeconds) &&
		floatPointersEqual(existing.CostAmount, ptrFloat64(amounts.CostAmount)) &&
		existing.MissingPricing == amounts.MissingPricing
}

func uuidPointersEqual(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func floatPointersEqual(left, right *float64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return roundMoney(*left) == roundMoney(*right)
}

func floatValueFromPtr(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func normalizeCreditConsumption(consumption creditConsumption) creditConsumption {
	consumption.Provider = normalizeBillingProvider(consumption.Provider)
	consumption.ServiceKind = normalizeBillingKey(consumption.ServiceKind)
	consumption.ModelID = strings.TrimSpace(consumption.ModelID)
	if consumption.ServiceKind == "" {
		consumption.ServiceKind = "llm"
	}
	if consumption.Provider == "" {
		consumption.Provider = inferProviderFromEndpoint(consumption.APIEndpoint)
	}
	if consumption.ModelID == "" {
		consumption.ModelID = "unknown"
	}
	if consumption.Usage.TotalTokens == 0 {
		consumption.Usage.TotalTokens = consumption.Usage.PromptTokens + consumption.Usage.CompletionTokens
	}
	if consumption.UTF8Bytes <= 0 {
		consumption.UTF8Bytes = int64(floatValue(firstPresent(consumption.RequestMetadata, "utf8_bytes", "text_utf8_bytes", "text_length")))
	}
	if consumption.CharacterCount <= 0 {
		consumption.CharacterCount = int64(floatValue(firstPresent(consumption.RequestMetadata, "character_count", "char_count", "text_length")))
	}
	return consumption
}

func (a *App) billingAmountsForConsumption(consumption creditConsumption) (billingAmounts, error) {
	mode := consumptionMode(consumption)
	rate, hasRate, free := a.modeBillingRate(mode, consumption.Provider, consumption.ServiceKind, consumption.ModelID)
	if !hasRate && !free {
		return billingAmounts{}, missingBillingRateError(consumption)
	}
	if consumption.DubbingTTS && consumption.ServiceKind == "tts" && a.billingModeConfig("dubbing") == nil && a.cfg.DubbingTTSCreditsMultiplier > 1 {
		rate.CreditsPerChar *= a.cfg.DubbingTTSCreditsMultiplier
		rate.CreditsPerUTF8Byte *= a.cfg.DubbingTTSCreditsMultiplier
		rate.CreditsPerSecond *= a.cfg.DubbingTTSCreditsMultiplier
	}
	creditCount, err := deriveCreditCount(consumption, rate)
	if err != nil && !free {
		return billingAmounts{}, err
	}
	costAmount, missingPricing := calculateProviderCost(consumption, rate)
	if free {
		creditCount = 0
	}
	return billingAmounts{
		Mode: mode, Promotion: free,
		CreditCount:    creditCount,
		CostAmount:     costAmount,
		MissingPricing: missingPricing,
		Rate:           &rate,
	}, nil
}

func (a *App) estimateCreditCount(consumption creditConsumption) int64 {
	creditCount, err := a.estimateCreditCountStrict(consumption)
	if err != nil {
		return 0
	}
	return creditCount
}

func (a *App) estimateCreditCountStrict(consumption creditConsumption) (int64, error) {
	amounts, err := a.billingAmountsForConsumption(normalizeCreditConsumption(consumption))
	if err != nil {
		return 0, err
	}
	return amounts.CreditCount, nil
}

func missingBillingRateError(consumption creditConsumption) error {
	return fmt.Errorf("未配置计费规则: service=%s provider=%s model=%s", consumption.ServiceKind, consumption.Provider, consumption.ModelID)
}

func incompleteBillingRateError(consumption creditConsumption, field string) error {
	return fmt.Errorf("计费规则不完整: service=%s provider=%s model=%s 缺少 %s", consumption.ServiceKind, consumption.Provider, consumption.ModelID, field)
}

func deriveCreditCount(consumption creditConsumption, rate billingRate) (int64, error) {
	switch consumption.ServiceKind {
	case "llm", "vision":
		promptTokens := consumption.Usage.PromptTokens
		completionTokens := consumption.Usage.CompletionTokens
		if promptTokens > 0 || completionTokens > 0 {
			inputTokensPerCredit := firstPositiveFloat(rate.InputTokensPerCredit, rate.TokensPerCredit)
			outputTokensPerCredit := firstPositiveFloat(rate.OutputTokensPerCredit, rate.TokensPerCredit)
			credits := 0.0
			if promptTokens > 0 {
				if inputTokensPerCredit <= 0 {
					return 0, incompleteBillingRateError(consumption, "input_tokens_per_credit")
				}
				credits += float64(promptTokens) / inputTokensPerCredit
			}
			if completionTokens > 0 {
				if outputTokensPerCredit <= 0 {
					return 0, incompleteBillingRateError(consumption, "output_tokens_per_credit")
				}
				credits += float64(completionTokens) / outputTokensPerCredit
			}
			return int64(math.Ceil(credits)), nil
		}

		if consumption.Usage.TotalTokens <= 0 {
			return 0, nil
		}
		tokensPerCredit := firstPositiveFloat(rate.TokensPerCredit, minPositiveFloat(rate.InputTokensPerCredit, rate.OutputTokensPerCredit))
		if tokensPerCredit <= 0 {
			return 0, incompleteBillingRateError(consumption, "tokens_per_credit")
		}
		return int64(math.Ceil(float64(consumption.Usage.TotalTokens) / tokensPerCredit)), nil
	case "audio_separation", "dubbing_asr":
		if consumption.DurationSeconds == nil || *consumption.DurationSeconds <= 0 {
			return 0, nil
		}
		if rate.CreditsPerMinute <= 0 {
			return 0, incompleteBillingRateError(consumption, "credits_per_minute")
		}
		return int64(math.Ceil(*consumption.DurationSeconds / 60 * rate.CreditsPerMinute)), nil
	case "asr":
		if consumption.DurationSeconds == nil || *consumption.DurationSeconds <= 0 {
			return 0, nil
		}
		if rate.CreditsPerMinute <= 0 {
			return 0, incompleteBillingRateError(consumption, "credits_per_minute")
		}
		// Billed by the second: credits for the actual duration, rounded up once at the end.
		return ceilCredits(*consumption.DurationSeconds * rate.CreditsPerMinute / 60), nil
	case "tts":
		if consumption.CharacterCount > 0 && rate.CreditsPerChar > 0 {
			return int64(math.Ceil(float64(consumption.CharacterCount) * rate.CreditsPerChar)), nil
		}
		if consumption.UTF8Bytes > 0 && rate.CreditsPerUTF8Byte > 0 {
			return int64(math.Ceil(float64(consumption.UTF8Bytes) * rate.CreditsPerUTF8Byte)), nil
		}
		if consumption.DurationSeconds == nil || *consumption.DurationSeconds <= 0 {
			return 0, nil
		}
		if rate.CreditsPerSecond <= 0 {
			return 0, incompleteBillingRateError(consumption, "credits_per_1k_chars 或 credits_per_1k_utf8_bytes 或 credits_per_second")
		}
		return int64(math.Ceil(*consumption.DurationSeconds * rate.CreditsPerSecond)), nil
	default:
		if consumption.CreditCount <= 0 {
			return 0, nil
		}
		return consumption.CreditCount, nil
	}
}

func calculateProviderCost(consumption creditConsumption, rate billingRate) (float64, bool) {
	switch consumption.ServiceKind {
	case "llm", "vision":
		if rate.InputCostPerMillion <= 0 && rate.OutputCostPerMillion <= 0 {
			return 0, true
		}
		input := float64(consumption.Usage.PromptTokens) * rate.InputCostPerMillion / 1_000_000
		output := float64(consumption.Usage.CompletionTokens) * rate.OutputCostPerMillion / 1_000_000
		if consumption.Usage.PromptTokens == 0 && consumption.Usage.CompletionTokens == 0 && consumption.Usage.TotalTokens > 0 {
			input = float64(consumption.Usage.TotalTokens) * rate.InputCostPerMillion / 1_000_000
		}
		return roundMoney(input + output), false
	case "audio_separation", "dubbing_asr":
		if rate.CostPerMinute <= 0 || consumption.DurationSeconds == nil || *consumption.DurationSeconds <= 0 {
			return 0, true
		}
		return roundMoney(*consumption.DurationSeconds / 60 * rate.CostPerMinute), false
	case "asr":
		if rate.CostPerMinute <= 0 {
			return 0, true
		}
		if consumption.DurationSeconds == nil || *consumption.DurationSeconds <= 0 {
			return 0, true
		}
		return roundMoney(*consumption.DurationSeconds * rate.CostPerMinute / 60), false
	case "tts":
		if rate.CostPerMillionChars > 0 {
			if consumption.CharacterCount <= 0 {
				return 0, true
			}
			return roundMoney(float64(consumption.CharacterCount) * rate.CostPerMillionChars / 1_000_000), false
		}
		if rate.CostPerMillionUTF8 <= 0 {
			return 0, true
		}
		utf8Bytes := consumption.UTF8Bytes
		if utf8Bytes <= 0 {
			return 0, true
		}
		return roundMoney(float64(utf8Bytes) * rate.CostPerMillionUTF8 / 1_000_000), false
	default:
		return 0, true
	}
}

func (a *App) findBillingRate(provider string, serviceKind string, modelID string) (billingRate, bool) {
	if rate, found := a.findStoredBillingRate(provider, serviceKind, modelID); found {
		return rate, true
	}
	return a.builtinBillingRate(provider, serviceKind, modelID)
}

// findStoredBillingRate searches only the general rules saved in billing_rates.
func (a *App) findStoredBillingRate(provider string, serviceKind string, modelID string) (billingRate, bool) {
	rates := a.loadBillingRates()
	provider = normalizeBillingProvider(provider)
	serviceKind = normalizeBillingKey(serviceKind)
	modelID = strings.TrimSpace(modelID)

	var fallback *billingRate
	for _, rate := range rates {
		if rate.ServiceKind != "" && rate.ServiceKind != serviceKind {
			continue
		}
		if rate.Provider != "" && rate.Provider != provider {
			continue
		}
		if billingModelIDMatches(rate.ModelID, modelID) {
			return rate, true
		}
		if rate.ModelID == "" || rate.ModelID == "*" {
			copy := rate
			fallback = &copy
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	return billingRate{}, false
}

// Legacy installations retain defaults until the administrator saves mode rates.
func (a *App) builtinBillingRate(provider string, serviceKind string, modelID string) (billingRate, bool) {
	provider = normalizeBillingProvider(provider)
	serviceKind = normalizeBillingKey(serviceKind)
	modelID = strings.TrimSpace(modelID)
	if provider == "dashscope" && serviceKind == "tts" && (modelID == a.dashscope.DubbingTTSModel() || modelID == "qwen3-tts-instruct-flash") {
		return billingRate{Provider: provider, ServiceKind: serviceKind, ModelID: modelID, CreditsPerChar: 0.016, CostPerMillionChars: 80}, true
	}
	if provider == "aliyunmps" && serviceKind == "audio_separation" && modelID == "MusicDemix" && a.cfg.DubbingSeparationCreditsPerMinute > 0 {
		return billingRate{Provider: provider, ServiceKind: serviceKind, ModelID: modelID, CreditsPerMinute: a.cfg.DubbingSeparationCreditsPerMinute, CostPerMinute: 0.1}, true
	}
	if provider == "dashscope" && serviceKind == "dubbing_asr" && modelID == a.cfg.DubbingASRModel && a.cfg.DubbingASRCreditsPerMinute > 0 {
		return billingRate{Provider: provider, ServiceKind: serviceKind, ModelID: modelID, CreditsPerMinute: a.cfg.DubbingASRCreditsPerMinute, CostPerMinute: 0.0048}, true
	}
	return billingRate{}, false
}

func (a *App) loadBillingRates() []billingRate {
	var setting models.SystemSetting
	if err := a.db.First(&setting, "key = ?", "billing_rates").Error; err != nil {
		return nil
	}
	var raw any
	if err := json.Unmarshal(setting.Value, &raw); err != nil {
		return nil
	}
	var rates []billingRate
	collectBillingRates(raw, "", &rates)
	return rates
}

func collectBillingRates(raw any, serviceKind string, rates *[]billingRate) {
	switch value := raw.(type) {
	case []any:
		for _, item := range value {
			collectBillingRates(item, serviceKind, rates)
		}
	case map[string]any:
		if looksLikeBillingRate(value) {
			rate := parseBillingRate(value, serviceKind)
			*rates = append(*rates, rate)
			return
		}
		for key, nested := range value {
			if key == "modes" || key == "pricing_model" {
				continue
			}
			nextKind := serviceKind
			switch normalizeBillingKey(key) {
			case "llm", "asr", "tts", "vision", "audio_separation", "dubbing_asr":
				nextKind = normalizeBillingKey(key)
			case "rates":
				nextKind = serviceKind
			}
			collectBillingRates(nested, nextKind, rates)
		}
	}
}

func looksLikeBillingRate(value map[string]any) bool {
	for _, key := range []string{"provider", "model_id", "service_kind", "input_cost_per_million", "output_cost_per_million", "cost_per_minute", "cost_per_million_utf8_bytes", "cost_per_million_bytes", "cost_per_million_chars", "cost_per_million_characters", "input_tokens_per_credit", "output_tokens_per_credit", "prompt_tokens_per_credit", "completion_tokens_per_credit", "tokens_per_credit", "input_credits_per_token", "output_credits_per_token", "prompt_credits_per_token", "completion_credits_per_token", "credits_per_token", "credits_per_1k_utf8_bytes", "credits_per_thousand_utf8_bytes", "credits_per_utf8_byte", "credits_per_utf8_bytes", "credits_per_byte", "credits_per_million_utf8_bytes", "credits_per_1k_chars", "credits_per_thousand_chars", "credits_per_char", "credits_per_character", "credits_per_million_chars", "credits_per_minute", "credits_per_second"} {
		if _, ok := value[key]; ok {
			return true
		}
	}
	return false
}

func parseBillingRate(value map[string]any, serviceKind string) billingRate {
	creditsPerUTF8Byte := floatValue(firstPresent(value, "credits_per_utf8_byte", "credits_per_utf8_bytes", "credits_per_byte"))
	if creditsPerUTF8Byte <= 0 {
		creditsPer1KUTF8Bytes := floatValue(firstPresent(value, "credits_per_1k_utf8_bytes", "credits_per_thousand_utf8_bytes"))
		if creditsPer1KUTF8Bytes > 0 {
			creditsPerUTF8Byte = creditsPer1KUTF8Bytes / 1_000
		}
	}
	if creditsPerUTF8Byte <= 0 {
		creditsPerMillionUTF8Bytes := floatValue(value["credits_per_million_utf8_bytes"])
		if creditsPerMillionUTF8Bytes > 0 {
			creditsPerUTF8Byte = creditsPerMillionUTF8Bytes / 1_000_000
		}
	}
	creditsPerChar := floatValue(firstPresent(value, "credits_per_char", "credits_per_character"))
	if creditsPerChar <= 0 {
		creditsPer1KChars := floatValue(firstPresent(value, "credits_per_1k_chars", "credits_per_thousand_chars"))
		if creditsPer1KChars > 0 {
			creditsPerChar = creditsPer1KChars / 1_000
		}
	}
	if creditsPerChar <= 0 {
		creditsPerMillionChars := floatValue(value["credits_per_million_chars"])
		if creditsPerMillionChars > 0 {
			creditsPerChar = creditsPerMillionChars / 1_000_000
		}
	}
	tokensPerCredit := parseTokensPerCredit(value)
	inputTokensPerCredit := parseDirectionalTokensPerCredit(value, tokensPerCredit, "input", "prompt")
	outputTokensPerCredit := parseDirectionalTokensPerCredit(value, tokensPerCredit, "output", "completion")
	rate := billingRate{
		Provider:              normalizeBillingProvider(billingStringValue(value["provider"])),
		ServiceKind:           normalizeBillingKey(firstNonEmpty(billingStringValue(value["service_kind"]), serviceKind)),
		ModelID:               strings.TrimSpace(firstNonEmpty(billingStringValue(value["model_id"]), billingStringValue(value["model"]))),
		InputCostPerMillion:   floatValue(value["input_cost_per_million"]),
		OutputCostPerMillion:  floatValue(value["output_cost_per_million"]),
		CostPerMinute:         floatValue(value["cost_per_minute"]),
		CostPerMillionUTF8:    floatValue(firstPresent(value, "cost_per_million_utf8_bytes", "cost_per_million_bytes")),
		CostPerMillionChars:   floatValue(firstPresent(value, "cost_per_million_chars", "cost_per_million_characters")),
		InputTokensPerCredit:  inputTokensPerCredit,
		OutputTokensPerCredit: outputTokensPerCredit,
		TokensPerCredit:       tokensPerCredit,
		CreditsPerUTF8Byte:    creditsPerUTF8Byte,
		CreditsPerChar:        creditsPerChar,
		CreditsPerMinute:      parseCreditsPerMinute(value),
		CreditsPerSecond:      floatValue(value["credits_per_second"]),
	}
	return rate
}

// ceilCredits rounds up to whole credits, ignoring float noise such as 165.00000000000003.
func ceilCredits(credits float64) int64 {
	return int64(math.Ceil(math.Round(credits*1e6) / 1e6))
}

func parseCreditsPerMinute(value map[string]any) float64 {
	creditsPerMinute := floatValue(value["credits_per_minute"])
	if creditsPerMinute > 0 {
		return creditsPerMinute
	}

	legacyCreditsPerSecond := floatValue(value["credits_per_second"])
	if legacyCreditsPerSecond > 0 {
		return legacyCreditsPerSecond * 60
	}
	return 0
}

func parseTokensPerCredit(value map[string]any) float64 {
	tokensPerCredit := floatValue(value["tokens_per_credit"])
	if tokensPerCredit > 0 {
		return tokensPerCredit
	}

	legacyCreditsPerToken := floatValue(value["credits_per_token"])
	if legacyCreditsPerToken <= 0 {
		return 0
	}
	if legacyCreditsPerToken < 1 {
		return 1 / legacyCreditsPerToken
	}
	return legacyCreditsPerToken
}

func parseDirectionalTokensPerCredit(value map[string]any, fallback float64, aliases ...string) float64 {
	for _, alias := range aliases {
		tokensPerCredit := floatValue(value[alias+"_tokens_per_credit"])
		if tokensPerCredit > 0 {
			return tokensPerCredit
		}
	}
	for _, alias := range aliases {
		creditsPerToken := floatValue(value[alias+"_credits_per_token"])
		if creditsPerToken > 0 {
			if creditsPerToken < 1 {
				return 1 / creditsPerToken
			}
			return creditsPerToken
		}
	}
	return fallback
}

func firstPositiveFloat(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func minPositiveFloat(values ...float64) float64 {
	minValue := 0.0
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if minValue == 0 || value < minValue {
			minValue = value
		}
	}
	return minValue
}

func firstPresent(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return value
		}
	}
	return nil
}

func inferProviderFromEndpoint(endpoint string) string {
	switch {
	case strings.Contains(endpoint, "elevenlabs"):
		return "elevenlabs"
	case strings.Contains(endpoint, "funasr"), strings.Contains(endpoint, "qwen3"), strings.Contains(endpoint, "timeline-view/analyze"):
		return "dashscope"
	case strings.Contains(endpoint, "text-to-speech"), strings.Contains(endpoint, "analyze-images"), strings.Contains(endpoint, "ocr-images"):
		return "siliconflow"
	default:
		return "openai"
	}
}

func (a *App) inferOpenAICompatibleProvider(modelID string) string {
	if billingModelIDMatches(a.cfg.OpenAIProModel, modelID) {
		if provider := inferProviderFromOpenAIBaseURL(a.cfg.OpenAIProAPIBaseURL); provider != "" {
			return provider
		}
	}
	if billingModelIDMatches(a.cfg.OpenAIFlashModel, modelID) {
		if provider := inferProviderFromOpenAIBaseURL(a.cfg.OpenAIFlashAPIBaseURL); provider != "" {
			return provider
		}
	}
	if billingModelIDMatches(a.cfg.DeepSeekModel, modelID) {
		if provider := inferProviderFromOpenAIBaseURL(a.cfg.DeepSeekAPIBaseURL); provider != "" {
			return provider
		}
	}
	if billingModelIDMatches(a.cfg.MovieNarrationModel, modelID) {
		if provider := inferProviderFromOpenAIBaseURL(a.cfg.MovieNarrationAPIBaseURL); provider != "" {
			return provider
		}
	}
	if billingModelIDMatches(a.cfg.MovieVisionModel, modelID) {
		if provider := inferProviderFromOpenAIBaseURL(a.cfg.MovieVisionAPIBaseURL); provider != "" {
			return provider
		}
	}
	return inferProviderFromModelID(modelID)
}

func inferProviderFromOpenAIBaseURL(baseURL string) string {
	baseURL = normalizeBillingKey(baseURL)
	switch {
	case strings.Contains(baseURL, "dashscope"), strings.Contains(baseURL, "aliyuncs"):
		return "dashscope"
	case strings.Contains(baseURL, "siliconflow"):
		return "siliconflow"
	case strings.Contains(baseURL, "deepseek"):
		return "deepseek"
	case strings.Contains(baseURL, "minimaxi"), strings.Contains(baseURL, "minimax"):
		return "minimax"
	case strings.Contains(baseURL, "moonshot"), strings.Contains(baseURL, "kimi"):
		return "moonshot"
	case strings.Contains(baseURL, "zhipu"), strings.Contains(baseURL, "bigmodel"):
		return "zhipu"
	case strings.Contains(baseURL, "volces"), strings.Contains(baseURL, "volcengine"), strings.Contains(baseURL, "doubao"):
		return "volcengine"
	case strings.Contains(baseURL, "openrouter"):
		return "openrouter"
	case strings.Contains(baseURL, "openai"):
		return "openai"
	default:
		return ""
	}
}

func inferProviderFromModelID(modelID string) string {
	modelID = normalizeBillingModelID(modelID)
	switch {
	case strings.HasPrefix(modelID, "qwen"), strings.HasPrefix(modelID, "qwq"), strings.Contains(modelID, "/qwen"):
		return "dashscope"
	case strings.HasPrefix(modelID, "deepseek"):
		return "deepseek"
	case strings.HasPrefix(modelID, "minimax"):
		return "minimax"
	case strings.HasPrefix(modelID, "gpt"), strings.HasPrefix(modelID, "o1"), strings.HasPrefix(modelID, "o3"), strings.HasPrefix(modelID, "o4"):
		return "openai"
	default:
		return "openai"
	}
}

func normalizeBillingKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeBillingProvider(value string) string {
	value = normalizeBillingKey(value)
	value = strings.NewReplacer("-", "", "_", "", " ", "").Replace(value)
	switch value {
	case "fish", "fishaudio", "fishspeech":
		return "fishaudio"
	case "eleven", "elevenlab", "elevenlabs":
		return "elevenlabs"
	case "silicon", "siliconflow":
		return "siliconflow"
	case "minimaxi":
		return "minimax"
	default:
		return value
	}
}

func billingModelIDMatches(configured string, actual string) bool {
	configured = strings.TrimSpace(configured)
	actual = strings.TrimSpace(actual)
	if configured == "" || actual == "" {
		return false
	}
	if configured == actual || strings.EqualFold(configured, actual) {
		return true
	}
	configuredAliases := billingModelIDAliases(configured)
	actualAliases := billingModelIDAliases(actual)
	for _, configuredAlias := range configuredAliases {
		for _, actualAlias := range actualAliases {
			if configuredAlias != "" && configuredAlias == actualAlias {
				return true
			}
		}
	}
	return false
}

func normalizeBillingModelID(value string) string {
	value = normalizeBillingKey(value)
	value = strings.NewReplacer("_", "-", " ", "-").Replace(value)
	for strings.Contains(value, "--") {
		value = strings.ReplaceAll(value, "--", "-")
	}
	return strings.Trim(value, "-")
}

func billingModelIDAliases(value string) []string {
	aliases := make([]string, 0, 4)
	add := func(candidate string) {
		candidate = normalizeBillingModelID(candidate)
		if candidate == "" {
			return
		}
		for _, existing := range aliases {
			if existing == candidate {
				return
			}
		}
		aliases = append(aliases, candidate)
	}

	add(value)
	trimmed := strings.TrimSpace(value)
	if slash := strings.LastIndex(trimmed, "/"); slash >= 0 && slash < len(trimmed)-1 {
		add(trimmed[slash+1:])
	}
	for _, alias := range append([]string(nil), aliases...) {
		add(stripBillingModelDateSuffix(alias))
	}
	return aliases
}

func stripBillingModelDateSuffix(value string) string {
	parts := strings.Split(value, "-")
	if len(parts) < 4 {
		return value
	}
	for index := 0; index <= len(parts)-3; index++ {
		if isFourDigitYear(parts[index]) && isTwoDigitNumber(parts[index+1]) && isTwoDigitNumber(parts[index+2]) {
			stripped := append([]string{}, parts[:index]...)
			stripped = append(stripped, parts[index+3:]...)
			return strings.Join(stripped, "-")
		}
	}
	return value
}

func isFourDigitYear(value string) bool {
	if len(value) != 4 {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func isTwoDigitNumber(value string) bool {
	if len(value) != 2 {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func cloneMetadata(metadata map[string]any) map[string]any {
	out := make(map[string]any)
	for key, value := range metadata {
		out[key] = value
	}
	return out
}

func ptrFloat64(value float64) *float64 {
	return &value
}

func roundMoney(value float64) float64 {
	return math.Round(value*1_000_000) / 1_000_000
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func billingStringValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func floatValue(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		parsed, _ := v.Float64()
		return parsed
	case string:
		var parsed float64
		_, _ = fmt.Sscanf(v, "%f", &parsed)
		return parsed
	default:
		return 0
	}
}

func audioDurationWithFallback(audio []byte, text string) (float64, string) {
	if duration, ok := wavDuration(audio); ok {
		return duration, "wav_header"
	}
	if duration, ok := mp3Duration(audio); ok {
		return duration, "mp3_header"
	}
	return estimateSpeechDuration(text), "text_estimate"
}

func estimateSpeechDuration(text string) float64 {
	duration := float64(len([]rune(text))) / 4.0
	if duration < 3 {
		return 3
	}
	if duration > 180 {
		return 180
	}
	return duration
}

func wavDuration(audio []byte) (float64, bool) {
	if len(audio) < 44 || string(audio[:4]) != "RIFF" || string(audio[8:12]) != "WAVE" {
		return 0, false
	}
	var byteRate uint32
	var dataSize uint32
	for i := 12; i+8 <= len(audio); {
		chunkID := string(audio[i : i+4])
		chunkSize := binary.LittleEndian.Uint32(audio[i+4 : i+8])
		chunkStart := i + 8
		chunkEnd := chunkStart + int(chunkSize)
		if chunkEnd > len(audio) {
			return 0, false
		}
		if chunkID == "fmt " && chunkSize >= 12 {
			byteRate = binary.LittleEndian.Uint32(audio[chunkStart+8 : chunkStart+12])
		}
		if chunkID == "data" {
			dataSize = chunkSize
		}
		if byteRate > 0 && dataSize > 0 {
			return float64(dataSize) / float64(byteRate), true
		}
		i = chunkEnd
		if i%2 == 1 {
			i++
		}
	}
	return 0, false
}

func mp3Duration(audio []byte) (float64, bool) {
	offset := 0
	if len(audio) > 10 && string(audio[:3]) == "ID3" {
		tagSize := (int(audio[6]&0x7f) << 21) + (int(audio[7]&0x7f) << 14) + (int(audio[8]&0x7f) << 7) + int(audio[9]&0x7f)
		offset = 10 + tagSize
		if offset >= len(audio) {
			return 0, false
		}
	}
	for i := offset; i+4 <= len(audio); i++ {
		if audio[i] != 0xff || audio[i+1]&0xe0 != 0xe0 {
			continue
		}
		versionBits := (audio[i+1] >> 3) & 0x03
		layerBits := (audio[i+1] >> 1) & 0x03
		bitrateIndex := (audio[i+2] >> 4) & 0x0f
		if versionBits == 1 || layerBits != 1 || bitrateIndex == 0 || bitrateIndex == 15 {
			continue
		}
		bitrate := mp3BitrateKbps(versionBits, bitrateIndex)
		if bitrate <= 0 {
			return 0, false
		}
		dataBytes := len(audio) - i
		return float64(dataBytes*8) / float64(bitrate*1000), true
	}
	return 0, false
}

func mp3BitrateKbps(versionBits byte, index byte) int {
	mpeg1Layer3 := []int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
	mpeg2Layer3 := []int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}
	if versionBits == 3 {
		return mpeg1Layer3[index]
	}
	return mpeg2Layer3[index]
}
