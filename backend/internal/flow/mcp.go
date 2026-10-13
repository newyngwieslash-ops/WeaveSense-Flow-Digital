package flow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"yingce/backend/internal/model"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/gorm"
)

// MCP Gateway: operator-approved remote MCP (streamable HTTP) connections.
// Credentials are resolved server-side from CredentialRef and injected as an
// HTTP header; they never appear in prompts, skills, tool output or the API.

var (
	ErrMCPNotFound    = errors.New("flow: mcp connection not found")
	ErrMCPNotApproved = errors.New("flow: mcp connection is not approved")
	ErrMCPToolDenied  = errors.New("flow: mcp tool is not in the approved allow-list")
	ErrMCPSchemaDrift = errors.New("flow: mcp tool schema changed since approval; operator re-approval required")
	ErrMCPBudget      = errors.New("flow: mcp daily call budget exhausted")
	ErrMCPInvalid     = errors.New("flow: invalid mcp connection")
)

const mcpToolPrefix = "mcp_"

var mcpToolNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type MCPCostRule struct {
	PerCallCredits int `json:"per_call_credits"`
	MaxCallsPerDay int `json:"max_calls_per_day"`
	// ReconcileTool (operator-configured) answers {"idempotency_key": k} with
	// {"status":"succeeded","output":...} | {"status":"not_found"} for calls
	// whose outcome is unknown. Without it, unknown calls are never retried.
	ReconcileTool string `json:"reconcile_tool,omitempty"`
}

type MCPApprovedTool struct {
	Hash        string `json:"hash"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

type MCPToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Hash        string `json:"hash"`
	InputSchema any    `json:"inputSchema"`
}

type MCPConnectionInput struct {
	Name          string      `json:"name"`
	Endpoint      string      `json:"endpoint"`
	Transport     string      `json:"transport"`
	CredentialRef string      `json:"credentialRef"`
	TimeoutMS     int         `json:"timeoutMs"`
	OutputCap     int         `json:"outputCap"`
	CostRule      MCPCostRule `json:"costRule"`
}

// MCPConnectionView is the API shape: no secret, only the reference name.
type MCPConnectionView struct {
	model.FlowMCPConnection
	AllowedTools []string                   `json:"allowedTools"`
	Approved     map[string]MCPApprovedTool `json:"approvedTools"`
	CostRule     MCPCostRule                `json:"costRule"`
}

type MCPGateway struct {
	DB *gorm.DB
	// Credentials resolves a CredentialRef to the secret header value.
	Credentials func(ref string) (string, error)
	HTTP        *http.Client
	// AllowInsecure permits http:// endpoints (tests / local mocks only).
	AllowInsecure bool
	Now           func() time.Time
}

func (g MCPGateway) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now().UTC()
}

// EnvCredentials resolves "env:FLOW_MCP_CRED_*" references from the process environment.
func EnvCredentials(ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	name, ok := strings.CutPrefix(ref, "env:")
	if !ok || !strings.HasPrefix(name, "FLOW_MCP_CRED_") {
		return "", fmt.Errorf("%w: credential ref must be env:FLOW_MCP_CRED_*", ErrMCPInvalid)
	}
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("%w: credential %s is not configured", ErrMCPInvalid, name)
	}
	return v, nil
}

func (g MCPGateway) credential(ref string) (string, error) {
	if g.Credentials != nil {
		return g.Credentials(ref)
	}
	return EnvCredentials(ref)
}

func MCPToolHash(name, description string, schema any) string {
	raw, _ := json.Marshal(map[string]any{"name": name, "description": description, "inputSchema": schema})
	// Round-trip through map[string]any so key order is canonical.
	var canon any
	_ = json.Unmarshal(raw, &canon)
	raw, _ = json.Marshal(canon)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func MCPAgentToolName(slug, tool string) string {
	n := mcpToolPrefix + slug + "_" + tool
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

func IsMCPToolName(name string) bool { return strings.HasPrefix(name, mcpToolPrefix) }

func (g MCPGateway) Create(scope Scope, userID string, in MCPConnectionInput) (model.FlowMCPConnection, error) {
	u, err := url.Parse(strings.TrimSpace(in.Endpoint))
	if err != nil || u.Host == "" || u.User != nil || !(u.Scheme == "https" || (g.AllowInsecure && u.Scheme == "http")) {
		return model.FlowMCPConnection{}, fmt.Errorf("%w: endpoint must be an https URL", ErrMCPInvalid)
	}
	if in.Transport == "" {
		in.Transport = "streamable_http"
	}
	if in.Transport != "streamable_http" {
		return model.FlowMCPConnection{}, fmt.Errorf("%w: only streamable_http transport is supported", ErrMCPInvalid)
	}
	if in.CredentialRef != "" && !strings.HasPrefix(in.CredentialRef, "env:FLOW_MCP_CRED_") {
		return model.FlowMCPConnection{}, fmt.Errorf("%w: credential ref must be env:FLOW_MCP_CRED_*", ErrMCPInvalid)
	}
	clamp := func(v, def, max int) int {
		if v <= 0 {
			return def
		}
		if v > max {
			return max
		}
		return v
	}
	cost, _ := json.Marshal(in.CostRule)
	id := NewID("mcp_")
	row := model.FlowMCPConnection{ID: id, Slug: id[4:12], TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID, Name: strings.TrimSpace(in.Name),
		Endpoint: u.String(), Transport: in.Transport, CredentialRef: in.CredentialRef, AllowedTools: "[]", ApprovedSchemas: "{}",
		TimeoutMS: clamp(in.TimeoutMS, 10000, 30000), OutputCap: clamp(in.OutputCap, 16000, 64000), CostRule: string(cost),
		Status: "pending", Version: 0, CreatedBy: userID, CreatedAt: g.now(), UpdatedAt: g.now()}
	return row, g.DB.Create(&row).Error
}

func (g MCPGateway) get(scope Scope, id string) (model.FlowMCPConnection, error) {
	var c model.FlowMCPConnection
	if err := g.DB.First(&c, "id = ? AND tenant_id = ? AND workspace_id = ?", id, scope.TenantID, scope.WorkspaceID).Error; err != nil {
		return c, ErrMCPNotFound
	}
	return c, nil
}

func (g MCPGateway) View(c model.FlowMCPConnection) MCPConnectionView {
	v := MCPConnectionView{FlowMCPConnection: c}
	_ = json.Unmarshal([]byte(c.AllowedTools), &v.AllowedTools)
	_ = json.Unmarshal([]byte(c.ApprovedSchemas), &v.Approved)
	_ = json.Unmarshal([]byte(c.CostRule), &v.CostRule)
	return v
}

func (g MCPGateway) List(scope Scope) ([]MCPConnectionView, error) {
	var rows []model.FlowMCPConnection
	if err := g.DB.Where("tenant_id = ? AND workspace_id = ?", scope.TenantID, scope.WorkspaceID).Order("created_at").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]MCPConnectionView, 0, len(rows))
	for _, r := range rows {
		out = append(out, g.View(r))
	}
	return out, nil
}

type headerRT struct {
	base   http.RoundTripper
	secret string
}

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if h.secret != "" {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+h.secret)
	}
	return h.base.RoundTrip(r)
}

func (g MCPGateway) session(ctx context.Context, c model.FlowMCPConnection) (*mcp.ClientSession, string, error) {
	secret, err := g.credential(c.CredentialRef)
	if err != nil {
		return nil, "", err
	}
	// Production: public-address-only dialer (blocks loopback/private/metadata,
	// checked on the resolved socket address). Tests with AllowInsecure use loopback mocks.
	var base http.RoundTripper = PublicTransport()
	if g.AllowInsecure {
		base = http.DefaultTransport
	}
	if g.HTTP != nil && g.HTTP.Transport != nil {
		base = g.HTTP.Transport
	}
	hc := &http.Client{Transport: headerRT{base: base, secret: secret}, Timeout: time.Duration(c.TimeoutMS) * time.Millisecond}
	client := mcp.NewClient(&mcp.Implementation{Name: "weavesense-flow-mcp-gateway", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: c.Endpoint, HTTPClient: hc, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return nil, secret, fmt.Errorf("mcp connect failed: %s", redact(err.Error(), secret))
	}
	return cs, secret, nil
}

func discoverTools(ctx context.Context, cs *mcp.ClientSession) (map[string]MCPToolInfo, error) {
	out := map[string]MCPToolInfo{}
	var cursor string
	for i := 0; i < 20; i++ {
		res, err := cs.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, t := range res.Tools {
			out[t.Name] = MCPToolInfo{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema, Hash: MCPToolHash(t.Name, t.Description, t.InputSchema)}
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}

// Discover lists the server's current tools with schema hashes (for operator review).
func (g MCPGateway) Discover(ctx context.Context, scope Scope, id string) ([]MCPToolInfo, error) {
	c, err := g.get(scope, id)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutMS)*time.Millisecond)
	defer cancel()
	cs, _, err := g.session(cctx, c)
	if err != nil {
		return nil, err
	}
	defer cs.Close()
	m, err := discoverTools(cctx, cs)
	if err != nil {
		return nil, err
	}
	out := make([]MCPToolInfo, 0, len(m))
	for _, t := range m {
		out = append(out, t)
	}
	return out, nil
}

// Approve (operator) freezes the allow-list and each allowed tool's schema hash.
func (g MCPGateway) Approve(ctx context.Context, scope Scope, id, operatorID string, allowed []string) (MCPConnectionView, error) {
	c, err := g.get(scope, id)
	if err != nil {
		return MCPConnectionView{}, err
	}
	if c.Status == "revoked" {
		return MCPConnectionView{}, fmt.Errorf("%w: connection is revoked", ErrMCPInvalid)
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutMS)*time.Millisecond)
	defer cancel()
	cs, _, err := g.session(cctx, c)
	if err != nil {
		return MCPConnectionView{}, err
	}
	defer cs.Close()
	live, err := discoverTools(cctx, cs)
	if err != nil {
		return MCPConnectionView{}, err
	}
	approved := map[string]MCPApprovedTool{}
	for _, name := range allowed {
		t, ok := live[name]
		if !ok || !mcpToolNameRe.MatchString(name) {
			return MCPConnectionView{}, fmt.Errorf("%w: tool %q not offered by server", ErrMCPInvalid, name)
		}
		approved[name] = MCPApprovedTool{Hash: t.Hash, Description: t.Description, InputSchema: t.InputSchema}
	}
	al, _ := json.Marshal(allowed)
	ap, _ := json.Marshal(approved)
	if err := g.DB.Model(&c).Updates(map[string]any{"allowed_tools": string(al), "approved_schemas": string(ap), "status": "approved",
		"status_reason": "", "approved_by": operatorID, "version": c.Version + 1, "updated_at": g.now()}).Error; err != nil {
		return MCPConnectionView{}, err
	}
	c, _ = g.get(scope, id)
	return g.View(c), nil
}

func (g MCPGateway) Revoke(scope Scope, id, reason string) error {
	c, err := g.get(scope, id)
	if err != nil {
		return err
	}
	return g.DB.Model(&c).Updates(map[string]any{"status": "revoked", "status_reason": reason, "updated_at": g.now()}).Error
}

func redact(s, secret string) string {
	if secret == "" || len(secret) < 4 {
		return s
	}
	return strings.ReplaceAll(s, secret, "[redacted]")
}

func (g MCPGateway) usage(c model.FlowMCPConnection, tool, actor, status string, start time.Time, outBytes int, credits int) {
	g.DB.Create(&model.FlowMCPUsage{ID: NewID("mu_"), TenantID: c.TenantID, ConnectionID: c.ID, Tool: tool, ActorID: actor, Status: status,
		DurationMS: time.Since(start).Milliseconds(), OutputBytes: outBytes, Credits: credits, CreatedAt: g.now()})
}

// Execute runs one allowed tool. The live schema must still match the approved
// hash; any change flips the connection to needs_reapproval and blocks the call.
func (g MCPGateway) Execute(ctx context.Context, scope Scope, actorID, connID, tool string, args map[string]any) (string, bool, error) {
	return g.execute(ctx, scope, actorID, connID, tool, args, "")
}

func (g MCPGateway) execute(ctx context.Context, scope Scope, actorID, connID, tool string, args map[string]any, idemKey string) (string, bool, error) {
	start := time.Now()
	c, err := g.get(scope, connID)
	if err != nil {
		return "", false, err
	}
	v := g.View(c)
	if c.Status != "approved" {
		return "", false, fmt.Errorf("%w (status %s)", ErrMCPNotApproved, c.Status)
	}
	ap, ok := v.Approved[tool]
	if !ok {
		return "", false, ErrMCPToolDenied
	}
	if v.CostRule.MaxCallsPerDay > 0 {
		var n int64
		day := g.now().Truncate(24 * time.Hour)
		g.DB.Model(&model.FlowMCPUsage{}).Where("tenant_id = ? AND connection_id = ? AND status = ? AND created_at >= ?", c.TenantID, c.ID, "succeeded", day).Count(&n)
		if int(n) >= v.CostRule.MaxCallsPerDay {
			g.usage(c, tool, actorID, "budget_blocked", start, 0, 0)
			return "", false, ErrMCPBudget
		}
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutMS)*time.Millisecond)
	defer cancel()
	cs, secret, err := g.session(cctx, c)
	if err != nil {
		g.usage(c, tool, actorID, "failed", start, 0, 0)
		return "", false, err
	}
	defer cs.Close()
	live, err := discoverTools(cctx, cs)
	if err != nil {
		g.usage(c, tool, actorID, "failed", start, 0, 0)
		return "", false, fmt.Errorf("mcp list tools failed: %s", redact(err.Error(), secret))
	}
	if lt, ok := live[tool]; !ok || lt.Hash != ap.Hash {
		g.DB.Model(&c).Updates(map[string]any{"status": "needs_reapproval", "status_reason": "schema changed for tool " + tool, "updated_at": g.now()})
		g.usage(c, tool, actorID, "blocked_schema_drift", start, 0, 0)
		return "", false, ErrMCPSchemaDrift
	}
	params := &mcp.CallToolParams{Name: tool, Arguments: args}
	if idemKey != "" {
		// Lets servers dedupe and answer the reconcile lookup for this key.
		params.Meta = mcp.Meta{"idempotency_key": idemKey}
	}
	res, err := cs.CallTool(cctx, params)
	if err != nil {
		// The request may have reached the server: outcome unknown.
		g.usage(c, tool, actorID, "unknown", start, 0, 0)
		return "", false, fmt.Errorf("%w: %s", errMCPSent, redact(err.Error(), secret))
	}
	var b strings.Builder
	for _, ct := range res.Content {
		if t, ok := ct.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
			b.WriteString("\n")
		} else {
			b.WriteString("[non-text content omitted]\n")
		}
	}
	if res.StructuredContent != nil {
		raw, _ := json.Marshal(res.StructuredContent)
		b.Write(raw)
	}
	out := redact(b.String(), secret)
	truncated := false
	if r := []rune(out); len(r) > c.OutputCap {
		out, truncated = string(r[:c.OutputCap]), true
	}
	status := "succeeded"
	if res.IsError {
		status = "tool_error"
	}
	g.usage(c, tool, actorID, status, start, len(out), v.CostRule.PerCallCredits)
	if res.IsError {
		return out, truncated, fmt.Errorf("mcp tool returned an error: %s", out)
	}
	return out, truncated, nil
}

// AgentTools renders approved tools of approved connections as Pi function tools.
// Schemas come from the approval snapshot, never from the live server.
func (g MCPGateway) AgentTools(scope Scope) []map[string]any {
	var rows []model.FlowMCPConnection
	g.DB.Where("tenant_id = ? AND workspace_id = ? AND status = ?", scope.TenantID, scope.WorkspaceID, "approved").Order("created_at").Find(&rows)
	out := []map[string]any{}
	for _, c := range rows {
		v := g.View(c)
		for _, name := range v.AllowedTools {
			ap := v.Approved[name]
			params := ap.InputSchema
			if params == nil {
				params = map[string]any{"type": "object"}
			}
			out = append(out, map[string]any{"type": "function", "function": map[string]any{
				"name": MCPAgentToolName(c.Slug, name), "description": "[MCP " + c.Name + "] " + ap.Description, "parameters": params}})
		}
	}
	return out
}

// ResolveAgentTool maps an agent tool name back to (connection, tool) in scope.
func (g MCPGateway) ResolveAgentTool(scope Scope, name string) (model.FlowMCPConnection, string, bool) {
	rest, ok := strings.CutPrefix(name, mcpToolPrefix)
	if !ok || len(rest) < 10 || rest[8] != '_' {
		return model.FlowMCPConnection{}, "", false
	}
	var c model.FlowMCPConnection
	if err := g.DB.First(&c, "slug = ? AND tenant_id = ? AND workspace_id = ?", rest[:8], scope.TenantID, scope.WorkspaceID).Error; err != nil {
		return c, "", false
	}
	v := g.View(c)
	for _, t := range v.AllowedTools {
		if MCPAgentToolName(c.Slug, t) == name {
			return c, t, true
		}
	}
	return c, "", false
}
