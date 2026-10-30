package agriculture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"yingce/backend/internal/agricanonical"
	"yingce/backend/internal/platform"
)

// ProjectionQuery is server-selected. Neither identity nor an authorization
// decision may be supplied by a browser, model, or provider.
type ProjectionQuery struct {
	Kind       string
	Action     string
	Resources  []ResourceRef
	Parameters json.RawMessage
}
type ProjectionScope struct {
	ActorID, TenantID, WorkspaceID string
	Action                         string
	Resources                      []ResourceRef
	ResourceVersions               []ResourceVersion
}

// ProjectionBinding must be derived independently from authoritative domain
// rows by the adapter. Version is a representation/result version, not a role.
type ProjectionBinding struct {
	Scope               ProjectionScope
	Projection, Version string
}
type ProjectionProvider interface {
	Describe(context.Context, ProjectionScope, ProjectionQuery) (ProjectionBinding, error)
	Load(context.Context, ProjectionBinding, ProjectionQuery) ([]byte, error)
}
type RefreshActor func(context.Context) (string, error)

type ScopedProjectionCache struct {
	service Service
	cache   *platform.BoundedReadCache[string, []byte]
}

func NewScopedProjectionCache(s Service) *ScopedProjectionCache {
	return &ScopedProjectionCache{s, platform.NewBoundedReadCache[string, []byte](256, 8<<20, 4, 30*time.Second)}
}
func projectionScope(d AuthorizationDecision) ProjectionScope {
	return ProjectionScope{d.ActorID, d.TenantID, d.WorkspaceID, d.Action, slices.Clone(d.Resources), slices.Clone(d.ResourceVersions)}
}
func scopeEqual(a, b ProjectionScope) bool {
	return a.ActorID == b.ActorID && a.TenantID == b.TenantID && a.WorkspaceID == b.WorkspaceID && a.Action == b.Action && slices.Equal(a.Resources, b.Resources) && slices.Equal(a.ResourceVersions, b.ResourceVersions)
}
func validBinding(b ProjectionBinding, s ProjectionScope) bool {
	return scopeEqual(b.Scope, s) && ownershipID.MatchString(b.Projection) && ownershipID.MatchString(b.Version)
}
func projectionDigest(d AuthorizationDecision, b ProjectionBinding, parameters []byte) (string, error) {
	// Exclude receipt timestamps, include ALL actual authorization dimensions.
	raw, e := json.Marshal(struct {
		Scope               ProjectionScope
		Farms               []string
		Policy              string
		Epoch               int64
		Projection, Version string
		Parameters          json.RawMessage
	}{projectionScope(d), d.FarmIDs, d.PolicyVersion, d.AuthorizationVersion, b.Projection, b.Version, parameters})
	if e != nil {
		return "", e
	}
	canonical, e := agricanonical.Canonicalize(raw)
	if e != nil {
		return "", e
	}
	hash := sha256.Sum256(canonical)
	return hex.EncodeToString(hash[:]), nil
}
func freshProjection(ctx context.Context, s Service, refresh RefreshActor, workspace string, q ProjectionQuery) (AuthorizationDecision, error) {
	if refresh == nil {
		return AuthorizationDecision{}, ErrUnauthenticated
	}
	actor, e := refresh(ctx)
	if e != nil {
		return AuthorizationDecision{}, e
	}
	if actor == "" {
		return AuthorizationDecision{}, ErrUnauthenticated
	}
	return s.Authorize(ctx, actor, workspace, q.Action, q.Resources)
}
func (c *ScopedProjectionCache) Query(ctx context.Context, refresh RefreshActor, workspace string, q ProjectionQuery, p ProjectionProvider) ([]byte, error) {
	if c == nil || p == nil {
		return nil, ErrDependencyUnavailable
	}
	if !knownAction(q.Action) || !strings.HasSuffix(q.Action, ".read") {
		return nil, ErrInvalid
	}
	switch q.Kind {
	case "search", "map", "tool":
	default:
		return nil, ErrInvalid
	}
	if len(q.Parameters) > 64<<10 {
		return nil, ErrInvalid
	}
	canonical, e := agricanonical.Canonicalize(q.Parameters)
	if e != nil {
		return nil, ErrInvalid
	}
	q.Parameters = canonical
	d, e := freshProjection(ctx, c.service, refresh, workspace, q)
	if e != nil {
		return nil, e
	}
	binding, e := p.Describe(ctx, projectionScope(d), q)
	if e != nil {
		return nil, e
	}
	if !validBinding(binding, projectionScope(d)) {
		return nil, ErrDependencyUnavailable
	}
	// Kind is also part of the key; equal parameter objects across products never collide.
	params, e := json.Marshal(struct {
		Kind   string
		Params json.RawMessage
	}{q.Kind, canonical})
	if e != nil {
		return nil, e
	}
	key, e := projectionDigest(d, binding, params)
	if e != nil {
		return nil, e
	}
	value, e := c.cache.Get(ctx, key, func(ctx context.Context) ([]byte, int, error) {
		value, e := p.Load(ctx, binding, q)
		if e != nil {
			return nil, 0, e
		}
		if len(value) > 1<<20 {
			return nil, 0, ErrDependencyUnavailable
		}
		// A fill completing after revocation cannot populate a useful old permission entry.
		if e := c.check(ctx, refresh, workspace, q, p, key, params); e != nil {
			return nil, 0, e
		}
		return slices.Clone(value), len(value), nil
	})
	if e != nil {
		return nil, e
	}
	if e = c.check(ctx, refresh, workspace, q, p, key, params); e != nil {
		return nil, e
	}
	return slices.Clone(value), nil
}
func (c *ScopedProjectionCache) check(ctx context.Context, refresh RefreshActor, workspace string, q ProjectionQuery, p ProjectionProvider, key string, params []byte) error {
	d, e := freshProjection(ctx, c.service, refresh, workspace, q)
	if e != nil {
		return e
	}
	b, e := p.Describe(ctx, projectionScope(d), q)
	if e != nil {
		return e
	}
	if !validBinding(b, projectionScope(d)) {
		return ErrDependencyUnavailable
	}
	// Describe may perform remote I/O. Revalidate after it, not only before it.
	actor, e := refresh(ctx)
	if e != nil {
		return e
	}
	d, e = c.service.Reauthorize(ctx, actor, d)
	if e != nil {
		return e
	}
	next, e := projectionDigest(d, b, params)
	if e != nil {
		return e
	}
	if next != key {
		return ErrConflict
	}
	return ctx.Err()
}
