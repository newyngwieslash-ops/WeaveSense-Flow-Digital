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
	proxyWith := func(method, enginePath string, mutate func(body []byte, u *model.User, idem string) ([]byte, error)) gin.HandlerFunc {
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
				if strings.Contains(path, "?") {
					path += "&" + q
				} else {
					path += "?" + q
				}
			}
			body, _ := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
			if mutate != nil {
				var err error
				if body, err = mutate(body, u, c.GetHeader("X-Idempotency-Key")); err != nil {
					reply(c, http.StatusBadRequest, nil, err.Error())
					return
				}
			}
			status, out, err := client.Do(c.Request.Context(), s, Actor{Type: "user", ID: u.ID}, method, path, c.GetHeader("X-Idempotency-Key"), body)
			if err != nil {
				reply(c, http.StatusBadGateway, nil, "loom engine unavailable")
				return
			}
			c.Data(status, "application/json", out)
		}
	}
	proxy := func(method, enginePath string) gin.HandlerFunc { return proxyWith(method, enginePath, nil) }
	proxyCommand := func(command, method, enginePath string) gin.HandlerFunc {
		return func(c *gin.Context) {
			db := reqDB(c, db)
			if !cfg.Enabled() {
				reply(c, http.StatusServiceUnavailable, nil, "loom engine not configured")
				return
			}
			s, u, ok := scoped(c)
			if !ok {
				return
			}
			body, _ := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
			dispatcher := CommandDispatcher{DB: db, Engine: client}
			row, replayed, err := dispatcher.Enqueue(c.Request.Context(), CommandInput{
				Scope:          s,
				Actor:          Actor{Type: "user", ID: u.ID},
				Command:        command,
				Method:         method,
				Path:           enginePath,
				IdempotencyKey: c.GetHeader("X-Idempotency-Key"),
				Payload:        body,
			})
			if errors.Is(err, ErrCommandConflict) {
				reply(c, http.StatusConflict, nil, err.Error())
				return
			}
			if err != nil {
				reply(c, http.StatusInternalServerError, nil, "command outbox error")
				return
			}
			if strings.EqualFold(c.GetHeader("X-Flow-Outbox-Mode"), "async") {
				reply(c, http.StatusAccepted, gin.H{"operation_id": row.OperationID, "outbox_id": row.ID, "status": row.Status, "accepted": true, "replayed": replayed}, "accepted")
				return
			}
			if replayed && (row.Status == "dispatched" || row.Status == "failed") && row.ResponseStatus > 0 {
				c.Data(row.ResponseStatus, "application/json", []byte(row.ResponseBody))
				return
			}
			dispatched, derr := dispatcher.DispatchOne(c.Request.Context(), row.ID)
			if derr != nil && dispatched.ResponseStatus == 0 {
				reply(c, http.StatusBadGateway, gin.H{"operation_id": dispatched.OperationID, "outbox_id": dispatched.ID, "status": dispatched.Status}, "loom engine unavailable")
				return
			}
			c.Data(dispatched.ResponseStatus, "application/json", []byte(dispatched.ResponseBody))
		}
	}
	g.GET("/sources", proxy(http.MethodGet, "/v1/sources"))
	g.POST("/sources", proxy(http.MethodPost, "/v1/sources"))
	g.GET("/schedules", proxy(http.MethodGet, "/v1/schedules"))
	g.POST("/schedules", proxy(http.MethodPost, "/v1/schedules"))
	g.POST("/collection-runs", proxyCommand("collection.request", http.MethodPost, "/v1/collection-runs"))
	g.GET("/collection-runs", proxy(http.MethodGet, "/v1/collection-runs"))
	g.GET("/documents", proxy(http.MethodGet, "/v1/documents"))
	g.GET("/operations/:id", proxy(http.MethodGet, "/v1/operations/:id"))
	g.GET("/monitors", proxy(http.MethodGet, "/v1/monitors"))
	g.POST("/sources/:id/revoke", proxy(http.MethodPost, "/v1/sources/:id/revoke"))
	g.GET("/connector-samples", proxy(http.MethodGet, "/v1/connector-samples"))
	g.POST("/documents/extract", proxy(http.MethodPost, "/v1/documents/extract"))
	g.POST("/documents/transcript", proxy(http.MethodPost, "/v1/documents/transcript"))
	g.GET("/reports", proxy(http.MethodGet, "/v1/reports"))
	g.GET("/reports/:id", proxy(http.MethodGet, "/v1/reports/:id"))
	g.GET("/brief-schedules", proxy(http.MethodGet, "/v1/brief-schedules"))
	registerContractRoutes(g, proxy, proxyWith, proxyCommand)
	registerGaps9Routes(g, db, client, cfg, scoped)
	registerCanvasHandoffRoutes(g, db, scoped)
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
