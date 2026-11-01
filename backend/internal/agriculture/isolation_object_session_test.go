package agriculture

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"yingce/backend/internal/auth"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
)

func assertObjectDenial(t *testing.T, method string, status int, headers http.Header, body []byte) {
	t.Helper()
	for _, key := range []string{"Location", "Content-Disposition", "Content-Range", "ETag", "Set-Cookie"} {
		if headers.Get(key) != "" {
			t.Fatal("denied object response leaked redirect, content metadata or cookie")
		}
	}
	if !strings.Contains(headers.Get("Cache-Control"), "no-store") || headers.Get("Vary") != "Cookie" {
		t.Fatal("denied object response cacheable")
	}
	if method == "HEAD" {
		if len(body) != 0 {
			t.Fatal("denied HEAD response contains bytes")
		}
		return
	}
	want, ok := map[int][2]string{400: {"invalid_request", "请求参数无效"}, 401: {"unauthenticated", "请先登录"}, 403: {"forbidden", "当前主体无此权限"}, 404: {"resource_unavailable", "资源不可用"}, 416: {"range_unavailable", "请求的字节范围不可用"}}[status]
	if !ok {
		t.Fatal("unexpected object denial status")
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || len(envelope) != 4 || string(envelope["data"]) != "null" {
		t.Fatal("object error envelope leaked extra fields or bytes")
	}
	var code int
	var reason, message string
	if json.Unmarshal(envelope["code"], &code) != nil || json.Unmarshal(envelope["reason"], &reason) != nil || json.Unmarshal(envelope["msg"], &message) != nil || code != status || reason != want[0] || message != want[1] {
		t.Fatal("object error envelope differs from fixed safe response")
	}
}

func objectPostgresSecondWorkspace(t *testing.T, migration *gorm.DB, org OrganizationResult, owner string) string {
	t.Helper()
	id := flow.NewID("ws_")
	err := flow.TenantTx(migration, org.Tenant.ID, func(tx *gorm.DB) error {
		if err := tx.Create(&model.Workspace{ID: id, TenantID: org.Tenant.ID, Product: Product, Name: "Private unjoined object workspace", Slug: id}).Error; err != nil {
			return err
		}
		return tx.Create(&model.WorkspaceMembership{ID: flow.NewID("wm_"), WorkspaceID: id, UserID: owner, Role: "owner"}).Error
	})
	if err != nil {
		t.Fatal("object second-workspace fixture failed")
	}
	return id
}

// Real persisted auth-domain session over platform PostgreSQL. Full platform
// runtime/Redis startup is independently required by the route matrix suite.
// Fixture writes use the migration role; runtime never performs schema DDL.
func objectPostgresSessionServer(t *testing.T, s Service, migration *gorm.DB, actor string) (*httptest.Server, *http.Cookie) {
	t.Helper()
	password := auth.RandomToken()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal("object session password setup failed")
	}
	if err = migration.Model(&model.User{}).Where("id=?", actor).Update("password_hash", hash).Error; err != nil {
		t.Fatal("object session credential fixture failed")
	}
	authentication := auth.New(repository.New(s.DB), nil, nil)
	login, err := authentication.Login(auth.LoginRequest{Username: actor, Password: password})
	if err != nil {
		t.Fatal("object PostgreSQL real auth login failed")
	}
	t.Cleanup(func() {
		if err := migration.Where("user_id=?", actor).Delete(&model.AuthSession{}).Error; err != nil {
			t.Error("object auth session cleanup failed")
		}
	})
	router := gin.New()
	API{Service: s, User: func(c *gin.Context) (*model.User, error) {
		cookie, _ := c.Cookie(auth.SessionCookieName)
		return authentication.CurrentUser(cookie)
	}}.Register(router.Group("/api"))
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server, &http.Cookie{Name: auth.SessionCookieName, Value: login.Session, Path: "/", HttpOnly: true}
}
