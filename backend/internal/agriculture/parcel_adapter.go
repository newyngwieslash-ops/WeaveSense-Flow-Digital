package agriculture

import (
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"regexp"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

var parcelHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type verifiedParcelReceipt struct{ Receipt ParcelReceipt }

func (p *ParcelPlatform) Execute(ctx context.Context, row model.FlowCommandOutbox) (flow.CommandOutcome, error) {
	unknown := flow.CommandOutcome{State: flow.CommandUnknown}
	id, e := farmCommandOperation(row)
	if e != nil {
		return unknown, e
	}
	op, e := p.loadOperation(ctx, row.ActorID, row.WorkspaceID, id)
	if e != nil {
		return flow.CommandOutcome{State: flow.CommandReconciliation}, nil
	}
	if op.TenantID != row.TenantID || op.CommandID != row.ID || row.Command != "agri.parcel."+op.Action || op.State != "pending" {
		return flow.CommandOutcome{State: flow.CommandReconciliation}, nil
	}
	raw, e := p.receiptCall(ctx, op)
	absent := errors.Is(e, ErrNotFound)
	if absent {
		payload, decodeErr := parcelPayload(op)
		if decodeErr != nil {
			return unknown, decodeErr
		}
		decision, authErr := p.authorizePayload(ctx, op.ActorID, op.WorkspaceID, payload)
		e = authErr
		if e == nil && decision.AuthorizationVersion != op.AuthorizationVersion {
			e = ErrConflict
		}
		if e == nil {
			body, bodyErr := parcelBody(op, false)
			if bodyErr != nil {
				return unknown, bodyErr
			}
			raw, e = p.Client.farmAuthorized(ctx, p.Service, op.ActorID, decision, "POST", parcelPath(op, false), body, op.ID)
		}
	}
	if absent && errors.Is(e, ErrConflict) {
		recovered, recoveryErr := p.receiptCall(ctx, op)
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
	var receipt ParcelReceipt
	if strictFarmJSON(raw, &receipt, 1<<20) != nil {
		return unknown, ErrDependencyUnavailable
	}
	fields, e := parcelDecodeMap(raw)
	if e != nil {
		return unknown, e
	}
	delete(fields, "payload_hash")
	_, digest, e := farmCanonical(fields)
	kind := "agri.parcel.created"
	if op.Action == "revise" {
		kind = "agri.parcel.revised"
	}
	if e != nil || receipt.PayloadHash != digest || receipt.SchemaVersion != "1.0" || receipt.Producer != "agri-gis" || receipt.State != "applied" || receipt.OperationID != op.ID || receipt.TenantID != op.TenantID || receipt.WorkspaceID != op.WorkspaceID || receipt.RequestHash != op.RequestHash || receipt.FarmID != op.FarmID || !parcelIDPattern.MatchString(receipt.ParcelID) || receipt.EventType != kind || !ownershipID.MatchString(receipt.EventID) || !parcelHashPattern.MatchString(receipt.GeometryHash) || receipt.GeometryRevision != op.ExpectedRevision+1 || receipt.AggregateRevision != receipt.GeometryRevision || receipt.PreviousGeometryRevision != op.ExpectedRevision {
		return unknown, ErrDependencyUnavailable
	}
	if op.Action == "create" {
		if receipt.PreviousGeometryHash != "" {
			return unknown, ErrDependencyUnavailable
		}
	} else if receipt.ParcelID != op.ParcelID || !parcelHashPattern.MatchString(receipt.PreviousGeometryHash) {
		return unknown, ErrDependencyUnavailable
	}
	e = p.Service.withWorkspace(ctx, op.ActorID, op.WorkspaceID, false, func(tx *gorm.DB, s workspaceContext) error { return p.operationAuthority(tx, s, op) })
	if e != nil {
		return flow.CommandOutcome{State: flow.CommandReconciliation}, nil
	}
	view := parcelView(op)
	view.State = "completed"
	view.ParcelID = receipt.ParcelID
	view.GeometryRevision = receipt.GeometryRevision
	body, _ := json.Marshal(view)
	return flow.CommandOutcome{State: flow.CommandCompleted, HTTPStatus: 200, ResponseBody: body, Receipt: verifiedParcelReceipt{receipt}}, nil
}

func (p *ParcelPlatform) ApplyReceipt(ctx context.Context, tx *gorm.DB, row model.FlowCommandOutbox, out flow.CommandOutcome) error {
	id, e := farmCommandOperation(row)
	if e != nil {
		return e
	}
	var op model.AgricultureParcelOperation
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
		state = "rejected"
		code = "INVALID_ARGUMENT"
		if out.HTTPStatus == 409 {
			code = "STATE_CONFLICT"
		}
	case flow.CommandReconciliation:
		state = "reconciliation_required"
		code = "RECONCILIATION_REQUIRED"
	case flow.CommandCompleted:
		verified, ok := out.Receipt.(verifiedParcelReceipt)
		if !ok {
			return ErrDependencyUnavailable
		}
		receipt := verified.Receipt
		scope, e := resolve(tx, op.ActorID, op.WorkspaceID, false)
		if e != nil {
			return e
		}
		if e = p.operationAuthority(tx, scope, op); e != nil {
			return e
		}
		var prior int64
		if e = tx.Model(&model.AgricultureParcelEvent{}).Where("producer=? AND event_id=?", receipt.Producer, receipt.EventID).Count(&prior).Error; e != nil {
			return e
		}
		if prior != 0 {
			return ErrConflict
		}
		reference := model.AgricultureParcelReference{TenantID: op.TenantID, WorkspaceID: op.WorkspaceID, ParcelID: receipt.ParcelID, FarmID: op.FarmID, CreatedAt: p.Service.now()}
		if e = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&reference).Error; e != nil {
			return e
		}
		var current model.AgricultureParcelReference
		if e = tx.Where("tenant_id=? AND workspace_id=? AND parcel_id=?", op.TenantID, op.WorkspaceID, receipt.ParcelID).First(&current).Error; e != nil {
			return e
		}
		if current.FarmID != op.FarmID {
			return ErrConflict
		}
		if e = tx.Create(&model.AgricultureParcelEvent{Producer: receipt.Producer, EventID: receipt.EventID, TenantID: op.TenantID, WorkspaceID: op.WorkspaceID, OperationID: op.ID, PayloadHash: receipt.PayloadHash, FarmID: op.FarmID, ParcelID: receipt.ParcelID, GeometryRevision: receipt.GeometryRevision, CreatedAt: p.Service.now()}).Error; e != nil {
			return e
		}
		op.ParcelID = receipt.ParcelID
		op.ResultRevision = receipt.GeometryRevision
		state = "completed"
	}
	op.State = state
	op.ErrorCode = code
	op.UpdatedAt = p.Service.now()
	return tx.Save(&op).Error
}
