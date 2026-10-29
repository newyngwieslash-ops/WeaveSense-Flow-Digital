package agriculture

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"yingce/backend/internal/model"
)

func TestGISClientRealHTTPServiceAndDelegation(t *testing.T) {
	s, decision, keys, _ := delegationFixture(t)
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	var ownershipCalls, delegationCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch r.URL.Path {
		case ownershipResolvePath:
			claims, err := verifyServiceAssertion(keys, "ownership.resolve", token, r.Method, r.URL.Path, body, s.now())
			if err != nil {
				t.Error("invalid service assertion on wire")
				w.WriteHeader(401)
				return
			}
			ownershipCalls.Add(1)
			response := struct {
				Resources []ownershipWire `json:"resources"`
			}{[]ownershipWire{{Resource: ResourceRef{"farm", "farm_a"}, TenantID: claims.TenantID, WorkspaceID: claims.WorkspaceID, FarmIDs: []string{"farm_a"}, Revision: 1, Status: "active", ReceiptID: "http_fixture_receipt", IssuedAt: s.now().Truncate(time.Second).Format("2006-01-02T15:04:05Z"), ExpiresAt: s.now().Add(30 * time.Second).Truncate(time.Second).Format("2006-01-02T15:04:05Z")}}}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
		case "/internal/agri/v1/delegation/check":
			claims, err := verifyDelegation(keys, token, r.Method, r.URL.Path, body, s.now())
			if err != nil || claims.Actor.ID != "bob" {
				t.Error("invalid user delegation on wire")
				w.WriteHeader(401)
				return
			}
			delegationCalls.Add(1)
			_, _ = w.Write([]byte(`{"accepted":true}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client, err := NewGISClient(server.URL, keys)
	if err != nil {
		t.Fatal(err)
	}
	client.now = s.now
	proofs, err := client.Resolve(context.Background(), OwnershipContext{TenantID: decision.TenantID, WorkspaceID: decision.WorkspaceID}, []ResourceRef{{"farm", "farm_a"}})
	if err != nil || len(proofs) != 1 || proofs[0].Resource.ID != "farm_a" {
		t.Fatalf("ownership HTTP result: %v", err)
	}
	s.Ownership = client
	raw, err := client.CallAuthorized(context.Background(), s, "bob", decision, "POST", "/internal/agri/v1/delegation/check", []byte(`{"z":2,"a":1}`), "operation_http")
	if err != nil || string(raw) != `{"accepted":true}` || ownershipCalls.Load() != 2 || delegationCalls.Load() != 1 {
		t.Fatalf("trusted HTTP chain failed: %v", err)
	}
	if _, err := client.CallAuthorized(context.Background(), s, "bob", decision, "POST", "https://attacker.invalid/path", []byte(`{}`), "operation_http"); err == nil {
		t.Fatal("caller URL accepted")
	}
}

func TestGISClientRejectsRedirectsTimeoutOversizeAndUnsafeConfig(t *testing.T) {
	now := time.Now().UTC()
	keys, _ := identityTestKeys(t, now, 0x41)
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "")
	for _, value := range []string{"http://127.0.0.1:8080", "http://public.example", "https://u:p@public.example", "https://public.example/?q=1", "https://public.example/#fragment", "https://public.example/base"} {
		if _, err := NewGISClient(value, keys); err == nil {
			t.Fatalf("unsafe deployment origin accepted: %s", value)
		}
	}
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	var destinationCalled atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalled.Store(true); w.WriteHeader(200) }))
	defer destination.Close()
	for _, mode := range []string{"redirect", "timeout", "oversize", "secret_error"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "redirect":
					http.Redirect(w, r, destination.URL, 307)
				case "timeout":
					time.Sleep(100 * time.Millisecond)
				case "oversize":
					_, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
				case "secret_error":
					w.WriteHeader(500)
					_, _ = w.Write([]byte("secret internal bearer and SQL"))
				}
			}))
			defer server.Close()
			client, err := NewGISClient(server.URL, keys)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "timeout" {
				client.client.Timeout = 10 * time.Millisecond
			}
			_, err = client.Resolve(context.Background(), OwnershipContext{TenantID: "tenant_a", WorkspaceID: "workspace_a"}, []ResourceRef{{"farm", "farm_a"}})
			if !errors.Is(err, ErrDependencyUnavailable) || strings.Contains(err.Error(), "secret") || destinationCalled.Load() {
				t.Fatalf("transport did not fail closed/redact: %v", err)
			}
		})
	}
}

func TestGISClientRequiresTLSForEveryNonLoopbackPeer(t *testing.T) {
	now := time.Now().UTC()
	keys, _ := identityTestKeys(t, now, 0x41)
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "10.1.2.3,gis.internal,localhost,127.0.0.1,::1")
	for _, value := range []string{"http://10.1.2.3:8080", "http://gis.internal:8080", "http://localhost:8080"} {
		if _, err := NewGISClient(value, keys); !errors.Is(err, ErrServiceIdentity) {
			t.Fatalf("allowlist bypassed TLS identity requirement: %s %v", value, err)
		}
	}
	for _, value := range []string{"https://10.1.2.3:8443", "https://localhost:8443", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		if _, err := NewGISClient(value, keys); err != nil {
			t.Fatalf("explicit TLS/loopback origin rejected: %s %v", value, err)
		}
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"accepted":true}`)) }))
	defer server.Close()
	client, err := NewGISClient(server.URL, keys)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.send(context.Background(), ownershipResolvePath, []byte(`{}`), "test-only-ticket"); !errors.Is(err, ErrDependencyUnavailable) {
		t.Fatal("untrusted TLS server certificate accepted")
	}
	// Trust only this explicit fixture CA, retaining the real platform outbound
	// dialer and hostname verification. Production never sets InsecureSkipVerify.
	transport := client.client.Transport.(*http.Transport).Clone()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	client.client.Transport = transport
	defer transport.CloseIdleConnections()
	if raw, err := client.send(context.Background(), ownershipResolvePath, []byte(`{}`), "test-only-ticket"); err != nil || string(raw) != `{"accepted":true}` {
		t.Fatalf("authenticated TLS fixture failed: %v", err)
	}
}

func TestServiceIdentityConfigurationMissingOrInvalidFailsClosed(t *testing.T) {
	s, org, _ := authorizationFixture(t)
	t.Setenv("FLOW_AGRICULTURE_ENABLED", "true")
	t.Setenv("FLOW_AGRICULTURE_ALLOW_SQLITE_DEV", "true")
	t.Setenv("FLOW_AGRICULTURE_SERVICE_IDENTITY_CONFIG", "")
	gin.SetMode(gin.TestMode)
	root := gin.New()
	if err := RegisterFromEnv(root, root.Group("/api"), s.DB, func(*gin.Context) (*model.User, error) { return &model.User{ID: "owner"}, nil }); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/agri/v1/workspaces/"+org.Workspace.ID+"/authorization", strings.NewReader(`{"action":"agri.farm.read","resources":[{"kind":"farm","id":"farm_a"}]}`))
	w := httptest.NewRecorder()
	root.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatalf("missing production provider did not fail closed: %d", w.Code)
	}
	t.Setenv("FLOW_AGRICULTURE_SERVICE_IDENTITY_CONFIG", "/nonexistent/private-secret-config")
	other := gin.New()
	err := RegisterFromEnv(other, other.Group("/api"), s.DB, func(*gin.Context) (*model.User, error) { return nil, nil })
	if !errors.Is(err, ErrServiceIdentity) || strings.Contains(err.Error(), "private-secret") {
		t.Fatal("missing file config not sanitized")
	}
}

func TestServiceIdentityConfigLoadsIndependentExternalKeysOnly(t *testing.T) {
	now := time.Now().UTC()
	_, platform := identityTestKeys(t, now, 0x41)
	_, gis := identityTestKeys(t, now, 0x42)
	toConfig := func(key ServiceIdentityKey, private bool) serviceKeyConfig {
		out := serviceKeyConfig{ID: key.ID, PublicKey: base64.StdEncoding.EncodeToString(key.PublicKey), NotBefore: key.NotBefore.Format("2006-01-02T15:04:05Z"), NotAfter: key.NotAfter.Format("2006-01-02T15:04:05Z")}
		if private {
			out.PrivateSeed = base64.StdEncoding.EncodeToString(key.PrivateKey.Seed())
		}
		return out
	}
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	config := serviceIdentityConfig{GISBaseURL: "http://127.0.0.1:8080", PlatformSigningKeyID: platform.ID, PlatformKeys: []serviceKeyConfig{toConfig(platform, true)}, GISKeys: []serviceKeyConfig{toConfig(gis, false)}}
	path := filepath.Join(t.TempDir(), "explicit-test-fixture-keys.json")
	write := func() {
		raw, _ := json.Marshal(config)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if client, verify, err := loadServiceIdentity(path); err != nil || client == nil || verify == nil {
		t.Fatalf("explicit external config rejected: %v", err)
	}
	config.GISKeys = []serviceKeyConfig{toConfig(platform, false)}
	write()
	if _, _, err := loadServiceIdentity(path); err == nil {
		t.Fatal("shared bidirectional key accepted")
	}
	config.GISKeys = []serviceKeyConfig{toConfig(gis, true)}
	write()
	if _, _, err := loadServiceIdentity(path); err == nil {
		t.Fatal("GIS private key loaded into platform")
	}
	config.GISKeys = []serviceKeyConfig{toConfig(gis, false)}
	config.PlatformKeys[0].PrivateSeed = ""
	write()
	if _, _, err := loadServiceIdentity(path); err == nil {
		t.Fatal("missing signing private key defaulted")
	}
}

func TestPlatformIdentityTemplateCannotBecomeWorkingConfiguration(t *testing.T) {
	path := filepath.Join("..", "..", "..", "deploy", "agriculture", "platform-identity.template.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config serviceIdentityConfig
	if strictIdentityJSON(raw, &config, 64<<10) != nil {
		t.Fatal("template JSON does not match actual configuration fields")
	}
	if !strings.Contains(config.PlatformKeys[0].PrivateSeed, "<") || !strings.Contains(config.GISKeys[0].PublicKey, "<") {
		t.Fatal("template contains apparent working key material")
	}
	if _, _, err := loadServiceIdentity(path); !errors.Is(err, ErrServiceIdentity) {
		t.Fatalf("placeholder template unexpectedly usable: %v", err)
	}
}
