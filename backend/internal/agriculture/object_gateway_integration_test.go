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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestObjectGatewayRealHTTPPostgres(t *testing.T) {
	python := os.Getenv("AGRI_IDENTITY_PYTHON")
	if python == "" || os.Getenv("AGRI_TEST_PG_ISOLATED") != "1" || os.Getenv("AGRI_TEST_GEO_MIGRATION_DSN") == "" {
		t.Skip("requires explicit isolated GIS PostgreSQL and Python")
	}
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	s, org := fixture(t)
	join(t, s, org, "bob", "viewer")
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
	expected := []byte("synthetic-private-object-v1\n0123456789\n")
	digest := sha256.Sum256(expected)
	if port < 1 || port > 65535 || !ownershipID.MatchString(objectID) || ready["version"] != float64(1) || ready["content_sha256"] != hex.EncodeToString(digest[:]) || ready["size_bytes"] != float64(len(expected)) {
		t.Fatal("invalid object fixture readiness")
	}
	client, e := NewGISClient("http://127.0.0.1:"+strconv.Itoa(int(port)), platform)
	if e != nil {
		t.Fatal(e)
	}
	s.Ownership = client
	s.RegisterInternalAuthority(callbackRouter, gisKeys)
	if _, e = s.GrantResourceBinding(context.Background(), "owner", org.Workspace.ID, "bob", ResourceRef{"asset", "asset_a"}, []string{"agri.asset_download.read"}, nil, revisionPointer(0)); e != nil {
		t.Fatalf("fixture grant: %v", e)
	}
	server, cookie := objectSessionServer(t, s)
	target := fmt.Sprintf("/api/agri/v1/workspaces/%s/objects/%s/versions/1", org.Workspace.ID, objectID)
	control := func(name string) map[string]any {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"command": name})
		if _, e := stdin.Write(append(b, '\n')); e != nil {
			t.Fatal("private fixture control closed")
		}
		return receive()
	}
	call := func(method, p, rng string, want int, wantBody []byte) {
		t.Helper()
		h := http.Header{}
		if rng != "" {
			h.Set("Range", rng)
		}
		status, headers, body := objectHTTP(t, server, cookie, method, p, h)
		if status != want {
			t.Fatalf("object method=%s expected=%d observed=%d", method, want, status)
		}
		if wantBody != nil && string(body) != string(wantBody) {
			t.Fatal("actual GIS bytes differ")
		}
		if !strings.Contains(headers.Get("Cache-Control"), "no-store") || headers.Get("ETag") != "" || headers.Get("Set-Cookie") != "" || strings.Contains(string(body), "object_test_fixture") {
			t.Fatal("private object metadata/cache leaked")
		}
	}
	t.Run("session_get_head_ranges", func(t *testing.T) {
		status, h, _ := objectHTTP(t, server, nil, "GET", target, nil)
		if status != 401 || !strings.Contains(h.Get("Cache-Control"), "no-store") {
			t.Fatal("anonymous private access")
		}
		call("GET", target, "", 200, expected)
		call("HEAD", target, "", 200, []byte{})
		call("GET", target, "bytes=0-8", 206, expected[:9])
		call("GET", target, "bytes=-4", 206, expected[len(expected)-4:])
		call("GET", target, "bytes=999-", 416, nil)
		if control("stats")["active_objects"] != float64(1) {
			t.Fatal("read mutated private object registration")
		}
	})
	t.Run("scope_and_current_resource_withdrawal", func(t *testing.T) {
		call("GET", strings.Replace(target, org.Workspace.ID, "foreign_workspace", 1), "", 404, nil)
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
		status := "suspended"
		if e := s.ChangeMember(context.Background(), "owner", org.Workspace.ID, "bob", nil, &status); e != nil {
			t.Fatal(e)
		}
		call("GET", target, "", 404, nil)
		status = "active"
		if e := s.ChangeMember(context.Background(), "owner", org.Workspace.ID, "bob", nil, &status); e != nil {
			t.Fatal(e)
		}
		call("GET", target, "", 200, expected)
		if control("revoke_object")["ok"] != true {
			t.Fatal("revoke fixture failed")
		}
		call("GET", target, "", 404, nil)
		if control("stats")["active_objects"] != float64(0) {
			t.Fatal("revoked object remained active")
		}
	})
}
