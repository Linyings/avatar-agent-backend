package server

import (
	"fmt"
	"os"
	"strings"

	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/media"
)

// defaultTalkingModel drives F06 footage from the narration voice itself; lab
// comparison (run lab-53490743/lab-fcce4099) showed zero audio offset versus
// 0.8–1.4 s for the general video model that re-voices the script.
const defaultTalkingModel = "kling-v2-ai-avatar-pro/image-audio-to-video"

type avatarTalkingProvider struct {
	avatar.Provider
	ModelID string
}

// talkingModelID is the configured talking-avatar model, or "" when F06 uses
// the general VIDEO_* model. AVATAR_TALKING_MODEL=off restores that path.
func talkingModelID() string {
	v := strings.TrimSpace(os.Getenv("AVATAR_TALKING_MODEL"))
	switch strings.ToLower(v) {
	case "":
		return defaultTalkingModel
	case "off", "none", "video":
		return ""
	}
	return v
}

// talkingClient uses the configured RunningHub video account and site, so
// uploads, orders and queries all go to the same place.
func (w *avatarWorkflowService) talkingClient() *avatar.RunningHub {
	o := w.app.avatarMVP.options.VideoDefaults()
	if os.Getenv("RUNNINGHUB_BASE_URL") == "" && o.Protocol == "runninghub" && o.BaseURL != "" && o.APIKey != "" {
		return avatar.NewRunningHub(o.BaseURL, o.APIKey)
	}
	return w.speechClient()
}

func (w *avatarWorkflowService) defaultTalking() (avatarTalkingProvider, error) {
	id := talkingModelID()
	if id == "" {
		return avatarTalkingProvider{}, nil
	}
	model, ok := media.Find(id)
	if !ok || model.Kind != media.KindAvatar {
		return avatarTalkingProvider{}, fmt.Errorf("数字人口型模型 %s 不在注册表中", id)
	}
	p, err := avatar.NewTalkingAvatar(w.talkingClient(), model)
	if err != nil {
		return avatarTalkingProvider{}, err
	}
	return avatarTalkingProvider{Provider: p, ModelID: model.ID}, nil
}

func (w *avatarWorkflowService) talkingProvider() (avatarTalkingProvider, error) {
	if w.talking == nil {
		return avatarTalkingProvider{}, nil
	}
	return w.talking()
}

// avatarFootageConfig identifies how talking footage is made. Changing the
// model invalidates cached footage so a new model never reuses another's clips.
func (w *avatarWorkflowService) avatarFootageConfig() string {
	if p, err := w.talkingProvider(); err == nil && p.ModelID != "" {
		return "talking:" + p.ModelID
	}
	return w.app.avatarMVP.options.VideoConfigID()
}

// runFootageConfig is the footage identity fixed when the run was created.
// Runs predating talking avatars keep the general video model.
func (w *avatarWorkflowService) runFootageConfig(s *avatar.WorkflowState) string {
	if v := s.ConfigSnapshot["avatar_footage"]; v != "" {
		return v
	}
	if len(s.ConfigSnapshot) > 0 {
		return w.app.avatarMVP.options.VideoConfigID()
	}
	return w.avatarFootageConfig()
}

// avatarOrderProvider resolves the API an order was or will be placed on. A
// persisted order keeps its own model even after the default changes, and a
// new order follows the model its run was created with.
func (w *avatarWorkflowService) avatarOrderProvider(s *avatar.WorkflowState, order *avatar.WorkflowStep) (avatar.Provider, string, error) {
	if order.Model == "" && order.ProviderTaskID == "" {
		if id, ok := strings.CutPrefix(w.runFootageConfig(s), "talking:"); ok {
			if current, err := w.talkingProvider(); err == nil && current.ModelID == id {
				return current.Provider, id, nil
			}
			order = &avatar.WorkflowStep{Model: id}
		}
	}
	if order.Model != "" && order.Model != w.app.avatarMVP.options.Model {
		if model, ok := media.Find(order.Model); ok && model.Kind == media.KindAvatar {
			p, err := avatar.NewTalkingAvatar(w.talkingClient(), model)
			return p, model.ID, err
		}
		return nil, "", fmt.Errorf("订单模型 %s 已不可用，原订单已保留", order.Model)
	}
	if w.app.avatarMVP.providerErr != nil {
		return nil, "", fmt.Errorf("人物视频服务配置错误")
	}
	return w.app.avatarMVP.provider, w.app.avatarMVP.options.Model, nil
}
