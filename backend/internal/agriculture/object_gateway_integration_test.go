package agriculture

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestObjectGatewayRealHTTPPostgres(t *testing.T) {
	runObjectGatewayRealHTTPPostgres(t, false)
}

func TestObjectGatewayDoublePostgresIsolation(t *testing.T) {
	runObjectGatewayRealHTTPPostgres(t, true)
}

func runObjectGatewayRealHTTPPostgres(t *testing.T, platformPostgres bool) {
	python := os.Getenv("AGRI_IDENTITY_PYTHON")
	if python == "" || os.Getenv("AGRI_TEST_PG_ISOLATED") != "1" || os.Getenv("AGRI_TEST_GEO_MIGRATION_DSN") == "" {
		t.Skip("requires explicit isolated GIS PostgreSQL and Python")
	}
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	var s Service
	var org OrganizationResult
	var migration *gorm.DB
	ownerID, actorID := "owner", "bob"
	foreignWorkspace := "foreign_workspace"
	foreignTenant, secondWorkspace := "foreign_tenant", "unjoined_workspace"
	if platformPostgres {
		s, migration, ownerID, actorID = postgresFixture(t)
		var err error
		org, err = s.CreateOrganization(context.Background(), ownerID, "Object isolation fixture", "object-isolation-org")
		if err != nil {
			t.Fatal("object platform PostgreSQL organization failed")
		}
		invite, err := s.CreateInvite(context.Background(), ownerID, org.Workspace.ID, actorID+"@fixture.invalid", "viewer")
		if err != nil {
			t.Fatal("object platform PostgreSQL invitation failed")
		}
		if _, err = s.AcceptInvite(context.Background(), actorID, invite.Token); err != nil {
			t.Fatal("object platform PostgreSQL membership failed")
		}
		foreign, err := s.CreateOrganization(context.Background(), ownerID, "Foreign object isolation fixture", "object-isolation-foreign")
		if err != nil {
			t.Fatal("foreign object platform PostgreSQL organization failed")
		}
		foreignWorkspace = foreign.Workspace.ID
		foreignTenant = foreign.Tenant.ID
		secondWorkspace = objectPostgresSecondWorkspace(t, migration, org, ownerID)
	} else {
		s, org = fixture(t)
		join(t, s, org, actorID, "viewer")
	}
	now := time.Now().UTC().Truncate(time.Second)
	platform, pk := identityTestKeys(t, now, 0x74)
	gisKeys, gk := identityTestKeys(t, now, 0x75)
	callbackRouter := gin.New()
	callback := httptest.NewServer(callbackRouter)
	defer callback.Close()
	root, e := filepath.Abs("../../..")
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	if e = os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	config := map[string]any{"tenant": org.Tenant.ID, "workspace": org.Workspace.ID, "platform": callback.URL, "owner_dsn": os.Getenv("AGRI_TEST_GEO_MIGRATION_DSN"), "app_dsn": os.Getenv("AGRI_TEST_GEO_RUNTIME_DSN"), "platform_keys": []any{map[string]any{"kid": pk.ID, "public_key": base64.StdEncoding.EncodeToString(pk.PublicKey), "not_before": pk.NotBefore.Format(time.RFC3339), "not_after": pk.NotAfter.Format(time.RFC3339), "revoked": false}}, "callback_key": map[string]any{"kid": gk.ID, "seed": base64.StdEncoding.EncodeToString(gk.PrivateKey.Seed()), "not_before": gk.NotBefore.Format(time.RFC3339), "not_after": gk.NotAfter.Format(time.RFC3339)}}
	config["extended"], config["foreign_tenant"], config["foreign_workspace"], config["second_workspace"] = platformPostgres, foreignTenant, foreignWorkspace, secondWorkspace
	raw, e := json.Marshal(config)
	if e != nil {
		t.Fatal("encode private fixture")
	}
	path := filepath.Join(dir, "private.json")
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(python, filepath.Join(root, "services/agri-gis/backend/tests/object_http_fixture.py"), path)
	cmd.Dir = filepath.Join(root, "services/agri-gis/backend")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "PYTHONPATH=" + cmd.Dir, "PYTHONUNBUFFERED=1", "HOME=" + dir, "TMPDIR=" + dir}
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	stdin, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	cmd.Stderr = io.Discard
	if e = cmd.Start(); e != nil {
		t.Fatal("start GIS object fixture")
	}
	defer func() { stdin.Close(); cmd.Process.Kill(); cmd.Wait() }()
	messages := make(chan map[string]any, 8)
	go func() {
		defer close(messages)
		scan := bufio.NewScanner(stdout)
		for scan.Scan() {
			var v map[string]any
			if json.Unmarshal(scan.Bytes(), &v) == nil {
				messages <- v
			}
		}
	}()
	receive := func() map[string]any {
		t.Helper()
		select {
		case v := <-messages:
			if v == nil {
				t.Fatal("GIS object fixture exited")
			}
			return v
		case <-time.After(30 * time.Second):
			t.Fatal("GIS object fixture deadline")
			return nil
		}
	}
	ready := receive()
	port, _ := ready["port"].(float64)
	objectID, _ := ready["object_id"].(string)
	foreignObjectID, _ := ready["foreign_object_id"].(string)
	reportObjectID, _ := ready["report_object_id"].(string)
	expected := []byte("synthetic-private-object-v1\n0123456789\n")
	digest := sha256.Sum256(expected)
	if port < 1 || port > 65535 || !ownershipID.MatchString(objectID) || ready["version"] != float64(1) || ready["content_sha256"] != hex.EncodeToString(digest[:]) || ready["size_bytes"] != float64(len(expected)) {
		t.Fatal("invalid object fixture readiness")
	}
	client, e := NewGISClient("http://127.0.0.1:"+strconv.Itoa(int(port)), platform)
	if platformPostgres && (!ownershipID.MatchString(foreignObjectID) || !ownershipID.MatchString(reportObjectID)) {
		t.Fatal("extended object fixture readiness invalid")
	}
	if e != nil {
		t.Fatal(e)
	}
	s.Ownership = client
	s.RegisterInternalAuthority(callbackRouter, gisKeys)
	if _, e = s.GrantResourceBinding(context.Background(), ownerID, org.Workspace.ID, actorID, ResourceRef{"asset", "asset_a"}, []string{"agri.asset_download.read"}, nil, revisionPointer(0)); e != nil {
		t.Fatalf("fixture grant: %v", e)
	}
	if platformPostgres {
		if _, e = s.GrantResourceBinding(context.Background(), ownerID, org.Workspace.ID, actorID, ResourceRef{"report", "report_a"}, []string{"agri.report.read"}, nil, revisionPointer(0)); e != nil {
			t.Fatal("report fixture grant failed")
		}
	}
	var server *httptest.Server
	var cookie *http.Cookie
	if platformPostgres {
		server, cookie = objectPostgresSessionServer(t, s, migration, actorID)
	} else {
		server, cookie = objectSessionServer(t, s)
	}
	server.Client().CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	server.Client().Timeout = 10 * time.Second
	target := fmt.Sprintf("/api/agri/v1/workspaces/%s/objects/%s/versions/1", org.Workspace.ID, objectID)
	reportTarget := strings.Replace(target, objectID, reportObjectID, 1)
	activeObjects := float64(1)
	if platformPostgres {
		activeObjects = 2
	}
	control := func(name string) map[string]any {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"command": name})
		if _, e := stdin.Write(append(b, '\n')); e != nil {
			t.Fatal("private fixture control closed")
		}
		return receive()
	}
	type fetched struct {
		status  int
		headers http.Header
		body    []byte
		err     error
	}
	fetch := func(p string, credential *http.Cookie) fetched {
		req, err := http.NewRequest("GET", server.URL+p, nil)
		if err != nil {
			return fetched{err: err}
		}
		if credential != nil {
			req.AddCookie(credential)
		}
		res, err := server.Client().Do(req)
		if err != nil {
			return fetched{err: err}
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		return fetched{res.StatusCode, res.Header, body, err}
	}
	call := func(method, p, rng string, want int, wantBody []byte) {
		t.Helper()
		var before map[string]string
		var geoBefore any
		if platformPostgres {
			var err error
			before, err = chainPlatformSnapshot(migration)
			if err != nil {
				t.Fatal("platform snapshot failed")
			}
			geoBefore = control("snapshot")["sha256"]
			if hash, ok := geoBefore.(string); !ok || len(hash) != 64 {
				t.Fatal("GIS snapshot missing")
			}
		}
		h := http.Header{}
		if rng != "" {
			h.Set("Range", rng)
		}
		status, headers, body := objectHTTP(t, server, cookie, method, p, h)
		if status != want {
			t.Fatalf("object method=%s expected=%d observed=%d", method, want, status)
		}
		if want >= 400 {
			assertObjectDenial(t, method, status, headers, body)
		}
		if platformPostgres {
			after, err := chainPlatformSnapshot(migration)
			if err != nil || !reflect.DeepEqual(before, after) || geoBefore != control("snapshot")["sha256"] {
				t.Fatal("object request changed platform or GIS business state")
			}
			if want >= 400 {
				for _, name := range []string{"Location", "Content-Disposition", "Content-Range"} {
					if headers.Get(name) != "" {
						t.Fatal("denied object response leaked redirect or content metadata")
					}
				}
				for _, secret := range []string{org.Tenant.ID, org.Workspace.ID, objectID, actorID, string(expected)} {
					if strings.Contains(string(body), secret) {
						t.Fatal("denied object response disclosed scope or content")
					}
				}
			}
		}
		if wantBody != nil && string(body) != string(wantBody) {
			t.Fatal("actual GIS bytes differ")
		}
		if !strings.Contains(headers.Get("Cache-Control"), "no-store") || headers.Get("ETag") != "" || headers.Get("Set-Cookie") != "" || strings.Contains(string(body), "object_test_fixture") {
			t.Fatal("private object metadata/cache leaked")
		}
	}
	t.Run("session_get_head_ranges", func(t *testing.T) {
		validCookie := cookie
		cookie = nil
		call("GET", target, "", 401, nil)
		call("HEAD", target, "", 401, []byte{})
		cookie = validCookie
		call("GET", target, "", 200, expected)
		call("HEAD", target, "", 200, []byte{})
		call("GET", target, "bytes=0-8", 206, expected[:9])
		call("GET", target, "bytes=-4", 206, expected[len(expected)-4:])
		call("GET", target, "bytes=999-", 416, nil)
		if platformPostgres {
			call("GET", reportTarget, "", 200, expected)
			call("HEAD", reportTarget, "", 200, []byte{})
			call("GET", reportTarget, "bytes=0-8", 206, expected[:9])
		}
		if control("stats")["active_objects"] != activeObjects {
			t.Fatal("read mutated private object registration")
		}
		if platformPostgres {
			before, err := chainPlatformSnapshot(migration)
			if err != nil {
				t.Fatal("concurrent object snapshot failed")
			}
			geoBefore := control("snapshot")["sha256"]
			type expectedFetch struct {
				fetched
				want int
			}
			results := make(chan expectedFetch, 12)
			for i := 0; i < 12; i++ {
				path := target
				want := 200
				if i%3 == 1 {
					path = reportTarget
				}
				if i%3 == 2 {
					path = strings.Replace(target, objectID, foreignObjectID, 1)
					want = 404
				}
				go func(p string, status int) { results <- expectedFetch{fetch(p, cookie), status} }(path, want)
			}
			allowed, denied := 0, 0
			for i := 0; i < 12; i++ {
				result := <-results
				if result.err != nil || result.status != result.want {
					t.Fatal("concurrent object request failed")
				}
				if result.status == 200 && string(result.body) == string(expected) {
					allowed++
				} else if result.status == 404 && !strings.Contains(string(result.body), string(expected)) {
					assertObjectDenial(t, "GET", result.status, result.headers, result.body)
					denied++
				} else {
					t.Fatal("concurrent object scope leaked or failed")
				}
			}
			if allowed != 8 || denied != 4 {
				t.Fatal("concurrent object result counts differ")
			}
			after, err := chainPlatformSnapshot(migration)
			if err != nil || !reflect.DeepEqual(before, after) || geoBefore != control("snapshot")["sha256"] {
				t.Fatal("concurrent reads changed business state")
			}
		}
	})
	t.Run("scope_and_current_resource_withdrawal", func(t *testing.T) {
		call("GET", strings.Replace(target, org.Workspace.ID, foreignWorkspace, 1), "", 404, nil)
		call("HEAD", strings.Replace(target, org.Workspace.ID, foreignWorkspace, 1), "", 404, []byte{})
		call("GET", strings.Replace(target, objectID, "object_unavailable", 1), "", 404, nil)
		call("GET", strings.TrimSuffix(target, "/1")+"/2", "", 404, nil)
		if platformPostgres {
			call("GET", strings.Replace(target, org.Workspace.ID, secondWorkspace, 1), "", 404, nil)
			call("HEAD", strings.Replace(target, org.Workspace.ID, secondWorkspace, 1), "", 404, []byte{})
			call("GET", strings.Replace(target, objectID, foreignObjectID, 1), "", 404, nil)
			call("HEAD", strings.Replace(target, objectID, foreignObjectID, 1), "", 404, []byte{})
			call("GET", strings.Replace(reportTarget, org.Workspace.ID, foreignWorkspace, 1), "", 404, nil)
			call("HEAD", strings.Replace(reportTarget, org.Workspace.ID, secondWorkspace, 1), "", 404, []byte{})
		}
		call("GET", target+"?tenant_id=foreign", "", 400, nil)
		if control("withdraw_resource")["ok"] != true {
			t.Fatal("withdraw fixture failed")
		}
		call("GET", target, "", 404, nil)
		if control("restore_resource")["ok"] != true {
			t.Fatal("restore fixture failed")
		}
		call("GET", target, "", 200, expected)
	})
	t.Run("platform_membership_and_object_revocation", func(t *testing.T) {
		if platformPostgres {
			// The original signed assertion has already passed GIS authorization
			// when actual local file reading is paused. Revoke on platform PG,
			// then require GIS's post-read current-authority callback to deny.
			for _, path := range []string{target, reportTarget} {
				if control("arm_read")["ok"] != true {
					t.Fatal("read barrier arm failed")
				}
				result := make(chan fetched, 1)
				go func(p string) { result <- fetch(p, cookie) }(path)
				started := false
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					if control("read_state")["started"] == true {
						started = true
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !started {
					control("release_read")
					t.Fatal("actual storage read barrier not reached")
				}
				suspended := "suspended"
				if err := s.ChangeMember(context.Background(), ownerID, org.Workspace.ID, actorID, nil, &suspended); err != nil {
					control("release_read")
					t.Fatal("mid-read real revocation failed")
				}
				before, err := chainPlatformSnapshot(migration)
				if err != nil {
					control("release_read")
					t.Fatal("revocation snapshot failed")
				}
				geoBefore := control("snapshot")["sha256"]
				if control("release_read")["ok"] != true {
					t.Fatal("read barrier release failed")
				}
				got := <-result
				if got.err != nil || got.status != 403 || strings.Contains(string(got.body), string(expected)) {
					t.Fatal("revoked signed in-flight object disclosed bytes or wrong denial")
				}
				assertObjectDenial(t, "GET", got.status, got.headers, got.body)
				for _, name := range []string{"Location", "Content-Disposition", "Content-Range"} {
					if got.headers.Get(name) != "" {
						t.Fatal("revoked object disclosed headers")
					}
				}
				after, err := chainPlatformSnapshot(migration)
				if err != nil || !reflect.DeepEqual(before, after) || geoBefore != control("snapshot")["sha256"] {
					t.Fatal("rejected in-flight object changed business state")
				}
				active := "active"
				if err := s.ChangeMember(context.Background(), ownerID, org.Workspace.ID, actorID, nil, &active); err != nil {
					t.Fatal("restore fixture membership failed")
				}
				call("GET", path, "", 200, expected)
			}
		}
		status := "suspended"
		if e := s.ChangeMember(context.Background(), ownerID, org.Workspace.ID, actorID, nil, &status); e != nil {
			t.Fatal(e)
		}
		call("GET", target, "", 404, nil)
		call("HEAD", target, "", 404, []byte{})
		if platformPostgres {
			call("GET", reportTarget, "", 404, nil)
			call("HEAD", reportTarget, "", 404, []byte{})
		}
		status = "active"
		if e := s.ChangeMember(context.Background(), ownerID, org.Workspace.ID, actorID, nil, &status); e != nil {
			t.Fatal(e)
		}
		call("GET", target, "", 200, expected)
		if control("revoke_object")["ok"] != true {
			t.Fatal("revoke fixture failed")
		}
		call("GET", target, "", 404, nil)
		if control("stats")["active_objects"] != activeObjects-1 {
			t.Fatal("revoked object remained active")
		}
	})
}
