// Explicit operator command. Catalog generation is never triggered by browsing.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/config"
)

type receipt struct {
	Voice      string          `json:"voice"`
	Model      string          `json:"model"`
	TaskID     string          `json:"provider_task_id"`
	Submission string          `json:"submission"`
	Started    time.Time       `json:"started_at"`
	Finished   *time.Time      `json:"finished_at,omitempty"`
	Seconds    float64         `json:"seconds"`
	Usage      json.RawMessage `json:"usage,omitempty"`
}

func main() {
	paid := flag.Bool("paid", false, "explicitly allow paid catalog generation")
	voice := flag.String("voice", "", "configured provider voice ID")
	flag.Parse()
	if !*paid || *voice == "" {
		fmt.Fprintln(os.Stderr, "需要 --paid 和 --voice；此操作只生成目录试听，不启动制作任务")
		os.Exit(2)
	}
	if err := execute(*voice); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(voice string) (err error) {
	cfg := config.Load()
	if cfg.TTSModel != "doubao-seed-tts-2.0" {
		return fmt.Errorf("此目录预生成命令仅适用于当前异步豆包 TTS；不自动切换模型")
	}
	key := os.Getenv("RUNNINGHUB_API_KEY")
	if key == "" {
		key = cfg.Avatar.APIKey
	}
	if key == "" {
		return fmt.Errorf("服务端未配置配音密钥")
	}
	base := os.Getenv("RUNNINGHUB_BASE_URL")
	if base == "" {
		base = "https://www.runninghub.cn"
	}
	root := cfg.Avatar.DataRoot
	if root == "" {
		root = "/app/data/avatar-mvp"
	}
	root = filepath.Join(root, "workflow-v2", "catalog")
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	id := avatar.Digest([]string{cfg.TTSModel, voice})
	sample := filepath.Join(root, id+".wav")
	if _, err := os.Stat(sample); err == nil {
		fmt.Println("已有真实试听，复用，不提交新订单")
		return nil
	}
	lock, err := os.OpenFile(filepath.Join(root, ".voice-generator.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("试听预生成器已运行或上次中断；请先检查原进程和订单，不自动抢占")
	}
	lock.Close()
	defer os.Remove(lock.Name())
	statePath := filepath.Join(root, id+"-order.json")
	state := receipt{Voice: voice, Model: cfg.TTSModel, Started: time.Now().UTC()}
	if data, err := os.ReadFile(statePath); err == nil {
		if json.Unmarshal(data, &state) != nil {
			return fmt.Errorf("原试听订单记录不可读")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	ctx = avatar.WithTelemetry(ctx, filepath.Join(root, "operations.jsonl"))
	ctx, finish := avatar.BeginOperation(ctx, "catalog_voice", voice)
	defer func() { finish(err) }()
	client := avatar.NewRunningHub(base, key)
	if state.TaskID == "" {
		if state.Submission != "" {
			return fmt.Errorf("原试听提交状态待确认，禁止重复付费")
		}
		state.Submission = "submitting"
		if err := avatar.WriteJSON(statePath, state); err != nil {
			return err
		}
		result, err := client.SubmitSpeech(ctx, avatar.SpeechRequest{Model: cfg.TTSModel, Speaker: voice, Language: cfg.TTSLanguage, Format: "wav", Text: "你好，我会用这个声音，为你讲解视频里的重点内容。"})
		if err != nil {
			state.Submission = "unknown"
			_ = avatar.WriteJSON(statePath, state)
			return fmt.Errorf("试听提交未完成，请核对已保存的供应商状态")
		}
		state.TaskID = result.TaskID
		state.Submission = "submitted"
		if err := avatar.WriteJSON(statePath, state); err != nil {
			return err
		}
	}
	for {
		result, err := client.QuerySpeech(ctx, state.TaskID)
		if len(result.Usage) > 0 {
			state.Usage = result.Usage
		}
		if err == nil && result.Status == "SUCCESS" {
			if err = client.Download(ctx, result.AudioURL(), sample); err != nil {
				return fmt.Errorf("试听下载失败，原订单保留；重跑仅查单")
			}
			now := time.Now().UTC()
			state.Finished = &now
			state.Seconds = now.Sub(state.Started).Seconds()
			state.Submission = "completed"
			if err = avatar.WriteJSON(statePath, state); err != nil {
				return err
			}
			fmt.Printf("真实试听完成；原订单 %s，墙钟 %.2f 秒；费用保留在目录订单报告\n", state.TaskID, state.Seconds)
			return nil
		}
		if result.Status == "FAILED" || result.Status == "FAILURE" {
			state.Submission = "terminal_failed"
			_ = avatar.WriteJSON(statePath, state)
			return fmt.Errorf("试听订单失败；原单保留，不自动重交")
		}
		select {
		case <-ctx.Done():
			_ = avatar.WriteJSON(statePath, state)
			return fmt.Errorf("查询中断；重跑仅查原试听订单")
		case <-time.After(2 * time.Second):
		}
	}
}
