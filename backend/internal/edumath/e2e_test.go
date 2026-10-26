package edumath

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"yingce/backend/internal/database"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

const testKey = "edu-math-test-key-0123456789abcdef-xyz"

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"u_alice", "u_bob", "u_carol"} {
		if err := db.Create(&model.User{ID: id, Username: id}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// startMathService launches the real Python math service (services/math-service).
// Skips when python/sympy are missing unless EDU_MATH_REQUIRE_SERVICE=1 (CI).
func startMathService(t *testing.T) string {
	t.Helper()
	py := os.Getenv("MATH_PYTHON")
	if py == "" {
		py = "python3"
	}
	root, _ := filepath.Abs("../../../services/math-service")
	if err := exec.Command(py, "-c", "import sympy").Run(); err != nil {
		if os.Getenv("EDU_MATH_REQUIRE_SERVICE") == "1" {
			t.Fatalf("math service python unavailable: %v", err)
		}
		t.Skip("python3 with sympy not available; set MATH_PYTHON")
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	cmd := exec.Command(py, "-m", "mathsvc.server")
	cmd.Dir = root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "MATH_SERVICE_KEY=" + testKey, "MATH_DATA_DIR=" + t.TempDir(), "MATH_LISTEN=" + addr}
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("math service log:\n%s", logs.String())
		}
	})
	base := "http://" + addr
	for i := 0; i < 100; i++ {
		if r, err := http.Get(base + "/healthz"); err == nil {
			r.Body.Close()
			return base
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("math service did not start:\n%s", logs.String())
	return ""
}

type harness struct {
	t   *testing.T
	db  *gorm.DB
	svc *Service
	r   *gin.Engine
	now atomic.Int64
}

func newHarness(t *testing.T, base string) *harness { return newHarnessDB(t, base, testDB(t)) }

func newHarnessDB(t *testing.T, base string, db *gorm.DB) *harness {
	gin.SetMode(gin.TestMode)
	h := &harness{t: t, db: db}
	h.svc = &Service{DB: h.db, Client: Client{BaseURL: base, Key: []byte(testKey)}, URLKey: []byte(strings.Repeat("u", 32))}
	h.svc.Now = func() time.Time {
		if v := h.now.Load(); v != 0 {
			return time.Unix(v, 0).UTC()
		}
		return time.Now().UTC()
	}
	user := func(c *gin.Context) (*model.User, error) {
		id := c.GetHeader("X-Test-User")
		if id == "" {
			return nil, fmt.Errorf("no user")
		}
		return &model.User{ID: id}, nil
	}
	h.r = gin.New()
	(&API{Svc: h.svc, DB: h.db, User: user}).Register(h.r.Group("/api"))
	return h
}

func (h *harness) call(user, method, path string, body any, hdr ...string) (int, map[string]any, []byte, http.Header) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.Bytes(), w.Header()
}

func data(m map[string]any) map[string]any { d, _ := m["data"].(map[string]any); return d }

func (h *harness) waitJob(user, id string) map[string]any {
	h.t.Helper()
	for i := 0; i < 600; i++ {
		_, out, raw, _ := h.call(user, "GET", "/api/edu/jobs/"+id, nil)
		d := data(out)
		switch d["status"] {
		case "succeeded", "failed", "cancelled":
			return d
		case nil:
			h.t.Fatalf("job get: %s", raw)
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatal("job did not finish")
	return nil
}

func problem(a, hh string) map[string]any {
	return map[string]any{"family_id": "SOLID-ANGLE-02", "input_state": "sufficient", "unit": "", "params": map[string]any{"a": a, "h": hh}}
}

// setupTeacher binds u_alice as teacher in her personal tenant and funds her.
func (h *harness) setupTeacher() string {
	tenant := model.PersonalTenantID("u_alice")
	if err := h.db.Save(&model.CreditAccount{UserID: "u_alice", AvailableMicrocredits: 1_000_000}).Error; err != nil {
		h.t.Fatal(err)
	}
	code, _, raw, _ := h.call("u_alice", "POST", "/api/edu/roles", map[string]any{"tenantId": tenant, "userId": "u_alice", "role": "teacher"})
	if code != 200 {
		h.t.Fatalf("bind teacher: %d %s", code, raw)
	}
	return tenant
}

func (h *harness) confirmedRevision(a, hh string) string {
	h.t.Helper()
	code, out, raw, _ := h.call("u_alice", "POST", "/api/edu/problems", map[string]any{"familyId": "SOLID-ANGLE-02", "title": "正四棱锥线面角", "structured": problem(a, hh)})
	if code != 201 {
		h.t.Fatalf("create problem: %d %s", code, raw)
	}
	rev := data(out)["revision"].(map[string]any)
	if rev["state"] != "needs_confirmation" {
		h.t.Fatalf("new revision must need confirmation: %v", rev)
	}
	// Analysis before confirmation is refused.
	if code, _, _, _ := h.call("u_alice", "POST", "/api/edu/problem-revisions/"+rev["id"].(string)+"/analyze", map[string]any{"idempotencyKey": "early"}); code != 409 {
		h.t.Fatalf("unconfirmed analyze must be 409, got %d", code)
	}
	if code, _, _, _ := h.call("u_alice", "POST", "/api/edu/problem-revisions/"+rev["id"].(string)+"/confirm", map[string]any{"inputHash": "sha256:bad"}); code != 409 {
		h.t.Fatalf("confirm with wrong hash must be 409, got %d", code)
	}
	code, _, raw, _ = h.call("u_alice", "POST", "/api/edu/problem-revisions/"+rev["id"].(string)+"/confirm", map[string]any{"inputHash": rev["inputHash"]})
	if code != 200 {
		h.t.Fatalf("confirm: %d %s", code, raw)
	}
	return rev["id"].(string)
}

func fetch(h *harness, user, signed string) (int, string, http.Header) {
	u, _ := url.Parse(signed)
	code, _, raw, hd := h.call(user, "GET", u.RequestURI(), nil)
	return code, string(raw), hd
}

// TestG1SolidAngle02EndToEnd drives the first real chain against the real Python
// math service: confirm → analyze → evidence → lesson → silent preview →
// review/publish → authorized retrieval (teacher + student) → settlement.
func TestG1SolidAngle02EndToEnd(t *testing.T) { runG1(t, newHarness(t, startMathService(t))) }

func runG1(t *testing.T, h *harness) {
	tenant := h.setupTeacher()
	revID := h.confirmedRevision("2", "1")

	// Analyze returns a job immediately; same key returns the same job.
	code, out, raw, _ := h.call("u_alice", "POST", "/api/edu/problem-revisions/"+revID+"/analyze", map[string]any{"idempotencyKey": "k-analyze-1"})
	if code != 202 {
		t.Fatalf("analyze: %d %s", code, raw)
	}
	jobID := data(out)["id"].(string)
	_, out2, _, _ := h.call("u_alice", "POST", "/api/edu/problem-revisions/"+revID+"/analyze", map[string]any{"idempotencyKey": "k-analyze-1"})
	if data(out2)["id"] != jobID {
		t.Fatal("idempotent analyze must return the same job")
	}
	job := h.waitJob("u_alice", jobID)
	if job["status"] != "succeeded" || job["settlement"] != "settled" {
		t.Fatalf("analysis job: %v", job)
	}
	solID, _ := job["solutionRevisionId"].(string)
	var sol model.EduSolutionRevision
	h.db.First(&sol, "id = ?", solID)
	if sol.MathState != "verified" || !strings.Contains(sol.EvidenceLevels, "symbolic") || sol.SolutionStatus != "unique" {
		t.Fatalf("solution evidence: %+v", sol)
	}

	// Lesson build from the verified solution.
	code, out, raw, _ = h.call("u_alice", "POST", "/api/edu/solution-revisions/"+solID+"/lesson", map[string]any{"idempotencyKey": "k-lesson-1"})
	if code != 202 {
		t.Fatalf("lesson: %d %s", code, raw)
	}
	lj := h.waitJob("u_alice", data(out)["id"].(string))
	if lj["status"] != "succeeded" {
		t.Fatalf("lesson job: %v", lj)
	}
	lessonID := lj["lessonRevisionId"].(string)
	_, out, _, _ = h.call("u_alice", "GET", "/api/edu/lesson-revisions/"+lessonID, nil)
	lesson := data(out)["lesson"].(map[string]any)
	hash := lesson["lessonHash"].(string)
	if lesson["state"] != "ready_for_review" {
		t.Fatalf("lesson state: %v", lesson)
	}

	// Review/publish binding (constraint 3).
	if code, _, _, _ := h.call("u_alice", "POST", "/api/edu/lesson-revisions/"+lessonID+"/publish", map[string]any{"lessonHash": hash}); code != 409 {
		t.Fatalf("publish before review must be 409, got %d", code)
	}
	if code, _, _, _ := h.call("u_alice", "POST", "/api/edu/lesson-revisions/"+lessonID+"/review", map[string]any{"lessonHash": "sha256:other", "decision": "approved"}); code != 409 {
		t.Fatalf("review with stale hash must be 409, got %d", code)
	}
	// second_reviewer policy blocks self review.
	h.db.Save(&model.EduOrgPolicy{TenantID: tenant, ReviewMode: "second_reviewer"})
	if code, _, _, _ := h.call("u_alice", "POST", "/api/edu/lesson-revisions/"+lessonID+"/review", map[string]any{"lessonHash": hash, "decision": "approved"}); code != 403 {
		t.Fatalf("self review under second_reviewer must be 403, got %d", code)
	}
	h.db.Save(&model.EduOrgPolicy{TenantID: tenant, ReviewMode: "self_allowed"})
	code, out, raw, _ = h.call("u_alice", "POST", "/api/edu/lesson-revisions/"+lessonID+"/review", map[string]any{"lessonHash": hash, "decision": "approved", "comment": "自审：已核对"})
	if code != 200 || data(out)["selfReview"] != true {
		t.Fatalf("self review: %d %s", code, raw)
	}
	code, out, raw, _ = h.call("u_alice", "POST", "/api/edu/lesson-revisions/"+lessonID+"/publish", map[string]any{"lessonHash": hash})
	if code != 200 {
		t.Fatalf("publish: %d %s", code, raw)
	}
	pubID := data(out)["id"].(string)
	_, out, _, _ = h.call("u_alice", "POST", "/api/edu/lesson-revisions/"+lessonID+"/publish", map[string]any{"lessonHash": hash})
	if data(out)["id"] != pubID {
		t.Fatal("publish must be idempotent and immutable")
	}

	// Teacher retrieves the silent preview through a short-lived signed URL.
	var l model.EduLessonRevision
	h.db.First(&l, "id = ?", lessonID)
	code, out, raw, _ = h.call("u_alice", "GET", "/api/edu/artifacts/"+l.PreviewArtifact+"/url", nil)
	if code != 200 {
		t.Fatalf("teacher url: %d %s", code, raw)
	}
	teacherURL := data(out)["url"].(string)
	code, body, hd := fetch(h, "", teacherURL)
	if code != 200 || !strings.Contains(hd.Get("Content-Security-Policy"), "sandbox") || hd.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("teacher preview fetch: %d %v", code, hd)
	}
	if !strings.Contains(body, "√22") || strings.Contains(strings.ToLower(body), "<script") {
		t.Fatalf("teacher preview must show the answer and have no script")
	}
	// Tampered or reused-by-another-user links fail.
	if code, _, _ := fetch(h, "", strings.Replace(teacherURL, "u=u_alice", "u=u_bob", 1)); code != 403 {
		t.Fatalf("tampered url must be 403, got %d", code)
	}

	// Student path (W1 APIs): class → invite → join → consent → assignment.
	stu := []string{"X-Edu-Tenant", tenant}
	code, out, raw, _ = h.call("u_alice", "POST", "/api/edu/classes", map[string]any{"name": "高二(3)班"})
	if code != 201 {
		t.Fatalf("create class: %d %s", code, raw)
	}
	classID := data(out)["id"].(string)
	code, out, raw, _ = h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/assignments", map[string]any{"publicationId": pubID, "title": "线面角练习"})
	if code != 201 {
		t.Fatalf("create assignment: %d %s", code, raw)
	}
	asID := data(out)["id"].(string)
	if code, _, _, _ := h.call("u_bob", "GET", "/api/edu/student/assignments/"+asID, nil, stu...); code != 403 && code != 404 {
		t.Fatalf("non-member must be refused, got %d", code)
	}
	code, out, raw, _ = h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/invites", map[string]any{"ttlHours": 24, "maxUses": 30})
	if code != 201 {
		t.Fatalf("invite: %d %s", code, raw)
	}
	inviteCode := data(out)["code"].(string)
	wrongLast := "0"
	if strings.HasSuffix(inviteCode, "0") {
		wrongLast = "1"
	}
	if code, _, _, _ := h.call("u_bob", "POST", "/api/edu-join", map[string]any{"code": inviteCode[:len(inviteCode)-1] + wrongLast}); code != 404 {
		t.Fatalf("wrong secret must be 404, got %d", code)
	}
	code, out, raw, _ = h.call("u_bob", "POST", "/api/edu-join", map[string]any{"code": inviteCode})
	if code != 200 || data(out)["consentRequired"] != true {
		t.Fatalf("join: %d %s", code, raw)
	}
	enrollID := data(out)["enrollmentId"].(string)
	var wsm int64
	h.db.Model(&model.WorkspaceMembership{}).Where("user_id = ?", "u_bob").Where("workspace_id = ?", model.PersonalWorkspaceID("u_alice")).Count(&wsm)
	if wsm != 0 {
		t.Fatal("joining a class must never grant workspace membership")
	}
	// Guardian consent missing → blocked (FR-I05), even though enrolled.
	code, out, _, _ = h.call("u_bob", "GET", "/api/edu/student/assignments/"+asID, nil, stu...)
	if code != 403 || data(out)["error"].(map[string]any)["code"] != "CONSENT_REQUIRED" {
		t.Fatalf("missing consent must block, got %d %v", code, out)
	}
	if code, _, _, _ := h.call("u_alice", "POST", "/api/edu/consents", map[string]any{"studentUserId": "u_bob", "status": "granted"}); code != 400 {
		t.Fatalf("consent without evidence must be 400, got %d", code)
	}
	if code, _, raw, _ := h.call("u_alice", "POST", "/api/edu/consents", map[string]any{"studentUserId": "u_bob", "status": "granted", "evidenceRef": "form-2026-0001"}); code != 200 {
		t.Fatalf("record consent: %d %s", code, raw)
	}
	code, out, raw, _ = h.call("u_bob", "GET", "/api/edu/student/assignments/"+asID, nil, stu...)
	if code != 200 {
		t.Fatalf("student view: %d %s", code, raw)
	}
	for _, leak := range []string{"√22", "sqrt(22)", "\\\\sqrt{22}", "\"answer\""} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("student response leaks %q", leak)
		}
	}
	studentURL := data(out)["preview"].(map[string]any)["url"].(string)
	code, body, _ = fetch(h, "", studentURL)
	if code != 200 || strings.Contains(body, "√22") || strings.Contains(body, "sqrt{22}") {
		t.Fatalf("student preview fetch: %d leak=%v", code, strings.Contains(body, "√22"))
	}
	// Students never reach staff endpoints or teacher artifacts.
	if code, _, _, _ := h.call("u_bob", "GET", "/api/edu/jobs/"+jobID, nil); code != 403 {
		t.Fatalf("student on staff endpoint must be 403, got %d", code)
	}
	if code, _, _, _ := h.call("u_bob", "GET", "/api/edu/classes/"+classID+"/roster", nil); code != 403 {
		t.Fatalf("student reading roster must be 403, got %d", code)
	}
	bobTeacherURL, _ := h.svc.SignedURL(l.PreviewArtifact, "u_bob", time.Minute)
	if code, _, _ := fetch(h, "", bobTeacherURL); code != 403 {
		t.Fatalf("student fetching teacher artifact must be 403, got %d", code)
	}
	// Consent withdrawal blocks already-issued links.
	if code, _, raw, _ := h.call("u_alice", "POST", "/api/edu/consents", map[string]any{"studentUserId": "u_bob", "status": "withdrawn"}); code != 200 {
		t.Fatalf("withdraw consent: %d %s", code, raw)
	}
	if code, _, _ := fetch(h, "", studentURL); code != 403 {
		t.Fatalf("consent withdrawn must block fetch, got %d", code)
	}
	h.call("u_alice", "POST", "/api/edu/consents", map[string]any{"studentUserId": "u_bob", "status": "granted", "evidenceRef": "form-2026-0002"})
	if code, _, _ := fetch(h, "", studentURL); code != 200 {
		t.Fatalf("re-granted consent: %d", code)
	}
	// Withdrawing the enrollment blocks new fetches of already-issued links; the code cannot re-enroll.
	if code, _, raw, _ := h.call("u_alice", "POST", "/api/edu/enrollments/"+enrollID+"/withdraw", nil); code != 200 {
		t.Fatalf("withdraw: %d %s", code, raw)
	}
	if code, _, _ := fetch(h, "", studentURL); code != 403 {
		t.Fatalf("withdrawn student must be blocked, got %d", code)
	}
	if code, _, _, _ := h.call("u_bob", "POST", "/api/edu-join", map[string]any{"code": inviteCode}); code != 403 {
		t.Fatalf("withdrawn student re-joining by code must be 403, got %d", code)
	}
	// Expired links fail.
	h.now.Store(time.Now().Add(6 * time.Minute).Unix())
	if code, _, _ := fetch(h, "", teacherURL); code != 403 {
		t.Fatalf("expired link must be 403, got %d", code)
	}
	h.now.Store(0)

	// Settlement: each finished stage charged once; roots settled; reserved back to 0.
	var runs []model.EduStageRun
	h.db.Find(&runs)
	var sum int64
	for _, r := range runs {
		if r.SettledAt == nil && r.CostMicro > 0 {
			t.Fatalf("unsettled stage run %+v", r)
		}
		sum += r.CostMicro
	}
	if len(runs) != 6 {
		t.Fatalf("expected 6 stage runs (3 analyze + 3 lesson), got %d", len(runs))
	}
	var acct model.CreditAccount
	h.db.First(&acct, "user_id = ?", "u_alice")
	if acct.ReservedMicrocredits != 0 || acct.AvailableMicrocredits != 1_000_000-sum {
		t.Fatalf("ledger mismatch: %+v sum=%d", acct, sum)
	}
	var usage int64
	h.db.Model(&model.FlowUsageRecord{}).Where("usage_type = ?", flow.UsageMathComputeMS).Count(&usage)
	if usage != 6 {
		t.Fatalf("usage lines = %d", usage)
	}
	// Re-syncing terminal jobs never charges again.
	for i := 0; i < 3; i++ {
		h.waitJob("u_alice", jobID)
		_, _ = h.svc.SyncOnce(context.Background(), 50)
	}
	h.db.First(&acct, "user_id = ?", "u_alice")
	if acct.AvailableMicrocredits != 1_000_000-sum {
		t.Fatal("re-sync must not charge twice")
	}
}

// TestBudgetExceededNothingDispatched: insufficient funds → BUDGET_EXCEEDED, no service job.
func TestBudgetExceededNothingDispatched(t *testing.T) {
	h := newHarness(t, startMathService(t))
	h.setupTeacher()
	h.db.Save(&model.CreditAccount{UserID: "u_alice", AvailableMicrocredits: 10})
	revID := h.confirmedRevision("2", "1")
	code, _, raw, _ := h.call("u_alice", "POST", "/api/edu/problem-revisions/"+revID+"/analyze", map[string]any{"idempotencyKey": "k"})
	if code != 402 || !strings.Contains(string(raw), "BUDGET_EXCEEDED") {
		t.Fatalf("want 402 BUDGET_EXCEEDED, got %d %s", code, raw)
	}
	var n int64
	h.db.Model(&model.EduMathJob{}).Count(&n)
	if n != 0 {
		t.Fatal("no job may be created without budget")
	}
}

// TestWorkerRecoveryAndReconcile: the background worker alone completes a job
// (no client polling), and an unknown submit outcome is reconciled by key —
// both when the service did accept it and when it never arrived.
func TestWorkerRecoveryAndReconcile(t *testing.T) {
	base := startMathService(t)
	target, _ := url.Parse(base)
	var dropStart atomic.Bool
	var starts atomic.Int64
	proxy := httputil.NewSingleHostReverseProxy(target)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/v1/jobs" {
			starts.Add(1)
		}
		if dropStart.Load() && r.Method == "POST" && r.URL.Path == "/v1/jobs" {
			// Forward (the service accepts it) then drop the connection: outcome unknown to the caller.
			rec := httptest.NewRecorder()
			proxy.ServeHTTP(rec, r)
			hj, _ := w.(http.Hijacker)
			conn, buf, _ := hj.Hijack()
			_ = buf.Flush()
			conn.Close()
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer front.Close()
	h := newHarness(t, front.URL)
	h.setupTeacher()
	revID := h.confirmedRevision("2", "2")

	// 1) Accepted-but-unknown → reconciliation_required, reservation held, no blind retry.
	dropStart.Store(true)
	code, out, raw, _ := h.call("u_alice", "POST", "/api/edu/problem-revisions/"+revID+"/analyze", map[string]any{"idempotencyKey": "k-unknown"})
	if code != 202 || data(out)["status"] != "reconciliation_required" {
		t.Fatalf("want reconciliation_required, got %d %s", code, raw)
	}
	jobID := data(out)["id"].(string)
	var acct model.CreditAccount
	h.db.First(&acct, "user_id = ?", "u_alice")
	if acct.ReservedMicrocredits == 0 {
		t.Fatal("budget must stay held while the outcome is unknown")
	}
	dropStart.Store(false)
	// 2) Worker-only progress: reconcile finds the accepted job and drives it to completion.
	for i := 0; i < 600; i++ {
		_, _ = h.svc.SyncOnce(context.Background(), 50)
		var j model.EduMathJob
		h.db.First(&j, "id = ?", jobID)
		if j.Status == "succeeded" && j.Settlement == "settled" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	var j model.EduMathJob
	h.db.First(&j, "id = ?", jobID)
	if j.Status != "succeeded" || j.ServiceJobID == "" || j.Settlement != "settled" {
		t.Fatalf("reconciled job: %+v", j)
	}
	if starts.Load() != 1 {
		t.Fatalf("an accepted-but-unknown submit must be found by key, not resent (starts=%d)", starts.Load())
	}
	// 3) Never-arrived → reconcile proves not executed and resubmits under the same key.
	h.svc.Client.BaseURL = "http://127.0.0.1:1"
	code, out, _, _ = h.call("u_alice", "POST", "/api/edu/problem-revisions/"+revID+"/analyze", map[string]any{"idempotencyKey": "k-lost"})
	if code != 202 || data(out)["status"] != "reconciliation_required" {
		t.Fatalf("lost submit must be reconciliation_required: %v", out)
	}
	h.svc.Client.BaseURL = front.URL
	lost := data(out)["id"].(string)
	code, out, _, _ = h.call("u_alice", "POST", "/api/edu/jobs/"+lost+"/reconcile", nil)
	if code != 200 || data(out)["status"] == "reconciliation_required" {
		t.Fatalf("reconcile: %d %v", code, out)
	}
	if done := h.waitJob("u_alice", lost); done["status"] != "succeeded" {
		t.Fatalf("resubmitted job: %v", done)
	}
}

// TestStudentLeakGuard covers the platform-side second answer guard.
func TestStudentLeakGuard(t *testing.T) {
	teacher := []byte(`{"answer":{"plain":"2√22/11","exact":"2*sqrt(22)/11","latex":"\\frac{2\\sqrt{22}}{11}"},"check_answer_key":{"plain":"2√7/7"}}`)
	ok := map[string]any{"projection": "student", "reveal": "none", "hints": []any{"考虑建系"}}
	if StudentLeak(ok, teacher) {
		t.Fatal("clean projection flagged")
	}
	for _, bad := range []map[string]any{
		{"projection": "student", "reveal": "none", "hints": []any{"答案是 2√22/11"}},
		{"projection": "student", "reveal": "none", "x": map[string]any{"k": "2*sqrt(22)/11"}},
		{"projection": "student", "reveal": "none", "answer": "x"},
		{"projection": "teacher", "reveal": "none"},
		{"projection": "student", "reveal": "none", "cq": "2√7/7"},
		{"projection": "student", "reveal": "none", "check_answer_key": map[string]any{}},
		{"projection": "student", "reveal": "none", "beats": []any{map[string]any{"id": "s1", "locked": true, "board": []any{"x"}}}},
		{"projection": "student", "reveal": "none", "beats": []any{map[string]any{"id": "s1"}}},
	} {
		if !StudentLeak(bad, teacher) {
			t.Fatalf("leak not caught: %v", bad)
		}
	}
	// Integer answers: a marker that is part of the public givens is skipped,
	// any other occurrence is still caught.
	vol := []byte(`{"answer":{"plain":"60 cm³","exact":"60","latex":"60"},"check_answer_key":{"plain":"3","exact":"3"},` +
		`"problem":{"text":"x²/4 + y²/3 = 1","params":{"a":"2"}},"check_question":{"prompt":"p","params":{}}}`)
	clean := map[string]any{"projection": "student", "reveal": "none", "problem": map[string]any{"text": "x²/4 + y²/3 = 1"},
		"beats": []any{map[string]any{"id": "s1", "title": "t", "locked": true}}}
	if StudentLeak(clean, vol) {
		t.Fatal("public given flagged as leak")
	}
	if !StudentLeak(map[string]any{"projection": "student", "reveal": "none", "hints": []any{"体积为 60"}}, vol) {
		t.Fatal("integer answer leak not caught")
	}
}
