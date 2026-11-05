package models

import (
	"time"

	"github.com/google/uuid"
)

type DesktopAuthorizationRequest struct {
	Base
	StateHash             string     `gorm:"size:128;index;not null" json:"-"`
	CodeChallenge         string     `gorm:"size:128;not null" json:"-"`
	AuthorizationCodeHash *string    `gorm:"size:128" json:"-"`
	UserID                *uuid.UUID `gorm:"type:uuid;index" json:"-"`
	ExpiresAt             time.Time  `gorm:"index;not null" json:"expires_at"`
	AuthorizedAt          *time.Time `gorm:"index" json:"authorized_at"`
	RedeemedAt            *time.Time `gorm:"index" json:"redeemed_at"`
}

func (DesktopAuthorizationRequest) TableName() string { return "desktop_authorization_requests" }

type RevokedAuthToken struct {
	JTI       string    `gorm:"size:64;primaryKey" json:"-"`
	ExpiresAt time.Time `gorm:"index;not null" json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

func (RevokedAuthToken) TableName() string { return "revoked_auth_tokens" }
