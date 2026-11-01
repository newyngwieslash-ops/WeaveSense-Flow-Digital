package agriculture

import (
	"context"
	"gorm.io/gorm"
	"sort"
	"time"
	"yingce/backend/internal/model"
)

func assetCatalogResources(ids []string) ([]ResourceRef, error) {
	if len(ids) < 1 || len(ids) > 200 {
		return nil, ErrInvalid
	}
	refs := make([]ResourceRef, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if !assetCatalogID.MatchString(id) || seen[id] {
			return nil, ErrInvalid
		}
		seen[id] = true
		refs = append(refs, ResourceRef{"asset", id})
	}
	return canonicalResources(refs)
}

// This profile, and only this profile, admits a real no-farm catalog object.
// The ordinary ownership validator and its active/nonempty-farm rule stay intact.
func validateAssetCatalogOwnership(now time.Time, scope OwnershipContext, refs []ResourceRef, proofs []ResourceOwnership) (map[ResourceRef]ResourceOwnership, error) {
	if len(refs) == 0 || len(refs) != len(proofs) || len(refs) > 200 {
		return nil, ErrDependencyUnavailable
	}
	requested := map[ResourceRef]bool{}
	for _, r := range refs {
		if r.Kind != "asset" || !assetCatalogID.MatchString(r.ID) || requested[r] {
			return nil, ErrInvalid
		}
		requested[r] = true
	}
	out := map[ResourceRef]ResourceOwnership{}
	for _, p := range proofs {
		if !requested[p.Resource] || out[p.Resource].Resource.ID != "" {
			return nil, ErrDependencyUnavailable
		}
		if p.TenantID != scope.TenantID || p.WorkspaceID != scope.WorkspaceID || p.Status == "revoked" {
			return nil, ErrNotFound
		}
		if p.Status != "validating" && p.Status != "ready" && p.Status != "quarantined" && p.Status != "rejected" {
			return nil, ErrDependencyUnavailable
		}
		if p.Revision < 1 || p.Revision > maxOwnershipRevision || p.FarmIDs == nil || len(p.FarmIDs) > 1 || p.AssignedActorID != "" || !ownershipID.MatchString(p.ReceiptID) || p.IssuedAt.IsZero() || p.ExpiresAt.IsZero() || p.IssuedAt.After(now.Add(5*time.Second)) || p.IssuedAt.Before(now.Add(-60*time.Second)) || !p.ExpiresAt.After(now) || !p.ExpiresAt.After(p.IssuedAt) || p.ExpiresAt.Sub(p.IssuedAt) > 60*time.Second {
			return nil, ErrDependencyUnavailable
		}
		if len(p.FarmIDs) == 1 && !ownershipID.MatchString(p.FarmIDs[0]) {
			return nil, ErrDependencyUnavailable
		}
		out[p.Resource] = p
	}
	return out, nil
}
func (g *GISClient) ResolveAssetCatalog(ctx context.Context, scope OwnershipContext, ids []string) ([]ResourceOwnership, error) {
	if g == nil {
		return nil, ErrDependencyUnavailable
	}
	refs, e := assetCatalogResources(ids)
	if e != nil {
		return nil, e
	}
	ordered := []string{}
	for _, r := range refs {
		ordered = append(ordered, r.ID)
	}
	body, _, e := farmCanonical(map[string]any{"tenant_id": scope.TenantID, "workspace_id": scope.WorkspaceID, "asset_ids": ordered})
	if e != nil {
		return nil, e
	}
	token, canonical, e := issueServiceAssertion(g.keys, "asset.ownership.read", scope, body, g.now())
	if e != nil {
		return nil, ErrDependencyUnavailable
	}
	raw, e := g.send(ctx, assetCatalogOwnershipPath, canonical, token)
	if e != nil {
		return nil, e
	}
	var result struct {
		Resources []ownershipWire `json:"resources"`
	}
	if strictIdentityJSON(raw, &result, 1<<20) != nil {
		return nil, ErrDependencyUnavailable
	}
	proofs := []ResourceOwnership{}
	for _, w := range result.Resources {
		issued, e := identityTime(w.IssuedAt)
		if e != nil {
			return nil, ErrDependencyUnavailable
		}
		expires, e := identityTime(w.ExpiresAt)
		if e != nil {
			return nil, ErrDependencyUnavailable
		}
		proofs = append(proofs, ResourceOwnership{Resource: w.Resource, TenantID: w.TenantID, WorkspaceID: w.WorkspaceID, FarmIDs: w.FarmIDs, Revision: w.Revision, Status: w.Status, ReceiptID: w.ReceiptID, IssuedAt: issued, ExpiresAt: expires, AssignedActorID: w.AssignedActorID})
	}
	_, e = validateAssetCatalogOwnership(g.now(), scope, refs, proofs)
	return proofs, e
}
func (p *AssetCatalogPlatform) withOwnership(ctx context.Context, actor, ws string, ids []string, fn func(*gorm.DB, workspaceContext, int64, map[ResourceRef]ResourceOwnership) error, otherActors ...string) error {
	refs, e := assetCatalogResources(ids)
	if e != nil {
		return e
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
		return e
	}
	proofCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	proofs, e := p.Client.ResolveAssetCatalog(proofCtx, OwnershipContext{tenant, ws}, ids)
	if e != nil {
		return e
	}
	if proofCtx.Err() != nil {
		return ErrDependencyUnavailable
	}
	return p.Service.withWorkspacePrincipals(ctx, actor, ws, false, otherActors, func(tx *gorm.DB, s workspaceContext) error {
		if s.Tenant.ID != tenant {
			return ErrNotFound
		}
		current, e := currentAuthorizationVersion(tx, tenant)
		if e != nil {
			return e
		}
		if current != epoch {
			return ErrConflict
		}
		verified, e := validateAssetCatalogOwnership(p.Service.now(), OwnershipContext{tenant, ws}, refs, proofs)
		if e != nil {
			return e
		}
		return fn(tx, s, current, verified)
	})
}
func (p *AssetCatalogPlatform) Authorize(ctx context.Context, actor, ws, action string, ids []string) (AuthorizationDecision, error) {
	var out AuthorizationDecision
	if action != "agri.asset.read" && action != "agri.asset.create" && action != "agri.asset_download.read" {
		return out, ErrInvalid
	}
	if _, e := assetCatalogResources(ids); e != nil {
		return out, e
	}
	preflight := p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		if !roleAllowsAction(s.Roles, action) {
			return ErrForbidden
		}
		for _, id := range ids {
			var b model.AgricultureResourceBinding
			if tx.Where("tenant_id=? AND workspace_id=? AND subject_type='user' AND subject_id=? AND resource_kind='asset' AND resource_id=? AND state='active' AND (expires_at IS NULL OR expires_at>?)", s.Tenant.ID, ws, actor, id, p.Service.now()).First(&b).Error != nil {
				return ErrNotFound
			}
			caps, e := bindingCapabilities(b)
			if e != nil {
				return e
			}
			found := false
			for _, cap := range caps {
				found = found || cap == action
			}
			if !found {
				return ErrForbidden
			}
		}
		return nil
	})
	if preflight != nil {
		return out, preflight
	}
	e := p.withOwnership(ctx, actor, ws, ids, func(tx *gorm.DB, s workspaceContext, epoch int64, proofs map[ResourceRef]ResourceOwnership) error {
		now := p.Service.now()
		if !roleAllowsAction(s.Roles, action) {
			return ErrForbidden
		}
		farms, e := authorizeOwned(tx, s, actor, action, proofs, now)
		if e != nil {
			return e
		}
		expires := now.Add(30 * time.Second)
		refs := []ResourceRef{}
		for r, proof := range proofs {
			refs = append(refs, r)
			if proof.ExpiresAt.Before(expires) {
				expires = proof.ExpiresAt
			}
			if action == "agri.asset_download.read" && proof.Status != "ready" {
				return ErrNotFound
			}
			if e := assetCatalogExactGrant(tx, s, actor, r.ID, action, proof, now); e != nil {
				return e
			}
		}
		sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
		expires, e = authorizationExpiry(tx, s, actor, action, proofs, now, expires)
		if e != nil {
			return e
		}
		versions := []ResourceVersion{}
		for _, r := range refs {
			versions = append(versions, ResourceVersion{Resource: r, Revision: proofs[r].Revision})
		}
		out = AuthorizationDecision{ActorID: actor, TenantID: s.Tenant.ID, WorkspaceID: ws, Action: action, Resources: refs, ResourceVersions: versions, FarmIDs: farms, AuthorizationVersion: epoch, PolicyVersion: authorizationPolicyVersion, IssuedAt: now, ExpiresAt: expires}
		return nil
	})
	return out, e
}
func (p *AssetCatalogPlatform) AuthorizeAssetDownload(ctx context.Context, actor, workspace, assetID string) (AuthorizationDecision, error) {
	return p.Authorize(ctx, actor, workspace, "agri.asset_download.read", []string{assetID})
}
func assetCatalogExactGrant(tx *gorm.DB, s workspaceContext, actor, id, action string, proof ResourceOwnership, now time.Time) error {
	var b model.AgricultureResourceBinding
	if tx.Where("tenant_id=? AND workspace_id=? AND resource_kind='asset' AND resource_id=? AND subject_type='user' AND subject_id=? AND state='active' AND (expires_at IS NULL OR expires_at>?)", s.Tenant.ID, s.Workspace.ID, id, actor, now).First(&b).Error != nil {
		return ErrNotFound
	}
	hash, e := ownershipBindingHash(proof)
	if e != nil {
		return e
	}
	if b.ReceiptRevision != proof.Revision || b.ReceiptHash != hash {
		return ErrForbidden
	}
	caps, e := bindingCapabilities(b)
	if e != nil {
		return e
	}
	for _, cap := range caps {
		if cap == action {
			return nil
		}
	}
	return ErrForbidden
}

// Only the existing binding-management entry points call this adapter. Ordinary
// business delegation retains its original active/nonempty-farm validator.
func (s Service) withBindingOwnership(ctx context.Context, actor, ws string, resources []ResourceRef, fn func(*gorm.DB, workspaceContext, int64, map[ResourceRef]ResourceOwnership) error, otherActors ...string) error {
	catalog := false
	for _, r := range resources {
		if r.Kind == "asset" && assetCatalogID.MatchString(r.ID) {
			catalog = true
		}
	}
	if !catalog {
		return s.withOwnership(ctx, actor, ws, resources, fn, otherActors...)
	}
	if len(resources) != 1 || resources[0].Kind != "asset" || !assetCatalogID.MatchString(resources[0].ID) {
		return ErrInvalid
	}
	client, ok := s.Ownership.(*GISClient)
	if !ok || client == nil {
		return ErrDependencyUnavailable
	}
	p := AssetCatalogPlatform{Service: s, Client: client}
	return p.withOwnership(ctx, actor, ws, []string{resources[0].ID}, fn, otherActors...)
}
