package models

import (
	"github.com/google/uuid"
)

// AgentMessage Agent 对话消息持久化
//
// M-02：会话历史此前只存在进程内存里，服务重启即丢失，
// 多实例部署还会各读各的。落库后 history 接口与上下文构建
// 都能拿到稳定的历史。
//
// 策略是 cache-through：内存命中优先（Agent 循环中间轮次读写频繁），
// 冷启动时从库回填。因此这里不做唯一约束，重复写入由上层幂等处理。
type AgentMessage struct {
	Base
	ProjectID uuid.UUID `gorm:"type:uuid;index:idx_agent_messages_project_created,priority:1;not null" json:"project_id"`
	UserID    uuid.UUID `gorm:"type:uuid;index;not null" json:"user_id"`
	TurnID    string    `gorm:"size:64;index" json:"turn_id"`
	Role      string    `gorm:"size:16;not null" json:"role"` // user / assistant / tool
	Content   string    `gorm:"type:text" json:"content"`
	ToolName  string    `gorm:"size:64" json:"tool_name,omitempty"`
	ToolID    string    `gorm:"size:128" json:"tool_id,omitempty"`
	// Sequence 同一项目内的单调序号，用于稳定排序
	// （同一秒内写入的多条消息靠 CreatedAt 无法定序）
	Sequence int64 `gorm:"index:idx_agent_messages_project_created,priority:2;not null;default:0" json:"sequence"`
}

func (AgentMessage) TableName() string { return "agent_messages" }
