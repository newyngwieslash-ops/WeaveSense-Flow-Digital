package flow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestMCPInternalTargetsParse(t *testing.T) {
	bad := []string{
		`[{"endpoint":"http://169.254.169.254:80/mcp","tools":["x"]}]`,
		`[{"endpoint":"http://metadata.google.internal:80/mcp","tools":["x"]}]`,
		`[{"endpoint":"http://10.0.0.5/mcp","tools":["x"]}]`,
		`[{"endpoint":"http://10.0.0.5:8080/mcp?x=1","tools":["x"]}]`,
		`[{"endpoint":"http://u:p@10.0.0.5:8080/mcp","tools":["x"]}]`,
		`[{"endpoint":"http://10.0.0.5:8080/mcp","tools":[]}]`,
		`[{"endpoint":"ftp://10.0.0.5:8080/mcp","tools":["x"]}]`,
		`[{"endpoint":"http://10.0.0.5:8080/mcp","tools":["x"],"credentialRef":"secret:abc"}]`,
		`not json`,
	}
	for _, raw := range bad {
		if _, err := ParseInternalTargets(raw); err == nil {
			t.Fatalf("must reject %s", raw)
		}
	}
	got, err := ParseInternalTargets(`[{"endpoint":"http://math-service:8090/mcp","tools":["math_job_get"],"credentialRef":"mint:edumath"}]`)
	if err != nil || len(got) != 1 || got[0].Endpoint != "http://math-service:8090/mcp" {
		t.Fatalf("parse: %v %+v", err, got)
	}
	if none, err := ParseInternalTargets(""); err != nil || none != nil {
		t.Fatal("empty config means no internal targets")
	}
}

func TestMCPInternalTargetExactAllowlist(t *testing.T) {
	t.Setenv("FLOW_MCP_CRED_TEST", testSecret)
	tools, _, _, conv, ss := setupAgent(t)
	m, endpoint := startMockMCP(t) // loopback http: non-public
	target := endpoint + "/mcp"
	gw := &MCPGateway{DB: tools.DB} // production mode: AllowInsecure false
	tools.MCP = gw
	in := MCPConnectionInput{Name: "Math", Endpoint: target, CredentialRef: "env:FLOW_MCP_CRED_TEST"}
	if _, err := gw.Create(ss.Scope, "u_alice", in); err == nil {
		t.Fatal("http loopback endpoint must be refused without an exact internal target")
	}
	gw.Internal = []InternalTarget{{Endpoint: target, Tools: []string{"lookup"}}}
	if _, err := gw.Create(ss.Scope, "u_alice", MCPConnectionInput{Name: "x", Endpoint: endpoint + "/mcp2", CredentialRef: in.CredentialRef}); err == nil {
		t.Fatal("only the exact endpoint string is admitted")
	}
	conn, err := gw.Create(ss.Scope, "u_alice", in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gw.Approve(context.Background(), ss.Scope, conn.ID, "op", []string{"lookup", "delete_everything"}); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("tools outside the target's exact list cannot be approved: %v", err)
	}
	if _, err := gw.Approve(context.Background(), ss.Scope, conn.ID, "op", []string{"lookup"}); err != nil {
		t.Fatal(err)
	}
	at := gw.AgentTools(ss.Scope)
	if len(at) != 1 {
		t.Fatalf("agent tools: %+v", at)
	}
	name := at[0]["function"].(map[string]any)["name"].(string)
	r, err := tools.Invoke(context.Background(), conv, ss, "u_alice", "i1", name, map[string]any{"q": "acme"}, "")
	if err != nil || r.Status != "succeeded" {
		t.Fatalf("internal call: %v %+v", err, r)
	}
	if len(m.authSeen) == 0 {
		t.Fatal("credential must be injected")
	}
	// Shrinking the deployment allowlist takes effect without re-approval.
	gw.Internal = []InternalTarget{{Endpoint: target, Tools: []string{"other"}}}
	if len(gw.AgentTools(ss.Scope)) != 0 {
		t.Fatal("a tool removed from the target list is no longer offered")
	}
	if _, _, err := gw.Execute(context.Background(), ss.Scope, "u_alice", conn.ID, "lookup", map[string]any{"q": "a"}); err == nil {
		t.Fatal("a tool removed from the target list must not execute")
	}
	// Removing the target entirely: the http endpoint is refused before any dial.
	gw.Internal = nil
	if _, _, err := gw.Execute(context.Background(), ss.Scope, "u_alice", conn.ID, "lookup", map[string]any{"q": "a"}); err == nil {
		t.Fatal("a removed internal target must not be reachable")
	}
}

func TestMCPInternalTargetRefusesRedirectAndMintsTenantCredential(t *testing.T) {
	tools, _, _, _, ss := setupAgent(t)
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	target := srv.URL + "/mcp"
	gw := &MCPGateway{DB: tools.DB, Internal: []InternalTarget{{Endpoint: target, Tools: []string{"lookup"}, CredentialRef: "mint:edumath"}}}
	if _, err := gw.Create(ss.Scope, "u_alice", MCPConnectionInput{Name: "m", Endpoint: target, CredentialRef: "mint:edumath"}); err == nil {
		t.Fatal("mint refs need a configured minter")
	}
	gw.Mint = func(_ context.Context, ref, tenant string) (string, error) { return "minted-for-" + tenant, nil }
	if _, err := gw.Create(ss.Scope, "u_alice", MCPConnectionInput{Name: "m", Endpoint: target, CredentialRef: "env:FLOW_MCP_CRED_TEST"}); err == nil {
		t.Fatal("the target's required credential ref is enforced")
	}
	if _, err := gw.Create(ss.Scope, "u_alice", MCPConnectionInput{Name: "m", Endpoint: "https://example.com/mcp", CredentialRef: "mint:edumath"}); err == nil {
		t.Fatal("minted credentials are never sent to non-internal endpoints")
	}
	conn, err := gw.Create(ss.Scope, "u_alice", MCPConnectionInput{Name: "m", Endpoint: target, CredentialRef: "mint:edumath"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gw.Approve(context.Background(), ss.Scope, conn.ID, "op", []string{"lookup"}); err == nil {
		t.Fatal("redirect must not be followed")
	}
	if len(seen) == 0 || seen[0] != "Bearer minted-for-"+ss.Scope.TenantID {
		t.Fatalf("tenant-bound minted credential expected: %v", seen)
	}
}

type fakeMath struct {
	enabled bool
	calls   []string
}

func (f *fakeMath) Enabled(*gorm.DB, Scope) bool { return f.enabled }
func (f *fakeMath) Call(_ context.Context, _ *gorm.DB, _ Scope, userID, tool string, args map[string]any, key string) (any, error) {
	f.calls = append(f.calls, tool+"|"+key)
	return map[string]any{"job_id": "j1", "status": "running", "done": false}, nil
}

func TestPlatformMathToolsGatedAndKeyed(t *testing.T) {
	tools, _, _, conv, ss := setupAgent(t)
	fm := &fakeMath{}
	tools.MCP = &MCPGateway{DB: tools.DB, Math: fm}
	if len(tools.MCP.AgentTools(ss.Scope)) != 0 {
		t.Fatal("math tools are hidden unless the education pack is installed")
	}
	if _, err := tools.Invoke(context.Background(), conv, ss, "u_alice", "c0", MathToolPrefix+"math_job_wait", map[string]any{"job_id": "j1"}, ""); err != nil {
		t.Fatalf("invoke on disabled workspace returns a failed result, not a call: %v", err)
	}
	if len(fm.calls) != 0 {
		t.Fatal("adapter must not be called when disabled")
	}
	fm.enabled = true
	at := tools.MCP.AgentTools(ss.Scope)
	if len(at) != len(MathToolCatalog) {
		t.Fatalf("tools: %d", len(at))
	}
	for _, x := range at {
		if !IsMathToolName(x["function"].(map[string]any)["name"].(string)) {
			t.Fatal("prefix")
		}
	}
	name := MathToolPrefix + "math_analyze_start"
	r, err := tools.Invoke(context.Background(), conv, ss, "u_alice", "c1", name, map[string]any{"problem_revision_id": "pr1"}, "")
	if err != nil || r.Status != "succeeded" {
		t.Fatalf("%v %+v", err, r)
	}
	r2, _ := tools.Invoke(context.Background(), conv, ss, "u_alice", "c1", name, map[string]any{"problem_revision_id": "pr1"}, "")
	if !r2.Replayed || len(fm.calls) != 1 {
		t.Fatalf("replay must not start another job: %+v %v", r2, fm.calls)
	}
	if fm.calls[0] != "math_analyze_start|tool:"+conv.ID+":c1" {
		t.Fatalf("idempotency key is the tool call: %v", fm.calls)
	}
	if _, err := tools.Invoke(context.Background(), conv, ss, "u_alice", "c2", name, map[string]any{"problem_revision_id": "pr1", "extra": 1}, ""); err == nil {
		t.Fatal("arguments are validated against the exact schema")
	}
	if _, err := tools.Invoke(context.Background(), conv, ss, "u_alice", "c3", MathToolPrefix+"math_publish", map[string]any{}, ""); err == nil {
		t.Fatal("only the exact catalog is callable")
	}
}
