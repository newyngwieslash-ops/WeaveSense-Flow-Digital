// Package flow implements the WeaveSense-Flow platform side: tenant/workspace
// scope resolution, signed execution contexts for Loom Engine, an Engine
// client, and idempotent consumption of Engine outbox events.
package flow

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Actor identifies who an execution context acts for.
type Actor struct {
	Type string `json:"type"` // user | service_principal | schedule
	ID   string `json:"id"`
}

// ExecutionContext is the signed, short-lived claim set from docs/flow/service-contracts.md §1.
type ExecutionContext struct {
	Issuer              string   `json:"iss"`
	Audience            string   `json:"aud"`
	IssuedAt            int64    `json:"iat"`
	ExpiresAt           int64    `json:"exp"`
	JTI                 string   `json:"jti"`
	TenantID            string   `json:"tenant_id"`
	WorkspaceID         string   `json:"workspace_id"`
	Actor               Actor    `json:"actor"`
	OperationID         string   `json:"operation_id,omitempty"`
	JobID               string   `json:"job_id,omitempty"`
	Scopes              []string `json:"scopes"`
	BudgetReservationID string   `json:"budget_reservation_id,omitempty"`
	// CapabilitySnapshotID (PRD §8.1 RunContext.capabilitySnapshotId): the run's frozen capabilities.
	CapabilitySnapshotID string `json:"capability_snapshot_id,omitempty"`
	PolicyVersion        string `json:"policy_version"`
}

const MaxTokenTTL = 5 * time.Minute

var (
	ErrBadToken     = errors.New("flow: invalid execution context")
	ErrExpiredToken = errors.New("flow: execution context expired")
)

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func NewID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// Sign produces an HS256 JWT. TTL is clamped to MaxTokenTTL.
func Sign(key []byte, c ExecutionContext, now time.Time, ttl time.Duration) (string, error) {
	if len(key) < 32 {
		return "", errors.New("flow: signing key must be >= 32 bytes")
	}
	if ttl <= 0 || ttl > MaxTokenTTL {
		ttl = MaxTokenTTL
	}
	if c.TenantID == "" || c.WorkspaceID == "" || c.Audience == "" {
		return "", errors.New("flow: tenant, workspace and audience are required")
	}
	c.IssuedAt, c.ExpiresAt = now.Unix(), now.Add(ttl).Unix()
	if c.JTI == "" {
		c.JTI = NewID("jti_")
	}
	header := b64([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	unsigned := header + "." + b64(body)
	m := hmac.New(sha256.New, key)
	m.Write([]byte(unsigned))
	return unsigned + "." + b64(m.Sum(nil)), nil
}

// Verify checks signature, algorithm, audience and expiry.
func Verify(key []byte, token, audience string, now time.Time) (ExecutionContext, error) {
	var c ExecutionContext
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return c, ErrBadToken
	}
	h, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || string(h) != `{"alg":"HS256","typ":"JWT"}` {
		return c, ErrBadToken
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, m.Sum(nil)) {
		return c, ErrBadToken
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(body, &c) != nil {
		return c, ErrBadToken
	}
	if c.Audience != audience || c.TenantID == "" || c.WorkspaceID == "" {
		return c, ErrBadToken
	}
	if now.Unix() >= c.ExpiresAt || c.ExpiresAt-c.IssuedAt > int64(MaxTokenTTL/time.Second) {
		return c, ErrExpiredToken
	}
	return c, nil
}

func (c ExecutionContext) HasScope(s string) bool {
	for _, v := range c.Scopes {
		if v == s {
			return true
		}
	}
	return false
}
