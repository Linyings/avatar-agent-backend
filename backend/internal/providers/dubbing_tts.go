package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Legacy voice identifiers are stable project data, independent of the configured synthesis model.
const legacyDubbingVoicePrefix = "qwen3-tts-instruct-flash:"

func (d DashScopeClient) DubbingTTSModel() string { return strings.TrimSpace(d.cfg.QwenTTSModel) }

// DashScope returns both dashscope-result-* and dashscope-* OSS buckets.
// Restrict downloads to that bucket namespace on an Alibaba OSS endpoint.
var dubbingAudioHost = regexp.MustCompile(`^dashscope-[a-z0-9]+(?:-[a-z0-9]+)*\.oss-[a-z0-9]+(?:-[a-z0-9]+)*\.aliyuncs\.com$`)

type DubbingTTSVoice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Official non-realtime Instruct system voices, verified 2026-09-11:
// https://help.aliyun.com/zh/model-studio/qwen-tts-voice-list
// Flash-only voices (including dialect voices) are not supported by this model.
var DubbingTTSVoices = []DubbingTTSVoice{
	{"qwen3-tts-instruct-flash:Ethan", "晨煦 · 男声 · 千问"},
	{"qwen3-tts-instruct-flash:Moon", "月白 · 帅气男声 · 千问"},
	{"qwen3-tts-instruct-flash:Kai", "凯 · 舒缓男声 · 千问"},
	{"qwen3-tts-instruct-flash:Vincent", "田叔 · 沙哑男声 · 千问"},
	{"qwen3-tts-instruct-flash:Arthur", "徐大爷 · 老者男声 · 千问"},
	{"qwen3-tts-instruct-flash:Momo", "茉兔 · 搞怪女声 · 千问"},
	{"qwen3-tts-instruct-flash:Vivian", "十三 · 小暴躁女声 · 千问"},
	{"qwen3-tts-instruct-flash:Cherry", "芊悦 · 女声 · 千问"},
	{"qwen3-tts-instruct-flash:Serena", "苏瑶 · 温柔女声 · 千问"},
	{"qwen3-tts-instruct-flash:Chelsie", "千雪 · 二次元女声 · 千问"},
	{"qwen3-tts-instruct-flash:Maia", "四月 · 知性女声 · 千问"},
	{"qwen3-tts-instruct-flash:Nofish", "不吃鱼 · 男声 · 千问"},
	{"qwen3-tts-instruct-flash:Bella", "萌宝 · 萝莉女声 · 千问"},
	{"qwen3-tts-instruct-flash:Eldric Sage", "沧明子 · 睿智老者 · 千问"},
	{"qwen3-tts-instruct-flash:Mia", "乖小妹 · 柔和女声 · 千问"},
	{"qwen3-tts-instruct-flash:Mochi", "沙小弥 · 男童声 · 千问"},
	{"qwen3-tts-instruct-flash:Bellona", "燕铮莺 · 洪亮女声 · 千问"},
	{"qwen3-tts-instruct-flash:Bunny", "萌小姬 · 萝莉女声 · 千问"},
	{"qwen3-tts-instruct-flash:Neil", "阿闻 · 新闻男声 · 千问"},
	{"qwen3-tts-instruct-flash:Elias", "墨讲师 · 讲解女声 · 千问"},
	{"qwen3-tts-instruct-flash:Nini", "邻家妹妹 · 甜美女声 · 千问"},
	{"qwen3-tts-instruct-flash:Seren", "小婉 · 舒缓女声 · 千问"},
	{"qwen3-tts-instruct-flash:Pip", "顽屁小孩 · 男童声 · 千问"},
	{"qwen3-tts-instruct-flash:Stella", "少女阿月 · 少女声 · 千问"},
}

// Preserve the existing selection until an administrator saves a new one.
var DefaultDubbingTTSVoices = DubbingTTSVoices[:10]

func ValidDubbingTTSVoice(voice string) bool {
	for _, v := range DubbingTTSVoices {
		if v.Value == voice {
			return true
		}
	}
	return false
}

func (d DashScopeClient) DubbingTextToSpeech(ctx context.Context, text, voice, instructions string, speed float64) ([]byte, int64, error) {
	if d.DubbingTTSModel() == "" {
		return nil, 0, fmt.Errorf("千问配音模型未配置（QWEN_TTS_MODEL）")
	}
	if !ValidDubbingTTSVoice(voice) {
		return nil, 0, fmt.Errorf("请重新选择千问配音音色")
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	model := d.DubbingTTSModel()
	input := map[string]any{"text": text, "voice": strings.TrimPrefix(voice, legacyDubbingVoicePrefix), "language_type": "Auto"}
	// Only Instruct models accept natural-language delivery instructions.
	if strings.Contains(model, "-instruct-") {
		input["instructions"] = fmt.Sprintf("根据台词表达自然的情绪，避免平淡朗读。保持角色音色稳定。语速约为正常的 %.2f 倍。只说提供的台词，不添加字句或解释。%s", speed, instructions)
		input["optimize_instructions"] = true
	}
	payload, _ := json.Marshal(map[string]any{"model": model, "input": input})

	req, err := http.NewRequestWithContext(ctx, "POST", dashScopeURL("/services/aigc/multimodal-generation/generation"), bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+d.cfg.DashScopeAPIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("千问配音请求失败")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != 200 {
		return nil, 0, newDashScopeHTTPError(resp, body)
	}
	var result struct {
		Output struct {
			Audio struct {
				URL string `json:"url"`
			} `json:"audio"`
		} `json:"output"`
		Usage struct {
			Characters int64 `json:"characters"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &result) != nil {
		return nil, 0, fmt.Errorf("千问配音响应无效")
	}
	audioURL, err := url.Parse(result.Output.Audio.URL)
	if err != nil || audioURL.User != nil || !dubbingAudioHost.MatchString(strings.ToLower(audioURL.Hostname())) || audioURL.Port() != "" || (audioURL.Scheme != "http" && audioURL.Scheme != "https") {
		return nil, 0, fmt.Errorf("千问未返回有效音频地址")
	}
	audioURL.Scheme = "https"
	download, _ := http.NewRequestWithContext(ctx, "GET", audioURL.String(), nil)
	client := &http.Client{Timeout: 45 * time.Second, Transport: d.httpClient.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	audioResp, err := client.Do(download)
	if err != nil {
		return nil, 0, fmt.Errorf("千问音频下载失败")
	}
	defer audioResp.Body.Close()
	if audioResp.StatusCode != 200 {
		return nil, 0, fmt.Errorf("千问音频下载失败: HTTP %d", audioResp.StatusCode)
	}
	audio, err := io.ReadAll(io.LimitReader(audioResp.Body, (32<<20)+1))
	if err != nil || len(audio) <= 44 || len(audio) > 32<<20 || string(audio[:4]) != "RIFF" || string(audio[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("千问未返回有效 WAV 音频")
	}
	if result.Usage.Characters <= 0 {
		return nil, 0, fmt.Errorf("千问未返回配音计费用量")
	}
	return audio, result.Usage.Characters, nil
}
