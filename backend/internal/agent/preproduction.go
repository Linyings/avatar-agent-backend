// preproduction.go — 前期沟通流程
// Agent 在任务开始前与用户对话确认 4 项内容，确认后生成 F01 草稿
package agent

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────
// 前期沟通数据结构
// ─────────────────────────────────────────────

// PreProductionStep 前期沟通步骤
type PreProductionStep string

const (
	StepScriptDirection  PreProductionStep = "script_direction"  // 步骤 1：文案方向
	StepProductionMethod PreProductionStep = "production_method" // 步骤 2：实现方式
	StepMaterialNeeds    PreProductionStep = "material_needs"    // 步骤 3：素材裁剪需求
	StepFinalExpectation PreProductionStep = "final_expectation" // 步骤 4：最终效果预期
	StepComplete         PreProductionStep = "complete"
)

// PreProductionPlan 前期沟通方案
type PreProductionPlan struct {
	mu               sync.Mutex
	ID               string            `json:"id"`
	ProjectID        string            `json:"projectId"`
	UserID           string            `json:"userId"`
	CurrentStep      PreProductionStep `json:"currentStep"`
	ScriptDirection  *ScriptDirection  `json:"scriptDirection,omitempty"`
	ProductionMethod *ProductionMethod `json:"productionMethod,omitempty"`
	MaterialNeeds    []MaterialNeed    `json:"materialNeeds,omitempty"`
	FinalExpectation *FinalExpectation `json:"finalExpectation,omitempty"`
	Status           string            `json:"status"` // "draft" | "confirmed"
	ConfirmedAt      *time.Time        `json:"confirmedAt,omitempty"`
	CreatedAt        time.Time         `json:"createdAt"`
}

// ScriptDirection 文案方向（步骤 1）
type ScriptDirection struct {
	Title       string `json:"title"`
	Audience    string `json:"audience"`
	Style       string `json:"style"`
	EstDuration string `json:"estDuration"`
	Structure   string `json:"structure"`
}

// ProductionMethod 实现方式（步骤 2）
type ProductionMethod struct {
	AvatarEnabled bool   `json:"avatarEnabled"`
	AnimationType string `json:"animationType"` // 矢量/线条/白板/none
	RealFootage   bool   `json:"realFootage"`
	AspectRatio   string `json:"aspectRatio"` // 9:16 / 16:9 / 1:1
	VoiceID       string `json:"voiceId"`
	AngleMode     string `json:"angleMode"` // single / multi
}

// FinalExpectation 最终效果预期（步骤 4）
type FinalExpectation struct {
	TotalDuration  string `json:"totalDuration"`
	PacingStyle    string `json:"pacingStyle"`
	AspectRatio    string `json:"aspectRatio"`
	VoiceConfirmed bool   `json:"voiceConfirmed"`
	Description    string `json:"description"`
}

// ─────────────────────────────────────────────
// 前期沟通会话管理
// ─────────────────────────────────────────────

// preProductionStore 前期沟通会话仓库（按项目存储）
type preProductionStore struct {
	mu    sync.Mutex
	plans map[string]*PreProductionPlan // projectID -> plan
}

var globalPreProduction = &preProductionStore{plans: make(map[string]*PreProductionPlan)}

// StartPreProduction 开始前期沟通
// M-06 修复：若已有进行中的计划则返回它，不覆盖
func StartPreProduction(projectID, userID string) *PreProductionPlan {
	globalPreProduction.mu.Lock()
	defer globalPreProduction.mu.Unlock()
	if existing, ok := globalPreProduction.plans[projectID]; ok && existing.Status == "draft" {
		return existing
	}
	plan := &PreProductionPlan{
		ID:          uuid.New().String(),
		ProjectID:   projectID,
		UserID:      userID,
		CurrentStep: StepScriptDirection,
		Status:      "draft",
		CreatedAt:   time.Now(),
	}
	globalPreProduction.plans[projectID] = plan
	return plan
}

// GetPreProduction 获取前期沟通方案
func GetPreProduction(projectID string) *PreProductionPlan {
	globalPreProduction.mu.Lock()
	defer globalPreProduction.mu.Unlock()
	return globalPreProduction.plans[projectID]
}

// ─────────────────────────────────────────────
// 步骤 1：文案方向确认
// ─────────────────────────────────────────────

// GenerateScriptDirections 生成 2-3 个文案方向提案
// 调用 LLM 根据用户主题生成方向选项
func GenerateScriptDirections(topic string) []ScriptDirection {
	// 实际实现由 LLM 生成，这里返回示例结构
	return []ScriptDirection{
		{
			Title:       fmt.Sprintf("面向初学者的 %s 入门教程", topic),
			Audience:    "零基础初学者",
			Style:       "轻松讲解",
			EstDuration: "3-5 分钟",
			Structure:   "开场引入 → 核心概念讲解 → 实际操作演示 → 总结回顾",
		},
		{
			Title:       fmt.Sprintf("%s 进阶实战指南", topic),
			Audience:    "有一定基础的开发者",
			Style:       "技术深度",
			EstDuration: "5-8 分钟",
			Structure:   "问题引入 → 原理分析 → 代码实战 → 最佳实践总结",
		},
		{
			Title:       fmt.Sprintf("%s 常见误区与避坑", topic),
			Audience:    "中级开发者",
			Style:       "问题驱动",
			EstDuration: "3-4 分钟",
			Structure:   "误区展示 → 正确做法 → 原因分析 → 实用建议",
		},
	}
}

// BuildScriptDirectionQuestion 构建文案方向确认问题
func BuildScriptDirectionQuestion(directions []ScriptDirection) *QuestionEvent {
	choices := make([]ConfirmChoice, 0, len(directions)+1)
	for i, d := range directions {
		desc := fmt.Sprintf("受众：%s | 风格：%s | 时长：%s", d.Audience, d.Style, d.EstDuration)
		choices = append(choices, ConfirmChoice{
			Key:         fmt.Sprintf("direction-%d", i+1),
			Label:       d.Title,
			Description: desc,
			Recommended: i == 0,
		})
	}
	choices = append(choices, ConfirmChoice{
		Key:         "custom",
		Label:       "自己描述方向",
		Description: "输入你自己想要的文案方向和风格",
	})

	return &QuestionEvent{
		Type:              "question",
		QuestionID:        "pre-prod-script-direction",
		Title:             "选择文案方向",
		Subtitle:          "Agent 根据你的主题生成了以下方向，选择一个开始制作",
		Choices:           choices,
		AllowCustom:       true,
		CustomPlaceholder: "描述你想要的文案风格、受众和结构…",
	}
}

// ConfirmScriptDirection 确认文案方向
func ConfirmScriptDirection(plan *PreProductionPlan, direction ScriptDirection) {
	plan.mu.Lock()
	defer plan.mu.Unlock()
	plan.ScriptDirection = &direction
	plan.CurrentStep = StepProductionMethod
}

// ─────────────────────────────────────────────
// 步骤 2：实现方式确认
// ─────────────────────────────────────────────

// GenerateProductionMethods 生成实现方式提案
func GenerateProductionMethods(direction *ScriptDirection) []ProductionMethod {
	return []ProductionMethod{
		{
			AvatarEnabled: true,
			AnimationType: "矢量",
			RealFootage:   false,
			AspectRatio:   "9:16",
			AngleMode:     "single",
			VoiceID:       "default",
		},
		{
			AvatarEnabled: true,
			AnimationType: "线条",
			RealFootage:   true,
			AspectRatio:   "16:9",
			AngleMode:     "single",
			VoiceID:       "default",
		},
		{
			AvatarEnabled: false,
			AnimationType: "白板",
			RealFootage:   false,
			AspectRatio:   "16:9",
			AngleMode:     "single",
			VoiceID:       "default",
		},
	}
}

// BuildProductionMethodQuestion 构建实现方式确认问题
func BuildProductionMethodQuestion(methods []ProductionMethod) *QuestionEvent {
	choices := make([]ConfirmChoice, 0, len(methods))
	for i, m := range methods {
		avatarLabel := "数字人口播"
		if !m.AvatarEnabled {
			avatarLabel = "纯动画"
		}
		footageLabel := ""
		if m.RealFootage {
			footageLabel = " + 真实素材"
		}
		desc := fmt.Sprintf("%s + %s动画%s | 比例 %s", avatarLabel, m.AnimationType, footageLabel, m.AspectRatio)
		choices = append(choices, ConfirmChoice{
			Key:         fmt.Sprintf("method-%d", i+1),
			Label:       fmt.Sprintf("方案 %d：%s", i+1, avatarLabel),
			Description: desc,
			Recommended: i == 0,
		})
	}

	return &QuestionEvent{
		Type:        "question",
		QuestionID:  "pre-prod-production-method",
		Title:       "选择实现方式",
		Subtitle:    "Agent 根据文案方向规划了以下制作方案",
		Choices:     choices,
		AllowCustom: false,
	}
}

// ConfirmProductionMethod 确认实现方式
func ConfirmProductionMethod(plan *PreProductionPlan, method ProductionMethod) {
	plan.mu.Lock()
	defer plan.mu.Unlock()
	plan.ProductionMethod = &method
	plan.CurrentStep = StepMaterialNeeds
}

// ─────────────────────────────────────────────
// 步骤 3：素材裁剪需求确认
// ─────────────────────────────────────────────

// GenerateMaterialNeeds 生成素材需求清单
func GenerateMaterialNeeds(method *ProductionMethod, segments []ScriptSegment) []MaterialNeed {
	if method == nil {
		return nil
	}
	var needs []MaterialNeed
	for i, seg := range segments {
		need := MaterialNeed{
			ID:        seg.ID,
			SegmentID: seg.ID,
			Content:   seg.Content,
		}
		if method.AvatarEnabled && method.AnimationType != "none" {
			need.Type = "avatar"
			need.Duration = "12s"
		} else if method.AnimationType != "none" {
			need.Type = "animation"
			need.Duration = "10s"
		}
		// 交替插入实拍素材，保证镜头节奏不单一（原 int 取模逻辑的等价实现）
		if method.RealFootage && i%2 == 0 {
			need.Type = "real_footage"
			need.Duration = "8s"
		}
		needs = append(needs, need)
	}
	return needs
}

// BuildMaterialNeedsQuestion 构建素材需求确认问题
func BuildMaterialNeedsQuestion(needs []MaterialNeed) *QuestionEvent {
	var lines []string
	for _, n := range needs {
		lines = append(lines, fmt.Sprintf("%s（%s）：%s — %s", segmentLabel(n.SegmentID), n.Duration, n.Type, truncate(n.Content, 40)))
	}

	return &QuestionEvent{
		Type:       "question",
		QuestionID: "pre-prod-material-needs",
		Title:      "确认素材需求清单",
		Subtitle:   "Agent 为每段文案规划了素材类型和时长，请确认或调整",
		Body:       strings.Join(lines, "\n"),
		Choices: []ConfirmChoice{
			{Key: "confirm-all", Label: "全部确认", Description: "接受所有素材需求，进入下一步", Recommended: true},
			{Key: "adjust", Label: "调整部分需求", Description: "对某些段的素材类型或时长进行调整"},
			{Key: "replan", Label: "让 Agent 重新规划", Description: "Agent 重新分析文案，生成新的素材需求方案"},
		},
		AllowCustom: false,
	}
}

// ConfirmMaterialNeeds 确认素材需求
func ConfirmMaterialNeeds(plan *PreProductionPlan, needs []MaterialNeed) {
	plan.mu.Lock()
	defer plan.mu.Unlock()
	plan.MaterialNeeds = needs
	plan.CurrentStep = StepFinalExpectation
}

// ─────────────────────────────────────────────
// 步骤 4：最终效果预期确认
// ─────────────────────────────────────────────

// GenerateFinalExpectation 生成最终效果预期
func GenerateFinalExpectation(direction *ScriptDirection, method *ProductionMethod, needs []MaterialNeed) *FinalExpectation {
	totalDur := "3-5 分钟"
	pacing := "标准"
	ratio := "16:9"

	if direction != nil {
		if direction.EstDuration != "" {
			totalDur = direction.EstDuration
		}
		if direction.Style != "" {
			pacing = direction.Style
		}
	}
	if method != nil && method.AspectRatio != "" {
		ratio = method.AspectRatio
	}

	return &FinalExpectation{
		TotalDuration:  totalDur,
		PacingStyle:    pacing,
		AspectRatio:    ratio,
		VoiceConfirmed: true,
		Description: fmt.Sprintf("总时长约 %s，%s 风格，%s 画面比例，共 %d 段素材",
			totalDur, pacing, ratio, len(needs)),
	}
}

// BuildFinalExpectationQuestion 构建最终效果确认问题
func BuildFinalExpectationQuestion(exp *FinalExpectation) *QuestionEvent {
	return &QuestionEvent{
		Type:       "question",
		QuestionID: "pre-prod-final-expectation",
		Title:      "确认最终效果",
		Subtitle:   "请确认以下效果预期，确认后 Agent 将生成制作草稿并开始执行",
		Body: fmt.Sprintf("总时长：%s\n节奏风格：%s\n画面比例：%s\n素材段数：%s\n\n%s",
			exp.TotalDuration, exp.PacingStyle, exp.AspectRatio, "见素材清单", exp.Description),
		Choices: []ConfirmChoice{
			{Key: "confirm", Label: "确认，开始制作", Description: "Agent 将生成 F01 七项输入草稿并开始执行 Workflow", Recommended: true},
			{Key: "adjust", Label: "需要调整", Description: "回到之前的步骤修改某些设置"},
		},
		AllowCustom: false,
	}
}

// ConfirmFinalExpectation 确认最终效果，完成前期沟通
func ConfirmFinalExpectation(plan *PreProductionPlan, exp *FinalExpectation) {
	plan.mu.Lock()
	defer plan.mu.Unlock()
	plan.FinalExpectation = exp
	plan.CurrentStep = StepComplete
	plan.Status = "confirmed"
	now := time.Now()
	plan.ConfirmedAt = &now
}

// IsComplete 检查前期沟通是否完成
func (p *PreProductionPlan) IsComplete() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Status == "confirmed"
}

// GetCurrentStepQuestion 获取当前步骤的确认问题
func (p *PreProductionPlan) GetCurrentStepQuestion() *QuestionEvent {
	return p.GetCurrentStepQuestionWithSegments(nil)
}

// GetCurrentStepQuestionWithSegments 同 GetCurrentStepQuestion，但可由调用方
// 传入项目当前的真实段落。
//
// H-11：素材需求步骤此前固定传 nil 段落，导致清单永远为空。
// 段落来自 ProjectState（avatar 工作流的 Segments），必须由 handler 取来传入。
func (p *PreProductionPlan) GetCurrentStepQuestionWithSegments(segments []ScriptSegment) *QuestionEvent {
	p.mu.Lock()
	defer p.mu.Unlock()

	switch p.CurrentStep {
	case StepScriptDirection:
		directions := GenerateScriptDirections("")
		return BuildScriptDirectionQuestion(directions)
	case StepProductionMethod:
		if p.ScriptDirection == nil {
			return nil
		}
		methods := GenerateProductionMethods(p.ScriptDirection)
		return BuildProductionMethodQuestion(methods)
	case StepMaterialNeeds:
		if p.ProductionMethod == nil {
			return nil
		}
		// 没有段落时明确说明，而不是给出一个空清单让用户困惑
		if len(segments) == 0 {
			return &QuestionEvent{
				Type:       "question",
				QuestionID: "pre-prod-material-needs-empty",
				Title:      "还没有可规划的文案段落",
				Subtitle:   "素材需求依赖文案段落。请先生成或粘贴文案（F02/F03），再回到这一步。",
				Choices: []ConfirmChoice{
					{Key: "back-to-script", Label: "先去生成文案", Description: "返回文案步骤，生成分段内容后再继续", Recommended: true},
					{Key: "manual-segments", Label: "手动提供文案", Description: "直接粘贴文案，由 Agent 自动分段"},
				},
				AllowCustom:       true,
				CustomPlaceholder: "粘贴你的完整文案，Agent 会自动切分并规划素材…",
			}
		}
		needs := GenerateMaterialNeeds(p.ProductionMethod, segments)
		p.MaterialNeeds = needs
		return BuildMaterialNeedsQuestion(needs)
	case StepFinalExpectation:
		if p.ProductionMethod == nil || p.ScriptDirection == nil {
			return nil
		}
		exp := GenerateFinalExpectation(p.ScriptDirection, p.ProductionMethod, p.MaterialNeeds)
		return BuildFinalExpectationQuestion(exp)
	default:
		return nil
	}
}

// ─────────────────────────────────────────────
// 辅助
// ─────────────────────────────────────────────

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "…"
}
