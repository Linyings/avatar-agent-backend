package avatar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// HTTPProvider is the documented vendor-neutral gateway protocol. Native vendor
// protocols remain separate adapters; an arbitrary API key alone is not compatible.
type HTTPProvider struct {
	transport *RunningHub
	options   Options
}

func (c *HTTPProvider) Upload(ctx context.Context, path string) (string, error) {
	raw, err := uploadVideoMedia(ctx, c.transport, path, c.options.UploadPath, nil, 30<<20)
	if err != nil {
		return "", err
	}
	var out struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &out) != nil || !validHTTPS(out.URL) {
		return "", fmt.Errorf("视频上传接口未返回有效 url")
	}
	return out.URL, nil
}
func (c *HTTPProvider) Submit(ctx context.Context, in GenerationRequest) (ProviderResult, error) {
	if len(in.ImageURLs) != 1 || len(in.AudioURLs) != 1 || !validHTTPS(in.ImageURLs[0]) || !validHTTPS(in.AudioURLs[0]) || strings.TrimSpace(in.Prompt) == "" {
		return ProviderResult{}, fmt.Errorf("视频口播需要一张人物参考图和一段指定配音")
	}
	seconds, err := strconv.Atoi(in.Duration)
	if err != nil {
		return ProviderResult{}, fmt.Errorf("视频时长无效")
	}
	if _, err = c.options.GenerationDuration(seconds); err != nil {
		return ProviderResult{}, err
	}
	in.Model = c.options.Model
	in.Resolution = c.options.Resolution
	raw, _ := json.Marshal(in)
	raw, err = c.transport.request(ctx, c.options.GenerationPath, "application/json", bytes.NewReader(raw), true)
	if err != nil {
		return ProviderResult{}, err
	}
	var out struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &out) != nil || out.ID == "" {
		return ProviderResult{}, &submissionError{true, "视频提交响应未能确认任务 ID；禁止直接重新生成"}
	}
	return ProviderResult{TaskID: out.ID, Status: "RUNNING"}, nil
}
func (c *HTTPProvider) Query(ctx context.Context, id string) (ProviderResult, error) {
	endpoint := strings.ReplaceAll(c.options.QueryPath, "{id}", url.PathEscape(id))
	raw, err := c.transport.requestMethod(ctx, "GET", endpoint, "application/json", nil, false)
	if err != nil {
		return ProviderResult{}, err
	}
	var out struct {
		ID, Status, URL string
		Usage           json.RawMessage
	}
	if json.Unmarshal(raw, &out) != nil || out.ID != id {
		return ProviderResult{}, fmt.Errorf("视频查询响应或任务 ID 无效")
	}
	status, err := normalizeVideoStatus(out.Status)
	if err != nil {
		return ProviderResult{}, err
	}
	result := ProviderResult{TaskID: id, Status: status, Usage: out.Usage}
	if status == "SUCCESS" {
		if !validHTTPS(out.URL) {
			return result, fmt.Errorf("视频成功响应缺少有效视频地址")
		}
		result.Results = append(result.Results, struct {
			URL        string `json:"url"`
			OutputType string `json:"outputType"`
		}{out.URL, "mp4"})
	}
	return result, nil
}
func (c *HTTPProvider) Download(ctx context.Context, raw, path string) error {
	return c.transport.Download(ctx, raw, path)
}
