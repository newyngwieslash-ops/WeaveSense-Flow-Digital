package agriculture

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func ownershipFixture() (time.Time, OwnershipContext, ResourceOwnership) {
	now := time.Date(2026, 10, 29, 5, 0, 0, 0, time.UTC)
	scope := OwnershipContext{TenantID: "tenant_A", WorkspaceID: "workspace_A"}
	proof := ResourceOwnership{Resource: ResourceRef{Kind: "parcel", ID: "parcel_A"}, TenantID: scope.TenantID,
		WorkspaceID: scope.WorkspaceID, FarmIDs: []string{"farm_Z", "farm_A"}, Revision: 1, Status: "active",
		ReceiptID: "receipt_A", IssuedAt: now.Add(-time.Second), ExpiresAt: now.Add(30 * time.Second)}
	return now, scope, proof
}

func TestCanonicalResourcesStrictContract(t *testing.T) {
	tests := []struct {
		name string
		refs []ResourceRef
	}{
		{"empty", nil},
		{"unknown_kind", []ResourceRef{{"user", "user_A"}}},
		{"kind_case", []ResourceRef{{"Farm", "farm_A"}}},
		{"empty_id", []ResourceRef{{"farm", ""}}},
		{"numeric_start", []ResourceRef{{"farm", "0farm"}}},
		{"underscore_start", []ResourceRef{{"farm", "_farm"}}},
		{"unicode", []ResourceRef{{"farm", "farm_甲"}}},
		{"path", []ResourceRef{{"farm", "farm/other"}}},
		{"control", []ResourceRef{{"farm", "farm_A\n"}}},
		{"whitespace", []ResourceRef{{"farm", " farm_A"}}},
		{"too_long", []ResourceRef{{"farm", strings.Repeat("a", 129)}}},
		{"duplicate", []ResourceRef{{"farm", "farm_A"}, {"farm", "farm_A"}}},
		{"too_many", make([]ResourceRef, 201)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := canonicalResources(test.refs)
			if !errors.Is(err, ErrInvalid) || got != nil {
				t.Fatalf("expected invalid request with no partial result, got %v", err)
			}
		})
	}
	refs := []ResourceRef{{"report", "same"}, {"parcel", "parcel_B"}, {"asset", "same"}, {"job", "job_A"}, {"farm", "farm_A"}, {"parcel", "parcel_A"}}
	before := append([]ResourceRef(nil), refs...)
	got, err := canonicalResources(refs)
	want := []ResourceRef{{"asset", "same"}, {"farm", "farm_A"}, {"job", "job_A"}, {"parcel", "parcel_A"}, {"parcel", "parcel_B"}, {"report", "same"}}
	if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(refs, before) {
		t.Fatalf("canonical order or input preservation failed: %v", err)
	}
	got[0].ID = "changed"
	if !reflect.DeepEqual(refs, before) {
		t.Fatal("canonical resources alias the input")
	}
	limit := make([]ResourceRef, 200)
	for i := range limit {
		limit[i] = ResourceRef{"asset", fmt.Sprintf("asset_%03d", i)}
	}
	limit[0].ID = strings.Repeat("a", 128)
	if _, err = canonicalResources(limit); err != nil {
		t.Fatalf("valid boundary rejected: %v", err)
	}
}

func TestValidateOwnershipReceiptFailures(t *testing.T) {
	tests := []struct {
		name string
		edit func(*ResourceOwnership)
		err  error
	}{
		{"tenant", func(p *ResourceOwnership) { p.TenantID = "tenant_B" }, ErrNotFound},
		{"workspace", func(p *ResourceOwnership) { p.WorkspaceID = "workspace_B" }, ErrNotFound},
		{"inactive", func(p *ResourceOwnership) { p.Status = "disabled" }, ErrNotFound},
		{"deleted", func(p *ResourceOwnership) { p.Status = "deleted" }, ErrNotFound},
		{"missing_tenant", func(p *ResourceOwnership) { p.TenantID = "" }, ErrDependencyUnavailable},
		{"malformed_workspace", func(p *ResourceOwnership) { p.WorkspaceID = "../workspace" }, ErrDependencyUnavailable},
		{"missing_status", func(p *ResourceOwnership) { p.Status = "" }, ErrDependencyUnavailable},
		{"zero_revision", func(p *ResourceOwnership) { p.Revision = 0 }, ErrDependencyUnavailable},
		{"negative_revision", func(p *ResourceOwnership) { p.Revision = -1 }, ErrDependencyUnavailable},
		{"unsafe_revision", func(p *ResourceOwnership) { p.Revision = 1 << 53 }, ErrDependencyUnavailable},
		{"missing_receipt", func(p *ResourceOwnership) { p.ReceiptID = "" }, ErrDependencyUnavailable},
		{"invalid_receipt", func(p *ResourceOwnership) { p.ReceiptID = "receipt?secret" }, ErrDependencyUnavailable},
		{"missing_issue", func(p *ResourceOwnership) { p.IssuedAt = time.Time{} }, ErrDependencyUnavailable},
		{"missing_expiry", func(p *ResourceOwnership) { p.ExpiresAt = time.Time{} }, ErrDependencyUnavailable},
		{"future", func(p *ResourceOwnership) { p.IssuedAt = p.IssuedAt.Add(6*time.Second + time.Nanosecond) }, ErrDependencyUnavailable},
		{"stale", func(p *ResourceOwnership) { p.IssuedAt = p.IssuedAt.Add(-59*time.Second - time.Nanosecond) }, ErrDependencyUnavailable},
		{"expired", func(p *ResourceOwnership) { p.ExpiresAt = p.IssuedAt.Add(time.Second) }, ErrDependencyUnavailable},
		{"lifetime", func(p *ResourceOwnership) { p.ExpiresAt = p.IssuedAt.Add(60*time.Second + time.Nanosecond) }, ErrDependencyUnavailable},
		{"reversed_times", func(p *ResourceOwnership) {
			p.IssuedAt = p.IssuedAt.Add(6 * time.Second)
			p.ExpiresAt = p.IssuedAt.Add(-time.Second)
		}, ErrDependencyUnavailable},
		{"no_farms", func(p *ResourceOwnership) { p.FarmIDs = nil }, ErrDependencyUnavailable},
		{"duplicate_farms", func(p *ResourceOwnership) { p.FarmIDs = []string{"farm_A", "farm_A"} }, ErrDependencyUnavailable},
		{"invalid_farm", func(p *ResourceOwnership) { p.FarmIDs = []string{"farm/A"} }, ErrDependencyUnavailable},
		{"many_farms", func(p *ResourceOwnership) { p.FarmIDs = make([]string, 201) }, ErrDependencyUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now, scope, proof := ownershipFixture()
			test.edit(&proof)
			got, err := validateOwnership(now, scope, []ResourceRef{proof.Resource}, []ResourceOwnership{proof})
			if !errors.Is(err, test.err) || got != nil {
				t.Fatalf("expected %v and no result, got %v", test.err, err)
			}
		})
	}
}

func TestValidateOwnershipExactSetAndServerContext(t *testing.T) {
	now, scope, proof := ownershipFixture()
	other := proof
	other.Resource = ResourceRef{"job", "job_A"}
	refs := []ResourceRef{proof.Resource, other.Resource}
	for name, proofs := range map[string][]ResourceOwnership{
		"missing": {proof}, "empty": nil, "duplicate": {proof, proof}, "extra": {proof, other, proof},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := validateOwnership(now, scope, refs, proofs); !errors.Is(err, ErrDependencyUnavailable) || got != nil {
				t.Fatalf("inexact provider set accepted: %v", err)
			}
		})
	}
	unknown := other
	unknown.Resource.Kind = "unknown"
	if _, err := validateOwnership(now, scope, refs, []ResourceOwnership{proof, unknown}); !errors.Is(err, ErrDependencyUnavailable) {
		t.Fatal("unknown provider resource accepted")
	}
	if _, err := validateOwnership(now, OwnershipContext{}, refs, []ResourceOwnership{proof, other}); !errors.Is(err, ErrDependencyUnavailable) {
		t.Fatal("missing trusted scope accepted")
	}
	if _, err := validateOwnership(time.Time{}, scope, refs, []ResourceOwnership{proof, other}); !errors.Is(err, ErrDependencyUnavailable) {
		t.Fatal("missing server time accepted")
	}
	if _, err := validateOwnership(now, scope, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid caller resource set accepted")
	}
	if got, err := validateOwnership(now, scope, refs, []ResourceOwnership{other, proof}); err != nil || len(got) != 2 {
		t.Fatalf("response order should not matter: %v", err)
	}
}

func TestValidateOwnershipCopiesAndBoundaries(t *testing.T) {
	now, scope, proof := ownershipFixture()
	proof.Revision = maxOwnershipRevision
	proof.IssuedAt = now.Add(5 * time.Second)
	proof.ExpiresAt = proof.IssuedAt.Add(60 * time.Second)
	// Assignment remains a domain fact, not the trusted caller identity.
	proof.AssignedActorID = "12345678-1234-1234-1234-123456789abc"
	got, err := validateOwnership(now, scope, []ResourceRef{proof.Resource}, []ResourceOwnership{proof})
	if err != nil {
		t.Fatal(err)
	}
	out := got[proof.Resource]
	if !reflect.DeepEqual(out.FarmIDs, []string{"farm_A", "farm_Z"}) || !reflect.DeepEqual(proof.FarmIDs, []string{"farm_Z", "farm_A"}) || out.AssignedActorID != proof.AssignedActorID {
		t.Fatal("canonical copy or assignment preservation failed")
	}
	out.FarmIDs[0] = "changed"
	if proof.FarmIDs[1] != "farm_A" {
		t.Fatal("output aliases provider farms")
	}
	proof.FarmIDs[0] = "provider_changed"
	if got[proof.Resource].FarmIDs[1] != "farm_Z" {
		t.Fatal("provider mutation changes validated receipt")
	}
	_, _, farm := ownershipFixture()
	farm.Resource = ResourceRef{"farm", "farm_A"}
	farm.FarmIDs = []string{"farm_A"}
	farm.IssuedAt, farm.ExpiresAt = now.Add(-59*time.Second), now.Add(time.Second)
	if _, err := validateOwnership(now, scope, []ResourceRef{farm.Resource}, []ResourceOwnership{farm}); err != nil {
		t.Fatalf("valid farm lifetime boundary rejected: %v", err)
	}
	for _, farms := range [][]string{{"farm_B"}, {"farm_A", "farm_B"}} {
		farm.FarmIDs = farms
		if _, err := validateOwnership(now, scope, []ResourceRef{farm.Resource}, []ResourceOwnership{farm}); !errors.Is(err, ErrDependencyUnavailable) {
			t.Fatal("farm proof did not require exactly its own farm")
		}
	}
	_, _, multi := ownershipFixture()
	multi.FarmIDs = make([]string, 200)
	for i := range multi.FarmIDs {
		multi.FarmIDs[i] = fmt.Sprintf("farm_%03d", 199-i)
	}
	if got, err := validateOwnership(now, scope, []ResourceRef{multi.Resource}, []ResourceOwnership{multi}); err != nil || len(got[multi.Resource].FarmIDs) != 200 {
		t.Fatalf("200-farm receipt rejected: %v", err)
	}
}

func TestValidateOwnershipBatchIsAllOrNothingAndCopiesEachReceipt(t *testing.T) {
	now, scope, first := ownershipFixture()
	second := first // Provider slices may themselves alias; validated slices must not.
	second.Resource = ResourceRef{"report", "report_A"}
	refs := []ResourceRef{first.Resource, second.Resource}
	got, err := validateOwnership(now, scope, refs, []ResourceOwnership{first, second})
	if err != nil {
		t.Fatal(err)
	}
	got[first.Resource].FarmIDs[0] = "changed"
	if got[second.Resource].FarmIDs[0] != "farm_A" || first.FarmIDs[1] != "farm_A" {
		t.Fatal("validated receipts alias one another or provider input")
	}
	second.ExpiresAt = now
	if got, err := validateOwnership(now, scope, refs, []ResourceOwnership{first, second}); got != nil || !errors.Is(err, ErrDependencyUnavailable) {
		t.Fatal("invalid batch returned partial ownership")
	}
}
