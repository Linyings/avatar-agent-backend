package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/providers"
)

func TestContentMotionUsesAvatarFramesAndReusesValidatedPlanOnRenderRetry(t *testing.T) {
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	root := t.TempDir()
	a.avatarMVP.options.RuntimeRoot = root
	if err := os.WriteFile(filepath.Join(root, "native-styles.json"), []byte(`[{"id":"mg-3d-render","name":"3D","entry":"demos/04/index.html","design":{"features":"柔光和体积","treatment":"sculpted"}}]`), 0600); err != nil {
		t.Fatal(err)
	}
	promptDir := filepath.Join(root, "templates", "mg-3d-render")
	os.MkdirAll(promptDir, 0700)
	os.WriteFile(filepath.Join(promptDir, "original-prompt.md"), []byte("# Creative seed\nSOFT brand original story\n# Signature features\n柔和棚拍与圆润材质\n# How to work\ncopy output"), 0600)
	s.Input.Style = "mg-3d-render"
	s.Input.UseAvatar = true
	s.Input.Direction = "人物与动效融合"
	s.Segments = []avatar.Segment{{ID: "p1", Text: "研发投入110亿元。", Duration: 3, Fingerprint: "speech-v1"}}
	s.Materials = []avatar.Material{{NeedID: "host", SegmentID: "p1", Kind: "avatar", Path: "materials/host.mp4", Duration: 3, Fingerprint: "host-v1"}}
	need := avatar.MaterialNeed{ID: "motion", SegmentID: "p1", Kind: "animation", Duration: 3, Content: "解释研发投入", Template: "data"}
	s.Needs = []avatar.MaterialNeed{need}
	if err := w.save(&s); err != nil {
		t.Fatal(err)
	}
	modelCalls, renderCalls := 0, 0
	w.content = func(_ context.Context, prompt string, input any, frames []string) (json.RawMessage, error) {
		modelCalls++
		in := input.(map[string]any)
		if len(frames) != 1 || frames[0] != "actual-avatar-frame" || in["visual_source"] != "generated-avatar-frames" || in["direction"] != s.Input.Direction {
			t.Fatalf("missing actual context: %v", in)
		}
		if strings.Contains(fmt.Sprint(in["style_reference"]), "SOFT") || !strings.Contains(prompt, "不能复制参考片") {
			t.Fatal("demo story leaked into motion planning")
		}
		return json.RawMessage(`{"version":"content-motion/1","tone":"warm","visual_context":"人物背景为暖色","scenes":[{"quote":"研发投入110亿元","title":"研发投入","layout":"data","elements":[{"label":"110亿元","kind":"coin"}]}]}`), nil
	}
	w.media = func(_ context.Context, stage, dir string) (json.RawMessage, error) {
		if stage == "motion_context" {
			return json.RawMessage(`{"frames":["actual-avatar-frame"],"source":"generated-avatar-frames","avatar_ids":["host"]}`), nil
		}
		if stage != "material" {
			t.Fatalf("unexpected stage %s", stage)
		}
		renderCalls++
		var input struct {
			Plan avatar.MotionPlan `json:"motion_plan"`
		}
		data, _ := os.ReadFile(filepath.Join(dir, "media-input.json"))
		json.Unmarshal(data, &input)
		if err := avatar.ValidateMotionPlan(input.Plan, s.Segments[0].Text); err != nil {
			t.Fatal(err)
		}
		if renderCalls == 1 {
			return nil, fmt.Errorf("temporary local encoder failure")
		}
		os.WriteFile(filepath.Join(dir, "materials", "motion.mp4"), []byte("rendered-current-content"), 0600)
		return json.RawMessage(`{"need_id":"motion","segment_id":"p1","kind":"animation","path":"materials/motion.mp4","duration":3}`), nil
	}
	if err := w.produceMaterial(context.Background(), &s, need, "F07"); err == nil {
		t.Fatal("expected renderer failure")
	}
	if err := w.produceMaterial(context.Background(), &s, need, "F07"); err != nil {
		t.Fatal(err)
	}
	if modelCalls != 1 || renderCalls != 2 || s.Materials[1].MotionPlan == nil {
		t.Fatalf("plan lost or model resubmitted: %d %d", modelCalls, renderCalls)
	}
	before := w.motionFingerprint(&s, need, s.Segments[0])
	s.Materials[0].Fingerprint = "new-host-frame"
	if before == w.motionFingerprint(&s, need, s.Segments[0]) {
		t.Fatal("avatar change reused stale motion")
	}
}

func TestContentMotionCannotUseUnrelatedOrMissingAvatarFrames(t *testing.T) {
	s := avatar.WorkflowState{Materials: []avatar.Material{{Kind: "avatar", SegmentID: "p2"}, {Kind: "animation", SegmentID: "p1"}}}
	if len(motionAvatarMaterials(&s, "p1")) != 0 {
		t.Fatal("unrelated frame selected")
	}
}

// Explicit opt-in acceptance, using one existing segment and the configured
// vision model. An isolated fixture DB prevents mutation of the user project.
func TestContentMotionLivePlanning(t *testing.T) {
	file := os.Getenv("AVATAR_MOTION_LIVE_INPUT")
	if file == "" {
		t.Skip("live model acceptance is opt-in")
	}
	var fixture struct {
		Input   avatar.WorkflowInput `json:"input"`
		Segment avatar.Segment       `json:"segment"`
		Need    avatar.MaterialNeed  `json:"need"`
		Context json.RawMessage      `json:"context"`
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	a, _, _, s := workflowFixture(t)
	w := a.avatarWorkflow
	s.Input = fixture.Input
	// Frame extraction has already read the real files; this isolated project
	// must not try to resolve the original project's asset paths.
	s.Input.Assets = nil
	s.Segments = []avatar.Segment{fixture.Segment}
	s.Needs = []avatar.MaterialNeed{fixture.Need}
	a.avatarMVP.options.RuntimeRoot = os.Getenv("AVATAR_MOTION_RUNTIME")
	cfg := config.Load()
	// Opt-in diagnostics retain only model output, never credentials, request
	// headers or the submitted images. Useful for provider JSON regressions.
	target, err := url.Parse(cfg.MovieVisionAPIBaseURL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(request *http.Request) { director(request); request.Host = target.Host }
	proxy.ModifyResponse = func(response *http.Response) error {
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		response.Body = io.NopCloser(bytes.NewReader(body))
		if err == nil && response.StatusCode == 200 {
			var result struct {
				Choices json.RawMessage `json:"choices"`
			}
			if json.Unmarshal(body, &result) == nil {
				_ = avatar.WriteJSON(os.Getenv("AVATAR_MOTION_LIVE_OUTPUT")+".response.json", result)
			}
		}
		return err
	}
	upstream := httptest.NewServer(proxy)
	defer upstream.Close()
	cfg.MovieVisionAPIBaseURL = upstream.URL
	client := providers.NewOpenAIClient(cfg, nil)
	w.content = client.AvatarContent
	if replay := os.Getenv("AVATAR_MOTION_LIVE_REPLAY"); replay != "" {
		// Replay an observed successful model response when verifying local
		// validation/formatting repairs, without billing a second model call.
		var response struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		body, err := os.ReadFile(replay)
		if err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal(body, &response) != nil || len(response.Choices) == 0 {
			t.Fatal("missing observed model response")
		}
		w.content = func(context.Context, string, any, []string) (json.RawMessage, error) {
			return json.RawMessage(response.Choices[0].Message.Content), nil
		}
		t.Log("replaying captured real model output; no new provider request")
	}
	w.media = func(_ context.Context, stage, _ string) (json.RawMessage, error) {
		if stage != "motion_context" {
			return nil, fmt.Errorf("unexpected stage")
		}
		return fixture.Context, nil
	}
	s.Steps = []avatar.WorkflowStep{{ID: "F07/" + fixture.Need.ID, Status: "running", Attempt: 1}}
	if err = os.MkdirAll(filepath.Join(w.dir(s.ID), "materials"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = w.save(&s); err != nil {
		t.Fatal(err)
	}
	plan, err := w.planMotion(context.Background(), &s, &s.Steps[0], fixture.Need, fixture.Segment, "live-verification")
	if err != nil {
		t.Fatal(err)
	}
	if err = avatar.WriteJSON(os.Getenv("AVATAR_MOTION_LIVE_OUTPUT"), plan); err != nil {
		t.Fatal(err)
	}
	t.Logf("live plan validated: %d scenes; actual avatar frames supplied; original project unchanged", len(plan.Scenes))
}
