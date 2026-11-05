package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type desktopOSSRoundTrip func(*http.Request) (*http.Response, error)

func (f desktopOSSRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDesktopDownloadOSSChannels(t *testing.T) {
	root := t.TempDir()
	writeDesktopUpdateFixture(t, root, "win32/x64/latest.json", `{"schemaVersion":1,"enabled":true,"version":"0.2.13","releaseDate":"2026-09-01T00:00:00Z"}`)
	writeDesktopUpdateFixture(t, root, "win32/x64/NarratoAI-PC-Setup-0.2.13.exe", "installer")
	app := newDesktopUpdateTestApp(root)
	app.cfg.DesktopDownloadOSSBucket = "test-bucket"
	app.cfg.DesktopDownloadOSSRegion = "cn-shanghai"
	app.cfg.DubbingAccessKeyID = "test-key"
	app.cfg.DubbingAccessKeySecret = "test-secret"
	old := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = old })
	calls := 0
	size := int64(9)
	http.DefaultClient = &http.Client{Transport: desktopOSSRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "HEAD" || r.URL.Host != "test-bucket.oss-cn-shanghai.aliyuncs.com" {
			t.Fatalf("unexpected OSS request %s %s", r.Method, r.URL.Host)
		}
		return &http.Response{StatusCode: 200, ContentLength: size, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		app.Router().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	one := get(windowsDesktopDownloadPath)
	u, _ := url.Parse(one.Header().Get("Location"))
	if one.Code != 302 || u.Host != "test-bucket.oss-cn-shanghai.aliyuncs.com" || u.Query().Get("Signature") == "" || u.Query().Get("Expires") == "" {
		t.Fatalf("invalid signed redirect: %d %s", one.Code, u)
	}
	if strings.Contains(u.String(), "test-secret") {
		t.Fatal("secret leaked")
	}
	two := get(windowsDesktopDownloadPath + "?source=server")
	if calls != 1 || two.Header().Get("Location") != "/desktop-updates/win32/x64/NarratoAI-PC-Setup-0.2.13.exe?source=server" {
		t.Fatal("server channel used OSS")
	}
	size = 1
	fallback := get(windowsDesktopDownloadPath)
	if fallback.Header().Get("Location") != two.Header().Get("Location") {
		t.Fatal("incomplete upload must fall back to server")
	}
	if one.Header().Get("Cache-Control") != desktopUpdateMetadataCache {
		t.Fatal("signed URL must not be cached")
	}
}

func TestDesktopUpdateInstallerRedirectsToOSSForExistingClients(t *testing.T) {
	root := t.TempDir()
	writeDesktopUpdateFixture(t, root, "win32/x64/latest.json", `{"schemaVersion":1,"enabled":true,"version":"0.2.14","releaseDate":"2026-09-01T00:00:00Z"}`)
	writeDesktopUpdateFixture(t, root, "win32/x64/NarratoAI-PC-Setup-0.2.14.exe", "installer")
	app := newDesktopUpdateTestApp(root)
	app.cfg.DesktopDownloadOSSBucket = "test-bucket"
	app.cfg.DesktopDownloadOSSRegion = "cn-shanghai"
	app.cfg.DubbingAccessKeyID = "test-key"
	app.cfg.DubbingAccessKeySecret = "test-secret"
	old := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = old })
	http.DefaultClient = &http.Client{Transport: desktopOSSRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodHead || r.URL.Host != "test-bucket.oss-cn-shanghai.aliyuncs.com" {
			t.Fatalf("unexpected OSS request %s %s", r.Method, r.URL.Host)
		}
		return &http.Response{StatusCode: http.StatusOK, ContentLength: 9, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}

	accelerated := httptest.NewRecorder()
	app.Router().ServeHTTP(accelerated, httptest.NewRequest(http.MethodGet, "/desktop-updates/win32/x64/NarratoAI-PC-Setup-0.2.14.exe", nil))
	location, err := url.Parse(accelerated.Header().Get("Location"))
	if err != nil || accelerated.Code != http.StatusFound || location.Host != "test-bucket.oss-cn-shanghai.aliyuncs.com" || location.Query().Get("Signature") == "" {
		t.Fatalf("expected OSS redirect, got status=%d location=%q", accelerated.Code, accelerated.Header().Get("Location"))
	}

	server := httptest.NewRecorder()
	app.Router().ServeHTTP(server, httptest.NewRequest(http.MethodGet, "/desktop-updates/win32/x64/NarratoAI-PC-Setup-0.2.14.exe?source=server", nil))
	if server.Code != http.StatusOK || server.Body.String() != "installer" {
		t.Fatalf("server fallback status=%d body=%q", server.Code, server.Body.String())
	}
}
