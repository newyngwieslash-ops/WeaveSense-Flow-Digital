package edumath

import (
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

// TestLessonEdits covers FR-T05/T06 and AC-17/18 on the real math service: structured field edits with
// an impact preview and a cost bound, partial regeneration into a NEW child revision (base and its
// publication unchanged), the head lock with conflict details and the caller's edits echoed back, save
// as branch, idempotent replay, release of a failed edit's head, and the review inheritance rule
// (default none; pronunciation_only inherits only for a pronunciation-only edit of an approved parent).
func TestLessonEdits(t *testing.T) { runEdits(t, newHarness(t, startMathService(t))) }

func runEdits(t *testing.T, h *harness) {
	tenant := h.setupTeacher()
	pubID, tl := publishG1(t, h)
	var pub model.EduPublication
	h.db.First(&pub, "id = ?", pubID)
	l0 := pub.LessonRevisionID
	lr := "/api/edu/lesson-revisions/"

	c, out, raw, _ := h.call("u_alice", "GET", lr+l0+"/editable", nil)
	if c != 200 {
		t.Fatalf("editable: %d %s", c, raw)
	}
	ed := data(out)
	beats := ed["beats"].([]any)
	b0 := beats[0].(map[string]any)["id"].(string)
	h0 := ed["lessonHash"].(string)
	var openHint float64 = -1
	for _, x := range ed["hints"].([]any) {
		hm := x.(map[string]any)
		if hm["kind"] != "answer" && openHint < 0 {
			openHint = hm["level"].(float64)
		}
	}
	if c, _, _, _ := h.call("u_bob", "GET", lr+l0+"/editable", nil); c != 403 && c != 404 {
		t.Fatalf("other users must not read the editable lesson, got %d", c)
	}

	edits := []map[string]any{{"target": "beat", "id": b0, "field": "say", "value": "先看底面。"}, {"target": "beat", "id": b0, "field": "board", "value": []string{"连接 AC"}}}
	c, out, raw, _ = h.call("u_alice", "POST", lr+l0+"/edit-impact", map[string]any{"edits": edits})
	imp, _ := data(out)["impact"].(map[string]any)
	if c != 200 || imp["manualCheck"] != true || !strings.Contains(string(mustJSON(imp["invalidates"])), "\"review\"") || data(out)["estimate"] == nil {
		t.Fatalf("impact: %d %s", c, raw)
	}
	if c, out, _, _ := h.call("u_alice", "POST", lr+l0+"/edit-impact", map[string]any{"edits": []map[string]any{{"target": "answer", "field": "plain", "value": "1"}}}); c != 403 || errCode(out) != "POLICY_REVOKED" {
		t.Fatalf("answer is not editable: %d %v", c, out)
	}
	if c, out, _, _ := h.call("u_alice", "POST", lr+l0+"/edit-impact", map[string]any{"edits": []map[string]any{{"target": "pronunciation", "term": "棱", "reading": "léng"}}}); c != 200 ||
		data(out)["approvalInherited"] != false || strings.Contains(string(mustJSON(data(out)["impact"].(map[string]any)["invalidates"])), "\"review\"") {
		t.Fatalf("pronunciation impact under default rule: %d %v", c, out)
	}

	save := func(id, hash, key string, e any, branch bool) (int, map[string]any, []byte) {
		body := map[string]any{"baseLessonHash": hash, "edits": e, "idempotencyKey": key}
		if branch {
			body["saveAs"] = "branch"
		}
		c, out, raw, _ := h.call("u_alice", "POST", lr+id+"/edits", body)
		return c, out, raw
	}
	child := func(out map[string]any) (string, map[string]any) {
		j := data(out)["job"].(map[string]any)
		done := h.waitJob("u_alice", j["id"].(string))
		id, _ := done["lessonRevisionId"].(string)
		return id, done
	}

	// Stale hash: refused, the caller's edits come back.
	if c, out, raw := save(l0, "sha256:stale", "e-stale", edits, false); c != 409 || errCode(out) != "REVISION_CONFLICT" || !strings.Contains(string(raw), "先看底面") {
		t.Fatalf("stale base: %d %s", c, raw)
	}
	c, out, raw = save(l0, h0, "e-1", edits, false)
	if c != 202 {
		t.Fatalf("save: %d %s", c, raw)
	}
	jobID := data(out)["job"].(map[string]any)["id"]
	c1, j1 := child(out)
	if j1["status"] != "succeeded" || c1 == "" || c1 == l0 {
		t.Fatalf("edit job: %v", j1)
	}
	var r1, base model.EduLessonRevision
	h.db.First(&r1, "id = ?", c1)
	h.db.First(&base, "id = ?", l0)
	if r1.ParentLessonRevisionID != l0 || r1.State != "ready_for_review" || r1.EditClasses != "reasoning,wording" || r1.LessonHash == h0 ||
		r1.SolutionRevisionID != base.SolutionRevisionID || r1.ApprovalInheritedFrom != "" {
		t.Fatalf("child revision: %+v", r1)
	}
	var pubAfter model.EduPublication
	h.db.First(&pubAfter, "id = ?", pubID)
	if base.State != "approved" || base.LessonHash != h0 || pubAfter.LessonHash != h0 || pubAfter.LessonRevisionID != l0 {
		t.Fatalf("base and its publication must stay unchanged: %+v %+v", base, pubAfter)
	}
	_, out, _, _ = h.call("u_alice", "GET", lr+c1+"/editable", nil)
	if q := string(mustJSON(data(out)["quality"])); !strings.Contains(q, b0) || !strings.Contains(q, "teacher_edited_not_auto_verified") {
		t.Fatalf("edited reasoning must be flagged for manual check, not reported as verified: %s", q)
	}
	// Publishing the unreviewed child is blocked (constraint 3).
	if c, _, _, _ := h.call("u_alice", "POST", lr+c1+"/publish", map[string]any{"lessonHash": r1.LessonHash}); c != 409 {
		t.Fatalf("unreviewed child must not publish, got %d", c)
	}

	// Replay returns the same job; the head of l0 is taken, so a second normal save conflicts (AC-17).
	if c, out, _ := save(l0, h0, "e-1", edits, false); c != 202 || data(out)["job"].(map[string]any)["id"] != jobID {
		t.Fatalf("replay: %d %v", c, out)
	}
	other := []map[string]any{{"target": "beat", "id": b0, "field": "title", "value": "另一种说法"}}
	c, out, raw = save(l0, h0, "e-2", other, false)
	conf, _ := data(out)["conflict"].(map[string]any)
	if c != 409 || conf["reason"] != "head_taken" || conf["latestRevisionId"] != c1 || !strings.Contains(string(raw), "另一种说法") {
		t.Fatalf("head conflict: %d %s", c, raw)
	}
	// Save as branch keeps both versions.
	c, out, raw = save(l0, h0, "e-3", other, true)
	if c != 202 {
		t.Fatalf("branch: %d %s", c, raw)
	}
	cb, _ := child(out)
	var rb model.EduLessonRevision
	h.db.First(&rb, "id = ?", cb)
	if rb.ParentLessonRevisionID != l0 || rb.EditClasses != "wording" || cb == c1 {
		t.Fatalf("branch revision: %+v", rb)
	}

	// A failed edit (hint leaking the answer) releases the head of its base.
	leak := []map[string]any{{"target": "hint", "level": openHint, "field": "text", "value": "答案是 " + tl.Answer.Plain}}
	c, out, raw = save(c1, r1.LessonHash, "e-leak", leak, false)
	if c != 202 {
		t.Fatalf("leak save: %d %s", c, raw)
	}
	if _, jl := child(out); jl["status"] != "failed" || !strings.Contains(string(mustJSON(jl["error"])), "VALIDATION_FAILED") {
		t.Fatalf("leaking hint must fail: %v", jl)
	}

	// Review inheritance: approve c1, switch the rule, then a pronunciation-only edit inherits.
	if c, _, raw, _ := h.call("u_alice", "POST", lr+c1+"/review", map[string]any{"lessonHash": r1.LessonHash, "decision": "approved", "comment": "核对板书"}); c != 200 {
		t.Fatalf("approve child: %d %s", c, raw)
	}
	if c, _, raw, _ := h.call("u_alice", "PUT", "/api/edu/policy", map[string]any{"reviewMode": "self_allowed", "consentMode": "guardian_required", "editReviewInherit": "sometimes"}); c != 400 {
		t.Fatalf("bad rule must be 400: %d %s", c, raw)
	}
	if c, _, raw, _ := h.call("u_alice", "PUT", "/api/edu/policy", map[string]any{"reviewMode": "self_allowed", "consentMode": "guardian_required", "editReviewInherit": "pronunciation_only"}); c != 200 {
		t.Fatalf("set rule: %d %s", c, raw)
	}
	pron := []map[string]any{{"target": "pronunciation", "term": "棱", "reading": "léng"}}
	c, out, raw = save(c1, r1.LessonHash, "e-pron", pron, false) // the failed leak edit no longer holds the head
	if c != 202 {
		t.Fatalf("pronunciation save after released head: %d %s", c, raw)
	}
	c2, _ := child(out)
	var r2 model.EduLessonRevision
	h.db.First(&r2, "id = ?", c2)
	var rec model.EduReviewRecord
	h.db.First(&rec, "lesson_revision_id = ?", c2)
	if r2.State != "approved" || r2.ApprovalInheritedFrom != c1 || rec.InheritedFrom == "" || rec.LessonHash != r2.LessonHash {
		t.Fatalf("pronunciation-only edit must inherit approval with a marked record: %+v %+v", r2, rec)
	}
	if c, _, raw, _ := h.call("u_alice", "POST", lr+c2+"/publish", map[string]any{"lessonHash": r2.LessonHash}); c != 200 {
		t.Fatalf("publish inherited approval: %d %s", c, raw)
	}
	// Under the inheritance rule a wording edit still needs review; back on the default rule a pronunciation edit does too.
	c, out, _ = save(c2, r2.LessonHash, "e-word", other, false)
	c3, _ := child(out)
	var r3 model.EduLessonRevision
	h.db.First(&r3, "id = ?", c3)
	if c != 202 || r3.State != "ready_for_review" {
		t.Fatalf("wording edit must need review: %d %+v", c, r3)
	}
	h.db.Save(&model.EduOrgPolicy{TenantID: tenant, ReviewMode: "self_allowed", ConsentMode: "guardian_required", EditReviewInherit: "none"})
	c, out, _ = save(c2, r2.LessonHash, "e-pron2", []map[string]any{{"target": "pronunciation", "term": "锥", "reading": "zhuī"}}, true)
	c4, _ := child(out)
	var r4 model.EduLessonRevision
	h.db.First(&r4, "id = ?", c4)
	if c != 202 || r4.State != "ready_for_review" || r4.ApprovalInheritedFrom != "" {
		t.Fatalf("default rule: pronunciation edit must need review: %d %+v", c, r4)
	}

	c, out, raw, _ = h.call("u_alice", "GET", lr+c2+"/history", nil)
	lin, _ := data(out)["lineage"].([]any)
	if c != 200 || len(lin) != 3 || lin[1].(map[string]any)["id"] != c1 || lin[2].(map[string]any)["id"] != l0 || data(out)["latestRevisionId"] != c3 {
		t.Fatalf("history: %d %s", c, raw)
	}
	var n int64
	h.db.Model(&model.EduLessonEdit{}).Where("base_lesson_revision_id = ? AND state = ?", c1, "failed").Count(&n)
	if n != 1 {
		t.Fatalf("failed edit must be recorded as failed, got %d", n)
	}
}
