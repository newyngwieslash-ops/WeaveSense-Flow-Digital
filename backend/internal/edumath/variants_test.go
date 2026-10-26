package edumath

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/model"
)

// TestVariantSetsAndExtensions covers FR-M07/FR-T11/FR-S08 and AC-21/22/28/29 on the
// real math service: a variant set is a job, every item is independently verified and
// numbers only (near transfer), answer keys stay on staff APIs, review binds the set
// hash under the institution policy, only an approved set of the same lesson can be
// assigned, the set is the self-test unit with no feedback before the last item, and a
// per student extension moves only that student's effective due time.
func TestVariantSetsAndExtensions(t *testing.T) { runVariants(t, newHarness(t, startMathService(t))) }

func runVariants(t *testing.T, h *harness) {
	tenant := h.setupTeacher()
	pubID, tl := publishG1(t, h)
	var pub model.EduPublication
	h.db.First(&pub, "id = ?", pubID)
	lessonID := pub.LessonRevisionID
	stu := []string{"X-Edu-Tenant", tenant}

	base := "/api/edu/lesson-revisions/" + lessonID + "/variant-sets"
	if c, _, _, _ := h.call("u_alice", "POST", base, map[string]any{"count": 9, "idempotencyKey": "vs-bad"}); c != 400 {
		t.Fatalf("count over the cap must be 400, got %d", c)
	}
	c, out, raw, _ := h.call("u_alice", "POST", base, map[string]any{"count": 3, "idempotencyKey": "vs-1"})
	if c != 202 || data(out)["kind"] != "variant_set" {
		t.Fatalf("start variant set: %d %s", c, raw)
	}
	job := h.waitJob("u_alice", data(out)["id"].(string))
	setID, _ := job["variantSetId"].(string)
	if job["status"] != "succeeded" || setID == "" {
		t.Fatalf("variant job: %v", job)
	}
	// Replay with the same key returns the same job; a different count under the same key conflicts.
	if c, out, _, _ := h.call("u_alice", "POST", base, map[string]any{"count": 3, "idempotencyKey": "vs-1"}); c != 202 || data(out)["id"] != job["id"] {
		t.Fatalf("idempotent replay: %d %v", c, out)
	}
	if c, out, _, _ := h.call("u_alice", "POST", base, map[string]any{"count": 2, "idempotencyKey": "vs-1"}); c != 409 || errCode(out) != "REVISION_CONFLICT" {
		t.Fatalf("key reuse with other input: %d %v", c, out)
	}

	c, out, raw, _ = h.call("u_alice", "GET", "/api/edu/variant-sets/"+setID, nil)
	if c != 200 {
		t.Fatalf("get set: %d %s", c, raw)
	}
	vs := data(out)
	items := vs["items"].([]any)
	keys := vs["answerKeys"].(map[string]any)
	// sin depends only on h/a, so (4,2) and (6,3) repeat the parent and are dropped, never padded.
	if vs["state"] != "ready_for_review" || vs["requested"].(float64) != 3 || len(items) != 2 || len(keys) != 2 {
		t.Fatalf("set content: %s", raw)
	}
	if !strings.Contains(string(raw), "answer_reused") {
		t.Fatalf("dropped candidates must carry their reason: %s", raw)
	}
	for _, x := range items {
		it := x.(map[string]any)
		k := keys[it["id"].(string)].(map[string]any)
		if it["math_state"] != "verified" || it["change_type"] != "numbers_only" || it["transfer"] != "near" || it["answer_key"] != nil {
			t.Fatalf("item: %v", it)
		}
		if k["exact"] == tl.Answer.Exact {
			t.Fatalf("variant reuses the parent answer: %v", k)
		}
	}
	_, out, _, _ = h.call("u_alice", "GET", base, nil)
	if l, _ := out["data"].([]any); len(l) != 1 || strings.Contains(string(mustJSON(l)), "answerKeys") {
		t.Fatalf("list must not carry keys: %v", out)
	}

	// Not approved yet: cannot be assigned (AC-22).
	_, out, _, _ = h.call("u_alice", "POST", "/api/edu/classes", map[string]any{"name": "高二(9)班"})
	classID := data(out)["id"].(string)
	t0 := time.Now().UTC().Truncate(time.Second)
	h.now.Store(t0.Unix())
	mk := func(body map[string]any) (int, map[string]any) {
		body["publicationId"] = pubID
		c, out, _, _ := h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/assignments", body)
		return c, out
	}
	if c, out := mk(map[string]any{"title": "x", "variantSetId": setID}); c != 409 || errCode(out) != "VALIDATION_FAILED" {
		t.Fatalf("unapproved set must not be assignable: %d %v", c, out)
	}
	// Review binds the hash; second_reviewer blocks self review; self_allowed lets the author approve.
	rv := "/api/edu/variant-sets/" + setID + "/review"
	if c, out, _, _ := h.call("u_alice", "POST", rv, map[string]any{"setHash": "sha256:stale", "decision": "approved"}); c != 409 || errCode(out) != "REVISION_CONFLICT" {
		t.Fatalf("stale hash: %d %v", c, out)
	}
	h.db.Save(&model.EduOrgPolicy{TenantID: tenant, ReviewMode: "second_reviewer"})
	if c, _, _, _ := h.call("u_alice", "POST", rv, map[string]any{"setHash": vs["setHash"], "decision": "approved"}); c != 403 {
		t.Fatalf("self review under second_reviewer must be 403, got %d", c)
	}
	h.db.Save(&model.EduOrgPolicy{TenantID: tenant, ReviewMode: "self_allowed"})
	if c, _, raw, _ := h.call("u_alice", "POST", rv, map[string]any{"setHash": vs["setHash"], "decision": "approved", "comment": "逐题核对"}); c != 200 {
		t.Fatalf("approve: %d %s", c, raw)
	}
	if c, _, _, _ := h.call("u_alice", "POST", rv, map[string]any{"setHash": vs["setHash"], "decision": "approved"}); c != 409 {
		t.Fatalf("second decision must be 409, got %d", c)
	}

	due := t0.Add(2 * time.Hour)
	c, out = mk(map[string]any{"title": "变式自测", "variantSetId": setID, "answerPolicy": "after_due", "dueAt": due.Format(time.RFC3339)})
	if c != 201 || data(out)["variantSetId"] != setID {
		t.Fatalf("assign approved set: %d %v", c, out)
	}
	asID := data(out)["id"].(string)

	_, out, _, _ = h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/invites", map[string]any{"ttlHours": 24, "maxUses": 5})
	code := data(out)["code"]
	secret := strings.SplitN(code.(string), ".", 2)[1]
	c, out, raw, _ = h.call("u_alice", "GET", "/api/edu/classes/"+classID+"/invites", nil)
	if l, _ := out["data"].([]any); c != 200 || len(l) != 1 || strings.Contains(string(raw), secret) {
		t.Fatalf("invite list must not carry the secret: %d %s", c, raw)
	}
	c, _, raw, _ = h.call("u_alice", "GET", "/api/edu/publications", nil)
	if c != 200 || !strings.Contains(string(raw), pubID) || !strings.Contains(string(raw), "正四棱锥") {
		t.Fatalf("publications list: %d %s", c, raw)
	}
	for _, u := range []string{"u_bob", "u_dan"} {
		if c, _, raw, _ := h.call(u, "POST", "/api/edu-join", map[string]any{"code": code}); c != 200 {
			t.Fatalf("join %s: %d %s", u, c, raw)
		}
		h.call("u_alice", "POST", "/api/edu/consents", map[string]any{"studentUserId": u, "status": "granted", "evidenceRef": "form-vs-" + u})
	}
	markers := []string{}
	for _, k := range keys {
		km := k.(map[string]any)
		markers = append(markers, km["exact"].(string), km["latex"].(string))
	}
	noKeys := func(where string, raw []byte) {
		t.Helper()
		for _, m := range markers {
			if len(m) >= 3 && strings.Contains(string(raw), m) {
				t.Fatalf("%s leaks variant answer %q: %s", where, m, raw)
			}
		}
	}
	p := "/api/edu/student/assignments/" + asID
	if c, out, _, _ := h.call("u_bob", "POST", p+"/attempts", map[string]any{"item": "v1", "kind": "answer", "response": "1", "clientKey": "bob-vs-0000"}, stu...); c != 409 || errCode(out) != "SELFTEST_NOT_STARTED" {
		t.Fatalf("set item before start: %d %v", c, out)
	}
	c, out, raw, _ = h.call("u_bob", "POST", p+"/selftest/start", nil, stu...)
	qs, _ := data(out)["questions"].([]any)
	if c != 200 || data(out)["measurement"] != "set" || len(qs) != 2 {
		t.Fatalf("start set: %d %s", c, raw)
	}
	noKeys("selftest start", raw)
	_, _, raw, _ = h.call("u_bob", "GET", p+"/learning", nil, stu...)
	noKeys("learning after start", raw)
	if c, _, _, _ := h.call("u_bob", "POST", p+"/attempts", map[string]any{"item": "check", "kind": "answer", "response": "1", "clientKey": "bob-vs-0001"}, stu...); c != 400 {
		t.Fatalf("check item is replaced by the set: %d", c)
	}
	k1 := keys["v1"].(map[string]any)["exact"].(string)
	c, out, raw, _ = h.call("u_bob", "POST", p+"/attempts", map[string]any{"item": "v1", "kind": "answer", "response": k1, "clientKey": "bob-vs-0002"}, stu...)
	if at := data(out)["attempt"].(map[string]any); c != 201 || at["result"] != "recorded" || data(out)["selfTestComplete"] != false {
		t.Fatalf("first item must be recorded without feedback: %d %s", c, raw)
	}
	if c, out, _, _ := h.call("u_bob", "POST", p+"/attempts", map[string]any{"item": "v1", "kind": "answer", "response": "2", "clientKey": "bob-vs-0003"}, stu...); c != 409 || errCode(out) != "ITEM_ALREADY_SUBMITTED" {
		t.Fatalf("item resubmission: %d %v", c, out)
	}
	_, _, raw, _ = h.call("u_bob", "GET", p+"/learning", nil, stu...)
	if strings.Contains(string(raw), `"correct"`) || strings.Contains(string(raw), "answerKeys") {
		t.Fatalf("learning state must not reveal set results before the unit is done: %s", raw)
	}
	// (the student's own response is echoed back; that is not a key leak)
	_, out, raw, _ = h.call("u_bob", "GET", "/api/edu/student/progress", nil, stu...)
	if !strings.Contains(string(raw), `"selfTest":"in_progress"`) {
		t.Fatalf("progress must not grade a partly submitted unit: %s", raw)
	}
	c, out, raw, _ = h.call("u_bob", "POST", p+"/attempts", map[string]any{"item": "v2", "kind": "answer", "response": "1/7", "clientKey": "bob-vs-0004"}, stu...)
	if c != 201 || data(out)["selfTestComplete"] != true {
		t.Fatalf("last item completes the unit: %d %s", c, raw)
	}
	_, out, raw, _ = h.call("u_bob", "GET", p+"/learning", nil, stu...)
	st := data(out)["selfTest"].(map[string]any)
	if st["state"] != "submitted" || st["answerKeys"] == nil || !strings.Contains(string(raw), `"correct"`) {
		t.Fatalf("after the unit: results and keys: %s", raw)
	}
	_, _, raw, _ = h.call("u_bob", "GET", "/api/edu/student/progress", nil, stu...)
	if !strings.Contains(string(raw), `"selfTest":"partial"`) || !strings.Contains(string(raw), `"independentResults":1`) {
		t.Fatalf("the whole set is one measurement (one right, one wrong = partial): %s", raw)
	}
	_, out, raw, _ = h.call("u_alice", "GET", "/api/edu/assignments/"+asID+"/dashboard", nil)
	if st := data(out)["selfTest"].(map[string]any); st["independent"].(float64) != 1 || st["independentCorrect"].(float64) != 0 {
		t.Fatalf("dashboard counts the unit once: %s", raw)
	}

	// Per student extension (AC-21): only u_dan's due moves; validation and membership checks.
	ext := "/api/edu/assignments/" + asID + "/extensions"
	sh := time.FixedZone("UTC+8", 8*3600)
	if c, _, _, _ := h.call("u_alice", "POST", ext, map[string]any{"studentUserId": "u_dan", "dueAt": t0.Add(time.Hour).In(sh).Format(time.RFC3339)}); c != 400 {
		t.Fatalf("extension before due must be 400, got %d", c)
	}
	if c, _, _, _ := h.call("u_alice", "POST", ext, map[string]any{"studentUserId": "u_carol", "dueAt": t0.Add(5 * time.Hour).Format(time.RFC3339)}); c != 404 {
		t.Fatalf("non enrolled student must be 404, got %d", c)
	}
	if c, _, raw, _ := h.call("u_alice", "POST", ext, map[string]any{"studentUserId": "u_dan", "dueAt": t0.Add(4 * time.Hour).In(sh).Format(time.RFC3339), "reason": "病假"}); c != 200 {
		t.Fatalf("extension: %d %s", c, raw)
	}
	if c, _, raw, _ := h.call("u_alice", "POST", ext, map[string]any{"studentUserId": "u_dan", "dueAt": t0.Add(5 * time.Hour).Format(time.RFC3339), "reason": "病假延长"}); c != 200 {
		t.Fatalf("extension update: %d %s", c, raw)
	}
	var row model.EduAssignmentExtension
	h.db.First(&row, "assignment_id = ? AND student_user_id = ?", asID, "u_dan")
	if !row.DueAt.Equal(t0.Add(5 * time.Hour)) {
		t.Fatalf("extension must be one row, stored as the same instant: %v", row.DueAt)
	}
	if c, _, _, _ := h.call("u_bob", "POST", ext, map[string]any{"studentUserId": "u_bob", "dueAt": t0.Add(9 * time.Hour).Format(time.RFC3339)}, stu...); c != 403 && c != 404 {
		t.Fatalf("student must not grant extensions, got %d", c)
	}
	h.now.Store(t0.Add(3 * time.Hour).Unix())
	if c, out, _, _ := h.call("u_bob", "POST", p+"/attempts", map[string]any{"kind": "answer", "response": "1", "clientKey": "bob-vs-0005"}, stu...); c != 409 || errCode(out) != "ASSIGNMENT_CLOSED" {
		t.Fatalf("bob is past due: %d %v", c, out)
	}
	c, out, raw, _ = h.call("u_dan", "POST", p+"/attempts", map[string]any{"kind": "answer", "response": "1", "clientKey": "dan-vs-0001"}, stu...)
	if c != 201 || data(out)["attempt"].(map[string]any)["late"] != false {
		t.Fatalf("dan has an extension: %d %s", c, raw)
	}
	_, out, _, _ = h.call("u_dan", "GET", p+"/learning", nil, stu...)
	pol := data(out)["policy"].(map[string]any)
	if pol["answerAvailable"] != false || pol["extendedDueAt"] == nil {
		t.Fatalf("after_due release follows dan's own due: %v", pol)
	}
	_, out, _, _ = h.call("u_bob", "GET", p+"/learning", nil, stu...)
	if data(out)["policy"].(map[string]any)["answerAvailable"] != true {
		t.Fatalf("bob's answer is released after his due: %v", data(out)["policy"])
	}
	if c, _, _, _ := h.call("u_alice", "POST", ext+"/u_dan/remove", nil); c != 200 {
		t.Fatalf("remove extension: %d", c)
	}
	if c, out, _, _ := h.call("u_dan", "POST", p+"/attempts", map[string]any{"kind": "answer", "response": "2", "clientKey": "dan-vs-0002"}, stu...); c != 409 || errCode(out) != "ASSIGNMENT_CLOSED" {
		t.Fatalf("removed extension closes writes: %d %v", c, out)
	}
	h.now.Store(0)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
