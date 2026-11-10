package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/models"
	"gorm.io/gorm"
)

var errProjectProducing = errors.New("项目仍在制作或结束收尾中，请先停止制作，待任务停止后再删除")

// Only server-derived UUID paths are accepted. Reject symlink ancestors before
// RemoveAll: a linked workflow-v2 parent must never redirect project deletion.
func checkedProjectDataPath(root, target string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || root == filepath.Dir(root) {
		return "", fmt.Errorf("unsafe project storage path")
	}
	for parent := filepath.Dir(target); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
			return "", fmt.Errorf("unsafe project storage ancestor")
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	return target, nil
}

// Admission and uploads share these locks. Files go first, database records go
// last: on any cleanup error the project remains visible and deletion is retryable.
func (a *App) purgeAvatarProjectData(projectID uuid.UUID) error {
	if a == nil || a.avatarMVP == nil || a.db == nil || projectID == uuid.Nil {
		return fmt.Errorf("project storage unavailable")
	}
	unlock := lockPackageProject(projectID.String())
	defer unlock()
	a.avatarMVP.mu.Lock()
	defer a.avatarMVP.mu.Unlock()
	return a.db.Transaction(func(tx *gorm.DB) error {
		if err := avatarTransactionLock(tx); err != nil {
			return err
		}
		var runs []models.WorkflowRun
		if err := tx.Where("project_id=?", projectID).Find(&runs).Error; err != nil {
			return err
		}
		paths := []string{filepath.Join(a.avatarMVP.options.DataRoot, "workflow-v2", "projects", projectID.String())}
		ids := make([]uuid.UUID, 0, len(runs))
		for _, run := range runs {
			if _, active := workflowLaunchLocks.Load(run.ID.String()); active {
				return errProjectProducing
			}
			var markers []models.WorkflowStepExecution
			if err := tx.Where("workflow_run_id=? AND step_id IN ?", run.ID, []string{avatarMarker, workflowMarker}).Find(&markers).Error; err != nil {
				return err
			}
			if len(markers) == 0 && (run.Status == "running" || run.Status == "stopping") {
				return errProjectProducing
			}
			for _, marker := range markers {
				var state struct {
					Status string `json:"status"`
				}
				if err := json.Unmarshal(marker.Result, &state); err != nil {
					return err
				}
				if marker.Status == "running" || marker.Status == "stopping" || state.Status == "running" || state.Status == "stopping" {
					return errProjectProducing
				}
			}
			ids = append(ids, run.ID)
			paths = append(paths, a.avatarMVP.taskDir(run.ID), filepath.Join(a.avatarMVP.options.DataRoot, "workflow-v2", "runs", run.ID.String()))
		}
		for i, path := range paths {
			checked, err := checkedProjectDataPath(a.avatarMVP.options.DataRoot, path)
			if err != nil {
				return err
			}
			paths[i] = checked
		}
		for _, path := range paths {
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("remove project files: %w", err)
			}
		}
		now := time.Now().UTC()
		reservations := tx.Model(&models.CreditReservation{}).Where("project_id=?", projectID)
		if len(ids) > 0 {
			reservations = tx.Model(&models.CreditReservation{}).Where("(project_id=? OR workflow_run_id IN ?)", projectID, ids)
		}
		if err := reservations.Where("status=?", creditReservationPending).Updates(map[string]any{"status": creditReservationReleased, "released_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		if len(ids) > 0 {
			if err := tx.Unscoped().Where("workflow_run_id IN ?", ids).Delete(&models.WorkflowStepExecution{}).Error; err != nil {
				return err
			}
			if err := tx.Unscoped().Where("id IN ?", ids).Delete(&models.WorkflowRun{}).Error; err != nil {
				return err
			}
		}
		// Keep financial ledgers; remove project content, uploads and conversation data.
		for _, model := range []any{&models.ProjectWorkflowEvent{}, &models.ProjectWorkflow{}, &models.ProjectVideoSource{}, &models.ProjectSubtitle{}, &models.ProjectGenerationConfig{}, &models.ProjectScript{}, &models.ProjectVoiceover{}, &models.AgentMessage{}, &models.AgentMemory{}, &models.UploadRecord{}} {
			if err := tx.Unscoped().Where("project_id=?", projectID).Delete(model).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&models.Project{}, "id=?", projectID).Error
	})
}
