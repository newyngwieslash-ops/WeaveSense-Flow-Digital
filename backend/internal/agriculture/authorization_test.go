package agriculture

import (
	"context"
	"errors"
	"testing"
	"time"

	"yingce/backend/internal/model"
)

type fixtureOwnershipResolver struct {
	rows   map[ResourceRef]ResourceOwnership
	before func()
	err    error
}

func (r *fixtureOwnershipResolver) Resolve(ctx context.Context, scope OwnershipContext, refs []ResourceRef) ([]ResourceOwnership, error) {
	if r.before != nil {
		hook := r.before
		r.before = nil
		hook()
	}
	if r.err != nil {
		return nil, r.err
	}
	out := []ResourceOwnership{}
	for _, ref := range refs {
		if p, ok := r.rows[ref]; ok {
			out = append(out, p)
		} else {
			return nil, ErrNotFound
		}
	}
	return out, nil
}

// These GIS receipts are explicit contract fixtures, not real farm evidence.
func authorizationFixture(t *testing.T) (Service, OrganizationResult, *fixtureOwnershipResolver) {
	t.Helper()
	s, org := fixture(t)
	now := time.Now().UTC()
	s.Now = func() time.Time { return now }
	r := &fixtureOwnershipResolver{rows: map[ResourceRef]ResourceOwnership{}}
	for _, id := range []string{"farm_a", "farm_b"} {
		ref := ResourceRef{Kind: "farm", ID: id}
		r.rows[ref] = ResourceOwnership{Resource: ref, TenantID: org.Tenant.ID, WorkspaceID: org.Workspace.ID, FarmIDs: []string{id}, Revision: 1, Status: "active", ReceiptID: "fixture_receipt_" + id, IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
	}
	for _, id := range []string{"parcel_a", "asset_a", "asset_multi"} {
		kind := "parcel"
		if id != "parcel_a" {
			kind = "asset"
		}
		ref := ResourceRef{Kind: kind, ID: id}
		farms := []string{"farm_a"}
		if id == "asset_multi" {
			farms = append(farms, "farm_b")
		}
		r.rows[ref] = ResourceOwnership{Resource: ref, TenantID: org.Tenant.ID, WorkspaceID: org.Workspace.ID, FarmIDs: farms, Revision: 1, Status: "active", ReceiptID: "fixture_receipt_" + id, IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
	}
	s.Ownership = r
	return s, org, r
}

func TestAuthorizationExplicitFarmScopeAndRoleCeiling(t *testing.T) {
	s, org, _ := authorizationFixture(t)
	ctx := context.Background()
	join(t, s, org, "bob", "viewer")
	if _, e := s.Authorize(ctx, "bob", org.Workspace.ID, "agri.farm.read", []ResourceRef{{Kind: "farm", ID: "farm_a"}}); !errors.Is(e, ErrNotFound) {
		t.Fatal("workspace membership implied farm access", e)
	}
	caps := []string{"agri.farm.read", "agri.parcel.read", "agri.asset.read"}
	if _, e := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ResourceRef{Kind: "farm", ID: "farm_a"}, caps, nil, revisionPointer(0)); e != nil {
		t.Fatal(e)
	}
	decision, e := s.Authorize(ctx, "bob", org.Workspace.ID, "agri.parcel.read", []ResourceRef{{Kind: "parcel", ID: "parcel_a"}})
	if e != nil || len(decision.FarmIDs) != 1 || decision.FarmIDs[0] != "farm_a" {
		t.Fatal("explicit farm read rejected", e)
	}
	for _, tc := range []struct {
		action string
		ref    ResourceRef
		want   error
	}{
		{"agri.farm.read", ResourceRef{"farm", "farm_b"}, ErrNotFound},
		{"agri.parcel.update", ResourceRef{"parcel", "parcel_a"}, ErrForbidden},
		{"agri.asset_download.read", ResourceRef{"asset", "asset_a"}, ErrForbidden},
		{"agri.asset.read", ResourceRef{"asset", "asset_multi"}, ErrNotFound},
		{"agri.billing.update", ResourceRef{"workspace", org.Workspace.ID}, ErrForbidden},
	} {
		if _, err := s.Authorize(ctx, "bob", org.Workspace.ID, tc.action, []ResourceRef{tc.ref}); !errors.Is(err, tc.want) {
			t.Errorf("%s %s: %v", tc.action, tc.ref.ID, err)
		}
	}
	if _, e := s.Authorize(ctx, "owner", org.Workspace.ID, "agri.farm.read", []ResourceRef{{"farm", "farm_a"}}); !errors.Is(e, ErrNotFound) {
		t.Fatal("owner governance implied content", e)
	}
}

func TestAuthorizationRechecksWithdrawalOutsideDatabaseTransaction(t *testing.T) {
	s, org, r := authorizationFixture(t)
	ctx := context.Background()
	join(t, s, org, "bob", "analyst")
	if _, e := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ResourceRef{"farm", "farm_a"}, []string{"agri.farm.read"}, nil, revisionPointer(0)); e != nil {
		t.Fatal(e)
	}
	before, e := s.Authorize(ctx, "bob", org.Workspace.ID, "agri.farm.read", []ResourceRef{{"farm", "farm_a"}})
	if e != nil {
		t.Fatal(e)
	}
	r.before = func() {
		roles := []string{"viewer"}
		if err := s.ChangeMember(ctx, "owner", org.Workspace.ID, "bob", &roles, nil); err != nil {
			t.Fatal(err)
		}
	}
	// SQLite fixture has exactly one connection; this callback would deadlock if
	// remote resolution were inside the authorization transaction.
	if _, e := s.Authorize(ctx, "bob", org.Workspace.ID, "agri.farm.read", before.Resources); !errors.Is(e, ErrConflict) {
		t.Fatal("revocation race reused stale snapshot", e)
	}
	if _, e := s.Reauthorize(ctx, "bob", before); !errors.Is(e, ErrConflict) {
		t.Fatal("old background decision survived epoch change", e)
	}
	status := "removed"
	if e := s.ChangeMember(ctx, "owner", org.Workspace.ID, "bob", nil, &status); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Reauthorize(ctx, "bob", before); !errors.Is(e, ErrNotFound) {
		t.Fatal("removed member background access survived", e)
	}
}

func TestAuthorizationProviderFailuresAndCrossScope(t *testing.T) {
	for _, kind := range []string{"absent", "unavailable", "cross_tenant", "cross_workspace", "expired", "inactive"} {
		t.Run(kind, func(t *testing.T) {
			s, org, r := authorizationFixture(t)
			ctx := context.Background()
			join(t, s, org, "bob", "viewer")
			ref := ResourceRef{"farm", "farm_a"}
			if _, e := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ref, []string{"agri.farm.read"}, nil, revisionPointer(0)); e != nil {
				t.Fatal(e)
			}
			want := ErrDependencyUnavailable
			p := r.rows[ref]
			switch kind {
			case "absent":
				s.Ownership = nil
			case "unavailable":
				r.err = errors.New("fixture network failure")
			case "cross_tenant":
				p.TenantID = "tenant_other"
				want = ErrNotFound
			case "cross_workspace":
				p.WorkspaceID = "workspace_other"
				want = ErrNotFound
			case "expired":
				p.ExpiresAt = s.now()
			case "inactive":
				p.Status = "archived"
				want = ErrNotFound
			}
			r.rows[ref] = p
			if _, e := s.Authorize(ctx, "bob", org.Workspace.ID, "agri.farm.read", []ResourceRef{ref}); !errors.Is(e, want) {
				t.Fatal(kind, e)
			}
		})
	}
}

func TestAuthorizationDirectResourceRevisionAndIndependentDownload(t *testing.T) {
	s, org, r := authorizationFixture(t)
	ctx := context.Background()
	join(t, s, org, "bob", "viewer")
	ref := ResourceRef{"asset", "asset_multi"}
	if _, e := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ref, []string{"agri.asset.read", "agri.asset_download.read"}, nil, revisionPointer(0)); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Authorize(ctx, "bob", org.Workspace.ID, "agri.asset_download.read", []ResourceRef{ref}); e != nil {
		t.Fatal(e)
	}
	p := r.rows[ref]
	p.Revision++
	r.rows[ref] = p
	if _, e := s.Authorize(ctx, "bob", org.Workspace.ID, "agri.asset_download.read", []ResourceRef{ref}); !errors.Is(e, ErrNotFound) {
		t.Fatal("direct grant expanded to changed asset revision", e)
	}
}

func TestAuthorizationStatusChecksAreNotReplacedByEpoch(t *testing.T) {
	s, org, _ := authorizationFixture(t)
	ctx := context.Background()
	join(t, s, org, "bob", "viewer")
	if _, e := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ResourceRef{"farm", "farm_a"}, []string{"agri.farm.read"}, nil, revisionPointer(0)); e != nil {
		t.Fatal(e)
	}
	before, e := s.Authorize(ctx, "bob", org.Workspace.ID, "agri.farm.read", []ResourceRef{{"farm", "farm_a"}})
	if e != nil {
		t.Fatal(e)
	}
	if e := s.DB.Model(&model.User{}).Where("id=?", "bob").Update("status", model.UserStatusDisabled).Error; e != nil {
		t.Fatal(e)
	}
	if _, e := s.Reauthorize(ctx, "bob", before); !errors.Is(e, ErrForbidden) {
		t.Fatal("disabled user trusted unchanged epoch", e)
	}
}

func TestRemovedMemberRejoiningDoesNotResurrectResourceGrants(t *testing.T) {
	s, org, _ := authorizationFixture(t)
	ctx := context.Background()
	join(t, s, org, "bob", "viewer")
	ref := ResourceRef{"farm", "farm_a"}
	if _, err := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ref, []string{"agri.farm.read"}, nil, revisionPointer(0)); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"removed", "active"} {
		if err := s.ChangeMember(ctx, "owner", org.Workspace.ID, "bob", nil, &status); err != nil {
			t.Fatal(err)
		}
	}
	join(t, s, org, "bob", "viewer")
	if _, err := s.Authorize(ctx, "bob", org.Workspace.ID, "agri.farm.read", []ResourceRef{ref}); !errors.Is(err, ErrNotFound) {
		t.Fatal("rejoin silently restored removed resource access", err)
	}
}

func TestDirectGrantDoesNotOverrideFarmAllowAndDeadline(t *testing.T) {
	s, org, _ := authorizationFixture(t)
	ctx := context.Background()
	join(t, s, org, "bob", "viewer")
	if _, err := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ResourceRef{"farm", "farm_a"}, []string{"agri.asset.read", "agri.asset_download.read"}, nil, revisionPointer(0)); err != nil {
		t.Fatal(err)
	}
	soon := s.now().Add(3 * time.Second)
	ref := ResourceRef{"asset", "asset_a"}
	if _, err := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ref, []string{"agri.asset.read"}, &soon, revisionPointer(0)); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"agri.asset.read", "agri.asset_download.read"} {
		decision, err := s.Authorize(ctx, "bob", org.Workspace.ID, action, []ResourceRef{ref})
		if err != nil || !decision.ExpiresAt.Equal(s.now().Add(30*time.Second)) {
			t.Fatal("narrow direct grant overrode stronger inherited allow", action, err, decision.ExpiresAt)
		}
	}
}

func TestDecisionDeadlineAndOwnershipChanges(t *testing.T) {
	s, org, r := authorizationFixture(t)
	ctx := context.Background()
	join(t, s, org, "bob", "viewer")
	soon := s.now().Add(3 * time.Second)
	ref := ResourceRef{"asset", "asset_a"}
	if _, err := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ref, []string{"agri.asset.read"}, &soon, revisionPointer(0)); err != nil {
		t.Fatal(err)
	}
	decision, err := s.Authorize(ctx, "bob", org.Workspace.ID, "agri.asset.read", []ResourceRef{ref})
	if err != nil || !decision.ExpiresAt.Equal(soon) {
		t.Fatal("decision outlived supporting grant", err)
	}
	p := r.rows[ref]
	p.FarmIDs = append(p.FarmIDs, "farm_b")
	r.rows[ref] = p
	if _, err := s.Authorize(ctx, "bob", org.Workspace.ID, "agri.asset.read", []ResourceRef{ref}); !errors.Is(err, ErrNotFound) {
		t.Fatal("unchanged revision widened direct ownership grant", err)
	}
}

func TestFieldRoleCombinationDoesNotBypassAssignment(t *testing.T) {
	s, org, r := authorizationFixture(t)
	ctx := context.Background()
	join(t, s, org, "bob", "field_operator")
	roles := []string{"field_operator", "viewer"}
	if err := s.ChangeMember(ctx, "owner", org.Workspace.ID, "bob", &roles, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ResourceRef{"farm", "farm_a"}, []string{"agri.inspection.update"}, nil, revisionPointer(0)); err != nil {
		t.Fatal(err)
	}
	ref := ResourceRef{"parcel", "parcel_a"}
	if _, err := s.Authorize(ctx, "bob", org.Workspace.ID, "agri.inspection.update", []ResourceRef{ref}); !errors.Is(err, ErrNotFound) {
		t.Fatal("viewer combination expanded field write assignment", err)
	}
	p := r.rows[ref]
	p.AssignedActorID = "bob"
	r.rows[ref] = p
	if _, err := s.Authorize(ctx, "bob", org.Workspace.ID, "agri.inspection.update", []ResourceRef{ref}); err != nil {
		t.Fatal("assigned field write denied", err)
	}
}

func TestNonManagerCannotProbeOwnershipThroughGrant(t *testing.T) {
	s, org, r := authorizationFixture(t)
	ctx := context.Background()
	join(t, s, org, "bob", "viewer")
	called := false
	r.before = func() { called = true }
	for _, resource := range []ResourceRef{{"farm", "farm_a"}, {"farm", "farm_missing"}} {
		if _, err := s.GrantResourceBinding(ctx, "bob", org.Workspace.ID, "bob", resource, []string{"agri.farm.read"}, nil, revisionPointer(0)); !errors.Is(err, ErrForbidden) {
			t.Fatal("nonmanager response revealed ownership existence", err)
		}
	}
	if called {
		t.Fatal("nonmanager invoked ownership discovery")
	}
}
