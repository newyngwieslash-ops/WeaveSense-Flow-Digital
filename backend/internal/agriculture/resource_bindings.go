package agriculture

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func canonicalBindingCapabilities(input []string) ([]string, error) {
	if len(input) == 0 || len(input) > 200 {
		return nil, ErrInvalid
	}
	seen := make(map[string]bool, len(input))
	out := append([]string(nil), input...)
	for _, capability := range out {
		if !knownAction(capability) || strings.HasPrefix(capability, "agri.billing.") || seen[capability] {
			return nil, ErrInvalid
		}
		seen[capability] = true
	}
	sort.Strings(out)
	return out, nil
}

var ErrPreconditionRequired = errors.New("agriculture: expected revision required")

func bindingExpectedRevision(input *int64, allowAbsent bool) (int64, error) {
	if input == nil {
		return 0, ErrPreconditionRequired
	}
	value := *input
	if value < 0 || value > maximumAuthorizationVersion || (!allowAbsent && value == 0) {
		return 0, ErrInvalid
	}
	return value, nil
}

func (s Service) GrantResourceBinding(ctx context.Context, actorID, workspaceID, targetUserID string, resource ResourceRef, capabilities []string, expiresAt *time.Time, expectedRevision *int64) (model.AgricultureResourceBinding, error) {
	var out model.AgricultureResourceBinding
	expected, err := bindingExpectedRevision(expectedRevision, true)
	if err != nil {
		return out, err
	}
	caps, err := canonicalBindingCapabilities(capabilities)
	if err != nil || targetUserID == "" {
		return out, ErrInvalid
	}
	// An actor without any grant-management role must not use the ownership
	// resolver as an existence oracle. Resource-specific authority is still
	// rechecked after resolution in the final transaction.
	if err := s.withWorkspace(ctx, actorID, workspaceID, false, func(_ *gorm.DB, scope workspaceContext) error {
		if !scope.Owner && !hasRole(scope.Roles, "tenant_admin") && !hasRole(scope.Roles, "farm_manager") {
			return ErrForbidden
		}
		return nil
	}); err != nil {
		return out, err
	}
	// Copy caller-owned memory before crossing the external ownership boundary.
	var expiry *time.Time
	if expiresAt != nil {
		value := expiresAt.UTC()
		expiry = &value
	}
	err = s.withOwnership(ctx, actorID, workspaceID, []ResourceRef{resource}, func(tx *gorm.DB, scope workspaceContext, version int64, proofs map[ResourceRef]ResourceOwnership) error {
		now := s.now()
		if expiry != nil && (!expiry.After(now) || expiry.After(now.Add(366*24*time.Hour))) {
			return ErrInvalid
		}
		if !scope.Owner && !hasRole(scope.Roles, "tenant_admin") {
			if !hasRole(scope.Roles, "farm_manager") {
				return ErrForbidden
			}
			if _, err := authorizeOwned(tx, scope, actorID, "agri.authorization.update", proofs, now); err != nil {
				return err
			}
			// A scoped manager cannot delegate a capability they do not possess.
			for _, capability := range caps {
				if _, err := authorizeDelegationOwned(tx, scope, actorID, capability, proofs, now); err != nil {
					return err
				}
			}
			if err := checkDelegationExpiry(tx, scope, actorID, caps, proofs, expiry, now); err != nil {
				return err
			}
		}
		target, err := resolve(tx, targetUserID, workspaceID, false)
		if err != nil {
			return err
		}
		for _, capability := range caps {
			if !roleAllowsAction(target.Roles, capability) {
				return ErrForbidden
			}
		}
		proof, ok := proofs[resource]
		if !ok {
			return ErrDependencyUnavailable
		}
		digest, err := ownershipBindingHash(proof)
		if err != nil {
			return err
		}
		encodedCaps, err := json.Marshal(caps)
		if err != nil {
			return err
		}
		query := tx.Where("tenant_id=? AND workspace_id=? AND resource_kind=? AND resource_id=? AND subject_type='user' AND subject_id=?", scope.Tenant.ID, workspaceID, resource.Kind, resource.ID, targetUserID)
		err = query.Clauses(clause.Locking{Strength: "UPDATE"}).First(&out).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		isNew := errors.Is(err, gorm.ErrRecordNotFound)
		if (isNew && expected != 0) || (!isNew && out.AuthVersion != expected) {
			return ErrConflict
		}
		if isNew {
			out = model.AgricultureResourceBinding{ID: flow.NewID("arb_"), TenantID: scope.Tenant.ID, WorkspaceID: workspaceID, ResourceKind: resource.Kind, ResourceID: resource.ID, SubjectType: "user", SubjectID: targetUserID, CreatedAt: now}
		}
		out.Capabilities, out.AuthVersion, out.State, out.ExpiresAt = string(encodedCaps), version+1, "active", expiry
		out.ReceiptHash, out.ReceiptRevision = digest, proof.Revision
		out.ConfirmedAt, out.UpdatedAt = now, now
		if isNew {
			err = tx.Create(&out).Error
		} else {
			err = tx.Model(&model.AgricultureResourceBinding{}).Where("id=? AND tenant_id=? AND workspace_id=?", out.ID, scope.Tenant.ID, workspaceID).Updates(map[string]any{"capabilities": out.Capabilities, "auth_version": out.AuthVersion, "state": out.State, "expires_at": out.ExpiresAt, "receipt_hash": out.ReceiptHash, "receipt_revision": out.ReceiptRevision, "confirmed_at": out.ConfirmedAt, "updated_at": now}).Error
		}
		if err != nil {
			return err
		}
		return audit(tx, now, scope, actorID, "resource_binding.granted", out.ID, map[string]any{"subject_id": targetUserID, "resource": resource, "capabilities": caps, "auth_version": out.AuthVersion})
	}, targetUserID)
	if err != nil {
		return model.AgricultureResourceBinding{}, err
	}
	return out, nil
}

// checkDelegationExpiry deliberately intersects all relevant supporting grants,
// even when an alternative authority path could last longer. A manager cannot
// turn temporary authority into a permanent ACL. These are audited ACL changes,
// not cascading delegation chains: later departure does not silently rewrite
// historical grants; revocation remains a separate administrative operation.
func checkDelegationExpiry(tx *gorm.DB, scope workspaceContext, actorID string, capabilities []string, proofs map[ResourceRef]ResourceOwnership, expiry *time.Time, now time.Time) error {
	needed := map[string]bool{"agri.authorization.update": true}
	for _, capability := range capabilities {
		needed[capability] = true
	}
	relevant := map[ResourceRef]bool{}
	for ref, proof := range proofs {
		relevant[ref] = true
		for _, farm := range proof.FarmIDs {
			relevant[ResourceRef{Kind: "farm", ID: farm}] = true
		}
	}
	var rows []model.AgricultureResourceBinding
	if err := tx.Where("tenant_id=? AND workspace_id=? AND subject_type='user' AND subject_id=? AND state='active' AND (expires_at IS NULL OR expires_at>?)", scope.Tenant.ID, scope.Workspace.ID, actorID, now).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		ref := ResourceRef{Kind: row.ResourceKind, ID: row.ResourceID}
		if !relevant[ref] || row.ExpiresAt == nil {
			continue
		}
		if ref.Kind != "farm" && row.ReceiptRevision != proofs[ref].Revision {
			continue
		}
		caps, err := bindingCapabilities(row)
		if err != nil {
			return err
		}
		for _, capability := range caps {
			if needed[capability] && (expiry == nil || expiry.After(*row.ExpiresAt)) {
				return ErrForbidden
			}
		}
	}
	return nil
}

func (s Service) ListBindings(ctx context.Context, actorID, workspaceID string) ([]model.AgricultureResourceBinding, error) {
	out := []model.AgricultureResourceBinding{}
	err := s.withWorkspace(ctx, actorID, workspaceID, true, func(tx *gorm.DB, scope workspaceContext) error {
		if !scope.Owner && !hasRole(scope.Roles, "tenant_admin") {
			return ErrForbidden
		}
		return tx.Where("tenant_id=? AND workspace_id=?", scope.Tenant.ID, workspaceID).Order("created_at,id").Find(&out).Error
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func bindingInWorkspace(tx *gorm.DB, scope workspaceContext, bindingID string) (model.AgricultureResourceBinding, error) {
	var row model.AgricultureResourceBinding
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND tenant_id=? AND workspace_id=?", bindingID, scope.Tenant.ID, scope.Workspace.ID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, ErrNotFound
	}
	return row, err
}

func revokeBinding(tx *gorm.DB, scope workspaceContext, actorID string, row model.AgricultureResourceBinding, version int64, now time.Time) error {
	if row.State == "revoked" {
		return nil
	}
	if err := tx.Model(&model.AgricultureResourceBinding{}).Where("id=? AND tenant_id=? AND workspace_id=?", row.ID, scope.Tenant.ID, scope.Workspace.ID).Updates(map[string]any{"state": "revoked", "capabilities": "[]", "auth_version": version + 1, "updated_at": now}).Error; err != nil {
		return err
	}
	return audit(tx, now, scope, actorID, "resource_binding.revoked", row.ID, map[string]any{"auth_version": version + 1})
}

func (s Service) RevokeBinding(ctx context.Context, actorID, workspaceID, bindingID string, expectedRevision *int64) error {
	expected, err := bindingExpectedRevision(expectedRevision, false)
	if err != nil {
		return err
	}
	var initial model.AgricultureResourceBinding
	manager := false
	err = s.withWorkspace(ctx, actorID, workspaceID, true, func(tx *gorm.DB, scope workspaceContext) error {
		if !scope.Owner && !hasRole(scope.Roles, "tenant_admin") && !hasRole(scope.Roles, "farm_manager") {
			return ErrForbidden
		}
		var err error
		initial, err = bindingInWorkspace(tx, scope, bindingID)
		if err != nil {
			return err
		}
		if scope.Owner || hasRole(scope.Roles, "tenant_admin") {
			if initial.AuthVersion != expected {
				return ErrConflict
			}
			// Revoking authority must not depend on GIS availability.
			version, err := currentAuthorizationVersion(tx, scope.Tenant.ID)
			if err != nil {
				return err
			}
			return revokeBinding(tx, scope, actorID, initial, version, s.now())
		}
		manager = true
		return nil
	})
	if err != nil || !manager {
		return err
	}
	resource := ResourceRef{Kind: initial.ResourceKind, ID: initial.ResourceID}
	return s.withOwnership(ctx, actorID, workspaceID, []ResourceRef{resource}, func(tx *gorm.DB, scope workspaceContext, version int64, proofs map[ResourceRef]ResourceOwnership) error {
		if !hasRole(scope.Roles, "farm_manager") {
			return ErrForbidden
		}
		row, err := bindingInWorkspace(tx, scope, bindingID)
		if err != nil {
			return err
		}
		if _, err := authorizeOwned(tx, scope, actorID, "agri.authorization.update", proofs, s.now()); err != nil {
			return err
		}
		if row.AuthVersion != expected || row.AuthVersion != initial.AuthVersion || row.ResourceKind != resource.Kind || row.ResourceID != resource.ID {
			return ErrConflict
		}
		return revokeBinding(tx, scope, actorID, row, version, s.now())
	})
}
