package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/database"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

var archiveSchemaPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type options struct {
	mode                    string
	databaseURL             string
	archiveSchema           string
	tokenToCreditRatio      float64
	creditsPerCNY           float64
	expectedUserCount       int64
	allowApply              bool
	confirmDestroyProjects  bool
	confirmResetUsageLedger bool
}

type tableCount struct {
	TableName string
	Exists    bool
	Rows      int64
}

type balanceSummary struct {
	UserCount                    int64
	ExistingAccountCount         int64
	MissingAccountCount          int64
	LegacyTotalTokens            int64
	LegacyUsedTokens             int64
	LegacyRemainingTokens        int64
	LegacyTotalRechargedCNY      float64
	RemainingPaidValueCNY        float64
	CompensatedCredits           int64
	UsersWithCompensation        int64
	ActiveSubscriptionGrantCount int64
	SubscriptionRemainingCredits int64
}

type snapshotRow struct {
	UserID                    uuid.UUID `gorm:"column:user_id"`
	CompensatedCredits        int64     `gorm:"column:compensated_credits"`
	RemainingPaidValueCNY     float64   `gorm:"column:remaining_paid_value_cny"`
	SubscriptionRemaining     int64     `gorm:"column:subscription_remaining"`
	PostMigrationBalanceAfter int64     `gorm:"column:post_migration_balance_after"`
	LegacyTotal               int64     `gorm:"column:legacy_total"`
	LegacyUsed                int64     `gorm:"column:legacy_used"`
	LegacyRemaining           int64     `gorm:"column:legacy_remaining"`
	LegacyTotalRechargedCNY   float64   `gorm:"column:legacy_paid_cny"`
}

func main() {
	opts := parseOptions()
	if err := run(opts); err != nil {
		log.Fatalf("credits migrate failed: %v", err)
	}
}

func parseOptions() options {
	defaultSchema := "migration_" + time.Now().UTC().Format("20060102_150405") + "_credits_reset"
	opts := options{}
	flag.StringVar(&opts.mode, "mode", "inspect", "inspect, dry-run, or apply")
	flag.StringVar(&opts.databaseURL, "database-url", firstNonEmpty(os.Getenv("CLONE_DATABASE_URL"), os.Getenv("DATABASE_URL")), "PostgreSQL URL; defaults to CLONE_DATABASE_URL then DATABASE_URL")
	flag.StringVar(&opts.archiveSchema, "archive-schema", defaultSchema, "schema used to archive production tables during apply")
	flag.Float64Var(&opts.tokenToCreditRatio, "token-to-credit-ratio", 1, "legacy remaining token to new credit ratio")
	flag.Float64Var(&opts.creditsPerCNY, "credits-per-cny", 0, "minimum compensation floor based on remaining paid value")
	flag.Int64Var(&opts.expectedUserCount, "expected-user-count", 0, "abort apply if user count does not match this value")
	flag.BoolVar(&opts.allowApply, "allow-apply", false, "required for apply")
	flag.BoolVar(&opts.confirmDestroyProjects, "confirm-destroy-project-data", false, "required for apply; acknowledges project/upload/workflow cleanup")
	flag.BoolVar(&opts.confirmResetUsageLedger, "confirm-reset-usage-ledger", false, "required for apply; acknowledges usage and credit ledger reset")
	flag.Parse()
	return opts
}

func run(opts options) error {
	opts.mode = strings.TrimSpace(strings.ToLower(opts.mode))
	if opts.databaseURL == "" {
		return errors.New("missing database URL; set CLONE_DATABASE_URL or pass --database-url")
	}
	if opts.tokenToCreditRatio < 0 || opts.creditsPerCNY < 0 {
		return errors.New("compensation ratios must be non-negative")
	}
	if !archiveSchemaPattern.MatchString(opts.archiveSchema) {
		return fmt.Errorf("invalid archive schema %q", opts.archiveSchema)
	}

	db, err := database.Open(config.Config{DatabaseURL: opts.databaseURL})
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	switch opts.mode {
	case "inspect":
		return inspect(db, opts)
	case "dry-run":
		return dryRun(db, opts)
	case "apply":
		return apply(db, opts)
	case "verify":
		return verify(db, opts)
	default:
		return fmt.Errorf("unknown mode %q", opts.mode)
	}
}

func inspect(db *gorm.DB, opts options) error {
	fmt.Println("== Database ==")
	var databaseName, userName string
	var serverAddr sql.NullString
	if err := db.Raw("select current_database(), current_user, inet_server_addr()::text").Row().Scan(&databaseName, &userName, &serverAddr); err != nil {
		return err
	}
	if !serverAddr.Valid {
		serverAddr.String = "unknown"
	}
	fmt.Printf("database=%s user=%s server_addr=%s\n", databaseName, userName, serverAddr.String)

	fmt.Println("\n== Table counts ==")
	counts, err := collectTableCounts(db, migrationTables())
	if err != nil {
		return err
	}
	for _, count := range counts {
		if count.Exists {
			fmt.Printf("%-34s %10d\n", count.TableName, count.Rows)
		} else {
			fmt.Printf("%-34s %10s\n", count.TableName, "missing")
		}
	}

	if err := printIntegrityChecks(db); err != nil {
		return err
	}

	fmt.Println("\n== Balance dry-run summary ==")
	summary, err := collectBalanceSummary(db, opts)
	if err != nil {
		return err
	}
	printBalanceSummary(summary, opts)
	return nil
}

func dryRun(db *gorm.DB, opts options) error {
	fmt.Println("== Credits migration dry-run ==")
	summary, err := collectBalanceSummary(db, opts)
	if err != nil {
		return err
	}
	printBalanceSummary(summary, opts)
	return nil
}

func apply(db *gorm.DB, opts options) error {
	if !opts.allowApply || !opts.confirmDestroyProjects || !opts.confirmResetUsageLedger {
		return errors.New("apply requires --allow-apply --confirm-destroy-project-data --confirm-reset-usage-ledger")
	}

	summary, err := collectBalanceSummary(db, opts)
	if err != nil {
		return err
	}
	if opts.expectedUserCount > 0 && summary.UserCount != opts.expectedUserCount {
		return fmt.Errorf("user count mismatch: got %d, expected %d", summary.UserCount, opts.expectedUserCount)
	}

	fmt.Println("== Applying credits migration ==")
	printBalanceSummary(summary, opts)

	return db.Transaction(func(tx *gorm.DB) error {
		if err := archiveTables(tx, opts.archiveSchema, migrationTables()); err != nil {
			return err
		}
		if err := createZeroAccountsForMissingUsers(tx); err != nil {
			return err
		}
		if err := createSnapshotTable(tx, opts); err != nil {
			return err
		}
		if err := updateAccountsFromSnapshot(tx, opts.archiveSchema); err != nil {
			return err
		}
		if err := resetProjectUsageAndLedgerTables(tx); err != nil {
			return err
		}
		if err := ensureCreditLedgerRecordsTable(tx); err != nil {
			return err
		}
		if err := insertMigrationLedgerRecords(tx, opts.archiveSchema); err != nil {
			return err
		}
		return nil
	})
}

func verify(db *gorm.DB, opts options) error {
	fmt.Println("== Credits migration verification ==")
	fmt.Printf("archive_schema=%s\n", opts.archiveSchema)

	snapshotExists, err := tableExistsInSchema(db, opts.archiveSchema, "user_balance_snapshot")
	if err != nil {
		return err
	}
	if !snapshotExists {
		return fmt.Errorf("archive snapshot %s.user_balance_snapshot does not exist", opts.archiveSchema)
	}

	type currentSummary struct {
		Users                  int64   `gorm:"column:users"`
		Accounts               int64   `gorm:"column:accounts"`
		Projects               int64   `gorm:"column:projects"`
		Uploads                int64   `gorm:"column:uploads"`
		UsageRecords           int64   `gorm:"column:usage_records"`
		LedgerRecords          int64   `gorm:"column:ledger_records"`
		LedgerAmount           int64   `gorm:"column:ledger_amount"`
		AccountCredits         int64   `gorm:"column:account_credits"`
		AccountUsed            int64   `gorm:"column:account_used"`
		AccountRechargedAmount float64 `gorm:"column:account_recharged_amount"`
	}
	var current currentSummary
	if err := db.Raw(`
SELECT
  (SELECT count(*) FROM users) AS users,
  (SELECT count(*) FROM user_token_accounts) AS accounts,
  (SELECT count(*) FROM projects) AS projects,
  (SELECT count(*) FROM upload_records) AS uploads,
  (SELECT count(*) FROM token_usage_records) AS usage_records,
  (SELECT count(*) FROM credit_ledger_records WHERE item = '系统迁移补偿') AS ledger_records,
  (SELECT COALESCE(SUM(change_amount), 0) FROM credit_ledger_records WHERE item = '系统迁移补偿') AS ledger_amount,
  (SELECT COALESCE(SUM(total_tokens), 0) FROM user_token_accounts) AS account_credits,
  (SELECT COALESCE(SUM(used_tokens), 0) FROM user_token_accounts) AS account_used,
  (SELECT COALESCE(SUM(total_recharged), 0)::float8 FROM user_token_accounts) AS account_recharged_amount
`).Scan(&current).Error; err != nil {
		return err
	}

	type snapshotSummary struct {
		Users                  int64   `gorm:"column:users"`
		CompensatedUsers       int64   `gorm:"column:compensated_users"`
		CompensatedCredits     int64   `gorm:"column:compensated_credits"`
		RemainingPaidValueCNY  float64 `gorm:"column:remaining_paid_value_cny"`
		LegacyProjects         int64   `gorm:"column:legacy_projects"`
		LegacyUploads          int64   `gorm:"column:legacy_uploads"`
		LegacyUsageRecords     int64   `gorm:"column:legacy_usage_records"`
		LegacyUserTokenAccount int64   `gorm:"column:legacy_user_token_accounts"`
	}
	var snapshot snapshotSummary
	if err := db.Raw(`
SELECT
  (SELECT count(*) FROM ` + quoteQualified(opts.archiveSchema+".user_balance_snapshot") + `) AS users,
  (SELECT count(*) FROM ` + quoteQualified(opts.archiveSchema+".user_balance_snapshot") + ` WHERE compensated_credits > 0) AS compensated_users,
  (SELECT COALESCE(SUM(compensated_credits), 0) FROM ` + quoteQualified(opts.archiveSchema+".user_balance_snapshot") + `) AS compensated_credits,
  (SELECT COALESCE(SUM(remaining_paid_value_cny), 0)::float8 FROM ` + quoteQualified(opts.archiveSchema+".user_balance_snapshot") + `) AS remaining_paid_value_cny,
  (SELECT count(*) FROM ` + quoteQualified(opts.archiveSchema+".projects") + `) AS legacy_projects,
  (SELECT count(*) FROM ` + quoteQualified(opts.archiveSchema+".upload_records") + `) AS legacy_uploads,
  (SELECT count(*) FROM ` + quoteQualified(opts.archiveSchema+".token_usage_records") + `) AS legacy_usage_records,
  (SELECT count(*) FROM ` + quoteQualified(opts.archiveSchema+".user_token_accounts") + `) AS legacy_user_token_accounts
`).Scan(&snapshot).Error; err != nil {
		return err
	}

	fmt.Printf("current users=%d accounts=%d projects=%d uploads=%d usage_records=%d\n", current.Users, current.Accounts, current.Projects, current.Uploads, current.UsageRecords)
	fmt.Printf("current account_credits=%d account_used=%d account_recharged_amount=%.2f\n", current.AccountCredits, current.AccountUsed, current.AccountRechargedAmount)
	fmt.Printf("current migration_ledger_records=%d migration_ledger_amount=%d\n", current.LedgerRecords, current.LedgerAmount)
	fmt.Printf("snapshot users=%d compensated_users=%d compensated_credits=%d remaining_paid_value_cny=%.2f\n", snapshot.Users, snapshot.CompensatedUsers, snapshot.CompensatedCredits, snapshot.RemainingPaidValueCNY)
	fmt.Printf("archive projects=%d uploads=%d usage_records=%d user_token_accounts=%d\n", snapshot.LegacyProjects, snapshot.LegacyUploads, snapshot.LegacyUsageRecords, snapshot.LegacyUserTokenAccount)

	var problems []string
	if current.Users != snapshot.Users {
		problems = append(problems, fmt.Sprintf("current users %d != snapshot users %d", current.Users, snapshot.Users))
	}
	if current.Accounts != snapshot.Users {
		problems = append(problems, fmt.Sprintf("current accounts %d != snapshot users %d", current.Accounts, snapshot.Users))
	}
	if current.Projects != 0 || current.Uploads != 0 || current.UsageRecords != 0 {
		problems = append(problems, "project/upload/usage tables were not cleared")
	}
	if current.AccountCredits != snapshot.CompensatedCredits {
		problems = append(problems, fmt.Sprintf("account credits %d != snapshot compensated credits %d", current.AccountCredits, snapshot.CompensatedCredits))
	}
	if current.AccountUsed != 0 {
		problems = append(problems, fmt.Sprintf("account used credits should be 0, got %d", current.AccountUsed))
	}
	if current.LedgerRecords != snapshot.CompensatedUsers {
		problems = append(problems, fmt.Sprintf("ledger records %d != compensated users %d", current.LedgerRecords, snapshot.CompensatedUsers))
	}
	if current.LedgerAmount != snapshot.CompensatedCredits {
		problems = append(problems, fmt.Sprintf("ledger amount %d != snapshot compensated credits %d", current.LedgerAmount, snapshot.CompensatedCredits))
	}
	if snapshot.LegacyProjects == 0 || snapshot.LegacyUploads == 0 || snapshot.LegacyUsageRecords == 0 {
		problems = append(problems, "archive is missing expected legacy project/upload/usage rows")
	}
	if len(problems) > 0 {
		return fmt.Errorf("verification failed: %s", strings.Join(problems, "; "))
	}

	fmt.Println("verification=passed")
	return nil
}

func migrationTables() []string {
	return []string{
		"users",
		"user_profiles",
		"user_token_accounts",
		"token_recharge_records",
		"wechat_payments",
		"creem_checkout_sessions",
		"creem_webhook_events",
		"projects",
		"project_workflows",
		"project_workflow_events",
		"project_video_sources",
		"project_subtitles",
		"project_generation_configs",
		"project_scripts",
		"project_voiceovers",
		"upload_records",
		"token_usage_records",
		"credit_ledger_records",
		"user_subscriptions",
		"subscription_credit_grants",
	}
}

func collectTableCounts(db *gorm.DB, tables []string) ([]tableCount, error) {
	counts := make([]tableCount, 0, len(tables))
	for _, table := range tables {
		exists, err := tableExists(db, table)
		if err != nil {
			return nil, err
		}
		count := tableCount{TableName: table, Exists: exists}
		if exists {
			if err := db.Raw("select count(*) from " + quoteIdent(table)).Scan(&count.Rows).Error; err != nil {
				return nil, err
			}
		}
		counts = append(counts, count)
	}
	return counts, nil
}

func collectBalanceSummary(db *gorm.DB, opts options) (balanceSummary, error) {
	subscriptionSelect, err := subscriptionRowsSQL(db)
	if err != nil {
		return balanceSummary{}, err
	}
	rechargeSelect, err := rechargeRowsSQL(db)
	if err != nil {
		return balanceSummary{}, err
	}

	var summary balanceSummary
	err = db.Raw(`
WITH recharge_rows AS (
`+rechargeSelect+`
),
account_rows AS (
  SELECT
    u.id AS user_id,
    CASE WHEN a.user_id IS NULL THEN 0 ELSE 1 END AS has_account,
    COALESCE(a.total_tokens, 0)::bigint AS legacy_total,
    COALESCE(a.used_tokens, 0)::bigint AS legacy_used,
    GREATEST(COALESCE(a.total_tokens, 0) - COALESCE(a.used_tokens, 0), 0)::bigint AS legacy_remaining,
    GREATEST(COALESCE(a.total_recharged, 0), COALESCE(r.recharge_paid_cny, 0))::numeric AS legacy_paid_cny
  FROM users u
  LEFT JOIN user_token_accounts a ON a.user_id = u.id
  LEFT JOIN recharge_rows r ON r.user_id = u.id
),
subscription_rows AS (
`+subscriptionSelect+`
),
calculated AS (
  SELECT
    a.*,
    COALESCE(s.active_grants, 0) AS active_subscription_grants,
    COALESCE(s.subscription_remaining, 0)::bigint AS subscription_remaining,
    CASE
      WHEN a.legacy_total > 0
      THEN a.legacy_paid_cny * a.legacy_remaining::numeric / a.legacy_total::numeric
      ELSE 0
    END AS remaining_paid_value_cny
  FROM account_rows a
  LEFT JOIN subscription_rows s ON s.user_id = a.user_id
),
compensated AS (
  SELECT
    *,
    GREATEST(
      CEIL(legacy_remaining::numeric * @token_to_credit_ratio),
      CEIL(remaining_paid_value_cny * @credits_per_cny)
    )::bigint AS compensated_credits
  FROM calculated
)
SELECT
  COUNT(*) AS user_count,
  COALESCE(SUM(has_account), 0) AS existing_account_count,
  COUNT(*) - COALESCE(SUM(has_account), 0) AS missing_account_count,
  COALESCE(SUM(legacy_total), 0) AS legacy_total_tokens,
  COALESCE(SUM(legacy_used), 0) AS legacy_used_tokens,
  COALESCE(SUM(legacy_remaining), 0) AS legacy_remaining_tokens,
  COALESCE(SUM(legacy_paid_cny), 0)::float8 AS legacy_total_recharged_cny,
  COALESCE(SUM(remaining_paid_value_cny), 0)::float8 AS remaining_paid_value_cny,
  COALESCE(SUM(compensated_credits), 0) AS compensated_credits,
  COALESCE(SUM(CASE WHEN compensated_credits > 0 THEN 1 ELSE 0 END), 0) AS users_with_compensation,
  COALESCE(SUM(active_subscription_grants), 0) AS active_subscription_grant_count,
  COALESCE(SUM(subscription_remaining), 0) AS subscription_remaining_credits
FROM compensated
`, map[string]any{
		"token_to_credit_ratio": opts.tokenToCreditRatio,
		"credits_per_cny":       opts.creditsPerCNY,
	}).Scan(&summary).Error
	return summary, err
}

func printBalanceSummary(summary balanceSummary, opts options) {
	fmt.Printf("token_to_credit_ratio=%.6f credits_per_cny=%.6f\n", opts.tokenToCreditRatio, opts.creditsPerCNY)
	fmt.Printf("users=%d existing_accounts=%d missing_accounts=%d\n", summary.UserCount, summary.ExistingAccountCount, summary.MissingAccountCount)
	fmt.Printf("legacy_total_tokens=%d legacy_used_tokens=%d legacy_remaining_tokens=%d\n", summary.LegacyTotalTokens, summary.LegacyUsedTokens, summary.LegacyRemainingTokens)
	fmt.Printf("legacy_total_recharged_cny=%.2f remaining_paid_value_cny=%.2f\n", summary.LegacyTotalRechargedCNY, summary.RemainingPaidValueCNY)
	fmt.Printf("compensated_credits=%d users_with_compensation=%d\n", summary.CompensatedCredits, summary.UsersWithCompensation)
	fmt.Printf("active_subscription_grants=%d subscription_remaining_credits=%d\n", summary.ActiveSubscriptionGrantCount, summary.SubscriptionRemainingCredits)
}

func printIntegrityChecks(db *gorm.DB) error {
	fmt.Println("\n== Integrity checks ==")
	var row struct {
		MissingProfiles int64 `gorm:"column:missing_profiles"`
		MissingAccounts int64 `gorm:"column:missing_accounts"`
		OrphanProfiles  int64 `gorm:"column:orphan_profiles"`
		OrphanAccounts  int64 `gorm:"column:orphan_accounts"`
		InvalidBalances int64 `gorm:"column:invalid_balances"`
	}
	if err := db.Raw(`
SELECT
  (SELECT count(*) FROM users u LEFT JOIN user_profiles p ON p.user_id = u.id WHERE p.user_id IS NULL) AS missing_profiles,
  (SELECT count(*) FROM users u LEFT JOIN user_token_accounts a ON a.user_id = u.id WHERE a.user_id IS NULL) AS missing_accounts,
  (SELECT count(*) FROM user_profiles p LEFT JOIN users u ON u.id = p.user_id WHERE u.id IS NULL) AS orphan_profiles,
  (SELECT count(*) FROM user_token_accounts a LEFT JOIN users u ON u.id = a.user_id WHERE u.id IS NULL) AS orphan_accounts,
  (SELECT count(*) FROM user_token_accounts WHERE total_tokens < used_tokens) AS invalid_balances
`).Scan(&row).Error; err != nil {
		return err
	}
	fmt.Printf("missing_profiles=%d missing_accounts=%d orphan_profiles=%d orphan_accounts=%d invalid_balances=%d\n",
		row.MissingProfiles,
		row.MissingAccounts,
		row.OrphanProfiles,
		row.OrphanAccounts,
		row.InvalidBalances,
	)

	if exists, err := tableExists(db, "token_recharge_records"); err != nil {
		return err
	} else if exists {
		var recharge struct {
			Count   int64   `gorm:"column:count"`
			Credits int64   `gorm:"column:credits"`
			Price   float64 `gorm:"column:price"`
		}
		if err := db.Raw(`
SELECT
  count(*) AS count,
  COALESCE(SUM(COALESCE(token_amount, 0) + COALESCE(bonus_tokens, 0)), 0) AS credits,
  COALESCE(SUM(price), 0)::float8 AS price
FROM token_recharge_records
`).Scan(&recharge).Error; err != nil {
			return err
		}
		fmt.Printf("recharge_records=%d recharge_credits=%d recharge_price=%.2f\n", recharge.Count, recharge.Credits, recharge.Price)

		type rechargeMethodRow struct {
			PaymentMethod string  `gorm:"column:payment_method"`
			Count         int64   `gorm:"column:count"`
			Price         float64 `gorm:"column:price"`
		}
		var methods []rechargeMethodRow
		if err := db.Raw(`
SELECT
  COALESCE(payment_method, '') AS payment_method,
  count(*) AS count,
  COALESCE(SUM(price), 0)::float8 AS price
FROM token_recharge_records
GROUP BY COALESCE(payment_method, '')
ORDER BY count DESC, payment_method
`).Scan(&methods).Error; err != nil {
			return err
		}
		for _, method := range methods {
			fmt.Printf("recharge_method=%q count=%d price=%.2f\n", method.PaymentMethod, method.Count, method.Price)
		}

		var accountVsRecharge struct {
			UsersWithDifference int64   `gorm:"column:users_with_difference"`
			AccountAmount       float64 `gorm:"column:account_amount"`
			RechargeAmount      float64 `gorm:"column:recharge_amount"`
			Difference          float64 `gorm:"column:difference"`
		}
		if err := db.Raw(`
WITH recharges AS (
  SELECT user_id, COALESCE(SUM(price), 0)::numeric AS recharge_amount
  FROM token_recharge_records
  GROUP BY user_id
),
joined AS (
  SELECT
    u.id AS user_id,
    COALESCE(a.total_recharged, 0)::numeric AS account_amount,
    COALESCE(r.recharge_amount, 0)::numeric AS recharge_amount
  FROM users u
  LEFT JOIN user_token_accounts a ON a.user_id = u.id
  LEFT JOIN recharges r ON r.user_id = u.id
)
SELECT
  count(*) FILTER (WHERE ABS(account_amount - recharge_amount) > 0.01) AS users_with_difference,
  COALESCE(SUM(account_amount), 0)::float8 AS account_amount,
  COALESCE(SUM(recharge_amount), 0)::float8 AS recharge_amount,
  COALESCE(SUM(account_amount - recharge_amount), 0)::float8 AS difference
FROM joined
`).Scan(&accountVsRecharge).Error; err != nil {
			return err
		}
		fmt.Printf("account_vs_recharge users_with_difference=%d account_amount=%.2f recharge_amount=%.2f difference=%.2f\n",
			accountVsRecharge.UsersWithDifference,
			accountVsRecharge.AccountAmount,
			accountVsRecharge.RechargeAmount,
			accountVsRecharge.Difference,
		)
	}

	if exists, err := tableExists(db, "wechat_payments"); err != nil {
		return err
	} else if exists {
		type paymentStatusRow struct {
			Status     string  `gorm:"column:status"`
			TradeState string  `gorm:"column:trade_state"`
			Count      int64   `gorm:"column:count"`
			Amount     float64 `gorm:"column:amount"`
		}
		var statuses []paymentStatusRow
		if err := db.Raw(`
SELECT
  COALESCE(status, '') AS status,
  COALESCE(trade_state, '') AS trade_state,
  count(*) AS count,
  COALESCE(SUM(total_amount), 0)::float8 AS amount
FROM wechat_payments
GROUP BY COALESCE(status, ''), COALESCE(trade_state, '')
ORDER BY count DESC, status, trade_state
LIMIT 20
`).Scan(&statuses).Error; err != nil {
			return err
		}
		for _, status := range statuses {
			fmt.Printf("wechat_payment_status status=%q trade_state=%q count=%d amount=%.2f\n",
				status.Status,
				status.TradeState,
				status.Count,
				status.Amount,
			)
		}

		var paymentJoin struct {
			PaidWithoutRechargeCount int64   `gorm:"column:paid_without_recharge_count"`
			PaidWithoutRechargeSum   float64 `gorm:"column:paid_without_recharge_sum"`
			RechargeMissingPaidCount int64   `gorm:"column:recharge_missing_paid_count"`
			RechargeMissingPaidSum   float64 `gorm:"column:recharge_missing_paid_sum"`
		}
		if err := db.Raw(`
SELECT
  (SELECT count(*)
   FROM wechat_payments p
   LEFT JOIN token_recharge_records r ON r.payment_id = p.id
   WHERE p.status = 'paid' AND r.id IS NULL) AS paid_without_recharge_count,
  (SELECT COALESCE(SUM(p.total_amount), 0)::float8
   FROM wechat_payments p
   LEFT JOIN token_recharge_records r ON r.payment_id = p.id
   WHERE p.status = 'paid' AND r.id IS NULL) AS paid_without_recharge_sum,
  (SELECT count(*)
   FROM token_recharge_records r
   LEFT JOIN wechat_payments p ON p.id = r.payment_id AND p.status = 'paid'
   WHERE r.payment_id IS NOT NULL AND p.id IS NULL) AS recharge_missing_paid_count,
  (SELECT COALESCE(SUM(r.price), 0)::float8
   FROM token_recharge_records r
   LEFT JOIN wechat_payments p ON p.id = r.payment_id AND p.status = 'paid'
   WHERE r.payment_id IS NOT NULL AND p.id IS NULL) AS recharge_missing_paid_sum
`).Scan(&paymentJoin).Error; err != nil {
			return err
		}
		fmt.Printf("wechat_recharge_join paid_without_recharge=%d amount=%.2f recharge_missing_paid=%d amount=%.2f\n",
			paymentJoin.PaidWithoutRechargeCount,
			paymentJoin.PaidWithoutRechargeSum,
			paymentJoin.RechargeMissingPaidCount,
			paymentJoin.RechargeMissingPaidSum,
		)
	}

	return nil
}

func rechargeRowsSQL(db *gorm.DB) (string, error) {
	exists, err := tableExists(db, "token_recharge_records")
	if err != nil {
		return "", err
	}
	if !exists {
		return `
  SELECT
    NULL::uuid AS user_id,
    0::numeric AS recharge_paid_cny
  WHERE false`, nil
	}
	return `
  SELECT
    user_id,
    COALESCE(SUM(price), 0)::numeric AS recharge_paid_cny
  FROM token_recharge_records
  GROUP BY user_id`, nil
}

func subscriptionRowsSQL(db *gorm.DB) (string, error) {
	exists, err := tableExists(db, "subscription_credit_grants")
	if err != nil {
		return "", err
	}
	if !exists {
		return `
  SELECT
    NULL::uuid AS user_id,
    0::bigint AS active_grants,
    0::bigint AS subscription_remaining
  WHERE false`, nil
	}
	return `
  SELECT
    user_id,
    COUNT(*)::bigint AS active_grants,
    COALESCE(SUM(GREATEST(credit_amount - used_amount, 0)), 0)::bigint AS subscription_remaining
  FROM subscription_credit_grants
  WHERE status = 'active' AND expires_at > now()
  GROUP BY user_id`, nil
}

func archiveTables(tx *gorm.DB, schema string, tables []string) error {
	if err := tx.Exec("CREATE SCHEMA IF NOT EXISTS " + quoteIdent(schema)).Error; err != nil {
		return err
	}
	for _, table := range tables {
		exists, err := tableExists(tx, table)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		archiveName := schema + "." + table
		if err := tx.Exec("CREATE TABLE " + quoteQualified(archiveName) + " AS TABLE " + quoteIdent(table)).Error; err != nil {
			return fmt.Errorf("archive %s: %w", table, err)
		}
	}
	return nil
}

func createZeroAccountsForMissingUsers(tx *gorm.DB) error {
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
	for _, userID := range userIDs {
		if err := tx.Exec(`
INSERT INTO user_token_accounts (id, created_at, updated_at, user_id, total_tokens, used_tokens, total_recharged)
VALUES (?, ?, ?, ?, 0, 0, 0)
ON CONFLICT (user_id) DO NOTHING
`, uuid.New(), now, now, userID).Error; err != nil {
			return err
		}
	}
	return nil
}

func createSnapshotTable(tx *gorm.DB, opts options) error {
	subscriptionSelect, err := subscriptionRowsSQL(tx)
	if err != nil {
		return err
	}
	rechargeSelect, err := rechargeRowsSQL(tx)
	if err != nil {
		return err
	}

	return tx.Exec(`
CREATE TABLE `+quoteQualified(opts.archiveSchema+".user_balance_snapshot")+` AS
WITH recharge_rows AS (
`+rechargeSelect+`
),
account_rows AS (
  SELECT
    u.id AS user_id,
    u.username,
    u.email,
    COALESCE(a.total_tokens, 0)::bigint AS legacy_total,
    COALESCE(a.used_tokens, 0)::bigint AS legacy_used,
    GREATEST(COALESCE(a.total_tokens, 0) - COALESCE(a.used_tokens, 0), 0)::bigint AS legacy_remaining,
    COALESCE(a.total_recharged, 0)::numeric AS account_paid_cny,
    COALESCE(r.recharge_paid_cny, 0)::numeric AS recharge_paid_cny,
    GREATEST(COALESCE(a.total_recharged, 0), COALESCE(r.recharge_paid_cny, 0))::numeric AS legacy_paid_cny,
    a.last_recharge_at
  FROM users u
  LEFT JOIN user_token_accounts a ON a.user_id = u.id
  LEFT JOIN recharge_rows r ON r.user_id = u.id
),
subscription_rows AS (
`+subscriptionSelect+`
),
calculated AS (
  SELECT
    a.*,
    COALESCE(s.subscription_remaining, 0)::bigint AS subscription_remaining,
    CASE
      WHEN a.legacy_total > 0
      THEN a.legacy_paid_cny * a.legacy_remaining::numeric / a.legacy_total::numeric
      ELSE 0
    END AS remaining_paid_value_cny
  FROM account_rows a
  LEFT JOIN subscription_rows s ON s.user_id = a.user_id
),
compensated AS (
  SELECT
    *,
    GREATEST(
      CEIL(legacy_remaining::numeric * ?),
      CEIL(remaining_paid_value_cny * ?)
    )::bigint AS compensated_credits
  FROM calculated
)
SELECT
  *,
  compensated_credits + subscription_remaining AS post_migration_balance_after,
  ?::numeric AS token_to_credit_ratio,
  ?::numeric AS credits_per_cny,
  now() AS captured_at
FROM compensated
`, opts.tokenToCreditRatio, opts.creditsPerCNY, opts.tokenToCreditRatio, opts.creditsPerCNY).Error
}

func updateAccountsFromSnapshot(tx *gorm.DB, schema string) error {
	return tx.Exec(`
UPDATE user_token_accounts a
SET
  total_tokens = s.compensated_credits,
  used_tokens = 0,
  total_recharged = s.remaining_paid_value_cny,
  updated_at = now()
FROM ` + quoteQualified(schema+".user_balance_snapshot") + ` s
WHERE a.user_id = s.user_id
`).Error
}

func resetProjectUsageAndLedgerTables(tx *gorm.DB) error {
	tables := []string{
		"upload_records",
		"project_voiceovers",
		"project_scripts",
		"project_generation_configs",
		"project_subtitles",
		"project_video_sources",
		"project_workflow_events",
		"project_workflows",
		"projects",
		"token_usage_records",
		"credit_ledger_records",
	}
	existing := make([]string, 0, len(tables))
	for _, table := range tables {
		ok, err := tableExists(tx, table)
		if err != nil {
			return err
		}
		if ok {
			existing = append(existing, quoteIdent(table))
		}
	}
	if len(existing) == 0 {
		return nil
	}
	return tx.Exec("TRUNCATE TABLE " + strings.Join(existing, ", ") + " RESTART IDENTITY").Error
}

func ensureCreditLedgerRecordsTable(tx *gorm.DB) error {
	return tx.Exec(`
CREATE TABLE IF NOT EXISTS credit_ledger_records (
  id uuid PRIMARY KEY,
  created_at timestamptz,
  updated_at timestamptz,
  deleted_at timestamptz,
  user_id uuid NOT NULL,
  project_id uuid,
  project_name varchar(200) NOT NULL DEFAULT '',
  event_type varchar(32) NOT NULL,
  item varchar(160) NOT NULL DEFAULT '',
  change_amount bigint NOT NULL,
  balance_after bigint NOT NULL,
  reason varchar(255) NOT NULL DEFAULT '',
  request_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  recorded_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_credit_ledger_records_deleted_at ON credit_ledger_records (deleted_at);
CREATE INDEX IF NOT EXISTS idx_credit_ledger_records_user_id ON credit_ledger_records (user_id);
CREATE INDEX IF NOT EXISTS idx_credit_ledger_records_project_id ON credit_ledger_records (project_id);
CREATE INDEX IF NOT EXISTS idx_credit_ledger_records_event_type ON credit_ledger_records (event_type);
CREATE INDEX IF NOT EXISTS idx_credit_ledger_records_change_amount ON credit_ledger_records (change_amount);
CREATE INDEX IF NOT EXISTS idx_credit_ledger_records_recorded_at ON credit_ledger_records (recorded_at);
`).Error
}

func insertMigrationLedgerRecords(tx *gorm.DB, schema string) error {
	var rows []snapshotRow
	if err := tx.Raw(`
SELECT
  user_id,
  compensated_credits,
  remaining_paid_value_cny::float8 AS remaining_paid_value_cny,
  subscription_remaining,
  post_migration_balance_after,
  legacy_total,
  legacy_used,
  legacy_remaining,
  legacy_paid_cny::float8 AS legacy_paid_cny
FROM ` + quoteQualified(schema+".user_balance_snapshot") + `
WHERE compensated_credits > 0
ORDER BY user_id
`).Scan(&rows).Error; err != nil {
		return err
	}

	now := time.Now().UTC()
	const batchSize = 500
	for start := 0; start < len(rows); start += batchSize {
		end := int(math.Min(float64(start+batchSize), float64(len(rows))))
		records := make([]models.CreditLedgerRecord, 0, end-start)
		for _, row := range rows[start:end] {
			records = append(records, models.CreditLedgerRecord{
				Base: models.Base{
					ID:        uuid.New(),
					CreatedAt: now,
					UpdatedAt: now,
				},
				UserID:       row.UserID,
				EventType:    "admin_adjustment",
				Item:         "系统迁移补偿",
				ChangeAmount: row.CompensatedCredits,
				BalanceAfter: row.PostMigrationBalanceAfter,
				Reason:       "Token 余额迁移为 Credits",
				RequestMetadata: datatypes.JSON([]byte(fmt.Sprintf(
					`{"migration":"credits_reset","legacy_total":%d,"legacy_used":%d,"legacy_remaining":%d,"legacy_paid_cny":%.6f,"remaining_paid_value_cny":%.6f,"subscription_remaining":%d}`,
					row.LegacyTotal,
					row.LegacyUsed,
					row.LegacyRemaining,
					row.LegacyTotalRechargedCNY,
					row.RemainingPaidValueCNY,
					row.SubscriptionRemaining,
				))),
				RecordedAt: now,
			})
		}
		if err := tx.Create(&records).Error; err != nil {
			return err
		}
	}
	return nil
}

func tableExists(db *gorm.DB, table string) (bool, error) {
	var exists bool
	err := db.Raw(`
SELECT EXISTS (
  SELECT 1
  FROM information_schema.tables
  WHERE table_schema = current_schema() AND table_name = ?
)`, table).Scan(&exists).Error
	return exists, err
}

func tableExistsInSchema(db *gorm.DB, schema string, table string) (bool, error) {
	var exists bool
	err := db.Raw(`
SELECT EXISTS (
  SELECT 1
  FROM information_schema.tables
  WHERE table_schema = ? AND table_name = ?
)`, schema, table).Scan(&exists).Error
	return exists, err
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func quoteQualified(name string) string {
	parts := strings.Split(name, ".")
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, quoteIdent(part))
	}
	return strings.Join(quoted, ".")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
