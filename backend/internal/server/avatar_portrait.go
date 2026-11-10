package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/narratoai/backend-go/internal/agent"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/media"
)

type portraitPolicy struct {
	Version   string `json:"version"`
	Model     string `json:"model"`
	Skill     string `json:"skill"`
	SkillHash string `json:"skill_hash"`
	SkillText string `json:"skill_text"`
}

func currentPortraitPolicy() portraitPolicy {
	doc, err := agent.ReadSkill("portrait-realism", "")
	if err != nil {
		panic(err)
	}
	model := strings.TrimSpace(os.Getenv("AVATAR_PORTRAIT_MODEL"))
	if model == "" {
		model = "seedream-v4.7/image-to-image"
	}
	return portraitPolicy{Version: "portrait/1", Model: model, Skill: doc.Name, SkillHash: avatar.Digest(doc.Content), SkillText: doc.Content}
}
func encodePortraitPolicy(p portraitPolicy) string { b, _ := json.Marshal(p); return string(b) }
func runPortraitPolicy(s *avatar.WorkflowState) (portraitPolicy, error) {
	var p portraitPolicy
	if raw := s.ConfigSnapshot["portrait_policy"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &p); err != nil || p.Version != "portrait/1" || p.SkillHash != avatar.Digest(p.SkillText) {
			return p, fmt.Errorf("人物质量策略快照无效")
		}
	}
	return p, nil
}
func (w *avatarWorkflowService) avatarQualityFingerprint(s *avatar.WorkflowState) string {
	return s.ConfigSnapshot["portrait_policy"]
}

// A frontal head does not establish gaze. Unknown never counts as a pass.
type portraitAssessment struct {
	Frontal  string `json:"frontal"`
	Gaze     string `json:"gaze"`
	Identity string `json:"identity"`
	Detail   string `json:"detail"`
	Framing  string `json:"framing"`
	Reason   string `json:"reason"`
}

func (q portraitAssessment) accepted() bool {
	return q.Frontal == "pass" && q.Gaze == "pass" && q.Identity == "pass" && q.Detail == "pass" && q.Framing == "pass"
}

func (w *avatarWorkflowService) portraitFrames(ctx context.Context, s *avatar.WorkflowState, source string, video bool) ([]string, error) {
	var out struct {
		Frames []string `json:"frames"`
	}
	if err := w.mediaStep(ctx, s, "portrait-frames", map[string]any{"source": source, "video": video}, &out); err != nil {
		return nil, err
	}
	if len(out.Frames) == 0 {
		return nil, fmt.Errorf("人物验收没有取得图像")
	}
	return out.Frames, nil
}
func (w *avatarWorkflowService) assessPortrait(ctx context.Context, s *avatar.WorkflowState, id, source, reference string, video bool) (portraitAssessment, error) {
	var q portraitAssessment
	path := filepath.Join(w.dir(s.ID), "portrait", avatar.Digest(id)+"-review.json")
	policy, err := runPortraitPolicy(s)
	if err != nil {
		return q, err
	}
	err = w.step(ctx, s, id, []any{policy, source, reference, video}, func(ctx context.Context, step *avatar.WorkflowStep) error {
		if data, e := os.ReadFile(path); e == nil && json.Unmarshal(data, &q) == nil {
			return nil
		}
		frames, e := w.portraitFrames(ctx, s, source, video)
		if e != nil {
			return e
		}
		if reference != "" {
			ref, e := w.portraitFrames(ctx, s, reference, false)
			if e != nil {
				return e
			}
			frames = append(ref[:1], frames...)
		}
		prompt := "检查人物图像，只返回 JSON：{frontal:pass|fail|unknown,gaze:pass|fail|unknown,identity:pass|fail|unknown,detail:pass|fail|unknown,framing:pass|fail|unknown,reason:中文原因}。逐项判断：头部正面；双眼看摄像机（不是镜头附近）；与第一张参考图身份、年龄、五官一致；皮肤自然无塑料感和明显纹理漂移；头顶双肩完整且有裁切余量。无法判断必须 unknown。没有独立参考时 identity 表示输入身份可辨认。视频后续图为时间采样，允许眨眼和轻微点头，持续侧脸或偏视判 fail。不得从静帧宣称口型同步通过。"
		if reference != "" {
			prompt += "有独立参考：第一张仅用于身份对比，不参与朝向、视线、质感、取景是否合格的判断；这些指标只检查后续目标图像。"
		}
		if e = w.model(ctx, s, step, prompt, map[string]any{"has_reference": reference != "", "video_samples": video, "skill": policy.Skill, "skill_hash": policy.SkillHash, "criteria": policy.SkillText}, frames, &q); e != nil {
			return e
		}
		return avatar.WriteJSON(path, q)
	})
	if err == nil {
		data, e := os.ReadFile(path)
		if e != nil {
			return q, e
		}
		err = json.Unmarshal(data, &q)
	}
	return q, err
}

func (w *avatarWorkflowService) preparePortrait(ctx context.Context, s *avatar.WorkflowState, source string) (string, error) {
	p, err := runPortraitPolicy(s)
	if err != nil {
		return "", err
	}
	if p.Version == "" {
		return source, nil
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	key := avatar.Digest([]any{hex.EncodeToString(sum[:]), p})
	if workflowRerunsNode(s, "F06") {
		key = avatar.Digest([]any{key, s.ID})
	}
	folder := filepath.Join(w.dir(s.ID), "portrait")
	if err = os.MkdirAll(folder, 0700); err != nil {
		return "", err
	}
	// Extensionless images let Upload detect JPEG/PNG bytes correctly.
	cache := filepath.Join(w.projectDir(s.ProjectID), "cache", "portraits", key)
	dest := filepath.Join(folder, key)
	if _, e := os.Stat(cache + ".accepted.json"); e == nil {
		if e = copyWorkflowFile(cache, dest); e == nil {
			return dest, nil
		}
	}
	q, err := w.assessPortrait(ctx, s, "F06/portrait-input-"+key, source, "", false)
	if err != nil {
		return "", err
	}
	if q.accepted() {
		if err = copyWorkflowFile(source, dest); err != nil {
			return "", err
		}
	} else {
		if q.Identity != "pass" {
			return "", fmt.Errorf("上传照片的人物身份无法清晰辨认，请提供清晰参考照片：%s", q.Reason)
		}
		model, ok := media.Find(p.Model)
		if !ok || model.Kind != media.KindI2I {
			return "", fmt.Errorf("正脸参考图模型未配置为有效图生图模型")
		}
		err = w.step(ctx, s, "F06/portrait-generate-"+key, []any{key, p.Model}, func(ctx context.Context, order *avatar.WorkflowStep) error {
			if _, e := os.Stat(dest); e == nil {
				return nil
			}
			client := w.talkingClient()
			if order.ProviderTaskID == "" {
				if order.Submission == "submitting" || order.Submission == "unknown" {
					return fmt.Errorf("正脸参考图订单状态待确认，禁止重复提交")
				}
				if order.Submission == "terminal_failed" {
					return fmt.Errorf("正脸参考图订单失败，请明确重做人物素材")
				}
				url, e := client.Upload(ctx, source)
				if e != nil {
					return e
				}
				prompt := "将参考图中的同一人物准备为正脸讲解参考照片。纠正侧脸姿态，头部正面，双眼直视镜头；保留人物身份与年龄，不复制原图侧脸姿态。普通矩形全画幅，腰部以上中景，完整头顶双肩和取景余量，无文字。\n" + p.SkillText
				body, e := model.Body(media.Request{Prompt: prompt, Images: []string{url}, Ratio: "3:4", Resolution: "1k"})
				if e != nil {
					return e
				}
				order.Model = p.Model
				order.GenerationPrompt = prompt
				order.PromptInput, _ = json.Marshal(map[string]any{"skill": p.Skill, "skill_hash": p.SkillHash, "reference_sha256": hex.EncodeToString(sum[:]), "policy": p.Version})
				if e = w.beforeSubmit(s, order.ID); e != nil {
					return e
				}
				result, e := client.SubmitMedia(ctx, model.Path, body)
				if e != nil {
					order.Submission = "rejected"
					if avatar.SubmissionUncertain(e) {
						order.Submission = "unknown"
					}
					_ = w.save(s)
					return fmt.Errorf("正脸参考图提交失败，订单已保留")
				}
				order.ProviderTaskID = result.TaskID
				order.Submission = "submitted"
				if e = w.save(s); e != nil {
					return e
				}
			}
			result, e := w.poll(ctx, s, order, client.QueryMedia)
			if e != nil {
				return e
			}
			url := result.MediaURL("image")
			if url == "" {
				return fmt.Errorf("正脸参考图订单未返回图片")
			}
			return client.Download(ctx, url, dest)
		})
		if err != nil {
			return "", err
		}
		q, err = w.assessPortrait(ctx, s, "F06/portrait-review-"+key, dest, source, false)
		if err != nil {
			return "", err
		}
		if !q.accepted() {
			return "", fmt.Errorf("正脸参考图未通过朝向、视线、身份或质感验收：%s；请更换参考照片或明确重做", q.Reason)
		}
	}
	if err = os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		return "", err
	}
	if err = copyWorkflowFile(dest, cache); err != nil {
		return "", err
	}
	if err = avatar.WriteJSON(cache+".accepted.json", map[string]any{"policy": p, "assessment": q, "source_sha256": hex.EncodeToString(sum[:])}); err != nil {
		return "", err
	}
	return dest, nil
}
