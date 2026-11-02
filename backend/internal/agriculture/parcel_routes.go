package agriculture

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"io"
	"net/http"
	"strconv"
	"yingce/backend/internal/flow"
)

type parcelWriteBody struct {
	WorkspaceID          string          `json:"workspace_id"`
	PreviewID            string          `json:"preview_id,omitempty"`
	Action               string          `json:"action,omitempty"`
	FarmID               string          `json:"farm_id,omitempty"`
	ParcelID             string          `json:"parcel_id,omitempty"`
	ExpectedRevision     *int64          `json:"expected_revision"`
	ExpectedFarmRevision int64           `json:"expected_farm_revision,omitempty"`
	Parcel               json.RawMessage `json:"parcel,omitempty"`
	Geometry             json.RawMessage `json:"geometry,omitempty"`
	SourceCRS            string          `json:"source_crs,omitempty"`
	CoordinateOrder      string          `json:"coordinate_order,omitempty"`
	Reason               string          `json:"reason"`
}

func parcelResponse(c *gin.Context, data any, err error) {
	if err != nil {
		farmResponse(c, nil, err)
		return
	}
	op, ok := data.(ParcelOperationView)
	if !ok {
		farmResponse(c, data, nil)
		return
	}
	status := http.StatusOK
	if c.Request.Method != "GET" {
		switch op.State {
		case "pending", "reconciliation_required":
			status = http.StatusAccepted
		case "completed":
			if op.Action == "create" {
				status = http.StatusCreated
			}
		case "rejected":
			status = http.StatusUnprocessableEntity
			if op.ErrorCode == "STATE_CONFLICT" {
				status = http.StatusConflict
			}
			c.JSON(status, gin.H{"error": gin.H{"code": op.ErrorCode, "message": "Parcel operation was not applied; read and preview again", "retryable": false, "details": gin.H{"operation_id": op.OperationID, "status_url": op.StatusURL}}, "request_id": flow.NewID("req_")})
			return
		}
	}
	id := flow.NewID("req_")
	c.Header("X-Request-ID", id)
	c.JSON(status, gin.H{"data": op, "meta": gin.H{"request_id": id, "schema_version": "1.0"}})
}

func (a API) registerParcels(api gin.IRouter) {
	g := api.Group("/agri/v1")
	g.Use(a.sessionMiddleware(parcelResponse))
	p := a.Parcels
	if p == nil {
		p = NewParcelPlatform(a.Service, nil)
	}
	actor := func(c *gin.Context) string { return c.GetString("agriculture.actor") }
	limit := func(c *gin.Context) (int, error) {
		if c.Query("limit") == "" {
			return 50, nil
		}
		v, e := strconv.Atoi(c.Query("limit"))
		if e != nil || v < 1 || v > 200 {
			return 0, ErrInvalid
		}
		return v, nil
	}
	g.GET("/farms/:farm_id/parcel-context", func(c *gin.Context) {
		v, e := p.Context(c.Request.Context(), actor(c), c.Query("workspace_id"), c.Param("farm_id"))
		parcelResponse(c, v, e)
	})
	g.GET("/parcel-farms", func(c *gin.Context) {
		n, e := limit(c)
		if e != nil {
			parcelResponse(c, nil, e)
			return
		}
		v, e := p.Parents(c.Request.Context(), actor(c), c.Query("workspace_id"), n, c.Query("cursor"))
		parcelResponse(c, v, e)
	})
	g.GET("/farms/:farm_id/parcels", func(c *gin.Context) {
		n, e := limit(c)
		if e != nil {
			parcelResponse(c, nil, e)
			return
		}
		v, e := p.List(c.Request.Context(), actor(c), c.Query("workspace_id"), c.Param("farm_id"), n, c.Query("cursor"))
		parcelResponse(c, v, e)
	})
	g.GET("/parcels/:parcel_id", func(c *gin.Context) {
		var revision int64
		var e error
		if values, exists := c.Request.URL.Query()["revision"]; exists {
			if len(values) != 1 || values[0] == "" || c.Query("at") != "" {
				parcelResponse(c, nil, ErrInvalid)
				return
			}
			revision, e = strconv.ParseInt(values[0], 10, 64)
			if e != nil || revision < 1 {
				parcelResponse(c, nil, ErrInvalid)
				return
			}
		}
		v, e := p.Read(c.Request.Context(), actor(c), c.Query("workspace_id"), c.Param("parcel_id"), revision, c.Query("at"))
		parcelResponse(c, v, e)
	})
	g.GET("/parcels/:parcel_id/history", func(c *gin.Context) {
		n, e := limit(c)
		if e != nil {
			parcelResponse(c, nil, e)
			return
		}
		v, e := p.History(c.Request.Context(), actor(c), c.Query("workspace_id"), c.Param("parcel_id"), n, c.Query("cursor"))
		parcelResponse(c, v, e)
	})
	g.GET("/parcel-operations/:operation_id", func(c *gin.Context) {
		v, e := p.Operation(c.Request.Context(), actor(c), c.Query("workspace_id"), c.Param("operation_id"))
		parcelResponse(c, v, e)
	})
	write := func(action string, preview bool) gin.HandlerFunc {
		return func(c *gin.Context) {
			var b parcelWriteBody
			raw, e := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
			if e != nil || strictFarmJSON(raw, &b, 1<<20) != nil {
				parcelResponse(c, nil, ErrInvalid)
				return
			}
			if !bindingBodyRevisionOnly(c, parcelResponse) {
				return
			}
			chosen := action
			if preview {
				if b.PreviewID != "" {
					parcelResponse(c, nil, ErrInvalid)
					return
				}
				chosen = b.Action
			} else {
				if b.Action != "" || b.FarmID != "" || b.ParcelID != "" {
					parcelResponse(c, nil, ErrInvalid)
					return
				}
				b.FarmID = c.Param("farm_id")
				b.ParcelID = c.Param("parcel_id")
			}
			payload := ParcelPayload{Action: chosen, FarmID: b.FarmID, ParcelID: b.ParcelID, ExpectedRevision: b.ExpectedRevision, ExpectedFarmRevision: b.ExpectedFarmRevision, Parcel: b.Parcel, Geometry: b.Geometry, SourceCRS: b.SourceCRS, CoordinateOrder: b.CoordinateOrder, Reason: b.Reason}
			actionCap := "agri.parcel.create"
			if chosen == "revise" {
				actionCap = "agri.parcel.update"
			}
			e = p.Service.withWorkspace(c.Request.Context(), actor(c), b.WorkspaceID, false, func(_ *gorm.DB, s workspaceContext) error {
				if !roleAllowsAction(s.Roles, actionCap) {
					return ErrForbidden
				}
				return nil
			})
			if e != nil {
				parcelResponse(c, nil, e)
				return
			}
			if e = validateParcelPayload(payload); e != nil {
				parcelResponse(c, nil, e)
				return
			}
			if p.Client == nil {
				parcelResponse(c, nil, ErrDependencyUnavailable)
				return
			}
			if preview {
				v, e := p.Preview(c.Request.Context(), actor(c), b.WorkspaceID, payload)
				parcelResponse(c, v, e)
				return
			}
			keys := c.Request.Header.Values("Idempotency-Key")
			if len(keys) != 1 {
				parcelResponse(c, nil, ErrInvalid)
				return
			}
			v, e := p.Confirm(c.Request.Context(), actor(c), b.WorkspaceID, b.PreviewID, keys[0], payload)
			parcelResponse(c, v, e)
		}
	}
	g.POST("/parcel-previews", write("", true))
	g.POST("/farms/:farm_id/parcels", write("create", false))
	g.POST("/parcels/:parcel_id/revisions", write("revise", false))
}
