package model

import "time"

// AgricultureAssetOperation is an immutable approval intent and delivery state,
// not the authoritative GIS catalog or its metadata history.
type AgricultureAssetOperation struct {
	ID                   string `gorm:"primaryKey;size:40"`
	TenantID             string `gorm:"size:40;not null;index"`
	WorkspaceID          string `gorm:"size:40;not null"`
	ActorID              string `gorm:"size:128;not null"`
	Action               string `gorm:"size:16;not null"`
	AssetID              string `gorm:"size:128"`
	AssetVersionID       string `gorm:"size:128"`
	FarmID               string `gorm:"size:128"`
	ExpectedRevision     int64
	ResultRevision       int64
	RequestHash          string  `gorm:"size:64;not null"`
	GrantPlanHash        string  `gorm:"size:64;not null"`
	Payload              string  `gorm:"type:text;not null"`
	Summary              string  `gorm:"type:text;not null"`
	State                string  `gorm:"size:32;not null"`
	CommandID            string  `gorm:"size:40"`
	IdempotencyHash      *string `gorm:"size:64;uniqueIndex:ux_agri_asset_idempotency"`
	AuthorizationVersion int64
	ExpiresAt            time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
	ErrorCode            string `gorm:"size:64"`
}

func (AgricultureAssetOperation) TableName() string { return "flow_agriculture_asset_operations" }

type AgricultureAssetEvent struct {
	Producer       string `gorm:"primaryKey;size:40"`
	EventID        string `gorm:"primaryKey;size:128"`
	TenantID       string `gorm:"size:40;not null"`
	WorkspaceID    string `gorm:"size:40;not null"`
	OperationID    string `gorm:"size:40;not null;uniqueIndex"`
	PayloadHash    string `gorm:"size:64;not null"`
	AssetID        string `gorm:"size:128;not null"`
	AssetVersionID string `gorm:"size:128;not null"`
	Revision       int64
	CreatedAt      time.Time
}

func (AgricultureAssetEvent) TableName() string { return "flow_agriculture_asset_events" }
