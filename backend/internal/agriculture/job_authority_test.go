package agriculture

import (
	"context"
	"errors"
	"testing"
	"time"
)

func jobAuthorityFixture(t *testing.T, expiry *time.Time) (Service, JobAuthoritySnapshot, *fixtureOwnershipResolver) {
	t.Helper()
	s, org, resolver := authorizationFixture(t)
	join(t, s, org, "bob", "analyst")
	if _, err := s.GrantResourceBinding(context.Background(), "owner", org.Workspace.ID, "bob", ResourceRef{"farm", "farm_a"}, []string{"agri.analysis.submit"}, expiry, revisionPointer(0)); err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := s.NewJobAuthority(context.Background(), "bob", org.Workspace.ID, "operation_fixture", []ResourceRef{{"asset", "asset_a"}}, []byte(`{"algorithm":"ndvi","revision":1}`))
	if err != nil {
		t.Fatal(err)
	}
	return s, snapshot, resolver
}

func TestJobAuthorityOutlivesRequestButNotCurrentAuthority(t *testing.T) {
	s, snapshot, resolver := jobAuthorityFixture(t, nil)
	digest, err := snapshot.Digest()
	if err != nil {
		t.Fatal(err)
	}
	now := s.now().Add(2 * time.Hour)
	s.Now = func() time.Time { return now }
	for ref, row := range resolver.rows {
		row.IssuedAt = now
		row.ExpiresAt = now.Add(time.Minute)
		resolver.rows[ref] = row
	}
	next, err := s.CheckJobAuthority(context.Background(), snapshot, digest)
	if err != nil || !next.ExpiresAt.After(now) {
		t.Fatal("long job incorrectly reused expired request ticket", err)
	}
	altered := snapshot
	altered.InputHash = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err = s.CheckJobAuthority(context.Background(), altered, digest); !errors.Is(err, ErrInvalid) {
		t.Fatal("input mutation accepted", err)
	}
	ref := ResourceRef{"asset", "asset_a"}
	row := resolver.rows[ref]
	row.Revision++
	resolver.rows[ref] = row
	if _, err = s.CheckJobAuthority(context.Background(), snapshot, digest); !errors.Is(err, ErrConflict) {
		t.Fatal("resource revision changed", err)
	}
}

func TestJobAuthorityNaturalGrantExpiryRequiresFreshCheck(t *testing.T) {
	expires := time.Now().UTC().Add(2 * time.Minute)
	s, snapshot, resolver := jobAuthorityFixture(t, &expires)
	digest, _ := snapshot.Digest()
	now := expires.Add(time.Second)
	s.Now = func() time.Time { return now }
	for ref, row := range resolver.rows {
		row.IssuedAt = now
		row.ExpiresAt = now.Add(time.Minute)
		resolver.rows[ref] = row
	}
	if _, err := s.CheckJobAuthority(context.Background(), snapshot, digest); err == nil {
		t.Fatal("naturally expired grant accepted despite unchanged epoch")
	}
}

func TestJobAuthoritySnapshotStrictScopeAndRetry(t *testing.T) {
	s, snapshot, _ := jobAuthorityFixture(t, nil)
	digest, _ := snapshot.Digest()
	for i := 0; i < 2; i++ {
		if _, err := s.CheckJobAuthority(context.Background(), snapshot, digest); err != nil {
			t.Fatal("same immutable retry failed", err)
		}
	}
	for _, mutate := range []func(*JobAuthoritySnapshot){
		func(v *JobAuthoritySnapshot) { v.TenantID = "tenant_other" },
		func(v *JobAuthoritySnapshot) { v.Resources = []ResourceRef{{"asset", "asset_other"}} },
		func(v *JobAuthoritySnapshot) { v.ActorUserID = "unknown" },
		func(v *JobAuthoritySnapshot) { v.AuthorizationVersion++ },
	} {
		changed := snapshot
		mutate(&changed)
		hash, err := changed.Digest()
		if err == nil {
			_, err = s.CheckJobAuthority(context.Background(), changed, hash)
		}
		if err == nil {
			t.Fatal("substituted snapshot accepted")
		}
	}
	roles := []string{"viewer"}
	if err := s.ChangeMember(context.Background(), "owner", snapshot.WorkspaceID, "bob", &roles, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckJobAuthority(context.Background(), snapshot, digest); err == nil {
		t.Fatal("withdrawn authority retained")
	}
}
