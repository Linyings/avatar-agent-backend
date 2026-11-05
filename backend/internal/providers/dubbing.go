package providers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
)

// Dedicated credentials never leave the server. RPC and OSS hosts are fixed.
type DubbingClient struct {
	cfg  config.Config
	HTTP *http.Client
}

var ErrDubbingUpload = errors.New("dubbing upload failed before provider submission")

func NewDubbingClient(cfg config.Config) DubbingClient {
	return DubbingClient{cfg: cfg, HTTP: &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (d DubbingClient) Configured() bool {
	return d.cfg.DubbingAccessKeyID != "" && d.cfg.DubbingAccessKeySecret != "" && regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`).MatchString(d.cfg.DubbingOSSBucket)
}
func aliyunEscape(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
func signSHA1(secret, value string) string {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(value))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
func (d DubbingClient) rpc(ctx context.Context, action string, params url.Values) (map[string]any, error) {
	params.Set("Action", action)
	params.Set("Version", "2014-06-18")
	params.Set("Format", "JSON")
	params.Set("AccessKeyId", d.cfg.DubbingAccessKeyID)
	params.Set("SignatureMethod", "HMAC-SHA1")
	params.Set("SignatureVersion", "1.0")
	params.Set("SignatureNonce", uuid.NewString())
	params.Set("Timestamp", time.Now().UTC().Format("2006-01-02T15:04:05Z"))
	canonical := strings.ReplaceAll(params.Encode(), "+", "%20")
	params.Set("Signature", signSHA1(d.cfg.DubbingAccessKeySecret+"&", "POST&%2F&"+aliyunEscape(canonical)))
	req, err := http.NewRequestWithContext(ctx, "POST", "https://mts.cn-shanghai.aliyuncs.com/", strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return nil, errors.New("MPS 请求未确认，请查询任务状态，勿重复提交")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil {
		return nil, errors.New("MPS 响应格式无效")
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("MPS 请求失败 (%d, %v)", resp.StatusCode, result["Code"])
	}
	return result, nil
}
func (d DubbingClient) ossHost() string {
	return d.cfg.DubbingOSSBucket + ".oss-cn-shanghai.aliyuncs.com"
}
func (d DubbingClient) Submit(ctx context.Context, key string, wav []byte) (string, error) {
	if !d.Configured() {
		return "", errors.New("搞笑配音服务尚未配置")
	}
	if !regexp.MustCompile(`^dubbing/[a-f0-9-]+/[a-f0-9]+/input.wav$`).MatchString(key) {
		return "", errors.New("invalid object key")
	}
	date := time.Now().UTC().Format(http.TimeFormat)
	signature := signSHA1(d.cfg.DubbingAccessKeySecret, "PUT\n\naudio/wav\n"+date+"\n/"+d.cfg.DubbingOSSBucket+"/"+key)
	req, err := http.NewRequestWithContext(ctx, "PUT", "https://"+d.ossHost()+"/"+key, bytes.NewReader(wav))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "audio/wav")
	req.Header.Set("Date", date)
	req.Header.Set("Authorization", "OSS "+d.cfg.DubbingAccessKeyID+":"+signature)
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: 人声分离音频上传失败", ErrDubbingUpload)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("%w: 人声分离音频上传失败 (%d)", ErrDubbingUpload, resp.StatusCode)
	}
	base := "oss://" + d.ossHost() + "/"
	result, err := d.rpc(ctx, "SubmitIProductionJob", url.Values{"FunctionName": {"MusicDemix"}, "Input": {base + key}, "Output": {base + strings.TrimSuffix(key, "input.wav") + "output-{resultType}"}})
	if err != nil {
		return "", err
	}
	id, _ := result["JobId"].(string)
	if id == "" {
		return "", errors.New("MPS 未返回任务编号")
	}
	return id, nil
}
func (d DubbingClient) Query(ctx context.Context, id string) (map[string]any, error) {
	return d.rpc(ctx, "QueryIProductionJob", url.Values{"JobId": {id}})
}

// Only sign the server-derived output key, never a client-provided URL.
func (d DubbingClient) DownloadURL(key string) string {
	expires := strconv.FormatInt(time.Now().Add(15*time.Minute).Unix(), 10)
	signature := signSHA1(d.cfg.DubbingAccessKeySecret, "GET\n\n\n"+expires+"\n/"+d.cfg.DubbingOSSBucket+"/"+key)
	return "https://" + d.ossHost() + "/" + key + "?" + url.Values{"OSSAccessKeyId": {d.cfg.DubbingAccessKeyID}, "Expires": {expires}, "Signature": {signature}}.Encode()
}

// Reject inconsistent PCM headers; clients cannot declare a lower duration to underpay.
func DubbingWAVDuration(data []byte) (float64, error) {
	bad := errors.New("仅支持不超过 5 分钟的 16 位 PCM WAV 音频")
	if len(data) < 44 || len(data) > 30<<20 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0, bad
	}
	if uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data)) {
		return 0, bad
	}
	var rate, align, samples uint32
	for offset := 12; offset+8 <= len(data); {
		if !regexp.MustCompile(`^[A-Za-z0-9 ]{4}$`).Match(data[offset : offset+4]) {
			return 0, bad
		}
		size := uint64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		end := uint64(offset) + 8 + size
		if end > uint64(len(data)) {
			return 0, bad
		}
		chunk := data[offset+8 : int(end)]
		switch string(data[offset : offset+4]) {
		case "fmt ":
			if rate != 0 || len(chunk) < 16 || binary.LittleEndian.Uint16(chunk[:2]) != 1 {
				return 0, bad
			}
			channels := uint32(binary.LittleEndian.Uint16(chunk[2:4]))
			rate = binary.LittleEndian.Uint32(chunk[4:8])
			align = uint32(binary.LittleEndian.Uint16(chunk[12:14]))
			if (channels != 1 && channels != 2) || rate < 8000 || rate > 48000 || binary.LittleEndian.Uint16(chunk[14:16]) != 16 || align != channels*2 || binary.LittleEndian.Uint32(chunk[8:12]) != rate*align {
				return 0, bad
			}
		case "data":
			if samples != 0 {
				return 0, bad
			}
			samples = uint32(size)
		}
		offset = int(end + size%2)
	}
	if rate == 0 || samples == 0 || samples%align != 0 {
		return 0, bad
	}
	duration := float64(samples) / float64(rate*align)
	if duration > 300 {
		return 0, bad
	}
	return duration, nil
}
