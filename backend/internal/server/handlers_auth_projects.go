package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type registerRequest struct {
	Username    string  `json:"username" binding:"required"`
	Email       string  `json:"email" binding:"required,email"`
	Password    string  `json:"password" binding:"required"`
	Code        string  `json:"code"`
	DisplayName *string `json:"display_name"`
}

type loginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

type projectListItem struct {
	ID              uuid.UUID      `json:"id"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	Name            string         `json:"name"`
	Description     *string        `json:"description"`
	ThumbnailURL    *string        `json:"thumbnail_url"`
	OwnerID         uuid.UUID      `json:"owner_id"`
	Status          string         `json:"status"`
	Workflow        datatypes.JSON `json:"workflow"`
	Artifacts       datatypes.JSON `json:"artifacts"`
	ProjectMetadata datatypes.JSON `json:"metadata"`
}

var projectListColumns = []string{
	"id",
	"created_at",
	"updated_at",
	"name",
	"description",
	"thumbnail_url",
	"owner_id",
	"status",
	"current_step",
	"workflow_schema_version",
	"artifacts",
	"project_metadata",
}

var (
	usernamePattern      = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	passwordUpperPattern = regexp.MustCompile(`[A-Z]`)
	passwordLowerPattern = regexp.MustCompile(`[a-z]`)
	passwordDigitPattern = regexp.MustCompile(`[0-9]`)
)

func normalizeOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func validateRegisterInput(req registerRequest) error {
	if len(req.Username) < 3 {
		return errors.New("用户名至少 3 个字符")
	}
	if len(req.Username) > 20 {
		return errors.New("用户名不能超过 20 个字符")
	}
	if !usernamePattern.MatchString(req.Username) {
		return errors.New("用户名只能包含字母、数字、下划线和连字符")
	}
	return validatePasswordPolicy(req.Password)
}

func validatePasswordPolicy(password string) error {
	if len(password) < 8 {
		return errors.New("密码至少 8 个字符")
	}
	if len([]byte(password)) > 72 {
		return errors.New("密码不能超过 72 字节")
	}
	if !passwordUpperPattern.MatchString(password) ||
		!passwordLowerPattern.MatchString(password) ||
		!passwordDigitPattern.MatchString(password) {
		return errors.New("密码需包含大小写字母和数字")
	}
	return nil
}

func (a *App) register(c *gin.Context) {
	var req registerRequest
	if !bindJSON(c, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.Email = normalizeEmail(req.Email)
	req.DisplayName = normalizeOptionalString(req.DisplayName)
	if err := validateRegisterInput(req); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}

	user := models.User{
		Username:            req.Username,
		Email:               req.Email,
		DisplayName:         req.DisplayName,
		PasswordHash:        string(hash),
		PasswordAuthEnabled: true,
		IsActive:            true,
		IsVerified:          true,
		EmailVerifiedAt:     ptrTime(time.Now().UTC()),
	}

	if err := a.db.Transaction(func(tx *gorm.DB) error {
		if err := a.consumeEmailCodeWithDB(tx, req.Email, emailCodePurposeRegister, req.Code); err != nil {
			return err
		}
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		if _, err := ensureProfileWithDB(tx, user.ID); err != nil {
			return err
		}
		if _, err := ensureTokenAccountWithDB(tx, user.ID); err != nil {
			return err
		}
		return nil
	}); err != nil {
		if strings.Contains(err.Error(), "验证码") || strings.Contains(err.Error(), "邮箱验证码") {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		if message, ok := registerDuplicateMessage(err); ok {
			fail(c, http.StatusConflict, message)
			return
		}
		_ = c.Error(err)
		fail(c, http.StatusInternalServerError, "注册失败，请稍后重试")
		return
	}

	created(c, gin.H{"message": "注册成功", "success": true, "user_id": user.ID.String(), "username": user.Username, "email": user.Email})
}

func registerDuplicateMessage(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "unique") &&
		!strings.Contains(message, "duplicate") &&
		!strings.Contains(message, "duplicated") {
		return "", false
	}
	switch {
	case strings.Contains(message, "uq_users_email") ||
		strings.Contains(message, "uni_users_email") ||
		strings.Contains(message, "users.email"):
		return "邮箱已被注册", true
	case strings.Contains(message, "uq_users_username") ||
		strings.Contains(message, "uni_users_username") ||
		strings.Contains(message, "users.username"):
		return "用户名已存在", true
	default:
		return "", false
	}
}

func (a *App) login(c *gin.Context) {
	var req loginRequest
	if !bindJSON(c, &req) {
		return
	}
	var user models.User
	if err := a.db.First(&user, "email = ?", normalizeEmail(req.Email)).Error; err != nil {
		fail(c, http.StatusUnauthorized, "邮箱或密码错误")
		return
	}
	if strings.TrimSpace(user.PasswordHash) == "" {
		fail(c, http.StatusUnauthorized, "邮箱或密码错误")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
		fail(c, http.StatusUnauthorized, "邮箱或密码错误")
		return
	}
	now := time.Now().UTC()
	user.LastLoginAt = &now
	user.PasswordAuthEnabled = true
	_ = a.db.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"last_login_at":         now,
		"password_auth_enabled": true,
	}).Error
	tokens, err := a.makeTokens(user)
	if err != nil {
		fail(c, http.StatusInternalServerError, "生成令牌失败")
		return
	}
	ok(c, gin.H{"message": "登录成功", "success": true, "user_id": user.ID.String(), "username": user.Username, "email": user.Email, "tokens": tokens})
}

func (a *App) refresh(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refresh_token" binding:"required"`
	}
	if !bindJSON(c, &req) {
		return
	}
	tokens, err := a.refreshTokens(req.RefreshToken)
	if err != nil {
		fail(c, http.StatusUnauthorized, "无效的刷新令牌")
		return
	}
	ok(c, tokens)
}

func (a *App) parseRefreshToken(raw string) (map[string]any, models.User, bool) {
	return a.parseToken(raw, "refresh")
}

func (a *App) refreshTokens(raw string) (gin.H, error) {
	_, user, valid := a.parseRefreshToken(raw)
	if !valid {
		return nil, errors.New("invalid refresh token")
	}
	return a.makeTokens(user)
}

func (a *App) logout(c *gin.Context) {
	ok(c, gin.H{"message": "登出成功", "success": true})
}

func (a *App) resetPassword(c *gin.Context) {
	var req struct {
		Email           string `json:"email" binding:"required,email"`
		Code            string `json:"code" binding:"required"`
		NewPassword     string `json:"new_password" binding:"required,min=8"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if req.ConfirmPassword != "" && req.NewPassword != req.ConfirmPassword {
		fail(c, http.StatusBadRequest, "新密码和确认密码不匹配")
		return
	}
	if err := validatePasswordPolicy(req.NewPassword); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	req.Email = normalizeEmail(req.Email)
	if err := a.consumeEmailCode(req.Email, emailCodePurposeReset, req.Code); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}
	if err := a.db.Model(&models.User{}).Where("email = ?", req.Email).Updates(map[string]any{"password_hash": string(hash), "password_auth_enabled": true}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "重置密码失败")
		return
	}
	ok(c, gin.H{"message": "密码已重置", "success": true})
}

func (a *App) verifyEmail(c *gin.Context) {
	user := getUser(c)
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if err := a.consumeEmailCode(user.Email, emailCodePurposeVerify, req.Code); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now().UTC()
	user.IsVerified = true
	user.EmailVerifiedAt = &now
	if err := a.db.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"is_verified":       true,
		"email_verified_at": now,
	}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "邮箱验证失败")
		return
	}
	a.cacheActiveUser(user)
	ok(c, gin.H{"message": "邮箱验证成功", "success": true})
}

func (a *App) me(c *gin.Context) {
	user := getUser(c)
	profile, _ := a.ensureProfile(user.ID)
	ok(c, gin.H{
		"user_id":           user.ID.String(),
		"username":          user.Username,
		"email":             user.Email,
		"display_name":      user.DisplayName,
		"avatar_url":        user.AvatarURL,
		"timezone":          profile.Timezone,
		"language":          profile.Language,
		"is_active":         user.IsActive,
		"is_verified":       user.IsVerified,
		"is_admin":          user.IsAdmin,
		"email_verified_at": user.EmailVerifiedAt,
		"created_at":        user.CreatedAt,
		"last_login_at":     user.LastLoginAt,
	})
}

func (a *App) updateProfile(c *gin.Context) {
	user := getUser(c)
	var req struct {
		DisplayName *string `json:"display_name"`
		AvatarURL   *string `json:"avatar_url"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if err := a.db.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"display_name": req.DisplayName,
		"avatar_url":   req.AvatarURL,
	}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "更新用户资料失败")
		return
	}
	user.DisplayName = req.DisplayName
	user.AvatarURL = req.AvatarURL
	a.cacheActiveUser(user)
	c.Set("user", user)
	a.me(c)
}

func (a *App) getSettings(c *gin.Context) {
	user := getUser(c)
	profile, _ := a.ensureProfile(user.ID)
	ok(c, gin.H{"user_id": user.ID.String(), "preferences": profile.Preferences, "notification_settings": profile.NotificationSettings, "updated_at": profile.UpdatedAt})
}

func (a *App) updateSettings(c *gin.Context) {
	user := getUser(c)
	profile, _ := a.ensureProfile(user.ID)
	var req map[string]any
	if !bindJSON(c, &req) {
		return
	}
	if value, exists := req["preferences"]; exists {
		profile.Preferences = jsonMap(value)
	}
	if value, exists := req["notification_settings"]; exists {
		profile.NotificationSettings = jsonMap(value)
	}
	if err := a.db.Save(&profile).Error; err != nil {
		fail(c, http.StatusInternalServerError, "更新用户设置失败")
		return
	}
	ok(c, gin.H{"user_id": user.ID.String(), "preferences": profile.Preferences, "notification_settings": profile.NotificationSettings, "updated_at": profile.UpdatedAt})
}

func (a *App) changePassword(c *gin.Context) {
	user := getUser(c)
	var req struct {
		CurrentPassword string `json:"current_password" binding:"required"`
		NewPassword     string `json:"new_password" binding:"required,min=8"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.CurrentPassword)) != nil {
		fail(c, http.StatusBadRequest, "当前密码错误")
		return
	}
	if req.ConfirmPassword != "" && req.NewPassword != req.ConfirmPassword {
		fail(c, http.StatusBadRequest, "新密码和确认密码不匹配")
		return
	}
	if err := validatePasswordPolicy(req.NewPassword); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}
	user.PasswordHash = string(hash)
	if err := a.db.Model(&models.User{}).Where("id = ?", user.ID).Update("password_hash", user.PasswordHash).Error; err != nil {
		fail(c, http.StatusInternalServerError, "密码修改失败")
		return
	}
	a.cacheActiveUser(user)
	ok(c, gin.H{"message": "密码修改成功", "success": true})
}

func (a *App) createProject(c *gin.Context) {
	user := getUser(c)
	var req map[string]any
	if !bindJSON(c, &req) {
		return
	}
	name, _ := req["name"].(string)
	if name == "" {
		fail(c, http.StatusBadRequest, "项目名称不能为空")
		return
	}
	project := models.Project{
		Name:             name,
		OwnerID:          user.ID,
		Status:           "draft",
		Workflow:         defaultFrameWorkflowJSON("draft"),
		Artifacts:        emptyJSONObjectJSON(),
		Subtitle:         emptyJSON(),
		VideoContext:     emptyJSON(),
		VideoConfig:      emptyJSON(),
		AIAnalysisScript: emptyJSON(),
		ProjectMetadata:  emptyJSON(),
	}
	if v, ok := req["description"].(string); ok {
		project.Description = &v
	}
	if v, ok := req["thumbnail_url"].(string); ok {
		project.ThumbnailURL = &v
	}
	if v, exists := req["preview_image_url"]; exists {
		project.PreviewImageURL = optionalStringPointer(v)
	}
	if v, exists := req["local_preview_image_key"]; exists {
		project.LocalPreviewImageKey = optionalStringPointer(v)
	}
	if v, exists := req["preview_image_storage"]; exists {
		project.PreviewImageStorage = optionalStringPointer(v)
	}
	if v, exists := req["local_preview_image_missing"].(bool); exists {
		project.LocalPreviewImageMissing = &v
	}
	if v, exists := req["metadata"]; exists {
		project.ProjectMetadata = jsonMap(v)
	} else if v, exists := req["project_metadata"]; exists {
		project.ProjectMetadata = jsonMap(v)
	}
	// New avatar projects use the fixed V2 workflow. Asset paths are exclusively
	// managed by the authenticated upload API, never accepted through metadata.
	var metadata map[string]any
	_ = json.Unmarshal(project.ProjectMetadata, &metadata)
	if metadata != nil {
		delete(metadata, "avatar_v2")
		if metadata["inference_mode"] == "avatar" || metadata["kind"] == "avatar_mvp" {
			metadata["avatar_v2"] = avatar.WorkflowInput{Revision: 1, Title: name, Style: defaultWorkflowStyle(a.avatarMVP.options.RuntimeRoot), Voice: sVoice(a), Ratio: "9:16", UseAvatar: true, Assets: []avatar.SourceAsset{}, Angles: []string{}}
		}
		project.ProjectMetadata = jsonMap(metadata)
	}
	if v, exists := req["workflow"]; exists {
		project.Workflow = updatedWorkflowJSON(project.Workflow, project.Status, v)
	}
	if v, exists := req["artifacts"]; exists {
		project.Artifacts = jsonMap(v)
	}
	if err := a.db.Create(&project).Error; err != nil {
		fail(c, http.StatusInternalServerError, "创建项目失败")
		return
	}
	created(c, projectPublicResponse(project))
}

func (a *App) listProjects(c *gin.Context) {
	user := getUser(c)
	page, limit := pageLimit(c)
	var total int64
	q := a.db.Model(&models.Project{}).Where("owner_id = ?", user.ID)
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	if keyword := c.Query("q"); keyword != "" {
		q = q.Where("name LIKE ?", "%"+keyword+"%")
	}
	if err := q.Count(&total).Error; err != nil {
		fail(c, http.StatusInternalServerError, "查询项目总数失败")
		return
	}
	var items []models.Project
	offset := queryInt(c, "offset", (page-1)*limit)
	if offset < 0 {
		offset = 0
	}
	sortBy := projectSortColumn(c.Query("sort_by"))
	sortOrder := projectSortOrder(c.Query("sort_order"))
	if err := q.Select(projectListColumns).Order(sortBy + " " + sortOrder).Offset(offset).Limit(limit).Find(&items).Error; err != nil {
		fail(c, http.StatusInternalServerError, "查询项目失败")
		return
	}
	publicItems := make([]gin.H, 0, len(items))
	for _, item := range items {
		publicItems = append(publicItems, projectListPublicResponse(item))
	}
	ok(c, gin.H{
		"projects": publicItems,
		"items":    publicItems,
		"total":    total,
		"page":     page,
		"limit":    limit,
		"has_more": int64(offset+limit) < total,
		"pages":    (total + int64(limit) - 1) / int64(limit),
	})
}

func projectSortColumn(sortBy string) string {
	switch sortBy {
	case "name":
		return "name"
	case "status":
		return "status"
	case "created_at":
		return "created_at"
	case "updated_at":
		return "updated_at"
	default:
		return "created_at"
	}
}

func projectSortOrder(sortOrder string) string {
	if strings.EqualFold(sortOrder, "asc") {
		return "ASC"
	}
	return "DESC"
}

func (a *App) ownedProject(c *gin.Context) (models.Project, bool) {
	user := getUser(c)
	id, okID := parseIDParam(c, "project_id")
	if !okID {
		return models.Project{}, false
	}
	var project models.Project
	result := a.db.Where("id = ? AND owner_id = ?", id, user.ID).Limit(1).Find(&project)
	if result.Error != nil {
		fail(c, http.StatusInternalServerError, "查询项目失败")
		return models.Project{}, false
	}
	if result.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "项目不存在")
		return models.Project{}, false
	}
	return project, true
}

func (a *App) getProject(c *gin.Context) {
	project, okProject := a.ownedProject(c)
	if okProject {
		ok(c, projectPublicResponse(project))
	}
}

func (a *App) updateProject(c *gin.Context) {
	project, okProject := a.ownedProject(c)
	if !okProject {
		return
	}
	var req map[string]any
	if !bindJSON(c, &req) {
		return
	}
	if a.rejectLegacyProjectWorkflowUpdates(c, project.ID, req) {
		return
	}
	originalStatus := project.Status
	originalMetadata := project.ProjectMetadata
	if v, ok := req["name"].(string); ok {
		project.Name = v
	}
	if v, ok := req["description"].(string); ok {
		project.Description = &v
	}
	if v, ok := req["thumbnail_url"].(string); ok {
		project.ThumbnailURL = &v
	}
	if v, exists := req["preview_image_url"]; exists {
		project.PreviewImageURL = optionalStringPointer(v)
	}
	if v, exists := req["local_preview_image_key"]; exists {
		project.LocalPreviewImageKey = optionalStringPointer(v)
	}
	if v, exists := req["preview_image_storage"]; exists {
		project.PreviewImageStorage = optionalStringPointer(v)
	}
	if v, exists := req["local_preview_image_missing"].(bool); exists {
		project.LocalPreviewImageMissing = &v
	}
	if v, ok := req["status"].(string); ok {
		project.Status = v
	}
	if v, exists := req["metadata"]; exists {
		project.ProjectMetadata = jsonMap(v)
	} else if v, exists := req["project_metadata"]; exists {
		project.ProjectMetadata = jsonMap(v)
	}
	project.ProjectMetadata = preserveAvatarWorkflowMetadata(originalMetadata, project.ProjectMetadata)
	if v, exists := req["workflow"]; exists {
		project.Workflow = updatedWorkflowJSON(project.Workflow, project.Status, v)
	}
	if v, exists := req["artifacts"]; exists {
		project.Artifacts = jsonMap(v)
	}
	// Share the draft lock with avatar.configure so a rename cannot race a
	// settings save and restore either an old title or an old draft revision.
	a.avatarMVP.mu.Lock()
	defer a.avatarMVP.mu.Unlock()
	if err := a.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := avatarTransactionLock(tx); err != nil {
			return err
		}
		var latest models.Project
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "name", "project_metadata").Where("id=?", project.ID).First(&latest).Error; err != nil {
			return err
		}
		project.ProjectMetadata = preserveAvatarWorkflowMetadata(latest.ProjectMetadata, project.ProjectMetadata)
		if _, renamed := req["name"].(string); !renamed {
			project.Name = latest.Name
		}
		if in, err := workflowInput(latest); err == nil && in.Title != project.Name {
			in.Title = project.Name
			in.Revision++
			var meta map[string]any
			if err := json.Unmarshal(project.ProjectMetadata, &meta); err != nil {
				return err
			}
			meta["avatar_v2"] = in
			project.ProjectMetadata = jsonMap(meta)
		}
		if err := tx.Save(&project).Error; err != nil {
			return err
		}
		if originalStatus != "completed" && project.Status == "completed" && projectHasFinalVideoArtifact(project.Artifacts) {
			return a.confirmProjectReservationsTx(tx, project.ID)
		}
		return nil
	}); err != nil {
		if strings.Contains(err.Error(), "Credits余额不足") {
			fail(c, http.StatusPaymentRequired, err.Error())
			return
		}
		fail(c, http.StatusInternalServerError, "更新项目失败")
		return
	}
	_ = a.db.First(&project, "id = ?", project.ID).Error
	ok(c, projectPublicResponse(project))
}

func (a *App) releaseProjectBillingReservations(c *gin.Context) {
	project, okProject := a.ownedProject(c)
	if !okProject {
		return
	}
	var req map[string]any
	if c.Request.ContentLength != 0 {
		if !bindJSON(c, &req) {
			return
		}
	}
	reason := "workflow_released"
	if value, ok := req["reason"].(string); ok && strings.TrimSpace(value) != "" {
		reason = strings.TrimSpace(value)
	}
	if err := a.releaseProjectReservations(project.ID, reason); err != nil {
		fail(c, http.StatusInternalServerError, "释放 Credits 预留失败")
		return
	}
	ok(c, gin.H{"success": true, "project_id": project.ID.String(), "status": "released"})
}

func projectHasFinalVideoArtifact(raw datatypes.JSON) bool {
	value := jsonValue(raw)
	root, ok := value.(map[string]any)
	if !ok {
		return false
	}
	finalVideo, ok := root["final_video"].(map[string]any)
	if !ok || len(finalVideo) == 0 {
		return false
	}
	for _, key := range []string{"key", "url", "storage_key"} {
		if text := strings.TrimSpace(fmt.Sprint(finalVideo[key])); text != "" && text != "<nil>" {
			return true
		}
	}
	return false
}

func (a *App) deleteProject(c *gin.Context) {
	project, okProject := a.ownedProject(c)
	if !okProject {
		return
	}
	if err := a.db.Delete(&project).Error; err != nil {
		fail(c, http.StatusInternalServerError, "删除项目失败")
		return
	}
	a.purgeAvatarProjectData(project.ID)
	ok(c, gin.H{"project_id": project.ID.String(), "deleted": true, "message": "项目已删除", "success": true})
}

func optionalStringPointer(value any) *string {
	text, ok := value.(string)
	if !ok {
		return nil
	}
	return &text
}

func (a *App) uploadProjectPreview(c *gin.Context) {
	project, okProject := a.ownedProject(c)
	if !okProject {
		return
	}
	var req struct {
		PreviewImageURL string `json:"preview_image_url"`
		URL             string `json:"url"`
	}
	if !bindJSON(c, &req) {
		return
	}
	url := req.PreviewImageURL
	if url == "" {
		url = req.URL
	}
	project.PreviewImageURL = &url
	_ = a.db.Save(&project).Error
	ok(c, gin.H{"project_id": project.ID.String(), "preview_image_url": url, "message": "预览图已更新", "success": true})
}

func (a *App) projectJSONField(field string) gin.HandlerFunc {
	return func(c *gin.Context) {
		project, okProject := a.ownedProject(c)
		if !okProject {
			return
		}
		switch field {
		case "subtitle":
			ok(c, gin.H{"project_id": project.ID.String(), "subtitle": a.workflowJSONValueWithMeta(project.ID, WorkflowStepSubtitle, project.Subtitle)})
		case "video_context":
			ok(c, gin.H{"project_id": project.ID.String(), "video_context": a.workflowVideoSourceValue(project)})
		case "video_config":
			ok(c, a.workflowJSONValueWithMeta(project.ID, WorkflowStepConfig, project.VideoConfig))
		default:
			ok(c, a.workflowJSONValueWithMeta(project.ID, WorkflowStepScript, project.AIAnalysisScript))
		}
	}
}
