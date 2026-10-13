package model

import "time"

// FlowBillingAccount is the tenant's billing account. Usage is settled against
// the payer user's CreditAccount (personal tenants: the user; organizations:
// the owner, shared by every member).
type FlowBillingAccount struct {
	ID                string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID          string    `json:"tenantId" gorm:"size:40;not null;uniqueIndex"`
	PayerUserID       string    `json:"payerUserId" gorm:"size:36;not null;index"`
	Kind              string    `json:"kind" gorm:"size:16;not null"` // personal | shared
	MonthlyQuotaMicro int64     `json:"monthlyQuotaMicro"`            // 0 = unlimited (credit balance still applies)
	MaxOpenReserves   int       `json:"maxOpenReserves"`              // 0 = unlimited
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// FlowBudgetReservation holds credits for one run / child / external call.
// Root reservations move credits available→reserved in the platform ledger;
// children are carved atomically out of their parent's unallocated remainder.
type FlowBudgetReservation struct {
	ID             string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID       string     `json:"tenantId" gorm:"size:40;not null;uniqueIndex:ux_flow_res_idem,priority:1;index"`
	AccountID      string     `json:"accountId" gorm:"size:40;not null;index"`
	ParentID       string     `json:"parentId,omitempty" gorm:"size:40;index"`
	Purpose        string     `json:"purpose" gorm:"size:40"`
	RefID          string     `json:"refId" gorm:"size:80;index"`
	IdempotencyKey string     `json:"idempotencyKey" gorm:"size:160;not null;uniqueIndex:ux_flow_res_idem,priority:2"`
	ReservedMicro  int64      `json:"reservedMicro"`
	AllocatedMicro int64      `json:"allocatedMicro"` // held by open children
	ConsumedMicro  int64      `json:"consumedMicro"`
	Status         string     `json:"status" gorm:"size:16;not null;index"` // reserved | settled | released | unknown
	ExternalRef    string     `json:"externalRef,omitempty" gorm:"size:200"`
	Note           string     `json:"note,omitempty" gorm:"size:300"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	SettledAt      *time.Time `json:"settledAt,omitempty"`
}

// FlowInvite invites a user into an organization tenant (token stored hashed).
type FlowInvite struct {
	ID          string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID    string     `json:"tenantId" gorm:"size:40;not null;index"`
	WorkspaceID string     `json:"workspaceId" gorm:"size:40;not null"`
	Email       string     `json:"email" gorm:"size:200"`
	Role        string     `json:"role" gorm:"size:16;not null"` // workspace role: admin | editor | viewer
	TokenHash   string     `json:"-" gorm:"size:64;not null;uniqueIndex"`
	InvitedBy   string     `json:"invitedBy" gorm:"size:36"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	AcceptedBy  string     `json:"acceptedBy,omitempty" gorm:"size:36"`
	AcceptedAt  *time.Time `json:"acceptedAt,omitempty"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}
