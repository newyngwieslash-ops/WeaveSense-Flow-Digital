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
		default:
			reply(c, http.StatusBadGateway, nil, err.Error())
		}
	}
	g := api.Group("/intelligence/mcp/connections")
	g.GET("", func(c *gin.Context) {
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
