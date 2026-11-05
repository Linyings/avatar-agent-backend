package prompts

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSceneWriterBuildRoutesExistingCalls(t *testing.T) {
	s, err := NewService("../../prompt")
	if err != nil {
		t.Fatal(err)
	}
	b := NewBuilder(s)
	for _, source := range []string{"", "video-events-v1"} {
		for _, test := range []struct {
			purpose, scope, stage string
			allowRepair           bool
		}{
			{"script", "", "plan-draft-polish", false},
			{"script", "joint-original-plan-v1", "plan-draft-polish", false},
			{"script", "mix-continuity-v1", "refine-bridges", false},
			{"script", "single-chapter-v1", "refine-local", false},
			{"original-layout", "", "refine-local", true},
			{"original-layout", "", "", false},
			{"chapters", "", "", false},
			{"retrieve", "", "", false},
			{"match", "", "", false},
			{"editorial-review", "", "", false},
		} {
			t.Run(source+"/"+test.purpose+"/"+test.scope+"/"+test.stage, func(t *testing.T) {
				req := humanizerRequest("短剧漫剧", test.purpose)
				ctx := mapValue(req, "video_context")
				ctx["sourcePolicy"], ctx["repairScope"], ctx["allowNarrationRepair"] = source, test.scope, test.allowRepair
				ctx["evidence"] = []any{map[string]any{"id": "e-1", "description": "只知道他计划离开，尚未出发。"}}
				p, err := b.Build("script_draft", req)
				if err != nil {
					t.Fatal(err)
				}
				originalSystem, originalUser, _ := generalEditingOperationPrompt("script_draft", generalSceneWritingRequest(req))
				if test.stage == "" {
					if p.SystemPrompt != originalSystem || p.UserPrompt != originalUser {
						t.Fatal("non-writing operation changed")
					}
					return
				}
				if !strings.HasSuffix(p.UserPrompt, originalUser) || !strings.Contains(p.SystemPrompt, generalSceneWriterVersion+" / "+test.stage) {
					t.Fatal("original evidence/contract or stage not preserved")
				}
				if !strings.Contains(p.UserPrompt, generalSceneRefinement) || !strings.Contains(p.UserPrompt, generalHumanizerSkill) {
					t.Fatal("refinement/humanizer missing")
				}
				full := test.stage == "plan-draft-polish"
				if !strings.Contains(p.UserPrompt, generalPagekinRefinement) || strings.Contains(p.UserPrompt, generalPagekinContinuity) != full {
					t.Fatal("upstream writing skills missing or planning leaked into local rewrite")
				}
				if strings.Contains(p.UserPrompt, generalScenePlanning) != full || strings.Contains(p.UserPrompt, generalSceneDrafting) != full {
					t.Fatal("local/continuity call received full-story drafting guidance")
				}
			})
		}
	}
}

func TestSceneWriterDoesNotCreativelyRewriteRestorationOrZeroRatio(t *testing.T) {
	for _, expected := range []any{"应逐字补回的完整稿。", "", nil} {
		for _, ratio := range []any{0, float64(0), json.Number("0"), "0", 0.5, 1} {
			req := humanizerRequest("温情治愈", "script")
			ctx := mapValue(req, "video_context")
			ctx["repairScope"], ctx["expectedText"], ctx["narrationRatio"] = "single-chapter-v1", expected, ratio
			r, _ := firstFloatValue(ctx, "narrationRatio")
			shouldSkip := expected != nil || r == 0
			system, user := generalSceneWritingPrompt("system", "original request", req)
			if shouldSkip {
				if system != "system" || user != "original request" {
					t.Fatalf("restoration/source-only output got writing guidance: expected=%v ratio=%v", expected, ratio)
				}
			} else if !strings.Contains(system, "/ refine-local") {
				t.Fatal("ordinary local rewrite lost refinement")
			}
		}
	}
	for _, ctx := range []map[string]any{
		{},
		{"contract": "general-narration-v3", "purpose": "chapters"},
		{"contract": "movie-director-v1", "purpose": "script"},
	} {
		req := map[string]any{"model_operation": "narration_outline", "video_context": ctx}
		if got := generalSceneWriterStage(req); got != "" {
			t.Fatalf("writing skills leaked to other workflow: %s", got)
		}
	}
}

func TestSceneWriterIdentityTracksSkillContentAndScope(t *testing.T) {
	s, err := NewService("../../prompt")
	if err != nil {
		t.Fatal(err)
	}
	b := NewBuilder(s)
	full := humanizerRequest("温情治愈", "script")
	local := humanizerRequest("温情治愈", "script")
	mapValue(local, "video_context")["repairScope"] = "single-chapter-v1"
	fullHash, localHash := b.GeneralEditingPromptFingerprint("script_draft", full), b.GeneralEditingPromptFingerprint("script_draft", local)
	beforePlanning, beforeDrafting, beforeRefinement := generalScenePlanning, generalSceneDrafting, generalSceneRefinement
	beforeUpstream, beforeOwnership := generalPagekinRefinement, generalSceneSourceOwnership
	t.Cleanup(func() {
		generalScenePlanning, generalSceneDrafting, generalSceneRefinement = beforePlanning, beforeDrafting, beforeRefinement
		generalPagekinRefinement, generalSceneSourceOwnership = beforeUpstream, beforeOwnership
	})
	generalScenePlanning += "\nNew planning rule."
	generalSceneDrafting += "\nNew drafting rule."
	if fullHash == b.GeneralEditingPromptFingerprint("script_draft", full) {
		t.Fatal("updated drafting skill reuses old full draft")
	}
	if localHash != b.GeneralEditingPromptFingerprint("script_draft", local) {
		t.Fatal("unrelated full-draft skill invalidated local rewrite")
	}
	generalSceneRefinement += "\nNew refinement rule."
	if localHash == b.GeneralEditingPromptFingerprint("script_draft", local) {
		t.Fatal("updated refinement reuses old local repair")
	}
	for _, skill := range []*string{&generalPagekinRefinement, &generalSceneSourceOwnership} {
		before := b.GeneralEditingPromptFingerprint("script_draft", local)
		*skill += "\nUpdated source/ownership rule."
		if before == b.GeneralEditingPromptFingerprint("script_draft", local) {
			t.Fatal("updated upstream/ownership rule can replay an old draft")
		}
	}
}
