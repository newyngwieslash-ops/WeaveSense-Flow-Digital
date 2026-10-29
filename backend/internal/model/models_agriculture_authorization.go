package model

import "time"

// AgricultureAuthorizationEpoch invalidates authorization snapshots per tenant.
type AgricultureAuthorizationEpoch struct {
	TenantID  string    `json:"tenantId" gorm:"primaryKey;size:40;not null"`
	Version   int64     `json:"version" gorm:"not null"`
	UpdatedAt time.Time `json:"updatedAt" gorm:"not null"`
}

func (AgricultureAuthorizationEpoch) TableName() string {
	return "flow_agriculture_authorization_epochs"
}

// AgricultureResourceBinding stores authorization references, not GIS facts.
// Capabilities is a canonical JSON array produced by the authorization service;
// receipt metadata identifies the independently confirmed GIS resource version.
type AgricultureResourceBinding struct {
	ID              string     `json:"id" gorm:"primaryKey;size:40"`
	TenantID        string     `json:"tenantId" gorm:"size:40;not null"`
	WorkspaceID     string     `json:"workspaceId" gorm:"size:40;not null"`
	ResourceKind    string     `json:"resourceKind" gorm:"size:24;not null"`
	ResourceID      string     `json:"resourceId" gorm:"size:128;not null"`
	SubjectType     string     `json:"subjectType" gorm:"size:16;not null"`
	SubjectID       string     `json:"subjectId" gorm:"size:36;not null"`
	Capabilities    string     `json:"capabilities" gorm:"type:text;not null"`
	AuthVersion     int64      `json:"authVersion" gorm:"not null"`
	State           string     `json:"state" gorm:"size:16;not null"`
	ExpiresAt       *time.Time `json:"expiresAt,omitempty"`
	ReceiptHash     string     `json:"receiptHash" gorm:"size:64;not null"`
	ReceiptRevision int64      `json:"receiptRevision" gorm:"not null"`
	ConfirmedAt     time.Time  `json:"confirmedAt" gorm:"not null"`
	CreatedAt       time.Time  `json:"createdAt" gorm:"not null"`
	UpdatedAt       time.Time  `json:"updatedAt" gorm:"not null"`
}

func (AgricultureResourceBinding) TableName() string { return "flow_agriculture_resource_bindings" }
