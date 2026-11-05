package prompts

// This policy belongs to the general workflow. Matching owns editorial judgment;
// deterministic media validation, not another model review, gates execution.
func generalExecutionValidationRules(ctx map[string]any, rules string) string {
	if stringValue(ctx, "validationPolicy", "") != "reference-execution-v1" {
		return rules
	}
	return rules + ` REFERENCE EXECUTION POLICY (supersedes conflicting review requirements): Make content judgments within generation/retrieval/selection itself. There is no subsequent mandatory editorial-review or match-review call. Prefer dialogue/speaking-state correspondence for dialogue paraphrase, visible action/emotion for action narration, and relevant scene/context for background, motivation or transitions. When observations are uncertain, select the best grounded compatible candidate and record uncertainty in suggestions; uncertainty alone is not a reason to withhold otherwise executable output. Never invent evidence or use clearly unrelated footage. Citation span has NO fixed 30-second/45-second limit; references are not final playback intervals. Preserve the complete manuscript and valid source references. During chapters, do not predict picture feasibility or require an estimated voice duration to fit citations. During match, use supplied candidates and measured audio duration; starts must be detected cuts and ends may be inside shots. Check completeness, source identity and executable times before returning. Non-fatal quality observations go in suggestions; return issues without output only when required data or a usable candidate is genuinely missing. `
}
