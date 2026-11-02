package agriculture

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

type assetCatalogAuthorityRequest struct {
	ActorUserID          string   `json:"actor_user_id"`
	TenantID             string   `json:"tenant_id"`
	WorkspaceID          string   `json:"workspace_id"`
	Action               string   `json:"action"`
	AuthorizationVersion int64    `json:"authorization_version"`
	OperationID          string   `json:"operation_id"`
	RequestHash          string   `json:"request_hash"`
	GrantPlanHash        string   `json:"grant_plan_hash"`
	AssetIDs             []string `json:"asset_ids"`
	RequestPayloadHash   string   `json:"request_payload_hash"`
}

func (p *AssetCatalogPlatform) currentIntent(tx *gorm.DB, s workspaceContext, op model.AgricultureAssetOperation) error {
	epoch, e := currentAuthorizationVersion(tx, s.Tenant.ID)
	if e != nil {
		return e
	}
	if op.TenantID != s.Tenant.ID || op.WorkspaceID != s.Workspace.ID || epoch != op.AuthorizationVersion {
		return ErrConflict
	}
	payload, e := assetCatalogPayload(op)
	if e != nil {
		return e
	}
	if op.Action == "create" {
		if e = p.validatePlan(tx, s, payload); e != nil {
			return e
		}
		if op.FarmID == "" {
			if !s.Owner && !hasRole(s.Roles, "tenant_admin") {
				return ErrForbidden
			}
			return nil
		}
	}
	if !roleAllowsAction(s.Roles, "agri.asset.create") {
		return ErrForbidden
	}
	// The frozen intent's farm came from trusted typed ownership at preview.
	// Its epoch must still match; current domain proof is checked at dispatch
	// and receipt application. A parent grant is an independent positive path.
	if op.Action == "version" && op.FarmID != "" {
		var parent model.AgricultureResourceBinding
		err := tx.Where("tenant_id=? AND workspace_id=? AND subject_type='user' AND subject_id=? AND resource_kind='farm' AND resource_id=? AND state='active' AND (expires_at IS NULL OR expires_at>?)", s.Tenant.ID, s.Workspace.ID, op.ActorID, op.FarmID, p.Service.now()).First(&parent).Error
		if err == nil {
			caps, err := bindingCapabilities(parent)
			if err != nil {
				return err
			}
			if hasRole(caps, "agri.asset.create") {
				return nil
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	kind, id := "asset", op.AssetID
	need := []string{"agri.asset.create"}
	if op.Action == "create" {
		kind, id = "farm", op.FarmID
		if !s.Owner && !hasRole(s.Roles, "tenant_admin") {
			if !hasRole(s.Roles, "farm_manager") {
				return ErrForbidden
			}
			need = append(need, "agri.authorization.update")
			for _, g := range payload.InitialGrants {
				need = append(need, g.Capabilities...)
			}
		}
	}
	var b model.AgricultureResourceBinding
	if tx.Where("tenant_id=? AND workspace_id=? AND subject_type='user' AND subject_id=? AND resource_kind=? AND resource_id=? AND state='active' AND (expires_at IS NULL OR expires_at>?)", s.Tenant.ID, s.Workspace.ID, op.ActorID, kind, id, p.Service.now()).First(&b).Error != nil {
		return ErrForbidden
	}
	if op.Action == "version" && b.ReceiptRevision != op.ExpectedRevision {
		return ErrForbidden
	}
	caps, e := bindingCapabilities(b)
	if e != nil {
		return e
	}
	allowed := map[string]bool{}
	for _, c := range caps {
		allowed[c] = true
	}
	for _, c := range need {
		if !allowed[c] {
			return ErrForbidden
		}
	}
	if op.Action == "create" && !s.Owner && !hasRole(s.Roles, "tenant_admin") && b.ExpiresAt != nil {
		for _, g := range payload.InitialGrants {
			if g.ExpiresAt == nil || g.ExpiresAt.After(*b.ExpiresAt) {
				return ErrForbidden
			}
		}
	}
	return nil
}
func (p *AssetCatalogPlatform) checkCurrent(ctx context.Context, in assetCatalogAuthorityRequest) error {
	if assetCatalogPaths[in.Action] == "" || !ownershipID.MatchString(in.OperationID) || !parcelHashPattern.MatchString(in.RequestPayloadHash) {
		return ErrForbidden
	}
	if in.Action == "asset.context" {
		if len(in.AssetIDs) != 0 || in.RequestHash != zeroFarmHash || in.GrantPlanHash != zeroFarmHash {
			return ErrForbidden
		}
		_, hash, _ := farmCanonical(map[string]any{})
		if hash != in.RequestPayloadHash {
			return ErrForbidden
		}
		return p.Service.withWorkspace(ctx, in.ActorUserID, in.WorkspaceID, false, func(tx *gorm.DB, s workspaceContext) error {
			v, e := currentAuthorizationVersion(tx, s.Tenant.ID)
			if e != nil {
				return e
			}
			if s.Tenant.ID != in.TenantID || v != in.AuthorizationVersion {
				return ErrForbidden
			}
			return nil
		})
	}
	if in.Action == "asset.list" || in.Action == "asset.read" {
		if in.RequestHash != zeroFarmHash || in.GrantPlanHash != zeroFarmHash || len(in.AssetIDs) == 0 || len(in.AssetIDs) > 200 || (in.Action == "asset.read" && len(in.AssetIDs) != 1) {
			return ErrForbidden
		}
		d, e := p.Authorize(ctx, in.ActorUserID, in.WorkspaceID, "agri.asset.read", in.AssetIDs)
		if e != nil {
			return e
		}
		if d.TenantID != in.TenantID || d.AuthorizationVersion != in.AuthorizationVersion {
			return ErrConflict
		}
		return nil
	}
	op, e := p.loadOperation(ctx, in.ActorUserID, in.WorkspaceID, in.OperationID)
	if e != nil {
		return e
	}
	payload, e := assetCatalogPayload(op)
	if e != nil {
		return e
	}
	return p.Service.withWorkspacePrincipals(ctx, in.ActorUserID, in.WorkspaceID, false, assetCatalogGrantees(payload), func(tx *gorm.DB, s workspaceContext) error {
		if op.TenantID != in.TenantID || op.RequestHash != in.RequestHash || op.GrantPlanHash != in.GrantPlanHash || op.AuthorizationVersion != in.AuthorizationVersion {
			return ErrForbidden
		}
		receipt := in.Action == "asset.receipt"
		ids := assetCatalogOpIDs(op)
		if receipt {
			ids = []string{}
		}
		if !assetCatalogEqualIDs(ids, in.AssetIDs) {
			return ErrForbidden
		}
		if !receipt && in.Action != "asset.preview."+op.Action && in.Action != "asset."+op.Action {
			return ErrForbidden
		}
		if strings.HasPrefix(in.Action, "asset.preview.") && (op.State != "preview" || !op.ExpiresAt.After(p.Service.now())) {
			return ErrForbidden
		}
		if in.Action == "asset."+op.Action && op.State != "pending" {
			return ErrForbidden
		}
		if receipt && op.State == "preview" {
			return ErrForbidden
		}
		body, e := assetCatalogBody(op, receipt)
		if e != nil {
			return e
		}
		_, hash, e := farmCanonical(mustAssetCatalogObject(body))
		if e != nil || hash != in.RequestPayloadHash {
			return ErrForbidden
		}
		return p.currentIntent(tx, s, op)
	})
}
func mustAssetCatalogObject(raw []byte) map[string]any { v, _ := parcelDecodeMap(raw); return v }
func assetCatalogEqualIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func (p *AssetCatalogPlatform) workspaceCall(ctx context.Context, actor, ws, action, opID, requestHash, grantHash string, ids []string, body []byte) ([]byte, error) {
	if p.Client == nil {
		return nil, ErrDependencyUnavailable
	}
	path := assetCatalogPaths[action]
	if path == "" {
		return nil, ErrInvalid
	}
	sort.Strings(ids)
	bound, canonical, e := bindIdentityRequest("POST", path, body, opID, false)
	if e != nil {
		return nil, e
	}
	var tenant string
	var epoch int64
	e = p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		tenant = s.Tenant.ID
		var e error
		epoch, e = currentAuthorizationVersion(tx, tenant)
		return e
	})
	if e != nil {
		return nil, e
	}
	in := assetCatalogAuthorityRequest{actor, tenant, ws, action, epoch, opID, requestHash, grantHash, ids, bound.PayloadHash}
	if e = p.checkCurrent(ctx, in); e != nil {
		return nil, e
	}
	now := p.Service.now().UTC().Truncate(time.Second)
	until := now.Add(30 * time.Second)
	claims := map[string]any{"schema_version": "1.0", "issuer": "flow-platform", "subject": "flow-platform", "audience": "agri-gis", "actor": identityActor{"user", actor}, "tenant_id": tenant, "workspace_id": ws, "action": action, "authorization_version": epoch, "policy_version": authorizationPolicyVersion, "operation_id": opID, "request_hash": requestHash, "grant_plan_hash": grantHash, "asset_ids": ids, "issued_at": now.Format(time.RFC3339), "expires_at": until.Format(time.RFC3339), "jti": flow.NewID("jti_"), "trace_id": flow.NewID("trace_"), "request_id": flow.NewID("req_"), "request": bound}
	token, e := p.Client.keys.sign(assetCatalogType, claims, now, until)
	if e != nil {
		return nil, ErrDependencyUnavailable
	}
	return p.Client.farmSend(ctx, "POST", path, canonical, token)
}
func (p *AssetCatalogPlatform) RegisterInternal(root gin.IRouter, keys *ServiceKeyring) {
	p.RegisterDownloadAuthority(root, keys)
	root.POST(assetCatalogAuthorityPath, func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		fail := func(status int) { c.JSON(status, gin.H{"error": gin.H{"code": "CAPABILITY_DENIED"}}) }
		if keys == nil {
			fail(503)
			return
		}
		if c.Request.RequestURI != assetCatalogAuthorityPath || c.Request.URL.RawPath != "" || c.Request.URL.RawQuery != "" || c.Request.URL.ForceQuery {
			fail(400)
			return
		}
		raw, e := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
		var in assetCatalogAuthorityRequest
		if e != nil || strictIdentityJSON(raw, &in, 1<<20) != nil {
			fail(400)
			return
		}
		headers := c.Request.Header.Values("Authorization")
		if len(headers) != 1 || !strings.HasPrefix(headers[0], "Bearer ") {
			fail(401)
			return
		}
		claims, e := verifyServiceAssertion(keys, "asset.operation.check", strings.TrimPrefix(headers[0], "Bearer "), "POST", assetCatalogAuthorityPath, raw, p.Service.now())
		if e != nil || claims.TenantID != in.TenantID || claims.WorkspaceID != in.WorkspaceID {
			fail(401)
			return
		}
		if e = p.checkCurrent(c.Request.Context(), in); e != nil {
			fail(403)
			return
		}
		c.JSON(200, gin.H{"active": true, "authorization_version": in.AuthorizationVersion})
	})
}
