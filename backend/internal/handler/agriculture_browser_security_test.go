package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"yingce/backend/internal/auth"
	"yingce/backend/internal/browsersecurity"
	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
	"yingce/backend/internal/service"
)

func TestAgricultureCookieSecurityUsesDeploymentNotProxyHeaders(t *testing.T) {
	for _, tc := range []struct {
		origin, forwarded string
		secure            bool
	}{{"https://app.example", "http", true}, {"http://127.0.0.1:8091", "https", false}} {
		p, err := browsersecurity.Parse(tc.origin, true)
		if err != nil {
			t.Fatal(err)
		}
		r := gin.New()
		r.Use(p.Middleware())
		r.GET("/fixture", func(c *gin.Context) { setSessionCookie(c, "fixture", 30); clearSessionCookie(c) })
		req := httptest.NewRequest("GET", "/fixture", nil)
		req.Header.Set("X-Forwarded-Proto", tc.forwarded)
		req.Header.Set("X-Forwarded-Host", "attacker.invalid")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		count := 0
		for _, cookie := range w.Result().Cookies() {
			if cookie.Name == service.SessionCookieName {
				count++
				if cookie.Secure != tc.secure || !cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteLaxMode {
					t.Fatal("unsafe cookie")
				}
			}
		}
		if count != 2 {
			t.Fatal("set and clear must share security flags")
		}
	}
}

func TestAgricultureLogoutRequiresCurrentSessionCSRFBeforeRealRevocation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	if err = db.AutoMigrate(&model.User{}, &model.AuthSession{}, &model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&model.User{ID: "owner", Username: "owner", Status: model.UserStatusActive}).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&model.AuthSession{ID: "session", UserID: "owner", TokenHash: auth.HashToken("secret"), ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	svc := service.New(repository.New(db), t.TempDir())
	t.Cleanup(func() { svc.Close() })
	// A new DB connection and service instance must resolve persisted sessions,
	// not a process-local login cache; the same instance observes later logout.
	db2, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sql2, _ := db2.DB()
	t.Cleanup(func() { sql2.Close() })
	restarted := service.New(repository.New(db2), t.TempDir())
	t.Cleanup(func() { restarted.Close() })
	if _, err := restarted.CurrentUser("session.secret"); err != nil {
		t.Fatal("persisted session lost on service recreation")
	}
	p, _ := browsersecurity.Parse("https://app.example", false)
	r := gin.New()
	r.Use(p.Middleware())
	RegisterAuthRoutes(r.Group("/api"), svc)
	getToken := func(cookie string) string {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/auth/csrf", nil)
		req.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: cookie})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var out struct {
			Data struct {
				Token string `json:"token"`
			} `json:"data"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal("token acquisition failed")
		}
		return out.Data.Token
	}
	token := getToken("session.secret")
	for _, tc := range []struct{ origin, token string }{{"", token}, {"https://evil.example", token}, {p.Origin, ""}, {p.Origin, getToken("another.secret")}} {
		req := httptest.NewRequest("POST", "/api/auth/logout", nil)
		req.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: "session.secret"})
		req.Header.Set("Origin", tc.origin)
		req.Header.Set(browsersecurity.CSRFHeader, tc.token)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 403 {
			t.Fatal("untrusted logout accepted")
		}
		if _, err := svc.CurrentUser("session.secret"); err != nil {
			t.Fatal("CSRF rejection mutated live session")
		}
	}
	req := httptest.NewRequest("POST", "/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: "session.secret"})
	req.Header.Set("Origin", p.Origin)
	req.Header.Set(browsersecurity.CSRFHeader, token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if _, err := svc.CurrentUser("session.secret"); err == nil {
		t.Fatal("logout did not revoke persisted session")
	}
	if _, err := restarted.CurrentUser("session.secret"); err == nil {
		t.Fatal("second service accepted revoked cookie")
	}
	// Stale/unknown credentials can obtain their bound CSRF value and attempt
	// a legitimate login; token issuance is not proof of authenticated identity.
	if len(getToken("expired.credential")) != 64 {
		t.Fatal("stale cookie traps login recovery")
	}
}

func TestAgricultureUnboundOAuthIsClosedWithoutConsumingState(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	if err = db.AutoMigrate(&model.User{}, &model.SystemSetting{}, &model.OAuthState{}, &model.SMSChannel{}); err != nil {
		t.Fatal(err)
	}
	for _, record := range []any{
		&model.User{ID: "owner", Username: "owner", Status: model.UserStatusActive},
		&model.SystemSetting{Key: "linuxdo_oauth", ValueJSON: `{"enabled":true}`},
		&model.OAuthState{ID: "fixture-state", Provider: "linuxdo", StateHash: auth.HashToken("fixture-valid-state"), CodeVerifier: "fixture-pkce", ExpiresAt: time.Now().Add(time.Minute)},
	} {
		if err = db.Create(record).Error; err != nil {
			t.Fatal(err)
		}
	}
	svc := service.New(repository.New(db), t.TempDir())
	t.Cleanup(func() { svc.Close() })
	p, _ := browsersecurity.Parse("https://app.example", false)
	r := gin.New()
	r.Use(p.Middleware())
	RegisterAuthRoutes(r.Group("/api"), svc)
	RegisterOAuthCallbackRoutes(r, svc)
	server := httptest.NewServer(r)
	defer server.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, path := range []string{"/api/auth/linuxdo/start?acceptedTerms=true", "/api/auth/linuxdo/callback?state=fixture-valid-state&code=fixture-code", "/oauth/linuxdo/callback?state=fixture-valid-state&code=fixture-code"} {
		response, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Reason string `json:"reason"`
		}
		err = json.NewDecoder(response.Body).Decode(&body)
		response.Body.Close()
		if err != nil || response.StatusCode != 503 || body.Reason != "oauth_browser_binding_unavailable" || len(response.Cookies()) != 0 || response.Header.Get("Location") != "" {
			t.Fatal("unsafe OAuth route remained usable")
		}
	}
	var state model.OAuthState
	if err = db.First(&state, "id=?", "fixture-state").Error; err != nil || state.UsedAt != nil {
		t.Fatal("disabled callback consumed valid global state")
	}
	for _, agriculture := range []bool{false, true} {
		router := gin.New()
		if agriculture {
			router.Use(p.Middleware())
		}
		RegisterAuthRoutes(router.Group("/api"), svc)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/api/auth/settings", nil))
		var out struct {
			Data struct {
				Enabled bool `json:"linuxdoEnabled"`
			} `json:"data"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.Data.Enabled == agriculture {
			t.Fatalf("provider advertisement mismatch agriculture=%v status=%d", agriculture, w.Code)
		}
	}
}
