package database

import (
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/models"
)

func TestReadPathIndexStatementsIncludeWorkflowIndexesForSQLiteAndPostgres(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			statements := readPathIndexStatements(dialect)
			joined := strings.ToLower(strings.Join(statements, "\n"))
			for _, index := range []string{
				"uq_workflow_runs_web_active_scope",
				"uq_workflow_runs_pc_active_scope",
				"idx_workflow_runs_active_expiry",
				"idx_workflow_step_executions_run_created",
				"idx_workflow_step_executions_provider_submission",
			} {
				if !strings.Contains(joined, index) {
					t.Fatalf("%s index statements missing %s", dialect, index)
				}
			}
			if dialect == "postgres" && !strings.Contains(joined, "create unique index concurrently if not exists uq_workflow_runs_web_active_scope") {
				t.Fatalf("postgres workflow unique index is not created concurrently: %s", joined)
			}
			if dialect == "sqlite" && strings.Contains(joined, "concurrently") {
				t.Fatalf("sqlite statements contain PostgreSQL CONCURRENTLY: %s", joined)
			}
		})
	}
}

func TestAutoMigrateAddsWorkflowProviderSubmissionMetadata(t *testing.T) {
	db, err := Open(config.Config{DatabaseURL: "sqlite://file:workflow-provider-submission-migration?mode=memory&cache=shared"})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}

	migrator := db.Migrator()
	for _, column := range []string{"provider_input_hash", "provider_submission_claim_id", "provider_submission_claimed_at"} {
		if !migrator.HasColumn(&models.WorkflowStepExecution{}, column) {
			t.Fatalf("workflow_step_executions missing %s", column)
		}
	}
	if !migrator.HasIndex(&models.WorkflowStepExecution{}, "idx_workflow_step_executions_provider_submission") {
		t.Fatal("workflow_step_executions missing provider submission lookup index")
	}
}

func TestAutoMigrateAddsWorkflowCreditReservationIdentity(t *testing.T) {
	db, err := Open(config.Config{DatabaseURL: "sqlite://file:workflow-credit-migration?mode=memory&cache=shared"})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}

	migrator := db.Migrator()
	for _, column := range []string{"workflow_run_id", "workflow_step_execution_id"} {
		if !migrator.HasColumn(&models.CreditReservation{}, column) {
			t.Fatalf("credit_reservations missing %s", column)
		}
	}
	for _, index := range []string{"idx_credit_reservations_workflow_run_id", "idx_credit_reservations_workflow_step_execution_id"} {
		if !migrator.HasIndex(&models.CreditReservation{}, index) {
			t.Fatalf("credit_reservations missing %s", index)
		}
	}
}

func TestAutoMigrateAddsWorkflowRunClientProjectName(t *testing.T) {
	db, err := Open(config.Config{DatabaseURL: "sqlite://file:workflow-project-name-migration?mode=memory&cache=shared"})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}

	columnTypes, err := db.Migrator().ColumnTypes(&models.WorkflowRun{})
	if err != nil {
		t.Fatalf("load workflow run columns: %v", err)
	}
	for _, column := range columnTypes {
		if column.Name() != "client_project_name" {
			continue
		}
		if nullable, ok := column.Nullable(); !ok || nullable {
			t.Fatalf("client_project_name nullable = %v, known = %v", nullable, ok)
		}
		if _, ok := column.DefaultValue(); !ok {
			t.Fatal("client_project_name is missing an empty default")
		}
		return
	}
	t.Fatal("workflow_runs missing client_project_name")
}

func TestAutoMigrateAddsAlipaySubscriptionPersistence(t *testing.T) {
	db, err := Open(config.Config{DatabaseURL: "sqlite://file:alipay-subscription-migration?mode=memory&cache=shared"})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}

	migrator := db.Migrator()
	for _, model := range []any{&models.AlipaySubscriptionSession{}, &models.PaymentWebhookEvent{}} {
		if !migrator.HasTable(model) {
			t.Fatalf("missing migrated table for %T", model)
		}
	}
	for _, column := range []string{"payment_provider", "alipay_agreement_id", "grace_period_ends_at"} {
		if !migrator.HasColumn(&models.UserSubscription{}, column) {
			t.Fatalf("user_subscriptions missing %s", column)
		}
	}
	for _, column := range []string{"alipay_plan_id", "alipay_product_id", "subscription_provider_enabled"} {
		if !migrator.HasColumn(&models.SubscriptionPlan{}, column) {
			t.Fatalf("subscription_plans missing %s", column)
		}
	}
}
