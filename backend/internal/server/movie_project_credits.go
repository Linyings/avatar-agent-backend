package server

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
	"net/http"
)

// A desktop project is an attribution tag, never a web ProjectID or a billing exemption.
func movieBillingMetadata(c *gin.Context, metadata map[string]any) map[string]any {
	id, err := uuid.Parse(c.GetHeader("X-Movie-Project-Id"))
	if err != nil || id == uuid.Nil {
		return metadata
	}
	result := cloneMetadata(metadata)
	result["movie_project_id"] = id.String()
	return result
}

func movieProjectUsageQuery(db *gorm.DB, userID, projectID uuid.UUID, requestIDs []string) *gorm.DB {
	field := func(key string) string {
		if db.Dialector.Name() == "sqlite" {
			return "json_extract(request_metadata, '$." + key + "')"
		}
		return "request_metadata ->> '" + key + "'"
	}
	condition := field("movie_project_id") + " = ?"
	values := []any{projectID.String()}
	// Paid movie-model calls finalized before the tag survived finalization are still this project's:
	// the desktop names their workflow run after the movie project.
	runSteps := db.Model(&models.WorkflowStepExecution{}).Select("workflow_step_executions.provider_request_id").
		Joins("JOIN workflow_runs ON workflow_runs.id = workflow_step_executions.workflow_run_id").
		Where("workflow_runs.user_id = ? AND workflow_runs.client_type = ? AND workflow_runs.client_project_name = ? AND workflow_step_executions.provider_request_id IS NOT NULL",
			userID, "pc", movieProjectRunName(projectID))
	condition += " OR ((" + field("movie_project_id") + " IS NULL OR " + field("movie_project_id") + " = '') AND " + field("request_id") + " IN (?))"
	values = append(values, runSteps)
	if len(requestIDs) > 0 {
		// Legacy request IDs are matched only within the authenticated account and only
		// when a record does not already belong to another movie project.
		condition += " OR ((" + field("movie_project_id") + " IS NULL OR " + field("movie_project_id") + " = '') AND " + field("request_id") + " <> '' AND " + field("request_id") + " IN ?)"
		values = append(values, requestIDs)
	}
	return db.Model(&models.TokenUsageRecord{}).Where("user_id = ?", userID).Where("("+condition+")", values...)
}

// movieProjectRunName is the client_project_name the desktop gives every paid movie-model workflow run.
func movieProjectRunName(projectID uuid.UUID) string { return "电影项目 " + projectID.String() }

func (a *App) movieProjectCredits(c *gin.Context) {
	id, err := uuid.Parse(c.Param("project_id"))
	if err != nil || id == uuid.Nil {
		fail(c, http.StatusBadRequest, "无效的电影项目 ID")
		return
	}
	var input struct {
		RequestIDs []string `json:"request_ids"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || len(input.RequestIDs) > 2000 {
		fail(c, http.StatusBadRequest, "无效的项目扣费记录查询")
		return
	}
	for _, value := range input.RequestIDs {
		if len(value) > 200 {
			fail(c, http.StatusBadRequest, "无效的请求编号")
			return
		}
	}
	var total struct {
		Credits int64 `gorm:"column:credits"`
	}
	if err := movieProjectUsageQuery(a.db, getUser(c).ID, id, input.RequestIDs).Select("COALESCE(SUM(credit_count), 0) AS credits").Scan(&total).Error; err != nil {
		fail(c, http.StatusInternalServerError, "项目积分暂时无法读取")
		return
	}
	ok(c, gin.H{"total_consumed_credits": total.Credits, "attribution_version": 1})
}
