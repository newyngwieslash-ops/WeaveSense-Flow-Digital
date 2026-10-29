package agriculture

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/mail"
	"sort"
	"strings"
	"time"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInvalid    = errors.New("agriculture: invalid request")
	ErrForbidden  = errors.New("agriculture: forbidden")
	ErrNotFound   = errors.New("agriculture: unavailable resource")
	ErrConflict   = errors.New("agriculture: conflict")
	ErrInvitation = errors.New("agriculture: invalid invitation")
)

type Service struct {
	DB  *gorm.DB
	Now func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

type WorkspaceView struct {
	ID                    string   `json:"id"`
	TenantID              string   `json:"tenantId"`
	Name                  string   `json:"name"`
	Slug                  string   `json:"slug"`
	TenantName            string   `json:"tenantName"`
	TenantStatus          string   `json:"tenantStatus"`
	Roles                 []string `json:"roles"`
	CanManageMembers      bool     `json:"canManageMembers"`
	CanManageOrganization bool     `json:"canManageOrganization"`
}
type OrganizationResult struct {
	Tenant    model.Tenant  `json:"tenant"`
	Workspace WorkspaceView `json:"workspace"`
}
type MemberView struct {
	UserID  string   `json:"userId"`
	Name    string   `json:"name"`
	Roles   []string `json:"roles"`
	Status  string   `json:"status"`
	IsOwner bool     `json:"isOwner"`
}
type MembersResult struct {
	Members         []MemberView `json:"members"`
	AssignableRoles []string     `json:"assignableRoles"`
}
type InviteResult struct {
	Invite model.FlowInvite `json:"invite"`
	Token  string           `json:"token"`
}
type AcceptResult struct {
	TenantID    string   `json:"tenantId"`
	WorkspaceID string   `json:"workspaceId"`
	Roles       []string `json:"roles"`
}
type workspaceContext struct {
	Tenant    model.Tenant
	Workspace model.Workspace
	Owner     bool
	Roles     []string
}

func activeActor(tx *gorm.DB, actorID string) (model.User, error) {
	var actor model.User
	if actorID == "" {
		return actor, ErrForbidden
	}
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status = ?", actorID, model.UserStatusActive).First(&actor).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return actor, ErrForbidden
	}
	return actor, err
}

func rolesFor(tx *gorm.DB, tenantID, workspaceID, userID string) ([]string, error) {
	var rows []model.AgricultureRoleBinding
	err := tx.Where("tenant_id = ? AND user_id = ? AND (workspace_id = ? OR workspace_id = '')", tenantID, userID, workspaceID).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, row := range rows {
		if (governanceRole(row.Role) && row.WorkspaceID == "") || (!governanceRole(row.Role) && row.WorkspaceID == workspaceID) {
			out = append(out, row.Role)
		}
	}
	sort.Strings(out)
	return out, nil
}

func resolve(tx *gorm.DB, actorID, workspaceID string, allowDisabledOwner bool) (workspaceContext, error) {
	var out workspaceContext
	if err := tx.Where("id = ? AND product = ?", workspaceID, Product).First(&out.Workspace).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return out, ErrNotFound
		}
		return out, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", out.Workspace.TenantID).First(&out.Tenant).Error; err != nil {
		return out, err
	}
	if _, err := activeActor(tx, actorID); err != nil {
		return out, err
	}
	var membership model.Membership
	if err := tx.Where("tenant_id = ? AND user_id = ? AND status = 'active'", out.Tenant.ID, actorID).First(&membership).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return out, ErrNotFound
		}
		return out, err
	}
	out.Owner = out.Tenant.OwnerUserID == actorID && membership.Role == "owner"
	if out.Tenant.Status != "active" && !(allowDisabledOwner && out.Owner) {
		return out, ErrNotFound
	}
	var count int64
	if err := tx.Model(&model.WorkspaceMembership{}).Where("workspace_id = ? AND user_id = ?", workspaceID, actorID).Count(&count).Error; err != nil {
		return out, err
	}
	if count != 1 {
		return out, ErrNotFound
	}
	var err error
	out.Roles, err = rolesFor(tx, out.Tenant.ID, workspaceID, actorID)
	return out, err
}

// Only a server-resolved workspace supplies tenant context. Admission paths
// have their own exact scope and never fall back to an unscoped transaction.
func (s Service) withWorkspace(ctx context.Context, actorID, workspaceID string, allowDisabledOwner bool, fn func(*gorm.DB, workspaceContext) error) error {
	var w model.Workspace
	if err := s.DB.WithContext(ctx).Where("id = ? AND product = ?", workspaceID, Product).First(&w).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	return flow.TenantTx(s.DB.WithContext(ctx), w.TenantID, func(tx *gorm.DB) error {
		scope, err := resolve(tx, actorID, workspaceID, allowDisabledOwner)
		if err != nil {
			return err
		}
		return fn(tx, scope)
	})
}

func view(scope workspaceContext) WorkspaceView {
	return WorkspaceView{ID: scope.Workspace.ID, TenantID: scope.Tenant.ID, Name: scope.Workspace.Name, Slug: scope.Workspace.Slug, TenantName: scope.Tenant.Name, TenantStatus: scope.Tenant.Status, Roles: scope.Roles, CanManageMembers: scope.Tenant.Status == "active" && (scope.Owner || hasRole(scope.Roles, "tenant_admin")), CanManageOrganization: scope.Owner}
}

func audit(tx *gorm.DB, now time.Time, scope workspaceContext, actor, action, subject string, detail any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	return tx.Create(&model.AgricultureAuditEvent{ID: flow.NewID("aga_"), TenantID: scope.Tenant.ID, WorkspaceID: scope.Workspace.ID, ActorID: actor, Action: action, SubjectID: subject, Detail: string(raw), CreatedAt: now}).Error
}

func (s Service) ListWorkspaces(ctx context.Context, actorID string) ([]WorkspaceView, error) {
	if _, err := activeActor(s.DB.WithContext(ctx), actorID); err != nil {
		return nil, err
	}
	var candidates []model.Workspace
	err := s.DB.WithContext(ctx).Table("flow_workspaces w").Select("w.*").Joins("JOIN flow_workspace_memberships wm ON wm.workspace_id=w.id AND wm.user_id=?", actorID).Where("w.product=?", Product).Order("w.created_at,w.id").Find(&candidates).Error
	if err != nil {
		return nil, err
	}
	out := []WorkspaceView{}
	for _, w := range candidates {
		err = s.withWorkspace(ctx, actorID, w.ID, true, func(tx *gorm.DB, scope workspaceContext) error { out = append(out, view(scope)); return nil })
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s Service) CreateOrganization(ctx context.Context, actorID, name, key string) (OrganizationResult, error) {
	var out OrganizationResult
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 || len(key) < 16 || len(key) > 128 || strings.ContainsAny(key, "\r\n\x00") {
		return out, ErrInvalid
	}
	digest := sha256.Sum256([]byte(actorID + "\x00" + key))
	suffix := hex.EncodeToString(digest[:16])
	// The existing billing account prefixes tenant IDs with "ba_" in varchar(40).
	tenantID, workspaceID := "tn_a_"+suffix, "ws_ag_"+suffix
	err := flow.TenantTx(s.DB.WithContext(ctx), tenantID, func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", tenantID).Error; err != nil {
				return err
			}
		}
		var existing model.Tenant
		err := tx.Where("id=?", tenantID).First(&existing).Error
		if err == nil {
			if existing.OwnerUserID != actorID || existing.Name != name {
				return ErrConflict
			}
			scope, e := resolve(tx, actorID, workspaceID, true)
			if e != nil {
				return e
			}
			out = OrganizationResult{Tenant: existing, Workspace: view(scope)}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if _, err := activeActor(tx, actorID); err != nil {
			return err
		}
		t := model.Tenant{ID: tenantID, Kind: "organization", Name: name, OwnerUserID: actorID, Status: "active"}
		w := model.Workspace{ID: workspaceID, TenantID: tenantID, Slug: "default", Name: name, Product: Product}
		for _, row := range []any{&t, &model.Membership{ID: flow.NewID("mb_"), TenantID: tenantID, UserID: actorID, Role: "owner", Status: "active"}, &w, &model.WorkspaceMembership{ID: flow.NewID("wm_"), WorkspaceID: workspaceID, UserID: actorID, Role: "viewer"}} {
			if err := tx.Create(row).Error; err != nil {
				return err
			}
		}
		if _, err := (flow.Billing{DB: tx}).Account(tx, tenantID); err != nil {
			return err
		}
		scope := workspaceContext{Tenant: t, Workspace: w, Owner: true, Roles: []string{}}
		if err := audit(tx, s.now(), scope, actorID, "organization.created", tenantID, map[string]string{"product": Product}); err != nil {
			return err
		}
		out = OrganizationResult{Tenant: t, Workspace: view(scope)}
		return nil
	})
	return out, err
}

func normalizedEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || len(value) > 160 {
		return "", ErrInvalid
	}
	return value, nil
}

func (s Service) CreateInvite(ctx context.Context, actorID, workspaceID, email, role string) (InviteResult, error) {
	var out InviteResult
	roles, err := normalizeRoles([]string{role})
	if err != nil {
		return out, err
	}
	email, err = normalizedEmail(email)
	if err != nil {
		return out, err
	}
	err = s.withWorkspace(ctx, actorID, workspaceID, false, func(tx *gorm.DB, scope workspaceContext) error {
		if !scope.Owner && !hasRole(scope.Roles, "tenant_admin") {
			return ErrForbidden
		}
		if governanceRole(roles[0]) && !scope.Owner {
			return ErrForbidden
		}
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return err
		}
		token := hex.EncodeToString(b[:])
		h := sha256.Sum256([]byte(token))
		now := s.now()
		inv := model.FlowInvite{ID: flow.NewID("inv_"), TenantID: scope.Tenant.ID, WorkspaceID: workspaceID, Email: email, Role: roles[0], Product: Product, TokenHash: hex.EncodeToString(h[:]), InvitedBy: actorID, ExpiresAt: now.Add(7 * 24 * time.Hour), CreatedAt: now}
		if err := tx.Create(&inv).Error; err != nil {
			return err
		}
		if err := audit(tx, now, scope, actorID, "invite.created", inv.ID, map[string]string{"role": roles[0]}); err != nil {
			return err
		}
		out = InviteResult{Invite: inv, Token: token}
		return nil
	})
	return out, err
}

func (s Service) AcceptInvite(ctx context.Context, actorID, token string) (AcceptResult, error) {
	var out AcceptResult
	if len(token) != 64 {
		return out, ErrInvitation
	}
	if _, err := hex.DecodeString(token); err != nil {
		return out, ErrInvitation
	}
	h := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(h[:])
	var reference struct {
		InviteID        string
		TenantID        string
		WorkspaceID     string
		RecipientUserID string
	}
	db := s.DB.WithContext(ctx)
	if db.Dialector.Name() == "postgres" {
		err := db.Raw("SELECT invite_id, tenant_id, workspace_id, recipient_user_id FROM agri_admission.resolve_invite(?, ?)", hash, actorID).Scan(&reference).Error
		if err != nil {
			return out, err
		}
	} else {
		// SQLite is an explicitly local development fixture, not PostgreSQL RLS proof.
		actor, err := activeActor(db, actorID)
		if err != nil {
			return out, err
		}
		if actor.EmailVerifiedAt == nil {
			return out, ErrInvitation
		}
		var inv model.FlowInvite
		err = db.Where("token_hash=? AND product=?", hash, Product).First(&inv).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return out, ErrInvitation
		}
		if err != nil {
			return out, err
		}
		if !strings.EqualFold(strings.TrimSpace(actor.Email), inv.Email) {
			return out, ErrInvitation
		}
		reference.InviteID, reference.TenantID, reference.WorkspaceID, reference.RecipientUserID = inv.ID, inv.TenantID, inv.WorkspaceID, actorID
	}
	if reference.InviteID == "" || reference.TenantID == "" || reference.RecipientUserID != actorID {
		return out, ErrInvitation
	}
	err := flow.TenantTx(db, reference.TenantID, func(tx *gorm.DB) error {
		var tenant model.Tenant
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status='active'", reference.TenantID).First(&tenant).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrInvitation
			}
			return err
		}
		var inv model.FlowInvite
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND tenant_id=? AND workspace_id=? AND token_hash=? AND product=?", reference.InviteID, reference.TenantID, reference.WorkspaceID, hash, Product).First(&inv).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrInvitation
			}
			return err
		}
		// Lock both principals in stable order before rechecking them; reciprocal
		// invitations in different tenants must not acquire user locks backwards.
		var principals []model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ?", []string{actorID, inv.InvitedBy}).Order("id").Find(&principals).Error; err != nil {
			return err
		}
		actor, err := activeActor(tx, actorID)
		if err != nil {
			return err
		}
		if actor.EmailVerifiedAt == nil || !strings.EqualFold(strings.TrimSpace(actor.Email), inv.Email) || inv.AcceptedAt != nil || inv.RevokedAt != nil || !s.now().Before(inv.ExpiresAt) {
			return ErrInvitation
		}
		roles, err := normalizeRoles([]string{inv.Role})
		if err != nil {
			return ErrInvitation
		}
		inviter, err := resolve(tx, inv.InvitedBy, inv.WorkspaceID, false)
		if err != nil {
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
				return ErrInvitation
			}
			return err
		}
		if (!inviter.Owner && !hasRole(inviter.Roles, "tenant_admin")) || (governanceRole(roles[0]) && !inviter.Owner) {
			return ErrInvitation
		}
		var member model.Membership
		err = tx.Where("tenant_id=? AND user_id=?", tenant.ID, actorID).First(&member).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			member = model.Membership{ID: flow.NewID("mb_"), TenantID: tenant.ID, UserID: actorID, Role: "member", Status: "active"}
			if err = tx.Create(&member).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if member.Status != "active" {
			return ErrInvitation
		}
		var wm model.WorkspaceMembership
		err = tx.Where("workspace_id=? AND user_id=?", inv.WorkspaceID, actorID).First(&wm).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err = tx.Create(&model.WorkspaceMembership{ID: flow.NewID("wm_"), WorkspaceID: inv.WorkspaceID, UserID: actorID, Role: "viewer"}).Error; err != nil {
				return err
			}
			bindingWorkspace := inv.WorkspaceID
			if governanceRole(roles[0]) {
				bindingWorkspace = ""
			}
			binding := model.AgricultureRoleBinding{ID: flow.NewID("agr_"), TenantID: tenant.ID, WorkspaceID: bindingWorkspace, UserID: actorID, Role: roles[0]}
			if err = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&binding).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		// Existing workspace access never gains a stronger role from a stale invite.
		now := s.now()
		res := tx.Model(&model.FlowInvite{}).Where("id=? AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > ?", inv.ID, now).Updates(map[string]any{"accepted_at": now, "accepted_by": actorID})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrInvitation
		}
		actual, err := rolesFor(tx, tenant.ID, inv.WorkspaceID, actorID)
		if err != nil {
			return err
		}
		if err = audit(tx, now, inviter, actorID, "invite.accepted", inv.ID, map[string]any{"roles": actual}); err != nil {
			return err
		}
		out = AcceptResult{TenantID: tenant.ID, WorkspaceID: inv.WorkspaceID, Roles: actual}
		return nil
	})
	return out, err
}

func (s Service) Members(ctx context.Context, actorID, workspaceID string) (MembersResult, error) {
	out := MembersResult{Members: []MemberView{}, AssignableRoles: []string{}}
	err := s.withWorkspace(ctx, actorID, workspaceID, false, func(tx *gorm.DB, scope workspaceContext) error {
		if !scope.Owner && !hasRole(scope.Roles, "tenant_admin") {
			return ErrForbidden
		}
		for _, role := range AssignableRoles {
			if scope.Owner || !governanceRole(role) {
				out.AssignableRoles = append(out.AssignableRoles, role)
			}
		}
		var members []model.Membership
		if err := tx.Where("tenant_id=?", scope.Tenant.ID).Order("created_at,id").Find(&members).Error; err != nil {
			return err
		}
		for _, member := range members {
			var user model.User
			if err := tx.Select("id", "display_name").Where("id=?", member.UserID).First(&user).Error; err != nil {
				return err
			}
			roles, err := rolesFor(tx, scope.Tenant.ID, workspaceID, member.UserID)
			if err != nil {
				return err
			}
			out.Members = append(out.Members, MemberView{UserID: member.UserID, Name: user.DisplayName, Roles: roles, Status: member.Status, IsOwner: member.Role == "owner" && scope.Tenant.OwnerUserID == member.UserID})
		}
		return nil
	})
	return out, err
}

func (s Service) ChangeMember(ctx context.Context, actorID, workspaceID, targetID string, roles *[]string, status *string) error {
	if (roles == nil) == (status == nil) {
		return ErrInvalid
	}
	var canonical []string
	var err error
	if roles != nil {
		canonical, err = normalizeRoles(*roles)
		if err != nil {
			return err
		}
	}
	if status != nil && *status != "active" && *status != "suspended" && *status != "removed" {
		return ErrInvalid
	}
	return s.withWorkspace(ctx, actorID, workspaceID, false, func(tx *gorm.DB, scope workspaceContext) error {
		if !scope.Owner && !hasRole(scope.Roles, "tenant_admin") {
			return ErrForbidden
		}
		var target model.Membership
		if err := tx.Where("tenant_id=? AND user_id=?", scope.Tenant.ID, targetID).First(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if target.Role == "owner" || targetID == scope.Tenant.OwnerUserID || targetID == actorID {
			return ErrForbidden
		}
		current, err := rolesFor(tx, scope.Tenant.ID, workspaceID, targetID)
		if err != nil {
			return err
		}
		if !scope.Owner {
			for _, r := range append(append([]string{}, current...), canonical...) {
				if governanceRole(r) {
					return ErrForbidden
				}
			}
		}
		if roles != nil {
			if target.Status != "active" {
				return ErrConflict
			}
			var n int64
			if err := tx.Model(&model.WorkspaceMembership{}).Where("workspace_id=? AND user_id=?", workspaceID, targetID).Count(&n).Error; err != nil {
				return err
			}
			if n != 1 {
				return ErrNotFound
			}
			if err := tx.Where("tenant_id=? AND user_id=? AND (workspace_id=? OR workspace_id='')", scope.Tenant.ID, targetID, workspaceID).Delete(&model.AgricultureRoleBinding{}).Error; err != nil {
				return err
			}
			for _, role := range canonical {
				wid := workspaceID
				if governanceRole(role) {
					wid = ""
				}
				if err := tx.Create(&model.AgricultureRoleBinding{ID: flow.NewID("agr_"), TenantID: scope.Tenant.ID, WorkspaceID: wid, UserID: targetID, Role: role}).Error; err != nil {
					return err
				}
			}
			return audit(tx, s.now(), scope, actorID, "member.roles_changed", targetID, map[string]any{"roles": canonical})
		}
		if err := tx.Model(&model.Membership{}).Where("id=?", target.ID).Update("status", *status).Error; err != nil {
			return err
		}
		if *status == "removed" {
			var user model.User
			if err := tx.Select("email").Where("id=?", targetID).First(&user).Error; err != nil {
				return err
			}
			if email := strings.ToLower(strings.TrimSpace(user.Email)); email != "" {
				if err := tx.Model(&model.FlowInvite{}).Where("tenant_id=? AND product=? AND lower(trim(email))=? AND accepted_at IS NULL AND revoked_at IS NULL", scope.Tenant.ID, Product, email).Update("revoked_at", s.now()).Error; err != nil {
					return err
				}
			}
			if err := tx.Where("tenant_id=? AND user_id=?", scope.Tenant.ID, targetID).Delete(&model.AgricultureRoleBinding{}).Error; err != nil {
				return err
			}
			if err := tx.Where("user_id=? AND workspace_id IN (?)", targetID, tx.Model(&model.Workspace{}).Select("id").Where("tenant_id=?", scope.Tenant.ID)).Delete(&model.WorkspaceMembership{}).Error; err != nil {
				return err
			}
		}
		return audit(tx, s.now(), scope, actorID, "member.status_changed", targetID, map[string]string{"status": *status})
	})
}

func (s Service) ChangeOrganizationStatus(ctx context.Context, actorID, workspaceID, status string) error {
	if status != "active" && status != "disabled" {
		return ErrInvalid
	}
	return s.withWorkspace(ctx, actorID, workspaceID, true, func(tx *gorm.DB, scope workspaceContext) error {
		if !scope.Owner {
			return ErrForbidden
		}
		if err := tx.Model(&model.Tenant{}).Where("id=?", scope.Tenant.ID).Update("status", status).Error; err != nil {
			return err
		}
		return audit(tx, s.now(), scope, actorID, "organization.status_changed", scope.Tenant.ID, map[string]string{"status": status})
	})
}

func (s Service) RevokeInvite(ctx context.Context, actorID, workspaceID, inviteID string) error {
	return s.withWorkspace(ctx, actorID, workspaceID, false, func(tx *gorm.DB, scope workspaceContext) error {
		if !scope.Owner && !hasRole(scope.Roles, "tenant_admin") {
			return ErrForbidden
		}
		var inv model.FlowInvite
		if err := tx.Where("id=? AND tenant_id=? AND workspace_id=? AND product=?", inviteID, scope.Tenant.ID, workspaceID, Product).First(&inv).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if inv.AcceptedAt != nil {
			return ErrConflict
		}
		if governanceRole(inv.Role) && !scope.Owner {
			return ErrForbidden
		}
		if inv.RevokedAt == nil {
			if err := tx.Model(&inv).Update("revoked_at", s.now()).Error; err != nil {
				return err
			}
			return audit(tx, s.now(), scope, actorID, "invite.revoked", inviteID, map[string]string{})
		}
		return nil
	})
}
