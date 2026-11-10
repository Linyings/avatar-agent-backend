package server

import (
	"encoding/hex"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/narratoai/backend-go/internal/avatar"
	"io"
	"net/http"
	"net/url"
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

type workflowVoiceModel struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	DefaultVoice  string          `json:"default_voice"`
	SupportsSpeed bool            `json:"supports_speed"`
	Voices        []workflowVoice `json:"voices"`
}

// voiceModel is the TTS model a project is synthesized with.
func (w *avatarWorkflowService) voiceModel(in avatar.WorkflowInput) string {
	if in.VoiceModel != "" {
		return in.VoiceModel
	}
	return sTTSModel(w.app)
}

// voiceModels lists every selectable RunningHub model, deployment model first.
// Selection needs the RunningHub speech key, so other providers offer only theirs.
func (w *avatarWorkflowService) voiceModels() []workflowVoiceModel {
	current := sTTSModel(w.app)
	out := []workflowVoiceModel{}
	add := func(m avatar.SpeechModel) {
		out = append(out, workflowVoiceModel{ID: m.ID, Name: m.Name, DefaultVoice: m.DefaultVoice, SupportsSpeed: m.SupportsSpeed, Voices: w.voicesFor(m.ID)})
	}
	if m, ok := avatar.FindSpeechModel(current); ok {
		add(m)
	} else {
		out = append(out, workflowVoiceModel{ID: current, Name: current, DefaultVoice: sVoice(w.app), Voices: w.voices()})
	}
	if !w.runningHubSpeech() {
		return out
	}
	for _, m := range avatar.SpeechModels() {
		if m.ID != out[0].ID {
			add(m)
		}
	}
	return out
}

// voicesFor lists one model's voices; the deployment model keeps its legacy list.
func (w *avatarWorkflowService) voicesFor(model string) []workflowVoice {
	if model == "" || model == sTTSModel(w.app) {
		return w.voices()
	}
	m, ok := avatar.FindSpeechModel(model)
	if !ok {
		return nil
	}
	out := make([]workflowVoice, 0, len(m.Voices))
	for _, v := range m.Voices {
		item := workflowVoice{ID: v.ID, Name: v.Name, Model: m.ID}
		if w.existingVoiceSample(m.ID, v.ID) != "" {
			item.Sample = "/api/v1/avatar-mvp/catalog/voices/" + url.PathEscape(v.ID) + "/sample?model=" + url.QueryEscape(m.ID)
		}
		out = append(out, item)
	}
	return out
}

func (w *avatarWorkflowService) voiceSamplePathFor(model, id string) string {
	return filepath.Join(w.root(), "catalog", avatar.Digest([]string{model, id})+".wav")
}

// existingVoiceSample finds a recorded preview of the voice, falling back to a
// sibling version of the same model family that offers the same preset voice.
func (w *avatarWorkflowService) existingVoiceSample(model, id string) string {
	candidates := avatar.SpeechModelFamily(model)
	if len(candidates) == 0 {
		candidates = []string{model}
	}
	for _, m := range candidates {
		if found, ok := avatar.FindSpeechModel(m); ok && m != model && !found.HasVoice(id) {
			continue
		}
		if path := w.voiceSamplePathFor(m, id); func() bool { _, err := os.Stat(path); return err == nil }() {
			return path
		}
	}
	return ""
}

func (w *avatarWorkflowService) voices() []workflowVoice {
	model := sTTSModel(w.app)
	system := avatar.SystemSpeechVoices(model)
	names := map[string]string{}
	ids := []string{sVoice(w.app)}
	for _, v := range system {
		names[v.ID] = v.Name
		ids = append(ids, v.ID)
	}
	if configured := strings.TrimSpace(os.Getenv("AVATAR_TTS_VOICES")); configured != "" {
		ids = append(ids, strings.Split(configured, ",")...)
	} else {
		if w.app.cfg.TTSModel == "doubao-seed-tts-2.0" {
			ids = append(ids, "zh_female_vv_uranus_bigtts")
		} else if len(system) == 0 {
			ids = append(ids, "Serena")
		}
	}
	out := []workflowVoice{}
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if avatar.SpeechVoiceIncompatible(model, id) {
			continue
		}
		if id != "" && !seen[id] {
			v := workflowVoice{ID: id, Name: id, Model: model}
			if name := names[id]; name != "" {
				v.Name = name
			}
			if w.existingVoiceSample(model, id) != "" {
				v.Sample = "/api/v1/avatar-mvp/catalog/voices/" + url.PathEscape(id) + "/sample"
			}
			out = append(out, v)
			seen[id] = true
		}
	}
	return out
}
func (w *avatarWorkflowService) voiceSamplePath(id string) string {
	return w.voiceSamplePathFor(sTTSModel(w.app), id)
}
func (a *App) workflowVoiceSample(c *gin.Context) {
	voice := c.Param("voice")
	model := c.Query("model")
	if model == "" {
		model = sTTSModel(a)
	}
	found := false
	for _, v := range a.avatarWorkflow.voicesFor(model) {
		if v.ID == voice {
			found = true
		}
	}
	if !found {
		fail(c, 404, "音色不存在")
		return
	}
	path := a.avatarWorkflow.existingVoiceSample(model, voice)
	if path == "" {
		path = a.avatarWorkflow.voiceSamplePathFor(model, voice)
	}
	f, err := os.Open(path)
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
	// Samples keep the provider's original encoding (Doubao WAV, MiniMax MP3).
	head := make([]byte, 12)
	n, _ := f.Read(head)
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		fail(c, 404, "试听不可用")
		return
	}
	mediaType := "audio/wav"
	if n >= 3 && (string(head[:3]) == "ID3" || (head[0] == 0xff && head[1]&0xe0 == 0xe0)) {
		mediaType = "audio/mpeg"
	}
	c.Header("Content-Type", mediaType)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, max-age=3600")
	http.ServeContent(c.Writer, c.Request, "voice.wav", info.ModTime(), f)
}
func (w *avatarWorkflowService) validateInput(in *avatar.WorkflowInput, run bool) error {
	if err := avatar.ValidateVoiceSpeed(in.VoiceSpeed); err != nil {
		return err
	}
	if len(in.SegmentVoiceSpeeds) > 40 {
		return fmt.Errorf("单段语速设置过多")
	}
	for id, speed := range in.SegmentVoiceSpeeds {
		if !avatar.SafeID.MatchString(id) {
			return fmt.Errorf("配音段落 ID 无效")
		}
		if err := avatar.ValidateVoiceSpeed(speed); err != nil {
			return err
		}
	}
	in.VoiceModel = strings.TrimSpace(in.VoiceModel)
	if in.VoiceModel == sTTSModel(w.app) {
		// The deployment model is the implicit default; keep drafts portable.
		in.VoiceModel = ""
	}
	if in.VoiceModel != "" {
		if _, ok := avatar.FindSpeechModel(in.VoiceModel); !ok || !w.runningHubSpeech() {
			return fmt.Errorf("配音模型不在服务端可用清单")
		}
	}
	model := w.voiceModel(*in)
	speedSupported := strings.HasPrefix(model, "minimax/")
	if m, ok := avatar.FindSpeechModel(model); ok {
		speedSupported = m.SupportsSpeed
	}
	if w.runningHubSpeech() && !speedSupported {
		if avatar.EffectiveVoiceSpeed(*in, "") != 1 {
			return fmt.Errorf("当前配音模型尚未接入语速参数，请使用原速或选择支持语速的模型")
		}
		for id := range in.SegmentVoiceSpeeds {
			if avatar.EffectiveVoiceSpeed(*in, id) != 1 {
				return fmt.Errorf("当前配音模型尚未接入单段语速参数")
			}
		}
	}
	if len(in.ReferenceLinks) > 20 {
		return fmt.Errorf("最多添加20个网页链接")
	}
	links := make([]string, 0, len(in.ReferenceLinks))
	seen := map[string]bool{}
	for _, raw := range in.ReferenceLinks {
		value := strings.TrimSpace(raw)
		u, err := url.Parse(value)
		if err != nil || len(value) > 2048 || u == nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || strings.ContainsAny(u.Host, " \t\r\n\\") {
			return fmt.Errorf("请提供有效的http://或https://网页链接，不支持带账号密码的地址")
		}
		if !seen[value] {
			links = append(links, value)
			seen[value] = true
		}
	}
	in.ReferenceLinks = links
	if in.SubtitleColor != "" {
		if len(in.SubtitleColor) != 7 || in.SubtitleColor[0] != '#' {
			return fmt.Errorf("字幕颜色必须是 #RRGGBB 格式")
		}
		if _, err := hex.DecodeString(in.SubtitleColor[1:]); err != nil {
			return fmt.Errorf("字幕颜色必须是 #RRGGBB 格式")
		}
	}
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
	in.Voice = strings.TrimSpace(in.Voice)
	if in.VoiceModel == "" {
		// Legacy repair: drafts saved for another deployment model fall back.
		// Unknown IDs are not guessed; they fail the catalog check below.
		if in.Voice == "" || (avatar.SpeechVoiceIncompatible(model, in.Voice) && avatar.SpeechVoiceKnown(in.Voice)) {
			in.Voice = sVoice(w.app)
		}
	} else if in.Voice == "" {
		in.Voice = avatar.DefaultSpeechVoice(model)
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
	for _, v := range w.voicesFor(model) {
		if v.ID == in.Voice {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("音色不在所选配音模型的可用清单")
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
	return w.validatePackageInput(in, "F12")
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
	ok(c, gin.H{"version": avatar.WorkflowVersion, "styles": styles, "voices": a.avatarWorkflow.voices(), "ratios": []string{"9:16", "16:9", "1:1"}, "video_understanding": a.avatarWorkflow.visionConfigured(), "video_provider": a.avatarMVP.options.Provider, "video_model": a.avatarMVP.options.Model, "default_voice": sVoice(a), "voice_models": a.avatarWorkflow.voiceModels(), "default_voice_model": sTTSModel(a)})
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
	if strings.HasSuffix(c.FullPath(), "/card-preview") {
		path = filepath.Join(a.avatarMVP.options.RuntimeRoot, "catalog", style+"-card.mp4")
	}
	if strings.HasSuffix(c.FullPath(), "/card-poster") {
		path = filepath.Join(a.avatarMVP.options.RuntimeRoot, "catalog", style+"-card.jpg")
		mediaType = "image/jpeg"
	}
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
