package prompts

func generalJointAllocationRules(ctx map[string]any) string {
	if stringValue(ctx, "allocationPolicy", "") != "joint-candidates-v1" {
		return ""
	}
	switch stringValue(ctx, "purpose", "") {
	case "retrieve":
		return ` JOINT ALLOCATION OVERRIDE: Retrieve the correct observed actor/action stage even when its pictures are currently reserved by an original. Return that grounded candidate so the client can repair the reservation. Never substitute a nearby but different action merely to avoid an occupied range. A turn toward a statue cannot illustrate being slammed or standing up. Keep up to three genuinely compatible alternatives. lockedPictureCandidates are choices, NOT occupied ranges; all overlapping narration choices will be solved jointly. When feedback reports capacity, jointly reconsider ALL named competing chapters, not only the last one. `
	case "match":
		return ` JOINT ALLOCATION: jointAllocation is a complete non-overlapping selection from the retrieved candidates, respecting measured voice lengths. Prefer this selection when its observed content matches. You may choose other supplied candidates only if the whole timeline remains non-overlapping. Do not swap to an unrelated event for convenience. Report concrete content conflicts rather than returning arbitrary timestamps. `
	case "original-layout":
		return ` JOINT ORIGINAL REPAIR: narrationCandidatesWithoutReservations lists executable shot-aligned pictures for already voiced narration BEFORE excluding original reservations. requestedEvents explains their meaning. Preserve spoken texts and every original chapter. Release enough relevant edge footage for ALL competing narration, not only the last failed row. Choose the original's distinct central dramatic beat; edge actions already narrated should be released, not played twice. For example, if narration describes entry and a bloodied blade, keep only a distinct intervening confrontation for source sound, rather than reserving both actions again. Return an actually changed range, not an unchanged plan with a repair reason. Retained ranges must not overlap the needed narration allocations. originalDurationTargetMs is the desired TOTAL source-audio duration calculated from real TTS; approach it if meaningful beats permit, never cut a payoff solely to hit a quota. For video observations an event interval is not a subtitle sentence: it may contain several shots and can be split at supplied shot cuts; do not require keeping a whole broad event just to retain its central beat. Preserve coherent source audio and payoff, never invent a transcript. Include description for each shortened video original describing ONLY what remains. If the required narration spans both edges, consider the core interval between them. If no distinct original beat and sufficient narration pictures can coexist, report the precise conflicting chapter IDs and required narrative revision; never silently replace the action with nearby unrelated footage. `
	}
	return ""
}
