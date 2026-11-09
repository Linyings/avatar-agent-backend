package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
)

type SiliconFlowClient struct {
	cfg        config.Config
	httpClient *http.Client
}

const defaultSiliconFlowTTSModel = "FunAudioLLM/CosyVoice2-0.5B"

type ChatUsage struct {
	PromptTokens            int64                       `json:"prompt_tokens"`
	CompletionTokens        int64                       `json:"completion_tokens"`
	TotalTokens             int64                       `json:"total_tokens"`
	CompletionTokensDetails *ChatCompletionTokenDetails `json:"completion_tokens_details,omitempty"`
}

type ChatCompletionTokenDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

type ChatResult struct {
	Content   string         `json:"content"`
	Usage     ChatUsage      `json:"usage"`
	Model     string         `json:"model"`
	RequestID string         `json:"request_id"`
	Raw       map[string]any `json:"raw,omitempty"`
}

func NewSiliconFlowClient(cfg config.Config) SiliconFlowClient {
	return SiliconFlowClient{cfg: cfg, httpClient: &http.Client{Timeout: 5 * time.Minute}}
}

func (s SiliconFlowClient) Configured() bool {
	return s.cfg.SiliconFlowConfigured()
}

func (s SiliconFlowClient) AnalyzeImages(ctx context.Context, req map[string]any) (ChatResult, error) {
	prompt := "你是影视解说画面分析助手。请根据图片和前后字幕，只输出简洁剧情概述 JSON，字段包含 summary。"
	if prev, _ := req["previous_subtitle"].(string); prev != "" {
		prompt += "\n前段字幕：" + prev
	}
	if next, _ := req["next_subtitle"].(string); next != "" {
		prompt += "\n后段字幕：" + next
	}
	return s.visionChat(ctx, req, prompt, "Qwen/Qwen3-VL-32B-Instruct")
}

func (s SiliconFlowClient) OCRImages(ctx context.Context, req map[string]any) (ChatResult, error) {
	model, _ := req["model"].(string)
	if model == "" {
		model = "Qwen/Qwen3-VL-32B-Instruct"
	}
	prompt := "请识别每张图片左上角时间戳和画面中文字。只输出 JSON：{\"timestamps\":[],\"text\":[]}，数组长度必须等于图片数量。"
	return s.visionChat(ctx, req, prompt, model)
}

func (s SiliconFlowClient) TextToSpeech(ctx context.Context, text, voice string, speed, volume float64, serviceLevel string) ([]byte, string, int64, error) {
	if !s.Configured() {
		return []byte(""), "go-placeholder", 0, errors.New("SiliconFlow API Key 未配置")
	}
	model := strings.TrimSpace(s.cfg.SiliconFlowTTSModel)
	if model == "" {
		model = defaultSiliconFlowTTSModel
	}
	voice = strings.TrimSpace(voice)
	if voice == "" {
		voice = strings.TrimSpace(s.cfg.SiliconFlowTTSVoice)
	}
	if voice == "" {
		voice = model + ":alex"
	}
	gain := 0.0
	if volume != 1.0 {
		safeVolume := math.Max(0.1, volume)
		gain = 20 * math.Log10(safeVolume)
		gain = math.Max(-10.0, math.Min(10.0, gain))
	}
	payload := map[string]any{"model": model, "input": text, "voice": voice, "speed": speed, "gain": gain}
	raw, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.cfg.SiliconFlowBaseURL, "/")+"/audio/speech", bytes.NewReader(raw))
	if err != nil {
		return nil, "", 0, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+s.cfg.SiliconFlowAPIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return nil, "", 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", 0, fmt.Errorf("read SiliconFlow TTS audio: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, "", 0, fmt.Errorf("SiliconFlow TTS 失败: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, model, 0, nil
}

func (s SiliconFlowClient) UploadReferenceVoice(ctx context.Context, audio []byte, audioText, filename string) (string, error) {
	if !s.Configured() {
		return "speech:custom-name:" + uuid.NewString(), errors.New("SiliconFlow API Key 未配置")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(audio); err != nil {
		return "", err
	}
	_ = writer.WriteField("model", "FunAudioLLM/CosyVoice2-0.5B")
	_ = writer.WriteField("customName", "voice-"+uuid.NewString())
	_ = writer.WriteField("text", audioText)
	_ = writer.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.cfg.SiliconFlowBaseURL, "/")+"/uploads/audio/voice", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.SiliconFlowAPIKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("SiliconFlow 上传音色失败: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var out map[string]any
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", err
	}
	uri, _ := out["uri"].(string)
	if uri == "" {
		return "", errors.New("SiliconFlow 未返回音色 URI")
	}
	return uri, nil
}

func (s SiliconFlowClient) visionChat(ctx context.Context, req map[string]any, prompt string, model string) (ChatResult, error) {
	if !s.Configured() {
		return ChatResult{}, errors.New("SiliconFlow API Key 未配置")
	}
	content := []map[string]any{{"type": "text", "text": prompt}}
	if images, ok := req["images"].([]any); ok {
		for _, item := range images {
			image, ok := item.(map[string]any)
			if !ok {
				continue
			}
			data, _ := image["data"].(string)
			inputType, _ := image["type"].(string)
			if inputType == "base64" && !strings.HasPrefix(data, "data:") {
				mimeType, _ := image["mime_type"].(string)
				if mimeType == "" {
					mimeType = "image/png"
				}
				data = "data:" + mimeType + ";base64," + data
			}
			content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": data}})
		}
	}
	payload := map[string]any{
		"model":       model,
		"messages":    []map[string]any{{"role": "user", "content": content}},
		"temperature": 0.1,
		"max_tokens":  2048,
	}
	return s.chat(ctx, payload)
}

func (s SiliconFlowClient) chat(ctx context.Context, payload map[string]any) (ChatResult, error) {
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.cfg.SiliconFlowBaseURL, "/")+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return ChatResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.SiliconFlowAPIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return ChatResult{}, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return ChatResult{}, fmt.Errorf("SiliconFlow Chat 失败: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var rawResp map[string]any
	if err := json.Unmarshal(respBody, &rawResp); err != nil {
		return ChatResult{}, err
	}
	return parseChatResult(rawResp, fmt.Sprint(payload["model"])), nil
}

func ParseMaybeBase64JSONAudio(value string) ([]byte, error) {
	if value == "" {
		return nil, errors.New("empty audio")
	}
	return base64.StdEncoding.DecodeString(value)
}
