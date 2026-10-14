package flow

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// RegisterNotifyRoutes: §5.2 notification targets/deliveries and §12 usage summary.
func RegisterNotifyRoutes(api gin.IRouter, db *gorm.DB, user UserFunc) {
	scoped := func(c *gin.Context) (Scope, string, bool) {
		u, err := user(c)
		if err != nil || u == nil {
			reply(c, http.StatusUnauthorized, nil, "unauthorized")
			return Scope{}, "", false
		}
		s, err := ResolveScope(reqDB(c, db), u.ID, c.GetHeader("X-Workspace-Id"))
		if err != nil {
			reply(c, http.StatusNotFound, nil, "workspace not found")
			return Scope{}, "", false
		}
		return s, u.ID, true
	}
	canManage := func(c *gin.Context, s Scope) bool {
		if s.Role != "admin" && s.Role != "editor" {
			reply(c, http.StatusForbidden, nil, "forbidden")
			return false
		}
		return true
	}
	g := api.Group("/intelligence")
	g.GET("/notification-targets", func(c *gin.Context) {
		s, _, ok := scoped(c)
		if !ok {
			return
		}
		out, err := ListNotificationTargets(reqDB(c, db), s)
		if err != nil {
			reply(c, http.StatusInternalServerError, nil, "list error")
			return
		}
		reply(c, http.StatusOK, out, "ok")
	})
	g.POST("/notification-targets", func(c *gin.Context) {
		s, uid, ok := scoped(c)
		if !ok || !canManage(c, s) {
			return
		}
		var in TargetInput
		if c.ShouldBindJSON(&in) != nil {
			reply(c, http.StatusBadRequest, nil, "bad request")
			return
		}
		out, err := CreateNotificationTarget(reqDB(c, db), s, uid, in, time.Now().UTC())
		if errors.Is(err, ErrNotifyTarget) {
			reply(c, http.StatusBadRequest, nil, err.Error())
			return
		}
		if err != nil {
			reply(c, http.StatusInternalServerError, nil, "create error")
			return
		}
		reply(c, http.StatusCreated, out, "ok")
	})
	g.POST("/notification-targets/:id/revoke", func(c *gin.Context) {
		s, _, ok := scoped(c)
		if !ok || !canManage(c, s) {
			return
		}
		if err := RevokeNotificationTarget(reqDB(c, db), s, c.Param("id"), time.Now().UTC()); err != nil {
			if errors.Is(err, ErrNotFound) {
				reply(c, http.StatusNotFound, nil, "not found")
				return
			}
			reply(c, http.StatusInternalServerError, nil, "revoke error")
			return
		}
		reply(c, http.StatusOK, gin.H{"id": c.Param("id"), "status": "revoked"}, "ok")
	})
	g.GET("/notification-deliveries", func(c *gin.Context) {
		s, _, ok := scoped(c)
		if !ok {
			return
		}
		out, err := ListNotificationDeliveries(reqDB(c, db), s, c.Query("status"))
		if err != nil {
			reply(c, http.StatusInternalServerError, nil, "list error")
			return
		}
		reply(c, http.StatusOK, out, "ok")
	})
	g.POST("/notification-deliveries/:id/resolve", func(c *gin.Context) {
		s, _, ok := scoped(c)
		if !ok || !canManage(c, s) {
			return
		}
		var in struct {
			Delivered *bool `json:"delivered"`
		}
		if c.ShouldBindJSON(&in) != nil || in.Delivered == nil {
			reply(c, http.StatusBadRequest, nil, "delivered (bool) required")
			return
		}
		out, err := ResolveDelivery(reqDB(c, db), s, c.Param("id"), *in.Delivered, time.Now().UTC())
		switch {
		case errors.Is(err, ErrNotFound):
			reply(c, http.StatusNotFound, nil, "not found")
		case errors.Is(err, ErrBudgetState):
			reply(c, http.StatusConflict, nil, "delivery is not awaiting reconciliation")
		case err != nil:
			reply(c, http.StatusInternalServerError, nil, "resolve error")
		default:
			reply(c, http.StatusOK, out, "ok")
		}
	})
	g.GET("/usage", func(c *gin.Context) {
		s, _, ok := scoped(c)
		if !ok {
			return
		}
		now := time.Now().UTC()
		from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		out, err := SummarizeUsage(reqDB(c, db), s.TenantID, from)
		if err != nil {
			reply(c, http.StatusInternalServerError, nil, "usage error")
			return
		}
		reply(c, http.StatusOK, gin.H{"from": from, "priceVersion": CurrentPriceBook().Version, "types": UsageTypes, "usage": out}, "ok")
	})
}
