package model

import "time"

// AgricultureFarmOperation is platform-owned intent, never the farm fact store.
// A completed row is a permanent grant-registration tombstone.
type AgricultureFarmOperation struct {
	ID                   string `gorm:"primaryKey;size:40"`
	TenantID             string `gorm:"size:40;not null;index"`
	WorkspaceID          string `gorm:"size:40;not null"`
	ActorID              string `gorm:"size:128;not null"`
	Action               string `gorm:"size:16;not null"`
	FarmID               string `gorm:"size:128"`
	ExpectedRevision     int64
	ResultRevision       int64
	RequestHash          string  `gorm:"size:64;not null"`
	GrantPlanHash        string  `gorm:"size:64;not null"`
	Payload              string  `gorm:"type:text;not null"`
	Summary              string  `gorm:"type:text;not null"`
	State                string  `gorm:"size:32;not null"`
	CommandID            string  `gorm:"size:40"`
	IdempotencyHash      *string `gorm:"size:64;uniqueIndex:ux_agri_farm_idempotency"`
	AuthorizationVersion int64
	ExpiresAt            time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
	ErrorCode            string `gorm:"size:64"`
}

func (AgricultureFarmOperation) TableName() string { return "flow_agriculture_farm_operations" }

type AgricultureFarmEvent struct {
	Producer    string `gorm:"primaryKey;size:40"`
	EventID     string `gorm:"primaryKey;size:128"`
	TenantID    string `gorm:"size:40;not null"`
	WorkspaceID string `gorm:"size:40;not null"`
	OperationID string `gorm:"size:40;not null;uniqueIndex"`
	PayloadHash string `gorm:"size:64;not null"`
	FarmID      string `gorm:"size:128;not null"`
	Revision    int64
	CreatedAt   time.Time
}

func (AgricultureFarmEvent) TableName() string { return "flow_agriculture_farm_events" }
