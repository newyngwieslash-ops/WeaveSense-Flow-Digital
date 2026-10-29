package agriculture

import (
	"context"
	"errors"
	"testing"
	"time"
)

// PostgreSQL, RLS, transactions and service methods are real. GIS ownership
// receipts below are explicit contract fixtures, not verified live farm data.
func TestAgriculturePostgresResourceAuthorization(t *testing.T) {
	s, _, actor, recipient := postgresFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s.Now = func() time.Time { return now }
	org, err := s.CreateOrganization(ctx, actor, "Authorization PostgreSQL fixture", "pg-authorization-001")
	if err != nil {
		t.Fatal("runtime authorization organization creation failed")
	}
	inv, err := s.CreateInvite(ctx, actor, org.Workspace.ID, recipient+"@fixture.invalid", "viewer")
	if err != nil {
		t.Fatal("authorization fixture invitation failed")
	}
	if _, err := s.AcceptInvite(ctx, recipient, inv.Token); err != nil {
		t.Fatal("authorization fixture acceptance failed")
	}
	farm, otherFarm, parcel := ResourceRef{"farm", "farm_fixture_a"}, ResourceRef{"farm", "farm_fixture_b"}, ResourceRef{"parcel", "parcel_fixture_a"}
	resolver := &fixtureOwnershipResolver{rows: map[ResourceRef]ResourceOwnership{}}
	for _, ref := range []ResourceRef{farm, otherFarm, parcel} {
		farmID := farm.ID
		if ref == otherFarm {
			farmID = otherFarm.ID
		}
		resolver.rows[ref] = ResourceOwnership{Resource: ref, TenantID: org.Tenant.ID, WorkspaceID: org.Workspace.ID, FarmIDs: []string{farmID}, Revision: 1, Status: "active", ReceiptID: "fixture_receipt_" + ref.ID, IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
	}
	s.Ownership = resolver
	if _, err := s.Authorize(ctx, recipient, org.Workspace.ID, "agri.farm.read", []ResourceRef{farm}); !errors.Is(err, ErrNotFound) {
		t.Fatal("workspace viewer implicitly accessed a farm")
	}
	binding, err := s.GrantResourceBinding(ctx, actor, org.Workspace.ID, recipient, farm, []string{"agri.farm.read", "agri.parcel.read"}, nil)
	if err != nil {
		t.Fatalf("real PostgreSQL resource grant failed: %T", err)
	}
	decision, err := s.Authorize(ctx, recipient, org.Workspace.ID, "agri.parcel.read", []ResourceRef{parcel})
	if err != nil || len(decision.FarmIDs) != 1 || decision.FarmIDs[0] != farm.ID {
		t.Fatal("explicit farm-scoped parcel authorization failed")
	}
	if _, err := s.Authorize(ctx, recipient, org.Workspace.ID, "agri.farm.read", []ResourceRef{farm}); err != nil {
		t.Fatal("explicit same-farm authorization failed")
	}
	if _, err := s.Authorize(ctx, recipient, org.Workspace.ID, "agri.farm.read", []ResourceRef{otherFarm}); !errors.Is(err, ErrNotFound) {
		t.Fatal("same-tenant ungranted farm leaked")
	}
	if _, err := s.Authorize(ctx, recipient, org.Workspace.ID, "agri.parcel.update", []ResourceRef{parcel}); !errors.Is(err, ErrForbidden) {
		t.Fatal("viewer mutated parcel without capability")
	}
	proof := resolver.rows[parcel]
	crossed := proof
	crossed.TenantID = "tenant_other_fixture"
	resolver.rows[parcel] = crossed
	if _, err := s.Authorize(ctx, recipient, org.Workspace.ID, "agri.parcel.read", []ResourceRef{parcel}); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-tenant ownership proof accepted")
	}
	resolver.rows[parcel] = proof
	s.Ownership = nil
	if _, err := s.Authorize(ctx, recipient, org.Workspace.ID, "agri.parcel.read", []ResourceRef{parcel}); !errors.Is(err, ErrDependencyUnavailable) {
		t.Fatal("missing GIS dependency did not fail closed")
	}
	s.Ownership = resolver
	roles := []string{"analyst"}
	if err := s.ChangeMember(ctx, actor, org.Workspace.ID, recipient, &roles, nil); err != nil {
		t.Fatal("fixture role mutation failed")
	}
	if _, err := s.Reauthorize(ctx, recipient, decision); !errors.Is(err, ErrConflict) {
		t.Fatal("old decision survived role epoch change")
	}
	fresh, err := s.Authorize(ctx, recipient, org.Workspace.ID, "agri.parcel.read", []ResourceRef{parcel})
	if err != nil || fresh.AuthorizationVersion <= decision.AuthorizationVersion {
		t.Fatal("authorization epoch did not advance")
	}
	if err := s.RevokeBinding(ctx, actor, org.Workspace.ID, binding.ID); err != nil {
		t.Fatal("runtime binding revocation failed")
	}
	if _, err := s.Reauthorize(ctx, recipient, fresh); !errors.Is(err, ErrNotFound) {
		t.Fatal("revoked binding retained old decision access")
	}
	granted, err := s.GrantResourceBinding(ctx, actor, org.Workspace.ID, recipient, farm, []string{"agri.farm.read", "agri.parcel.read"}, nil)
	if err != nil || granted.ID != binding.ID {
		t.Fatal("regrant did not retain unique binding identity")
	}
	latest, err := s.Authorize(ctx, recipient, org.Workspace.ID, "agri.parcel.read", []ResourceRef{parcel})
	if err != nil || latest.AuthorizationVersion <= fresh.AuthorizationVersion {
		t.Fatal("regrant authorization epoch did not advance")
	}
	rows, err := s.ListBindings(ctx, actor, org.Workspace.ID)
	if err != nil || len(rows) != 1 {
		t.Fatal("regrant duplicated scoped binding")
	}
	status := "removed"
	if err := s.ChangeMember(ctx, actor, org.Workspace.ID, recipient, nil, &status); err != nil {
		t.Fatal("fixture member removal failed")
	}
	if _, err := s.Reauthorize(ctx, recipient, latest); !errors.Is(err, ErrNotFound) {
		t.Fatal("removed member retained resource authorization")
	}
	t.Run("reciprocal_grants", testPostgresReciprocalResourceGrants)
}

func testPostgresReciprocalResourceGrants(t *testing.T) {
	s, _, actor, recipient := postgresFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	org, err := s.CreateOrganization(ctx, actor, "Reciprocal grant fixture", "pg-reciprocal-001")
	if err != nil {
		t.Fatal("reciprocal organization creation failed")
	}
	inv, err := s.CreateInvite(ctx, actor, org.Workspace.ID, recipient+"@fixture.invalid", "analyst")
	if err != nil {
		t.Fatal("reciprocal invitation failed")
	}
	if _, err := s.AcceptInvite(ctx, recipient, inv.Token); err != nil {
		t.Fatal("reciprocal acceptance failed")
	}
	ownerRoles, memberRoles := []string{"analyst"}, []string{"tenant_admin", "analyst"}
	if err := s.ChangeMember(ctx, actor, org.Workspace.ID, actor, &ownerRoles, nil); err != nil {
		t.Fatal("explicit owner business role failed")
	}
	if err := s.ChangeMember(ctx, actor, org.Workspace.ID, recipient, &memberRoles, nil); err != nil {
		t.Fatal("explicit recipient governance failed")
	}
	now := time.Now().UTC()
	s.Now = func() time.Time { return now }
	ref := ResourceRef{"farm", "reciprocal_farm"}
	s.Ownership = &fixtureOwnershipResolver{rows: map[ResourceRef]ResourceOwnership{ref: {Resource: ref, TenantID: org.Tenant.ID, WorkspaceID: org.Workspace.ID, FarmIDs: []string{ref.ID}, Revision: 1, Status: "active", ReceiptID: "fixture_reciprocal_receipt", IssuedAt: now, ExpiresAt: now.Add(time.Minute)}}}
	type outcome struct {
		actor, target string
		err           error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	for _, pair := range [][2]string{{actor, recipient}, {recipient, actor}} {
		go func(pair [2]string) {
			<-start
			_, err := s.GrantResourceBinding(ctx, pair[0], org.Workspace.ID, pair[1], ref, []string{"agri.farm.read"}, nil)
			results <- outcome{pair[0], pair[1], err}
		}(pair)
	}
	close(start)
	for range 2 {
		select {
		case result := <-results:
			// An optimistic epoch conflict is a valid concurrency result, not a
			// deadlock or implicit success. Retry it once after the peer completes.
			if errors.Is(result.err, ErrConflict) {
				if _, err := s.GrantResourceBinding(ctx, result.actor, org.Workspace.ID, result.target, ref, []string{"agri.farm.read"}, nil); err != nil {
					t.Fatal("reciprocal conflict retry failed")
				}
			} else if result.err != nil {
				t.Fatalf("reciprocal grant failed: %T", result.err)
			}
		case <-ctx.Done():
			t.Fatal("reciprocal grants did not finish within bounded fixture deadline")
		}
	}
	rows, err := s.ListBindings(ctx, actor, org.Workspace.ID)
	if err != nil || len(rows) != 2 {
		t.Fatal("reciprocal grants did not produce exactly one binding per subject")
	}
	for _, user := range []string{actor, recipient} {
		if _, err := s.Authorize(ctx, user, org.Workspace.ID, "agri.farm.read", []ResourceRef{ref}); err != nil {
			t.Fatal("reciprocal explicit grant did not authorize")
		}
	}
}
