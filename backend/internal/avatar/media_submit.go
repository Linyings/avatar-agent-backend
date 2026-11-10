package avatar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SubmitMedia posts a body built by the media registry to a RunningHub
// standard model path. Like Submit, an unconfirmed submission is reported as
// uncertain so callers never resubmit a possibly paid order.
func (c *RunningHub) SubmitMedia(ctx context.Context, path string, body map[string]any) (ProviderResult, error) {
	if !strings.HasPrefix(path, "/openapi/v2/") || strings.Contains(path, "..") || strings.Contains(path, "://") {
		return ProviderResult{}, fmt.Errorf("媒体模型路径无效")
	}
	data, err := json.Marshal(body)
	if err != nil {
		return ProviderResult{}, err
	}
	raw, err := c.request(ctx, path, "application/json", bytes.NewReader(data), true)
	if err != nil {
		return ProviderResult{}, err
	}
	var rejection struct {
		TaskID  string          `json:"taskId"`
		Code    json.RawMessage `json:"errorCode"`
		Message string          `json:"errorMessage"`
	}
	if json.Unmarshal(raw, &rejection) == nil && rejection.TaskID == "" && len(rejection.Code) > 0 && string(rejection.Code) != `""` && string(rejection.Code) != "null" {
		message := "模型拒绝了请求参数（错误码 " + strings.Trim(string(rejection.Code), `"`) + "）"
		if reason := strings.TrimSpace(rejection.Message); reason != "" {
			message += "：" + string([]rune(reason)[:min(len([]rune(reason)), 200)])
		}
		return ProviderResult{}, &submissionError{false, message}
	}
	result, err := decodeProviderResult(raw)
	if err != nil || result.TaskID == "" {
		return ProviderResult{}, &submissionError{true, "RunningHub 提交响应未能确认任务 ID；禁止直接重新生成"}
	}
	return result, nil
}

// QueryMedia polls an order without assuming the output is a video.
func (c *RunningHub) QueryMedia(ctx context.Context, id string) (ProviderResult, error) {
	return c.queryTask(ctx, id)
}

// MediaURL returns the first HTTPS output of the given class: audio, image or video.
func (r ProviderResult) MediaURL(class string) string {
	for _, item := range r.Results {
		kind := strings.ToLower(item.OutputType)
		path := strings.ToLower(strings.Split(item.URL, "?")[0])
		ext := path[strings.LastIndex(path, ".")+1:]
		var got string
		switch {
		case kind == "mp3" || kind == "wav" || kind == "audio" || kind == "flac" || kind == "m4a" || ext == "mp3" || ext == "wav" || ext == "flac" || ext == "m4a":
			got = "audio"
		case kind == "png" || kind == "jpg" || kind == "jpeg" || kind == "webp" || kind == "image" || ext == "png" || ext == "jpg" || ext == "jpeg" || ext == "webp":
			got = "image"
		case kind == "mp4" || kind == "mov" || kind == "video" || ext == "mp4" || ext == "mov" || ext == "webm":
			got = "video"
		}
		if got == class && validHTTPS(item.URL) {
			return item.URL
		}
	}
	return ""
}
