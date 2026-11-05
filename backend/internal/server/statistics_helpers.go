package server

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
)

const (
	creditSumSQL  = "coalesce(sum(case when credit_count > 0 then credit_count else token_count end), 0)"
	costSumSQL    = "coalesce(sum(cost_amount), 0)"
	revenueSumSQL = "coalesce(sum(revenue_amount), 0)"
	profitSumSQL  = "coalesce(sum(profit_amount), 0)"
)

type statisticsPeriod struct {
	Start time.Time
	End   time.Time
	Label string
}

func parseStatisticsPeriod(c *gin.Context) statisticsPeriod {
	now := time.Now().UTC()
	days := queryInt(c, "days", 30)
	if days <= 0 {
		days = 30
	}
	start := now.AddDate(0, 0, -days+1)
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	end := now.Add(24 * time.Hour)

	if raw := c.Query("start_date"); raw != "" {
		if parsed, err := time.Parse("2006-01-02", raw); err == nil {
			start = parsed
		}
	}
	if raw := c.Query("end_date"); raw != "" {
		if parsed, err := time.Parse("2006-01-02", raw); err == nil {
			end = parsed.Add(24 * time.Hour)
		}
	}

	return statisticsPeriod{
		Start: start,
		End:   end,
		Label: fmt.Sprintf("%s - %s", start.Format("2006-01-02"), end.Add(-24*time.Hour).Format("2006-01-02")),
	}
}

func (p statisticsPeriod) applyUsage(db *gorm.DB) *gorm.DB {
	return db.Where("recorded_at >= ? AND recorded_at < ?", p.Start, p.End)
}

func (p statisticsPeriod) applyCreated(db *gorm.DB) *gorm.DB {
	return db.Where("created_at >= ? AND created_at < ?", p.Start, p.End)
}

func (p statisticsPeriod) applyPayments(db *gorm.DB) *gorm.DB {
	return db.Where("status = ? AND coalesce(success_time, created_at) >= ? AND coalesce(success_time, created_at) < ?", "paid", p.Start, p.End)
}

func creditSelect(alias string) string {
	return creditSumSQL + " as " + alias
}

func safeMargin(profit float64, revenue float64) float64 {
	if revenue == 0 {
		return 0
	}
	return roundMoney(profit / revenue * 100)
}

func accountSummary(account models.UserTokenAccount) gin.H {
	return gin.H{
		"total_tokens":      account.TotalTokens,
		"used_tokens":       account.UsedTokens,
		"remaining_tokens":  account.RemainingTokens(),
		"total_credits":     account.TotalTokens,
		"used_credits":      account.UsedTokens,
		"remaining_credits": account.RemainingTokens(),
		"total_recharged":   account.TotalRecharged,
		"last_recharge_at":  account.LastRechargeAt,
		"created_at":        account.CreatedAt,
		"updated_at":        account.UpdatedAt,
	}
}

func (a *App) userRecentUsage(userID uuid.UUID, days int) gin.H {
	if days <= 0 {
		days = 30
	}
	start := time.Now().UTC().AddDate(0, 0, -days+1)
	q := a.db.Model(&models.TokenUsageRecord{}).Where("user_id = ? AND recorded_at >= ?", userID, start)
	var totalUsed int64
	q.Select(creditSumSQL).Scan(&totalUsed)

	type usageRow struct {
		Key      string
		Credits  int64
		Requests int64
	}
	var apiRows []usageRow
	a.db.Model(&models.TokenUsageRecord{}).
		Where("user_id = ? AND recorded_at >= ?", userID, start).
		Select("api_endpoint as key, " + creditSelect("credits") + ", count(*) as requests").
		Group("api_endpoint").Order("credits DESC").Limit(5).Scan(&apiRows)
	topAPIs := make([]gin.H, 0, len(apiRows))
	for _, row := range apiRows {
		topAPIs = append(topAPIs, gin.H{"api_endpoint": row.Key, "token_count": row.Credits, "credit_count": row.Credits, "request_count": row.Requests})
	}

	var modelRows []usageRow
	a.db.Model(&models.TokenUsageRecord{}).
		Where("user_id = ? AND recorded_at >= ?", userID, start).
		Select("coalesce(nullif(model_id, ''), model_name) as key, " + creditSelect("credits") + ", count(*) as requests").
		Group("coalesce(nullif(model_id, ''), model_name)").Order("credits DESC").Limit(5).Scan(&modelRows)
	topModels := make([]gin.H, 0, len(modelRows))
	for _, row := range modelRows {
		topModels = append(topModels, gin.H{"model_name": row.Key, "model_id": row.Key, "token_count": row.Credits, "credit_count": row.Credits, "request_count": row.Requests})
	}

	type trendRow struct {
		Date     string
		Credits  int64
		Requests int64
	}
	var trendRows []trendRow
	a.db.Model(&models.TokenUsageRecord{}).
		Where("user_id = ? AND recorded_at >= ?", userID, start).
		Select("date(recorded_at) as date, " + creditSelect("credits") + ", count(*) as requests").
		Group("date(recorded_at)").Order("date ASC").Scan(&trendRows)
	dailyTrend := make([]gin.H, 0, len(trendRows))
	for _, row := range trendRows {
		dailyTrend = append(dailyTrend, gin.H{"date": row.Date, "token_count": row.Credits, "credit_count": row.Credits, "request_count": row.Requests})
	}

	return gin.H{
		"period_days":   days,
		"total_used":    totalUsed,
		"daily_average": float64(totalUsed) / float64(days),
		"top_apis":      topAPIs,
		"top_models":    topModels,
		"daily_trend":   dailyTrend,
	}
}
