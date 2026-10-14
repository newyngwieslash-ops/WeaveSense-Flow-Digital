package model

import "time"

// FlowModelTask is one Model Gateway call executed by the async pipeline:
// queued → running → succeeded | failed | retry_wait | reconciliation_required (→ reconciled). The task id
// is the Idempotency-Key sent to the gateway.
type FlowModelTask struct {
	ID            string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID      string     `json:"tenantId" gorm:"size:40;not null;uniqueIndex:ux_flow_model_task,priority:1"`
	IdemKey       string     `json:"idemKey" gorm:"size:160;not null;uniqueIndex:ux_flow_model_task,priority:2"`
	ReservationID string     `json:"reservationId" gorm:"size:40"`
	Purpose       string     `json:"purpose" gorm:"size:40"`
	Request       string     `json:"-" gorm:"type:text"` // JSON {system,user,max_tokens}
	Status        string     `json:"status" gorm:"size:32;not null;index"`
	Attempts      int        `json:"attempts"`
	MaxAttempts   int        `json:"maxAttempts"`
	Result        string     `json:"-" gorm:"type:text"`
	CostMicro     int64      `json:"costMicro"`
	LastError     string     `json:"lastError,omitempty" gorm:"size:500"`
	NextAttemptAt time.Time  `json:"nextAttemptAt" gorm:"index"`
	LeaseUntil    *time.Time `json:"leaseUntil,omitempty"`
	// Context is the caller's completion context (workspace, evidence, scope)
	// so a finished task can be validated and delivered asynchronously (v61).
	Context string `json:"-" gorm:"type:text"`
	// NotifiedAt: completion was pushed to waiters (parked runs, engine briefs).
	NotifiedAt *time.Time `json:"notifiedAt,omitempty" gorm:"index"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

func (FlowModelTask) TableName() string { return "flow_model_tasks" }

// FlowMCPOAuthGrant holds the authorization-code grant of one MCP connection.
// Tokens are AES-GCM encrypted with FLOW_OAUTH_ENC_KEY; never serialized.
type FlowMCPOAuthGrant struct {
	ConnectionID    string    `json:"connectionId" gorm:"primaryKey;size:40"`
	TenantID        string    `json:"tenantId" gorm:"size:40;not null;index"`
	AccessTokenEnc  string    `json:"-" gorm:"type:text"`
	RefreshTokenEnc string    `json:"-" gorm:"type:text"`
	Scope           string    `json:"scope" gorm:"size:300"`
	ExpiresAt       time.Time `json:"expiresAt"`
	GrantedBy       string    `json:"grantedBy" gorm:"size:36"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

func (FlowMCPOAuthGrant) TableName() string { return "flow_mcp_oauth_grants" }

// FlowOAuthState is a pending authorization request (state → PKCE verifier).
type FlowOAuthState struct {
	State        string    `json:"-" gorm:"primaryKey;size:64"`
	TenantID     string    `json:"tenantId" gorm:"size:40;not null"`
	ConnectionID string    `json:"connectionId" gorm:"size:40;not null"`
	UserID       string    `json:"userId" gorm:"size:36"`
	VerifierEnc  string    `json:"-" gorm:"type:text"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

func (FlowOAuthState) TableName() string { return "flow_oauth_states" }
