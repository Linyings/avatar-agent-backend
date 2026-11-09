// review.go — 审核门控（Confirmation Gate）
// 对应 NarratoAI-PC movie-composition-dialogue.service.ts + movie-confirmation-planner.service.ts
package agent

import (
	"fmt"
	"strings"
)

// ─────────────────────────────────────────────
// 审核类型定义
// ─────────────────────────────────────────────

// ReviewType 审核类型
type ReviewType string

const (
	ReviewTypeScript   ReviewType = "script"   // F02 文案审核（非阻断）
	ReviewTypeVoice    ReviewType = "voice"    // F04 配音审核（阻断式，整批）
	ReviewTypeMaterial ReviewType = "material" // F05 素材需求审核（阻断式）
	ReviewTypeShot     ReviewType = "shot"     // F10 分镜审核（提醒式 + 阻断式）
	ReviewTypeFact     ReviewType = "fact"     // 事实核验（阻断式，逐段）
)

// ReviewStatus 审核状态
type ReviewStatus string

const (
	ReviewStatusPending  ReviewStatus = "pending"  // 待审核
	ReviewStatusPassed   ReviewStatus = "passed"   // 通过
	ReviewStatusFailed   ReviewStatus = "failed"   // 失败，需用户确认
	ReviewStatusAccepted ReviewStatus = "accepted" // 用户已接受
	ReviewStatusFixed    ReviewStatus = "fixed"    // 用户已修复
)

// ReviewFinding 审核发现的问题
type ReviewFinding struct {
	ID        string    `json:"id"`
	SegmentID string    `json:"segmentId,omitempty"`
	Category  IssueKind `json:"category"` // fact/coverage/redundancy/format
	Message   string    `json:"message"`
	Expected  string    `json:"expected,omitempty"`
	Actual    string    `json:"actual,omitempty"`
	Blocking  bool      `json:"blocking"`
}

// ReviewResult 审核结果
type ReviewResult struct {
	Type     ReviewType      `json:"type"`
	Status   ReviewStatus    `json:"status"`
	Findings []ReviewFinding `json:"findings"`
	Question *QuestionEvent  `json:"question,omitempty"` // 需要用户回答时非空
	// Fingerprint 输入指纹（用于缓存，内容未变时复用）
	Fingerprint string `json:"fingerprint"`
}

// UserConfirmation 用户对审核问题的确认记录
type UserConfirmation struct {
	QuestionID string `json:"questionId"`
	ChoiceKey  string `json:"choiceKey"`
	CustomText string `json:"customText,omitempty"`
	SegmentID  string `json:"segmentId,omitempty"`
	Revision   int    `json:"revision"`
}

// ─────────────────────────────────────────────
// F02 文案审核（非阻断）
// ─────────────────────────────────────────────

// ScriptReviewInput F02 审核输入
type ScriptReviewInput struct {
	Segments       []ScriptSegment
	TargetDuration string // 目标时长，如 "3-5分钟"
	Topic          string
	Style          string
}

// ReviewScript 审核文案质量
// 无问题时返回 passed，有问题时生成确认选项
func ReviewScript(input ScriptReviewInput) ReviewResult {
	fingerprint := ComputeFingerprint(fmt.Sprintf("%v%s%s", input.Segments, input.TargetDuration, input.Topic))

	var findings []ReviewFinding

	// 检查结构完整性
	if len(input.Segments) < 2 {
		findings = append(findings, ReviewFinding{
			ID:       "struct-001",
			Category: IssueKindCoverage,
			Message:  "文案段落不足，建议至少包含开场、主体、结尾三个部分",
			Blocking: false,
		})
	}

	// 检查各段字数是否合理
	for _, seg := range input.Segments {
		charCount := len([]rune(seg.Content))
		if charCount < 30 {
			findings = append(findings, ReviewFinding{
				ID:        "len-" + seg.ID,
				SegmentID: seg.ID,
				Category:  IssueKindCoverage,
				Message:   fmt.Sprintf("%s 文案过短（%d 字），可能导致配音时间不足", segmentLabel(seg.ID), charCount),
				Blocking:  false,
			})
		}
		if charCount > 300 {
			findings = append(findings, ReviewFinding{
				ID:        "len-" + seg.ID + "-long",
				SegmentID: seg.ID,
				Category:  IssueKindCoverage,
				Message:   fmt.Sprintf("%s 文案过长（%d 字），单段超过 300 字可能影响节奏", segmentLabel(seg.ID), charCount),
				Blocking:  false,
			})
		}
	}

	if len(findings) == 0 {
		return ReviewResult{
			Type:        ReviewTypeScript,
			Status:      ReviewStatusPassed,
			Fingerprint: fingerprint,
		}
	}

	// 生成确认选项（禁止"是/否"空选项）
	question := buildScriptReviewQuestion(findings)

	return ReviewResult{
		Type:        ReviewTypeScript,
		Status:      ReviewStatusFailed,
		Findings:    findings,
		Question:    question,
		Fingerprint: fingerprint,
	}
}

func buildScriptReviewQuestion(findings []ReviewFinding) *QuestionEvent {
	msgs := make([]string, 0, len(findings))
	for _, f := range findings {
		msgs = append(msgs, f.Message)
	}

	return &QuestionEvent{
		Type:       "question",
		QuestionID: "script-review-" + findings[0].ID,
		Title:      "文案审核发现以下问题",
		Body:       strings.Join(msgs, "\n"),
		Choices: []ConfirmChoice{
			{
				Key:         "agent-fix",
				Label:       "让 Agent 修改",
				Description: "Agent 将针对上述问题重新调整文案，保持其他段落不变",
				Recommended: true,
			},
			{
				Key:         "self-edit",
				Label:       "我自己修改",
				Description: "打开文案编辑器手动修改",
			},
			{
				Key:         "accept",
				Label:       "接受当前版本继续",
				Description: "不修改，直接进入下一步",
			},
		},
		AllowCustom: false,
	}
}

// ─────────────────────────────────────────────
// F05 素材需求审核（阻断式）
// ─────────────────────────────────────────────

// MaterialReviewInput F05 审核输入
type MaterialReviewInput struct {
	Needs    []MaterialNeed
	Segments []ScriptSegment
}

// ReviewMaterials 审核素材需求合理性
func ReviewMaterials(input MaterialReviewInput) ReviewResult {
	fingerprint := ComputeFingerprint(fmt.Sprintf("%v", input.Needs))

	var findings []ReviewFinding

	// 检查每段素材需求是否覆盖
	segCovered := make(map[string]bool)
	for _, n := range input.Needs {
		segCovered[n.SegmentID] = true
	}
	for _, seg := range input.Segments {
		if !segCovered[seg.ID] {
			findings = append(findings, ReviewFinding{
				ID:        "coverage-" + seg.ID,
				SegmentID: seg.ID,
				Category:  IssueKindCoverage,
				Message:   fmt.Sprintf("%s 未规划素材需求", segmentLabel(seg.ID)),
				Blocking:  true,
			})
		}
	}

	if len(findings) == 0 {
		return ReviewResult{
			Type:        ReviewTypeMaterial,
			Status:      ReviewStatusPassed,
			Fingerprint: fingerprint,
		}
	}

	question := buildMaterialReviewQuestion(findings)

	return ReviewResult{
		Type:        ReviewTypeMaterial,
		Status:      ReviewStatusFailed,
		Findings:    findings,
		Question:    question,
		Fingerprint: fingerprint,
	}
}

func buildMaterialReviewQuestion(findings []ReviewFinding) *QuestionEvent {
	msgs := make([]string, 0, len(findings))
	for _, f := range findings {
		msgs = append(msgs, f.Message)
	}

	return &QuestionEvent{
		Type:       "question",
		QuestionID: "material-review-" + findings[0].ID,
		Title:      "素材需求审核",
		Body:       strings.Join(msgs, "\n"),
		Choices: []ConfirmChoice{
			{
				Key:         "agent-replan",
				Label:       "让 Agent 重新规划",
				Description: "Agent 将重新分析文案，补充缺失的素材需求",
				Recommended: true,
			},
			{
				Key:         "self-adjust",
				Label:       "手动调整",
				Description: "打开素材需求清单手动补充",
			},
		},
		AllowCustom: false,
	}
}

// ─────────────────────────────────────────────
// F10 分镜审核（提醒式 + 阻断式混合）
// ─────────────────────────────────────────────

// ShotReviewInput F10 审核输入
type ShotReviewInput struct {
	Shots    []Shot
	Segments []ScriptSegment
	Needs    []MaterialNeed
}

// ShotReviewReport 分镜审核报告
type ShotReviewReport struct {
	// Marks 提醒式标记（注入 Agent 上下文，不阻断）
	Marks []ShotMark
	// BlockingFindings 阻断式发现（需要用户选择）
	BlockingFindings []ReviewFinding
}

// ShotMark 画面匹配提醒标记
type ShotMark struct {
	ShotID    int    `json:"shotId"`
	SegmentID int    `json:"segmentId"`
	Kind      string `json:"kind"` // "picture_doubt" | "duration_short" | "diversity_low"
	Message   string `json:"message"`
}

// ReviewShots 审核分镜方案
func ReviewShots(input ShotReviewInput) ShotReviewReport {
	var marks []ShotMark
	var findings []ReviewFinding

	// 检查画面多样性（是否全是同一布局）
	layoutCounts := make(map[string]int)
	for _, s := range input.Shots {
		layoutCounts[s.Layout]++
	}
	if len(input.Shots) >= 4 {
		for layout, cnt := range layoutCounts {
			if float64(cnt)/float64(len(input.Shots)) > 0.8 {
				marks = append(marks, ShotMark{
					Kind:    "diversity_low",
					Message: fmt.Sprintf("画面布局单一：%d/%d 个镜头都是 %s 布局，建议增加多样性", cnt, len(input.Shots), layout),
				})
			}
		}
	}

	// 检查素材与台词语义匹配（简化版：avatar 段落不应全用动画）
	avatarSegments := 0
	for _, n := range input.Needs {
		if n.Type == "avatar" {
			avatarSegments++
		}
	}
	if len(input.Needs) > 0 && float64(avatarSegments)/float64(len(input.Needs)) > 0.9 {
		findings = append(findings, ReviewFinding{
			ID:       "avatar-ratio",
			Category: IssueKindCoverage,
			Message:  fmt.Sprintf("数字人出镜比例过高（%d/%d 段），全程人物口播可能显得单调", avatarSegments, len(input.Needs)),
			Blocking: true,
		})
	}

	return ShotReviewReport{
		Marks:            marks,
		BlockingFindings: findings,
	}
}

// BuildShotReviewQuestion 生成分镜审核确认问题
func BuildShotReviewQuestion(findings []ReviewFinding) *QuestionEvent {
	msgs := make([]string, 0, len(findings))
	for _, f := range findings {
		msgs = append(msgs, f.Message)
	}

	return &QuestionEvent{
		Type:       "question",
		QuestionID: "shot-review-" + findings[0].ID,
		Title:      "分镜审核发现问题",
		Body:       strings.Join(msgs, "\n"),
		Choices: []ConfirmChoice{
			{
				Key:         "add-animation",
				Label:       "增加动画素材比例",
				Description: "Agent 将重新规划部分段落，加入动画或真实素材，减少纯人物口播",
				Recommended: true,
			},
			{
				Key:         "keep-current",
				Label:       "保持当前方案",
				Description: "接受现有分镜，继续生成有声预览",
			},
			{
				Key:         "self-adjust",
				Label:       "手动调整分镜",
				Description: "打开分镜编辑器手动修改各镜头布局",
			},
		},
		AllowCustom: false,
	}
}

// ─────────────────────────────────────────────
// 确认选项质量门（禁止空选项）
// 对应 NarratoAI-PC contextualConfirmation + 选项质量过滤
// ─────────────────────────────────────────────

// ValidateConfirmChoices 校验确认选项质量
// 禁止：是/否、忽略/重试/继续 等空选项
// 每个选项必须有真实的 Description
func ValidateConfirmChoices(choices []ConfirmChoice) []string {
	var errors []string
	bannedLabels := []string{"是", "否", "忽略", "重试", "继续", "ok", "取消", "yes", "no"}

	for i, c := range choices {
		// 禁止空 description
		if strings.TrimSpace(c.Description) == "" {
			errors = append(errors, fmt.Sprintf("选项 %d (%q) 缺少 description，必须说明选择后会发生什么", i+1, c.Label))
		}
		// 禁止空洞选项
		for _, banned := range bannedLabels {
			if strings.EqualFold(strings.TrimSpace(c.Label), banned) {
				errors = append(errors, fmt.Sprintf("选项 %d 使用了禁止的空洞标签 %q，必须替换为具体内容描述", i+1, c.Label))
			}
		}
	}

	// 必须 2-4 个选项
	if len(choices) < 2 {
		errors = append(errors, "确认选项不足 2 个，必须提供至少 2 个有意义的选择")
	}
	if len(choices) > 4 {
		errors = append(errors, "确认选项超过 4 个，请精简为最关键的 2-4 个")
	}

	return errors
}
