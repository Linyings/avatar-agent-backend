package avatar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type Options struct {
	Provider, Protocol, Model, Resolution, UploadPath, GenerationPath, QueryPath                   string
	SupportsReferenceAudio                                                                         bool
	Enabled, BaselineApproved                                                                      bool
	BaselineRequestID                                                                              string
	BaseURL, APIKey, DataRoot, RuntimeRoot, Python, Node, FFmpeg, FFprobe, ASRBackend, ASRModelDir string
	PollInterval, JobTimeout                                                                       time.Duration
}
type Tools struct{ Options Options }

var ErrQualityRejected = errors.New("素材质量检查未通过")

// ResolvePaths runs before media subprocesses change their working directory.
// Bare executable names continue to use PATH; explicit paths are backend-relative.
func (o Options) ResolvePaths() Options {
	resolve := func(value string) string {
		if value == "" {
			return value
		}
		absolute, err := filepath.Abs(value)
		if err != nil {
			return value
		}
		return absolute
	}
	o.DataRoot = resolve(o.DataRoot)
	o.RuntimeRoot = resolve(o.RuntimeRoot)
	o.ASRModelDir = resolve(o.ASRModelDir)
	for _, binary := range []*string{&o.Python, &o.Node, &o.FFmpeg, &o.FFprobe} {
		if filepath.Dir(*binary) != "." {
			*binary = resolve(*binary)
		}
	}
	return o
}

type Clip struct {
	ID               string    `json:"id"`
	Start            float64   `json:"start"`
	End              float64   `json:"end"`
	Duration         float64   `json:"duration"`
	RequestDuration  int       `json:"request_duration"`
	SourceStart      float64   `json:"source_start"`
	FPS              float64   `json:"fps"`
	Width            int       `json:"width"`
	Height           int       `json:"height"`
	FaceZone         []float64 `json:"face_zone,omitempty"`
	AudioCorrelation float64   `json:"audio_correlation"`
}

func (t Tools) Check(ctx context.Context) error {
	root, err := filepath.Abs(t.Options.RuntimeRoot)
	if err != nil {
		return err
	}
	if _, err = os.Stat(filepath.Join(root, "node_modules", "@remotion", "renderer")); err != nil {
		return fmt.Errorf("渲染依赖未安装，请在 avatar-runtime 执行 npm ci")
	}
	return t.run(ctx, "", "doctor")
}
func (t Tools) Annotate(ctx context.Context, dir string) error {
	return t.run(ctx, dir, "semantics")
}
func (t Tools) Shotbook(ctx context.Context, dir string) error {
	return t.run(ctx, dir, "shotbook")
}
func (t Tools) Prepare(ctx context.Context, dir string) ([]Clip, error) {
	if err := t.run(ctx, dir, "prepare"); err != nil {
		return nil, err
	}
	var clips []Clip
	err := readJSON(filepath.Join(dir, "clips.json"), &clips)
	return clips, err
}
func (t Tools) Verify(ctx context.Context, dir, id string) (Clip, error) {
	if err := t.run(ctx, dir, "verify", id); err != nil {
		return Clip{}, err
	}
	var clip Clip
	err := readJSON(filepath.Join(dir, id, "verified.json"), &clip)
	return clip, err
}
func (t Tools) Render(ctx context.Context, dir string) error { return t.run(ctx, dir, "render") }
func (t Tools) run(ctx context.Context, dir, stage string, args ...string) error {
	root, err := filepath.Abs(t.Options.RuntimeRoot)
	if err != nil {
		return err
	}
	absDir := dir
	if dir != "" {
		absDir, err = filepath.Abs(dir)
		if err != nil {
			return err
		}
	}
	argv := []string{filepath.Join(root, "pipeline.py"), stage, "--task-dir", absDir, "--ffmpeg", t.Options.FFmpeg, "--ffprobe", t.Options.FFprobe, "--node", t.Options.Node, "--asr-backend", t.Options.ASRBackend, "--asr-model-dir", t.Options.ASRModelDir}
	argv = append(argv, args...)
	cmd := exec.CommandContext(ctx, t.Options.Python, argv...)
	cmd.Dir = root
	var tail limitedOutput
	cmd.Stdout = &tail
	cmd.Stderr = &tail
	if err = cmd.Run(); err != nil {
		if dir != "" {
			_ = os.WriteFile(filepath.Join(dir, stage+".log"), tail.Bytes(), 0600)
		} else {
			_ = os.MkdirAll(filepath.Join(root, ".cache"), 0700)
			_ = os.WriteFile(filepath.Join(root, ".cache", stage+".log"), tail.Bytes(), 0600)
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 2 {
			if stage == "verify" {
				return ErrQualityRejected
			}
			if stage == "prepare" {
				return fmt.Errorf("配音和口播稿对齐未通过")
			}
		}
		// Tool output is kept in the local task directory; never expose arbitrary paths or subprocess output.
		return fmt.Errorf("媒体步骤 %s 失败，请检查本地任务日志和运行依赖", stage)
	}
	return nil
}

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < 128<<10 {
		remaining := (128 << 10) - b.Len()
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}
func CopyFile(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(to)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	closeErr := dst.Close()
	if err != nil {
		return err
	}
	return closeErr
}
