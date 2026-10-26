package edumath

import (
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/model"
)

func TestRosterImport(t *testing.T) { runRosterImport(t, newHarness(t, "http://127.0.0.1:1")) }

func TestPGRosterImport(t *testing.T) {
	runRosterImport(t, newHarnessDB(t, "http://127.0.0.1:1", pgTestDB(t)))
}

// runRosterImport (DEV-016): CSV roster import is previewed then confirmed;
// only a pseudonymous reference and short display name are accepted, values
// that look like phones, ID cards or e-mail are refused; each row gets a
// single-use expiring claim code (hash only stored); claiming enrolls the user
// as a student without any workspace membership; revoked, reused, expired or
// tampered codes fail; other workspaces cannot import or read the roster.
func runRosterImport(t *testing.T, h *harness) {
	tenant := h.setupTeacher()
	_, out, _, _ := h.call("u_alice", "POST", "/api/edu/classes", map[string]any{"name": "高二(3)班"})
	classID := data(out)["id"].(string)
	imp := "/api/edu/classes/" + classID + "/roster-import"

	for _, bad := range []string{"", "姓名,手机\nA,1", "displayName\n小明"} {
		if c, _, _, _ := h.call("u_alice", "POST", imp, map[string]any{"csv": bad}); c != 400 {
			t.Fatalf("bad csv %q must be 400, got %d", bad, c)
		}
	}
	if c, _, _, _ := h.call("u_carol", "POST", imp, map[string]any{"csv": "externalRef\nS1"}); c != 403 && c != 404 {
		t.Fatalf("another workspace must not import, got %d", c)
	}
	dirty := "\ufeff学号,显示名\nS001,小一\nS002,13812345678\nS003,a@b.cn\n110101200001011234,x\nS001,重复\nbad ref,y\n"
	c, out, raw, _ := h.call("u_alice", "POST", imp, map[string]any{"csv": dirty})
	if c != 200 || data(out)["new"].(float64) != 1 || data(out)["invalid"].(float64) != 5 {
		t.Fatalf("preview with invalid rows: %d %s", c, raw)
	}
	if c, _, _, _ := h.call("u_alice", "POST", imp, map[string]any{"csv": dirty, "confirm": true}); c != 409 {
		t.Fatalf("confirm with invalid rows must be 409, got %d", c)
	}
	var n int64
	h.db.Model(&model.EduRosterEntry{}).Count(&n)
	if n != 0 {
		t.Fatalf("preview and refused confirm must write nothing: %d", n)
	}
	clean := "externalRef,displayName\nS001,小一\nS002,小二\nS003,\n"
	c, out, raw, _ = h.call("u_alice", "POST", imp, map[string]any{"csv": clean, "confirm": true, "ttlHours": 48})
	if c != 201 || len(data(out)["codes"].([]any)) != 3 {
		t.Fatalf("import: %d %s", c, raw)
	}
	codes := map[string]string{}
	ids := map[string]string{}
	for _, x := range data(out)["codes"].([]any) {
		m := x.(map[string]any)
		codes[m["externalRef"].(string)], ids[m["externalRef"].(string)] = m["code"].(string), m["entryId"].(string)
	}
	c, out, raw, _ = h.call("u_alice", "POST", imp, map[string]any{"csv": clean + "S004,小四\n", "confirm": true})
	if c != 201 || data(out)["existing"].(float64) != 3 || len(data(out)["codes"].([]any)) != 1 {
		t.Fatalf("re-import must only add new rows: %d %s", c, raw)
	}
	c, out, raw, _ = h.call("u_alice", "GET", "/api/edu/classes/"+classID+"/roster-entries", nil)
	if c != 200 || len(out["data"].([]any)) != 4 || strings.Contains(string(raw), "secretHash") || strings.Contains(string(raw), strings.SplitN(codes["S001"], ".", 2)[1]) {
		t.Fatalf("roster entries: %d %s", c, raw)
	}
	if c, _, _, _ := h.call("u_carol", "GET", "/api/edu/classes/"+classID+"/roster-entries", nil); c != 403 && c != 404 {
		t.Fatalf("another workspace must not read the roster, got %d", c)
	}

	claim := "/api/edu-roster-claim"
	if c, _, _, _ := h.call("", "POST", claim, map[string]any{"code": codes["S001"]}); c != 401 {
		t.Fatalf("anonymous claim must be 401, got %d", c)
	}
	tampered := codes["S001"][:len(codes["S001"])-1] + map[bool]string{true: "0", false: "1"}[!strings.HasSuffix(codes["S001"], "0")]
	if c, _, _, _ := h.call("u_bob", "POST", claim, map[string]any{"code": tampered}); c != 404 {
		t.Fatalf("tampered code must be 404, got %d", c)
	}
	c, out, raw, _ = h.call("u_bob", "POST", claim, map[string]any{"code": codes["S001"]})
	if c != 200 || data(out)["classId"] != classID || data(out)["externalRef"] != "S001" {
		t.Fatalf("claim: %d %s", c, raw)
	}
	if c, _, _, _ := h.call("u_dan", "POST", claim, map[string]any{"code": codes["S001"]}); c != 404 {
		t.Fatalf("a used code must be 404, got %d", c)
	}
	if c, _, _, _ := h.call("u_bob", "POST", claim, map[string]any{"code": codes["S002"]}); c != 403 {
		t.Fatalf("one roster row per student per class, got %d", c)
	}
	var wm int64
	h.db.Model(&model.WorkspaceMembership{}).Where("user_id = ? AND workspace_id = ?", "u_bob", model.PersonalWorkspaceID("u_alice")).Count(&wm)
	var enr model.EduEnrollment
	h.db.First(&enr, "class_id = ? AND student_user_id = ?", classID, "u_bob")
	if wm != 0 || enr.Status != "active" {
		t.Fatalf("claim must enroll without workspace membership: wm=%d enr=%+v", wm, enr)
	}
	stu := []string{"X-Edu-Tenant", tenant}
	if c, _, _, _ := h.call("u_bob", "GET", "/api/edu/student/assignments", nil, stu...); c != 403 {
		t.Fatalf("consent still gates the claimed student, got %d", c)
	}
	h.call("u_alice", "POST", "/api/edu/consents", map[string]any{"studentUserId": "u_bob", "status": "granted", "evidenceRef": "form-roster-1"})
	if c, _, raw, _ := h.call("u_bob", "GET", "/api/edu/student/assignments", nil, stu...); c != 200 {
		t.Fatalf("claimed student context: %d %s", c, raw)
	}
	if c, o, raw, _ := h.call("u_alice", "GET", "/api/edu/classes/"+classID+"/roster", nil); c != 200 || o["data"].([]any)[0].(map[string]any)["externalRef"] != "S001" {
		t.Fatalf("teacher roster shows the reference: %d %s", c, raw)
	}
	// Revoke and reissue.
	ent := "/api/edu/classes/" + classID + "/roster-entries/"
	if c, _, _, _ := h.call("u_alice", "POST", ent+ids["S002"]+"/revoke", nil); c != 200 {
		t.Fatalf("revoke: %d", c)
	}
	if c, _, _, _ := h.call("u_dan", "POST", claim, map[string]any{"code": codes["S002"]}); c != 404 {
		t.Fatalf("a revoked code must be 404, got %d", c)
	}
	if c, _, _, _ := h.call("u_alice", "POST", ent+ids["S001"]+"/revoke", nil); c != 409 {
		t.Fatalf("a claimed entry cannot be revoked here, got %d", c)
	}
	c, out, raw, _ = h.call("u_alice", "POST", ent+ids["S003"]+"/reissue", nil)
	if c != 200 {
		t.Fatalf("reissue: %d %s", c, raw)
	}
	fresh := data(out)["code"].(string)
	if c, _, _, _ := h.call("u_dan", "POST", claim, map[string]any{"code": codes["S003"]}); c != 404 {
		t.Fatalf("the old code must stop working after reissue, got %d", c)
	}
	// Expiry: the fresh code is pushed into the past.
	h.db.Model(&model.EduRosterEntry{}).Where("id = ?", ids["S003"]).Update("expires_at", time.Now().UTC().Add(-time.Minute))
	if c, _, _, _ := h.call("u_dan", "POST", claim, map[string]any{"code": fresh}); c != 404 {
		t.Fatalf("an expired code must be 404, got %d", c)
	}
	var dan int64
	h.db.Model(&model.EduEnrollment{}).Where("student_user_id = ?", "u_dan").Count(&dan)
	if dan != 0 {
		t.Fatalf("a failed claim must not leave an enrollment: %d", dan)
	}
	var audits int64
	h.db.Model(&model.EduAuditEvent{}).Where("action IN ?", []string{"class.roster.import", "class.roster.claim", "class.roster.revoke", "class.roster.reissue"}).Count(&audits)
	if audits != 5 {
		t.Fatalf("audit trail: %d", audits)
	}
}
