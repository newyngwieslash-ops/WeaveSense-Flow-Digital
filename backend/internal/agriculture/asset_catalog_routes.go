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

type assetCatalogWriteBody struct {
	WorkspaceID      string          `json:"workspace_id"`
	PreviewID        string          `json:"preview_id,omitempty"`
	Action           string          `json:"action,omitempty"`
	AssetID          string          `json:"asset_id,omitempty"`
	ExpectedRevision *int64          `json:"expected_revision"`
	Metadata         json.RawMessage `json:"metadata"`
	InitialGrants    []FarmGrant     `json:"initial_grants,omitempty"`
}

func assetCatalogResponse(c *gin.Context, data any, err error) {
	if errors.Is(err, ErrFarmGeometry) {
		c.JSON(422, gin.H{"error": gin.H{"code": "INVALID_METADATA", "message": "Asset metadata validation failed", "retryable": false, "details": gin.H{}}, "request_id": flow.NewID("req_")})
		return
	}
	if err != nil {
		respondAuthorizationV1(c, nil, err)
		return
	}
	op, ok := data.(AssetCatalogOperationView)
	if !ok {
		farmResponse(c, data, nil)
		return
	}
	status := 200
	if c.Request.Method != "GET" {
		switch op.State {
		case "pending", "reconciliation_required":
			status = 202
		case "completed":
			if op.Action == "create" {
				status = 201
			}
		case "rejected":
			status = 422
			if op.ErrorCode == "STATE_CONFLICT" {
				status = 409
			}
			c.JSON(status, gin.H{"error": gin.H{"code": op.ErrorCode, "message": "Asset operation was not applied; read and preview again", "retryable": false, "details": gin.H{"operation_id": op.OperationID, "status_url": op.StatusURL}}, "request_id": flow.NewID("req_")})
			return
		}
	}
	request := flow.NewID("req_")
	c.Header("X-Request-ID", request)
	c.JSON(status, gin.H{"data": op, "meta": gin.H{"request_id": request, "schema_version": "1.0"}})
}
func (a API) registerAssetCatalog(api gin.IRouter) {
	g := api.Group("/agri/v1")
	g.Use(a.sessionMiddleware(assetCatalogResponse))
	p := a.Assets
	if p == nil {
		p = NewAssetCatalogPlatform(a.Service, nil)
	}
	actor := func(c *gin.Context) string { return c.GetString("agriculture.actor") }
	g.GET("/asset-context", func(c *gin.Context) {
		v, e := p.Context(c.Request.Context(), actor(c), c.Query("workspace_id"))
		assetCatalogResponse(c, v, e)
	})
	g.GET("/assets", func(c *gin.Context) {
		limit := 50
		var e error
		if c.Query("limit") != "" {
			limit, e = strconv.Atoi(c.Query("limit"))
			if e != nil {
				assetCatalogResponse(c, nil, ErrInvalid)
				return
			}
		}
		v, e := p.List(c.Request.Context(), actor(c), c.Query("workspace_id"), c.Query("kind"), c.Query("data_class"), limit, c.Query("cursor"))
		assetCatalogResponse(c, v, e)
	})
	g.GET("/assets/:asset_id/versions/:version", func(c *gin.Context) {
		version, e := strconv.ParseInt(c.Param("version"), 10, 64)
		if e != nil || version < 1 {
			assetCatalogResponse(c, nil, ErrInvalid)
			return
		}
		v, e := p.Read(c.Request.Context(), actor(c), c.Query("workspace_id"), c.Param("asset_id"), version)
		assetCatalogResponse(c, v, e)
	})
	g.GET("/asset-operations/:operation_id", func(c *gin.Context) {
		v, e := p.Operation(c.Request.Context(), actor(c), c.Query("workspace_id"), c.Param("operation_id"))
		assetCatalogResponse(c, v, e)
	})
	write := func(action string, preview bool) gin.HandlerFunc {
		return func(c *gin.Context) {
			var b assetCatalogWriteBody
			raw, e := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
			if e != nil || strictFarmJSON(raw, &b, 1<<20) != nil {
				assetCatalogResponse(c, nil, ErrInvalid)
				return
			}
			if !bindingBodyRevisionOnly(c, assetCatalogResponse) {
				return
			}
			chosen := action
			if preview {
				if b.PreviewID != "" {
					assetCatalogResponse(c, nil, ErrInvalid)
					return
				}
				chosen = b.Action
			} else {
				if b.Action != "" || b.AssetID != "" {
					assetCatalogResponse(c, nil, ErrInvalid)
					return
				}
				b.AssetID = c.Param("asset_id")
			}
			payload := AssetCatalogPayload{Action: chosen, AssetID: b.AssetID, ExpectedRevision: b.ExpectedRevision, Metadata: b.Metadata, InitialGrants: b.InitialGrants}
			farm, e := validateAssetCatalogPayload(&payload)
			if e != nil {
				assetCatalogResponse(c, nil, e)
				return
			}
			e = p.Service.withWorkspace(c.Request.Context(), actor(c), b.WorkspaceID, false, func(_ *gorm.DB, s workspaceContext) error {
				if chosen == "create" && farm == "" {
					if !s.Owner && !hasRole(s.Roles, "tenant_admin") {
						return ErrForbidden
					}
				} else if !roleAllowsAction(s.Roles, "agri.asset.create") {
					return ErrForbidden
				}
				return nil
			})
			if e != nil {
				assetCatalogResponse(c, nil, e)
				return
			}
			if p.Client == nil {
				assetCatalogResponse(c, nil, ErrDependencyUnavailable)
				return
			}
			if preview {
				v, e := p.Preview(c.Request.Context(), actor(c), b.WorkspaceID, payload)
				assetCatalogResponse(c, v, e)
				return
			}
			keys := c.Request.Header.Values("Idempotency-Key")
			if len(keys) != 1 {
				assetCatalogResponse(c, nil, ErrInvalid)
				return
			}
			v, e := p.Confirm(c.Request.Context(), actor(c), b.WorkspaceID, b.PreviewID, keys[0], payload)
			assetCatalogResponse(c, v, e)
		}
	}
	g.POST("/asset-previews", write("", true))
	g.POST("/assets", write("create", false))
	g.POST("/assets/:asset_id/versions", write("version", false))
}
