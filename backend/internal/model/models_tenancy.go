package model

import "time"

// WeaveSense-Flow P1 tenancy model. Tenants own workspaces; users join via
// memberships. Existing users are mapped to a personal tenant (OwnerUserID).

type Tenant struct {
	ID          string    `json:"id" gorm:"primaryKey;size:40"`
	Kind        string    `json:"kind" gorm:"size:16;index"` // personal | organization
	Name        string    `json:"name" gorm:"size:120"`
	OwnerUserID string    `json:"ownerUserId,omitempty" gorm:"size:36;index"`
	PersonalKey *string   `json:"-" gorm:"size:40;uniqueIndex"` // = user id for personal tenants
	Status      string    `json:"status" gorm:"size:16;default:active"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (Tenant) TableName() string { return "flow_tenants" }

type Membership struct {
	ID        string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID  string    `json:"tenantId" gorm:"size:40;uniqueIndex:ux_flow_membership"`
	UserID    string    `json:"userId" gorm:"size:36;uniqueIndex:ux_flow_membership;index"`
	Role      string    `json:"role" gorm:"size:16"` // owner | admin | member
	Status    string    `json:"status" gorm:"size:16;default:active"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (Membership) TableName() string { return "flow_memberships" }

type Workspace struct {
	ID        string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID  string    `json:"tenantId" gorm:"size:40;index;uniqueIndex:ux_flow_workspace_slug"`
	Slug      string    `json:"slug" gorm:"size:64;uniqueIndex:ux_flow_workspace_slug"`
	Name      string    `json:"name" gorm:"size:120"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (Workspace) TableName() string { return "flow_workspaces" }

type WorkspaceMembership struct {
	ID          string    `json:"id" gorm:"primaryKey;size:40"`
	WorkspaceID string    `json:"workspaceId" gorm:"size:40;uniqueIndex:ux_flow_ws_membership"`
	UserID      string    `json:"userId" gorm:"size:36;uniqueIndex:ux_flow_ws_membership;index"`
	Role        string    `json:"role" gorm:"size:16"` // admin | editor | viewer
	CreatedAt   time.Time `json:"createdAt"`
}

func (WorkspaceMembership) TableName() string { return "flow_workspace_memberships" }

type ServicePrincipal struct {
	ID        string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID  string     `json:"tenantId" gorm:"size:40;index"`
	Name      string     `json:"name" gorm:"size:120"`
	Scopes    string     `json:"scopes" gorm:"size:512"` // space separated
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

func (ServicePrincipal) TableName() string { return "flow_service_principals" }

// FlowEventInbox deduplicates Loom Engine outbox events (consumer_id + event_id).
type FlowEventInbox struct {
	ConsumerID  string `gorm:"primaryKey;size:64"`
	EventID     string `gorm:"primaryKey;size:64"`
	TenantID    string `gorm:"size:40;index"`
	OperationID string `gorm:"size:64;index"`
	Type        string `gorm:"size:64"`
	Payload     string `gorm:"type:text"`
	ReceivedAt  time.Time
}

func (FlowEventInbox) TableName() string { return "flow_event_inbox" }

// FlowOperationProjection is the platform's read model of Engine operations.
type FlowOperationProjection struct {
	OperationID string `gorm:"primaryKey;size:64"`
	TenantID    string `gorm:"size:40;index"`
	WorkspaceID string `gorm:"size:40;index"`
	JobID       string `gorm:"size:64"`
	Status      string `gorm:"size:24"`
	Sequence    int64
	AsOf        time.Time
	// PRD §12.1: SourceVersion = the Engine event sequence this row reflects; SyncStatus = synced | stale (computed at read).
	SourceVersion int64  `gorm:"not null;default:0"`
	SyncStatus    string `gorm:"size:16;not null;default:synced"`
}

func (FlowOperationProjection) TableName() string { return "flow_operation_projections" }
