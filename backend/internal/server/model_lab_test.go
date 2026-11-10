package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func labFixture(t *testing.T) (*App, *httptest.Server, *int32) {
	t.Helper()
	a := newTestApp(t)
	a.avatarMVP.options.DataRoot = t.TempDir()
	var submits int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/openapi/v2/query":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if strings.HasPrefix(body["taskId"], "bad") {
				_, _ = w.Write([]byte(`{"taskId":"` + body["taskId"] + `","status":"FAILED","errorCode":"500"}`))
				return
			}
			_, _ = w.Write([]byte(`{"taskId":"` + body["taskId"] + `","status":"SUCCESS","results":[{"url":"https://cdn.example.com/out.mp3","outputType":"mp3"}],"usage":{"thirdPartyConsumeMoney":"0.03"}}`))
		case strings.HasPrefix(r.URL.Path, "/openapi/v2/"):
			n := atomic.AddInt32(&submits, 1)
			id := "ok-" + r.URL.Path[len("/openapi/v2/"):]
			if strings.Contains(r.URL.Path, "suno") {
				id = "bad-" + id
			}
			_ = n
			_, _ = w.Write([]byte(`{"taskId":"` + strings.ReplaceAll(id, "/", "_") + `","status":"RUNNING"}`))
		}
	}))
	t.Cleanup(provider.Close)
	t.Setenv("RUNNINGHUB_BASE_URL", provider.URL)
	t.Setenv("RUNNINGHUB_API_KEY", "lab-key")
	a.modelLab.download = func(ctx context.Context, url, path string) error {
		return os.WriteFile(path, []byte("ID3fake"), 0o600)
	}
	a.modelLab.pollInterval = 10 * time.Millisecond
	return a, provider, &submits
}

func TestModelLabRunsEachModelOnceAndRecordsCostAndFailures(t *testing.T) {
	a, _, submits := labFixture(t)
	models := []string{"mureka-ai/mureka-v9/generate-bgm", "mureka-ai/mureka-v8/generate-bgm", "rhart-audio/suno-v5.5/single"}
	_, err := a.modelLab.create(context.Background(), "admin", labRequest{Kind: "music", Prompt: "轻快科技感背景音乐", Models: models, Confirm: 2})
	if err == nil || !strings.Contains(err.Error(), "确认") {
		t.Fatalf("confirmation count mismatch must be rejected: %v", err)
	}
	run, err := a.modelLab.create(context.Background(), "admin", labRequest{Kind: "music", Prompt: "轻快科技感背景音乐", Models: models, Confirm: 3})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := a.modelLab.get(run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "done" {
			run = got
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lab run did not finish: %#v", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if atomic.LoadInt32(submits) != 3 {
		t.Fatalf("each model must be submitted exactly once, got %d", *submits)
	}
	var ok, failed int
	for _, r := range run.Results {
		switch r.Status {
		case "succeeded":
			ok++
			if r.Cost != "0.03" || r.File == "" || r.Seconds < 0 || r.Sent == nil {
				t.Fatalf("succeeded result incomplete: %#v", r)
			}
			if _, err := os.Stat(filepath.Join(a.modelLab.dir(run.ID), r.File)); err != nil {
				t.Fatal("result file missing")
			}
		case "failed":
			failed++
		}
	}
	if ok != 2 || failed != 1 {
		t.Fatalf("results: ok=%d failed=%d %#v", ok, failed, run.Results)
	}
	if err := a.modelLab.rate(run.ID, models[0], 4, "节奏好"); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.modelLab.get(run.ID); got.Results[0].Score != 4 || got.Results[0].Note != "节奏好" {
		t.Fatal("rating not persisted")
	}
}

func TestModelLabDrivesTalkingAvatarsWithTheGivenAudio(t *testing.T) {
	a, _, _ := labFixture(t)
	run, err := a.modelLab.create(context.Background(), "admin", labRequest{Kind: "avatar", Images: []string{"https://cdn.example.com/face.png"}, Audio: "https://cdn.example.com/voice.wav", Models: []string{"kling-v2-ai-avatar-standard/image-audio-to-video"}, Confirm: 1, holdForTest: true})
	if err != nil {
		t.Fatal(err)
	}
	if sent := run.Results[0].Sent; sent["imageUrl"] != "https://cdn.example.com/face.png" || sent["audioUrl"] != "https://cdn.example.com/voice.wav" {
		t.Fatalf("avatar body: %#v", sent)
	}
	if _, err := a.modelLab.create(context.Background(), "admin", labRequest{Kind: "avatar", Images: []string{"https://cdn.example.com/face.png"}, Audio: "http://insecure/voice.wav", Models: []string{"kling-v2-ai-avatar-standard/image-audio-to-video"}, Confirm: 1, holdForTest: true}); err == nil {
		t.Fatal("non-https audio accepted")
	}
}

func TestModelLabValidatesModelsKindAndLimits(t *testing.T) {
	a, _, submits := labFixture(t)
	bad := []labRequest{
		{Kind: "music", Prompt: "p", Models: []string{"kling-v3.0-pro/image-to-video"}, Confirm: 1},
		{Kind: "music", Prompt: "p", Models: []string{"not/a/model"}, Confirm: 1},
		{Kind: "music", Prompt: "p", Models: []string{"mureka-ai/mureka-v9/generate-bgm", "mureka-ai/mureka-v9/generate-bgm"}, Confirm: 2},
		{Kind: "i2v", Prompt: "p", Models: []string{"kling-v3.0-pro/image-to-video"}, Confirm: 1},
		{Kind: "music", Prompt: "p", Models: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}, Confirm: 9},
	}
	for i, r := range bad {
		if _, err := a.modelLab.create(context.Background(), "admin", r); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	if atomic.LoadInt32(submits) != 0 {
		t.Fatal("invalid requests created orders")
	}
}

func TestModelLabResumesSubmittedOrdersWithoutResubmitting(t *testing.T) {
	a, _, submits := labFixture(t)
	run, err := a.modelLab.create(context.Background(), "admin", labRequest{Kind: "music", Prompt: "p", Models: []string{"mureka-ai/mureka-v9/generate-bgm"}, Confirm: 1, holdForTest: true})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a restart after the order was accepted but before completion.
	run.Results[0].Status, run.Results[0].TaskID, run.Status = "running", "ok-resumed", "running"
	if err := a.modelLab.save(run); err != nil {
		t.Fatal(err)
	}
	unknown := run
	unknown.ID = "lab-unknown"
	unknown.Results = append([]labResult(nil), run.Results...)
	unknown.Results[0].Status, unknown.Results[0].TaskID = "submitting", ""
	if err := a.modelLab.save(unknown); err != nil {
		t.Fatal(err)
	}
	a.modelLab.resume()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, _ := a.modelLab.get(run.ID)
		if got.Status == "done" {
			if got.Results[0].Status != "succeeded" {
				t.Fatalf("resumed order not completed: %#v", got.Results[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("resume did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, _ := a.modelLab.get("lab-unknown")
	if got.Results[0].Status != "unknown" {
		t.Fatalf("an unconfirmed submission must be marked unknown, not resubmitted: %#v", got.Results[0])
	}
	if atomic.LoadInt32(submits) != 0 {
		t.Fatalf("resume resubmitted %d orders", *submits)
	}
}

func TestModelLabRoutesAreAdminOnly(t *testing.T) {
	f := newTTSWorkflowFixture(t, "siliconflow")
	if r := request(f.app, "GET", "/api/v1/admin/model-lab/runs", nil, f.token); r.Code != http.StatusForbidden {
		t.Fatalf("non-admin reached the lab: %d", r.Code)
	}
	if r := request(f.app, "POST", "/api/v1/admin/model-lab/runs", map[string]any{"kind": "music"}, f.token); r.Code != http.StatusForbidden {
		t.Fatalf("non-admin could start a paid lab run: %d", r.Code)
	}
	r := request(f.app, "GET", "/api/v1/avatar-mvp/media/models?kind=t2v", nil, f.token)
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "kling") || !strings.Contains(r.Body.String(), "capabilities") {
		t.Fatalf("media catalog: %d", r.Code)
	}
	if err := f.app.db.Model(&f.user).Update("is_admin", true).Error; err != nil {
		t.Fatal(err)
	}
	if r := request(f.app, "GET", "/api/v1/admin/model-lab/runs", nil, f.token); r.Code != http.StatusOK {
		t.Fatalf("admin list: %d", r.Code)
	}
	if r := request(f.app, "POST", "/api/v1/admin/model-lab/runs", map[string]any{"kind": "music", "prompt": "p", "models": []string{"mureka-ai/mureka-v9/generate-bgm"}, "confirm": 0}, f.token); r.Code != http.StatusBadRequest {
		t.Fatalf("admin run without confirmation must be rejected: %d", r.Code)
	}
}
