package prompts

import (
	"strings"
	"testing"
)

func movieRequest(purpose string) map[string]any {
	return map[string]any{"model_operation": "narration_outline", "movie_prompt_version": 1, "custom_prompt": "只返回 {\"text\":\"\"}。",
		"video_context": map[string]any{"purpose": purpose, "materials": []any{"e1"}}}
}

func TestMovieWritingCallsGetSceneWriterSkills(t *testing.T) {
	cases := []struct {
		purpose, stage string
		continuity     bool
	}{
		{"movie_screenplay_full_draft", "plan-draft-polish", true},
		{"movie_screenplay_write", "plan-draft-polish", true},
		{"movie_screenplay_user_rewrite", "refine-local", false},
		{"movie_screenplay_selection_rewrite", "refine-local", false},
		{"movie_screenplay_recitation_repair", "refine-local", false},
		{"movie_screenplay_closing_repair", "refine-local", false},
		{"movie_screenplay_full_repair", "refine-local", false},
	}
	for _, tc := range cases {
		t.Run(tc.purpose, func(t *testing.T) {
			system, user, ok := movieOwnContractPrompt(movieRequest(tc.purpose))
			if !ok {
				t.Fatal("movie own-contract prompt not used")
			}
			marker := "\nSCENE WRITING SKILLS " + movieSceneWriterVersion + " / " + tc.stage + " "
			if !strings.Contains(user, marker) || !strings.Contains(user, movieSceneAdaptation) || !strings.Contains(user, generalPagekinRefinement) {
				t.Fatalf("skills missing for %s", tc.purpose)
			}
			if strings.Contains(user, generalPagekinContinuity) != tc.continuity || strings.Contains(user, generalScenePlanning) != tc.continuity {
				t.Fatalf("continuity/planning presence wrong for %s", tc.purpose)
			}
			// The general-only adaptation talks about writingBrief / mixBlocks fields movie mode does not send.
			if strings.Contains(user, generalSceneSourceOwnership) {
				t.Fatal("general-only adaptation leaked into movie prompt")
			}
			// The task contract stays first and is still the only output definition.
			if strings.Index(user, "Task instructions") > strings.Index(user, marker) || !strings.Contains(system, "task instructions, the required JSON shape") {
				t.Fatal("task contract must precede and outrank the skills")
			}
			info := MovieSceneWritingInfo("script_draft", movieRequest(tc.purpose), user)
			if info == nil || info["stage"] != tc.stage || info["version"] != movieSceneWriterVersion {
				t.Fatalf("writing info = %#v", info)
			}
			if MovieSceneWritingFingerprint("script_draft", movieRequest(tc.purpose)) == "" {
				t.Fatal("writing call must carry a skills fingerprint")
			}
		})
	}
}

func TestMovieNonWritingCallsAreUnchanged(t *testing.T) {
	for _, purpose := range []string{"movie_screenplay_full_review", "movie_screenplay_full_outline", "movie_screenplay_catalog", "movie_storyboard_match",
		"movie_storyboard_segment", "movie_agent_chat", "movie_screenplay_original_supplement", "movie_format_repair"} {
		t.Run(purpose, func(t *testing.T) {
			_, user, ok := movieOwnContractPrompt(movieRequest(purpose))
			if !ok {
				t.Fatal("expected movie own-contract prompt")
			}
			if strings.Contains(user, "SCENE WRITING SKILLS") || MovieSceneWritingInfo("script_draft", movieRequest(purpose), user) != nil || MovieSceneWritingFingerprint("script_draft", movieRequest(purpose)) != "" {
				t.Fatalf("%s must not receive writing skills", purpose)
			}
		})
	}
	// General-mode and non-movie requests never take the movie path.
	if MovieSceneWritingFingerprint("script_draft", map[string]any{"model_operation": "narration_outline", "video_context": map[string]any{"contract": "general-narration-v3", "purpose": "script"}}) != "" {
		t.Fatal("general request must not get the movie skills fingerprint")
	}
	if MovieSceneWritingFingerprint("script_match", movieRequest("movie_screenplay_full_draft")) != "" {
		t.Fatal("only script_draft writes prose")
	}
}
