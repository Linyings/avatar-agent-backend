package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestWorkflowCardPreviewServesOnlyCompressedMedia(t *testing.T) {
	a := newTestApp(t)
	root := t.TempDir()
	a.avatarMVP.options.RuntimeRoot = root
	os.MkdirAll(filepath.Join(root, "catalog"), 0700)
	os.WriteFile(filepath.Join(root, "native-styles.json"), []byte(`[{"id":"mg-sample","name":"sample","entry":"demos/sample/index.html"}]`), 0600)
	os.WriteFile(filepath.Join(root, "catalog", "mg-sample.mp4"), []byte("original-video"), 0600)
	os.WriteFile(filepath.Join(root, "catalog", "mg-sample.jpg"), []byte("original-poster"), 0600)
	r := gin.New()
	r.GET("/catalog/:style/card-preview", a.workflowStylePreview)
	r.GET("/catalog/:style/card-poster", a.workflowStylePreview)
	missing := httptest.NewRecorder()
	r.ServeHTTP(missing, httptest.NewRequest("GET", "/catalog/mg-sample/card-preview", nil))
	if missing.Code != 404 {
		t.Fatalf("missing compressed preview fell back to original: %d %s", missing.Code, missing.Body)
	}
	for _, tc := range []struct{ suffix, ext, mime string }{{"card-preview", "mp4", "video/mp4"}, {"card-poster", "jpg", "image/jpeg"}} {
		os.WriteFile(filepath.Join(root, "catalog", "mg-sample-card."+tc.ext), []byte("compressed-media"), 0600)
		request := httptest.NewRequest("GET", "/catalog/mg-sample/"+tc.suffix, nil)
		request.Header.Set("Range", "bytes=0-9")
		recorder := httptest.NewRecorder()
		r.ServeHTTP(recorder, request)
		if recorder.Code != 206 || recorder.Body.String() != "compressed" || recorder.Header().Get("Content-Type") != tc.mime {
			t.Fatalf("wrong card media: %d %q %s", recorder.Code, recorder.Body, recorder.Header().Get("Content-Type"))
		}
	}
	styles, err := loadWorkflowStyles(root)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(styles[0])
	var style map[string]any
	json.Unmarshal(data, &style)
	if style["card_preview"] != "/api/v1/avatar-mvp/catalog/mg-sample/card-preview" || style["card_poster"] != "/api/v1/avatar-mvp/catalog/mg-sample/card-poster" {
		t.Fatalf("compressed media absent from catalog: %s", data)
	}
}
