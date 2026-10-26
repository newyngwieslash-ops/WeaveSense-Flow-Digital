package edumath

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"yingce/backend/internal/model"
)

func TestRoleBindingScopeExpiry(t *testing.T) {
	runRoleBindings(t, newHarness(t, "http://127.0.0.1:1"))
}

func TestPGRoleBindingScopeExpiry(t *testing.T) {
	runRoleBindings(t, newHarnessDB(t, "http://127.0.0.1:1", pgTestDB(t)))
}

// runRoleBindings (DEV-013): staff roles may be scoped to one workspace and end
// on a date; expiry and revoke take effect on the next request without any
// job; revoke needs a reason; every change is audited; a platform admin can
// manage bindings but gains no education role.
func runRoleBindings(t *testing.T, h *harness) {
	tenant := h.setupTeacher()
	h.call("u_alice", "GET", "/api/edu/classes", nil) // creates the personal workspace lazily
	ws := model.PersonalWorkspaceID("u_alice")
	now := time.Now().UTC()
	ws2 := "ws_roles_second"
	h.db.Create(&model.Workspace{ID: ws2, TenantID: tenant, Slug: "roles-second", Name: "二组", CreatedAt: now})
	for i, w := range []string{ws, ws2} {
		h.db.Create(&model.WorkspaceMembership{ID: "wm_rc" + string(rune('a'+i)), WorkspaceID: w, UserID: "u_carol", Role: "editor", CreatedAt: now})
		h.db.Create(&model.WorkspaceMembership{ID: "wm_rd" + string(rune('a'+i)), WorkspaceID: w, UserID: "u_dave", Role: "editor", CreatedAt: now})
	}
	h.db.Create(&model.Membership{ID: "m_rcarol", TenantID: tenant, UserID: "u_carol", Role: "member", Status: "active", CreatedAt: now})
	h.db.Create(&model.Membership{ID: "m_rdave", TenantID: tenant, UserID: "u_dave", Role: "admin", Status: "active", CreatedAt: now})
	in1 := []string{"X-Workspace-Id", ws}
	in2 := []string{"X-Workspace-Id", ws2}
	staff := func(user string, hdr []string) int {
		t.Helper()
		c, _, _, _ := h.call(user, "GET", "/api/edu/classes", nil, hdr...)
		return c
	}
	bind := func(user string, body map[string]any) (int, []byte) {
		t.Helper()
		body["tenantId"] = tenant
		c, _, raw, _ := h.call(user, "POST", "/api/edu/roles", body)
		return c, raw
	}

	for name, body := range map[string]map[string]any{
		"past expiry":       {"userId": "u_carol", "role": "teacher", "expiresAt": now.Add(-time.Hour).Format(time.RFC3339)},
		"too long":          {"userId": "u_carol", "role": "teacher", "expiresAt": now.Add(4 * 365 * 24 * time.Hour).Format(time.RFC3339)},
		"scoped org_admin":  {"userId": "u_carol", "role": "org_admin", "workspaceId": ws},
		"foreign workspace": {"userId": "u_carol", "role": "teacher", "workspaceId": model.PersonalWorkspaceID("u_bob")},
		"student role":      {"userId": "u_carol", "role": "student"},
	} {
		if c, raw := bind("u_alice", body); c != 400 {
			t.Fatalf("%s must be 400: %d %s", name, c, raw)
		}
	}
	if c, _ := bind("u_carol", map[string]any{"userId": "u_carol", "role": "org_admin"}); c != 403 {
		t.Fatalf("a non-admin must not bind roles, got %d", c)
	}

	// Workspace scoped teacher: works in ws, not in ws2.
	if c, raw := bind("u_alice", map[string]any{"userId": "u_carol", "role": "teacher", "workspaceId": ws, "reason": "高二数学"}); c != 200 {
		t.Fatalf("scoped bind: %d %s", c, raw)
	}
	if staff("u_carol", in1) != 200 || staff("u_carol", in2) != 403 {
		t.Fatalf("scoped role: ws=%d ws2=%d", staff("u_carol", in1), staff("u_carol", in2))
	}
	// Renew with an end date, then let it lapse.
	if c, raw := bind("u_alice", map[string]any{"userId": "u_carol", "role": "teacher", "expiresAt": now.Add(time.Hour).Format(time.RFC3339)}); c != 200 {
		t.Fatalf("renew: %d %s", c, raw)
	}
	if staff("u_carol", in2) != 200 {
		t.Fatalf("renewed tenant wide role must work in ws2")
	}
	h.db.Model(&model.EduRoleBinding{}).Where("tenant_id = ? AND user_id = ? AND role = ?", tenant, "u_carol", "teacher").
		Update("expires_at", now.Add(-time.Minute))
	if staff("u_carol", in1) != 403 {
		t.Fatalf("an expired role must be refused on the next request")
	}
	_, out, _, _ := h.call("u_alice", "POST", "/api/edu/classes", map[string]any{"name": "高三(2)班"})
	classID := data(out)["id"].(string)
	if c, _, _, _ := h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/teachers", map[string]any{"userId": "u_carol"}); c != 400 {
		t.Fatalf("an expired teacher cannot be given a class, got %d", c)
	}
	c, out, raw, _ := h.call("u_alice", "GET", "/api/edu/roles?tenantId="+tenant, nil)
	eff := map[string]string{}
	for _, x := range out["data"].([]any) {
		m := x.(map[string]any)
		b := m["binding"].(map[string]any)
		eff[b["userId"].(string)+"/"+b["role"].(string)] = m["effective"].(string)
	}
	if c != 200 || eff["u_carol/teacher"] != "expired" || eff["u_alice/teacher"] != "active" {
		t.Fatalf("list roles: %d %s", c, raw)
	}
	if c, _ := bind("u_alice", map[string]any{"userId": "u_carol", "role": "teacher"}); c != 200 || staff("u_carol", in1) != 200 {
		t.Fatalf("renew without end date must restore access")
	}
	// Revoke needs a reason and takes effect at once.
	rv := func(body map[string]any) int {
		body["tenantId"] = tenant
		c, _, _, _ := h.call("u_alice", "POST", "/api/edu/roles/revoke", body)
		return c
	}
	if rv(map[string]any{"userId": "u_carol", "role": "teacher"}) != 400 {
		t.Fatalf("revoke without reason must be 400")
	}
	if rv(map[string]any{"userId": "u_carol", "role": "teacher", "reason": "调岗"}) != 200 || staff("u_carol", in1) != 403 {
		t.Fatalf("revoke must take effect")
	}
	if rv(map[string]any{"userId": "u_carol", "role": "teacher", "reason": "again"}) != 404 {
		t.Fatalf("second revoke must be 404")
	}
	if c, _, _, _ := h.call("u_carol", "GET", "/api/edu/roles?tenantId="+tenant, nil); c != 403 {
		t.Fatalf("a non-admin must not list roles, got %d", c)
	}
	// A platform admin manages bindings but holds no education role.
	if c, _, _, _ := h.call("u_dave", "GET", "/api/edu/roles?tenantId="+tenant, nil); c != 200 {
		t.Fatalf("platform admin lists roles, got %d", c)
	}
	if staff("u_dave", in1) != 403 {
		t.Fatalf("platform admin must not gain staff access implicitly")
	}
	var b model.EduRoleBinding
	h.db.First(&b, "tenant_id = ? AND user_id = ? AND role = ?", tenant, "u_carol", "teacher")
	if b.Status != "revoked" || b.RevokedBy != "u_alice" || b.RevokedAt == nil || b.Reason != "调岗" {
		t.Fatalf("revoke trail: %+v", b)
	}
	countAudit := func(db *gorm.DB, action string) int64 {
		var n int64
		db.Model(&model.EduAuditEvent{}).Where("tenant_id = ? AND action = ? AND subject = ?", tenant, action, "u_carol").Count(&n)
		return n
	}
	if countAudit(h.db, "role.bind") != 1 || countAudit(h.db, "role.renew") != 2 || countAudit(h.db, "role.revoke") != 1 {
		t.Fatalf("audit: bind=%d renew=%d revoke=%d", countAudit(h.db, "role.bind"), countAudit(h.db, "role.renew"), countAudit(h.db, "role.revoke"))
	}
}
