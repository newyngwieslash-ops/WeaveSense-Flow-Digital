package edumath

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/model"
)

// publishG1 runs SOLID-ANGLE-02 (a=2, h=1) to a published lesson and returns the
// publication id and the teacher lesson (for expected values in assertions only).
func publishG1(t *testing.T, h *harness) (string, teacherLesson) {
	t.Helper()
	revID := h.confirmedRevision("2", "1")
	_, out, _, _ := h.call("u_alice", "POST", "/api/edu/problem-revisions/"+revID+"/analyze", map[string]any{"idempotencyKey": "learn-a"})
	job := h.waitJob("u_alice", data(out)["id"].(string))
	_, out, _, _ = h.call("u_alice", "POST", "/api/edu/solution-revisions/"+job["solutionRevisionId"].(string)+"/lesson", map[string]any{"idempotencyKey": "learn-l"})
	lj := h.waitJob("u_alice", data(out)["id"].(string))
	lessonID, _ := lj["lessonRevisionId"].(string)
	var l model.EduLessonRevision
	h.db.First(&l, "id = ?", lessonID)
	tenant := model.PersonalTenantID("u_alice")
	h.db.Save(&model.EduOrgPolicy{TenantID: tenant, ReviewMode: "self_allowed"})
	if code, _, raw, _ := h.call("u_alice", "POST", "/api/edu/lesson-revisions/"+lessonID+"/review", map[string]any{"lessonHash": l.LessonHash, "decision": "approved", "comment": "自审"}); code != 200 {
		t.Fatalf("review: %d %s", code, raw)
	}
	code, out, raw, _ := h.call("u_alice", "POST", "/api/edu/lesson-revisions/"+lessonID+"/publish", map[string]any{"lessonHash": l.LessonHash})
	if code != 200 {
		t.Fatalf("publish: %d %s", code, raw)
	}
	var ta model.EduArtifact
	h.db.First(&ta, "id = ?", l.LessonArtifact)
	b, _, err := h.svc.Client.Artifact(context.Background(), tenant, ta)
	if err != nil {
		t.Fatal(err)
	}
	var tl teacherLesson
	if err := json.Unmarshal(b, &tl); err != nil || tl.CheckAnswerKey.Exact == "" {
		t.Fatalf("teacher lesson: %v", err)
	}
	return data(out)["id"].(string), tl
}

func errCode(out map[string]any) string {
	e, _ := data(out)["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

// TestStudentLearningLoop covers FR-S01..S10 on the real math service: policy
// windows, idempotent attempts with a frozen first valid answer, server-side
// grading, ordered and gated hints, answer policies, the independent self-test,
// spaced review that needs a new attempt, teacher judgement and progress with
// an insufficient-evidence state.
func TestStudentLearningLoop(t *testing.T) { runLearning(t, newHarness(t, startMathService(t))) }

func runLearning(t *testing.T, h *harness) {
	tenant := h.setupTeacher()
	pubID, tl := publishG1(t, h)
	t0 := time.Now().UTC().Truncate(time.Second)
	h.now.Store(t0.Unix())
	stu := []string{"X-Edu-Tenant", tenant}

	_, out, _, _ := h.call("u_alice", "POST", "/api/edu/classes", map[string]any{"name": "高二(5)班"})
	classID := data(out)["id"].(string)
	mk := func(body map[string]any) (int, map[string]any) {
		body["publicationId"] = pubID
		code, out, _, _ := h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/assignments", body)
		return code, out
	}
	if code, _ := mk(map[string]any{"title": "x", "answerPolicy": "after_due"}); code != 400 {
		t.Fatalf("after_due without dueAt must be 400, got %d", code)
	}
	if code, _ := mk(map[string]any{"title": "x", "startsAt": t0.Add(time.Hour).Format(time.RFC3339), "dueAt": t0.Format(time.RFC3339)}); code != 400 {
		t.Fatalf("due before start must be 400, got %d", code)
	}
	if code, _ := mk(map[string]any{"title": "x", "answerPolicy": "whenever"}); code != 400 {
		t.Fatalf("unknown answer policy must be 400, got %d", code)
	}
	code, out := mk(map[string]any{"title": "线面角练习", "answerPolicy": "after_submission", "dueAt": t0.Add(30 * 24 * time.Hour).Format(time.RFC3339)})
	if code != 201 {
		t.Fatalf("assignment A: %d %v", code, out)
	}
	asA := data(out)["id"].(string)
	// Start/due given in UTC+8 are stored as the same instant in UTC.
	sh := time.FixedZone("UTC+8", 8*3600)
	code, out = mk(map[string]any{"title": "限时", "answerPolicy": "teacher_release",
		"startsAt": t0.Add(time.Hour).In(sh).Format(time.RFC3339), "dueAt": t0.Add(2 * time.Hour).In(sh).Format(time.RFC3339)})
	if code != 201 {
		t.Fatalf("assignment B: %d %v", code, out)
	}
	asB := data(out)["id"].(string)
	var bRow model.EduAssignment
	h.db.First(&bRow, "id = ?", asB)
	if bRow.StartsAt == nil || !bRow.StartsAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("startsAt must be the same instant: %v", bRow.StartsAt)
	}

	// Enrol + consent u_bob.
	_, out, _, _ = h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/invites", map[string]any{"ttlHours": 24, "maxUses": 5})
	if code, _, raw, _ := h.call("u_bob", "POST", "/api/edu-join", map[string]any{"code": data(out)["code"]}); code != 200 {
		t.Fatalf("join: %d %s", code, raw)
	}
	pA := "/api/edu/student/assignments/" + asA
	if code, out, _, _ := h.call("u_bob", "POST", pA+"/hints", map[string]any{"level": 1}, stu...); code != 403 || errCode(out) != "CONSENT_REQUIRED" {
		t.Fatalf("hints without consent must be blocked: %d %v", code, out)
	}
	h.call("u_alice", "POST", "/api/edu/consents", map[string]any{"studentUserId": "u_bob", "status": "granted", "evidenceRef": "form-learn-1"})
	// A non-member never reaches the learning endpoints.
	if code, _, _, _ := h.call("u_carol", "POST", pA+"/attempts", map[string]any{"kind": "answer", "response": "1", "clientKey": "carol-0001"}, stu...); code != 403 && code != 404 {
		t.Fatalf("non-member attempt must be refused, got %d", code)
	}

	markers := []string{"√22", "sqrt(22)", tl.CheckAnswerKey.Plain}
	noAnswer := func(where string, raw []byte) {
		t.Helper()
		for _, m := range markers {
			if m != "" && strings.Contains(string(raw), m) {
				t.Fatalf("%s leaked answer marker %q: %s", where, m, raw)
			}
		}
	}
	// Hints: order, gate before attempt, level 5 under policy.
	if code, out, _, _ := h.call("u_bob", "POST", pA+"/hints", map[string]any{"level": 2}, stu...); code != 409 || errCode(out) != "HINT_ORDER" {
		t.Fatalf("level 2 first must be HINT_ORDER: %d %v", code, out)
	}
	code, out, raw, _ := h.call("u_bob", "POST", pA+"/hints", map[string]any{"level": 1}, stu...)
	if code != 200 || data(out)["text"] == "" || data(out)["level"].(float64) != 1 {
		t.Fatalf("hint 1: %d %s", code, raw)
	}
	noAnswer("hint 1", raw)
	h.call("u_bob", "POST", pA+"/hints", map[string]any{"level": 2}, stu...)
	h.call("u_bob", "POST", pA+"/hints", map[string]any{"level": 3}, stu...)
	if code, out, _, _ := h.call("u_bob", "POST", pA+"/hints", map[string]any{"level": 4}, stu...); code != 409 || errCode(out) != "HINT_GATED" {
		t.Fatalf("level 4 before an attempt must be gated: %d %v", code, out)
	}
	_, _, raw, _ = h.call("u_bob", "GET", pA+"/learning", nil, stu...)
	noAnswer("learning state", raw)

	// Attempts.
	att := func(key, resp string, extra ...any) (int, map[string]any) {
		t.Helper()
		body := map[string]any{"kind": "answer", "response": resp, "clientKey": key}
		for i := 0; i+1 < len(extra); i += 2 {
			body[extra[i].(string)] = extra[i+1]
		}
		code, out, _, _ := h.call("u_bob", "POST", pA+"/attempts", body, stu...)
		return code, out
	}
	if code, _ := att("short", "1"); code != 400 {
		t.Fatalf("short clientKey must be 400, got %d", code)
	}
	code, out = att("bob-blank-1", "   ")
	if code != 201 || data(out)["attempt"].(map[string]any)["valid"] != false {
		t.Fatalf("blank must be recorded invalid: %d %v", code, out)
	}
	code, out = att("bob-try-0001", "sqrt(22)/11")
	a1 := data(out)["attempt"].(map[string]any)
	if code != 201 || a1["result"] != "incorrect" || a1["firstValid"] != true || a1["independent"] != false || a1["hintLevel"].(float64) != 3 {
		t.Fatalf("first attempt: %d %v", code, a1)
	}
	if j := asJSON(out); strings.Contains(j, "2*sqrt(22)") || strings.Contains(j, "2√22") || strings.Contains(j, tl.Answer.Exact) {
		t.Fatalf("attempt result must not carry the expected answer: %s", j)
	}
	code, out = att("bob-try-0001", "4/√22")
	if code != 200 || data(out)["replayed"] != true || data(out)["attempt"].(map[string]any)["id"] != a1["id"] {
		t.Fatalf("replayed clientKey must return the original attempt: %d %v", code, out)
	}
	code, out = att("bob-try-0002", " sqrt(22) / 11 ")
	if data(out)["attempt"].(map[string]any)["invalidReason"] != "duplicate" {
		t.Fatalf("same response must be recorded as duplicate: %v", out)
	}
	code, out = att("bob-try-0003", "4/√22")
	a3 := data(out)["attempt"].(map[string]any)
	if a3["result"] != "correct" || a3["firstValid"] != false || data(out)["answerAvailable"] != true {
		t.Fatalf("second valid attempt: %v", out)
	}
	var first int64
	h.db.Model(&model.EduAttempt{}).Where("assignment_id = ? AND first_valid = ?", asA, true).Count(&first)
	if first != 1 {
		t.Fatalf("exactly one frozen first valid attempt, got %d", first)
	}
	// Level 4 and 5 now open (after_submission).
	if code, _, raw, _ := h.call("u_bob", "POST", pA+"/hints", map[string]any{"level": 4}, stu...); code != 200 {
		t.Fatalf("hint 4: %d %s", code, raw)
	}
	code, out, raw, _ = h.call("u_bob", "POST", pA+"/hints", map[string]any{"level": 5}, stu...)
	if code != 200 || !strings.Contains(string(raw), "√22") || len(data(out)["beats"].([]any)) == 0 {
		t.Fatalf("full solution: %d %s", code, raw)
	}

	// Self-test: cannot answer before start; key hidden until submitted; one submission.
	if code, out := att("bob-check-01", tl.CheckAnswerKey.Exact, "item", "check"); code != 409 || errCode(out) != "SELFTEST_NOT_STARTED" {
		t.Fatalf("check before start: %d %v", code, out)
	}
	code, out, raw, _ = h.call("u_bob", "POST", pA+"/selftest/start", map[string]any{}, stu...)
	if code != 200 || data(out)["question"].(map[string]any)["prompt"] == "" || data(out)["selfTest"].(map[string]any)["sawSolution"] != true {
		t.Fatalf("selftest start: %d %s", code, raw)
	}
	noAnswer("selftest start", raw)
	_, _, raw, _ = h.call("u_bob", "GET", pA+"/learning", nil, stu...)
	if strings.Contains(string(raw), "answerKey") {
		t.Fatalf("self-test key must stay hidden before submission: %s", raw)
	}
	code, out = att("bob-check-02", tl.CheckAnswerKey.Exact, "item", "check")
	ca := data(out)["attempt"].(map[string]any)
	if code != 201 || ca["result"] != "correct" || ca["independent"] != true {
		t.Fatalf("self-test answer: %d %v", code, out)
	}
	if code, _ := att("bob-check-03", "1", "item", "check"); code != 409 {
		t.Fatalf("second self-test submission must be 409, got %d", code)
	}
	_, out, _, _ = h.call("u_bob", "GET", pA+"/learning", nil, stu...)
	if data(out)["selfTest"].(map[string]any)["answerKey"] == nil {
		t.Fatal("self-test key is shown after submission")
	}

	// Review: created by the incorrect first attempt, due +1d, needs a new attempt.
	_, out, _, _ = h.call("u_bob", "GET", "/api/edu/student/review", nil, stu...)
	items, _ := out["data"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["due"] != false || items[0].(map[string]any)["reason"] != "incorrect" {
		t.Fatalf("review list: %v", out["data"])
	}
	rvID := items[0].(map[string]any)["id"].(string)
	if code, out := att("bob-rev-0001", "4/√22", "reviewItemId", rvID); code != 409 || errCode(out) != "REVIEW_NOT_DUE" {
		t.Fatalf("early review: %d %v", code, out)
	}
	h.now.Store(t0.Add(25 * time.Hour).Unix())
	if code, out := att("bob-rev-0002", "2√22/11", "reviewItemId", rvID); code != 201 {
		t.Fatalf("review attempt: %d %v", code, out)
	}
	var rv model.EduReviewItem
	h.db.First(&rv, "id = ?", rvID)
	if rv.Stage != 2 || rv.State != "pending" || !rv.DueAt.Equal(t0.Add(25*time.Hour).Add(72*time.Hour)) {
		t.Fatalf("review must advance to +3d: %+v", rv)
	}

	// Teacher judgement sits next to the automatic one; another teacher's class is invisible.
	code, out, raw, _ = h.call("u_alice", "GET", "/api/edu/assignments/"+asA+"/attempts", nil)
	if code != 200 || len(data(out)["attempts"].([]any)) < 5 {
		t.Fatalf("teacher attempts: %d %s", code, raw)
	}
	if code, out, _, _ := h.call("u_alice", "POST", "/api/edu/attempts/"+a1["id"].(string)+"/judge", map[string]any{"result": "partial", "note": "思路对，计算错"}); code != 200 || data(out)["result"] != "incorrect" {
		t.Fatalf("judge: %d %v", code, out)
	}
	if code, _, _, _ := h.call("u_bob", "POST", "/api/edu/attempts/"+a1["id"].(string)+"/judge", map[string]any{"result": "correct"}); code != 403 && code != 404 {
		t.Fatalf("student cannot judge, got %d", code)
	}
	_, out, _, _ = h.call("u_bob", "GET", "/api/edu/student/progress", nil, stu...)
	sum := data(out)["summary"].(map[string]any)
	rows := data(out)["assignments"].([]any)
	if sum["evidence"] != "insufficient" || sum["independentCorrect"] != nil || len(rows) != 1 || rows[0].(map[string]any)["firstAttempt"] != "partial" {
		t.Fatalf("progress: %v", out["data"])
	}

	// Assignment B: start window, teacher_release, due without late submission.
	h.now.Store(t0.Unix())
	pB := "/api/edu/student/assignments/" + asB
	if code, out, _, _ := h.call("u_bob", "GET", pB+"/learning", nil, stu...); code != 403 || errCode(out) != "NOT_STARTED" {
		t.Fatalf("before start: %d %v", code, out)
	}
	h.now.Store(t0.Add(90 * time.Minute).Unix())
	if code, out, raw, _ := h.call("u_bob", "POST", pB+"/attempts", map[string]any{"kind": "no_idea", "clientKey": "bob-b-noidea"}, stu...); code != 201 || data(out)["attempt"].(map[string]any)["result"] != "no_idea" {
		t.Fatalf("no idea: %d %s", code, raw)
	}
	for lv := 1; lv <= 4; lv++ {
		if code, _, raw, _ := h.call("u_bob", "POST", pB+"/hints", map[string]any{"level": lv}, stu...); code != 200 {
			t.Fatalf("B hint %d: %d %s", lv, code, raw)
		}
	}
	if code, out, _, _ := h.call("u_bob", "POST", pB+"/hints", map[string]any{"level": 5}, stu...); code != 409 || errCode(out) != "ANSWER_NOT_RELEASED" {
		t.Fatalf("teacher_release must hold level 5: %d %v", code, out)
	}
	if code, _, raw, _ := h.call("u_alice", "POST", "/api/edu/assignments/"+asB+"/release-answer", map[string]any{}); code != 200 {
		t.Fatalf("release: %d %s", code, raw)
	}
	if code, _, _, _ := h.call("u_bob", "POST", pB+"/hints", map[string]any{"level": 5}, stu...); code != 200 {
		t.Fatalf("released level 5 must open, got %d", code)
	}
	h.now.Store(t0.Add(3 * time.Hour).Unix())
	if code, out, _, _ := h.call("u_bob", "POST", pB+"/attempts", map[string]any{"kind": "answer", "response": "1", "clientKey": "bob-b-late01"}, stu...); code != 409 || errCode(out) != "ASSIGNMENT_CLOSED" {
		t.Fatalf("late without allowLate: %d %v", code, out)
	}
	var audits int64
	h.db.Model(&model.EduAuditEvent{}).Where("action IN ?", []string{"hint.unlock", "attempt.submit", "attempt.judge", "assignment.release_answer"}).Count(&audits)
	if audits < 10 {
		t.Fatalf("learning actions must be audited, got %d", audits)
	}
}
