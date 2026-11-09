package server

import (
	"fmt"
	"github.com/narratoai/backend-go/internal/avatar"
	"os"
	"strings"
)

func (w *avatarWorkflowService) runningHubSpeech() bool {
	return w.app.cfg.TTSModel == "doubao-seed-tts-2.0" || strings.HasPrefix(w.app.cfg.TTSModel, "minimax/")
}
func (w *avatarWorkflowService) speechConfigured() error {
	if w.runningHubSpeech() {
		if os.Getenv("RUNNINGHUB_API_KEY") == "" && (w.app.avatarMVP.options.Protocol != "runninghub" || w.app.avatarMVP.options.APIKey == "") {
			return fmt.Errorf("配音服务未配置")
		}
	} else if w.app.cfg.DashScopeAPIKey == "" || w.app.cfg.QwenTTSModel == "" {
		return fmt.Errorf("配音服务未配置")
	}
	return nil
}
func (w *avatarWorkflowService) validatePackageInput(in *avatar.WorkflowInput, target string) error {
	if err := w.validateInput(in, false); err != nil {
		return err
	}
	if strings.TrimSpace(in.Topic) == "" && strings.TrimSpace(in.Script) == "" {
		return fmt.Errorf("请先填写主题或文案")
	}
	if !isKnownWorkflowNode(target) {
		return fmt.Errorf("制作节点不存在")
	}
	for _, s := range in.Assets {
		if s.Kind == "video" && !s.Excluded && !w.visionConfigured() {
			return fmt.Errorf("视频理解模型未配置，请先移除视频素材或配置服务")
		}
	}
	if target >= "F01.2" && !w.app.cfg.OpenAIFlashConfigured() && !w.app.cfg.AgentConfigured() {
		return fmt.Errorf("文本模型未配置")
	}
	if target >= "F04" {
		if err := w.speechConfigured(); err != nil {
			return err
		}
	}
	if target >= "F06" && in.UseAvatar {
		if in.Photo == "" {
			return fmt.Errorf("请先上传人物照片")
		}
		if err := w.app.avatarMVP.options.ValidateVideo(); err != nil {
			return err
		}
	}
	if style, ok := findWorkflowStyle(w.app.avatarMVP.options.RuntimeRoot, in.Style); !ok || (!style.Legacy && !style.Published) {
		return fmt.Errorf("所选风格尚未发布")
	}
	return nil
}
