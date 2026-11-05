package providers

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/config"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Opt-in integration test. Credentials remain in memory; output contains no signed URLs.
func TestDubbingLive(t *testing.T) {
	if os.Getenv("DUBBING_LIVE_TEST") != "1" {
		t.Skip("live provider test disabled")
	}
	credentials, err := os.ReadFile(os.Getenv("DUBBING_LIVE_CREDENTIAL_FILE"))
	if err != nil {
		t.Fatal("credential file unreadable")
	}
	tokens := regexp.MustCompile(`[A-Za-z0-9+/=_-]{20,}`).FindAllString(string(credentials), -1)
	var id, secret string
	for _, token := range tokens {
		if strings.HasPrefix(token, "LTAI") {
			id = token
		} else {
			secret = token
		}
	}
	if id == "" || secret == "" {
		t.Fatal("credential file format invalid")
	}
	client := NewDubbingClient(config.Config{DubbingAccessKeyID: id, DubbingAccessKeySecret: secret, DubbingOSSBucket: "narratoai-dubbing-audio-shanghai"})
	audio, err := os.ReadFile(os.Getenv("DUBBING_LIVE_AUDIO"))
	if err != nil {
		t.Fatal("audio unreadable")
	}
	duration, err := DubbingWAVDuration(audio)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "dubbing/" + uuid.NewString() + "/" + strings.ReplaceAll(uuid.NewString(), "-", "") + "/"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	job, err := client.Submit(ctx, prefix+"input.wav", audio)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("submitted job=%s duration=%.3fs", job, duration)
	for {
		result, err := client.Query(ctx, job)
		if err != nil {
			t.Fatal(err)
		}
		if result["State"] == "Fail" {
			t.Fatal("provider job failed")
		}
		if result["State"] == "Success" {
			var details struct {
				Code     string
				Duration float64
				Data     string
			}
			raw, _ := result["Result"].(string)
			if json.Unmarshal([]byte(raw), &details) != nil || details.Code != "Success" {
				t.Fatal("invalid result")
			}
			var outputs struct {
				Result []struct {
					File string `json:"file"`
				} `json:"result"`
			}
			if json.Unmarshal([]byte(details.Data), &outputs) != nil {
				t.Fatal("invalid outputs")
			}
			count := 0
			for _, file := range outputs.Result {
				if file.File != prefix+"output-vocals.wav" && file.File != prefix+"output-accompaniment.wav" {
					t.Fatal("unexpected result key")
				}
				req, _ := http.NewRequestWithContext(ctx, "GET", client.DownloadURL(file.File), nil)
				resp, err := client.HTTP.Do(req)
				if err != nil {
					t.Fatal("download failed")
				}
				bytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 30<<20))
				resp.Body.Close()
				if resp.StatusCode != 200 || readErr != nil || len(bytes) < 44 {
					t.Fatal("invalid downloaded audio")
				}
				if err = os.WriteFile(filepath.Join(os.Getenv("DUBBING_LIVE_OUTPUT"), filepath.Base(file.File)), bytes, 0600); err != nil {
					t.Fatal(err)
				}
				count++
			}
			if count != 2 {
				t.Fatal("expected vocals and accompaniment")
			}
			t.Logf("success: 2 audio files, provider duration %.3fs", details.Duration)
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("job timed out; query saved job instead of resubmitting")
		case <-time.After(5 * time.Second):
		}
	}
}
