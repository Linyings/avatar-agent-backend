package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/prompts"
	"github.com/narratoai/backend-go/internal/providers"
	"gorm.io/gorm"
)

const (
	streamDeltaFlushInterval      = 80 * time.Millisecond
	streamDeltaFlushEvents        = 24
	streamDeltaFlushRunes         = 160
	streamToolHeartbeatInterval   = 15 * time.Second
	adminAnnouncementSource       = "admin_announcement"
	adminAnnouncementAudience     = "all_active_users"
	adminAnnouncementLegacyPrefix = "legacy:"
	adminAnnouncementBatchSize    = 500
	maxUploadFileSizeBytes        = 2 * 1024 * 1024 * 1024
)

var malformedJSONStringTerminator = regexp.MustCompile(`',\s*['"]([A-Za-z_][A-Za-z0-9_]*)"\s*:`)

var allowedMediaProxyHosts = map[string]struct{}{
	"store.narratoai.cn": {},
	"r2.narratoai.cn":    {},
}

type adminAnnouncementSummary struct {
	ID              string     `json:"id"`
	Type            string     `json:"type"`
	Title           string     `json:"title"`
	Message         string     `json:"message"`
	ExpiresAt       *time.Time `json:"expires_at"`
	PublishedAt     time.Time  `json:"published_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	TargetUserCount int        `json:"target_user_count"`
}

type elevenLabsScribeJob struct {
	UserID    uuid.UUID
	ProjectID *uuid.UUID
	Filename  string
	Model     string
}

type adminAnnouncementGroup struct {
	ID              string
	Type            string
	Title           string
	Message         string
	ExpiresAt       *time.Time
	PublishedAt     time.Time
	UpdatedAt       time.Time
	TargetUserCount int
}

type legacyAnnouncementRef struct {
	Type      string  `json:"type"`
	Title     string  `json:"title"`
	Message   string  `json:"message"`
	CreatedAt string  `json:"created_at"`
	ExpiresAt *string `json:"expires_at,omitempty"`
}

type streamToolEmitter struct {
	mu           sync.Mutex
	c            *gin.Context
	flusher      http.Flusher
	thinking     strings.Builder
	answer       strings.Builder
	pendingCount int
	lastFlush    time.Time
}

type bufferedToolEvent struct {
	event string
	data  map[string]any
}

type bufferedToolEmitter struct {
	events []bufferedToolEvent
}

func (e *bufferedToolEmitter) emit(event string, data map[string]any) error {
	e.events = append(e.events, bufferedToolEvent{event: event, data: data})
	return nil
}

func (e *bufferedToolEmitter) replay(c *gin.Context, stage, message string) (http.Flusher, error) {
	flusher := beginToolEventStream(c, stage, message)
	emitter := newStreamToolEmitter(c, flusher)
	for _, buffered := range e.events {
		if err := emitter.emit(buffered.event, buffered.data); err != nil {
			return flusher, err
		}
	}
	return flusher, emitter.flush()
}

func newStreamToolEmitter(c *gin.Context, flusher http.Flusher) *streamToolEmitter {
	return &streamToolEmitter{c: c, flusher: flusher, lastFlush: time.Now()}
}

func (e *streamToolEmitter) emit(event string, data map[string]any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.emitLocked(event, data)
}

func (e *streamToolEmitter) emitLocked(event string, data map[string]any) error {
	switch event {
	case "thinking_delta", "answer_delta":
		return e.bufferDeltaLocked(event, data)
	case "heartbeat":
		e.writeCommentLocked("heartbeat")
		return nil
	default:
		if err := e.flushLocked(); err != nil {
			return err
		}
		e.writeLocked(event, data)
		return nil
	}
}

func (e *streamToolEmitter) bufferDelta(event string, data map[string]any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.bufferDeltaLocked(event, data)
}

func (e *streamToolEmitter) bufferDeltaLocked(event string, data map[string]any) error {
	delta, _ := data["delta"].(string)
	if delta == "" {
		return nil
	}

	if event == "thinking_delta" {
		if e.answer.Len() > 0 {
			if err := e.flushLocked(); err != nil {
				return err
			}
		}
		e.thinking.WriteString(delta)
	} else {
		if e.thinking.Len() > 0 {
			if err := e.flushLocked(); err != nil {
				return err
			}
		}
		e.answer.WriteString(delta)
	}

	e.pendingCount++
	if e.pendingCount >= streamDeltaFlushEvents ||
		maxRuneCount(e.thinking.String(), e.answer.String()) >= streamDeltaFlushRunes ||
		time.Since(e.lastFlush) >= streamDeltaFlushInterval {
		return e.flushLocked()
	}
	return nil
}

func (e *streamToolEmitter) flush() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.flushLocked()
}

func (e *streamToolEmitter) flushLocked() error {
	if e.thinking.Len() > 0 {
		e.writeLocked("thinking_delta", gin.H{"delta": e.thinking.String()})
		e.thinking.Reset()
	}
	if e.answer.Len() > 0 {
		e.writeLocked("answer_delta", gin.H{"delta": e.answer.String()})
		e.answer.Reset()
	}
	e.pendingCount = 0
	e.lastFlush = time.Now()
	return nil
}

func (e *streamToolEmitter) write(event string, data map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.writeLocked(event, data)
}

func (e *streamToolEmitter) writeLocked(event string, data map[string]any) {
	e.c.SSEvent(event, data)
	if e.flusher != nil {
		e.flusher.Flush()
	}
}

func (e *streamToolEmitter) writeComment(comment string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.writeCommentLocked(comment)
}

func (e *streamToolEmitter) writeCommentLocked(comment string) {
	_, _ = e.c.Writer.WriteString(": " + comment + "\n\n")
	if e.flusher != nil {
		e.flusher.Flush()
	}
}

func startToolStreamHeartbeat(ctx context.Context, emitter *streamToolEmitter, interval time.Duration) func() {
	if emitter == nil || interval <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				_ = emitter.emit("heartbeat", nil)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-stopped
	}
}

func maxRuneCount(values ...string) int {
	maxCount := 0
	for _, value := range values {
		if count := len([]rune(value)); count > maxCount {
			maxCount = count
		}
	}
	return maxCount
}

func decodeNotificationData(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}

	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil
	}
	return data
}

func isAdminAnnouncement(item models.Notification) bool {
	if item.AnnouncementID != nil {
		return true
	}

	data := decodeNotificationData(item.Data)
	if data == nil {
		return false
	}

	source, _ := data["source"].(string)
	return source == adminAnnouncementSource
}

func encodeLegacyAnnouncementRef(item models.Notification) string {
	ref := legacyAnnouncementRef{
		Type:      item.Type,
		Title:     item.Title,
		Message:   item.Message,
		CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if item.ExpiresAt != nil {
		expiresAt := item.ExpiresAt.UTC().Format(time.RFC3339Nano)
		ref.ExpiresAt = &expiresAt
	}

	payload, err := json.Marshal(ref)
	if err != nil {
		return ""
	}

	return adminAnnouncementLegacyPrefix + base64.RawURLEncoding.EncodeToString(payload)
}

func decodeLegacyAnnouncementRef(raw string) (legacyAnnouncementRef, error) {
	if !strings.HasPrefix(raw, adminAnnouncementLegacyPrefix) {
		return legacyAnnouncementRef{}, fmt.Errorf("invalid announcement id")
	}

	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, adminAnnouncementLegacyPrefix))
	if err != nil {
		return legacyAnnouncementRef{}, err
	}

	var ref legacyAnnouncementRef
	if err := json.Unmarshal(payload, &ref); err != nil {
		return legacyAnnouncementRef{}, err
	}

	if ref.Type == "" || ref.Title == "" || ref.Message == "" || ref.CreatedAt == "" {
		return legacyAnnouncementRef{}, fmt.Errorf("invalid announcement ref")
	}

	return ref, nil
}

func groupAdminAnnouncements(items []models.Notification) []adminAnnouncementSummary {
	groups := make(map[string]*adminAnnouncementGroup)

	for _, item := range items {
		if !isAdminAnnouncement(item) {
			continue
		}

		groupID := ""
		if item.AnnouncementID != nil {
			groupID = item.AnnouncementID.String()
		} else {
			groupID = encodeLegacyAnnouncementRef(item)
		}
		if groupID == "" {
			continue
		}

		group, exists := groups[groupID]
		if !exists {
			group = &adminAnnouncementGroup{
				ID:              groupID,
				Type:            item.Type,
				Title:           item.Title,
				Message:         item.Message,
				ExpiresAt:       item.ExpiresAt,
				PublishedAt:     item.CreatedAt,
				UpdatedAt:       item.UpdatedAt,
				TargetUserCount: 0,
			}
			groups[groupID] = group
		}

		group.TargetUserCount++
		if item.CreatedAt.Before(group.PublishedAt) {
			group.PublishedAt = item.CreatedAt
		}
		if item.UpdatedAt.After(group.UpdatedAt) {
			group.UpdatedAt = item.UpdatedAt
		}
	}

	summaries := make([]adminAnnouncementSummary, 0, len(groups))
	for _, group := range groups {
		summaries = append(summaries, adminAnnouncementSummary{
			ID:              group.ID,
			Type:            group.Type,
			Title:           group.Title,
			Message:         group.Message,
			ExpiresAt:       group.ExpiresAt,
			PublishedAt:     group.PublishedAt,
			UpdatedAt:       group.UpdatedAt,
			TargetUserCount: group.TargetUserCount,
		})
	}

	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].PublishedAt.Equal(summaries[j].PublishedAt) {
			return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt)
		}
		return summaries[i].PublishedAt.After(summaries[j].PublishedAt)
	})

	return summaries
}

func adminAnnouncementListQuery(db *gorm.DB) *gorm.DB {
	return db.Model(&models.Notification{}).
		Where("announcement_id IS NOT NULL OR CAST(data AS TEXT) LIKE ?", "%"+adminAnnouncementSource+"%")
}

func legacyAdminAnnouncementQuery(db *gorm.DB, ref legacyAnnouncementRef) (*gorm.DB, error) {
	createdAt, err := time.Parse(time.RFC3339Nano, ref.CreatedAt)
	if err != nil {
		return nil, err
	}

	query := db.Model(&models.Notification{}).
		Where("announcement_id IS NULL").
		Where("type = ? AND title = ? AND message = ? AND created_at = ?", ref.Type, ref.Title, ref.Message, createdAt).
		Where("CAST(data AS TEXT) LIKE ?", "%"+adminAnnouncementSource+"%")

	if ref.ExpiresAt == nil || strings.TrimSpace(*ref.ExpiresAt) == "" {
		return query.Where("expires_at IS NULL"), nil
	}

	expiresAt, err := time.Parse(time.RFC3339Nano, *ref.ExpiresAt)
	if err != nil {
		return nil, err
	}

	return query.Where("expires_at = ?", expiresAt), nil
}

func adminAnnouncementQuery(db *gorm.DB, announcementID string) (*gorm.DB, *uuid.UUID, error) {
	if parsedID, err := uuid.Parse(announcementID); err == nil {
		return db.Model(&models.Notification{}).Where("announcement_id = ?", parsedID), &parsedID, nil
	}

	ref, err := decodeLegacyAnnouncementRef(announcementID)
	if err != nil {
		return nil, nil, err
	}

	query, err := legacyAdminAnnouncementQuery(db, ref)
	if err != nil {
		return nil, nil, err
	}
	return query, nil, nil
}

func (a *App) publicSystemSettings(c *gin.Context) {
	var items []models.SystemSetting
	a.db.Find(&items, "is_public = ?", true)
	out := gin.H{}
	for _, item := range items {
		out[item.Key] = item.Value
	}
	ok(c, out)
}

func (a *App) createSystemSetting(c *gin.Context) {
	user := getUser(c)
	var req struct {
		Key         string         `json:"key" binding:"required"`
		Value       map[string]any `json:"value"`
		Description *string        `json:"description"`
		IsPublic    bool           `json:"is_public"`
	}
	if !bindJSON(c, &req) {
		return
	}
	item := models.SystemSetting{Key: req.Key, Value: jsonMap(req.Value), Description: req.Description, IsPublic: req.IsPublic, UpdatedBy: &user.ID}
	if req.Key == "billing_rates" {
		if err := validateModeBillingConfig(req.Value); err != nil {
			fail(c, 400, err.Error())
			return
		}
		item.IsPublic = false
	}
	if err := a.db.Create(&item).Error; err != nil {
		fail(c, http.StatusConflict, "配置键已存在")
		return
	}
	created(c, item)
}

func (a *App) listSystemSettings(c *gin.Context) {
	var items []models.SystemSetting
	a.db.Order("created_at DESC").Find(&items)
	ok(c, items)
}

func (a *App) getSystemSetting(c *gin.Context) {
	var item models.SystemSetting
	if err := a.db.First(&item, "key = ?", c.Param("key")).Error; err != nil {
		fail(c, http.StatusNotFound, "配置不存在")
		return
	}
	ok(c, item)
}

func (a *App) updateSystemSetting(c *gin.Context) {
	user := getUser(c)
	var item models.SystemSetting
	if err := a.db.First(&item, "key = ?", c.Param("key")).Error; err != nil {
		fail(c, http.StatusNotFound, "配置不存在")
		return
	}
	var req map[string]any
	if !bindJSON(c, &req) {
		return
	}
	if v, ok := req["value"]; ok {
		if item.Key == "billing_rates" {
			if err := validateModeBillingConfig(v); err != nil {
				fail(c, 400, err.Error())
				return
			}
		}
		item.Value = jsonMap(v)
	}
	if v, ok := req["description"].(string); ok {
		item.Description = &v
	}
	if v, ok := req["is_public"].(bool); ok {
		item.IsPublic = v
	}
	item.UpdatedBy = &user.ID
	if item.Key == "billing_rates" {
		item.IsPublic = false
	}
	a.db.Save(&item)
	ok(c, item)
}

func (a *App) deleteSystemSetting(c *gin.Context) {
	a.db.Delete(&models.SystemSetting{}, "key = ?", c.Param("key"))
	ok(c, gin.H{"message": "配置已删除", "success": true})
}

func (a *App) createNotification(c *gin.Context) {
	var req struct {
		UserID    string         `json:"user_id" binding:"required"`
		Type      string         `json:"type" binding:"required"`
		Title     string         `json:"title" binding:"required"`
		Message   string         `json:"message" binding:"required"`
		Data      map[string]any `json:"data"`
		ExpiresAt *time.Time     `json:"expires_at"`
	}
	if !bindJSON(c, &req) {
		return
	}
	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		fail(c, http.StatusBadRequest, "无效用户ID")
		return
	}
	item := models.Notification{UserID: userID, Type: req.Type, Title: req.Title, Message: req.Message, Data: jsonMap(req.Data), ExpiresAt: req.ExpiresAt}
	a.db.Create(&item)
	created(c, item)
}

func (a *App) broadcastAdminNotification(c *gin.Context) {
	var req struct {
		Type      string         `json:"type"`
		Title     string         `json:"title" binding:"required"`
		Message   string         `json:"message" binding:"required"`
		Data      map[string]any `json:"data"`
		ExpiresAt *time.Time     `json:"expires_at"`
	}
	if !bindJSON(c, &req) {
		return
	}

	title := strings.TrimSpace(req.Title)
	message := strings.TrimSpace(req.Message)
	if title == "" || message == "" {
		fail(c, http.StatusBadRequest, "公告标题和正文不能为空")
		return
	}

	notificationType := strings.TrimSpace(req.Type)
	if notificationType == "" {
		notificationType = "system"
	}

	var users []models.User
	if err := a.db.Select("id").Where("is_active = ?", true).Find(&users).Error; err != nil {
		fail(c, http.StatusInternalServerError, "读取用户列表失败")
		return
	}

	publishedAt := time.Now().UTC()
	if len(users) == 0 {
		created(c, gin.H{
			"success":           true,
			"created_count":     0,
			"target_user_count": 0,
			"published_at":      publishedAt.Format(time.RFC3339),
		})
		return
	}

	announcementID := uuid.New()
	data := make(map[string]any, len(req.Data)+2)
	for key, value := range req.Data {
		data[key] = value
	}
	data["source"] = adminAnnouncementSource
	data["audience"] = adminAnnouncementAudience
	data["announcement_id"] = announcementID.String()

	items := make([]models.Notification, 0, len(users))
	for _, user := range users {
		items = append(items, models.Notification{
			UserID:         user.ID,
			AnnouncementID: &announcementID,
			Type:           notificationType,
			Title:          title,
			Message:        message,
			Data:           jsonMap(data),
			ExpiresAt:      req.ExpiresAt,
		})
	}

	if err := a.db.Transaction(func(tx *gorm.DB) error {
		return tx.CreateInBatches(&items, adminAnnouncementBatchSize).Error
	}); err != nil {
		fail(c, http.StatusInternalServerError, "发布公告失败")
		return
	}

	created(c, gin.H{
		"announcement_id":   announcementID.String(),
		"success":           true,
		"created_count":     len(items),
		"target_user_count": len(users),
		"published_at":      publishedAt.Format(time.RFC3339),
	})
}

func (a *App) listAdminAnnouncements(c *gin.Context) {
	var items []models.Notification
	if err := adminAnnouncementListQuery(a.db).Order("created_at DESC").Find(&items).Error; err != nil {
		fail(c, http.StatusInternalServerError, "读取公告列表失败")
		return
	}

	ok(c, groupAdminAnnouncements(items))
}

func (a *App) updateAdminAnnouncement(c *gin.Context) {
	var req struct {
		Title     string     `json:"title" binding:"required"`
		Message   string     `json:"message" binding:"required"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if !bindJSON(c, &req) {
		return
	}

	title := strings.TrimSpace(req.Title)
	message := strings.TrimSpace(req.Message)
	if title == "" || message == "" {
		fail(c, http.StatusBadRequest, "公告标题和正文不能为空")
		return
	}

	query, announcementID, err := adminAnnouncementQuery(a.db, c.Param("announcement_id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "无效公告ID")
		return
	}

	var existingItems []models.Notification
	if err := query.Find(&existingItems).Error; err != nil {
		fail(c, http.StatusInternalServerError, "读取公告失败")
		return
	}
	if len(existingItems) == 0 {
		fail(c, http.StatusNotFound, "公告不存在")
		return
	}

	ids := make([]uuid.UUID, 0, len(existingItems))
	for _, item := range existingItems {
		ids = append(ids, item.ID)
	}

	updates := map[string]any{
		"title":      title,
		"message":    message,
		"expires_at": req.ExpiresAt,
	}

	var targetID uuid.UUID
	if announcementID != nil {
		targetID = *announcementID
	} else {
		targetID = uuid.New()
		updates["announcement_id"] = targetID
	}

	if err := a.db.Model(&models.Notification{}).Where("id IN ?", ids).Updates(updates).Error; err != nil {
		fail(c, http.StatusInternalServerError, "更新公告失败")
		return
	}

	var items []models.Notification
	if err := a.db.Where("id IN ?", ids).Find(&items).Error; err != nil {
		fail(c, http.StatusInternalServerError, "读取更新后的公告失败")
		return
	}

	summaries := groupAdminAnnouncements(items)
	if len(summaries) == 0 {
		fail(c, http.StatusInternalServerError, "更新后的公告数据无效")
		return
	}

	ok(c, summaries[0])
}

func (a *App) deleteAdminAnnouncement(c *gin.Context) {
	query, _, err := adminAnnouncementQuery(a.db, c.Param("announcement_id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "无效公告ID")
		return
	}

	var items []models.Notification
	if err := query.Find(&items).Error; err != nil {
		fail(c, http.StatusInternalServerError, "读取公告失败")
		return
	}
	if len(items) == 0 {
		fail(c, http.StatusNotFound, "公告不存在")
		return
	}

	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}

	if err := a.db.Delete(&models.Notification{}, "id IN ?", ids).Error; err != nil {
		fail(c, http.StatusInternalServerError, "删除公告失败")
		return
	}

	ok(c, gin.H{
		"success":       true,
		"deleted_count": len(ids),
		"message":       "公告已删除",
	})
}

func (a *App) listNotifications(c *gin.Context) {
	user := getUser(c)
	now := time.Now().UTC()
	limit := queryInt(c, "limit", 20)
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	offset := queryInt(c, "offset", 0)
	if offset < 0 {
		offset = 0
	}

	query := a.db.Model(&models.Notification{}).
		Where("user_id = ?", user.ID).
		Where("(expires_at IS NULL OR expires_at >= ?)", now)

	if notificationType := strings.TrimSpace(c.Query("type")); notificationType != "" {
		query = query.Where("type = ?", notificationType)
	}
	if raw := strings.TrimSpace(c.Query("is_read")); raw != "" {
		switch raw {
		case "true":
			query = query.Where("is_read = ?", true)
		case "false":
			query = query.Where("is_read = ?", false)
		default:
			fail(c, http.StatusBadRequest, "is_read 必须是 true 或 false")
			return
		}
	}

	var items []models.Notification
	query.Order("created_at DESC").Limit(limit).Offset(offset).Find(&items)
	ok(c, items)
}

func (a *App) markNotificationRead(c *gin.Context) {
	user := getUser(c)
	id, okID := parseIDParam(c, "notification_id")
	if !okID {
		return
	}
	now := time.Now().UTC()
	var item models.Notification
	if err := a.db.First(&item, "id = ? AND user_id = ?", id, user.ID).Error; err != nil {
		fail(c, http.StatusNotFound, "通知不存在")
		return
	}
	item.IsRead = true
	item.ReadAt = &now
	a.db.Save(&item)
	ok(c, item)
}

func (a *App) markAllNotificationsRead(c *gin.Context) {
	user := getUser(c)
	now := time.Now().UTC()
	a.db.Model(&models.Notification{}).Where("user_id = ? AND is_read = ?", user.ID, false).Updates(map[string]any{"is_read": true, "read_at": now})
	ok(c, gin.H{"message": "全部通知已标记为已读", "success": true})
}

func (a *App) notificationStats(c *gin.Context) {
	user := getUser(c)
	now := time.Now().UTC()
	var total, unread int64
	baseQuery := func() *gorm.DB {
		return a.db.Model(&models.Notification{}).
			Where("user_id = ?", user.ID).
			Where("(expires_at IS NULL OR expires_at >= ?)", now)
	}
	baseQuery().Count(&total)
	baseQuery().Where("is_read = ?", false).Count(&unread)

	var rows []struct {
		Type  string `json:"type"`
		Count int64  `json:"count"`
	}
	baseQuery().
		Select("type, count(*) as count").
		Group("type").
		Scan(&rows)

	byType := make(gin.H, len(rows))
	for _, row := range rows {
		byType[row.Type] = row.Count
	}

	readCount := total - unread
	ok(c, gin.H{
		"total_count":  total,
		"unread_count": unread,
		"read_count":   readCount,
		"by_type":      byType,
		"total":        total,
		"unread":       unread,
		"read":         readCount,
	})
}

func (a *App) deleteNotification(c *gin.Context) {
	user := getUser(c)
	id, okID := parseIDParam(c, "notification_id")
	if !okID {
		return
	}
	a.db.Delete(&models.Notification{}, "id = ? AND user_id = ?", id, user.ID)
	ok(c, gin.H{"message": "通知已删除", "success": true})
}

func (a *App) promptTemplates(c *gin.Context) {
	if a.promptService == nil {
		ok(c, gin.H{"items": []gin.H{}})
		return
	}
	templateTypes := []string{}
	if raw := strings.TrimSpace(c.Query("template_type")); raw != "" {
		templateTypes = append(templateTypes, raw)
	} else if raw := strings.TrimSpace(c.Query("type")); raw != "" {
		templateTypes = append(templateTypes, raw)
	} else {
		templateTypes = append(templateTypes, "fast")
	}
	ok(c, gin.H{"items": a.promptService.List(templateTypes...)})
}

func extractJSONWorkflowIdentity(c *gin.Context, payload map[string]any) (*workflowRequestIdentity, bool) {
	identity, err := extractWorkflowRequestIdentityFromMap(payload)
	if err != nil {
		respondWorkflowRequestError(c, err)
		return nil, false
	}
	return identity, true
}

func (a *App) resolveJSONWorkflowExecution(c *gin.Context, userID uuid.UUID, identity *workflowRequestIdentity) (workflowRequestExecution, bool) {
	return a.resolveJSONWorkflowExecutionForProject(c, userID, nil, identity)
}

func (a *App) resolveJSONWorkflowExecutionForProject(c *gin.Context, userID uuid.UUID, projectID *uuid.UUID, identity *workflowRequestIdentity) (workflowRequestExecution, bool) {
	execution, err := a.resolveWorkflowRequestIdentity(c.Request.Context(), userID, projectID, identity)
	if err != nil {
		respondWorkflowRequestError(c, err)
		return workflowRequestExecution{}, false
	}
	return execution, true
}

func (a *App) respondJSONWorkflowProviderFailure(c *gin.Context, execution workflowRequestExecution, providerErr error) {
	if execution.Identity != nil && execution.Step.Status == workflowRunStatusSucceeded {
		respondJSONWorkflowProviderError(c, providerErr)
		return
	}
	if err := a.finalizeProviderFailure(c.Request.Context(), execution, providerErr); err != nil {
		respondWorkflowRequestError(c, err)
		return
	}
	if execution.Identity == nil {
		fail(c, http.StatusBadGateway, providerErr.Error())
		return
	}
	respondJSONWorkflowProviderError(c, providerErr)
}

func respondJSONWorkflowProviderError(c *gin.Context, providerErr error) {
	_, code, message := safeProviderFailureDiagnostics(providerErr)
	status := http.StatusBadGateway
	if code == "insufficient_credits" {
		status = http.StatusPaymentRequired
	} else if code == "request_timed_out" {
		status = http.StatusGatewayTimeout
	} else if code == "operation_interrupted" {
		status = http.StatusRequestTimeout
	}
	respondWorkflowRequestError(c, newWorkflowRequestError(status, code, code != "provider_account_unavailable" && code != "provider_content_blocked", message, providerErr))
}

func (a *App) respondJSONWorkflowFinalizationError(c *gin.Context, execution workflowRequestExecution, err error) {
	if execution.Identity != nil && execution.Step.Status != workflowRunStatusSucceeded && errors.Is(err, ErrInsufficientCredits) {
		if blockErr := a.finalizeProviderBlocked(c.Request.Context(), execution, "insufficient_credits", "insufficient credits"); blockErr != nil {
			respondWorkflowRequestError(c, blockErr)
			return
		}
		respondWorkflowRequestError(c, newWorkflowRequestError(http.StatusPaymentRequired, "insufficient_credits", false, "insufficient credits", err))
		return
	}
	respondWorkflowRequestError(c, err)
}

func (a *App) checkJSONWorkflowTokenBalance(c *gin.Context, userID uuid.UUID, required int64, execution workflowRequestExecution) bool {
	if execution.Identity != nil && !strings.HasPrefix(execution.Step.StepID, "dubbing-") {
		if free, _ := a.billingModeConfig("general")["free_experience"].(bool); free {
			return true
		}
	}
	if execution.Identity == nil {
		return a.checkTokenBalance(c, userID, required)
	}
	if required <= 0 {
		return true
	}
	_, snapshot, err := a.creditBalanceSnapshot(userID)
	if err != nil {
		respondWorkflowRequestError(c, newWorkflowRequestError(
			http.StatusInternalServerError,
			"credit_balance_unavailable",
			true,
			"credit balance is temporarily unavailable",
			err,
		))
		return false
	}
	if snapshot.RemainingCredits >= required {
		return true
	}
	if err := a.finalizeProviderBlocked(c.Request.Context(), execution, "insufficient_credits", "insufficient credits"); err != nil {
		respondWorkflowRequestError(c, err)
		return false
	}
	respondWorkflowRequestError(c, newWorkflowRequestError(
		http.StatusPaymentRequired,
		"insufficient_credits",
		false,
		"insufficient credits",
		nil,
	))
	return false
}

func (a *App) checkJSONWorkflowCreditBalance(c *gin.Context, userID uuid.UUID, consumption creditConsumption, execution workflowRequestExecution) bool {
	if execution.Identity == nil {
		return a.checkCreditBalance(c, userID, consumption)
	}
	requiredCredits, err := a.estimateCreditCountStrict(execution.EnrichCreditConsumption(consumption))
	if err != nil {
		errorCode := "billing_configuration_invalid"
		errorMessage := "operation failed"
		_, finalizeErr := a.workflowRuns.UpsertStep(
			c.Request.Context(),
			execution.Run.UserID,
			execution.Run.ID,
			WorkflowStepUpdateInput{
				StepID:           execution.Identity.WorkflowStepID,
				InputHash:        execution.Identity.WorkflowInputHash,
				Status:           workflowStepStatusFailed,
				ClientInstanceID: execution.Identity.ClientInstanceID,
				ErrorCode:        &errorCode,
				ErrorMessage:     &errorMessage,
			},
		)
		if finalizeErr != nil {
			respondWorkflowRequestError(c, finalizeErr)
			return false
		}
		respondWorkflowRequestError(c, newWorkflowRequestError(
			http.StatusBadRequest,
			"billing_configuration_invalid",
			false,
			"billing configuration is invalid",
			err,
		))
		return false
	}
	return a.checkJSONWorkflowTokenBalance(c, userID, requiredCredits, execution)
}

func (a *App) analyzeImages(c *gin.Context) {
	user := getUser(c)
	var req map[string]any
	if !bindJSON(c, &req) {
		return
	}
	identity, identityOK := extractJSONWorkflowIdentity(c, req)
	if !identityOK {
		return
	}
	images, _ := req["images"].([]any)
	if len(images) == 0 {
		fail(c, http.StatusBadRequest, "images 不能为空")
		return
	}
	if identity == nil && !a.checkTokenBalance(c, user.ID, int64(len(images)*50)) {
		return
	}
	if !a.silicon.Configured() {
		fail(c, http.StatusServiceUnavailable, "SiliconFlow API Key 未配置")
		return
	}
	execution, executionOK := a.resolveJSONWorkflowExecution(c, user.ID, identity)
	if !executionOK {
		return
	}
	if identity != nil && !a.checkJSONWorkflowTokenBalance(c, user.ID, int64(len(images)*50), execution) {
		return
	}
	result, err := a.silicon.AnalyzeImages(c.Request.Context(), req)
	if err != nil {
		a.respondJSONWorkflowProviderFailure(c, execution, err)
		return
	}
	summary := extractSummary(result.Content)
	billingTokens := result.Usage.TotalTokens / 5
	if err := a.finalizeProviderSuccess(c.Request.Context(), execution, providerSuccessFinalization{
		ProviderRequestID:        optionalStringPointer(result.RequestID),
		RequireProviderRequestID: identity != nil,
		Consumption: creditConsumption{
			UserID:          user.ID,
			CreditCount:     billingTokens,
			APIEndpoint:     "/api/v1/tools/analyze-images",
			Provider:        "siliconflow",
			ServiceKind:     "vision",
			ModelID:         result.Model,
			Usage:           result.Usage,
			RequestMetadata: movieBillingMetadata(c, gin.H{"request_id": result.RequestID, "raw_tokens": result.Usage.TotalTokens, "billing_rate": 0.2}),
		},
	}); err != nil {
		if execution.Identity != nil {
			a.respondJSONWorkflowFinalizationError(c, execution, err)
		} else {
			fail(c, http.StatusPaymentRequired, err.Error())
		}
		return
	}
	ok(c, gin.H{"content": result.Content, "summary": summary, "usage": result.Usage, "model": result.Model, "request_id": result.RequestID})
}

func (a *App) ocrImages(c *gin.Context) {
	user := getUser(c)
	var req map[string]any
	if !bindJSON(c, &req) {
		return
	}
	identity, identityOK := extractJSONWorkflowIdentity(c, req)
	if !identityOK {
		return
	}
	images, _ := req["images"].([]any)
	if len(images) == 0 {
		fail(c, http.StatusBadRequest, "images 不能为空")
		return
	}
	if identity == nil && !a.checkTokenBalance(c, user.ID, int64(len(images)*50)) {
		return
	}
	if !a.silicon.Configured() {
		fail(c, http.StatusServiceUnavailable, "SiliconFlow API Key 未配置")
		return
	}
	execution, executionOK := a.resolveJSONWorkflowExecution(c, user.ID, identity)
	if !executionOK {
		return
	}
	if identity != nil && !a.checkJSONWorkflowTokenBalance(c, user.ID, int64(len(images)*50), execution) {
		return
	}
	result, err := a.silicon.OCRImages(c.Request.Context(), req)
	if err != nil {
		a.respondJSONWorkflowProviderFailure(c, execution, err)
		return
	}
	timestamps, text := extractOCR(result.Content)
	billingTokens := result.Usage.TotalTokens / 5
	if err := a.finalizeProviderSuccess(c.Request.Context(), execution, providerSuccessFinalization{
		ProviderRequestID:        optionalStringPointer(result.RequestID),
		RequireProviderRequestID: identity != nil,
		Consumption: creditConsumption{
			UserID:          user.ID,
			CreditCount:     billingTokens,
			APIEndpoint:     "/api/v1/tools/ocr-images",
			Provider:        "siliconflow",
			ServiceKind:     "vision",
			ModelID:         result.Model,
			Usage:           result.Usage,
			RequestMetadata: movieBillingMetadata(c, gin.H{"request_id": result.RequestID, "raw_tokens": result.Usage.TotalTokens, "billing_rate": 0.2}),
		},
	}); err != nil {
		if execution.Identity != nil {
			a.respondJSONWorkflowFinalizationError(c, execution, err)
		} else {
			fail(c, http.StatusPaymentRequired, err.Error())
		}
		return
	}
	ok(c, gin.H{"timestamps": timestamps, "text": text, "usage": result.Usage, "model": result.Model, "request_id": result.RequestID})
}

func (a *App) referenceVoice(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		fail(c, http.StatusBadRequest, "缺少音频文件")
		return
	}
	opened, err := file.Open()
	if err != nil {
		fail(c, http.StatusBadRequest, "读取音频文件失败")
		return
	}
	defer opened.Close()
	audio, _ := io.ReadAll(opened)
	audioText := strings.TrimSpace(c.PostForm("audio_text"))
	serviceLevel := strings.TrimSpace(c.PostForm("service_level"))
	if serviceLevel == "" {
		serviceLevel = "basic"
	}
	if audioText == "" {
		fail(c, http.StatusBadRequest, "audio_text 不能为空")
		return
	}
	if serviceLevel == "premium" && a.fish.Configured() {
		model, err := a.fish.CreateModel(c.Request.Context(), audio, audioText, file.Filename, "NarratoAI-"+uuid.NewString())
		if err != nil {
			fail(c, http.StatusBadGateway, err.Error())
			return
		}
		created(c, gin.H{"voice_uri": model.ID, "audio_text": audioText, "custom_name": model.Title, "service_level": serviceLevel, "provider": "fishaudio", "created_at": time.Now().Format(time.RFC3339)})
		return
	}
	if !a.silicon.Configured() {
		fail(c, http.StatusServiceUnavailable, "SiliconFlow API Key 未配置")
		return
	}
	voiceURI, err := a.silicon.UploadReferenceVoice(c.Request.Context(), audio, audioText, file.Filename)
	if err != nil {
		fail(c, http.StatusBadGateway, err.Error())
		return
	}
	customName := "unknown"
	parts := strings.Split(voiceURI, ":")
	if len(parts) > 1 {
		customName = parts[1]
	}
	created(c, gin.H{"voice_uri": voiceURI, "audio_text": audioText, "custom_name": customName, "service_level": serviceLevel, "provider": "siliconflow", "created_at": time.Now().Format(time.RFC3339)})
}

func (a *App) textToSpeech(c *gin.Context) {
	endpoint := "/api/v1/tools/text-to-speech-with-uri"
	useQwen := c.Request.URL.Path == "/api/v1/tools/dubbing/tts"
	if useQwen {
		endpoint = "/api/v1/tools/dubbing/tts"
	}
	var instructions string
	user := getUser(c)
	var identity *workflowRequestIdentity
	var text, voice, serviceLevel, projectIDText string
	speed := 1.0
	volume := 1.0
	if c.ContentType() == "application/json" {
		var req map[string]any
		if !bindJSON(c, &req) {
			return
		}
		var identityOK bool
		identity, identityOK = extractJSONWorkflowIdentity(c, req)
		if !identityOK {
			return
		}
		text, _ = req["text"].(string)
		voice, _ = req["voice_uri"].(string)
		instructions, _ = req["instructions"].(string)
		if v, ok := req["service_level"].(string); ok {
			serviceLevel = v
		}
		if v, ok := req["speed"].(float64); ok {
			speed = v
		}
		if v, ok := req["volume"].(float64); ok {
			volume = v
		}
		projectIDText = billingStringValue(firstPresent(req, "project_id", "projectId"))
	} else {
		if c.ContentType() == "multipart/form-data" {
			if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
				fail(c, http.StatusBadRequest, "无效的表单请求")
				return
			}
		} else if err := c.Request.ParseForm(); err != nil {
			fail(c, http.StatusBadRequest, "无效的表单请求")
			return
		}
		values := c.Request.PostForm
		var err error
		identity, err = extractWorkflowRequestIdentityFromValues(values)
		if c.Request.MultipartForm != nil {
			for _, key := range workflowRequestIdentityKeys {
				delete(c.Request.MultipartForm.Value, key)
			}
		}
		if err != nil {
			respondWorkflowRequestError(c, err)
			return
		}
		text = values.Get("text")
		voice = values.Get("voice_uri")
		serviceLevel = values.Get("service_level")
		projectIDText = values.Get("project_id")
		if value := values.Get("speed"); value != "" {
			_, _ = fmt.Sscanf(value, "%f", &speed)
		}
		if value := values.Get("volume"); value != "" {
			_, _ = fmt.Sscanf(value, "%f", &volume)
		}
	}
	text = strings.TrimSpace(text)
	voice = strings.TrimSpace(voice)
	serviceLevel = strings.TrimSpace(serviceLevel)
	projectIDText = strings.TrimSpace(projectIDText)
	if serviceLevel == "" {
		serviceLevel = "basic"
	}
	projectID, err := projectIDFromString(projectIDText)
	if err != nil {
		fail(c, http.StatusBadRequest, "无效的项目 ID")
		return
	}
	if text == "" {
		fail(c, http.StatusBadRequest, "文本内容不能为空")
		return
	}
	if speed < 0.8 || speed > 1.4 {
		fail(c, http.StatusBadRequest, "speed 必须在 0.8 到 1.4 之间")
		return
	}
	if volume < 0 || volume > 2 {
		fail(c, http.StatusBadRequest, "volume 必须在 0.0 到 2.0 之间")
		return
	}

	execution := workflowRequestExecution{}
	versionGeneralVoice := !useQwen && identity != nil && generalFitVoiceStep.MatchString(identity.WorkflowStepID)
	if identity != nil && !versionGeneralVoice {
		var executionOK bool
		execution, executionOK = a.resolveJSONWorkflowExecutionForProject(c, user.ID, projectID, identity)
		if !executionOK {
			return
		}
	}
	utf8Bytes := int64(len([]byte(text)))
	characterCount := int64(len([]rune(text)))
	if useQwen {
		if identity == nil || execution.Run.ClientType != "pc" || !strings.HasPrefix(identity.WorkflowStepID, "dubbing-tts-") {
			fail(c, 400, "千问配音需要 PC 搞笑配音工作流")
			return
		}
		if !providers.ValidDubbingTTSVoice(voice) || characterCount > 600 || len([]rune(instructions)) > 1000 {
			fail(c, 400, "请使用千问音色，单段台词不超过 600 字，表演提示不超过 1000 字")
			return
		}
		// Conservative preflight; settlement uses the provider's character usage.
		characterCount *= 2
	}
	if identity == nil && serviceLevel == "premium" && !a.checkTokenBalance(c, user.ID, utf8Bytes*10) {
		return
	}

	provider := "siliconflow"
	precheckModel := strings.TrimSpace(a.cfg.SiliconFlowTTSModel)
	if precheckModel == "" {
		precheckModel = "FunAudioLLM/CosyVoice2-0.5B"
	}
	useFish := !useQwen && serviceLevel == "premium" && a.fish.Configured()
	if useQwen {
		provider = "dashscope"
		precheckModel = a.dashscope.DubbingTTSModel()
		if !a.dashscope.Configured() || precheckModel == "" {
			fail(c, 503, "千问配音服务未配置")
			return
		}
	} else if useFish {
		provider = "fishaudio"
		fishModel := strings.TrimSpace(a.cfg.FishAudioModel)
		if fishModel == "" {
			fishModel = "s1"
		}
		precheckModel = "fish-audio-" + fishModel
	} else if !a.silicon.Configured() {
		fail(c, http.StatusServiceUnavailable, "SiliconFlow API Key 未配置")
		return
	}
	estimatedDuration := estimateSpeechDuration(text) / speed
	fingerprintInput := map[string]any{
		"endpoint":      endpoint,
		"provider":      provider,
		"model":         precheckModel,
		"text":          text,
		"voice_uri":     voice,
		"service_level": serviceLevel,
		"speed":         speed,
		"volume":        volume,
	}
	if useQwen {
		fingerprintInput["instructions"] = instructions
	}
	inputFingerprint := workflowInputFingerprint(fingerprintInput)
	if versionGeneralVoice {
		identity, err = a.generalFitVoiceIdentity(c.Request.Context(), user.ID, projectID, identity, inputFingerprint)
		if err != nil {
			respondWorkflowRequestError(c, err)
			return
		}
		var executionOK bool
		execution, executionOK = a.resolveJSONWorkflowExecutionForProject(c, user.ID, projectID, identity)
		if !executionOK {
			return
		}
	}
	precheckConsumption := creditConsumption{
		UserID:          user.ID,
		ProjectID:       projectID,
		APIEndpoint:     endpoint,
		Provider:        provider,
		ServiceKind:     "tts",
		ModelID:         precheckModel,
		UTF8Bytes:       utf8Bytes,
		CharacterCount:  characterCount,
		DurationSeconds: &estimatedDuration,
		RequestMetadata: movieBillingMetadata(c, map[string]any{workflowInputFingerprintMetadataKey: inputFingerprint}),
	}
	if identity != nil && execution.Step.Status == workflowRunStatusSucceeded {
		if err := a.finalizeProviderSuccess(c.Request.Context(), execution, providerSuccessFinalization{Consumption: precheckConsumption}); err != nil {
			if errors.Is(err, ErrCreditReservationConflict) {
				respondWorkflowRequestError(c, newWorkflowRequestError(http.StatusConflict, "workflow_input_mismatch", false, "workflow input changed", err))
			} else {
				respondWorkflowRequestError(c, err)
			}
			return
		}
	} else if identity != nil {
		if !a.checkJSONWorkflowCreditBalance(c, user.ID, precheckConsumption, execution) {
			return
		}
	}

	// TTS can outlive the 90-second child-step lease. The desktop heartbeat
	// names its foreground step, not necessarily this per-segment request.
	stopWorkflowHeartbeat := a.startWorkflowExecutionHeartbeat(c.Request.Context(), execution, streamToolHeartbeatInterval)
	defer stopWorkflowHeartbeat()

	var audio []byte
	var model string
	var tokens int64
	if useQwen {
		model = a.dashscope.DubbingTTSModel()
		audio, characterCount, err = a.dashscope.DubbingTextToSpeech(c.Request.Context(), text, voice, instructions, speed)
	} else if useFish {
		audio, model, tokens, err = a.fish.TextToSpeech(c.Request.Context(), text, voice, speed, volume)
	} else {
		audio, model, tokens, err = a.silicon.TextToSpeech(c.Request.Context(), text, voice, speed, volume, serviceLevel)
	}
	if err != nil {
		a.respondJSONWorkflowProviderFailure(c, execution, err)
		return
	}
	if provider == "siliconflow" && serviceLevel == "premium" && tokens == 0 {
		tokens = utf8Bytes * 10
	}
	duration, durationSource := audioDurationWithFallback(audio, text)
	if err := a.finalizeProviderSuccess(c.Request.Context(), execution, providerSuccessFinalization{
		Consumption: creditConsumption{
			UserID:          user.ID,
			ProjectID:       projectID,
			CreditCount:     tokens,
			APIEndpoint:     endpoint,
			Provider:        provider,
			ServiceKind:     "tts",
			ModelID:         model,
			UTF8Bytes:       utf8Bytes,
			CharacterCount:  characterCount,
			DurationSeconds: &duration,
			RequestMetadata: movieBillingMetadata(c, gin.H{"service_level": serviceLevel, "text_length": characterCount, "utf8_bytes": utf8Bytes, "voice_uri": voice, "audio_size": len(audio), "duration_source": durationSource, workflowInputFingerprintMetadataKey: inputFingerprint}),
		},
	}); err != nil {
		if identity != nil {
			if errors.Is(err, ErrInsufficientCredits) {
				if blockErr := a.finalizeProviderBlocked(c.Request.Context(), execution, "insufficient_credits", "insufficient credits"); blockErr != nil {
					respondWorkflowRequestError(c, blockErr)
					return
				}
				respondWorkflowRequestError(c, newWorkflowRequestError(http.StatusPaymentRequired, "insufficient_credits", false, "insufficient credits", err))
			} else {
				respondWorkflowRequestError(c, err)
			}
		} else {
			fail(c, http.StatusPaymentRequired, err.Error())
		}
		return
	}
	if useQwen {
		c.Header("Content-Disposition", "attachment; filename=speech.wav")
		c.Data(200, "audio/wav", audio)
		return
	}
	c.Header("Content-Disposition", "attachment; filename=speech.mp3")
	c.Data(http.StatusOK, "audio/mpeg", audio)
}

func (a *App) mediaProxy(c *gin.Context) {
	rawURL := strings.TrimSpace(c.Query("url"))
	if rawURL == "" {
		fail(c, http.StatusBadRequest, "url 参数不能为空")
		return
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" {
		fail(c, http.StatusBadRequest, "只允许代理 https 媒体资源")
		return
	}

	if _, ok := allowedMediaProxyHosts[strings.ToLower(parsedURL.Host)]; !ok {
		fail(c, http.StatusBadRequest, "该媒体域名不允许代理")
		return
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		fail(c, http.StatusBadRequest, "媒体 URL 无效")
		return
	}
	req.Header.Set("User-Agent", "NarratoAI-MediaProxy/1.0")

	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fail(c, http.StatusBadGateway, fmt.Sprintf("下载媒体资源失败: %v", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fail(c, http.StatusBadGateway, fmt.Sprintf("下载媒体资源失败: HTTP %d", resp.StatusCode))
		return
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if strings.Contains(strings.ToLower(contentType), "text/html") {
		fail(c, http.StatusBadGateway, "媒体资源返回了 HTML 内容")
		return
	}

	c.Header("Cache-Control", "public, max-age=3600")
	c.Header("Content-Type", contentType)
	if disposition := resp.Header.Get("Content-Disposition"); disposition != "" {
		c.Header("Content-Disposition", disposition)
	}
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, resp.Body)
}

func (a *App) elevenLabsScribeTranscribe(c *gin.Context) {
	user := getUser(c)
	file, err := c.FormFile("file")
	if err != nil {
		fail(c, http.StatusBadRequest, "缺少音频文件")
		return
	}
	if file.Size > maxUploadFileSizeBytes {
		fail(c, http.StatusBadRequest, "音频文件超过上传大小限制")
		return
	}
	if !a.elevenlabs.Configured() {
		fail(c, http.StatusServiceUnavailable, "ElevenLabs API Key 未配置")
		return
	}
	opened, err := file.Open()
	if err != nil {
		fail(c, http.StatusBadRequest, "读取音频文件失败")
		return
	}
	defer opened.Close()
	audio, err := io.ReadAll(opened)
	if err != nil {
		fail(c, http.StatusBadRequest, "读取音频文件失败")
		return
	}
	if len(audio) == 0 {
		fail(c, http.StatusBadRequest, "音频文件不能为空")
		return
	}
	identity, identityErr := extractWorkflowRequestIdentityFromValues(c.Request.MultipartForm.Value)
	if identityErr != nil {
		respondWorkflowRequestError(c, identityErr)
		return
	}
	languageCode := strings.TrimSpace(c.PostForm("language_code"))
	projectID, err := projectIDFromString(c.PostForm("project_id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "无效的项目 ID")
		return
	}
	inputFingerprint := workflowInputFingerprint(map[string]any{
		"provider":      "elevenlabs",
		"model":         a.cfg.ElevenLabsScribeModel,
		"media_sha256":  fmt.Sprintf("%x", sha256.Sum256(audio)),
		"media_size":    len(audio),
		"language_code": languageCode,
	})
	if replayed, transcriptionID, found, err := a.findWorkflowProviderSubmissionReplay(c.Request.Context(), user.ID, projectID, identity, inputFingerprint); err != nil {
		respondWorkflowRequestError(c, err)
		return
	} else if found {
		ok(c, gin.H{"success": true, "provider": "elevenlabs", "model": a.cfg.ElevenLabsScribeModel, "status": "processing", "transcription_id": transcriptionID, "poll_interval_seconds": 3, "updated_at": time.Now().UTC()})
		_ = replayed
		return
	}
	execution, executionOK := a.resolveJSONWorkflowExecutionForProject(c, user.ID, projectID, identity)
	if !executionOK {
		return
	}
	minimumDuration := 60.0
	precheckConsumption := creditConsumption{UserID: user.ID, ProjectID: projectID, APIEndpoint: "/api/v1/tools/elevenlabs/scribe/transcribe", Provider: "elevenlabs", ServiceKind: "asr", ModelID: a.cfg.ElevenLabsScribeModel, DurationSeconds: &minimumDuration, RequestMetadata: movieBillingMetadata(c, map[string]any{workflowInputFingerprintMetadataKey: inputFingerprint})}
	if identity != nil && !a.checkJSONWorkflowCreditBalance(c, user.ID, precheckConsumption, execution) {
		return
	}
	claimID := ""
	if identity != nil {
		claim, err := a.claimWorkflowProviderSubmission(c.Request.Context(), execution, inputFingerprint)
		if err != nil {
			respondWorkflowRequestError(c, err)
			return
		}
		if claim.Replayed {
			ok(c, gin.H{"success": true, "provider": "elevenlabs", "model": a.cfg.ElevenLabsScribeModel, "status": "processing", "transcription_id": claim.ProviderTaskID, "poll_interval_seconds": 3, "updated_at": time.Now().UTC()})
			return
		}
		execution = claim.Execution
		claimID = claim.ClaimID
	}
	metadata := gin.H{"source": "frame_workflow"}
	if identity == nil {
		metadata["filename"] = file.Filename
	}
	if projectID != nil {
		metadata["project_id"] = projectID.String()
	}
	metadataBytes, _ := json.Marshal(metadata)
	submission, err := a.elevenlabs.SubmitScribeTranscription(c.Request.Context(), audio, file.Filename, languageCode, string(metadataBytes))
	if err != nil {
		if identity != nil {
			a.respondASRWorkflowSubmissionFailure(c, execution, claimID, err)
			return
		}
		fail(c, http.StatusBadGateway, err.Error())
		return
	}
	if strings.TrimSpace(submission.TranscriptionID) == "" {
		if identity != nil {
			a.respondASRWorkflowSubmissionFailure(c, execution, claimID, errors.New("ElevenLabs did not return a transcription task id"))
			return
		}
		fail(c, http.StatusBadGateway, "ElevenLabs 未返回可轮询的转录任务 ID")
		return
	}
	if identity != nil {
		if _, err := a.persistWorkflowProviderSubmission(c.Request.Context(), execution, claimID, submission.TranscriptionID, inputFingerprint); err != nil {
			respondWorkflowRequestError(c, err)
			return
		}
	} else {
		a.elevenLabsScribeJobs.Store(submission.TranscriptionID, elevenLabsScribeJob{UserID: user.ID, ProjectID: projectID, Filename: file.Filename, Model: submission.Model})
	}
	ok(c, gin.H{
		"success":               true,
		"provider":              "elevenlabs",
		"model":                 submission.Model,
		"status":                "processing",
		"transcription_id":      submission.TranscriptionID,
		"request_id":            submission.RequestID,
		"poll_interval_seconds": 3,
		"raw_result":            submission.RawResult,
		"updated_at":            time.Now().UTC(),
	})
}

func (a *App) elevenLabsScribeTranscript(c *gin.Context) {
	user := getUser(c)
	transcriptionID := strings.TrimSpace(c.Param("transcription_id"))
	if transcriptionID == "" {
		fail(c, http.StatusBadRequest, "缺少转录任务 ID")
		return
	}
	if !a.elevenlabs.Configured() {
		fail(c, http.StatusServiceUnavailable, "ElevenLabs API Key 未配置")
		return
	}

	values := c.Request.URL.Query()
	identity, err := extractWorkflowRequestIdentityFromValues(values)
	if err != nil {
		respondWorkflowRequestError(c, err)
		return
	}
	projectID, err := projectIDFromString(values.Get("project_id"))
	if err != nil {
		fail(c, http.StatusBadRequest, "无效的项目 ID")
		return
	}
	execution := workflowRequestExecution{}
	var job elevenLabsScribeJob
	if identity != nil {
		execution, err = a.resolveWorkflowProviderQuery(c.Request.Context(), user.ID, projectID, identity, transcriptionID, "")
		if err != nil {
			respondWorkflowRequestError(c, err)
			return
		}
	} else {
		stored, found := a.elevenLabsScribeJobs.Load(transcriptionID)
		if !found {
			fail(c, http.StatusNotFound, "转录任务不存在或已过期")
			return
		}
		if typed, ok := stored.(elevenLabsScribeJob); ok {
			job = typed
		}
		if job.UserID != user.ID {
			fail(c, http.StatusForbidden, "无权访问该转录任务")
			return
		}
	}

	result, err := a.elevenlabs.GetScribeTranscript(c.Request.Context(), transcriptionID, job.Filename)
	if err != nil {
		if httpErr, isHTTPError := err.(providers.ElevenLabsHTTPError); isHTTPError && isPendingScribeStatus(httpErr.StatusCode) {
			model := job.Model
			if identity != nil {
				model = strings.TrimSpace(a.cfg.ElevenLabsScribeModel)
				if model == "" {
					model = "scribe_v2"
				}
			}
			ok(c, gin.H{
				"success":          true,
				"provider":         "elevenlabs",
				"model":            model,
				"status":           "processing",
				"transcription_id": transcriptionID,
				"updated_at":       time.Now().UTC(),
			})
			return
		}
		if identity != nil {
			a.respondJSONWorkflowProviderFailure(c, execution, err)
			return
		}
		fail(c, http.StatusBadGateway, err.Error())
		return
	}

	if identity != nil {
		duration := result.Duration
		if err := a.finalizeProviderSuccess(c.Request.Context(), execution, providerSuccessFinalization{ProviderRequestID: &transcriptionID, Consumption: creditConsumption{UserID: user.ID, ProjectID: projectID, CreditCount: result.Tokens, APIEndpoint: "/api/v1/tools/elevenlabs/scribe/transcribe", Provider: "elevenlabs", ServiceKind: "asr", ModelID: result.Model, DurationSeconds: &duration, RequestMetadata: movieBillingMetadata(c, map[string]any{workflowInputFingerprintMetadataKey: valueOrEmpty(execution.Step.ProviderInputHash)})}}); err != nil {
			a.respondASRWorkflowFinalizationError(c, execution, err)
			return
		}
	} else if err := a.consumeElevenLabsScribeCredits(user.ID, job.ProjectID, transcriptionID, result); err != nil {
		fail(c, http.StatusPaymentRequired, err.Error())
		return
	}
	a.respondElevenLabsScribeResult(c, transcriptionID, result)
}

func isPendingScribeStatus(status int) bool {
	return status == http.StatusNotFound || status == http.StatusConflict
}

func (a *App) consumeElevenLabsScribeCredits(userID uuid.UUID, projectID *uuid.UUID, transcriptionID string, result providers.ScribeResult) error {
	if result.Tokens <= 0 {
		return nil
	}
	billingKey := userID.String() + ":" + transcriptionID
	if _, loaded := a.elevenLabsScribeBilled.LoadOrStore(billingKey, true); loaded {
		return nil
	}
	duration := result.Duration
	if err := a.billCredits(creditConsumption{
		UserID:          userID,
		ProjectID:       projectID,
		CreditCount:     result.Tokens,
		APIEndpoint:     "/api/v1/tools/elevenlabs/scribe/transcribe",
		Provider:        "elevenlabs",
		ServiceKind:     "asr",
		ModelID:         result.Model,
		DurationSeconds: &duration,
		RequestMetadata: gin.H{
			"transcription_id": transcriptionID,
			"duration":         result.Duration,
			"words":            len(result.Words),
			"audio_events":     len(result.AudioEvents),
			"language_code":    result.LanguageCode,
		},
	}); err != nil {
		a.elevenLabsScribeBilled.Delete(billingKey)
		return err
	}
	return nil
}

func (a *App) respondElevenLabsScribeResult(c *gin.Context, transcriptionID string, result providers.ScribeResult) {
	ok(c, gin.H{
		"success":           true,
		"provider":          "elevenlabs",
		"model":             result.Model,
		"status":            "completed",
		"transcription_id":  transcriptionID,
		"raw_result":        result.RawResult,
		"packed_transcript": result.PackedTranscript,
		"phrases":           result.Phrases,
		"words":             result.Words,
		"audio_events":      result.AudioEvents,
		"text_content":      result.Text,
		"language_code":     result.LanguageCode,
		"duration":          result.Duration,
		"tokens_consumed":   result.Tokens,
		"updated_at":        time.Now().UTC(),
	})
}

func (a *App) timelineViewAnalyze(c *gin.Context) {
	user := getUser(c)
	var payload map[string]any
	if !bindJSON(c, &payload) {
		return
	}
	identity, identityOK := extractJSONWorkflowIdentity(c, payload)
	if !identityOK {
		return
	}
	if identity == nil && !a.dashscope.Configured() {
		fail(c, http.StatusServiceUnavailable, "DashScope API Key 未配置")
		return
	}
	var req struct {
		ImageOSSURL string   `json:"image_oss_url"`
		Start       *float64 `json:"start"`
		End         *float64 `json:"end"`
		NFrames     int      `json:"n_frames"`
		Prompt      string   `json:"prompt"`
		Model       string   `json:"model"`
		ProjectID   string   `json:"project_id"`
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := json.Unmarshal(rawPayload, &req); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	projectID, err := projectIDFromString(req.ProjectID)
	if err != nil {
		fail(c, http.StatusBadRequest, "无效的项目 ID")
		return
	}
	ossURL := strings.TrimSpace(req.ImageOSSURL)
	if ossURL == "" {
		fail(c, http.StatusBadRequest, "缺少视觉辅助图临时地址 image_oss_url")
		return
	}
	if !strings.HasPrefix(ossURL, "oss://") {
		fail(c, http.StatusBadRequest, "image_oss_url 必须是 DashScope 临时存储 oss:// 地址")
		return
	}
	var start, end float64
	startOK := req.Start != nil
	endOK := req.End != nil
	if req.Start != nil {
		start = *req.Start
	}
	if req.End != nil {
		end = *req.End
	}
	nFrames := 10
	if req.NFrames > 0 {
		nFrames = req.NFrames
	}
	if nFrames < 1 {
		nFrames = 1
	}
	if nFrames > 24 {
		nFrames = 24
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = a.cfg.QwenVisionModel
	}
	prompt := strings.TrimSpace(req.Prompt)

	execution, executionOK := a.resolveJSONWorkflowExecutionForProject(c, user.ID, projectID, identity)
	if !executionOK {
		return
	}
	if identity != nil && !a.dashscope.Configured() {
		fail(c, http.StatusServiceUnavailable, "DashScope API Key 未配置")
		return
	}
	if identity != nil && !a.checkJSONWorkflowCreditBalance(c, user.ID, creditConsumption{
		CreditCount: 50,
		APIEndpoint: "/api/v1/tools/timeline-view/analyze",
		Provider:    "dashscope",
		ServiceKind: "vision",
		ModelID:     model,
		Usage:       providers.ChatUsage{TotalTokens: 50},
	}, execution) {
		return
	}

	var flusher http.Flusher
	var emitter *streamToolEmitter
	bufferedEmitter := &bufferedToolEmitter{}
	emit := bufferedEmitter.emit
	if identity == nil {
		flusher = beginToolEventStream(c, "timeline_view_analyze", "正在流式分析视觉辅助图")
		emitter = newStreamToolEmitter(c, flusher)
		emit = emitter.emit
	}
	vision, err := a.dashscope.AnalyzeTimelineImageStream(c.Request.Context(), ossURL, prompt, model, emit)
	if err != nil {
		if identity != nil {
			a.respondJSONWorkflowProviderFailure(c, execution, err)
			return
		}
		_ = emitter.flush()
		emitToolStreamError(c, flusher, err.Error())
		return
	}
	if identity == nil {
		_ = emitter.flush()
	}
	if identity != nil || vision.Usage.TotalTokens > 0 {
		if err := a.finalizeProviderSuccess(c.Request.Context(), execution, providerSuccessFinalization{
			ProviderRequestID:        optionalStringPointer(vision.RequestID),
			RequireProviderRequestID: identity != nil,
			Consumption: creditConsumption{
				UserID:          user.ID,
				ProjectID:       projectID,
				CreditCount:     vision.Usage.TotalTokens,
				APIEndpoint:     "/api/v1/tools/timeline-view/analyze",
				Provider:        "dashscope",
				ServiceKind:     "vision",
				ModelID:         vision.Model,
				Usage:           vision.Usage,
				RequestMetadata: movieBillingMetadata(c, gin.H{"start": start, "end": end, "n_frames": nFrames, "image_oss_url": ossURL, "request_id": vision.RequestID}),
			},
		}); err != nil {
			if identity != nil {
				a.respondJSONWorkflowFinalizationError(c, execution, err)
				return
			}
			emitToolStreamError(c, flusher, err.Error())
			return
		}
	}
	if identity != nil {
		flusher, err = bufferedEmitter.replay(c, "timeline_view_analyze", "正在流式分析视觉辅助图")
		if err != nil {
			return
		}
	}
	c.SSEvent("completed", gin.H{
		"success":             true,
		"provider":            "dashscope",
		"model":               vision.Model,
		"timeline_image_url":  ossURL,
		"timeline_image_size": 0,
		"range":               gin.H{"start": start, "end": end, "duration": end - start, "has_start": startOK, "has_end": endOK},
		"n_frames":            nFrames,
		"content":             vision.Content,
		"analysis":            vision.Parsed,
		"raw_result":          vision.Raw,
		"usage":               vision.Usage,
		"request_id":          vision.RequestID,
		"tokens_consumed":     vision.Usage.TotalTokens,
		"updated_at":          time.Now().UTC(),
	})
	if flusher != nil {
		flusher.Flush()
	}
}

func (a *App) timelineViewDecide(c *gin.Context) {
	user := getUser(c)
	var req map[string]any
	if !bindJSON(c, &req) {
		return
	}
	identity, identityOK := extractJSONWorkflowIdentity(c, req)
	if !identityOK {
		return
	}
	projectID, err := projectIDFromRequest(req)
	if err != nil {
		fail(c, http.StatusBadRequest, "无效的项目 ID")
		return
	}
	removeProjectIDFromRequest(req)
	if len(req) == 0 {
		fail(c, http.StatusBadRequest, "决策上下文不能为空")
		return
	}
	if _, exists := req["max_decisions"]; !exists {
		req["max_decisions"] = 12
	}
	if _, exists := req["default_n_frames"]; !exists {
		req["default_n_frames"] = 15
	}
	if identity == nil && !a.openai.ConfiguredFor("timeline_view_decision", req) {
		fail(c, http.StatusServiceUnavailable, "OpenAI 兼容 Flash 模型配置未配置")
		return
	}
	estimatedTokens := estimateTimelineDecisionTokens(req)
	decisionModel := strings.TrimSpace(a.cfg.OpenAIFlashModel)
	estimatedConsumption := creditConsumption{
		CreditCount: estimatedTokens,
		APIEndpoint: "/api/v1/tools/timeline-view/decide",
		Provider:    a.inferOpenAICompatibleProvider(decisionModel),
		ServiceKind: "llm",
		ModelID:     decisionModel,
		Usage:       providers.ChatUsage{TotalTokens: estimatedTokens},
	}
	if identity == nil && !a.checkCreditBalance(c, user.ID, estimatedConsumption) {
		return
	}
	execution, executionOK := a.resolveJSONWorkflowExecutionForProject(c, user.ID, projectID, identity)
	if !executionOK {
		return
	}
	if identity != nil && !a.openai.ConfiguredFor("timeline_view_decision", req) {
		fail(c, http.StatusServiceUnavailable, "OpenAI 兼容 Flash 模型配置未配置")
		return
	}
	if identity != nil && !a.checkJSONWorkflowCreditBalance(c, user.ID, estimatedConsumption, execution) {
		return
	}

	var flusher http.Flusher
	var emitter *streamToolEmitter
	bufferedEmitter := &bufferedToolEmitter{}
	emit := bufferedEmitter.emit
	if identity == nil {
		flusher = beginToolEventStream(c, "timeline_view_decision", "正在流式判断视觉片段")
		emitter = newStreamToolEmitter(c, flusher)
		emit = emitter.emit
	}
	var completed map[string]any
	err = a.openai.Stream(c.Request.Context(), "timeline_view_decision", req, func(event string, data map[string]any) error {
		if event == "completed" {
			completed = data
			return nil
		}
		return emit(event, data)
	})
	if err != nil {
		if identity != nil {
			a.respondJSONWorkflowProviderFailure(c, execution, err)
			return
		}
		_ = emitter.flush()
		c.SSEvent("error", gin.H{"message": err.Error()})
		if flusher != nil {
			flusher.Flush()
		}
		return
	}
	if identity == nil {
		_ = emitter.flush()
	}

	content := ""
	model := ""
	requestID := ""
	var usage providers.ChatUsage
	if completed != nil {
		content = fmt.Sprint(completed["content"])
		model = fmt.Sprint(completed["model"])
		requestID = fmt.Sprint(completed["request_id"])
		if typedUsage, ok := completed["usage"].(providers.ChatUsage); ok {
			usage = typedUsage
		} else if rawUsage, ok := completed["usage"].(map[string]any); ok {
			usage.PromptTokens = int64Value(rawUsage["prompt_tokens"])
			usage.CompletionTokens = int64Value(rawUsage["completion_tokens"])
			usage.TotalTokens = int64Value(rawUsage["total_tokens"])
		}
	}
	if identity != nil && execution.Step.Status == workflowRunStatusSucceeded && usage.TotalTokens <= 0 {
		if err := json.Unmarshal(execution.Step.Usage, &usage); err != nil {
			respondWorkflowRequestError(c, newWorkflowRequestError(http.StatusInternalServerError, "stored_provider_usage_invalid", true, "stored provider usage is invalid", err))
			return
		}
	}
	analysis := extractTimelineDecision(content)
	decisions := []any{}
	if raw, ok := analysis["decisions"].([]any); ok {
		decisions = raw
	}
	provider := a.inferOpenAICompatibleProvider(model)
	if identity != nil && usage.TotalTokens <= 0 {
		const code = "provider_usage_missing"
		const message = "网络不稳定导致模型用量统计缺失，请点击重试继续（已完成的步骤不受影响，也不影响剪辑效果）"
		if execution.Step.Status != workflowRunStatusSucceeded {
			if err := a.finalizeProviderNonSuccess(c.Request.Context(), execution, workflowStepStatusFailed, code, message); err != nil {
				respondWorkflowRequestError(c, err)
				return
			}
		}
		respondWorkflowRequestError(c, newWorkflowRequestError(http.StatusBadGateway, code, true, message, nil))
		return
	}
	if identity != nil || usage.TotalTokens > 0 {
		if err := a.finalizeProviderSuccess(c.Request.Context(), execution, providerSuccessFinalization{
			ProviderRequestID:        optionalStringPointer(requestID),
			RequireProviderRequestID: identity != nil,
			Consumption: creditConsumption{
				UserID:          user.ID,
				ProjectID:       projectID,
				CreditCount:     usage.TotalTokens,
				APIEndpoint:     "/api/v1/tools/timeline-view/decide",
				Provider:        provider,
				ServiceKind:     "llm",
				ModelID:         model,
				Usage:           usage,
				RequestMetadata: movieBillingMetadata(c, gin.H{"request_id": requestID}),
			},
		}); err != nil {
			if identity != nil {
				a.respondJSONWorkflowFinalizationError(c, execution, err)
				return
			}
			c.SSEvent("error", gin.H{"message": err.Error()})
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
	}
	if identity != nil {
		flusher, err = bufferedEmitter.replay(c, "timeline_view_decision", "正在流式判断视觉片段")
		if err != nil {
			return
		}
	}
	c.SSEvent("completed", gin.H{
		"success":         true,
		"provider":        provider,
		"model":           model,
		"content":         content,
		"analysis":        analysis,
		"decisions":       decisions,
		"usage":           usage,
		"request_id":      requestID,
		"tokens_consumed": usage.TotalTokens,
		"updated_at":      time.Now().UTC(),
	})
	if flusher != nil {
		flusher.Flush()
	}
}

func (a *App) frameWorkflowSubtitleValidate(c *gin.Context) {
	user := getUser(c)
	var req map[string]any
	if !bindJSON(c, &req) {
		return
	}
	identity, identityOK := extractJSONWorkflowIdentity(c, req)
	if !identityOK {
		return
	}
	projectID, err := projectIDFromRequest(req)
	if err != nil {
		fail(c, http.StatusBadRequest, "无效的项目 ID")
		return
	}
	removeProjectIDFromRequest(req)
	req = subtitleValidationPromptPayload(req)
	if !hasSubtitleValidationSegments(req) {
		fail(c, http.StatusBadRequest, "字幕 segments 不能为空")
		return
	}
	if _, exists := req["language"]; !exists {
		req["language"] = "zh"
	}
	if identity == nil && !a.openai.ConfiguredFor("subtitle_validation", req) {
		fail(c, http.StatusServiceUnavailable, "OpenAI 兼容 Flash 模型配置未配置")
		return
	}
	estimatedTokens := estimateSubtitleValidationTokens(req)
	model := strings.TrimSpace(a.cfg.OpenAIFlashModel)
	estimatedConsumption := creditConsumption{
		CreditCount: estimatedTokens,
		APIEndpoint: "/api/v1/tools/frame-workflow/subtitles/validate",
		Provider:    a.inferOpenAICompatibleProvider(model),
		ServiceKind: "llm",
		ModelID:     model,
		Usage:       providers.ChatUsage{TotalTokens: estimatedTokens},
	}
	if identity == nil && !a.checkCreditBalance(c, user.ID, estimatedConsumption) {
		return
	}
	execution, executionOK := a.resolveJSONWorkflowExecutionForProject(c, user.ID, projectID, identity)
	if !executionOK {
		return
	}
	if identity != nil && !a.openai.ConfiguredFor("subtitle_validation", req) {
		fail(c, http.StatusServiceUnavailable, "OpenAI 兼容 Flash 模型配置未配置")
		return
	}
	if identity != nil && !a.checkJSONWorkflowCreditBalance(c, user.ID, estimatedConsumption, execution) {
		return
	}

	var flusher http.Flusher
	var emitter *streamToolEmitter
	bufferedEmitter := &bufferedToolEmitter{}
	emit := bufferedEmitter.emit
	if identity == nil {
		flusher = beginToolEventStream(c, "subtitle_validation", "正在流式校对字幕")
		emitter = newStreamToolEmitter(c, flusher)
		emit = emitter.emit
	}
	var completed map[string]any
	err = a.openai.Stream(c.Request.Context(), "subtitle_validation", req, func(event string, data map[string]any) error {
		if event == "completed" {
			completed = data
			return nil
		}
		return emit(event, data)
	})
	if err != nil {
		if identity != nil {
			a.respondJSONWorkflowProviderFailure(c, execution, err)
			return
		}
		_ = emitter.flush()
		c.SSEvent("error", gin.H{"message": err.Error()})
		if flusher != nil {
			flusher.Flush()
		}
		return
	}
	if identity == nil {
		_ = emitter.flush()
	}

	content := ""
	model = ""
	requestID := ""
	var usage providers.ChatUsage
	if completed != nil {
		content = fmt.Sprint(completed["content"])
		model = fmt.Sprint(completed["model"])
		requestID = fmt.Sprint(completed["request_id"])
		if typedUsage, ok := completed["usage"].(providers.ChatUsage); ok {
			usage = typedUsage
		} else if rawUsage, ok := completed["usage"].(map[string]any); ok {
			usage.PromptTokens = int64Value(rawUsage["prompt_tokens"])
			usage.CompletionTokens = int64Value(rawUsage["completion_tokens"])
			usage.TotalTokens = int64Value(rawUsage["total_tokens"])
		}
	}
	if identity != nil && execution.Step.Status == workflowRunStatusSucceeded && usage.TotalTokens <= 0 {
		if err := json.Unmarshal(execution.Step.Usage, &usage); err != nil {
			respondWorkflowRequestError(c, newWorkflowRequestError(http.StatusInternalServerError, "stored_provider_usage_invalid", true, "stored provider usage is invalid", err))
			return
		}
	}
	provider := a.inferOpenAICompatibleProvider(model)
	if identity != nil && usage.TotalTokens <= 0 {
		const code = "provider_usage_missing"
		const message = "网络不稳定导致模型用量统计缺失，请点击重试继续（已完成的步骤不受影响，也不影响剪辑效果）"
		if execution.Step.Status != workflowRunStatusSucceeded {
			if err := a.finalizeProviderNonSuccess(c.Request.Context(), execution, workflowStepStatusFailed, code, message); err != nil {
				respondWorkflowRequestError(c, err)
				return
			}
		}
		respondWorkflowRequestError(c, newWorkflowRequestError(http.StatusBadGateway, code, true, message, nil))
		return
	}
	analysis, segments, parseErr := parseSubtitleValidationResponse(content, anySlice(req["segments"]))
	if parseErr != nil {
		const code = "provider_invalid_output"
		message := "subtitle validation response invalid: " + parseErr.Error()
		if identity != nil {
			if execution.Step.Status != workflowRunStatusSucceeded {
				if err := a.finalizeProviderNonSuccess(c.Request.Context(), execution, workflowStepStatusFailed, code, message); err != nil {
					respondWorkflowRequestError(c, err)
					return
				}
			}
			respondWorkflowRequestError(c, newWorkflowRequestError(http.StatusBadGateway, code, true, message, parseErr))
			return
		}
		c.SSEvent("error", gin.H{"message": message})
		if flusher != nil {
			flusher.Flush()
		}
		return
	}
	subtitles := analysis["subtitles"]
	if subtitles == nil {
		subtitles = subtitleRecordFromSegments(segments)
	}
	if identity != nil || usage.TotalTokens > 0 {
		if err := a.finalizeProviderSuccess(c.Request.Context(), execution, providerSuccessFinalization{
			ProviderRequestID:        optionalStringPointer(requestID),
			RequireProviderRequestID: identity != nil,
			Consumption: creditConsumption{
				UserID:          user.ID,
				ProjectID:       projectID,
				CreditCount:     usage.TotalTokens,
				APIEndpoint:     "/api/v1/tools/frame-workflow/subtitles/validate",
				Provider:        provider,
				ServiceKind:     "llm",
				ModelID:         model,
				Usage:           usage,
				RequestMetadata: movieBillingMetadata(c, gin.H{"request_id": requestID}),
			},
		}); err != nil {
			if identity != nil {
				a.respondJSONWorkflowFinalizationError(c, execution, err)
				return
			}
			c.SSEvent("error", gin.H{"message": err.Error()})
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
	}
	if identity != nil {
		flusher, err = bufferedEmitter.replay(c, "subtitle_validation", "正在流式校对字幕")
		if err != nil {
			return
		}
	}
	c.SSEvent("completed", gin.H{
		"success":           true,
		"provider":          provider,
		"model":             model,
		"content":           content,
		"analysis":          analysis,
		"subtitles":         subtitles,
		"segments":          segments,
		"corrections_count": int64Value(analysis["corrections_count"]),
		"usage":             usage,
		"request_id":        requestID,
		"tokens_consumed":   usage.TotalTokens,
		"updated_at":        time.Now().UTC(),
	})
	if flusher != nil {
		flusher.Flush()
	}
}

func subtitleValidationPromptPayload(req map[string]any) map[string]any {
	payload := map[string]any{}
	if segments, exists := req["segments"]; exists {
		payload["segments"] = segments
	}
	if contextSegments, exists := req["context_segments"]; exists {
		payload["context_segments"] = contextSegments
	}
	if language, ok := req["language"].(string); ok && strings.TrimSpace(language) != "" {
		payload["language"] = strings.TrimSpace(language)
	}
	return payload
}

func hasSubtitleValidationSegments(req map[string]any) bool {
	segments, ok := req["segments"].([]any)
	return ok && len(segments) > 0
}

func (a *App) frameWorkflowNarrativeInputFingerprint(req map[string]any) string {
	return workflowInputFingerprint(map[string]any{
		"endpoint":         "/api/v1/tools/frame-workflow/narrative-analyze",
		"profile":          "pro",
		"api_base_url":     strings.TrimRight(strings.TrimSpace(a.cfg.OpenAIProAPIBaseURL), "/"),
		"model":            strings.TrimSpace(a.cfg.OpenAIProModel),
		"thinking_type":    strings.TrimSpace(a.cfg.OpenAIProThinkingType),
		"reasoning_effort": strings.TrimSpace(a.cfg.OpenAIProReasoningEffort),
		"temperature":      a.cfg.OpenAITemperature,
		"request":          req,
	})
}

func (a *App) frameWorkflowNarrativeAnalyze(c *gin.Context) {
	user := getUser(c)
	var req map[string]any
	if !bindJSON(c, &req) {
		return
	}
	identity, identityOK := extractJSONWorkflowIdentity(c, req)
	if !identityOK {
		return
	}
	if identity != nil {
		workflowRequestID(c)
	}
	projectID, err := projectIDFromRequest(req)
	if err != nil {
		fail(c, http.StatusBadRequest, "无效的项目 ID")
		return
	}
	removeProjectIDFromRequest(req)
	if len(req) == 0 {
		fail(c, http.StatusBadRequest, "剧情理解上下文不能为空")
		return
	}
	if _, exists := req["language"]; !exists {
		req["language"] = "zh"
	}
	if identity == nil && !a.openai.ConfiguredFor("frame_narrative_understanding", req) {
		fail(c, http.StatusServiceUnavailable, "OpenAI 兼容 Pro 模型配置未配置")
		return
	}
	estimatedTokens := estimateNarrativeContextTokens(req)
	narrativeModel := strings.TrimSpace(a.openai.ModelFor("frame_narrative_understanding", req))
	if narrativeModel == "" {
		narrativeModel = strings.TrimSpace(a.cfg.OpenAIProModel)
	}
	inputFingerprint := a.frameWorkflowNarrativeInputFingerprint(req)
	estimatedConsumption := creditConsumption{
		UserID:      user.ID,
		ProjectID:   projectID,
		CreditCount: estimatedTokens,
		APIEndpoint: "/api/v1/tools/frame-workflow/narrative-analyze",
		Provider:    a.inferOpenAICompatibleProvider(narrativeModel),
		ServiceKind: "llm",
		ModelID:     narrativeModel,
		Usage:       providers.ChatUsage{TotalTokens: estimatedTokens},
		RequestMetadata: movieBillingMetadata(c, map[string]any{
			workflowInputFingerprintMetadataKey: inputFingerprint,
		}),
	}
	if identity == nil && !a.checkCreditBalance(c, user.ID, estimatedConsumption) {
		return
	}
	execution, executionOK := a.resolveMovieHarnessExecution(c, user.ID, projectID, identity, inputFingerprint, "frame_narrative_understanding", a.openai.ConfiguredFor("frame_narrative_understanding", req))
	if !executionOK {
		return
	}
	if identity != nil && !a.checkJSONWorkflowCreditBalance(c, user.ID, estimatedConsumption, execution) {
		return
	}

	emitter := beginSerializedToolEventStream(c, "frame_narrative_understanding", "正在流式理解剧情")
	stopHeartbeat := startToolStreamHeartbeat(c.Request.Context(), emitter, streamToolHeartbeatInterval)
	defer stopHeartbeat()
	stopWorkflowHeartbeat := a.startWorkflowExecutionHeartbeat(c.Request.Context(), execution, streamToolHeartbeatInterval)
	defer stopWorkflowHeartbeat()

	bufferedEmitter := &bufferedToolEmitter{}
	emit := emitter.emit
	if identity != nil {
		emit = bufferedEmitter.emit
	}
	var completed map[string]any
	err = a.openai.Stream(c.Request.Context(), "frame_narrative_understanding", req, func(event string, data map[string]any) error {
		if event == "completed" {
			completed = data
			return nil
		}
		return emit(event, data)
	})
	if err != nil {
		if identity != nil {
			if execution.Step.Status != workflowRunStatusSucceeded {
				if finalizeErr := a.finalizeProviderFailure(c.Request.Context(), execution, err); finalizeErr != nil {
					emitWorkflowToolStreamError(c, emitter, finalizeErr)
					return
				}
			}
			emitWorkflowToolStreamError(c, emitter, newWorkflowProviderStreamError(err))
			return
		}
		_ = emitter.flush()
		emitter.write("error", gin.H{"message": err.Error()})
		return
	}
	if identity == nil {
		if err := emitter.flush(); err != nil {
			return
		}
	}

	content := ""
	model := ""
	requestID := ""
	var usage providers.ChatUsage
	if completed != nil {
		content = fmt.Sprint(completed["content"])
		model = fmt.Sprint(completed["model"])
		requestID = fmt.Sprint(completed["request_id"])
		if typedUsage, ok := completed["usage"].(providers.ChatUsage); ok {
			usage = typedUsage
		} else if rawUsage, ok := completed["usage"].(map[string]any); ok {
			usage.PromptTokens = int64Value(rawUsage["prompt_tokens"])
			usage.CompletionTokens = int64Value(rawUsage["completion_tokens"])
			usage.TotalTokens = int64Value(rawUsage["total_tokens"])
		}
	}
	if identity != nil && execution.Step.Status == workflowRunStatusSucceeded && usage.TotalTokens <= 0 {
		if err := json.Unmarshal(execution.Step.Usage, &usage); err != nil {
			emitWorkflowToolStreamError(c, emitter, newWorkflowRequestError(http.StatusInternalServerError, "stored_provider_usage_invalid", true, "stored provider usage is invalid", err))
			return
		}
	}
	movieStoryEvents := isMovieStoryEventsRequest(req)
	var analysis map[string]any
	if movieStoryEvents {
		analysis = extractMovieStoryEvents(content)
	} else {
		analysis = extractNarrativeUnderstanding(content)
	}
	rawEventCount := len(anySlice(analysis["events"]))
	var usableEventCount int
	if movieStoryEvents {
		// Keep invalid array entries for the PC's targeted repair and preserve
		// the same canonical representation on both sides of the transport.
		analysis, usableEventCount = normalizeMovieStoryEvents(analysis)
	} else if isMovieImportanceSelectionRequest(req) {
		// Movie key-plot selection has a deliberately smaller contract than
		// narrative understanding: events carry score/priority/reasons and do
		// not need a prose description. Keep the regular narrative validator
		// unchanged for all other callers.
		analysis, usableEventCount = normalizeMovieImportanceSelection(analysis)
	} else if isMovieBackgroundResearchSummaryRequest(req) {
		// Public-search curation is a source-grounded reference summary, not a
		// video event list. Do not apply the generic narrative event contract.
		analysis, usableEventCount = normalizeMovieBackgroundResearchSummary(analysis)
	} else {
		analysis, usableEventCount = normalizeNarrativeUnderstandingEvents(analysis)
	}
	if movieStoryEvents {
		_, hasSummary := analysis["summary"].(string)
		log.Printf("[movie-story-events] request_id=%s contract=%s retained_events=%d invalid_events=%d summary_present=%t finish_reason=%v",
			requestID, movieStoryEventsContract, len(anySlice(analysis["events"])), len(anySlice(analysis["invalid_events"])), hasSummary, completed["finish_reason"])
	}
	if usableEventCount == 0 || ((req["movie_outline"] == true || movieStoryEvents) && completed["finish_reason"] == "length") {
		const code = "provider_invalid_output"
		log.Printf("[narrative-analyze] no usable events: raw_event_count=%d content_length=%d model=%s request_id=%s finish_reason=%v stream_chunks=%v stream_done=%v thinking_characters=%v completion_tokens=%d content_head=%.400s",
			rawEventCount, len(content), model, requestID, completed["finish_reason"], completed["stream_chunks"], completed["stream_done"], completed["thinking_characters"], usage.CompletionTokens, content)
		if identity != nil && execution.Step.Status != workflowRunStatusSucceeded {
			if err := a.finalizeProviderNonSuccess(c.Request.Context(), execution, workflowStepStatusFailed, code, "invalid provider output"); err != nil {
				emitWorkflowToolStreamError(c, emitter, err)
				return
			}
		}
		message := narrativeInvalidOutputMessage(req, content, completed)
		if movieStoryEvents {
			// Invalid output keeps the existing failure/finalization path. Return
			// its evidence as error details, never as a successful billed result.
			_ = emitter.emit("error", gin.H{
				"request_id": workflowRequestID(c), "code": code, "retryable": true, "message": message,
				"result": gin.H{"content": content, "analysis": analysis, "normalization_contract": movieStoryEventsContract,
					"model": model, "request_id": requestID, "usage": usage, "finish_reason": completed["finish_reason"]},
			})
			return
		}
		emitWorkflowToolStreamError(c, emitter, newWorkflowRequestError(http.StatusBadGateway, code, true, message, nil))
		return
	}
	provider := a.inferOpenAICompatibleProvider(model)
	if identity != nil && usage.TotalTokens <= 0 {
		const code = "provider_usage_missing"
		const message = "网络不稳定导致模型用量统计缺失，请点击重试继续（已完成的步骤不受影响，也不影响剪辑效果）"
		if execution.Step.Status != workflowRunStatusSucceeded {
			if err := a.finalizeProviderNonSuccess(c.Request.Context(), execution, workflowStepStatusFailed, code, message); err != nil {
				emitWorkflowToolStreamError(c, emitter, err)
				return
			}
		}
		emitWorkflowToolStreamError(c, emitter, newWorkflowRequestError(http.StatusBadGateway, code, true, message, nil))
		return
	}
	completedPayload := gin.H{
		"success": true, "provider": provider, "model": model, "content": content, "analysis": analysis,
		"characters": anySlice(analysis["characters"]), "relationships": anySlice(analysis["relationships"]),
		"emotion_curve": anySlice(analysis["emotion_curve"]), "events": anySlice(analysis["events"]),
		"usage": usage, "request_id": requestID, "finish_reason": completed["finish_reason"],
		"tokens_consumed": usage.TotalTokens, "updated_at": time.Now().UTC(),
	}
	if movieStoryEvents {
		completedPayload["normalization_contract"] = movieStoryEventsContract
	}
	if identity != nil || usage.TotalTokens > 0 {
		requestMetadata := gin.H{"request_id": requestID}
		if identity != nil {
			requestMetadata[workflowInputFingerprintMetadataKey] = inputFingerprint
		}
		if err := a.finalizeProviderSuccess(c.Request.Context(), execution, providerSuccessFinalization{
			ProviderRequestID:        optionalStringPointer(requestID),
			RequireProviderRequestID: identity != nil,
			CompletedResult:          completedPayload,
			Consumption: creditConsumption{
				UserID:          user.ID,
				ProjectID:       projectID,
				CreditCount:     usage.TotalTokens,
				APIEndpoint:     "/api/v1/tools/frame-workflow/narrative-analyze",
				Provider:        provider,
				ServiceKind:     "llm",
				ModelID:         model,
				Usage:           usage,
				RequestMetadata: movieBillingMetadata(c, requestMetadata),
			},
		}); err != nil {
			if identity != nil {
				if errors.Is(err, ErrInsufficientCredits) && execution.Step.Status != workflowRunStatusSucceeded {
					if blockErr := a.finalizeProviderBlocked(c.Request.Context(), execution, "insufficient_credits", "insufficient credits"); blockErr != nil {
						emitWorkflowToolStreamError(c, emitter, blockErr)
						return
					}
					emitWorkflowToolStreamError(c, emitter, newWorkflowRequestError(http.StatusPaymentRequired, "insufficient_credits", false, "insufficient credits", err))
					return
				}
				emitWorkflowToolStreamError(c, emitter, err)
				return
			}
			_ = emitter.flush()
			emitter.write("error", gin.H{"message": err.Error()})
			return
		}
	}
	if identity != nil {
		for _, event := range bufferedEmitter.events {
			if err := emitter.emit(event.event, event.data); err != nil {
				return
			}
		}
		_ = emitter.flush()
		emitter.write("completed", completedPayload)
		return
	}
	_ = emitter.flush()
	emitter.write("completed", completedPayload)
}

func beginToolEventStream(c *gin.Context, stage string, message string) http.Flusher {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	_, _ = c.Writer.WriteString(": connected\n\n")
	flusher, _ := c.Writer.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	c.SSEvent("stage", gin.H{"stage": stage, "message": message})
	if flusher != nil {
		flusher.Flush()
	}
	return flusher
}

func emitToolStreamError(c *gin.Context, flusher http.Flusher, message string) {
	c.SSEvent("error", gin.H{"message": message})
	if flusher != nil {
		flusher.Flush()
	}
}

func beginSerializedToolEventStream(c *gin.Context, stage string, message string) *streamToolEmitter {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	flusher, _ := c.Writer.(http.Flusher)
	emitter := newStreamToolEmitter(c, flusher)
	emitter.writeComment("connected")
	_ = emitter.emit("stage", gin.H{"stage": stage, "message": message})
	return emitter
}

func emitWorkflowToolStreamError(c *gin.Context, emitter *streamToolEmitter, err error) {
	requestErr, ok := asWorkflowRequestError(err)
	if !ok {
		requestErr = newWorkflowRequestError(
			http.StatusBadGateway,
			"workflow_finalization_failed",
			true,
			"workflow request failed",
			err,
		)
	}
	if !ok && err != nil {
		log.Printf("[workflow-tool-stream] unwrapped stream error: code=%s underlying_type=%T underlying_error=%v", requestErr.Code, err, err)
	}
	_ = emitter.emit("error", gin.H{
		"request_id": workflowRequestID(c),
		"code":       requestErr.Code,
		"retryable":  requestErr.Retryable,
		"message":    requestErr.Message,
	})
}

func (a *App) deleteVoice(c *gin.Context) {
	voiceURI := strings.TrimPrefix(c.Param("voice_uri"), "/")
	provider := c.Query("provider")
	if provider == "fishaudio" || (a.fish.Configured() && !strings.HasPrefix(voiceURI, "speech:")) {
		if err := a.fish.DeleteModel(c.Request.Context(), voiceURI); err != nil {
			ok(c, gin.H{"voice_uri": voiceURI, "deleted": true, "success": true, "provider": "fishaudio", "message": "音色删除完成（可能已不存在）: " + err.Error()})
			return
		}
		ok(c, gin.H{"voice_uri": voiceURI, "deleted": true, "success": true, "provider": "fishaudio"})
		return
	}
	ok(c, gin.H{"voice_uri": voiceURI, "deleted": true, "success": true, "provider": "siliconflow"})
}

func (a *App) consumeTokens(userID uuid.UUID, tokenCount int64, endpoint string, model string, projectID *uuid.UUID, metadata map[string]any) error {
	return a.consumeCredits(creditConsumption{
		UserID:          userID,
		ProjectID:       projectID,
		CreditCount:     tokenCount,
		APIEndpoint:     endpoint,
		Provider:        inferProviderFromEndpoint(endpoint),
		ServiceKind:     "custom",
		ModelID:         model,
		RequestMetadata: metadata,
	})
}

func (a *App) streamTool(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		endpoint := streamToolEndpoint(kind)
		user := getUser(c)
		if kind == "movie_vision" {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 13<<20)
		}
		var req map[string]any
		if !bindJSON(c, &req) {
			return
		}
		if kind == "movie_vision" {
			if !a.cfg.MovieVisionConfigured() && !a.cfg.OpenAIProSupportsVision {
				fail(c, http.StatusBadRequest, "当前推理主模型不支持图片输入")
				return
			}
			if _, video := req["video_data_url"]; video && !a.cfg.MovieVisionVideoInput() && !(req["general_video_policy"] == "video-events-v1" && a.cfg.MovieVisionConfigured()) {
				fail(c, http.StatusBadRequest, "当前画面分析模型不接受视频片段，请改用拼图")
				return
			}
			if _, err := providers.MovieVisionMessages(req); err != nil {
				fail(c, http.StatusBadRequest, err.Error())
				return
			}
		}
		identity, identityOK := extractJSONWorkflowIdentity(c, req)
		if !identityOK {
			return
		}
		if identity != nil {
			workflowRequestID(c)
		}
		projectID, err := projectIDFromRequest(req)
		if err != nil {
			fail(c, http.StatusBadRequest, "无效的项目 ID")
			return
		}
		removeProjectIDFromRequest(req)
		inputFingerprint := workflowInputFingerprint(map[string]any{"kind": kind, "request": req})
		if skills := prompts.MovieSceneWritingFingerprint(kind, req); skills != "" {
			inputFingerprint = workflowInputFingerprint(map[string]any{"kind": kind, "request": req, "writing_skills": skills})
		}
		identity, inputFingerprint, err = a.generalWorkflowIdentity(c.Request.Context(), user.ID, projectID, kind, req, identity, inputFingerprint)
		if err != nil {
			respondWorkflowRequestError(c, err)
			return
		}
		execution, executionOK := a.resolveMovieHarnessExecution(c, user.ID, projectID, identity, inputFingerprint, kind, a.openai.ConfiguredFor(kind, req))
		if !executionOK {
			return
		}
		estimatedTokens := estimateStreamTokens(req)
		if kind == "movie_vision" {
			// Inline JPEG or MP4 bytes are media input, not millions of text tokens.
			estimatedTokens = estimateStreamTokens(map[string]any{"prompt": req["prompt"]}) + 4096
		}
		model := a.openai.ModelFor(kind, req)
		estimatedConsumption := creditConsumption{
			CreditCount: estimatedTokens,
			APIEndpoint: endpoint,
			Provider:    a.inferOpenAICompatibleProvider(model),
			ServiceKind: "llm",
			ModelID:     model,
			Usage:       providers.ChatUsage{TotalTokens: estimatedTokens},
		}
		if !a.checkJSONWorkflowCreditBalance(c, user.ID, estimatedConsumption, execution) {
			return
		}
		emitter := beginSerializedToolEventStream(c, kind, streamToolStageMessage(kind))
		stopHeartbeat := startToolStreamHeartbeat(c.Request.Context(), emitter, streamToolHeartbeatInterval)
		defer stopHeartbeat()
		stopWorkflowHeartbeat := a.startWorkflowExecutionHeartbeat(c.Request.Context(), execution, streamToolHeartbeatInterval)
		defer stopWorkflowHeartbeat()

		bufferedEmitter := &bufferedToolEmitter{}
		emit := emitter.emit
		if identity != nil {
			emit = bufferedEmitter.emit
		}
		var completed map[string]any
		err = a.openai.Stream(c.Request.Context(), kind, req, func(event string, data map[string]any) error {
			if event == "completed" {
				completed = data
				if identity == nil {
					return emitter.emit(event, data)
				}
				return nil
			}
			return emit(event, data)
		})
		if err != nil {
			log.Printf(
				"stream tool provider error: kind=%s operation=%s error_type=%T error=%v",
				kind,
				strings.TrimSpace(fmt.Sprint(req["model_operation"])),
				err,
				err,
			)
			if identity != nil {
				if execution.Step.Status != workflowRunStatusSucceeded {
					if finalizeErr := a.finalizeProviderFailure(c.Request.Context(), execution, err); finalizeErr != nil {
						emitWorkflowToolStreamError(c, emitter, finalizeErr)
						return
					}
				}
				emitWorkflowToolStreamError(c, emitter, newWorkflowProviderStreamError(err))
				return
			}
			_ = emitter.flush()
			detail := gin.H{"message": err.Error()}
			if kind == "movie_vision" {
				detail["model"] = a.movieVisionModelName()
			}
			emitter.write("error", detail)
			return
		}
		usage, model, requestID := streamToolCompletionMetadata(completed)
		if identity != nil {
			if usage.TotalTokens <= 0 {
				if execution.Step.Status != workflowRunStatusSucceeded {
					if finalizeErr := a.finalizeProviderNonSuccess(c.Request.Context(), execution, workflowStepStatusFailed, "provider_usage_missing", "provider returned content but omitted token usage"); finalizeErr != nil {
						emitWorkflowToolStreamError(c, emitter, finalizeErr)
						return
					}
				}
				emitWorkflowToolStreamError(c, emitter, newWorkflowRequestError(http.StatusBadGateway, "provider_usage_missing", true, "provider returned content but omitted token usage", nil))
				return
			}
			providerRequestID, providerRequestIDErr := sanitizeWorkflowProviderRequestID(optionalStringPointer(requestID))
			if providerRequestIDErr != nil || providerRequestID == nil {
				const code = "provider_protocol_error"
				const message = "invalid provider response"
				if execution.Step.Status != workflowRunStatusSucceeded {
					if finalizeErr := a.finalizeProviderNonSuccess(c.Request.Context(), execution, workflowStepStatusFailed, code, message); finalizeErr != nil {
						emitWorkflowToolStreamError(c, emitter, finalizeErr)
						return
					}
				}
				emitWorkflowToolStreamError(c, emitter, newWorkflowRequestError(http.StatusBadGateway, code, true, message, providerRequestIDErr))
				return
			}
			provider := a.inferOpenAICompatibleProvider(model)
			if finalizeErr := a.finalizeProviderSuccess(c.Request.Context(), execution, providerSuccessFinalization{
				ProviderRequestID: providerRequestID,
				CompletedResult:   completed,
				Consumption: creditConsumption{
					UserID:          user.ID,
					ProjectID:       projectID,
					CreditCount:     usage.TotalTokens,
					APIEndpoint:     endpoint,
					Provider:        provider,
					ServiceKind:     "llm",
					ModelID:         model,
					Usage:           usage,
					RequestMetadata: movieBillingMetadata(c, gin.H{"request_id": requestID, workflowInputFingerprintMetadataKey: inputFingerprint}),
				},
			}); finalizeErr != nil {
				if errors.Is(finalizeErr, ErrInsufficientCredits) && execution.Step.Status != workflowRunStatusSucceeded {
					if blockedErr := a.finalizeProviderBlocked(c.Request.Context(), execution, "insufficient_credits", "insufficient credits"); blockedErr == nil {
						emitWorkflowToolStreamError(c, emitter, newWorkflowRequestError(http.StatusPaymentRequired, "insufficient_credits", false, "insufficient credits", finalizeErr))
						return
					}
				}
				emitWorkflowToolStreamError(c, emitter, finalizeErr)
				return
			}
			for _, event := range bufferedEmitter.events {
				if replayErr := emitter.emit(event.event, event.data); replayErr != nil {
					return
				}
			}
			_ = emitter.flush()
			emitter.write("completed", completed)
			return
		}

		_ = emitter.flush()
		if usage.TotalTokens > 0 {
			provider := a.inferOpenAICompatibleProvider(model)
			if billErr := a.billCredits(creditConsumption{
				UserID:          user.ID,
				ProjectID:       projectID,
				CreditCount:     usage.TotalTokens,
				APIEndpoint:     endpoint,
				Provider:        provider,
				ServiceKind:     "llm",
				ModelID:         model,
				Usage:           usage,
				RequestMetadata: movieBillingMetadata(c, gin.H{"request_id": requestID}),
			}); billErr != nil {
				emitter.write("error", gin.H{"message": billErr.Error()})
			}
		}
	}
}

func streamToolCompletionMetadata(completed map[string]any) (providers.ChatUsage, string, string) {
	if completed == nil {
		return providers.ChatUsage{}, "", ""
	}
	usage, _ := completed["usage"].(providers.ChatUsage)
	if rawUsage, ok := completed["usage"].(map[string]any); ok {
		usage.PromptTokens = int64Value(rawUsage["prompt_tokens"])
		usage.CompletionTokens = int64Value(rawUsage["completion_tokens"])
		usage.TotalTokens = int64Value(rawUsage["total_tokens"])
	}
	return usage, fmt.Sprint(completed["model"]), fmt.Sprint(completed["request_id"])
}

func newWorkflowProviderStreamError(providerErr error) *workflowRequestError {
	_, code, message := safeProviderFailureDiagnostics(providerErr)
	status := http.StatusBadGateway
	if code == "request_timed_out" {
		status = http.StatusGatewayTimeout
	} else if code == "operation_interrupted" {
		status = http.StatusRequestTimeout
	}
	return newWorkflowRequestError(status, code, code != "provider_account_unavailable" && code != "provider_content_blocked", message, providerErr)
}

func streamToolEndpoint(kind string) string {
	switch kind {
	case "movie_vision":
		return "/api/v1/tools/movie/vision/analyze"
	case "script_draft":
		return "/api/v1/tools/generate-narration-draft-stream"
	case "script_match":
		return "/api/v1/tools/match-narration-visuals-stream"
	default:
		return "/api/v1/tools/generate-script-stream"
	}
}

func streamToolStageMessage(kind string) string {
	switch kind {
	case "movie_vision":
		return "正在用推理主模型补充画面证据"
	case "script_draft":
		return "正在生成解说文案草稿"
	case "script_match":
		return "正在匹配解说文案画面"
	default:
		return "正在生成完整脚本"
	}
}

func (a *App) checkTokenBalance(c *gin.Context, userID uuid.UUID, required int64) bool {
	if c.Request == nil || !strings.Contains(c.Request.URL.Path, "/dubbing/") {
		if free, _ := a.billingModeConfig("general")["free_experience"].(bool); free {
			return true
		}
	}
	if required <= 0 {
		return true
	}
	_, snapshot, err := a.creditBalanceSnapshot(userID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "读取 Credits 账户失败")
		return false
	}
	if snapshot.RemainingCredits < required {
		c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{
			"error":             "Payment Required",
			"message":           "Credits余额不足，请充值后继续使用",
			"success":           false,
			"required_tokens":   required,
			"remaining_tokens":  snapshot.RemainingCredits,
			"required_credits":  required,
			"remaining_credits": snapshot.RemainingCredits,
		})
		return false
	}
	return true
}

func (a *App) checkCreditBalance(c *gin.Context, userID uuid.UUID, consumption creditConsumption) bool {
	requiredCredits, err := a.estimateCreditCountStrict(consumption)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return false
	}
	if requiredCredits <= 0 {
		return true
	}
	_, snapshot, err := a.creditBalanceSnapshot(userID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "读取 Credits 账户失败")
		return false
	}
	if snapshot.RemainingCredits < requiredCredits {
		requiredTokens := consumption.Usage.TotalTokens
		if requiredTokens <= 0 {
			requiredTokens = consumption.CreditCount
		}
		c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{
			"error":             "Payment Required",
			"message":           "Credits余额不足，请充值后继续使用",
			"success":           false,
			"required_tokens":   requiredTokens,
			"remaining_tokens":  snapshot.RemainingCredits,
			"required_credits":  requiredCredits,
			"remaining_credits": snapshot.RemainingCredits,
		})
		return false
	}
	return true
}

func estimateStreamTokens(req map[string]any) int64 {
	raw, _ := json.Marshal(req)
	if len(raw) == 0 || strings.TrimSpace(fmt.Sprint(req["video_context"])) == "" && len(raw) <= 2 {
		return 1
	}
	estimate := int64(len(raw)/4 + 4000)
	if estimate < 1200 {
		return 1200
	}
	return estimate
}

func estimateTimelineDecisionTokens(req map[string]any) int64 {
	raw, _ := json.Marshal(req)
	estimate := int64(len(raw)/4 + 800)
	if estimate < 800 {
		return 800
	}
	return estimate
}

func estimateSubtitleValidationTokens(req map[string]any) int64 {
	raw, _ := json.Marshal(req)
	estimate := int64(len(raw)/4 + 1000)
	if estimate < 1000 {
		return 1000
	}
	return estimate
}

func estimateNarrativeContextTokens(req map[string]any) int64 {
	raw, _ := json.Marshal(req)
	estimate := int64(len(raw)/4 + 1200)
	if estimate < 1200 {
		return 1200
	}
	return estimate
}

func int64Value(v any) int64 {
	switch value := v.(type) {
	case float64:
		return int64(value)
	case int:
		return int64(value)
	case int64:
		return value
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	default:
		return 0
	}
}

func (a *App) submitVendorASR(vendor string) gin.HandlerFunc {
	return func(c *gin.Context) {
		user := getUser(c)
		var payload map[string]any
		if !bindJSON(c, &payload) {
			return
		}
		identity, identityOK := extractJSONWorkflowIdentity(c, payload)
		if !identityOK {
			return
		}
		if vendor == "paraformer" && identity == nil {
			fail(c, http.StatusBadRequest, "搞笑配音必须提供工作流标识")
			return
		}
		projectID, err := projectIDFromRequest(payload)
		if err != nil {
			fail(c, http.StatusBadRequest, "无效的项目 ID")
			return
		}
		removeProjectIDFromRequest(payload)
		var req struct {
			FileURL            string `json:"file_url" binding:"required"`
			Language           string `json:"language"`
			ChannelID          []int  `json:"channel_id"`
			EnableITN          *bool  `json:"enable_itn"`
			DiarizationEnabled *bool  `json:"diarization_enabled"`
			SpeakerCount       *int   `json:"speaker_count"`
		}
		raw, err := json.Marshal(payload)
		if err != nil || json.Unmarshal(raw, &req) != nil || strings.TrimSpace(req.FileURL) == "" {
			fail(c, http.StatusBadRequest, "file_url is required")
			return
		}
		if !a.dashscope.Configured() {
			fail(c, http.StatusServiceUnavailable, "DashScope API Key 未配置")
			return
		}
		model := a.cfg.Qwen3ASRModel
		funASR := vendor == "funasr" || vendor == "paraformer"
		if funASR {
			model = a.cfg.FunASRModel
		}
		if vendor == "paraformer" {
			model = a.cfg.DubbingASRModel
		}
		parameters := map[string]any{}
		if len(req.ChannelID) > 0 {
			parameters["channel_id"] = req.ChannelID
		}
		if req.Language != "" {
			parameters["language"] = req.Language
		}
		if req.EnableITN != nil {
			parameters["enable_itn"] = *req.EnableITN
		}
		if req.DiarizationEnabled != nil {
			parameters["diarization_enabled"] = *req.DiarizationEnabled
		}
		if req.SpeakerCount != nil {
			parameters["speaker_count"] = *req.SpeakerCount
		}
		inputFingerprint := workflowInputFingerprint(map[string]any{"provider": "dashscope", "vendor": vendor, "model": model, "media_identity": strings.TrimSpace(req.FileURL), "parameters": parameters})
		if replayed, taskID, found, err := a.findWorkflowProviderSubmissionReplay(c.Request.Context(), user.ID, projectID, identity, inputFingerprint); err != nil {
			respondWorkflowRequestError(c, err)
			return
		} else if found {
			_ = replayed
			ok(c, gin.H{"success": true, "task_id": taskID, "status": "PENDING", "message": "任务已提交，正在处理中", "created_at": time.Now()})
			return
		}
		execution, executionOK := a.resolveJSONWorkflowExecutionForProject(c, user.ID, projectID, identity)
		if !executionOK {
			return
		}
		minimumDuration := 60.0
		precheckConsumption := creditConsumption{UserID: user.ID, ProjectID: projectID, APIEndpoint: vendorASRQueryEndpoint(vendor), Provider: "dashscope", ServiceKind: vendorASRServiceKind(vendor), ModelID: model, DurationSeconds: &minimumDuration, RequestMetadata: movieBillingMetadata(c, map[string]any{workflowInputFingerprintMetadataKey: inputFingerprint})}
		if identity != nil {
			if !a.checkJSONWorkflowCreditBalance(c, user.ID, precheckConsumption, execution) {
				return
			}
		} else if !a.checkCreditBalance(c, user.ID, precheckConsumption) {
			return
		}
		claimID := ""
		if identity != nil {
			claim, err := a.claimWorkflowProviderSubmission(c.Request.Context(), execution, inputFingerprint)
			if err != nil {
				respondWorkflowRequestError(c, err)
				return
			}
			if claim.Replayed {
				ok(c, gin.H{"success": true, "task_id": claim.ProviderTaskID, "status": "PENDING", "message": "任务已提交，正在处理中", "created_at": time.Now()})
				return
			}
			execution = claim.Execution
			claimID = claim.ClaimID
		}
		resp, err := a.dashscope.SubmitASR(c.Request.Context(), model, req.FileURL, parameters, funASR)
		if err != nil {
			if identity != nil {
				a.respondASRWorkflowSubmissionFailure(c, execution, claimID, err)
				return
			}
			fail(c, http.StatusBadGateway, err.Error())
			return
		}
		output, _ := resp["output"].(map[string]any)
		taskID, _ := output["task_id"].(string)
		status, _ := output["task_status"].(string)
		if taskID == "" {
			if identity != nil {
				a.respondASRWorkflowSubmissionFailure(c, execution, claimID, errors.New("DashScope did not return a task id"))
				return
			}
			fail(c, http.StatusBadGateway, "DashScope 未返回 task_id")
			return
		}
		if identity != nil {
			if _, err := a.persistWorkflowProviderSubmission(c.Request.Context(), execution, claimID, taskID, inputFingerprint); err != nil {
				respondWorkflowRequestError(c, err)
				return
			}
		}
		ok(c, gin.H{"success": true, "task_id": taskID, "status": status, "message": "任务已提交，正在处理中", "created_at": time.Now()})
	}
}

func (a *App) queryVendorASR(vendor string) gin.HandlerFunc {
	return func(c *gin.Context) {
		taskID := c.Param("task_id")
		user := getUser(c)
		values := c.Request.URL.Query()
		identity, err := extractWorkflowRequestIdentityFromValues(values)
		if err != nil {
			respondWorkflowRequestError(c, err)
			return
		}
		if vendor == "paraformer" && identity == nil {
			fail(c, http.StatusBadRequest, "搞笑配音必须提供工作流标识")
			return
		}
		projectID, err := projectIDFromString(values.Get("project_id"))
		if err != nil {
			fail(c, http.StatusBadRequest, "无效的项目 ID")
			return
		}
		execution := workflowRequestExecution{}
		if identity != nil {
			execution, err = a.resolveWorkflowProviderQuery(c.Request.Context(), user.ID, projectID, identity, taskID, "")
			if err != nil {
				respondWorkflowRequestError(c, err)
				return
			}
		}
		if !a.dashscope.Configured() {
			fail(c, http.StatusServiceUnavailable, "DashScope API Key 未配置")
			return
		}
		resp, err := a.dashscope.QueryTask(c.Request.Context(), taskID)
		if err != nil {
			if identity != nil {
				a.respondJSONWorkflowProviderFailure(c, execution, err)
				return
			}
			fail(c, http.StatusBadGateway, err.Error())
			return
		}
		normalized, tokens, err := a.dashscope.NormalizeASRQuery(c.Request.Context(), taskID, vendor, resp)
		if err != nil {
			if identity != nil {
				a.respondJSONWorkflowProviderFailure(c, execution, err)
				return
			}
			fail(c, http.StatusBadGateway, err.Error())
			return
		}
		if identity != nil && vendorASRNormalizedResultFailed(normalized) {
			a.respondJSONWorkflowProviderFailure(c, execution, errors.New("ASR provider result is unavailable"))
			return
		}
		if vendorASRNeedsMinimumBilling(normalized, tokens) {
			duration := 60.0
			normalized["duration"] = duration
			normalized["tokens_consumed"] = int64(duration * 15)
			normalized["billing_duration_fallback"] = true
			tokens = int64(duration * 15)
		}
		if identity != nil && vendorASRNormalizedResultSucceeded(normalized) {
			model := a.cfg.Qwen3ASRModel
			if vendor == "funasr" {
				model = a.cfg.FunASRModel
			}
			if vendor == "paraformer" {
				model = a.cfg.DubbingASRModel
			}
			duration := floatValue(normalized["duration"])
			if err := a.finalizeProviderSuccess(c.Request.Context(), execution, providerSuccessFinalization{ProviderRequestID: &taskID, Consumption: creditConsumption{UserID: user.ID, ProjectID: projectID, CreditCount: tokens, APIEndpoint: vendorASRQueryEndpoint(vendor), Provider: "dashscope", ServiceKind: vendorASRServiceKind(vendor), ModelID: model, DurationSeconds: &duration, RequestMetadata: movieBillingMetadata(c, map[string]any{workflowInputFingerprintMetadataKey: valueOrEmpty(execution.Step.ProviderInputHash)})}}); err != nil {
				a.respondASRWorkflowFinalizationError(c, execution, err)
				return
			}
			ok(c, normalized)
			return
		}
		if tokens > 0 {
			billingKey := user.ID.String() + ":" + vendor + ":" + taskID
			if _, loaded := a.vendorASRBilled.LoadOrStore(billingKey, true); loaded {
				ok(c, normalized)
				return
			}
			endpoint := vendorASRQueryEndpoint(vendor)
			model := a.cfg.Qwen3ASRModel
			if vendor == "funasr" {
				model = a.cfg.FunASRModel
			}
			if vendor == "paraformer" {
				model = a.cfg.DubbingASRModel
			}
			duration := floatValue(normalized["duration"])
			if err := a.billCredits(creditConsumption{
				UserID:          user.ID,
				ProjectID:       projectID,
				CreditCount:     tokens,
				APIEndpoint:     endpoint,
				Provider:        "dashscope",
				ServiceKind:     vendorASRServiceKind(vendor),
				ModelID:         model,
				DurationSeconds: &duration,
				RequestMetadata: movieBillingMetadata(c, gin.H{"task_id": taskID, "vendor": vendor}),
			}); err != nil {
				a.vendorASRBilled.Delete(billingKey)
				fail(c, http.StatusPaymentRequired, err.Error())
				return
			}
		}
		if identity != nil {
			// Processing and zero-usage terminal responses leave the step running for a later Provider result.
			if strings.EqualFold(fmt.Sprint(normalized["status"]), "FAILED") {
				a.respondJSONWorkflowProviderFailure(c, execution, errors.New("ASR provider reported task failure"))
				return
			}
		}
		ok(c, normalized)
	}
}

func vendorASRNormalizedResultFailed(normalized map[string]any) bool {
	status := strings.ToUpper(strings.TrimSpace(fmt.Sprint(normalized["status"])))
	if status == "PENDING" || status == "RUNNING" {
		return false
	}
	if success, ok := normalized["success"].(bool); ok && !success {
		return true
	}
	return status == "FAILED" || status == "SUCCEEDED" && !vendorASRNormalizedResultSucceeded(normalized)
}

func vendorASRNormalizedResultSucceeded(normalized map[string]any) bool {
	success, ok := normalized["success"].(bool)
	if !ok || !success || !strings.EqualFold(fmt.Sprint(normalized["status"]), "SUCCEEDED") {
		return false
	}
	rawResult, ok := normalized["raw_result"].(map[string]any)
	if !ok {
		return false
	}
	transcripts, ok := rawResult["transcripts"].([]any)
	return ok && len(transcripts) > 0
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (a *App) respondASRWorkflowSubmissionFailure(c *gin.Context, execution workflowRequestExecution, claimID string, providerErr error) {
	if err := a.releaseWorkflowProviderSubmissionClaim(c.Request.Context(), execution, claimID); err != nil {
		respondWorkflowRequestError(c, err)
		return
	}
	a.respondJSONWorkflowProviderFailure(c, execution, providerErr)
}

func (a *App) respondASRWorkflowFinalizationError(c *gin.Context, execution workflowRequestExecution, err error) {
	if errors.Is(err, ErrInsufficientCredits) {
		if blockErr := a.finalizeProviderBlocked(c.Request.Context(), execution, "insufficient_credits", "insufficient credits"); blockErr != nil {
			respondWorkflowRequestError(c, blockErr)
			return
		}
		respondWorkflowRequestError(c, newWorkflowRequestError(http.StatusPaymentRequired, "insufficient_credits", false, "insufficient credits", err))
		return
	}
	respondWorkflowRequestError(c, err)
}

func (a *App) dashscopeUploadPolicy(c *gin.Context) {
	var req struct {
		Provider string `json:"provider"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if req.Provider == "" {
		req.Provider = "qwen3"
	}
	if req.Provider != "qwen3" && req.Provider != "funasr" && req.Provider != "qwen-vision" && req.Provider != "paraformer" {
		fail(c, http.StatusBadRequest, "provider 必须是 qwen3、funasr 或 qwen-vision")
		return
	}
	policy, err := a.dashscope.UploadPolicy(c.Request.Context(), req.Provider)
	if err != nil {
		status := http.StatusBadGateway
		if !a.dashscope.Configured() {
			status = http.StatusServiceUnavailable
		}
		fail(c, status, err.Error())
		return
	}
	ok(c, policy)
}

func (a *App) adminListUsers(c *gin.Context) {
	page, limit := pageLimit(c)
	var total int64
	q := a.db.Model(&models.User{})
	if search := strings.TrimSpace(c.Query("search")); search != "" {
		like := "%" + search + "%"
		q = q.Where("username LIKE ? OR email LIKE ?", like, like)
	}
	if raw := strings.TrimSpace(c.Query("is_active")); raw != "" {
		q = q.Where("is_active = ?", raw == "true")
	}
	if raw := strings.TrimSpace(c.Query("is_admin")); raw != "" {
		q = q.Where("is_admin = ?", raw == "true")
	}
	if raw := strings.TrimSpace(c.Query("is_verified")); raw != "" {
		q = q.Where("is_verified = ?", raw == "true")
	}
	q.Count(&total)
	var items []models.User
	orderBy := strings.TrimSpace(c.Query("order_by"))
	if orderBy == "" {
		orderBy = "created_at"
	}
	if orderBy != "created_at" && orderBy != "last_login_at" && orderBy != "username" && orderBy != "email" {
		orderBy = "created_at"
	}
	order := strings.ToLower(strings.TrimSpace(c.Query("order")))
	if order != "asc" {
		order = "desc"
	}
	q.Order(orderBy + " " + order).Offset((page - 1) * limit).Limit(limit).Find(&items)
	out := make([]gin.H, 0, len(items))
	for _, user := range items {
		account, _ := a.ensureTokenAccount(user.ID)
		out = append(out, gin.H{
			"id":                user.ID.String(),
			"username":          user.Username,
			"email":             user.Email,
			"display_name":      user.DisplayName,
			"avatar_url":        user.AvatarURL,
			"is_active":         user.IsActive,
			"is_admin":          user.IsAdmin,
			"is_verified":       user.IsVerified,
			"created_at":        user.CreatedAt,
			"last_login_at":     user.LastLoginAt,
			"total_tokens":      account.TotalTokens,
			"remaining_tokens":  account.RemainingTokens(),
			"total_credits":     account.TotalTokens,
			"remaining_credits": account.RemainingTokens(),
		})
	}
	totalPages := (total + int64(limit) - 1) / int64(limit)
	ok(c, gin.H{"items": out, "total": total, "page": page, "limit": limit, "per_page": limit, "total_pages": totalPages, "pages": totalPages, "has_next": int64(page) < totalPages, "has_prev": page > 1})
}

func (a *App) adminUserDetail(c *gin.Context) {
	id, okID := parseIDParam(c, "user_id")
	if !okID {
		return
	}
	var user models.User
	if err := a.db.First(&user, "id = ?", id).Error; err != nil {
		fail(c, http.StatusNotFound, "用户不存在")
		return
	}
	account, _ := a.ensureTokenAccount(user.ID)
	detail := gin.H{
		"id":                user.ID.String(),
		"username":          user.Username,
		"email":             user.Email,
		"display_name":      user.DisplayName,
		"avatar_url":        user.AvatarURL,
		"is_active":         user.IsActive,
		"is_admin":          user.IsAdmin,
		"is_verified":       user.IsVerified,
		"email_verified_at": user.EmailVerifiedAt,
		"created_at":        user.CreatedAt,
		"last_login_at":     user.LastLoginAt,
		"user":              user,
		"token_account":     accountSummary(account),
		"recent_usage":      a.userRecentUsage(user.ID, 30),
	}
	ok(c, detail)
}

func (a *App) adminAdjustTokens(c *gin.Context) {
	id, okID := parseIDParam(c, "user_id")
	if !okID {
		return
	}
	var req struct {
		Amount int64  `json:"amount"`
		Reason string `json:"reason"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if _, err := a.ensureTokenAccount(id); err != nil {
		fail(c, http.StatusInternalServerError, "读取 Credits 账户失败")
		return
	}
	adminUser := getUser(c)
	adjustedAt := time.Now().UTC()
	var previous, current, actualChange int64
	if err := a.db.Transaction(func(tx *gorm.DB) error {
		var account models.UserTokenAccount
		if err := tx.First(&account, "user_id = ?", id).Error; err != nil {
			return err
		}
		previous = account.RemainingTokens()
		account.TotalTokens += req.Amount
		if err := tx.Save(&account).Error; err != nil {
			return err
		}
		current = account.RemainingTokens()
		actualChange = current - previous
		if actualChange == 0 {
			return nil
		}
		return a.createCreditLedgerRecord(tx, creditLedgerEntry{
			UserID:       id,
			EventType:    creditLedgerEventAdminAdjustment,
			Item:         "管理员调整",
			ChangeAmount: actualChange,
			BalanceAfter: current,
			Reason:       req.Reason,
			RequestMetadata: movieBillingMetadata(c, map[string]any{
				"requested_amount": req.Amount,
				"admin_user_id":    adminUser.ID.String(),
			}),
			RecordedAt: adjustedAt,
		})
	}); err != nil {
		fail(c, http.StatusInternalServerError, "调整 Credits 失败")
		return
	}
	ok(c, gin.H{"success": true, "message": "Credits 已调整", "user_id": id.String(), "amount": actualChange, "adjustment_amount": actualChange, "requested_amount": req.Amount, "previous_balance": previous, "current_balance": current, "remaining_tokens": current, "remaining_credits": current, "reason": req.Reason, "adjusted_at": adjustedAt})
}

func (a *App) adminUserStatus(c *gin.Context) {
	id, okID := parseIDParam(c, "user_id")
	if !okID {
		return
	}
	var req struct {
		IsActive bool `json:"is_active"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if err := a.db.Model(&models.User{}).Where("id = ?", id).Update("is_active", req.IsActive).Error; err != nil {
		fail(c, http.StatusInternalServerError, "更新用户状态失败")
		return
	}
	a.invalidateAuthUser(id)
	ok(c, gin.H{"success": true, "user_id": id.String(), "is_active": req.IsActive})
}

func (a *App) adminUserRole(c *gin.Context) {
	id, okID := parseIDParam(c, "user_id")
	if !okID {
		return
	}
	var req struct {
		IsAdmin bool `json:"is_admin"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if err := a.db.Model(&models.User{}).Where("id = ?", id).Update("is_admin", req.IsAdmin).Error; err != nil {
		fail(c, http.StatusInternalServerError, "更新用户角色失败")
		return
	}
	a.invalidateAuthUser(id)
	ok(c, gin.H{"success": true, "user_id": id.String(), "is_admin": req.IsAdmin})
}

func (a *App) adminDeleteUser(c *gin.Context) {
	id, okID := parseIDParam(c, "user_id")
	if !okID {
		return
	}
	adminUser := getUser(c)
	if adminUser.ID == id {
		fail(c, http.StatusBadRequest, "不能彻底删除当前登录的管理员账户")
		return
	}

	var user models.User
	if err := a.db.First(&user, "id = ?", id).Error; err != nil {
		fail(c, http.StatusNotFound, "用户不存在")
		return
	}

	deleted := map[string]int64{}
	if err := a.db.Transaction(func(tx *gorm.DB) error {
		var projectIDs []uuid.UUID
		if err := tx.Model(&models.Project{}).Unscoped().Where("owner_id = ?", id).Pluck("id", &projectIDs).Error; err != nil {
			return err
		}

		deleteRows := func(key string, model any, query string, args ...any) error {
			result := tx.Unscoped().Where(query, args...).Delete(model)
			if result.Error != nil {
				return result.Error
			}
			deleted[key] += result.RowsAffected
			return nil
		}

		for _, item := range []struct {
			key   string
			model any
		}{
			{"upload_records", &models.UploadRecord{}},
			{"notifications", &models.Notification{}},
			{"creem_checkout_sessions", &models.CreemCheckoutSession{}},
			{"subscription_credit_grants", &models.SubscriptionCreditGrant{}},
			{"user_subscriptions", &models.UserSubscription{}},
			{"credit_ledger_records", &models.CreditLedgerRecord{}},
			{"token_usage_records", &models.TokenUsageRecord{}},
			{"token_recharge_records", &models.TokenRechargeRecord{}},
			{"wechat_payments", &models.WeChatPayment{}},
			{"user_token_accounts", &models.UserTokenAccount{}},
			{"user_profiles", &models.UserProfile{}},
			{"oauth_login_tickets", &models.OAuthLoginTicket{}},
			{"oauth_accounts", &models.OAuthAccount{}},
		} {
			if err := deleteRows(item.key, item.model, "user_id = ?", id); err != nil {
				return err
			}
		}

		if len(projectIDs) > 0 {
			for _, item := range []struct {
				key   string
				model any
			}{
				{"project_workflow_events", &models.ProjectWorkflowEvent{}},
				{"project_workflows", &models.ProjectWorkflow{}},
				{"project_voiceovers", &models.ProjectVoiceover{}},
				{"project_scripts", &models.ProjectScript{}},
				{"project_generation_configs", &models.ProjectGenerationConfig{}},
				{"project_subtitles", &models.ProjectSubtitle{}},
				{"project_video_sources", &models.ProjectVideoSource{}},
			} {
				if err := deleteRows(item.key, item.model, "project_id IN ?", projectIDs); err != nil {
					return err
				}
			}
			if err := deleteRows("projects", &models.Project{}, "id IN ?", projectIDs); err != nil {
				return err
			}
		}

		if err := deleteRows("auth_email_codes", &models.AuthEmailCode{}, "email = ?", user.Email); err != nil {
			return err
		}
		if err := tx.Model(&models.SystemSetting{}).Where("updated_by = ?", id).Update("updated_by", nil).Error; err != nil {
			return err
		}
		if err := deleteRows("users", &models.User{}, "id = ?", id); err != nil {
			return err
		}
		return nil
	}); err != nil {
		fail(c, http.StatusInternalServerError, "彻底删除用户失败")
		return
	}

	a.invalidateAuthUser(id)
	ok(c, gin.H{
		"success":        true,
		"message":        "用户已彻底删除",
		"user_id":        id.String(),
		"deleted_counts": deleted,
	})
}

func (a *App) adminOverview(c *gin.Context) {
	period := parseStatisticsPeriod(c)
	var users, activeUsers, adminUsers, verifiedUsers, projects, usage, payments, totalRequests, activePeriodUsers, newUsers, missingPricing int64
	var revenue, periodRecharge, cost, confirmedRevenue, profit float64
	a.db.Model(&models.User{}).Count(&users)
	a.db.Model(&models.User{}).Where("is_active = ?", true).Count(&activeUsers)
	a.db.Model(&models.User{}).Where("is_admin = ?", true).Count(&adminUsers)
	a.db.Model(&models.User{}).Where("is_verified = ?", true).Count(&verifiedUsers)
	a.db.Model(&models.Project{}).Count(&projects)
	a.db.Model(&models.TokenUsageRecord{}).Select(creditSumSQL).Scan(&usage)
	a.db.Model(&models.WeChatPayment{}).Where("status = ?", "paid").Count(&payments)
	a.db.Model(&models.WeChatPayment{}).Where("status = ?", "paid").Select("coalesce(sum(total_amount), 0)").Scan(&revenue)

	periodUsage := period.applyUsage(a.db.Model(&models.TokenUsageRecord{}))
	periodUsage.Select("count(*)").Scan(&totalRequests)
	period.applyUsage(a.db.Model(&models.TokenUsageRecord{})).Select(creditSumSQL).Scan(&usage)
	period.applyUsage(a.db.Model(&models.TokenUsageRecord{})).Select("count(distinct user_id)").Scan(&activePeriodUsers)
	period.applyUsage(a.db.Model(&models.TokenUsageRecord{})).Where("missing_pricing = ?", true).Count(&missingPricing)
	period.applyUsage(a.db.Model(&models.TokenUsageRecord{})).Select(costSumSQL).Scan(&cost)
	period.applyUsage(a.db.Model(&models.TokenUsageRecord{})).Select(revenueSumSQL).Scan(&confirmedRevenue)
	period.applyUsage(a.db.Model(&models.TokenUsageRecord{})).Select(profitSumSQL).Scan(&profit)
	period.applyCreated(a.db.Model(&models.User{})).Count(&newUsers)
	period.applyPayments(a.db.Model(&models.WeChatPayment{})).Select("coalesce(sum(total_amount), 0)").Scan(&periodRecharge)

	ok(c, gin.H{
		"total_users":            users,
		"active_users":           activeUsers,
		"admin_users":            adminUsers,
		"verified_users":         verifiedUsers,
		"total_tokens_consumed":  usage,
		"total_credits_consumed": usage,
		"total_recharged":        revenue,
		"confirmed_revenue":      confirmedRevenue,
		"third_party_cost":       cost,
		"gross_profit":           profit,
		"gross_margin":           safeMargin(profit, confirmedRevenue),
		"currency":               billingCurrency,
		"missing_pricing_count":  missingPricing,
		"users":                  gin.H{"total": users, "active": activeUsers, "admin": adminUsers, "verified": verifiedUsers},
		"projects":               gin.H{"total": projects},
		"tokens":                 gin.H{"used": usage},
		"credits":                gin.H{"used": usage},
		"payments":               gin.H{"paid_count": payments, "revenue": revenue},
		"period_summary": gin.H{
			"start_date":             period.Start.Format("2006-01-02"),
			"end_date":               period.End.Add(-24 * time.Hour).Format("2006-01-02"),
			"total_tokens_consumed":  usage,
			"total_credits_consumed": usage,
			"total_requests":         totalRequests,
			"active_users":           activePeriodUsers,
			"new_users":              newUsers,
			"total_recharged":        periodRecharge,
			"confirmed_revenue":      confirmedRevenue,
			"third_party_cost":       cost,
			"gross_profit":           profit,
			"gross_margin":           safeMargin(profit, confirmedRevenue),
			"currency":               billingCurrency,
			"missing_pricing_count":  missingPricing,
		},
	})
}

func (a *App) adminUserRanking(c *gin.Context) {
	type row struct {
		UserID   string
		Credits  int64
		Requests int64
	}
	period := parseStatisticsPeriod(c)
	var rows []row
	period.applyUsage(a.db.Model(&models.TokenUsageRecord{})).
		Select("user_id, " + creditSelect("credits") + ", count(*) as requests").
		Group("user_id").Order("credits DESC").Limit(queryInt(c, "limit", 20)).Scan(&rows)
	var totalConsumption int64
	period.applyUsage(a.db.Model(&models.TokenUsageRecord{})).Select(creditSumSQL).Scan(&totalConsumption)
	items := make([]gin.H, 0, len(rows))
	for index, row := range rows {
		var user models.User
		a.db.First(&user, "id = ?", row.UserID)
		percentage := 0.0
		if totalConsumption > 0 {
			percentage = float64(row.Credits) / float64(totalConsumption) * 100
		}
		items = append(items, gin.H{"rank": index + 1, "user_id": row.UserID, "username": user.Username, "email": user.Email, "token_count": row.Credits, "tokens_used": row.Credits, "credit_count": row.Credits, "credits_used": row.Credits, "request_count": row.Requests, "percentage": percentage})
	}
	ok(c, gin.H{"items": items, "ranking": items, "total": len(items), "period": period.Label, "total_consumption": totalConsumption, "start_date": period.Start.Format("2006-01-02"), "end_date": period.End.Add(-24 * time.Hour).Format("2006-01-02")})
}

func (a *App) adminTokenTrends(c *gin.Context) {
	type row struct {
		Date        string
		Credits     int64
		Requests    int64
		UniqueUsers int64
		Cost        float64
		Revenue     float64
		Profit      float64
	}
	period := parseStatisticsPeriod(c)
	var rows []row
	period.applyUsage(a.db.Model(&models.TokenUsageRecord{})).
		Select("date(recorded_at) as date, " + creditSelect("credits") + ", count(*) as requests, count(distinct user_id) as unique_users, " + costSumSQL + " as cost, " + revenueSumSQL + " as revenue, " + profitSumSQL + " as profit").
		Group("date(recorded_at)").Order("date ASC").Scan(&rows)
	items := make([]gin.H, 0, len(rows))
	var totalCredits, totalRequests int64
	var totalCost, totalRevenue, totalProfit float64
	for _, row := range rows {
		totalCredits += row.Credits
		totalRequests += row.Requests
		totalCost += row.Cost
		totalRevenue += row.Revenue
		totalProfit += row.Profit
		items = append(items, gin.H{"date": row.Date, "token_count": row.Credits, "credit_count": row.Credits, "request_count": row.Requests, "unique_users": row.UniqueUsers, "cost_amount": row.Cost, "revenue_amount": row.Revenue, "profit_amount": row.Profit})
	}
	ok(c, gin.H{"items": items, "trend_data": items, "group_by": c.DefaultQuery("group_by", "day"), "total_tokens": totalCredits, "total_credits": totalCredits, "total_requests": totalRequests, "cost_amount": roundMoney(totalCost), "revenue_amount": roundMoney(totalRevenue), "profit_amount": roundMoney(totalProfit), "currency": billingCurrency, "start_date": period.Start.Format("2006-01-02"), "end_date": period.End.Add(-24 * time.Hour).Format("2006-01-02")})
}

func (a *App) adminModelUsage(c *gin.Context) {
	type row struct {
		Provider         string
		ServiceKind      string
		ModelID          string
		ModelName        string
		Credits          int64
		PromptTokens     int64
		CompletionTokens int64
		TotalModelTokens int64
		UTF8Bytes        int64
		DurationSeconds  float64
		Cost             float64
		Revenue          float64
		Profit           float64
		Requests         int64
		UniqueUsers      int64
		MissingPricing   int64
	}
	period := parseStatisticsPeriod(c)
	var rows []row
	period.applyUsage(a.db.Model(&models.TokenUsageRecord{})).
		Select("provider, service_kind, model_id, model_name, " + creditSelect("credits") + ", coalesce(sum(prompt_tokens), 0) as prompt_tokens, coalesce(sum(completion_tokens), 0) as completion_tokens, coalesce(sum(total_model_tokens), 0) as total_model_tokens, coalesce(sum(utf8_bytes), 0) as utf8_bytes, coalesce(sum(duration_seconds), 0) as duration_seconds, " + costSumSQL + " as cost, " + revenueSumSQL + " as revenue, " + profitSumSQL + " as profit, count(*) as requests, count(distinct user_id) as unique_users, coalesce(sum(case when missing_pricing then 1 else 0 end), 0) as missing_pricing").
		Group("provider, service_kind, model_id, model_name").Order("credits DESC").Scan(&rows)
	var totalCredits, totalRequests int64
	var totalCost, totalRevenue, totalProfit float64
	for _, row := range rows {
		totalCredits += row.Credits
		totalRequests += row.Requests
		totalCost += row.Cost
		totalRevenue += row.Revenue
		totalProfit += row.Profit
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		percentage := 0.0
		if totalCredits > 0 {
			percentage = float64(row.Credits) / float64(totalCredits) * 100
		}
		modelID := firstNonEmpty(row.ModelID, row.ModelName, "unknown")
		items = append(items, gin.H{"provider": row.Provider, "service_kind": row.ServiceKind, "model_id": modelID, "model_name": modelID, "token_count": row.Credits, "credit_count": row.Credits, "request_count": row.Requests, "unique_users": row.UniqueUsers, "percentage": percentage, "prompt_tokens": row.PromptTokens, "completion_tokens": row.CompletionTokens, "total_model_tokens": row.TotalModelTokens, "utf8_bytes": row.UTF8Bytes, "duration_seconds": row.DurationSeconds, "cost_amount": row.Cost, "revenue_amount": row.Revenue, "profit_amount": row.Profit, "gross_margin": safeMargin(row.Profit, row.Revenue), "currency": billingCurrency, "missing_pricing_count": row.MissingPricing})
	}
	ok(c, gin.H{"items": items, "model_stats": items, "total_tokens": totalCredits, "total_credits": totalCredits, "total_requests": totalRequests, "cost_amount": roundMoney(totalCost), "revenue_amount": roundMoney(totalRevenue), "profit_amount": roundMoney(totalProfit), "gross_margin": safeMargin(totalProfit, totalRevenue), "currency": billingCurrency, "period": period.Label})
}

func (a *App) adminAPIUsage(c *gin.Context) {
	type row struct {
		APIEndpoint    string
		Credits        int64
		Requests       int64
		UniqueUsers    int64
		UTF8Bytes      int64
		Cost           float64
		Revenue        float64
		Profit         float64
		MissingPricing int64
	}
	period := parseStatisticsPeriod(c)
	var rows []row
	period.applyUsage(a.db.Model(&models.TokenUsageRecord{})).
		Select("api_endpoint, " + creditSelect("credits") + ", count(*) as requests, count(distinct user_id) as unique_users, coalesce(sum(utf8_bytes), 0) as utf8_bytes, " + costSumSQL + " as cost, " + revenueSumSQL + " as revenue, " + profitSumSQL + " as profit, coalesce(sum(case when missing_pricing then 1 else 0 end), 0) as missing_pricing").
		Group("api_endpoint").Order("credits DESC").Scan(&rows)
	var totalCredits, totalRequests int64
	var totalCost, totalRevenue, totalProfit float64
	for _, row := range rows {
		totalCredits += row.Credits
		totalRequests += row.Requests
		totalCost += row.Cost
		totalRevenue += row.Revenue
		totalProfit += row.Profit
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		percentage := 0.0
		if totalCredits > 0 {
			percentage = float64(row.Credits) / float64(totalCredits) * 100
		}
		avg := 0.0
		if row.Requests > 0 {
			avg = float64(row.Credits) / float64(row.Requests)
		}
		items = append(items, gin.H{"api_endpoint": row.APIEndpoint, "token_count": row.Credits, "credit_count": row.Credits, "request_count": row.Requests, "unique_users": row.UniqueUsers, "percentage": percentage, "avg_tokens_per_request": avg, "avg_credits_per_request": avg, "utf8_bytes": row.UTF8Bytes, "cost_amount": row.Cost, "revenue_amount": row.Revenue, "profit_amount": row.Profit, "gross_margin": safeMargin(row.Profit, row.Revenue), "currency": billingCurrency, "missing_pricing_count": row.MissingPricing})
	}
	ok(c, gin.H{"items": items, "api_stats": items, "total_tokens": totalCredits, "total_credits": totalCredits, "total_requests": totalRequests, "cost_amount": roundMoney(totalCost), "revenue_amount": roundMoney(totalRevenue), "profit_amount": roundMoney(totalProfit), "gross_margin": safeMargin(totalProfit, totalRevenue), "currency": billingCurrency, "period": period.Label})
}

func extractSummary(content string) string {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stripCodeFence(content)), &parsed); err == nil {
		for _, key := range []string{"summary", "content", "description"} {
			if value, ok := parsed[key].(string); ok && strings.TrimSpace(value) != "" {
				return value
			}
		}
	}
	return strings.TrimSpace(content)
}

func vendorASRQueryEndpoint(vendor string) string {
	if vendor == "paraformer" {
		return "/api/v1/tools/dubbing/asr/query"
	}
	if vendor == "funasr" {
		return "/api/v1/tools/funasr/asr/query"
	}
	return "/api/v1/tools/qwen3/asr/query"
}

func vendorASRNeedsMinimumBilling(normalized map[string]any, tokens int64) bool {
	if tokens > 0 {
		return false
	}
	if success, ok := normalized["success"].(bool); ok && !success {
		return false
	}
	status := strings.ToUpper(strings.TrimSpace(fmt.Sprint(normalized["status"])))
	if status != "SUCCEEDED" {
		return false
	}
	return floatValue(normalized["duration"]) <= 0
}

func extractOCR(content string) ([]string, []string) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stripCodeFence(content)), &parsed); err != nil {
		return []string{}, []string{}
	}
	return stringSlice(parsed["timestamps"]), stringSlice(parsed["text"])
}

func extractTimelineDecision(content string) map[string]any {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stripCodeFence(content)), &parsed); err == nil && parsed != nil {
		return parsed
	}
	return map[string]any{}
}

func extractSubtitleValidation(content string) map[string]any {
	parsed, err := frameQualityGateJSONObject(content)
	if err != nil {
		return map[string]any{}
	}
	return parsed
}

func parseSubtitleValidationResponse(content string, expectedSegments []any) (map[string]any, []any, error) {
	analysis, err := frameQualityGateJSONObject(content)
	if err != nil {
		return nil, nil, fmt.Errorf("response is not a JSON object")
	}
	segments := anySlice(analysis["segments"])
	if len(segments) == 0 {
		segments = subtitleValidationSegmentsFromValue(analysis["subtitles"], expectedSegments)
	}
	if err := validateSubtitleValidationSegments(segments, expectedSegments); err != nil {
		return nil, nil, err
	}
	analysis["segments"] = segments
	return analysis, segments, nil
}

func subtitleValidationSegmentsFromValue(value any, expectedSegments []any) []any {
	if segments := anySlice(value); len(segments) > 0 {
		return segments
	}
	record, ok := value.(map[string]any)
	if !ok {
		return []any{}
	}
	segments := make([]any, 0, len(expectedSegments))
	for _, expected := range expectedSegments {
		id := subtitleValidationSegmentID(expected)
		segment, exists := record[id]
		if !exists {
			continue
		}
		segmentRecord, ok := segment.(map[string]any)
		if !ok {
			continue
		}
		if subtitleValidationSegmentID(segmentRecord) == "" {
			segmentRecord["id"] = id
		}
		segments = append(segments, segmentRecord)
	}
	return segments
}

func validateSubtitleValidationSegments(segments []any, expectedSegments []any) error {
	if len(segments) != len(expectedSegments) {
		return fmt.Errorf("expected %d target segments, received %d", len(expectedSegments), len(segments))
	}
	for index := range expectedSegments {
		expectedID := subtitleValidationSegmentID(expectedSegments[index])
		actualID := subtitleValidationSegmentID(segments[index])
		if expectedID == "" || actualID != expectedID {
			return fmt.Errorf("target segment id mismatch at index %d: expected %q, received %q", index, expectedID, actualID)
		}
	}
	return nil
}

func subtitleValidationSegmentID(segment any) string {
	record, ok := segment.(map[string]any)
	if !ok {
		return ""
	}
	id := strings.TrimSpace(fmt.Sprint(record["id"]))
	if id == "<nil>" {
		return ""
	}
	return id
}

var narrativeThinkTagPattern = regexp.MustCompile(`(?is)<think>.*?</think>`)

func extractNarrativeUnderstanding(content string) map[string]any {
	// MiniMax-M3 wraps answers in <think>…</think> (and sometimes markdown
	// fences) even when JSON output is requested; strip both before parsing.
	cleaned := strings.TrimSpace(narrativeThinkTagPattern.ReplaceAllString(strings.TrimSpace(content), ""))
	cleaned = stripCodeFence(cleaned)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(cleaned), &parsed); err == nil && parsed != nil {
		return parsed
	}
	repaired := malformedJSONStringTerminator.ReplaceAllString(cleaned, `","$1":`)
	if repaired != cleaned {
		if err := json.Unmarshal([]byte(repaired), &parsed); err == nil && parsed != nil {
			return parsed
		}
	}
	// 长素材的输出可能在根对象闭合前被截断：先尝试从原文挽救 events 数组，
	// 再退回平衡扫描（扫描可能捞到嵌套片段，所以放在挽救之后）。
	if salvaged := salvageEventsFromTruncatedPayload(content); salvaged != nil {
		return salvaged
	}
	if scanned, scanErr := frameQualityGateJSONObject(cleaned); scanErr == nil && scanned != nil {
		return scanned
	}
	return map[string]any{}
}

// salvageEventsFromTruncatedPayload 从可能被截断的剧情理解输出中挽救 events
// 数组：按括号配对逐个抽取已完整输出的事件对象。数组未开始时返回 nil。

// parseFlexibleSeconds accepts timestamps that models emit in various forms:
// 85.3, "85.3", "85秒", "1:25", "01:25.5", "1:02:03". Returns ok=false when the
// value cannot be interpreted as a non-negative second count.
func parseFlexibleSeconds(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, false
		}
		return typed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case string:
		return parseFlexibleSecondsString(typed)
	default:
		return 0, false
	}
}

func parseFlexibleSecondsString(raw string) (float64, bool) {
	trimmed := strings.NewReplacer("秒钟", "", "秒", "", " ", "").Replace(strings.TrimSpace(raw))
	trimmed = strings.TrimSuffix(trimmed, "s")
	trimmed = strings.TrimSuffix(trimmed, "S")
	if trimmed == "" {
		return 0, false
	}
	parts := strings.Split(trimmed, ":")
	if len(parts) > 3 {
		return 0, false
	}
	seconds := 0.0
	for _, part := range parts {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil || parsed < 0 {
			return 0, false
		}
		seconds = seconds*60 + parsed
	}
	return seconds, true
}

// parseNarrativeTimeRange extracts start/end seconds from flexible range values:
// {"start":...,"end":...}, [12.5, 40], "12.5-40", "1:25~2:30", "12.5 至 40 秒".
func parseNarrativeTimeRange(value any) (float64, float64, bool) {
	switch typed := value.(type) {
	case map[string]any:
		start, startOK := parseFlexibleSeconds(typed["start"])
		end, endOK := parseFlexibleSeconds(typed["end"])
		if startOK && endOK {
			return start, end, true
		}
		return 0, 0, false
	case []any:
		if len(typed) == 2 {
			start, startOK := parseFlexibleSeconds(typed[0])
			end, endOK := parseFlexibleSeconds(typed[1])
			if startOK && endOK {
				return start, end, true
			}
		}
		return 0, 0, false
	case string:
		trimmed := strings.NewReplacer("～", "~", "—", "-", "–", "-", "至", "~", "到", "~").Replace(strings.TrimSpace(typed))
		for _, separator := range []string{"~", "-"} {
			if idx := strings.Index(trimmed, separator); idx > 0 {
				start, startOK := parseFlexibleSecondsString(strings.TrimSpace(trimmed[:idx]))
				end, endOK := parseFlexibleSecondsString(strings.TrimSpace(trimmed[idx+len(separator):]))
				if startOK && endOK {
					return start, end, true
				}
			}
		}
		return 0, 0, false
	default:
		return 0, 0, false
	}
}

var narrativeEventDescriptionKeys = []string{"description", "summary", "event", "desc", "title", "name"}

// normalizeNarrativeEventRecord canonicalizes one story event: description from
// common alternative keys, numeric start/end from direct values, flexible
// strings, or nested time_range/timestamp payloads. Returns ok=false when a
// usable description plus start/end pair cannot be recovered.
func normalizeNarrativeEventRecord(record map[string]any) (map[string]any, bool) {
	normalized := make(map[string]any, len(record)+1)
	for key, value := range record {
		if key == "time_range" || key == "timestamp" {
			continue
		}
		normalized[key] = value
	}
	description := ""
	for _, key := range narrativeEventDescriptionKeys {
		if value, isString := record[key].(string); isString && strings.TrimSpace(value) != "" {
			description = strings.TrimSpace(value)
			break
		}
	}
	if description == "" {
		return nil, false
	}
	normalized["description"] = description
	start, startOK := parseFlexibleSeconds(record["start"])
	if !startOK {
		if rangeValue, hasRange := record["time_range"]; hasRange {
			start, _, startOK = parseNarrativeTimeRange(rangeValue)
		}
	}
	if !startOK {
		if rangeValue, hasRange := record["timestamp"]; hasRange {
			start, _, startOK = parseNarrativeTimeRange(rangeValue)
		}
	}
	end, endOK := parseFlexibleSeconds(record["end"])
	if !endOK {
		if rangeValue, hasRange := record["time_range"]; hasRange {
			_, end, endOK = parseNarrativeTimeRange(rangeValue)
		}
	}
	if !endOK {
		if rangeValue, hasRange := record["timestamp"]; hasRange {
			_, end, endOK = parseNarrativeTimeRange(rangeValue)
		}
	}
	if !startOK || !endOK {
		return nil, false
	}
	normalized["start"] = start
	normalized["end"] = end
	return normalized, true
}

// normalizeNarrativeUnderstandingEvents canonicalizes the events array of a
// parsed narrative understanding payload. Returns the payload and the number of
// usable events. Models occasionally emit timestamps as "mm:ss" strings, nested
// time_range values, or descriptions under alternative keys; the narration
// pipeline requires numeric seconds plus a non-empty description.
func normalizeNarrativeUnderstandingEvents(parsed map[string]any) (map[string]any, int) {
	if parsed == nil {
		return parsed, 0
	}
	events, _ := parsed["events"].([]any)
	if len(events) == 0 {
		if analysis, isMap := parsed["analysis"].(map[string]any); isMap {
			if nested, nestedOK := analysis["events"].([]any); nestedOK && len(nested) > 0 {
				parsed["events"] = nested
				events = nested
			}
		}
	}
	if len(events) == 0 {
		parsed["events"] = []any{}
		return parsed, 0
	}
	normalizedEvents := make([]any, 0, len(events))
	for _, item := range events {
		record, isMap := item.(map[string]any)
		if !isMap {
			continue
		}
		normalized, valid := normalizeNarrativeEventRecord(record)
		if !valid {
			continue
		}
		normalizedEvents = append(normalizedEvents, normalized)
	}
	parsed["events"] = normalizedEvents
	return parsed, len(normalizedEvents)
}

func subtitleRecordFromSegments(segments []any) gin.H {
	result := gin.H{}
	for index, item := range segments {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key := fmt.Sprint(record["id"])
		if strings.TrimSpace(key) == "" || key == "<nil>" {
			key = fmt.Sprint(index + 1)
		}
		result[key] = record
	}
	return result
}

func anySlice(value any) []any {
	raw, ok := value.([]any)
	if !ok {
		return []any{}
	}
	return raw
}

func stringSlice(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		return []string{}
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		out = append(out, fmt.Sprint(item))
	}
	return out
}

func stripCodeFence(content string) string {
	cleaned := strings.TrimSpace(content)
	if strings.HasPrefix(cleaned, "```") {
		cleaned = strings.TrimPrefix(cleaned, "```json")
		cleaned = strings.TrimPrefix(cleaned, "```")
		cleaned = strings.TrimSuffix(cleaned, "```")
	}
	return strings.TrimSpace(cleaned)
}
