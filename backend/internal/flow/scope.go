package flow

import (
	"errors"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
)

var ErrForbidden = errors.New("flow: workspace not accessible")

// Scope is the resolved tenant/workspace for an authenticated user.
type Scope struct {
	TenantID    string `json:"tenantId"`
	WorkspaceID string `json:"workspaceId"`
	Role        string `json:"role"`
}

// ResolveScope maps an authenticated user to a workspace. An empty workspaceID
// selects the personal default workspace (created lazily for new users).
// Access requires an active tenant membership AND a workspace membership.
func ResolveScope(db *gorm.DB, userID, workspaceID string) (Scope, error) {
	if userID == "" {
		return Scope{}, ErrForbidden
	}
	if workspaceID == "" {
		if err := db.Transaction(func(tx *gorm.DB) error { return model.EnsurePersonalTenantTx(tx, userID) }); err != nil {
			return Scope{}, err
		}
		workspaceID = model.PersonalWorkspaceID(userID)
	}
	var row struct {
		TenantID string
		Role     string
		Product  string
	}
	err := db.Table("flow_workspaces w").
		Select("w.tenant_id, wm.role, w.product").
		Joins("JOIN flow_workspace_memberships wm ON wm.workspace_id = w.id AND wm.user_id = ?", userID).
		Joins("JOIN flow_memberships m ON m.tenant_id = w.tenant_id AND m.user_id = ? AND m.status = 'active'", userID).
		Joins("JOIN flow_tenants t ON t.id = w.tenant_id AND t.status = 'active'").
		Where("w.id = ?", workspaceID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Scope{}, ErrForbidden
	}
	if err != nil {
		return Scope{}, err
	}
	if row.Product != "" {
		// Product-specific capabilities must be resolved by that product's
		// authorization service, never interpreted as legacy intelligence roles.
		return Scope{}, ErrForbidden
	}
	return Scope{TenantID: row.TenantID, WorkspaceID: workspaceID, Role: row.Role}, nil
}

// ListWorkspaces returns the workspaces a user can access.
func ListWorkspaces(db *gorm.DB, userID string) ([]model.Workspace, error) {
	if userID == "" {
		return nil, ErrForbidden
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return model.EnsurePersonalTenantTx(tx, userID) }); err != nil {
		return nil, err
	}
	var out []model.Workspace
	err := db.Table("flow_workspaces w").Select("w.*").
		Joins("JOIN flow_workspace_memberships wm ON wm.workspace_id = w.id AND wm.user_id = ?", userID).
		Joins("JOIN flow_memberships m ON m.tenant_id = w.tenant_id AND m.user_id = ? AND m.status = 'active'", userID).
		Joins("JOIN flow_tenants t ON t.id = w.tenant_id AND t.status = 'active'").
		Order("w.created_at").Find(&out).Error
	return out, err
}

func requireActiveWorkspace(db *gorm.DB, tenantID, workspaceID string) error {
	if tenantID == "" || workspaceID == "" {
		return ErrForbidden
	}
	var workspace model.Workspace
	err := db.Table("flow_workspaces w").Select("w.*").
		Joins("JOIN flow_tenants t ON t.id = w.tenant_id AND t.status = 'active'").
		Where("w.id = ? AND w.tenant_id = ?", workspaceID, tenantID).Take(&workspace).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrForbidden
	}
	if err == nil && workspace.Product != "" {
		return ErrForbidden
	}
	return err
}

// ScopesForRole maps workspace roles to Engine scopes.
func ScopesForRole(role string) []string {
	read := []string{"intel.sources.read", "intel.documents.read", "intel.operations.read"}
	switch role {
	case "owner", "admin", "editor", "analyst":
		return append(read, "intel.sources.write", "intel.monitors.write", "intel.schedules.write", "intel.collect", "intel.reports.write", "intel.research", "intel.mcp.use")
	}
	return read
}
