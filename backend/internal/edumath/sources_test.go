package edumath

import (
	"testing"
	"time"

	"yingce/backend/internal/model"
)

func TestSourceRights(t *testing.T) { runSourceRights(t, newHarness(t, startMathService(t))) }

func TestPGSourceRights(t *testing.T) {
	runSourceRights(t, newHarnessDB(t, startMathService(t), pgTestDB(t)))
}

// runSourceRights (DEV-026): a rights record on the problem gates assigning to
// students, every student read (including issued media links) and variant
// generation; expiry and withdrawal take effect on the next request; the
// dependents view lists what relies on the record; changes need a reason and
// are audited; a problem cannot be re-pointed to lift restrictions.
func runSourceRights(t *testing.T, h *harness) {
	tenant := h.setupTeacher()
	pubID, _ := publishG1(t, h)
	stu := []string{"X-Edu-Tenant", tenant}
	var pub model.EduPublication
	var l model.EduLessonRevision
	var pr model.EduProblemRevision
	h.db.First(&pub, "id = ?", pubID)
	h.db.First(&l, "id = ?", pub.LessonRevisionID)
	h.db.First(&pr, "id = ?", l.ProblemRevisionID)
	problemID := pr.ProblemID
	now := time.Now().UTC()

	mk := func(user string, body map[string]any) (int, map[string]any) {
		c, out, _, _ := h.call(user, "POST", "/api/edu/source-assets", body)
		return c, out
	}
	for name, body := range map[string]map[string]any{
		"no attribution": {"title": "教材例题", "kind": "textbook"},
		"bad kind":       {"title": "x", "kind": "pirated", "attribution": "y"},
		"past expiry":    {"title": "x", "kind": "teacher_original", "expiresAt": now.Add(-time.Hour).Format(time.RFC3339)},
		"no title":       {"title": " ", "kind": "teacher_original"},
	} {
		if c, _ := mk("u_alice", body); c != 400 {
			t.Fatalf("%s must be 400, got %d", name, c)
		}
	}
	if c, _ := mk("u_bob", map[string]any{"title": "x", "kind": "teacher_original"}); c != 403 && c != 404 {
		t.Fatalf("a non-staff user must not register sources, got %d", c)
	}
	c, out := mk("u_alice", map[string]any{"title": "人教版必修二 例 3", "kind": "textbook", "attribution": "人民教育出版社", "licenseRef": "校内教学使用"})
	if c != 201 {
		t.Fatalf("create source: %d %v", c, out)
	}
	srcID := data(out)["id"].(string)
	_, out = mk("u_alice", map[string]any{"title": "自编", "kind": "teacher_original", "allowStudent": true})
	other := data(out)["id"].(string)
	link := func(id string) int {
		c, _, _, _ := h.call("u_alice", "PUT", "/api/edu/problems/"+problemID+"/source", map[string]any{"sourceAssetId": id})
		return c
	}
	if link(srcID) != 200 || link(other) != 409 {
		t.Fatalf("link then re-point")
	}

	_, out, _, _ = h.call("u_alice", "POST", "/api/edu/classes", map[string]any{"name": "高一(3)班"})
	classID := data(out)["id"].(string)
	assign := func(key string) (int, map[string]any) {
		c, out, _, _ := h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/assignments", map[string]any{"publicationId": pubID, "title": "例题 " + key})
		return c, out
	}
	if c, out := assign("a"); c != 409 || errCode(out) != "RIGHTS_RESTRICTED" {
		t.Fatalf("assign without student rights: %d %v", c, out)
	}
	if c, out, _, _ := h.call("u_alice", "POST", "/api/edu/lesson-revisions/"+l.ID+"/variant-sets", map[string]any{"count": 2, "idempotencyKey": "vs-rights"}); c != 409 || errCode(out) != "RIGHTS_RESTRICTED" {
		t.Fatalf("variants without derivative rights: %d %v", c, out)
	}
	sr := "/api/edu/source-assets/" + srcID
	if c, _, _, _ := h.call("u_alice", "PUT", sr+"/rights", map[string]any{"allowStudent": true}); c != 400 {
		t.Fatalf("rights change without reason must be 400, got %d", c)
	}
	grant := func(exp time.Time) {
		t.Helper()
		if c, _, raw, _ := h.call("u_alice", "PUT", sr+"/rights", map[string]any{"allowStudent": true, "expiresAt": exp.Format(time.RFC3339), "reason": "出版社书面授权"}); c != 200 {
			t.Fatalf("grant: %d %s", c, raw)
		}
	}
	grant(now.Add(time.Hour))
	c, out = assign("b")
	if c != 201 {
		t.Fatalf("assign with rights: %d %v", c, out)
	}
	asID := data(out)["id"].(string)
	_, out, _, _ = h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/invites", map[string]any{"ttlHours": 24, "maxUses": 5})
	h.call("u_bob", "POST", "/api/edu-join", map[string]any{"code": data(out)["code"]})
	h.call("u_alice", "POST", "/api/edu/consents", map[string]any{"studentUserId": "u_bob", "status": "granted", "evidenceRef": "form-src-1"})
	view := "/api/edu/student/assignments/" + asID
	c, out, raw, _ := h.call("u_bob", "GET", view, nil, stu...)
	if c != 200 {
		t.Fatalf("student view: %d %s", c, raw)
	}
	studentURL := data(out)["preview"].(map[string]any)["url"].(string)

	// Expiry: next request is refused, issued media link too; renewal restores.
	h.db.Model(&model.EduSourceAsset{}).Where("id = ?", srcID).Update("expires_at", now.Add(-time.Minute))
	if c, _, _, _ := h.call("u_bob", "GET", view, nil, stu...); c == 200 {
		t.Fatalf("expired rights must stop student access")
	}
	if c, _, _ := fetch(h, "", studentURL); c == 200 {
		t.Fatalf("expired rights must stop issued media links")
	}
	if c, out, _, _ := h.call("u_alice", "GET", sr, nil); c != 200 || data(out)["live"] != false {
		t.Fatalf("expired source must read not live: %d %v", c, out)
	}
	grant(now.Add(2 * time.Hour))
	if c, _, _, _ := h.call("u_bob", "GET", view, nil, stu...); c != 200 {
		t.Fatalf("renewed rights must restore access, got %d", c)
	}
	// Withdrawal with dependents.
	if c, _, _, _ := h.call("u_alice", "POST", sr+"/withdraw", map[string]any{"reason": ""}); c != 400 {
		t.Fatalf("withdraw without reason must be 400, got %d", c)
	}
	c, out, raw, _ = h.call("u_alice", "POST", sr+"/withdraw", map[string]any{"reason": "授权到期未续"})
	dep, _ := data(out)["dependents"].(map[string]any)
	if c != 200 || len(dep["problems"].([]any)) != 1 || len(dep["publications"].([]any)) != 1 || len(dep["activeAssignments"].([]any)) != 1 ||
		dep["activeAssignments"].([]any)[0] != asID || len(dep["lessonRevisions"].([]any)) < 1 {
		t.Fatalf("withdraw: %d %s", c, raw)
	}
	if c, _, _, _ := h.call("u_bob", "GET", view, nil, stu...); c == 200 {
		t.Fatalf("withdrawn rights must stop student access")
	}
	if c, _, _, _ := h.call("u_alice", "POST", sr+"/withdraw", map[string]any{"reason": "again"}); c != 409 {
		t.Fatalf("second withdraw must be 409, got %d", c)
	}
	if c, _, _, _ := h.call("u_alice", "PUT", sr+"/rights", map[string]any{"allowStudent": true, "reason": "re-grant"}); c != 409 {
		t.Fatalf("a withdrawn source cannot be re-granted, got %d", c)
	}
	if link(other) != 409 {
		t.Fatalf("re-pointing after withdrawal must stay refused")
	}
	if c, _, _, _ := h.call("u_carol", "GET", sr, nil); c != 403 && c != 404 {
		t.Fatalf("another workspace must not read the source, got %d", c)
	}
	var n int64
	h.db.Model(&model.EduAuditEvent{}).Where("tenant_id = ? AND action IN ?", tenant, []string{"source.create", "source.link", "source.rights", "source.withdraw"}).Count(&n)
	if n != 6 { // 2 create + 1 link + 2 rights + 1 withdraw
		t.Fatalf("audit count %d", n)
	}
}
