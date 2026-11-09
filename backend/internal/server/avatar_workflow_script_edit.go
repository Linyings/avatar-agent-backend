package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/avatar"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
)

// Saving a review changes only the pending text, never confirms or starts work.
// It shares the confirmation lock so an older browser cannot approve stale text.
func (w *avatarWorkflowService) saveScriptReview(ctx context.Context, owner uuid.UUID, id, version string, segments []avatar.Segment) error {
	if _, err := w.load(ctx, owner, id); err != nil {
		return err
	}
	return w.updateTransaction(ctx, id, func(tx *gorm.DB, s *avatar.WorkflowState) error {
		if version == "" || s.Status != "awaiting_confirmation" || s.Confirmation != "script" || s.ConfirmationVersion != version || s.StopRequested {
			return fmt.Errorf("文案版本已更新或当前不在文案确认阶段，请刷新后重新编辑")
		}
		var p models.Project
		if err := tx.Where("id=? AND owner_id=?", s.ProjectID, owner).First(&p).Error; err != nil {
			return err
		}
		in, err := workflowInput(p)
		if err != nil {
			return err
		}
		if in.Revision != s.Input.Revision {
			return fmt.Errorf("制作需求已更新，请先处理最新草稿")
		}
		latest, err := latestAgentRunFrom(tx, owner, p.ID)
		if err != nil {
			return err
		}
		if latest.ID != s.ID {
			return fmt.Errorf("已有更新的制作版本，请刷新后编辑最新文案")
		}
		var body strings.Builder
		clean := make([]avatar.Segment, len(segments))
		for i, seg := range segments {
			if len([]rune(seg.Chapter)) > 200 {
				return fmt.Errorf("段落标题过长")
			}
			clean[i] = avatar.Segment{ID: seg.ID, Chapter: strings.TrimSpace(seg.Chapter), Text: strings.TrimSpace(seg.Text)}
			for _, old := range s.Segments {
				if old.ID == seg.ID {
					clean[i].DirectorSceneID = old.DirectorSceneID
				}
			}
			body.WriteString(clean[i].Text)
			// Only unchanged server-owned audio can be reused; client paths are ignored.
			for _, old := range s.Segments {
				if old.ID == seg.ID && old.Text == clean[i].Text {
					clean[i] = old
					clean[i].Chapter = strings.TrimSpace(seg.Chapter)
					break
				}
			}
		}
		script := body.String()
		if len([]rune(script)) > 10000 {
			return fmt.Errorf("口播文案不能超过10000字")
		}
		if err = avatar.ValidateSegments(script, clean); err != nil {
			return err
		}
		s.Script, s.Segments = script, clean
		avatar.CalibrateDirector(s)
		s.Edits.Script = &script
		s.Edits.Segments = clean
		s.ScriptConfirmed = false
		s.AudioConfirmed = false
		s.ConfirmationVersion = uuid.NewString()
		return nil
	})
}

func (a *App) workflowSaveScriptReview(c *gin.Context) {
	var request struct {
		Version  string           `json:"version"`
		Segments []avatar.Segment `json:"segments"`
	}
	if c.ShouldBindJSON(&request) != nil {
		fail(c, 400, "文案编辑参数无效")
		return
	}
	if err := a.avatarWorkflow.saveScriptReview(c.Request.Context(), getUser(c).ID, c.Param("id"), request.Version, request.Segments); err != nil {
		workflowError(c, err)
		return
	}
	a.workflowRunGet(c)
}
