package agriculture

// The only domain bridge in this file is compiled into a Go TEST binary. No
// production main, router, environment switch or token-mint endpoint uses it.
import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"yingce/backend/internal/auth"
	"yingce/backend/internal/browsersecurity"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/handler"
	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
	platformservice "yingce/backend/internal/service"
)

const chainBridgePath = "/api/agriculture/_test/workbench-attributes"
const chainGISPath = "/internal/agri/v1/test/workbench-attributes"

type chainDomain struct {
	Tenant              string `json:"tenant"`
	Workspace           string `json:"workspace"`
	Actor               string `json:"actor"`
	ProjectID           int    `json:"project_id"`
	TaskID              int    `json:"task_id"`
	PlotID              int    `json:"plot_id"`
	HistoricalVersionID int    `json:"historical_version_id"`
	HistoricalReviewID  int    `json:"historical_review_id"`
	TaskCode            string `json:"task_code"`
	PlotCode            string `json:"plot_code"`
	ParcelID            string `json:"parcel_id"`
	FarmID              string `json:"farm_id"`
}

type chainChild struct {
	cmd        *exec.Cmd
	input      io.WriteCloser
	lines      chan []byte
	done       chan struct{}
	err        error
	diagnostic chainDiagnostic
}

// Retain only a bounded, allowlisted startup diagnostic, never driver text.
type chainDiagnostic struct {
	mu  sync.Mutex
	raw []byte
}

func (d *chainDiagnostic) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.raw) < 8192 {
		n := 8192 - len(d.raw)
		if n > len(p) {
			n = len(p)
		}
		d.raw = append(d.raw, p[:n]...)
	}
	return len(p), nil
}
func (d *chainDiagnostic) safe() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var v struct {
		Code string `json:"code"`
		Type string `json:"type"`
		File string `json:"file"`
		Line int    `json:"line"`
	}
	dec := json.NewDecoder(bytes.NewReader(d.raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&v) != nil || v.Code != "identity_chain_fixture_failed" || !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,79}$`).MatchString(v.Type) || !regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,100}\.py$`).MatchString(v.File) || v.Line < 1 || v.Line > 100000 {
		return "no safe diagnostic"
	}
	return fmt.Sprintf("%s %s:%d", v.Type, v.File, v.Line)
}

func chainStart(t *testing.T, command, dir string, args ...string) *chainChild {
	t.Helper()
	c := &chainChild{cmd: exec.Command(command, args...), lines: make(chan []byte, 8), done: make(chan struct{})}
	c.cmd.Dir = dir
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	c.cmd.Env = chainEnvironment(dir, home)
	output, err := c.cmd.StdoutPipe()
	if err != nil {
		t.Fatal("fixture stdout unavailable")
	}
	c.input, err = c.cmd.StdinPipe()
	if err != nil {
		t.Fatal("fixture stdin unavailable")
	}
	c.cmd.Stderr = &c.diagnostic
	if err = c.cmd.Start(); err != nil {
		t.Fatal("fixture process startup failed")
	}
	go func() {
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 65536)
		for scanner.Scan() {
			line := append([]byte{}, scanner.Bytes()...)
			select {
			case c.lines <- line:
			default:
			}
		}
		close(c.lines)
	}()
	go func() { c.err = c.cmd.Wait(); close(c.done) }()
	t.Cleanup(func() {
		c.input.Close()
		select {
		case <-c.done:
			return
		default:
		}
		_ = c.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-c.done:
		case <-time.After(10 * time.Second):
			_ = c.cmd.Process.Kill()
			<-c.done
		}
	})
	return c
}

func chainEnvironment(dir, home string) []string {
	env := []string{"HOME=" + home, "TMPDIR=" + home, "LANG=C.UTF-8", "PYTHONPATH=" + dir, "PYTHONUNBUFFERED=1"}
	if source := os.Getenv("AGRI_E2E_SOURCE_COMMIT"); regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(source) {
		env = append(env, "AGRI_E2E_SOURCE_COMMIT="+source)
	}
	for _, key := range []string{"PATH", "CHROME_BIN", "NGINX_BIN", "NGINX_MIME_TYPES"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func TestIdentityChainChildEnvironment(t *testing.T) {
	for _, key := range []string{"PGPASSWORD", "AGRI_TEST_PG_RUNTIME_DSN", "AGRI_TEST_GEO_MIGRATION_DSN", "NODE_OPTIONS", "HTTPS_PROXY", "PGHOSTADDR"} {
		t.Setenv(key, "secret-sentinel")
	}
	env := chainEnvironment("/fixture/source", "/fixture/private")
	if strings.Contains(strings.Join(env, "\n"), "secret-sentinel") {
		t.Fatal("child inherited credential or runtime override")
	}
	if !strings.Contains(strings.Join(env, "\n"), "HOME=/fixture/private") {
		t.Fatal("private home missing")
	}
	const commit = "0123456789abcdef0123456789abcdef01234567"
	t.Setenv("AGRI_E2E_SOURCE_COMMIT", commit)
	if !strings.Contains(strings.Join(chainEnvironment("/source", "/private"), "\n"), "AGRI_E2E_SOURCE_COMMIT="+commit) {
		t.Fatal("validated nonsecret provenance missing")
	}
	for _, invalid := range []string{"main", strings.ToUpper(commit), commit + "\nPGPASSWORD=secret"} {
		t.Setenv("AGRI_E2E_SOURCE_COMMIT", invalid)
		if strings.Contains(strings.Join(chainEnvironment("/source", "/private"), "\n"), "AGRI_E2E_SOURCE_COMMIT=") {
			t.Fatal("invalid provenance passed to child")
		}
	}
}

func TestIdentityChainSafeDiagnostic(t *testing.T) {
	for _, raw := range []string{`secret-driver-message`, `{"code":"identity_chain_fixture_failed","type":"Error","file":"a.py","line":1,"dsn":"secret"}`, `{"code":"identity_chain_fixture_failed","type":"Error","file":"/private/a.py","line":1}`} {
		d := &chainDiagnostic{}
		_, _ = d.Write([]byte(raw))
		if d.safe() != "no safe diagnostic" {
			t.Fatal("untrusted diagnostic accepted")
		}
	}
	d := &chainDiagnostic{}
	_, _ = d.Write([]byte(`{"code":"identity_chain_fixture_failed","type":"IntegrityError","file":"fixture.py","line":42}`))
	if d.safe() != "IntegrityError fixture.py:42" {
		t.Fatal("safe source diagnostic lost")
	}
	_, _ = d.Write(bytes.Repeat([]byte("secret"), 10000))
	if len(d.raw) > 8192 {
		t.Fatal("diagnostic buffer unbounded")
	}
}

func chainLine(t *testing.T, c *chainChild, timeout time.Duration, target any) {
	t.Helper()
	select {
	case line, ok := <-c.lines:
		if !ok || json.Unmarshal(line, target) != nil {
			t.Fatalf("fixture readiness/protocol invalid (%s)", c.diagnostic.safe())
		}
	case <-time.After(timeout):
		t.Fatalf("fixture readiness/protocol deadline exceeded (%s)", c.diagnostic.safe())
	}
}

func chainPrivate(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal("private fixture encoding failed")
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal("private fixture configuration write failed")
	}
}

// Test assertion connection only. Product services never read another service's
// tables. All queries are bounded to these independently seeded fixture objects.
func chainSnapshot(db *gorm.DB, contexts []chainDomain) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for _, scope := range contexts {
		rows := map[string]string{}
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SELECT set_config('app.tenant_ids',?,true)", scope.Tenant).Error; err != nil {
				return err
			}
			for _, q := range []struct {
				name, table, where string
				args               []any
			}{
				{"plots", "farmland_plots", "plot_code=?", []any{scope.PlotCode}},
				{"tasks", "monitoring_tasks", "id=?", []any{scope.TaskID}},
				{"versions", "plot_versions", "plot_code=?", []any{scope.PlotCode}},
				{"reviews", "review_records", "task_id=?", []any{scope.TaskID}},
				{"operations", "plot_edit_operations", "task_id=?", []any{scope.TaskID}},
				{"audit", "agriculture_actor_audit", "tenant_id=? AND workspace_id=? AND project_id=?", []any{scope.Tenant, scope.Workspace, scope.ProjectID}},
				{"historical_version", "plot_versions", "id=?", []any{scope.HistoricalVersionID}},
				{"historical_review", "review_records", "id=?", []any{scope.HistoricalReviewID}},
			} {
				var raw string
				// table/where are compile-time constants, never request parameters.
				if err := tx.Raw("SELECT COALESCE(jsonb_agg(to_jsonb(f) ORDER BY id),'[]'::jsonb)::text FROM (SELECT * FROM "+q.table+" WHERE "+q.where+") f", q.args...).Scan(&raw).Error; err != nil {
					return err
				}
				rows[q.name] = raw
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		out[scope.Workspace] = rows
	}
	return out, nil
}

// Assertion-only migration connection in the disposable cluster. Discover every
// tenant again so an erroneously created organization cannot escape the snapshot.
// Session/CSRF security records are deliberately not domain governance records.
func chainPlatformSnapshot(db *gorm.DB) (map[string]string, error) {
	out := map[string]string{}
	err := db.Transaction(func(tx *gorm.DB) error {
		var tenants []string
		if err := tx.Model(&model.Tenant{}).Order("id").Pluck("id", &tenants).Error; err != nil {
			return err
		}
		if err := tx.Exec("SELECT set_config('app.tenant_ids',?,true)", strings.Join(tenants, ",")).Error; err != nil {
			return err
		}
		for _, table := range []string{"flow_tenants", "flow_memberships", "flow_workspaces", "flow_workspace_memberships", "flow_invites", "flow_billing_accounts", "flow_agriculture_role_bindings", "flow_agriculture_resource_bindings", "flow_agriculture_authorization_epochs", "flow_agriculture_audit_events"} {
			var value string
			// Sorting complete JSON also supports epoch rows whose key is tenant_id.
			if err := tx.Raw("SELECT COALESCE(jsonb_agg(to_jsonb(f) ORDER BY to_jsonb(f)::text),'[]')::text FROM " + table + " f").Scan(&value).Error; err != nil {
				return err
			}
			out[table] = value
		}
		return nil
	})
	return out, err
}

func TestIdentityBrowserGISWriteChain(t *testing.T) {
	required := os.Getenv("AGRI_IDENTITY_CHAIN_REQUIRED") == "1"
	for _, key := range []string{"AGRI_IDENTITY_PYTHON", "AGRI_TEST_PG_MIGRATION_DSN", "AGRI_TEST_PG_RUNTIME_DSN", "AGRI_TEST_GEO_MIGRATION_DSN", "AGRI_TEST_GEO_RUNTIME_DSN", "REDIS_URL", "CHROME_BIN", "NGINX_BIN", "NGINX_MIME_TYPES"} {
		if os.Getenv(key) == "" {
			if required {
				t.Fatalf("required chain dependency missing: %s", key)
			}
			t.Skip("explicit browser and isolated PostgreSQL chain dependencies not configured")
		}
	}
	if os.Getenv("AGRI_TEST_PG_ISOLATED") != "1" {
		if required {
			t.Fatal("chain requires disposable isolated PostgreSQL")
		}
		t.Skip("isolated PostgreSQL not authorized")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node required for actual browser chain")
	}
	for _, path := range []string{"web/dist-agriculture/index.html", "services/agri-gis/frontend/dist-agriculture/index.html", "scripts/agriculture/identity-chain-browser.mjs", "services/agri-gis/backend/tests/identity_chain_fixture.py"} {
		if _, err = os.Stat(filepath.Join(root, path)); err != nil {
			t.Fatal("required actual chain artifact missing")
		}
	}
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	s, seed, owner, actor := postgresFixture(t)
	ctx := context.Background()
	org, err := s.CreateOrganization(ctx, owner, "Identity browser chain", "identity-chain-main-001")
	if err != nil {
		t.Fatal("main organization setup failed")
	}
	foreign, err := s.CreateOrganization(ctx, owner, "Other chain tenant", "identity-chain-foreign-001")
	if err != nil {
		t.Fatal("foreign organization setup failed")
	}
	inv, err := s.CreateInvite(ctx, owner, org.Workspace.ID, actor+"@fixture.invalid", "editor")
	if err != nil {
		t.Fatal("fixture invitation failed")
	}
	if _, err = s.AcceptInvite(ctx, actor, inv.Token); err != nil {
		t.Fatal("fixture admission failed")
	}
	second := flow.NewID("ws_chain_")
	err = flow.TenantTx(seed, org.Tenant.ID, func(tx *gorm.DB) error {
		for _, record := range []any{&model.Workspace{ID: second, TenantID: org.Tenant.ID, Product: Product, Name: "Second fixture workspace", Slug: "second-chain"}, &model.WorkspaceMembership{ID: flow.NewID("wm_"), WorkspaceID: second, UserID: owner, Role: "viewer"}, &model.WorkspaceMembership{ID: flow.NewID("wm_"), WorkspaceID: second, UserID: actor, Role: "viewer"}, &model.AgricultureRoleBinding{ID: flow.NewID("agr_"), TenantID: org.Tenant.ID, WorkspaceID: second, UserID: actor, Role: "editor"}} {
			if e := tx.Create(record).Error; e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal("same-tenant workspace fixture failed")
	}
	password := auth.RandomToken()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal("fixture password hash failed")
	}
	if err = seed.Model(&model.User{}).Where("id=?", actor).Update("password_hash", hash).Error; err != nil {
		t.Fatal("fixture password initialization failed")
	}
	t.Cleanup(func() { _ = seed.Where("user_id=?", actor).Delete(&model.AuthSession{}).Error })
	private := t.TempDir()
	if err = os.Chmod(private, 0700); err != nil {
		t.Fatal(err)
	}
	newKeys := func(id string) (*ServiceKeyring, ServiceIdentityKey) {
		pub, priv, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			t.Fatal("fixture key generation failed")
		}
		now := time.Now().UTC().Truncate(time.Second)
		key := ServiceIdentityKey{ID: id, PublicKey: pub, PrivateKey: priv, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)}
		ring, e := NewServiceKeyring([]ServiceIdentityKey{key}, id)
		if e != nil {
			t.Fatal(e)
		}
		return ring, key
	}
	platformKeys, platformKey := newKeys("chain_platform")
	gisKeys, gisKey := newKeys("chain_gis")
	callbackRouter := gin.New()
	s.RegisterInternalAuthority(callbackRouter, gisKeys)
	callback := httptest.NewServer(callbackRouter)
	t.Cleanup(callback.Close)
	keyJSON := func(k ServiceIdentityKey, private bool) map[string]any {
		v := map[string]any{"kid": k.ID, "public_key": base64.StdEncoding.EncodeToString(k.PublicKey), "not_before": k.NotBefore.Format(time.RFC3339), "not_after": k.NotAfter.Format(time.RFC3339), "revoked": false}
		if private {
			v["seed"] = base64.StdEncoding.EncodeToString(k.PrivateKey.Seed())
		}
		return v
	}
	pythonConfig := filepath.Join(private, "gis.json")
	chainPrivate(t, pythonConfig, map[string]any{"contexts": []map[string]string{{"tenant": org.Tenant.ID, "workspace": org.Workspace.ID, "actor": actor}, {"tenant": org.Tenant.ID, "workspace": second, "actor": actor}, {"tenant": foreign.Tenant.ID, "workspace": foreign.Workspace.ID, "actor": actor}}, "platform": callback.URL, "platform_keys": []any{keyJSON(platformKey, false)}, "callback_key": keyJSON(gisKey, true), "geo_migration_dsn": os.Getenv("AGRI_TEST_GEO_MIGRATION_DSN"), "geo_runtime_dsn": os.Getenv("AGRI_TEST_GEO_RUNTIME_DSN")})
	python := chainStart(t, os.Getenv("AGRI_IDENTITY_PYTHON"), filepath.Join(root, "services/agri-gis/backend"), filepath.Join(root, "services/agri-gis/backend/tests/identity_chain_fixture.py"), pythonConfig)
	var ready struct {
		Port     int           `json:"port"`
		Contexts []chainDomain `json:"contexts"`
	}
	chainLine(t, python, 60*time.Second, &ready)
	if ready.Port < 1 || ready.Port > 65535 || len(ready.Contexts) != 3 {
		t.Fatal("invalid GIS readiness")
	}
	for i, want := range []string{org.Workspace.ID, second, foreign.Workspace.ID} {
		if ready.Contexts[i].Workspace != want || ready.Contexts[i].Actor != actor || ready.Contexts[i].TaskID == 0 || ready.Contexts[i].HistoricalVersionID == 0 {
			t.Fatal("GIS fixture identity mismatch")
		}
	}
	gis, err := NewGISClient(fmt.Sprintf("http://127.0.0.1:%d", ready.Port), platformKeys)
	if err != nil {
		t.Fatal("GIS client fixture failed")
	}
	s.Ownership = gis
	for i := 0; i < 2; i++ {
		d := ready.Contexts[i]
		if _, err = s.GrantResourceBinding(ctx, owner, d.Workspace, actor, ResourceRef{"parcel", d.ParcelID}, []string{"agri.parcel.read", "agri.parcel.update"}, nil, revisionPointer(0)); err != nil {
			t.Fatal("explicit resource grant setup failed")
		}
	}
	geo, err := gorm.Open(postgres.Open(os.Getenv("AGRI_TEST_GEO_MIGRATION_DSN")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("GIS assertion connection unavailable")
	}
	geoSQL, _ := geo.DB()
	t.Cleanup(func() { geoSQL.Close() })
	initial, err := chainSnapshot(geo, ready.Contexts)
	if err != nil {
		t.Fatal("initial GIS snapshot failed")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gatewayPort := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	origin := fmt.Sprintf("http://127.0.0.1:%d", gatewayPort)
	csrfKey := make([]byte, 32)
	if _, err = rand.Read(csrfKey); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(private, "csrf.key")
	if err = os.WriteFile(keyPath, csrfKey, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLOW_AGRICULTURE_ENABLED", "true")
	t.Setenv("FLOW_AGRICULTURE_EXTERNAL_ORIGIN", origin)
	t.Setenv("FLOW_AGRICULTURE_ALLOW_HTTP_LOOPBACK_DEV", "true")
	t.Setenv("FLOW_AGRICULTURE_CSRF_KEY_FILE", keyPath)
	policy, err := browsersecurity.FromEnv()
	if err != nil {
		t.Fatal("browser policy failed")
	}
	svc := platformservice.New(repository.New(s.DB), filepath.Join(private, "platform"))
	t.Cleanup(func() { svc.Close() })
	if err := svc.ValidateRuntime(); err != nil {
		t.Fatalf("real platform runtime initialization failed: %T", err)
	}
	// Match cmd/server/main.go: shared authentication handlers use the real
	// runtime coordinator for rate limits, not merely their service argument.
	handler.ConfigureRuntime(svc)
	t.Cleanup(func() { handler.ConfigureRuntime(nil) })
	user := func(c *gin.Context) (*model.User, error) {
		cookie, _ := c.Cookie(auth.SessionCookieName)
		return svc.CurrentUser(cookie)
	}
	api := API{Service: s, User: user}
	router := gin.New()
	var observationsMu sync.Mutex
	observations := []map[string]any{}
	violations := []string{}
	router.Use(func(c *gin.Context) {
		if c.Request.URL.Path != chainBridgePath {
			c.Next()
			return
		}
		before, e := chainSnapshot(geo, ready.Contexts)
		if e != nil {
			c.AbortWithStatus(503)
			return
		}
		c.Next()
		after, e := chainSnapshot(geo, ready.Contexts)
		observationsMu.Lock()
		defer observationsMu.Unlock()
		observations = append(observations, map[string]any{"status": c.Writer.Status()})
		if e != nil || (c.Writer.Status() >= 400 && !reflect.DeepEqual(before, after)) {
			violations = append(violations, "rejected_request_changed_domain_or_snapshot_failed")
		}
	})
	router.Use(policy.Middleware())
	handler.RegisterAuthRoutes(router.Group("/api"), svc)
	api.Register(router.Group("/api"))
	operationID := flow.NewID("op_chain_")
	mainDomain := ready.Contexts[0]
	router.POST(chainBridgePath, api.sessionMiddleware(respond), func(c *gin.Context) {
		var input struct {
			WorkspaceID string `json:"workspaceId"`
			TaskCode    string `json:"taskCode"`
			PlotCode    string `json:"plotCode"`
			CropType    string `json:"cropType"`
			OperationID string `json:"operationId"`
		}
		if !decode(c, &input) {
			return
		}
		if input.TaskCode != mainDomain.TaskCode || input.PlotCode != mainDomain.PlotCode || input.OperationID != operationID || input.CropType != "玉米" {
			respond(c, nil, ErrInvalid)
			return
		}
		actorID := c.GetString("agriculture.actor")
		decision, e := s.Authorize(c.Request.Context(), actorID, input.WorkspaceID, "agri.parcel.update", []ResourceRef{{"parcel", mainDomain.ParcelID}})
		if e != nil {
			respond(c, nil, e)
			return
		}
		body, _ := json.Marshal(map[string]string{"task_code": input.TaskCode, "plot_code": input.PlotCode, "crop_type": input.CropType})
		_, e = gis.CallAuthorized(c.Request.Context(), s, actorID, decision, "POST", chainGISPath, body, input.OperationID)
		if e != nil {
			respond(c, nil, e)
			return
		}
		respond(c, gin.H{"operationId": input.OperationID}, nil)
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	reportPath := filepath.Join(private, "browser-report.json")
	// Registered before child cleanup: stop owned processes first, then retain
	// only fixed-shape diagnostics before testing removes private credentials.
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		raw, e := os.ReadFile(reportPath)
		if e != nil || len(raw) > 1024*1024 {
			return
		}
		var report struct {
			Result      string `json:"result"`
			Stage       string `json:"stage"`
			APIStatuses []struct {
				Path      string `json:"path"`
				Status    int    `json:"status"`
				Completed bool   `json:"completed"`
			} `json:"api_statuses"`
			DOMFlags       map[string]bool `json:"dom_flags"`
			ExceptionCount int             `json:"browser_runtime_exception_count"`
			Results        []struct {
				Name   string `json:"name"`
				Result string `json:"result"`
			} `json:"results"`
		}
		if json.Unmarshal(raw, &report) != nil {
			return
		}
		stages := map[string]bool{"preflight": true, "anonymous_request": true, "real_login": true, "authorized_chain_write": true, "scope_and_identity_negatives": true, "awaiting_real_platform_revocation": true, "same_cookie_after_revocation": true, "complete": true}
		for _, stage := range []string{"real_login_navigation", "real_login_account", "real_login_password", "real_login_submit", "real_login_confirmed_workspace"} {
			stages[stage] = true
		}
		if !stages[report.Stage] || (report.Result != "pass" && report.Result != "fail") || len(report.Results) > 100 {
			return
		}
		for _, check := range report.Results {
			if check.Result != "pass" || !regexp.MustCompile(`^[a-z0-9_]{1,120}$`).MatchString(check.Name) {
				return
			}
		}
		if len(report.APIStatuses) > 80 || report.ExceptionCount < 0 || report.ExceptionCount > 10000 {
			return
		}
		paths := map[string]bool{"/api/auth/session": true, "/api/auth/login": true, "/api/auth/logout": true, "/api/auth/settings": true, "/api/agriculture/workspaces": true}
		for _, row := range report.APIStatuses {
			if !paths[row.Path] || row.Status < 100 || row.Status > 599 {
				return
			}
		}
		flags := map[string]bool{}
		for _, key := range []string{"loginRoute", "agriRoute", "documentComplete", "accountInput", "passwordInput", "submitButton", "actorVisible", "vueLoading", "vueReady", "vueError", "alertVisible"} {
			if value, ok := report.DOMFlags[key]; ok {
				flags[key] = value
			}
		}
		clean, _ := json.Marshal(map[string]any{"schema_version": "1.0", "result": report.Result, "stage": report.Stage, "results": report.Results, "diagnostic_only": true, "api_statuses": report.APIStatuses, "dom_flags": flags, "browser_runtime_exception_count": report.ExceptionCount})
		dir := filepath.Join(root, ".local/evidence")
		if os.MkdirAll(dir, 0700) == nil {
			_ = os.WriteFile(filepath.Join(dir, "identity-chain-browser.json"), clean, 0600)
		}
	})
	browserConfig := filepath.Join(private, "browser.json")
	chainPrivate(t, browserConfig, map[string]any{"platform_url": server.URL, "gateway_port": gatewayPort, "username": actor, "password": password, "workspace_id": org.Workspace.ID, "foreign_workspace_id": foreign.Workspace.ID, "second_workspace_id": second, "operation_id": operationID, "task_code": mainDomain.TaskCode, "plot_code": mainDomain.PlotCode, "parcel_id": mainDomain.ParcelID, "crop_type": "玉米", "report_path": reportPath})
	nodeChild := chainStart(t, node, root, filepath.Join(root, "scripts/agriculture/identity-chain-browser.mjs"), browserConfig)
	var event struct {
		Event string `json:"event"`
		Stage string `json:"stage"`
	}
	safeStage := func() string {
		if regexp.MustCompile(`^[a-z0-9_-]{1,64}$`).MatchString(event.Stage) {
			return event.Stage
		}
		return "unavailable"
	}
	chainLine(t, nodeChild, 180*time.Second, &event)
	if event.Event != "phase1_done" {
		t.Fatalf("browser login/write phase failed: %s", safeStage())
	}
	var successful map[string]map[string]string
	t.Run("browser_login_signed_write_and_actor_audit", func(t *testing.T) {
		successful, err = chainSnapshot(geo, ready.Contexts)
		if err != nil {
			t.Fatal("post-write snapshot failed")
		}
		if !reflect.DeepEqual(initial[second], successful[second]) || !reflect.DeepEqual(initial[foreign.Workspace.ID], successful[foreign.Workspace.ID]) {
			t.Fatal("write escaped tenant/workspace")
		}
		for _, name := range []string{"historical_version", "historical_review"} {
			if initial[org.Workspace.ID][name] != successful[org.Workspace.ID][name] {
				t.Fatal("historical identity mutated")
			}
		}
		var plots []struct {
			Version  int    `json:"version"`
			CropType string `json:"crop_type"`
		}
		if json.Unmarshal([]byte(successful[org.Workspace.ID]["plots"]), &plots) != nil || len(plots) != 1 || plots[0].Version != 2 || plots[0].CropType != "玉米" {
			t.Fatal("actual Workbench write did not commit")
		}
		var audit []struct {
			Actor      string `json:"actor_id"`
			LegacyRole string `json:"legacy_role"`
			Operation  string `json:"operation_id"`
			Request    string `json:"request_id"`
			Action     string `json:"action"`
		}
		if json.Unmarshal([]byte(successful[org.Workspace.ID]["audit"]), &audit) != nil || len(audit) == 0 {
			t.Fatal("stable actor audit missing")
		}
		for _, row := range audit {
			if row.Actor != actor || row.LegacyRole != "interpreter" || row.Operation != operationID || row.Request == "" || row.Action != "agri.parcel.update" {
				t.Fatal("audit actor or operation chain mismatch")
			}
		}
		observationsMu.Lock()
		defer observationsMu.Unlock()
		counts := map[int]int{}
		for _, o := range observations {
			counts[o["status"].(int)]++
		}
		if len(violations) > 0 || counts[200] != 1 || counts[401] < 1 || counts[400] < 3 || counts[404] < 2 || counts[403] < 2 {
			t.Fatal("browser negative matrix incomplete or domain mutation on rejection")
		}
	})
	if t.Failed() {
		return
	}
	viewer := []string{"viewer"}
	if err = s.ChangeMember(ctx, owner, org.Workspace.ID, actor, &viewer, nil); err != nil {
		t.Fatal("real role downgrade failed")
	}
	if _, err = io.WriteString(nodeChild.input, "{\"event\":\"continue_revoked\"}\n"); err != nil {
		t.Fatal("browser continuation failed")
	}
	chainLine(t, nodeChild, 90*time.Second, &event)
	if event.Event != "done" {
		t.Fatalf("same-browser revocation phase failed: %s", safeStage())
	}
	select {
	case <-nodeChild.done:
		if nodeChild.err != nil {
			t.Fatal("browser fixture failed")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("browser fixture did not exit")
	}
	t.Run("revoked_browser_write_has_no_domain_effect", func(t *testing.T) {
		after, e := chainSnapshot(geo, ready.Contexts)
		if e != nil || !reflect.DeepEqual(successful, after) {
			t.Fatal("revoked browser write changed domain")
		}
		observationsMu.Lock()
		defer observationsMu.Unlock()
		if len(violations) > 0 || len(observations) == 0 || observations[len(observations)-1]["status"] != 403 {
			t.Fatal("revocation was not a real rejected write")
		}
	})
	request := func(method, path, cookie string) (*http.Response, error) {
		jar, _ := cookiejar.New(nil)
		client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
		headers := http.Header{"Origin": []string{origin}}
		if cookie != "" {
			headers.Set("Cookie", auth.SessionCookieName+"="+cookie)
		}
		if method != "GET" && method != "HEAD" {
			req, _ := http.NewRequest("GET", server.URL+"/api/auth/csrf", nil)
			req.Header = headers.Clone()
			r, e := client.Do(req)
			if e != nil {
				return nil, e
			}
			var token struct {
				Data struct {
					Token string `json:"token"`
				} `json:"data"`
			}
			e = json.NewDecoder(r.Body).Decode(&token)
			r.Body.Close()
			if e != nil {
				return nil, e
			}
			headers.Set(browsersecurity.CSRFHeader, token.Data.Token)
		}
		req, _ := http.NewRequest(method, server.URL+path, bytes.NewBufferString("{}"))
		req.Header = headers
		req.Header.Set("Content-Type", "application/json")
		return client.Do(req)
	}
	t.Run("forged_cookie_has_no_domain_effect", func(t *testing.T) {
		before, e := chainSnapshot(geo, ready.Contexts)
		if e != nil {
			t.Fatal(e)
		}
		r, e := request("POST", chainBridgePath, "forged.invalid")
		if e != nil {
			t.Fatal("forged-cookie HTTP failed")
		}
		r.Body.Close()
		if r.StatusCode != 401 {
			t.Fatal("forged cookie accepted")
		}
		after, e := chainSnapshot(geo, ready.Contexts)
		if e != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("forged cookie changed domain")
		}
	})
	inventory := []map[string]any{}
	t.Run("unauthenticated_open_endpoint_inventory", func(t *testing.T) {
		platformBefore, e := chainPlatformSnapshot(seed)
		if e != nil {
			t.Fatal("platform governance snapshot failed")
		}
		before, e := chainSnapshot(geo, ready.Contexts)
		if e != nil {
			t.Fatal(e)
		}
		for _, route := range router.Routes() {
			if route.Path == chainBridgePath || !(strings.HasPrefix(route.Path, "/api/agriculture/") || strings.HasPrefix(route.Path, "/api/agri/v1/")) {
				continue
			}
			path := route.Path
			for _, param := range []string{":workspaceId", ":workspace_id"} {
				path = strings.ReplaceAll(path, param, org.Workspace.ID)
			}
			for _, param := range []string{":bindingId", ":binding_id", ":inviteId", ":userId"} {
				path = strings.ReplaceAll(path, param, "fixture_unknown")
			}
			r, e := request(route.Method, path, "")
			if e != nil {
				t.Fatal("anonymous inventory request failed")
			}
			r.Body.Close()
			if r.StatusCode != 401 {
				t.Fatalf("anonymous route not rejected: %s %s status=%d", route.Method, route.Path, r.StatusCode)
			}
			inventory = append(inventory, map[string]any{"method": route.Method, "path": route.Path, "status": r.StatusCode})
		}
		if len(inventory) < 16 {
			t.Fatal("opened agriculture endpoint inventory incomplete")
		}
		after, e := chainSnapshot(geo, ready.Contexts)
		if e != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("anonymous inventory changed domain")
		}
		platformAfter, e := chainPlatformSnapshot(seed)
		if e != nil || !reflect.DeepEqual(platformBefore, platformAfter) {
			t.Fatal("anonymous inventory changed platform governance")
		}
		r, e := http.Post(callback.URL+authorityCheckPath, "application/json", strings.NewReader("{}"))
		if e != nil {
			t.Fatal("unsigned callback failed")
		}
		r.Body.Close()
		if r.StatusCode != 401 {
			t.Fatal("unsigned service callback accepted")
		}
	})
	if t.Failed() {
		return
	}
	report, err := os.ReadFile(reportPath)
	if err != nil || len(report) > 1024*1024 || !json.Valid(report) {
		t.Fatal("sanitized browser report unavailable")
	}
	for _, secret := range []string{password, os.Getenv("AGRI_TEST_GEO_MIGRATION_DSN"), os.Getenv("AGRI_TEST_GEO_RUNTIME_DSN")} {
		if bytes.Contains(report, []byte(secret)) {
			t.Fatal("browser report contained private fixture material")
		}
	}
	evidence := filepath.Join(root, ".local/evidence")
	if err = os.MkdirAll(evidence, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(evidence, "identity-chain-browser.json"), report, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(operationID))
	snapshotRaw, _ := json.Marshal(successful)
	snapshotHash := sha256.Sum256(snapshotRaw)
	chainPrivate(t, filepath.Join(evidence, "identity-chain-boundary.json"), map[string]any{"result": "pass", "test_only_bridge": chainBridgePath, "operation_sha256": hex.EncodeToString(digest[:]), "snapshot_sha256": hex.EncodeToString(snapshotHash[:]), "agriculture_routes": inventory, "shared_auth_boundary": "real React login, CSRF/session handlers; public settings and CSRF are intentionally anonymous", "internal_boundary": "unsigned authority callback rejected; signed Python callback used on successful write", "scope_count": 3, "tenant_count": 2, "production_deployment": false, "live_farm_data": false})
}
