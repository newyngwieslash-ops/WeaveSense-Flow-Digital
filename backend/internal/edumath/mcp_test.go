package edumath

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

type bearerRT struct{ token string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func mcpSession(t *testing.T, base, token string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "edumath-test", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: base + "/mcp", HTTPClient: &http.Client{Transport: bearerRT{token}, Timeout: 10 * time.Second},
		MaxRetries: -1, DisableStandaloneSSE: true}, nil)
}

func callText(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, meta map[string]any) (map[string]any, bool) {
	t.Helper()
	p := &mcp.CallToolParams{Name: name, Arguments: args}
	if meta != nil {
		p.Meta = meta
	}
	res, err := cs.CallTool(context.Background(), p)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out map[string]any
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			_ = json.Unmarshal([]byte(tc.Text), &out)
		}
	}
	return out, res.IsError
}

// TestMCPProtocolWithGoSDK drives the service's MCP endpoint with the same Go MCP
// SDK client the platform gateway uses: discovery, keyed start, polling, reconcile.
func TestMCPProtocolWithGoSDK(t *testing.T) {
	base := startMathService(t)
	if _, err := mcpSession(t, base, "v1.bad.token"); err == nil {
		t.Fatal("invalid bearer must be rejected")
	}
	cs, err := mcpSession(t, base, MCPToken([]byte(testKey), "t_mcp", time.Hour, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 7 {
		t.Fatalf("tools: %v %d", err, len(tools.Tools))
	}
	hashes := map[string]string{}
	for _, tl := range tools.Tools {
		hashes[tl.Name] = flow.MCPToolHash(tl.Name, tl.Description, tl.InputSchema)
	}
	if hashes["math_analyze_start"] == "" || hashes["math_call_reconcile"] == "" {
		t.Fatal("expected tools missing")
	}
	prob := problem("2", "1")
	prob["family_id"] = "SOLID-ANGLE-02"
	start, isErr := callText(t, cs, "math_analyze_start", map[string]any{"problem": prob}, map[string]any{"idempotency_key": "mcp-k1"})
	if isErr || start["job_id"] == nil {
		t.Fatalf("start: %v", start)
	}
	again, _ := callText(t, cs, "math_analyze_start", map[string]any{"problem": prob}, map[string]any{"idempotency_key": "mcp-k1"})
	if again["job_id"] != start["job_id"] {
		t.Fatal("keyed start must be idempotent over MCP")
	}
	var job map[string]any
	for i := 0; i < 300; i++ {
		job, _ = callText(t, cs, "math_job_get", map[string]any{"job_id": start["job_id"]}, nil)
		if job["status"] == "succeeded" || job["status"] == "failed" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if job["status"] != "succeeded" {
		t.Fatalf("job: %v", job)
	}
	rec, _ := callText(t, cs, "math_call_reconcile", map[string]any{"idempotency_key": "mcp-k1"}, nil)
	if rec["status"] != "succeeded" {
		t.Fatalf("reconcile: %v", rec)
	}
	if rec2, _ := callText(t, cs, "math_call_reconcile", map[string]any{"idempotency_key": "never"}, nil); rec2["status"] != "not_found" {
		t.Fatalf("reconcile unknown: %v", rec2)
	}
	// Tenant isolation over MCP: another tenant's token cannot read the job.
	other, err := mcpSession(t, base, MCPToken([]byte(testKey), "t_other", time.Hour, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, isErr := callText(t, other, "math_job_get", map[string]any{"job_id": start["job_id"]}, nil); !isErr {
		t.Fatal("cross-tenant job read over MCP must fail")
	}
	if out, isErr := callText(t, cs, "math_media_render_start", map[string]any{"lesson_artifact_id": "ma_x", "media": "tts"}, nil); !isErr {
		t.Fatalf("TTS without provider must report an error: %v", out)
	}
}

// TestEducationMathPackInstallAndPin: the education-math pack installs into a
// workspace and runs freeze the skill hash (upgrade-safe).
func TestEducationMathPackInstallAndPin(t *testing.T) {
	db := testDB(t)
	sc, err := flow.ResolveScope(db, "u_alice", "")
	if err != nil {
		t.Fatal(err)
	}
	ps, _ := flow.SkillPacks()
	var pack flow.SkillPack
	for _, p := range ps {
		if p.ID == "education-math" {
			pack = p
		}
	}
	if len(pack.Skills) != 4 || len(pack.MCPRequirements) != 1 || pack.MCPRequirements[0] != "math-service" {
		t.Fatalf("education-math pack: %+v", pack)
	}
	for _, s := range pack.Skills {
		if _, err := flow.InstallSkill(db, sc, "u_alice", s.ID, ""); err != nil {
			t.Fatal(err)
		}
	}
	s, err := flow.LoadSkillForRun(db, sc, "run_edu_1", "edu-math-analysis")
	if err != nil || s.Hash == "" {
		t.Fatalf("load: %v", err)
	}
	var pin model.FlowSkillRunPin
	if db.First(&pin, "run_id = ? AND skill_id = ?", "run_edu_1", "edu-math-analysis").Error != nil || pin.Hash != s.Hash {
		t.Fatal("run must pin the skill hash")
	}
}
