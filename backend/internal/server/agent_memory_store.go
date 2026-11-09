// agent_memory_store.go — Agent 记忆的数据库持久化实现
package server

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/narratoai/backend-go/internal/agent"
	"github.com/narratoai/backend-go/internal/models"
)

// agentMemoryStore 把 agent.memoryStore 落到 agent_memories 表
type agentMemoryStore struct {
	db *gorm.DB
}

func newAgentMemoryStore(db *gorm.DB) *agentMemoryStore {
	return &agentMemoryStore{db: db}
}

// Load 读取项目的记忆，按时间正序返回
func (s *agentMemoryStore) Load(ctx context.Context, projectID string) ([]agent.Memory, error) {
	id, err := uuid.Parse(strings.TrimSpace(projectID))
	if err != nil {
		return nil, err
	}

	var rows []models.AgentMemory
	if err := s.db.WithContext(ctx).
		Where("project_id = ?", id).
		Order("sequence ASC, created_at ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}

	out := make([]agent.Memory, 0, len(rows))
	for _, row := range rows {
		out = append(out, agent.Memory{
			ID:        row.ID.String(),
			ProjectID: row.ProjectID.String(),
			Kind:      agent.MemoryKind(row.Kind),
			Content:   row.Content,
			CreatedAt: row.CreatedAt,
		})
	}
	return out, nil
}

// Save 保存一条记忆
func (s *agentMemoryStore) Save(ctx context.Context, mem agent.Memory) error {
	pid, err := uuid.Parse(strings.TrimSpace(mem.ProjectID))
	if err != nil {
		return err
	}
	uid := uuid.Nil // 记忆不强制归属用户，允许系统级记录

	var maxSeq int64
	_ = s.db.WithContext(ctx).Model(&models.AgentMemory{}).
		Where("project_id = ?", pid).
		Select("COALESCE(MAX(sequence), 0)").
		Scan(&maxSeq).Error

	createdAt := mem.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	row := models.AgentMemory{
		ProjectID: pid,
		UserID:    uid,
		Kind:      string(mem.Kind),
		Content:   mem.Content,
		Sequence:  maxSeq + 1,
	}
	row.ID = uuid.New()
	row.CreatedAt = createdAt
	row.UpdatedAt = createdAt
	return s.db.WithContext(ctx).Create(&row).Error
}

// Delete 删除指定 kind 的记忆；kind 为空时清空该项目全部记忆
func (s *agentMemoryStore) Delete(ctx context.Context, projectID string, kind agent.MemoryKind) (int, error) {
	pid, err := uuid.Parse(strings.TrimSpace(projectID))
	if err != nil {
		return 0, err
	}

	query := s.db.WithContext(ctx).Where("project_id = ?", pid)
	if kind != "" {
		query = query.Where("kind = ?", string(kind))
	}

	var count int64
	if err := query.Model(&models.AgentMemory{}).Count(&count).Error; err != nil {
		return 0, err
	}

	if err := query.Delete(&models.AgentMemory{}).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}
