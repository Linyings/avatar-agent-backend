package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
)

const (
	creditLedgerEventConsume         = "consume"
	creditLedgerEventRecharge        = "recharge"
	creditLedgerEventRefund          = "refund"
	creditLedgerEventAdminAdjustment = "admin_adjustment"
	creditLedgerEventExpiry          = "expiry"
	creditLedgerConsumeItemLabel     = "Credits 消费"
	creditLedgerConsumeLLMLabel      = "LLM 消费"
	creditLedgerConsumeVisionLabel   = "Vision 消费"
	creditLedgerConsumeASRLabel      = "ASR 消费"
	creditLedgerConsumeTTSLabel      = "TTS 消费"
)

type creditLedgerEntry struct {
	UserID          uuid.UUID
	ProjectID       *uuid.UUID
	ProjectName     string
	EventType       string
	Item            string
	ChangeAmount    int64
	BalanceAfter    int64
	Reason          string
	RequestMetadata map[string]any
	RecordedAt      time.Time
}

type creditLedgerFilters struct {
	ClientTaskID string
	EventType    string
	ProjectID    *uuid.UUID
}

type creditLedgerAggregate struct {
	Income  int64 `gorm:"column:income"`
	Expense int64 `gorm:"column:expense"`
	Net     int64 `gorm:"column:net"`
}

func (a *App) createCreditLedgerRecord(tx *gorm.DB, entry creditLedgerEntry) error {
	db := a.db
	if tx != nil {
		db = tx
	}

	projectName := strings.TrimSpace(entry.ProjectName)
	if entry.ProjectID != nil && projectName == "" {
		var project models.Project
		if err := db.Unscoped().Select("name").First(&project, "id = ?", *entry.ProjectID).Error; err == nil {
			projectName = project.Name
		}
	}

	record := models.CreditLedgerRecord{
		UserID:          entry.UserID,
		ProjectID:       entry.ProjectID,
		ProjectName:     projectName,
		EventType:       strings.TrimSpace(entry.EventType),
		Item:            strings.TrimSpace(entry.Item),
		ChangeAmount:    entry.ChangeAmount,
		BalanceAfter:    entry.BalanceAfter,
		Reason:          strings.TrimSpace(entry.Reason),
		RequestMetadata: jsonMap(cloneMetadata(entry.RequestMetadata)),
		RecordedAt:      entry.RecordedAt,
	}
	return db.Create(&record).Error
}

func (a *App) creditLedgerRecords(c *gin.Context) {
	user := getUser(c)
	page, limit := pageLimit(c)

	filters := creditLedgerFilters{
		EventType:    strings.TrimSpace(c.Query("event_type")),
		ClientTaskID: strings.TrimSpace(c.Query("client_task_id")),
	}
	if projectID := strings.TrimSpace(c.Query("project_id")); projectID != "" {
		parsed, err := uuid.Parse(projectID)
		if err != nil {
			fail(c, http.StatusBadRequest, "无效的项目 ID")
			return
		}
		filters.ProjectID = &parsed
	}

	var total int64
	if err := a.creditLedgerRecordsQuery(user.ID, filters).Count(&total).Error; err != nil {
		fail(c, http.StatusInternalServerError, "获取消费记录总数失败")
		return
	}

	var aggregate creditLedgerAggregate
	if err := a.creditLedgerRecordsQuery(user.ID, filters).
		Select(`
			COALESCE(SUM(CASE WHEN change_amount > 0 THEN change_amount ELSE 0 END), 0) AS income,
			COALESCE(SUM(CASE WHEN event_type = ? AND change_amount < 0 THEN -change_amount ELSE 0 END), 0) AS expense,
			COALESCE(SUM(change_amount), 0) AS net
		`, creditLedgerEventConsume).
		Scan(&aggregate).Error; err != nil {
		fail(c, http.StatusInternalServerError, "获取消费记录汇总失败")
		return
	}

	var items []models.CreditLedgerRecord
	if err := a.creditLedgerRecordsQuery(user.ID, filters).
		Order("recorded_at DESC, created_at DESC, id DESC").
		Offset((page - 1) * limit).
		Limit(limit).
		Find(&items).Error; err != nil {
		fail(c, http.StatusInternalServerError, "获取消费记录失败")
		return
	}

	projectNames := a.creditLedgerProjectNames(user.ID, items, filters.ProjectID)

	serializedItems := make([]gin.H, 0, len(items))
	for _, item := range items {
		if item.ProjectID != nil {
			if projectName := strings.TrimSpace(projectNames[*item.ProjectID]); projectName != "" {
				item.ProjectName = projectName
			}
		}
		serializedItems = append(serializedItems, serializeCreditLedgerRecord(item))
	}

	projectName := ""
	if filters.ProjectID != nil {
		projectName = strings.TrimSpace(projectNames[*filters.ProjectID])
	}
	totalPages := (total + int64(limit) - 1) / int64(limit)
	ok(c, gin.H{
		"items":       serializedItems,
		"total":       total,
		"page":        page,
		"limit":       limit,
		"per_page":    limit,
		"total_pages": totalPages,
		"pages":       totalPages,
		"has_next":    int64(page) < totalPages,
		"has_prev":    page > 1,
		"summary": gin.H{
			"income":                 aggregate.Income,
			"expense":                aggregate.Expense,
			"net":                    aggregate.Net,
			"total_consumed_credits": aggregate.Expense,
			"client_task_id":         filters.ClientTaskID,
			"project_id":             filters.ProjectID,
			"project_name":           projectName,
		},
	})
}

func (a *App) creditLedgerRecordDetail(c *gin.Context) {
	user := getUser(c)
	recordID, okID := parseIDParam(c, "record_id")
	if !okID {
		return
	}

	var record models.CreditLedgerRecord
	result := a.db.Where("id = ? AND user_id = ?", recordID, user.ID).Limit(1).Find(&record)
	if result.Error != nil {
		fail(c, http.StatusInternalServerError, "获取消费记录详情失败")
		return
	}
	if result.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "消费记录不存在")
		return
	}

	if record.ProjectID != nil {
		projectNames := a.creditLedgerProjectNames(user.ID, []models.CreditLedgerRecord{record}, nil)
		if projectName := strings.TrimSpace(projectNames[*record.ProjectID]); projectName != "" {
			record.ProjectName = projectName
		}
	}

	metadata := decodeCreditLedgerRequestMetadata(record.RequestMetadata)
	usageRecords, err := a.creditLedgerUsageRecordsForDetail(record)
	if err != nil {
		fail(c, http.StatusInternalServerError, "获取消费明细失败")
		return
	}

	serviceBreakdown := int64MapFromMetadata(metadata, "service_breakdown")
	endpointBreakdown := int64MapFromMetadata(metadata, "endpoint_breakdown")
	needsServiceBreakdown := len(serviceBreakdown) == 0
	needsEndpointBreakdown := len(endpointBreakdown) == 0
	if needsServiceBreakdown || needsEndpointBreakdown {
		for _, usage := range usageRecords {
			if needsServiceBreakdown {
				serviceBreakdown[usage.ServiceKind] += usage.CreditCount
			}
			if needsEndpointBreakdown {
				endpointBreakdown[usage.APIEndpoint] += usage.CreditCount
			}
		}
	}

	serializedUsage := make([]gin.H, 0, len(usageRecords))
	for _, usage := range usageRecords {
		serializedUsage = append(serializedUsage, serializeCreditLedgerUsageRecord(usage, user.IsAdmin))
	}

	ok(c, gin.H{
		"record":             serializeCreditLedgerRecord(record),
		"usage_records":      serializedUsage,
		"service_breakdown":  serviceBreakdown,
		"endpoint_breakdown": endpointBreakdown,
		"reservation_count":  int64ValueFromMetadata(metadata, "reservation_count"),
		"billing_mode":       billingStringValue(firstPresent(metadata, "billing_mode")),
	})
}

func (a *App) creditLedgerUsageRecordsForDetail(record models.CreditLedgerRecord) ([]models.TokenUsageRecord, error) {
	if record.EventType != creditLedgerEventConsume {
		return []models.TokenUsageRecord{}, nil
	}

	metadata := decodeCreditLedgerRequestMetadata(record.RequestMetadata)
	isWorkflowConfirm := billingStringValue(firstPresent(metadata, "billing_mode")) == "workflow_reservation_confirm"
	q := a.db.Where("user_id = ?", record.UserID)
	if record.ProjectID != nil {
		q = q.Where("project_id = ?", *record.ProjectID)
		if !isWorkflowConfirm {
			if reason := strings.TrimSpace(record.Reason); reason != "" {
				q = q.Where("api_endpoint = ?", reason)
			}
			if record.ChangeAmount < 0 {
				q = q.Where("credit_count = ?", -record.ChangeAmount)
			}
		}
	} else {
		if reason := strings.TrimSpace(record.Reason); reason != "" {
			q = q.Where("api_endpoint = ?", reason)
		}
		if record.ChangeAmount < 0 {
			q = q.Where("credit_count = ?", -record.ChangeAmount)
		}
	}

	var usageRecords []models.TokenUsageRecord
	err := q.Order("recorded_at ASC, id ASC").Find(&usageRecords).Error
	return usageRecords, err
}

func (a *App) creditLedgerRecordsQuery(userID uuid.UUID, filters creditLedgerFilters) *gorm.DB {
	q := a.db.Model(&models.CreditLedgerRecord{}).Where("user_id = ?", userID)
	if filters.ClientTaskID != "" {
		q = q.Where("request_metadata ->> 'workflow_run_id' IN (?)", a.db.Model(&models.WorkflowRun{}).Select("CAST(id AS TEXT)").Where("user_id = ? AND client_type = ? AND client_task_id = ?", userID, "pc", filters.ClientTaskID))
	}
	if filters.EventType != "" {
		q = q.Where("event_type = ?", filters.EventType)
	}
	if filters.ProjectID != nil {
		q = q.Where("project_id = ?", *filters.ProjectID)
	}
	return q
}

func (a *App) creditLedgerProjectNames(userID uuid.UUID, records []models.CreditLedgerRecord, extraProjectID *uuid.UUID) map[uuid.UUID]string {
	projectIDs := make(map[uuid.UUID]struct{})
	for _, record := range records {
		if record.ProjectID != nil {
			projectIDs[*record.ProjectID] = struct{}{}
		}
	}
	if extraProjectID != nil {
		projectIDs[*extraProjectID] = struct{}{}
	}
	if len(projectIDs) == 0 {
		return map[uuid.UUID]string{}
	}

	ids := make([]uuid.UUID, 0, len(projectIDs))
	for id := range projectIDs {
		ids = append(ids, id)
	}

	var projects []models.Project
	if err := a.db.Unscoped().
		Select("id", "name").
		Where("owner_id = ? AND id IN ?", userID, ids).
		Find(&projects).Error; err != nil {
		return map[uuid.UUID]string{}
	}

	names := make(map[uuid.UUID]string, len(projects))
	for _, project := range projects {
		if name := strings.TrimSpace(project.Name); name != "" {
			names[project.ID] = name
		}
	}
	return names
}

func serializeCreditLedgerRecord(record models.CreditLedgerRecord) gin.H {
	item := strings.TrimSpace(record.Item)
	reason := strings.TrimSpace(record.Reason)
	requestMetadata := any(record.RequestMetadata)
	if len(record.RequestMetadata) == 0 {
		requestMetadata = gin.H{}
	}

	if record.EventType == creditLedgerEventConsume {
		item = creditLedgerConsumeItemLabelForRecord(record)
		reason = ""
		requestMetadata = gin.H{}
	}

	return gin.H{
		"id":               record.ID,
		"created_at":       record.CreatedAt,
		"updated_at":       record.UpdatedAt,
		"deleted_at":       record.DeletedAt,
		"user_id":          record.UserID,
		"project_id":       record.ProjectID,
		"project_name":     record.ProjectName,
		"event_type":       record.EventType,
		"item":             item,
		"change_amount":    record.ChangeAmount,
		"balance_after":    record.BalanceAfter,
		"reason":           reason,
		"request_metadata": requestMetadata,
		"recorded_at":      record.RecordedAt,
	}
}

func serializeCreditLedgerUsageRecord(record models.TokenUsageRecord, includeFinancials bool) gin.H {
	item := gin.H{
		"id":                 record.ID,
		"user_id":            record.UserID,
		"project_id":         record.ProjectID,
		"token_count":        record.TokenCount,
		"credit_count":       record.CreditCount,
		"api_endpoint":       record.APIEndpoint,
		"model_name":         record.ModelName,
		"model_id":           record.ModelID,
		"provider":           record.Provider,
		"service_kind":       record.ServiceKind,
		"prompt_tokens":      record.PromptTokens,
		"completion_tokens":  record.CompletionTokens,
		"total_model_tokens": record.TotalModelTokens,
		"utf8_bytes":         record.UTF8Bytes,
		"duration_seconds":   record.DurationSeconds,
		"billing_month":      record.BillingMonth,
		"missing_pricing":    record.MissingPricing,
		"recorded_at":        record.RecordedAt,
	}
	if includeFinancials {
		item["request_metadata"] = record.RequestMetadata
		item["cost_amount"] = record.CostAmount
		item["revenue_amount"] = record.RevenueAmount
		item["profit_amount"] = record.ProfitAmount
	}
	return item
}

func creditLedgerConsumeItemLabelForRecord(record models.CreditLedgerRecord) string {
	if item := sanitizeCreditLedgerConsumeItem(strings.TrimSpace(record.Item)); item != "" {
		return item
	}

	if metadata := decodeCreditLedgerRequestMetadata(record.RequestMetadata); metadata != nil {
		if serviceKind := billingStringValue(firstPresent(metadata, "billing_service_kind", "service_kind")); serviceKind != "" {
			return creditLedgerConsumeItemLabelForServiceKind(serviceKind)
		}
		if rate, ok := metadata["billing_rate"].(map[string]any); ok {
			if serviceKind := billingStringValue(firstPresent(rate, "service_kind")); serviceKind != "" {
				return creditLedgerConsumeItemLabelForServiceKind(serviceKind)
			}
		}
	}

	if serviceKind := inferCreditLedgerServiceKindFromReason(record.Reason); serviceKind != "" {
		return creditLedgerConsumeItemLabelForServiceKind(serviceKind)
	}

	return creditLedgerConsumeItemLabel
}

func int64MapFromMetadata(metadata map[string]any, key string) map[string]int64 {
	result := map[string]int64{}
	value, ok := metadata[key]
	if !ok {
		return result
	}
	raw, ok := value.(map[string]any)
	if !ok {
		return result
	}
	for itemKey, itemValue := range raw {
		if normalizedKey := strings.TrimSpace(itemKey); normalizedKey != "" {
			result[normalizedKey] = ledgerInt64Value(itemValue)
		}
	}
	return result
}

func int64ValueFromMetadata(metadata map[string]any, key string) int64 {
	if metadata == nil {
		return 0
	}
	return ledgerInt64Value(metadata[key])
}

func ledgerInt64Value(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	default:
		return int64(floatValue(value))
	}
}

func sanitizeCreditLedgerConsumeItem(item string) string {
	switch strings.TrimSpace(item) {
	case creditLedgerConsumeLLMLabel,
		creditLedgerConsumeVisionLabel,
		creditLedgerConsumeASRLabel,
		creditLedgerConsumeTTSLabel,
		creditLedgerConsumeItemLabel:
		return strings.TrimSpace(item)
	default:
		return ""
	}
}

func creditLedgerConsumeItemLabelForServiceKind(serviceKind string) string {
	switch normalizeBillingKey(serviceKind) {
	case "llm":
		return creditLedgerConsumeLLMLabel
	case "vision":
		return creditLedgerConsumeVisionLabel
	case "asr":
		return creditLedgerConsumeASRLabel
	case "tts":
		return creditLedgerConsumeTTSLabel
	default:
		return creditLedgerConsumeItemLabel
	}
}

func decodeCreditLedgerRequestMetadata(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil
	}
	return metadata
}

func projectIDFromString(raw string) (*uuid.UUID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func projectIDFromRequest(req map[string]any) (*uuid.UUID, error) {
	if req == nil {
		return nil, nil
	}
	return projectIDFromString(billingStringValue(firstPresent(req, "project_id", "projectId")))
}

func removeProjectIDFromRequest(req map[string]any) {
	delete(req, "project_id")
	delete(req, "projectId")
}

func inferCreditLedgerServiceKindFromReason(reason string) string {
	reason = normalizeBillingKey(reason)
	switch {
	case strings.Contains(reason, "/asr/"), strings.Contains(reason, "scribe/transcribe"):
		return "asr"
	case strings.Contains(reason, "timeline-view/analyze"), strings.Contains(reason, "analyze-images"), strings.Contains(reason, "ocr-images"):
		return "vision"
	case strings.Contains(reason, "text-to-speech"), strings.Contains(reason, "/tts/"), strings.Contains(reason, "/speech/"):
		return "tts"
	case strings.Contains(reason, "generate-script-stream"), strings.Contains(reason, "generate-narration-draft-stream"), strings.Contains(reason, "match-narration-visuals-stream"), strings.Contains(reason, "timeline-view/decide"), strings.Contains(reason, "narrative-analyze"), strings.Contains(reason, "subtitles/validate"):
		return "llm"
	default:
		return ""
	}
}
