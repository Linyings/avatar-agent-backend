package avatar

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/narratoai/backend-go/internal/media"
)

// TalkingAvatar drives a registered image+audio model. Soundtrack agreement
// verifies audio preservation, not visual lip-sync accuracy.
type TalkingAvatar struct {
	transport *RunningHub
	model     media.Model
}

func NewTalkingAvatar(transport *RunningHub, model media.Model) (*TalkingAvatar, error) {
	if model.Kind != media.KindAvatar {
		return nil, fmt.Errorf("%s 不是数字人口型模型", model.ID)
	}
	return &TalkingAvatar{transport: transport, model: model}, nil
}

func (p *TalkingAvatar) Model() media.Model { return p.model }

func (p *TalkingAvatar) Upload(ctx context.Context, path string) (string, error) {
	return p.transport.Upload(ctx, path)
}

// Submit uses only the portrait, the voice and the prompt; duration, ratio and
// native-audio switches belong to general video models and are ignored.
func (p *TalkingAvatar) Submit(ctx context.Context, in GenerationRequest) (ProviderResult, error) {
	if len(in.ImageURLs) == 0 || len(in.AudioURLs) == 0 {
		return ProviderResult{}, fmt.Errorf("数字人口型需要人物图片和驱动配音")
	}
	body, err := p.model.Body(media.Request{Prompt: in.Prompt, Images: in.ImageURLs[:1], Audio: in.AudioURLs[0]})
	if err != nil {
		return ProviderResult{}, err
	}
	return p.transport.SubmitMedia(ctx, p.model.Path, body)
}

func (p *TalkingAvatar) Query(ctx context.Context, id string) (ProviderResult, error) {
	return p.transport.QueryMedia(ctx, id)
}

func (p *TalkingAvatar) Download(ctx context.Context, url, path string) error {
	return p.transport.Download(ctx, url, path)
}

// TalkingPrompt is the provider-facing direction for talking-avatar models:
// the identity comes from the portrait and the speech from the audio, so only
// performance and framing are described.
func TalkingPrompt(direction string) string {
	base := "同一位人物正对镜头，双眼直视镜头，像面对粉丝自然讲解；头部正面、视线目标为摄像机镜头；自然眨眼，轻微点头和表情，不转头、不侧脸。保持参考图的身份、年龄、服装、背景和光线。腰部以上中景，完整保留头顶与双肩，脸高约占画面五分之一，四周留取景余量。固定机位、固定焦距、固定拍摄距离，整段人物大小稳定，无镜头切换。无字幕、无文字。"
	if extra := strings.TrimSpace(CleanTalkingDirection(direction)); extra != "" {
		return base + "补充要求：" + extra + "。"
	}
	return base
}

// Pose/gaze/framing are backend-owned constraints. Free-form shot direction
// may supply appearance and environment, never a competing camera or gaze.
var talkingPoseClause = regexp.MustCompile(`(?i)面部|脸|头部|身体.*(?:朝|转)|看向|视线|眼神|注视|目光|凝视|朝向|侧身|转身|偏左|偏右|景别|近景|特写|取景|机位|焦距|拍摄距离|\d+\s*:\s*\d+|\b(?:face|head|gaze|look|eyes|profile|frontal|camera|framing|close.?up)\b`)

func CleanTalkingDirection(direction string) string {
	var kept []string
	for _, clause := range avatarDirectionClauses.Split(CleanAvatarDirection(direction), -1) {
		if clause = strings.TrimSpace(clause); clause != "" && !talkingPoseClause.MatchString(clause) {
			kept = append(kept, clause)
		}
	}
	return strings.Join(kept, "。")
}
