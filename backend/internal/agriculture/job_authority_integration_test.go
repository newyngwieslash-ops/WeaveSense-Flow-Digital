package agriculture

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/gin-gonic/gin"
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
)

// Platform uses real SQLite authorization; GIS uses isolated PostgreSQL roles,
// append-only snapshots/audit and a clearly test-only durable lease provider.
func TestJobAuthorityRealHTTPPostgres(t *testing.T) {
	python := os.Getenv("AGRI_IDENTITY_PYTHON")
	if python == "" || os.Getenv("AGRI_TEST_PG_ISOLATED") != "1" || os.Getenv("AGRI_TEST_GEO_MIGRATION_DSN") == "" {
		t.Skip("explicit isolated PG/Python required")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	s, org := fixture(t)
	join(t, s, org, "bob", "analyst")
	now := time.Now().UTC().Truncate(time.Second)
	platform, platformKey := identityTestKeys(t, now, 0x61)
	gisKeys, gisKey := identityTestKeys(t, now, 0x62)
	router := gin.New()
	callback := httptest.NewServer(router)
	defer callback.Close()
	cfg := map[string]any{"tenant": org.Tenant.ID, "workspace": org.Workspace.ID, "platform": callback.URL,
		"owner_dsn": os.Getenv("AGRI_TEST_GEO_MIGRATION_DSN"), "app_dsn": os.Getenv("AGRI_TEST_GEO_RUNTIME_DSN"), "worker_dsn": os.Getenv("AGRI_TEST_GEO_WORKER_DSN"),
		"platform_keys": []any{map[string]any{"kid": platformKey.ID, "public_key": base64.StdEncoding.EncodeToString(platformKey.PublicKey), "not_before": platformKey.NotBefore.Format(time.RFC3339), "not_after": platformKey.NotAfter.Format(time.RFC3339), "revoked": false}},
		"callback_key":  map[string]any{"kid": gisKey.ID, "seed": base64.StdEncoding.EncodeToString(gisKey.PrivateKey.Seed()), "not_before": gisKey.NotBefore.Format(time.RFC3339), "not_after": gisKey.NotAfter.Format(time.RFC3339)}}
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	raw, _ := json.Marshal(cfg)
	path := filepath.Join(dir, "private.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, filepath.Join(root, "services/agri-gis/backend/tests/job_http_fixture.py"), path)
	cmd.Dir = filepath.Join(root, "services/agri-gis/backend")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "PYTHONPATH=" + cmd.Dir, "PYTHONUNBUFFERED=1", "HOME=" + dir, "TMPDIR=" + dir}
	stdout, _ := cmd.StdoutPipe()
	stdin, _ := cmd.StdinPipe()
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		t.Fatal("start real GIS process")
	}
	defer func() { _ = stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	messages := make(chan map[string]any, 8)
	go func() {
		scan := bufio.NewScanner(stdout)
		for scan.Scan() {
			var value map[string]any
			if json.Unmarshal(scan.Bytes(), &value) == nil {
				messages <- value
			}
		}
		close(messages)
	}()
	receive := func() map[string]any {
		t.Helper()
		select {
		case value := <-messages:
			if value == nil {
				t.Fatal("GIS fixture exited")
			}
			return value
		case <-time.After(15 * time.Second):
			t.Fatal("GIS fixture readiness deadline")
			return nil
		}
	}
	port, _ := receive()["port"].(float64)
	if port < 1 || port > 65535 {
		t.Fatal("invalid fixture readiness")
	}
	base := "http://127.0.0.1:" + strconv.Itoa(int(port))
	client := &http.Client{Timeout: 4 * time.Second}
	for deadline := time.Now().Add(5 * time.Second); ; {
		response, e := client.Get(base + "/health")
		if e == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("GIS health unavailable")
		}
		time.Sleep(20 * time.Millisecond)
	}
	gis, err := NewGISClient(base, platform)
	if err != nil {
		t.Fatal(err)
	}
	s.Ownership = gis
	s.RegisterInternalAuthority(router, gisKeys)
	if _, err = s.GrantResourceBinding(context.Background(), "owner", org.Workspace.ID, "bob", ResourceRef{"farm", "farm_a"}, []string{"agri.analysis.submit"}, nil, revisionPointer(0)); err != nil {
		t.Fatal("grant fixture capability", err)
	}
	input := []byte(`{"algorithm":"ndvi","revision":1}`)
	snapshot, _, err := s.NewJobAuthority(context.Background(), "bob", org.Workspace.ID, "operation_chain", []ResourceRef{{"asset", "asset_a"}}, input)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := snapshot.Digest()
	checkpoint := JobCheckpoint{snapshot.JobID, digest, "worker_fixture", 1, 1, "claim", snapshot.InputHash}
	control := func(command string) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"command": command, "job_id": snapshot.JobID})
		_, _ = stdin.Write(append(raw, '\n'))
		return receive()
	}
	send := func(body []byte, token string, want int) {
		t.Helper()
		req, _ := http.NewRequest("POST", base+jobCheckpointPath, strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+token)
		response, e := client.Do(req)
		if e != nil {
			t.Fatal("actual GIS request failed")
		}
		defer response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("checkpoint status=%d expected=%d", response.StatusCode, want)
		}
	}
	mint := func(c JobCheckpoint) (string, []byte) {
		t.Helper()
		decision, e := s.CheckJobAuthority(context.Background(), snapshot, digest)
		if e != nil {
			t.Fatal(e)
		}
		body, _ := json.Marshal(c)
		token, canonical, e := s.issueDelegation(context.Background(), platform, "bob", decision, "POST", jobCheckpointPath, body, snapshot.OperationID)
		if e != nil {
			t.Fatal(e)
		}
		return token, canonical
	}
	t.Run("creation_and_checkpoint", func(t *testing.T) {
		if _, e := gis.RegisterJobAuthority(context.Background(), s, snapshot, input); e != nil {
			t.Fatal("persist signed creation", e)
		}
		if control("lease")["ok"] != true {
			t.Fatal("actual lease fixture")
		}
		if _, e := gis.CheckJobExecution(context.Background(), s, snapshot, checkpoint); e != nil {
			t.Fatal("current checkpoint", e)
		}
		checkpoint.Phase = "read_input"
		if _, e := gis.CheckJobExecution(context.Background(), s, snapshot, checkpoint); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("forged_scope_and_expired_identity", func(t *testing.T) {
		token, body := mint(checkpoint)
		send(append(body[:len(body)-1], []byte(`,"tenant_id":"foreign"}`)...), token, 401)
		claims, e := verifyDelegation(platform, token, "POST", jobCheckpointPath, body, s.now())
		if e != nil {
			t.Fatal(e)
		}
		claims.TenantID = "tenant_foreign"
		bad, e := platform.sign(delegationType, claims, s.now(), s.now().Add(30*time.Second))
		if e != nil {
			t.Fatal(e)
		}
		send(body, bad, 404)
		claims.TenantID = snapshot.TenantID
		claims.ResourceScope = []ResourceRef{{"asset", "asset_foreign"}}
		claims.ResourceVersions = []ResourceVersion{{Resource: claims.ResourceScope[0], Revision: 1}}
		bad, e = platform.sign(delegationType, claims, s.now(), s.now().Add(30*time.Second))
		if e != nil {
			t.Fatal(e)
		}
		send(body, bad, 403)
		claims.ResourceScope = snapshot.Resources
		claims.ResourceVersions = snapshot.ResourceVersions
		claims.IssuedAt = now.Add(-time.Minute).Format(time.RFC3339)
		claims.ExpiresAt = now.Add(-30 * time.Second).Format(time.RFC3339)
		bad, e = platform.sign(delegationType, claims, now.Add(-time.Minute), now.Add(-30*time.Second))
		if e != nil {
			t.Fatal(e)
		}
		send(body, bad, 401)
	})
	t.Run("retry_fencing_and_withdrawal", func(t *testing.T) {
		control("retry")
		token, body := mint(checkpoint)
		send(body, token, 403)
		checkpoint.Attempt = 2
		checkpoint.Fencing = 2
		checkpoint.Phase = "publish"
		if _, e := gis.CheckJobExecution(context.Background(), s, snapshot, checkpoint); e != nil {
			t.Fatal("same snapshot new lease", e)
		}
		token, body = mint(checkpoint)
		control("withdraw")
		send(body, token, 404)
		control("restore")
	})
	t.Run("current_platform_revocation", func(t *testing.T) {
		token, body := mint(checkpoint)
		roles := []string{"viewer"}
		if e := s.ChangeMember(context.Background(), "owner", org.Workspace.ID, "bob", &roles, nil); e != nil {
			t.Fatal(e)
		}
		send(body, token, 403)
		if _, e := gis.CheckJobExecution(context.Background(), s, snapshot, checkpoint); e == nil {
			t.Fatal("withdrawn platform rights minted")
		}
		if control("stats")["checks"] != float64(3) {
			t.Fatal("rejected requests changed persistent checks")
		}
	})
}
