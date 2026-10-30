package agriculture

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

// Actual persisted auth.Login session, not an actor header or fabricated cookie.
func objectSessionServer(t *testing.T, s Service) (*httptest.Server, *http.Cookie) {
	t.Helper()
	if e := s.DB.AutoMigrate(&model.AuthSession{}, &model.UserIdentity{}); e != nil {
		t.Fatal(e)
	}
	password := auth.RandomToken()
	hash, e := auth.HashPassword(password)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.DB.Model(&model.User{}).Where("id=?", "bob").Update("password_hash", hash).Error; e != nil {
		t.Fatal(e)
	}
	authentication := auth.New(repository.New(s.DB), nil, nil)
	login, e := authentication.Login(auth.LoginRequest{Username: "bob", Password: password})
	if e != nil {
		t.Fatal("fixture real login failed")
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	API{Service: s, User: func(c *gin.Context) (*model.User, error) {
		cookie, _ := c.Cookie(auth.SessionCookieName)
		return authentication.CurrentUser(cookie)
	}}.Register(router.Group("/api"))
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server, &http.Cookie{Name: auth.SessionCookieName, Value: login.Session, Path: "/", HttpOnly: true}
}

func objectHTTP(t *testing.T, server *httptest.Server, cookie *http.Cookie, method, path string, headers http.Header) (int, http.Header, []byte) {
	t.Helper()
	req, e := http.NewRequest(method, server.URL+path, nil)
	if e != nil {
		t.Fatal(e)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	res, e := server.Client().Do(req)
	if e != nil {
		t.Fatal("fixture HTTP transport failed")
	}
	defer res.Body.Close()
	body, e := io.ReadAll(res.Body)
	if e != nil {
		t.Fatal("gateway returned truncated response")
	}
	return res.StatusCode, res.Header, body
}

func TestObjectGatewayRealSessionAndStrictTransport(t *testing.T) {
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	s, org := fixture(t)
	join(t, s, org, "bob", "viewer")
	keys, _ := identityTestKeys(t, time.Now(), 0x71)
	var mode atomic.Value
	mode.Store("")
	var downloads atomic.Int32
	ref := ResourceRef{"asset", "asset_a"}
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := mode.Load().(string)
		if r.Method == "POST" {
			raw, _ := io.ReadAll(r.Body)
			capability := "ownership.resolve"
			if r.URL.Path == objectResolvePath {
				capability = "object.resolve"
			}
			assertion, e := verifyServiceAssertion(keys, capability, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), r.Method, r.URL.Path, raw, time.Now())
			if e != nil {
				t.Logf("fixture service assertion rejected capability=%s path=%s error=%v", capability, r.URL.Path, e)
				http.Error(w, "fixture_signature_rejected", 401)
				return
			}
			if assertion.TenantID != org.Tenant.ID || assertion.WorkspaceID != org.Workspace.ID {
				http.Error(w, "fixture_scope_rejected", 404)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == objectResolvePath {
				out := objectReference{org.Tenant.ID, org.Workspace.ID, "object_a", 1, ref, 1}
				switch current {
				case "wrong_tenant":
					out.TenantID = "other"
				case "wrong_workspace":
					out.WorkspaceID = "other"
				case "wrong_version":
					out.Version = 2
				case "bad_kind":
					out.Resource.Kind = "farm"
				case "stale_revision":
					out.ResourceRevision = 2
				case "unknown_metadata":
					io.WriteString(w, `{"extra":true}`)
					return
				case "duplicate_metadata":
					io.WriteString(w, `{"tenant_id":"a","tenant_id":"b"}`)
					return
				}
				json.NewEncoder(w).Encode(out)
				return
			}
			now := time.Now().UTC().Truncate(time.Second)
			json.NewEncoder(w).Encode(map[string]any{"resources": []ownershipWire{{Resource: ref, TenantID: org.Tenant.ID, WorkspaceID: org.Workspace.ID, FarmIDs: []string{"farm_a"}, Revision: 1, Status: "active", ReceiptID: "receipt_a", IssuedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(30 * time.Second).Format(time.RFC3339)}}})
			return
		}
		downloads.Add(1)
		claims, e := verifyDelegation(keys, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), r.Method, r.URL.Path, nil, time.Now())
		if e != nil || claims.Actor.ID != "bob" || claims.TenantID != org.Tenant.ID {
			http.Error(w, "fixture_delegation_rejected", 403)
			return
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Accept-Encoding") != "identity" {
			http.Error(w, "fixture_credentials_leaked", 500)
			return
		}
		if current == "redirect" {
			w.Header().Set("Location", "https://must-not-follow.invalid/secret")
			w.WriteHeader(302)
			return
		}
		if current == "range_unavailable" {
			w.WriteHeader(416)
			return
		}
		body := []byte("abcdef")
		status := 200
		switch r.Header.Get("Range") {
		case "bytes=0-2":
			status = 206
			body = body[:3]
			w.Header().Set("Content-Range", "bytes 0-2/6")
		case "bytes=2-":
			status = 206
			body = body[2:]
			w.Header().Set("Content-Range", "bytes 2-5/6")
		case "bytes=-2":
			status = 206
			body = body[4:]
			w.Header().Set("Content-Range", "bytes 4-5/6")
		}
		w.Header().Set("Set-Cookie", "private-upstream-secret=never-forward")
		w.Header().Set("ETag", "private-upstream-secret")
		w.Header().Set("Cache-Control", "public,max-age=99999")
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		switch current {
		case "png":
			w.Header().Set("Content-Type", "image/png")
		case "wrong_range":
			w.Header().Set("Content-Range", "bytes 1-3/6")
		case "bad_encoding":
			w.Header().Set("Content-Encoding", "gzip")
		case "oversized":
			w.Header().Set("Content-Length", strconv.FormatInt(maximumObjectBytes+1, 10))
			body = nil
		case "truncated":
			w.Header().Set("Content-Length", "7")
		case "chunked":
			w.Header().Del("Content-Length")
			w.WriteHeader(status)
			w.(http.Flusher).Flush()
			w.Write(body)
			return
		}
		w.WriteHeader(status)
		if r.Method != "HEAD" {
			w.Write(body)
		}
	}))
	defer peer.Close()
	client, e := NewGISClient(peer.URL, keys)
	if e != nil {
		t.Fatal(e)
	}
	s.Ownership = client
	if _, e = s.GrantResourceBinding(context.Background(), "owner", org.Workspace.ID, "bob", ref, []string{"agri.asset_download.read"}, nil, revisionPointer(0)); e != nil {
		t.Fatal(e)
	}
	server, cookie := objectSessionServer(t, s)
	if _, e := s.Authorize(context.Background(), "bob", org.Workspace.ID, "agri.asset_download.read", []ResourceRef{ref}); e != nil {
		t.Fatalf("fixture direct authorization failed: %v", e)
	}
	if _, e := client.resolveObject(context.Background(), OwnershipContext{org.Tenant.ID, org.Workspace.ID}, "object_a", 1); e != nil {
		t.Fatalf("fixture metadata resolution failed: %v", e)
	}
	path := fmt.Sprintf("/api/agri/v1/workspaces/%s/objects/object_a/versions/1", org.Workspace.ID)
	t.Run("actual_session_get_head_range_and_filtered_headers", func(t *testing.T) {
		for _, tc := range []struct {
			method, rng, body string
			status            int
		}{{"GET", "", "abcdef", 200}, {"HEAD", "", "", 200}, {"GET", "bytes=0-2", "abc", 206}, {"GET", "bytes=2-", "cdef", 206}, {"GET", "bytes=-2", "ef", 206}} {
			headers := http.Header{}
			if tc.rng != "" {
				headers.Set("Range", tc.rng)
			}
			status, h, b := objectHTTP(t, server, cookie, tc.method, path, headers)
			if status != tc.status || string(b) != tc.body {
				t.Fatalf("method=%s range=%s status=%d", tc.method, tc.rng, status)
			}
			if (h.Get("Cache-Control") != "private, no-store" && h.Get("Cache-Control") != "no-store") || h.Get("Vary") != "Cookie" || h.Get("Set-Cookie") != "" || h.Get("ETag") != "" || h.Get("Content-Type") != "application/octet-stream" || !strings.Contains(h.Get("Content-Disposition"), "attachment") || h.Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("unsafe response headers")
			}
		}
		mode.Store("png")
		status, h, _ := objectHTTP(t, server, cookie, "GET", path, nil)
		if status != 200 || h.Get("Content-Type") != "image/png" || h.Get("Content-Disposition") != "inline" {
			t.Fatal("safe raster MIME lost")
		}
		mode.Store("")
	})
	t.Run("malformed_request_and_session_fail_before_domain_download", func(t *testing.T) {
		before := downloads.Load()
		status, h, _ := objectHTTP(t, server, nil, "GET", path, nil)
		if status != 401 {
			t.Fatal("anonymous download accepted")
		}
		if h.Get("Cache-Control") != "no-store" && h.Get("Cache-Control") != "private, no-store" {
			t.Fatal("anonymous response cacheable")
		}
		if h.Get("Content-Security-Policy") == "" || h.Get("Vary") != "Cookie" || h.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("anonymous response lost private headers")
		}
		forged := *cookie
		forged.Value += "forged"
		status, _, _ = objectHTTP(t, server, &forged, "GET", path, nil)
		if status != 401 {
			t.Fatal("forged cookie accepted")
		}
		for _, p := range []string{path + "?tenant_id=other", strings.TrimSuffix(path, "1") + "01", strings.TrimSuffix(path, "1") + "0"} {
			status, _, _ = objectHTTP(t, server, cookie, "GET", p, nil)
			if status != 400 {
				t.Fatal("ambiguous object request accepted")
			}
		}
		for _, h := range []http.Header{{"Range": []string{"bytes=0-1,3-4"}}, {"Range": []string{"bytes=3-1"}}, {"Range": []string{"bytes=-0"}}, {"Range": []string{"bytes=0-1", "bytes=2-3"}}, {"If-Range": []string{"etag"}}} {
			status, _, _ = objectHTTP(t, server, cookie, "GET", path, h)
			if status != 400 {
				t.Fatal("ambiguous range accepted")
			}
		}
		if downloads.Load() != before {
			t.Fatal("rejected request reached object bytes")
		}
	})
	t.Run("strict_metadata_and_bytes_contract", func(t *testing.T) {
		for _, name := range []string{"wrong_tenant", "wrong_workspace", "wrong_version", "bad_kind", "unknown_metadata", "duplicate_metadata", "bad_encoding", "oversized", "truncated", "chunked", "redirect"} {
			mode.Store(name)
			status, h, b := objectHTTP(t, server, cookie, "GET", path, nil)
			if status != 503 {
				t.Fatalf("unsafe %s accepted status=%d", name, status)
			}
			if strings.Contains(string(b), "private-upstream-secret") || h.Get("Location") != "" || h.Get("Set-Cookie") != "" {
				t.Fatal("upstream private material exposed")
			}
		}
		mode.Store("stale_revision")
		status, _, _ := objectHTTP(t, server, cookie, "GET", path, nil)
		if status != 409 {
			t.Fatal("metadata revision drift accepted")
		}
		mode.Store("wrong_range")
		status, _, _ = objectHTTP(t, server, cookie, "GET", path, http.Header{"Range": []string{"bytes=0-2"}})
		if status != 503 {
			t.Fatal("different range accepted")
		}
		mode.Store("range_unavailable")
		status, _, _ = objectHTTP(t, server, cookie, "GET", path, nil)
		if status != 416 {
			t.Fatal("range failure disguised")
		}
		mode.Store("")
	})
	t.Run("foreign_scope_and_revocation_rechecked", func(t *testing.T) {
		before := downloads.Load()
		status, _, _ := objectHTTP(t, server, cookie, "GET", strings.Replace(path, org.Workspace.ID, "other_workspace", 1), nil)
		if status != 404 {
			t.Fatal("foreign scope accepted")
		}
		state := "suspended"
		if e := s.ChangeMember(context.Background(), "owner", org.Workspace.ID, "bob", nil, &state); e != nil {
			t.Fatal(e)
		}
		status, _, _ = objectHTTP(t, server, cookie, "GET", path, nil)
		if status != 404 {
			t.Fatalf("revoked actor status=%d", status)
		}
		if downloads.Load() != before {
			t.Fatal("revoked request reached bytes")
		}
	})
}

func TestServiceAssertionsNormalizeNonUTCClock(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	keys, _ := identityTestKeys(t, now, 0x73)
	for _, capability := range []string{"ownership.resolve", "object.resolve", "authority.check", "job.authority.check"} {
		_, _, path, e := serviceDirection(capability)
		if e != nil {
			t.Fatal(e)
		}
		token, body, e := issueServiceAssertion(keys, capability, OwnershipContext{"tenant_fixture", "workspace_fixture"}, []byte(`{"fixture":true}`), now.In(time.FixedZone("fixture_JST", 9*3600)))
		if e != nil {
			t.Fatal(e)
		}
		claims, e := verifyServiceAssertion(keys, capability, token, "POST", path, body, now)
		if e != nil || claims.IssuedAt != now.Format(time.RFC3339) {
			t.Fatalf("nonUTC clock broke %s", capability)
		}
	}
}
