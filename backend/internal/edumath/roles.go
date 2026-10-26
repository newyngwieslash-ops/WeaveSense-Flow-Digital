package edumath

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// Education role bindings (DEV-013): tenant wide or workspace scoped staff
// roles with an optional end date, explicit revoke with reason, and an audit
// trail. Platform owners and admins may manage bindings but do not gain any
// education role (content review, student records) by doing so.

const maxRoleTTL = 3 * 365 * 24 * time.Hour

var staffRoles = map[string]bool{"teacher": true, "reviewer": true, "org_admin": true}

func roleUser(c *gin.Context) *model.User {
	u, _ := c.Get("edumath.user")
	user, _ := u.(*model.User)
	return user
}

func (h *API) bindRole(c *gin.Context) {
	user := roleUser(c)
	var in struct {
		TenantID    string     `json:"tenantId"`
		UserID      string     `json:"userId"`
		Role        string     `json:"role"`
		WorkspaceID string     `json:"workspaceId"`
		ExpiresAt   *time.Time `json:"expiresAt"`
		Reason      string     `json:"reason"`
	}
	if user == nil || c.ShouldBindJSON(&in) != nil || in.TenantID == "" || in.UserID == "" {
		reply(c, 400, nil, "invalid body")
		return
	}
	tx := reqDB(c, h.DB)
	if !staffRoles[in.Role] {
		reply(c, 400, nil, "role must be teacher, reviewer or org_admin")
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if len([]rune(in.Reason)) > 200 {
		reply(c, 400, nil, "reason up to 200 chars")
		return
	}
	now := time.Now().UTC() // same clock as rolesIn
	if in.ExpiresAt != nil {
		e := in.ExpiresAt.UTC()
		if !e.After(now.Add(time.Minute)) || e.After(now.Add(maxRoleTTL)) {
			reply(c, 400, nil, "expiresAt must be in the future and within 3 years")
			return
		}
		in.ExpiresAt = &e
	}
	if !canManageRoles(tx, in.TenantID, user.ID) {
		reply(c, 403, nil, "forbidden")
		return
	}
	if in.WorkspaceID != "" {
		if in.Role == "org_admin" {
			reply(c, 400, nil, "org_admin is tenant wide and cannot be scoped to a workspace")
			return
		}
		var ws model.Workspace
		if tx.First(&ws, "id = ? AND tenant_id = ?", in.WorkspaceID, in.TenantID).Error != nil {
			reply(c, 400, nil, "workspace does not belong to the tenant")
			return
		}
	}
	var m int64
	tx.Model(&model.Membership{}).Where("tenant_id = ? AND user_id = ? AND status = ?", in.TenantID, in.UserID, "active").Count(&m)
	if m == 0 && !(in.TenantID == model.PersonalTenantID(user.ID) && in.UserID == user.ID) {
		reply(c, 400, nil, "staff roles require tenant membership")
		return
	}
	var b model.EduRoleBinding
	action := "role.bind"
	if tx.Where("tenant_id = ? AND user_id = ? AND role = ?", in.TenantID, in.UserID, in.Role).First(&b).Error == nil {
		action = "role.renew"
		if err := tx.Model(&b).Updates(map[string]any{"status": "active", "workspace_id": in.WorkspaceID, "expires_at": in.ExpiresAt,
			"revoked_by": "", "revoked_at": nil, "reason": in.Reason, "updated_at": now}).Error; err != nil {
			h.fail(c, err)
			return
		}
		tx.First(&b, "id = ?", b.ID)
	} else {
		b = model.EduRoleBinding{ID: flow.NewID("erb_"), TenantID: in.TenantID, UserID: in.UserID, Role: in.Role, WorkspaceID: in.WorkspaceID, Status: "active",
			CreatedBy: user.ID, CreatedAt: now, UpdatedAt: now, ExpiresAt: in.ExpiresAt, Reason: in.Reason}
		if err := tx.Create(&b).Error; err != nil {
			h.fail(c, err)
			return
		}
	}
	detail := in.Role
	if in.WorkspaceID != "" {
		detail += " ws=" + in.WorkspaceID
	}
	if in.ExpiresAt != nil {
		detail += " until=" + in.ExpiresAt.Format(time.RFC3339)
	}
	h.Svc.audit(tx, Actor{UserID: user.ID, TenantID: in.TenantID}, action, in.UserID, detail)
	reply(c, 200, b, "ok")
}

func (h *API) revokeRole(c *gin.Context) {
	user := roleUser(c)
	var in struct {
		TenantID string `json:"tenantId"`
		UserID   string `json:"userId"`
		Role     string `json:"role"`
		Reason   string `json:"reason"`
	}
	if user == nil || c.ShouldBindJSON(&in) != nil || in.TenantID == "" || in.UserID == "" || !staffRoles[in.Role] {
		reply(c, 400, nil, "tenantId, userId and a staff role are required")
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" || len([]rune(in.Reason)) > 200 {
		reply(c, 400, nil, "reason 1..200 chars is required")
		return
	}
	tx := reqDB(c, h.DB)
	if !canManageRoles(tx, in.TenantID, user.ID) {
		reply(c, 403, nil, "forbidden")
		return
	}
	now := h.Svc.now().UTC()
	res := tx.Model(&model.EduRoleBinding{}).Where("tenant_id = ? AND user_id = ? AND role = ? AND status = 'active'", in.TenantID, in.UserID, in.Role).
		Updates(map[string]any{"status": "revoked", "revoked_by": user.ID, "revoked_at": now, "reason": in.Reason, "updated_at": now})
	if res.Error != nil {
		h.fail(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		h.fail(c, ErrNotFound)
		return
	}
	h.Svc.audit(tx, Actor{UserID: user.ID, TenantID: in.TenantID}, "role.revoke", in.UserID, in.Role+" "+in.Reason)
	reply(c, 200, gin.H{"revoked": true}, "ok")
}

func (h *API) listRoles(c *gin.Context) {
	user := roleUser(c)
	tenant := c.Query("tenantId")
	if user == nil || tenant == "" {
		reply(c, 400, nil, "tenantId required")
		return
	}
	tx := reqDB(c, h.DB)
	if !canManageRoles(tx, tenant, user.ID) {
		reply(c, 403, nil, "forbidden")
		return
	}
	var bs []model.EduRoleBinding
	tx.Where("tenant_id = ? AND role IN ?", tenant, []string{"teacher", "reviewer", "org_admin"}).Order("user_id, role").Limit(1000).Find(&bs)
	now := time.Now().UTC()
	out := make([]gin.H, 0, len(bs))
	for _, b := range bs {
		state := b.Status
		if state == "active" && b.ExpiresAt != nil && !b.ExpiresAt.After(now) {
			state = "expired"
		}
		out = append(out, gin.H{"binding": b, "effective": state})
	}
	reply(c, 200, out, "ok")
}
