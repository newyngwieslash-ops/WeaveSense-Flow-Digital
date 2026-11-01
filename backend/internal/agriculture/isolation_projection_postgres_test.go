package agriculture

// These PostgreSQL projections are TEST-ONLY domain facts/events, not a W06
// provider. Authorization, sessions, cache and SSE use the production code.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"yingce/backend/internal/auth"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
)

type isolationPGRow struct {
	Tenant, Workspace, Actor, Job, Version, Value string
	Revision                                      int64
}
type isolationPGProvider struct {
	db        *gorm.DB
	loads     atomic.Int32
	afterLoad func()
}

func isolationPGTx(ctx context.Context, db *gorm.DB, tenant, workspace string, fn func(*gorm.DB) error) error {
	if tenant == "" || workspace == "" {
		return ErrNotFound
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT set_config('app.tenant_ids',?,true),set_config('app.workspace_id',?,true)", tenant, workspace).Error; err != nil {
			return err
		}
		return fn(tx)
	})
}
func (p *isolationPGProvider) row(ctx context.Context, scope ProjectionScope, job string) (isolationPGRow, error) {
	var row isolationPGRow
	err := isolationPGTx(ctx, p.db, scope.TenantID, scope.WorkspaceID, func(tx *gorm.DB) error {
		return tx.Table("agri_projection_test.rows").Where("tenant=? AND workspace=? AND actor=? AND job=?", scope.TenantID, scope.WorkspaceID, scope.ActorID, job).Take(&row).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrNotFound
	}
	return row, err
}
func (p *isolationPGProvider) Resolve(ctx context.Context, scope OwnershipContext, refs []ResourceRef) ([]ResourceOwnership, error) {
	out := []ResourceOwnership{}
	for _, ref := range refs {
		if ref.Kind != "job" {
			return nil, ErrNotFound
		}
		var row isolationPGRow
		err := isolationPGTx(ctx, p.db, scope.TenantID, scope.WorkspaceID, func(tx *gorm.DB) error {
			return tx.Table("agri_projection_test.rows").Where("tenant=? AND workspace=? AND job=?", scope.TenantID, scope.WorkspaceID, ref.ID).Order("actor").Take(&row).Error
		})
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		out = append(out, ResourceOwnership{Resource: ref, TenantID: row.Tenant, WorkspaceID: row.Workspace, FarmIDs: []string{"farm_projection"}, Revision: row.Revision, Status: "active", ReceiptID: "pg_projection_fixture", IssuedAt: now, ExpiresAt: now.Add(time.Minute)})
	}
	return out, nil
}

type isolationPGCacheProvider struct{ *isolationPGProvider }

func (p isolationPGCacheProvider) Describe(ctx context.Context, s ProjectionScope, q ProjectionQuery) (ProjectionBinding, error) {
	if len(q.Resources) != 1 {
		return ProjectionBinding{}, ErrInvalid
	}
	r, e := p.row(ctx, s, q.Resources[0].ID)
	return ProjectionBinding{s, "fixture_projection", r.Version}, e
}
func (p isolationPGCacheProvider) Load(ctx context.Context, b ProjectionBinding, q ProjectionQuery) ([]byte, error) {
	r, e := p.row(ctx, b.Scope, q.Resources[0].ID)
	if e != nil {
		return nil, e
	}
	p.loads.Add(1)
	if p.afterLoad != nil {
		f := p.afterLoad
		p.afterLoad = nil
		f()
	}
	return []byte(r.Value), nil
}

type isolationPGEventProvider struct{ *isolationPGProvider }

func (p isolationPGEventProvider) Describe(ctx context.Context, s ProjectionScope, job string) (JobStreamBinding, error) {
	r, e := p.row(ctx, s, job)
	return JobStreamBinding{s, job, "fixture_events", r.Version}, e
}
func (p isolationPGEventProvider) Read(ctx context.Context, b JobStreamBinding, after int64, limit int) ([]JobEvent, error) {
	var rows []struct {
		Sequence int64
		Data     string
	}
	e := isolationPGTx(ctx, p.db, b.Scope.TenantID, b.Scope.WorkspaceID, func(tx *gorm.DB) error {
		return tx.Table("agri_projection_test.events").Where("tenant=? AND workspace=? AND actor=? AND job=? AND sequence>?", b.Scope.TenantID, b.Scope.WorkspaceID, b.Scope.ActorID, b.JobID, after).Order("sequence").Limit(limit).Find(&rows).Error
	})
	out := []JobEvent{}
	for _, r := range rows {
		out = append(out, JobEvent{r.Sequence, json.RawMessage(r.Data)})
	}
	return out, e
}

type isolationPGFixture struct {
	s              Service
	seed           *gorm.DB
	owner, actor   string
	orgs           []OrganizationResult
	p              *isolationPGProvider
	authentication *auth.Service
	cookies        map[string]*http.Cookie
}

func newIsolationPGProjection(t *testing.T) *isolationPGFixture {
	t.Helper()
	s, seed, owner, actor := postgresFixture(t)
	f := &isolationPGFixture{s: s, seed: seed, owner: owner, actor: actor, p: &isolationPGProvider{db: s.DB}, cookies: map[string]*http.Cookie{}}
	sql := `CREATE SCHEMA IF NOT EXISTS agri_projection_test AUTHORIZATION flow_platform_migrate;
 GRANT USAGE ON SCHEMA agri_projection_test TO flow_platform_app;
 CREATE TABLE IF NOT EXISTS agri_projection_test.rows(tenant text,workspace text,actor text,job text,version text,value text,revision bigint,PRIMARY KEY(tenant,workspace,actor,job));
 CREATE TABLE IF NOT EXISTS agri_projection_test.events(tenant text,workspace text,actor text,job text,sequence bigint,data text,PRIMARY KEY(tenant,workspace,actor,job,sequence));`
	if e := seed.Exec(sql).Error; e != nil {
		t.Fatal("projection fixture schema failed")
	}
	for _, table := range []string{"rows", "events"} {
		sql := fmt.Sprintf(`ALTER TABLE agri_projection_test.%s ENABLE ROW LEVEL SECURITY;
 ALTER TABLE agri_projection_test.%s FORCE ROW LEVEL SECURITY;
 DROP POLICY IF EXISTS fixture_owner ON agri_projection_test.%s;
 CREATE POLICY fixture_owner ON agri_projection_test.%s TO flow_platform_migrate USING(true) WITH CHECK(true);
 DROP POLICY IF EXISTS fixture_scope ON agri_projection_test.%s;
 CREATE POLICY fixture_scope ON agri_projection_test.%s TO flow_platform_app USING(tenant=current_setting('app.tenant_ids',true) AND workspace=current_setting('app.workspace_id',true));
 REVOKE ALL ON agri_projection_test.%s FROM PUBLIC,flow_platform_app;
 GRANT SELECT ON agri_projection_test.%s TO flow_platform_app;`, table, table, table, table, table, table, table, table)
		if e := seed.Exec(sql).Error; e != nil {
			t.Fatal("projection fixture restricted policy failed")
		}
	}
	t.Cleanup(func() {
		for _, table := range []string{"events", "rows"} {
			if e := seed.Table("agri_projection_test."+table).Where("actor IN ?", []string{owner, actor}).Delete(map[string]any{}).Error; e != nil {
				t.Error("projection cleanup failed")
			}
		}
	})
	for i := 0; i < 2; i++ {
		org, e := s.CreateOrganization(context.Background(), owner, "PG projection fixture", fmt.Sprintf("projection-org-%d", i))
		if e != nil {
			t.Fatal("projection organization failed")
		}
		inv, e := s.CreateInvite(context.Background(), owner, org.Workspace.ID, actor+"@fixture.invalid", "viewer")
		if e != nil {
			t.Fatal("projection invitation failed")
		}
		if _, e = s.AcceptInvite(context.Background(), actor, inv.Token); e != nil {
			t.Fatal("projection membership failed")
		}
		f.orgs = append(f.orgs, org)
	}
	second := f.orgs[0]
	second.Workspace.ID = flow.NewID("ws_projection_")
	second.Workspace.Slug = "projection_second"
	if e := flow.TenantTx(seed, second.Tenant.ID, func(tx *gorm.DB) error {
		for _, row := range []any{&model.Workspace{ID: second.Workspace.ID, TenantID: second.Tenant.ID, Product: Product, Slug: second.Workspace.Slug, Name: "Projection second workspace"}, &model.WorkspaceMembership{ID: flow.NewID("wm_"), WorkspaceID: second.Workspace.ID, UserID: owner, Role: "admin"}, &model.WorkspaceMembership{ID: flow.NewID("wm_"), WorkspaceID: second.Workspace.ID, UserID: actor, Role: "viewer"}, &model.AgricultureRoleBinding{ID: flow.NewID("agr_"), TenantID: second.Tenant.ID, WorkspaceID: second.Workspace.ID, UserID: actor, Role: "viewer"}} {
			if e := tx.Create(row).Error; e != nil {
				return e
			}
		}
		return nil
	}); e != nil {
		t.Fatal("second workspace fixture failed")
	}
	f.orgs = append(f.orgs, second)
	f.s.Ownership = f.p
	for i, org := range f.orgs {
		// Owner governance deliberately grants no implicit content role.
		roles := []string{"viewer"}
		if e := f.s.ChangeMember(context.Background(), owner, org.Workspace.ID, owner, &roles, nil); e != nil {
			t.Fatal("projection explicit owner business role failed")
		}
		for _, who := range []string{owner, actor} {
			for _, job := range []string{"job_a", "job_b"} {
				value := fmt.Sprintf("scope_%d_%s_%s", i, who, job)
				if e := seed.Table("agri_projection_test.rows").Create(&isolationPGRow{org.Tenant.ID, org.Workspace.ID, who, job, "version_one", value, 1}).Error; e != nil {
					t.Fatal("projection row seed failed")
				}
				data, _ := json.Marshal(map[string]string{"fixture": value})
				if e := seed.Exec("INSERT INTO agri_projection_test.events VALUES(?,?,?,?,?,?)", org.Tenant.ID, org.Workspace.ID, who, job, 1, string(data)).Error; e != nil {
					t.Fatal("projection event seed failed")
				}
				if _, e := f.s.GrantResourceBinding(context.Background(), owner, org.Workspace.ID, who, ResourceRef{"job", job}, []string{"agri.analysis.read"}, nil, revisionPointer(0)); e != nil {
					t.Fatalf("projection explicit grant failed: %T", e)
				}
			}
		}
	}
	f.authentication = auth.New(repository.New(s.DB), nil, nil)
	for _, who := range []string{owner, actor} {
		pw := auth.RandomToken()
		hash, e := auth.HashPassword(pw)
		if e != nil {
			t.Fatal(e)
		}
		if e = seed.Model(&model.User{}).Where("id=?", who).Update("password_hash", hash).Error; e != nil {
			t.Fatal("projection credentials failed")
		}
		login, e := f.authentication.Login(auth.LoginRequest{Username: who, Password: pw})
		if e != nil {
			t.Fatal("projection real login failed")
		}
		f.cookies[who] = &http.Cookie{Name: auth.SessionCookieName, Value: login.Session}
	}
	t.Cleanup(func() {
		if e := seed.Where("user_id IN ?", []string{owner, actor}).Delete(&model.AuthSession{}).Error; e != nil {
			t.Error("projection auth cleanup failed")
		}
	})
	return f
}
func (f *isolationPGFixture) refresh(actor string) RefreshActor {
	return func(context.Context) (string, error) {
		u, e := f.authentication.CurrentUser(f.cookies[actor].Value)
		if e != nil {
			return "", e
		}
		return u.ID, nil
	}
}
func (f *isolationPGFixture) server(t *testing.T, hook func()) *httptest.Server {
	stream, e := NewJobEventStream(isolationPGEventProvider{f.p}, []byte(strings.Repeat("p", 32)))
	if e != nil {
		t.Fatal(e)
	}
	stream.poll = 10 * time.Millisecond
	stream.duration = 300 * time.Millisecond
	r := gin.New()
	if hook != nil {
		r.Use(func(c *gin.Context) { c.Writer = &eventBoundaryWriter{ResponseWriter: c.Writer, hook: hook}; c.Next() })
	}
	API{Service: f.s, JobEvents: stream, User: func(c *gin.Context) (*model.User, error) {
		cookie, _ := c.Cookie(auth.SessionCookieName)
		return f.authentication.CurrentUser(cookie)
	}}.Register(r.Group("/api"))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}
func TestIsolationProjectionPostgres(t *testing.T) {
	t.Run("cache_scope_keys_and_revision", func(t *testing.T) {
		f := newIsolationPGProjection(t)
		cache := NewScopedProjectionCache(f.s)
		p := isolationPGCacheProvider{f.p}
		for repeat := 0; repeat < 2; repeat++ {
			for i, org := range f.orgs {
				for _, actor := range []string{f.owner, f.actor} {
					for _, job := range []string{"job_a", "job_b"} {
						q := ProjectionQuery{Kind: "map", Action: "agri.analysis.read", Resources: []ResourceRef{{"job", job}}, Parameters: json.RawMessage(`{"same":true}`)}
						v, e := cache.Query(context.Background(), f.refresh(actor), org.Workspace.ID, q, p)
						if e != nil || string(v) != fmt.Sprintf("scope_%d_%s_%s", i, actor, job) {
							t.Fatalf("projection scope cache mismatch: %T", e)
						}
					}
				}
			}
		}
		if f.p.loads.Load() != 12 {
			t.Fatal("cache did not isolate all 12 actual scopes")
		}
		org := f.orgs[0]
		if e := f.seed.Exec("UPDATE agri_projection_test.rows SET revision=2,value='revision_two' WHERE tenant=? AND workspace=? AND job='job_a'", org.Tenant.ID, org.Workspace.ID).Error; e != nil {
			t.Fatal(e)
		}
		q := ProjectionQuery{Kind: "map", Action: "agri.analysis.read", Resources: []ResourceRef{{"job", "job_a"}}, Parameters: json.RawMessage(`{"same":true}`)}
		if _, e := cache.Query(context.Background(), f.refresh(f.actor), org.Workspace.ID, q, p); e == nil {
			t.Fatal("old resource grant survived its bound revision")
		}
		bindings, e := f.s.ListBindings(context.Background(), f.owner, org.Workspace.ID)
		if e != nil {
			t.Fatal("revision grant lookup failed")
		}
		updated := false
		for _, binding := range bindings {
			if binding.SubjectID == f.actor && binding.ResourceID == "job_a" {
				if _, e = f.s.GrantResourceBinding(context.Background(), f.owner, org.Workspace.ID, f.actor, ResourceRef{"job", "job_a"}, []string{"agri.analysis.read"}, nil, revisionPointer(binding.AuthVersion)); e != nil {
					t.Fatal("explicit current revision grant failed")
				}
				updated = true
			}
		}
		if !updated {
			t.Fatal("current revision grant was not refreshed")
		}
		v, e := cache.Query(context.Background(), f.refresh(f.actor), org.Workspace.ID, q, p)
		if e != nil || string(v) != "revision_two" || f.p.loads.Load() != 13 {
			t.Fatalf("resource revision reused old bytes: %T", e)
		}
	})
	t.Run("cache_revocation_and_inflight_fill", func(t *testing.T) {
		f := newIsolationPGProjection(t)
		org := f.orgs[0]
		cache := NewScopedProjectionCache(f.s)
		p := isolationPGCacheProvider{f.p}
		q := ProjectionQuery{Kind: "tool", Action: "agri.analysis.read", Resources: []ResourceRef{{"job", "job_a"}}, Parameters: json.RawMessage(`{}`)}
		if _, e := cache.Query(context.Background(), f.refresh(f.actor), org.Workspace.ID, q, p); e != nil {
			t.Fatal(e)
		}
		status := "suspended"
		if e := f.s.ChangeMember(context.Background(), f.owner, org.Workspace.ID, f.actor, nil, &status); e != nil {
			t.Fatal(e)
		}
		before, e := chainPlatformSnapshot(f.seed)
		if e != nil {
			t.Fatal(e)
		}
		if _, e := cache.Query(context.Background(), f.refresh(f.actor), org.Workspace.ID, q, p); e == nil {
			t.Fatal("cached bytes survived membership revocation")
		}
		after, e := chainPlatformSnapshot(f.seed)
		if e != nil || fmt.Sprint(before) != fmt.Sprint(after) {
			t.Fatal("denied cache changed business state")
		}
		status = "active"
		if e := f.s.ChangeMember(context.Background(), f.owner, org.Workspace.ID, f.actor, nil, &status); e != nil {
			t.Fatal(e)
		}
		q.Parameters = json.RawMessage(`{"fill":"new"}`)
		var hookErr error
		f.p.afterLoad = func() {
			status := "suspended"
			hookErr = f.s.ChangeMember(context.Background(), f.owner, org.Workspace.ID, f.actor, nil, &status)
		}
		if _, e := cache.Query(context.Background(), f.refresh(f.actor), org.Workspace.ID, q, p); e == nil || hookErr != nil {
			t.Fatal("inflight cache fill escaped persisted revocation")
		}
	})
	t.Run("sse_cursor_and_revocation", func(t *testing.T) {
		f := newIsolationPGProjection(t)
		srv := f.server(t, nil)
		path := func(w string) string { return "/api/agri/v1/workspaces/" + w + "/jobs/job_a/events" }
		res := eventRequest(t, srv, f.cookies[f.actor], path(f.orgs[0].Workspace.ID), "")
		cursor, data := firstEvent(t, res)
		res.Body.Close()
		if !strings.Contains(data, f.actor) {
			t.Fatal("wrong actor event")
		}
		for _, pair := range []struct{ actor, workspace string }{{f.owner, f.orgs[0].Workspace.ID}, {f.actor, f.orgs[1].Workspace.ID}, {f.actor, f.orgs[2].Workspace.ID}} {
			res = eventRequest(t, srv, f.cookies[pair.actor], path(pair.workspace), cursor)
			raw, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != 409 || strings.Contains(string(raw), "scope_") {
				t.Fatal("cross-scope SSE cursor accepted or leaked")
			}
		}
		status := "suspended"
		if e := f.s.ChangeMember(context.Background(), f.owner, f.orgs[0].Workspace.ID, f.actor, nil, &status); e != nil {
			t.Fatal(e)
		}
		res = eventRequest(t, srv, f.cookies[f.actor], path(f.orgs[0].Workspace.ID), cursor)
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 404 || strings.Contains(string(raw), "scope_") {
			t.Fatal("revoked membership replay leaked")
		}
		if e := f.authentication.Logout(f.cookies[f.actor].Value); e != nil {
			t.Fatal(e)
		}
		res = eventRequest(t, srv, f.cookies[f.actor], path(f.orgs[0].Workspace.ID), cursor)
		raw, _ = io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 401 || strings.Contains(string(raw), "scope_") {
			t.Fatal("logged out cursor replay leaked")
		}
	})
	t.Run("sse_every_frame_logout", func(t *testing.T) {
		f := newIsolationPGProjection(t)
		org := f.orgs[0]
		if e := f.seed.Exec("INSERT INTO agri_projection_test.events VALUES(?,?,?,?,?,?)", org.Tenant.ID, org.Workspace.ID, f.actor, "job_a", 2, `{"fixture":"SECOND_MUST_NOT_ESCAPE"}`).Error; e != nil {
			t.Fatal(e)
		}
		loggedOut := make(chan error, 1)
		srv := f.server(t, func() { loggedOut <- f.authentication.Logout(f.cookies[f.actor].Value) })
		res := eventRequest(t, srv, f.cookies[f.actor], "/api/agri/v1/workspaces/"+org.Workspace.ID+"/jobs/job_a/events", "")
		raw, e := io.ReadAll(res.Body)
		res.Body.Close()
		var logoutErr error
		select {
		case logoutErr = <-loggedOut:
		default:
			t.Fatal("first sensitive frame did not trigger the logout boundary")
		}
		if e != nil || logoutErr != nil || res.StatusCode != 200 || !strings.Contains(string(raw), `"sequence":1`) || strings.Contains(string(raw), "SECOND_MUST_NOT_ESCAPE") {
			t.Fatal("per-frame PostgreSQL logout boundary failed")
		}
	})
	t.Run("pool_concurrent_scope_and_empty_denial", func(t *testing.T) {
		f := newIsolationPGProjection(t)
		var wg sync.WaitGroup
		errs := make(chan error, 30)
		for i := 0; i < 30; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				org := f.orgs[i%3]
				scope := ProjectionScope{TenantID: org.Tenant.ID, WorkspaceID: org.Workspace.ID, ActorID: f.actor}
				r, e := f.p.row(context.Background(), scope, "job_a")
				if e != nil {
					errs <- e
				} else if r.Value != fmt.Sprintf("scope_%d_%s_job_a", i%3, f.actor) {
					errs <- errors.New("pooled projection scope leak")
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
		for i := 0; i < 10; i++ {
			var count int64
			if e := f.s.DB.Table("agri_projection_test.rows").Count(&count).Error; e != nil {
				t.Fatal("empty-scope probe unavailable")
			}
			if count != 0 {
				t.Fatal("pooled tenant scope escaped transaction")
			}
		}
		if e := isolationPGTx(context.Background(), f.s.DB, f.orgs[0].Tenant.ID, f.orgs[1].Workspace.ID, func(tx *gorm.DB) error {
			var n int64
			if e := tx.Table("agri_projection_test.rows").Count(&n).Error; e != nil {
				return e
			}
			if n != 0 {
				return errors.New("mixed tenant workspace matched rows")
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		if e := f.s.DB.Exec("UPDATE agri_projection_test.rows SET value='forbidden'").Error; e == nil {
			t.Fatal("runtime fixture provider has write privilege")
		}
	})
}
