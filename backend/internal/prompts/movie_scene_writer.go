package prompts

import (
	"crypto/sha256"
	_ "embed"
	"fmt"
	"strings"
)

// Movie mode reuses the general scene-writer skills. The general-only adaptation
// (writingBrief, mixBlocks, narrationSlots) is replaced by a movie adaptation.
//
//go:embed skills/general-scene-writer/movie-adaptation.md
var movieSceneAdaptation string

const movieSceneWriterVersion = "movie-scene-writer-v1"

// Purposes that write narration prose. Drafting gets planning + continuity; rewrites and repairs only refine.
var movieSceneWriterStages = map[string]string{
	"movie_screenplay_full_draft":        "plan-draft-polish",
	"movie_screenplay_write":             "plan-draft-polish",
	"movie_screenplay_user_rewrite":      "refine-local",
	"movie_screenplay_selection_rewrite": "refine-local",
	"movie_screenplay_recitation_repair": "refine-local",
	"movie_screenplay_closing_repair":    "refine-local",
	"movie_screenplay_full_repair":       "refine-local",
	"movie_screenplay_review_repair":     "refine-local",
	"movie_screenplay_editorial_repair":  "refine-local",
}

func movieSceneWriterStage(req map[string]any) string {
	if strings.TrimSpace(stringValue(req, "model_operation", "")) != "narration_outline" {
		return ""
	}
	return movieSceneWriterStages[strings.TrimSpace(stringValue(mapValue(req, "video_context"), "purpose", ""))]
}

func movieSceneSkills(stage string) []string {
	var skills []string
	if stage == "plan-draft-polish" {
		skills = append(skills, generalPagekinContinuity, generalScenePlanning, generalSceneDrafting)
	}
	return append(skills, generalPagekinRefinement, generalSceneRefinement, movieSceneAdaptation)
}

// The task instructions keep defining the output; the skills come after them as writing method only.
func movieSceneWritingPrompt(system, user string, req map[string]any) (string, string) {
	stage := movieSceneWriterStage(req)
	if stage == "" {
		return system, user
	}
	system += " The writing skills quoted in the input are method guidance for this call only: the task instructions, the required JSON shape, the source evidence and the user's requirements take precedence. Never output analysis, plans or extra fields."
	return system, user + "\n\nSCENE WRITING SKILLS " + movieSceneWriterVersion + " / " + stage + " (writing method; the task instructions above define the output):\n" + strings.Join(movieSceneSkills(stage), "\n\n")
}

// MovieSceneWritingFingerprint changes whenever the skills a movie writing call receives change,
// so a saved result written under older rules is not replayed. Empty for non-writing calls.
func MovieSceneWritingFingerprint(kind string, req map[string]any) string {
	stage := movieSceneWriterStage(req)
	if kind != "script_draft" || stage == "" {
		return ""
	}
	return movieSceneWriterVersion + "/" + stage + "/" + fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(movieSceneSkills(stage), "\x00"))))
}

// MovieSceneWritingInfo reports the skills actually present in the resolved prompt.
func MovieSceneWritingInfo(kind string, req map[string]any, user string) map[string]any {
	stage := movieSceneWriterStage(req)
	if kind != "script_draft" || stage == "" || !strings.Contains(user, "\nSCENE WRITING SKILLS "+movieSceneWriterVersion+" / "+stage+" ") {
		return nil
	}
	skills := []string{"natural-prose-refinement"}
	if stage == "plan-draft-polish" {
		skills = append([]string{"long-form-continuity"}, skills...)
	}
	return map[string]any{"version": movieSceneWriterVersion, "stage": stage, "upstream_skills": skills}
}
