package agriculture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
	"time"

	"yingce/backend/internal/agricanonical"
	"yingce/backend/internal/flow"
)

var identityActorID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var internalRequestPath = regexp.MustCompile(`^/internal/agri/v1/[A-Za-z0-9_/-]+$`)

type identityActor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type identityRequest struct {
	Method                  string `json:"method"`
	Path                    string `json:"path"`
	CanonicalizationVersion string `json:"canonicalization_version,omitempty"`
	PayloadHash             string `json:"payload_hash,omitempty"`
	OperationID             string `json:"operation_id,omitempty"`
}

type delegationClaims struct {
	SchemaVersion        string            `json:"schema_version"`
	Issuer               string            `json:"issuer"`
	Audience             string            `json:"audience"`
	Subject              string            `json:"subject"`
	Actor                identityActor     `json:"actor"`
	TenantID             string            `json:"tenant_id"`
	WorkspaceID          string            `json:"workspace_id"`
	ResourceScope        []ResourceRef     `json:"resource_scope"`
	Scopes               []string          `json:"scopes"`
	AuthorizationVersion int64             `json:"authorization_version"`
	PolicyVersion        string            `json:"policy_version"`
	FarmIDs              []string          `json:"farm_ids"`
	ResourceVersions     []ResourceVersion `json:"resource_versions"`
	IssuedAt             string            `json:"issued_at"`
	ExpiresAt            string            `json:"expires_at"`
	JTI                  string            `json:"jti"`
	TraceID              string            `json:"trace_id"`
	RequestID            string            `json:"request_id"`
	Request              identityRequest   `json:"request"`
}

func bindIdentityRequest(method, path string, body []byte, operationID string, service bool) (identityRequest, []byte, error) {
	request := identityRequest{Method: method, Path: path}
	if !internalRequestPath.MatchString(path) || strings.Contains(path, "//") || strings.HasSuffix(path, "/") {
		return request, nil, ErrInvalid
	}
	switch method {
	case "GET", "HEAD":
		if len(body) != 0 || operationID != "" || service {
			return request, nil, ErrInvalid
		}
		return request, nil, nil
	case "POST", "PUT", "PATCH", "DELETE":
		if service {
			if operationID != "" {
				return request, nil, ErrInvalid
			}
		} else if !ownershipID.MatchString(operationID) {
			return request, nil, ErrInvalid
		}
		canonical, err := agricanonical.Canonicalize(body)
		if err != nil {
			return request, nil, ErrInvalid
		}
		sum := sha256.Sum256(canonical)
		request.CanonicalizationVersion = canonicalPayloadVersion
		request.PayloadHash = hex.EncodeToString(sum[:])
		request.OperationID = operationID
		return request, canonical, nil
	default:
		return request, nil, ErrInvalid
	}
}

func validateDelegationClaims(c delegationClaims, now time.Time, key ServiceIdentityKey) error {
	if c.SchemaVersion != "1.0" || c.Issuer != "flow-platform" || c.Subject != "flow-platform" || c.Audience != "agri-gis" || c.Actor.Kind != "user" || !identityActorID.MatchString(c.Actor.ID) || !ownershipID.MatchString(c.TenantID) || !ownershipID.MatchString(c.WorkspaceID) || c.AuthorizationVersion < 1 || c.AuthorizationVersion > maximumAuthorizationVersion || c.PolicyVersion != authorizationPolicyVersion || len(c.Scopes) != 1 || !knownAction(c.Scopes[0]) || !ownershipID.MatchString(c.JTI) || !ownershipID.MatchString(c.TraceID) || !ownershipID.MatchString(c.RequestID) {
		return ErrServiceIdentity
	}
	refs, err := canonicalResources(c.ResourceScope)
	if err != nil || !slices.Equal(refs, c.ResourceScope) || len(c.ResourceVersions) != len(refs) || len(c.FarmIDs) == 0 || len(c.FarmIDs) > 200 {
		return ErrServiceIdentity
	}
	for i, ref := range refs {
		if c.ResourceVersions[i].Resource != ref || c.ResourceVersions[i].Revision < 1 || c.ResourceVersions[i].Revision > maxOwnershipRevision {
			return ErrServiceIdentity
		}
	}
	for i, farm := range c.FarmIDs {
		if !ownershipID.MatchString(farm) || (i > 0 && c.FarmIDs[i-1] >= farm) {
			return ErrServiceIdentity
		}
	}
	for _, ref := range refs {
		if ref.Kind == "farm" && !slices.Contains(c.FarmIDs, ref.ID) {
			return ErrServiceIdentity
		}
	}
	return validateIdentityTimes(c.IssuedAt, c.ExpiresAt, now, key)
}

// Only the internal transport consumes this ticket. The trusted actor is
// supplied by the platform caller; no browser body or LLM claim is propagated.
func (s Service) issueDelegation(ctx context.Context, keys *ServiceKeyring, actorID string, previous AuthorizationDecision, method, path string, body []byte, operationID string) (string, []byte, error) {
	request, canonical, err := bindIdentityRequest(method, path, body, operationID, false)
	if err != nil {
		return "", nil, err
	}
	decision, err := s.Reauthorize(ctx, actorID, previous)
	if err != nil {
		return "", nil, err
	}
	now := s.now().Truncate(time.Second)
	expires := now.Add(30 * time.Second)
	if decision.ExpiresAt.Before(expires) {
		expires = decision.ExpiresAt.Truncate(time.Second)
	}
	if !expires.After(now) {
		return "", nil, ErrForbidden
	}
	claims := delegationClaims{SchemaVersion: "1.0", Issuer: "flow-platform", Audience: "agri-gis", Subject: "flow-platform", Actor: identityActor{Kind: "user", ID: actorID}, TenantID: decision.TenantID, WorkspaceID: decision.WorkspaceID, ResourceScope: decision.Resources, Scopes: []string{decision.Action}, AuthorizationVersion: decision.AuthorizationVersion, PolicyVersion: decision.PolicyVersion, FarmIDs: decision.FarmIDs, ResourceVersions: decision.ResourceVersions, IssuedAt: now.Format("2006-01-02T15:04:05Z"), ExpiresAt: expires.Format("2006-01-02T15:04:05Z"), JTI: flow.NewID("jti_"), TraceID: flow.NewID("trace_"), RequestID: flow.NewID("req_"), Request: request}
	key, err := keys.signingKey(now, expires)
	if err != nil {
		return "", nil, ErrDependencyUnavailable
	}
	if validateDelegationClaims(claims, now, key) != nil {
		return "", nil, ErrServiceIdentity
	}
	token, err := keys.sign(delegationType, claims, now, expires)
	return token, canonical, err
}

func verifyDelegation(keys *ServiceKeyring, token, method, path string, body []byte, now time.Time) (delegationClaims, error) {
	var claims delegationClaims
	key, err := keys.verify(token, delegationType, now, &claims)
	if err != nil {
		return claims, err
	}
	if validateDelegationClaims(claims, now, key) != nil {
		return delegationClaims{}, ErrServiceIdentity
	}
	bound, _, err := bindIdentityRequest(method, path, body, claims.Request.OperationID, false)
	if err != nil || bound != claims.Request {
		return delegationClaims{}, ErrServiceIdentity
	}
	return claims, nil
}
