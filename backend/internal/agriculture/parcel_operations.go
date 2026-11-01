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

func (p *ParcelPlatform) authorizePayload(ctx context.Context, actor, ws string, payload ParcelPayload) (AuthorizationDecision, error) {
	var empty AuthorizationDecision
	if e := validateParcelPayload(payload); e != nil {
		return empty, e
	}
	action, ref := parcelResource(payload)
	decision, e := p.Service.Authorize(ctx, actor, ws, action, []ResourceRef{ref})
	if e != nil {
		return empty, e
	}
	expected := *payload.ExpectedRevision
	if payload.Action == "create" {
		expected = payload.ExpectedFarmRevision
	}
	if len(decision.FarmIDs) != 1 || len(decision.ResourceVersions) != 1 || decision.ResourceVersions[0].Resource != ref || decision.ResourceVersions[0].Revision != expected || (payload.Action == "create" && decision.FarmIDs[0] != payload.FarmID) {
		return empty, ErrConflict
	}
	return decision, nil
}

func (p *ParcelPlatform) Preview(ctx context.Context, actor, ws string, payload ParcelPayload) (ParcelPreview, error) {
	var empty ParcelPreview
	decision, e := p.authorizePayload(ctx, actor, ws, payload)
	if e != nil {
		return empty, e
	}
	raw, hash, e := farmCanonical(payload)
	if e != nil {
		return empty, e
	}
	now := p.Service.now()
	op := model.AgricultureParcelOperation{ID: flow.NewID("pop_"), TenantID: decision.TenantID, WorkspaceID: ws, ActorID: actor, Action: payload.Action, FarmID: decision.FarmIDs[0], ParcelID: payload.ParcelID, ExpectedRevision: *payload.ExpectedRevision, ExpectedFarmRevision: payload.ExpectedFarmRevision, RequestHash: hash, Payload: string(raw), Summary: "{}", State: "preview", AuthorizationVersion: decision.AuthorizationVersion, ExpiresAt: now.Add(300 * time.Second), CreatedAt: now, UpdatedAt: now}
	body, e := parcelBody(op, true)
	if e != nil {
		return empty, e
	}
	response, e := p.Client.farmAuthorized(ctx, p.Service, actor, decision, "POST", parcelPath(op, true), body, op.ID)
	if e != nil {
		return empty, e
	}
	var preview struct {
		Summary json.RawMessage `json:"summary"`
	}
	if strictFarmJSON(response, &preview, 1<<20) != nil || len(preview.Summary) == 0 {
		return empty, ErrDependencyUnavailable
	}
	if _, e = parcelDecodeMap(preview.Summary); e != nil {
		return empty, e
	}
	// GIS previews are read-only; persist only after validation has succeeded,
	// and without retaining a platform transaction during external work.
	op.Summary = string(preview.Summary)
	e = p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		version, e := currentAuthorizationVersion(tx, s.Tenant.ID)
		if e != nil {
			return e
		}
		if version != decision.AuthorizationVersion || !decision.ExpiresAt.After(p.Service.now()) {
			return ErrConflict
		}
		return tx.Create(&op).Error
	})
	if e != nil {
		return empty, e
	}
	return ParcelPreview{op.ID, op.ExpiresAt, hash, op.Action, op.FarmID, op.ParcelID, op.ExpectedRevision, preview.Summary}, nil
}

func (p *ParcelPlatform) loadOperation(ctx context.Context, actor, ws, id string) (model.AgricultureParcelOperation, error) {
	var op model.AgricultureParcelOperation
	e := p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		err := tx.Where("id=? AND tenant_id=? AND workspace_id=? AND actor_id=?", id, s.Tenant.ID, ws, actor).First(&op).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	})
	return op, e
}
func (p *ParcelPlatform) Operation(ctx context.Context, actor, ws, id string) (ParcelOperationView, error) {
	op, e := p.loadOperation(ctx, actor, ws, id)
	return parcelView(op), e
}

func (p *ParcelPlatform) Confirm(ctx context.Context, actor, ws, id, key string, payload ParcelPayload) (ParcelOperationView, error) {
	var empty ParcelOperationView
	if len(key) < 1 || len(key) > 200 || strings.TrimSpace(key) != key {
		return empty, ErrInvalid
	}
	if e := validateParcelPayload(payload); e != nil {
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
	_, idem, e := farmCanonical(map[string]any{"parts": []string{op.TenantID, ws, actor, payload.Action, key}})
	if e != nil {
		return empty, e
	}
	if op.RequestHash != hash || op.Action != payload.Action {
		return empty, ErrConflict
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
	decision, e := p.authorizePayload(ctx, actor, ws, payload)
	if e != nil {
		return empty, e
	}
	e = p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND tenant_id=? AND workspace_id=? AND actor_id=?", id, s.Tenant.ID, ws, actor).First(&op).Error; e != nil {
			return ErrNotFound
		}
		if op.IdempotencyHash != nil {
			if *op.IdempotencyHash != idem || op.RequestHash != hash {
				return ErrConflict
			}
			return nil
		}
		if op.State != "preview" || !op.ExpiresAt.After(p.Service.now()) || op.RequestHash != hash || op.FarmID != decision.FarmIDs[0] {
			return ErrConflict
		}
		version, e := currentAuthorizationVersion(tx, s.Tenant.ID)
		if e != nil {
			return e
		}
		if version != decision.AuthorizationVersion || !decision.ExpiresAt.After(p.Service.now()) {
			return ErrConflict
		}
		var duplicates int64
		if e = tx.Model(&model.AgricultureParcelOperation{}).Where("idempotency_hash=? AND id<>?", idem, id).Count(&duplicates).Error; e != nil {
			return e
		}
		if duplicates != 0 {
			return ErrConflict
		}
		encoded, _, e := farmCanonical(map[string]string{"operation_id": op.ID})
		if e != nil {
			return e
		}
		dispatcher := p.Dispatcher
		dispatcher.DB = tx
		row, _, e := dispatcher.Enqueue(ctx, flow.CommandInput{Scope: flow.Scope{TenantID: op.TenantID, WorkspaceID: ws}, Actor: flow.Actor{Type: "user", ID: actor}, Command: "agri.parcel." + op.Action, Method: "POST", Path: parcelPath(op, false), IdempotencyKey: "parcel:" + idem, Payload: encoded})
		if e != nil {
			return e
		}
		op.IdempotencyHash = &idem
		op.CommandID = row.ID
		op.AuthorizationVersion = version
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
