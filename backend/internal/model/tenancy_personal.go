package model

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// PersonalTenantID is deterministic so that migration and lazy creation agree.
func PersonalTenantID(userID string) string    { return "tn_u_" + userID }
func PersonalWorkspaceID(userID string) string { return "ws_u_" + userID }

// EnsurePersonalTenantTx creates (if missing) the personal tenant, owner
// membership, default workspace and workspace admin membership for a user.
func EnsurePersonalTenantTx(tx *gorm.DB, userID string) error {
	tid, wid := PersonalTenantID(userID), PersonalWorkspaceID(userID)
	var t Tenant
	err := tx.First(&t, "id = ?", tid).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("读取个人租户：%w", err)
	}
	key := userID
	rows := []any{
		&Tenant{ID: tid, Kind: "personal", Name: "Personal", OwnerUserID: userID, PersonalKey: &key, Status: "active"},
		&Membership{ID: "mb_u_" + userID, TenantID: tid, UserID: userID, Role: "owner", Status: "active"},
		&Workspace{ID: wid, TenantID: tid, Slug: "default", Name: "Default"},
		&WorkspaceMembership{ID: "wm_u_" + userID, WorkspaceID: wid, UserID: userID, Role: "admin"},
	}
	for _, r := range rows {
		if err := tx.Create(r).Error; err != nil {
			return fmt.Errorf("创建个人租户：%w", err)
		}
	}
	return nil
}
