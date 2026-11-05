package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/narratoai/backend-go/internal/providers"
)

type ttsProxyUpstreamCall struct {
	Method, Path, Query, Auth, Cookie, Model, ContentType string
	Body                                                  []byte
}

func newTTSProxyFixture(t *testing.T, siliconKey, fishKey string, handler http.HandlerFunc) (*App, string, string, *[]ttsProxyUpstreamCall) {
	t.Helper()
	calls := &[]ttsProxyUpstreamCall{}
	var mu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		*calls = append(*calls, ttsProxyUpstreamCall{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
			Auth: r.Header.Get("Authorization"), Cookie: r.Header.Get("Cookie"),
			Model: r.Header.Get("model"), ContentType: r.Header.Get("Content-Type"), Body: body,
		})
		mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(body))
		handler(w, r)
	}))
	t.Cleanup(upstream.Close)

	app := newTestApp(t)
	cfg := app.cfg
	cfg.SiliconFlowAPIKey = siliconKey
	cfg.SiliconFlowBaseURL = upstream.URL + "/v1/"
	cfg.FishAudioAPIKey = fishKey
	cfg.FishAudioBaseURL = upstream.URL
	app.cfg = cfg
	app.silicon = providers.NewSiliconFlowClient(cfg)
	app.fish = providers.NewFishAudioClient(cfg)

	admin := registerAndMakeAdmin(t, app, "tts-admin@example.com")
	user := registerAndLogin(t, app, "tts-user@example.com")
	return app, admin, user, calls
}

func rawRequest(app *App, method, path string, body []byte, token string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	return w
}

func TestTTSAdminProxyPermissionsAndStatus(t *testing.T) {
	app, admin, user, calls := newTTSProxyFixture(t, "sf-key", "", func(w http.ResponseWriter, r *http.Request) {})

	for _, path := range []string{
		"/api/v1/admin/tts/providers",
		"/api/v1/admin/tts/siliconflow/audio/voice/list",
		"/api/v1/admin/tts/fishaudio/model",
	} {
		if r := request(app, http.MethodGet, path, nil, user); r.Code != http.StatusForbidden {
			t.Fatalf("non-admin %s: %d", path, r.Code)
		}
	}

	r := request(app, http.MethodGet, "/api/v1/admin/tts/providers", nil, admin)
	if r.Code != http.StatusOK {
		t.Fatalf("status: %d %s", r.Code, r.Body.String())
	}
	var status map[string]map[string]bool
	if err := json.Unmarshal(r.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status["siliconflow"]["configured"] || status["fishaudio"]["configured"] {
		t.Fatalf("unexpected status: %s", r.Body.String())
	}

	r = request(app, http.MethodGet, "/api/v1/admin/tts/fishaudio/model", nil, admin)
	if r.Code != http.StatusServiceUnavailable || !strings.Contains(r.Body.String(), "FISH_AUDIO_API_KEY") {
		t.Fatalf("unconfigured fish: %d %s", r.Code, r.Body.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("upstream should not be called: %+v", *calls)
	}
}

func TestTTSAdminProxySiliconFlowUnconfigured(t *testing.T) {
	app, admin, _, _ := newTTSProxyFixture(t, "", "fish-key", func(w http.ResponseWriter, r *http.Request) {})
	r := request(app, http.MethodGet, "/api/v1/admin/tts/siliconflow/audio/voice/list", nil, admin)
	if r.Code != http.StatusServiceUnavailable || !strings.Contains(r.Body.String(), "SILICONFLOW_API_KEY") {
		t.Fatalf("unconfigured siliconflow: %d %s", r.Code, r.Body.String())
	}
}

func TestTTSAdminProxySiliconFlow(t *testing.T) {
	listBody := `{"result":[{"uri":"speech:abc","customName":"demo"}]}`
	app, admin, _, calls := newTTSProxyFixture(t, "sf-key", "fish-key", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/audio/voice/list":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("x-siliconcloud-trace-id", "trace-1")
			_, _ = w.Write([]byte(listBody))
		case "/v1/uploads/audio/voice":
			_, _ = w.Write([]byte(`{"uri":"speech:new"}`))
		case "/v1/audio/voice/deletions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":20015,"message":"voice not found"}`))
		}
	})

	r := rawRequest(app, http.MethodGet, "/api/v1/admin/tts/siliconflow/audio/voice/list", nil, admin,
		map[string]string{"Cookie": "session=secret"})
	if r.Code != http.StatusOK || r.Body.String() != listBody {
		t.Fatalf("list: %d %s", r.Code, r.Body.String())
	}
	if r.Header().Get("x-siliconcloud-trace-id") != "trace-1" {
		t.Fatalf("trace header missing: %v", r.Header())
	}
	last := (*calls)[len(*calls)-1]
	if last.Path != "/v1/audio/voice/list" || last.Auth != "Bearer sf-key" || last.Cookie != "" {
		t.Fatalf("upstream call: %+v", last)
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("model", "FunAudioLLM/CosyVoice2-0.5B")
	_ = mw.WriteField("customName", "demo")
	fw, _ := mw.CreateFormFile("file", "sample.mp3")
	_, _ = fw.Write([]byte("fake-audio-bytes"))
	_ = mw.Close()
	r = rawRequest(app, http.MethodPost, "/api/v1/admin/tts/siliconflow/uploads/audio/voice", buf.Bytes(), admin,
		map[string]string{"Content-Type": mw.FormDataContentType()})
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "speech:new") {
		t.Fatalf("upload: %d %s", r.Code, r.Body.String())
	}
	last = (*calls)[len(*calls)-1]
	upload := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(last.Body))
	upload.Header.Set("Content-Type", last.ContentType)
	if err := upload.ParseMultipartForm(1 << 20); err != nil {
		t.Fatalf("upstream multipart: %v", err)
	}
	file, header, err := upload.FormFile("file")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(file)
	if header.Filename != "sample.mp3" || string(data) != "fake-audio-bytes" ||
		upload.FormValue("customName") != "demo" || upload.FormValue("model") != "FunAudioLLM/CosyVoice2-0.5B" {
		t.Fatalf("multipart not intact: %s %q %v", header.Filename, data, upload.MultipartForm.Value)
	}

	r = rawRequest(app, http.MethodPost, "/api/v1/admin/tts/siliconflow/audio/voice/deletions", []byte(`{"uri":"speech:x"}`), admin,
		map[string]string{"Content-Type": "application/json"})
	if r.Code != http.StatusBadRequest || r.Body.String() != `{"code":20015,"message":"voice not found"}` {
		t.Fatalf("error passthrough: %d %s", r.Code, r.Body.String())
	}

	before := len(*calls)
	if r := request(app, http.MethodGet, "/api/v1/admin/tts/siliconflow/audio/speech", nil, admin); r.Code != http.StatusNotFound {
		t.Fatalf("non-allowlisted method: %d", r.Code)
	}
	if r := request(app, http.MethodPost, "/api/v1/admin/tts/siliconflow/chat/completions", map[string]any{}, admin); r.Code != http.StatusNotFound {
		t.Fatalf("non-allowlisted path: %d", r.Code)
	}
	if len(*calls) != before {
		t.Fatal("rejected requests reached upstream")
	}
}

func TestTTSAdminProxyFishAudio(t *testing.T) {
	app, admin, _, calls := newTTSProxyFixture(t, "sf-key", "fish-key", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/model" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"total":1,"items":[{"_id":"m1","title":"demo"}]}`))
		case r.URL.Path == "/v1/tts":
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte{0xFF, 0xFB, 0x01, 0x02})
		case r.URL.Path == "/model/m_1-x" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		}
	})

	r := request(app, http.MethodGet, "/api/v1/admin/tts/fishaudio/model?page_size=10&page_number=2&self=true&title=demo", nil, admin)
	if r.Code != http.StatusOK || r.Body.String() != `{"total":1,"items":[{"_id":"m1","title":"demo"}]}` {
		t.Fatalf("list: %d %s", r.Code, r.Body.String())
	}
	last := (*calls)[len(*calls)-1]
	if last.Query != "page_size=10&page_number=2&self=true&title=demo" || last.Auth != "Bearer fish-key" {
		t.Fatalf("list upstream: %+v", last)
	}

	r = rawRequest(app, http.MethodPost, "/api/v1/admin/tts/fishaudio/v1/tts", []byte(`{"text":"hi","reference_id":"m1"}`), admin,
		map[string]string{"Content-Type": "application/json", "model": "speech-1.6"})
	if r.Code != http.StatusOK || r.Header().Get("Content-Type") != "audio/mpeg" || !bytes.Equal(r.Body.Bytes(), []byte{0xFF, 0xFB, 0x01, 0x02}) {
		t.Fatalf("tts: %d %q %v", r.Code, r.Body.Bytes(), r.Header())
	}
	last = (*calls)[len(*calls)-1]
	if last.Model != "speech-1.6" || string(last.Body) != `{"text":"hi","reference_id":"m1"}` {
		t.Fatalf("tts upstream: %+v", last)
	}

	r = rawRequest(app, http.MethodPost, "/api/v1/admin/tts/fishaudio/v1/tts?model=s2-pro", []byte(`{"text":"hi","reference_id":"m1"}`), admin,
		map[string]string{"Content-Type": "application/json"})
	last = (*calls)[len(*calls)-1]
	if r.Code != http.StatusOK || last.Model != "s2-pro" || last.Query != "" {
		t.Fatalf("tts model from query: code=%d upstream=%+v", r.Code, last)
	}

	r = request(app, http.MethodDelete, "/api/v1/admin/tts/fishaudio/model/m_1-x", nil, admin)
	if r.Code != http.StatusOK || (*calls)[len(*calls)-1].Path != "/model/m_1-x" {
		t.Fatalf("delete: %d", r.Code)
	}

	before := len(*calls)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/tts/fishaudio/model/..%2Fx"},
		{http.MethodGet, "/api/v1/admin/tts/fishaudio/model/a/b"},
		{http.MethodPost, "/api/v1/admin/tts/fishaudio/wallet"},
		{http.MethodPut, "/api/v1/admin/tts/fishaudio/model/m1"},
	} {
		if r := request(app, tc.method, tc.path, nil, admin); r.Code != http.StatusNotFound {
			t.Fatalf("%s %s: %d", tc.method, tc.path, r.Code)
		}
	}
	if !fishAudioProxyAllowed(http.MethodGet, "/model/m1") || fishAudioProxyAllowed(http.MethodGet, "/model/../x") {
		t.Fatal("fish allowlist id check")
	}
	if len(*calls) != before {
		t.Fatal("rejected requests reached upstream")
	}
}

func TestTTSAdminProxyRetriesStalledReads(t *testing.T) {
	attempts := 0
	app, admin, _, calls := newTTSProxyFixture(t, "", "fish-key", func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"message":"relay hiccup"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total":1,"items":[{"_id":"m1"}]}`))
	})
	r := rawRequest(app, http.MethodGet, "/api/v1/admin/tts/fishaudio/model?self=true", nil, admin, nil)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"m1"`) || len(*calls) != 2 {
		t.Fatalf("read retry: code=%d body=%s calls=%d", r.Code, r.Body.String(), len(*calls))
	}

	attempts = 0
	*calls = nil
	app2, admin2, _, calls2 := newTTSProxyFixture(t, "", "fish-key", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"message":"relay down"}`))
	})
	r = rawRequest(app2, http.MethodPost, "/api/v1/admin/tts/fishaudio/v1/tts", []byte(`{"text":"hi"}`), admin2, map[string]string{"Content-Type": "application/json"})
	if r.Code != http.StatusBadGateway || len(*calls2) != 1 {
		t.Fatalf("writes must not be retried: code=%d calls=%d", r.Code, len(*calls2))
	}
}

func TestTTSAdminProxyReadOutrunsStalledAttempt(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	app, admin, _, _ := newTTSProxyFixture(t, "", "fish-key", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		first := attempts == 1
		mu.Unlock()
		if first {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"total":1,"items":[{"_id":"m1"}]}`))
	})
	start := time.Now()
	r := rawRequest(app, http.MethodGet, "/api/v1/admin/tts/fishaudio/model?self=true", nil, admin, nil)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"m1"`) {
		t.Fatalf("stalled read: code=%d body=%s", r.Code, r.Body.String())
	}
	if elapsed := time.Since(start); elapsed > ttsAdminReadStagger+3*time.Second {
		t.Fatalf("a stalled first attempt delayed the answer by %s", elapsed)
	}
}
