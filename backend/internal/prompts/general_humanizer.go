package prompts

import (
	"crypto/sha256"
	_ "embed"
	"fmt"
	"strings"
)

// Vendored verbatim from holygeek00/humanizer-zh-cn (MIT), version 2.9.1-zh.2.
// Embedding makes the installed rule set available to every deployed backend;
// production never depends on the developer's Codex home directory.
//
//go:embed skills/humanizer-zh-cn/SKILL.md
var generalHumanizerSkill string

func generalUsesHumanizer(kind string, req map[string]any) bool {
	ctx := mapValue(req, "video_context")
	writesNarration := stringValue(ctx, "purpose", "") == "script" || (stringValue(ctx, "purpose", "") == "original-layout" && ctx["allowNarrationRepair"] == true)
	return kind == "script_draft" && stringValue(req, "model_operation", "") == "narration_outline" &&
		stringValue(ctx, "contract", "") == "general-narration-v3" && writesNarration
}

// Take the actual selected template's writing section, excluding its legacy
// output schema and prohibitions on original markers. Nested style headings
// (e.g. montage's editing-style subsections) remain part of the reference.
func generalSelectedStyle(template string) string {
	lines := strings.Split(strings.ReplaceAll(template, "\r\n", "\n"), "\n")
	var selected []string
	inside := false
	for _, line := range lines {
		title := strings.TrimSpace(line)
		if strings.HasPrefix(title, "## ") && strings.Contains(title, "创作风格") {
			inside = true
		}
		if inside && (title == "## 创作流程" || title == "## 文案输出要求" || title == "## 禁止事项") {
			break
		}
		if inside {
			selected = append(selected, line)
		}
	}
	if len(selected) == 0 {
		// Custom templates need not follow the bundled Markdown headings.
		// The system-level scope below still prevents their schemas from winning.
		return strings.TrimSpace(template)
	}
	return strings.TrimSpace(strings.Join(selected, "\n"))
}

func generalHumanizedPrompt(system, user string, rendered RenderedTemplate, context map[string]string) (string, string) {
	system += `
GENERAL HUMANIZER INTEGRATION general-humanizer-zh-cn-v1:
先依据当前素材和用户所选风格构思，再在同一次响应内部按 humanizer-zh-cn 嵌入模式审校，返回最终稿。不要增加独立审核步骤。
优先级：本次任务的事实依据、输出 JSON 契约和局部修复范围 > 用户明确的语言、视角、所选风格与原片比例 > 去 AI 味要求 > 通用口语化默认语气。所选风格可改变节奏、幽默程度和情绪浓度；不要把所有模式写成同一种口吻。
下面的模板只提供写作声线参考，示例不是当前素材事实。忽略其中与本次 JSON、原片计划或配音流程冲突的旧输出/剪辑要求。技能使用嵌入模式，只润色允许修改的解说正文，不输出初稿、审校意见、评分、解释或额外字段。
保留事实、姓名、数量、事件顺序和真正的不确定性；不得用编造细节换取自然感。原片编号、[[ORIGINAL:id]] 标记及锁定范围不可删除或改写；局部修复不得扩展到整稿，expectedText 存在时逐字恢复。0% 解说仍只输出原片计划，不添加引导解说。原片前只铺垫未揭晓的冲突，原片后推进新后果，不复述刚播过的内容。
若当前任务为 original-layout 且明确允许 narrationRepairs，仅润色列出的冲突解说段，保留本步骤的 originals 与 narrationRepairs JSON 契约；原片边缘允许按本次编排修复授权缩短。不得改写其他段落或另写整稿。`
	style := generalSelectedStyle(rendered.UserPrompt)
	if style == "" {
		style = rendered.SystemPrompt
	}
	prefix := fmt.Sprintf("SELECTED STYLE TEMPLATE: %s (%s)\n语言：%s；叙事视角：%s。字数和时长遵循本次任务预算。\n%s\n", rendered.Meta.Title, rendered.Meta.ID, context["output_language"], context["narration_perspective"], style)
	return system, prefix + "\nHUMANIZER-ZH-CN SKILL (embedded mode; style guidance only):\n" + generalHumanizerSkill + "\n\nCURRENT TASK CONTRACT AND INPUT:\n" + user
}

// Hash the actual prompt, including resolved style text and embedded skill, so
// retries after a writing-policy/template change cannot replay an older draft.
func (b *Builder) GeneralEditingPromptFingerprint(kind string, req map[string]any) string {
	if !generalUsesHumanizer(kind, req) {
		return GeneralEditingPromptFingerprint(kind, req)
	}
	parts, err := b.Build(kind, req)
	if err != nil {
		return GeneralEditingPromptFingerprint(kind, req)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(parts.SystemPrompt+"\x00"+parts.UserPrompt)))
}
