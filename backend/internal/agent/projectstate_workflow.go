// projectstate_workflow.go — 把 avatar.WorkflowState 适配为 agent.ProjectState
//
// agent 包定义 ProjectState 接口时还不知道持久层长什么样；这里的适配器负责把
// avatar 工作流的真实数据接到接口上，让 evidence / review / determinePhase
// 能够读到项目内容而不是空数组。
package agent

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/narratoai/backend-go/internal/avatar"
)

// WorkflowProjectState ProjectState 的 avatar 工作流实现
type WorkflowProjectState struct {
	state avatar.WorkflowState
}

// NewWorkflowProjectState 由工作流快照构造只读项目状态
func NewWorkflowProjectState(state avatar.WorkflowState) *WorkflowProjectState {
	return &WorkflowProjectState{state: state}
}

// Revision 返回项目版本。
// 用内容哈希而非计数器：提案的乐观锁需要"内容变了才失效"，
// 单纯递增会让未改动项目上的提案被无谓作废。
func (w *WorkflowProjectState) Revision() int {
	h := sha256.Sum256([]byte(w.Fingerprint() + w.state.Stage + w.state.Status))
	return int(binary.BigEndian.Uint32(h[:4]) & 0x7FFFFFFF)
}

// GetPresets 返回预设：风格/画幅/方向/声音等创作约束
func (w *WorkflowProjectState) GetPresets() string {
	in := w.state.Input
	parts := make([]string, 0, 8)
	add := func(label, value string) {
		if strings.TrimSpace(value) != "" {
			parts = append(parts, fmt.Sprintf("%s：%s", label, value))
		}
	}
	add("主题", in.Topic)
	add("标题", in.Title)
	add("风格", in.Style)
	add("文案方向", in.Direction)
	add("画幅", in.Ratio)
	add("声音", in.Voice)
	add("数字人出镜", boolLabel(in.UseAvatar))
	add("动效画面", boolLabel(avatar.AnimationEnabled(in)))
	if len(in.ReferenceLinks) > 0 {
		analyzed, unavailable := 0, 0
		for _, ref := range w.state.References {
			if ref.Kind == "link" {
				if ref.Status == "analyzed" {
					analyzed++
				} else {
					unavailable++
				}
			}
		}
		add("参考网页", fmt.Sprintf("%d 个，已分析 %d 个，无法读取 %d 个；使用 avatar_inspect 查看 reference_links 和 references，未分析的链接不能当作已读取正文", len(in.ReferenceLinks), analyzed, unavailable))
	}
	if len(in.Angles) > 0 {
		add("出镜角度", strings.Join(in.Angles, "、"))
	}
	if len(parts) == 0 {
		return "（暂无预设）"
	}
	return strings.Join(parts, "\n")
}

// GetIndex 返回项目概要：只给数量，不预载原文，避免撑爆上下文
func (w *WorkflowProjectState) GetIndex() string {
	return fmt.Sprintf(
		"段落 %d 个｜素材需求 %d 条｜分镜 %d 个｜状态 %s",
		len(w.state.Segments), len(w.state.Needs), len(w.state.Shots), fallback(w.state.Status, "未知"),
	)
}

// GetWorkflowState 返回当前工作流状态。
// 显式标注「当前节点」，让 determinePhase 不需要在整段历史里猜最远节点。
func (w *WorkflowProjectState) GetWorkflowState() string {
	stage := strings.TrimSpace(w.state.Stage)
	if stage == "" {
		stage = "F01"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "当前节点: %s\n", stage)
	fmt.Fprintf(&b, "状态: %s\n", fallback(w.state.Status, "未知"))
	fmt.Fprintf(&b, "文案已确认: %s\n", boolLabel(w.state.ScriptConfirmed))
	fmt.Fprintf(&b, "配音已确认: %s\n", boolLabel(w.state.AudioConfirmed))
	fmt.Fprintf(&b, "当前规划节点: %s\n", strings.Join(avatar.PlanWorkflow(w.state.Input, &w.state).Nodes, "、"))
	if w.state.Status == "awaiting_confirmation" {
		if w.state.Confirmation == "script" {
			fmt.Fprintln(&b, "等待用户确认文案，确认后才继续配音")
		} else if w.state.Confirmation == "shots" {
			fmt.Fprintln(&b, "等待用户审阅全部镜头的有声融合预览，确认后才正式渲染")
		} else if w.state.Confirmation == "final" {
			fmt.Fprintln(&b, "等待用户完整审阅成片，确认后交付最终视频和封面")
		} else {
			fmt.Fprintln(&b, "等待用户确认配音，确认后才继续制作画面")
		}
	}

	// 已完成节点按执行顺序列出，供模型判断前置条件
	if len(w.state.Steps) > 0 {
		done := make([]string, 0, len(w.state.Steps))
		for _, step := range w.state.Steps {
			if step.Status == "done" || step.Status == "completed" || step.Status == "passed" || step.Status == "succeeded" {
				done = append(done, step.ID)
			}
		}
		if len(done) > 0 {
			fmt.Fprintf(&b, "已完成节点: %s\n", strings.Join(done, "、"))
		}
	}
	if w.state.Error != "" {
		fmt.Fprintf(&b, "错误: %s\n", w.state.Error)
	}
	return strings.TrimRight(b.String(), "\n")
}

// GetNodeStatuses 返回 F01-F12 各节点的执行状态。
// 数据源是工作流快照里的 Steps（真实执行记录）与 Stage（当前节点），
// 未出现在 Steps 里的节点保持 pending，避免把未执行的节点误标为完成。
func (w *WorkflowProjectState) GetNodeStatuses() map[string]string {
	out := make(map[string]string)
	for _, node := range avatar.PlanWorkflow(w.state.Input, &w.state).Nodes {
		out[node] = "pending"
	}
	if w.state.Input.Revision > 0 {
		out["F01"] = "done"
	}

	for _, step := range w.state.Steps {
		id := strings.SplitN(strings.ToUpper(strings.TrimSpace(step.ID)), "/", 2)[0]
		if _, ok := out[id]; !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(step.Status)) {
		case "done", "completed", "passed", "succeeded", "success":
			if out[id] != "running" && out[id] != "failed" {
				out[id] = "done"
			}
		case "running", "processing", "in_progress":
			if out[id] != "failed" {
				out[id] = "running"
			}
		case "failed", "error":
			out[id] = "failed"
		case "skipped":
			out[id] = "skipped"
		}
	}

	// 只有工作流确实处于活跃状态时，才把 Stage 指向的节点标为 running。
	// 否则新建的空项目（status=draft/pending、没有任何 Steps）会被显示成
	// 「正在执行 F01」，让用户误以为后台真的有任务在跑。
	if isWorkflowActive(w.state.Status) {
		if stage := strings.SplitN(strings.ToUpper(strings.TrimSpace(w.state.Stage)), "/", 2)[0]; out[stage] == "pending" {
			out[stage] = "running"
		}
	}
	stage := strings.SplitN(w.state.Stage, "/", 2)[0]
	if w.state.Status == "awaiting_confirmation" {
		out[avatar.ConfirmationNode(w.state.Confirmation)] = "awaiting_confirmation"
	} else if _, ok := out[stage]; ok && (w.state.Status == "failed" || w.state.Status == "interrupted" || w.state.Status == "stopped" || w.state.Status == "submission_unknown") {
		out[stage] = w.state.Status
	}
	return out
}

// isWorkflowActive 判断工作流是否真的在运行中。
// 只有明确的进行中状态才算活跃；draft/pending/completed/failed 等都不算。
func isWorkflowActive(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "processing", "in_progress", "active", "executing":
		return true
	default:
		return false
	}
}

// GetScriptSegments 检索脚本段落
func (w *WorkflowProjectState) GetScriptSegments(query string) ([]ScriptSegment, error) {
	out := make([]ScriptSegment, 0, len(w.state.Segments))
	for _, seg := range w.state.Segments {
		if !matchesQuery(query, seg.Text, seg.Chapter, seg.ID) {
			continue
		}
		out = append(out, ScriptSegment{
			ID:      seg.ID,
			Content: seg.Text,
			Tag:     seg.Chapter,
		})
	}
	return out, nil
}

// GetMaterialNeeds 检索素材需求
func (w *WorkflowProjectState) GetMaterialNeeds(query string) ([]MaterialNeed, error) {
	out := make([]MaterialNeed, 0, len(w.state.Needs))
	for _, need := range w.state.Needs {
		if !matchesQuery(query, need.Content, need.Kind, need.SegmentID, need.ID) {
			continue
		}
		out = append(out, MaterialNeed{
			ID:        need.ID,
			Type:      need.Kind,
			Duration:  formatSeconds(need.Duration),
			Content:   need.Content,
			SegmentID: need.SegmentID,
		})
	}
	return out, nil
}

// GetShots 检索分镜
func (w *WorkflowProjectState) GetShots(query string) ([]Shot, error) {
	out := make([]Shot, 0, len(w.state.Shots))
	for _, shot := range w.state.Shots {
		if !matchesQuery(query, shot.Keyword, shot.Primary, shot.Overlay, shot.SegmentID, shot.ID) {
			continue
		}
		out = append(out, Shot{
			ID:        shot.ID,
			Layout:    shot.Layout,
			Duration:  formatSeconds(shot.End - shot.Start),
			SegmentID: shot.SegmentID,
			Source:    shot.Primary,
		})
	}
	return out, nil
}

// Fingerprint 段落内容指纹，供提案乐观锁与审核缓存使用
func (w *WorkflowProjectState) Fingerprint() string {
	h := sha256.New()
	for _, seg := range w.state.Segments {
		h.Write([]byte(seg.ID))
		h.Write([]byte{0})
		h.Write([]byte(seg.Text))
		h.Write([]byte{0})
	}
	h.Write([]byte(w.state.Script))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// ─────────────────────────────────────────────
// 辅助
// ─────────────────────────────────────────────

// matchesQuery 空查询视为全量命中；否则对候选字段做不区分大小写的子串匹配
func matchesQuery(query string, fields ...string) bool {
	q := strings.TrimSpace(strings.ToLower(query))
	if q == "" {
		return true
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

func formatSeconds(v float64) string {
	if v <= 0 {
		return ""
	}
	if v == float64(int(v)) {
		return fmt.Sprintf("%d 秒", int(v))
	}
	return fmt.Sprintf("%.1f 秒", v)
}

func boolLabel(v bool) string {
	if v {
		return "是"
	}
	return "否"
}

func fallback(value, def string) string {
	if strings.TrimSpace(value) == "" {
		return def
	}
	return value
}
