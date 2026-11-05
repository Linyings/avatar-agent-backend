package avatar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MiniMax uses the documented H3 V2 multimodal reference mode, not first-frame mode.
type MiniMax struct {
	transport *RunningHub
	options   Options
}

func uploadVideoMedia(ctx context.Context, c *RunningHub, path, endpoint string, fields map[string]string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() <= 0 || info.Size() > limit {
		return nil, fmt.Errorf("视频参考素材为空或超过上传大小限制")
	}
	var header [512]byte
	n, _ := file.Read(header[:])
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	name := filepath.Base(path)
	if filepath.Ext(name) == "" {
		switch http.DetectContentType(header[:n]) {
		case "image/png":
			name += ".png"
		case "image/jpeg":
			name += ".jpg"
		}
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for k, v := range fields {
		if err = form.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	part, err := form.CreateFormFile("file", name)
	if err != nil {
		return nil, err
	}
	if _, err = io.Copy(part, file); err != nil {
		return nil, err
	}
	if err = form.Close(); err != nil {
		return nil, err
	}
	return c.request(ctx, endpoint, form.FormDataContentType(), &body, false)
}

func (c *MiniMax) Upload(ctx context.Context, path string) (string, error) {
	limit := int64(30 << 20)
	if strings.EqualFold(filepath.Ext(path), ".wav") || strings.EqualFold(filepath.Ext(path), ".mp3") {
		limit = 15 << 20
	}
	raw, err := uploadVideoMedia(ctx, c.transport, path, "/v1/files/upload", map[string]string{"purpose": "video_generation_input"}, limit)
	if err != nil {
		return "", err
	}
	var out struct {
		File struct {
			ID json.RawMessage `json:"file_id"`
		} `json:"file"`
		Base *struct {
			Code int `json:"status_code"`
		} `json:"base_resp"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Base == nil || out.Base.Code != 0 || scalarID(out.File.ID) == "" {
		return "", fmt.Errorf("MiniMax 上传未返回有效文件 ID")
	}
	return "mm_file://" + scalarID(out.File.ID), nil
}

func miniMaxReference(value string) bool {
	return validHTTPS(value) || (strings.HasPrefix(value, "mm_file://") && len(value) > len("mm_file://") && !strings.ContainsAny(strings.TrimPrefix(value, "mm_file://"), "/?# \r\n"))
}

func (c *MiniMax) Submit(ctx context.Context, in GenerationRequest) (ProviderResult, error) {
	if len(in.ImageURLs) != 1 || len(in.AudioURLs) != 1 || !miniMaxReference(in.ImageURLs[0]) || !miniMaxReference(in.AudioURLs[0]) || strings.TrimSpace(in.Prompt) == "" {
		return ProviderResult{}, fmt.Errorf("MiniMax 口播需要一张人物参考图和一段参考配音")
	}
	seconds, err := strconv.Atoi(in.Duration)
	if err != nil {
		return ProviderResult{}, fmt.Errorf("视频时长无效")
	}
	seconds, err = c.options.GenerationDuration(seconds)
	if err != nil {
		return ProviderResult{}, err
	}
	body, _ := json.Marshal(map[string]any{"model": c.options.Model, "content": []any{
		map[string]any{"type": "text", "text": "使用参考图片1中的人物，参考音频1为该人物的完整对白。" + in.Prompt},
		map[string]any{"type": "image_url", "image_url": map[string]string{"url": in.ImageURLs[0]}, "role": "reference_image"},
		map[string]any{"type": "audio_url", "audio_url": map[string]string{"url": in.AudioURLs[0]}, "role": "reference_audio"},
	}, "duration": seconds, "resolution": c.options.Resolution, "ratio": "9:16"})
	raw, err := c.transport.request(ctx, c.options.GenerationPath, "application/json", bytes.NewReader(body), true)
	if err != nil {
		return ProviderResult{}, err
	}
	var out struct {
		ID string `json:"task_id"`
	}
	if json.Unmarshal(raw, &out) != nil || out.ID == "" {
		return ProviderResult{}, &submissionError{true, "MiniMax 提交响应未能确认任务 ID；禁止直接重新生成"}
	}
	return ProviderResult{TaskID: out.ID, Status: "RUNNING"}, nil
}

func (c *MiniMax) Query(ctx context.Context, id string) (ProviderResult, error) {
	raw, err := c.transport.requestMethod(ctx, "GET", "/v2/query/video_generation/"+url.PathEscape(id), "application/json", nil, false)
	if err != nil {
		return ProviderResult{}, err
	}
	var out struct {
		Task struct {
			ID, Model, Status, Modality string
			Content                     struct{ URL string }
			Usage                       json.RawMessage
		} `json:"task"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Task.ID != id || (out.Task.Model != "" && out.Task.Model != c.options.Model) || (out.Task.Modality != "" && out.Task.Modality != "video") {
		return ProviderResult{}, fmt.Errorf("MiniMax 查询响应或任务 ID 无效")
	}
	status, err := normalizeVideoStatus(out.Task.Status)
	if err != nil {
		return ProviderResult{}, err
	}
	result := ProviderResult{TaskID: id, Status: status, Usage: out.Task.Usage}
	if status == "SUCCESS" {
		if !validHTTPS(out.Task.Content.URL) {
			return result, fmt.Errorf("MiniMax 成功响应缺少视频结果")
		}
		result.Results = append(result.Results, struct {
			URL        string `json:"url"`
			OutputType string `json:"outputType"`
		}{out.Task.Content.URL, "mp4"})
	}
	return result, nil
}

func (c *MiniMax) Download(ctx context.Context, raw, path string) error {
	return c.transport.Download(ctx, raw, path)
}
