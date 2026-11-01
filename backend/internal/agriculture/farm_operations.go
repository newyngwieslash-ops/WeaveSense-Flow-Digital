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

type FarmPreview struct {
	PreviewID        string          `json:"preview_id"`
	ExpiresAt        time.Time       `json:"expires_at"`
	RequestHash      string          `json:"request_hash"`
	Action           string          `json:"action"`
	FarmID           string          `json:"farm_id"`
	ExpectedRevision int64           `json:"expected_revision"`
	Summary          json.RawMessage `json:"summary"`
}

func (p *FarmPlatform) Preview(ctx context.Context, actor, ws string, payload FarmPayload) (result FarmPreview, resultErr error) {
	if e := validateFarmPayload(&payload); e != nil {
		return result, e
	}
	raw, hash, e := farmCanonical(payload)
	if e != nil {
		return result, e
	}
	grantHash := zeroFarmHash
	if payload.Action == "create" {
		_, grantHash, e = farmCanonical(map[string]any{"initial_grants": payload.InitialGrants})
		if e != nil {
			return result, e
		}
	}
	now := p.Service.now()
	op := model.AgricultureFarmOperation{ID: flow.NewID("fop_"), WorkspaceID: ws, ActorID: actor, Action: payload.Action, FarmID: payload.FarmID, ExpectedRevision: payload.ExpectedRevision, RequestHash: hash, GrantPlanHash: grantHash, Payload: string(raw), Summary: "{}", State: "preview", ExpiresAt: now.Add(300 * time.Second), CreatedAt: now, UpdatedAt: now}
	var decision AuthorizationDecision
	if payload.Action != "create" {
		decision, e = p.Service.Authorize(ctx, actor, ws, "agri.farm."+payload.Action, []ResourceRef{{Kind: "farm", ID: payload.FarmID}})
		if e != nil {
			return result, e
		}
		if len(decision.ResourceVersions) != 1 || decision.ResourceVersions[0].Revision != payload.ExpectedRevision {
			return result, ErrConflict
		}
	}
	e = p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		op.TenantID = s.Tenant.ID
		var err error
		op.AuthorizationVersion, err = currentAuthorizationVersion(tx, s.Tenant.ID)
		if err != nil {
			return err
		}
		if payload.Action == "create" {
			if err = p.validateCreateAuthority(tx, s, op); err != nil {
				return err
			}
		}
		return tx.Create(&op).Error
	})
	if e != nil {
		return result, e
	}
	var response []byte
	previewAccepted := false
	defer func() {
		if !previewAccepted {
			// Only the newly-created, never-confirmed intent can be removed.
			// GIS previews do not mutate facts; failed validation must not leave
			// a confirmable intent or a business side effect.
			cleanupErr := flow.TenantTx(p.Service.DB.WithContext(context.WithoutCancel(ctx)), op.TenantID, func(tx *gorm.DB) error {
				return tx.Where("id=? AND tenant_id=? AND actor_id=? AND state='preview' AND command_id=''", op.ID, op.TenantID, actor).Delete(&model.AgricultureFarmOperation{}).Error
			})
			if cleanupErr != nil {
				result = FarmPreview{}
				resultErr = ErrDependencyUnavailable
			}
		}
	}()
	if payload.Action == "create" {
		body, _ := farmCommandBody(op, false)
		response, e = p.workspaceCall(ctx, actor, ws, "farm.preview.create", op.ID, hash, grantHash, "/internal/agri/v1/farms/preview-create", []string{}, body)
	} else {
		body := map[string]any{"expected_revision": payload.ExpectedRevision}
		if payload.Action == "update" {
			body["farm"] = payload.Farm
		} else {
			body["reason"] = payload.Reason
		}
		encoded, _, _ := farmCanonical(body)
		response, e = p.Client.farmAuthorized(ctx, p.Service, actor, decision, "POST", "/internal/agri/v1/farms/"+payload.FarmID+"/preview-"+payload.Action, encoded, op.ID)
	}
	if e != nil {
		return result, e
	}
	var summary struct {
		Summary json.RawMessage `json:"summary"`
	}
	if strictFarmJSON(response, &summary, 1<<20) != nil || len(summary.Summary) == 0 {
		return result, ErrDependencyUnavailable
	}
	// Display exact validated GIS facts and every explicit grant; the immutable
	// request hash is additional binding, never a substitute for a visible preview.
	full, _, e := farmCanonical(map[string]any{"farm": summary.Summary, "initial_grants": payload.InitialGrants, "action": payload.Action, "reason": payload.Reason})
	if e != nil {
		return result, e
	}
	e = p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		return tx.Model(&model.AgricultureFarmOperation{}).Where("id=? AND actor_id=? AND state='preview'", op.ID, actor).Update("summary", string(full)).Error
	})
	if e != nil {
		return result, e
	}
	previewAccepted = true
	return FarmPreview{op.ID, op.ExpiresAt, hash, payload.Action, payload.FarmID, payload.ExpectedRevision, full}, nil
}

func (p *FarmPlatform) Operation(ctx context.Context, actor, ws, id string) (FarmOperationView, error) {
	var op model.AgricultureFarmOperation
	e := p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		err := tx.Where("id=? AND tenant_id=? AND workspace_id=? AND actor_id=?", id, s.Tenant.ID, ws, actor).First(&op).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	})
	return farmOperationView(op), e
}

func (p *FarmPlatform) Confirm(ctx context.Context, actor, ws, id, key string, payload FarmPayload) (FarmOperationView, error) {
	if len(key) < 1 || len(key) > 200 || strings.TrimSpace(key) != key {
		return FarmOperationView{}, ErrInvalid
	}
	if e := validateFarmPayload(&payload); e != nil {
		return FarmOperationView{}, e
	}
	_, hash, e := farmCanonical(payload)
	if e != nil {
		return FarmOperationView{}, e
	}
	// A replay reads the same durable operation, not the now-stale farm
	// revision. In particular archiving must not make its own receipt
	// unrecoverable through the active-only ownership resolver.
	var existing model.AgricultureFarmOperation
	e = p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		if err := tx.Where("id=? AND tenant_id=? AND workspace_id=? AND actor_id=?", id, s.Tenant.ID, ws, actor).First(&existing).Error; err != nil {
			return ErrNotFound
		}
		return nil
	})
	if e != nil {
		return FarmOperationView{}, e
	}
	if existing.IdempotencyHash != nil {
		_, idem, _ := farmCanonical(map[string]any{"parts": []string{existing.TenantID, ws, actor, payload.Action, key}})
		if *existing.IdempotencyHash != idem || existing.RequestHash != hash || existing.Action != payload.Action {
			return FarmOperationView{}, ErrConflict
		}
		if existing.State == "pending" {
			_, _ = p.Dispatcher.ForTenant(existing.TenantID).DispatchOne(ctx, existing.CommandID)
		}
		return p.Operation(ctx, actor, ws, id)
	}
	var decision AuthorizationDecision
	if payload.Action != "create" {
		decision, e = p.Service.Authorize(ctx, actor, ws, "agri.farm."+payload.Action, []ResourceRef{{Kind: "farm", ID: payload.FarmID}})
		if e != nil {
			return FarmOperationView{}, e
		}
	}
	var op model.AgricultureFarmOperation
	e = p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND tenant_id=? AND workspace_id=? AND actor_id=?", id, s.Tenant.ID, ws, actor).First(&op).Error; err != nil {
			return ErrNotFound
		}
		if op.RequestHash != hash || op.Action != payload.Action {
			return ErrConflict
		}
		_, idem, _ := farmCanonical(map[string]any{"parts": []string{s.Tenant.ID, ws, actor, payload.Action, key}})
		var duplicate int64
		if err := tx.Model(&model.AgricultureFarmOperation{}).Where("idempotency_hash=? AND id<>?", idem, op.ID).Count(&duplicate).Error; err != nil {
			return err
		}
		if duplicate != 0 {
			return ErrConflict
		}
		if op.IdempotencyHash != nil {
			if *op.IdempotencyHash != idem {
				return ErrConflict
			}
			return nil
		}
		if op.State != "preview" || !op.ExpiresAt.After(p.Service.now()) || op.Summary == "{}" {
			return ErrConflict
		}
		version, err := currentAuthorizationVersion(tx, s.Tenant.ID)
		if err != nil {
			return err
		}
		if payload.Action == "create" {
			if err = p.validateCreateAuthority(tx, s, op); err != nil {
				return err
			}
		} else if version != decision.AuthorizationVersion || len(decision.ResourceVersions) != 1 || decision.ResourceVersions[0].Revision != op.ExpectedRevision {
			return ErrConflict
		}
		op.AuthorizationVersion = version
		body, _, _ := farmCanonical(map[string]string{"operation_id": op.ID})
		dispatcher := p.Dispatcher
		dispatcher.DB = tx
		row, _, err := dispatcher.Enqueue(ctx, flow.CommandInput{Scope: flow.Scope{TenantID: s.Tenant.ID, WorkspaceID: ws}, Actor: flow.Actor{Type: "user", ID: actor}, Command: "agri.farm." + op.Action, Method: "POST", Path: "/internal/agri/v1/farms/commands", IdempotencyKey: "farm:" + idem, Payload: body})
		if err != nil {
			return err
		}
		op.CommandID = row.ID
		op.IdempotencyHash = &idem
		op.State = "pending"
		op.UpdatedAt = p.Service.now()
		return tx.Save(&op).Error
	})
	if e != nil {
		return FarmOperationView{}, e
	}
	if op.State == "pending" {
		_, _ = p.Dispatcher.ForTenant(op.TenantID).DispatchOne(ctx, op.CommandID)
	}
	return p.Operation(ctx, actor, ws, id)
}
