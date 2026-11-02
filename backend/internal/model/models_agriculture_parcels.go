package model

import "time"

// AgricultureParcelOperation stores platform intent and authorization binding,
// never an authoritative parcel geometry or mutable copy of GIS history.
type AgricultureParcelOperation struct {
	ID                   string `gorm:"primaryKey;size:40"`
	TenantID             string `gorm:"size:40;not null;index"`
	WorkspaceID          string `gorm:"size:40;not null"`
	ActorID              string `gorm:"size:128;not null"`
	Action               string `gorm:"size:16;not null"`
	FarmID               string `gorm:"size:128;not null"`
	ParcelID             string `gorm:"size:128"`
	ExpectedRevision     int64
	ExpectedFarmRevision int64
	ResultRevision       int64
	RequestHash          string  `gorm:"size:64;not null"`
	Payload              string  `gorm:"type:text;not null"`
	Summary              string  `gorm:"type:text;not null"`
	State                string  `gorm:"size:32;not null"`
	CommandID            string  `gorm:"size:40"`
	IdempotencyHash      *string `gorm:"size:64;uniqueIndex:ux_agri_parcel_idempotency"`
	AuthorizationVersion int64
	ExpiresAt            time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
	ErrorCode            string `gorm:"size:64"`
}

func (AgricultureParcelOperation) TableName() string { return "flow_agriculture_parcel_operations" }

type AgricultureParcelEvent struct {
	Producer         string `gorm:"primaryKey;size:40"`
	EventID          string `gorm:"primaryKey;size:128"`
	TenantID         string `gorm:"size:40;not null"`
	WorkspaceID      string `gorm:"size:40;not null"`
	OperationID      string `gorm:"size:40;not null;uniqueIndex"`
	PayloadHash      string `gorm:"size:64;not null"`
	FarmID           string `gorm:"size:128;not null"`
	ParcelID         string `gorm:"size:128;not null"`
	GeometryRevision int64
	CreatedAt        time.Time
}

func (AgricultureParcelEvent) TableName() string { return "flow_agriculture_parcel_events" }

// A stable ownership reference enables selecting only explicitly granted
// parcels for list authorization. GIS remains authoritative for all facts.
type AgricultureParcelReference struct {
	TenantID    string `gorm:"primaryKey;size:40"`
	WorkspaceID string `gorm:"primaryKey;size:40"`
	ParcelID    string `gorm:"primaryKey;size:128"`
	FarmID      string `gorm:"size:128;not null;index"`
	CreatedAt   time.Time
}

func (AgricultureParcelReference) TableName() string { return "flow_agriculture_parcel_references" }
