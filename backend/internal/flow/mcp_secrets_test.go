package flow

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yingce/backend/internal/model"

	"github.com/gin-gonic/gin"
)

func testKEK(t *testing.T, seed byte) SecretKEK {
	k, err := ParseKEK(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{seed}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSecretEnvelopeBindsIdentityAndKey(t *testing.T) {
	kek := testKEK(t, 7)
	ct, wk, err := SealSecret(kek, "tn_a", "ws_a", "sec_1", testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte(testSecret)) || bytes.Contains(wk, kek.Key) {
		t.Fatal("ciphertext or wrapped key contains plaintext")
	}
	row := model.FlowMCPSecret{ID: "sec_1", TenantID: "tn_a", WorkspaceID: "ws_a", Ciphertext: ct, WrappedDEK: wk, KeyID: kek.ID}
	if v, err := OpenSecret(kek, row); err != nil || v != testSecret {
		t.Fatalf("open: %q %v", v, err)
	}
	moved := row
	moved.TenantID = "tn_b"
	if _, err := OpenSecret(kek, moved); err == nil {
		t.Fatal("a row copied to another tenant must not decrypt")
	}
	if _, err := OpenSecret(testKEK(t, 8), row); err == nil {
		t.Fatal("wrong KEK must not decrypt")
	}
	if _, err := ParseKEK("c2hvcnQ="); err == nil {
		t.Fatal("short KEK must be rejected")
	}
}

func TestTenantSavedSecretDrivesMCPGatewayAndNeverLeaks(t *testing.T) {
	tools, _, _, conv, ss := setupAgent(t)
	_, endpoint := startMockMCP(t)
	kek := testKEK(t, 3)
	gw := &MCPGateway{DB: tools.DB, AllowInsecure: true, KEK: func() (SecretKEK, error) { return kek, nil }}
	tools.MCP = gw
	sec, err := gw.secrets().Create(ss.Scope, "u_alice", MCPSecretInput{Name: "companies token", Value: testSecret})
	if err != nil {
		t.Fatal(err)
	}
	var raw model.FlowMCPSecret
	tools.DB.First(&raw, "id = ?", sec.ID)
	if bytes.Contains(raw.Ciphertext, []byte(testSecret)) || len(raw.WrappedDEK) == 0 {
		t.Fatal("secret must be stored encrypted")
	}
	// Another workspace cannot reference it.
	if _, err := gw.Create(Scope{TenantID: ss.Scope.TenantID, WorkspaceID: "ws_other"}, "u_alice", MCPConnectionInput{Endpoint: endpoint + "/mcp", CredentialRef: "secret:" + sec.ID}); err == nil {
		t.Fatal("secret from another workspace must be rejected")
	}
	conn, err := gw.Create(ss.Scope, "u_alice", MCPConnectionInput{Name: "Companies", Endpoint: endpoint + "/mcp", CredentialRef: "secret:" + sec.ID})
	if err != nil {
		t.Fatal(err)
	}
	v, err := gw.Approve(context.Background(), ss.Scope, conn.ID, "op_admin", []string{"lookup"})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.SchemaHash) != 64 {
		t.Fatalf("schema_hash must be recorded on approval: %q", v.SchemaHash)
	}
	name := gw.AgentTools(ss.Scope)[0]["function"].(map[string]any)["name"].(string)
	r, err := tools.Invoke(context.Background(), conv, ss, "u_alice", "m1", name, map[string]any{"q": "acme"}, "")
	if err != nil || r.Status != "succeeded" || strings.Contains(string(r.Output), testSecret) {
		t.Fatalf("invoke via tenant secret: %v %+v", err, r)
	}
	// Rotation: old value no longer works against the server, new one is used.
	if _, err := gw.secrets().Rotate(ss.Scope, sec.ID, MCPSecretInput{Value: "wrong-token"}); err != nil {
		t.Fatal(err)
	}
	if r, _ := tools.Invoke(context.Background(), conv, ss, "u_alice", "m2", name, map[string]any{"q": "acme"}, ""); r.Status == "succeeded" {
		t.Fatal("rotated secret must be used on the next call")
	}
	if err := gw.secrets().Delete(ss.Scope, sec.ID); err != ErrSecretInUse {
		t.Fatalf("delete while referenced: %v", err)
	}
	// KEK missing → clear failure, env fallback still works for operator refs.
	gw.KEK = func() (SecretKEK, error) { return SecretKEK{}, ErrSecretKEK }
	if _, err := gw.secrets().Create(ss.Scope, "u", MCPSecretInput{Name: "x", Value: "y"}); err != ErrSecretKEK {
		t.Fatalf("missing KEK: %v", err)
	}
	t.Setenv("FLOW_MCP_CRED_TEST", testSecret)
	if _, err := gw.Create(ss.Scope, "u_alice", MCPConnectionInput{Endpoint: endpoint + "/mcp", CredentialRef: "env:FLOW_MCP_CRED_TEST"}); err != nil {
		t.Fatalf("env fallback: %v", err)
	}
}

func TestMCPSecretAPIAndPRDPathAlias(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tools, _, _, _, ss := setupAgent(t)
	kek := testKEK(t, 5)
	gw := &MCPGateway{DB: tools.DB, AllowInsecure: true, KEK: func() (SecretKEK, error) { return kek, nil }}
	var u model.User
	tools.DB.First(&u, "id = ?", "u_alice")
	r := gin.New()
	RegisterMCPRoutes(r.Group("/api"), tools.DB, gw, func(*gin.Context) (*model.User, error) { return &u, nil })
	call := func(method, path string, body any) (int, string) {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Workspace-Id", ss.Scope.WorkspaceID)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	code, body := call(http.MethodPost, "/api/mcp/secrets", map[string]any{"name": "tok", "value": testSecret})
	if code != http.StatusCreated || strings.Contains(body, testSecret) || !strings.Contains(body, `"ref":"secret:sec_`) {
		t.Fatalf("create: %d %s", code, body)
	}
	for _, p := range []string{"/api/mcp/secrets", "/api/intelligence/mcp/secrets"} {
		code, body = call(http.MethodGet, p, nil)
		if code != http.StatusOK || strings.Contains(body, testSecret) || strings.Contains(body, "iphertext") || strings.Contains(body, "rapped") {
			t.Fatalf("list %s: %d %s", p, code, body)
		}
	}
	for _, p := range []string{"/api/mcp/connections", "/api/intelligence/mcp/connections"} {
		if code, body := call(http.MethodGet, p, nil); code != http.StatusOK {
			t.Fatalf("%s: %d %s", p, code, body)
		}
	}
	if code, _ := call(http.MethodPost, "/api/mcp/secrets", map[string]any{"name": "tok", "value": "a\nb"}); code != http.StatusBadRequest {
		t.Fatalf("multi-line value must be rejected: %d", code)
	}
	if code, _ := call(http.MethodDelete, "/api/mcp/secrets/sec_missing", nil); code != http.StatusNotFound {
		t.Fatalf("delete missing: %d", code)
	}
}
