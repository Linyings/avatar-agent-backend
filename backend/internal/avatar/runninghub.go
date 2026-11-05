package avatar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const SeedancePath = "/openapi/v2/bytedance/seedance-2.5-token/multimodal-video"

type RunningHub struct {
	BaseURL, APIKey      string
	Name, GenerationPath string
	HTTP                 *http.Client
	fetch                func(context.Context, string) ([]byte, error)
}
type GenerationRequest struct {
	Model          string   `json:"model,omitempty"`
	Prompt         string   `json:"prompt"`
	ImageURLs      []string `json:"imageUrls"`
	AudioURLs      []string `json:"audioUrls"`
	Duration       string   `json:"duration"`
	Resolution     string   `json:"resolution"`
	Ratio          string   `json:"ratio"`
	GenerateAudio  bool     `json:"generateAudio"`
	RealPersonMode bool     `json:"realPersonMode"`
	OutputFormat   string   `json:"outputFormat"`
}
type ProviderResult struct {
	TaskID       string `json:"taskId"`
	Status       string `json:"status"`
	ErrorCode    string `json:"errorCode"`
	ErrorMessage string `json:"errorMessage"`
	Results      []struct {
		URL        string `json:"url"`
		OutputType string `json:"outputType"`
	} `json:"results"`
	Usage json.RawMessage `json:"usage,omitempty"`
}

func (r ProviderResult) VideoURL() string {
	for _, v := range r.Results {
		if v.OutputType == "mp4" || v.OutputType == "video" || strings.HasSuffix(strings.ToLower(strings.Split(v.URL, "?")[0]), ".mp4") {
			return v.URL
		}
	}
	return ""
}

type submissionError struct {
	uncertain bool
	message   string
}

func (e *submissionError) Error() string { return e.message }
func SubmissionUncertain(err error) bool {
	var e *submissionError
	return errors.As(err, &e) && e.uncertain
}

type transientError struct{ message string }

func (e *transientError) Error() string { return e.message }

func (c *RunningHub) transportError(err error, submission bool) error {
	reason := "连接中断"
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		reason = "超时"
	}
	if submission {
		return &submissionError{true, c.label() + " 提交" + reason + "，未确认任务是否已受理；不得自动重发"}
	}
	return &transientError{c.label() + " 请求" + reason + "，可以重试"}
}

func NewRunningHub(base, key string) *RunningHub {
	return &RunningHub{BaseURL: strings.TrimRight(base, "/"), APIKey: key, HTTP: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *RunningHub) request(ctx context.Context, path, contentType string, body io.Reader, submission bool) ([]byte, error) {
	return c.requestMethod(ctx, "POST", path, contentType, body, submission)
}
func (c *RunningHub) label() string {
	if c.Name != "" {
		return c.Name
	}
	return "RunningHub"
}
func (c *RunningHub) requestMethod(ctx context.Context, method, path, contentType string, body io.Reader, submission bool) (data []byte, err error) {
	started := time.Now().UTC()
	status, requestID := 0, ""
	var responseBody []byte
	defer func() { recordProviderHTTP(ctx, path, started, status, responseBody, requestID, err) }()
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", contentType)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, c.transportError(err, submission)
	}
	defer resp.Body.Close()
	status, requestID = resp.StatusCode, resp.Header.Get("X-Request-Id")
	data, err = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	responseBody = data
	if err != nil {
		return nil, &submissionError{submission, c.label() + " 响应读取中断"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &submissionError{submission && (resp.StatusCode >= 500 || resp.StatusCode == 408), fmt.Sprintf("%s 返回 HTTP %d", c.label(), resp.StatusCode)}
	}
	return data, nil
}
func (c *RunningHub) Upload(ctx context.Context, path string) (string, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Second):
			}
		}
		uploaded, err := c.uploadOnce(ctx, path)
		if err == nil {
			return uploaded, nil
		}
		last = err
		var transient *transientError
		if !errors.As(err, &transient) {
			return "", err
		}
	}
	return "", last
}

func (c *RunningHub) uploadOnce(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() > 40<<20 {
		return "", fmt.Errorf("上传文件超过 40MB")
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	filename := filepath.Base(path)
	if filepath.Ext(filename) == "" {
		var header [512]byte
		n, _ := file.Read(header[:])
		_, _ = file.Seek(0, io.SeekStart)
		switch http.DetectContentType(header[:n]) {
		case "image/png":
			filename += ".png"
		case "image/jpeg":
			filename += ".jpg"
		}
	}
	part, err := form.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err = io.Copy(part, file); err != nil {
		return "", err
	}
	if err = form.Close(); err != nil {
		return "", err
	}
	data, err := c.request(ctx, "/openapi/v2/media/upload/binary", form.FormDataContentType(), &body, false)
	if err != nil {
		return "", err
	}
	var out struct {
		Code int `json:"code"`
		Data struct {
			URL string `json:"download_url"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &out) != nil || out.Code != 0 || !validHTTPS(out.Data.URL) {
		return "", fmt.Errorf("RunningHub 上传未返回有效 download_url")
	}
	return out.Data.URL, nil
}
func (c *RunningHub) Submit(ctx context.Context, input GenerationRequest) (ProviderResult, error) {
	// RunningHub selects the model through its endpoint, not a model body field.
	input.Model = ""
	data, err := json.Marshal(input)
	if err != nil {
		return ProviderResult{}, err
	}
	endpoint := c.GenerationPath
	if endpoint == "" {
		endpoint = SeedancePath
	}
	raw, err := c.request(ctx, endpoint, "application/json", bytes.NewReader(data), true)
	if err != nil {
		return ProviderResult{}, err
	}
	result, err := decodeProviderResult(raw)
	if err != nil || result.TaskID == "" {
		return ProviderResult{}, &submissionError{true, "RunningHub 提交响应未能确认任务 ID；禁止直接重新生成"}
	}
	return result, nil
}
func (c *RunningHub) Query(ctx context.Context, id string) (ProviderResult, error) {
	result, err := c.queryTask(ctx, id)
	if err != nil {
		return result, err
	}
	if result.Status == "SUCCESS" && !validHTTPS(result.VideoURL()) {
		return result, fmt.Errorf("RunningHub 成功响应缺少视频结果")
	}
	return result, nil
}
func (c *RunningHub) queryTask(ctx context.Context, id string) (ProviderResult, error) {
	data, _ := json.Marshal(map[string]string{"taskId": id})
	raw, err := c.request(ctx, "/openapi/v2/query", "application/json", bytes.NewReader(data), false)
	if err != nil {
		return ProviderResult{}, err
	}
	result, err := decodeProviderResult(raw)
	if err != nil {
		return result, err
	}
	if result.TaskID != "" && result.TaskID != id {
		return result, fmt.Errorf("RunningHub 查询任务 ID 不一致")
	}
	result.TaskID = id
	return result, nil
}
func decodeProviderResult(raw []byte) (ProviderResult, error) {
	var r ProviderResult
	var code struct {
		Code json.RawMessage `json:"code"`
	}
	if json.Unmarshal(raw, &r) != nil || json.Unmarshal(raw, &code) != nil || r.Status == "" {
		return r, fmt.Errorf("RunningHub 任务响应无效")
	}
	if len(code.Code) > 0 && string(code.Code) != "0" && string(code.Code) != "null" && string(code.Code) != `"0"` {
		return r, fmt.Errorf("RunningHub 任务请求被拒绝")
	}
	switch r.Status {
	case "QUEUED", "PENDING", "RUNNING", "SUCCESS", "FAILED", "FAILURE", "CANCELED", "CANCELLED":
	default:
		return r, fmt.Errorf("RunningHub 返回未知任务状态")
	}
	// Arbitrary upstream diagnostics can contain prompts and signed URLs.
	r.ErrorMessage = ""
	// RunningHub reports billing both in usage and in top-level fields. Keep the
	// supplier's exact values and units; do not infer a currency or total them here.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	usage := map[string]json.RawMessage{}
	_ = json.Unmarshal(r.Usage, &usage)
	if usage == nil {
		usage = map[string]json.RawMessage{}
	}
	for _, name := range []string{"consumeMoney", "thirdPartyConsumeMoney", "consumeCoins", "currency", "billingUnit", "tokenUsage"} {
		if value, ok := fields[name]; ok && string(value) != "null" {
			usage[name] = value
		}
	}
	if len(usage) > 0 {
		r.Usage, _ = json.Marshal(usage)
	}
	return r, nil
}
func validHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}

// Download accepts only public HTTPS destinations and never forwards the API key.
func (c *RunningHub) Download(ctx context.Context, raw, path string) error {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
		last = c.downloadOnce(ctx, raw, path)
		if last == nil || last.Error() == "视频地址无效" {
			return last
		}
	}
	return last
}

func (c *RunningHub) downloadOnce(ctx context.Context, raw, path string) error {
	if !validHTTPS(raw) {
		return fmt.Errorf("视频地址无效")
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: publicDialContext}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 3 * time.Minute, Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 || !validHTTPS(req.URL.String()) {
			return fmt.Errorf("无效的媒体跳转")
		}
		return nil
	}}
	req, _ := http.NewRequestWithContext(ctx, "GET", raw, nil)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("视频素材下载失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("视频素材下载返回 HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(path + ".part")
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, (250<<20)+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil || n == 0 || n > 250<<20 {
		os.Remove(path + ".part")
		return fmt.Errorf("视频素材下载不完整或过大")
	}
	return os.Rename(path+".part", path)
}
