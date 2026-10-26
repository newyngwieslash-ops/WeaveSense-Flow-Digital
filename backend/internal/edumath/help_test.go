package edumath

import (
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/model"
)

// TestHelpRequestsAndDashboard covers FR-S11, FR-T12 and FR-T13: private help
// requests with idempotent submit, grouping by assignment/kind/beat with content
// errors first, public notes refused when they carry personal information,
// students never seeing classmates' requests, and the class distribution.
func TestHelpRequestsAndDashboard(t *testing.T) { runHelp(t, newHarness(t, startMathService(t))) }

func runHelp(t *testing.T, h *harness) {
	tenant := h.setupTeacher()
	pubID, _ := publishG1(t, h)
	stu := []string{"X-Edu-Tenant", tenant}
	h.db.Create(&model.User{ID: "u_dan", Username: "u_dan", DisplayName: "张小明"})
	_, out, _, _ := h.call("u_alice", "POST", "/api/edu/classes", map[string]any{"name": "高二(6)班"})
	classID := data(out)["id"].(string)
	_, out, _, _ = h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/assignments", map[string]any{"publicationId": pubID, "title": "求助测试", "answerPolicy": "after_submission"})
	asID := data(out)["id"].(string)
	_, out, _, _ = h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/invites", map[string]any{"ttlHours": 24, "maxUses": 5})
	code := data(out)["code"]
	for i, u := range []string{"u_bob", "u_dan"} {
		if c, _, raw, _ := h.call(u, "POST", "/api/edu-join", map[string]any{"code": code}); c != 200 {
			t.Fatalf("join %s: %d %s", u, c, raw)
		}
		h.call("u_alice", "POST", "/api/edu/consents", map[string]any{"studentUserId": u, "status": "granted", "evidenceRef": "form-help-" + string(rune('1'+i))})
	}
	p := "/api/edu/student/assignments/" + asID + "/help"
	help := func(u, key, kind, beat, msg string) (int, map[string]any) {
		c, out, _, _ := h.call(u, "POST", p, map[string]any{"kind": kind, "beatId": beat, "message": msg, "clientKey": key}, stu...)
		return c, out
	}
	if c, out := help("u_bob", "bob-help-001", "content_error", "s1", "第二步的符号好像错了"); c != 201 {
		t.Fatalf("bob help: %d %v", c, out)
	}
	if c, out := help("u_bob", "bob-help-001", "content_error", "s1", "again"); c != 200 || data(out)["replayed"] != true {
		t.Fatalf("replay: %d %v", c, out)
	}
	help("u_dan", "dan-help-001", "content_error", "s1", "s1 有错")
	help("u_dan", "dan-help-002", "dont_understand", "s2", "为什么法向量这样取")
	if c, _ := help("u_bob", "bob-help-002", "gossip", "", ""); c != 400 {
		t.Fatalf("unknown kind must be 400, got %d", c)
	}
	if c, _ := help("u_carol", "carol-help-1", "ask_teacher", "", "hi"); c != 403 && c != 404 {
		t.Fatalf("non-member help must be refused, got %d", c)
	}

	c, out, raw, _ := h.call("u_alice", "GET", "/api/edu/assignments/"+asID+"/help", nil)
	groups, _ := out["data"].([]any)
	if c != 200 || len(groups) != 2 {
		t.Fatalf("teacher groups: %d %s", c, raw)
	}
	g0 := groups[0].(map[string]any)
	if g0["kind"] != "content_error" || g0["open"].(float64) != 2 || len(g0["requests"].([]any)) != 2 {
		t.Fatalf("content errors must be grouped and first: %v", g0)
	}
	learnKey := groups[1].(map[string]any)["groupKey"].(string)

	// Students only see their own requests.
	_, _, raw, _ = h.call("u_dan", "GET", "/api/edu/student/help", nil, stu...)
	if strings.Contains(string(raw), "符号好像错了") || strings.Contains(string(raw), "u_bob") {
		t.Fatalf("dan must not see bob's request: %s", raw)
	}

	reply := func(key, body string, public bool) (int, map[string]any) {
		c, out, _, _ := h.call("u_alice", "POST", "/api/edu/assignments/"+asID+"/help/reply", map[string]any{"groupKey": key, "body": body, "public": public})
		return c, out
	}
	for _, bad := range []string{"张小明同学指出第二步有误，已更正。", "有问题请联系 13800138000", "邮件 a.b@example.com"} {
		if c, out := reply(g0["groupKey"].(string), bad, true); c != 422 || errCode(out) != "PERSONAL_INFO" {
			t.Fatalf("public note with personal info must be refused: %q %d %v", bad, c, out)
		}
	}
	if c, _ := reply("eas_other|content_error|s1", "x", false); c != 400 {
		t.Fatalf("foreign group key must be 400, got %d", c)
	}
	if c, out := reply(g0["groupKey"].(string), "第二步符号已核对：课件无误，注意向量方向。", true); c != 200 || data(out)["answered"].(float64) != 2 {
		t.Fatalf("public reply: %d %v", c, out)
	}
	if c, out := reply(learnKey, "法向量取 (1,1,0) 的原因见第 2 步。", false); c != 200 || data(out)["answered"].(float64) != 1 {
		t.Fatalf("private reply: %d %v", c, out)
	}
	_, out, raw, _ = h.call("u_bob", "GET", "/api/edu/student/help", nil, stu...)
	if len(data(out)["classNotes"].([]any)) != 1 || strings.Contains(string(raw), "法向量取") {
		t.Fatalf("bob sees the public note but not dan's private reply: %s", raw)
	}
	_, _, raw, _ = h.call("u_dan", "GET", "/api/edu/student/help", nil, stu...)
	if !strings.Contains(string(raw), "法向量取") {
		t.Fatalf("dan sees his private reply: %s", raw)
	}

	// Dashboard distribution.
	h.call("u_bob", "POST", "/api/edu/student/assignments/"+asID+"/attempts", map[string]any{"kind": "answer", "response": "2√22/11", "clientKey": "bob-dash-001"}, stu...)
	c, out, raw, _ = h.call("u_alice", "GET", "/api/edu/assignments/"+asID+"/dashboard", nil)
	d := data(out)
	dist, _ := d["distribution"].(map[string]any)
	if c != 200 || d["students"].(float64) != 2 || dist["completed_independently"].(float64) != 1 || dist["not_started"].(float64) != 1 ||
		d["help"].(map[string]any)["openReports"].(float64) != 0 {
		t.Fatalf("dashboard: %d %s", c, raw)
	}
	if strings.Contains(string(raw), "u_bob") {
		t.Fatalf("dashboard is aggregate only: %s", raw)
	}
	// A teacher without the class sees nothing.
	ws := model.PersonalWorkspaceID("u_alice")
	now := time.Now().UTC()
	h.db.Create(&model.Membership{ID: "m_carol_h", TenantID: tenant, UserID: "u_carol", Role: "member", Status: "active", CreatedAt: now})
	h.db.Create(&model.WorkspaceMembership{ID: "wm_carol_h", WorkspaceID: ws, UserID: "u_carol", Role: "editor", CreatedAt: now})
	h.call("u_alice", "POST", "/api/edu/roles", map[string]any{"tenantId": tenant, "userId": "u_carol", "role": "teacher"})
	for _, path := range []string{"/dashboard", "/help", "/attempts"} {
		if c, _, _, _ := h.call("u_carol", "GET", "/api/edu/assignments/"+asID+path, nil, "X-Workspace-Id", ws); c != 404 {
			t.Fatalf("non-teaching teacher %s must be 404, got %d", path, c)
		}
	}
}
