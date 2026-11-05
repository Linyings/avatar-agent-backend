package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/providers"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const qwenVoiceSettingKey = "dubbing_qwen_enabled_voices"

func (a *App) enabledQwenVoices() ([]providers.DubbingTTSVoice, error) {
	var setting models.SystemSetting
	err := a.db.Where("key = ?", qwenVoiceSettingKey).First(&setting).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return providers.DefaultDubbingTTSVoices, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal(setting.Value, &ids); err != nil {
		return nil, err
	}
	selected := map[string]bool{}
	for _, id := range ids {
		selected[id] = true
	}
	voices := []providers.DubbingTTSVoice{}
	for _, voice := range providers.DubbingTTSVoices {
		if selected[voice.Value] {
			voices = append(voices, voice)
		}
	}
	return voices, nil
}

func (a *App) adminQwenVoices(c *gin.Context) {
	voices, err := a.enabledQwenVoices()
	if err != nil {
		fail(c, http.StatusInternalServerError, "读取千问音色设置失败")
		return
	}
	ids := []string{}
	for _, voice := range voices {
		ids = append(ids, voice.Value)
	}
	ok(c, gin.H{"model": a.dashscope.DubbingTTSModel(), "voices": providers.DubbingTTSVoices, "enabled_voices": ids})
}

func (a *App) adminSaveQwenVoices(c *gin.Context) {
	var req struct {
		EnabledVoices []string `json:"enabled_voices"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if len(req.EnabledVoices) < 2 || len(req.EnabledVoices) > len(providers.DubbingTTSVoices) {
		fail(c, http.StatusBadRequest, "请至少选择两个不同音色，供不同角色使用")
		return
	}
	seen := map[string]bool{}
	for _, id := range req.EnabledVoices {
		if !providers.ValidDubbingTTSVoice(id) || seen[id] {
			fail(c, http.StatusBadRequest, "存在不支持或重复的千问音色")
			return
		}
		seen[id] = true
	}
	user := getUser(c)
	setting := models.SystemSetting{Key: qwenVoiceSettingKey, Value: jsonMap(req.EnabledVoices), UpdatedBy: &user.ID}
	if err := a.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value", "updated_by", "updated_at"})}).Create(&setting).Error; err != nil {
		fail(c, http.StatusInternalServerError, "保存千问音色设置失败")
		return
	}
	a.adminQwenVoices(c)
}

func (a *App) adminPreviewQwenVoice(c *gin.Context) {
	var req struct {
		Voice string `json:"voice_uri"`
		Text  string `json:"text"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if !providers.ValidDubbingTTSVoice(req.Voice) || len([]rune(req.Text)) < 1 || len([]rune(req.Text)) > 200 {
		fail(c, http.StatusBadRequest, "请选择有效音色，试听文本限 1–200 字")
		return
	}
	audio, _, err := a.dashscope.DubbingTextToSpeech(c.Request.Context(), req.Text, req.Voice, "", 1)
	if err != nil {
		fail(c, http.StatusBadGateway, "千问试听失败，请稍后重试")
		return
	}
	c.Data(http.StatusOK, "audio/wav", audio)
}
