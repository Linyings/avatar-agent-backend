package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Only server-resolved, published installer names can be signed. No object key
// or destination URL comes from the download request.
func signedDesktopOSSURL(method, bucket, region, name, keyID, secret string, expires time.Time) string {
	object := "desktop-downloads/win32/x64/" + name
	expiry := strconv.FormatInt(expires.Unix(), 10)
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(method + "\n\n\n" + expiry + "\n/" + bucket + "/" + object))
	query := url.Values{"OSSAccessKeyId": {keyID}, "Expires": {expiry}, "Signature": {base64.StdEncoding.EncodeToString(mac.Sum(nil))}}
	return "https://" + bucket + ".oss-" + region + ".aliyuncs.com/" + object + "?" + query.Encode()
}

func (a *App) desktopInstallerOSSURL(ctx context.Context, release windowsDesktopReleaseResponse) string {
	if a.cfg.DesktopDownloadOSSBucket == "" || a.cfg.DubbingAccessKeyID == "" || a.cfg.DubbingAccessKeySecret == "" {
		return ""
	}
	sign := func(method string) string {
		return signedDesktopOSSURL(method, a.cfg.DesktopDownloadOSSBucket, a.cfg.DesktopDownloadOSSRegion, release.FileName, a.cfg.DubbingAccessKeyID, a.cfg.DubbingAccessKeySecret, time.Now().Add(time.Hour))
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, sign(http.MethodHead), nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	// New releases continue to download from the server until their OSS upload
	// is complete. An unavailable bucket must not break the existing download.
	if resp.StatusCode != http.StatusOK || resp.ContentLength != release.FileSize {
		return ""
	}
	return sign(http.MethodGet)
}
