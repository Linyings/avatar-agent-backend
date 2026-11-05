package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkflowNativeStylesDoNotPublishMissingSourceOrPreview(t *testing.T) {
	root := t.TempDir()
	manifest := `[{"id":"mg-line-art","name":"连续线条","version":"mg-native/1","source":"02-line-art","entry":"demos/02-line-art/index.html","signature":"一笔画"}]`
	os.WriteFile(filepath.Join(root, "native-styles.json"), []byte(manifest), 0600)
	styles, err := loadWorkflowStyles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(styles) != 3 {
		t.Fatalf("legacy styles or native style missing: %d", len(styles))
	}
	s := styles[0]
	if s.ID != "mg-line-art" || s.Published {
		t.Fatalf("missing assets published: %#v", s)
	}
	os.MkdirAll(filepath.Join(root, "catalog"), 0700)
	os.WriteFile(filepath.Join(root, "catalog", "mg-line-art.mp4"), []byte("preview"), 0600)
	styles, _ = loadWorkflowStyles(root)
	if styles[0].Published {
		t.Fatal("preview alone published as source-backed style")
	}
	os.MkdirAll(filepath.Join(root, "templates", "mg-line-art"), 0700)
	os.WriteFile(filepath.Join(root, "templates", "mg-line-art", "SOURCE.json"), []byte(`{"license":"MIT"}`), 0600)
	os.MkdirAll(filepath.Join(root, "mg-upstream", "demos", "02-line-art"), 0700)
	os.WriteFile(filepath.Join(root, "mg-upstream", "demos", "02-line-art", "index.html"), []byte("source"), 0600)
	for _, name := range []string{"catalog/mg-line-art.jpg", "catalog/mg-line-art-reference.mp4", "catalog/mg-line-art-source.zip", "templates/mg-line-art/original-prompt.md"} {
		os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte("fixture"), 0600)
	}
	styles, _ = loadWorkflowStyles(root)
	if !styles[0].Published {
		t.Fatal("complete style unavailable")
	}
	data, _ := json.Marshal(styles[0])
	if len(data) == 0 {
		t.Fatal("missing catalog response")
	}
}

func TestWorkflowNativeStyleManifestRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "native-styles.json"), []byte(`[{"id":"../outside","entry":"../../secret"}]`), 0600)
	if _, err := loadWorkflowStyles(root); err == nil {
		t.Fatal("unsafe manifest accepted")
	}
}
