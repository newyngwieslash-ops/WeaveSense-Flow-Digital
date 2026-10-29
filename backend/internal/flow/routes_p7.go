package flow

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// RegisterP7Routes mounts member management and operator resolution of
// unknown external outcomes (MCP calls, model reservations).
func RegisterP7Routes(api gin.IRouter, db *gorm.DB, user UserFunc, mcp *MCPGateway) {
	scoped := func(c *gin.Context) (Scope, bool) {
		u, err := user(c)
		if err != nil || u == nil {
			reply(c, http.StatusUnauthorized, nil, "unauthorized")
			return Scope{}, false
		}
		s, err := ResolveScope(reqDB(c, db), u.ID, c.GetHeader("X-Workspace-Id"))
		if err != nil {
			if errors.Is(err, ErrForbidden) {
				reply(c, http.StatusNotFound, nil, "workspace not found")
			} else {
				reply(c, http.StatusInternalServerError, nil, "scope resolution failed")
			}
			return Scope{}, false
		}
		return s, true
	}
	fail := func(c *gin.Context, err error) {
		switch {
		case errors.Is(err, ErrForbidden):
			reply(c, http.StatusForbidden, nil, "forbidden")
		case errors.Is(err, ErrNotFound), errors.Is(err, ErrBudgetNotFound):
			reply(c, http.StatusNotFound, nil, "not found")
		case errors.Is(err, ErrInvalid):
			reply(c, http.StatusBadRequest, nil, "invalid request")
		case errors.Is(err, ErrBudgetState):
			reply(c, http.StatusConflict, nil, "not in a resolvable state")
		default:
			reply(c, http.StatusInternalServerError, nil, "error")
		}
	}
	g := api.Group("/intelligence")
	g.GET("/members", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		mcp := mcp.withDB(db)
		_ = mcp
		s, ok := scoped(c)
		if !ok {
			return
		}
		out, err := ListMembers(db, s)
		if err != nil {
			fail(c, err)
			return
		}
		reply(c, http.StatusOK, gin.H{"members": out, "canManage": s.Role == "admin"}, "ok")
	})
	g.PATCH("/members/:userId", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		mcp := mcp.withDB(db)
		_ = mcp
		s, ok := scoped(c)
		if !ok {
			return
		}
		var body struct {
			Role string `json:"role"`
		}
		_ = c.ShouldBindJSON(&body)
		if err := SetMemberRole(db, s, c.Param("userId"), body.Role); err != nil {
			fail(c, err)
			return
		}
		reply(c, http.StatusOK, gin.H{"ok": true}, "ok")
	})
	g.GET("/mcp/calls", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		mcp := mcp.withDB(db)
		_ = mcp
		s, ok := scoped(c)
		if !ok {
			return
		}
		if s.Role != "admin" {
			fail(c, ErrForbidden)
			return
		}
		out, err := ListMCPCalls(db, s, c.Query("status"))
		if err != nil {
			fail(c, err)
			return
		}
		reply(c, http.StatusOK, out, "ok")
	})
	g.POST("/mcp/calls/:id/resolve", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		mcp := mcp.withDB(db)
		_ = mcp
		s, ok := scoped(c)
		if !ok {
			return
		}
		var body struct {
			Outcome string `json:"outcome"`
			Output  string `json:"output"`
		}
		_ = c.ShouldBindJSON(&body)
		row, err := mcp.ResolveMCPCall(s, c.Param("id"), body.Outcome, body.Output)
		if err != nil {
			fail(c, err)
			return
		}
		reply(c, http.StatusOK, row, "ok")
	})
	g.POST("/billing/reservations/:id/resolve", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		mcp := mcp.withDB(db)
		_ = mcp
		s, ok := scoped(c)
		if !ok {
			return
		}
		var body struct {
			Outcome   string `json:"outcome"`
			CostMicro int64  `json:"costMicro"`
		}
		_ = c.ShouldBindJSON(&body)
		r, err := ResolveReservation(db, s, c.Param("id"), body.Outcome, body.CostMicro)
		if err != nil {
			fail(c, err)
			return
		}
		reply(c, http.StatusOK, r, "ok")
	})
}
