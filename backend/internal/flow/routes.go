package flow

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"yingce/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// UserFunc resolves the authenticated user from the request.
type UserFunc func(*gin.Context) (*model.User, error)

func reply(c *gin.Context, status int, data any, msg string) {
	code := 0
	if status >= 400 {
		code = status
	}
	c.JSON(status, gin.H{"code": code, "data": data, "msg": msg})
}

// RegisterRoutes mounts /api/intelligence/* and the internal event sink.
func RegisterRoutes(api gin.IRouter, db *gorm.DB, cfg Config, user UserFunc) {
	client := EngineClient{Cfg: cfg}
	scoped := func(c *gin.Context) (Scope, *model.User, bool) {
		u, err := user(c)
		if err != nil || u == nil {
			reply(c, http.StatusUnauthorized, nil, "unauthorized")
			return Scope{}, nil, false
		}
		s, err := ResolveScope(db, u.ID, c.GetHeader("X-Workspace-Id"))
		if err != nil {
			if errors.Is(err, ErrForbidden) {
				reply(c, http.StatusNotFound, nil, "workspace not found")
			} else {
				reply(c, http.StatusInternalServerError, nil, "scope error")
			}
			return Scope{}, nil, false
		}
		return s, u, true
	}
	g := api.Group("/intelligence")
	g.GET("/workspaces", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		u, err := user(c)
		if err != nil || u == nil {
			reply(c, http.StatusUnauthorized, nil, "unauthorized")
			return
		}
		ws, err := ListWorkspaces(db, u.ID)
		if err != nil {
			reply(c, http.StatusInternalServerError, nil, "list error")
			return
		}
		reply(c, http.StatusOK, ws, "ok")
	})
	proxy := func(method, enginePath string) gin.HandlerFunc {
		return func(c *gin.Context) {
			db := reqDB(c, db)
			_ = db
			if !cfg.Enabled() {
				reply(c, http.StatusServiceUnavailable, nil, "loom engine not configured")
				return
			}
			s, u, ok := scoped(c)
			if !ok {
				return
			}
			path := enginePath
			for _, p := range c.Params {
				path = strings.ReplaceAll(path, ":"+p.Key, p.Value)
			}
			if q := c.Request.URL.RawQuery; q != "" {
				path += "?" + q
			}
			body, _ := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
			status, out, err := client.Do(c.Request.Context(), s, Actor{Type: "user", ID: u.ID}, method, path, c.GetHeader("X-Idempotency-Key"), body)
			if err != nil {
				reply(c, http.StatusBadGateway, nil, "loom engine unavailable")
				return
			}
			c.Data(status, "application/json", out)
		}
	}
	g.GET("/sources", proxy(http.MethodGet, "/v1/sources"))
	g.POST("/sources", proxy(http.MethodPost, "/v1/sources"))
	g.GET("/schedules", proxy(http.MethodGet, "/v1/schedules"))
	g.POST("/schedules", proxy(http.MethodPost, "/v1/schedules"))
	g.POST("/collection-runs", proxy(http.MethodPost, "/v1/collection-runs"))
	g.GET("/documents", proxy(http.MethodGet, "/v1/documents"))
	g.GET("/operations/:id", proxy(http.MethodGet, "/v1/operations/:id"))
	g.GET("/monitors", proxy(http.MethodGet, "/v1/monitors"))
	g.POST("/sources/:id/revoke", proxy(http.MethodPost, "/v1/sources/:id/revoke"))
	g.GET("/connector-samples", proxy(http.MethodGet, "/v1/connector-samples"))
	g.GET("/reports", proxy(http.MethodGet, "/v1/reports"))
	g.GET("/reports/:id", proxy(http.MethodGet, "/v1/reports/:id"))
	g.GET("/brief-schedules", proxy(http.MethodGet, "/v1/brief-schedules"))
	g.GET("/skills", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		s, _, ok := scoped(c)
		if !ok {
			return
		}
		ps, err := SkillPacks()
		installed, err2 := ListInstalledSkills(db, s)
		if err != nil || err2 != nil {
			reply(c, http.StatusInternalServerError, nil, "skills error")
			return
		}
		reply(c, http.StatusOK, gin.H{"packs": ps, "installed": installed}, "ok")
	})
	g.POST("/skills/install", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		s, u, ok := scoped(c)
		if !ok {
			return
		}
		if !hasScope(s.Role, "intel.reports.write") {
			reply(c, http.StatusForbidden, nil, "forbidden")
			return
		}
		var in struct {
			SkillID string `json:"skillId"`
			Version string `json:"version"`
			PackID  string `json:"packId"`
		}
		if c.ShouldBindJSON(&in) != nil {
			reply(c, http.StatusBadRequest, nil, "bad request")
			return
		}
		var out []model.FlowSkillInstall
		ps, _ := SkillPacks()
		for _, p := range ps {
			for _, sk := range p.Skills {
				if (in.PackID != "" && p.ID == in.PackID) || (in.PackID == "" && sk.ID == in.SkillID) {
					row, err := InstallSkill(db, s, u.ID, sk.ID, map[bool]string{true: in.Version, false: ""}[in.PackID == ""])
					if err != nil {
						reply(c, http.StatusNotFound, nil, err.Error())
						return
					}
					out = append(out, row)
				}
			}
		}
		if len(out) == 0 {
			reply(c, http.StatusNotFound, nil, "skill not found")
			return
		}
		reply(c, http.StatusOK, out, "ok")
	})
	g.GET("/notifications", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		s, u, ok := scoped(c)
		if !ok {
			return
		}
		var rows []NotificationView
		err := TenantTx(db, s.TenantID, func(tx *gorm.DB) (e error) { rows, e = ListNotificationsForUser(tx, s, u.ID); return })
		if err != nil {
			reply(c, http.StatusInternalServerError, nil, "list error")
			return
		}
		reply(c, http.StatusOK, rows, "ok")
	})
	RegisterAgentRoutes(api, db, client, user)

	// Engine → platform outbox delivery. Authenticated with a context token
	// whose audience is the platform issuer; tenant must match the event.
	api.POST("/internal/flow/events", func(c *gin.Context) {
		db := reqDB(c, db)
		_ = db
		tok := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		ctx, err := Verify(cfg.SigningKey, tok, cfg.Issuer, time.Now())
		if err != nil || ctx.Actor.Type != "service_principal" {
			reply(c, http.StatusUnauthorized, nil, "invalid context")
			return
		}
		var ev Event
		if err := json.NewDecoder(io.LimitReader(c.Request.Body, 1<<20)).Decode(&ev); err != nil {
			reply(c, http.StatusBadRequest, nil, "bad event")
			return
		}
		if ev.TenantID != ctx.TenantID {
			reply(c, http.StatusForbidden, nil, "tenant mismatch")
			return
		}
		applied, err := ConsumeEvent(db, "platform-projection", ev)
		if err != nil {
			reply(c, http.StatusInternalServerError, nil, "consume failed")
			return
		}
		reply(c, http.StatusOK, gin.H{"applied": applied}, "ok")
	})
}
