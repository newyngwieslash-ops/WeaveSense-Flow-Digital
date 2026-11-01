package agriculture

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"io"
	"net/http"
	"strconv"
	"yingce/backend/internal/flow"
)

type farmWriteBody struct {
	WorkspaceID      string          `json:"workspace_id"`
	PreviewID        string          `json:"preview_id"`
	Action           string          `json:"action,omitempty"`
	FarmID           string          `json:"farm_id,omitempty"`
	ExpectedRevision int64           `json:"expected_revision,omitempty"`
	Farm             json.RawMessage `json:"farm,omitempty"`
	InitialGrants    []FarmGrant     `json:"initial_grants,omitempty"`
	Reason           string          `json:"reason,omitempty"`
}

func farmResponse(c *gin.Context, data any, err error) {
	if op, ok := data.(FarmOperationView); ok && err == nil && op.State == "rejected" && c.Request.Method != "GET" {
		status := 422
		if op.ErrorCode == "STATE_CONFLICT" {
			status = 409
		}
		c.JSON(status, gin.H{"error": gin.H{"code": op.ErrorCode, "message": "农场操作未应用，请重新读取并预览", "retryable": false, "details": gin.H{"operation_id": op.OperationID, "status_url": op.StatusURL}}, "request_id": flow.NewID("req_")})
		return
	}
	if err != nil {
		if errors.Is(err, ErrFarmGeometry) {
			c.JSON(422, gin.H{"error": gin.H{"code": "INVALID_GEOMETRY", "message": "农场几何无效", "retryable": false, "details": gin.H{}}, "request_id": flow.NewID("req_")})
			return
		}
		respondAuthorizationV1(c, nil, err)
		return
	}
	status := 200
	if operation, ok := data.(FarmOperationView); ok && c.Request.Method != "GET" {
		if operation.State == "pending" || operation.State == "reconciliation_required" {
			status = 202
		} else if operation.State == "completed" && operation.Action == "create" && c.Request.Method == "POST" {
			status = 201
		}
	}
	requestID := flow.NewID("req_")
	c.Header("X-Request-ID", requestID)
	if page, ok := data.(farmListPage); ok {
		c.JSON(status, gin.H{"data": page.Items, "meta": gin.H{"request_id": requestID, "schema_version": "1.0"}, "next_cursor": page.NextCursor, "has_more": page.HasMore})
		return
	}
	c.JSON(status, gin.H{"data": data, "meta": gin.H{"request_id": requestID, "schema_version": "1.0"}})
}

type farmListPage struct {
	Items      any
	NextCursor any
	HasMore    any
}

func (a API) registerFarms(api gin.IRouter) {
	g := api.Group("/agri/v1")
	g.Use(a.sessionMiddleware(farmResponse))
	p := a.Farms
	if p == nil {
		p = NewFarmPlatform(a.Service, nil)
	}
	actor := func(c *gin.Context) string { return c.GetString("agriculture.actor") }
	g.GET("/context", func(c *gin.Context) {
		out, e := p.Context(c.Request.Context(), actor(c), c.Query("workspace_id"))
		farmResponse(c, out, e)
	})
	g.GET("/farms", func(c *gin.Context) {
		limit := 50
		var e error
		if c.Query("limit") != "" {
			limit, e = strconv.Atoi(c.Query("limit"))
			if e != nil {
				farmResponse(c, nil, ErrInvalid)
				return
			}
		}
		status := c.DefaultQuery("status", "active")
		out, e := p.ListFarms(c.Request.Context(), actor(c), c.Query("workspace_id"), status, limit, c.Query("cursor"), c.Query("search"))
		if e != nil {
			farmResponse(c, nil, e)
			return
		}
		farmResponse(c, farmListPage{out["items"], out["next_cursor"], out["has_more"]}, nil)
	})
	g.GET("/farms/:farm_id", func(c *gin.Context) {
		out, e := p.ReadFarm(c.Request.Context(), actor(c), c.Query("workspace_id"), c.Param("farm_id"))
		farmResponse(c, out, e)
	})
	g.GET("/farm-operations/:operation_id", func(c *gin.Context) {
		out, e := p.Operation(c.Request.Context(), actor(c), c.Query("workspace_id"), c.Param("operation_id"))
		farmResponse(c, out, e)
	})
	write := func(action string, preview bool) gin.HandlerFunc {
		return func(c *gin.Context) {
			action := action
			var body farmWriteBody
			raw, readErr := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
			if readErr != nil || strictFarmJSON(raw, &body, 1<<20) != nil {
				farmResponse(c, nil, ErrInvalid)
				return
			}
			if !bindingBodyRevisionOnly(c, farmResponse) {
				return
			}
			if preview {
				if body.PreviewID != "" {
					farmResponse(c, nil, ErrInvalid)
					return
				}
				action = body.Action
			} else {
				if body.Action != "" || body.FarmID != "" {
					farmResponse(c, nil, ErrInvalid)
					return
				}
				body.FarmID = c.Param("farm_id")
			}
			payload := FarmPayload{Action: action, FarmID: body.FarmID, ExpectedRevision: body.ExpectedRevision, Farm: body.Farm, InitialGrants: body.InitialGrants, Reason: body.Reason}
			// Establish trusted membership and role ceiling before dependency calls,
			// including disabled providers. A caller cannot choose tenant or role.
			e := p.Service.withWorkspace(c.Request.Context(), actor(c), body.WorkspaceID, false, func(_ *gorm.DB, s workspaceContext) error {
				if action == "create" {
					if !s.Owner && !hasRole(s.Roles, "tenant_admin") {
						return ErrForbidden
					}
				} else if !roleAllowsAction(s.Roles, "agri.farm."+action) {
					return ErrForbidden
				}
				return nil
			})
			if e != nil {
				farmResponse(c, nil, e)
				return
			}
			if p.Client == nil {
				farmResponse(c, nil, ErrDependencyUnavailable)
				return
			}
			if preview {
				out, e := p.Preview(c.Request.Context(), actor(c), body.WorkspaceID, payload)
				farmResponse(c, out, e)
				return
			}
			keys := c.Request.Header.Values("Idempotency-Key")
			if len(keys) != 1 {
				farmResponse(c, nil, ErrInvalid)
				return
			}
			out, e := p.Confirm(c.Request.Context(), actor(c), body.WorkspaceID, body.PreviewID, keys[0], payload)
			farmResponse(c, out, e)
		}
	}
	g.POST("/farm-previews", write("", true))
	g.POST("/farms", write("create", false))
	g.PATCH("/farms/:farm_id", write("update", false))
	g.POST("/farms/:farm_id/archive", write("archive", false))
}
