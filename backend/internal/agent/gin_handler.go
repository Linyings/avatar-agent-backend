// gin_handler.go — Agent Gin 路由处理器
// 适配现有 server 的 Gin 框架
package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// RegisterGinRoutes 在 Gin 路由组上注册 Agent API
// 用法：agent.RegisterGinRoutes(r.Group("/api/v1/agent"), agent.Deps{...})
// deps 由 server 层注入真实 LLM、项目解析器与消息存储。
func RegisterGinRoutes(rg *gin.RouterGroup, deps Deps) {
	globalDeps = deps
	rg.POST("/chat", GinHandleChat)
	rg.GET("/stream", GinHandleStream)
	rg.POST("/confirm", GinHandleConfirm)
	rg.POST("/stop", GinHandleStop)
	rg.GET("/history", GinHandleHistory)
	rg.POST("/pre-production/start", GinHandlePreProductionStart)
	rg.POST("/pre-production/confirm", GinHandlePreProductionConfirm)
	rg.GET("/pre-production/get", GinHandlePreProductionGet)
}

// globalDeps 路由注册时注入的依赖。
// 放在包级是为了让既有 handler 签名保持 Gin 风格（不额外传参），
// 代价是同一进程内只能有一套实现——目前单实例部署，可接受。
var globalDeps Deps

// GinHandleChat POST /api/v1/agent/chat
func GinHandleChat(c *gin.Context) {
	var input ChatInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	if input.Message == "" && !input.Accept {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message is required"})
		return
	}

	if input.ProjectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "projectId is required"})
		return
	}
	if !authorizeProject(c, input.ProjectID) {
		return
	}

	if input.RequestID == "" {
		input.RequestID = uuid.New().String()
	}
	if input.TurnID == "" {
		input.TurnID = uuid.New().String()
	}

	// 项目状态注入：拿不到就明确失败，不要用空状态继续跑
	// （空状态会让工具读到假数据，比直接报错更难排查）
	var projectState ProjectState
	owner := requestOwner(c)
	if globalDeps.Resolve != nil {
		state, err := globalDeps.Resolve(c.Request.Context(), owner, input.ProjectID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
			return
		}
		projectState = state
	}

	config := SessionConfig{
		ProjectID:     input.ProjectID,
		UserID:        owner.String(),
		LLM:           globalDeps.LLM,
		MessageStore:  globalDeps.Messages,
		MemoryBackend: globalDeps.Memories,
		Runner:        globalDeps.Runner,
		Packages:      globalDeps.Packages,
	}
	session := GetOrCreateSession(config)
	if globalDeps.Messages != nil {
		session.hydrate(c.Request.Context(), input.ProjectID)
	}

	session.SetEmitter(func(event any) {
		globalSSEHub.Publish(input.ProjectID, event)
	})

	// 对话开始前把节点进度推给工作台，让 12 个节点卡片反映真实执行状态，
	// 而不是等前端自己猜或轮询。
	if statuses := projectState.GetNodeStatuses(); len(statuses) > 0 {
		globalSSEHub.Publish(input.ProjectID, gin.H{
			"type":   "node_status",
			"turnId": input.TurnID,
			"nodes":  statuses,
		})
	}

	output, err := session.Chat(c.Request.Context(), input, projectState)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  "agent execution failed",
			"detail": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, output)
}

// authorizeProject 校验当前用户是否有权访问该项目。
//
// H-07：只验证登录态不足以保证数据隔离——任何登录用户只要拿到别人的
// projectId 就能读历史、发对话、看预制方案。返回 404 而不是 403，
// 避免泄露「这个项目确实存在，只是不属于你」。
//
// 未注入 Authorizer 时一律拒绝：宁可功能不可用，也不能默默放开越权。
func authorizeProject(c *gin.Context, projectID string) bool {
	if strings.TrimSpace(projectID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "projectId is required"})
		return false
	}
	if globalDeps.Authorize == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "project access check unavailable"})
		return false
	}
	if err := globalDeps.Authorize(c.Request.Context(), requestOwner(c), projectID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return false
	}
	return true
}

// requestOwner 从 Gin 上下文取当前用户 ID。
// 兼容 server 包注入的 models.User（有 ID 字段）与中间件可能放入的字符串。
func requestOwner(c *gin.Context) uuid.UUID {
	v, exists := c.Get("user")
	if !exists {
		return uuid.Nil
	}
	switch u := v.(type) {
	case interface{ GetID() uuid.UUID }:
		return u.GetID()
	case string:
		if id, err := uuid.Parse(u); err == nil {
			return id
		}
	}
	// models.User 是结构体，用反射取 ID 会在热路径上变慢，
	// 因此 server 侧同时写入 "userID" 字符串键，见 server.setAgentUser
	if raw, ok := c.Get("userID"); ok {
		if s, ok := raw.(string); ok {
			if id, err := uuid.Parse(s); err == nil {
				return id
			}
		}
	}
	return uuid.Nil
}

// GinHandleStream GET /api/v1/agent/stream?projectId=xxx
func GinHandleStream(c *gin.Context) {
	projectID := c.Query("projectId")
	if projectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "projectId is required"})
		return
	}
	if !authorizeProject(c, projectID) {
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	client := globalSSEHub.Subscribe(projectID)
	defer globalSSEHub.Unsubscribe(client)

	// 发送连接成功事件
	fmt.Fprintf(c.Writer, "event: connected\ndata: {\"projectId\":%q}\n\n", projectID)
	c.Writer.Flush()

	ctx := c.Request.Context()
	for {
		select {
		case event := <-client.Events:
			data, _ := jsonMarshal(event)
			fmt.Fprintf(c.Writer, "data: %s\n\n", data)
			c.Writer.Flush()
		case <-ctx.Done():
			return
		case <-client.Done:
			return
		}
	}
}

// GinHandleConfirm POST /api/v1/agent/confirm
func GinHandleConfirm(c *gin.Context) {
	var req struct {
		ProjectID  string `json:"projectId"`
		QuestionID string `json:"questionId"`
		ChoiceKey  string `json:"choiceKey"`
		CustomText string `json:"customText,omitempty"`
		Accept     bool   `json:"accept"`
		ProposalID string `json:"proposalId"`
		Revision   int    `json:"revision"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if !authorizeProject(c, req.ProjectID) {
		return
	}

	if req.Accept {
		if globalDeps.Packages != nil {
			result, err := globalDeps.Packages.Confirm(c.Request.Context(), requestOwner(c).String(), req.ProjectID, req.ProposalID, req.Revision)
			if err != nil {
				c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, result)
			return
		}
		p := globalProposals.Get(req.ProjectID)
		if p == nil || p.Applied {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no pending proposal"})
			return
		}
		// C-04 修复：提案写入尚未实现，明确返回 pending 而非谎报 applied
		// TODO: 接入真实 projectState 写入后，在写入成功后再调用 MarkApplied
		c.JSON(http.StatusNotImplemented, gin.H{
			"status": "pending",
			"note":   "提案写入通道未就绪，请等待后端集成完成",
		})
		return
	}

	globalSSEHub.Publish(req.ProjectID, gin.H{
		"type":       "question_closed",
		"questionId": req.QuestionID,
	})

	c.JSON(http.StatusOK, gin.H{"status": "confirmed"})
}

// GinHandleStop POST /api/v1/agent/stop
func GinHandleStop(c *gin.Context) {
	var req struct {
		ProjectID string `json:"projectId"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if !authorizeProject(c, req.ProjectID) {
		return
	}

	globalSessions.mu.Lock()
	entry, ok := globalSessions.sessions[req.ProjectID]
	globalSessions.mu.Unlock()

	if ok {
		entry.session.Abort()
	}
	c.JSON(http.StatusOK, gin.H{"status": "stopped"})
}

// GinHandleHistory GET /api/v1/agent/history?projectId=xxx
func GinHandleHistory(c *gin.Context) {
	projectID := c.Query("projectId")
	if !authorizeProject(c, projectID) {
		return
	}

	globalSessions.mu.Lock()
	entry, ok := globalSessions.sessions[projectID]
	globalSessions.mu.Unlock()

	// 有持久化时以库为准（重启后内存已空，SSE 会话可能也不存在）
	if globalDeps.Messages != nil {
		if stored, err := globalDeps.Messages.Load(c.Request.Context(), projectID); err == nil {
			if stored == nil {
				stored = []ChatMessage{}
			}
			c.JSON(http.StatusOK, gin.H{"messages": stored})
			return
		} else if !ok {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "聊天历史暂时无法读取，请重试"})
			return
		}
	}
	if !ok {
		c.JSON(http.StatusOK, gin.H{"messages": []any{}})
		return
	}

	history := entry.session.getNormalizedHistory()
	c.JSON(http.StatusOK, gin.H{"messages": history})
}

// GinHandlePreProductionStart POST /api/v1/agent/pre-production/start
func GinHandlePreProductionStart(c *gin.Context) {
	var req struct {
		ProjectID string `json:"projectId"`
		UserID    string `json:"userId"`
		Topic     string `json:"topic"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if !authorizeProject(c, req.ProjectID) {
		return
	}

	plan := StartPreProduction(req.ProjectID, req.UserID)
	directions := GenerateScriptDirections(req.Topic)
	question := BuildScriptDirectionQuestion(directions)

	c.JSON(http.StatusOK, gin.H{
		"plan":     plan,
		"question": question,
	})
}

// GinHandlePreProductionConfirm POST /api/v1/agent/pre-production/confirm
func GinHandlePreProductionConfirm(c *gin.Context) {
	var req struct {
		ProjectID  string `json:"projectId"`
		Step       string `json:"step"`
		ChoiceKey  string `json:"choiceKey"`
		CustomText string `json:"customText,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if !authorizeProject(c, req.ProjectID) {
		return
	}

	plan := GetPreProduction(req.ProjectID)
	if plan == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pre-production not started"})
		return
	}

	switch PreProductionStep(req.Step) {
	case StepScriptDirection:
		// H-05 修复：自定义文本与预设方向互斥，避免双重确认
		if req.CustomText != "" {
			ConfirmScriptDirection(plan, ScriptDirection{Title: req.CustomText, Style: "自定义"})
			break
		}
		directions := GenerateScriptDirections("")
		for i, d := range directions {
			if req.ChoiceKey == fmt.Sprintf("direction-%d", i+1) {
				ConfirmScriptDirection(plan, d)
				break
			}
		}

	case StepProductionMethod:
		methods := GenerateProductionMethods(plan.ScriptDirection)
		for i, m := range methods {
			if req.ChoiceKey == fmt.Sprintf("method-%d", i+1) {
				ConfirmProductionMethod(plan, m)
				break
			}
		}

	case StepMaterialNeeds:
		ConfirmMaterialNeeds(plan, plan.MaterialNeeds)

	case StepFinalExpectation:
		exp := GenerateFinalExpectation(plan.ScriptDirection, plan.ProductionMethod, plan.MaterialNeeds)
		ConfirmFinalExpectation(plan, exp)
	}

	// 素材需求步骤依赖真实段落：从项目状态取，避免给出空清单（H-11）
	segments := loadSegments(c, req.ProjectID)
	question := plan.GetCurrentStepQuestionWithSegments(segments)
	c.JSON(http.StatusOK, gin.H{
		"plan":     plan,
		"question": question,
		"complete": plan.IsComplete(),
	})
}

// loadSegments 从项目状态读取段落。
// 取不到时返回 nil，由 GetCurrentStepQuestionWithSegments 给出明确提示。
func loadSegments(c *gin.Context, projectID string) []ScriptSegment {
	if globalDeps.Resolve == nil || projectID == "" {
		return nil
	}
	state, err := globalDeps.Resolve(c.Request.Context(), requestOwner(c), projectID)
	if err != nil || state == nil {
		return nil
	}
	segments, err := state.GetScriptSegments("")
	if err != nil {
		return nil
	}
	return segments
}

// GinHandlePreProductionGet GET /api/v1/agent/pre-production/get?projectId=xxx
func GinHandlePreProductionGet(c *gin.Context) {
	projectID := c.Query("projectId")
	if !authorizeProject(c, projectID) {
		return
	}
	plan := GetPreProduction(projectID)
	if plan == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "pre-production not found"})
		return
	}
	question := plan.GetCurrentStepQuestionWithSegments(loadSegments(c, projectID))
	c.JSON(http.StatusOK, gin.H{
		"plan":     plan,
		"question": question,
	})
}

// jsonMarshal 辅助 JSON 序列化
func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}
