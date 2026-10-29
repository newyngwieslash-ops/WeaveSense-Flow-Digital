package agriculture

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"yingce/backend/internal/outbound"
)

// GISClient has a deployment-fixed origin and cannot accept caller-chosen URLs.
// Its transport reuses platform DNS/private-host checks, disallows redirects,
// and never includes a bearer token or response body in an error or log.
type GISClient struct {
	base   *url.URL
	client *http.Client
	keys   *ServiceKeyring
	now    func() time.Time
}

func NewGISClient(baseURL string, keys *ServiceKeyring) (*GISClient, error) {
	base, err := outbound.ValidateCustomRelayURL(baseURL)
	if err != nil || keys == nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || base.RawPath != "" || (base.Path != "" && base.Path != "/") {
		return nil, ErrServiceIdentity
	}
	// Ownership responses are not independently signed: non-local peers must
	// authenticate their server with TLS. HTTP is only a literal loopback
	// development exception, still subject to the exact platform allowlist.
	// A name such as localhost is not accepted as proof of loopback ownership.
	if base.Scheme == "http" {
		address, err := netip.ParseAddr(base.Hostname())
		if err != nil || address.Zone() != "" || !address.Unmap().IsLoopback() {
			return nil, ErrServiceIdentity
		}
	}
	return &GISClient{base: base, client: outbound.CustomRelayHTTPClient(10 * time.Second), keys: keys, now: time.Now}, nil
}

func (g *GISClient) send(ctx context.Context, path string, body []byte, token string) ([]byte, error) {
	return g.sendMethod(ctx, "POST", path, body, token)
}

func (g *GISClient) sendMethod(ctx context.Context, method, path string, body []byte, token string) ([]byte, error) {
	if g == nil || g.base == nil || g.client == nil || !internalRequestPath.MatchString(path) {
		return nil, ErrDependencyUnavailable
	}
	endpoint := *g.base
	endpoint.Path = path
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, ErrInvalid
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := g.client.Do(req)
	if err != nil {
		return nil, ErrDependencyUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		switch response.StatusCode {
		case 401, 403:
			return nil, ErrForbidden
		case 404:
			return nil, ErrNotFound
		case 409:
			return nil, ErrConflict
		default:
			return nil, ErrDependencyUnavailable
		}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, ErrDependencyUnavailable
	}
	return raw, nil
}

type ownershipWire struct {
	Resource        ResourceRef `json:"resource"`
	TenantID        string      `json:"tenant_id"`
	WorkspaceID     string      `json:"workspace_id"`
	FarmIDs         []string    `json:"farm_ids"`
	Revision        int64       `json:"revision"`
	Status          string      `json:"status"`
	ReceiptID       string      `json:"receipt_id"`
	IssuedAt        string      `json:"issued_at"`
	ExpiresAt       string      `json:"expires_at"`
	AssignedActorID string      `json:"assigned_actor_id,omitempty"`
}

// Resolve supplies only the pre-authorization ownership capability. The final
// user authorization still independently verifies these receipts and scope.
func (g *GISClient) Resolve(ctx context.Context, scope OwnershipContext, resources []ResourceRef) ([]ResourceOwnership, error) {
	if g == nil {
		return nil, ErrDependencyUnavailable
	}
	refs, err := canonicalResources(resources)
	if err != nil {
		return nil, err
	}
	body, err := marshalIdentityBody(struct {
		TenantID    string        `json:"tenant_id"`
		WorkspaceID string        `json:"workspace_id"`
		Resources   []ResourceRef `json:"resources"`
	}{scope.TenantID, scope.WorkspaceID, refs})
	if err != nil {
		return nil, err
	}
	now := g.now().UTC()
	token, canonical, err := issueServiceAssertion(g.keys, "ownership.resolve", scope, body, now)
	if err != nil {
		return nil, ErrDependencyUnavailable
	}
	raw, err := g.send(ctx, ownershipResolvePath, canonical, token)
	if err != nil {
		return nil, err
	}
	var response struct {
		Resources []ownershipWire `json:"resources"`
	}
	if strictIdentityJSON(raw, &response, 1<<20) != nil {
		return nil, ErrDependencyUnavailable
	}
	proofs := make([]ResourceOwnership, 0, len(response.Resources))
	for _, wire := range response.Resources {
		issued, err := identityTime(wire.IssuedAt)
		if err != nil {
			return nil, ErrDependencyUnavailable
		}
		expires, err := identityTime(wire.ExpiresAt)
		if err != nil {
			return nil, ErrDependencyUnavailable
		}
		proofs = append(proofs, ResourceOwnership{Resource: wire.Resource, TenantID: wire.TenantID, WorkspaceID: wire.WorkspaceID, FarmIDs: wire.FarmIDs, Revision: wire.Revision, Status: wire.Status, ReceiptID: wire.ReceiptID, IssuedAt: issued, ExpiresAt: expires, AssignedActorID: wire.AssignedActorID})
	}
	if _, err := validateOwnership(now, scope, refs, proofs); err != nil {
		return nil, err
	}
	return proofs, nil
}

// CallAuthorized is for trusted platform composition only. No public route
// returns its internally issued token, and issuance always reauthorizes actor.
func (g *GISClient) CallAuthorized(ctx context.Context, s Service, actorID string, previous AuthorizationDecision, method, path string, body []byte, operationID string) ([]byte, error) {
	if g == nil {
		return nil, ErrDependencyUnavailable
	}
	token, canonical, err := s.issueDelegation(ctx, g.keys, actorID, previous, method, path, body, operationID)
	if err != nil {
		return nil, err
	}
	return g.sendMethod(ctx, method, path, canonical, token)
}
