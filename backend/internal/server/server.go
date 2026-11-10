package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/narratoai/backend-go/internal/agent"
	"github.com/narratoai/backend-go/internal/config"
	"github.com/narratoai/backend-go/internal/models"
	"github.com/narratoai/backend-go/internal/prompts"
	"github.com/narratoai/backend-go/internal/providers"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type creemBillingClient interface {
	Configured() bool
	VerifyWebhookSignature(payload []byte, signature string) error
	CreateCheckout(ctx context.Context, req providers.CreemCheckoutRequest) (providers.CreemCheckoutResponse, error)
	GetCheckout(ctx context.Context, checkoutID string) (map[string]any, error)
	CancelSubscription(ctx context.Context, subscriptionID string, scheduled bool) error
	UpgradeSubscription(ctx context.Context, subscriptionID, productID, updateBehavior string) (map[string]any, error)
	CreateCustomerPortal(ctx context.Context, customerID string) (string, error)
}

type wechatPayClient interface {
	Configured() bool
	CreateNativeOrder(ctx context.Context, req providers.WeChatNativeOrderRequest) (providers.WeChatNativeOrderResponse, error)
	QueryTransactionByOutTradeNo(ctx context.Context, outTradeNo string) (providers.WeChatTransaction, error)
	DecryptNotification(payload []byte) (providers.WeChatTransaction, error)
	VerifyNotificationSignature(timestamp, nonce string, body []byte, signatureB64, serialNo string) error
}

type mailer interface {
	SendVerificationCode(ctx context.Context, to, code, purpose, locale string) error
}

type App struct {
	avatarMVP      *avatarMVPService
	avatarWorkflow *avatarWorkflowService
	modelLab       *modelLabService
	cfg            config.Config
	db             *gorm.DB
	r              *gin.Engine

	wechat          wechatPayClient
	creem           creemBillingClient
	alipay          providers.AlipayClient
	dashscope       providers.DashScopeClient
	dubbing         *providers.DubbingClient
	silicon         providers.SiliconFlowClient
	openai          providers.OpenAIClient
	fish            providers.FishAudioClient
	elevenlabs      providers.ElevenLabsClient
	promptService   *prompts.Service
	promptBuilder   *prompts.Builder
	mailer          mailer
	oauthHTTPClient *http.Client
	workflowRuns    *WorkflowRunStateService
	storySearch     *storyContextSearchService

	authUserCache          sync.Map
	elevenLabsScribeJobs   sync.Map
	elevenLabsScribeBilled sync.Map
	vendorASRBilled        sync.Map

	legacyWorkflowRejectedCount atomic.Int64
}

type cachedAuthUser struct {
	user      models.User
	expiresAt time.Time
}

const authUserCacheTTL = 5 * time.Second

func New(cfg config.Config, db *gorm.DB) *App {
	if !cfg.Debug {
		gin.SetMode(gin.ReleaseMode)
	}

	promptService, promptBuilder := initPromptServices(cfg.PromptTemplateDir)
	app := &App{
		cfg:             cfg,
		db:              db,
		wechat:          providers.NewWeChatClient(cfg),
		creem:           providers.NewCreemClient(cfg),
		alipay:          providers.NewAlipayClient(cfg, providers.AlipayMethods{}),
		dashscope:       providers.NewDashScopeClient(cfg),
		silicon:         providers.NewSiliconFlowClient(cfg),
		openai:          providers.NewOpenAIClient(cfg, promptBuilder),
		fish:            providers.NewFishAudioClient(cfg),
		elevenlabs:      providers.NewElevenLabsClient(cfg),
		promptService:   promptService,
		promptBuilder:   promptBuilder,
		mailer:          newSMTPMailer(cfg),
		oauthHTTPClient: http.DefaultClient,
		storySearch:     newStoryContextSearchService(providers.NewTavilyClient(cfg)),
	}
	app.workflowRuns = NewWorkflowRunStateService(db, WorkflowRunStateHooks{
		AfterComplete: app.confirmWorkflowRunReservationsTx,
		AfterCancel:   app.releaseWorkflowRunReservationsTx,
		AfterExpire:   app.releaseWorkflowRunReservationsTx,
		AfterActivity: app.extendWorkflowRunReservationsTx,
	})
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.Use(cors.New(cors.Config{
		AllowOrigins:     cfg.CORSOrigins,
		AllowMethods:     []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "Accept", "Range", "X-Movie-Project-Id"},
		ExposeHeaders:    []string{"X-Total-Count", "X-Page-Count", "Accept-Ranges", "Content-Range", "Content-Length"},
		AllowCredentials: true,
		CustomSchemas:    []string{"app://"},
		MaxAge:           12 * time.Hour,
	}))
	app.r = r
	app.avatarMVP = newAvatarMVP(app)
	app.avatarWorkflow = newAvatarWorkflow(app)
	app.modelLab = newModelLab(app)
	app.routes()
	return app
}

func initPromptServices(dir string) (*prompts.Service, *prompts.Builder) {
	service, err := prompts.NewService(dir)
	if err != nil {
		return nil, nil
	}
	return service, prompts.NewBuilder(service)
}

func (a *App) Router() *gin.Engine {
	return a.r
}

// agentLLM 提供给 agent 包的模型客户端
func (a *App) agentLLM() agent.LLMClient {
	return newAgentLLMAdapter(a.cfg)
}

// agentMessages 提供给 agent 包的对话历史存储
func (a *App) agentMessages() agent.MessageStore {
	if a.db == nil {
		return nil
	}
	return newAgentMessageStore(a.db)
}

// agentMemories 提供给 agent 包的记忆持久化后端（M-03 修复）
func (a *App) agentMemories() agent.MemoryPersistence {
	if a.db == nil {
		return nil
	}
	return newAgentMemoryStore(a.db)
}

func (a *App) WorkflowRuns() *WorkflowRunStateService {
	return a.workflowRuns
}

func (a *App) SetWorkflowRunStateHooks(hooks WorkflowRunStateHooks) {
	a.workflowRuns.SetHooks(hooks)
}

func (a *App) routes() {
	a.r.GET("/health", a.health)
	if a.cfg.DesktopUpdateRoot != "" {
		a.r.GET("/desktop-updates/*filePath", a.desktopUpdateFile)
		a.r.HEAD("/desktop-updates/*filePath", a.desktopUpdateFile)
	}
	v1 := a.r.Group(a.cfg.APIv1Prefix)
	avatarRoutes := v1.Group("/avatar-mvp", a.authRequired())
	a.registerAvatarWorkflow(avatarRoutes)
	avatarRoutes.GET("/config", a.avatarConfig)

	// Agent API — 前期沟通 + 对话 + 确认 + SSE 流
	agentRoutes := v1.Group("/agent", a.authRequired())
	agent.RegisterGinRoutes(agentRoutes, agent.Deps{
		LLM:       a.agentLLM(),
		Resolve:   agentProjectResolver{app: a}.Resolve,
		Authorize: agentProjectAuthorizer{app: a}.Authorize,
		Messages:  a.agentMessages(),
		Memories:  a.agentMemories(),
		Runner:    agentProjectRunner{app: a},
		Packages:  avatarPackageHost{app: a},
	})
	a.registerPackageRoutes(agentRoutes)

	avatarRoutes.GET("/projects/:projectId/task", a.avatarLatestForProject)
	avatarRoutes.POST("/tasks", a.avatarCreate)
	avatarRoutes.GET("/tasks/:id", a.avatarGet)
	avatarRoutes.POST("/tasks/:id/retry", a.avatarRetry)
	avatarRoutes.GET("/tasks/:id/video", a.avatarVideo)
	v1.GET("/desktop/releases/windows/latest", a.latestWindowsDesktopRelease)
	v1.GET("/desktop/download/windows", a.downloadLatestWindowsDesktopRelease)
	v1.HEAD("/desktop/download/windows", a.downloadLatestWindowsDesktopRelease)
	v1.GET("/test", func(c *gin.Context) { ok(c, gin.H{"message": "API v1 正常工作", "version": "0.1.0"}) })

	v1.GET("/email/unsubscribe", a.unsubscribeEmailCampaignHTML)
	v1.POST("/email/unsubscribe", a.unsubscribeEmailCampaignJSON)
	auth := v1.Group("/auth")
	auth.POST("/register", a.register)
	auth.POST("/request-email-code", a.emailCode)
	auth.POST("/login", a.login)
	auth.POST("/refresh", a.refresh)
	auth.POST("/forgot-password", a.forgotPassword)
	auth.POST("/reset-password", a.resetPassword)
	auth.POST("/logout", a.authRequired(), a.logout)
	auth.GET("/me", a.authRequired(), a.me)
	auth.POST("/send-verification-code", a.authRequired(), a.emailCode)
	auth.POST("/verify-email", a.authRequired(), a.verifyEmail)
	auth.GET("/oauth/:provider/start", a.oauthStart)
	auth.GET("/oauth/:provider/callback", a.oauthCallback)
	auth.POST("/oauth/exchange", a.oauthExchange)
	auth.POST("/desktop/requests", a.createDesktopAuthorizationRequest)
	auth.POST("/desktop/requests/:request_id/authorize", a.authRequired(), a.authorizeDesktopAuthorizationRequest)
	auth.POST("/desktop/exchange", a.exchangeDesktopAuthorization)
	auth.POST("/desktop/refresh", a.refreshDesktopAuthorization)
	auth.DELETE("/desktop/session", a.authRequired(), a.revokeDesktopAuthorizationSession)
	auth.GET("/email-quota", a.authRequired(), func(c *gin.Context) {
		ok(c, a.emailQuota(c))
	})

	users := v1.Group("/users", a.authRequired())
	users.GET("/profile", a.me)
	users.PUT("/profile", a.updateProfile)
	users.GET("/settings", a.getSettings)
	users.PUT("/settings", a.updateSettings)
	users.PUT("/password", a.changePassword)

	projects := v1.Group("/projects", a.authRequired())
	projects.POST("/", a.createProject)
	projects.GET("/", a.listProjects)
	projects.GET("/:project_id", a.getProject)
	projects.PUT("/:project_id", a.updateProject)
	projects.POST("/:project_id/billing-reservations/release", a.releaseProjectBillingReservations)
	projects.GET("/:project_id/workflow", a.getProjectWorkflow)
	projects.PUT("/:project_id/workflow/steps/:step", a.putProjectWorkflowStepParam)
	projects.DELETE("/:project_id", a.deleteProject)
	projects.GET("/:project_id/subtitle", a.projectJSONField("subtitle"))
	projects.GET("/:project_id/video_context", a.projectJSONField("video_context"))
	projects.GET("/:project_id/video_config", a.projectJSONField("video_config"))
	projects.GET("/:project_id/ai-script", a.projectJSONField("ai_analysis_script"))

	workflowRuns := v1.Group("/workflow-runs", a.authRequired())
	workflowRuns.POST("/", a.createWorkflowRun)
	workflowRuns.POST("/pc/project-names/sync", a.syncPCWorkflowProjectNames)
	workflowRuns.GET("/:run_id", a.getWorkflowRun)
	workflowRuns.GET("/:run_id/steps/:step_id/result", a.getMovieHarnessResult)
	workflowRuns.POST("/:run_id/heartbeat", a.heartbeatWorkflowRun)
	workflowRuns.POST("/:run_id/resume", a.resumeWorkflowRun)
	workflowRuns.POST("/:run_id/interrupt", a.interruptWorkflowRun)
	workflowRuns.POST("/:run_id/complete", a.completeWorkflowRun)
	workflowRuns.POST("/:run_id/cancel", a.cancelWorkflowRun)
	workflowRuns.PUT("/:run_id/steps/:step_id", a.putWorkflowStepExecution)

	tokens := v1.Group("/token-accounts", a.authRequired())
	tokens.GET("/balance", a.tokenBalance)
	tokens.POST("/recharge", a.createRecharge)
	tokens.GET("/recharge/:out_trade_no/status", a.rechargeStatus)
	tokens.GET("/usage/stats", a.usageStats)
	tokens.GET("/usage/records", a.usageRecords)
	tokens.GET("/recharge/records", a.rechargeRecords)
	tokens.GET("/ledger/records", a.creditLedgerRecords)
	tokens.GET("/ledger/records/:record_id", a.creditLedgerRecordDetail)
	v1.POST("/token-accounts/webhook", a.wechatWebhook)
	v1.POST("/subscriptions/payments/wechat/webhook", a.wechatWebhook)

	credits := v1.Group("/credit-accounts", a.authRequired())
	credits.GET("/balance", a.tokenBalance)
	credits.POST("/recharge", a.createRecharge)
	credits.POST("/recharge/creem", a.createCreemCreditRecharge)
	credits.GET("/recharge/:out_trade_no/status", a.rechargeStatus)
	credits.GET("/usage/stats", a.usageStats)
	credits.GET("/usage/records", a.usageRecords)
	credits.POST("/usage/movie-project/:project_id", a.movieProjectCredits)
	credits.GET("/recharge/records", a.rechargeRecords)
	credits.GET("/ledger/records", a.creditLedgerRecords)
	credits.GET("/ledger/records/:record_id", a.creditLedgerRecordDetail)
	v1.POST("/credit-accounts/webhook", a.wechatWebhook)

	creditPayments := v1.Group("/credit-payments", a.authRequired())
	creditPayments.POST("", a.createCreditPayment)
	creditPayments.GET("/:out_trade_no", a.creditPaymentStatus)
	creditPayments.POST("/:out_trade_no/refund", a.adminRequired(), a.refundCreditPayment)
	v1.POST("/credit-payments/wechat/webhook", a.wechatCreditPaymentWebhook)
	v1.POST("/credit-payments/alipay/webhook", a.alipayCreditPaymentWebhook)

	subscriptions := v1.Group("/subscriptions")
	subscriptions.GET("/health", a.subscriptionHealth)
	subscriptions.GET("/admin/plans", a.authRequired(), a.adminRequired(), a.listSubscriptionPlansAdmin)
	subscriptions.GET("/plans", a.listSubscriptionPlans)
	subscriptions.GET("/plans/:plan_id", a.getSubscriptionPlan)
	subscriptions.POST("/plans", a.authRequired(), a.adminRequired(), a.createSubscriptionPlan)
	subscriptions.PUT("/plans/:plan_id", a.authRequired(), a.adminRequired(), a.updateSubscriptionPlan)
	subscriptions.DELETE("/plans/:plan_id", a.authRequired(), a.adminRequired(), a.deleteSubscriptionPlan)
	subscriptions.GET("/current", a.authRequired(), a.currentSubscription)
	subscriptions.POST("/initialize", a.authRequired(), a.initializeSubscription)
	subscriptions.POST("/upgrade", a.authRequired(), a.upgradeSubscription)
	subscriptions.POST("/cancel", a.authRequired(), a.cancelSubscription)
	subscriptions.POST("/checkout", a.authRequired(), a.createSubscriptionCheckout)
	subscriptions.POST("/alipay/checkout", a.authRequired(), a.createAlipaySubscriptionCheckout)
	subscriptions.POST("/checkout/sync", a.authRequired(), a.syncCreemCheckout)
	subscriptions.POST("/portal", a.authRequired(), a.createSubscriptionPortal)
	subscriptions.POST("/alipay/sync", a.authRequired(), a.syncAlipaySubscription)
	subscriptions.POST("/alipay/portal", a.authRequired(), a.createAlipaySubscriptionPortal)
	subscriptions.POST("/alipay/cancel", a.authRequired(), a.cancelAlipaySubscription)
	subscriptions.POST("/alipay/webhook", a.alipaySubscriptionWebhook)
	subscriptions.GET("/alipay/return", a.alipaySubscriptionReturn)
	subscriptions.GET("/stats", a.authRequired(), a.adminRequired(), a.subscriptionStats)
	subscriptions.POST("/tasks/reset-tokens", a.authRequired(), a.adminRequired(), a.resetMonthlyTokens)
	subscriptions.POST("/tasks/handle-expired", a.authRequired(), a.adminRequired(), a.handleExpiredSubscriptions)
	subscriptions.POST("/tokens/consume", a.authRequired(), a.consumeSubscriptionTokens)
	subscriptions.GET("/debug/user/:user_id", a.authRequired(), a.adminRequired(), a.debugUserSubscription)
	subscriptions.POST("/payments/:out_trade_no/refund", a.authRequired(), a.adminRequired(), a.refundSubscriptionPayment)
	subscriptions.POST("/payments/:out_trade_no/close", a.authRequired(), a.adminRequired(), a.closeSubscriptionPayment)

	v1.POST("/billing/creem/webhook", a.creemWebhook)
	v1.POST("/billing/creem/checkout/sync", a.authRequired(), a.syncCreemCheckout)

	packages := v1.Group("/token-packages")
	packages.GET("/admin/packages", a.authRequired(), a.adminRequired(), a.listTokenPackagesAdmin)
	packages.GET("/packages", a.listTokenPackages)
	packages.GET("/packages/:package_id", a.authRequired(), a.getTokenPackage)
	packages.POST("/packages", a.authRequired(), a.adminRequired(), a.createTokenPackage)
	packages.PUT("/packages/:package_id", a.authRequired(), a.adminRequired(), a.updateTokenPackage)
	packages.DELETE("/packages/:package_id", a.authRequired(), a.adminRequired(), a.deleteTokenPackage)

	creditPackages := v1.Group("/credit-packages")
	creditPackages.GET("/admin/packages", a.authRequired(), a.adminRequired(), a.listTokenPackagesAdmin)
	creditPackages.GET("/packages", a.listTokenPackages)
	creditPackages.GET("/packages/:package_id", a.authRequired(), a.getTokenPackage)
	creditPackages.POST("/packages", a.authRequired(), a.adminRequired(), a.createTokenPackage)
	creditPackages.PUT("/packages/:package_id", a.authRequired(), a.adminRequired(), a.updateTokenPackage)
	creditPackages.DELETE("/packages/:package_id", a.authRequired(), a.adminRequired(), a.deleteTokenPackage)

	system := v1.Group("/system")
	system.GET("/settings/public", a.publicSystemSettings)
	system.POST("/settings", a.authRequired(), a.adminRequired(), a.createSystemSetting)
	system.GET("/settings", a.authRequired(), a.adminRequired(), a.listSystemSettings)
	system.GET("/settings/:key", a.authRequired(), a.adminRequired(), a.getSystemSetting)
	system.PUT("/settings/:key", a.authRequired(), a.adminRequired(), a.updateSystemSetting)
	system.DELETE("/settings/:key", a.authRequired(), a.adminRequired(), a.deleteSystemSetting)
	system.POST("/notifications", a.authRequired(), a.adminRequired(), a.createNotification)
	system.POST("/notifications/batch", a.authRequired(), a.adminRequired(), a.createNotificationsBatch)
	system.GET("/notifications", a.authRequired(), a.listNotifications)
	system.PUT("/notifications/read-all", a.authRequired(), a.markAllNotificationsRead)
	system.GET("/notifications/stats", a.authRequired(), a.notificationStats)
	system.PUT("/notifications/:notification_id/read", a.authRequired(), a.markNotificationRead)
	system.DELETE("/notifications/:notification_id", a.authRequired(), a.deleteNotification)
	system.POST("/maintenance/cleanup-expired", a.authRequired(), a.adminRequired(), a.cleanupExpiredSystemData)

	tools := v1.Group("/tools")
	tools.GET("/prompt-templates", a.promptTemplates)
	tools.POST("/analyze-images", a.authRequired(), a.analyzeImages)
	tools.POST("/ocr-images", a.authRequired(), a.ocrImages)
	tools.POST("/upload-reference-voice", a.authRequired(), a.referenceVoice)
	tools.POST("/text-to-speech-with-uri", a.authRequired(), a.textToSpeech)
	tools.POST("/elevenlabs/scribe/transcribe", a.authRequired(), a.elevenLabsScribeTranscribe)
	tools.GET("/elevenlabs/scribe/transcribe/:transcription_id", a.authRequired(), a.elevenLabsScribeTranscript)
	tools.POST("/timeline-view/decide", a.authRequired(), a.timelineViewDecide)
	tools.POST("/timeline-view/analyze", a.authRequired(), a.timelineViewAnalyze)
	tools.GET("/movie/vision/capability", a.authRequired(), a.movieVisionCapability)
	tools.POST("/movie/vision/analyze", a.authRequired(), a.streamTool("movie_vision"))
	tools.POST("/frame-workflow/subtitles/validate", a.authRequired(), a.frameWorkflowSubtitleValidate)
	tools.POST("/frame-workflow/narrative-analyze", a.authRequired(), a.frameWorkflowNarrativeAnalyze)
	tools.POST("/frame-workflow/timeline-decisions/review", a.authRequired(), a.frameWorkflowTimelineDecisionReview)
	tools.POST("/frame-workflow/narrative-understanding/review", a.authRequired(), a.frameWorkflowNarrativeReview)
	tools.POST("/story-context/versions", a.authRequired(), a.searchStoryVersions)
	tools.POST("/story-context/search", a.authRequired(), a.searchStoryContext)
	tools.GET("/media-proxy", a.authRequired(), a.mediaProxy)
	tools.DELETE("/voice/*voice_uri", a.authRequired(), a.deleteVoice)
	tools.POST("/generate-script-stream", a.authRequired(), a.streamTool("script"))
	tools.POST("/generate-narration-draft-stream", a.authRequired(), a.streamTool("script_draft"))
	tools.POST("/match-narration-visuals-stream", a.authRequired(), a.streamTool("script_match"))
	qwen3 := tools.Group("/qwen3", a.authRequired())
	qwen3.POST("/asr/submit", a.submitVendorASR("qwen3"))
	qwen3.GET("/asr/query/:task_id", a.queryVendorASR("qwen3"))
	dashscope := tools.Group("/dashscope", a.authRequired())
	dashscope.POST("/temp-files/upload-policy", a.dashscopeUploadPolicy)
	funasr := tools.Group("/funasr", a.authRequired())
	funasr.POST("/asr/submit", a.submitVendorASR("funasr"))
	funasr.GET("/asr/query/:task_id", a.queryVendorASR("funasr"))
	dubbing := tools.Group("/dubbing", a.authRequired())
	dubbing.GET("/capabilities", a.dubbingCapabilities)
	tools.GET("/billing/modes", a.authRequired(), a.billingModeAvailability)
	dubbing.POST("/asr/submit", a.submitVendorASR("paraformer"))
	dubbing.POST("/tts", a.textToSpeech)
	dubbing.GET("/asr/query/:task_id", a.queryVendorASR("paraformer"))
	dubbing.POST("/separation/submit", a.submitDubbingSeparation)
	dubbing.GET("/separation/query/:task_id", a.queryDubbingSeparation)

	admin := v1.Group("/admin", a.authRequired(), a.adminRequired())
	a.registerModelLab(admin, v1.Group("/avatar-mvp", a.authRequired()))
	admin.GET("/tts/qwen/voices", a.adminQwenVoices)
	admin.PUT("/tts/qwen/voices", a.adminSaveQwenVoices)
	admin.POST("/tts/qwen/preview", a.adminPreviewQwenVoice)
	admin.GET("/tts/providers", a.adminTTSProviders)
	admin.Any("/tts/siliconflow/*path", a.adminSiliconFlowProxy)
	admin.Any("/tts/fishaudio/*path", a.adminFishAudioProxy)
	admin.GET("/billing/models", a.adminBillingModels)
	admin.GET("/users", a.adminListUsers)
	admin.GET("/users/:user_id", a.adminUserDetail)
	admin.POST("/users/:user_id/tokens", a.adminAdjustTokens)
	admin.POST("/users/:user_id/credits", a.adminAdjustTokens)
	admin.PUT("/users/:user_id/status", a.adminUserStatus)
	admin.PUT("/users/:user_id/role", a.adminUserRole)
	admin.DELETE("/users/:user_id", a.adminDeleteUser)
	admin.GET("/notifications/announcements", a.listAdminAnnouncements)
	admin.PUT("/notifications/announcements/:announcement_id", a.updateAdminAnnouncement)
	admin.DELETE("/notifications/announcements/:announcement_id", a.deleteAdminAnnouncement)
	admin.POST("/notifications/broadcast", a.broadcastAdminNotification)
	admin.GET("/statistics/overview", a.adminOverview)
	admin.GET("/statistics/users/ranking", a.adminUserRanking)
	admin.GET("/statistics/tokens/trends", a.adminTokenTrends)
	admin.GET("/statistics/models/usage", a.adminModelUsage)
	admin.GET("/statistics/apis/usage", a.adminAPIUsage)

	a.registerOpenAPI()
}

func (a *App) health(c *gin.Context) {
	ok(c, gin.H{
		"status":     "healthy",
		"service":    "narratoai-backend-go",
		"version":    "0.1.0",
		"git_commit": "unknown",
		"git_branch": "unknown",
		"build_date": time.Now().UTC().Format(time.RFC3339),
	})
}

func ok(c *gin.Context, body any) { c.JSON(http.StatusOK, body) }

func created(c *gin.Context, body any) { c.JSON(http.StatusCreated, body) }

func fail(c *gin.Context, status int, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": http.StatusText(status), "message": message, "success": false})
}

func bindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

func jsonMap(v any) datatypes.JSON {
	if v == nil {
		return emptyJSON()
	}
	data, _ := json.Marshal(v)
	return datatypes.JSON(data)
}

func emptyJSON() datatypes.JSON { return datatypes.JSON([]byte("{}")) }

func getUser(c *gin.Context) models.User {
	user, _ := c.Get("user")
	return user.(models.User)
}

func parseIDParam(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		fail(c, http.StatusBadRequest, "无效的 ID")
		return uuid.Nil, false
	}
	return id, true
}

func pageLimit(c *gin.Context) (int, int) {
	page := queryInt(c, "page", 1)
	limit := queryInt(c, "limit", queryInt(c, "page_size", queryInt(c, "per_page", 20)))
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return page, limit
}

func queryInt(c *gin.Context, key string, fallback int) int {
	var value int
	if raw := strings.TrimSpace(c.Query(key)); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &value); err == nil {
			return value
		}
	}
	return fallback
}

func randomCode(prefix string) string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return strings.ToUpper(prefix + hex.EncodeToString(buf))
}

func generatedResourceName(prefix string) string {
	return prefix + "-" + uuid.NewString()
}

func deletedAtValue(deletedAt gorm.DeletedAt) any {
	if !deletedAt.Valid {
		return nil
	}
	return deletedAt.Time
}

func (a *App) authRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
			fail(c, http.StatusUnauthorized, "缺少 Bearer Token")
			return
		}

		_, user, valid := a.parseToken(strings.TrimSpace(header[7:]), "access")
		if !valid {
			fail(c, http.StatusUnauthorized, "无效的访问令牌")
			return
		}
		c.Set("user", user)
		// 同时暴露字符串形式的用户 ID：跨包（如 agent）不便断言 models.User，
		// 类型断言失败会静默拿到零值，导致越权读取。
		c.Set("userID", user.ID.String())
		c.Next()
	}
}

func (a *App) parseToken(raw, expectedType string) (map[string]any, models.User, bool) {
	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) {
		return []byte(a.cfg.SecretKey), nil
	})
	if err != nil || !token.Valid || claims["type"] != expectedType {
		return nil, models.User{}, false
	}
	userID, err := uuid.Parse(fmt.Sprint(claims["sub"]))
	if err != nil {
		return nil, models.User{}, false
	}
	jti := strings.TrimSpace(fmt.Sprint(claims["jti"]))
	if jti == "" || jti == "<nil>" {
		return nil, models.User{}, false
	}
	revoked, err := a.tokenRevoked(jti)
	if err != nil || revoked {
		return nil, models.User{}, false
	}
	user, err := a.activeUser(userID)
	if err != nil {
		return nil, models.User{}, false
	}
	result := make(map[string]any, len(claims))
	for key, value := range claims {
		result[key] = value
	}
	return result, user, true
}

func (a *App) tokenRevoked(jti string) (bool, error) {
	var count int64
	err := a.db.Model(&models.RevokedAuthToken{}).
		Where("jti = ? AND expires_at > ?", jti, time.Now().UTC()).
		Count(&count).Error
	return count > 0, err
}

func (a *App) activeUser(userID uuid.UUID) (models.User, error) {
	if value, ok := a.authUserCache.Load(userID); ok {
		cached, ok := value.(cachedAuthUser)
		if ok && time.Now().Before(cached.expiresAt) {
			return cached.user, nil
		}
		a.authUserCache.Delete(userID)
	}

	return a.activeUserFromDB(userID)
}

func (a *App) activeUserFromDB(userID uuid.UUID) (models.User, error) {
	var user models.User
	if err := a.db.Take(&user, "id = ? AND is_active = ?", userID, true).Error; err != nil {
		return models.User{}, err
	}
	a.cacheActiveUser(user)
	return user, nil
}

func (a *App) cacheActiveUser(user models.User) {
	if user.ID == uuid.Nil || !user.IsActive {
		return
	}
	a.authUserCache.Store(user.ID, cachedAuthUser{
		user:      user,
		expiresAt: time.Now().Add(authUserCacheTTL),
	})
}

func (a *App) invalidateAuthUser(userID uuid.UUID) {
	if userID != uuid.Nil {
		a.authUserCache.Delete(userID)
	}
}

func (a *App) adminRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		user := getUser(c)
		if !user.IsAdmin {
			if fresh, err := a.activeUserFromDB(user.ID); err == nil {
				user = fresh
				c.Set("user", user)
			}
		}
		if !user.IsAdmin {
			fail(c, http.StatusForbidden, "需要管理员权限")
			return
		}
		c.Next()
	}
}

func (a *App) makeTokens(user models.User) (gin.H, error) {
	access, err := a.makeToken(user, "access", a.cfg.AccessTTL)
	if err != nil {
		return nil, err
	}
	refresh, err := a.makeToken(user, "refresh", a.cfg.RefreshTTL)
	if err != nil {
		return nil, err
	}
	return gin.H{"access_token": access, "refresh_token": refresh, "token_type": "bearer", "expires_in": int(a.cfg.AccessTTL.Seconds())}, nil
}

func (a *App) makeToken(user models.User, typ string, ttl time.Duration) (string, error) {
	now := time.Now()
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":      user.ID.String(),
		"username": user.Username,
		"email":    user.Email,
		"type":     typ,
		"jti":      uuid.NewString(),
		"iat":      now.Unix(),
		"exp":      now.Add(ttl).Unix(),
	}).SignedString([]byte(a.cfg.SecretKey))
}

func (a *App) ensureTokenAccount(userID uuid.UUID) (models.UserTokenAccount, error) {
	return ensureTokenAccountWithDB(a.db, userID)
}

func ensureTokenAccountWithDB(db *gorm.DB, userID uuid.UUID) (models.UserTokenAccount, error) {
	var account models.UserTokenAccount
	err := db.First(&account, "user_id = ?", userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		account = models.UserTokenAccount{UserID: userID}
		err = db.Create(&account).Error
	}
	return account, err
}

func (a *App) ensureProfile(userID uuid.UUID) (models.UserProfile, error) {
	return ensureProfileWithDB(a.db, userID)
}

func ensureProfileWithDB(db *gorm.DB, userID uuid.UUID) (models.UserProfile, error) {
	var profile models.UserProfile
	err := db.First(&profile, "user_id = ?", userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		profile = models.UserProfile{
			UserID:               userID,
			Timezone:             "Asia/Shanghai",
			Language:             "zh-CN",
			Preferences:          jsonMap(gin.H{"theme": "light", "auto_save": true}),
			NotificationSettings: jsonMap(gin.H{"email_notifications": true}),
		}
		err = db.Create(&profile).Error
	}
	return profile, err
}

func publicURL(domain, key string) string {
	if domain == "" {
		return ""
	}
	return strings.TrimRight(domain, "/") + "/" + strings.TrimLeft(key, "/")
}

func uploadURL(c *gin.Context, key string) string {
	scheme := c.GetHeader("X-Forwarded-Proto")
	if scheme == "" {
		if c.Request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := c.GetHeader("X-Forwarded-Host")
	if host == "" {
		host = c.Request.Host
	}
	return fmt.Sprintf("%s://%s/uploads/%s", scheme, host, strings.TrimLeft(key, "/"))
}
