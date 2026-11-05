package prompts

import (
	"fmt"
	"math"
	"strings"
	"unicode"
)

// This is an index into the existing source, not a second plot analysis. Never
// promote model summaries, speaker numbers or overlapping timestamps to facts.
func generalSceneWritingRequest(req map[string]any) map[string]any {
	if generalSceneWriterStage(req) == "" {
		return req
	}
	prepared := generalSceneDeduplicatedRequest(req)
	ctx := mapValue(prepared, "video_context")
	next := sceneCopy(prepared)
	nextCtx := sceneCopy(ctx)
	nextCtx["writingBrief"] = generalSceneWritingBrief(ctx)
	if stringValue(ctx, "repairScope", "") == "mix-continuity-v1" && ctx["userApproved"] != true && stringValue(ctx, "scriptPolicy", "") != "as-saved" {
		// This existing pass is authorized to rewrite all automatic narration.
		// Do not prime it with the unverified draft's spoilers/invented relations.
		// Locked originals, source material and marker diagnostics remain intact.
		delete(nextCtx, "script")
	}
	next["video_context"] = nextCtx
	return next
}

func sceneCopy(source map[string]any) map[string]any {
	copy := make(map[string]any, len(source))
	for k, v := range source {
		copy[k] = v
	}
	return copy
}

func sceneObjects(value any) []map[string]any {
	if objects, ok := value.([]map[string]any); ok {
		return objects
	}
	var objects []map[string]any
	if items, ok := value.([]any); ok {
		for _, item := range items {
			object, _ := item.(map[string]any)
			objects = append(objects, object)
		}
	}
	return objects
}

func sceneFields(source map[string]any, keys ...string) map[string]any {
	result := map[string]any{}
	for _, key := range keys {
		if value, exists := source[key]; exists {
			result[key] = value
		}
	}
	return result
}

func sceneRange(source map[string]any, seconds bool) (float64, float64, bool) {
	start, hasStart := firstFloatValue(source, "sourceStartMs", "startMs")
	end, hasEnd := firstFloatValue(source, "sourceEndMs", "endMs")
	if !hasStart || !hasEnd {
		if !seconds {
			return 0, 0, false
		}
		start, hasStart = firstFloatValue(source, "start")
		end, hasEnd = firstFloatValue(source, "end")
		start, end = start*1000, end*1000
	}
	valid := hasStart && hasEnd && !math.IsNaN(start) && !math.IsNaN(end) &&
		!math.IsInf(start, 0) && !math.IsInf(end, 0) && start >= 0 && end > start
	return start, end, valid
}

func sceneEvidenceRefs(evidence []map[string]any, start, end float64) []any {
	refs := []any{}
	for _, e := range evidence {
		es, ee, ok := sceneRange(e, false)
		if id, exists := e["id"]; exists && id != nil && ok && es < end && ee > start {
			refs = append(refs, id)
		}
	}
	return refs
}

func sceneTextKey(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, text)
}

// A quoted line can locate a candidate source; it cannot identify its speaker.
func sceneCharacterRefs(character map[string]any, evidence []map[string]any) []any {
	quotes, _ := character["evidence"].([]any)
	if text, ok := character["evidence"].([]string); ok {
		for _, quote := range text {
			quotes = append(quotes, quote)
		}
	}
	refs := []any{}
	for _, e := range evidence {
		id, exists := e["id"]
		if !exists || id == nil {
			continue
		}
		text := sceneTextKey(stringValue(e, "text", "") + stringValue(e, "visual", "") + stringValue(e, "audio", ""))
		for _, raw := range quotes {
			quote, ok := raw.(string)
			key := sceneTextKey(quote)
			if ok && len([]rune(key)) >= 4 && strings.Contains(text, key) {
				refs = append(refs, id)
				break
			}
		}
	}
	return refs
}

// Only explicit structural labels are indexed; mentioning a preview/advert in
// dialogue is not a reason to exclude a dramatic scene. Nothing is removed.
func scenePackagingLabel(item map[string]any) string {
	for _, key := range []string{"contentType", "segmentType", "type", "action"} {
		label := strings.ToLower(strings.TrimSpace(stringValue(item, key, "")))
		if (label == "opening" || label == "ending") && (key == "contentType" || key == "segmentType") {
			return "credits"
		}
		switch label {
		case "preview", "trailer", "next_episode_preview", "下集预告", "预告片", "下集预告片":
			return "preview"
		case "advertisement", "ad", "广告", "广告口播", "片头广告":
			return "advertisement"
		case "credits", "片头曲", "片尾曲", "片头字幕", "片尾字幕":
			return "credits"
		case "recap", "重复回顾", "前情回顾":
			return "recap"
		}
	}
	return ""
}

func generalSceneWritingBrief(ctx map[string]any) map[string]any {
	evidence := sceneObjects(ctx["evidence"])
	narrative := mapValue(mapValue(ctx, "context"), "narrative_context")
	characters, beats, originals, packaging, relationships, slots := []any{}, []any{}, []any{}, []any{}, []any{}, []any{}
	for i, character := range sceneObjects(narrative["characters"]) {
		if character == nil {
			continue
		}
		entry := sceneFields(character, "id", "name", "role", "aliases")
		entry["sourceRef"] = fmt.Sprintf("context.narrative_context.characters[%d]", i)
		entry["candidateEvidenceIds"] = sceneCharacterRefs(character, evidence)
		entry["authority"] = "analysis-candidate-not-confirmed-identity"
		characters = append(characters, entry)
	}
	for i, event := range sceneObjects(narrative["events"]) {
		if event == nil {
			continue
		}
		entry := sceneFields(event, "id", "action", "participants", "object", "phase", "support_type")
		entry["sourceRef"] = fmt.Sprintf("context.narrative_context.events[%d]", i)
		entry["authority"] = "analysis-candidate-check-raw-evidence"
		if start, end, ok := sceneRange(event, true); ok {
			entry["startMs"], entry["endMs"] = start, end
			entry["overlappingEvidenceIds"] = sceneEvidenceRefs(evidence, start, end)
		}
		if label := scenePackagingLabel(event); label != "" {
			entry["packagingHint"] = label
		}
		beats = append(beats, entry)
	}
	for _, e := range evidence {
		if label := scenePackagingLabel(e); label != "" {
			entry := sceneFields(e, "id", "startMs", "endMs")
			entry["packagingHint"] = label
			packaging = append(packaging, entry)
		}
	}
	for i, relationship := range sceneObjects(narrative["relationships"]) {
		if relationship == nil {
			continue
		}
		entry := sceneFields(relationship, "source", "target", "relationship", "evidence")
		entry["sourceRef"] = fmt.Sprintf("context.narrative_context.relationships[%d]", i)
		entry["authority"] = "directed-analysis-candidate-check-raw-evidence"
		relationships = append(relationships, entry)
	}
	for i, original := range sceneObjects(ctx["originalClips"]) {
		if original == nil {
			continue
		}
		entry := sceneFields(original, "id", "sourceStartMs", "sourceEndMs", "evidenceIds")
		entry["sourceRef"] = fmt.Sprintf("originalClips[%d]", i)
		entry["deliveryRef"] = fmt.Sprintf("originalClips[%d].dialogue", i)
		if start, end, ok := sceneRange(original, false); ok {
			entry["overlappingEvidenceIds"] = sceneEvidenceRefs(evidence, start, end)
		}
		if id, exists := original["id"]; exists {
			marker := fmt.Sprintf("[[ORIGINAL:%v]]", id)
			entry["marker"] = marker
			if script, ok := ctx["script"].(string); ok {
				entry["markerOccurrencesInDraft"] = strings.Count(script, marker)
			}
		}
		originals = append(originals, entry)
	}
	// Narration slots expose only wholly contained source evidence. An edge cue
	// is not split or reclassified; keep it in the original input for reference.
	for i, block := range sceneObjects(mapValue(ctx, "mixBlocks")["blocks"]) {
		if block == nil {
			continue
		}
		entry := sceneFields(block, "index", "sourceStartMs", "sourceEndMs", "nextMarker", "targetCharacters")
		entry["sourceRef"] = fmt.Sprintf("mixBlocks.blocks[%d]", i)
		eligible, sourceCues, earlier, later := []any{}, []any{}, []any{}, []any{}
		if start, end, ok := sceneRange(block, false); ok {
			for _, e := range evidence {
				es, ee, valid := sceneRange(e, false)
				id, hasID := e["id"]
				if !valid || !hasID || id == nil {
					continue
				}
				if es >= start && ee <= end && scenePackagingLabel(e) == "" {
					eligible = append(eligible, id)
					sourceCues = append(sourceCues, sceneFields(e, "id", "startMs", "endMs", "text", "visual", "audio", "uncertainty"))
				}
			}
			for _, original := range sceneObjects(ctx["originalClips"]) {
				os, oe, valid := sceneRange(original, false)
				if !valid {
					continue
				}
				if oe <= start {
					earlier = append(earlier, original["id"])
				} else if os >= end {
					later = append(later, original["id"])
				}
			}
		}
		entry["eligibleEvidenceIds"] = eligible
		entry["sourceCues"] = sourceCues
		entry["alreadyDeliveredOriginalIds"] = earlier
		entry["withholdOriginalIds"] = later
		entry["mayBeEmpty"] = true
		slots = append(slots, entry)
	}
	return map[string]any{
		"version": "source-character-ownership-v1", "stage": generalSceneWriterStage(map[string]any{"model_operation": "narration_outline", "video_context": ctx}),
		"characters": characters, "relationships": relationships, "storyBeats": beats, "originalDelivery": originals, "narrationSlots": slots, "packagingHints": packaging,
		"authority": "source-index-only-not-new-evidence",
	}
}
