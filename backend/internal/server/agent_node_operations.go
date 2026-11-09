package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/agentpackages"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/models"
)

// Drafts carry the exact base run and content fingerprint. Provider work starts
// only through Confirm, using the same transactional admission as other edits.
func (h avatarPackageHost) draftNode(ctx context.Context, p models.Project, in avatar.WorkflowInput, pkg agentpackages.Manifest, args map[string]any) (any, error) {
	s, err := h.app.latestAgentRun(ctx, p.OwnerID, p.ID)
	if err != nil {
		return nil, fmt.Errorf("尚无运行可编辑，请先制作")
	}
	if s.Status == "running" || s.Status == "stopping" || (s.Status == "submission_unknown" && !workflowTestingMode()) {
		return nil, fmt.Errorf("请等待当前步骤结束或暂停；待核对订单不能绕过")
	}
	if s.Input.Revision != in.Revision {
		return nil, fmt.Errorf("草稿和产物版本不同，请先执行最新草稿")
	}
	changes, _ := args["changes"].(string)
	if strings.TrimSpace(changes) == "" {
		return nil, fmt.Errorf("请说明修改内容")
	}
	edits := avatar.WorkflowEdits{BaseRunID: s.ID}
	node, _ := args["node"].(string)
	target := "F12"
	details := []string{changes}
	if pkg.Entrypoint == "rerun" {
		node, _ = args["from"].(string)
		if value, ok := args["target"].(string); ok {
			target = value
		}
		if !isKnownWorkflowNode(node) || !isKnownWorkflowNode(target) || node > target {
			return nil, fmt.Errorf("请选择有效的重做起点和终点，起点不得晚于终点")
		}
		if node >= "F04" && !s.ScriptConfirmed {
			return nil, fmt.Errorf("请先确认口播文案，再从配音或后续节点重做")
		}
		if node >= "F05" && !s.AudioConfirmed {
			return nil, fmt.Errorf("请先确认配音，再从素材或后续节点重做")
		}
		edits.RerunFrom = node
		details = append(details, fmt.Sprintf("从 %s 重新执行到 %s；保留之前的步骤及历史版本。起点及后续不会直接使用旧结果，涉及模型和生成服务时会产生新的调用费用。稿件、配音审核仍需确认。", node, target))
	} else {
		if node == "F01.6" {
			value, ok := args["director"]
			if !ok {
				return nil, fmt.Errorf("请提供修改后的完整导演稿")
			}
			raw, _ := json.Marshal(value)
			var plan avatar.DirectorPlan
			if err := json.Unmarshal(raw, &plan); err != nil {
				return nil, err
			}
			if err := avatar.ValidateDirector(plan); err != nil {
				return nil, err
			}
			edits.Director = &plan
			details = append(details, "更新导演稿，保留已确认台词与可复用配音，重新审阅受影响的镜头。")
			details = append(details, "全片设计："+plan.Summary)
			for _, scene := range plan.Scenes {
				details = append(details, fmt.Sprintf("%s｜%s｜构图 %s｜画面：%s｜动效：%s｜转场：%s｜声音：%s｜人物窗：%s %s", scene.ID, scene.Purpose, scene.Composition, scene.Visual, scene.Motion, scene.Transition, scene.Sound, scene.AvatarShape, scene.AvatarPosition))
			}
		}
		allowed := map[string][]string{"F01.4": {"descriptions"}, "F02": {"script"}, "F03": {"segments"}, "F05": {"needs"}, "F06": {"needs"}, "F07": {"needs"}, "F08": {"needs"}, "F09": {"subtitles"}, "F10": {"shots"}, "F11": {"shots", "subtitles", "presentation"}, "F11.1": {"shots", "subtitles", "presentation"}, "F11.2": {"shots", "subtitles", "presentation"}, "F12": {"shots", "subtitles", "presentation"}}
		allowed["F01.6"] = []string{"director"}
		for _, materialNode := range []string{"F05", "F06", "F07", "F08"} {
			allowed[materialNode] = append(allowed[materialNode], "skip_need_ids")
		}
		fields, ok := allowed[node]
		if !ok {
			return nil, fmt.Errorf("该节点请使用 avatar_configure 或 edit_voice 编辑")
		}
		provided := 0
		for key := range args {
			if key == "node" || key == "changes" {
				continue
			}
			valid := false
			for _, field := range fields {
				if field == key {
					valid = true
				}
			}
			if !valid {
				return nil, fmt.Errorf("%s 不支持编辑 %s", node, key)
			}
			provided++
		}
		if provided == 0 {
			return nil, fmt.Errorf("必须提供节点的实际修改内容；只重做请使用 avatar_rerun")
		}
		if value, ok := args["script"].(string); ok {
			if strings.TrimSpace(value) == "" || len([]rune(value)) > 10000 {
				return nil, fmt.Errorf("口播稿为空或过长")
			}
			edits.Script = &value
			target = "F03"
			details = append(details, "完整口播稿："+value)
		}
		if value, ok := args["segments"]; ok {
			raw, _ := json.Marshal(value)
			if err = json.Unmarshal(raw, &edits.Segments); err != nil {
				return nil, err
			}
			if err = avatar.ValidateSegments(s.Script, edits.Segments); err != nil {
				return nil, err
			}
			target = "F03"
			for _, seg := range edits.Segments {
				details = append(details, seg.ID+"："+seg.Text)
			}
		}
		if value, ok := args["descriptions"]; ok {
			edits.Descriptions, err = mergePackageRows(s.Descriptions, value)
			if err != nil {
				return nil, err
			}
			for _, d := range edits.Descriptions {
				if strings.TrimSpace(d.Text) == "" {
					return nil, fmt.Errorf("素材解读不能为空")
				}
				details = append(details, d.ID+"："+d.Text)
			}
			target = "F03"
		}
		if value, ok := args["skip_need_ids"]; ok {
			if _, editing := args["needs"]; editing {
				return nil, fmt.Errorf("跳过素材与修改提示词请分别提交方案")
			}
			edits.Needs, err = h.app.avatarWorkflow.skipMaterialNeeds(s, node, value)
			if err != nil {
				return nil, err
			}
			details = append(details, "跳过所选素材，使用该段已就绪的完整替代画面；保留配音，重新组装镜头并再次审阅。")
		}
		if value, ok := args["needs"]; ok {
			kind := map[string]string{"F06": "avatar", "F07": "animation", "F08": "real"}[node]
			if kind != "" {
				for _, v := range value.([]any) {
					patch := v.(map[string]any)
					found := false
					for _, n := range s.Needs {
						if n.ID == patch["id"] && n.Kind == kind {
							found = true
						}
					}
					if !found {
						return nil, fmt.Errorf("%s 只能修改其对应类型的已有素材", node)
					}
					if k, ok := patch["kind"]; ok && k != kind {
						return nil, fmt.Errorf("不能在此节点改变素材类型")
					}
				}
			}
			edits.Needs, err = mergePackageRows(s.Needs, value)
			if err != nil {
				return nil, err
			}
			if err = avatar.ValidateNeeds(s.Input, s.Segments, edits.Needs); err != nil {
				return nil, err
			}
			if err = avatar.BindSourceDescriptions(s.Input, s.Descriptions, edits.Needs); err != nil {
				return nil, err
			}
			for _, n := range edits.Needs {
				details = append(details, n.ID+"："+n.Content)
			}
		}
		if value, ok := args["shots"]; ok {
			edits.Shots, err = mergePackageRows(s.Shots, value)
			if err != nil {
				return nil, err
			}
			if err = avatar.ValidateWorkflowShots(edits.Shots, s.Segments, s.Materials, s.Duration); err != nil {
				return nil, err
			}
			for _, shot := range edits.Shots {
				details = append(details, shot.ID+"："+shot.Layout+" / "+shot.Keyword)
			}
		}
		if value, ok := args["subtitles"]; ok {
			list := value.([]any)
			if len(list) == 0 {
				return nil, fmt.Errorf("字幕修改不能为空")
			}
			edits.Subtitles = map[string]string{}
			for _, v := range list {
				row := v.(map[string]any)
				id := row["id"].(string)
				text := row["text"].(string)
				if _, duplicate := edits.Subtitles[id]; duplicate {
					return nil, fmt.Errorf("字幕段落重复")
				}
				edits.Subtitles[id] = text
				details = append(details, id+" 字幕："+text)
			}
		}
		if value, ok := args["presentation"]; ok {
			if len(value.(map[string]any)) == 0 {
				return nil, fmt.Errorf("画面设置修改不能为空")
			}
			raw, _ := json.Marshal(value)
			if err = json.Unmarshal(raw, &edits.Presentation); err != nil {
				return nil, err
			}
			details = append(details, "画面设置："+string(raw))
		}
		preview := in
		edits.ApplyPresentation(&preview)
		if err = h.app.avatarWorkflow.validateInput(&preview, false); err != nil {
			return nil, err
		}
		if err = avatar.ValidateSubtitleEdits(preview, s.Segments); err != nil {
			return nil, err
		}
	}
	proposal := packageProposal{ID: uuid.NewString(), Revision: in.Revision, Package: pkg.ID, Node: node, TargetNode: target, Lines: details, Edits: edits, BaseFingerprint: packageFingerprint(s), CreatedAt: time.Now().UTC()}
	if err = h.storeProposal(ctx, p, proposal); err != nil {
		return nil, err
	}
	return gin.H{"proposal": proposal, "status": "pending", "submitted": false, "note": "方案已保存，尚未执行。请用户在当前工作台确认；确认后按范围重做，保留历史版本。"}, nil
}
