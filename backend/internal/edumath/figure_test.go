package edumath

import (
	"strings"
	"testing"
)

// TestFigurePredictionsAndUse covers FR-S05 and AC-25/26 server side: the
// student projection carries an interactive figure spec without the answer, a
// prediction is validated against the published lesson and the first choice is
// kept, interaction counts are bounded aggregates, classmates and non-members
// are refused, and the teacher summary is separate from mastery.
func TestFigurePredictionsAndUse(t *testing.T) { runFigure(t, newHarness(t, startMathService(t))) }

func runFigure(t *testing.T, h *harness) {
	tenant := h.setupTeacher()
	pubID, lesson := publishG1(t, h)
	stu := []string{"X-Edu-Tenant", tenant}
	_, out, _, _ := h.call("u_alice", "POST", "/api/edu/classes", map[string]any{"name": "高二(7)班"})
	classID := data(out)["id"].(string)
	_, out, _, _ = h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/assignments", map[string]any{"publicationId": pubID, "title": "图形探索", "answerPolicy": "after_submission"})
	asID := data(out)["id"].(string)
	_, out, _, _ = h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/invites", map[string]any{"ttlHours": 24, "maxUses": 5})
	code := data(out)["code"]
	if c, _, raw, _ := h.call("u_bob", "POST", "/api/edu-join", map[string]any{"code": code}); c != 200 {
		t.Fatalf("join: %d %s", c, raw)
	}
	h.call("u_alice", "POST", "/api/edu/consents", map[string]any{"studentUserId": "u_bob", "status": "granted", "evidenceRef": "form-fig-1"})

	// The student lesson carries the explore spec and no answer.
	c, out, raw, _ := h.call("u_bob", "GET", "/api/edu/student/assignments/"+asID, nil, stu...)
	if c != 200 || !strings.Contains(string(raw), `"explore"`) || !strings.Contains(string(raw), `"foot"`) {
		t.Fatalf("student lesson must carry the explore spec: %d %s", c, raw)
	}
	for _, m := range []string{lesson.Answer.Exact, lesson.Answer.Latex, lesson.Answer.Plain} {
		if len(m) >= 3 && strings.Contains(string(raw), m) {
			t.Fatalf("student lesson leaks answer %q", m)
		}
	}

	p := "/api/edu/student/assignments/" + asID + "/predictions"
	if c, out, _, _ := h.call("u_bob", "POST", p, map[string]any{"promptId": "nope", "choice": 0}, stu...); c != 404 || errCode(out) != "PROMPT_NOT_FOUND" {
		t.Fatalf("unknown prompt: %d %v", c, out)
	}
	if c, _, _, _ := h.call("u_bob", "POST", p, map[string]any{"promptId": "foot", "choice": 99}, stu...); c != 400 {
		t.Fatalf("out of range choice must be 400, got %d", c)
	}
	if c, _, _, _ := h.call("u_bob", "POST", p, map[string]any{"promptId": "foot"}, stu...); c != 400 {
		t.Fatalf("missing choice must be 400, got %d", c)
	}
	c, out, raw, _ = h.call("u_bob", "POST", p, map[string]any{"promptId": "foot", "choice": 1}, stu...)
	pr, _ := data(out)["prediction"].(map[string]any)
	if c != 201 || pr["choice"].(float64) != 1 || pr["beforeAttempt"] != true || pr["choiceText"] == "" {
		t.Fatalf("prediction: %d %s", c, raw)
	}
	c, out, _, _ = h.call("u_bob", "POST", p, map[string]any{"promptId": "foot", "choice": 0}, stu...)
	if pr, _ := data(out)["prediction"].(map[string]any); c != 200 || data(out)["replayed"] != true || pr["choice"].(float64) != 1 {
		t.Fatalf("first prediction must be kept: %d %v", c, out)
	}
	if c, _, _, _ := h.call("u_carol", "POST", p, map[string]any{"promptId": "foot", "choice": 0}, "X-Edu-Tenant", tenant); c != 403 && c != 404 {
		t.Fatalf("non-member prediction must be refused, got %d", c)
	}
	_, out, _, _ = h.call("u_bob", "GET", "/api/edu/student/assignments/"+asID+"/learning", nil, stu...)
	if ps, _ := data(out)["predictions"].([]any); len(ps) != 1 {
		t.Fatalf("learning state must list my prediction: %v", out)
	}

	ev := "/api/edu/student/assignments/" + asID + "/figure-events"
	if c, _, _, _ := h.call("u_bob", "POST", ev, map[string]any{"drags": 501}, stu...); c != 400 {
		t.Fatalf("over-cap counts must be 400, got %d", c)
	}
	if c, _, _, _ := h.call("u_bob", "POST", ev, map[string]any{"drags": -1}, stu...); c != 400 {
		t.Fatalf("negative counts must be 400, got %d", c)
	}
	for i := 0; i < 2; i++ {
		if c, out, raw, _ := h.call("u_bob", "POST", ev, map[string]any{"drags": 3, "keys": 2, "resets": 1}, stu...); c != 200 || data(out)["recorded"] != true {
			t.Fatalf("figure events: %d %s", c, raw)
		}
	}

	c, out, raw, _ = h.call("u_alice", "GET", "/api/edu/assignments/"+asID+"/figure-summary", nil)
	if c != 200 {
		t.Fatalf("summary: %d %s", c, raw)
	}
	fig := data(out)["figure"].(map[string]any)
	foot, _ := data(out)["predictions"].(map[string]any)["foot"].([]any)
	if fig["studentsUsed"].(float64) != 1 || fig["studentsKeyboardOrForm"].(float64) != 1 || len(foot) != 1 || foot[0].(map[string]any)["count"].(float64) != 1 {
		t.Fatalf("summary content: %s", raw)
	}
	if strings.Contains(string(raw), "u_bob") {
		t.Fatalf("summary must be aggregate only: %s", raw)
	}
	if c, _, _, _ := h.call("u_bob", "GET", "/api/edu/assignments/"+asID+"/figure-summary", nil); c != 403 && c != 404 {
		t.Fatalf("student must not read the summary, got %d", c)
	}
}
