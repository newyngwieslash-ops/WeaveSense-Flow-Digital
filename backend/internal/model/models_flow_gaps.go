package model

import "time"

// FlowUsageRecord is one metered usage line (§12). Quantity is in the type's
// smallest integer unit; PriceVersion and unit price are frozen at record time.
type FlowUsageRecord struct {
	ID             string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID       string    `json:"tenantId" gorm:"size:40;not null;uniqueIndex:ux_flow_usage_idem,priority:1;index:idx_flow_usage_type,priority:1"`
	WorkspaceID    string    `json:"workspaceId" gorm:"size:40"`
	UsageType      string    `json:"usageType" gorm:"size:32;not null;index:idx_flow_usage_type,priority:2"`
	Quantity       int64     `json:"quantity"`
	UnitPriceMicro int64     `json:"unitPriceMicro"`
	CostMicro      int64     `json:"costMicro"`
	PriceVersion   string    `json:"priceVersion" gorm:"size:40;not null"`
	ReservationID  string    `json:"reservationId,omitempty" gorm:"size:40;index"`
	SubjectRef     string    `json:"subjectRef" gorm:"size:200"`
	Status         string    `json:"status" gorm:"size:24;not null"` // recorded | charged | insufficient
	IdemKey        string    `json:"idemKey" gorm:"size:200;not null;uniqueIndex:ux_flow_usage_idem,priority:2"`
	CreatedAt      time.Time `json:"createdAt" gorm:"index"`
}

func (FlowUsageRecord) TableName() string { return "flow_usage_records" }

// FlowNotificationTarget is a tenant-authorised external notification target (§5.2/§12).
// Webhook secrets are referenced (env:NAME), never stored.
type FlowNotificationTarget struct {
	ID             string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID       string    `json:"tenantId" gorm:"size:40;not null;uniqueIndex:ux_flow_ntarget,priority:1"`
	WorkspaceID    string    `json:"workspaceId" gorm:"size:40;not null;uniqueIndex:ux_flow_ntarget,priority:2"`
	Channel        string    `json:"channel" gorm:"size:16;not null;uniqueIndex:ux_flow_ntarget,priority:3"` // email | webhook
	Address        string    `json:"address" gorm:"size:500;not null;uniqueIndex:ux_flow_ntarget,priority:4"`
	SecretRef      string    `json:"secretRef,omitempty" gorm:"size:120"`
	EventTypes     string    `json:"eventTypes" gorm:"size:300"`     // comma separated; default report.completed
	Status         string    `json:"status" gorm:"size:16;not null"` // active | revoked | disabled
	DisabledReason string    `json:"disabledReason,omitempty" gorm:"size:300"`
	CreatedBy      string    `json:"createdBy" gorm:"size:36"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func (FlowNotificationTarget) TableName() string { return "flow_notification_targets" }

// FlowNotificationDelivery is one send of one event to one target. ID is the stable
// delivery_id sent to receivers; (tenant, target, event) is unique (idempotent fan-out).
type FlowNotificationDelivery struct {
	ID            string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID      string     `json:"tenantId" gorm:"size:40;not null;uniqueIndex:ux_flow_ndelivery,priority:1"`
	WorkspaceID   string     `json:"workspaceId" gorm:"size:40;not null;index"`
	TargetID      string     `json:"targetId" gorm:"size:40;not null;uniqueIndex:ux_flow_ndelivery,priority:2"`
	EventID       string     `json:"eventId" gorm:"size:64;not null;uniqueIndex:ux_flow_ndelivery,priority:3"`
	EventType     string     `json:"eventType" gorm:"size:40"`
	Title         string     `json:"title" gorm:"size:300"`
	RefID         string     `json:"refId" gorm:"size:64"`
	Status        string     `json:"status" gorm:"size:32;not null;index"` // pending | sending | delivered | retry_wait | failed | cancelled | reconciliation_required
	Attempts      int        `json:"attempts"`
	MaxAttempts   int        `json:"maxAttempts"`
	NextAttemptAt time.Time  `json:"nextAttemptAt" gorm:"index"`
	LeaseUntil    *time.Time `json:"leaseUntil,omitempty"`
	LastError     string     `json:"lastError,omitempty" gorm:"size:500"`
	Receipt       string     `json:"receipt,omitempty" gorm:"size:300"`
	DeliveredAt   *time.Time `json:"deliveredAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

func (FlowNotificationDelivery) TableName() string { return "flow_notification_deliveries" }
