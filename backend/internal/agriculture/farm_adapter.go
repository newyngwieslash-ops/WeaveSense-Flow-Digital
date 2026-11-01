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

type farmVerifiedReceipt struct {
	Receipt FarmReceipt
	Proofs  []ResourceOwnership
	Version int64
}

func farmCommandOperation(row model.FlowCommandOutbox) (string, error) {
	var in struct {
		OperationID string `json:"operation_id"`
	}
	if strictFarmJSON([]byte(row.Payload), &in, 1024) != nil || !ownershipID.MatchString(in.OperationID) {
		return "", ErrInvalid
	}
	return in.OperationID, nil
}
func (p *FarmPlatform) Execute(ctx context.Context, row model.FlowCommandOutbox) (flow.CommandOutcome, error) {
	unknown := flow.CommandOutcome{State: flow.CommandUnknown}
	id, e := farmCommandOperation(row)
	if e != nil {
		return unknown, e
	}
	var op model.AgricultureFarmOperation
	e = p.Service.withWorkspace(ctx, row.ActorID, row.WorkspaceID, false, func(tx *gorm.DB, s workspaceContext) error {
		if s.Tenant.ID != row.TenantID {
			return ErrForbidden
		}
		return tx.Where("id=? AND command_id=? AND actor_id=? AND tenant_id=?", id, row.ID, row.ActorID, row.TenantID).First(&op).Error
	})
	if e != nil {
		return flow.CommandOutcome{State: flow.CommandReconciliation}, nil
	}
	if row.Command != "agri.farm."+op.Action || op.State != "pending" {
		return flow.CommandOutcome{State: flow.CommandReconciliation}, nil
	}
	// Always query durable GIS receipt before retrying a possibly applied write.
	body, _ := farmCommandBody(op, true)
	raw, e := p.workspaceCall(ctx, op.ActorID, op.WorkspaceID, "farm.receipt", op.ID, op.RequestHash, op.GrantPlanHash, "/internal/agri/v1/farms/commands/receipt", []string{}, body)
	confirmedAbsent := errors.Is(e, ErrNotFound)
	if errors.Is(e, ErrNotFound) {
		body, _ = farmCommandBody(op, false)
		if op.Action == "create" {
			raw, e = p.workspaceCall(ctx, op.ActorID, op.WorkspaceID, "farm.create", op.ID, op.RequestHash, op.GrantPlanHash, "/internal/agri/v1/farms/commands/create", []string{}, body)
		} else {
			var d AuthorizationDecision
			d, e = p.Service.Authorize(ctx, op.ActorID, op.WorkspaceID, "agri.farm."+op.Action, []ResourceRef{{Kind: "farm", ID: op.FarmID}})
			if e == nil && (len(d.ResourceVersions) != 1 || d.ResourceVersions[0].Revision != op.ExpectedRevision) {
				e = ErrConflict
			}
			if e == nil {
				method, path := "PATCH", "/internal/agri/v1/farms/"+op.FarmID
				if op.Action == "archive" {
					method = "POST"
					path += "/archive"
				}
				raw, e = p.Client.farmAuthorized(ctx, p.Service, op.ActorID, d, method, path, body, op.ID)
			}
		}
	}
	if confirmedAbsent && errors.Is(e, ErrConflict) {
		// A concurrent same-operation worker may have committed between the
		// initial lookup and this conflict. Reconcile once more; only proven
		// absence is a deterministic rejected write.
		receiptBody, _ := farmCommandBody(op, true)
		recovered, recoveryErr := p.workspaceCall(ctx, op.ActorID, op.WorkspaceID, "farm.receipt", op.ID, op.RequestHash, op.GrantPlanHash, "/internal/agri/v1/farms/commands/receipt", []string{}, receiptBody)
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
		if errors.Is(e, ErrFarmGeometry) || errors.Is(e, ErrInvalid) {
			return flow.CommandOutcome{State: flow.CommandRejected, HTTPStatus: 422}, nil
		}
		return unknown, e
	}
	var receipt FarmReceipt
	if strictFarmJSON(raw, &receipt, 1<<20) != nil {
		return unknown, ErrDependencyUnavailable
	}
	var fields map[string]any
	if json.Unmarshal(raw, &fields) != nil {
		return unknown, ErrDependencyUnavailable
	}
	delete(fields, "payload_hash")
	_, digest, e := farmCanonical(fields)
	event := map[string]string{"create": "agri.farm.created", "update": "agri.farm.updated", "archive": "agri.farm.archived"}[op.Action]
	if e != nil || receipt.PayloadHash != digest || receipt.SchemaVersion != "1.0" || receipt.Producer != "agri-gis" || receipt.State != "applied" || receipt.OperationID != op.ID || receipt.TenantID != op.TenantID || receipt.WorkspaceID != op.WorkspaceID || receipt.RequestHash != op.RequestHash || receipt.GrantPlanHash != op.GrantPlanHash || receipt.EventType != event || !ownershipID.MatchString(receipt.EventID) || !ownershipID.MatchString(receipt.FarmID) || receipt.Revision < 1 || receipt.AggregateRevision != receipt.Revision {
		return unknown, ErrDependencyUnavailable
	}
	if (op.Action == "create" && receipt.Revision != 1) || (op.Action != "create" && (receipt.FarmID != op.FarmID || receipt.Revision != op.ExpectedRevision+1)) {
		return unknown, ErrDependencyUnavailable
	}
	verified := farmVerifiedReceipt{Receipt: receipt}
	if op.Action == "create" {
		verified.Proofs, e = p.Client.Resolve(ctx, OwnershipContext{op.TenantID, op.WorkspaceID}, []ResourceRef{{Kind: "farm", ID: receipt.FarmID}})
		if e != nil {
			return unknown, e
		}
		if len(verified.Proofs) != 1 || verified.Proofs[0].Revision != receipt.Revision {
			return unknown, ErrConflict
		}
	}
	e = p.Service.withWorkspace(ctx, op.ActorID, op.WorkspaceID, false, func(tx *gorm.DB, s workspaceContext) error {
		var err error
		verified.Version, err = currentAuthorizationVersion(tx, s.Tenant.ID)
		if err != nil {
			return err
		}
		if op.Action == "create" {
			return p.validateCreateAuthority(tx, s, op)
		}
		return p.checkFarmWriteGrant(tx, s, op)
	})
	if e != nil {
		return flow.CommandOutcome{State: flow.CommandReconciliation}, nil
	}
	view := farmOperationView(op)
	view.State = "completed"
	view.FarmID = receipt.FarmID
	view.Revision = receipt.Revision
	safe, _ := json.Marshal(view)
	return flow.CommandOutcome{State: flow.CommandCompleted, HTTPStatus: 200, ResponseBody: safe, Receipt: verified}, nil
}

func (p *FarmPlatform) checkFarmWriteGrant(tx *gorm.DB, s workspaceContext, op model.AgricultureFarmOperation) error {
	if !roleAllowsAction(s.Roles, "agri.farm."+op.Action) {
		return ErrForbidden
	}
	var b model.AgricultureResourceBinding
	if tx.Where("tenant_id=? AND workspace_id=? AND resource_kind='farm' AND resource_id=? AND subject_type='user' AND subject_id=? AND state='active' AND (expires_at IS NULL OR expires_at>?)", s.Tenant.ID, s.Workspace.ID, op.FarmID, op.ActorID, p.Service.now()).First(&b).Error != nil {
		return ErrForbidden
	}
	caps, e := bindingCapabilities(b)
	if e != nil {
		return e
	}
	for _, cap := range caps {
		if cap == "agri.farm."+op.Action {
			return nil
		}
	}
	return ErrForbidden
}

func (p *FarmPlatform) ApplyReceipt(ctx context.Context, tx *gorm.DB, row model.FlowCommandOutbox, out flow.CommandOutcome) error {
	id, e := farmCommandOperation(row)
	if e != nil {
		return e
	}
	var op model.AgricultureFarmOperation
	if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND command_id=? AND tenant_id=? AND workspace_id=? AND actor_id=?", id, row.ID, row.TenantID, row.WorkspaceID, row.ActorID).First(&op).Error; e != nil {
		return e
	}
	if op.State == "completed" {
		return nil
	}
	if op.State != "pending" {
		return ErrConflict
	}
	state := "pending"
	code := ""
	switch out.State {
	case flow.CommandReconciliation:
		state = "reconciliation_required"
		code = "RECONCILIATION_REQUIRED"
	case flow.CommandRejected:
		state = "rejected"
		code = "INVALID_ARGUMENT"
		if out.HTTPStatus == 409 {
			code = "STATE_CONFLICT"
		}
	case flow.CommandCompleted:
		verified, ok := out.Receipt.(farmVerifiedReceipt)
		if !ok {
			return ErrDependencyUnavailable
		}
		r := verified.Receipt
		scope, e := resolve(tx, op.ActorID, op.WorkspaceID, false)
		if e != nil {
			return e
		}
		version, e := currentAuthorizationVersion(tx, scope.Tenant.ID)
		if e != nil {
			return e
		}
		if version != verified.Version {
			return ErrConflict
		}
		var previous model.AgricultureFarmEvent
		lookup := tx.Where("producer=? AND event_id=?", r.Producer, r.EventID).First(&previous).Error
		if lookup == nil {
			if previous.OperationID != op.ID || previous.PayloadHash != r.PayloadHash {
				return ErrConflict
			}
			return ErrConflict
		}
		if !errors.Is(lookup, gorm.ErrRecordNotFound) {
			return lookup
		}
		if op.Action == "create" {
			if e = p.validateCreateAuthority(tx, scope, op); e != nil {
				return e
			}
			proofs, e := validateOwnership(p.Service.now(), OwnershipContext{op.TenantID, op.WorkspaceID}, []ResourceRef{{Kind: "farm", ID: r.FarmID}}, verified.Proofs)
			if e != nil {
				return e
			}
			payload, _ := farmPayload(op)
			for _, grant := range payload.InitialGrants {
				// Initial registration is insert-only. Existing/revoked bindings are never
				// reactivated by delayed or duplicate GIS events.
				if _, e = p.Service.grantOwnedBinding(tx, scope, op.ActorID, grant.SubjectUserID, ResourceRef{Kind: "farm", ID: r.FarmID}, grant.Capabilities, grant.ExpiresAt, 0, version, proofs); e != nil {
					return e
				}
				version, e = currentAuthorizationVersion(tx, scope.Tenant.ID)
				if e != nil {
					return e
				}
			}
		} else if e = p.checkFarmWriteGrant(tx, scope, op); e != nil {
			return e
		}
		if e = tx.Create(&model.AgricultureFarmEvent{Producer: r.Producer, EventID: r.EventID, TenantID: op.TenantID, WorkspaceID: op.WorkspaceID, OperationID: op.ID, PayloadHash: r.PayloadHash, FarmID: r.FarmID, Revision: r.Revision, CreatedAt: p.Service.now()}).Error; e != nil {
			return e
		}
		op.FarmID = r.FarmID
		op.ResultRevision = r.Revision
		state = "completed"
	}
	op.State = state
	op.ErrorCode = code
	op.UpdatedAt = p.Service.now()
	return tx.Save(&op).Error
}
