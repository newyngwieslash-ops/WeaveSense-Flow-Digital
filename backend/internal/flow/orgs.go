package flow

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrInviteInvalid = errors.New("flow: invite invalid, expired or already used")

// CreateOrganization creates an organization tenant with the caller as owner,
// a default workspace (caller admin) and a shared billing account paid by the owner.
func CreateOrganization(db *gorm.DB, ownerID, name string) (model.Tenant, model.Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 {
		return model.Tenant{}, model.Workspace{}, errors.New("flow: organization name required")
	}
	t := model.Tenant{ID: NewID("tn_"), Kind: "organization", Name: name, OwnerUserID: ownerID, Status: "active"}
	w := model.Workspace{ID: NewID("ws_"), TenantID: t.ID, Slug: "default", Name: name}
	err := db.Transaction(func(tx *gorm.DB) error {
		for _, r := range []any{&t, &model.Membership{ID: NewID("mb_"), TenantID: t.ID, UserID: ownerID, Role: "owner", Status: "active"}, &w,
			&model.WorkspaceMembership{ID: NewID("wm_"), WorkspaceID: w.ID, UserID: ownerID, Role: "admin"}} {
			if err := tx.Create(r).Error; err != nil {
				return err
			}
		}
		_, err := Billing{DB: tx}.Account(tx, t.ID)
		return err
	})
	return t, w, err
}

func hashToken(tok string) string {
	s := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(s[:])
}

// CreateInvite (workspace admin of an organization tenant) returns the one-time token.
func CreateInvite(db *gorm.DB, scope Scope, inviterID, email, role string, ttl time.Duration) (model.FlowInvite, string, error) {
	if scope.Role != "admin" && scope.Role != "owner" {
		return model.FlowInvite{}, "", ErrForbidden
	}
	var t model.Tenant
	if err := requireActiveWorkspace(db, scope.TenantID, scope.WorkspaceID); err != nil {
		return model.FlowInvite{}, "", err
	}
	if err := db.First(&t, "id = ?", scope.TenantID).Error; err != nil {
		return model.FlowInvite{}, "", err
	}
	if t.Kind != "organization" {
		return model.FlowInvite{}, "", ErrInvalid
	}
	if ttl <= 0 {
		return model.FlowInvite{}, "", ErrInvalid
	}
	switch role {
	case "admin", "editor", "analyst", "viewer":
	default:
		return model.FlowInvite{}, "", ErrInvalid
	}
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return model.FlowInvite{}, "", err
	}
	tok := hex.EncodeToString(b[:])
	inv := model.FlowInvite{ID: NewID("inv_"), TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID, Email: strings.TrimSpace(email), Role: role,
		TokenHash: hashToken(tok), InvitedBy: inviterID, ExpiresAt: time.Now().UTC().Add(ttl), CreatedAt: time.Now().UTC()}
	return inv, tok, db.Create(&inv).Error
}

// AcceptInvite joins the user to the tenant and workspace. Single use.
func AcceptInvite(db *gorm.DB, userID, token string) (Scope, error) {
	var out Scope
	err := db.Transaction(func(tx *gorm.DB) error {
		var inv model.FlowInvite
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&inv, "token_hash = ?", hashToken(token)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrInviteInvalid
			}
			return err
		}
		now := time.Now().UTC()
		if inv.Product != "" || inv.AcceptedAt != nil || inv.RevokedAt != nil || !now.Before(inv.ExpiresAt) {
			return ErrInviteInvalid
		}
		if err := requireActiveWorkspace(tx, inv.TenantID, inv.WorkspaceID); err != nil {
			return err
		}
		var m model.Membership
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&m, "tenant_id = ? AND user_id = ?", inv.TenantID, userID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil && m.Status != "active" {
			return ErrForbidden
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Create(&model.Membership{ID: NewID("mb_"), TenantID: inv.TenantID, UserID: userID, Role: "member", Status: "active"}).Error; err != nil {
				return err
			}
		}
		var wm model.WorkspaceMembership
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&wm, "workspace_id = ? AND user_id = ?", inv.WorkspaceID, userID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			wm = model.WorkspaceMembership{ID: NewID("wm_"), WorkspaceID: inv.WorkspaceID, UserID: userID, Role: inv.Role}
			if err := tx.Create(&wm).Error; err != nil {
				return err
			}
		}
		res := tx.Model(&model.FlowInvite{}).Where("id = ? AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > ?", inv.ID, now).Updates(map[string]any{"accepted_by": userID, "accepted_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrInviteInvalid
		}
		out = Scope{TenantID: inv.TenantID, WorkspaceID: inv.WorkspaceID, Role: wm.Role}
		return nil
	})
	return out, err
}

// RemoveMember removes only this workspace grant. Tenant-wide governance must
// use a separately verified tenant administrator, not a workspace role.
func RemoveMember(db *gorm.DB, scope Scope, userID string) error {
	if scope.Role != "admin" {
		return ErrForbidden
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := requireActiveWorkspace(tx, scope.TenantID, scope.WorkspaceID); err != nil {
			return err
		}
		var member model.Membership
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&member, "tenant_id = ? AND user_id = ?", scope.TenantID, userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if member.Role == "owner" || member.Role == "admin" {
			return ErrForbidden
		}
		if err := tx.Where("user_id = ? AND workspace_id = ?", userID, scope.WorkspaceID).Delete(&model.WorkspaceMembership{}).Error; err != nil {
			return err
		}
		var remaining int64
		if err := tx.Model(&model.WorkspaceMembership{}).Where("user_id = ? AND workspace_id IN (?)", userID,
			tx.Model(&model.Workspace{}).Select("id").Where("tenant_id = ?", scope.TenantID)).Count(&remaining).Error; err != nil {
			return err
		}
		if remaining == 0 {
			return tx.Model(&member).Update("status", "removed").Error
		}
		return nil
	})
}

// AuthorizeGrant re-checks that a schedule's granting actor ("user:<id>") still
// has the scope in that workspace (membership active, tenant active, role allows).
func AuthorizeGrant(db *gorm.DB, actor, tenantID, workspaceID, scope string) error {
	userID, ok := strings.CutPrefix(actor, "user:")
	if !ok || userID == "" {
		return ErrForbidden
	}
	s, err := ResolveScope(db, userID, workspaceID)
	if err != nil || s.TenantID != tenantID || !hasScope(s.Role, scope) {
		return ErrForbidden
	}
	return nil
}
