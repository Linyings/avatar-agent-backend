package avatar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// SpeechRequest is one RunningHub text-to-speech job. The video pipeline stores the result as WAV.
const doubaoTTSModel = "doubao-seed-tts-2.0"

var errSpeechValidation = errors.New("配音请求参数校验失败（供应商错误码 1007）")

// Only expose diagnostics owned by this adapter; upstream messages can contain
// credentials, prompt text or signed URLs.
func SpeechFailureMessage(err error) string {
	if errors.Is(err, errSpeechValidation) {
		return errSpeechValidation.Error()
	}
	return "配音模型调用失败；提交状态已记录"
}

type SpeechRequest struct {
	Model, Speaker, Language, Format, Text string
	Speed                                  float64
}

// Synthesize submits a TTS request, waits for the task, and returns the audio bytes.
// Supports any TTS model configured in TTS_MODEL environment variable.
func (c *RunningHub) Synthesize(ctx context.Context, in SpeechRequest) ([]byte, int64, error) {
	if strings.TrimSpace(in.Text) == "" {
		return nil, 0, fmt.Errorf("配音稿件为空")
	}
	if in.Model == "" {
		return nil, 0, fmt.Errorf("配音模型未配置（TTS_MODEL）")
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
	if strings.TrimSpace(in.Text) == "" {
		return ProviderResult{}, fmt.Errorf("配音参数未配置")
	}
	if in.Model == "" {
		return ProviderResult{}, fmt.Errorf("配音模型未配置（TTS_MODEL）")
	}
	in.Speaker = strings.TrimSpace(in.Speaker)
	if in.Speaker == "" {
		in.Speaker = DefaultSpeechVoice(in.Model)
	}
	// Do not silently change the voice of a frozen run at the paid boundary.
	// Draft validation handles migration before fingerprints and orders exist.
	if SpeechVoiceIncompatible(in.Model, in.Speaker) {
		return ProviderResult{}, fmt.Errorf("音色与当前配音模型不匹配，请在项目中改用当前模型音色")
	}
	if in.Language == "" {
		in.Language = "zh-cn"
	}

	// 构建 API 路径：使用模型名称作为路径的一部分
	// 例如：/openapi/v2/bytedance/doubao-seed-tts-2.0 或 /openapi/v2/minimax/speech-2.8-hd
	apiPath := "/openapi/v2/"

	// 根据模型名称判断提供商
	if strings.HasPrefix(in.Model, "minimax/") {
		apiPath += "rhart-audio/text-to-audio/" + strings.TrimPrefix(in.Model, "minimax/")
	} else if strings.HasPrefix(in.Model, "doubao-") || strings.HasPrefix(in.Model, "bytedance/") {
		// doubao 和 bytedance 模型使用 bytedance 路径
		modelName := strings.TrimPrefix(in.Model, "bytedance/")
		apiPath += "bytedance/" + modelName
	} else {
		// 默认使用模型名称作为路径
		apiPath += in.Model
	}

	params := map[string]any{
		"text": in.Text, "speaker": in.Speaker, "format": in.Format,
		"sample_rate": "24000", "explicit_language": in.Language, "enable_subtitle": false,
	}
	if strings.HasPrefix(in.Model, "minimax/") {
		speed := in.Speed
		if speed == 0 {
			speed = 1
		}
		if err := ValidateVoiceSpeed(speed); err != nil {
			return ProviderResult{}, err
		}
		params = map[string]any{"text": in.Text, "voice_id": in.Speaker, "speed": speed, "volume": 1, "pitch": 0, "english_normalization": false, "enable_base64_output": false}
	}
	body, err := json.Marshal(params)
	if err != nil {
		return ProviderResult{}, err
	}
	raw, err := c.request(ctx, apiPath, "application/json", bytes.NewReader(body), true)
	if err != nil {
		return ProviderResult{}, err
	}
	var rejection struct {
		TaskID string          `json:"taskId"`
		Code   json.RawMessage `json:"errorCode"`
	}
	if json.Unmarshal(raw, &rejection) == nil && rejection.TaskID == "" && (string(rejection.Code) == "1007" || string(rejection.Code) == `"1007"`) {
		return ProviderResult{}, errSpeechValidation
	}
	result, err := decodeProviderResult(raw)
	if err != nil || result.TaskID == "" {
		return ProviderResult{}, &submissionError{uncertain: true, message: "配音提交状态待确认"}
	}
	return result, nil
}
