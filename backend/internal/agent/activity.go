package agent

import "time"

// Activity survives page refresh within the running server. Workflow execution
// itself is separately persisted and remains the authority after server restart.
type Activity struct {
	Status    string    `json:"status"`
	Phase     string    `json:"phase"`
	Tool      string    `json:"tool,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	TurnID    string    `json:"turn_id"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func ProjectActivity(project string) *Activity {
	globalSessions.mu.Lock()
	entry := globalSessions.sessions[project]
	globalSessions.mu.Unlock()
	if entry == nil {
		return nil
	}
	entry.session.mu.Lock()
	defer entry.session.mu.Unlock()
	if entry.session.activity.Status == "" {
		return nil
	}
	out := entry.session.activity
	return &out
}

func (s *SessionState) setActivity(status, phase, tool, detail string) {
	s.mu.Lock()
	now := time.Now().UTC()
	if s.activity.TurnID != s.turnID || s.activity.Status == "" {
		s.activity.StartedAt = now
	}
	s.activity.Status = status
	s.activity.Phase = phase
	s.activity.Tool = tool
	s.activity.Detail = detail
	s.activity.TurnID = s.turnID
	s.activity.UpdatedAt = now
	out := s.activity
	emitter := s.emitter
	s.mu.Unlock()
	if emitter != nil {
		emitter(map[string]any{"type": "agent_activity", "activity": out})
	}
}
