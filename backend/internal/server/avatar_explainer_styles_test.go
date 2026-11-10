package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/narratoai/backend-go/internal/avatar"
)

func explainerCatalogFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"native-styles.json":                           `[{"id":"mg-line-art","name":"旧线条","entry":"demos/02-line-art/index.html","design":{"features":"旧风格"}}]`,
		"explainer-styles.json":                        `[{"id":"explain-podcast","name":"口播图解","version":"explainer/1","source":"awesome-opus5-5-videos","production_mode":"content-motion","design":{"features":"纸张与线稿","palette":["#111111","#222222","#ffffff","#dddddd","#aaaaaa"]},"preview_kind":"reference-poster"}]`,
		"catalog/explain-podcast-reference.jpg":        "poster",
		"templates/explain-podcast/original-prompt.md": "example brand must not enter production",
		"templates/explain-podcast/production.md":      "图解全屏时保留人物小窗；只使用已有原声音频。",
		"templates/explain-podcast/SOURCE.json":        `{"slug":"stokebuilder-356793"}`,
	}
	for name, value := range files {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestExplainerCatalogPublishesPromptBasedStylesAndArchivesOldChoices(t *testing.T) {
	root := explainerCatalogFixture(t)
	styles, err := loadWorkflowStyles(root)
	if err != nil {
		t.Fatal(err)
	}
	if styles[0].ID != "explain-podcast" || !styles[0].Published || styles[0].CardPoster == "" || styles[0].CardPreview != "" {
		t.Fatalf("poster-only reference must remain selectable without pretending to be a video: %+v", styles[0])
	}
	old, found := findWorkflowStyle(root, "mg-line-art")
	if !found || old.Legacy || !old.Archived {
		t.Fatalf("old runs must retain their real renderer: %+v", old)
	}
	if got := defaultWorkflowStyle(root); got != "explain-podcast" {
		t.Fatalf("wrong default: %s", got)
	}
	os.Remove(filepath.Join(root, "templates/explain-podcast/production.md"))
	styles, err = loadWorkflowStyles(root)
	if err != nil || styles[0].Published {
		t.Fatal("a poster alone must not publish a production style")
	}
}

func TestExplainerCatalogRejectsUnsafeAndDuplicateIDs(t *testing.T) {
	for _, manifest := range []string{
		`[{"id":"../outside"}]`,
		`[{"id":"explain-a"},{"id":"explain-a"}]`,
		`[{"id":"mg-line-art"}]`,
	} {
		root := explainerCatalogFixture(t)
		os.WriteFile(filepath.Join(root, "explainer-styles.json"), []byte(manifest), 0600)
		if _, err := loadWorkflowStyles(root); err == nil {
			t.Fatalf("unsafe manifest accepted: %s", manifest)
		}
	}
}

func TestExplainerProductionContextIncludesMethodWithoutOriginalStory(t *testing.T) {
	root := explainerCatalogFixture(t)
	context := workflowStyleProductionContext(root, "explain-podcast")
	data, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "人物小窗") || !strings.Contains(string(data), "纸张与线稿") || strings.Contains(string(data), "example brand") {
		t.Fatalf("production method missing or example leaked: %s", data)
	}
}

func TestExplainerMethodsReachDirectorAndAnimationCodeGeneration(t *testing.T) {
	a, _, _, state := workflowFixture(t)
	w := a.avatarWorkflow
	root := explainerCatalogFixture(t)
	a.avatarMVP.options.RuntimeRoot = root
	state.Version = avatar.WorkflowVersion
	state.Input.Style = "explain-podcast"
	state.Input.UseAvatar = false
	state.Director = nil
	state.Segments = []avatar.Segment{{ID: "p1", Text: "先输入内容，再查看结果。", Duration: 4, DirectorSceneID: "d1"}}
	need := avatar.MaterialNeed{ID: "motion", SegmentID: "p1", Kind: "animation", Duration: 4, Prompt: "展示处理过程"}
	state.Needs = []avatar.MaterialNeed{need}
	if err := w.save(&state); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	w.content = func(ctx context.Context, prompt string, input any, frames []string) (json.RawMessage, error) {
		in := input.(map[string]any)
		if strings.Contains(prompt, "统一导演稿") {
			context := in["selected_style"].(map[string]any)
			if !strings.Contains(context["method"].(string), "人物小窗") {
				t.Fatal("director missed selected method")
			}
			seen["director"] = true
			return json.RawMessage(`{"summary":"解释当前流程","scenes":[{"id":"d1","purpose":"说明流程","composition":"visual-hero","visual":"展开过程","motion":"逐步推进","estimated_duration":4}]}`), nil
		}
		if !strings.Contains(in["style_reference"].(string), "人物小窗") || strings.Contains(in["style_reference"].(string), "example brand") {
			t.Fatal("motion request lost method or copied original story")
		}
		if avatar.MotionCodeEnabled(ctx) {
			seen["code"] = true
			return json.RawMessage(`{"code":"ctx.fillStyle=palette[0];ctx.fillRect(0,0,w,h);ctx.fillStyle=palette[2];ctx.fillText(scene.title,30,50);"}`), nil
		}
		seen["plan"] = true
		return json.RawMessage(`{"version":"content-motion/1","tone":"neutral","visual_context":"无人像","scenes":[{"quote":"先输入内容，再查看结果","title":"输入内容","layout":"steps","elements":[{"label":"输入内容","kind":"document"},{"label":"查看结果","kind":"code"}]}]}`), nil
	}
	w.media = func(_ context.Context, stage, dir string) (json.RawMessage, error) {
		switch stage {
		case "motion_context":
			return json.RawMessage(`{"frames":[],"source":"no-avatar"}`), nil
		case "motion_timing":
			return json.RawMessage(`{"scenes":[{"quote":"先输入内容，再查看结果","title":"输入内容","layout":"steps","elements":[{"label":"输入内容","kind":"document","local_at":0},{"label":"查看结果","kind":"code","local_at":2}],"duration":4}],"animation":{}}`), nil
		case "motion_validate":
			return json.RawMessage(`{"valid":true}`), nil
		case "material":
			os.WriteFile(filepath.Join(dir, "materials", "motion.mp4"), []byte("fixture"), 0600)
			return json.RawMessage(`{"need_id":"motion","segment_id":"p1","kind":"animation","path":"materials/motion.mp4","duration":4}`), nil
		default:
			t.Fatalf("unexpected stage: %s", stage)
			return nil, nil
		}
	}
	if err := w.designDirector(context.Background(), &state); err != nil {
		t.Fatal(err)
	}
	if err := w.produceMaterial(context.Background(), &state, need, "F07"); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"director", "plan", "code"} {
		if !seen[phase] {
			t.Fatalf("missing production phase: %s", phase)
		}
	}
	before := w.motionFingerprint(&state, need, state.Segments[0])
	os.WriteFile(filepath.Join(root, "templates/explain-podcast/production.md"), []byte("改为左下人物窗"), 0600)
	if before == w.motionFingerprint(&state, need, state.Segments[0]) {
		t.Fatal("changed method reused stale animation")
	}
}
