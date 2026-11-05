package prompts

import "fmt"

// The video workflow sends the same observation as context.items[id].visual,
// evidence[id].text and separate visual/audio/uncertainty fields. Keep a single
// authoritative copy for writing. Only exact duplicates are replaced; this is
// not summarization, truncation, a cache, or a change to stored project evidence.
func generalSceneDeduplicatedRequest(req map[string]any) map[string]any {
	if generalSceneWriterStage(req) == "" {
		return req
	}
	ctx := mapValue(req, "video_context")
	if stringValue(ctx, "sourcePolicy", "") != "video-events-v1" {
		return req
	}
	evidence, ok := ctx["evidence"].([]any)
	if !ok {
		return req
	}
	clone := func(m map[string]any) map[string]any {
		n := make(map[string]any, len(m))
		for k, v := range m {
			n[k] = v
		}
		return n
	}
	byID := map[string]map[string]any{}
	for _, raw := range evidence {
		if e, ok := raw.(map[string]any); ok && e["source"] == "video" {
			key := fmt.Sprint(e["id"])
			if _, exists := byID[key]; exists {
				// Ambiguous IDs must retain all original data.
				return req
			}
			byID[key] = e
		}
	}
	newEvidence := make([]any, len(evidence))
	for i, raw := range evidence {
		newEvidence[i] = raw
		e, ok := raw.(map[string]any)
		if !ok || e["source"] != "video" {
			continue
		}
		visual, hasVisual := e["visual"].(string)
		if !hasVisual {
			continue
		}
		text := "画面观察：" + visual
		if audio, ok := e["audio"].(string); ok && audio != "" {
			text += "；声音信息（非逐字字幕）：" + audio
		}
		if uncertainty, ok := e["uncertainty"].(string); ok && uncertainty != "" {
			text += "；不确定：" + uncertainty
		}
		if e["text"] == text {
			c := clone(e)
			delete(c, "text")
			newEvidence[i] = c
		}
	}
	contextMap, contextIsMap := ctx["context"].(map[string]any)
	newContext := clone(contextMap)
	if items, ok := newContext["items"].(map[string]any); ok {
		newItems := clone(items)
		for id, raw := range items {
			item, ok := raw.(map[string]any)
			e := byID[id]
			visual, vOK := item["visual"].(string)
			text, tOK := e["text"].(string)
			if !ok || e == nil || !vOK || !tOK || visual != text {
				continue
			}
			start, sOK := firstFloatValue(item, "start_time")
			end, eOK := firstFloatValue(item, "end_time")
			es, esOK := firstFloatValue(e, "startMs")
			ee, eeOK := firstFloatValue(e, "endMs")
			if !sOK || !eOK || !esOK || !eeOK || start*1000 != es || end*1000 != ee {
				continue
			}
			if _, exists := item["visualEvidenceId"]; exists {
				continue
			}
			c := clone(item)
			delete(c, "visual")
			c["visualEvidenceId"] = e["id"]
			newItems[id] = c
		}
		newContext["items"] = newItems
	}
	newCtx := clone(ctx)
	newCtx["evidence"] = newEvidence
	if contextIsMap {
		newCtx["context"] = newContext
	}
	newReq := clone(req)
	newReq["video_context"] = newCtx
	return newReq
}
