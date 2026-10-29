package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/auth"
	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
	"yingce/backend/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLogoutAPIDatabaseFailureIsNotSuccess(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.User{}, &model.AuthSession{}, &model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.User{ID: "owner", Username: "owner", Status: model.UserStatusActive}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AuthSession{ID: "session", UserID: "owner", TokenHash: auth.HashToken("secret"), ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	// A real database rejection exercises repository error propagation, not a
	// mocked successful logout. The original session must remain retryable.
	if err := db.Exec(`CREATE TRIGGER deny_logout BEFORE DELETE ON auth_sessions BEGIN SELECT RAISE(ABORT, 'private-database-detail'); END`).Error; err != nil {
		t.Fatal(err)
	}
	svc := service.New(repository.New(db), t.TempDir())
	t.Cleanup(func() { _ = svc.Close() })
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterAuthRoutes(router.Group("/api"), svc)
	logout := func(cookie string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
		req.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: cookie})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	w := logout("session.secret")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("database failure status=%d want 500 body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Code == 0 {
		t.Fatalf("failure returned success envelope: %s", w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("failed logout cleared caller cookie before server revocation")
	}
	if strings.Contains(w.Body.String(), "private-database-detail") {
		t.Fatal("database detail leaked to client")
	}
	if _, err := svc.CurrentUser("session.secret"); err != nil {
		t.Fatalf("failed deletion unexpectedly revoked session: %v", err)
	}
	if err := db.Exec("DROP TRIGGER deny_logout").Error; err != nil {
		t.Fatal(err)
	}
	w = logout("session.secret")
	if w.Code != http.StatusOK {
		t.Fatalf("retry failed: status=%d body=%s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != service.SessionCookieName || cookies[0].MaxAge >= 0 {
		t.Fatal("successful logout did not expire caller cookie")
	}
	if _, err := svc.CurrentUser("session.secret"); err == nil {
		t.Fatal("successful logout left session authenticated")
	}
}

func TestSessionAPIInfrastructureFailureIsNotAnonymousSuccess(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.User{}, &model.AuthSession{}, &model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	svc := service.New(repository.New(db), t.TempDir())
	t.Cleanup(func() { _ = svc.Close() })
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterAuthRoutes(router.Group("/api"), svc)
	get := func(cookie string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
		req.AddCookie(&http.Cookie{Name: service.SessionCookieName, Value: cookie})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	// Existing anonymous/unknown-session response stays compatible.
	for _, cookie := range []string{"", "unknown.secret"} {
		w := get(cookie)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"user":null`) {
			t.Fatalf("anonymous contract changed: %d %s", w.Code, w.Body.String())
		}
	}
	if err := db.Create(&model.User{ID: "disabled", Username: "disabled", Status: model.UserStatusDisabled}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AuthSession{ID: "session", UserID: "disabled", TokenHash: auth.HashToken("secret"), ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	w := get("session.secret")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"user":null`) {
		t.Fatalf("disabled-user contract changed: %d %s", w.Code, w.Body.String())
	}
	// Close the actual database: reads now fail, not a fake store response.
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	w = get("session.secret")
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), `"user":null`) {
		t.Fatalf("outage presented as anonymous success: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "database is closed") {
		t.Fatal("database internals leaked to browser")
	}
}
