// agent_messages.go — Agent 对话历史的持久化实现
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

// agentMessageStore 把 agent 的 MessageStore 接口落到 agent_messages 表
type agentMessageStore struct {
	db *gorm.DB
}

func newAgentMessageStore(db *gorm.DB) *agentMessageStore {
	return &agentMessageStore{db: db}
}

const agentHistoryLimit = 60 // 单次回填的最近消息数，与 agent 侧 MaxHistoryMessages 对齐

// Load 读取项目最近的对话历史，按时间正序返回。
func (s *agentMessageStore) Load(ctx context.Context, projectID string) ([]agent.ChatMessage, error) {
	id, err := uuid.Parse(strings.TrimSpace(projectID))
	if err != nil {
		return nil, err
	}

	var rows []models.AgentMessage
	// 先按 sequence 倒序取最近 N 条，再反转成正序，
	// 避免 ORDER BY ASC + LIMIT 拿到最旧的 N 条
	if err := s.db.WithContext(ctx).
		Where("project_id = ?", id).
		Order("sequence DESC, created_at DESC").
		Limit(agentHistoryLimit).
		Find(&rows).Error; err != nil {
		return nil, err
	}

	out := make([]agent.ChatMessage, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		out = append(out, agent.ChatMessage{
			ID:        row.ID.String(),
			Role:      row.Role,
			Content:   row.Content,
			ToolName:  row.ToolName,
			ToolID:    row.ToolID,
			CreatedAt: row.CreatedAt,
		})
	}
	return out, nil
}

// Append 追加一条消息。
// sequence 取该项目当前最大值 +1；并发写入下可能重号，
// 但排序主键是 (sequence, created_at)，重号不会导致乱序到丢消息。
func (s *agentMessageStore) Append(ctx context.Context, projectID, userID string, msg agent.ChatMessage) error {
	pid, err := uuid.Parse(strings.TrimSpace(projectID))
	if err != nil {
		return err
	}
	uid, err := uuid.Parse(strings.TrimSpace(userID))
	if err != nil {
		// 未登录场景（如内部触发）允许无归属消息，但不伪造用户
		uid = uuid.Nil
	}

	var maxSeq int64
	_ = s.db.WithContext(ctx).Model(&models.AgentMessage{}).
		Where("project_id = ?", pid).
		Select("COALESCE(MAX(sequence), 0)").
		Scan(&maxSeq).Error

	createdAt := msg.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	row := models.AgentMessage{
		ProjectID: pid,
		UserID:    uid,
		Role:      msg.Role,
		Content:   msg.Content,
		ToolName:  msg.ToolName,
		ToolID:    msg.ToolID,
		Sequence:  maxSeq + 1,
	}
	row.ID = uuid.New()
	row.CreatedAt = createdAt
	row.UpdatedAt = createdAt
	return s.db.WithContext(ctx).Create(&row).Error
}
