package agriculture

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"yingce/backend/internal/database"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/kernel"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Enablement is explicit: the default film/math profile remains unchanged.
// PostgreSQL must have the narrowly owned admission function provisioned.
func RegisterFromEnv(root, api gin.IRouter, db *gorm.DB, user flow.UserFunc) error {
	enabled, err := strconv.ParseBool(envDefault("FLOW_AGRICULTURE_ENABLED", "false"))
	if err != nil {
		return errors.New("invalid agriculture enablement configuration")
	}
	if !enabled {
		return nil
	}
	if db.Dialector.Name() == "postgres" {
		if err := database.RequireAgricultureAdmission(db); err != nil {
			return err
		}
	} else {
		dev, e := strconv.ParseBool(envDefault("FLOW_AGRICULTURE_ALLOW_SQLITE_DEV", "false"))
		if e != nil || !dev {
			return errors.New("agriculture requires PostgreSQL or explicit isolated SQLite development mode")
		}
	}
	service := Service{DB: db}
	if path := os.Getenv("FLOW_AGRICULTURE_SERVICE_IDENTITY_CONFIG"); path != "" {
		client, gisKeys, err := loadServiceIdentity(path)
		if err != nil {
			return err
		}
		service.Ownership = client
		service.RegisterInternalAuthority(root, gisKeys)
	}
	API{Service: service, User: user}.Register(api)
	return nil
}
func envDefault(name, value string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return value
}

type API struct {
	Service Service
	User    flow.UserFunc
}

var ErrUnauthenticated = errors.New("agriculture: unauthenticated")

func respond(c *gin.Context, data any, err error) {
	status, reason, msg := http.StatusOK, "", "ok"
	if err != nil {
		data = nil
		status, reason, msg = http.StatusInternalServerError, "agriculture_unavailable", "农业服务暂不可用"
		switch {
		case errors.Is(err, ErrPreconditionRequired):
			status, reason, msg = 428, "precondition_required", "必须提供预期授权版本"
		case errors.Is(err, ErrUnauthenticated):
			status, reason, msg = 401, "unauthenticated", "请先登录"
		case errors.Is(err, ErrDependencyUnavailable):
			status, reason, msg = 503, "dependency_unavailable", "资源归属验证暂不可用"
		case errors.Is(err, ErrInvalid):
			status, reason, msg = 400, "invalid_request", "请求参数无效"
		case errors.Is(err, ErrInvitation):
			status, reason, msg = 400, "invalid_invitation", "邀请不可用"
		case errors.Is(err, ErrForbidden):
			status, reason, msg = 403, "forbidden", "当前主体无此权限"
		case errors.Is(err, ErrNotFound):
			status, reason, msg = 404, "resource_unavailable", "资源不可用"
		case errors.Is(err, ErrConflict):
			status, reason, msg = 409, "state_conflict", "对象状态冲突，请重新读取"
		}
	}
	code := 0
	if status >= 400 {
		code = status
	}
	c.JSON(status, gin.H{"code": code, "data": data, "msg": msg, "reason": reason})
}

func decode(c *gin.Context, target any) bool {
	return decodeWithResponder(c, target, respond)
}

func decodeWithResponder(c *gin.Context, target any, response func(*gin.Context, any, error)) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		response(c, nil, ErrInvalid)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		response(c, nil, ErrInvalid)
		return false
	}
	return true
}

// Binding writes use an explicit body revision, not an HTTP representation
// ETag. Reject If-Match rather than silently ignoring a conflicting condition.
func bindingBodyRevisionOnly(c *gin.Context, response func(*gin.Context, any, error)) bool {
	if len(c.Request.Header.Values("If-Match")) > 0 {
		response(c, nil, ErrInvalid)
		return false
	}
	return true
}

func (a API) sessionMiddleware(response func(*gin.Context, any, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("Vary", "Cookie")
		actor, err := a.User(c)
		if err != nil || actor == nil {
			var appErr *kernel.AppError
			if err != nil && (!errors.As(err, &appErr) || (appErr.Status != 401 && appErr.Status != 403)) {
				response(c, nil, err)
			} else {
				response(c, nil, ErrUnauthenticated)
			}
			c.Abort()
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			origin := c.GetHeader("Origin")
			if origin != "" {
				parsed, e := url.Parse(origin)
				if e != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host != c.Request.Host || parsed.User != nil || parsed.Path != "" {
					response(c, nil, ErrForbidden)
					c.Abort()
					return
				}
			}
			if strings.EqualFold(c.GetHeader("Sec-Fetch-Site"), "cross-site") {
				response(c, nil, ErrForbidden)
				c.Abort()
				return
			}
		}
		c.Set("agriculture.actor", actor.ID)
		c.Next()
	}
}

func (a API) Register(api gin.IRouter) {
	a.registerAuthorizationV1(api)
	g := api.Group("/agriculture")
	g.Use(a.sessionMiddleware(respond))
	actor := func(c *gin.Context) string { return c.GetString("agriculture.actor") }
	g.GET("/workspaces", func(c *gin.Context) {
		out, err := a.Service.ListWorkspaces(c.Request.Context(), actor(c))
		respond(c, out, err)
	})
	g.POST("/workspaces/:workspaceId/authorize", func(c *gin.Context) {
		var body struct {
			Action    string        `json:"action"`
			Resources []ResourceRef `json:"resources"`
		}
		if !decode(c, &body) {
			return
		}
		out, err := a.Service.Authorize(c.Request.Context(), actor(c), c.Param("workspaceId"), body.Action, body.Resources)
		respond(c, out, err)
	})
	g.GET("/workspaces/:workspaceId/resource-bindings", func(c *gin.Context) {
		out, err := a.Service.ListBindings(c.Request.Context(), actor(c), c.Param("workspaceId"))
		respond(c, out, err)
	})
	g.POST("/workspaces/:workspaceId/resource-bindings", func(c *gin.Context) {
		if !bindingBodyRevisionOnly(c, respond) {
			return
		}
		var body struct {
			SubjectID        string      `json:"subjectId"`
			Resource         ResourceRef `json:"resource"`
			Capabilities     []string    `json:"capabilities"`
			ExpiresAt        *time.Time  `json:"expiresAt"`
			ExpectedRevision *int64      `json:"expectedRevision"`
		}
		if !decode(c, &body) {
			return
		}
		out, err := a.Service.GrantResourceBinding(c.Request.Context(), actor(c), c.Param("workspaceId"), body.SubjectID, body.Resource, body.Capabilities, body.ExpiresAt, body.ExpectedRevision)
		respond(c, out, err)
	})
	g.POST("/workspaces/:workspaceId/resource-bindings/:bindingId/revoke", func(c *gin.Context) {
		if !bindingBodyRevisionOnly(c, respond) {
			return
		}
		var body struct {
			ExpectedRevision *int64 `json:"expectedRevision"`
		}
		if !decode(c, &body) {
			return
		}
		err := a.Service.RevokeBinding(c.Request.Context(), actor(c), c.Param("workspaceId"), c.Param("bindingId"), body.ExpectedRevision)
		respond(c, gin.H{"ok": true}, err)
	})
	g.POST("/organizations", func(c *gin.Context) {
		var body struct {
			Name string `json:"name"`
		}
		if !decode(c, &body) {
			return
		}
		out, err := a.Service.CreateOrganization(c.Request.Context(), actor(c), body.Name, c.GetHeader("X-Idempotency-Key"))
		respond(c, out, err)
	})
	g.POST("/invites/accept", func(c *gin.Context) {
		var body struct {
			Token string `json:"token"`
		}
		if !decode(c, &body) {
			return
		}
		out, err := a.Service.AcceptInvite(c.Request.Context(), actor(c), body.Token)
		respond(c, out, err)
	})
	g.GET("/workspaces/:workspaceId/members", func(c *gin.Context) {
		out, err := a.Service.Members(c.Request.Context(), actor(c), c.Param("workspaceId"))
		respond(c, out, err)
	})
	g.POST("/workspaces/:workspaceId/invites", func(c *gin.Context) {
		var body struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		}
		if !decode(c, &body) {
			return
		}
		out, err := a.Service.CreateInvite(c.Request.Context(), actor(c), c.Param("workspaceId"), body.Email, body.Role)
		respond(c, out, err)
	})
	g.POST("/workspaces/:workspaceId/invites/:inviteId/revoke", func(c *gin.Context) {
		err := a.Service.RevokeInvite(c.Request.Context(), actor(c), c.Param("workspaceId"), c.Param("inviteId"))
		respond(c, gin.H{"ok": true}, err)
	})
	g.PATCH("/workspaces/:workspaceId/members/:userId", func(c *gin.Context) {
		var body struct {
			Roles  *[]string `json:"roles"`
			Status *string   `json:"status"`
		}
		if !decode(c, &body) {
			return
		}
		err := a.Service.ChangeMember(c.Request.Context(), actor(c), c.Param("workspaceId"), c.Param("userId"), body.Roles, body.Status)
		respond(c, gin.H{"ok": true}, err)
	})
	g.PATCH("/workspaces/:workspaceId/organization", func(c *gin.Context) {
		var body struct {
			Status string `json:"status"`
		}
		if !decode(c, &body) {
			return
		}
		err := a.Service.ChangeOrganizationStatus(c.Request.Context(), actor(c), c.Param("workspaceId"), body.Status)
		respond(c, gin.H{"ok": true}, err)
	})
}
