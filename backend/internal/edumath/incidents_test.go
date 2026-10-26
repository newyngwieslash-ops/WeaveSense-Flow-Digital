package edumath

import (
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

func TestContentIncidentChain(t *testing.T) { runIncidents(t, newHarness(t, startMathService(t))) }

func TestPGContentIncidentChain(t *testing.T) {
	runIncidents(t, newHarnessDB(t, startMathService(t), pgTestDB(t)))
}

// runIncidents (DEV-027, AC-34; DEV-025): a blocking content incident withdraws
// the publication, suspends its tasks (attempts kept), revokes student media so
// an already issued link stops working, and records the impact; the withdrawn
// version cannot be republished, reassigned or re-reviewed; a fix must be an
// edit of that revision with a fresh human review (inherited approval refused);
// the task then migrates to the fix with old attempts left in their context.
func runIncidents(t *testing.T, h *harness) {
	tenant := h.setupTeacher()
	pubID, _ := publishG1(t, h)
	stu := []string{"X-Edu-Tenant", tenant}
	var pub model.EduPublication
	h.db.First(&pub, "id = ?", pubID)
	l0 := pub.LessonRevisionID
	lr := "/api/edu/lesson-revisions/"

	_, out, _, _ := h.call("u_alice", "POST", "/api/edu/classes", map[string]any{"name": "高二(7)班"})
	classID := data(out)["id"].(string)
	code, out, raw, _ := h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/assignments", map[string]any{"publicationId": pubID, "title": "线面角", "answerPolicy": "after_submission"})
	if code != 201 {
		t.Fatalf("assignment: %d %s", code, raw)
	}
	asA := data(out)["id"].(string)
	_, out, _, _ = h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/invites", map[string]any{"ttlHours": 24, "maxUses": 5})
	h.call("u_bob", "POST", "/api/edu-join", map[string]any{"code": data(out)["code"]})
	h.call("u_alice", "POST", "/api/edu/consents", map[string]any{"studentUserId": "u_bob", "status": "granted", "evidenceRef": "form-inc-1"})
	pA := "/api/edu/student/assignments/" + asA
	code, out, raw, _ = h.call("u_bob", "GET", pA, nil, stu...)
	if code != 200 {
		t.Fatalf("student view: %d %s", code, raw)
	}
	studentURL := data(out)["preview"].(map[string]any)["url"].(string)
	if code, _, raw, _ := h.call("u_bob", "POST", pA+"/attempts", map[string]any{"kind": "answer", "response": "1", "clientKey": "bob-inc-0001"}, stu...); code != 200 && code != 201 {
		t.Fatalf("attempt: %d %s", code, raw)
	}

	inc := "/api/edu/publications/" + pubID + "/incidents"
	if code, _, _, _ := h.call("u_bob", "POST", inc, map[string]any{"severity": "blocking", "reason": "x"}); code != 403 && code != 404 {
		t.Fatalf("a student must not open incidents, got %d", code)
	}
	if code, _, _, _ := h.call("u_carol", "POST", inc, map[string]any{"severity": "blocking", "reason": "x"}); code != 403 && code != 404 {
		t.Fatalf("another workspace must not open incidents, got %d", code)
	}
	for _, bad := range []map[string]any{{"severity": "urgent", "reason": "x"}, {"severity": "minor", "reason": " "}, {"severity": "minor", "reason": strings.Repeat("长", 401)}} {
		if code, _, _, _ := h.call("u_alice", "POST", inc, bad); code != 400 {
			t.Fatalf("bad incident %v must be 400, got %d", bad, code)
		}
	}
	// Minor: recorded with impact, nothing frozen; repeated report is idempotent.
	code, out, raw, _ = h.call("u_alice", "POST", inc, map[string]any{"severity": "minor", "reason": "板书措辞不准确"})
	if code != 201 {
		t.Fatalf("minor: %d %s", code, raw)
	}
	incID := data(out)["incident"].(map[string]any)["id"].(string)
	if c, o, _, _ := h.call("u_alice", "POST", inc, map[string]any{"severity": "minor", "reason": "again"}); c != 200 || data(o)["incident"].(map[string]any)["id"] != incID {
		t.Fatalf("minor replay: %d %v", c, o)
	}
	if code, _, _, _ := h.call("u_bob", "GET", pA, nil, stu...); code != 200 {
		t.Fatalf("minor must not freeze access, got %d", code)
	}
	// Escalate to blocking: the whole chain runs on the same incident.
	code, out, raw, _ = h.call("u_alice", "POST", inc, map[string]any{"severity": "blocking", "reason": "答案错误：线面角应重新计算"})
	if code != 201 || data(out)["incident"].(map[string]any)["id"] != incID || data(out)["incident"].(map[string]any)["severity"] != "blocking" {
		t.Fatalf("blocking: %d %s", code, raw)
	}
	imp := data(out)["impact"].(map[string]any)
	if len(imp["assignments"].([]any)) != 1 || imp["students"].(float64) != 1 || imp["attempts"].(float64) < 1 || len(imp["revokedArtifacts"].([]any)) < 1 ||
		imp["classes"].([]any)[0] != classID {
		t.Fatalf("impact: %v", imp)
	}
	var p2 model.EduPublication
	var l0r model.EduLessonRevision
	var as model.EduAssignment
	h.db.First(&p2, "id = ?", pubID)
	h.db.First(&l0r, "id = ?", l0)
	h.db.First(&as, "id = ?", asA)
	if p2.State != "withdrawn" || l0r.State != "withdrawn" || as.State != "suspended" {
		t.Fatalf("withdrawal chain: pub=%s lesson=%s assignment=%s", p2.State, l0r.State, as.State)
	}
	var revoked int64
	h.db.Model(&model.EduArtifact{}).Where("id IN ? AND revoked_at IS NOT NULL", []string{l0r.StudentArtifact, l0r.StudentPreview}).Count(&revoked)
	if revoked != 2 {
		t.Fatalf("student artifacts must be revoked, got %d", revoked)
	}
	if code, _, _ := fetch(h, "", studentURL); code == 200 {
		t.Fatalf("an issued student link must stop working after withdrawal")
	}
	if code, _, _, _ := h.call("u_bob", "GET", pA, nil, stu...); code == 200 {
		t.Fatalf("suspended assignment must not be served to students")
	}
	if code, _, _, _ := h.call("u_alice", "GET", "/api/edu/artifacts/"+l0r.PreviewArtifact+"/url", nil); code != 200 {
		t.Fatalf("teacher keeps the teacher preview for the investigation, got %d", code)
	}
	var attempts int64
	h.db.Model(&model.EduAttempt{}).Where("assignment_id = ?", asA).Count(&attempts)
	if attempts < 1 {
		t.Fatalf("attempts must be kept")
	}
	// The withdrawn version is frozen.
	if c, o, _, _ := h.call("u_alice", "POST", lr+l0+"/publish", map[string]any{"lessonHash": l0r.LessonHash}); c != 409 || errCode(o) != "POLICY_REVOKED" {
		t.Fatalf("republish withdrawn: %d %v", c, o)
	}
	if c, _, _, _ := h.call("u_alice", "POST", lr+l0+"/review", map[string]any{"lessonHash": l0r.LessonHash, "decision": "approved", "comment": "再审"}); c == 200 {
		t.Fatalf("a withdrawn revision must not be re-approved")
	}
	if c, _, _, _ := h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/assignments", map[string]any{"publicationId": pubID, "title": "再布置"}); c != 409 {
		t.Fatalf("assigning a withdrawn publication must be 409, got %d", c)
	}
	// Resolution needs a published, freshly reviewed descendant.
	ir := "/api/edu/incidents/" + incID
	if c, _, _, _ := h.call("u_alice", "POST", ir+"/resolve", map[string]any{"note": ""}); c != 400 {
		t.Fatalf("resolve without note must be 400, got %d", c)
	}
	if c, _, _, _ := h.call("u_alice", "POST", ir+"/resolve", map[string]any{"note": "已改"}); c != 409 {
		t.Fatalf("blocking resolve without fix must be 409, got %d", c)
	}
	if c, _, _, _ := h.call("u_alice", "POST", ir+"/resolve", map[string]any{"note": "已改", "fixPublicationId": pubID}); c != 409 {
		t.Fatalf("the withdrawn publication is not a fix, got %d", c)
	}
	// Even under the pronunciation_only rule the withdrawn parent passes on no approval.
	h.db.Save(&model.EduOrgPolicy{TenantID: tenant, ReviewMode: "self_allowed", ConsentMode: "guardian_required", EditReviewInherit: "pronunciation_only"})
	edit := func(base, hash, key string, e any) model.EduLessonRevision {
		t.Helper()
		c, o, raw, _ := h.call("u_alice", "POST", lr+base+"/edits", map[string]any{"baseLessonHash": hash, "edits": e, "idempotencyKey": key, "saveAs": "branch"})
		if c != 202 {
			t.Fatalf("edit %s: %d %s", key, c, raw)
		}
		done := h.waitJob("u_alice", data(o)["job"].(map[string]any)["id"].(string))
		var r model.EduLessonRevision
		h.db.First(&r, "id = ?", done["lessonRevisionId"])
		if r.ID == "" {
			t.Fatalf("edit %s produced no revision: %v", key, done)
		}
		return r
	}
	_, out, _, _ = h.call("u_alice", "GET", lr+l0+"/editable", nil)
	b0 := data(out)["beats"].([]any)[0].(map[string]any)["id"].(string)
	pron := []map[string]any{{"target": "pronunciation", "term": "棱", "reading": "léng"}}
	f1 := edit(l0, l0r.LessonHash, "fix-1", []map[string]any{{"target": "beat", "id": b0, "field": "say", "value": "重新计算线面角。"}})
	if f1.State != "ready_for_review" || f1.ParentLessonRevisionID != l0 {
		t.Fatalf("fix revision: %+v", f1)
	}
	fp := edit(l0, l0r.LessonHash, "fix-pron", pron)
	if fp.State != "ready_for_review" || fp.ApprovalInheritedFrom != "" {
		t.Fatalf("a withdrawn parent must not pass on approval: %+v", fp)
	}
	publish := func(r model.EduLessonRevision) string {
		t.Helper()
		if r.State != "approved" {
			if c, _, raw, _ := h.call("u_alice", "POST", lr+r.ID+"/review", map[string]any{"lessonHash": r.LessonHash, "decision": "approved", "comment": "重审修正"}); c != 200 {
				t.Fatalf("review fix: %d %s", c, raw)
			}
		}
		c, o, raw, _ := h.call("u_alice", "POST", lr+r.ID+"/publish", map[string]any{"lessonHash": r.LessonHash})
		if c != 200 {
			t.Fatalf("publish fix: %d %s", c, raw)
		}
		return data(o)["id"].(string)
	}
	fixPub := publish(f1)
	// A pronunciation edit of the reviewed fix inherits approval; it descends from the incident revision but cannot close it.
	var f1r model.EduLessonRevision
	h.db.First(&f1r, "id = ?", f1.ID)
	inh := edit(f1.ID, f1r.LessonHash, "fix-inh", pron)
	if inh.State != "approved" || inh.ApprovalInheritedFrom != f1.ID {
		t.Fatalf("inherited child: %+v", inh)
	}
	inhPub := publish(inh)
	if c, o, _, _ := h.call("u_alice", "POST", ir+"/resolve", map[string]any{"note": "继承审核", "fixPublicationId": inhPub}); c != 409 || !strings.Contains(string(mustJSON(o)), "fresh human review") {
		t.Fatalf("inherited approval must not close an incident: %d %v", c, o)
	}
	if c, _, _, _ := h.call("u_carol", "POST", ir+"/resolve", map[string]any{"note": "x", "fixPublicationId": fixPub}); c != 403 && c != 404 {
		t.Fatalf("another workspace must not resolve, got %d", c)
	}
	code, out, raw, _ = h.call("u_alice", "POST", ir+"/resolve", map[string]any{"note": "已重算并重审", "fixPublicationId": fixPub})
	if code != 200 || data(out)["incident"].(map[string]any)["state"] != "resolved" || data(out)["incident"].(map[string]any)["fixPublicationId"] != fixPub {
		t.Fatalf("resolve: %d %s", code, raw)
	}
	if c, _, _, _ := h.call("u_alice", "POST", ir+"/resolve", map[string]any{"note": "again", "fixPublicationId": fixPub}); c != 409 {
		t.Fatalf("second resolve must be 409, got %d", c)
	}
	if c, o, _, _ := h.call("u_alice", "GET", "/api/edu/incidents?state=resolved", nil); c != 200 || len(o["data"].([]any)) != 1 {
		t.Fatalf("list incidents: %d %v", c, o)
	}
	if c, _, _, _ := h.call("u_carol", "GET", ir, nil); c != 403 && c != 404 {
		t.Fatalf("another workspace must not read the incident, got %d", c)
	}

	// Migration (DEV-025): preview first, then confirm; old attempts stay put.
	mg := "/api/edu/assignments/" + asA + "/migrate"
	if c, _, _, _ := h.call("u_alice", "POST", mg, map[string]any{"publicationId": pubID, "reason": "x", "confirm": true}); c != 409 {
		t.Fatalf("migrating to a withdrawn publication must be 409, got %d", c)
	}
	if c, _, _, _ := h.call("u_alice", "POST", mg, map[string]any{"publicationId": fixPub, "reason": ""}); c != 400 {
		t.Fatalf("migration without reason must be 400, got %d", c)
	}
	if c, _, _, _ := h.call("u_bob", "POST", mg, map[string]any{"publicationId": fixPub, "reason": "x"}); c != 403 && c != 404 {
		t.Fatalf("a student must not migrate, got %d", c)
	}
	code, out, raw, _ = h.call("u_alice", "POST", mg, map[string]any{"publicationId": fixPub, "reason": "内容事故修正"})
	pv := data(out)
	if code != 200 || pv["confirmed"] != false || pv["affectedStudents"].(float64) != 1 || pv["attemptsKept"].(float64) != float64(attempts) || pv["incidentId"] != incID {
		t.Fatalf("migration preview: %d %s", code, raw)
	}
	h.db.First(&as, "id = ?", asA)
	if as.State != "suspended" {
		t.Fatalf("preview must not change anything: %s", as.State)
	}
	code, out, raw, _ = h.call("u_alice", "POST", mg, map[string]any{"publicationId": fixPub, "reason": "内容事故修正", "confirm": true})
	if code != 201 || data(out)["toAssignmentId"] == nil {
		t.Fatalf("migrate: %d %s", code, raw)
	}
	asB := data(out)["toAssignmentId"].(string)
	if c, _, _, _ := h.call("u_alice", "POST", mg, map[string]any{"publicationId": fixPub, "reason": "again", "confirm": true}); c != 409 {
		t.Fatalf("second migration must be 409, got %d", c)
	}
	h.db.First(&as, "id = ?", asA)
	var nb model.EduAssignment
	h.db.First(&nb, "id = ?", asB)
	var kept int64
	h.db.Model(&model.EduAttempt{}).Where("assignment_id = ?", asA).Count(&kept)
	if as.State != "migrated" || nb.State != "active" || nb.PublicationID != fixPub || nb.AnswerPolicy != "after_submission" || kept != attempts {
		t.Fatalf("migration result: old=%s new=%+v kept=%d", as.State, nb, kept)
	}
	if c, _, raw, _ := h.call("u_bob", "GET", "/api/edu/student/assignments/"+asB, nil, stu...); c != 200 {
		t.Fatalf("student sees the migrated task: %d %s", c, raw)
	}
	if c, o, _, _ := h.call("u_alice", "GET", "/api/edu/assignments/"+asB+"/migrations", nil); c != 200 || len(o["data"].([]any)) != 1 {
		t.Fatalf("migrations: %d %v", c, o)
	}
	var audits int64
	h.db.Model(&model.EduAuditEvent{}).Where("action IN ?", []string{"incident.open.minor", "incident.escalate", "incident.resolve", "assignment.migrate"}).Count(&audits)
	if audits != 4 {
		t.Fatalf("audit trail: %d", audits)
	}
}
