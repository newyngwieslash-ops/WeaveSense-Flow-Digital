package agriculture

import (
	"context"
	"errors"
	"testing"
	"time"

	"yingce/backend/internal/model"
)

func revisionPointer(value int64) *int64 { return &value }

func TestResourceBindingServiceRequiresExplicitRevision(t *testing.T) {
	s, org, _ := bindingFixture(t)
	join(t, s, org, "bob", "viewer")
	ctx := context.Background()
	ref := ResourceRef{"farm", "farm_one"}
	before := bindingEpoch(t, s, org.Tenant.ID)
	if _, err := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ref, []string{"agri.farm.read"}, nil, nil); !errors.Is(err, ErrPreconditionRequired) {
		t.Fatalf("service bypassed missing revision: %v", err)
	}
	if err := s.RevokeBinding(ctx, "owner", org.Workspace.ID, "binding_absent", nil); !errors.Is(err, ErrPreconditionRequired) {
		t.Fatalf("service revoke bypassed revision: %v", err)
	}
	for _, revision := range []int64{-1, maximumAuthorizationVersion + 1} {
		if _, err := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ref, []string{"agri.farm.read"}, nil, revisionPointer(revision)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid service revision accepted: %v", err)
		}
	}
	if bindingEpoch(t, s, org.Tenant.ID) != before {
		t.Fatal("missing revision changed epoch")
	}
}

type bindingTestResolver struct {
	now   time.Time
	err   error
	calls int
}

func (r *bindingTestResolver) Resolve(_ context.Context, scope OwnershipContext, resources []ResourceRef) ([]ResourceOwnership, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	out := []ResourceOwnership{}
	for _, ref := range resources {
		out = append(out, ResourceOwnership{Resource: ref, TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID, FarmIDs: []string{ref.ID}, Revision: 1, Status: "active", ReceiptID: "receipt_fixture", IssuedAt: r.now, ExpiresAt: r.now.Add(time.Minute)})
	}
	return out, nil
}

func bindingFixture(t *testing.T) (Service, OrganizationResult, *bindingTestResolver) {
	t.Helper()
	s, org := fixture(t)
	now := time.Now().UTC()
	s.Now = func() time.Time { return now }
	r := &bindingTestResolver{now: now}
	s.Ownership = r
	return s, org, r
}

func bindingEpoch(t *testing.T, s Service, tenantID string) int64 {
	t.Helper()
	var row model.AgricultureAuthorizationEpoch
	if err := s.DB.Where("tenant_id=?", tenantID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.Version
}

func TestResourceBindingGrantReplaceRevoke(t *testing.T) {
	s, org, resolver := bindingFixture(t)
	join(t, s, org, "bob", "viewer")
	ctx := context.Background()
	ref := ResourceRef{Kind: "farm", ID: "farm_one"}
	before := bindingEpoch(t, s, org.Tenant.ID)
	expiry := s.now().Add(time.Hour)
	row, err := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ref, []string{"agri.parcel.read", "agri.farm.read"}, &expiry, revisionPointer(0))
	if err != nil {
		t.Fatal(err)
	}
	if row.Capabilities != `["agri.farm.read","agri.parcel.read"]` || row.AuthVersion != before+1 || len(row.ReceiptHash) != 64 || row.ReceiptRevision != 1 || row.SubjectType != "user" || row.State != "active" {
		t.Fatalf("unexpected binding: %+v", row)
	}
	if bindingEpoch(t, s, org.Tenant.ID) != before+1 {
		t.Fatal("grant epoch not atomic")
	}
	updated, err := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ref, []string{"agri.farm.read"}, nil, revisionPointer(row.AuthVersion))
	if err != nil || updated.ID != row.ID || updated.ExpiresAt != nil || updated.AuthVersion != before+2 {
		t.Fatalf("replace: %+v %v", updated, err)
	}
	listed, err := s.ListBindings(ctx, "owner", org.Workspace.ID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list: %v %v", listed, err)
	}
	if _, err = s.ListBindings(ctx, "bob", org.Workspace.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer list: %v", err)
	}
	resolver.err = ErrDependencyUnavailable
	calls := resolver.calls
	if err = s.RevokeBinding(ctx, "owner", org.Workspace.ID, row.ID, revisionPointer(updated.AuthVersion)); err != nil {
		t.Fatal(err)
	}
	if resolver.calls != calls {
		t.Fatal("governance revoke called GIS")
	}
	if err = s.DB.First(&updated, "id=?", row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.State != "revoked" || updated.Capabilities != "[]" || updated.AuthVersion != before+3 {
		t.Fatalf("revoke lost history: %+v", updated)
	}
	if err = s.RevokeBinding(ctx, "owner", org.Workspace.ID, row.ID, revisionPointer(updated.AuthVersion)); err != nil {
		t.Fatal(err)
	}
	if bindingEpoch(t, s, org.Tenant.ID) != before+3 {
		t.Fatal("replayed revoke changed epoch")
	}
}

func TestResourceBindingInvalidGrantNeverPersists(t *testing.T) {
	s, org, _ := bindingFixture(t)
	join(t, s, org, "bob", "viewer")
	ctx := context.Background()
	ref := ResourceRef{Kind: "farm", ID: "farm_one"}
	before := bindingEpoch(t, s, org.Tenant.ID)
	past, tooFar := s.now(), s.now().Add(367*24*time.Hour)
	tests := []struct {
		name, target string
		caps         []string
		expiry       *time.Time
	}{
		{"empty", "bob", nil, nil}, {"duplicate", "bob", []string{"agri.farm.read", "agri.farm.read"}, nil},
		{"unknown", "bob", []string{"agri.farm.destroy"}, nil}, {"finance", "bob", []string{"agri.billing.read"}, nil},
		{"role_ceiling", "bob", []string{"agri.parcel.update"}, nil}, {"owner_no_content", "owner", []string{"agri.farm.read"}, nil},
		{"nonmember", "carol", []string{"agri.farm.read"}, nil}, {"expiry_now", "bob", []string{"agri.farm.read"}, &past},
		{"expiry_too_far", "bob", []string{"agri.farm.read"}, &tooFar},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, tc.target, ref, tc.caps, tc.expiry, revisionPointer(0)); err == nil {
				t.Fatal("invalid grant accepted")
			}
		})
	}
	if _, err := s.GrantResourceBinding(ctx, "bob", org.Workspace.ID, "bob", ref, []string{"agri.farm.read"}, nil, revisionPointer(0)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("self grant: %v", err)
	}
	if err := s.DB.Model(&model.User{}).Where("id='bob'").Update("status", model.UserStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ref, []string{"agri.farm.read"}, nil, revisionPointer(0)); err == nil {
		t.Fatal("disabled target grant accepted")
	}
	var count int64
	if err := s.DB.Model(&model.AgricultureResourceBinding{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed grant persisted: %d %v", count, err)
	}
	if bindingEpoch(t, s, org.Tenant.ID) != before {
		t.Fatal("failed grant changed epoch")
	}
}

func TestResourceBindingManagerCannotWidenDelegation(t *testing.T) {
	s, org, _ := bindingFixture(t)
	join(t, s, org, "bob", "farm_manager")
	join(t, s, org, "carol", "viewer")
	ctx := context.Background()
	ref := ResourceRef{Kind: "farm", ID: "farm_one"}
	if _, err := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ref, []string{"agri.authorization.update", "agri.farm.read", "agri.asset_download.read"}, nil, revisionPointer(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantResourceBinding(ctx, "bob", org.Workspace.ID, "carol", ref, []string{"agri.parcel.read"}, nil, revisionPointer(0)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("manager widened capability: %v", err)
	}
	if _, err := s.GrantResourceBinding(ctx, "bob", org.Workspace.ID, "carol", ResourceRef{Kind: "farm", ID: "farm_two"}, []string{"agri.farm.read"}, nil, revisionPointer(0)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("manager widened farm: %v", err)
	}
	row, err := s.GrantResourceBinding(ctx, "bob", org.Workspace.ID, "carol", ref, []string{"agri.farm.read"}, nil, revisionPointer(0))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeBinding(ctx, "carol", org.Workspace.ID, row.ID, revisionPointer(row.AuthVersion)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer revoked: %v", err)
	}
	if err = s.RevokeBinding(ctx, "bob", org.Workspace.ID, row.ID, revisionPointer(row.AuthVersion)); err != nil {
		t.Fatal(err)
	}
	// Delegating download authority on a farm root is distinct from executing
	// an actual download: the public operation still requires an asset ref.
	if err = s.DB.First(&row, "id=?", row.ID).Error; err != nil || row.State != "revoked" {
		t.Fatalf("read revoked grant: %+v %v", row, err)
	}
	if _, err = s.GrantResourceBinding(ctx, "bob", org.Workspace.ID, "carol", ref, []string{"agri.asset_download.read"}, nil, revisionPointer(row.AuthVersion)); err != nil {
		t.Fatalf("manager could not delegate explicitly held download capability: %v", err)
	}
	if _, err = s.Authorize(ctx, "carol", org.Workspace.ID, "agri.asset_download.read", []ResourceRef{ref}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("delegation widened actual download resource type: %v", err)
	}
}

func TestResourceBindingCanonicalCapabilitiesDoNotMutateCaller(t *testing.T) {
	input := []string{"agri.parcel.read", "agri.farm.read"}
	output, err := canonicalBindingCapabilities(input)
	if err != nil || input[0] != "agri.parcel.read" || output[0] != "agri.farm.read" {
		t.Fatalf("canonicalization: %v %v %v", input, output, err)
	}
	output[0] = "changed"
	if input[0] != "agri.parcel.read" {
		t.Fatal("returned caller-owned slice")
	}
}

func TestResourceBindingManagerCannotExtendTemporaryAuthority(t *testing.T) {
	s, org, _ := bindingFixture(t)
	join(t, s, org, "bob", "farm_manager")
	join(t, s, org, "carol", "viewer")
	ctx := context.Background()
	ref := ResourceRef{Kind: "farm", ID: "farm_one"}
	limit := s.now().Add(time.Hour)
	if _, err := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ref, []string{"agri.authorization.update", "agri.farm.read"}, &limit, revisionPointer(0)); err != nil {
		t.Fatal(err)
	}
	longer := limit.Add(time.Second)
	for _, expiry := range []*time.Time{nil, &longer} {
		if _, err := s.GrantResourceBinding(ctx, "bob", org.Workspace.ID, "carol", ref, []string{"agri.farm.read"}, expiry, revisionPointer(0)); !errors.Is(err, ErrForbidden) {
			t.Fatalf("temporary authority extended: %v", err)
		}
	}
	granted, err := s.GrantResourceBinding(ctx, "bob", org.Workspace.ID, "carol", ref, []string{"agri.farm.read"}, &limit, revisionPointer(0))
	if err != nil {
		t.Fatal(err)
	}
	shorter := limit.Add(-time.Minute)
	if _, err := s.GrantResourceBinding(ctx, "bob", org.Workspace.ID, "carol", ref, []string{"agri.farm.read"}, &shorter, revisionPointer(granted.AuthVersion)); err != nil {
		t.Fatal(err)
	}
}

func TestResourceBindingDisabledOrganizationOwnerCanRevokeWithoutRestoring(t *testing.T) {
	s, org, resolver := bindingFixture(t)
	join(t, s, org, "bob", "viewer")
	join(t, s, org, "carol", "tenant_admin")
	ctx := context.Background()
	row, err := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ResourceRef{Kind: "farm", ID: "farm_one"}, []string{"agri.farm.read"}, nil, revisionPointer(0))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&model.Tenant{}).Where("id=?", org.Tenant.ID).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	resolver.err = ErrDependencyUnavailable
	if rows, err := s.ListBindings(ctx, "owner", org.Workspace.ID); err != nil || len(rows) != 1 {
		t.Fatalf("disabled owner list: %v %v", rows, err)
	}
	if _, err := s.ListBindings(ctx, "carol", org.Workspace.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled admin list: %v", err)
	}
	if err := s.RevokeBinding(ctx, "carol", org.Workspace.ID, row.ID, revisionPointer(row.AuthVersion)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled admin revoke: %v", err)
	}
	if err := s.RevokeBinding(ctx, "owner", org.Workspace.ID, row.ID, revisionPointer(row.AuthVersion)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ResourceRef{Kind: "farm", ID: "farm_one"}, []string{"agri.farm.read"}, nil, revisionPointer(0)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled owner grant: %v", err)
	}
}

func TestResourceBindingAuditFailureRollsBackRowAndEpoch(t *testing.T) {
	s, org, _ := bindingFixture(t)
	join(t, s, org, "bob", "viewer")
	before := bindingEpoch(t, s, org.Tenant.ID)
	table := (model.AgricultureAuditEvent{}).TableName()
	if err := s.DB.Exec("CREATE TRIGGER fail_binding_audit BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(ABORT, 'fixture audit failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.GrantResourceBinding(context.Background(), "owner", org.Workspace.ID, "bob", ResourceRef{Kind: "farm", ID: "farm_one"}, []string{"agri.farm.read"}, nil, revisionPointer(0)); err == nil {
		t.Fatal("audit failure hidden")
	}
	var count int64
	if err := s.DB.Model(&model.AgricultureResourceBinding{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("row survived rollback: %d %v", count, err)
	}
	if bindingEpoch(t, s, org.Tenant.ID) != before {
		t.Fatal("epoch survived rollback")
	}
}

func TestResourceBindingUnavailableGISAndCrossWorkspaceFailClosed(t *testing.T) {
	s, org, resolver := bindingFixture(t)
	join(t, s, org, "bob", "viewer")
	ctx := context.Background()
	ref := ResourceRef{Kind: "farm", ID: "farm_one"}
	resolver.err = ErrDependencyUnavailable
	if _, err := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ref, []string{"agri.farm.read"}, nil, revisionPointer(0)); !errors.Is(err, ErrDependencyUnavailable) {
		t.Fatalf("ownership failure hidden: %v", err)
	}
	resolver.err = nil
	row, err := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ref, []string{"agri.farm.read"}, nil, revisionPointer(0))
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateOrganization(ctx, "owner", "Other fixture", "fixture-create-other-01")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeBinding(ctx, "owner", other.Workspace.ID, row.ID, revisionPointer(row.AuthVersion)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace revoke: %v", err)
	}
	listed, err := s.ListBindings(ctx, "owner", other.Workspace.ID)
	if err != nil || len(listed) != 0 {
		t.Fatalf("cross-workspace listing: %+v %v", listed, err)
	}
	if _, err := s.GrantResourceBinding(ctx, "owner", other.Workspace.ID, "bob", ref, []string{"agri.farm.read"}, nil, revisionPointer(0)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant target: %v", err)
	}
	before := bindingEpoch(t, s, org.Tenant.ID)
	table := (model.AgricultureAuditEvent{}).TableName()
	if err := s.DB.Exec("CREATE TRIGGER fail_revoke_audit BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(ABORT, 'fixture audit failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeBinding(ctx, "owner", org.Workspace.ID, row.ID, revisionPointer(row.AuthVersion)); err == nil {
		t.Fatal("revoke audit failure hidden")
	}
	var stored model.AgricultureResourceBinding
	if err := s.DB.First(&stored, "id=?", row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.State != "active" || stored.Capabilities != row.Capabilities || bindingEpoch(t, s, org.Tenant.ID) != before {
		t.Fatal("failed revoke was not atomic")
	}
}
