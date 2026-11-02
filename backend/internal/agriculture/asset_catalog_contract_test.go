package agriculture

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"yingce/backend/internal/model"
)

func TestAssetCatalogOwnershipProfile(t *testing.T) {
	now := time.Now().UTC()
	id := "asset_" + strings.Repeat("a", 32)
	scope := OwnershipContext{"tenant_a", "workspace_a"}
	refs := []ResourceRef{{"asset", id}}
	proof := ResourceOwnership{Resource: refs[0], TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID, FarmIDs: []string{}, Revision: 1, Status: "validating", ReceiptID: "receipt_fixture", IssuedAt: now, ExpiresAt: now.Add(30 * time.Second)}
	if _, e := validateAssetCatalogOwnership(now, scope, refs, []ResourceOwnership{proof}); e != nil {
		t.Fatal(e)
	}
	if _, e := validateOwnership(now, scope, refs, []ResourceOwnership{proof}); e == nil {
		t.Fatal("typed no-farm catalog silently widened ordinary ownership")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*ResourceOwnership)
	}{{"foreign_tenant", func(p *ResourceOwnership) { p.TenantID = "other_tenant" }}, {"foreign_workspace", func(p *ResourceOwnership) { p.WorkspaceID = "other_workspace" }}, {"nil_farms", func(p *ResourceOwnership) { p.FarmIDs = nil }}, {"fabricated_active", func(p *ResourceOwnership) { p.Status = "active" }}, {"revoked", func(p *ResourceOwnership) { p.Status = "revoked" }}, {"unknown_resource", func(p *ResourceOwnership) { p.Resource.ID = "asset_" + strings.Repeat("b", 32) }}, {"expired", func(p *ResourceOwnership) { p.ExpiresAt = now }}, {"zero_revision", func(p *ResourceOwnership) { p.Revision = 0 }}} {
		t.Run(tc.name, func(t *testing.T) {
			p := proof
			tc.mutate(&p)
			if _, e := validateAssetCatalogOwnership(now, scope, refs, []ResourceOwnership{p}); e == nil {
				t.Fatal("invalid typed proof accepted")
			}
		})
	}
}
func TestAssetCatalogOwnershipHashGolden(t *testing.T) {
	proof := ResourceOwnership{Resource: ResourceRef{"asset", "asset_" + strings.Repeat("a", 32)}, TenantID: "tenant_a", WorkspaceID: "workspace_a", FarmIDs: []string{}, Revision: 2, Status: "ready"}
	hash, e := ownershipBindingHash(proof)
	if e != nil || hash != "d882b7d95a8d27a644fbab45f86bb6634edb5bb07be4f236db31c9ca939f25b5" {
		t.Fatal("Go/Python asset object ownership hash drift")
	}
	proof.FarmIDs = nil
	changed, _ := ownershipBindingHash(proof)
	if changed == hash {
		t.Fatal("null farm list confused with exact empty scope")
	}
}
func TestAssetCatalogPayloadContract(t *testing.T) {
	raw, _ := json.Marshal(assetCatalogFixtureMetadata("fixture"))
	p := AssetCatalogPayload{Action: "create", ExpectedRevision: revisionPointer(0), Metadata: raw, InitialGrants: []FarmGrant{{SubjectUserID: "fixture_user", Capabilities: []string{"agri.asset.read"}}}}
	if farm, e := validateAssetCatalogPayload(&p); e != nil || farm != "" {
		t.Fatal("valid no-farm metadata rejected")
	}
	canonical, hash, e := farmCanonical(p)
	if e != nil {
		t.Fatal(e)
	}
	op := model.AgricultureAssetOperation{ID: "aop_fixture", Action: "create", Payload: string(canonical), RequestHash: hash, GrantPlanHash: zeroFarmHash}
	body, e := assetCatalogBody(op, false)
	if e != nil {
		t.Fatal(e)
	}
	var encoded map[string]any
	if json.Unmarshal(body, &encoded) != nil || len(encoded) != 6 || encoded["asset_id"] != nil || encoded["expected_revision"] != float64(0) {
		t.Fatal("typed creation body identity drift")
	}
	missing := p
	missing.ExpectedRevision = nil
	if _, e := validateAssetCatalogPayload(&missing); !errors.Is(e, ErrPreconditionRequired) {
		t.Fatal("missing revision accepted")
	}
	missing = p
	missing.InitialGrants = nil
	if _, e := validateAssetCatalogPayload(&missing); e == nil {
		t.Fatal("implicit creator grant accepted")
	}
	missing = p
	missing.InitialGrants = []FarmGrant{{SubjectUserID: "fixture_user", Capabilities: []string{"agri.authorization.update"}}}
	if _, e := validateAssetCatalogPayload(&missing); e == nil {
		t.Fatal("unreviewed governance capability accepted")
	}
}
func TestAssetCatalogOperationHTTPContract(t *testing.T) {
	for _, tc := range []struct {
		method, state, action string
		want                  int
	}{{"GET", "pending", "create", 200}, {"GET", "completed", "create", 200}, {"GET", "reconciliation_required", "version", 200}, {"POST", "pending", "create", 202}, {"POST", "reconciliation_required", "version", 202}, {"POST", "completed", "create", 201}, {"POST", "completed", "version", 200}} {
		t.Run(tc.method+"_"+tc.state+"_"+tc.action, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(tc.method, "/api/agri/v1/assets", nil)
			assetCatalogResponse(c, assetCatalogView(model.AgricultureAssetOperation{ID: "aop_fixture", WorkspaceID: "workspace_fixture", State: tc.state, Action: tc.action}), nil)
			if w.Code != tc.want {
				t.Fatalf("status=%d", w.Code)
			}
			var body struct {
				Data map[string]any `json:"data"`
			}
			if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Data["state"] != tc.state || body.Data["job_id"] != nil || body.Data["events_url"] != nil {
				t.Fatal("pending state or unavailable event stream misrepresented")
			}
		})
	}
}
