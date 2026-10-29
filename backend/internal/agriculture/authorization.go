package agriculture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"yingce/backend/internal/model"
)

const authorizationPolicyVersion = "agriculture-policy/1.0.0"
const maximumAuthorizationVersion int64 = 9007199254740991

// A decision is an authorization snapshot, not a signed credential, approval,
// price reservation, or substitute for GIS domain validation at execution.
type AuthorizationDecision struct {
	ActorID              string            `json:"actorId"`
	TenantID             string            `json:"tenantId"`
	WorkspaceID          string            `json:"workspaceId"`
	Action               string            `json:"action"`
	Resources            []ResourceRef     `json:"resources"`
	ResourceVersions     []ResourceVersion `json:"resourceVersions"`
	FarmIDs              []string          `json:"farmIds"`
	AuthorizationVersion int64             `json:"authorizationVersion"`
	PolicyVersion        string            `json:"policyVersion"`
	IssuedAt             time.Time         `json:"issuedAt"`
	ExpiresAt            time.Time         `json:"expiresAt"`
}

type ResourceVersion struct {
	Resource ResourceRef `json:"resource"`
	Revision int64       `json:"revision"`
}

// Bind immutable ownership meaning, not the rotating receipt timestamp or ID.
// A direct asset grant must not silently expand to a different farm set even
// if a broken provider forgets to advance its revision.
func ownershipBindingHash(p ResourceOwnership) (string, error) {
	raw, err := json.Marshal(struct {
		Resource                ResourceRef
		TenantID, WorkspaceID   string
		FarmIDs                 []string
		Revision                int64
		Status, AssignedActorID string
	}{p.Resource, p.TenantID, p.WorkspaceID, p.FarmIDs, p.Revision, p.Status, p.AssignedActorID})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func authorizationExpiry(tx *gorm.DB, scope workspaceContext, actorID, action string, proofs map[ResourceRef]ResourceOwnership, now, expires time.Time) (time.Time, error) {
	bindings, err := effectiveBindings(tx, scope, actorID, proofs, now)
	if err != nil {
		return time.Time{}, err
	}
	for ref, proof := range proofs {
		_, allowed, until := resourceAccess(ref, proof, bindings, action, expires)
		if !allowed {
			return time.Time{}, ErrForbidden
		}
		if until.Before(expires) {
			expires = until
		}
	}
	return expires, nil
}

type effectiveGrant struct {
	Capabilities []string
	ExpiresAt    *time.Time
}

func effectiveBindings(tx *gorm.DB, scope workspaceContext, actorID string, proofs map[ResourceRef]ResourceOwnership, now time.Time) (map[ResourceRef]effectiveGrant, error) {
	var rows []model.AgricultureResourceBinding
	if err := tx.Where("tenant_id=? AND workspace_id=? AND subject_type='user' AND subject_id=? AND state='active' AND (expires_at IS NULL OR expires_at>?)", scope.Tenant.ID, scope.Workspace.ID, actorID, now).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[ResourceRef]effectiveGrant{}
	for _, row := range rows {
		ref := ResourceRef{row.ResourceKind, row.ResourceID}
		if ref.Kind != "farm" {
			p, ok := proofs[ref]
			if !ok || p.Revision != row.ReceiptRevision {
				continue
			}
			hash, err := ownershipBindingHash(p)
			if err != nil {
				return nil, err
			}
			if hash != row.ReceiptHash {
				continue
			}
		}
		caps, err := bindingCapabilities(row)
		if err != nil {
			return nil, err
		}
		out[ref] = effectiveGrant{caps, row.ExpiresAt}
	}
	return out, nil
}

// Positive grants compose by union. A narrower direct grant is not a deny
// override for a complete farm-level path. Choose the longest valid path, then
// the caller intersects its deadline with all other requested resources.
func resourceAccess(ref ResourceRef, proof ResourceOwnership, bindings map[ResourceRef]effectiveGrant, action string, base time.Time) (bool, bool, time.Time) {
	direct, hasDirect := bindings[ref]
	hasDirect = hasDirect && ref.Kind != "farm"
	directAllowed := hasDirect && hasRole(direct.Capabilities, action)
	directUntil := base
	if direct.ExpiresAt != nil && direct.ExpiresAt.Before(directUntil) {
		directUntil = *direct.ExpiresAt
	}
	farmVisible, farmAllowed := true, true
	farmUntil := base
	for _, farm := range proof.FarmIDs {
		binding, exists := bindings[ResourceRef{"farm", farm}]
		if !exists {
			farmVisible = false
			farmAllowed = false
			continue
		}
		if !hasRole(binding.Capabilities, action) {
			farmAllowed = false
		}
		if binding.ExpiresAt != nil && binding.ExpiresAt.Before(farmUntil) {
			farmUntil = *binding.ExpiresAt
		}
	}
	until := farmUntil
	if directAllowed && (!farmAllowed || directUntil.After(until)) {
		until = directUntil
	}
	return hasDirect || farmVisible, directAllowed || farmAllowed, until
}

func currentAuthorizationVersion(tx *gorm.DB, tenantID string) (int64, error) {
	var epoch model.AgricultureAuthorizationEpoch
	if err := tx.Where("tenant_id=?", tenantID).First(&epoch).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrDependencyUnavailable
		}
		return 0, err
	}
	if epoch.Version < 1 || epoch.Version > maximumAuthorizationVersion {
		return 0, ErrDependencyUnavailable
	}
	return epoch.Version, nil
}

func bumpAuthorizationVersion(tx *gorm.DB, tenantID string, now time.Time) error {
	if tenantID == "" {
		return ErrForbidden
	}
	row := model.AgricultureAuthorizationEpoch{TenantID: tenantID, Version: 1, UpdatedAt: now}
	result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}}, DoUpdates: clause.Assignments(map[string]any{"version": gorm.Expr("flow_agriculture_authorization_epochs.version + 1"), "updated_at": now}), Where: clause.Where{Exprs: []clause.Expression{clause.Lt{Column: clause.Column{Table: "flow_agriculture_authorization_epochs", Name: "version"}, Value: maximumAuthorizationVersion}}}}).Create(&row)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

// GIS network work is deliberately outside database transactions. The final
// transaction revalidates membership and epoch, so revocation during resolution
// cannot be hidden by an earlier successful authorization snapshot.
func (s Service) withOwnership(ctx context.Context, actorID, workspaceID string, resources []ResourceRef, fn func(*gorm.DB, workspaceContext, int64, map[ResourceRef]ResourceOwnership) error, otherActors ...string) error {
	refs, err := canonicalResources(resources)
	if err != nil {
		return err
	}
	var tenantID string
	var version int64
	err = s.withWorkspace(ctx, actorID, workspaceID, false, func(tx *gorm.DB, scope workspaceContext) error {
		tenantID = scope.Tenant.ID
		var e error
		version, e = currentAuthorizationVersion(tx, tenantID)
		return e
	})
	if err != nil {
		return err
	}
	if s.Ownership == nil {
		return ErrDependencyUnavailable
	}
	proofCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	proof, err := s.Ownership.Resolve(proofCtx, OwnershipContext{TenantID: tenantID, WorkspaceID: workspaceID}, append([]ResourceRef{}, refs...))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return ErrDependencyUnavailable
	}
	if proofCtx.Err() != nil {
		return ErrDependencyUnavailable
	}
	return s.withWorkspacePrincipals(ctx, actorID, workspaceID, false, otherActors, func(tx *gorm.DB, scope workspaceContext) error {
		if scope.Tenant.ID != tenantID {
			return ErrNotFound
		}
		current, e := currentAuthorizationVersion(tx, tenantID)
		if e != nil {
			return e
		}
		if current != version {
			return ErrConflict
		}
		verified, e := validateOwnership(s.now(), OwnershipContext{TenantID: tenantID, WorkspaceID: workspaceID}, refs, proof)
		if e != nil {
			return e
		}
		return fn(tx, scope, current, verified)
	})
}

func bindingCapabilities(binding model.AgricultureResourceBinding) ([]string, error) {
	var caps []string
	if json.Unmarshal([]byte(binding.Capabilities), &caps) != nil || len(caps) == 0 || len(caps) > 200 {
		return nil, ErrDependencyUnavailable
	}
	seen := map[string]bool{}
	for _, cap := range caps {
		if !knownAction(cap) || seen[cap] {
			return nil, ErrDependencyUnavailable
		}
		seen[cap] = true
	}
	return caps, nil
}

func actionResourceCompatible(action, kind string) bool {
	switch {
	case strings.HasPrefix(action, "agri.farm."):
		return kind == "farm"
	case strings.HasPrefix(action, "agri.parcel."):
		return kind == "parcel" || kind == "farm"
	case action == "agri.asset_download.read":
		return kind == "asset"
	case strings.HasPrefix(action, "agri.asset."):
		return kind == "asset" || kind == "farm"
	case strings.HasPrefix(action, "agri.analysis."):
		return kind == "job" || action == "agri.analysis.submit" && (kind == "farm" || kind == "asset" || kind == "parcel")
	case strings.HasPrefix(action, "agri.inspection."):
		return kind == "job" || kind == "parcel" || kind == "farm"
	case strings.HasPrefix(action, "agri.report."):
		return kind == "report" || kind == "farm"
	case strings.HasPrefix(action, "agri.authorization."):
		return kind != "workspace"
	}
	return false
}

func authorizeOwned(tx *gorm.DB, scope workspaceContext, actorID, action string, proofs map[ResourceRef]ResourceOwnership, now time.Time) ([]string, error) {
	return authorizeBoundCapabilities(tx, scope, actorID, action, proofs, now, true)
}

// Delegating a child-resource capability from a farm root is not execution of
// that action against the farm. All scope and role checks remain identical.
func authorizeDelegationOwned(tx *gorm.DB, scope workspaceContext, actorID, action string, proofs map[ResourceRef]ResourceOwnership, now time.Time) ([]string, error) {
	return authorizeBoundCapabilities(tx, scope, actorID, action, proofs, now, false)
}

func authorizeBoundCapabilities(tx *gorm.DB, scope workspaceContext, actorID, action string, proofs map[ResourceRef]ResourceOwnership, now time.Time, checkKind bool) ([]string, error) {
	if !knownAction(action) || len(proofs) == 0 {
		return nil, ErrInvalid
	}
	byRef, err := effectiveBindings(tx, scope, actorID, proofs, now)
	if err != nil {
		return nil, err
	}
	farms := map[string]bool{}
	denied := false
	for ref, proof := range proofs {
		if checkKind && !actionResourceCompatible(action, ref.Kind) {
			return nil, ErrInvalid
		}
		visible, allowed, _ := resourceAccess(ref, proof, byRef, action, now.Add(30*time.Second))
		if !visible {
			return nil, ErrNotFound
		}
		if !allowed {
			denied = true
		}
		for _, farm := range proof.FarmIDs {
			farms[farm] = true
		}
		// Field operators receive only their assigned minimum projection. A role
		// combination with a broader explicitly bound business role can be used.
		if hasRole(scope.Roles, "field_operator") && !hasBroaderBusinessAction(scope.Roles, action) && proof.AssignedActorID != actorID {
			return nil, ErrNotFound
		}
	}
	if denied || !roleAllowsAction(scope.Roles, action) {
		return nil, ErrForbidden
	}
	out := make([]string, 0, len(farms))
	for id := range farms {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func hasBroaderBusinessAction(roles []string, action string) bool {
	others := []string{}
	for _, role := range roles {
		if role != "field_operator" {
			others = append(others, role)
		}
	}
	return roleAllowsAction(others, action)
}

// actorID is supplied by the trusted platform session/service adapter, never
// decoded from a browser or model request. Both read and write callers use this.
func (s Service) Authorize(ctx context.Context, actorID, workspaceID, action string, resources []ResourceRef) (AuthorizationDecision, error) {
	var out AuthorizationDecision
	if !knownAction(action) {
		return out, ErrInvalid
	}
	if strings.HasPrefix(action, "agri.billing.") {
		if len(resources) != 1 || resources[0] != (ResourceRef{Kind: "workspace", ID: workspaceID}) {
			return out, ErrInvalid
		}
		err := s.withWorkspace(ctx, actorID, workspaceID, false, func(tx *gorm.DB, scope workspaceContext) error {
			if !roleAllowsAction(scope.Roles, action) {
				return ErrForbidden
			}
			version, e := currentAuthorizationVersion(tx, scope.Tenant.ID)
			if e != nil {
				return e
			}
			now := s.now()
			out = AuthorizationDecision{ActorID: actorID, TenantID: scope.Tenant.ID, WorkspaceID: workspaceID, Action: action, Resources: append([]ResourceRef{}, resources...), FarmIDs: []string{}, AuthorizationVersion: version, PolicyVersion: authorizationPolicyVersion, IssuedAt: now, ExpiresAt: now.Add(30 * time.Second)}
			return nil
		})
		return out, err
	}
	err := s.withOwnership(ctx, actorID, workspaceID, resources, func(tx *gorm.DB, scope workspaceContext, version int64, proofs map[ResourceRef]ResourceOwnership) error {
		now := s.now()
		farms, e := authorizeOwned(tx, scope, actorID, action, proofs, now)
		if e != nil {
			return e
		}
		refs, e := canonicalResources(resources)
		if e != nil {
			return e
		}
		expires := now.Add(30 * time.Second)
		for _, p := range proofs {
			if p.ExpiresAt.Before(expires) {
				expires = p.ExpiresAt
			}
		}
		expires, e = authorizationExpiry(tx, scope, actorID, action, proofs, now, expires)
		if e != nil {
			return e
		}
		versions := make([]ResourceVersion, 0, len(refs))
		for _, ref := range refs {
			versions = append(versions, ResourceVersion{Resource: ref, Revision: proofs[ref].Revision})
		}
		out = AuthorizationDecision{ActorID: actorID, TenantID: scope.Tenant.ID, WorkspaceID: workspaceID, Action: action, Resources: refs, ResourceVersions: versions, FarmIDs: farms, AuthorizationVersion: version, PolicyVersion: authorizationPolicyVersion, IssuedAt: now, ExpiresAt: expires}
		return nil
	})
	return out, err
}

// Reauthorization is required for background use; an old decision is not a
// bearer credential. The caller must independently authenticate actorID.
func (s Service) Reauthorize(ctx context.Context, actorID string, previous AuthorizationDecision) (AuthorizationDecision, error) {
	if actorID == "" || actorID != previous.ActorID || previous.PolicyVersion != authorizationPolicyVersion || !s.now().Before(previous.ExpiresAt) {
		return AuthorizationDecision{}, ErrForbidden
	}
	next, err := s.Authorize(ctx, actorID, previous.WorkspaceID, previous.Action, previous.Resources)
	if err != nil {
		return AuthorizationDecision{}, err
	}
	if next.TenantID != previous.TenantID || next.AuthorizationVersion != previous.AuthorizationVersion || !slices.Equal(next.FarmIDs, previous.FarmIDs) || !slices.Equal(next.ResourceVersions, previous.ResourceVersions) {
		return AuthorizationDecision{}, ErrConflict
	}
	return next, nil
}
