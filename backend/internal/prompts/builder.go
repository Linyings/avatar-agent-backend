package prompts

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const runtimeMarker = "## 本次实际任务输入（高优先级）"
const scriptStageOneMarker = "### 第一阶段"
const scriptStageTwoMarker = "### 第二阶段"

const narrationHumanizerGuidance = `Humanizer-zh natural-writing rules:
- Remove filler openers, meta chatter, vague attributions, generic conclusions, and ornamental claims that add no story information.
- Avoid formulaic contrasts such as "not only...but also", forced three-part lists, repetitive transition words, AI buzzwords, slogan-like quotable lines, and overused em dashes.
- Vary sentence lengths and syntax. Mix short, direct lines with naturally developed sentences so the voice sounds spoken rather than mechanically uniform.
- Prefer specific evidence-grounded actions, motives, conflicts, and feelings over abstract praise or inflated symbolism. Keep a clear point of view and trust the audience without over-explaining.
- Preserve the requested perspective, meaning, chronology, and factual evidence. Humanizing the copy never licenses invented facts, dialogue, identities, motives, or outcomes.
- Return only the narration fields required by the operation. Do not add editing notes, quality scores, change summaries, or Humanizer commentary.`

type Builder struct {
	service *Service
}

type PromptParts struct {
	SystemPrompt string
	UserPrompt   string
	Meta         TemplateMeta
}

func NewBuilder(service *Service) *Builder {
	return &Builder{service: service}
}

func (b *Builder) Build(kind string, req map[string]any) (PromptParts, error) {
	videoConfig := mapValue(req, "video_config")
	styleMode := firstStringFromMaps(req, videoConfig, "style_model", "style_mode")
	meta := b.resolveTemplate(kind, styleMode)
	if meta.ID == "" {
		return PromptParts{}, fmt.Errorf("未找到可用提示词模板")
	}
	promptLocale := promptLocaleFromConfig(videoConfig)
	context := b.templateContext(videoConfig, promptLocale)
	rendered, err := b.service.Render(meta.ID, context)
	if err != nil {
		return PromptParts{}, err
	}
	systemPrompt := strings.TrimSpace(rendered.SystemPrompt)
	generalReq := req
	if generalUsesHumanizer(kind, req) {
		generalReq = generalSceneWritingRequest(req)
	}
	if system, user, ok := generalEditingOperationPrompt(kind, generalReq); ok {
		if generalUsesHumanizer(kind, req) {
			system, user = generalSceneWritingPrompt(system, user, req)
			system, user = generalHumanizedPrompt(system, user, rendered, context)
		}
		return PromptParts{SystemPrompt: system, UserPrompt: user, Meta: rendered.Meta}, nil
	}
	if system, user, ok := movieNarrationOperationPrompt(req); ok {
		return PromptParts{SystemPrompt: system, UserPrompt: user, Meta: rendered.Meta}, nil
	}
	if system, user, ok := movieOwnContractPrompt(req); ok {
		return PromptParts{SystemPrompt: system, UserPrompt: user, Meta: rendered.Meta}, nil
	}
	if operation := strings.ToLower(strings.TrimSpace(fmt.Sprint(req["model_operation"]))); strings.HasPrefix(operation, "narration_") {
		system, user, ok := narrationOperationPrompt(operation, req, promptLocale)
		if ok {
			return PromptParts{SystemPrompt: system, UserPrompt: user, Meta: rendered.Meta}, nil
		}
	}
	templateUserPrompt := promptTemplateForKind(kind, stripEmbeddedExamples(rendered.UserPrompt))
	userPrompt := b.userPrompt(kind, req, videoConfig, templateUserPrompt, promptLocale)
	return PromptParts{SystemPrompt: systemPrompt, UserPrompt: userPrompt, Meta: rendered.Meta}, nil
}

func narrationOperationPrompt(operation string, req map[string]any, locale string) (string, string, bool) {
	input, _ := json.MarshalIndent(map[string]any{
		"video_config":  req["video_config"],
		"video_context": req["video_context"],
	}, "", "  ")
	custom := strings.TrimSpace(stringValue(req, "custom_prompt", ""))
	system := "You are a film narration workflow component. Use only supplied evidence, preserve chronology and facts, and output exactly one JSON object without Markdown."
	var rules string
	switch operation {
	case "narration_outline":
		rules = `Create the requested number of chronological chapters. Output {"summary":"...","chapters":[{"chapter_id":"chapter-01","start":0,"end":60,"target_characters":200,"objective":"...","opening_bridge":"...","closing_hook":"...","event_ids":[],"characters":[],"retrieval_keywords":[]}]}. Chapter character budgets must sum exactly to contract.target_characters. Ranges must be inside the source duration and ordered. Only use story_event_index, which already contains the selected valid events. Omitted events are intentionally excluded by the character budget and must not be restored, reviewed as uncovered, or requested for repair.`
		rules += ` Chapter start and end values must use source timeline seconds. Never use milliseconds or output-timeline positions. Every chapter must satisfy 0 <= start < end <= contract.source_duration_seconds and stay inside contract.valid_chapter_range.`
	case "narration_segment":
		rules = `Write the current chapter as vivid commentary and analysis, never as a transcript. Do not recite, quote, or closely paraphrase the subtitle lines; the narration must explain what is happening on screen and why, so the audience follows the plot direction instead of hearing the dialogue repeated back.

Analyze and convey:
- Plot direction: what is driving the scene, what the characters want, how the story moves forward and where it is heading.
- Motivation and conflict: why each character acts this way, who is opposing whom, and what is at stake.
- Emotion and tension: how the mood shifts, what the audience should feel, and what it sets up for later.
- Continuity: connect to earlier and later events using the supplied callbacks and rolling memory (setups, payoffs, character state).

Evidence rules:
- Ground every statement in the supplied exact/adjacent/callback evidence, the global narrative context, the chapter outline, and rolling memory. You may interpret and analyze the evidence, but never invent people, events, dialogue, motives, or outcomes not supported by it, and never contradict it.
- Keep chronology and facts intact; do not move facts between chapters.

Style:
- Sound like a compelling video narrator: conversational, energetic, clear, with natural rhythm and occasional hooks. Avoid mechanical summaries, list-like recaps, filler openers, or repetitive sentence patterns.

Segment count:
- Return exactly the number of narration items requested by item_count_range. Each item must express a complete, naturally flowing thought. Never split one continuous explanation into several short fragments merely to create more items.

Output {"items":[{"_id":"1","text":"...","evidence_refs":["subtitle-1"]}],"summary":"compact chapter summary","rolling_memory":{"covered_event_ids":[],"character_state":[],"unresolved_threads":[]}}. Length has a 70%-130% hard acceptance range around chapter.target_characters. The client rejects text outside that range after bounded retries. Never truncate mid-thought: rewrite complete, fluent sentences and omit lower-priority details instead. Use only the selected chapter events and prioritize evidence alignment, factual accuracy, and fluent explanation inside that range. IDs start at 1. Do not output timeline fields.`
		rules += " Assign evidence chronologically across the returned items. When chapter.event_ids has at least as many events as required items, allocate each selected event to exactly one item and never repeat an event in another item; when the counts are equal, each item must cover one distinct event. When there are fewer events than items, split the event into chronological, non-overlapping evidence subparts and do not repeat the same fact. Each item's evidence_refs must belong only to its assigned event or subpart."
		rules += " For this operation, chapter.target_characters defines the real acceptance range. generation_constraints overrides video_config.word_count and may intentionally use a lower retry target and maximum as a guard band. Never exceed generation_constraints.maximum or generate the global word_count again inside one chapter."
		rules += " Obey generation_constraints exactly: return exactly required_item_count items, keep every item at or below per_item_maximum_characters, and keep the chapter total inside minimum and maximum. When previous_rejected_draft is present, rewrite that rejected draft instead of expanding it: remove repetition and lower-priority detail, retain only the evidence-grounded core facts for the remaining selected events, and produce complete fluent sentences. If previous_rejected_draft.event_rebalance is present, use the rejected items as the editing source, remove every fact that belongs only to discarded_event_ids, cover only current_event_ids, and do not start over or add replacement detail. This is a chapter-generation correction, not post-generation truncation. When generation_constraints.strict_final_retry is true, the target is deliberately conservative: treat maximum and per_item_maximum_characters as absolute ceilings, prefer concise complete analysis, and do not reuse facts from an event removed from chapter.event_ids."
	case "narration_humanize":
		rules = `Rewrite the supplied narration draft so it sounds naturally human while preserving its meaning and evidence. Return every input item exactly once as {"items":[{"_id":1,"text":"humanized narration"}]}. Preserve the input item count, _id values and order exactly. Modify only text. Never merge, split, omit, add or renumber items, and never modify event, evidence, chapter, priority, or time-anchor fields. Count spoken letters and numbers only; punctuation and whitespace do not count. The spoken-character total must remain inside target_character_range, which is a hard acceptance range. Never cut evidence-backed facts, weaken analysis, or add filler solely for length. Preserve every person, identity, event, relationship, motive, causal link, chronology, outcome, perspective and evidence-backed claim from the draft and protected_context. Do not add dialogue, facts, editing notes, quality scores, change summaries, explanations, Markdown or fields other than _id and text.`
		rules += " Treat target_character_range.maximum and per_item_maximum_characters as absolute ceilings. When character_budget_recovery is present, naturally rewrite the existing sentences to remove at least required_reduction_characters spoken characters; remove repetition and wordy transitions, never facts or anchors. Return no item above per_item_maximum_characters."
	case "narration_global_review":
		rules = `Audit the complete narration against the outline, selected event index, chapter evidence catalogs and evidence references. Check identities, relationships, chronology, causality, unsupported claims, repeated telling of the same event, transitions, opening promise and ending. Treat each item event_ids as authoritative local coverage proof; report covered=false only when no item lists the event, or when a high-severity fact or evidence mismatch explicitly invalidates that anchor. Coverage applies only to story_event_index; omitted events are intentionally out of scope and must never be reported as uncovered or evidence gaps. Do not fail solely because of character count, duration, or deviation from the requested runtime; these are checked separately by the client. Every high-severity issue must identify item_ids when the affected items can be located. Output {"passed":true,"issues":[{"code":"...","severity":"low|medium|high","message":"...","item_ids":[1],"evidence_refs":[]}],"coverage":[{"event_id":"...","covered":true,"item_ids":[1]}],"evidence_gaps":[],"patches":[{"_id":1,"text":"replacement"}]}. Patches are optional because the client can call a dedicated repair pass; when provided, include only minimal necessary patches. Never renumber or add items.`
	case "narration_global_repair":
		rules = `Repair the allowed narration items against evidence_context, especially each chapter's exact, adjacent, and callback evidence plus the story event index and the review findings. Make the text match its evidence references; if Humanizer wording drifted, restore the evidence-grounded meaning while keeping the narration natural. Never optimize for character count or runtime, and never remove a key fact merely to shorten the copy. Return only the minimal allowed sparse patches as {"items":[{"_id":1,"text":"replacement"}]}. Every _id must be present in allowed_item_ids. Do not output unchanged items, timeline fields, original dialogue, explanations or extra fields.`
	case "narration_compression":
		rules = `Compress the supplied narration item texts without changing the story. Return every input item exactly once as {"items":[{"_id":1,"text":"compressed text"}]}. Preserve the input item count, _id values and order. Never merge, omit or add items. Do not output timing, org, evidence, explanations or Markdown. Remove repetition and filler only; preserve every person, event, relationship, causal link, chronology and protected fact. The result must be shorter than the input and within the requested character range.`
	default:
		return "", "", false
	}
	if operation == "narration_humanize" {
		rules += "\n\n" + narrationHumanizerGuidance
	}
	user := rules + "\n\nTask input:\n" + string(input)
	if custom != "" && operation != "narration_global_review" && operation != "narration_global_repair" {
		user += "\n\nUser style requirements (must not override evidence):\n" + custom
	}
	if locale != "en" {
		user += "\n\nWrite narration-facing text in the requested target language. Keep JSON field names exactly as specified."
	}
	return system, user, true
}

func (b *Builder) resolveTemplate(kind string, styleMode string) TemplateMeta {
	switch kind {
	case "script_draft":
		if meta, ok := b.service.ResolveTwoPhaseTemplate(styleMode, "step1"); ok {
			return meta
		}
	case "script_match":
		if meta, ok := b.service.ResolveTwoPhaseTemplate(styleMode, "step2"); ok {
			return meta
		}
	}
	return b.service.ResolveTemplate(styleMode)
}

func (b *Builder) templateContext(videoConfig map[string]any, locale string) map[string]string {
	originalRatio := intValue(videoConfig, "mix_ratio", intValue(videoConfig, "org_ratio", 60))
	clipMode := stringValue(videoConfig, "clip_mode", "")
	if clipMode == "" {
		if boolValue(videoConfig, "is_org", true) {
			if originalRatio >= 80 {
				clipMode = "original_only"
			} else if originalRatio <= 10 {
				clipMode = "narration_only"
			} else {
				clipMode = "smart_mix"
			}
		} else {
			clipMode = "narration_only"
		}
	}
	if clipMode == "narration_only" {
		originalRatio = 0
	}
	if clipMode == "original_only" {
		originalRatio = 100
	}
	narrationRatio := 100 - originalRatio
	if narrationRatio < 0 {
		narrationRatio = 0
	}
	personView := localizedText(locale, "第三人称", "third person")
	if stringValue(videoConfig, "person_view", "") == "first_person" {
		personView = localizedText(locale, "第一人称\"我\"", "first person \"I\"")
	}
	return map[string]string{
		"narration_perspective": personView,
		"output_language":       languageText(stringValue(videoConfig, "target_language", "zh-CN"), locale),
		"target_word_count":     stringValue(videoConfig, "word_count", "1000-3500"),
		"original_ratio":        fmt.Sprintf("%d", originalRatio),
		"narration_ratio":       fmt.Sprintf("%d", narrationRatio),
		"audio_speed":           fmt.Sprintf("%v", valueOr(videoConfig["audio_speed"], "5")),
	}
}

func (b *Builder) userPrompt(kind string, req map[string]any, videoConfig map[string]any, templateUserPrompt string, locale string) string {
	videoContext := formatVideoContext(req["video_context"], locale)
	if FormatCacheContext(req["cache_context"]) != "" {
		videoContext = dynamicVideoContextForCache(req["video_context"], locale)
	}
	helpText := strings.TrimSpace(firstStringFromMaps(req, videoConfig, "help_text"))
	customPrompt := strings.TrimSpace(stringValue(req, "custom_prompt", ""))

	var parts []string
	if customPrompt != "" {
		parts = append(parts, localizedText(locale, "## 用户自定义附加要求（高优先级）", "## User custom requirements (high priority)"), customPrompt)
	}
	if kind == "script" || kind == "script_draft" {
		parts = append(parts, narrationEngagementPrompt(locale))
	}
	if templateUserPrompt != "" {
		parts = append(parts, templateUserPrompt)
	}
	switch kind {
	case "script_draft":
		parts = append(parts, scriptDraftRuntimeBlock(videoConfig, videoContext, helpText, locale))
	case "script_match":
		parts = append(parts, scriptMatchRuntimeBlock(videoConfig, videoContext, helpText, approvedNarrationScript(req), locale))
		parts = append(parts, narrationBlockEditingPrompt(locale))
		parts = append(parts, anchoredNarrationMatchPrompt(locale))
		parts = append(parts, openingEndingSongExclusionPrompt(locale))
		parts = append(parts, advertisementExclusionPrompt(locale))
	default:
		parts = append(parts, runtimeBlock(videoConfig, videoContext, helpText, locale))
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func openingEndingSongExclusionPrompt(locale string) string {
	if locale == "en" {
		return strings.Join([]string{
			"## Opening and ending song exclusion (highest priority)",
			"1. Before matching any footage, inspect the chronological transcript, audio events, visual analysis, title cards, credits, repeated theme lyrics, and narrative context. Identify only confidently supported opening-theme and ending-theme intervals. Do not classify ordinary story music, montage music, a cold open, recap, preview, or post-credit story scene as a song interval.",
			"2. Return every confident interval in metadata.excluded_source_ranges using {start, end, type, confidence, reason}. type must be opening_song or ending_song, timestamps are source-video seconds, and confidence must be 0..1. Return an empty array when the evidence is insufficient.",
			"3. Every item start/end must not overlap any excluded_source_ranges interval. This applies to narration footage and original-audio items. Choose a different story interval instead; never shorten the exclusion or use it to fill duration.",
			"4. This section overrides the earlier output rule. Output exactly {\"items\":[...],\"metadata\":{\"excluded_source_ranges\":[...]}} and no other top-level fields.",
		}, "\n")
	}
	return strings.Join([]string{
		"## 片头曲与片尾曲排除（最高优先级）",
		"1. 匹配画面前，结合按时间排列的字幕、音频事件、视觉分析、标题字幕、演职员表、重复主题歌词和剧情理解，仅识别证据充分的片头曲与片尾曲区间。普通剧情配乐、蒙太奇音乐、冷开场、前情回顾、下集预告和片尾彩蛋剧情不得误判。",
		"2. 将可信区间写入 metadata.excluded_source_ranges，每项格式为 {start, end, type, confidence, reason}。type 只能是 opening_song 或 ending_song，时间单位为原视频秒，confidence 范围为 0..1；证据不足时返回空数组。",
		"3. 所有 items 的 start/end 都不得与 excluded_source_ranges 有任何重叠，包括解说画面和原片声片段。必须改选其他剧情区间，不得缩短禁用区间，也不得用禁用区间补足时长。",
		"4. 本节覆盖前面的输出规则。顶层必须严格输出 {\"items\":[...],\"metadata\":{\"excluded_source_ranges\":[...]}}，不得输出其他顶层字段。",
	}, "\n")
}

func advertisementExclusionPrompt(locale string) string {
	if locale == "en" {
		return strings.Join([]string{
			"## Advertisement exclusion (highest priority)",
			"1. Inspect transcript, OCR text, audio speech, visual analysis and story events for inserted advertisements, gambling promotions, casino or betting calls to action, QR codes, URLs, website or app promotion, sponsorship and unrelated marketing.",
			"2. Return each confident advertisement interval in metadata.excluded_source_ranges using {start, end, type, confidence, reason}. type must be advertisement, gambling_ad, website_promotion or inserted_ad. Keep source-video seconds and include the complete advert from entry to return to story.",
			"3. Narration footage and original-audio items must not overlap advertisement ranges. Never classify an advertisement as original footage and never use it to fill duration or ratio.",
			"4. Merge these ranges with opening and ending exclusions in the same metadata.excluded_source_ranges array. When evidence is insufficient, do not guess.",
		}, "\n")
	}
	return strings.Join([]string{
		"## 广告内容排除（最高优先级）",
		"1. 结合字幕、OCR 文字、音频口播、视觉分析和剧情事件，识别插播广告、博彩或赌场推广、投注引导、二维码、网址、网站或应用推广、赞助和与剧情无关的营销内容。",
		"2. 将可信广告区间写入 metadata.excluded_source_ranges，每项格式为 {start, end, type, confidence, reason}。type 必须是 advertisement、gambling_ad、website_promotion 或 inserted_ad，使用原视频秒数，并覆盖从广告进入到恢复剧情的完整区间。",
		"3. 解说画面和原片声片段都不得与广告区间重叠。禁止把广告判定为原片，也禁止用广告补足时长或比例。",
		"4. 广告区间与片头片尾区间合并写入同一个 metadata.excluded_source_ranges；证据不足时不得猜测。",
	}, "\n")
}
func anchoredNarrationMatchPrompt(locale string) string {
	if locale == "en" {
		return strings.Join([]string{
			"## Per-item evidence anchors (highest priority; overrides global picture search and text splitting)",
			"1. Treat every script_item as one immutable narration unit. Return each _id and its exact text once; never merge, split, omit, duplicate, rewrite, or reorder narration items.",
			"2. Choose start and end only inside that item's own evidence window [source_start, source_end]. The matched interval must overlap its event_ids or evidence_refs and must never borrow footage from another item's window.",
			"3. chapter_id, event_ids, evidence_refs, source_start, source_end, and priority are read-only input constraints. The model must not modify anchor fields or infer replacement anchors.",
			"4. If no valid interval exists inside an item's own evidence window, leave that item unmatched for the client to repair individually; do not move already matched items.",
		}, "\n")
	}
	return strings.Join([]string{
		"## 逐条证据锚点（最高优先级，覆盖全局搜索画面和拆分文案规则）",
		"1. 每个 script_item 都是不可变的一条解说。每个 _id 及其原始 text 必须恰好返回一次；不得合并、拆分、遗漏、重复、改写或重排。",
		"2. 每条解说的 start/end 只能从自身 [source_start, source_end] 证据窗口中选择，并且必须与自身 event_ids 或 evidence_refs 对应；禁止借用其他文案的画面窗口。",
		"3. chapter_id、event_ids、evidence_refs、source_start、source_end 和 priority 都是只读约束，模型不得修改锚点字段，也不得猜测替代锚点。",
		"4. 如果某条文案在自身证据窗口内找不到有效画面，让该条保持未匹配并交给客户端单独修复；不得移动已经匹配成功的条目。",
	}, "\n")
}

// narrationBlockEditingPrompt forces the matched timeline into larger
// alternating blocks of original footage and narration instead of switching
// every few seconds, which reads badly in the final video.
func narrationBlockEditingPrompt(locale string) string {
	if locale == "en" {
		return strings.Join([]string{
			"## Editing rhythm: large continuous blocks; original footage then narration (highest priority; overrides the template's non-linear material reuse and global per-line picture search)",
			"1. Build the whole timeline from a few large pairs: original block -> narration block -> original block -> narration block ... Each original block plays 20-40 seconds of original footage only (org items, no narration inside); each narration block speaks 20-40 seconds of narration only (text items, no original audio inside). Keep the total number of original/narration switches around 2-4 per video.",
			"2. A narration block must directly follow and explain the original block that was just shown. Never narrate scenes that have not appeared yet.",
			"3. Original blocks must be continuous, in-order footage. Use one continuous source range (start->end) to cover the whole original block when possible; if more than one clip is required, the clips must be temporally adjacent and their start/end must increase monotonically. It is forbidden to cut the original footage into many small fragments or to jump back and forth across the source.",
			"4. Inside a narration block, every narration unit must be matched to the exact source interval it is describing, and the picture must switch to that interval at the same moment the voice starts (no lead, no lag). Consecutive narration units must use temporally adjacent footage that advances in story order; do not stitch unrelated footage together.",
			"5. Keep the overall narration_ratio / original_ratio unchanged; prefer adjusting unit durations inside a block over splitting blocks into small fragments.",
			"6. Every segment's actual footage length (end - start) must be at least as long as the narration it carries. All start/end values must reference real source footage.",
			"7. Original-footage items may use new temporary _id values that do not conflict with narration ids. Output items in playback order; never sort the timeline by _id. This overrides the earlier increasing-id rule.",
		}, "\n")
	}
	return strings.Join([]string{
		"## 成片节奏：大块连续素材，先原片后解说（最高优先级，覆盖模板中“素材可非线性使用”“全局搜索画面”等指引）",
		"1. 整条时间线由少量大块成对组成：“原片块 → 解说块 → 原片块 → 解说块 …”。每个原片块连续播放 20–40 秒原片（只含 org 片段，不夹解说）；每个解说块连续解说 20–40 秒（只含 text 片段，不夹原片声）；一整条成片的原片/解说切换次数约 2–4 次。",
		"2. 解说块必须紧跟它讲解的原片块：观众先看画面、再听对这段画面的解说；不要提前讲还没有出现的画面。",
		"3. 原片块必须是连续、按时间顺序的素材：尽量用一个连续区间（start→end）覆盖整个原片块；确需多段时，各段必须时间相邻、start/end 单调递增。禁止把原片切成大量小碎片，禁止在全片范围跳来跳去乱序拼贴。",
		"4. 解说块内部画面必须与解说一一对应且连续推进：每条解说的背景画面必须是该解说正在讲解的素材区间，解说开口的同时就切到该画面（不得提前或滞后）；块内相邻解说对应的素材区间必须时间相邻、按剧情顺序推进，禁止把不相邻的画面拼在一起。",
		"5. 保持整体 narration_ratio / original_ratio 不变；优先在块内调整片段时长，不要为了凑比例拆成小碎块。",
		"6. 每个片段的实际素材时长（end - start）不得小于该解说所需时长；所有 start/end 必须指向原视频中的真实素材。",
		"7. 原片段可以使用新的临时 _id（不得与解说 _id 冲突）；items 必须按成片播放顺序输出，不得按 _id 排序时间线。本规则覆盖前面“_id 从 1 递增”的要求。",
	}, "\n")
}

func narrationEngagementPrompt(locale string) string {
	if locale == "en" {
		return strings.Join([]string{
			"## Audience retention and engagement requirements (high priority)",
			"1. The opening determines retention: make the first narration unit suitable for the first five seconds and immediately present the strongest source-supported conflict, pain point, stakes, contrast, or unresolved question. Do not begin with greetings or generic background.",
			"2. Sustain completion through controlled suspense and progressive disclosure. Each unit must add new information or emotional movement; do not repeat facts or deliberately hide context required to understand the story.",
			"3. Create emotion and resonance through concrete actions, dialogue, choices, consequences, and relationship changes rather than empty sensational adjectives.",
			"4. Use controversy only when the source genuinely supports conflicting positions or choices. Never manufacture outrage, polarize characters, or distort facts merely to invite comments.",
			"5. Surface practical value only when the source contains a real lesson, method, warning, or decision reference. Never turn fictional plot into unsupported advice.",
			"6. When appropriate, end with one concise, source-grounded unresolved question, choice, or viewpoint that naturally invites discussion. Do not beg for likes, follows, or comments, and do not invent a cliffhanger.",
			"7. Treat title pain point, cover focus, and channel or category alignment as internal focus-selection checks only. Do not output titles, cover copy, hashtags, tags, or fields outside the required JSON schema.",
			"8. Retention, completion, comments, likes, and saves are optimization goals, never permission to fabricate plot, relationships, motives, dialogue, visual evidence, or outcomes.",
			"9. Choose only the engagement mechanisms that fit the source and selected narration style; do not force suspense, controversy, resonance, and practical value into every story.",
		}, "\n")
	}
	return strings.Join([]string{
		"## 观众留存与互动要求（高优先级）",
		"1. 开头决定留存：第一条解说必须适合放在成片前 5 秒，立即呈现素材中证据最强的冲突、痛点、利害关系、反差或未解问题；不要用问候、空泛背景或与主线无关的铺垫开场。",
		"2. 用信息递进和适度悬念提高完播。每条解说都必须增加新事实、推进关系或推动情绪变化；不得重复同一信息，也不得故意隐去理解剧情所必需的前提。",
		"3. 文案要带情绪，但情绪必须落在人物动作、对白、选择、后果和关系变化上，不要只堆砌“震惊、崩溃、命运、真相”等空泛煽动词。",
		"4. 只有素材真实存在立场冲突、道德选择或理解分歧时，才可以突出争议以引导评论；不得为制造争议歪曲人物、恶意极化或篡改事实。",
		"5. 只有素材确实包含方法、教训、警示或决策参考时，才提炼实用价值以增强收藏动机；不得把虚构剧情包装成无依据的建议。",
		"6. 结尾在合适时使用一个简洁、基于素材的未解问题、选择题或观点问题，自然引导观众讨论；不要乞求点赞、关注或评论，不要虚构后续悬念。",
		"7. 将“标题埋痛点、封面有焦点、标签卡赛道”仅作为内部主线选择和信息聚焦检查：主线应有明确痛点，核心焦点应单一，表达应符合所选解说风格和内容赛道。当前接口不得输出标题、封面文案、标签或 JSON 结构之外的字段。",
		"8. 5 秒留存、平均观看时长、完播、评论、点赞和收藏只是优化目标，绝不是虚构情节、人物关系、动机、对白、画面证据或结局的理由。",
		"9. 根据素材和用户选择的解说风格选用最合适的吸引力机制，不要求每条素材同时强行加入悬念、争议、共鸣和实用价值。",
	}, "\n")
}

func promptTemplateForKind(kind string, templateUserPrompt string) string {
	if kind != "script_draft" && kind != "script_match" {
		return templateUserPrompt
	}
	stageOne, stageTwo, ok := splitScriptStages(templateUserPrompt)
	if !ok {
		return templateUserPrompt
	}
	if kind == "script_draft" {
		return stageOne
	}
	return stageTwo
}

func splitScriptStages(templateUserPrompt string) (string, string, bool) {
	content := strings.TrimSpace(templateUserPrompt)
	stageOneStart := strings.Index(content, scriptStageOneMarker)
	stageTwoStart := strings.Index(content, scriptStageTwoMarker)
	if stageOneStart < 0 || stageTwoStart < 0 || stageTwoStart <= stageOneStart {
		return "", "", false
	}
	stageOne := strings.TrimSpace(content[stageOneStart:stageTwoStart])
	stageOne = strings.TrimSpace(strings.TrimSuffix(stageOne, "---"))
	stageTwo := strings.TrimSpace(content[stageTwoStart:])
	return stageOne, stageTwo, true
}

func scriptDraftRuntimeBlock(videoConfig map[string]any, videoContext, helpText string, locale string) string {
	referenceText, previousText := typeScriptContext(videoConfig, locale)
	if locale == "en" {
		lines := []string{
			"## Actual task input for this run (high priority)",
			"If any examples, default parameters, or output format inside the template conflict with the requirements below, the actual task requirements below take precedence.",
			"- Editing mode: " + clipModeLabel(stringValue(videoConfig, "clip_mode", ""), locale),
			"- Additional notes: " + defaultText(helpText, "None"),
			"- Previous plot summary: " + defaultText(previousText, "None"),
			"- Style reference copy: " + defaultText(referenceText, "None"),
			"- Task requirement: Only complete Stage 1 narration copywriting. Do not perform visual matching, timeline matching, original-audio insertion, or final clip JSON generation.",
			"",
			"### Video context (chronological order)",
			defaultText(videoContext, "None"),
			"",
			"## Narration draft output requirements (system high priority)",
			"1. Output exactly one JSON object only. Do not output Markdown, explanations, analysis, or code fences.",
			"2. The top-level JSON structure must be {\"items\": [...]}.",
			"3. Each item must contain exactly 2 fields: _id and text.",
			"4. _id must be an increasing string starting from \"1\".",
			"5. text must be a reviewable narration paragraph or sentence in English, ordered by narrative flow.",
			"6. Do not output start, end, org, video, summary, analysis, reason, or any visual-matching fields.",
		}
		return strings.Join(lines, "\n")
	}
	lines := []string{
		runtimeMarker,
		"若模板中的示例数据、默认参数、输出格式与以下要求冲突，必须以下述实际任务为准。",
		"- 剪辑模式：" + clipModeLabel(stringValue(videoConfig, "clip_mode", ""), locale),
		"- 辅助说明：" + defaultText(helpText, "无"),
		"- 前情提要：" + defaultText(previousText, "无"),
		"- 风格参考文案：" + defaultText(referenceText, "无"),
		"- 任务要求：本次只完成第一阶段的解说文案创作。不要做画面匹配、时间线匹配、原片声穿插或最终剪辑 JSON 生成。",
	}
	lines = append(lines,
		"",
		"### 视频上下文（按时间顺序）",
		defaultText(videoContext, "无"),
		"",
		"## 解说文案草稿输出要求（系统高优先级）",
		"1. 只能输出单个 JSON 对象，不要输出 Markdown、解释、分析说明或代码块。",
		"2. JSON 顶层结构必须是 {\"items\": [...]}。",
		"3. items 中每个元素必须且只能包含 2 个字段：_id、text。",
		"4. _id 必须是从 \"1\" 开始递增的字符串。",
		"5. text 必须是可供用户审核编辑的解说文案段落或短句，并按叙事顺序排列。",
		"6. 严禁输出 start、end、org、video、summary、analysis、reason 或任何画面匹配字段。",
	)
	return strings.Join(lines, "\n")
}

func scriptMatchRatioLines(videoConfig map[string]any, locale string) []string {
	originalRatio := intValue(videoConfig, "mix_ratio", intValue(videoConfig, "org_ratio", 60))
	if originalRatio < 0 {
		originalRatio = 0
	}
	if originalRatio > 100 {
		originalRatio = 100
	}
	narrationRatio := 100 - originalRatio
	outputDurationMS, hasOutputDuration := firstFloatValue(videoConfig, "output_duration_ms")
	targetOriginalSeconds := outputDurationMS / 1000 * float64(originalRatio) / 100

	if locale == "en" {
		lines := []string{
			fmt.Sprintf("- Target original-footage ratio: %d%%", originalRatio),
			fmt.Sprintf("- Target narration ratio: %d%%", narrationRatio),
		}
		if hasOutputDuration && outputDurationMS > 0 {
			lines = append(lines, fmt.Sprintf("- Target original-footage playback (soft estimate): about %.1f seconds", targetOriginalSeconds))
		}
		if originalRatio > 0 {
			lines = append(lines,
				"- You must output at least one original-footage item with empty text and non-empty org; an all-narration timeline is invalid.",
				"- Play each original-footage block first, then the narration block that explains that same block. Preserve chronological continuity.",
			)
		}
		return lines
	}

	lines := []string{
		fmt.Sprintf("- 目标原片占比：%d%%", originalRatio),
		fmt.Sprintf("- 目标解说占比：%d%%", narrationRatio),
	}
	if hasOutputDuration && outputDurationMS > 0 {
		lines = append(lines, fmt.Sprintf("- 目标原片播放量（软估算）：约 %.1f 秒", targetOriginalSeconds))
	}
	if originalRatio > 0 {
		lines = append(lines,
			"- 必须至少输出一个 text 为空且 org 非空的原片段；全解说时间线视为无效。",
			"- 必须先播放原片块，再播放对该原片块的解说块；素材时间顺序和剧情衔接必须连续。",
		)
	}
	return lines
}
func scriptMatchRuntimeBlock(videoConfig map[string]any, videoContext, helpText, approvedScript string, locale string) string {
	referenceText, previousText := typeScriptContext(videoConfig, locale)
	if approvedScript == "" {
		approvedScript = localizedText(locale, "无", "None")
	}
	if locale == "en" {
		lines := []string{
			"## Actual task input for this run (high priority)",
			"If any examples, default parameters, or output format inside the template conflict with the requirements below, the actual task requirements below take precedence.",
			"- Editing mode: " + clipModeLabel(stringValue(videoConfig, "clip_mode", ""), locale),
			"- Additional notes: " + defaultText(helpText, "None"),
			"- Previous plot summary: " + defaultText(previousText, "None"),
			"- Style reference copy: " + defaultText(referenceText, "None"),
			"- Task requirement: Only complete Stage 2 visual matching. Use the approved narration draft below as fixed input. Do not rewrite the narration.",
			"",
			"### Approved narration draft (must be used)",
			approvedScript,
			"",
			"### Video context (chronological order)",
			defaultText(videoContext, "None"),
			"",
			"## Final output requirements (system high priority)",
			"1. Output exactly one JSON object only. Do not output Markdown, explanations, analysis, or code fences.",
			"2. The top-level JSON structure must be {\"items\": [...]}.",
			"3. Each item must contain exactly 5 fields: _id, text, start, end, org.",
			"4. _id must be an increasing string starting from \"1\".",
			"5. start and end must be numbers in seconds, with reasonable decimals, and end > start.",
			"6. Narration text must come from the approved draft without rewriting; it may only be split into smaller pieces without changing words. Original-audio segments must set text to \"\" and put the matching subtitle in org.",
			"7. Avoid identical time ranges in adjacent segments. The output must be directly JSON-parseable.",
			"8. Do not output extra fields such as video, summary, analysis, or reason.",
		}
		lines = append(lines, "")
		lines = append(lines, scriptMatchRatioLines(videoConfig, locale)...)
		return strings.Join(lines, "\n")
	}
	lines := []string{
		runtimeMarker,
		"若模板中的示例数据、默认参数、输出格式与以下要求冲突，必须以下述实际任务为准。",
		"- 剪辑模式：" + clipModeLabel(stringValue(videoConfig, "clip_mode", ""), locale),
		"- 辅助说明：" + defaultText(helpText, "无"),
		"- 前情提要：" + defaultText(previousText, "无"),
		"- 风格参考文案：" + defaultText(referenceText, "无"),
		"- 任务要求：本次只完成第二阶段的画面匹配。必须以下方用户确认后的解说文案作为固定输入，不得改写文案。",
	}
	lines = append(lines, scriptMatchRatioLines(videoConfig, locale)...)
	lines = append(lines, "")
	lines = append(lines,
		"",
		"### 已确认解说文案（必须逐条使用）",
		approvedScript,
		"",
		"### 视频上下文（按时间顺序）",
		defaultText(videoContext, "无"),
		"",
		"## 最终输出要求（系统高优先级）",
		"1. 只能输出单个 JSON 对象，不要输出 Markdown、解释、分析说明或代码块。",
		"2. JSON 顶层结构必须是 {\"items\": [...]}。",
		"3. items 中每个元素必须且只能包含 5 个字段：_id、text、start、end、org。",
		"4. _id 必须是从 \"1\" 开始递增的字符串。",
		"5. start 和 end 必须是数字（单位：秒），保留合理小数即可，且 end > start。",
		"6. 解说片段的 text 必须来自已确认文案，不得改写；只允许在不改字的前提下拆成更小片段。保留原片声时 text 必须为 \"\"、org 填对应字幕。",
		"7. 相邻片段避免完全相同的时间区间；输出内容必须能直接通过 JSON 解析。",
		"8. 严禁输出 video、summary、analysis、reason 等额外字段。",
	)
	return strings.Join(lines, "\n")
}

func approvedNarrationScript(req map[string]any) string {
	for _, key := range []string{"script_items", "approved_script_items", "draft_items", "narration_draft_items"} {
		if formatted := formatApprovedNarrationItems(req[key]); formatted != "" {
			return formatted
		}
	}
	return ""
}

func formatApprovedNarrationItems(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	default:
		raw, err := json.MarshalIndent(typed, "", "  ")
		if err != nil {
			return strings.TrimSpace(fmt.Sprint(typed))
		}
		return strings.TrimSpace(string(raw))
	}
}

func runtimeBlock(videoConfig map[string]any, videoContext, helpText string, locale string) string {
	referenceText, previousText := typeScriptContext(videoConfig, locale)
	if locale == "en" {
		lines := []string{
			"## Actual task input for this run (high priority)",
			"If any examples, default parameters, or output format inside the template conflict with the requirements below, the actual task requirements below take precedence.",
			"- Editing mode: " + clipModeLabel(stringValue(videoConfig, "clip_mode", ""), locale),
			"- Additional notes: " + defaultText(helpText, "None"),
			"- Previous plot summary: " + defaultText(previousText, "None"),
			"- Style reference copy: " + defaultText(referenceText, "None"),
			"- Task requirement: In one response, complete narration copywriting, segment splitting, and visual matching. Do not output any intermediate draft.",
			"",
			"### Video context (chronological order)",
			defaultText(videoContext, "None"),
			"",
			"## Final output requirements (system high priority)",
			"1. Output exactly one JSON object only. Do not output Markdown, explanations, analysis, or code fences.",
			"2. The top-level JSON structure must be {\"items\": [...]}.",
			"3. Each item must contain exactly 5 fields: _id, text, start, end, org.",
			"4. _id must be an increasing string starting from \"1\".",
			"5. start and end must be numbers in seconds, with reasonable decimals, and end > start.",
			"6. Narration segments must put content in text and set org to \"\". Original-audio segments must set text to \"\" and put the matching subtitle in org.",
			"7. Avoid identical time ranges in adjacent segments. The output must be directly JSON-parseable.",
			"8. Do not output extra fields such as video, summary, analysis, or reason.",
			"9. Generate all narration copy in English.",
		}
		return strings.Join(lines, "\n")
	}
	lines := []string{
		runtimeMarker,
		"若模板中的示例数据、默认参数、输出格式与以下要求冲突，必须以下述实际任务为准。",
		"- 剪辑模式：" + clipModeLabel(stringValue(videoConfig, "clip_mode", ""), locale),
		"- 辅助说明：" + defaultText(helpText, "无"),
		"- 前情提要：" + defaultText(previousText, "无"),
		"- 风格参考文案：" + defaultText(referenceText, "无"),
		"- 任务要求：在一次回答中同时完成解说文案创作、片段拆分与画面匹配，不要输出任何中间草稿。",
	}
	lines = append(lines,
		"",
		"### 视频上下文（按时间顺序）",
		defaultText(videoContext, "无"),
		"",
		"## 最终输出要求（系统高优先级）",
		"1. 只能输出单个 JSON 对象，不要输出 Markdown、解释、分析说明或代码块。",
		"2. JSON 顶层结构必须是 {\"items\": [...]}。",
		"3. items 中每个元素必须且只能包含 5 个字段：_id、text、start、end、org。",
		"4. _id 必须是从 \"1\" 开始递增的字符串。",
		"5. start 和 end 必须是数字（单位：秒），保留合理小数即可，且 end > start。",
		"6. 解说片段使用 text 填内容、org 必须为 \"\"；保留原片声时 text 必须为 \"\"、org 填对应字幕。",
		"7. 相邻片段避免完全相同的时间区间；输出内容必须能直接通过 JSON 解析。",
		"8. 严禁输出 video、summary、analysis、reason 等额外字段。",
	)
	return strings.Join(lines, "\n")
}

func stripEmbeddedExamples(templateText string) string {
	content := strings.TrimSpace(templateText)
	if idx := strings.Index(content, "# 输入SRT数据 (SRT Content)"); idx >= 0 {
		content = strings.TrimSpace(content[:idx])
	}
	if strings.Contains(content, "以下数据将贯穿整个任务流程：") && strings.Contains(content, "# 总工作流程") {
		parts := strings.SplitN(content, "# 总工作流程", 2)
		if len(parts) == 2 {
			content = "# 总工作流程" + parts[1]
		}
	}
	return regexp.MustCompile(`\n{3,}`).ReplaceAllString(strings.TrimSpace(content), "\n\n")
}

func typeScriptContext(videoConfig map[string]any, locale string) (string, string) {
	value := strings.TrimSpace(stringValue(videoConfig, "type_script_value", ""))
	switch stringValue(videoConfig, "type_script", "") {
	case "reference":
		return defaultText(value, localizedText(locale, "无", "None")), localizedText(locale, "无", "None")
	case "previous":
		return localizedText(locale, "无", "None"), defaultText(value, localizedText(locale, "无", "None"))
	default:
		return localizedText(locale, "无", "None"), localizedText(locale, "无", "None")
	}
}

func mapValue(source map[string]any, key string) map[string]any {
	if value, ok := source[key].(map[string]any); ok {
		return value
	}
	return map[string]any{}
}

type videoContextSegment struct {
	Start    float64
	End      float64
	Subtitle string
	Visual   string
	Speaker  string
	Index    int
}

func formatVideoContext(value any, locale string) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		if items, ok := typed["items"]; ok {
			formattedItems := formatVideoContext(items, locale)
			narrative := formatNarrativeContext(typed["narrative_context"], locale)
			switch {
			case narrative != "" && formattedItems != "":
				return narrative + "\n\n" + formattedItems
			case narrative != "":
				return narrative
			case formattedItems != "":
				return formattedItems
			}
		}
		if segment, ok := videoContextSegmentFromMap(typed, 0); ok {
			return formatVideoContextSegments([]videoContextSegment{segment}, locale)
		}
		segments := make([]videoContextSegment, 0, len(typed))
		for _, item := range typed {
			if itemMap, ok := item.(map[string]any); ok {
				if segment, ok := videoContextSegmentFromMap(itemMap, len(segments)); ok {
					segments = append(segments, segment)
				}
			}
		}
		if len(segments) > 0 {
			return formatVideoContextSegments(segments, locale)
		}
	case []any:
		segments := make([]videoContextSegment, 0, len(typed))
		for _, item := range typed {
			if itemMap, ok := item.(map[string]any); ok {
				if segment, ok := videoContextSegmentFromMap(itemMap, len(segments)); ok {
					segments = append(segments, segment)
				}
			}
		}
		if len(segments) > 0 {
			return formatVideoContextSegments(segments, locale)
		}
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return strings.TrimSpace(fmt.Sprint(value))
	}
	return strings.TrimSpace(string(raw))
}

func videoContextSegmentFromMap(item map[string]any, index int) (videoContextSegment, bool) {
	start, hasStart := firstFloatValue(item, "start_time", "start")
	end, hasEnd := firstFloatValue(item, "end_time", "end")
	subtitle := strings.TrimSpace(firstStringValue(item, "subtitle", "text", "content"))
	visual := strings.TrimSpace(firstStringValue(item, "visual", "picture", "description"))
	speaker := strings.TrimSpace(firstContextStringValue(item, "speaker_id", "speakerId", "speaker", "speaker_id_label"))

	if !hasStart || !hasEnd {
		return videoContextSegment{}, false
	}
	if subtitle == "" && visual == "" {
		return videoContextSegment{}, false
	}

	return videoContextSegment{
		Start:    start,
		End:      end,
		Subtitle: subtitle,
		Visual:   visual,
		Speaker:  speaker,
		Index:    index,
	}, true
}

func formatVideoContextSegments(segments []videoContextSegment, locale string) string {
	sort.SliceStable(segments, func(i, j int) bool {
		if segments[i].Start == segments[j].Start {
			return segments[i].Index < segments[j].Index
		}
		return segments[i].Start < segments[j].Start
	})

	lines := []string{fmt.Sprintf("Total Segments: %d", len(segments))}
	for _, segment := range segments {
		parts := []string{fmt.Sprintf("[%s -> %s]", formatContextSecond(segment.Start), formatContextSecond(segment.End))}
		if segment.Speaker != "" {
			parts = append(parts, localizedText(locale, "(说话人 "+segment.Speaker+")", "(speaker "+segment.Speaker+")"))
		}
		if segment.Subtitle != "" {
			parts = append(parts, segment.Subtitle)
		}
		if segment.Visual != "" {
			parts = append(parts, localizedText(locale, "(视觉)", "(visual) ")+segment.Visual)
		}
		lines = append(lines, strings.Join(parts, " "))
	}
	return strings.Join(lines, "\n")
}

func formatNarrativeContext(value any, locale string) string {
	if value == nil {
		return ""
	}
	text := strings.TrimSpace(formatVideoContextRaw(value))
	if text == "" || text == "null" || text == "{}" || text == "[]" {
		return ""
	}
	title := localizedText(locale, "### 全局剧情理解（供解说文案与画面匹配优先参考）", "### Global narrative understanding (prioritize for narration and visual matching)")
	return title + "\n" + text
}

func dynamicVideoContextForCache(value any, locale string) string {
	pointer := localizedText(locale,
		"主要字幕、视觉分析和切点信息见上方可缓存共享素材上下文。",
		"Primary transcript, visual analysis, and timeline decisions are in the cacheable shared context above.",
	)
	if typed, ok := value.(map[string]any); ok {
		if narrative := formatNarrativeContext(typed["narrative_context"], locale); narrative != "" {
			return pointer + "\n\n" + narrative
		}
	}
	return pointer
}

func FormatCacheContext(value any) string {
	text := strings.TrimSpace(formatVideoContextRaw(value))
	if text == "" || text == "null" || text == "{}" || text == "[]" {
		return ""
	}
	return text
}

func formatVideoContextRaw(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		raw, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return fmt.Sprint(value)
		}
		return string(raw)
	}
}

func formatContextSecond(value float64) string {
	text := fmt.Sprintf("%.2f", value)
	text = strings.TrimRight(text, "0")
	text = strings.TrimRight(text, ".")
	if text == "" || text == "-0" {
		return "0"
	}
	return text
}

func stringValue(source map[string]any, key, fallback string) string {
	if value, ok := source[key].(string); ok && value != "" {
		return value
	}
	return fallback
}

func firstStringValue(source map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := source[key].(string); ok {
			return value
		}
	}
	return ""
}

func firstContextStringValue(source map[string]any, keys ...string) string {
	for _, key := range keys {
		switch value := source[key].(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				return value
			}
		case float64:
			return formatContextSecond(value)
		case int:
			return strconv.Itoa(value)
		case int64:
			return strconv.FormatInt(value, 10)
		case json.Number:
			return value.String()
		}
	}
	return ""
}

func firstFloatValue(source map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		switch value := source[key].(type) {
		case float64:
			return value, true
		case float32:
			return float64(value), true
		case int:
			return float64(value), true
		case int64:
			return float64(value), true
		case json.Number:
			if parsed, err := value.Float64(); err == nil {
				return parsed, true
			}
		case string:
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

func firstStringFromMaps(primary map[string]any, secondary map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(primary, key, ""); value != "" {
			return value
		}
		if value := stringValue(secondary, key, ""); value != "" {
			return value
		}
	}
	return ""
}

func intValue(source map[string]any, key string, fallback int) int {
	switch value := source[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	case json.Number:
		i, _ := value.Int64()
		return int(i)
	default:
		return fallback
	}
}

func boolValue(source map[string]any, key string, fallback bool) bool {
	if value, ok := source[key].(bool); ok {
		return value
	}
	return fallback
}

func valueOr(value any, fallback any) any {
	if value == nil {
		return fallback
	}
	return value
}

func promptLocaleFromConfig(videoConfig map[string]any) string {
	targetLanguage := strings.ToLower(strings.TrimSpace(stringValue(videoConfig, "target_language", "zh-CN")))
	if strings.HasPrefix(targetLanguage, "en") || targetLanguage == "english" || targetLanguage == "英文" {
		return "en"
	}
	return "zh-CN"
}

func localizedText(locale, zhCN, en string) string {
	if locale == "en" {
		return en
	}
	return zhCN
}

func languageText(value string, locale string) string {
	switch value {
	case "zh-CN":
		return localizedText(locale, "中文", "Chinese")
	case "zh-TW":
		return localizedText(locale, "繁體中文", "Traditional Chinese")
	case "en-US":
		return localizedText(locale, "英文", "English")
	case "ja-JP":
		return localizedText(locale, "日文", "Japanese")
	case "ko-KR":
		return localizedText(locale, "韩文", "Korean")
	default:
		if value == "" {
			return localizedText(locale, "中文", "Chinese")
		}
		return value
	}
}

func personViewText(value string) string {
	if value == "first_person" {
		return "第一人称"
	}
	return "第三人称"
}

func clipModeLabel(value string, locale string) string {
	switch value {
	case "narration_only":
		return localizedText(locale, "纯解说", "narration only")
	case "original_only":
		return localizedText(locale, "纯原片", "original only")
	case "smart_mix", "":
		return localizedText(locale, "智能穿插", "smart mix")
	default:
		return value
	}
}

func defaultText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
