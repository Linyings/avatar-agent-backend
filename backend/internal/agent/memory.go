// memory.go — Agent 长期记忆
//
// 独立成类型而非嵌在 SessionState 里，是为了让工具持有稳定的引用：
// 工具在 RegisterAvatarTools 时捕获它，会话重建时记忆仍在。
// M-03 修复：支持持久化，通过 backend 注入的 store 在启动时回填和写入时同步。
package agent

import (
	"context"
	"strings"
	"sync"
)

// MemoryPersistence 记忆持久化接口（可选）
type MemoryPersistence interface {
	Load(ctx context.Context, projectID string) ([]Memory, error)
	Save(ctx context.Context, mem Memory) error
	Delete(ctx context.Context, projectID string, kind MemoryKind) (int, error)
}

// memoryStore 按项目隔离的记忆仓库
type memoryStore struct {
	mu      sync.Mutex
	entries map[string][]Memory // projectID -> memories
	backend MemoryPersistence   // 可选的持久化后端
}

func newMemoryStore() *memoryStore {
	return &memoryStore{entries: make(map[string][]Memory)}
}

func newMemoryStoreWithBackend(backend MemoryPersistence) *memoryStore {
	return &memoryStore{
		entries: make(map[string][]Memory),
		backend: backend,
	}
}

const maxMemoryEntries = 60 // 单项目上限，防止无界增长撑爆上下文

// hydrate 从持久化后端回填记忆到内存（首次访问时调用）
// 不加锁，由调用方持有锁
func (m *memoryStore) hydrate(ctx context.Context, projectID string) {
	if m.backend == nil {
		return
	}
	if _, loaded := m.entries[projectID]; loaded {
		return // 已回填过
	}
	memories, err := m.backend.Load(ctx, projectID)
	if err != nil || len(memories) == 0 {
		m.entries[projectID] = []Memory{} // 标记已回填，避免重复查询
		return
	}
	m.entries[projectID] = memories
}

// Remember 写入一条记忆。同 kind 同内容视为重复，不重复追加。
func (m *memoryStore) Remember(projectID string, kind MemoryKind, content string) Memory {
	m.mu.Lock()
	defer m.mu.Unlock()

	// NEW-C-01 修复：首次访问时从后端回填，避免在锁内调用 hydrate
	if m.backend != nil {
		if _, loaded := m.entries[projectID]; !loaded {
			// 注意：hydrate 不加锁，直接在当前锁内调用
			m.hydrate(context.Background(), projectID)
		}
	}

	content = strings.TrimSpace(content)
	entry := NewMemory(projectID, kind, content)
	if content == "" {
		return entry
	}
	list := m.entries[projectID]
	for _, existing := range list {
		if existing.Kind == kind && existing.Content == content {
			return existing
		}
	}
	// 超出上限时丢弃最旧的非 decision 记忆（决策比偏好更重要）
	if len(list) >= maxMemoryEntries {
		dropIdx := 0
		for i, e := range list {
			if e.Kind != MemoryDecision {
				dropIdx = i
				break
			}
		}
		list = append(list[:dropIdx], list[dropIdx+1:]...)
	}
	m.entries[projectID] = append(list, entry)

	// 同步到持久化后端
	if m.backend != nil {
		go m.backend.Save(context.Background(), entry)
	}

	return entry
}

// Forget 删除记忆。kind 为空时清空该项目的全部记忆。
func (m *memoryStore) Forget(projectID string, kind MemoryKind) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	// NEW-C-01 修复：首次访问时从后端回填
	if m.backend != nil {
		if _, loaded := m.entries[projectID]; !loaded {
			m.hydrate(context.Background(), projectID)
		}
	}

	list := m.entries[projectID]
	if kind == "" {
		count := len(list)
		delete(m.entries, projectID)
		// 同步删除到持久化后端
		if m.backend != nil {
			go m.backend.Delete(context.Background(), projectID, "")
		}
		return count
	}
	kept := make([]Memory, 0, len(list))
	removed := 0
	for _, e := range list {
		if e.Kind == kind {
			removed++
			continue
		}
		kept = append(kept, e)
	}
	m.entries[projectID] = kept

	// 同步删除到持久化后端
	if m.backend != nil && removed > 0 {
		go m.backend.Delete(context.Background(), projectID, kind)
	}

	return removed
}

// All 返回项目全部记忆的快照
func (m *memoryStore) All(projectID string) []Memory {
	m.mu.Lock()
	defer m.mu.Unlock()

	// NEW-C-01 修复：首次访问时从后端回填
	if m.backend != nil {
		if _, loaded := m.entries[projectID]; !loaded {
			m.hydrate(context.Background(), projectID)
		}
	}

	list := m.entries[projectID]
	out := make([]Memory, len(list))
	copy(out, list)
	return out
}

// Render 渲染为提示词片段
func (m *memoryStore) Render(projectID string) string {
	list := m.All(projectID)
	if len(list) == 0 {
		return ""
	}
	parts := make([]string, 0, len(list))
	for _, e := range list {
		parts = append(parts, "["+string(e.Kind)+"] "+e.Content)
	}
	return strings.Join(parts, "\n")
}

// Count 返回记忆条数
func (m *memoryStore) Count(projectID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries[projectID])
}

// parseMemoryKind 解析模型给出的 kind 字符串
func parseMemoryKind(raw string) MemoryKind {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "preference":
		return MemoryPreference
	case "fact":
		return MemoryFact
	case "decision":
		return MemoryDecision
	case "checkpoint":
		return MemoryCheckpoint
	default:
		return MemoryFact
	}
}
