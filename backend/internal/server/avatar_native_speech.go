package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/narratoai/backend-go/internal/avatar"
)

func (w *avatarWorkflowService) nativeSpeechConfigured() error {
	o := w.app.avatarMVP.options.VideoDefaults()
	if o.Protocol != "runninghub" || o.Model != avatar.NativeSpeechModel || o.GenerationPath != avatar.SeedancePath {
		return fmt.Errorf("原声口播需要配置 RunningHub Seedance 2.5 多模态视频接口")
	}
	if err := o.ValidateVideo(); err != nil {
		return err
	}
	return w.app.avatarMVP.providerErr
}

func nativeAsset(in avatar.WorkflowInput, id string) (avatar.SourceAsset, bool) {
	for _, a := range in.Assets {
		if a.ID == id && !a.Excluded {
			return a, true
		}
	}
	return avatar.SourceAsset{}, false
}

func (w *avatarWorkflowService) nativeSpeechFingerprint(s *avatar.WorkflowState, seg avatar.Segment) string {
	photo, _ := nativeAsset(s.Input, s.Input.Photo)
	voice, _ := nativeAsset(s.Input, s.Input.VoiceReference)
	values := []any{"seedance-native/2", seg.Text, seg.NativePrompt, seg.NativeRevision, photo.SHA256, photo.ID, voice.SHA256, voice.ID, s.Input.Direction, s.Input.Ratio, s.Input.UseAvatar, s.ConfigSnapshot["video"]}
	// Changes to the default voice anchor must also invalidate later segments.
	if voice.ID == "" && len(s.Segments) > 0 && seg.ID != s.Segments[0].ID {
		values = append(values, s.Segments[0].Fingerprint)
	}
	return avatar.Digest(values)
}

func (w *avatarWorkflowService) nativeReference(ctx context.Context, s *avatar.WorkflowState, seg avatar.Segment, pieces []string) (string, error) {
	source := ""
	if s.Input.VoiceReference != "" {
		a, ok := nativeAsset(s.Input, s.Input.VoiceReference)
		if !ok || a.Kind != "audio" {
			return "", fmt.Errorf("参考音色文件不存在，请重新选择已上传音频")
		}
		var err error
		source, err = safeWorkflowFile(w.projectDir(s.ProjectID), a.Path)
		if err != nil {
			return "", err
		}
	} else if len(s.Segments) > 0 && s.Segments[0].ID != seg.ID {
		if s.Segments[0].NativeVideo == "" {
			return "", fmt.Errorf("首段原声尚未就绪，不能继续生成依赖其音色的段落")
		}
		source = filepath.Join(w.dir(s.ID), filepath.FromSlash(s.Segments[0].NativeVideo))
	} else if len(pieces) > 0 {
		source = filepath.Join(w.dir(s.ID), filepath.FromSlash(pieces[0]))
	}
	if source == "" {
		return "", nil
	}
	var out struct {
		Path string `json:"path"`
	}
	if err := w.mediaStep(ctx, s, "voice_reference", map[string]any{"source": source}, &out); err != nil {
		return "", err
	}
	return safeWorkflowFile(w.dir(s.ID), out.Path)
}

func (w *avatarWorkflowService) produceNativeSpeech(ctx context.Context, s *avatar.WorkflowState, seg *avatar.Segment, parent *avatar.WorkflowStep) error {
	fingerprint := w.nativeSpeechFingerprint(s, *seg)
	folder := filepath.Join(w.dir(s.ID), seg.ID)
	if err := os.MkdirAll(folder, 0700); err != nil {
		return err
	}
	cache := filepath.Join(w.projectDir(s.ProjectID), "cache", "native-speech", fingerprint)
	files := []string{"native.mp4", "audio.wav", "timestamps.json", "alignment-cache.json"}
	var cached struct {
		Duration float64 `json:"duration"`
	}
	if raw, err := os.ReadFile(filepath.Join(cache, "result.json")); err == nil && json.Unmarshal(raw, &cached) == nil && cached.Duration > 0 {
		valid := true
		for _, name := range files {
			if stat, e := os.Stat(filepath.Join(cache, name)); e != nil || stat.Size() == 0 {
				valid = false
			}
		}
		if valid {
			for _, name := range files {
				if err := copyWorkflowFile(filepath.Join(cache, name), filepath.Join(folder, name)); err != nil {
					return err
				}
			}
			seg.Audio, seg.NativeVideo, seg.Duration, seg.Fingerprint = seg.ID+"/audio.wav", seg.ID+"/native.mp4", cached.Duration, fingerprint
			parent.CostStatus = "reused"
			w.nativeArtifacts(s, *seg)
			return nil
		}
	}
	if s.ConfigSnapshot["video"] != "" && s.ConfigSnapshot["video"] != w.app.avatarMVP.options.VideoConfigID() {
		return fmt.Errorf("原声视频服务配置已变化，请恢复原配置后查询已有订单")
	}
	provider := w.app.avatarMVP.provider
	if provider == nil || w.app.avatarMVP.providerErr != nil {
		return fmt.Errorf("Seedance 视频服务不可用")
	}
	if w.app.avatarMVP.options.Model != avatar.NativeSpeechModel {
		return fmt.Errorf("原声口播必须使用 Seedance 2.5")
	}
	pieces := avatar.NativeSpeechPieces(seg.Text)
	var paths []string
	for _, piece := range pieces {
		rel := seg.ID + "/native-" + piece.ID + ".mp4"
		dest := filepath.Join(w.dir(s.ID), filepath.FromSlash(rel))
		err := w.step(ctx, s, "F04/"+seg.ID+"-"+piece.ID, []any{fingerprint, piece}, func(ctx context.Context, order *avatar.WorkflowStep) error {
			if info, e := os.Stat(dest); e == nil && info.Size() > 0 {
				return nil
			}
			if order.Submission == "terminal_failed" {
				order.PreserveOrder()
				order.ProviderTaskID = ""
				order.Submission = ""
			}
			if order.Model != "" && order.Model != avatar.NativeSpeechModel {
				return fmt.Errorf("已有订单不属于 Seedance 2.5，不能切换接口查单")
			}
			if order.ProviderTaskID == "" {
				if order.Submission == "unknown" || order.Submission == "submitting" {
					return fmt.Errorf("原声视频订单状态待确认，未重复提交")
				}
				images := []string{}
				if s.Input.Photo != "" {
					photo, ok := nativeAsset(s.Input, s.Input.Photo)
					if !ok {
						return fmt.Errorf("人物照片不可用")
					}
					source, err := safeWorkflowFile(w.projectDir(s.ProjectID), photo.Path)
					if err != nil {
						return err
					}
					u, err := provider.Upload(ctx, source)
					if err != nil {
						return fmt.Errorf("人物照片上传失败，尚未提交生成")
					}
					images = append(images, u)
				} else if s.Input.UseAvatar {
					return fmt.Errorf("请先上传人物照片")
				}
				reference, err := w.nativeReference(ctx, s, *seg, paths)
				if err != nil {
					return err
				}
				audios := []string{}
				if reference != "" {
					u, e := provider.Upload(ctx, reference)
					if e != nil {
						return fmt.Errorf("参考音色上传失败，尚未提交生成")
					}
					audios = append(audios, u)
				}
				prompt := avatar.NativeSpeechPrompt(piece.Text, s.Input.Direction+"\n"+seg.NativePrompt, len(audios) > 0, s.Input.UseAvatar)
				order.Model, order.GenerationPrompt = avatar.NativeSpeechModel, prompt
				order.PromptInput, _ = json.Marshal(map[string]any{"speech_mode": avatar.NativeSpeechMode, "text": piece.Text, "voice_reference": s.Input.VoiceReference, "uses_voice_reference": len(audios) > 0, "request_duration": piece.Duration, "timing": "estimated-until-video-returned"})
				if err = w.beforeSubmit(s, order.ID); err != nil {
					return err
				}
				order.CostStatus, order.ErrorType = "unknown", "provider"
				result, err := provider.Submit(ctx, avatar.GenerationRequest{Model: avatar.NativeSpeechModel, Prompt: prompt, ImageURLs: images, AudioURLs: audios, Duration: strconv.Itoa(piece.Duration), Resolution: w.app.avatarMVP.options.Resolution, Ratio: s.Input.Ratio, GenerateAudio: true, RealPersonMode: true, OutputFormat: "mp4"})
				if err != nil {
					order.Submission = "rejected"
					if avatar.SubmissionUncertain(err) {
						order.Submission = "unknown"
					}
					_ = w.save(s)
					return fmt.Errorf("Seedance 原声视频提交失败，原订单状态已保存")
				}
				order.ProviderTaskID, order.Submission = result.TaskID, "submitted"
				if err = w.save(s); err != nil {
					return err
				}
			}
			result, err := w.poll(ctx, s, order, provider.Query)
			if err != nil {
				return err
			}
			if result.VideoURL() == "" {
				return fmt.Errorf("Seedance 没有返回视频")
			}
			// A failed partial download must not be mistaken for a finished clip.
			temp := dest + ".download"
			if err = provider.Download(ctx, result.VideoURL(), temp); err != nil {
				return fmt.Errorf("Seedance 视频下载失败，恢复时继续查询原订单")
			}
			if err = os.Rename(temp, dest); err != nil {
				return err
			}
			order.Submission = "completed"
			return nil
		})
		if err != nil {
			return err
		}
		paths = append(paths, rel)
	}
	var result struct {
		Duration float64 `json:"duration"`
		Video    string  `json:"video"`
		Audio    string  `json:"audio"`
	}
	if err := w.mediaStep(ctx, s, "native_speech", map[string]any{"segment": seg, "pieces": paths}, &result); err != nil {
		return err
	}
	if result.Duration <= 0 || result.Video != seg.ID+"/native.mp4" || result.Audio != seg.ID+"/audio.wav" {
		return fmt.Errorf("原声视频未返回有效的音画映射")
	}
	seg.Audio, seg.NativeVideo, seg.Duration, seg.Fingerprint = result.Audio, result.Video, result.Duration, fingerprint
	for _, name := range files {
		if err := copyWorkflowFile(filepath.Join(folder, name), filepath.Join(cache, name)); err != nil {
			return err
		}
	}
	if err := avatar.WriteJSON(filepath.Join(cache, "result.json"), result); err != nil {
		return err
	}
	w.nativeArtifacts(s, *seg)
	return nil
}

func (w *avatarWorkflowService) nativeArtifacts(s *avatar.WorkflowState, seg avatar.Segment) {
	w.addArtifacts(s, []avatar.WorkflowArtifact{{ID: "audio-" + seg.ID, Name: seg.ID + " 视频原声.wav", Path: seg.Audio, MediaType: "audio/wav"}, {ID: "native-" + seg.ID, Name: seg.ID + " Seedance 原声口播.mp4", Path: seg.NativeVideo, MediaType: "video/mp4"}})
}

func (w *avatarWorkflowService) useNativeMaterial(ctx context.Context, s *avatar.WorkflowState, need avatar.MaterialNeed) error {
	var seg avatar.Segment
	for _, p := range s.Segments {
		if p.ID == need.SegmentID {
			seg = p
		}
	}
	if seg.NativeVideo == "" || seg.Audio == "" || seg.Duration <= 0 {
		return fmt.Errorf("请先生成本段 Seedance 原声口播")
	}
	return w.step(ctx, s, "F06/"+need.ID, []any{seg.Fingerprint, need}, func(ctx context.Context, step *avatar.WorkflowStep) error {
		relative := "materials/" + need.ID + ".mp4"
		source, err := safeWorkflowFile(w.dir(s.ID), seg.NativeVideo)
		if err != nil {
			return err
		}
		if err = copyWorkflowFile(source, filepath.Join(w.dir(s.ID), filepath.FromSlash(relative))); err != nil {
			return err
		}
		m := avatar.Material{NeedID: need.ID, SegmentID: seg.ID, Kind: "avatar", Path: relative, Duration: seg.Duration, Fingerprint: seg.Fingerprint, BaseFingerprint: seg.Fingerprint}
		replaced := false
		for i := range s.Materials {
			if s.Materials[i].NeedID == need.ID {
				s.Materials[i] = m
				replaced = true
			}
		}
		if !replaced {
			s.Materials = append(s.Materials, m)
		}
		step.CostStatus, step.ProductionPhase = "reused", "复用 Seedance 原声人物画面"
		w.addArtifacts(s, []avatar.WorkflowArtifact{{ID: "material-" + need.ID, Name: need.ID + " 人物素材.mp4", Path: relative, MediaType: "video/mp4"}})
		return nil
	})
}

func validateVoiceReference(in avatar.WorkflowInput) error {
	if in.SpeechMode != "" && in.SpeechMode != "tts" && in.SpeechMode != avatar.NativeSpeechMode {
		return fmt.Errorf("声音生成模式无效")
	}
	if strings.TrimSpace(in.VoiceReference) == "" {
		return nil
	}
	a, ok := nativeAsset(in, in.VoiceReference)
	if !ok || a.Kind != "audio" || a.Duration < 2 || a.Duration > 30 {
		return fmt.Errorf("请选择本项目已上传的 2–30 秒参考音色音频")
	}
	return nil
}
