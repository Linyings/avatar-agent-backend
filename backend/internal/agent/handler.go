// handler.go — Agent HTTP API 处理器
// 注册路由：/api/v1/agent/chat, /api/v1/agent/stream, /api/v1/agent/confirm 等
package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────
// 会话管理器（按 projectID 管理 AgentSession）
// ─────────────────────────────────────────────

type sessionEntry struct {
	session    *SessionState
	lastAccess time.Time
}

type sessionManager struct {
	mu       sync.Mutex
	sessions map[string]*sessionEntry // projectID -> entry
}

var globalSessions = &sessionManager{sessions: make(map[string]*sessionEntry)}

// NEW-H-01 修复：Session 过期时间（30 分钟无活动自动清理）
const sessionTTL = 30 * time.Minute

// GetOrCreateSession 获取或创建会话
func GetOrCreateSession(config SessionConfig) *SessionState {
	globalSessions.mu.Lock()
	defer globalSessions.mu.Unlock()

	// NEW-H-01：清理过期 Session
	now := time.Now()
	for pid, entry := range globalSessions.sessions {
		if now.Sub(entry.lastAccess) > sessionTTL {
			// An in-flight model request can outlive the idle TTL. Do not create
			// a second Session for that project while its first turn still runs.
			if entry.session.turnMu.TryLock() {
				delete(globalSessions.sessions, pid)
				entry.session.Abort()
				entry.session.turnMu.Unlock()
			}
		}
	}

	if entry, ok := globalSessions.sessions[config.ProjectID]; ok {
		entry.lastAccess = now
		return entry.session
	}
	s := NewSession(config)
	globalSessions.sessions[config.ProjectID] = &sessionEntry{
		session:    s,
		lastAccess: now,
	}
	return s
}

// ─────────────────────────────────────────────
// SSE 事件流管理
// ─────────────────────────────────────────────

// SSEClient 单个 SSE 客户端连接
type SSEClient struct {
	ID        string
	ProjectID string
	Events    chan any
	Done      chan struct{}
}

// sseHub 管理所有 SSE 连接
type sseHub struct {
	mu      sync.Mutex
	clients map[string][]*SSEClient // projectID -> clients
}

var globalSSEHub = &sseHub{clients: make(map[string][]*SSEClient)}

// Subscribe 订阅项目事件流
func (h *sseHub) Subscribe(projectID string) *SSEClient {
	client := &SSEClient{
		ID:        uuid.New().String(),
		ProjectID: projectID,
		Events:    make(chan any, 64),
		Done:      make(chan struct{}),
	}
	h.mu.Lock()
	h.clients[projectID] = append(h.clients[projectID], client)
	h.mu.Unlock()
	return client
}

// Unsubscribe 取消订阅
func (h *sseHub) Unsubscribe(client *SSEClient) {
	h.mu.Lock()
	clients := h.clients[client.ProjectID]
	for i, c := range clients {
		if c.ID == client.ID {
			h.clients[client.ProjectID] = append(clients[:i], clients[i+1:]...)
			break
		}
	}
	h.mu.Unlock()
	close(client.Done)
}

// Publish 向项目所有订阅者推送事件
func (h *sseHub) Publish(projectID string, event any) {
	h.mu.Lock()
	clients := append([]*SSEClient(nil), h.clients[projectID]...)
	h.mu.Unlock()
	for _, c := range clients {
		select {
		case c.Events <- event:
		default: // channel 满了就丢弃，避免阻塞
		}
	}
}

// PublishNodeStatus 对外暴露节点状态推送。
//
// 执行引擎住在 server 包，它需要把真实执行进度推给工作台，
// 但不该自己维护一份 SSE 连接表——统一走这里的 hub。
func PublishNodeStatus(projectID, node, status string) {
	if projectID == "" || node == "" {
		return
	}
	globalSSEHub.Publish(projectID, map[string]any{
		"type":   "node_status",
		"node":   node,
		"status": status,
		"nodes":  map[string]string{node: status},
	})
}

// State transitions (including approvals and failures) are emitted only after
// persistence, so clients can refresh the authoritative snapshot immediately.
func PublishWorkflowState(projectID, runID, stage, status string) {
	globalSSEHub.Publish(projectID, map[string]any{"type": "workflow_state", "run_id": runID, "stage": stage, "status": status})
}

// ─────────────────────────────────────────────
// HTTP 处理函数
// ─────────────────────────────────────────────

// HandleChat POST /api/v1/agent/chat
func HandleChat(w http.ResponseWriter, r *http.Request) {
	var input ChatInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}

	if input.Message == "" && !input.Accept {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "message is required"})
		return
	}

	if input.RequestID == "" {
		input.RequestID = uuid.New().String()
	}
	if input.TurnID == "" {
		input.TurnID = uuid.New().String()
	}

	// 获取或创建会话
	config := SessionConfig{
		ProjectID: input.ProjectID,
	}
	session := GetOrCreateSession(config)

	// 设置 SSE 事件推送
	session.SetEmitter(func(event any) {
		globalSSEHub.Publish(input.ProjectID, event)
	})

	// 执行对话（这里用 nil 作为 projectState 占位，实际由 server 注入）
	output, err := session.Chat(r.Context(), input, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":  "agent execution failed",
			"detail": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, output)
}

// HandleStream GET /api/v1/agent/stream?projectId=xxx
// SSE 事件流
func HandleStream(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("projectId")
	if projectID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "projectId is required"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	client := globalSSEHub.Subscribe(projectID)
	defer globalSSEHub.Unsubscribe(client)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming not supported"})
		return
	}

	// 发送连接成功事件
	fmt.Fprintf(w, "event: connected\ndata: {\"projectId\":%q}\n\n", projectID)
	flusher.Flush()

	for {
		select {
		case event := <-client.Events:
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		case <-client.Done:
			return
		}
	}
}

// HandleConfirm POST /api/v1/agent/confirm
// 用户确认提案或审核选项
func HandleConfirm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID  string `json:"projectId"`
		QuestionID string `json:"questionId"`
		ChoiceKey  string `json:"choiceKey"`
		CustomText string `json:"customText,omitempty"`
		Accept     bool   `json:"accept"` // 直接接受提案
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}

	// 接受提案
	if req.Accept {
		p := globalProposals.Get(req.ProjectID)
		if p == nil || p.Applied {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "no pending proposal"})
			return
		}
		globalProposals.MarkApplied(req.ProjectID)
		writeJSON(w, http.StatusOK, map[string]any{"status": "applied"})
		return
	}

	// 处理确认选项
	confirmation := UserConfirmation{
		QuestionID: req.QuestionID,
		ChoiceKey:  req.ChoiceKey,
		CustomText: req.CustomText,
	}
	_ = confirmation // 实际处理由具体审核逻辑完成

	// 推送 question_closed 事件
	globalSSEHub.Publish(req.ProjectID, map[string]any{
		"type":       "question_closed",
		"questionId": req.QuestionID,
	})

	writeJSON(w, http.StatusOK, map[string]any{"status": "confirmed"})
}

// HandleStop POST /api/v1/agent/stop
func HandleStop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string `json:"projectId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}

	globalSessions.mu.Lock()
	entry, ok := globalSessions.sessions[req.ProjectID]
	globalSessions.mu.Unlock()

	if ok {
		entry.session.Abort()
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "stopped"})
}

// HandleHistory GET /api/v1/agent/history?projectId=xxx
func HandleHistory(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("projectId")
	if projectID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "projectId is required"})
		return
	}

	globalSessions.mu.Lock()
	entry, ok := globalSessions.sessions[projectID]
	globalSessions.mu.Unlock()

	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"messages": []any{}})
		return
	}

	history := entry.session.getNormalizedHistory()
	writeJSON(w, http.StatusOK, map[string]any{"messages": history})
}

// ─────────────────────────────────────────────
// 前期沟通 API
// ─────────────────────────────────────────────

// HandlePreProductionStart POST /api/v1/agent/pre-production/start
func HandlePreProductionStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID string `json:"projectId"`
		UserID    string `json:"userId"`
		Topic     string `json:"topic"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}

	plan := StartPreProduction(req.ProjectID, req.UserID)

	// 生成第一步的确认问题
	directions := GenerateScriptDirections(req.Topic)
	question := BuildScriptDirectionQuestion(directions)

	writeJSON(w, http.StatusOK, map[string]any{
		"plan":     plan,
		"question": question,
	})
}

// HandlePreProductionConfirm POST /api/v1/agent/pre-production/confirm
func HandlePreProductionConfirm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID  string         `json:"projectId"`
		Step       string         `json:"step"`
		ChoiceKey  string         `json:"choiceKey"`
		CustomText string         `json:"customText,omitempty"`
		Data       map[string]any `json:"data,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
		return
	}

	plan := GetPreProduction(req.ProjectID)
	if plan == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "pre-production not started"})
		return
	}

	switch PreProductionStep(req.Step) {
	case StepScriptDirection:
		// 从选项中恢复方向
		directions := GenerateScriptDirections("")
		var selected *ScriptDirection
		for i, d := range directions {
			if req.ChoiceKey == fmt.Sprintf("direction-%d", i+1) {
				selected = &d
				break
			}
		}
		if selected == nil && req.CustomText != "" {
			selected = &ScriptDirection{Title: req.CustomText, Style: "自定义"}
		}
		if selected != nil {
			ConfirmScriptDirection(plan, *selected)
		}

	case StepProductionMethod:
		methods := GenerateProductionMethods(plan.ScriptDirection)
		var selected *ProductionMethod
		for i, m := range methods {
			if req.ChoiceKey == fmt.Sprintf("method-%d", i+1) {
				selected = &m
				break
			}
		}
		if selected != nil {
			ConfirmProductionMethod(plan, *selected)
		}

	case StepMaterialNeeds:
		ConfirmMaterialNeeds(plan, plan.MaterialNeeds)

	case StepFinalExpectation:
		exp := GenerateFinalExpectation(plan.ScriptDirection, plan.ProductionMethod, plan.MaterialNeeds)
		ConfirmFinalExpectation(plan, exp)
	}

	// 返回下一步问题
	question := plan.GetCurrentStepQuestion()
	writeJSON(w, http.StatusOK, map[string]any{
		"plan":     plan,
		"question": question,
		"complete": plan.IsComplete(),
	})
}

// HandlePreProductionGet GET /api/v1/agent/pre-production/:projectId
func HandlePreProductionGet(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("projectId")
	plan := GetPreProduction(projectID)
	if plan == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "pre-production not found"})
		return
	}
	question := plan.GetCurrentStepQuestion()
	writeJSON(w, http.StatusOK, map[string]any{
		"plan":     plan,
		"question": question,
	})
}

// ─────────────────────────────────────────────
// 路由注册
// ─────────────────────────────────────────────

// RegisterRoutes 注册 Agent API 路由到指定 mux
func RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/agent/chat", HandleChat)
	mux.HandleFunc("/api/v1/agent/stream", HandleStream)
	mux.HandleFunc("/api/v1/agent/confirm", HandleConfirm)
	mux.HandleFunc("/api/v1/agent/stop", HandleStop)
	mux.HandleFunc("/api/v1/agent/history", HandleHistory)
	mux.HandleFunc("/api/v1/agent/pre-production/start", HandlePreProductionStart)
	mux.HandleFunc("/api/v1/agent/pre-production/confirm", HandlePreProductionConfirm)
	mux.HandleFunc("/api/v1/agent/pre-production/get", HandlePreProductionGet)
}

// ─────────────────────────────────────────────
// 辅助
// ─────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(data)
}
