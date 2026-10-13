package flow

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"yingce/backend/internal/model"
)

// Interactive MCP OAuth: authorization code + PKCE (S256) per connection.
// credentialRef "oauthcode:FLOW_MCP_OAUTHCODE_<NAME>" reads, server-side only:
//   <NAME>_AUTH_URL, _TOKEN_URL, _CLIENT_ID, optional _CLIENT_SECRET, _SCOPE,
//   _REVOKE_URL, and _REDIRECT_URL (…/api/intelligence/mcp/oauth/callback).
// Access/refresh tokens are AES-256-GCM encrypted with FLOW_OAUTH_ENC_KEY,
// refreshed before expiry (rotation honoured), and revocable (RFC 7009).

const oauthCodePrefix = "oauthcode:"

var (
	ErrOAuthNotAuthorized = errors.New("flow: mcp oauth authorization required")
	ErrOAuthState         = errors.New("flow: invalid or expired oauth state")
)

type oauthCodeConfig struct {
	AuthURL, TokenURL, ClientID, ClientSecret, Scope, RevokeURL, RedirectURL string
}

func oauthCodeConfigFor(ref string) (oauthCodeConfig, error) {
	name, ok := strings.CutPrefix(ref, oauthCodePrefix)
	if !ok || !strings.HasPrefix(name, "FLOW_MCP_OAUTHCODE_") {
		return oauthCodeConfig{}, fmt.Errorf("%w: ref must be oauthcode:FLOW_MCP_OAUTHCODE_*", ErrMCPInvalid)
	}
	c := oauthCodeConfig{AuthURL: os.Getenv(name + "_AUTH_URL"), TokenURL: os.Getenv(name + "_TOKEN_URL"), ClientID: os.Getenv(name + "_CLIENT_ID"),
		ClientSecret: os.Getenv(name + "_CLIENT_SECRET"), Scope: os.Getenv(name + "_SCOPE"), RevokeURL: os.Getenv(name + "_REVOKE_URL"), RedirectURL: os.Getenv(name + "_REDIRECT_URL")}
	if c.AuthURL == "" || c.TokenURL == "" || c.ClientID == "" || c.RedirectURL == "" {
		return c, fmt.Errorf("%w: oauth %s is not configured", ErrMCPInvalid, name)
	}
	return c, nil
}

// --- token encryption -------------------------------------------------------

func oauthAEAD() (cipher.AEAD, error) {
	k := os.Getenv("FLOW_OAUTH_ENC_KEY")
	if len(k) < 32 {
		return nil, errors.New("flow: FLOW_OAUTH_ENC_KEY (>= 32 chars) is required for oauth token storage")
	}
	sum := sha256.Sum256([]byte(k))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func sealSecret(plain, aad string) (string, error) {
	if plain == "" {
		return "", nil
	}
	a, err := oauthAEAD()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return "v1:" + base64.RawStdEncoding.EncodeToString(a.Seal(nonce, nonce, []byte(plain), []byte(aad))), nil
}

func openSecret(enc, aad string) (string, error) {
	if enc == "" {
		return "", nil
	}
	a, err := oauthAEAD()
	if err != nil {
		return "", err
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(enc, "v1:"))
	if err != nil || len(raw) < a.NonceSize() {
		return "", errors.New("flow: corrupt oauth secret")
	}
	out, err := a.Open(nil, raw[:a.NonceSize()], raw[a.NonceSize():], []byte(aad))
	if err != nil {
		return "", errors.New("flow: oauth secret failed authentication")
	}
	return string(out), nil
}

func randToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// --- flow -------------------------------------------------------------------

// OAuthStart creates a PKCE authorization request and returns the URL to open.
func (g MCPGateway) OAuthStart(scope Scope, userID, connID string) (string, error) {
	if scope.Role != "admin" {
		return "", ErrForbidden
	}
	c, err := g.get(scope, connID)
	if err != nil {
		return "", err
	}
	cfg, err := oauthCodeConfigFor(c.CredentialRef)
	if err != nil {
		return "", err
	}
	verifier := randToken(48)
	venc, err := sealSecret(verifier, "pkce:"+connID)
	if err != nil {
		return "", err
	}
	state := randToken(24)
	if err := g.DB.Create(&model.FlowOAuthState{State: state, TenantID: scope.TenantID, ConnectionID: connID, UserID: userID, VerifierEnc: venc, ExpiresAt: g.now().Add(10 * time.Minute)}).Error; err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {cfg.ClientID}, "redirect_uri": {cfg.RedirectURL}, "state": {state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}}
	if cfg.Scope != "" {
		q.Set("scope", cfg.Scope)
	}
	sep := "?"
	if strings.Contains(cfg.AuthURL, "?") {
		sep = "&"
	}
	return cfg.AuthURL + sep + q.Encode(), nil
}

type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}

func (g MCPGateway) oauthPost(ctx context.Context, cfg oauthCodeConfig, endpoint string, form url.Values) (oauthTokenResponse, int, error) {
	var out oauthTokenResponse
	if cfg.ClientSecret == "" {
		form.Set("client_id", cfg.ClientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return out, 0, err
	}
	if cfg.ClientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(cfg.ClientID), url.QueryEscape(cfg.ClientSecret))
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	hc := g.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return out, 0, errors.New("oauth endpoint unreachable")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	_ = json.Unmarshal(raw, &out)
	return out, resp.StatusCode, nil
}

func (g MCPGateway) storeGrant(tenantID, connID, userID string, tok oauthTokenResponse, keepRefresh string) error {
	aenc, err := sealSecret(tok.AccessToken, "access:"+connID)
	if err != nil {
		return err
	}
	refresh := tok.RefreshToken
	if refresh == "" {
		refresh = keepRefresh // no rotation: keep the previous refresh token
	}
	renc, err := sealSecret(refresh, "refresh:"+connID)
	if err != nil {
		return err
	}
	if tok.ExpiresIn <= 0 {
		tok.ExpiresIn = 3600
	}
	now := g.now()
	row := model.FlowMCPOAuthGrant{ConnectionID: connID, TenantID: tenantID, AccessTokenEnc: aenc, RefreshTokenEnc: renc, Scope: tok.Scope,
		ExpiresAt: now.Add(time.Duration(tok.ExpiresIn) * time.Second), GrantedBy: userID, CreatedAt: now, UpdatedAt: now}
	return g.DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "connection_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"access_token_enc", "refresh_token_enc", "scope", "expires_at", "updated_at"})}).Create(&row).Error
}

// OAuthCallback consumes the single-use state and exchanges the code (PKCE).
func (g MCPGateway) OAuthCallback(ctx context.Context, userID, state, code string) (string, error) {
	var st model.FlowOAuthState
	if err := g.DB.First(&st, "state = ?", state).Error; err != nil {
		return "", ErrOAuthState
	}
	if del := g.DB.Where("state = ?", state).Delete(&model.FlowOAuthState{}); del.RowsAffected != 1 {
		return "", ErrOAuthState // already consumed
	}
	if st.UserID != userID || g.now().After(st.ExpiresAt) || code == "" {
		return "", ErrOAuthState
	}
	var c model.FlowMCPConnection
	if err := g.DB.First(&c, "id = ? AND tenant_id = ?", st.ConnectionID, st.TenantID).Error; err != nil {
		return "", ErrMCPNotFound
	}
	cfg, err := oauthCodeConfigFor(c.CredentialRef)
	if err != nil {
		return "", err
	}
	verifier, err := openSecret(st.VerifierEnc, "pkce:"+c.ID)
	if err != nil {
		return "", err
	}
	tok, status, err := g.oauthPost(ctx, cfg, cfg.TokenURL, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {cfg.RedirectURL}, "code_verifier": {verifier}})
	if err != nil {
		return "", err
	}
	if status != http.StatusOK || tok.AccessToken == "" {
		return "", fmt.Errorf("oauth code exchange failed (%d %s)", status, tok.Error)
	}
	return c.ID, g.storeGrant(c.TenantID, c.ID, userID, tok, "")
}

// oauthCodeToken returns a valid access token, refreshing it when it expires
// within 60s. Concurrent refreshes are serialised by a row lock.
func (g MCPGateway) oauthCodeToken(ctx context.Context, c model.FlowMCPConnection) (string, error) {
	cfg, err := oauthCodeConfigFor(c.CredentialRef)
	if err != nil {
		return "", err
	}
	var out string
	err = g.DB.Transaction(func(tx *gorm.DB) error {
		var gr model.FlowMCPOAuthGrant
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&gr, "connection_id = ? AND tenant_id = ?", c.ID, c.TenantID).Error; err != nil {
			return ErrOAuthNotAuthorized
		}
		if gr.ExpiresAt.After(g.now().Add(60 * time.Second)) {
			out, err = openSecret(gr.AccessTokenEnc, "access:"+c.ID)
			return err
		}
		refresh, err := openSecret(gr.RefreshTokenEnc, "refresh:"+c.ID)
		if err != nil || refresh == "" {
			return ErrOAuthNotAuthorized
		}
		tok, status, err := g.oauthPost(ctx, cfg, cfg.TokenURL, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
		if err != nil {
			return err
		}
		if status != http.StatusOK || tok.AccessToken == "" {
			if tok.Error == "invalid_grant" {
				tx.Where("connection_id = ?", c.ID).Delete(&model.FlowMCPOAuthGrant{})
				return ErrOAuthNotAuthorized
			}
			return fmt.Errorf("oauth refresh failed (%d)", status)
		}
		if err := (MCPGateway{DB: tx, Now: g.Now}).storeGrant(c.TenantID, c.ID, gr.GrantedBy, tok, refresh); err != nil {
			return err
		}
		out = tok.AccessToken
		return nil
	})
	return out, err
}

// OAuthRevoke revokes the tokens at the provider (best effort, RFC 7009) and deletes the grant.
func (g MCPGateway) OAuthRevoke(ctx context.Context, scope Scope, connID string) error {
	if scope.Role != "admin" {
		return ErrForbidden
	}
	c, err := g.get(scope, connID)
	if err != nil {
		return err
	}
	var gr model.FlowMCPOAuthGrant
	if err := g.DB.First(&gr, "connection_id = ? AND tenant_id = ?", c.ID, c.TenantID).Error; err != nil {
		return nil
	}
	if cfg, err := oauthCodeConfigFor(c.CredentialRef); err == nil && cfg.RevokeURL != "" {
		if rt, _ := openSecret(gr.RefreshTokenEnc, "refresh:"+c.ID); rt != "" {
			_, _, _ = g.oauthPost(ctx, cfg, cfg.RevokeURL, url.Values{"token": {rt}, "token_type_hint": {"refresh_token"}})
		}
		if at, _ := openSecret(gr.AccessTokenEnc, "access:"+c.ID); at != "" {
			_, _, _ = g.oauthPost(ctx, cfg, cfg.RevokeURL, url.Values{"token": {at}, "token_type_hint": {"access_token"}})
		}
	}
	return g.DB.Where("connection_id = ?", c.ID).Delete(&model.FlowMCPOAuthGrant{}).Error
}

func (g MCPGateway) OAuthStatus(scope Scope, connID string) (gin.H, error) {
	c, err := g.get(scope, connID)
	if err != nil {
		return nil, err
	}
	var gr model.FlowMCPOAuthGrant
	if err := g.DB.First(&gr, "connection_id = ? AND tenant_id = ?", c.ID, c.TenantID).Error; err != nil {
		return gin.H{"connected": false}, nil
	}
	return gin.H{"connected": true, "expiresAt": gr.ExpiresAt, "scope": gr.Scope}, nil
}

// RegisterMCPOAuthRoutes mounts the interactive OAuth endpoints (admins only).
func RegisterMCPOAuthRoutes(api gin.IRouter, db *gorm.DB, gw *MCPGateway, user UserFunc) {
	scoped := func(c *gin.Context, db *gorm.DB) (Scope, string, bool) {
		u, err := user(c)
		if err != nil || u == nil {
			reply(c, http.StatusUnauthorized, nil, "unauthorized")
			return Scope{}, "", false
		}
		s, err := ResolveScope(db, u.ID, c.GetHeader("X-Workspace-Id"))
		if err != nil {
			reply(c, http.StatusNotFound, nil, "workspace not found")
			return Scope{}, "", false
		}
		return s, u.ID, true
	}
	fail := func(c *gin.Context, err error) {
		switch {
		case errors.Is(err, ErrForbidden):
			reply(c, http.StatusForbidden, nil, "forbidden")
		case errors.Is(err, ErrMCPNotFound):
			reply(c, http.StatusNotFound, nil, "not found")
		default:
			reply(c, http.StatusBadRequest, nil, err.Error())
		}
	}
	g := api.Group("/intelligence/mcp")
	g.POST("/connections/:id/oauth/start", func(c *gin.Context) {
		db := reqDB(c, db)
		s, uid, ok := scoped(c, db)
		if !ok {
			return
		}
		u, err := gw.withDB(db).OAuthStart(s, uid, c.Param("id"))
		if err != nil {
			fail(c, err)
			return
		}
		reply(c, http.StatusOK, gin.H{"authorizeUrl": u}, "ok")
	})
	g.GET("/connections/:id/oauth", func(c *gin.Context) {
		db := reqDB(c, db)
		s, _, ok := scoped(c, db)
		if !ok {
			return
		}
		st, err := gw.withDB(db).OAuthStatus(s, c.Param("id"))
		if err != nil {
			fail(c, err)
			return
		}
		reply(c, http.StatusOK, st, "ok")
	})
	g.POST("/connections/:id/oauth/revoke", func(c *gin.Context) {
		db := reqDB(c, db)
		s, _, ok := scoped(c, db)
		if !ok {
			return
		}
		if err := gw.withDB(db).OAuthRevoke(c.Request.Context(), s, c.Param("id")); err != nil {
			fail(c, err)
			return
		}
		reply(c, http.StatusOK, gin.H{"revoked": true}, "ok")
	})
	// Browser redirect target: the user's session cookie must match the state's user.
	g.GET("/oauth/callback", func(c *gin.Context) {
		db := reqDB(c, db)
		u, err := user(c)
		if err != nil || u == nil {
			c.Redirect(http.StatusFound, "/intelligence/mcp?oauth=login_required")
			return
		}
		if e := c.Query("error"); e != "" {
			c.Redirect(http.StatusFound, "/intelligence/mcp?oauth=denied")
			return
		}
		if _, err := gw.withDB(db).OAuthCallback(c.Request.Context(), u.ID, c.Query("state"), c.Query("code")); err != nil {
			c.Redirect(http.StatusFound, "/intelligence/mcp?oauth=failed")
			return
		}
		c.Redirect(http.StatusFound, "/intelligence/mcp?oauth=connected")
	})
}
