package agriculture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

type projectionFixtureProvider struct {
	binding ProjectionBinding
	value   []byte
	loads   atomic.Int32
	hook    func()
	bad     bool
}

func (p *projectionFixtureProvider) Describe(ctx context.Context, scope ProjectionScope, q ProjectionQuery) (ProjectionBinding, error) {
	b := p.binding
	if p.bad {
		b.Scope.TenantID = "foreign"
	}
	return b, nil
}
func (p *projectionFixtureProvider) Load(ctx context.Context, b ProjectionBinding, q ProjectionQuery) ([]byte, error) {
	p.loads.Add(1)
	if p.hook != nil {
		hook := p.hook
		p.hook = nil
		hook()
	}
	return p.value, nil
}
func TestScopedProjectionCacheActualAuthority(t *testing.T) {
	s, org, _ := authorizationFixture(t)
	join(t, s, org, "bob", "viewer")
	join(t, s, org, "carol", "viewer")
	for _, actor := range []string{"bob", "carol"} {
		if _, e := s.GrantResourceBinding(context.Background(), "owner", org.Workspace.ID, actor, ResourceRef{"farm", "farm_a"}, []string{"agri.parcel.read"}, nil, revisionPointer(0)); e != nil {
			t.Fatal(e)
		}
	}
	q := ProjectionQuery{Kind: "map", Action: "agri.parcel.read", Resources: []ResourceRef{{"parcel", "parcel_a"}}, Parameters: json.RawMessage(`{"bbox":[1,2,3,4]}`)}
	cache := NewScopedProjectionCache(s)
	fresh := func(actor string) RefreshActor { return func(context.Context) (string, error) { return actor, nil } }
	provider := func(actor string) *projectionFixtureProvider {
		d, e := s.Authorize(context.Background(), actor, org.Workspace.ID, q.Action, q.Resources)
		if e != nil {
			t.Fatal(e)
		}
		return &projectionFixtureProvider{binding: ProjectionBinding{projectionScope(d), "map_minimal", "result_v1"}, value: []byte(actor + " private result")}
	}
	bob, carol := provider("bob"), provider("carol")
	for _, pair := range []struct {
		actor string
		p     *projectionFixtureProvider
	}{{"bob", bob}, {"carol", carol}, {"bob", bob}, {"carol", carol}} {
		got, e := cache.Query(context.Background(), fresh(pair.actor), org.Workspace.ID, q, pair.p)
		if e != nil || string(got) != pair.actor+" private result" {
			t.Fatalf("scope result %v", e)
		}
	}
	if bob.loads.Load() != 1 || carol.loads.Load() != 1 {
		t.Fatal("expected isolated real cache hits")
	}
	bob.bad = true
	if _, e := cache.Query(context.Background(), fresh("bob"), org.Workspace.ID, q, bob); !errors.Is(e, ErrDependencyUnavailable) {
		t.Fatal("provider foreign scope accepted")
	}
	bob.bad = false
	status := "suspended"
	if e := s.ChangeMember(context.Background(), "owner", org.Workspace.ID, "bob", nil, &status); e != nil {
		t.Fatal(e)
	}
	if _, e := cache.Query(context.Background(), fresh("bob"), org.Workspace.ID, q, bob); e == nil {
		t.Fatal("cached value survived revocation")
	}
	if bob.loads.Load() != 1 {
		t.Fatal("revoked actor triggered loader")
	}
	// A delayed provider cannot publish a fill after the actual persisted actor loses access.
	carol.binding.Version = "result_v2"
	carol.hook = func() {
		if e := s.ChangeMember(context.Background(), "owner", org.Workspace.ID, "carol", nil, &status); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := cache.Query(context.Background(), fresh("carol"), org.Workspace.ID, q, carol); e == nil {
		t.Fatal("revoked in-flight fill returned bytes")
	}
	if _, e := cache.Query(context.Background(), fresh("owner"), org.Workspace.ID, q, nil); !errors.Is(e, ErrDependencyUnavailable) {
		t.Fatal("missing provider did not close")
	}
}
func TestScopedProjectionCacheCrossTenantAndVersionKeys(t *testing.T) {
	// Actual result/projection/parameter dimensions invalidate independently.
	// The separate two-tenant test below exercises real scoped cache hits.
	s, org, _ := authorizationFixture(t)
	join(t, s, org, "bob", "viewer")
	if _, e := s.GrantResourceBinding(context.Background(), "owner", org.Workspace.ID, "bob", ResourceRef{"farm", "farm_a"}, []string{"agri.parcel.read"}, nil, revisionPointer(0)); e != nil {
		t.Fatal(e)
	}
	cache := NewScopedProjectionCache(s)
	q := ProjectionQuery{Kind: "search", Action: "agri.parcel.read", Resources: []ResourceRef{{"parcel", "parcel_a"}}, Parameters: json.RawMessage(`{}`)}
	refresh := func(context.Context) (string, error) { return "bob", nil }
	d, e := s.Authorize(context.Background(), "bob", org.Workspace.ID, q.Action, q.Resources)
	if e != nil {
		t.Fatal(e)
	}
	p := &projectionFixtureProvider{binding: ProjectionBinding{projectionScope(d), "search_minimal", "v1"}, value: []byte("tenant A")}
	if _, e = cache.Query(context.Background(), refresh, org.Workspace.ID, q, p); e != nil {
		t.Fatal(e)
	}
	for _, change := range []string{"projection", "version", "kind", "parameters"} {
		switch change {
		case "projection":
			p.binding.Projection = "search_restricted"
		case "version":
			p.binding.Version = "v2"
		case "kind":
			q.Kind = "tool"
		case "parameters":
			q.Parameters = json.RawMessage(`{"query":"new"}`)
		}
		p.value = []byte(change)
		v, e := cache.Query(context.Background(), refresh, org.Workspace.ID, q, p)
		if e != nil || string(v) != change {
			t.Fatalf("dimension %s: %v", change, e)
		}
	}
	// Explicit tenant and workspace changes in the complete scope digest cannot collide.
	a, e := projectionDigest(d, p.binding, q.Parameters)
	if e != nil {
		t.Fatal(e)
	}
	other := d
	other.TenantID = "tenant_other"
	other.WorkspaceID = "workspace_other"
	b := p.binding
	b.Scope = projectionScope(other)
	z, e := projectionDigest(other, b, q.Parameters)
	if e != nil || a == z {
		t.Fatal("cross tenant digest collision")
	}
	if p.loads.Load() != 5 {
		t.Fatal(fmt.Sprint("missing version cache isolation ", p.loads.Load()))
	}
	// Unknown/duplicate parameters rejected before provider/load.
	q.Parameters = json.RawMessage(`{"x":1,"x":2}`)
	if _, e = cache.Query(context.Background(), refresh, org.Workspace.ID, q, p); !errors.Is(e, ErrInvalid) {
		t.Fatal("duplicate params accepted")
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, e = cache.Query(ctx, refresh, org.Workspace.ID, q, p); e == nil {
		t.Fatal("expired operation accepted")
	}
}

func TestScopedProjectionCacheActualTwoTenantSameInputs(t *testing.T) {
	s, o, _ := eventSetup(t)
	other, e := s.CreateOrganization(context.Background(), "owner", "Other cache fixture", "cache-other-0001")
	if e != nil {
		t.Fatal(e)
	}
	join(t, s, other, "bob", "viewer")
	seedEventScope(t, s, other, "bob", "job_a", "other")
	if _, e = s.GrantResourceBinding(context.Background(), "owner", other.Workspace.ID, "bob", ResourceRef{"job", "job_a"}, []string{"agri.analysis.read"}, nil, revisionPointer(0)); e != nil {
		t.Fatal(e)
	}
	cache := NewScopedProjectionCache(s)
	q := ProjectionQuery{Kind: "tool", Action: "agri.analysis.read", Resources: []ResourceRef{{"job", "job_a"}}, Parameters: json.RawMessage(`{"query":"same"}`)}
	refresh := func(context.Context) (string, error) { return "bob", nil }
	providers := map[string]*projectionFixtureProvider{}
	for _, org := range []OrganizationResult{o, other} {
		d, e := s.Authorize(context.Background(), "bob", org.Workspace.ID, q.Action, q.Resources)
		if e != nil {
			t.Fatal(e)
		}
		providers[org.Workspace.ID] = &projectionFixtureProvider{binding: ProjectionBinding{projectionScope(d), "tool_minimal", "v1"}, value: []byte(org.Tenant.ID)}
	}
	for i := 0; i < 3; i++ {
		for _, org := range []OrganizationResult{o, other} {
			value, e := cache.Query(context.Background(), refresh, org.Workspace.ID, q, providers[org.Workspace.ID])
			if e != nil || string(value) != org.Tenant.ID {
				t.Fatalf("tenant private cache collision %v", e)
			}
		}
	}
	for _, p := range providers {
		if p.loads.Load() != 1 {
			t.Fatal("independent cache hits missing")
		}
	}
}

func TestScopedProjectionRejectsWriteAndPostDescriptionRevocation(t *testing.T) {
	s, o, _ := authorizationFixture(t)
	join(t, s, o, "bob", "viewer")
	if _, e := s.GrantResourceBinding(context.Background(), "owner", o.Workspace.ID, "bob", ResourceRef{"farm", "farm_a"}, []string{"agri.parcel.read"}, nil, revisionPointer(0)); e != nil {
		t.Fatal(e)
	}
	cache := NewScopedProjectionCache(s)
	q := ProjectionQuery{Kind: "map", Action: "agri.parcel.update", Resources: []ResourceRef{{"parcel", "parcel_a"}}, Parameters: json.RawMessage(`{}`)}
	refresh := func(context.Context) (string, error) { return "bob", nil }
	p := &projectionFixtureProvider{}
	if _, e := cache.Query(context.Background(), refresh, o.Workspace.ID, q, p); !errors.Is(e, ErrInvalid) {
		t.Fatal("write action accepted for read cache")
	}
	q.Action = "agri.parcel.read"
	d, e := s.Authorize(context.Background(), "bob", o.Workspace.ID, q.Action, q.Resources)
	if e != nil {
		t.Fatal(e)
	}
	p.binding = ProjectionBinding{projectionScope(d), "map_minimal", "v1"}
	p.value = []byte("private")
	wrapper := &descriptionRevocation{projectionFixtureProvider: p, run: func() {
		status := "suspended"
		if e := s.ChangeMember(context.Background(), "owner", o.Workspace.ID, "bob", nil, &status); e != nil {
			t.Fatal(e)
		}
	}}
	if _, e := cache.Query(context.Background(), refresh, o.Workspace.ID, q, wrapper); e == nil {
		t.Fatal("post-description revoked cache result escaped")
	}
}

type descriptionRevocation struct {
	*projectionFixtureProvider
	calls int
	run   func()
}

func (p *descriptionRevocation) Describe(ctx context.Context, s ProjectionScope, q ProjectionQuery) (ProjectionBinding, error) {
	p.calls++
	b, e := p.projectionFixtureProvider.Describe(ctx, s, q)
	if p.calls == 2 {
		p.run()
	}
	return b, e
}

func TestScopedProjectionWaitingFillCannotReviveRevokedActor(t *testing.T) {
	s, o, _ := authorizationFixture(t)
	join(t, s, o, "bob", "viewer")
	if _, e := s.GrantResourceBinding(context.Background(), "owner", o.Workspace.ID, "bob", ResourceRef{"farm", "farm_a"}, []string{"agri.parcel.read"}, nil, revisionPointer(0)); e != nil {
		t.Fatal(e)
	}
	q := ProjectionQuery{Kind: "map", Action: "agri.parcel.read", Resources: []ResourceRef{{"parcel", "parcel_a"}}, Parameters: json.RawMessage(`{}`)}
	d, e := s.Authorize(context.Background(), "bob", o.Workspace.ID, q.Action, q.Resources)
	if e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	p := &projectionFixtureProvider{binding: ProjectionBinding{projectionScope(d), "map_minimal", "v1"}, value: []byte("private"), hook: func() { close(entered); <-release }}
	cache := NewScopedProjectionCache(s)
	var refreshed atomic.Int32
	refresh := func(context.Context) (string, error) { refreshed.Add(1); return "bob", nil }
	done := make(chan error, 2)
	query := func() { _, e := cache.Query(context.Background(), refresh, o.Workspace.ID, q, p); done <- e }
	go query()
	<-entered
	go query()
	deadline := time.Now().Add(time.Second)
	for refreshed.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if refreshed.Load() < 2 {
		close(release)
		t.Fatal("second request never authenticated")
	}
	status := "suspended"
	if e := s.ChangeMember(context.Background(), "owner", o.Workspace.ID, "bob", nil, &status); e != nil {
		close(release)
		t.Fatal(e)
	}
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case e := <-done:
			if e == nil {
				t.Fatal("shared waiting fill returned revoked bytes")
			}
		case <-time.After(time.Second):
			t.Fatal("cache waiter did not terminate")
		}
	}
	if p.loads.Load() != 1 {
		t.Fatal("expected one bounded shared fill")
	}
}
