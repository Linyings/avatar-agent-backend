package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type Base struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

func (b *Base) BeforeCreate(*gorm.DB) error {
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	return nil
}

type User struct {
	Base
	Username            string     `gorm:"size:50;uniqueIndex:uq_users_username;not null" json:"username"`
	Email               string     `gorm:"size:255;uniqueIndex:uq_users_email;not null" json:"email"`
	DisplayName         *string    `gorm:"size:100" json:"display_name"`
	AvatarURL           *string    `json:"avatar_url"`
	IsActive            bool       `gorm:"default:true" json:"is_active"`
	IsVerified          bool       `gorm:"default:false" json:"is_verified"`
	IsAdmin             bool       `gorm:"default:false" json:"is_admin"`
	PasswordHash        string     `gorm:"size:255;not null" json:"-"`
	PasswordAuthEnabled bool       `gorm:"default:true" json:"password_auth_enabled"`
	EmailVerifiedAt     *time.Time `json:"email_verified_at"`
	LastLoginAt         *time.Time `json:"last_login_at"`
}

func (User) TableName() string { return "users" }

type AuthEmailCode struct {
	Base
	Email      string     `gorm:"size:255;index:idx_auth_email_codes_email_purpose_created,priority:1;not null" json:"email"`
	Purpose    string     `gorm:"size:32;index:idx_auth_email_codes_email_purpose_created,priority:2;not null" json:"purpose"`
	CodeHash   string     `gorm:"size:128;not null" json:"-"`
	Attempts   int        `gorm:"default:0;not null" json:"attempts"`
	ExpiresAt  time.Time  `gorm:"index;not null" json:"expires_at"`
	ConsumedAt *time.Time `gorm:"index" json:"consumed_at"`
}

func (AuthEmailCode) TableName() string { return "auth_email_codes" }

type OAuthAccount struct {
	Base
	UserID                uuid.UUID  `gorm:"type:uuid;index;not null" json:"user_id"`
	Provider              string     `gorm:"size:32;uniqueIndex:uq_oauth_accounts_provider_user;not null" json:"provider"`
	ProviderUserID        string     `gorm:"size:128;uniqueIndex:uq_oauth_accounts_provider_user;not null" json:"provider_user_id"`
	ProviderEmail         string     `gorm:"size:255;index" json:"provider_email"`
	ProviderEmailVerified bool       `gorm:"default:false" json:"provider_email_verified"`
	LinkedAt              time.Time  `gorm:"not null" json:"linked_at"`
	LastLoginAt           *time.Time `json:"last_login_at"`
}

func (OAuthAccount) TableName() string { return "oauth_accounts" }

type OAuthState struct {
	Base
	Provider        string     `gorm:"size:32;index;not null" json:"provider"`
	StateHash       string     `gorm:"size:128;uniqueIndex;not null" json:"-"`
	CodeVerifier    string     `gorm:"size:128;not null" json:"-"`
	RedirectPath    string     `gorm:"size:1024" json:"redirect_path"`
	FrontendBaseURL string     `gorm:"size:1024" json:"frontend_base_url"`
	ExpiresAt       time.Time  `gorm:"index;not null" json:"expires_at"`
	ConsumedAt      *time.Time `gorm:"index" json:"consumed_at"`
}

func (OAuthState) TableName() string { return "oauth_states" }

type OAuthLoginTicket struct {
	Base
	TicketHash   string     `gorm:"size:128;uniqueIndex;not null" json:"-"`
	UserID       uuid.UUID  `gorm:"type:uuid;index;not null" json:"user_id"`
	Provider     string     `gorm:"size:32;index;not null" json:"provider"`
	RedirectPath string     `gorm:"size:1024" json:"redirect_path"`
	ExpiresAt    time.Time  `gorm:"index;not null" json:"expires_at"`
	ConsumedAt   *time.Time `gorm:"index" json:"consumed_at"`
}

func (OAuthLoginTicket) TableName() string { return "oauth_login_tickets" }

type UserProfile struct {
	UserID               uuid.UUID      `gorm:"type:uuid;primaryKey" json:"user_id"`
	Timezone             string         `gorm:"size:50;default:Asia/Shanghai" json:"timezone"`
	Language             string         `gorm:"size:10;default:zh-CN" json:"language"`
	Preferences          datatypes.JSON `json:"preferences"`
	NotificationSettings datatypes.JSON `json:"notification_settings"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
}

func (UserProfile) TableName() string { return "user_profiles" }

type Project struct {
	Base
	Name                     string         `gorm:"size:200;not null" json:"name"`
	Description              *string        `json:"description"`
	ThumbnailURL             *string        `json:"thumbnail_url"`
	VideoURL                 *string        `json:"-"`
	OwnerID                  uuid.UUID      `gorm:"type:uuid;index;not null" json:"owner_id"`
	Status                   string         `gorm:"size:20;default:draft;index" json:"status"`
	CurrentStep              *string        `gorm:"size:50;index" json:"-"`
	WorkflowMigratedAt       *time.Time     `json:"-"`
	WorkflowSchemaVersion    *int           `gorm:"default:1" json:"-"`
	Workflow                 datatypes.JSON `gorm:"column:workflow" json:"workflow"`
	Artifacts                datatypes.JSON `gorm:"column:artifacts" json:"artifacts"`
	Subtitle                 datatypes.JSON `json:"-"`
	VideoContext             datatypes.JSON `json:"-"`
	VideoConfig              datatypes.JSON `json:"-"`
	AIAnalysisScript         datatypes.JSON `json:"-"`
	ProjectMetadata          datatypes.JSON `gorm:"column:project_metadata" json:"metadata"`
	PreviewImageURL          *string        `json:"-"`
	LocalPreviewImageKey     *string        `gorm:"size:255" json:"-"`
	PreviewImageStorage      *string        `gorm:"size:20" json:"-"`
	LocalPreviewImageMissing *bool          `json:"-"`
	AudioURL                 *string        `json:"-"`
}

func (Project) TableName() string { return "projects" }

type ProjectWorkflow struct {
	Base
	ProjectID     uuid.UUID  `gorm:"type:uuid;uniqueIndex;not null" json:"project_id"`
	CurrentStep   *string    `gorm:"size:50;index" json:"current_step"`
	Revision      int        `gorm:"default:1;not null;index" json:"revision"`
	Status        string     `gorm:"size:20;default:active;index" json:"status"`
	FailureReason *string    `json:"failure_reason"`
	LastStepType  *string    `gorm:"size:50;index" json:"last_step_type"`
	LastStepID    *uuid.UUID `gorm:"type:uuid;index" json:"last_step_id"`
}

func (ProjectWorkflow) TableName() string { return "project_workflows" }

type ProjectWorkflowEvent struct {
	Base
	ProjectID        uuid.UUID      `gorm:"type:uuid;index;not null" json:"project_id"`
	WorkflowID       *uuid.UUID     `gorm:"type:uuid;index" json:"workflow_id"`
	StepType         *string        `gorm:"size:50;index" json:"step_type"`
	StepID           *uuid.UUID     `gorm:"type:uuid;index" json:"step_id"`
	EventType        string         `gorm:"size:80;index;not null" json:"event_type"`
	WorkflowRevision *int           `gorm:"index" json:"workflow_revision"`
	ActorType        string         `gorm:"size:30;default:system;index" json:"actor_type"`
	Message          *string        `json:"message"`
	Metadata         datatypes.JSON `json:"metadata"`
}

func (ProjectWorkflowEvent) TableName() string { return "project_workflow_events" }

type ProjectVideoSource struct {
	Base
	ProjectID        uuid.UUID      `gorm:"type:uuid;index;not null" json:"project_id"`
	UploadID         *uuid.UUID     `gorm:"type:uuid;index" json:"upload_id"`
	OriginalFilename string         `gorm:"size:255;not null" json:"original_filename"`
	VideoURL         *string        `gorm:"size:2048" json:"video_url"`
	StorageKey       *string        `gorm:"size:1024;index" json:"storage_key"`
	ContentType      *string        `gorm:"size:127" json:"content_type"`
	FileSize         *int64         `json:"file_size"`
	DurationMs       *int64         `json:"duration_ms"`
	IsTrimmed        bool           `gorm:"default:false" json:"is_trimmed"`
	TrimStartMs      *int64         `json:"trim_start_ms"`
	TrimEndMs        *int64         `json:"trim_end_ms"`
	ASREngine        *string        `gorm:"size:50" json:"asr_engine"`
	FrontendParams   datatypes.JSON `json:"frontend_params"`
	RawSnapshot      datatypes.JSON `json:"raw_snapshot"`
	InputHash        *string        `gorm:"size:64;index" json:"input_hash"`
	DependencyHash   *string        `gorm:"size:64;index" json:"dependency_hash"`
	ContentHash      *string        `gorm:"size:64;index" json:"content_hash"`
	Status           string         `gorm:"size:20;default:active;index" json:"status"`
	WorkflowRevision int            `gorm:"default:1;not null" json:"workflow_revision"`
	ClearedAt        *time.Time     `gorm:"index" json:"cleared_at"`
	ClearedReason    *string        `json:"cleared_reason"`
}

func (ProjectVideoSource) TableName() string { return "project_video_sources" }

type ProjectSubtitle struct {
	Base
	ProjectID        uuid.UUID      `gorm:"type:uuid;index;not null" json:"project_id"`
	VideoSourceID    *uuid.UUID     `gorm:"type:uuid;index" json:"video_source_id"`
	ASREngine        *string        `gorm:"size:50" json:"asr_engine"`
	Language         *string        `gorm:"size:20" json:"language"`
	SubtitleFormat   *string        `gorm:"size:20" json:"subtitle_format"`
	Content          datatypes.JSON `json:"content"`
	RawResult        datatypes.JSON `json:"raw_result"`
	RawSnapshot      datatypes.JSON `json:"raw_snapshot"`
	InputHash        *string        `gorm:"size:64;index" json:"input_hash"`
	DependencyHash   *string        `gorm:"size:64;index" json:"dependency_hash"`
	ContentHash      *string        `gorm:"size:64;index" json:"content_hash"`
	Status           string         `gorm:"size:20;default:active;index" json:"status"`
	WorkflowRevision int            `gorm:"default:1;not null" json:"workflow_revision"`
	ClearedAt        *time.Time     `gorm:"index" json:"cleared_at"`
	ClearedReason    *string        `json:"cleared_reason"`
}

func (ProjectSubtitle) TableName() string { return "project_subtitles" }

type ProjectGenerationConfig struct {
	Base
	ProjectID        uuid.UUID      `gorm:"type:uuid;index;not null" json:"project_id"`
	SubtitleID       *uuid.UUID     `gorm:"type:uuid;index" json:"subtitle_id"`
	Style            *string        `gorm:"size:50;index" json:"style"`
	Language         *string        `gorm:"size:20;index" json:"language"`
	ClipMode         *string        `gorm:"size:50;index" json:"clip_mode"`
	OriginalRatio    *string        `gorm:"size:20" json:"original_ratio"`
	NarrationRatio   *string        `gorm:"size:20" json:"narration_ratio"`
	VoiceProvider    *string        `gorm:"size:50;index" json:"voice_provider"`
	VoiceID          *string        `gorm:"size:100;index" json:"voice_id"`
	RawParams        datatypes.JSON `json:"raw_params"`
	RawSnapshot      datatypes.JSON `json:"raw_snapshot"`
	InputHash        *string        `gorm:"size:64;index" json:"input_hash"`
	DependencyHash   *string        `gorm:"size:64;index" json:"dependency_hash"`
	ParamsHash       *string        `gorm:"size:64;index" json:"params_hash"`
	Status           string         `gorm:"size:20;default:active;index" json:"status"`
	WorkflowRevision int            `gorm:"default:1;not null" json:"workflow_revision"`
	ClearedAt        *time.Time     `gorm:"index" json:"cleared_at"`
	ClearedReason    *string        `json:"cleared_reason"`
}

func (ProjectGenerationConfig) TableName() string { return "project_generation_configs" }

type ProjectScript struct {
	Base
	ProjectID        uuid.UUID      `gorm:"type:uuid;index;not null" json:"project_id"`
	ConfigID         *uuid.UUID     `gorm:"type:uuid;index" json:"config_id"`
	ScriptContent    datatypes.JSON `json:"script_content"`
	ModelName        *string        `gorm:"size:100;index" json:"model_name"`
	RawResult        datatypes.JSON `json:"raw_result"`
	RawSnapshot      datatypes.JSON `json:"raw_snapshot"`
	InputHash        *string        `gorm:"size:64;index" json:"input_hash"`
	DependencyHash   *string        `gorm:"size:64;index" json:"dependency_hash"`
	ScriptHash       *string        `gorm:"size:64;index" json:"script_hash"`
	Status           string         `gorm:"size:20;default:active;index" json:"status"`
	WorkflowRevision int            `gorm:"default:1;not null" json:"workflow_revision"`
	ClearedAt        *time.Time     `gorm:"index" json:"cleared_at"`
	ClearedReason    *string        `json:"cleared_reason"`
}

func (ProjectScript) TableName() string { return "project_scripts" }

type ProjectVoiceover struct {
	Base
	ProjectID        uuid.UUID      `gorm:"type:uuid;index;not null" json:"project_id"`
	ScriptID         *uuid.UUID     `gorm:"type:uuid;index" json:"script_id"`
	UploadID         *uuid.UUID     `gorm:"type:uuid;index" json:"upload_id"`
	VoiceProvider    *string        `gorm:"size:50;index" json:"voice_provider"`
	VoiceID          *string        `gorm:"size:100;index" json:"voice_id"`
	AudioURL         *string        `gorm:"size:2048" json:"audio_url"`
	StorageKey       *string        `gorm:"size:1024;index" json:"storage_key"`
	Segments         datatypes.JSON `json:"segments"`
	Params           datatypes.JSON `json:"params"`
	RawSnapshot      datatypes.JSON `json:"raw_snapshot"`
	InputHash        *string        `gorm:"size:64;index" json:"input_hash"`
	DependencyHash   *string        `gorm:"size:64;index" json:"dependency_hash"`
	ContentHash      *string        `gorm:"size:64;index" json:"content_hash"`
	Status           string         `gorm:"size:20;default:active;index" json:"status"`
	WorkflowRevision int            `gorm:"default:1;not null" json:"workflow_revision"`
	ClearedAt        *time.Time     `gorm:"index" json:"cleared_at"`
	ClearedReason    *string        `json:"cleared_reason"`
}

func (ProjectVoiceover) TableName() string { return "project_voiceovers" }

type TokenPackage struct {
	Base
	Name                    string  `gorm:"size:50;uniqueIndex;not null" json:"name"`
	DisplayName             string  `gorm:"size:100;not null" json:"display_name"`
	Description             *string `json:"description"`
	TokenAmount             int64   `gorm:"not null" json:"token_amount"`
	Price                   float64 `gorm:"type:decimal(10,2);not null" json:"price"`
	BonusTokens             int64   `gorm:"default:0" json:"bonus_tokens"`
	CreemProductID          *string `gorm:"size:64;index" json:"creem_product_id"`
	CreemEnabled            bool    `gorm:"default:false;index" json:"creem_enabled"`
	CreemCurrency           string  `gorm:"size:3;default:USD" json:"creem_currency"`
	Currency                string  `gorm:"size:3;not null;default:CNY" json:"currency"`
	ValidityHours           int     `gorm:"not null;default:720;check:chk_token_packages_validity_hours,validity_hours = 720" json:"validity_hours"`
	IsTrial                 bool    `gorm:"not null;default:false;index" json:"is_trial"`
	PerAccountPurchaseLimit int     `gorm:"not null;default:0" json:"per_account_purchase_limit"`
	IsActive                bool    `gorm:"default:true;index" json:"is_active"`
	IsFeatured              bool    `gorm:"default:false" json:"is_featured"`
	SortOrder               int     `gorm:"default:0;index" json:"sort_order"`
}

func (TokenPackage) TableName() string { return "token_packages" }

type CreditPaymentOrder struct {
	Base
	UserID                uuid.UUID       `gorm:"type:uuid;not null;index" json:"user_id"`
	PackageID             uuid.UUID       `gorm:"type:uuid;not null;index" json:"package_id"`
	Provider              string          `gorm:"size:20;not null;index" json:"provider"`
	OutTradeNo            string          `gorm:"size:64;not null;uniqueIndex:uq_credit_payment_orders_out_trade_no" json:"out_trade_no"`
	ProviderTransactionID string          `gorm:"size:128;index" json:"provider_transaction_id"`
	Status                string          `gorm:"size:20;not null;default:pending;index" json:"status"`
	AmountCNY             decimal.Decimal `gorm:"type:decimal(10,2);not null" json:"amount_cny"`
	Credits               int64           `gorm:"not null" json:"credits"`
	PackageName           string          `gorm:"size:100;not null" json:"package_name"`
	IsTrial               bool            `gorm:"not null;default:false" json:"is_trial"`
	CodeURL               string          `gorm:"type:text" json:"code_url"`
	PaidAt                *time.Time      `json:"paid_at"`
	ExpiresAt             *time.Time      `json:"expires_at"`
	RefundedAt            *time.Time      `json:"refunded_at"`
	ProviderPayload       datatypes.JSON  `json:"provider_payload"`
}

func (CreditPaymentOrder) TableName() string { return "credit_payment_orders" }

type PurchasedCreditLot struct {
	Base
	UserID           uuid.UUID  `gorm:"type:uuid;not null;index:idx_credit_lots_spend,priority:1" json:"user_id"`
	PaymentOrderID   uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex:uq_purchased_credit_lots_order_id" json:"payment_order_id"`
	GrantedCredits   int64      `gorm:"not null" json:"granted_credits"`
	RemainingCredits int64      `gorm:"not null" json:"remaining_credits"`
	Status           string     `gorm:"size:20;not null;index:idx_credit_lots_spend,priority:2" json:"status"`
	ExpiresAt        time.Time  `gorm:"not null;index:idx_credit_lots_spend,priority:3" json:"expires_at"`
	RefundedAt       *time.Time `json:"refunded_at"`
}

func (PurchasedCreditLot) TableName() string { return "purchased_credit_lots" }

type CreditTrialClaim struct {
	Base
	UserID         uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:uq_credit_trial_claims_user_id" json:"user_id"`
	PaymentOrderID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"payment_order_id"`
}

func (CreditTrialClaim) TableName() string { return "credit_trial_claims" }

type SubscriptionPlan struct {
	Base
	Name                        string         `gorm:"size:50;uniqueIndex;not null" json:"name"`
	DisplayName                 string         `gorm:"size:100;not null" json:"display_name"`
	Description                 *string        `json:"description"`
	MonthlyCredits              int64          `gorm:"not null" json:"monthly_credits"`
	PriceMonthly                float64        `gorm:"type:decimal(10,2);not null" json:"price_monthly"`
	PriceYearly                 float64        `gorm:"type:decimal(10,2);not null" json:"price_yearly"`
	Currency                    string         `gorm:"size:3;default:USD" json:"currency"`
	CreemProductIDMonthly       string         `gorm:"size:64;index" json:"creem_product_id_monthly"`
	CreemProductIDYearly        string         `gorm:"size:64;index" json:"creem_product_id_yearly"`
	AlipayPlanID                string         `gorm:"size:128;index" json:"alipay_plan_id"`
	AlipayProductID             string         `gorm:"size:128;index" json:"alipay_product_id"`
	SubscriptionProviderEnabled bool           `gorm:"default:false;index" json:"subscription_provider_enabled"`
	Features                    datatypes.JSON `json:"features"`
	Limits                      datatypes.JSON `json:"limits"`
	IsActive                    bool           `gorm:"default:true;index" json:"is_active"`
	IsFeatured                  bool           `gorm:"default:false" json:"is_featured"`
	SortOrder                   int            `gorm:"default:0;index" json:"sort_order"`
}

func (SubscriptionPlan) TableName() string { return "subscription_plans" }

type UserSubscription struct {
	Base
	UserID                 uuid.UUID  `gorm:"type:uuid;index;not null" json:"user_id"`
	PlanID                 uuid.UUID  `gorm:"type:uuid;index;not null" json:"plan_id"`
	BillingCycle           string     `gorm:"size:16;index;not null" json:"billing_cycle"`
	Status                 string     `gorm:"size:32;index;not null" json:"status"`
	CreemCustomerID        string     `gorm:"size:64;index" json:"creem_customer_id"`
	CreemSubscriptionID    string     `gorm:"size:64;uniqueIndex" json:"creem_subscription_id"`
	CreemProductID         string     `gorm:"size:64;index" json:"creem_product_id"`
	PaymentProvider        string     `gorm:"size:20;default:creem;index" json:"payment_provider"`
	AlipayAgreementID      string     `gorm:"size:128;uniqueIndex" json:"alipay_agreement_id"`
	AlipaySubscriptionID   string     `gorm:"size:128;uniqueIndex" json:"alipay_subscription_id"`
	CurrentPeriodStart     *time.Time `json:"current_period_start"`
	CurrentPeriodEnd       *time.Time `json:"current_period_end"`
	CancelAtPeriodEnd      bool       `gorm:"default:false" json:"cancel_at_period_end"`
	CanceledAt             *time.Time `json:"canceled_at"`
	NextChargeAt           *time.Time `gorm:"index" json:"next_charge_at"`
	GracePeriodEndsAt      *time.Time `gorm:"index" json:"grace_period_ends_at"`
	LastProviderSyncAt     *time.Time `json:"last_provider_sync_at"`
	ProviderFailureReason  string     `gorm:"size:512" json:"provider_failure_reason"`
	TrialEnd               *time.Time `json:"trial_end"`
	NextCreditGrantAt      *time.Time `json:"next_credit_grant_at"`
	MonthlyCreditsSnapshot int64      `gorm:"default:0" json:"monthly_credits_snapshot"`
}

func (UserSubscription) TableName() string { return "user_subscriptions" }

type SubscriptionCreditGrant struct {
	Base
	UserID         uuid.UUID `gorm:"type:uuid;index;not null" json:"user_id"`
	SubscriptionID uuid.UUID `gorm:"type:uuid;index;not null" json:"subscription_id"`
	PlanID         uuid.UUID `gorm:"type:uuid;index;not null" json:"plan_id"`
	CreditAmount   int64     `gorm:"not null" json:"credit_amount"`
	UsedAmount     int64     `gorm:"default:0" json:"used_amount"`
	PeriodStart    time.Time `gorm:"index;not null" json:"period_start"`
	PeriodEnd      time.Time `gorm:"index;not null" json:"period_end"`
	ExpiresAt      time.Time `gorm:"index;not null" json:"expires_at"`
	IdempotencyKey string    `gorm:"size:160;uniqueIndex;not null" json:"idempotency_key"`
	SourceEventID  string    `gorm:"size:80;index" json:"source_event_id"`
	Status         string    `gorm:"size:20;default:active;index" json:"status"`
}

func (SubscriptionCreditGrant) TableName() string { return "subscription_credit_grants" }

func (g SubscriptionCreditGrant) RemainingCredits() int64 {
	if g.CreditAmount < g.UsedAmount {
		return 0
	}
	return g.CreditAmount - g.UsedAmount
}

type UserTokenAccount struct {
	Base
	UserID         uuid.UUID  `gorm:"type:uuid;uniqueIndex;not null" json:"user_id"`
	TotalTokens    int64      `gorm:"default:0" json:"total_tokens"`
	UsedTokens     int64      `gorm:"default:0" json:"used_tokens"`
	TotalRecharged float64    `gorm:"type:decimal(10,2);default:0" json:"total_recharged"`
	LastRechargeAt *time.Time `json:"last_recharge_at"`
}

func (UserTokenAccount) TableName() string { return "user_token_accounts" }

func (a UserTokenAccount) RemainingTokens() int64 {
	return a.TotalTokens - a.UsedTokens
}

type TokenRechargeRecord struct {
	Base
	UserID        uuid.UUID  `gorm:"type:uuid;index;not null" json:"user_id"`
	TokenAmount   int64      `json:"token_amount"`
	BonusTokens   int64      `gorm:"default:0" json:"bonus_tokens"`
	Price         float64    `gorm:"type:decimal(10,2)" json:"price"`
	PaymentMethod string     `gorm:"size:20;default:wechat" json:"payment_method"`
	PaymentID     *uuid.UUID `gorm:"type:uuid" json:"payment_id"`
	PackageID     *uuid.UUID `gorm:"type:uuid" json:"package_id"`
}

func (TokenRechargeRecord) TableName() string { return "token_recharge_records" }

type TokenUsageRecord struct {
	ID               uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	UserID           uuid.UUID      `gorm:"type:uuid;index;not null" json:"user_id"`
	ProjectID        *uuid.UUID     `gorm:"type:uuid" json:"project_id"`
	TokenCount       int64          `json:"token_count"`
	CreditCount      int64          `gorm:"default:0;index" json:"credit_count"`
	APIEndpoint      string         `gorm:"size:100;index" json:"api_endpoint"`
	ModelName        string         `gorm:"size:50;index" json:"model_name"`
	ModelID          string         `gorm:"size:128;index" json:"model_id"`
	Provider         string         `gorm:"size:50;index" json:"provider"`
	ServiceKind      string         `gorm:"size:30;index" json:"service_kind"`
	PromptTokens     int64          `gorm:"default:0" json:"prompt_tokens"`
	CompletionTokens int64          `gorm:"default:0" json:"completion_tokens"`
	TotalModelTokens int64          `gorm:"default:0" json:"total_model_tokens"`
	UTF8Bytes        int64          `gorm:"default:0" json:"utf8_bytes"`
	DurationSeconds  *float64       `gorm:"type:decimal(15,6)" json:"duration_seconds"`
	BillingMonth     string         `gorm:"size:7;index" json:"billing_month"`
	RequestMetadata  datatypes.JSON `json:"request_metadata"`
	CostAmount       *float64       `gorm:"type:decimal(15,6)" json:"cost_amount"`
	RevenueAmount    *float64       `gorm:"type:decimal(15,6)" json:"revenue_amount"`
	ProfitAmount     *float64       `gorm:"type:decimal(15,6)" json:"profit_amount"`
	MissingPricing   bool           `gorm:"default:false;index" json:"missing_pricing"`
	RecordedAt       time.Time      `gorm:"index" json:"recorded_at"`
}

func (r *TokenUsageRecord) BeforeCreate(*gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	if r.RecordedAt.IsZero() {
		r.RecordedAt = time.Now().UTC()
	}
	return nil
}

func (TokenUsageRecord) TableName() string { return "token_usage_records" }

type CreditReservation struct {
	Base
	UserID                  uuid.UUID      `gorm:"type:uuid;index;not null" json:"user_id"`
	ProjectID               *uuid.UUID     `gorm:"type:uuid;index" json:"project_id"`
	WorkflowRunID           *uuid.UUID     `gorm:"type:uuid;index" json:"workflow_run_id"`
	WorkflowStepExecutionID *uuid.UUID     `gorm:"type:uuid;index" json:"workflow_step_execution_id"`
	APIEndpoint             string         `gorm:"size:100;index" json:"api_endpoint"`
	Provider                string         `gorm:"size:50;index" json:"provider"`
	ServiceKind             string         `gorm:"size:30;index" json:"service_kind"`
	ModelID                 string         `gorm:"size:128;index" json:"model_id"`
	CreditCount             int64          `gorm:"not null;index" json:"credit_count"`
	PromptTokens            int64          `gorm:"default:0" json:"prompt_tokens"`
	CompletionTokens        int64          `gorm:"default:0" json:"completion_tokens"`
	TotalModelTokens        int64          `gorm:"default:0" json:"total_model_tokens"`
	UTF8Bytes               int64          `gorm:"default:0" json:"utf8_bytes"`
	CharacterCount          int64          `gorm:"default:0" json:"character_count"`
	DurationSeconds         *float64       `gorm:"type:decimal(15,6)" json:"duration_seconds"`
	BillingMonth            string         `gorm:"size:7;index" json:"billing_month"`
	RequestMetadata         datatypes.JSON `json:"request_metadata"`
	CostAmount              *float64       `gorm:"type:decimal(15,6)" json:"cost_amount"`
	MissingPricing          bool           `gorm:"default:false;index" json:"missing_pricing"`
	Status                  string         `gorm:"size:20;default:pending;index;not null" json:"status"`
	IdempotencyKey          string         `gorm:"size:180;uniqueIndex;not null" json:"idempotency_key"`
	ExpiresAt               time.Time      `gorm:"index;not null" json:"expires_at"`
	ConfirmedAt             *time.Time     `gorm:"index" json:"confirmed_at"`
	ReleasedAt              *time.Time     `gorm:"index" json:"released_at"`
	RecordedAt              time.Time      `gorm:"index;not null" json:"recorded_at"`
}

func (r *CreditReservation) BeforeCreate(*gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	if r.RecordedAt.IsZero() {
		r.RecordedAt = time.Now().UTC()
	}
	if r.BillingMonth == "" {
		r.BillingMonth = r.RecordedAt.Format("2006-01")
	}
	if r.Status == "" {
		r.Status = "pending"
	}
	if len(r.RequestMetadata) == 0 {
		r.RequestMetadata = datatypes.JSON([]byte("{}"))
	}
	return nil
}

func (CreditReservation) TableName() string { return "credit_reservations" }

type CreditLedgerRecord struct {
	Base
	UserID          uuid.UUID      `gorm:"type:uuid;index;not null" json:"user_id"`
	ProjectID       *uuid.UUID     `gorm:"type:uuid;index" json:"project_id"`
	ProjectName     string         `gorm:"size:200" json:"project_name"`
	EventType       string         `gorm:"size:32;index;not null" json:"event_type"`
	Item            string         `gorm:"size:160" json:"item"`
	ChangeAmount    int64          `gorm:"index;not null" json:"change_amount"`
	BalanceAfter    int64          `gorm:"not null" json:"balance_after"`
	Reason          string         `gorm:"size:255" json:"reason"`
	RequestMetadata datatypes.JSON `json:"request_metadata"`
	RecordedAt      time.Time      `gorm:"index;not null" json:"recorded_at"`
}

func (r *CreditLedgerRecord) BeforeCreate(*gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	if r.RecordedAt.IsZero() {
		r.RecordedAt = time.Now().UTC()
	}
	if len(r.RequestMetadata) == 0 {
		r.RequestMetadata = datatypes.JSON([]byte("{}"))
	}
	return nil
}

func (CreditLedgerRecord) TableName() string { return "credit_ledger_records" }

type WeChatPayment struct {
	Base
	UserID          uuid.UUID      `gorm:"type:uuid;index;not null" json:"user_id"`
	OutTradeNo      string         `gorm:"size:32;uniqueIndex" json:"out_trade_no"`
	PrepayID        *string        `gorm:"size:64" json:"prepay_id"`
	CodeURL         *string        `gorm:"size:512" json:"code_url"`
	TransactionID   *string        `gorm:"size:32;index" json:"transaction_id"`
	Subject         string         `gorm:"size:256" json:"subject"`
	TotalAmount     float64        `gorm:"type:decimal(10,2)" json:"total_amount"`
	Currency        string         `gorm:"size:3;default:CNY" json:"currency"`
	OpenID          *string        `gorm:"size:128;index" json:"openid"`
	TradeType       string         `gorm:"size:16;default:NATIVE" json:"trade_type"`
	BankType        *string        `gorm:"size:16" json:"bank_type"`
	TradeState      string         `gorm:"size:32;default:NOTPAY;index" json:"trade_state"`
	TradeStateDesc  *string        `gorm:"size:256" json:"trade_state_desc"`
	Status          string         `gorm:"size:20;default:pending;index" json:"status"`
	TimeExpire      *time.Time     `json:"time_expire"`
	SuccessTime     *time.Time     `json:"success_time"`
	NotifyTime      *time.Time     `json:"notify_time"`
	NotifyType      *string        `gorm:"size:64" json:"notify_type"`
	PaymentMetadata datatypes.JSON `json:"payment_metadata"`
	Attach          *string        `gorm:"size:128" json:"attach"`
	FailureReason   *string        `json:"failure_reason"`
}

func (WeChatPayment) TableName() string { return "wechat_payments" }

type CreemCheckoutSession struct {
	Base
	UserID              uuid.UUID      `gorm:"type:uuid;index;not null" json:"user_id"`
	Purpose             string         `gorm:"size:32;index;not null" json:"purpose"`
	RequestID           string         `gorm:"size:80;uniqueIndex;not null" json:"request_id"`
	CreemCheckoutID     string         `gorm:"size:64;uniqueIndex" json:"creem_checkout_id"`
	CheckoutURL         string         `gorm:"size:2048" json:"checkout_url"`
	CreemProductID      string         `gorm:"size:64;index" json:"creem_product_id"`
	CreemCustomerID     string         `gorm:"size:64;index" json:"creem_customer_id"`
	CreemSubscriptionID string         `gorm:"size:64;index" json:"creem_subscription_id"`
	CreemOrderID        string         `gorm:"size:64;index" json:"creem_order_id"`
	PlanID              *uuid.UUID     `gorm:"type:uuid;index" json:"plan_id"`
	PackageID           *uuid.UUID     `gorm:"type:uuid;index" json:"package_id"`
	BillingCycle        string         `gorm:"size:16;index" json:"billing_cycle"`
	Status              string         `gorm:"size:32;default:pending;index" json:"status"`
	Amount              float64        `gorm:"type:decimal(10,2)" json:"amount"`
	Currency            string         `gorm:"size:3;default:USD" json:"currency"`
	Metadata            datatypes.JSON `json:"metadata"`
}

func (CreemCheckoutSession) TableName() string { return "creem_checkout_sessions" }

type CreemWebhookEvent struct {
	Base
	EventID         string         `gorm:"size:80;uniqueIndex;not null" json:"event_id"`
	EventType       string         `gorm:"size:80;index;not null" json:"event_type"`
	Payload         datatypes.JSON `json:"payload"`
	ProcessedAt     *time.Time     `json:"processed_at"`
	ProcessingError *string        `json:"processing_error"`
}

func (CreemWebhookEvent) TableName() string { return "creem_webhook_events" }

type AlipaySubscriptionSession struct {
	Base
	UserID               uuid.UUID      `gorm:"type:uuid;index;not null" json:"user_id"`
	PlanID               uuid.UUID      `gorm:"type:uuid;index;not null" json:"plan_id"`
	RequestID            string         `gorm:"size:80;uniqueIndex;not null" json:"request_id"`
	AlipayAgreementID    string         `gorm:"size:128;index" json:"alipay_agreement_id"`
	AlipaySubscriptionID string         `gorm:"size:128;index" json:"alipay_subscription_id"`
	CheckoutURL          string         `gorm:"size:2048" json:"checkout_url"`
	Status               string         `gorm:"size:32;default:pending;index" json:"status"`
	Amount               float64        `gorm:"type:decimal(10,2);not null" json:"amount"`
	Currency             string         `gorm:"size:3;default:CNY" json:"currency"`
	Metadata             datatypes.JSON `json:"metadata"`
}

func (AlipaySubscriptionSession) TableName() string { return "alipay_subscription_sessions" }

type PaymentWebhookEvent struct {
	Base
	Provider         string         `gorm:"size:20;uniqueIndex:uq_payment_webhook_provider_event;not null" json:"provider"`
	EventID          string         `gorm:"size:128;uniqueIndex:uq_payment_webhook_provider_event;not null" json:"event_id"`
	EventType        string         `gorm:"size:80;index;not null" json:"event_type"`
	ObjectID         string         `gorm:"size:128;index" json:"object_id"`
	SignatureValid   bool           `gorm:"default:false" json:"signature_valid"`
	Payload          datatypes.JSON `json:"payload"`
	ProcessingStatus string         `gorm:"size:24;default:pending;index" json:"processing_status"`
	AttemptCount     int            `gorm:"default:0" json:"attempt_count"`
	ProcessedAt      *time.Time     `json:"processed_at"`
	LastError        string         `gorm:"size:1024" json:"last_error"`
}

func (PaymentWebhookEvent) TableName() string { return "payment_webhook_events" }

type SystemSetting struct {
	Base
	Key         string         `gorm:"size:100;uniqueIndex;not null" json:"key"`
	Value       datatypes.JSON `json:"value"`
	Description *string        `json:"description"`
	IsPublic    bool           `gorm:"default:false;index" json:"is_public"`
	UpdatedBy   *uuid.UUID     `gorm:"type:uuid" json:"updated_by"`
}

func (SystemSetting) TableName() string { return "system_settings" }

type Notification struct {
	Base
	UserID         uuid.UUID      `gorm:"type:uuid;index;not null" json:"user_id"`
	AnnouncementID *uuid.UUID     `gorm:"type:uuid;index" json:"announcement_id,omitempty"`
	Type           string         `gorm:"size:32;index;not null" json:"type"`
	Title          string         `gorm:"size:200;not null" json:"title"`
	Message        string         `json:"message"`
	Data           datatypes.JSON `json:"data"`
	IsRead         bool           `gorm:"default:false;index" json:"is_read"`
	ReadAt         *time.Time     `json:"read_at"`
	ExpiresAt      *time.Time     `json:"expires_at"`
}

func (Notification) TableName() string { return "notifications" }

type UploadRecord struct {
	Base
	UserID           uuid.UUID      `gorm:"type:uuid;index;not null" json:"user_id"`
	ProjectID        *uuid.UUID     `gorm:"type:uuid;index" json:"project_id"`
	AssetType        *string        `gorm:"size:32;index" json:"asset_type"`
	StepType         *string        `gorm:"size:50;index" json:"step_type"`
	StepID           *uuid.UUID     `gorm:"type:uuid;index" json:"step_id"`
	OriginalFilename string         `gorm:"size:255;not null" json:"original_filename"`
	StorageKey       string         `gorm:"size:1024;index;not null" json:"storage_key"`
	FileSize         *int64         `json:"file_size"`
	ContentType      string         `gorm:"size:127;not null" json:"content_type"`
	Status           string         `gorm:"size:20;default:pending;index" json:"status"`
	StorageProvider  string         `gorm:"size:32;default:local;index" json:"storage_provider"`
	PresignedURL     *string        `gorm:"size:2048" json:"-"`
	ExpiresAt        *time.Time     `json:"expires_at"`
	UploadedAt       *time.Time     `json:"uploaded_at"`
	ETag             *string        `gorm:"size:255" json:"etag"`
	ExtraMetadata    datatypes.JSON `json:"extra_metadata"`
}

func (UploadRecord) TableName() string { return "upload_records" }
