package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/narratoai/backend-go/internal/config"
)

type FishAudioClient struct {
	cfg        config.Config
	httpClient *http.Client
}

type FishAudioModel struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Raw         map[string]any `json:"raw,omitempty"`
}

func NewFishAudioClient(cfg config.Config) FishAudioClient {
	return FishAudioClient{cfg: cfg, httpClient: &http.Client{Timeout: 90 * time.Second}}
}

func (f FishAudioClient) Configured() bool {
	return f.cfg.FishAudioAPIKey != ""
}

func (f FishAudioClient) TextToSpeech(ctx context.Context, text, referenceID string, speed, volume float64) ([]byte, string, int64, error) {
	if !f.Configured() {
		return nil, "", 0, fmt.Errorf("FishAudio API Key 未配置")
	}
	if referenceID == "" {
		referenceID = "default"
	}
	model := f.cfg.FishAudioModel
	if model == "" {
		model = "s1"
	}
	payload := map[string]any{"text": text, "reference_id": referenceID, "format": "mp3", "speed": speed, "volume": volume}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(f.cfg.FishAudioBaseURL, "/")+"/v1/tts", bytes.NewReader(raw))
	if err != nil {
		return nil, "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+f.cfg.FishAudioAPIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("model", model)
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, "", 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, "", 0, fmt.Errorf("FishAudio TTS 失败: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, "fish-audio-" + model, int64(len([]byte(text)) * 10), nil
}

func (f FishAudioClient) CreateModel(ctx context.Context, audio []byte, transcript, filename, title string) (FishAudioModel, error) {
	if !f.Configured() {
		return FishAudioModel{}, fmt.Errorf("FishAudio API Key 未配置")
	}
	if title == "" {
		title = "NarratoAI Voice"
	}
	if filename == "" {
		filename = "voice.mp3"
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("voices", filename)
	if err != nil {
		return FishAudioModel{}, err
	}
	if _, err := part.Write(audio); err != nil {
		return FishAudioModel{}, err
	}
	_ = writer.WriteField("title", title)
	_ = writer.WriteField("description", "Created by NarratoAI Go backend")
	_ = writer.WriteField("visibility", "private")
	_ = writer.WriteField("type", "tts")
	_ = writer.WriteField("train_mode", "fast")
	_ = writer.WriteField("enhance_audio_quality", "true")
	if transcript != "" {
		_ = writer.WriteField("texts", transcript)
	}
	_ = writer.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(f.cfg.FishAudioBaseURL, "/")+"/model", &body)
	if err != nil {
		return FishAudioModel{}, err
	}
	req.Header.Set("Authorization", "Bearer "+f.cfg.FishAudioAPIKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return FishAudioModel{}, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return FishAudioModel{}, fmt.Errorf("FishAudio 创建模型失败: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var raw map[string]any
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return FishAudioModel{}, err
	}
	model := FishAudioModel{Raw: raw}
	model.ID = firstString(raw, "id", "_id", "model_id")
	model.Title = firstString(raw, "title", "name")
	model.Description = firstString(raw, "description")
	if model.ID == "" {
		return FishAudioModel{}, fmt.Errorf("FishAudio 创建模型成功但未返回 id")
	}
	return model, nil
}

func (f FishAudioClient) DeleteModel(ctx context.Context, modelID string) error {
	if !f.Configured() {
		return fmt.Errorf("FishAudio API Key 未配置")
	}
	modelID = strings.TrimSpace(strings.TrimPrefix(modelID, "/"))
	if modelID == "" {
		return fmt.Errorf("模型 ID 不能为空")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimRight(f.cfg.FishAudioBaseURL, "/")+"/model/"+modelID, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+f.cfg.FishAudioAPIKey)
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("FishAudio 删除模型失败: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func firstString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}
