package edumath

import (
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/model"
)

// TestClassesInvitesConsentAuthorization covers the W1 server rules without the
// math service: class visibility per teaching assignment, admin-only teacher
// assignment / consent / policy, invite secret/expiry/uses/revocation, and that
// only published publications of the same workspace can be assigned.
func TestClassesInvitesConsentAuthorization(t *testing.T) {
	h := newHarness(t, "http://127.0.0.1:1")
	tenant := h.setupTeacher()
	ws := model.PersonalWorkspaceID("u_alice")
	now := time.Now().UTC()
	// u_carol: tenant member + workspace member + teacher role, but not an admin.
	h.db.Create(&model.Membership{ID: "m_carol", TenantID: tenant, UserID: "u_carol", Role: "member", Status: "active", CreatedAt: now})
	h.db.Create(&model.WorkspaceMembership{ID: "wm_carol", WorkspaceID: ws, UserID: "u_carol", Role: "editor", CreatedAt: now})
	if code, _, raw, _ := h.call("u_alice", "POST", "/api/edu/roles", map[string]any{"tenantId": tenant, "userId": "u_carol", "role": "teacher"}); code != 200 {
		t.Fatalf("bind carol: %d %s", code, raw)
	}
	carol := []string{"X-Workspace-Id", ws}

	code, out, raw, _ := h.call("u_alice", "POST", "/api/edu/classes", map[string]any{"name": "高二(1)班"})
	if code != 201 {
		t.Fatalf("class: %d %s", code, raw)
	}
	classID := data(out)["id"].(string)
	// Carol does not teach the class: invisible, cannot invite, cannot read roster.
	_, out, _, _ = h.call("u_carol", "GET", "/api/edu/classes", nil, carol...)
	if l, _ := out["data"].([]any); len(l) != 0 {
		t.Fatalf("carol must not list alice's class: %v", out["data"])
	}
	for _, p := range []string{"/api/edu/classes/" + classID + "/invites"} {
		if code, _, _, _ := h.call("u_carol", "POST", p, map[string]any{}, carol...); code != 404 {
			t.Fatalf("non-teaching teacher on %s must be 404, got %d", p, code)
		}
	}
	if code, _, _, _ := h.call("u_carol", "GET", "/api/edu/classes/"+classID+"/roster", nil, carol...); code != 404 {
		t.Fatalf("roster for non-teaching teacher must be 404, got %d", code)
	}
	// Teacher-only actor cannot assign teachers, record consent, or change policy.
	if code, _, _, _ := h.call("u_carol", "POST", "/api/edu/classes/"+classID+"/teachers", map[string]any{"userId": "u_carol"}, carol...); code != 403 {
		t.Fatalf("teacher self-assigning must be 403, got %d", code)
	}
	if code, _, _, _ := h.call("u_carol", "POST", "/api/edu/consents", map[string]any{"studentUserId": "u_bob", "status": "granted", "evidenceRef": "tick"}, carol...); code != 403 {
		t.Fatalf("teacher recording consent must be 403, got %d", code)
	}
	if code, _, _, _ := h.call("u_carol", "PUT", "/api/edu/policy", map[string]any{"reviewMode": "self_allowed", "consentMode": "not_applicable", "reason": "x y z"}, carol...); code != 403 {
		t.Fatalf("teacher changing policy must be 403, got %d", code)
	}
	// Admin (tenant owner) assigns carol; now she sees the class.
	if code, _, raw, _ := h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/teachers", map[string]any{"userId": "u_carol"}); code != 200 {
		t.Fatalf("assign carol: %d %s", code, raw)
	}
	_, out, _, _ = h.call("u_carol", "GET", "/api/edu/classes", nil, carol...)
	if l, _ := out["data"].([]any); len(l) != 1 {
		t.Fatalf("carol must now list the class: %v", out["data"])
	}

	// Invites: max uses, expiry, revocation, malformed codes.
	_, out, _, _ = h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/invites", map[string]any{"ttlHours": 1, "maxUses": 1})
	one := data(out)["code"].(string)
	if strings.Contains(raw2(out), "secretHash") {
		t.Fatal("secret hash must not be returned")
	}
	if code, _, raw, _ := h.call("u_bob", "POST", "/api/edu-join", map[string]any{"code": one}); code != 200 {
		t.Fatalf("join: %d %s", code, raw)
	}
	if code, _, _, _ := h.call("u_bob", "POST", "/api/edu-join", map[string]any{"code": one}); code != 200 {
		t.Fatalf("re-join while active must be idempotent, got %d", code)
	}
	h.db.Create(&model.User{ID: "u_dan", Username: "u_dan"})
	if code, _, _, _ := h.call("u_dan", "POST", "/api/edu-join", map[string]any{"code": one}); code != 404 {
		t.Fatalf("exhausted invite must be 404, got %d", code)
	}
	_, out, _, _ = h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/invites", map[string]any{"ttlHours": 1, "maxUses": 5})
	exp := data(out)["code"].(string)
	h.now.Store(time.Now().Add(2 * time.Hour).Unix())
	if code, _, _, _ := h.call("u_dan", "POST", "/api/edu-join", map[string]any{"code": exp}); code != 404 {
		t.Fatalf("expired invite must be 404, got %d", code)
	}
	h.now.Store(0)
	_, out, _, _ = h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/invites", map[string]any{})
	rv := data(out)["code"].(string)
	invID := data(out)["invite"].(map[string]any)["id"].(string)
	if code, _, _, _ := h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/invites/"+invID+"/revoke", nil); code != 200 {
		t.Fatalf("revoke: %d", code)
	}
	for _, c := range []string{rv, "garbage", "eci_x.y", ""} {
		if code, _, _, _ := h.call("u_dan", "POST", "/api/edu-join", map[string]any{"code": c}); code != 404 && code != 400 {
			t.Fatalf("invalid code %q must be refused, got %d", c, code)
		}
	}
	if code, _, _, _ := h.call("", "POST", "/api/edu-join", map[string]any{"code": rv}); code != 401 {
		t.Fatalf("anonymous join must be 401, got %d", code)
	}
	// Bob is a student only: no workspace membership, staff endpoints refused.
	var n int64
	h.db.Model(&model.WorkspaceMembership{}).Where("user_id = ?", "u_bob").Where("workspace_id = ?", ws).Count(&n)
	if n != 0 {
		t.Fatal("student must not become a workspace member")
	}
	if code, _, _, _ := h.call("u_bob", "GET", "/api/edu/classes", nil, "X-Workspace-Id", ws); code != 403 {
		t.Fatalf("student listing classes must be 403, got %d", code)
	}
	// Roster shows consent state; consent needs evidence; only for students of this tenant.
	_, out, _, _ = h.call("u_alice", "GET", "/api/edu/classes/"+classID+"/roster", nil)
	ro, _ := out["data"].([]any)
	if len(ro) != 1 || ro[0].(map[string]any)["consentOk"] != false {
		t.Fatalf("roster: %v", out["data"])
	}
	if code, _, _, _ := h.call("u_alice", "POST", "/api/edu/consents", map[string]any{"studentUserId": "u_dan", "status": "granted", "evidenceRef": "form-1"}); code != 404 {
		t.Fatalf("consent for a non-student must be 404, got %d", code)
	}
	// Policy: not_applicable requires a reason and is audited.
	if code, _, _, _ := h.call("u_alice", "PUT", "/api/edu/policy", map[string]any{"reviewMode": "self_allowed", "consentMode": "not_applicable"}); code != 400 {
		t.Fatalf("not_applicable without reason must be 400, got %d", code)
	}
	if code, _, raw, _ := h.call("u_alice", "PUT", "/api/edu/policy", map[string]any{"reviewMode": "second_reviewer", "consentMode": "not_applicable", "reason": "成人继续教育机构"}); code != 200 {
		t.Fatalf("policy: %d %s", code, raw)
	}
	_, out, _, _ = h.call("u_alice", "GET", "/api/edu/classes/"+classID+"/roster", nil)
	if out["data"].([]any)[0].(map[string]any)["consentOk"] != true {
		t.Fatal("not_applicable must lift the consent gate")
	}
	var audits int64
	h.db.Model(&model.EduAuditEvent{}).Where("action = ?", "policy.set").Count(&audits)
	if audits != 1 {
		t.Fatal("policy change must be audited")
	}

	// Assignments only from published publications of the class workspace.
	if code, _, _, _ := h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/assignments", map[string]any{"publicationId": "epu_missing"}); code != 409 {
		t.Fatalf("unpublished must be 409, got %d", code)
	}
	h.db.Create(&model.EduMathJob{ID: "ej_other", TenantID: tenant, WorkspaceID: "ws_other", Kind: "lesson_build", IdemKey: "k", InputHash: "h", ProblemRevisionID: "r", Status: "succeeded", CreatedAt: now})
	h.db.Create(&model.EduLessonRevision{ID: "elr_other", TenantID: tenant, ProblemRevisionID: "r", SolutionRevisionID: "s", JobID: "ej_other", LessonHash: "h", State: "approved", CreatedAt: now})
	h.db.Create(&model.EduPublication{ID: "epu_other", TenantID: tenant, LessonRevisionID: "elr_other", LessonHash: "h", ReviewRecordID: "x", State: "published", CreatedAt: now})
	if code, _, _, _ := h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/assignments", map[string]any{"publicationId": "epu_other"}); code != 404 {
		t.Fatalf("publication from another workspace must be 404, got %d", code)
	}
}

func raw2(m map[string]any) string {
	var b strings.Builder
	for k := range data(m)["invite"].(map[string]any) {
		b.WriteString(k + ",")
	}
	return b.String()
}
