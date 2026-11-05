//go:build ignore

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/database"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const (
	defaultCompensationAmount = int64(2500)
	defaultCompensationKey    = "platform_compensation_2500_20260514"
	platformCompensationLabel = "平台补偿"
)

type compensationOptions struct {
	mode       string
	amount     int64
	key        string
	allowApply bool
}

type compensationStats struct {
	Users           int64 `gorm:"column:users"`
	Accounts        int64 `gorm:"column:accounts"`
	MissingAccounts int64 `gorm:"column:missing_accounts"`
	AccountCredits  int64 `gorm:"column:account_credits"`
	LedgerRecords   int64 `gorm:"column:ledger_records"`
	LedgerAmount    int64 `gorm:"column:ledger_amount"`
}

type updatedBalance struct {
	UserID       uuid.UUID `gorm:"column:user_id"`
	BalanceAfter int64     `gorm:"column:balance_after"`
}

func main() {
	opts := parseCompensationOptions()
	if err := runCompensation(opts); err != nil {
		log.Fatalf("platform compensation failed: %v", err)
	}
}

func parseCompensationOptions() compensationOptions {
	opts := compensationOptions{}
	flag.StringVar(&opts.mode, "mode", "dry-run", "inspect, dry-run, apply, or verify")
	flag.Int64Var(&opts.amount, "amount", defaultCompensationAmount, "Credits to add to every user")
	flag.StringVar(&opts.key, "key", defaultCompensationKey, "idempotency key stored in request_metadata.migration")
	flag.BoolVar(&opts.allowApply, "allow-apply", false, "required for apply")
	flag.Parse()
	return opts
}

func runCompensation(opts compensationOptions) error {
	opts.mode = strings.TrimSpace(strings.ToLower(opts.mode))
	opts.key = strings.TrimSpace(opts.key)
	if opts.amount <= 0 {
		return errors.New("amount must be positive")
	}
	if opts.key == "" {
		return errors.New("key is required")
	}

	db, err := database.Open(config.Load())
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	switch opts.mode {
	case "inspect", "dry-run":
		stats, err := collectCompensationStats(db, opts)
		if err != nil {
			return err
		}
		printCompensationStats(opts, stats)
		if stats.LedgerRecords > 0 {
			fmt.Println("status=already_applied")
			return nil
		}
		fmt.Printf("would_add_credits_per_user=%d\n", opts.amount)
		fmt.Printf("would_total_credit_increase=%d\n", stats.Users*opts.amount)
		return nil
	case "apply":
		if !opts.allowApply {
			return errors.New("apply requires --allow-apply")
		}
		return applyCompensation(db, opts)
	case "verify":
		stats, err := collectCompensationStats(db, opts)
		if err != nil {
			return err
		}
		printCompensationStats(opts, stats)
		if stats.LedgerRecords != stats.Users {
			return fmt.Errorf("ledger_records %d != users %d", stats.LedgerRecords, stats.Users)
		}
		expectedAmount := stats.Users * opts.amount
		if stats.LedgerAmount != expectedAmount {
			return fmt.Errorf("ledger_amount %d != expected %d", stats.LedgerAmount, expectedAmount)
		}
		if stats.MissingAccounts != 0 {
			return fmt.Errorf("missing_accounts %d != 0", stats.MissingAccounts)
		}
		fmt.Println("verification=passed")
		return nil
	default:
		return fmt.Errorf("unknown mode %q", opts.mode)
	}
}

func collectCompensationStats(db *gorm.DB, opts compensationOptions) (compensationStats, error) {
	var stats compensationStats
	err := db.Raw(`
SELECT
  (SELECT count(*) FROM users) AS users,
  (SELECT count(*) FROM user_token_accounts) AS accounts,
  (SELECT count(*) FROM users u LEFT JOIN user_token_accounts a ON a.user_id = u.id WHERE a.user_id IS NULL) AS missing_accounts,
  (SELECT COALESCE(SUM(total_tokens), 0) FROM user_token_accounts) AS account_credits,
  (SELECT count(*) FROM credit_ledger_records WHERE request_metadata ->> 'migration' = ?) AS ledger_records,
  (SELECT COALESCE(SUM(change_amount), 0) FROM credit_ledger_records WHERE request_metadata ->> 'migration' = ?) AS ledger_amount
`, opts.key, opts.key).Scan(&stats).Error
	return stats, err
}

func printCompensationStats(opts compensationOptions, stats compensationStats) {
	fmt.Printf("mode=%s key=%s amount=%d\n", opts.mode, opts.key, opts.amount)
	fmt.Printf("users=%d accounts=%d missing_accounts=%d account_credits=%d\n", stats.Users, stats.Accounts, stats.MissingAccounts, stats.AccountCredits)
	fmt.Printf("existing_platform_compensation_records=%d existing_platform_compensation_amount=%d\n", stats.LedgerRecords, stats.LedgerAmount)
}

func applyCompensation(db *gorm.DB, opts compensationOptions) error {
	fmt.Println("== Applying platform compensation ==")
	return db.Transaction(func(tx *gorm.DB) error {
		stats, err := collectCompensationStats(tx, opts)
		if err != nil {
			return err
		}
		printCompensationStats(opts, stats)
		if stats.LedgerRecords > 0 {
			return fmt.Errorf("compensation key %q already has %d ledger records", opts.key, stats.LedgerRecords)
		}

		if err := createMissingCompensationAccounts(tx); err != nil {
			return err
		}

		now := time.Now().UTC()
		var balances []updatedBalance
		if err := tx.Raw(`
UPDATE user_token_accounts
SET total_tokens = total_tokens + ?, updated_at = ?
RETURNING user_id, GREATEST(total_tokens - used_tokens, 0) AS balance_after
`, opts.amount, now).Scan(&balances).Error; err != nil {
			return err
		}
		if int64(len(balances)) != stats.Users {
			return fmt.Errorf("updated accounts %d != users %d", len(balances), stats.Users)
		}

		metadata, err := json.Marshal(map[string]any{
			"migration": opts.key,
			"amount":    opts.amount,
			"scope":     "all_users",
		})
		if err != nil {
			return err
		}

		const batchSize = 500
		for start := 0; start < len(balances); start += batchSize {
			end := int(math.Min(float64(start+batchSize), float64(len(balances))))
			records := make([]models.CreditLedgerRecord, 0, end-start)
			for _, balance := range balances[start:end] {
				records = append(records, models.CreditLedgerRecord{
					Base: models.Base{
						ID:        uuid.New(),
						CreatedAt: now,
						UpdatedAt: now,
					},
					UserID:          balance.UserID,
					EventType:       "admin_adjustment",
					Item:            platformCompensationLabel,
					ChangeAmount:    opts.amount,
					BalanceAfter:    balance.BalanceAfter,
					Reason:          platformCompensationLabel,
					RequestMetadata: datatypes.JSON(metadata),
					RecordedAt:      now,
				})
			}
			if err := tx.Create(&records).Error; err != nil {
				return err
			}
		}

		fmt.Printf("applied_users=%d total_credit_increase=%d\n", len(balances), int64(len(balances))*opts.amount)
		return nil
	})
}

func createMissingCompensationAccounts(tx *gorm.DB) error {
	var userIDs []uuid.UUID
	if err := tx.Raw(`
SELECT u.id
FROM users u
LEFT JOIN user_token_accounts a ON a.user_id = u.id
WHERE a.user_id IS NULL
ORDER BY u.id
`).Scan(&userIDs).Error; err != nil {
		return err
	}
	if len(userIDs) == 0 {
		return nil
	}

	now := time.Now().UTC()
	accounts := make([]models.UserTokenAccount, 0, len(userIDs))
	for _, userID := range userIDs {
		accounts = append(accounts, models.UserTokenAccount{
			Base: models.Base{
				ID:        uuid.New(),
				CreatedAt: now,
				UpdatedAt: now,
			},
			UserID:         userID,
			TotalTokens:    0,
			UsedTokens:     0,
			TotalRecharged: 0,
		})
	}
	return tx.Create(&accounts).Error
}

func init() {
	log.SetOutput(os.Stderr)
}
