package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"yingce/backend/internal/model"
)

// OAuthClientCredentials resolves "oauth:FLOW_MCP_OAUTH_<NAME>" credential refs
// with the OAuth 2.0 client-credentials grant. Configuration lives only in the
// server environment (<NAME>_TOKEN_URL, _CLIENT_ID, _CLIENT_SECRET, optional
// _SCOPE / _AUDIENCE); access tokens are cached server-side until shortly
// before expiry and are never returned by any API.
type OAuthClientCredentials struct {
	DB   *gorm.DB
	HTTP *http.Client
	Now  func() time.Time
}

func (o OAuthClientCredentials) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now().UTC()
}

func (o OAuthClientCredentials) Token(ctx context.Context, ref string) (string, error) {
	name, ok := strings.CutPrefix(ref, "oauth:")
	if !ok || !strings.HasPrefix(name, "FLOW_MCP_OAUTH_") {
		return "", fmt.Errorf("%w: oauth ref must be oauth:FLOW_MCP_OAUTH_*", ErrMCPInvalid)
	}
	if o.DB != nil {
		var row model.FlowOAuthToken
		if o.DB.First(&row, "credential_ref = ?", ref).Error == nil && row.ExpiresAt.After(o.now().Add(60*time.Second)) {
			return row.AccessToken, nil
		}
	}
	tokenURL, id, secret := os.Getenv(name+"_TOKEN_URL"), os.Getenv(name+"_CLIENT_ID"), os.Getenv(name+"_CLIENT_SECRET")
	if tokenURL == "" || id == "" || secret == "" {
		return "", fmt.Errorf("%w: oauth credential %s is not configured", ErrMCPInvalid, name)
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	if v := os.Getenv(name + "_SCOPE"); v != "" {
		form.Set("scope", v)
	}
	if v := os.Getenv(name + "_AUDIENCE"); v != "" {
		form.Set("audience", v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(url.QueryEscape(id), url.QueryEscape(secret))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	hc := o.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("oauth token request failed")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oauth token endpoint returned %d", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if json.Unmarshal(raw, &tok) != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("oauth token endpoint returned no access_token")
	}
	if tok.ExpiresIn <= 0 {
		tok.ExpiresIn = 300
	}
	if o.DB != nil {
		o.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(&model.FlowOAuthToken{CredentialRef: ref, AccessToken: tok.AccessToken, ExpiresAt: o.now().Add(time.Duration(tok.ExpiresIn) * time.Second)})
	}
	return tok.AccessToken, nil
}
