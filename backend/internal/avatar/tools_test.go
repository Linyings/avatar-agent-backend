package avatar

import (
	"path/filepath"
	"testing"
)

func TestResolvePathsBeforeMediaWorkingDirectoryChanges(t *testing.T) {
	o := (Options{DataRoot: "data/avatar-mvp", RuntimeRoot: "avatar-runtime", Python: filepath.Join(".venv-avatar", "Scripts", "python.exe"), Node: "node", FFmpeg: "ffmpeg", FFprobe: "ffprobe", ASRModelDir: "models/whisper"}).ResolvePaths()
	for _, path := range []string{o.DataRoot, o.RuntimeRoot, o.Python, o.ASRModelDir} {
		if !filepath.IsAbs(path) {
			t.Fatalf("path still depends on subprocess cwd: %s", path)
		}
	}
	if o.Node != "node" || o.FFmpeg != "ffmpeg" || o.FFprobe != "ffprobe" {
		t.Fatal("PATH executable names were changed")
	}
	if (Options{}).ResolvePaths().ASRModelDir != "" {
		t.Fatal("empty optional model path was populated")
	}
}
