package flow

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Config is read from env (names match .env.flow.example).
type Config struct {
	EngineURL  string
	SigningKey []byte
	Issuer     string
	Audience   string
	TTL        time.Duration
}

func ConfigFromEnv() Config {
	ttl := MaxTokenTTL
	if v, err := time.ParseDuration(strings.TrimSpace(os.Getenv("FLOW_INTERNAL_TOKEN_TTL_SECONDS")) + "s"); err == nil && v > 0 {
		ttl = v
	}
	get := func(k, d string) string {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
		return d
	}
	return Config{
		EngineURL:  strings.TrimRight(get("LOOM_ENGINE_URL", ""), "/"),
		SigningKey: []byte(os.Getenv("FLOW_INTERNAL_JWT_SIGNING_KEY")),
		Issuer:     get("FLOW_INTERNAL_JWT_ISSUER", "flow-platform"),
		Audience:   get("FLOW_INTERNAL_JWT_AUDIENCE", "loom-engine"),
		TTL:        ttl,
	}
}

func (c Config) Enabled() bool { return c.EngineURL != "" && len(c.SigningKey) >= 32 }

// EngineClient forwards scoped requests to Loom Engine with a fresh context token.
type EngineClient struct {
	Cfg  Config
	HTTP *http.Client
}

func (e EngineClient) Do(ctx context.Context, scope Scope, actor Actor, method, path, idemKey string, body []byte) (int, []byte, error) {
	tok, err := Sign(e.Cfg.SigningKey, ExecutionContext{Issuer: e.Cfg.Issuer, Audience: e.Cfg.Audience,
		TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID, Actor: actor, Scopes: ScopesForRole(scope.Role),
		CapabilitySnapshotID: capabilityFromCtx(ctx), PolicyVersion: "policy_v1"}, time.Now(), e.Cfg.TTL)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, e.Cfg.EngineURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("X-Idempotency-Key", idemKey)
	}
	hc := e.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("loom engine: %w", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, out, err
}

// DoStream opens a scoped request to Loom Engine and returns the raw HTTP response
// so callers (such as §10.4 media streaming) can stream bytes without buffering or Base64 overhead.
func (e EngineClient) DoStream(ctx context.Context, scope Scope, actor Actor, method, path string, headers map[string]string) (*http.Response, error) {
	tok, err := Sign(e.Cfg.SigningKey, ExecutionContext{Issuer: e.Cfg.Issuer, Audience: e.Cfg.Audience,
		TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID, Actor: actor, Scopes: ScopesForRole(scope.Role),
		CapabilitySnapshotID: capabilityFromCtx(ctx), PolicyVersion: "policy_v1"}, time.Now(), e.Cfg.TTL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, e.Cfg.EngineURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	hc := e.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 120 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("loom engine: %w", err)
	}
	return resp, nil
}
