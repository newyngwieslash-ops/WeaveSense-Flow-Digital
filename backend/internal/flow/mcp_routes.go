package flow

import (
	"errors"
	"net/http"

	"yingce/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// RegisterMCPRoutes mounts /api/intelligence/mcp/*. Workspace admins create and
// revoke connections; only platform operators (User.Role == admin) approve the
// tool allow-list. Responses never include credential values.
func RegisterMCPRoutes(api gin.IRouter, db *gorm.DB, gw *MCPGateway, user UserFunc) {
	scoped := func(c *gin.Context) (Scope, *model.User, bool) {
		u, err := user(c)
		if err != nil || u == nil {
			reply(c, http.StatusUnauthorized, nil, "unauthorized")
			return Scope{}, nil, false
		}
		s, err := ResolveScope(db, u.ID, c.GetHeader("X-Workspace-Id"))
		if err != nil {
			reply(c, http.StatusNotFound, nil, "workspace not found")
			return Scope{}, nil, false
		}
		return s, u, true
	}
	fail := func(c *gin.Context, err error) {
		switch {
		case errors.Is(err, ErrMCPNotFound):
			reply(c, http.StatusNotFound, nil, "connection not found")
		case errors.Is(err, ErrMCPInvalid):
			reply(c, http.StatusBadRequest, nil, err.Error())
		case errors.Is(err, ErrSecretNotFound):
			reply(c, http.StatusNotFound, nil, "secret not found")
		case errors.Is(err, ErrSecretInUse):
			reply(c, http.StatusConflict, nil, err.Error())
		case errors.Is(err, ErrSecretKEK):
			reply(c, http.StatusServiceUnavailable, nil, err.Error())
		default:
			reply(c, http.StatusBadGateway, nil, err.Error())
		}
	}
	// PRD §11 path is /api/mcp/connections; /api/intelligence/mcp/connections stays as the
	// original path (same handlers, same auth).
	for _, base := range []string{"/intelligence/mcp", "/mcp"} {
		registerMCPConnectionRoutes(api.Group(base+"/connections"), db, gw, scoped, fail)
		registerMCPSecretRoutes(api.Group(base+"/secrets"), db, gw, scoped, fail)
	}
}

type mcpScopedFn func(c *gin.Context) (Scope, *model.User, bool)

func registerMCPConnectionRoutes(g *gin.RouterGroup, db *gorm.DB, gw *MCPGateway, scoped mcpScopedFn, fail func(*gin.Context, error)) {
	g.GET("", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		gw := gw.withDB(db)
		_ = gw
		s, _, ok := scoped(c)
		if !ok {
			return
		}
		rows, err := gw.List(s)
		if err != nil {
			fail(c, err)
			return
		}
		reply(c, http.StatusOK, rows, "ok")
	})
	g.POST("", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		gw := gw.withDB(db)
		_ = gw
		s, u, ok := scoped(c)
		if !ok {
			return
		}
		if s.Role != "admin" {
			reply(c, http.StatusForbidden, nil, "workspace admin required")
			return
		}
		var in MCPConnectionInput
		if c.ShouldBindJSON(&in) != nil {
			reply(c, http.StatusBadRequest, nil, "bad request")
			return
		}
		row, err := gw.Create(s, u.ID, in)
		if err != nil {
			fail(c, err)
			return
		}
		reply(c, http.StatusCreated, gw.View(row), "ok")
	})
	g.GET("/:id/tools", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		gw := gw.withDB(db)
		_ = gw
		s, u, ok := scoped(c)
		if !ok {
			return
		}
		if s.Role != "admin" && u.Role != model.UserRoleAdmin {
			reply(c, http.StatusForbidden, nil, "forbidden")
			return
		}
		tools, err := gw.Discover(c.Request.Context(), s, c.Param("id"))
		if err != nil {
			fail(c, err)
			return
		}
		reply(c, http.StatusOK, tools, "ok")
	})
	g.POST("/:id/approve", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		gw := gw.withDB(db)
		_ = gw
		s, u, ok := scoped(c)
		if !ok {
			return
		}
		if u.Role != model.UserRoleAdmin {
			reply(c, http.StatusForbidden, nil, "platform operator approval required")
			return
		}
		var in struct {
			AllowedTools []string `json:"allowedTools"`
		}
		if c.ShouldBindJSON(&in) != nil {
			reply(c, http.StatusBadRequest, nil, "bad request")
			return
		}
		v, err := gw.Approve(c.Request.Context(), s, c.Param("id"), u.ID, in.AllowedTools)
		if err != nil {
			fail(c, err)
			return
		}
		reply(c, http.StatusOK, v, "ok")
	})
	g.POST("/:id/revoke", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		gw := gw.withDB(db)
		_ = gw
		s, u, ok := scoped(c)
		if !ok {
			return
		}
		if s.Role != "admin" && u.Role != model.UserRoleAdmin {
			reply(c, http.StatusForbidden, nil, "forbidden")
			return
		}
		if err := gw.Revoke(s, c.Param("id"), "revoked by "+u.ID); err != nil {
			fail(c, err)
			return
		}
		reply(c, http.StatusOK, gin.H{"revoked": true}, "ok")
	})
}

// MCPSecretView is the only shape a secret is ever returned in: metadata plus the
// reference to put into a connection's credentialRef. No value, no ciphertext.
type MCPSecretView struct {
	model.FlowMCPSecret
	Ref string `json:"ref"`
}

func secretView(r model.FlowMCPSecret) MCPSecretView {
	return MCPSecretView{FlowMCPSecret: r, Ref: secretRefPrefix + r.ID}
}

// registerMCPSecretRoutes: tenant-saved MCP credentials (workspace admins only).
func registerMCPSecretRoutes(g *gin.RouterGroup, db *gorm.DB, gw *MCPGateway, scoped mcpScopedFn, fail func(*gin.Context, error)) {
	admin := func(c *gin.Context) (Scope, *model.User, MCPSecrets, bool) {
		s, u, ok := scoped(c)
		if !ok {
			return s, u, MCPSecrets{}, false
		}
		if s.Role != "admin" {
			reply(c, http.StatusForbidden, nil, "workspace admin required")
			return s, u, MCPSecrets{}, false
		}
		return s, u, gw.withDB(reqDB(c, db)).secrets(), true
	}
	g.GET("", func(c *gin.Context) {
		s, _, sec, ok := admin(c)
		if !ok {
			return
		}
		rows, err := sec.List(s)
		if err != nil {
			fail(c, err)
			return
		}
		out := make([]MCPSecretView, 0, len(rows))
		for _, r := range rows {
			out = append(out, secretView(r))
		}
		reply(c, http.StatusOK, out, "ok")
	})
	g.POST("", func(c *gin.Context) {
		s, u, sec, ok := admin(c)
		if !ok {
			return
		}
		var in MCPSecretInput
		if c.ShouldBindJSON(&in) != nil {
			reply(c, http.StatusBadRequest, nil, "bad request")
			return
		}
		row, err := sec.Create(s, u.ID, in)
		if err != nil {
			fail(c, err)
			return
		}
		reply(c, http.StatusCreated, secretView(row), "ok")
	})
	g.PUT("/:id", func(c *gin.Context) {
		s, _, sec, ok := admin(c)
		if !ok {
			return
		}
		var in MCPSecretInput
		if c.ShouldBindJSON(&in) != nil {
			reply(c, http.StatusBadRequest, nil, "bad request")
			return
		}
		row, err := sec.Rotate(s, c.Param("id"), in)
		if err != nil {
			fail(c, err)
			return
		}
		reply(c, http.StatusOK, secretView(row), "ok")
	})
	g.DELETE("/:id", func(c *gin.Context) {
		s, _, sec, ok := admin(c)
		if !ok {
			return
		}
		if err := sec.Delete(s, c.Param("id")); err != nil {
			fail(c, err)
			return
		}
		reply(c, http.StatusOK, gin.H{"deleted": true}, "ok")
	})
}
