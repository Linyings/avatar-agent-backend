package server

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/narratoai/backend-go/internal/avatar"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type workflowVoice struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Model  string `json:"model"`
	Sample string `json:"sample,omitempty"`
}

func (w *avatarWorkflowService) voices() []workflowVoice {
	model := sTTSModel(w.app)
	ids := strings.Split(os.Getenv("AVATAR_TTS_VOICES"), ",")
	if len(ids) == 1 && ids[0] == "" {
		ids = []string{sVoice(w.app)}
		if w.app.cfg.TTSModel == "doubao-seed-tts-2.0" {
			ids = append(ids, "zh_female_vv_uranus_bigtts")
		} else {
			ids = append(ids, "Serena")
		}
	}
	out := []workflowVoice{}
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			v := workflowVoice{ID: id, Name: id, Model: model}
			if id == "zh_male_shaonianzixin_uranus_bigtts" {
				v.Name = "少年自信 · 男声"
			}
			if id == "zh_female_vv_uranus_bigtts" {
				v.Name = "Vivi 2.0 · 女声"
			}
			if _, err := os.Stat(w.voiceSamplePath(id)); err == nil {
				v.Sample = "/api/v1/avatar-mvp/catalog/voices/" + id + "/sample"
			}
			out = append(out, v)
			seen[id] = true
		}
	}
	return out
}
func (w *avatarWorkflowService) voiceSamplePath(id string) string {
	return filepath.Join(w.root(), "catalog", avatar.Digest([]string{sTTSModel(w.app), id})+".wav")
}
func (a *App) workflowVoiceSample(c *gin.Context) {
	voice := c.Param("voice")
	found := false
	for _, v := range a.avatarWorkflow.voices() {
		if v.ID == voice {
			found = true
		}
	}
	if !found {
		fail(c, 404, "音色不存在")
		return
	}
	f, err := os.Open(a.avatarWorkflow.voiceSamplePath(voice))
	if err != nil {
		fail(c, 404, "尚无该音色的真实试听，首次分段配音成功后可用")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		fail(c, 404, "试听不可用")
		return
	}
	c.Header("Content-Type", "audio/wav")
	c.Header("Cache-Control", "private, max-age=3600")
	http.ServeContent(c.Writer, c.Request, "voice.wav", info.ModTime(), f)
}
func (w *avatarWorkflowService) validateInput(in *avatar.WorkflowInput, run bool) error {
	if in.SubtitleScale != 0 && (in.SubtitleScale < .75 || in.SubtitleScale > 1.5) {
		return fmt.Errorf("字幕缩放范围为0.75–1.5")
	}
	if in.AvatarPosition != "" && in.AvatarPosition != "left" && in.AvatarPosition != "right" {
		return fmt.Errorf("人物位置只能选择左或右")
	}
	if len(in.SubtitleEdits) > 100 {
		return fmt.Errorf("字幕修订过多")
	}
	for id, text := range in.SubtitleEdits {
		if !avatar.SafeID.MatchString(id) || len([]rune(text)) > 2000 {
			return fmt.Errorf("字幕修订无效")
		}
	}
	if in.Style == "" {
		in.Style = defaultWorkflowStyle(w.app.avatarMVP.options.RuntimeRoot)
	}
	if in.Ratio == "" {
		in.Ratio = "9:16"
	}
	if in.Voice == "" {
		in.Voice = sVoice(w.app)
	}
	if len([]rune(in.Script)) > 10000 || len([]rune(in.Topic)) > 2000 || len([]rune(in.Title)) > 100 || len([]rune(in.Direction)) > 2000 {
		return fmt.Errorf("输入过长")
	}
	style, foundStyle := findWorkflowStyle(w.app.avatarMVP.options.RuntimeRoot, in.Style)
	if !foundStyle {
		return fmt.Errorf("请选择已发布的动效风格")
	}
	if run && !style.Legacy && !style.Published {
		return fmt.Errorf("该动效风格尚未准备完成，请选择已发布的风格")
	}
	if in.Ratio != "9:16" && in.Ratio != "16:9" && in.Ratio != "1:1" {
		return fmt.Errorf("不支持的画面比例")
	}
	found := false
	for _, v := range w.voices() {
		if v.ID == in.Voice {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("音色不在服务端可用清单")
	}
	for _, angle := range in.Angles {
		if angle != "正面" && angle != "左前方20度" && angle != "右前方20度" {
			return fmt.Errorf("机位不在允许清单")
		}
	}
	if len(in.Angles) > 0 && strings.TrimSpace(in.Direction) == "" {
		return fmt.Errorf("多角度需要填写明确的制作要求")
	}
	if !run {
		return nil
	}
	if strings.TrimSpace(in.Topic) == "" && strings.TrimSpace(in.Script) == "" {
		return fmt.Errorf("主题或文案至少填写一项")
	}
	if in.UseAvatar && in.Photo == "" {
		return fmt.Errorf("数字人需要上传人物照片")
	}
	if w.app.cfg.OpenAIFlashAPIKey == "" || w.app.cfg.OpenAIFlashModel == "" {
		return fmt.Errorf("文本推理模型未配置")
	}
	if in.UseAvatar {
		if err := w.app.avatarMVP.options.ValidateVideo(); err != nil {
			return err
		}
	}
	if w.app.cfg.TTSModel == "doubao-seed-tts-2.0" {
		if os.Getenv("RUNNINGHUB_API_KEY") == "" && w.app.avatarMVP.options.APIKey == "" {
			return fmt.Errorf("配音服务未配置")
		}
	} else if w.app.cfg.DashScopeAPIKey == "" {
		return fmt.Errorf("TTS服务未配置")
	}
	for _, s := range in.Assets {
		if s.Kind == "video" && !s.Excluded && !w.visionConfigured() {
			return fmt.Errorf("上传视频路线需要配置视频理解模型（QWEN_VISION_MODEL / DASHSCOPE_API_KEY），无视频路线不受影响")
		}
	}
	return nil
}
func (w *avatarWorkflowService) visionConfigured() bool {
	return w.app.cfg.MovieVisionConfigured() || (os.Getenv("AVATAR_VISION_USE_FLASH") == "true" && w.app.cfg.OpenAIFlashConfigured())
}
func (a *App) workflowCatalog(c *gin.Context) {
	root := a.avatarMVP.options.RuntimeRoot
	styles, err := loadWorkflowStyles(root)
	if err != nil {
		fail(c, 503, "动效风格目录不可用")
		return
	}
	ok(c, gin.H{"version": avatar.WorkflowVersion, "styles": styles, "voices": a.avatarWorkflow.voices(), "ratios": []string{"9:16", "16:9", "1:1"}, "max_duration": 90, "video_understanding": a.avatarWorkflow.visionConfigured(), "video_provider": a.avatarMVP.options.Provider, "video_model": a.avatarMVP.options.Model, "default_voice": sVoice(a)})
}
func (a *App) workflowStylePreview(c *gin.Context) {
	style := c.Param("style")
	meta, found := findWorkflowStyle(a.avatarMVP.options.RuntimeRoot, style)
	if !found {
		fail(c, 404, "风格不存在")
		return
	}
	path := filepath.Join(a.avatarMVP.options.RuntimeRoot, "catalog", style+".mp4")
	mediaType := "video/mp4"
	if strings.HasSuffix(c.FullPath(), "/poster") {
		path = filepath.Join(a.avatarMVP.options.RuntimeRoot, "catalog", style+".jpg")
		mediaType = "image/jpeg"
	}
	if !meta.Legacy {
		switch {
		case strings.HasSuffix(c.FullPath(), "/reference"):
			path = filepath.Join(a.avatarMVP.options.RuntimeRoot, "catalog", style+"-reference.mp4")
		case strings.HasSuffix(c.FullPath(), "/prompt"):
			path = filepath.Join(a.avatarMVP.options.RuntimeRoot, "templates", style, "original-prompt.md")
			mediaType = "text/plain; charset=utf-8"
		case strings.HasSuffix(c.FullPath(), "/source"):
			path = filepath.Join(a.avatarMVP.options.RuntimeRoot, "catalog", style+"-source.zip")
			mediaType = "application/zip"
			c.Header("Content-Disposition", `attachment; filename="`+style+`-source.zip"`)
		}
	} else if strings.HasSuffix(c.FullPath(), "/reference") || strings.HasSuffix(c.FullPath(), "/source") || strings.HasSuffix(c.FullPath(), "/prompt") {
		fail(c, 404, "旧版仅提供简化样例")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		fail(c, 404, "动态预览尚未生成")
		return
	}
	defer f.Close()
	info, _ := f.Stat()
	c.Header("Content-Type", mediaType)
	c.Header("Cache-Control", "private, no-cache")
	c.Header("X-Content-Type-Options", "nosniff")
	http.ServeContent(c.Writer, c.Request, filepath.Base(path), info.ModTime(), f)
}
