package server

// Motion prompt context compression.
//
// Every motion scene costs one model request. The full production input
// (style reference ~2.9 KB, director scene, material design, direction) was
// resent verbatim for every scene, so a five-scene segment paid for the same
// style prose five times. Providers with a per-request price make that the
// dominant cost, and long segments grow the bill linearly with scene count.
//
// The design phase legitimately needs the whole style reference: it decides the
// palette, layout and tone for the segment. The per-scene code phase does not —
// the design phase has already distilled the look into style_profile.palette,
// tone and visual_context. Code scenes therefore receive a compact digest.

import (
	"strings"

	"github.com/narratoai/backend-go/internal/avatar"
)

const (
	// designStyleReferenceBudget bounds the style prose in a design request.
	// The design phase needs enough to fix palette and layout, not the whole
	// upstream creative brief.
	designStyleReferenceBudget = 2400
	// codeStyleReferenceBudget bounds the style prose handed to a code scene.
	// The design phase keeps the fuller text; only per-scene calls are cut.
	codeStyleReferenceBudget = 600
	// codeDirectorBudget bounds the director scene handed to a code scene.
	codeDirectorBudget = 700
)

// compactCodeInput builds the reduced input used for per-scene code requests.
// The design phase already resolved palette, tone and visual context, so the
// long style and director prose is replaced by a bounded digest. Everything the
// scene actually draws from — scene timing, elements, budget — is untouched.
func compactCodeInput(full map[string]any, styleDigest string) map[string]any {
	out := make(map[string]any, len(full))
	for key, value := range full {
		out[key] = value
	}
	if digest := strings.TrimSpace(styleDigest); digest != "" {
		out["style_reference"] = truncateRunes(digest, codeStyleReferenceBudget)
	} else {
		delete(out, "style_reference")
	}
	// style_profile is already small and carries the palette the code needs.
	// direction restates whole-film goals the design phase consumed.
	delete(out, "direction")
	if director, ok := out["director"]; ok {
		out["director"] = trimDirectorValue(director, codeDirectorBudget)
	}
	return out
}

// trimDirectorValue keeps only the fields a code scene draws from, dropping the
// narration prose and long visual descriptions already encoded in the scene.
func trimDirectorValue(value any, budget int) any {
	scenes, ok := value.([]avatar.DirectorScene)
	if !ok {
		return truncateAnyRunes(value, budget)
	}
	trimmed := make([]map[string]any, 0, len(scenes))
	for _, scene := range scenes {
		trimmed = append(trimmed, map[string]any{
			"purpose":     truncateRunes(scene.Purpose, 120),
			"composition": scene.Composition,
			"motion":      truncateRunes(scene.Motion, 200),
			"avatar_shape": scene.AvatarShape,
			"avatar_position": scene.AvatarPosition,
		})
	}
	return trimmed
}

func truncateRunes(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}

func truncateAnyRunes(value any, limit int) any {
	if text, ok := value.(string); ok {
		return truncateRunes(text, limit)
	}
	return value
}

// styleDigestForCode condenses the style reference into the concise form a code
// scene needs: the signature features plus palette, without the production
// method and original creative brief that the design phase already applied.
func styleDigestForCode(features string, palette []string) string {
	featureText := truncateRunes(features, 300)
	if len(palette) == 0 {
		return featureText
	}
	return featureText + " 调色板：" + strings.Join(palette, " ")
}
