package models

import (
	"github.com/google/uuid"
)

// AgentMemory Agent 记忆持久化
//
// M-03：记忆此前只存在进程内存里，服务重启即丢失。
// 落库后 preference/fact/decision/checkpoint 可以跨会话保留。
//
// 策略是 cache-through：内存命中优先（频繁读写），
// 冷启动时从库回填。
type AgentMemory struct {
	Base
	ProjectID uuid.UUID `gorm:"type:uuid;index:idx_agent_memories_project_kind,priority:1;not null" json:"project_id"`
	UserID    uuid.UUID `gorm:"type:uuid;index;not null" json:"user_id"`
	Kind      string    `gorm:"size:24;index:idx_agent_memories_project_kind,priority:2;not null" json:"kind"` // preference / fact / decision / checkpoint
	Content   string    `gorm:"type:text;not null" json:"content"`
	// Sequence 同一项目内的单调序号，用于稳定排序
	Sequence int64 `gorm:"index;not null;default:0" json:"sequence"`
}

func (AgentMemory) TableName() string { return "agent_memories" }
