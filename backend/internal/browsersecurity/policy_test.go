package browsersecurity

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestOriginConfiguration(t *testing.T) {
	for _, origin := range []string{"https://:443", "https://app.example:443", "http://127.0.0.1:80", "https://-host.example", "https://host..example"} {
		if _, err := Parse(origin, true); err == nil {
			t.Fatalf("noncanonical origin accepted: %s", origin)
		}
	}
	for _, origin := range []string{"", "*", "https://a,https://b", "https://a/", "https://a?", "https://a?q=x", "https://a#x", "https://u:p@a", "https://a:0", "https://a:65536", "https://a:080", "https://a:", "https://A", "https://a ", "http://localhost:8000", "http://127.0.0.2:8000", "http://example.com", "ftp://example.com"} {
		t.Run(origin, func(t *testing.T) {
			if _, err := Parse(origin, true); err == nil {
				t.Fatalf("accepted %q", origin)
			}
		})
	}
	for _, origin := range []string{"https://app.example", "https://app.example:8443", "http://127.0.0.1:8091", "http://[::1]:8091"} {
		p, err := Parse(origin, true)
		if err != nil || p.Secure != strings.HasPrefix(origin, "https:") {
			t.Fatalf("valid config %q: %v", origin, err)
		}
	}
	if _, err := Parse("http://127.0.0.1:8091", false); err == nil {
		t.Fatal("implicit development allowed")
	}
	t.Setenv("FLOW_AGRICULTURE_ENABLED", "true")
	t.Setenv("FLOW_AGRICULTURE_EXTERNAL_ORIGIN", "")
	if _, err := FromEnv(); err == nil {
		t.Fatal("missing configuration accepted")
	}
	t.Setenv("FLOW_AGRICULTURE_ENABLED", "false")
	if p, err := FromEnv(); err != nil || p != nil {
		t.Fatal("legacy profile changed")
	}
}

func boundary(t *testing.T) (*Policy, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	p, err := Parse("https://app.example", false)
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(p.Middleware())
	r.Any("/api/auth/login", func(c *gin.Context) { c.Status(204) })
	r.Any("/api/agriculture/workspaces", func(c *gin.Context) { c.Status(204) })
	r.POST("/internal/agriculture/authority", func(c *gin.Context) { c.Status(401) })
	return p, r
}

func TestStrictOriginAndSessionToken(t *testing.T) {
	_, r := boundary(t)
	for _, path := range []string{"/api/auth/login", "/api/agriculture/workspaces"} {
		for _, tc := range []struct {
			name    string
			origins []string
			tokens  []string
			cookie  string
			status  int
		}{
			{"valid", []string{"https://app.example"}, []string{csrfToken("session", "s.secret")}, "s.secret", 204},
			{"missing-origin", nil, []string{csrfToken("session", "s.secret")}, "s.secret", 403},
			{"null", []string{"null"}, nil, "s.secret", 403},
			{"duplicate", []string{"https://app.example", "https://app.example"}, nil, "s.secret", 403},
			{"comma", []string{"https://app.example,https://evil.example"}, nil, "s.secret", 403},
			{"downgrade", []string{"http://app.example"}, nil, "s.secret", 403},
			{"other-port", []string{"https://app.example:8443"}, nil, "s.secret", 403},
			{"missing-token", []string{"https://app.example"}, nil, "s.secret", 403},
			{"duplicate-token", []string{"https://app.example"}, []string{csrfToken("session", "s.secret"), csrfToken("session", "s.secret")}, "s.secret", 403},
			{"other-session", []string{"https://app.example"}, []string{csrfToken("session", "old.secret")}, "s.secret", 403},
			{"preauth-not-session", []string{"https://app.example"}, []string{csrfToken("preauth", "s.secret")}, "s.secret", 403},
		} {
			t.Run(path+tc.name, func(t *testing.T) {
				req := httptest.NewRequest("POST", path, nil)
				req.Host = "evil.example"
				req.Header.Set("X-Forwarded-Host", "app.example")
				req.Header.Set("X-Forwarded-Proto", "http")
				for _, v := range tc.origins {
					req.Header.Add("Origin", v)
				}
				for _, v := range tc.tokens {
					req.Header.Add(CSRFHeader, v)
				}
				req.AddCookie(&http.Cookie{Name: SessionCookie, Value: tc.cookie})
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				if w.Code != tc.status {
					t.Fatalf("status %d want %d", w.Code, tc.status)
				}
			})
		}
	}
	// A signed service route is not magically authenticated by skipping browser
	// CSRF: its own handler still returns 401 without service credentials.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/internal/agriculture/authority", nil))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

func TestRealHTTPAnonymousFlowAndKeyRotation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p, err := Parse("http://127.0.0.1:8091", true)
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(p.Middleware())
	r.POST("/api/auth/login", func(c *gin.Context) { c.Status(204) })
	s := httptest.NewServer(r)
	defer s.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	resp, err := client.Get(s.URL + "/api/auth/csrf")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(body.Data.Token) != 64 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("bad token response")
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].Secure || cookies[0].MaxAge != 600 || cookies[0].Domain != "" {
		t.Fatal("unsafe preauth cookie")
	}
	req, _ := http.NewRequest("POST", s.URL+"/api/auth/login", nil)
	req.Header.Set("Origin", p.Origin)
	req.Header.Set(CSRFHeader, body.Data.Token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatal(resp.StatusCode)
	}
	// A different key rejects preauth MAC, but the session credential still
	// derives the same token; no per-process session store was introduced.
	p2, _ := Parse(p.Origin, true)
	r2 := gin.New()
	r2.Use(p2.Middleware())
	r2.POST("/api/auth/login", func(c *gin.Context) { c.Status(204) })
	request := httptest.NewRequest("POST", "/api/auth/login", nil)
	request.Header.Set("Origin", p.Origin)
	request.Header.Set(CSRFHeader, body.Data.Token)
	request.AddCookie(cookies[0])
	w := httptest.NewRecorder()
	r2.ServeHTTP(w, request)
	if w.Code != 403 {
		t.Fatal("old anonymous context survived key rotation")
	}
	request = httptest.NewRequest("POST", "/api/auth/login", nil)
	request.Header.Set("Origin", p.Origin)
	request.Header.Set(CSRFHeader, csrfToken("session", "s.secret"))
	request.AddCookie(&http.Cookie{Name: SessionCookie, Value: "s.secret"})
	w = httptest.NewRecorder()
	r2.ServeHTTP(w, request)
	if w.Code != 204 {
		t.Fatal("session token invalidated by anonymous key rotation")
	}
}

func TestExpiredTamperedAndDuplicateCookies(t *testing.T) {
	p, r := boundary(t)
	for _, expiry := range []int64{time.Now().Unix() - 1, time.Now().Unix() + 3600} {
		value := fmt.Sprintf("%d.%s", expiry, strings.Repeat("a", 64))
		value += "." + p.guestMAC(value)
		req := httptest.NewRequest("POST", "/api/auth/login", nil)
		req.Header.Set("Origin", p.Origin)
		req.Header.Set(CSRFHeader, csrfToken("preauth", value))
		req.AddCookie(&http.Cookie{Name: p.guestCookie(), Value: value})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 403 {
			t.Fatal("invalid expiry accepted")
		}
	}
	req := httptest.NewRequest("POST", "/api/auth/login", nil)
	req.Header.Set("Origin", p.Origin)
	req.Header.Set(CSRFHeader, csrfToken("session", "s.secret"))
	for range 2 {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: "s.secret"})
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("duplicate session cookies accepted")
	}
}

func TestReplicaClockSkewIsBoundedWithoutExtendingExpiry(t *testing.T) {
	p, _ := Parse("https://app.example", false)
	issued := time.Unix(1800000000, 0)
	value := fmt.Sprintf("%d.%s", issued.Unix()+600, strings.Repeat("a", 64))
	value += "." + p.guestMAC(value)
	request := httptest.NewRequest("POST", "/api/auth/login", nil)
	request.AddCookie(&http.Cookie{Name: p.guestCookie(), Value: value})
	for _, tc := range []struct {
		offset time.Duration
		valid  bool
	}{{-5 * time.Second, true}, {-6 * time.Second, false}, {599 * time.Second, true}, {600 * time.Second, false}, {601 * time.Second, false}} {
		p.now = func() time.Time { return issued.Add(tc.offset) }
		_, _, ok := p.credential(request)
		if ok != tc.valid {
			t.Fatalf("offset %s: accepted=%v", tc.offset, ok)
		}
	}
}

func TestSharedKeyFileAndReplicaRestart(t *testing.T) {
	t.Setenv("FLOW_AGRICULTURE_ENABLED", "true")
	t.Setenv("FLOW_AGRICULTURE_EXTERNAL_ORIGIN", "https://app.example")
	dir := t.TempDir()
	path := filepath.Join(dir, "csrf.key")
	t.Setenv("FLOW_AGRICULTURE_CSRF_KEY_FILE", path)
	if _, err := FromEnv(); err == nil {
		t.Fatal("missing key accepted")
	}
	for _, tc := range []struct {
		size int
		mode os.FileMode
	}{{31, 0600}, {33, 0600}, {32, 0644}, {32, 0660}} {
		if err := os.WriteFile(path, []byte(strings.Repeat("x", tc.size)), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, tc.mode); err != nil {
			t.Fatal(err)
		}
		if _, err := FromEnv(); err == nil {
			t.Fatalf("unsafe key accepted: %+v", tc)
		}
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	os.Chmod(path, 0600)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLOW_AGRICULTURE_CSRF_KEY_FILE", link)
	if _, err := FromEnv(); err == nil {
		t.Fatal("symlink accepted")
	}
	t.Setenv("FLOW_AGRICULTURE_CSRF_KEY_FILE", dir)
	if _, err := FromEnv(); err == nil {
		t.Fatal("directory accepted")
	}
	t.Setenv("FLOW_AGRICULTURE_CSRF_KEY_FILE", path)
	p1, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	p2, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	value := fmt.Sprintf("%d.%s", time.Now().Unix()+600, strings.Repeat("a", 64))
	value += "." + p1.guestMAC(value)
	req := httptest.NewRequest("POST", "/api/auth/login", nil)
	req.Header.Set("Origin", p1.Origin)
	req.Header.Set(CSRFHeader, csrfToken("preauth", value))
	req.AddCookie(&http.Cookie{Name: p1.guestCookie(), Value: value})
	r := gin.New()
	r.Use(p2.Middleware())
	r.POST("/api/auth/login", func(c *gin.Context) { c.Status(204) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatal("shared key rejected across replicas/restart")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("y", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	p3, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	r3 := gin.New()
	r3.Use(p3.Middleware())
	r3.POST("/api/auth/login", func(c *gin.Context) { c.Status(204) })
	w = httptest.NewRecorder()
	r3.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("rotated key accepted old nonce")
	}
}
