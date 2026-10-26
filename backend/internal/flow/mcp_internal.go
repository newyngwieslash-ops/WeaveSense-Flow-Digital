package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"syscall"
	"time"
)

// InternalTarget is one exact internal MCP endpoint admitted by deployment
// configuration (PRD FR-P04). It never widens the global egress policy: a
// connection reaches a non-public address only when its endpoint string equals
// Endpoint, its dialer is pinned to that host:port, redirects are refused, and
// only the listed tools can ever be approved on it.
type InternalTarget struct {
	Endpoint string   `json:"endpoint"`
	Tools    []string `json:"tools"`
	// CredentialRef, when set, is the only credential the connection may use
	// (for example "mint:edumath" for a short-lived tenant-bound token).
	CredentialRef string `json:"credentialRef,omitempty"`
}

// MintCredentials mints a short-lived credential for a "mint:<name>" ref,
// bound to the connection's tenant. Configured by the server (nil = disabled).
type MintCredentials func(ctx context.Context, ref, tenantID string) (string, error)

const mintRefPrefix = "mint:"

var blockedInternalHosts = map[string]bool{"metadata": true, "metadata.google.internal": true, "metadata.goog": true, "instance-data": true}

// ParseInternalTargets reads FLOW_MCP_INTERNAL_TARGETS (a JSON array of
// InternalTarget). Empty input yields no targets.
func ParseInternalTargets(raw string) ([]InternalTarget, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var in []InternalTarget
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return nil, fmt.Errorf("FLOW_MCP_INTERNAL_TARGETS: %w", err)
	}
	out := make([]InternalTarget, 0, len(in))
	for i, t := range in {
		u, err := url.Parse(strings.TrimSpace(t.Endpoint))
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, fmt.Errorf("FLOW_MCP_INTERNAL_TARGETS[%d]: endpoint must be an http(s) URL without credentials, query or fragment", i)
		}
		if u.Port() == "" {
			return nil, fmt.Errorf("FLOW_MCP_INTERNAL_TARGETS[%d]: endpoint must name an explicit port", i)
		}
		host := strings.ToLower(u.Hostname())
		if blockedInternalHosts[host] {
			return nil, fmt.Errorf("FLOW_MCP_INTERNAL_TARGETS[%d]: metadata hosts are never allowed", i)
		}
		if ip, err := netip.ParseAddr(host); err == nil && !internalAddrAllowed(ip) {
			return nil, fmt.Errorf("FLOW_MCP_INTERNAL_TARGETS[%d]: address %s is not allowed", i, ip)
		}
		if len(t.Tools) == 0 {
			return nil, fmt.Errorf("FLOW_MCP_INTERNAL_TARGETS[%d]: tools must list the exact tools that may be approved", i)
		}
		for _, name := range t.Tools {
			if !mcpToolNameRe.MatchString(name) {
				return nil, fmt.Errorf("FLOW_MCP_INTERNAL_TARGETS[%d]: invalid tool name %q", i, name)
			}
		}
		if t.CredentialRef != "" && !strings.HasPrefix(t.CredentialRef, mintRefPrefix) && !strings.HasPrefix(t.CredentialRef, "env:FLOW_MCP_CRED_") {
			return nil, fmt.Errorf("FLOW_MCP_INTERNAL_TARGETS[%d]: credentialRef must be mint:<name> or env:FLOW_MCP_CRED_*", i)
		}
		t.Endpoint = u.String()
		out = append(out, t)
	}
	return out, nil
}

// internalAddrAllowed: loopback and private ranges may be named explicitly;
// link-local (cloud metadata), unspecified and multicast never.
func internalAddrAllowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && !ip.IsUnspecified() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsMulticast() && !ip.IsInterfaceLocalMulticast()
}

func (g MCPGateway) internalTarget(endpoint string) (InternalTarget, bool) {
	for _, t := range g.Internal {
		if t.Endpoint == endpoint {
			return t, true
		}
	}
	return InternalTarget{}, false
}

// internalToolAllowed reports whether tool may be approved on endpoint.
func (g MCPGateway) internalToolAllowed(endpoint, tool string) bool {
	t, ok := g.internalTarget(endpoint)
	return !ok || slices.Contains(t.Tools, tool)
}

var errInternalRedirect = errors.New("flow: redirects are not followed for internal MCP targets")

// internalClient dials only the target's exact host:port and never follows redirects.
func internalClient(t InternalTarget, secret string, timeout time.Duration) *http.Client {
	u, _ := url.Parse(t.Endpoint)
	want := net.JoinHostPort(u.Hostname(), u.Port())
	d := &net.Dialer{Timeout: 5 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil || !internalAddrAllowed(ap.Addr()) {
			return fmt.Errorf("%w: %s", ErrEgressBlocked, address)
		}
		return nil
	}}
	tr := &http.Transport{Proxy: nil, MaxIdleConns: 10, IdleConnTimeout: 60 * time.Second, TLSHandshakeTimeout: 10 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr != want {
				return nil, fmt.Errorf("%w: %s is not the configured internal target", ErrEgressBlocked, addr)
			}
			return d.DialContext(ctx, network, addr)
		}}
	return &http.Client{Transport: headerRT{base: tr, secret: secret}, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errInternalRedirect }}
}
