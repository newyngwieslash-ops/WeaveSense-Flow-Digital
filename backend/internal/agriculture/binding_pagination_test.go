package agriculture

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

func paginationFixture(t *testing.T, count int) (Service, OrganizationResult) {
	t.Helper()
	s, org, resolver := authorizationFixture(t)
	join(t, s, org, "bob", "viewer")
	join(t, s, org, "carol", "tenant_admin")
	for i := 0; i < count; i++ {
		ref := ResourceRef{"farm", fmt.Sprintf("farm_page_%03d", i)}
		proof := resolver.rows[ResourceRef{"farm", "farm_a"}]
		proof.Resource = ref
		proof.FarmIDs = []string{ref.ID}
		resolver.rows[ref] = proof
		if _, err := s.GrantResourceBinding(context.Background(), "owner", org.Workspace.ID, "bob", ref, []string{"agri.farm.read"}, nil, revisionPointer(0)); err != nil {
			t.Fatal(err)
		}
	}
	return s, org
}

func TestBindingPaginationRealScopedPagesAndDefaultLimit(t *testing.T) {
	s, org := paginationFixture(t, 53)
	ctx := context.Background()
	first, err := s.ListBindingPage(ctx, "owner", org.Workspace.ID, 0, "")
	if err != nil || len(first.Items) != 50 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	second, err := s.ListBindingPage(ctx, "owner", org.Workspace.ID, 0, first.NextCursor)
	if err != nil || len(second.Items) != 3 || second.HasMore || second.NextCursor != "" {
		t.Fatalf("second page: %+v %v", second, err)
	}
	seen := map[string]bool{}
	for _, row := range append(first.Items, second.Items...) {
		if seen[row.ID] || row.TenantID != org.Tenant.ID || row.WorkspaceID != org.Workspace.ID {
			t.Fatal("duplicate or foreign scoped row")
		}
		seen[row.ID] = true
	}
	var ordered []model.AgricultureResourceBinding
	if err := s.DB.Where("tenant_id=? AND workspace_id=?", org.Tenant.ID, org.Workspace.ID).Order("created_at,id").Find(&ordered).Error; err != nil {
		t.Fatal(err)
	}
	for i, row := range append(first.Items, second.Items...) {
		if ordered[i].ID != row.ID {
			t.Fatal("page order unstable")
		}
	}
	other, err := s.CreateOrganization(ctx, "owner", "Empty pagination fixture", "pagination-other-org")
	if err != nil {
		t.Fatal(err)
	}
	empty, err := s.ListBindingPage(ctx, "owner", other.Workspace.ID, 200, "")
	if err != nil || len(empty.Items) != 0 || empty.Items == nil || empty.HasMore || empty.NextCursor != "" {
		t.Fatalf("empty scoped page: %+v %v", empty, err)
	}
}

func TestBindingPaginationCursorScopeEpochAndLimits(t *testing.T) {
	s, org := paginationFixture(t, 3)
	ctx := context.Background()
	first, err := s.ListBindingPage(ctx, "owner", org.Workspace.ID, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{-1, 201, 10000} {
		if _, err := s.ListBindingPage(ctx, "owner", org.Workspace.ID, limit, ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid limit %d: %v", limit, err)
		}
	}
	if _, err := s.ListBindingPage(ctx, "bob", org.Workspace.ID, 1, first.NextCursor); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cursor granted viewer governance: %v", err)
	}
	if _, err := s.ListBindingPage(ctx, "carol", org.Workspace.ID, 1, first.NextCursor); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-actor cursor accepted: %v", err)
	}
	other, err := s.CreateOrganization(ctx, "owner", "Other cursor scope", "pagination-other-org")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListBindingPage(ctx, "owner", other.Workspace.ID, 1, first.NextCursor); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-tenant cursor accepted: %v", err)
	}
	position, err := decodeBindingCursor(first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	position.Offset = 100
	raw, _ := json.Marshal(position)
	if _, err := s.ListBindingPage(ctx, "owner", org.Workspace.ID, 1, base64.RawURLEncoding.EncodeToString(raw)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("offset beyond scoped count accepted: %v", err)
	}
	if err := s.RevokeBinding(ctx, "owner", org.Workspace.ID, first.Items[0].ID, revisionPointer(first.Items[0].AuthVersion)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListBindingPage(ctx, "owner", org.Workspace.ID, 1, first.NextCursor); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale epoch accepted: %v", err)
	}
}

func TestBindingPaginationCursorStrictDecoding(t *testing.T) {
	valid := bindingCursor{SchemaVersion: "1.0", ActorID: "owner", TenantID: "tenant", WorkspaceID: "workspace", AuthorizationVersion: 1, Offset: 1, Sort: "created_at,id", Filter: "all"}
	raw, _ := json.Marshal(valid)
	cases := []string{"", strings.Repeat("a", 1401), "not_base64!", base64.RawURLEncoding.EncodeToString(append(raw, []byte(" {}")...)), base64.RawURLEncoding.EncodeToString([]byte(`{"unexpected":true}`)), base64.RawURLEncoding.EncodeToString([]byte("null"))}
	for _, value := range cases {
		if _, err := decodeBindingCursor(value); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid cursor accepted: %q %v", value, err)
		}
	}
	for _, modify := range []func(*bindingCursor){func(p *bindingCursor) { p.Offset = -1 }, func(p *bindingCursor) { p.Offset = maximumAuthorizationVersion + 1 }, func(p *bindingCursor) { p.Sort = "id DESC" }, func(p *bindingCursor) { p.Filter = "other" }, func(p *bindingCursor) { p.SchemaVersion = "2.0" }, func(p *bindingCursor) { p.AuthorizationVersion = 0 }} {
		p := valid
		modify(&p)
		data, _ := json.Marshal(p)
		if _, err := decodeBindingCursor(base64.RawURLEncoding.EncodeToString(data)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid fields accepted: %+v", p)
		}
	}
}

func TestBindingPaginationCanonicalHTTPMetadataAndValidation(t *testing.T) {
	s, org := paginationFixture(t, 3)
	base := "/api/agri/v1/workspaces/" + org.Workspace.ID + "/resource-bindings"
	w := authorizationHTTP(t, s, "owner", "GET", base+"?limit=2", "", "")
	assertV1(t, w, 200, "")
	var first struct {
		Data       []authorizationV1Binding `json:"data"`
		NextCursor string                   `json:"next_cursor"`
		HasMore    bool                     `json:"has_more"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil || len(first.Data) != 2 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("page metadata: %s %v", w.Body.String(), err)
	}
	w = authorizationHTTP(t, s, "owner", "GET", base+"?limit=2&cursor="+url.QueryEscape(first.NextCursor), "", "")
	assertV1(t, w, 200, "")
	var second struct {
		Data       []authorizationV1Binding `json:"data"`
		NextCursor string                   `json:"next_cursor"`
		HasMore    bool                     `json:"has_more"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &second); err != nil || len(second.Data) != 1 || second.HasMore || second.NextCursor != "" {
		t.Fatalf("last metadata: %s %v", w.Body.String(), err)
	}
	for _, query := range []string{"limit=0", "limit=-1", "limit=201", "limit=1.5", "limit=", "limit=1&limit=2", "cursor=", "cursor=bad!", "tenant_id=foreign", "filter=active"} {
		assertV1(t, authorizationHTTP(t, s, "owner", "GET", base+"?"+query, "", ""), 400, "INVALID_ARGUMENT")
	}
	if err := s.RevokeBinding(context.Background(), "owner", org.Workspace.ID, first.Data[0].ID, revisionPointer(first.Data[0].AuthorizationVersion)); err != nil {
		t.Fatal(err)
	}
	assertV1(t, authorizationHTTP(t, s, "owner", "GET", base+"?cursor="+url.QueryEscape(first.NextCursor), "", ""), 409, "REVISION_CONFLICT")
}
