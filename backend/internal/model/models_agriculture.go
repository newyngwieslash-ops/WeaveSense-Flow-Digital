package model

import "time"

// AgricultureRoleBinding augments the existing tenant/workspace memberships.
// An empty WorkspaceID is tenant governance, never an all-farms permission.
type AgricultureRoleBinding struct {
	ID          string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID    string    `json:"tenantId" gorm:"size:40;not null;uniqueIndex:ux_agriculture_role_binding"`
	WorkspaceID string    `json:"workspaceId" gorm:"size:40;not null;default:'';uniqueIndex:ux_agriculture_role_binding"`
	UserID      string    `json:"userId" gorm:"size:36;not null;uniqueIndex:ux_agriculture_role_binding"`
	Role        string    `json:"role" gorm:"size:24;not null;uniqueIndex:ux_agriculture_role_binding"`
	CreatedAt   time.Time `json:"createdAt" gorm:"not null"`
}

func (AgricultureRoleBinding) TableName() string { return "flow_agriculture_role_bindings" }

// AgricultureAuditEvent contains policy/action evidence, not invitation secrets.
// Detail must be serialized from an allowlisted change summary; never include
// email addresses, tokens, credentials, request bodies or signed URLs.
type AgricultureAuditEvent struct {
	ID          string    `json:"id" gorm:"primaryKey;size:40"`
	TenantID    string    `json:"tenantId" gorm:"size:40;not null;index"`
	WorkspaceID string    `json:"workspaceId" gorm:"size:40;not null;default:''"`
	ActorID     string    `json:"actorId" gorm:"size:36;not null"`
	Action      string    `json:"action" gorm:"size:64;not null"`
	SubjectID   string    `json:"subjectId" gorm:"size:80;not null"`
	Detail      string    `json:"detail" gorm:"type:text;not null;default:'{}'"`
	CreatedAt   time.Time `json:"createdAt" gorm:"not null;index"`
}

func (AgricultureAuditEvent) TableName() string { return "flow_agriculture_audit_events" }
