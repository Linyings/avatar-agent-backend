package database

import (
	"fmt"
	"strings"

	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func Open(cfg config.Config) (*gorm.DB, error) {
	gormCfg := &gorm.Config{Logger: logger.Default.LogMode(logger.Warn)}
	url := cfg.DatabaseURL

	if strings.HasPrefix(url, "sqlite://") {
		return gorm.Open(sqlite.Open(strings.TrimPrefix(url, "sqlite://")), gormCfg)
	}
	if strings.HasPrefix(url, "sqlite:///") {
		return gorm.Open(sqlite.Open(strings.TrimPrefix(url, "sqlite:///")), gormCfg)
	}

	url = strings.Replace(url, "postgresql+asyncpg://", "postgres://", 1)
	url = strings.Replace(url, "postgresql://", "postgres://", 1)
	return gorm.Open(postgres.Open(url), gormCfg)
}

func AutoMigrate(db *gorm.DB) error {
	if err := prepareLegacyUniqueConstraintDrops(db); err != nil {
		return err
	}
	if err := db.AutoMigrate(
		&models.User{},
		&models.AuthEmailCode{},
		&models.OAuthAccount{},
		&models.OAuthState{},
		&models.OAuthLoginTicket{},
		&models.DesktopAuthorizationRequest{},
		&models.RevokedAuthToken{},
		&models.UserProfile{},
		&models.Project{},
		&models.EmailCampaign{},
		&models.EmailCampaignRecipient{},
		&models.EmailSuppression{},
		&models.ProjectWorkflow{},
		&models.ProjectWorkflowEvent{},
		&models.WorkflowRun{},
		&models.WorkflowStepExecution{},
		&models.ProjectVideoSource{},
		&models.ProjectSubtitle{},
		&models.ProjectGenerationConfig{},
		&models.ProjectScript{},
		&models.ProjectVoiceover{},
		&models.TokenPackage{},
		&models.CreditPaymentOrder{},
		&models.PurchasedCreditLot{},
		&models.CreditTrialClaim{},
		&models.SubscriptionPlan{},
		&models.UserSubscription{},
		&models.SubscriptionCreditGrant{},
		&models.UserTokenAccount{},
		&models.TokenRechargeRecord{},
		&models.TokenUsageRecord{},
		&models.CreditReservation{},
		&models.CreditLedgerRecord{},
		&models.WeChatPayment{},
		&models.CreemCheckoutSession{},
		&models.CreemWebhookEvent{},
		&models.AlipaySubscriptionSession{},
		&models.PaymentWebhookEvent{},
		&models.SystemSetting{},
		&models.Notification{},
		&models.UploadRecord{},
	); err != nil {
		return err
	}

	requiredIndexes := []struct {
		model any
		name  string
	}{
		{&models.CreditPaymentOrder{}, "uq_credit_payment_orders_out_trade_no"},
		{&models.PurchasedCreditLot{}, "uq_purchased_credit_lots_order_id"},
		{&models.CreditTrialClaim{}, "uq_credit_trial_claims_user_id"},
	}
	for _, requiredIndex := range requiredIndexes {
		if !db.Migrator().HasIndex(requiredIndex.model, requiredIndex.name) {
			return fmt.Errorf("required index %s is missing", requiredIndex.name)
		}
	}
	if err := migrateCreditPackagesToCNY(db); err != nil {
		return fmt.Errorf("migrate credit packages to CNY: %w", err)
	}

	return ensureReadPathIndexes(db)
}

func ensureReadPathIndexes(db *gorm.DB) error {
	for _, statement := range readPathIndexStatements(db.Dialector.Name()) {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

func readPathIndexStatements(dialect string) []string {
	concurrently := ""
	if dialect == "postgres" {
		concurrently = " CONCURRENTLY"
	}

	subscriptionGrantIndex := "CREATE INDEX" + concurrently + " IF NOT EXISTS idx_subscription_credit_grants_user_active_expiry ON subscription_credit_grants(user_id, expires_at) WHERE status = 'active' AND deleted_at IS NULL"
	if dialect == "postgres" {
		subscriptionGrantIndex = "CREATE INDEX" + concurrently + " IF NOT EXISTS idx_subscription_credit_grants_user_active_expiry ON subscription_credit_grants(user_id, expires_at) INCLUDE (credit_amount, used_amount) WHERE status = 'active' AND deleted_at IS NULL"
	}

	return []string{
		"CREATE INDEX" + concurrently + " IF NOT EXISTS idx_projects_owner_active_updated_at ON projects(owner_id, updated_at DESC) WHERE deleted_at IS NULL",
		"CREATE INDEX" + concurrently + " IF NOT EXISTS idx_projects_owner_status_active_updated_at ON projects(owner_id, status, updated_at DESC) WHERE deleted_at IS NULL",
		"CREATE INDEX" + concurrently + " IF NOT EXISTS idx_projects_owner_active_created_at ON projects(owner_id, created_at DESC) WHERE deleted_at IS NULL",
		"CREATE INDEX" + concurrently + " IF NOT EXISTS idx_projects_owner_active_name ON projects(owner_id, name) WHERE deleted_at IS NULL",
		"CREATE UNIQUE INDEX" + concurrently + " IF NOT EXISTS uq_workflow_runs_web_active_scope ON workflow_runs(project_id) WHERE client_type = 'web' AND project_id IS NOT NULL AND status IN ('running', 'interrupted', 'blocked', 'finalizing')",
		"CREATE UNIQUE INDEX" + concurrently + " IF NOT EXISTS uq_workflow_runs_pc_active_scope ON workflow_runs(user_id, client_type, client_task_id) WHERE client_type = 'pc' AND client_task_id IS NOT NULL AND status IN ('running', 'interrupted', 'blocked', 'finalizing')",
		"CREATE INDEX" + concurrently + " IF NOT EXISTS idx_workflow_runs_active_expiry ON workflow_runs(expires_at) WHERE status IN ('running', 'interrupted', 'blocked', 'finalizing')",
		"CREATE INDEX" + concurrently + " IF NOT EXISTS idx_workflow_step_executions_run_created ON workflow_step_executions(workflow_run_id, created_at)",
		"CREATE INDEX" + concurrently + " IF NOT EXISTS idx_workflow_step_executions_provider_submission ON workflow_step_executions(workflow_run_id, step_id, provider_input_hash, provider_request_id) WHERE provider_input_hash IS NOT NULL AND provider_request_id IS NOT NULL",
		"CREATE INDEX" + concurrently + " IF NOT EXISTS idx_token_packages_active_sort_price ON token_packages(sort_order, price, id) WHERE is_active = true AND deleted_at IS NULL",
		"CREATE INDEX" + concurrently + " IF NOT EXISTS idx_subscription_plans_active_sort_price ON subscription_plans(sort_order, price_monthly, id) WHERE is_active = true AND deleted_at IS NULL",
		subscriptionGrantIndex,
	}
}

func prepareLegacyUniqueConstraintDrops(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" {
		return nil
	}

	compatConstraints := []struct {
		Table      string
		Constraint string
		Columns    string
	}{
		{Table: "users", Constraint: "uni_users_username", Columns: "username"},
		{Table: "users", Constraint: "uni_users_email", Columns: "email"},
		{Table: "token_packages", Constraint: "uni_token_packages_name", Columns: "name"},
		{Table: "user_token_accounts", Constraint: "uni_user_token_accounts_user_id", Columns: "user_id"},
		{Table: "wechat_payments", Constraint: "uni_wechat_payments_out_trade_no", Columns: "out_trade_no"},
		{Table: "system_settings", Constraint: "uni_system_settings_key", Columns: "key"},
	}

	for _, item := range compatConstraints {
		stmt := `
DO $$
BEGIN
  IF to_regclass('` + item.Table + `') IS NOT NULL
     AND NOT EXISTS (
       SELECT 1
       FROM pg_constraint
       WHERE conrelid = to_regclass('` + item.Table + `')
         AND conname = '` + item.Constraint + `'
     )
  THEN
    EXECUTE 'ALTER TABLE ` + quoteIdentifier(item.Table) + ` ADD CONSTRAINT ` + quoteIdentifier(item.Constraint) + ` UNIQUE (` + item.Columns + `)';
  END IF;
END $$;
`
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
