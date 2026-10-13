package flow

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"yingce/backend/internal/model"
)

// mockOAuth is an authorization server: /authorize issues codes bound to the
// PKCE challenge, /token handles authorization_code + refresh_token (rotating),
// /revoke records revoked tokens.
type mockOAuth struct {
	mu        sync.Mutex
	challenge map[string]string
	refresh   map[string]bool
	revoked   []string
	issued    int
}

func startMockOAuth(t *testing.T) (*mockOAuth, *httptest.Server) {
	m := &mockOAuth{challenge: map[string]string{}, refresh: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "cid" {
			http.Error(w, "bad", 400)
			return
		}
		m.mu.Lock()
		code := "code-" + q.Get("state")[:6]
		m.challenge[code] = q.Get("code_challenge")
		m.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	issue := func(w http.ResponseWriter) {
		m.issued++
		n := string(rune('0' + m.issued))
		m.refresh["r"+n] = true
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "a" + n, "refresh_token": "r" + n, "expires_in": 120, "scope": "tools"})
	}
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		m.mu.Lock()
		defer m.mu.Unlock()
		if id, sec, ok := r.BasicAuth(); !ok || id != "cid" || sec != "csec" {
			w.WriteHeader(401)
			return
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			ch, ok := m.challenge[r.Form.Get("code")]
			delete(m.challenge, r.Form.Get("code"))
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != ch {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			issue(w)
		case "refresh_token":
			if !m.refresh[r.Form.Get("refresh_token")] {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			delete(m.refresh, r.Form.Get("refresh_token")) // rotation: old refresh token is single use
			issue(w)
		}
	})
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		m.mu.Lock()
		m.revoked = append(m.revoked, r.Form.Get("token"))
		delete(m.refresh, r.Form.Get("token"))
		m.mu.Unlock()
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return m, srv
}

func TestMCPOAuthAuthorizationCodePKCERefreshRevoke(t *testing.T) {
	db := testDB(t)
	m, srv := startMockOAuth(t)
	t.Setenv("FLOW_OAUTH_ENC_KEY", strings.Repeat("e", 32))
	for k, v := range map[string]string{"AUTH_URL": srv.URL + "/authorize", "TOKEN_URL": srv.URL + "/token", "CLIENT_ID": "cid", "CLIENT_SECRET": "csec",
		"SCOPE": "tools", "REVOKE_URL": srv.URL + "/revoke", "REDIRECT_URL": "https://flow.example/api/intelligence/mcp/oauth/callback"} {
		t.Setenv("FLOW_MCP_OAUTHCODE_DEMO_"+k, v)
	}
	now := time.Now().UTC()
	gw := MCPGateway{DB: db, AllowInsecure: true, Now: func() time.Time { return now }}
	ss, _ := ResolveSubject(db, "u_alice", AgentSubject{Kind: SubjectWorkspace})
	c, err := gw.Create(ss.Scope, "u_alice", MCPConnectionInput{Name: "OA", Endpoint: "https://mcp.example/mcp", CredentialRef: "oauthcode:FLOW_MCP_OAUTHCODE_DEMO", TimeoutMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gw.oauthCodeToken(context.Background(), c); !errors.Is(err, ErrOAuthNotAuthorized) {
		t.Fatalf("before authorization: %v", err)
	}
	authURL, err := gw.OAuthStart(ss.Scope, "u_alice", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The browser follows the authorize URL; the server redirects back with code+state.
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noFollow.Get(authURL)
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize %v %v", resp, err)
	}
	back, _ := url.Parse(resp.Header.Get("Location"))
	if _, err := gw.OAuthCallback(context.Background(), "u_bob", back.Query().Get("state"), back.Query().Get("code")); err == nil {
		t.Fatal("another user completed the flow")
	}
	// state is single use: bob's attempt consumed it, so start again.
	authURL, _ = gw.OAuthStart(ss.Scope, "u_alice", c.ID)
	resp, _ = noFollow.Get(authURL)
	back, _ = url.Parse(resp.Header.Get("Location"))
	if _, err := gw.OAuthCallback(context.Background(), "u_alice", back.Query().Get("state"), back.Query().Get("code")); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.OAuthCallback(context.Background(), "u_alice", back.Query().Get("state"), back.Query().Get("code")); !errors.Is(err, ErrOAuthState) {
		t.Fatal("state replay accepted")
	}
	var gr model.FlowMCPOAuthGrant
	db.First(&gr, "connection_id = ?", c.ID)
	if strings.Contains(gr.AccessTokenEnc, "a1") || strings.Contains(gr.RefreshTokenEnc, "r1") || !strings.HasPrefix(gr.AccessTokenEnc, "v1:") {
		t.Fatal("tokens stored in clear")
	}
	if tok, err := gw.oauthCodeToken(context.Background(), c); err != nil || tok != "a1" {
		t.Fatalf("access %q %v", tok, err)
	}
	now = now.Add(2 * time.Minute) // expired → refresh with rotation
	if tok, err := gw.oauthCodeToken(context.Background(), c); err != nil || tok != "a2" {
		t.Fatalf("refresh %q %v", tok, err)
	}
	if tok, _ := gw.oauthCodeToken(context.Background(), c); tok != "a2" {
		t.Fatal("refreshed token not cached")
	}
	t.Setenv("FLOW_OAUTH_ENC_KEY", strings.Repeat("x", 32))
	if _, err := gw.oauthCodeToken(context.Background(), c); err == nil {
		t.Fatal("decrypted with the wrong key")
	}
	t.Setenv("FLOW_OAUTH_ENC_KEY", strings.Repeat("e", 32))
	if err := gw.OAuthRevoke(context.Background(), Scope{TenantID: ss.Scope.TenantID, WorkspaceID: ss.Scope.WorkspaceID, Role: "editor"}, c.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("editor revoked")
	}
	if err := gw.OAuthRevoke(context.Background(), ss.Scope, c.ID); err != nil {
		t.Fatal(err)
	}
	if len(m.revoked) != 2 || m.revoked[0] != "r2" || m.revoked[1] != "a2" {
		t.Fatalf("revoked %v", m.revoked)
	}
	if _, err := gw.oauthCodeToken(context.Background(), c); !errors.Is(err, ErrOAuthNotAuthorized) {
		t.Fatal("token usable after revoke")
	}
}
