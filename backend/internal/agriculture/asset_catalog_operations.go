package agriculture

import (
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

func (p *AssetCatalogPlatform) validatePlan(tx *gorm.DB, s workspaceContext, payload AssetCatalogPayload) error {
	for _, g := range payload.InitialGrants {
		target, e := resolve(tx, g.SubjectUserID, s.Workspace.ID, false)
		if e != nil {
			return e
		}
		if g.ExpiresAt != nil && (!g.ExpiresAt.After(p.Service.now()) || g.ExpiresAt.After(p.Service.now().Add(366*24*time.Hour))) {
			return ErrInvalid
		}
		for _, cap := range g.Capabilities {
			if !roleAllowsAction(target.Roles, cap) {
				return ErrForbidden
			}
		}
	}
	return nil
}
func (p *AssetCatalogPlatform) prepare(ctx context.Context, actor, ws string, payload AssetCatalogPayload) (string, int64, error) {
	farm, e := validateAssetCatalogPayload(&payload)
	if e != nil {
		return "", 0, e
	}
	if payload.Action == "version" {
		d, e := p.Authorize(ctx, actor, ws, "agri.asset.create", []string{payload.AssetID})
		if e != nil {
			return "", 0, e
		}
		if len(d.ResourceVersions) != 1 || d.ResourceVersions[0].Revision != *payload.ExpectedRevision {
			return "", 0, ErrConflict
		}
		if (farm == "" && len(d.FarmIDs) != 0) || (farm != "" && (len(d.FarmIDs) != 1 || d.FarmIDs[0] != farm)) {
			return "", 0, ErrConflict
		}
		return farm, d.AuthorizationVersion, nil
	}
	var version int64
	if farm == "" {
		e = p.Service.withWorkspacePrincipals(ctx, actor, ws, false, assetCatalogGrantees(payload), func(tx *gorm.DB, s workspaceContext) error {
			if !s.Owner && !hasRole(s.Roles, "tenant_admin") {
				return ErrForbidden
			}
			if e := p.validatePlan(tx, s, payload); e != nil {
				return e
			}
			var e error
			version, e = currentAuthorizationVersion(tx, s.Tenant.ID)
			return e
		})
		return farm, version, e
	}
	e = p.Service.withOwnership(ctx, actor, ws, []ResourceRef{{"farm", farm}}, func(tx *gorm.DB, s workspaceContext, v int64, proofs map[ResourceRef]ResourceOwnership) error {
		if !roleAllowsAction(s.Roles, "agri.asset.create") {
			return ErrForbidden
		}
		if _, e := authorizeOwned(tx, s, actor, "agri.asset.create", proofs, p.Service.now()); e != nil {
			return e
		}
		if e := p.validatePlan(tx, s, payload); e != nil {
			return e
		}
		if !s.Owner && !hasRole(s.Roles, "tenant_admin") {
			if !hasRole(s.Roles, "farm_manager") {
				return ErrForbidden
			}
			if _, e := authorizeOwned(tx, s, actor, "agri.authorization.update", proofs, p.Service.now()); e != nil {
				return e
			}
			for _, g := range payload.InitialGrants {
				for _, cap := range g.Capabilities {
					if _, e := authorizeDelegationOwned(tx, s, actor, cap, proofs, p.Service.now()); e != nil {
						return e
					}
				}
				if e := checkDelegationExpiry(tx, s, actor, g.Capabilities, proofs, g.ExpiresAt, p.Service.now()); e != nil {
					return e
				}
			}
		}
		version = v
		return nil
	}, assetCatalogGrantees(payload)...)
	return farm, version, e
}
func assetCatalogGrantees(v AssetCatalogPayload) []string {
	ids := []string{}
	for _, g := range v.InitialGrants {
		ids = append(ids, g.SubjectUserID)
	}
	return ids
}
func (p *AssetCatalogPlatform) loadOperation(ctx context.Context, actor, ws, id string) (model.AgricultureAssetOperation, error) {
	var op model.AgricultureAssetOperation
	e := p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		e := tx.Where("id=? AND tenant_id=? AND workspace_id=? AND actor_id=?", id, s.Tenant.ID, ws, actor).First(&op).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return e
	})
	return op, e
}
func (p *AssetCatalogPlatform) Operation(ctx context.Context, actor, ws, id string) (AssetCatalogOperationView, error) {
	op, e := p.loadOperation(ctx, actor, ws, id)
	return assetCatalogView(op), e
}
func (p *AssetCatalogPlatform) Preview(ctx context.Context, actor, ws string, payload AssetCatalogPayload) (AssetCatalogPreview, error) {
	var empty AssetCatalogPreview
	farm, e := validateAssetCatalogPayload(&payload)
	if e != nil {
		return empty, e
	}
	_, version, e := p.prepare(ctx, actor, ws, payload)
	if e != nil {
		return empty, e
	}
	raw, hash, e := farmCanonical(payload)
	if e != nil {
		return empty, e
	}
	_, grantHash, e := farmCanonical(map[string]any{"initial_grants": payload.InitialGrants})
	if e != nil {
		return empty, e
	}
	now := p.Service.now()
	op := model.AgricultureAssetOperation{ID: flow.NewID("aop_"), ActorID: actor, WorkspaceID: ws, Action: payload.Action, AssetID: payload.AssetID, FarmID: farm, ExpectedRevision: *payload.ExpectedRevision, RequestHash: hash, GrantPlanHash: grantHash, Payload: string(raw), Summary: "{}", State: "preview", AuthorizationVersion: version, ExpiresAt: now.Add(5 * time.Minute), CreatedAt: now, UpdatedAt: now}
	e = p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		v, e := currentAuthorizationVersion(tx, s.Tenant.ID)
		if e != nil {
			return e
		}
		if v != version {
			return ErrConflict
		}
		op.TenantID = s.Tenant.ID
		return tx.Create(&op).Error
	})
	if e != nil {
		return empty, e
	}
	// A persisted, actor-bound intent supports the GIS's nonrecursive current
	// callback. Invalid previews are deleted only while still unsubmitted.
	accepted := false
	defer func() {
		if !accepted {
			_ = p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
				return tx.Where("id=? AND tenant_id=? AND workspace_id=? AND actor_id=? AND state='preview' AND command_id=''", op.ID, s.Tenant.ID, ws, actor).Delete(&model.AgricultureAssetOperation{}).Error
			})
		}
	}()
	body, e := assetCatalogBody(op, false)
	if e != nil {
		return empty, e
	}
	reply, e := p.workspaceCall(ctx, actor, ws, "asset.preview."+payload.Action, op.ID, hash, grantHash, assetCatalogOpIDs(op), body)
	if e != nil {
		return empty, e
	}
	var summary struct {
		Summary json.RawMessage `json:"summary"`
	}
	if strictFarmJSON(reply, &summary, 1<<20) != nil {
		return empty, ErrDependencyUnavailable
	}
	if _, e = parcelDecodeMap(summary.Summary); e != nil {
		return empty, e
	}
	e = p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		v, e := currentAuthorizationVersion(tx, s.Tenant.ID)
		if e != nil {
			return e
		}
		if v != version {
			return ErrConflict
		}
		return tx.Model(&model.AgricultureAssetOperation{}).Where("id=? AND tenant_id=? AND workspace_id=? AND actor_id=? AND state='preview' AND summary='{}'", op.ID, s.Tenant.ID, ws, actor).Update("summary", string(summary.Summary)).Error
	})
	if e != nil {
		return empty, e
	}
	accepted = true
	grants := payload.InitialGrants
	if grants == nil {
		grants = []FarmGrant{}
	}
	return AssetCatalogPreview{op.ID, op.ExpiresAt, hash, payload.Action, payload.AssetID, *payload.ExpectedRevision, summary.Summary, grants}, nil
}

// An already-approved version intent may lose its exact old ownership receipt
// solely because another writer committed. Only an unchanged current grant and
// epoch allow reporting that deterministic CAS conflict; revoked authority does
// not gain a metadata read or a permission refresh through this path.
func(p *AssetCatalogPlatform) staleIntentConflict(ctx context.Context,op model.AgricultureAssetOperation)bool{
 if op.Action!="version"{return false};if e:=p.Service.withWorkspace(ctx,op.ActorID,op.WorkspaceID,false,func(tx *gorm.DB,s workspaceContext)error{return p.currentIntent(tx,s,op)});e!=nil{return false};facts,e:=p.Client.ResolveAssetCatalog(ctx,OwnershipContext{op.TenantID,op.WorkspaceID},[]string{op.AssetID});return e==nil&&len(facts)==1&&facts[0].Revision>op.ExpectedRevision
}
func assetCatalogOpIDs(op model.AgricultureAssetOperation) []string {
	if op.Action == "version" {
		return []string{op.AssetID}
	}
	return []string{}
}
func (p *AssetCatalogPlatform) Confirm(ctx context.Context, actor, ws, id, key string, payload AssetCatalogPayload) (AssetCatalogOperationView, error) {
	var empty AssetCatalogOperationView
	if len(key) < 1 || len(key) > 200 || strings.TrimSpace(key) != key {
		return empty, ErrInvalid
	}
	if _, e := validateAssetCatalogPayload(&payload); e != nil {
		return empty, e
	}
	_, hash, e := farmCanonical(payload)
	if e != nil {
		return empty, e
	}
	op, e := p.loadOperation(ctx, actor, ws, id)
	if e != nil {
		return empty, e
	}
	if op.RequestHash != hash || op.Action != payload.Action {
		return empty, ErrConflict
	}
	_, idem, e := farmCanonical(map[string]any{"parts": []string{op.TenantID, ws, actor, payload.Action, key}})
	if e != nil {
		return empty, e
	}
	if op.IdempotencyHash != nil {
		if *op.IdempotencyHash != idem {
			return empty, ErrConflict
		}
		if op.State == "pending" {
			_, _ = p.Dispatcher.ForTenant(op.TenantID).DispatchOne(ctx, op.CommandID)
		}
		return p.Operation(ctx, actor, ws, id)
	}
	_, version, e := p.prepare(ctx, actor, ws, payload)
	if e != nil {
		if errors.Is(e,ErrForbidden)&&p.staleIntentConflict(ctx,op){return empty,ErrConflict}
		return empty, e
	}
	e = p.Service.withWorkspacePrincipals(ctx, actor, ws, false, assetCatalogGrantees(payload), func(tx *gorm.DB, s workspaceContext) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND tenant_id=? AND workspace_id=? AND actor_id=?", id, s.Tenant.ID, ws, actor).First(&op).Error; e != nil {
			return ErrNotFound
		}
		if op.IdempotencyHash != nil {
			if *op.IdempotencyHash != idem {
				return ErrConflict
			}
			return nil
		}
		if op.State != "preview" || op.Summary == "{}" || !op.ExpiresAt.After(p.Service.now()) || op.AuthorizationVersion != version {
			return ErrConflict
		}
		v, e := currentAuthorizationVersion(tx, s.Tenant.ID)
		if e != nil {
			return e
		}
		if v != version {
			return ErrConflict
		}
		var duplicates int64
		if e = tx.Model(&model.AgricultureAssetOperation{}).Where("idempotency_hash=? AND id<>?", idem, id).Count(&duplicates).Error; e != nil {
			return e
		}
		if duplicates != 0 {
			return ErrConflict
		}
		encoded, _, _ := farmCanonical(map[string]string{"operation_id": op.ID})
		d := p.Dispatcher
		d.DB = tx
		row, _, e := d.Enqueue(ctx, flow.CommandInput{Scope: flow.Scope{TenantID: op.TenantID, WorkspaceID: ws}, Actor: flow.Actor{Type: "user", ID: actor}, Command: "agri.asset." + op.Action, Method: "POST", Path: assetCatalogPaths["asset."+op.Action], IdempotencyKey: "asset:" + idem, Payload: encoded})
		if e != nil {
			return e
		}
		op.CommandID = row.ID
		op.IdempotencyHash = &idem
		op.State = "pending"
		op.UpdatedAt = p.Service.now()
		return tx.Save(&op).Error
	})
	if e != nil {
		return empty, e
	}
	if op.State == "pending" {
		_, _ = p.Dispatcher.ForTenant(op.TenantID).DispatchOne(ctx, op.CommandID)
	}
	return p.Operation(ctx, actor, ws, id)
}
