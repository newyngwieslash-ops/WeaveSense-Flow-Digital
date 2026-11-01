package agriculture

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"yingce/backend/internal/auth"
	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
)

// Only tests use these durable SQLite event/domain projections. They are not a
// production W06 queue, and do not construct an authenticated user from claims.
type eventFixtureProjection struct {
	ID                                                    uint
	Revision                                              int64
	Tenant, Workspace, Actor, Job, Projection, Generation string
}
type eventFixtureRow struct {
	ID                            uint
	Tenant, Workspace, Actor, Job string
	Sequence                      int64
	Data                          string
}
type eventFixtureProvider struct {
	db     *gorm.DB
	before func()
	reads  atomic.Int32
}

func (p *eventFixtureProvider) Describe(ctx context.Context, s ProjectionScope, job string) (JobStreamBinding, error) {
	var row eventFixtureProjection
	e := p.db.WithContext(ctx).Where("tenant=? AND workspace=? AND actor=? AND job=?", s.TenantID, s.WorkspaceID, s.ActorID, job).First(&row).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return JobStreamBinding{}, ErrNotFound
	}
	return JobStreamBinding{s, row.Job, row.Projection, row.Generation}, e
}
func (p *eventFixtureProvider) Read(ctx context.Context, b JobStreamBinding, after int64, limit int) ([]JobEvent, error) {
	p.reads.Add(1)
	var rows []eventFixtureRow
	e := p.db.WithContext(ctx).Where("tenant=? AND workspace=? AND actor=? AND job=? AND sequence>?", b.Scope.TenantID, b.Scope.WorkspaceID, b.Scope.ActorID, b.JobID, after).Order("sequence ASC").Limit(limit).Find(&rows).Error
	out := []JobEvent{}
	for _, r := range rows {
		out = append(out, JobEvent{r.Sequence, json.RawMessage(r.Data)})
	}
	if p.before != nil {
		f := p.before
		p.before = nil
		f()
	}
	return out, e
}

type eventOwnership struct{ db *gorm.DB }

func (p eventOwnership) Resolve(ctx context.Context, s OwnershipContext, refs []ResourceRef) ([]ResourceOwnership, error) {
	out := []ResourceOwnership{}
	for _, r := range refs {
		if r.Kind != "job" {
			return nil, ErrNotFound
		}
		var row eventFixtureProjection
		if e := p.db.WithContext(ctx).Where("tenant=? AND workspace=? AND job=?", s.TenantID, s.WorkspaceID, r.ID).First(&row).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return nil, ErrNotFound
			}
			return nil, e
		}
		if row.Revision < 1 {
			return nil, ErrNotFound
		}
		now := time.Now().UTC()
		out = append(out, ResourceOwnership{Resource: r, TenantID: s.TenantID, WorkspaceID: s.WorkspaceID, FarmIDs: []string{"farm_a"}, Revision: row.Revision, Status: "active", ReceiptID: "fixture_receipt", IssuedAt: now, ExpiresAt: now.Add(time.Minute)})
	}
	return out, nil
}
func eventSetup(t *testing.T) (Service, OrganizationResult, *eventFixtureProvider) {
	t.Helper()
	s, o := fixture(t)
	if e := s.DB.AutoMigrate(&eventFixtureProjection{}, &eventFixtureRow{}, &model.AuthSession{}, &model.UserIdentity{}); e != nil {
		t.Fatal(e)
	}
	p := &eventFixtureProvider{db: s.DB}
	s.Ownership = eventOwnership{s.DB}
	for _, actor := range []string{"bob", "carol"} {
		join(t, s, o, actor, "viewer")
		seedEventScope(t, s, o, actor, "job_a", actor)
		if _, e := s.GrantResourceBinding(context.Background(), "owner", o.Workspace.ID, actor, ResourceRef{"job", "job_a"}, []string{"agri.analysis.read"}, nil, revisionPointer(0)); e != nil {
			t.Fatal(e)
		}
	}
	return s, o, p
}
func seedEventScope(t *testing.T, s Service, o OrganizationResult, actor, job, value string) {
	t.Helper()
	if e := s.DB.Create(&eventFixtureProjection{Tenant: o.Tenant.ID, Workspace: o.Workspace.ID, Actor: actor, Job: job, Projection: "job_minimal", Generation: "generation_one", Revision: 1}).Error; e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(map[string]string{"fixture": value})
	if e := s.DB.Create(&eventFixtureRow{Tenant: o.Tenant.ID, Workspace: o.Workspace.ID, Actor: actor, Job: job, Sequence: 1, Data: string(raw)}).Error; e != nil {
		t.Fatal(e)
	}
}
func eventServer(t *testing.T, s Service, p JobEventProvider, middleware ...gin.HandlerFunc) (*httptest.Server, *JobEventStream, *auth.Service, map[string]*http.Cookie) {
	t.Helper()
	authentication := auth.New(repository.New(s.DB), nil, nil)
	cookies := map[string]*http.Cookie{}
	for _, actor := range []string{"bob", "carol"} {
		pw := auth.RandomToken()
		hash, e := auth.HashPassword(pw)
		if e != nil {
			t.Fatal(e)
		}
		if e = s.DB.Model(&model.User{}).Where("id=?", actor).Update("password_hash", hash).Error; e != nil {
			t.Fatal(e)
		}
		v, e := authentication.Login(auth.LoginRequest{Username: actor, Password: pw})
		if e != nil {
			t.Fatal(e)
		}
		cookies[actor] = &http.Cookie{Name: auth.SessionCookieName, Value: v.Session}
	}
	stream, e := NewJobEventStream(p, []byte(strings.Repeat("k", 32)))
	if p != nil && e != nil {
		t.Fatal(e)
	}
	if stream != nil {
		stream.poll = 10 * time.Millisecond
		stream.duration = 500 * time.Millisecond
	}
	r := gin.New()
	r.Use(middleware...)
	a := API{Service: s, User: func(c *gin.Context) (*model.User, error) {
		cookie, _ := c.Cookie(auth.SessionCookieName)
		return authentication.CurrentUser(cookie)
	}}
	a.JobEvents = stream
	a.Register(r.Group("/api"))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, stream, authentication, cookies
}
func eventRequest(t *testing.T, srv *httptest.Server, cookie *http.Cookie, path, cursor string) *http.Response {
	t.Helper()
	req, e := http.NewRequest("GET", srv.URL+path, nil)
	if e != nil {
		t.Fatal(e)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if cursor != "" {
		req.Header.Set("Last-Event-ID", cursor)
	}
	res, e := srv.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	return res
}
func firstEvent(t *testing.T, res *http.Response) (string, string) {
	t.Helper()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("event status %d %s", res.StatusCode, b)
	}
	scan := bufio.NewScanner(res.Body)
	id := ""
	for scan.Scan() {
		line := scan.Text()
		if strings.HasPrefix(line, "id: ") {
			id = strings.TrimPrefix(line, "id: ")
		}
		if strings.HasPrefix(line, "data: ") {
			return id, strings.TrimPrefix(line, "data: ")
		}
	}
	t.Fatal("missing sensitive event")
	return "", ""
}
func TestJobEventsRealSessionScopeReplayAndRevocation(t *testing.T) {
	s, o, p := eventSetup(t)
	srv, stream, authentication, cookies := eventServer(t, s, p)
	path := "/api/agri/v1/workspaces/" + o.Workspace.ID + "/jobs/job_a/events"
	res := eventRequest(t, srv, cookies["bob"], path, "")
	cursor, data := firstEvent(t, res)
	res.Body.Close()
	if !strings.Contains(data, "bob") || strings.Contains(data, "carol") {
		t.Fatal("actor projection leak")
	}
	res = eventRequest(t, srv, cookies["carol"], path, cursor)
	if res.StatusCode != 409 {
		t.Fatal("other actor cursor accepted")
	}
	res.Body.Close()
	res = eventRequest(t, srv, cookies["carol"], path, "")
	_, data = firstEvent(t, res)
	res.Body.Close()
	if !strings.Contains(data, "carol") {
		t.Fatal("wrong actor projection")
	}
	// Append does not invalidate stream-generation cursor. Reconnect sees ONLY seq2.
	if e := s.DB.Create(&eventFixtureRow{Tenant: o.Tenant.ID, Workspace: o.Workspace.ID, Actor: "bob", Job: "job_a", Sequence: 2, Data: `{"fixture":"append"}`}).Error; e != nil {
		t.Fatal(e)
	}
	res = eventRequest(t, srv, cookies["bob"], path, cursor)
	_, data = firstEvent(t, res)
	res.Body.Close()
	if !strings.Contains(data, "append") || !strings.Contains(data, `"sequence":2`) {
		t.Fatal("resume duplicated old event")
	}
	prior := stream.now
	stream.now = func() time.Time { return prior().Add(6 * time.Minute) }
	res = eventRequest(t, srv, cookies["bob"], path, cursor)
	if res.StatusCode != 409 {
		t.Fatal("expired cursor accepted")
	}
	res.Body.Close()
	stream.now = prior
	// Logout happens after provider reads but before fresh session/output: no bytes.
	p.before = func() {
		if e := authentication.Logout(cookies["bob"].Value); e != nil {
			t.Fatal(e)
		}
	}
	res = eventRequest(t, srv, cookies["bob"], path, "")
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 401 || strings.Contains(string(raw), "fixture") {
		t.Fatal("logout race leaked buffered batch")
	}
	status := "suspended"
	p.before = func() {
		if e := s.ChangeMember(context.Background(), "owner", o.Workspace.ID, "carol", nil, &status); e != nil {
			t.Fatal(e)
		}
	}
	res = eventRequest(t, srv, cookies["carol"], path, "")
	raw, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 404 || strings.Contains(string(raw), "fixture") {
		t.Fatal("membership revocation leaked batch")
	}
}
func TestJobEventsCrossTenantAndDefaultUnavailable(t *testing.T) {
	s, o, p := eventSetup(t)
	other, e := s.CreateOrganization(context.Background(), "owner", "Other fixture", "event-other-0001")
	if e != nil {
		t.Fatal(e)
	}
	join(t, s, other, "bob", "viewer")
	seedEventScope(t, s, other, "bob", "job_a", "other tenant")
	if _, e = s.GrantResourceBinding(context.Background(), "owner", other.Workspace.ID, "bob", ResourceRef{"job", "job_a"}, []string{"agri.analysis.read"}, nil, revisionPointer(0)); e != nil {
		t.Fatal(e)
	}
	srv, _, _, cookies := eventServer(t, s, p)
	path := func(workspace string) string { return "/api/agri/v1/workspaces/" + workspace + "/jobs/job_a/events" }
	res := eventRequest(t, srv, cookies["bob"], path(o.Workspace.ID), "")
	cursor, _ := firstEvent(t, res)
	res.Body.Close()
	res = eventRequest(t, srv, cookies["bob"], path(other.Workspace.ID), cursor)
	if res.StatusCode != 409 {
		t.Fatal("cross tenant cursor accepted")
	}
	res.Body.Close()
	res = eventRequest(t, srv, cookies["bob"], path(other.Workspace.ID), "")
	_, data := firstEvent(t, res)
	res.Body.Close()
	if !strings.Contains(data, "other tenant") {
		t.Fatal("cross tenant wrong bytes")
	}
	for _, candidate := range []string{path("foreign_workspace"), strings.Replace(path(o.Workspace.ID), "job_a", "job_b", 1)} {
		res = eventRequest(t, srv, cookies["bob"], candidate, "")
		if res.StatusCode != 404 {
			t.Fatal("foreign job/workspace allowed")
		}
		res.Body.Close()
	}
	res = eventRequest(t, srv, nil, path(o.Workspace.ID), "")
	if res.StatusCode != 401 {
		t.Fatal("anonymous SSE")
	}
	res.Body.Close()
	off, _, _, cc := eventServer(t, s, nil)
	res = eventRequest(t, off, cc["bob"], path(o.Workspace.ID), "")
	if res.StatusCode != 503 {
		t.Fatal("missing provider did not fail closed")
	}
	res.Body.Close()
}

func TestJobEventsEstablishedStreamStopsOnLogoutOrRevocation(t *testing.T) {
	for _, mode := range []string{"logout", "membership", "generation", "resource_version", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			s, o, p := eventSetup(t)
			srv, stream, authentication, cookies := eventServer(t, s, p)
			path := "/api/agri/v1/workspaces/" + o.Workspace.ID + "/jobs/job_a/events"
			res := eventRequest(t, srv, cookies["bob"], path, "")
			_, _ = firstEvent(t, res)
			switch mode {
			case "logout":
				if e := authentication.Logout(cookies["bob"].Value); e != nil {
					t.Fatal(e)
				}
			case "membership":
				status := "suspended"
				if e := s.ChangeMember(context.Background(), "owner", o.Workspace.ID, "bob", nil, &status); e != nil {
					t.Fatal(e)
				}
			case "generation":
				if e := s.DB.Model(&eventFixtureProjection{}).Where("actor=?", "bob").Update("generation", "generation_two").Error; e != nil {
					t.Fatal(e)
				}
			case "resource_version":
				if e := s.DB.Model(&eventFixtureProjection{}).Where("job=?", "job_a").Update("revision", 2).Error; e != nil {
					t.Fatal(e)
				}
			case "disconnect":
				res.Body.Close()
			}
			if e := s.DB.Create(&eventFixtureRow{Tenant: o.Tenant.ID, Workspace: o.Workspace.ID, Actor: "bob", Job: "job_a", Sequence: 2, Data: `{"fixture":"MUST_NOT_LEAK"}`}).Error; e != nil {
				t.Fatal(e)
			}
			if mode != "disconnect" {
				raw, e := io.ReadAll(res.Body)
				res.Body.Close()
				if e != nil {
					t.Fatal(e)
				}
				if strings.Contains(string(raw), "MUST_NOT_LEAK") {
					t.Fatal("old subscription continued sensitive events")
				}
			}
			deadline := time.Now().Add(time.Second)
			for len(stream.slots) > 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if len(stream.slots) != 0 {
				t.Fatal("disconnect/revocation retained subscription slot")
			}
		})
	}
}
func TestJobEventCursorIntegrityAndRestart(t *testing.T) {
	p := &eventFixtureProvider{}
	key := []byte(strings.Repeat("q", 32))
	one, _ := NewJobEventStream(p, key)
	two, _ := NewJobEventStream(p, key)
	token, e := one.cursor(strings.Repeat("a", 64), 4)
	if e != nil {
		t.Fatal(e)
	}
	if seq, e := two.resume(token, strings.Repeat("a", 64)); e != nil || seq != 4 {
		t.Fatal("shared-key restart lost valid cursor")
	}
	for _, bad := range []string{"4", token + "x", strings.Repeat("a", 513), "a.b.c"} {
		if _, e := two.resume(bad, strings.Repeat("a", 64)); e == nil {
			t.Fatal("malformed cursor accepted")
		}
	}
	if _, e := two.resume(token, strings.Repeat("b", 64)); !errors.Is(e, ErrConflict) {
		t.Fatal("different permissions accepted")
	}
	rotated, _ := NewJobEventStream(p, []byte(strings.Repeat("r", 32)))
	if _, e := rotated.resume(token, strings.Repeat("a", 64)); !errors.Is(e, ErrForbidden) {
		t.Fatal("rotated key accepted old cursor")
	}
	if _, e := NewJobEventStream(p, nil); e == nil {
		t.Fatal("missing shared cursor key accepted")
	}
}

type cancellingEventProvider struct {
	*eventFixtureProvider
	cancelled chan struct{}
}

func (p *cancellingEventProvider) Read(ctx context.Context, b JobStreamBinding, after int64, limit int) ([]JobEvent, error) {
	<-ctx.Done()
	close(p.cancelled)
	return nil, ctx.Err()
}
func TestJobEventsProviderCancellationAndMalformedBatch(t *testing.T) {
	t.Run("provider_observes_request_cancellation", func(t *testing.T) {
		s, o, p := eventSetup(t)
		blocked := &cancellingEventProvider{p, make(chan struct{})}
		srv, _, _, cookies := eventServer(t, s, blocked)
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/agri/v1/workspaces/"+o.Workspace.ID+"/jobs/job_a/events", nil)
		req.AddCookie(cookies["bob"])
		done := make(chan struct{})
		go func() {
			r, _ := srv.Client().Do(req)
			if r != nil {
				r.Body.Close()
			}
			close(done)
		}()
		time.Sleep(30 * time.Millisecond)
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("HTTP cancellation did not terminate")
		}
		select {
		case <-blocked.cancelled:
		case <-time.After(time.Second):
			t.Fatal("provider failed to see cancellation")
		}
	})
	t.Run("whole_batch_rejected_before_first_byte", func(t *testing.T) {
		s, o, p := eventSetup(t)
		if e := s.DB.Create(&eventFixtureRow{Tenant: o.Tenant.ID, Workspace: o.Workspace.ID, Actor: "bob", Job: "job_a", Sequence: 1, Data: `{"secret":"duplicate sequence"}`}).Error; e != nil {
			t.Fatal(e)
		}
		srv, _, _, cookies := eventServer(t, s, p)
		r := eventRequest(t, srv, cookies["bob"], "/api/agri/v1/workspaces/"+o.Workspace.ID+"/jobs/job_a/events", "")
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 503 || strings.Contains(string(b), "duplicate sequence") || strings.Contains(string(b), "fixture") {
			t.Fatal("malformed batch exposed events")
		}
	})
}

// This wraps the REAL net/http response writer. The first sensitive write is
// completed before the durable session/membership is revoked synchronously.
type eventBoundaryWriter struct {
	gin.ResponseWriter
	hook  func()
	fired bool
}

func (w *eventBoundaryWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *eventBoundaryWriter) Write(b []byte) (int, error) {
	n, e := w.ResponseWriter.Write(b)
	if !w.fired && strings.Contains(string(b), "event: job\n") {
		w.fired = true
		w.hook()
	}
	return n, e
}
func TestJobEventsEachWriteRechecksCurrentAuthority(t *testing.T) {
	for _, mode := range []string{"logout", "membership"} {
		t.Run(mode, func(t *testing.T) {
			s, o, p := eventSetup(t)
			if e := s.DB.Create(&eventFixtureRow{Tenant: o.Tenant.ID, Workspace: o.Workspace.ID, Actor: "bob", Job: "job_a", Sequence: 2, Data: `{"fixture":"SECOND_MUST_NOT_ESCAPE"}`}).Error; e != nil {
				t.Fatal(e)
			}
			var revoke func()
			middleware := func(c *gin.Context) {
				c.Writer = &eventBoundaryWriter{ResponseWriter: c.Writer, hook: func() { revoke() }}
				c.Next()
			}
			srv, _, authentication, cookies := eventServer(t, s, p, middleware)
			revoke = func() {
				if mode == "logout" {
					if e := authentication.Logout(cookies["bob"].Value); e != nil {
						t.Error(e)
					}
				} else {
					status := "suspended"
					if e := s.ChangeMember(context.Background(), "owner", o.Workspace.ID, "bob", nil, &status); e != nil {
						t.Error(e)
					}
				}
			}
			res := eventRequest(t, srv, cookies["bob"], "/api/agri/v1/workspaces/"+o.Workspace.ID+"/jobs/job_a/events", "")
			b, e := io.ReadAll(res.Body)
			res.Body.Close()
			if e != nil {
				t.Fatal(e)
			}
			if res.StatusCode != 200 || !strings.Contains(string(b), `"sequence":1`) {
				t.Fatal("first actual event was not written")
			}
			if strings.Contains(string(b), "SECOND_MUST_NOT_ESCAPE") {
				t.Fatal("second event escaped after first-write revocation")
			}
		})
	}
}
