package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	desktopUpdateMetadataCache = "no-cache, no-store, must-revalidate"
	desktopUpdatePayloadCache  = "public, max-age=31536000, immutable"
	windowsDesktopDownloadPath = "/api/v1/desktop/download/windows"
)

var (
	desktopUpdatePayloadName           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.(?:nupkg|exe|zip|dmg)$`)
	desktopUpdateInstallerMetadataName = regexp.MustCompile(`^NarratoAI-PC-Setup-[0-9]+\.[0-9]+\.[0-9]+\.json$`)
	desktopVersionPattern              = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
)

type desktopReleaseManifest struct {
	SchemaVersion int       `json:"schemaVersion"`
	Enabled       bool      `json:"enabled"`
	Version       string    `json:"version"`
	ReleaseDate   time.Time `json:"releaseDate"`
}

type windowsDesktopReleaseResponse struct {
	Platform     string    `json:"platform"`
	Architecture string    `json:"architecture"`
	Version      string    `json:"version"`
	FileName     string    `json:"file_name"`
	FileSize     int64     `json:"file_size"`
	PublishedAt  time.Time `json:"published_at"`
	DownloadURL  string    `json:"download_url"`
}

func isDesktopUpdatePath(relativePath string) bool {
	if relativePath == "" || strings.Contains(relativePath, "\\") {
		return false
	}
	parts := strings.Split(relativePath, "/")
	if len(parts) != 3 {
		return false
	}
	platform, arch, name := parts[0], parts[1], parts[2]
	if !((platform == "win32" && arch == "x64") || (platform == "darwin" && (arch == "x64" || arch == "arm64"))) {
		return false
	}
	if name == "latest.json" || name == "RELEASES" || name == "RELEASES.json" || desktopUpdateInstallerMetadataName.MatchString(name) {
		return true
	}
	return desktopUpdatePayloadName.MatchString(name)
}

func resolveDesktopUpdateFile(root, wildcardPath string) (string, os.FileInfo, bool) {
	relativePath := strings.TrimPrefix(wildcardPath, "/")
	if !isDesktopUpdatePath(relativePath) {
		return "", nil, false
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", nil, false
	}
	resolvedFile, err := filepath.EvalSymlinks(filepath.Join(resolvedRoot, filepath.FromSlash(relativePath)))
	if err != nil {
		return "", nil, false
	}
	relativeResolved, err := filepath.Rel(resolvedRoot, resolvedFile)
	if err != nil || relativeResolved == ".." || strings.HasPrefix(relativeResolved, ".."+string(filepath.Separator)) {
		return "", nil, false
	}
	info, err := os.Stat(resolvedFile)
	if err != nil || !info.Mode().IsRegular() {
		return "", nil, false
	}
	return resolvedFile, info, true
}

func resolveLatestWindowsDesktopRelease(root string) (windowsDesktopReleaseResponse, error) {
	manifestPath, _, ok := resolveDesktopUpdateFile(root, "/win32/x64/latest.json")
	if !ok {
		return windowsDesktopReleaseResponse{}, errors.New("desktop release manifest unavailable")
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return windowsDesktopReleaseResponse{}, fmt.Errorf("open desktop release manifest: %w", err)
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return windowsDesktopReleaseResponse{}, fmt.Errorf("read desktop release manifest: %w", err)
	}
	if len(payload) > 64<<10 {
		return windowsDesktopReleaseResponse{}, errors.New("desktop release manifest is too large")
	}
	var manifest desktopReleaseManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return windowsDesktopReleaseResponse{}, fmt.Errorf("decode desktop release manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 || !manifest.Enabled || !desktopVersionPattern.MatchString(manifest.Version) || manifest.ReleaseDate.IsZero() {
		return windowsDesktopReleaseResponse{}, errors.New("desktop release manifest is not publishable")
	}
	name := "NarratoAI-PC-Setup-" + manifest.Version + ".exe"
	_, info, ok := resolveDesktopUpdateFile(root, "/win32/x64/"+name)
	if !ok || info.Size() <= 0 {
		return windowsDesktopReleaseResponse{}, errors.New("desktop installer unavailable")
	}
	return windowsDesktopReleaseResponse{
		Platform: "windows", Architecture: "x64", Version: manifest.Version,
		FileName: name, FileSize: info.Size(), PublishedAt: manifest.ReleaseDate,
		DownloadURL: windowsDesktopDownloadPath,
	}, nil
}

func desktopUpdateContentType(name string) string {
	switch {
	case name == "latest.json" || name == "RELEASES.json" || desktopUpdateInstallerMetadataName.MatchString(name):
		return "application/json; charset=utf-8"
	case name == "RELEASES":
		return "text/plain; charset=utf-8"
	case strings.HasSuffix(name, ".exe"):
		return "application/vnd.microsoft.portable-executable"
	case strings.HasSuffix(name, ".nupkg") || strings.HasSuffix(name, ".zip"):
		return "application/zip"
	case strings.HasSuffix(name, ".dmg"):
		return "application/x-apple-diskimage"
	default:
		return "application/octet-stream"
	}
}

func (a *App) desktopUpdateFile(c *gin.Context) {
	path, info, ok := resolveDesktopUpdateFile(a.cfg.DesktopUpdateRoot, c.Param("filePath"))
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	defer file.Close()

	name := info.Name()
	// The desktop updater uses the versioned static feed URL directly. Redirect
	// the currently published installer to the private OSS object so existing
	// clients (including older releases) get the accelerated path without a
	// client update. The explicit server channel adds source=server and keeps
	// the original static download available as a fallback.
	if c.Request.Method == http.MethodGet && c.Query("source") != "server" && strings.HasSuffix(name, ".exe") {
		if release, releaseErr := resolveLatestWindowsDesktopRelease(a.cfg.DesktopUpdateRoot); releaseErr == nil && release.FileName == name {
			if signed := a.desktopInstallerOSSURL(c.Request.Context(), release); signed != "" {
				c.Header("Cache-Control", desktopUpdateMetadataCache)
				c.Redirect(http.StatusFound, signed)
				return
			}
		}
	}
	cacheControl := desktopUpdatePayloadCache
	if name == "latest.json" || name == "RELEASES" || name == "RELEASES.json" || desktopUpdateInstallerMetadataName.MatchString(name) {
		cacheControl = desktopUpdateMetadataCache
	}
	c.Header("Cache-Control", cacheControl)
	c.Header("Content-Type", desktopUpdateContentType(name))
	c.Header("X-Content-Type-Options", "nosniff")
	http.ServeContent(c.Writer, c.Request, name, info.ModTime(), file)
}

func (a *App) latestWindowsDesktopRelease(c *gin.Context) {
	release, err := resolveLatestWindowsDesktopRelease(a.cfg.DesktopUpdateRoot)
	if err != nil {
		log.Printf("resolve latest Windows desktop release: %v", err)
		fail(c, http.StatusServiceUnavailable, "Windows installer is temporarily unavailable")
		return
	}
	ok(c, release)
}

func (a *App) downloadLatestWindowsDesktopRelease(c *gin.Context) {
	release, err := resolveLatestWindowsDesktopRelease(a.cfg.DesktopUpdateRoot)
	if err != nil {
		log.Printf("resolve Windows desktop download: %v", err)
		fail(c, http.StatusServiceUnavailable, "Windows installer is temporarily unavailable")
		return
	}
	c.Header("Cache-Control", desktopUpdateMetadataCache)
	// HEAD checks the published local artifact without following a cross-origin
	// redirect: browser availability checks must not depend on OSS CORS policy.
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		return
	}
	if c.Query("source") != "server" {
		if signed := a.desktopInstallerOSSURL(c.Request.Context(), release); signed != "" {
			c.Redirect(http.StatusFound, signed)
			return
		}
	}
	serverPath := "/desktop-updates/win32/x64/" + url.PathEscape(release.FileName) + "?source=server"
	c.Redirect(http.StatusFound, serverPath)
}
