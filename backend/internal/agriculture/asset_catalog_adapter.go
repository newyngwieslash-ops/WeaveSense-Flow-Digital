package agriculture

import (
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

type assetCatalogVerifiedReceipt struct {
	Receipt AssetCatalogReceipt
	Proofs  []ResourceOwnership
	Epoch   int64
}

func (p *AssetCatalogPlatform) receipt(ctx context.Context, op model.AgricultureAssetOperation) ([]byte, error) {
	body, e := assetCatalogBody(op, true)
	if e != nil {
		return nil, e
	}
	return p.workspaceCall(ctx, op.ActorID, op.WorkspaceID, "asset.receipt", op.ID, op.RequestHash, op.GrantPlanHash, []string{}, body)
}
func (p *AssetCatalogPlatform) Execute(ctx context.Context, row model.FlowCommandOutbox) (flow.CommandOutcome, error) {
	unknown := flow.CommandOutcome{State: flow.CommandUnknown}
	id, e := farmCommandOperation(row)
	if e != nil {
		return unknown, e
	}
	op, e := p.loadOperation(ctx, row.ActorID, row.WorkspaceID, id)
	if e != nil {
		return flow.CommandOutcome{State: flow.CommandReconciliation}, nil
	}
	if op.CommandID != row.ID || op.TenantID != row.TenantID || op.State != "pending" || row.Command != "agri.asset."+op.Action {
		return flow.CommandOutcome{State: flow.CommandReconciliation}, nil
	}
	raw, e := p.receipt(ctx, op)
	absent := errors.Is(e, ErrNotFound)
	if absent {
		payload, decodeErr := assetCatalogPayload(op)
		if decodeErr != nil {
			return unknown, decodeErr
		}
		_, epoch, prepareErr := p.prepare(ctx, op.ActorID, op.WorkspaceID, payload)
		e = prepareErr
		if errors.Is(e, ErrForbidden) && p.staleIntentConflict(ctx, op) {
			e = ErrConflict
		}
		if e == nil && epoch != op.AuthorizationVersion {
			e = ErrConflict
		}
		if e == nil {
			body, bodyErr := assetCatalogBody(op, false)
			if bodyErr != nil {
				return unknown, bodyErr
			}
			raw, e = p.workspaceCall(ctx, op.ActorID, op.WorkspaceID, "asset."+op.Action, op.ID, op.RequestHash, op.GrantPlanHash, assetCatalogOpIDs(op), body)
		}
	}
	if absent && errors.Is(e, ErrConflict) {
		recovered, recoveryErr := p.receipt(ctx, op)
		if errors.Is(recoveryErr, ErrNotFound) {
			return flow.CommandOutcome{State: flow.CommandRejected, HTTPStatus: 409}, nil
		}
		if recoveryErr != nil {
			return flow.CommandOutcome{State: flow.CommandReconciliation}, nil
		}
		raw, e = recovered, nil
	}
	if e != nil {
		if errors.Is(e, ErrForbidden) || errors.Is(e, ErrConflict) {
			return flow.CommandOutcome{State: flow.CommandReconciliation}, nil
		}
		if errors.Is(e, ErrInvalid) || errors.Is(e, ErrFarmGeometry) {
			return flow.CommandOutcome{State: flow.CommandRejected, HTTPStatus: 422}, nil
		}
		return unknown, e
	}
	var receipt AssetCatalogReceipt
	if strictFarmJSON(raw, &receipt, 1<<20) != nil {
		return unknown, ErrDependencyUnavailable
	}
	fields, e := parcelDecodeMap(raw)
	if e != nil {
		return unknown, e
	}
	delete(fields, "payload_hash")
	_, hash, e := farmCanonical(fields)
	event := "agri.asset.registered"
	if op.Action == "version" {
		event = "agri.asset.version_registered"
	}
	if e != nil || receipt.PayloadHash != hash || receipt.SchemaVersion != "1.0" || receipt.Producer != "agri-gis" || receipt.TenantID != op.TenantID || receipt.WorkspaceID != op.WorkspaceID || receipt.OperationID != op.ID || receipt.RequestHash != op.RequestHash || receipt.GrantPlanHash != op.GrantPlanHash || receipt.State != "applied" || receipt.Revision != op.ExpectedRevision+1 || receipt.AggregateRevision != receipt.Revision || !assetCatalogID.MatchString(receipt.AssetID) || !assetCatalogVersionID.MatchString(receipt.AssetVersionID) || !ownershipID.MatchString(receipt.EventID) || receipt.EventType != event || (op.Action == "version" && receipt.AssetID != op.AssetID) {
		return unknown, ErrDependencyUnavailable
	}
	proofs, e := p.Client.ResolveAssetCatalog(ctx, OwnershipContext{op.TenantID, op.WorkspaceID}, []string{receipt.AssetID})
	if e != nil {
		return unknown, e
	}
	if len(proofs) != 1 || proofs[0].Revision != receipt.Revision || proofs[0].Status != "validating" {
		return flow.CommandOutcome{State: flow.CommandReconciliation}, nil
	}
	if (op.FarmID == "" && len(proofs[0].FarmIDs) != 0) || (op.FarmID != "" && (len(proofs[0].FarmIDs) != 1 || proofs[0].FarmIDs[0] != op.FarmID)) {
		return unknown, ErrDependencyUnavailable
	}
	var epoch int64
	e = p.Service.withWorkspace(ctx, op.ActorID, op.WorkspaceID, false, func(tx *gorm.DB, s workspaceContext) error {
		if e := p.currentIntent(tx, s, op); e != nil {
			return e
		}
		var e error
		epoch, e = currentAuthorizationVersion(tx, s.Tenant.ID)
		return e
	})
	if e != nil {
		return flow.CommandOutcome{State: flow.CommandReconciliation}, nil
	}
	result := assetCatalogView(op)
	result.State = "completed"
	result.AssetID = receipt.AssetID
	result.Revision = receipt.Revision
	result.AssetVersionID = receipt.AssetVersionID
	body, _ := json.Marshal(result)
	return flow.CommandOutcome{State: flow.CommandCompleted, HTTPStatus: 200, ResponseBody: body, Receipt: assetCatalogVerifiedReceipt{receipt, proofs, epoch}}, nil
}
func (p *AssetCatalogPlatform) ApplyReceipt(ctx context.Context, tx *gorm.DB, row model.FlowCommandOutbox, out flow.CommandOutcome) error {
	id, e := farmCommandOperation(row)
	if e != nil {
		return e
	}
	var op model.AgricultureAssetOperation
	if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND tenant_id=? AND workspace_id=? AND actor_id=? AND command_id=?", id, row.TenantID, row.WorkspaceID, row.ActorID, row.ID).First(&op).Error; e != nil {
		return e
	}
	if op.State == "completed" {
		return nil
	}
	if op.State != "pending" {
		return ErrConflict
	}
	state, code := "pending", ""
	switch out.State {
	case flow.CommandRejected:
		state, code = "rejected", "INVALID_ARGUMENT"
		if out.HTTPStatus == 409 {
			code = "STATE_CONFLICT"
		}
	case flow.CommandReconciliation:
		state, code = "reconciliation_required", "RECONCILIATION_REQUIRED"
	case flow.CommandCompleted:
		verified, ok := out.Receipt.(assetCatalogVerifiedReceipt)
		if !ok {
			return ErrDependencyUnavailable
		}
		r := verified.Receipt
		s, e := resolve(tx, op.ActorID, op.WorkspaceID, false)
		if e != nil {
			return e
		}
		if e = p.currentIntent(tx, s, op); e != nil {
			return e
		}
		version, e := currentAuthorizationVersion(tx, s.Tenant.ID)
		if e != nil {
			return e
		}
		if version != verified.Epoch {
			return ErrConflict
		}
		proofs, e := validateAssetCatalogOwnership(p.Service.now(), OwnershipContext{op.TenantID, op.WorkspaceID}, []ResourceRef{{"asset", r.AssetID}}, verified.Proofs)
		if e != nil {
			return e
		}
		var previous int64
		if e = tx.Model(&model.AgricultureAssetEvent{}).Where("producer=? AND event_id=?", r.Producer, r.EventID).Count(&previous).Error; e != nil {
			return e
		}
		if previous != 0 {
			return ErrConflict
		}
		if op.Action == "create" {
			payload, e := assetCatalogPayload(op)
			if e != nil {
				return e
			}
			for _, g := range payload.InitialGrants {
				if _, e = p.Service.grantOwnedBinding(tx, s, op.ActorID, g.SubjectUserID, ResourceRef{"asset", r.AssetID}, g.Capabilities, g.ExpiresAt, 0, version, proofs); e != nil {
					return e
				}
				version, e = currentAuthorizationVersion(tx, s.Tenant.ID)
				if e != nil {
					return e
				}
			}
		}
		if e = tx.Create(&model.AgricultureAssetEvent{Producer: r.Producer, EventID: r.EventID, TenantID: op.TenantID, WorkspaceID: op.WorkspaceID, OperationID: op.ID, PayloadHash: r.PayloadHash, AssetID: r.AssetID, AssetVersionID: r.AssetVersionID, Revision: r.Revision, CreatedAt: p.Service.now()}).Error; e != nil {
			return e
		}
		op.AssetID = r.AssetID
		op.AssetVersionID = r.AssetVersionID
		op.ResultRevision = r.Revision
		state = "completed"
	}
	op.State = state
	op.ErrorCode = code
	op.UpdatedAt = p.Service.now()
	return tx.Save(&op).Error
}
