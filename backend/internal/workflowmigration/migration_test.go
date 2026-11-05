package workflowmigration

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/database"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func TestRunDryRunDoesNotWriteAndApplyIsIdempotent(t *testing.T) {
	db := newMigrationTestDB(t)
	project := seedLegacyProject(t, db)

	dry, err := Run(context.Background(), db, Options{DryRun: true})
	if err != nil {
		t.Fatalf("dry run migration: %v", err)
	}
	assertStepReport(t, dry, "video-source", 1, 1, 0)
	assertStepReport(t, dry, "subtitles", 1, 1, 0)
	assertStepReport(t, dry, "config", 1, 1, 0)
	assertStepReport(t, dry, "script", 1, 1, 0)
	assertStepReport(t, dry, "voiceover", 1, 1, 0)
	assertActiveCount(t, db, "project_video_sources", project.ID, 0)

	applied, err := Run(context.Background(), db, Options{})
	if err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	assertStepReport(t, applied, "video-source", 1, 1, 1)
	assertStepReport(t, applied, "voiceover", 1, 1, 1)
	assertActiveCount(t, db, "project_video_sources", project.ID, 1)
	assertActiveCount(t, db, "project_subtitles", project.ID, 1)
	assertActiveCount(t, db, "project_generation_configs", project.ID, 1)
	assertActiveCount(t, db, "project_scripts", project.ID, 1)
	assertActiveCount(t, db, "project_voiceovers", project.ID, 1)

	verify, err := Verify(context.Background(), db)
	if err != nil {
		t.Fatalf("verify migration: %v", err)
	}
	if verify.HasMissingLegacyRows() {
		t.Fatalf("verify has missing rows: %#v", verify.Steps)
	}

	second, err := Run(context.Background(), db, Options{})
	if err != nil {
		t.Fatalf("second migration: %v", err)
	}
	assertStepReport(t, second, "video-source", 1, 0, 0)
	assertActiveCount(t, db, "project_video_sources", project.ID, 1)

	var stored models.Project
	if err := db.First(&stored, "id = ?", project.ID).Error; err != nil {
		t.Fatalf("reload project: %v", err)
	}
	if stored.WorkflowMigratedAt == nil {
		t.Fatal("workflow_migrated_at was not marked")
	}
	var root models.ProjectWorkflow
	if err := db.First(&root, "project_id = ?", project.ID).Error; err != nil {
		t.Fatalf("workflow root missing: %v", err)
	}
	if root.CurrentStep == nil || *root.CurrentStep != "voiceover" || root.LastStepType == nil || *root.LastStepType != "voiceover" {
		t.Fatalf("workflow root step = current:%v last:%v, want voiceover", root.CurrentStep, root.LastStepType)
	}
	if !strings.Contains(string(stored.Subtitle), "legacy subtitle") {
		t.Fatalf("legacy subtitle cache was changed: %s", string(stored.Subtitle))
	}
}

func TestVerifyReportsMissingLegacyRowsBeforeApply(t *testing.T) {
	db := newMigrationTestDB(t)
	project := seedLegacyProject(t, db)

	verify, err := Verify(context.Background(), db)
	if err != nil {
		t.Fatalf("verify migration: %v", err)
	}
	assertStepReport(t, verify, "video-source", 1, 0, 0)
	assertActiveCount(t, db, "project_scripts", project.ID, 0)
	if !verify.HasMissingLegacyRows() {
		t.Fatalf("verify should report missing rows: %#v", verify.Steps)
	}
}

func newMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg := config.Config{DatabaseURL: "sqlite://file:" + uuid.NewString() + "?mode=memory&cache=shared"}
	db, err := database.Open(cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.AutoMigrate(db); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

func seedLegacyProject(t *testing.T, db *gorm.DB) models.Project {
	t.Helper()
	user := models.User{
		Username:     "migration-user-" + uuid.NewString(),
		Email:        uuid.NewString() + "@example.com",
		PasswordHash: "hash",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	videoURL := "https://cdn.example.com/legacy.mp4"
	audioURL := "https://cdn.example.com/legacy.mp3"
	project := models.Project{
		Name:             "legacy workflow project",
		OwnerID:          user.ID,
		VideoURL:         &videoURL,
		AudioURL:         &audioURL,
		VideoContext:     datatypes.JSON([]byte(`{"1":{"text":"legacy context"}}`)),
		Subtitle:         datatypes.JSON([]byte(`{"1":{"subtitle":"legacy subtitle","start_time":0,"end_time":1}}`)),
		VideoConfig:      datatypes.JSON([]byte(`{"style":"dramatic","language":"zh"}`)),
		AIAnalysisScript: datatypes.JSON([]byte(`{"items":[{"text":"legacy script"}],"model_name":"test-model"}`)),
	}
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project
}

func assertStepReport(t *testing.T, report Report, step string, candidates int, pending int, inserted int) {
	t.Helper()
	for _, item := range report.Steps {
		if item.Step != step {
			continue
		}
		if item.LegacyCandidates != candidates || item.PendingInsert != pending || item.Inserted != inserted {
			t.Fatalf("step %s report = %#v, want candidates=%d pending=%d inserted=%d", step, item, candidates, pending, inserted)
		}
		return
	}
	t.Fatalf("missing step report for %s: %#v", step, report.Steps)
}

func assertActiveCount(t *testing.T, db *gorm.DB, table string, projectID uuid.UUID, want int64) {
	t.Helper()
	var count int64
	if err := db.Table(table).Where("project_id = ? AND deleted_at IS NULL AND cleared_at IS NULL", projectID).Count(&count).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if count != want {
		t.Fatalf("%s active rows = %d, want %d", table, count, want)
	}
}
