package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/config"
)

func newDesktopUpdateTestApp(root string) *App {
	return New(config.Config{
		Debug:             true,
		Environment:       "testing",
		APIv1Prefix:       "/api/v1",
		CORSOrigins:       []string{"*"},
		SecretKey:         "test-secret-key-test-secret-key-1234",
		PromptTemplateDir: "../../prompt",
		DesktopUpdateRoot: root,
	}, nil)
}

func writeDesktopUpdateFixture(t *testing.T, root, relativePath, contents string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestDesktopUpdatesAreDisabledWithoutAConfiguredRoot(t *testing.T) {
	app := newDesktopUpdateTestApp("")
	req := httptest.NewRequest(http.MethodGet, "/desktop-updates/win32/x64/latest.json", nil)
	rec := httptest.NewRecorder()

	app.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestDesktopUpdatesServeMetadataAndRangedPayloads(t *testing.T) {
	root := t.TempDir()
	manifest := `{"schemaVersion":1,"enabled":true,"version":"0.2.1"}`
	payload := "0123456789abcdef"
	writeDesktopUpdateFixture(t, root, "win32/x64/latest.json", manifest)
	writeDesktopUpdateFixture(t, root, "win32/x64/RELEASES", "release metadata")
	installerMetadata := `{"version":"0.2.1","fileName":"NarratoAI-PC-Setup-0.2.1.exe","size":42,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	writeDesktopUpdateFixture(t, root, "win32/x64/NarratoAI-PC-Setup-0.2.1.json", installerMetadata)
	writeDesktopUpdateFixture(t, root, "win32/x64/NarratoAI-PC-0.2.1-full.nupkg", payload)
	app := newDesktopUpdateTestApp(root)

	t.Run("manifest", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/desktop-updates/win32/x64/latest.json", nil)
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache, no-store, must-revalidate" {
			t.Fatalf("Cache-Control = %q", got)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Fatalf("Content-Type = %q", got)
		}
		if rec.Body.String() != manifest {
			t.Fatalf("body = %q", rec.Body.String())
		}
	})

	t.Run("installer metadata", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/desktop-updates/win32/x64/NarratoAI-PC-Setup-0.2.1.json", nil)
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != desktopUpdateMetadataCache {
			t.Fatalf("Cache-Control = %q", got)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Fatalf("Content-Type = %q", got)
		}
		if rec.Body.String() != installerMetadata {
			t.Fatalf("body = %q", rec.Body.String())
		}
	})

	t.Run("range", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/desktop-updates/win32/x64/NarratoAI-PC-0.2.1-full.nupkg", nil)
		req.Header.Set("Range", "bytes=2-5")
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, req)

		if rec.Code != http.StatusPartialContent {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if rec.Body.String() != payload[2:6] {
			t.Fatalf("body = %q", rec.Body.String())
		}
		if got := rec.Header().Get("Content-Range"); got != "bytes 2-5/16" {
			t.Fatalf("Content-Range = %q", got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Fatalf("Cache-Control = %q", got)
		}
	})

	t.Run("head", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodHead, "/desktop-updates/win32/x64/NarratoAI-PC-0.2.1-full.nupkg", nil)
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("HEAD body length = %d", rec.Body.Len())
		}
		if got := rec.Header().Get("Content-Length"); got != "16" {
			t.Fatalf("Content-Length = %q", got)
		}
	})
}

func TestDesktopUpdatesRejectUnsafeOrUnpublishedPaths(t *testing.T) {
	root := t.TempDir()
	writeDesktopUpdateFixture(t, root, "secret.txt", "secret")
	writeDesktopUpdateFixture(t, root, "win32/x64/private.log", "private")
	app := newDesktopUpdateTestApp(root)

	paths := []string{
		"/desktop-updates/",
		"/desktop-updates/secret.txt",
		"/desktop-updates/win32/x64/private.log",
		"/desktop-updates/win32/x64/NarratoAI-PC-Setup-0.2.1-extra.json",
		"/desktop-updates/win32/arm64/latest.json",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			app.Router().ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
			}
		})
	}
}

func TestLatestWindowsDesktopReleaseMetadataAndDownload(t *testing.T) {
	root := t.TempDir()
	writeDesktopUpdateFixture(t, root, "win32/x64/latest.json", `{
		"schemaVersion":1,"enabled":true,"version":"0.2.4",
		"releaseDate":"2026-08-03T12:00:00Z",
		"releaseNotes":["Initial desktop release"],"packageSize":42,
		"artifactSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}`)
	writeDesktopUpdateFixture(t, root, "win32/x64/NarratoAI-PC-Setup-0.2.4.exe", "installer")
	app := newDesktopUpdateTestApp(root)

	metadata := httptest.NewRecorder()
	app.Router().ServeHTTP(metadata, httptest.NewRequest(http.MethodGet, "/api/v1/desktop/releases/windows/latest", nil))
	if metadata.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", metadata.Code, metadata.Body.String())
	}
	var release windowsDesktopReleaseResponse
	if err := json.Unmarshal(metadata.Body.Bytes(), &release); err != nil {
		t.Fatal(err)
	}
	if release.Version != "0.2.4" || release.FileName != "NarratoAI-PC-Setup-0.2.4.exe" || release.FileSize != 9 {
		t.Fatalf("release = %#v", release)
	}
	if release.DownloadURL != "/api/v1/desktop/download/windows" {
		t.Fatalf("download_url = %q", release.DownloadURL)
	}

	download := httptest.NewRecorder()
	app.Router().ServeHTTP(download, httptest.NewRequest(http.MethodGet, "/api/v1/desktop/download/windows", nil))
	if download.Code != http.StatusFound {
		t.Fatalf("status = %d", download.Code)
	}
	if got := download.Header().Get("Location"); got != "/desktop-updates/win32/x64/NarratoAI-PC-Setup-0.2.4.exe?source=server" {
		t.Fatalf("Location = %q", got)
	}

	head := httptest.NewRecorder()
	app.Router().ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/api/v1/desktop/download/windows", nil))
	if head.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d", head.Code)
	}
	if got := head.Header().Get("Location"); got != "" {
		t.Fatalf("HEAD Location = %q", got)
	}
	if head.Body.Len() != 0 {
		t.Fatalf("HEAD body length = %d", head.Body.Len())
	}
}

func TestLatestWindowsDesktopReleaseUnavailable(t *testing.T) {
	manifest := `{"schemaVersion":1,"enabled":true,"version":"0.2.4","releaseDate":"2026-08-03T12:00:00Z"}`

	tests := []struct {
		name      string
		configure func(t *testing.T, root string)
	}{
		{name: "missing manifest", configure: func(t *testing.T, root string) {}},
		{name: "malformed manifest", configure: func(t *testing.T, root string) {
			writeDesktopUpdateFixture(t, root, "win32/x64/latest.json", "{")
		}},
		{name: "disabled manifest", configure: func(t *testing.T, root string) {
			writeDesktopUpdateFixture(t, root, "win32/x64/latest.json", `{"schemaVersion":1,"enabled":false,"version":"0.2.4","releaseDate":"2026-08-03T12:00:00Z"}`)
		}},
		{name: "unsupported schema", configure: func(t *testing.T, root string) {
			writeDesktopUpdateFixture(t, root, "win32/x64/latest.json", `{"schemaVersion":2,"enabled":true,"version":"0.2.4","releaseDate":"2026-08-03T12:00:00Z"}`)
		}},
		{name: "invalid version", configure: func(t *testing.T, root string) {
			writeDesktopUpdateFixture(t, root, "win32/x64/latest.json", `{"schemaVersion":1,"enabled":true,"version":"v0.2.4","releaseDate":"2026-08-03T12:00:00Z"}`)
		}},
		{name: "missing installer", configure: func(t *testing.T, root string) {
			writeDesktopUpdateFixture(t, root, "win32/x64/latest.json", manifest)
		}},
		{name: "zero-byte installer", configure: func(t *testing.T, root string) {
			writeDesktopUpdateFixture(t, root, "win32/x64/latest.json", manifest)
			writeDesktopUpdateFixture(t, root, "win32/x64/NarratoAI-PC-Setup-0.2.4.exe", "")
		}},
		{name: "escaping installer symlink", configure: func(t *testing.T, root string) {
			writeDesktopUpdateFixture(t, root, "win32/x64/latest.json", manifest)
			outside := filepath.Join(t.TempDir(), "NarratoAI-PC-Setup-0.2.4.exe")
			if err := os.WriteFile(outside, []byte("installer"), 0o644); err != nil {
				t.Fatalf("write external installer: %v", err)
			}
			link := filepath.Join(root, "win32", "x64", "NarratoAI-PC-Setup-0.2.4.exe")
			if err := os.Symlink(outside, link); err != nil {
				t.Skipf("symlinks unsupported: %v", err)
			}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.configure(t, root)
			app := newDesktopUpdateTestApp(root)

			requests := []struct {
				method string
				path   string
			}{
				{method: http.MethodGet, path: "/api/v1/desktop/releases/windows/latest"},
				{method: http.MethodGet, path: "/api/v1/desktop/download/windows"},
				{method: http.MethodHead, path: "/api/v1/desktop/download/windows"},
			}
			for _, request := range requests {
				rec := httptest.NewRecorder()
				app.Router().ServeHTTP(rec, httptest.NewRequest(request.method, request.path, nil))
				if rec.Code != http.StatusServiceUnavailable {
					t.Fatalf("%s %s status = %d, body = %s", request.method, request.path, rec.Code, rec.Body.String())
				}
			}
		})
	}
}
