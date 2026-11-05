package providers

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
)

func newDashScopeHTTPError(resp *http.Response, body []byte) error {
	sum := sha256.Sum256(body)
	providerCode := ""
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) == nil {
		for _, key := range []string{"code", "error_code", "errorCode"} {
			if value, ok := envelope[key].(string); ok && strings.TrimSpace(value) != "" {
				providerCode = strings.TrimSpace(value)
				break
			}
		}
	}
	requestID := ""
	status := 0
	if resp != nil {
		status = resp.StatusCode
		requestID = strings.TrimSpace(resp.Header.Get("X-DashScope-RequestId"))
		if requestID == "" {
			requestID = strings.TrimSpace(resp.Header.Get("X-Request-Id"))
		}
	}
	return &DashScopeHTTPError{StatusCode: status, RequestID: requestID, ProviderCode: providerCode, BodyLength: len(body), BodySHA256: fmt.Sprintf("%x", sum[:])}
}

type DashScopeClient struct {
	cfg        config.Config
	httpClient *http.Client
}

// DashScopeHTTPError carries only safe upstream metadata. Response bodies are
// intentionally reduced to a hash and length so prompts and media URLs never
// enter workflow errors or logs.
type DashScopeHTTPError struct {
	StatusCode   int
	RequestID    string
	ProviderCode string
	BodyLength   int
	BodySHA256   string
}

func (e *DashScopeHTTPError) Error() string {
	if e == nil {
		return "DashScope HTTP error"
	}
	return fmt.Sprintf("DashScope request failed: HTTP %d", e.StatusCode)
}

func (e *DashScopeHTTPError) ProviderHTTPStatus() int {
	if e == nil {
		return 0
	}
	return e.StatusCode
}

func (e *DashScopeHTTPError) ProviderDiagnosticFields() map[string]any {
	if e == nil {
		return map[string]any{}
	}
	return map[string]any{
		"request_id":    e.RequestID,
		"provider_code": e.ProviderCode,
		"body_length":   e.BodyLength,
		"body_sha256":   e.BodySHA256,
	}
}

const (
	dashScopeMaxAttempts              = 3
	dashScopeAPIBaseURL               = "https://dashscope.aliyuncs.com/api/v1"
	dashScopeMaxStreamScanTokenSize   = 10 * 1024 * 1024
	dashScopeCompatibleModeAPIBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
)

var dashScopeAPIBaseURLForTest string

func NewDashScopeClient(cfg config.Config) DashScopeClient {
	return DashScopeClient{cfg: cfg, httpClient: &http.Client{Timeout: 0}}
}

func SetDashScopeAPIBaseURLForTest(baseURL string) func() {
	previous := dashScopeAPIBaseURLForTest
	dashScopeAPIBaseURLForTest = strings.TrimRight(baseURL, "/")
	return func() {
		dashScopeAPIBaseURLForTest = previous
	}
}

func dashScopeURL(path string) string {
	baseURL := dashScopeAPIBaseURL
	if dashScopeAPIBaseURLForTest != "" {
		baseURL = dashScopeAPIBaseURLForTest
	}
	return strings.TrimRight(baseURL, "/") + path
}

func (d DashScopeClient) Configured() bool {
	return d.cfg.DashScopeConfigured()
}

func (d DashScopeClient) SubmitASR(ctx context.Context, model string, fileURL string, parameters map[string]any, funASR bool) (map[string]any, error) {
	if !d.Configured() {
		return nil, errors.New("DashScope API Key 未配置")
	}
	input := map[string]any{"file_url": fileURL}
	if funASR {
		input = map[string]any{"file_urls": []string{fileURL}}
	}
	payload := map[string]any{
		"model":      model,
		"input":      input,
		"parameters": parameters,
	}
	return d.doJSON(ctx, http.MethodPost, "/services/audio/asr/transcription", payload, fileURL)
}

func (d DashScopeClient) QueryTask(ctx context.Context, taskID string) (map[string]any, error) {
	if !d.Configured() {
		return nil, errors.New("DashScope API Key 未配置")
	}
	return d.doJSON(ctx, http.MethodGet, "/tasks/"+taskID, nil, "")
}

func (d DashScopeClient) UploadPolicy(ctx context.Context, provider string) (map[string]any, error) {
	if !d.Configured() {
		return nil, errors.New("DashScope API Key 未配置")
	}
	model := d.cfg.Qwen3ASRModel
	switch provider {
	case "paraformer":
		model = d.cfg.DubbingASRModel
	case "funasr":
		model = d.cfg.FunASRModel
	case "qwen-vision":
		model = d.cfg.QwenVisionModel
	}
	return d.UploadPolicyForModel(ctx, provider, model)
}

func (d DashScopeClient) UploadPolicyForModel(ctx context.Context, provider string, model string) (map[string]any, error) {
	if !d.Configured() {
		return nil, errors.New("DashScope API Key 未配置")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, errors.New("DashScope 上传凭证缺少模型")
	}
	policyURL := dashScopeURL("/uploads?action=getPolicy&model=" + url.QueryEscape(model))
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+d.cfg.DashScopeAPIKey)
	requestCtx, cancel := contextWithOptionalTimeout(ctx, d.requestTimeout())
	defer cancel()
	respHTTP, respBody, err := d.doWithRetry(requestCtx, http.MethodGet, policyURL, nil, headers)
	if err != nil {
		return nil, err
	}
	if respHTTP.StatusCode >= 400 {
		return nil, newDashScopeHTTPError(respHTTP, respBody)
	}
	var resp map[string]any
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}
	data, _ := resp["data"].(map[string]any)
	if data == nil {
		return nil, errors.New("DashScope 上传凭证响应缺少 data 字段")
	}
	required := []string{"upload_host", "upload_dir", "oss_access_key_id", "policy", "signature", "x_oss_object_acl", "x_oss_forbid_overwrite"}
	for _, key := range required {
		if fmt.Sprint(data[key]) == "" || data[key] == nil {
			return nil, fmt.Errorf("DashScope 上传凭证缺少必要字段: %s", key)
		}
	}
	data["provider"] = provider
	data["model"] = model
	data["requested_at"] = time.Now().UTC()
	return data, nil
}

func (d DashScopeClient) UploadTempFile(ctx context.Context, model string, filename string, contentType string, data []byte) (string, map[string]any, error) {
	policy, err := d.UploadPolicyForModel(ctx, "qwen-vision", model)
	if err != nil {
		return "", nil, err
	}
	uploadHost := fmt.Sprint(policy["upload_host"])
	uploadDir := strings.TrimRight(fmt.Sprint(policy["upload_dir"]), "/")
	if uploadHost == "" || uploadDir == "" {
		return "", nil, errors.New("DashScope 上传凭证缺少 upload_host 或 upload_dir")
	}
	ext := filepath.Ext(filename)
	if ext == "" {
		ext = ".png"
	}
	objectKey := uploadDir + "/" + uuid.NewString() + ext

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("OSSAccessKeyId", fmt.Sprint(policy["oss_access_key_id"]))
	_ = writer.WriteField("Signature", fmt.Sprint(policy["signature"]))
	_ = writer.WriteField("policy", fmt.Sprint(policy["policy"]))
	_ = writer.WriteField("x-oss-object-acl", fmt.Sprint(policy["x_oss_object_acl"]))
	_ = writer.WriteField("x-oss-forbid-overwrite", fmt.Sprint(policy["x_oss_forbid_overwrite"]))
	_ = writer.WriteField("key", objectKey)
	_ = writer.WriteField("success_action_status", "200")
	fileHeader := textproto.MIMEHeader{}
	fileHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, escapeQuotes(filepath.Base(filename))))
	if strings.TrimSpace(contentType) != "" {
		fileHeader.Set("Content-Type", contentType)
	}
	part, err := writer.CreatePart(fileHeader)
	if err != nil {
		return "", nil, err
	}
	if _, err := part.Write(data); err != nil {
		return "", nil, err
	}
	if err := writer.Close(); err != nil {
		return "", nil, err
	}

	headers := http.Header{}
	headers.Set("Content-Type", writer.FormDataContentType())
	requestCtx, cancel := contextWithOptionalTimeout(ctx, d.requestTimeout())
	defer cancel()
	resp, respBody, err := d.doWithRetry(requestCtx, http.MethodPost, uploadHost, body.Bytes(), headers)
	if err != nil {
		return "", nil, err
	}
	if resp.StatusCode >= 400 {
		return "", nil, newDashScopeHTTPError(resp, respBody)
	}
	return "oss://" + objectKey, policy, nil
}

func escapeQuotes(value string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, "\\\"").Replace(value)
}

type TimelineVisionResult struct {
	Content   string         `json:"content"`
	Parsed    map[string]any `json:"parsed,omitempty"`
	Usage     ChatUsage      `json:"usage"`
	Model     string         `json:"model"`
	RequestID string         `json:"request_id"`
	Raw       map[string]any `json:"raw"`
}

func (d DashScopeClient) AnalyzeTimelineImage(ctx context.Context, imageURL string, prompt string, model string) (TimelineVisionResult, error) {
	return d.AnalyzeTimelineImageStream(ctx, imageURL, prompt, model, nil)
}

func (d DashScopeClient) AnalyzeTimelineImageStream(ctx context.Context, imageURL string, prompt string, model string, emit func(event string, data map[string]any) error) (TimelineVisionResult, error) {
	if !d.Configured() {
		return TimelineVisionResult{}, errors.New("DashScope API Key 未配置")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = d.cfg.QwenVisionModel
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = defaultTimelineVisionPrompt()
	}
	if strings.Contains(prompt, "画面网格") {
		prompt += "\n时间标签规则：格子顶部 # 后的整数只是帧编号，不是秒数；带 s 后缀的小数才是实际源视频时间。只能引用标签中实际存在的时间，不把帧编号转换成时间，不自行补出整数秒或插值区间。读不清时标就报告帧编号并说明无法确认时间。"
	}
	// json_object requires an explicit JSON instruction even when the caller
	// supplies its own visual question instead of the default prompt.
	if !strings.Contains(strings.ToLower(prompt), "json") {
		prompt += "\n只输出一个 JSON 对象，包含 summary、visual_events（每项含 time_range 和 description）及 concerns。不得输出 Markdown。"
	}
	payload := map[string]any{
		"model": model,
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": prompt},
				{"type": "image_url", "image_url": map[string]any{"url": imageURL}},
			},
		}},
		"response_format": map[string]any{"type": "json_object"},
		"stream":          true,
		"stream_options":  map[string]any{"include_usage": true},
	}
	raw, _ := json.Marshal(payload)
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+d.cfg.DashScopeAPIKey)
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "text/event-stream")
	if strings.HasPrefix(imageURL, "oss://") {
		headers.Set("X-DashScope-OssResourceResolve", "enable")
	}
	baseURL := strings.TrimRight(d.cfg.QwenVisionBaseURL, "/")
	if baseURL == "" {
		baseURL = dashScopeCompatibleModeAPIBaseURL
	}
	resp, err := d.doStreamWithRetry(ctx, http.MethodPost, baseURL+"/chat/completions", raw, headers)
	if err != nil {
		return TimelineVisionResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return TimelineVisionResult{}, newDashScopeHTTPError(resp, respBody)
	}
	return parseTimelineVisionStream(resp.Body, model, emit)
}

func (d DashScopeClient) NormalizeASRQuery(ctx context.Context, taskID string, vendor string, resp map[string]any) (map[string]any, int64, error) {
	output, _ := resp["output"].(map[string]any)
	status, _ := output["task_status"].(string)
	if status == "" {
		status = "UNKNOWN"
	}
	if status == "FAILED" {
		return map[string]any{"success": false, "task_id": taskID, "status": "FAILED", "progress": 0, "message": "任务处理失败", "error": output["message"], "updated_at": time.Now()}, 0, nil
	}
	if status == "PENDING" || status == "RUNNING" {
		progress := 0
		if status == "RUNNING" {
			progress = 50
		}
		return map[string]any{"success": true, "task_id": taskID, "status": status, "progress": progress, "message": "正在处理中...", "updated_at": time.Now()}, 0, nil
	}
	if status != "SUCCEEDED" {
		return map[string]any{"success": false, "task_id": taskID, "status": status, "progress": 0, "message": "未知任务状态", "updated_at": time.Now()}, 0, nil
	}

	transcriptionURL := findTranscriptionURL(output)
	if transcriptionURL == "" {
		return map[string]any{"success": false, "task_id": taskID, "status": status, "progress": 100, "message": "任务完成但未返回转录结果", "error": "transcription_url 为空", "updated_at": time.Now()}, 0, nil
	}
	data, err := d.fetchJSON(ctx, transcriptionURL)
	if err != nil {
		return map[string]any{"success": false, "task_id": taskID, "status": status, "progress": 100, "message": "下载转录结果失败", "error": err.Error(), "updated_at": time.Now()}, 0, nil
	}
	normalized := normalizeTranscription(taskID, vendor, data, resp["usage"])
	tokens := numberToInt64Provider(normalized["tokens_consumed"])
	return normalized, tokens, nil
}

func (d DashScopeClient) fetchJSON(ctx context.Context, url string) (map[string]any, error) {
	requestCtx, cancel := contextWithOptionalTimeout(ctx, d.requestTimeout())
	defer cancel()
	resp, body, err := d.doWithRetry(requestCtx, http.MethodGet, url, nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("下载转录结果失败: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (d DashScopeClient) doJSON(ctx context.Context, method, path string, payload any, fileURL string) (map[string]any, error) {
	var raw []byte
	if payload != nil {
		raw, _ = json.Marshal(payload)
	}

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+d.cfg.DashScopeAPIKey)
	headers.Set("Content-Type", "application/json")
	headers.Set("X-DashScope-Async", "enable")
	if strings.HasPrefix(fileURL, "oss://") {
		headers.Set("X-DashScope-OssResourceResolve", "enable")
	}

	requestCtx, cancel := contextWithOptionalTimeout(ctx, d.requestTimeout())
	defer cancel()
	resp, respBody, err := d.doWithRetry(requestCtx, method, dashScopeURL(path), raw, headers)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, newDashScopeHTTPError(resp, respBody)
	}
	var out map[string]any
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (d DashScopeClient) doStreamWithRetry(ctx context.Context, method, url string, rawBody []byte, headers http.Header) (*http.Response, error) {
	var lastErr error
	var lastStatus int
	var lastBody []byte

	for attempt := 1; attempt <= dashScopeMaxAttempts; attempt++ {
		var body io.Reader
		if rawBody != nil {
			body = bytes.NewReader(rawBody)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, body)
		if err != nil {
			return nil, err
		}
		for key, values := range headers {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}

		resp, err := d.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if !isTransientDashScopeError(err) || attempt == dashScopeMaxAttempts {
				return nil, friendlyDashScopeError(err)
			}
			if sleepErr := sleepBeforeDashScopeRetry(ctx, attempt); sleepErr != nil {
				return nil, sleepErr
			}
			continue
		}

		if isRetriableDashScopeStatus(resp.StatusCode) && attempt < dashScopeMaxAttempts {
			lastStatus = resp.StatusCode
			lastBody, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if sleepErr := sleepBeforeDashScopeRetry(ctx, attempt); sleepErr != nil {
				return nil, sleepErr
			}
			continue
		}

		return resp, nil
	}

	if lastErr != nil {
		return nil, friendlyDashScopeError(lastErr)
	}
	return &http.Response{StatusCode: lastStatus, Body: io.NopCloser(bytes.NewReader(lastBody))}, nil
}

func (d DashScopeClient) doWithRetry(ctx context.Context, method, url string, rawBody []byte, headers http.Header) (*http.Response, []byte, error) {
	var lastErr error
	var lastStatus int
	var lastBody []byte

	for attempt := 1; attempt <= dashScopeMaxAttempts; attempt++ {
		var body io.Reader
		if rawBody != nil {
			body = bytes.NewReader(rawBody)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, body)
		if err != nil {
			return nil, nil, err
		}
		for key, values := range headers {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}

		resp, err := d.httpClient.Do(req)
		if err != nil {
			lastErr = err
			if !isTransientDashScopeError(err) || attempt == dashScopeMaxAttempts {
				return nil, nil, friendlyDashScopeError(err)
			}
			if sleepErr := sleepBeforeDashScopeRetry(ctx, attempt); sleepErr != nil {
				return nil, nil, sleepErr
			}
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if !isTransientDashScopeError(readErr) || attempt == dashScopeMaxAttempts {
				return nil, nil, friendlyDashScopeError(readErr)
			}
			if sleepErr := sleepBeforeDashScopeRetry(ctx, attempt); sleepErr != nil {
				return nil, nil, sleepErr
			}
			continue
		}

		if isRetriableDashScopeStatus(resp.StatusCode) && attempt < dashScopeMaxAttempts {
			lastStatus = resp.StatusCode
			lastBody = respBody
			if sleepErr := sleepBeforeDashScopeRetry(ctx, attempt); sleepErr != nil {
				return nil, nil, sleepErr
			}
			continue
		}

		return resp, respBody, nil
	}

	if lastErr != nil {
		return nil, nil, friendlyDashScopeError(lastErr)
	}
	return &http.Response{StatusCode: lastStatus}, lastBody, nil
}

func isTransientDashScopeError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "connection reset by peer") ||
		strings.Contains(message, "connection refused") ||
		strings.Contains(message, "unexpected eof") ||
		strings.Contains(message, "server closed idle connection")
}

func isRetriableDashScopeStatus(status int) bool {
	return status == http.StatusTooManyRequests ||
		status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout
}

func friendlyDashScopeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "client.timeout exceeded") {
		return fmt.Errorf("DashScope 服务响应超时，请稍后重试或缩小输入图片: %w", err)
	}
	if isTransientDashScopeError(err) {
		return fmt.Errorf("DashScope 服务网络连接被重置，请稍后重试: %w", err)
	}
	return err
}

func (d DashScopeClient) requestTimeout() time.Duration {
	if d.cfg.DashScopeTimeout > 0 {
		return d.cfg.DashScopeTimeout
	}
	return 60 * time.Second
}

func sleepBeforeDashScopeRetry(ctx context.Context, attempt int) error {
	delay := time.Duration(attempt*500) * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func findTranscriptionURL(output map[string]any) string {
	if result, ok := output["result"].(map[string]any); ok {
		if url, ok := result["transcription_url"].(string); ok {
			return url
		}
	}
	if results, ok := output["results"].([]any); ok {
		for _, item := range results {
			row, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if url, ok := row["transcription_url"].(string); ok && url != "" {
				return url
			}
		}
	}
	return ""
}

func normalizeTranscription(taskID, vendor string, data map[string]any, usage any) map[string]any {
	content := map[string]any{}
	var fullText []string
	var maxEnd float64
	var language any
	segmentIndex := 1
	if transcripts, ok := data["transcripts"].([]any); ok {
		for _, rawTranscript := range transcripts {
			transcript, ok := rawTranscript.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := transcript["text"].(string); ok && text != "" {
				fullText = append(fullText, text)
			}
			channelID := numberToInt64Provider(transcript["channel_id"])
			if sentences, ok := transcript["sentences"].([]any); ok {
				for _, rawSentence := range sentences {
					sentence, ok := rawSentence.(map[string]any)
					if !ok {
						continue
					}
					start := numberToFloat(sentence["begin_time"]) / 1000
					end := numberToFloat(sentence["end_time"]) / 1000
					if end > maxEnd {
						maxEnd = end
					}
					text, _ := sentence["text"].(string)
					if text != "" && len(fullText) == 0 {
						fullText = append(fullText, text)
					}
					if language == nil {
						language = sentence["language"]
					}
					item := map[string]any{"start_time": start, "end_time": end, "subtitle": text, "channel_id": channelID}
					if speaker := sentence["speaker_id"]; speaker != nil {
						item["speaker_id"] = speaker
					}
					content[fmt.Sprintf("%d", segmentIndex)] = item
					segmentIndex++
				}
			}
		}
	}
	if props, ok := data["properties"].(map[string]any); ok {
		if durationMS := numberToFloat(props["original_duration_in_milliseconds"]); durationMS/1000 > maxEnd {
			maxEnd = durationMS / 1000
		}
	}
	if audioInfo, ok := data["audio_info"].(map[string]any); ok {
		if duration := numberToFloat(audioInfo["duration"]); duration > maxEnd {
			maxEnd = duration
		}
	}
	tokensPerSecond := 15.0
	tokens := int64(maxEnd * tokensPerSecond)
	if usageMap, ok := usage.(map[string]any); ok {
		if duration := numberToFloat(usageMap["duration"]); duration > 0 {
			tokens = int64(duration * tokensPerSecond)
			if duration > maxEnd {
				maxEnd = duration
			}
		}
	}
	return map[string]any{
		"success":         true,
		"task_id":         taskID,
		"status":          "SUCCEEDED",
		"progress":        100,
		"message":         "转录完成",
		"output_format":   "json",
		"content":         content,
		"text_content":    strings.Join(fullText, "\n"),
		"segments_count":  len(content),
		"raw_result":      data,
		"tokens_consumed": tokens,
		"duration":        maxEnd,
		"language":        language,
		"vendor":          vendor,
		"updated_at":      time.Now(),
	}
}

func defaultTimelineVisionPrompt() string {
	return `你是视频剪辑质检助手。图片是 timeline_view.py 生成的视觉辅助图：上方是按时间排列的 filmstrip，下方是音频 waveform、词标签和静音阴影。
请只输出 JSON 对象，不要 Markdown。结构：
{
  "summary": "一句话概括这个时间段发生了什么",
  "visual_events": [{"time_range": "00.00-00.00", "description": "画面/动作/字幕/构图变化"}],
  "cut_safety": {"rating": "safe|caution|unsafe", "reason": "判断依据"},
  "concerns": ["视觉跳变、闪帧、字幕遮挡、波形尖峰、画面错位等问题"],
  "recommendations": ["面向剪辑的具体建议"]
}
重点判断：视觉连续性、是否适合切点、音频波形异常、字幕/overlay 是否遮挡、静音阴影是否提供干净切点。`
}

func parseTimelineVisionStream(body io.Reader, fallbackModel string, emit func(event string, data map[string]any) error) (TimelineVisionResult, error) {
	result := TimelineVisionResult{
		Model: fallbackModel,
		Raw:   map[string]any{"stream": true},
	}
	var full strings.Builder
	chunkCount := 0

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), dashScopeMaxStreamScanTokenSize)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		chunkCount++
		if result.RequestID == "" {
			result.RequestID = firstStringProvider(chunk, "id", "request_id")
		}
		if model := firstStringProvider(chunk, "model"); model != "" {
			result.Model = model
		}
		if usageMap, ok := chunk["usage"].(map[string]any); ok {
			result.Usage = ChatUsage{
				PromptTokens:     numberToInt64Provider(usageMap["prompt_tokens"]),
				CompletionTokens: numberToInt64Provider(usageMap["completion_tokens"]),
				TotalTokens:      numberToInt64Provider(usageMap["total_tokens"]),
			}
		}

		thinkingDelta, answerDelta := extractStreamDeltas(chunk)
		if thinkingDelta != "" && emit != nil {
			if err := emit("thinking_delta", map[string]any{"delta": thinkingDelta}); err != nil {
				return TimelineVisionResult{}, err
			}
		}
		if answerDelta != "" {
			full.WriteString(answerDelta)
			if emit != nil {
				if err := emit("answer_delta", map[string]any{"delta": answerDelta}); err != nil {
					return TimelineVisionResult{}, err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return TimelineVisionResult{}, friendlyDashScopeError(err)
	}

	result.Content = full.String()
	result.Raw["chunks"] = chunkCount
	if err := json.Unmarshal([]byte(result.Content), &result.Parsed); err != nil {
		result.Parsed = nil
	}
	return result, nil
}

func firstStringProvider(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func numberToFloat(v any) float64 {
	switch value := v.(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case json.Number:
		parsed, _ := value.Float64()
		return parsed
	default:
		return 0
	}
}

func numberToInt64Provider(v any) int64 {
	switch value := v.(type) {
	case float64:
		return int64(value)
	case int:
		return int64(value)
	case int64:
		return value
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	default:
		return 0
	}
}
