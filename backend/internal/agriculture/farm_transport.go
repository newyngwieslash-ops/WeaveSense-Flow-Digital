package agriculture

import (
	"bytes"
	"context"
	"io"
	"net/http"
)

// farmSend shares the fixed-origin, no-redirect, bounded GIS client. Domain
// validation status is preserved without exposing raw upstream text.
func (g *GISClient) farmSend(ctx context.Context, method, path string, body []byte, token string) ([]byte, error) {
	if g == nil || g.base == nil || g.client == nil || !internalRequestPath.MatchString(path) {
		return nil, ErrDependencyUnavailable
	}
	endpoint := *g.base
	endpoint.Path = path
	req, e := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(body))
	if e != nil {
		return nil, ErrInvalid
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, e := g.client.Do(req)
	if e != nil {
		return nil, ErrDependencyUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case 200, 201:
	case 400:
		return nil, ErrInvalid
	case 401, 403:
		return nil, ErrForbidden
	case 404:
		return nil, ErrNotFound
	case 409:
		return nil, ErrConflict
	case 422:
		return nil, ErrFarmGeometry
	default:
		return nil, ErrDependencyUnavailable
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return nil, ErrDependencyUnavailable
	}
	return raw, nil
}
func (g *GISClient) farmAuthorized(ctx context.Context, s Service, actor string, d AuthorizationDecision, method, path string, body []byte, operationID string) ([]byte, error) {
	if g == nil {
		return nil, ErrDependencyUnavailable
	}
	token, canonical, e := s.issueDelegation(ctx, g.keys, actor, d, method, path, body, operationID)
	if e != nil {
		return nil, e
	}
	return g.farmSend(ctx, method, path, canonical, token)
}
