package avatar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Provider keeps workflow persistence, alignment and rendering independent of vendor APIs.
type Provider interface {
	Upload(context.Context, string) (string, error)
	Submit(context.Context, GenerationRequest) (ProviderResult, error)
	Query(context.Context, string) (ProviderResult, error)
	Download(context.Context, string, string) error
}

func (o Options) VideoDefaults() Options {
	if o.Provider == "" {
		o.Provider = "runninghub"
	}
	if o.Protocol == "" {
		switch o.Provider {
		case "runninghub", "minimax":
			o.Protocol = o.Provider
		default:
			o.Protocol = "http"
		}
	}
	switch o.Protocol {
	case "runninghub":
		if o.BaseURL == "" && o.Provider == "runninghub" {
			o.BaseURL = "https://www.runninghub.cn"
		}
		if o.Model == "" {
			o.Model = "seedance-2.5-token"
		}
		if o.GenerationPath == "" {
			o.GenerationPath = SeedancePath
		}
		if o.Resolution == "" {
			o.Resolution = "720p"
		}
		o.SupportsReferenceAudio = true
	case "minimax":
		if o.BaseURL == "" && o.Provider == "minimax" {
			o.BaseURL = "https://api.minimax.cn"
		}
		if o.Model == "" {
			o.Model = "MiniMax-H3"
		}
		if o.Resolution == "" {
			o.Resolution = "768P"
		}
		if o.GenerationPath == "" {
			o.GenerationPath = "/v2/video_generation"
		}
		o.SupportsReferenceAudio = true
	case "http":
		if o.UploadPath == "" {
			o.UploadPath = "/media"
		}
		if o.GenerationPath == "" {
			o.GenerationPath = "/videos"
		}
		if o.QueryPath == "" {
			o.QueryPath = "/videos/{id}"
		}
		if o.Resolution == "" {
			o.Resolution = "720p"
		}
	}
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	return o
}

func (o Options) ValidateVideo() error {
	o = o.VideoDefaults()
	if o.Protocol != "runninghub" && o.Protocol != "minimax" && o.Protocol != "http" {
		return fmt.Errorf("视频接口协议不支持，请配置 VIDEO_API_PROTOCOL（runninghub / minimax / http）")
	}
	if o.APIKey == "" {
		return fmt.Errorf("视频生成服务 %s 未配置 API Key（VIDEO_API_KEY）", o.Provider)
	}
	u, err := url.Parse(o.BaseURL)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("视频生成服务地址无效（VIDEO_API_BASE_URL）")
	}
	if o.Model == "" {
		return fmt.Errorf("视频模型未配置（VIDEO_MODEL）")
	}
	for _, path := range []string{o.UploadPath, o.GenerationPath, o.QueryPath} {
		if path != "" && (!strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "://")) {
			return fmt.Errorf("视频接口路径必须是服务地址下的相对路径")
		}
	}
	if o.Protocol != "http" && (o.UploadPath != "" || o.QueryPath != "") {
		return fmt.Errorf("VIDEO_UPLOAD_PATH 和 VIDEO_QUERY_PATH 仅用于通用 http 网关协议")
	}
	if !o.SupportsReferenceAudio {
		return fmt.Errorf("当前口播流程要求图片和指定配音参考；请确认服务能力并设置 VIDEO_REFERENCE_AUDIO_SUPPORTED=true")
	}
	if o.Protocol == "runninghub" && o.Model != "seedance-2.5-token" && o.GenerationPath == SeedancePath {
		return fmt.Errorf("切换 RunningHub 模型时必须同时配置匹配的 VIDEO_GENERATION_PATH")
	}
	if o.Protocol == "minimax" {
		switch o.Model {
		case "MiniMax-H3", "MiniMax-H3-Highspeed", "MiniMax-H3-Lite", "MiniMax-H3-Max":
		default:
			return fmt.Errorf("MiniMax 适配器仅支持 H3 V2 模型，请核对 VIDEO_MODEL")
		}
		if o.Resolution != "768P" && !(o.Model == "MiniMax-H3" && o.Resolution == "2K") && !(o.Model != "MiniMax-H3" && o.Resolution == "480P") {
			return fmt.Errorf("当前 MiniMax 模型不支持所配置的 VIDEO_RESOLUTION")
		}
	}
	if o.Protocol == "http" && !strings.Contains(o.QueryPath, "{id}") {
		return fmt.Errorf("通用视频查单路径必须包含 {id}")
	}
	return nil
}

// VideoConfigID excludes secrets so credential rotation can resume existing orders.
func (o Options) VideoConfigID() string {
	o = o.VideoDefaults()
	data, _ := json.Marshal([]any{o.Provider, o.Protocol, o.BaseURL, o.Model, o.Resolution, o.UploadPath, o.GenerationPath, o.QueryPath, o.SupportsReferenceAudio})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (o Options) GenerationDuration(seconds int) (int, error) {
	o = o.VideoDefaults()
	if seconds < 1 || seconds > 15 {
		return 0, fmt.Errorf("口播镜头时长超出当前支持范围（1–15 秒）")
	}
	// Seedance 2.5 multimodal-video accepts integer durations of 4–30 seconds.
	// Generate enough source footage for short narration, then use its existing
	// source in/out mapping; never stretch, repeat, or change the narration.
	if o.Protocol == "runninghub" && o.Model == "seedance-2.5-token" && o.GenerationPath == SeedancePath && seconds < 4 {
		seconds = 4
	}
	if o.Protocol == "minimax" {
		min := 4
		if o.Model == "MiniMax-H3-Max" {
			min = 5
		}
		if seconds < min {
			seconds = min
		}
	}
	return seconds, nil
}

func NewVideoProvider(o Options) (Provider, error) {
	o = o.VideoDefaults()
	// Missing credentials are reported by configured(), without preventing server startup.
	check := o
	if check.APIKey == "" {
		check.APIKey = "configuration-check"
	}
	if err := check.ValidateVideo(); err != nil {
		return nil, err
	}
	transport := NewRunningHub(o.BaseURL, o.APIKey)
	switch o.Protocol {
	case "runninghub":
		transport.GenerationPath = o.GenerationPath
		return transport, nil
	case "minimax":
		transport.Name = "MiniMax"
		return &MiniMax{transport: transport, options: o}, nil
	case "http":
		transport.Name = o.Provider
		return &HTTPProvider{transport: transport, options: o}, nil
	}
	return nil, fmt.Errorf("视频接口协议不支持")
}

func normalizeVideoStatus(status string) (string, error) {
	switch strings.ToLower(status) {
	case "queued", "pending":
		return "QUEUED", nil
	case "running", "processing":
		return "RUNNING", nil
	case "succeeded", "success", "completed":
		return "SUCCESS", nil
	case "failed", "failure":
		return "FAILED", nil
	case "cancelled", "canceled":
		return "CANCELLED", nil
	default:
		return "", fmt.Errorf("视频服务返回未知任务状态")
	}
}

func scalarID(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		if _, err := strconv.ParseUint(n.String(), 10, 64); err == nil {
			return n.String()
		}
	}
	return ""
}
