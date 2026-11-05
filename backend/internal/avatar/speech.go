package avatar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

const doubaoTTSModel = "doubao-seed-tts-2.0"

// SpeechRequest is one RunningHub text-to-speech job. The video pipeline stores the result as WAV.
type SpeechRequest struct {
	Model, Speaker, Language, Format, Text string
}

// Synthesize submits doubao-seed-tts-2.0, waits for the task, and returns the audio bytes.
func (c *RunningHub) Synthesize(ctx context.Context, in SpeechRequest) ([]byte, int64, error) {
	if strings.TrimSpace(in.Text) == "" {
		return nil, 0, fmt.Errorf("配音稿件为空")
	}
	if in.Model == "" {
		in.Model = doubaoTTSModel
	}
	if in.Model != doubaoTTSModel || strings.TrimSpace(in.Speaker) == "" {
		return nil, 0, fmt.Errorf("配音模型或音色未配置（TTS_MODEL / TTS_SPEAKER）")
	}
	if in.Language == "" {
		in.Language = "zh-cn"
	}
	in.Format = "wav"
	result, err := c.SubmitSpeech(ctx, in)
	if err != nil {
		return nil, 0, err
	}
	deadline := time.Now().Add(2 * time.Minute)
	var pollErr error
	for result.Status != "SUCCESS" {
		if result.Status == "FAILED" || result.Status == "FAILURE" || result.Status == "CANCELLED" || result.Status == "CANCELED" {
			return nil, 0, fmt.Errorf("配音任务失败")
		}
		if time.Now().After(deadline) {
			if pollErr != nil {
				return nil, 0, pollErr
			}
			return nil, 0, fmt.Errorf("配音任务超时")
		}
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-time.After(2 * time.Second):
		}
		next, err := c.QuerySpeech(ctx, result.TaskID)
		if err != nil {
			pollErr = err
			continue
		}
		result = next
		pollErr = nil
	}
	audioURL := result.AudioURL()
	if !validHTTPS(audioURL) {
		return nil, 0, fmt.Errorf("配音结果地址无效")
	}
	audio, err := c.fetchAudio(ctx, audioURL)
	if err != nil {
		return nil, 0, err
	}
	return audio, int64(utf8.RuneCountInString(in.Text)), nil
}

// QuerySpeech queries an existing audio order without the video's result validation.
func (c *RunningHub) QuerySpeech(ctx context.Context, id string) (ProviderResult, error) {
	result, err := c.queryTask(ctx, id)
	if err == nil && result.Status == "SUCCESS" && !validHTTPS(result.AudioURL()) {
		return result, fmt.Errorf("配音订单成功但缺少有效声音结果，原订单已保留")
	}
	return result, err
}

func (r ProviderResult) AudioURL() string {
	for _, item := range r.Results {
		kind := strings.ToLower(item.OutputType)
		path := strings.ToLower(strings.Split(item.URL, "?")[0])
		if kind == "wav" || kind == "mp3" || kind == "audio" || strings.HasSuffix(path, ".wav") || strings.HasSuffix(path, ".mp3") {
			return item.URL
		}
	}
	return ""
}

func (c *RunningHub) fetchAudio(ctx context.Context, raw string) ([]byte, error) {
	if c.fetch != nil {
		return c.fetch(ctx, raw)
	}
	file, err := os.CreateTemp("", "avatar-tts-*.wav")
	if err != nil {
		return nil, err
	}
	path := file.Name()
	_ = file.Close()
	_ = os.Remove(path)
	if err = c.Download(ctx, raw, path); err != nil {
		return nil, err
	}
	defer os.Remove(path)
	return os.ReadFile(path)
}

// SubmitSpeech lets the workflow persist the paid order before polling.
func (c *RunningHub) SubmitSpeech(ctx context.Context, in SpeechRequest) (ProviderResult, error) {
	if strings.TrimSpace(in.Text) == "" || strings.TrimSpace(in.Speaker) == "" {
		return ProviderResult{}, fmt.Errorf("配音参数未配置")
	}
	if in.Model == "" {
		in.Model = doubaoTTSModel
	}
	if in.Model != doubaoTTSModel {
		return ProviderResult{}, fmt.Errorf("不支持的异步配音模型")
	}
	if in.Language == "" {
		in.Language = "zh-cn"
	}
	body, err := json.Marshal(map[string]any{
		"text": in.Text, "speaker": in.Speaker, "format": in.Format,
		"sample_rate": "24000", "explicit_language": in.Language, "enable_subtitle": false,
	})
	if err != nil {
		return ProviderResult{}, err
	}
	raw, err := c.request(ctx, "/openapi/v2/bytedance/"+in.Model, "application/json", bytes.NewReader(body), true)
	if err != nil {
		return ProviderResult{}, err
	}
	result, err := decodeProviderResult(raw)
	if err != nil || result.TaskID == "" {
		return ProviderResult{}, &submissionError{uncertain: true, message: "配音提交状态待确认"}
	}
	return result, nil
}
