package edumath

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"yingce/backend/internal/database"
	"yingce/backend/internal/model"
)

// Actual app-role PostgreSQL and real artifact HTTP transport. The peer's
// deterministic bytes/publication rows are explicit fixtures, not live lessons.
func TestEduPostgresSignedArtifactAdmission(t *testing.T) {
	if os.Getenv("AGRI_TEST_PG_ISOLATED") != "1" || os.Getenv("AGRI_TEST_PG_MIGRATION_DSN") == "" || os.Getenv("AGRI_TEST_PG_RUNTIME_DSN") == "" {
		t.Skip("requires owned isolated platform PostgreSQL roles")
	}
	open := func(key string) *gorm.DB {
		db, err := database.Open(database.Config{Driver: "postgres", DSN: os.Getenv(key)})
		if err != nil {
			t.Fatal("fixture connection unavailable")
		}
		db = db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
		pool, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		pool.SetMaxOpenConns(1)
		pool.SetMaxIdleConns(1)
		t.Cleanup(func() { pool.Close() })
		return db
	}
	owner, app := open("AGRI_TEST_PG_MIGRATION_DSN"), open("AGRI_TEST_PG_RUNTIME_DSN")
	if err := database.MigrateSchema(owner); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	suffix := fmt.Sprintf("ef%d", now.UnixNano())
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	data := []byte("explicit student artifact fixture")
	sum := sha256.Sum256(data)
	sha := "sha256:" + hex.EncodeToString(sum[:])
	type school struct{ tenant, workspace, student, teacher, role, artifact, serviceArtifact, assignment string }
	schools := []school{}
	for i := 0; i < 2; i++ {
		tag := fmt.Sprintf("%d_%s", i, suffix)
		s := school{tenant: "tn_" + tag, workspace: "ws_" + tag, student: "s_" + tag, teacher: "t_" + tag, role: "r_" + tag, artifact: "a_" + tag, serviceArtifact: fmt.Sprintf("ma_%032x", uint64(now.UnixNano())+uint64(i)), assignment: "as_" + tag}
		job, lesson, pub, class, problem, revision := "j_"+tag, "l_"+tag, "p_"+tag, "c_"+tag, "pr_"+tag, "rv_"+tag
		for _, row := range []any{
			&model.User{ID: s.student, Username: s.student, Status: "active"}, &model.User{ID: s.teacher, Username: s.teacher, Status: "active"},
			&model.Tenant{ID: s.tenant, Kind: "organization", OwnerUserID: s.teacher, Status: "active"}, &model.Workspace{ID: s.workspace, TenantID: s.tenant, Slug: "fixture", Name: "fixture"},
			&model.EduRoleBinding{ID: s.role, TenantID: s.tenant, UserID: s.student, Role: "student", Status: "active"},
			&model.EduClass{ID: class, TenantID: s.tenant, WorkspaceID: s.workspace, Name: "fixture", Status: "active"},
			&model.EduTeachingAssignment{ID: "ta_" + tag, TenantID: s.tenant, ClassID: class, UserID: s.teacher, Status: "active"},
			&model.EduEnrollment{ID: "en_" + tag, TenantID: s.tenant, ClassID: class, StudentUserID: s.student, Status: "active"},
			&model.EduConsentRecord{ID: "co_" + tag, TenantID: s.tenant, StudentUserID: s.student, Kind: "guardian", Status: "granted", EvidenceRef: "fixture-only", RecordedBy: s.teacher},
			&model.EduProblem{ID: problem, TenantID: s.tenant, WorkspaceID: s.workspace, FamilyID: "fixture"},
			&model.EduProblemRevision{ID: revision, TenantID: s.tenant, ProblemID: problem, Revision: 1, Structured: "{}", InputHash: "fixture", State: "confirmed"},
			&model.EduMathJob{ID: job, TenantID: s.tenant, WorkspaceID: s.workspace, Kind: "lesson_build", IdemKey: tag, InputHash: "fixture", ProblemRevisionID: revision, Status: "succeeded", Settlement: "settled"},
			&model.EduArtifact{ID: s.artifact, TenantID: s.tenant, JobID: job, ServiceArtifactID: s.serviceArtifact, Kind: "student_projection", Audience: "student", MediaType: "text/plain", SHA256: sha, Size: int64(len(data))},
			&model.EduLessonRevision{ID: lesson, TenantID: s.tenant, ProblemRevisionID: revision, SolutionRevisionID: "solution_" + tag, JobID: job, StudentArtifact: s.artifact, LessonHash: "fixture", State: "approved"},
			&model.EduPublication{ID: pub, TenantID: s.tenant, LessonRevisionID: lesson, LessonHash: "fixture", ReviewRecordID: "review_" + tag, State: "published", PublishedBy: s.teacher},
			&model.EduAssignment{ID: s.assignment, TenantID: s.tenant, ClassID: class, PublicationID: pub, State: "active", Title: "fixture"},
		} {
			if err := owner.Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
		schools = append(schools, s)
	}
	var networkCalls, freeConnections atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		networkCalls.Add(1)
		// Prove authorization transaction ended before fetching artifact bytes.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var safe bool
		if err := app.WithContext(ctx).Raw("SELECT coalesce(current_setting('app.tenant_ids',true),'')='' AND txid_current_if_assigned() IS NULL").Scan(&safe).Error; err != nil || !safe {
			w.WriteHeader(503)
			return
		}
		freeConnections.Add(1)
		contextBytes, err := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Math-Context"))
		if err != nil {
			w.WriteHeader(401)
			return
		}
		var claims struct {
			Tenant   string `json:"tenant_id"`
			Audience string `json:"aud"`
			Expiry   int64  `json:"exp"`
		}
		if json.Unmarshal(contextBytes, &claims) != nil || claims.Tenant != schools[0].tenant || claims.Audience != "math-service" || claims.Expiry <= time.Now().Unix() {
			w.WriteHeader(403)
			return
		}
		bodyHash := sha256.Sum256(nil)
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(r.Header.Get("X-Math-Context") + "\n" + r.Method + "\n" + r.URL.RequestURI() + "\n" + hex.EncodeToString(bodyHash[:])))
		if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(r.Header.Get("X-Math-Signature"))) || r.URL.Path != "/v1/artifacts/"+schools[0].serviceArtifact {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Artifact-Sha256", sha)
		w.Header().Set("X-Artifact-Audience", "student")
		_, _ = w.Write(data)
	}))
	defer peer.Close()
	service := &Service{DB: app, URLKey: key, Client: Client{BaseURL: peer.URL, Key: key, HTTP: peer.Client()}}
	user := func(c *gin.Context) (*model.User, error) {
		if c.GetHeader("X-Test-Session") == schools[0].student {
			return &model.User{ID: schools[0].student, Status: "active"}, nil
		}
		return nil, nil
	}
	router := gin.New()
	(&API{DB: app, Svc: service, User: user}).Register(router.Group("/api"))
	signed, _ := service.SignedURL(schools[0].artifact, schools[0].student, time.Minute)
	request := func(path, actor, tenant string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Test-Session", actor)
		req.Header.Set("X-Edu-Tenant", tenant)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	denied := func(t *testing.T, path string) {
		t.Helper()
		before := networkCalls.Load()
		w := request(path, "", "")
		if w.Code != 403 {
			t.Fatalf("signed artifact denial status %d", w.Code)
		}
		if networkCalls.Load() != before {
			t.Fatal("denied request fetched external bytes")
		}
	}
	t.Run("active_nonmember_student", func(t *testing.T) {
		var n int64
		if err := owner.Model(&model.Membership{}).Where("user_id=?", schools[0].student).Count(&n).Error; err != nil || n != 0 {
			t.Fatal("fixture must have no platform tenant membership")
		}
		ids, err := eduTenantIDs(app, schools[0].student)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, id := range ids {
			if id == schools[0].tenant {
				found = true
			}
			if id == schools[1].tenant {
				t.Fatal("resolver leaked other school")
			}
		}
		if !found {
			t.Fatal("active nonmember student lost school admission")
		}
		w := request("/api/edu/student/assignments", schools[0].student, schools[0].tenant)
		if w.Code != 200 || !strings.Contains(w.Body.String(), schools[0].assignment) || strings.Contains(w.Body.String(), schools[1].assignment) {
			t.Fatalf("student assignment admission status %d", w.Code)
		}
		w = request(signed, "", "")
		if w.Code != 200 || w.Body.String() != string(data) || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("valid signed artifact status %d", w.Code)
		}
	})
	t.Run("signature_negatives_no_http", func(t *testing.T) {
		u, _ := url.Parse(signed)
		q := u.Query()
		q.Set("u", schools[1].student)
		u.RawQuery = q.Encode()
		denied(t, u.String())
		u, _ = url.Parse(signed)
		u.Path = "/api/edu/files/" + schools[1].artifact
		denied(t, u.String())
		u, _ = url.Parse(signed)
		q = u.Query()
		q.Set("sig", strings.Repeat("0", 64))
		u.RawQuery = q.Encode()
		denied(t, u.String())
		old := *service
		old.Now = func() time.Time { return now.Add(-10 * time.Minute) }
		expired, _ := old.SignedURL(schools[0].artifact, schools[0].student, time.Minute)
		denied(t, expired)
	})
	t.Run("revoked_actor_or_role_no_http", func(t *testing.T) {
		change := func(row any, column string, value any) {
			if err := owner.Model(row).Update(column, value).Error; err != nil {
				t.Fatal(err)
			}
		}
		actor := model.User{ID: schools[0].student}
		binding := model.EduRoleBinding{ID: schools[0].role}
		change(&actor, "status", "disabled")
		denied(t, signed)
		change(&actor, "status", "active")
		change(&binding, "status", "revoked")
		denied(t, signed)
		change(&binding, "status", "active")
		past := now.Add(-time.Minute)
		change(&binding, "expires_at", past)
		denied(t, signed)
		change(&binding, "expires_at", nil)
		artifact := model.EduArtifact{ID: schools[0].artifact}
		change(&artifact, "revoked_at", now)
		denied(t, signed)
		change(&artifact, "revoked_at", nil)
	})
	t.Run("pooled_scope_and_cross_school", func(t *testing.T) {
		cross, _ := service.SignedURL(schools[1].artifact, schools[0].student, time.Minute)
		denied(t, cross)
		w := request("/api/edu/student/assignments", schools[0].student, schools[1].tenant)
		if w.Code != 403 {
			t.Fatalf("forged school header status %d", w.Code)
		}
		var n int64
		if err := app.Model(&model.EduArtifact{}).Where("id IN ?", []string{schools[0].artifact, schools[1].artifact}).Count(&n).Error; err != nil || n != 0 {
			t.Fatal("artifact scope leaked into connection pool")
		}
		if w := request(signed, "", ""); w.Code != 200 {
			t.Fatalf("valid scope failed after denials %d", w.Code)
		}
	})
	if networkCalls.Load() != 2 || freeConnections.Load() != 2 {
		t.Fatalf("fixture transport calls=%d no-transaction=%d", networkCalls.Load(), freeConnections.Load())
	}
}

func TestEduPostgresOnboardingAdmission(t *testing.T) {
	if os.Getenv("AGRI_TEST_PG_ISOLATED") != "1" || os.Getenv("AGRI_TEST_PG_MIGRATION_DSN") == "" || os.Getenv("AGRI_TEST_PG_RUNTIME_DSN") == "" {
		t.Skip("requires owned isolated platform PostgreSQL roles")
	}
	open := func(key string) *gorm.DB {
		db, err := database.Open(database.Config{Driver: "postgres", DSN: os.Getenv(key)})
		if err != nil {
			t.Fatal("fixture connection unavailable")
		}
		db = db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
		pool, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { pool.Close() })
		return db
	}
	owner, app := open("AGRI_TEST_PG_MIGRATION_DSN"), open("AGRI_TEST_PG_RUNTIME_DSN")
	if err := database.MigrateSchema(owner); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	tag := fmt.Sprintf("join%d", now.UnixNano())
	tenant, class := "tn_"+tag, "cl_"+tag
	users := map[string]*model.User{}
	for _, prefix := range []string{"a", "b", "c", "d"} {
		id := prefix + "_" + tag
		u := &model.User{ID: id, Username: id, Status: "active"}
		if err := owner.Create(u).Error; err != nil {
			t.Fatal(err)
		}
		users[prefix] = u
	}
	for _, row := range []any{&model.Tenant{ID: tenant, Kind: "organization", OwnerUserID: users["a"].ID, Status: "active"}, &model.EduClass{ID: class, TenantID: tenant, WorkspaceID: "ws_" + tag, Name: "fixture", Status: "active"}} {
		if err := owner.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	secretBytes := make([]byte, 24)
	if _, err := rand.Read(secretBytes); err != nil {
		t.Fatal(err)
	}
	secret := hex.EncodeToString(secretBytes)
	invite := model.EduClassInvite{ID: "eci_" + tag, TenantID: tenant, ClassID: class, SecretHash: secretHash(secret), ExpiresAt: now.Add(time.Hour), MaxUses: 1, Status: "active"}
	roster := model.EduRosterEntry{ID: "erc_" + tag, TenantID: tenant, ClassID: class, ExternalRef: "fixture", State: "pending", SecretHash: secretHash(secret), ExpiresAt: now.Add(time.Hour)}
	for _, row := range []any{&invite, &roster} {
		if err := owner.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	router := gin.New()
	user := func(c *gin.Context) (*model.User, error) { return users[c.GetHeader("X-Test-Actor")], nil }
	(&API{DB: app, Svc: &Service{DB: app}, User: user}).Register(router.Group("/api"))
	call := func(actor, path, code string) int {
		body, _ := json.Marshal(map[string]any{"code": code, "tenant_id": "forged"})
		req := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-Actor", actor)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}
	inviteCode, rosterCode := invite.ID+"."+secret, roster.ID+"."+secret
	t.Run("class_invite_hash_and_atomic_limit", func(t *testing.T) {
		if code := call("", "/api/edu-join", inviteCode); code != 401 {
			t.Fatalf("anonymous status %d", code)
		}
		if code := call("b", "/api/edu-join", invite.ID+"."+strings.Repeat("0", 48)); code != 404 {
			t.Fatalf("bad secret status %d", code)
		}
		if code := call("b", "/api/edu-join", inviteCode); code != 200 {
			t.Fatalf("valid join status %d", code)
		}
		if code := call("b", "/api/edu-join", inviteCode); code != 200 {
			t.Fatalf("active enrollment idempotent replay status %d", code)
		}
		if code := call("c", "/api/edu-join", inviteCode); code != 404 {
			t.Fatalf("exhausted invite status %d", code)
		}
		var current model.EduClassInvite
		if err := owner.First(&current, "id=?", invite.ID).Error; err != nil || current.Uses != 1 {
			t.Fatal("invite count not atomic/idempotent")
		}
	})
	t.Run("roster_claim_single_use", func(t *testing.T) {
		if code := call("c", "/api/edu-roster-claim", roster.ID+"."+strings.Repeat("0", 48)); code != 404 {
			t.Fatalf("bad roster secret status %d", code)
		}
		if code := call("c", "/api/edu-roster-claim", rosterCode); code != 200 {
			t.Fatalf("valid roster claim status %d", code)
		}
		if code := call("d", "/api/edu-roster-claim", rosterCode); code != 404 {
			t.Fatalf("reused roster claim status %d", code)
		}
		var current model.EduRosterEntry
		if err := owner.First(&current, "id=?", roster.ID).Error; err != nil || current.State != "claimed" || current.ClaimedBy != users["c"].ID || current.SecretHash != "" {
			t.Fatal("roster claim receipt incorrect")
		}
	})
	t.Run("disabled_or_revoked_actor_denied", func(t *testing.T) {
		fresh := model.EduClassInvite{ID: "eci_second_" + tag, TenantID: tenant, ClassID: class, SecretHash: secretHash(secret), ExpiresAt: now.Add(time.Hour), MaxUses: 10, Status: "active"}
		if err := owner.Create(&fresh).Error; err != nil {
			t.Fatal(err)
		}
		freshCode := fresh.ID + "." + secret
		if err := owner.Model(&model.User{}).Where("id=?", users["d"].ID).Update("status", "disabled").Error; err != nil {
			t.Fatal(err)
		}
		if code := call("d", "/api/edu-join", freshCode); code != 404 {
			t.Fatalf("disabled account status %d", code)
		}
		if err := owner.Model(&model.EduRoleBinding{}).Where("tenant_id=? AND user_id=?", tenant, users["b"].ID).Update("status", "revoked").Error; err != nil {
			t.Fatal(err)
		}
		if code := call("b", "/api/edu-join", freshCode); code != 403 {
			t.Fatalf("revoked student role status %d", code)
		}
		if err := owner.Model(&fresh).Update("expires_at", now.Add(-time.Minute)).Error; err != nil {
			t.Fatal(err)
		}
		if code := call("c", "/api/edu-join", freshCode); code != 404 {
			t.Fatalf("expired invite status %d", code)
		}
		var current model.EduClassInvite
		if err := owner.First(&current, "id=?", fresh.ID).Error; err != nil || current.Uses != 0 {
			t.Fatal("denial consumed invitation")
		}
	})
	t.Run("metadata_scope_and_no_platform_grants", func(t *testing.T) {
		for _, row := range []any{&model.EduClassInvite{}, &model.EduRosterEntry{}, &model.EduEnrollment{}} {
			var n int64
			if err := app.Model(row).Where("tenant_id=?", tenant).Count(&n).Error; err != nil || n != 0 {
				t.Fatal("unscoped app sees onboarding metadata")
			}
		}
		for _, row := range []any{&model.Membership{}, &model.WorkspaceMembership{}} {
			var n int64
			if err := owner.Model(row).Where("user_id IN ?", []string{users["b"].ID, users["c"].ID}).Count(&n).Error; err != nil || n != 0 {
				t.Fatal("student onboarding granted platform membership")
			}
		}
		var n int64
		if err := owner.Model(&model.EduEnrollment{}).Where("tenant_id=? AND status='active'", tenant).Count(&n).Error; err != nil || n != 2 {
			t.Fatal("real enrollment rows missing")
		}
	})
}
