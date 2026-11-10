package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/agentpackages"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

func (h avatarPackageHost) storeProposal(ctx context.Context, p models.Project, proposal packageProposal) error {
	return h.app.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current models.Project
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND owner_id=?", p.ID, p.OwnerID).First(&current).Error; err != nil {
			return err
		}
		in, err := workflowInput(current)
		if err != nil {
			return err
		}
		if proposal.Applied {
			if stored := proposalOf(current); stored == nil || stored.ID != proposal.ID {
				return fmt.Errorf("提案已更新，请刷新")
			}
		} else if in.Revision != proposal.Revision {
			return fmt.Errorf("项目版本已更新，提案失效")
		}
		meta := map[string]any{}
		_ = json.Unmarshal(current.ProjectMetadata, &meta)
		meta["agent_package_proposal"] = proposal
		return tx.Model(&current).Update("project_metadata", jsonMap(meta)).Error
	})
}
func (h avatarPackageHost) draft(ctx context.Context, p models.Project, in avatar.WorkflowInput, pkg agentpackages.Manifest, args map[string]any) (any, error) {
	s, err := h.app.latestAgentRun(ctx, p.OwnerID, p.ID)
	if err != nil {
		return nil, fmt.Errorf("尚无产物可修改，请先制作")
	}
	if s.Status == "running" || s.Status == "stopping" {
		return nil, fmt.Errorf("请等待当前步骤完成或先暂停制作")
	}
	if s.Input.Revision != in.Revision {
		return nil, fmt.Errorf("当前草稿与运行版本不同，请先执行最新草稿")
	}
	edits := avatar.WorkflowEdits{BaseRunID: s.ID}
	var details []string
	switch pkg.Entrypoint {
	case "edit_script":
		edits.Segments, err = mergePackageRows(s.Segments, args["segments"])
		if err != nil {
			return nil, err
		}
		var body strings.Builder
		for _, seg := range edits.Segments {
			body.WriteString(seg.Text)
			details = append(details, seg.ID+"："+seg.Text)
		}
		if err = avatar.ValidateSegments(body.String(), edits.Segments); err != nil {
			return nil, err
		}
	case "edit_voice":
		list, _ := args["segmentIds"].([]any)
		if len(list) == 0 {
			return nil, fmt.Errorf("请选择重配音段落")
		}
		ids := map[string]bool{}
		for _, seg := range s.Segments {
			ids[seg.ID] = true
		}
		for _, id := range list {
			x, _ := id.(string)
			if !ids[x] {
				return nil, fmt.Errorf("配音段落不存在")
			}
			edits.RegenerateSegments = append(edits.RegenerateSegments, x)
		}
		voice, _ := args["voice"].(string)
		voiceModel, _ := args["voice_model"].(string)
		if voiceModel != "" && voice == "" {
			if m, ok := avatar.FindSpeechModel(voiceModel); ok {
				voice = m.DefaultVoice
			}
		}
		if speed, ok := args["speed"].(float64); ok {
			if speed == 0 {
				return nil, fmt.Errorf("请提供 0.8–1.3 倍的配音语速")
			}
			if err := avatar.ValidateVoiceSpeed(speed); err != nil {
				return nil, err
			}
			edits.SegmentVoiceSpeeds = map[string]float64{}
			for _, id := range edits.RegenerateSegments {
				edits.SegmentVoiceSpeeds[id] = speed
			}
			details = append(details, fmt.Sprintf("所选段落语速：%.2f 倍；同步更新口型视频、字幕及镜头时间轴", speed))
		}
		if voice != "" {
			next := s.Input
			next.Voice = voice
			if voiceModel != "" {
				next.VoiceModel = voiceModel
			}
			if err := h.app.avatarWorkflow.validateInput(&next, false); err != nil {
				return nil, err
			}
			edits.Voice = next.Voice
			// Always record the target model so a voice switch is never applied to
			// the wrong provider, including a return to the deployment default.
			edits.VoiceModel = h.app.avatarWorkflow.voiceModel(next)
			edits.RegenerateSegments = nil
			for _, seg := range s.Segments {
				edits.RegenerateSegments = append(edits.RegenerateSegments, seg.ID)
			}
		}
		details = append(details, "重新配音："+strings.Join(edits.RegenerateSegments, "、"))
		if voice != "" {
			details = append(details, "配音模型："+edits.VoiceModel+"；音色："+edits.Voice)
		}
	case "edit_material":
		edits.Needs, err = mergePackageRows(s.Needs, args["needs"])
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
	case "edit_shot":
		edits.Shots, err = mergePackageRows(s.Shots, args["shots"])
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
	changes, _ := args["changes"].(string)
	if strings.TrimSpace(changes) == "" {
		return nil, fmt.Errorf("请说明修改内容")
	}
	proposal := packageProposal{ID: uuid.NewString(), Revision: in.Revision, Package: pkg.ID, Lines: append([]string{changes}, details...), Edits: edits, BaseFingerprint: packageFingerprint(s), CreatedAt: time.Now().UTC()}
	if err = h.storeProposal(ctx, p, proposal); err != nil {
		return nil, err
	}
	return gin.H{"proposal": proposal, "status": "pending", "note": "请用户在界面确认；尚未修改任何产物"}, nil
}

func (h avatarPackageHost) Confirm(ctx context.Context, owner, project, id string, revision int) (any, error) {
	unlock := lockPackageProject(project)
	defer unlock()
	p, in, err := h.project(ctx, owner, project)
	if err != nil {
		return nil, err
	}
	proposal := proposalOf(p)
	if proposal == nil || id == "" || proposal.ID != id || proposal.Revision != revision {
		return nil, fmt.Errorf("提案不存在或已更新，请刷新")
	}
	if proposal.Applied {
		return gin.H{"status": "applied", "run_id": proposal.RunID}, nil
	}
	s, err := h.app.latestAgentRun(ctx, p.OwnerID, p.ID)
	if err != nil {
		return nil, err
	}
	request := "proposal-" + proposal.ID
	target := "F12"
	switch proposal.Package {
	case "avatar.edit-script":
		target = "F03"
	case "avatar.edit-voice":
		target = "F04"
	}
	if proposal.TargetNode != "" {
		target = proposal.TargetNode
	}
	// A response/write failure after admission can be retried using the same run.
	hash := avatar.Digest([]any{owner, project, request})
	var admitted models.WorkflowRun
	existing := h.app.db.WithContext(ctx).Where("user_id=? AND project_id=? AND input_hash=?", p.OwnerID, p.ID, hash).First(&admitted).Error == nil
	if !existing {
		if in.Revision != revision {
			return nil, fmt.Errorf("项目版本已变化，请重新生成提案")
		}
		if s.Status == "running" || s.Status == "stopping" || (s.Status == "submission_unknown" && !workflowTestingMode()) {
			return nil, fmt.Errorf("当前运行尚未结束或订单待核对，不能应用修改")
		}
		if packageFingerprint(s) != proposal.BaseFingerprint {
			return nil, fmt.Errorf("产物已变化，请重新生成提案")
		}
		effective := in
		proposal.Edits.ApplyPresentation(&effective)
		proposal.Edits.ApplyVoice(&effective)
		if err = h.app.avatarWorkflow.validateInput(&effective, false); err != nil {
			return nil, err
		}
		if err = h.app.avatarWorkflow.validatePackageInput(&effective, target); err != nil {
			return nil, err
		}
	}
	next, err := h.app.avatarWorkflow.createToChecked(ctx, p.OwnerID, p.ID, request, in.Revision, target, proposal, proposal.Edits)
	if err != nil {
		return nil, err
	}
	proposal.Applied = true
	proposal.RunID = next.ID
	if err = h.storeProposal(ctx, p, *proposal); err != nil {
		return nil, err
	}
	if next.Status == "running" {
		h.app.avatarWorkflow.launch(next, p.OwnerID)
	}
	return gin.H{"status": "applied", "run_id": next.ID, "note": "修改已应用到新运行，实际制作进度请查看画布"}, nil
}
