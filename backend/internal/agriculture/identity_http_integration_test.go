package agriculture

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"yingce/backend/internal/auth"
	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
)

// This suite requires an explicit interpreter, never substitutes a mock when
// Python dependencies are absent. CI must set AGRI_IDENTITY_PYTHON.
func TestIdentityRealHTTPGoPython(t *testing.T) {
	python := os.Getenv("AGRI_IDENTITY_PYTHON")
	if python == "" {
		t.Skip("set AGRI_IDENTITY_PYTHON for required cross-language integration")
	}
	python, err := filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	s, org := fixture(t)
	join(t, s, org, "bob", "viewer")
	if err = s.DB.AutoMigrate(&model.AuthSession{}, &model.UserIdentity{}); err != nil {
		t.Fatal(err)
	}
	password := auth.RandomToken()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal("hash fixture password")
	}
	if err = s.DB.Model(&model.User{}).Where("id = ?", "bob").Update("password_hash", hash).Error; err != nil {
		t.Fatal(err)
	}
	authentication := auth.New(repository.New(s.DB), nil, nil)
	login, err := authentication.Login(auth.LoginRequest{Username: "bob", Password: password})
	if err != nil {
		t.Fatal("real fixture login failed")
	}
	actor, err := authentication.CurrentUser(login.Session)
	if err != nil || actor.ID != "bob" {
		t.Fatal("trusted session failed")
	}
	if _, err = authentication.CurrentUser(login.Session + "forged"); err == nil {
		t.Fatal("forged session accepted")
	}
	now := time.Now().UTC().Truncate(time.Second)
	newKey := func(id string) (*ServiceKeyring, ServiceIdentityKey) {
		pub, priv, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		k := ServiceIdentityKey{ID: id, PublicKey: pub, PrivateKey: priv, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)}
		ring, e := NewServiceKeyring([]ServiceIdentityKey{k}, id)
		if e != nil {
			t.Fatal(e)
		}
		return ring, k
	}
	platformKeys, platformKey := newKey("platform_fixture")
	revokedKeys, revokedKey := newKey("revoked_fixture")
	gisKeys, gisKey := newKey("gis_fixture")
	var callbacks atomic.Int64
	router := gin.New()
	router.Use(func(c *gin.Context) { callbacks.Add(1); c.Next() })
	s.RegisterInternalAuthority(router, gisKeys)
	authority := httptest.NewServer(router)
	t.Cleanup(authority.Close)
	config := map[string]any{"tenant": org.Tenant.ID, "workspace": org.Workspace.ID, "platform": authority.URL,
		"platform_keys": []any{map[string]any{"kid": platformKey.ID, "public_key": base64.StdEncoding.EncodeToString(platformKey.PublicKey), "not_before": platformKey.NotBefore.Format(time.RFC3339), "not_after": platformKey.NotAfter.Format(time.RFC3339), "revoked": false}},
		"callback_key":  map[string]any{"kid": gisKey.ID, "seed": base64.StdEncoding.EncodeToString(gisKey.PrivateKey.Seed()), "not_before": gisKey.NotBefore.Format(time.RFC3339), "not_after": gisKey.NotAfter.Format(time.RFC3339)}}
	config["platform_keys"] = append(config["platform_keys"].([]any), map[string]any{"kid": revokedKey.ID, "public_key": base64.StdEncoding.EncodeToString(revokedKey.PublicKey), "not_before": revokedKey.NotBefore.Format(time.RFC3339), "not_after": revokedKey.NotAfter.Format(time.RFC3339), "revoked": true})
	raw, _ := json.Marshal(config)
	configPath := filepath.Join(t.TempDir(), "identity.json")
	if err = os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, filepath.Join(root, "services/agri-gis/backend/tests/identity_http_fixture.py"), configPath)
	cmd.Dir = filepath.Join(root, "services/agri-gis/backend")
	cmd.Env = append(os.Environ(), "PYTHONPATH="+cmd.Dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	// Child stderr is deliberately not printed: no accidental credential traces.
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		t.Fatal("Python fixture process failed to start")
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	ports := make(chan string, 1)
	go func() {
		scan := bufio.NewScanner(stdout)
		if scan.Scan() {
			ports <- scan.Text()
		} else {
			ports <- ""
		}
	}()
	var port string
	select {
	case port = <-ports:
	case <-time.After(10 * time.Second):
		t.Fatal("Python fixture readiness deadline")
	}
	p, e := strconv.Atoi(port)
	if e != nil || p < 1 || p > 65535 {
		t.Fatal("Python fixture failed before readiness; validate interpreter dependencies")
	}
	base := "http://127.0.0.1:" + port
	client := &http.Client{Timeout: 3 * time.Second}
	ready := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		response, e := client.Get(base + "/health")
		if e == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !ready {
		t.Fatal("real FastAPI health unavailable")
	}
	gis, err := NewGISClient(base, platformKeys)
	if err != nil {
		t.Fatal(err)
	}
	s.Ownership = gis
	binding, err := s.GrantResourceBinding(context.Background(), "owner", org.Workspace.ID, actor.ID, ResourceRef{"farm", "farm_a"}, []string{"agri.farm.read"}, nil, revisionPointer(0))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := s.Authorize(context.Background(), actor.ID, org.Workspace.ID, "agri.farm.read", []ResourceRef{{"farm", "farm_a"}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/internal/agri/v1/delegation/check"
	output, err := gis.CallAuthorized(context.Background(), s, actor.ID, decision, "GET", path, nil, "")
	if err != nil || !bytes.Contains(output, []byte(`"verified":true`)) {
		t.Fatal("real signed round trip failed")
	}
	if callbacks.Load() == 0 {
		t.Fatal("no actual reverse authority HTTP request")
	}
	token, _, err := s.issueDelegation(context.Background(), platformKeys, actor.ID, decision, "GET", path, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	request := func(name, method, target, body, ticket string, want int) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			req, e := http.NewRequest(method, base+target, strings.NewReader(body))
			if e != nil {
				t.Fatal("request construction")
			}
			req.Header.Set("Authorization", "Bearer "+ticket)
			res, e := client.Do(req)
			if e != nil {
				t.Fatal("actual HTTP request failed")
			}
			defer res.Body.Close()
			_, _ = io.Copy(io.Discard, res.Body)
			if res.StatusCode != want {
				t.Fatalf("status %d; want %d", res.StatusCode, want)
			}
		})
	}
	request("valid_read", "GET", path, "", token, 200)
	wrongResourcePath := "/internal/agri/v1/probe/farm_b"
	wrongResourceToken, _, err := s.issueDelegation(context.Background(), platformKeys, actor.ID, decision, "GET", wrongResourcePath, nil, "")
	if err != nil {
		t.Fatal("signed scope-path mismatch fixture issuance failed")
	}
	request("scope_path_mismatch", "GET", wrongResourcePath, "", wrongResourceToken, 403)
	request("signature", "GET", path, "", token[:len(token)-4]+"AAAA", 401)
	request("method", "POST", path, "", token, 401)
	request("path", "GET", "/internal/agri/v1/delegation/other", "", token, 401)
	request("query", "GET", path+"?tenant_id=forged", "", token, 401)
	request("body", "GET", path, `{"resources":[]}`, token, 401)
	writeToken, _, err := s.issueDelegation(context.Background(), platformKeys, actor.ID, decision, "POST", path, []byte(`{"resources":[{"kind":"farm","id":"farm_a"}]}`), "operation_fixture")
	if err != nil {
		t.Fatal("test write binding issuance failed")
	}
	request("write_payload_hash", "POST", path, `{"resources":[{"kind":"farm","id":"farm_b"}]}`, writeToken, 401)
	var claims delegationClaims
	if _, err = platformKeys.verify(token, delegationType, time.Now(), &claims); err != nil {
		t.Fatal(err)
	}
	mutated := func(change func(*delegationClaims)) string {
		c := claims
		change(&c)
		ticket, e := platformKeys.sign(delegationType, c, time.Now(), time.Now().Add(30*time.Second))
		if e != nil {
			t.Fatal("test negative signing failed")
		}
		return ticket
	}
	request("audience", "GET", path, "", mutated(func(c *delegationClaims) { c.Audience = "flow-platform" }), 401)
	request("expired", "GET", path, "", mutated(func(c *delegationClaims) {
		c.IssuedAt = time.Now().Add(-40 * time.Second).UTC().Format(time.RFC3339)
		c.ExpiresAt = time.Now().Add(-10 * time.Second).UTC().Format(time.RFC3339)
	}), 401)
	revokedTicket, err := revokedKeys.sign(delegationType, claims, time.Now(), time.Now().Add(30*time.Second))
	if err != nil {
		t.Fatal("test revoked-key signing failed")
	}
	request("revoked_signing_key", "GET", path, "", revokedTicket, 401)
	request("actor", "GET", path, "", mutated(func(c *delegationClaims) { c.Actor.ID = "carol" }), 403)
	request("epoch", "GET", path, "", mutated(func(c *delegationClaims) { c.AuthorizationVersion++ }), 403)
	if err = s.RevokeBinding(context.Background(), "owner", org.Workspace.ID, binding.ID, revisionPointer(binding.AuthVersion)); err != nil {
		t.Fatal(err)
	}
	request("revoked_binding_old_ticket", "GET", path, "", token, 403)
	if _, err = gis.CallAuthorized(context.Background(), s, actor.ID, decision, "GET", path, nil, ""); err == nil {
		t.Fatal("revoked decision reissued")
	}
	if err = authentication.Logout(login.Session); err != nil {
		t.Fatal(err)
	}
	if _, err = authentication.CurrentUser(login.Session); err == nil {
		t.Fatal("revoked session remained valid")
	}
	t.Log(fmt.Sprintf("real HTTP verified; authority callbacks=%d; fixture ownership only; no production GIS acceptance", callbacks.Load()))
}
