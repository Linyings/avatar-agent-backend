package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const ttsAdminProxyMaxBody = 50 << 20

var (
	ttsAdminProxyClient = &http.Client{Timeout: 5 * time.Minute}
	fishModelPath       = regexp.MustCompile(`^/model/[A-Za-z0-9_-]{1,64}$`)
)

func (a *App) adminTTSProviders(c *gin.Context) {
	ok(c, gin.H{
		"siliconflow": gin.H{"configured": a.silicon.Configured()},
		"fishaudio":   gin.H{"configured": a.fish.Configured()},
	})
}

func siliconFlowProxyAllowed(method, path string) bool {
	switch method + " " + path {
	case "GET /audio/voice/list",
		"POST /uploads/audio/voice",
		"POST /audio/voice/deletions",
		"POST /audio/speech",
		"POST /audio/transcriptions":
		return true
	}
	return false
}

func fishAudioProxyAllowed(method, path string) bool {
	if path == "/model" {
		return method == http.MethodGet || method == http.MethodPost
	}
	if path == "/v1/tts" {
		return method == http.MethodPost
	}
	if fishModelPath.MatchString(path) {
		return method == http.MethodGet || method == http.MethodPatch || method == http.MethodDelete
	}
	return false
}

func (a *App) adminSiliconFlowProxy(c *gin.Context) {
	path := c.Param("path")
	if !siliconFlowProxyAllowed(c.Request.Method, path) {
		fail(c, http.StatusNotFound, "不支持的 SiliconFlow 接口")
		return
	}
	if !a.silicon.Configured() {
		fail(c, http.StatusServiceUnavailable, "SiliconFlow API Key 未配置（SILICONFLOW_API_KEY）")
		return
	}
	forwardTTSAdminProxy(c, "SiliconFlow", a.cfg.SiliconFlowBaseURL, path, a.cfg.SiliconFlowAPIKey, nil)
}

func (a *App) adminFishAudioProxy(c *gin.Context) {
	path := c.Param("path")
	if !fishAudioProxyAllowed(c.Request.Method, path) {
		fail(c, http.StatusNotFound, "不支持的 Fish.audio 接口")
		return
	}
	if !a.fish.Configured() {
		fail(c, http.StatusServiceUnavailable, "Fish.audio API Key 未配置（FISH_AUDIO_API_KEY）")
		return
	}
	// Fish.audio reads the TTS model from a `model` header. Browsers on another origin (the desktop
	// app) cannot send that custom header, so it may also arrive as ?model= and is moved to the header.
	if path == "/v1/tts" {
		query := c.Request.URL.Query()
		if model := strings.TrimSpace(query.Get("model")); model != "" {
			c.Request.Header.Set("model", model)
			query.Del("model")
			c.Request.URL.RawQuery = query.Encode()
		}
	}
	forwardTTSAdminProxy(c, "Fish.audio", a.cfg.FishAudioBaseURL, path, a.cfg.FishAudioAPIKey, []string{"model"})
}

func forwardTTSAdminProxy(c *gin.Context, provider, baseURL, path, apiKey string, extraHeaders []string) {
	target := strings.TrimRight(baseURL, "/") + path
	if c.Request.URL.RawQuery != "" {
		target += "?" + c.Request.URL.RawQuery
	}
	if c.Request.Method == http.MethodGet {
		forwardTTSAdminRead(c, provider, target, apiKey)
		return
	}
	var body io.Reader
	if c.Request.Body != nil && c.Request.Body != http.NoBody {
		body = http.MaxBytesReader(c.Writer, c.Request.Body, ttsAdminProxyMaxBody)
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, target, body)
	if err != nil {
		fail(c, http.StatusBadGateway, provider+" 请求失败："+err.Error())
		return
	}
	req.ContentLength = c.Request.ContentLength
	for _, name := range append([]string{"Content-Type", "Accept"}, extraHeaders...) {
		if v := c.GetHeader(name); v != "" {
			req.Header.Set(name, v)
		}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := ttsAdminProxyClient.Do(req)
	if err != nil {
		fail(c, http.StatusBadGateway, provider+" 请求失败："+err.Error())
		return
	}
	defer resp.Body.Close()
	for _, name := range []string{"Content-Type", "Content-Disposition", "Content-Length", "x-siliconcloud-trace-id"} {
		if v := resp.Header.Get(name); v != "" {
			c.Header(name, v)
		}
	}
	c.Status(resp.StatusCode)
	_, _ = io.Copy(c.Writer, resp.Body)
}

// The Fish.audio relay often stalls on a request and answers a fresh one quickly, so a read is
// sent again every few seconds while earlier copies keep running, and the first good answer wins.
// Writes are never repeated.
const (
	ttsAdminReadAttempts = 6
	ttsAdminReadStagger  = 5 * time.Second
	ttsAdminReadTimeout  = 55 * time.Second
)

type ttsAdminReadResult struct {
	resp *http.Response
	body []byte
	err  error
}

func forwardTTSAdminRead(c *gin.Context, provider, target, apiKey string) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), ttsAdminReadTimeout)
	defer cancel()
	results := make(chan ttsAdminReadResult, ttsAdminReadAttempts)
	launch := func() {
		go func() {
			resp, body, err := fetchTTSAdminRead(ctx, c.GetHeader("Accept"), target, apiKey)
			results <- ttsAdminReadResult{resp, body, err}
		}()
	}
	launch()
	launched, finished := 1, 0
	stagger := time.NewTicker(ttsAdminReadStagger)
	defer stagger.Stop()
	var lastErr error
	for finished < launched || launched < ttsAdminReadAttempts {
		select {
		case result := <-results:
			finished++
			if result.err == nil && result.resp.StatusCode < 500 {
				for _, name := range []string{"Content-Type", "Content-Disposition", "x-siliconcloud-trace-id"} {
					if v := result.resp.Header.Get(name); v != "" {
						c.Header(name, v)
					}
				}
				c.Data(result.resp.StatusCode, result.resp.Header.Get("Content-Type"), result.body)
				return
			}
			if result.err == nil {
				lastErr = fmt.Errorf("HTTP %d %s", result.resp.StatusCode, strings.TrimSpace(string(result.body)))
			} else {
				lastErr = result.err
			}
			if launched < ttsAdminReadAttempts {
				launch()
				launched++
			}
		case <-stagger.C:
			if launched < ttsAdminReadAttempts {
				launch()
				launched++
			}
		case <-ctx.Done():
			if lastErr == nil {
				lastErr = ctx.Err()
			}
			fail(c, http.StatusBadGateway, provider+" 请求失败："+lastErr.Error())
			return
		}
	}
	fail(c, http.StatusBadGateway, provider+" 请求失败："+lastErr.Error())
}

func fetchTTSAdminRead(ctx context.Context, accept, target, apiKey string) (*http.Response, []byte, error) {
	// Each attempt gets its own connection: a shared (HTTP/2) connection is exactly what stalls.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	return resp, body, nil
}
