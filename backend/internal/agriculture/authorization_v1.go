package agriculture

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"

	"github.com/gin-gonic/gin"
)

type authorizationV1Decision struct {
	ActorUserID          string            `json:"actor_user_id"`
	TenantID             string            `json:"tenant_id"`
	WorkspaceID          string            `json:"workspace_id"`
	Action               string            `json:"action"`
	ResourceScope        []ResourceRef     `json:"resource_scope"`
	ResourceVersions     []ResourceVersion `json:"resource_versions"`
	FarmIDs              []string          `json:"farm_ids"`
	AuthorizationVersion int64             `json:"authorization_version"`
	PolicyVersion        string            `json:"policy_version"`
	IssuedAt             time.Time         `json:"issued_at"`
	ExpiresAt            time.Time         `json:"expires_at"`
}

func decisionV1(d AuthorizationDecision) authorizationV1Decision {
	return authorizationV1Decision{ActorUserID: d.ActorID, TenantID: d.TenantID, WorkspaceID: d.WorkspaceID, Action: d.Action, ResourceScope: d.Resources, ResourceVersions: d.ResourceVersions, FarmIDs: d.FarmIDs, AuthorizationVersion: d.AuthorizationVersion, PolicyVersion: d.PolicyVersion, IssuedAt: d.IssuedAt.UTC(), ExpiresAt: d.ExpiresAt.UTC()}
}

type authorizationV1Binding struct {
	ID                   string      `json:"id"`
	TenantID             string      `json:"tenant_id"`
	WorkspaceID          string      `json:"workspace_id"`
	Resource             ResourceRef `json:"resource"`
	SubjectType          string      `json:"subject_type"`
	SubjectUserID        string      `json:"subject_user_id"`
	Capabilities         []string    `json:"capabilities"`
	AuthorizationVersion int64       `json:"authorization_version"`
	State                string      `json:"state"`
	ExpiresAt            *time.Time  `json:"expires_at"`
	CreatedAt            time.Time   `json:"created_at"`
	UpdatedAt            time.Time   `json:"updated_at"`
}

func bindingV1(row model.AgricultureResourceBinding) (authorizationV1Binding, error) {
	var caps []string
	if json.Unmarshal([]byte(row.Capabilities), &caps) != nil || caps == nil || row.SubjectType != "user" {
		return authorizationV1Binding{}, ErrDependencyUnavailable
	}
	for _, capability := range caps {
		if !knownAction(capability) {
			return authorizationV1Binding{}, ErrDependencyUnavailable
		}
	}
	var expiry *time.Time
	if row.ExpiresAt != nil {
		value := row.ExpiresAt.UTC()
		expiry = &value
	}
	return authorizationV1Binding{ID: row.ID, TenantID: row.TenantID, WorkspaceID: row.WorkspaceID, Resource: ResourceRef{row.ResourceKind, row.ResourceID}, SubjectType: row.SubjectType, SubjectUserID: row.SubjectID, Capabilities: caps, AuthorizationVersion: row.AuthVersion, State: row.State, ExpiresAt: expiry, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}, nil
}

func respondAuthorizationV1(c *gin.Context, data any, err error) {
	requestID := c.GetString("agriculture.request_id")
	if requestID == "" {
		requestID = flow.NewID("req_")
		c.Set("agriculture.request_id", requestID)
	}
	c.Header("X-Request-ID", requestID)
	if err == nil {
		envelope := gin.H{"data": data, "meta": gin.H{"request_id": requestID, "schema_version": "1.0"}}
		if page, ok := data.(authorizationV1BindingPage); ok {
			envelope["data"] = page.Items
			envelope["next_cursor"] = page.NextCursor
			envelope["has_more"] = page.HasMore
		}
		c.JSON(200, envelope)
		return
	}
	status, code, message, retryable := 500, "INTERNAL_ERROR", "农业服务暂不可用", false
	switch {
	case errors.Is(err, ErrPreconditionRequired):
		status, code, message = 428, "PRECONDITION_REQUIRED", "必须提供预期授权版本"
	case errors.Is(err, ErrUnauthenticated):
		status, code, message = 401, "UNAUTHENTICATED", "请先登录"
	case errors.Is(err, ErrInvalid):
		status, code, message = 400, "INVALID_ARGUMENT", "请求参数无效"
	case errors.Is(err, ErrForbidden):
		status, code, message = 403, "CAPABILITY_DENIED", "当前主体无此权限"
	case errors.Is(err, ErrNotFound):
		status, code, message = 404, "RESOURCE_NOT_FOUND", "资源不可用"
	case errors.Is(err, ErrConflict):
		status, code, message = 409, "REVISION_CONFLICT", "授权版本冲突，请重新读取"
	case errors.Is(err, ErrDependencyUnavailable):
		status, code, message, retryable = 503, "DEPENDENCY_UNAVAILABLE", "资源归属验证暂不可用", true
	}
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message, "retryable": retryable, "details": gin.H{}}, "request_id": requestID})
}

type authorizationV1BindingPage struct {
	Items      []authorizationV1Binding
	NextCursor string
	HasMore    bool
}

// This is the canonical agricultural protocol adapter. The platform UI's
// /agriculture compatibility routes keep their existing camelCase envelope;
// both adapters use exactly the same session middleware and domain services.
func (a API) registerAuthorizationV1(api gin.IRouter) {
	g := api.Group("/agri/v1")
	g.Use(func(c *gin.Context) { c.Set("agriculture.request_id", flow.NewID("req_")); c.Next() }, a.sessionMiddleware(respondAuthorizationV1))
	actor := func(c *gin.Context) string { return c.GetString("agriculture.actor") }
	g.POST("/workspaces/:workspace_id/authorization", func(c *gin.Context) {
		var body struct {
			Action    string        `json:"action"`
			Resources []ResourceRef `json:"resources"`
		}
		if !decodeWithResponder(c, &body, respondAuthorizationV1) {
			return
		}
		out, err := a.Service.Authorize(c.Request.Context(), actor(c), c.Param("workspace_id"), body.Action, body.Resources)
		respondAuthorizationV1(c, decisionV1(out), err)
	})
	g.GET("/workspaces/:workspace_id/resource-bindings", func(c *gin.Context) {
		limit := 0
		for key, values := range c.Request.URL.Query() {
			if (key != "limit" && key != "cursor") || len(values) != 1 || values[0] == "" {
				respondAuthorizationV1(c, nil, ErrInvalid)
				return
			}
		}
		if value, ok := c.GetQuery("limit"); ok {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > 200 {
				respondAuthorizationV1(c, nil, ErrInvalid)
				return
			}
			limit = parsed
		}
		page, err := a.Service.ListBindingPage(c.Request.Context(), actor(c), c.Param("workspace_id"), limit, c.Query("cursor"))
		if err != nil {
			respondAuthorizationV1(c, nil, err)
			return
		}
		out := make([]authorizationV1Binding, 0, len(page.Items))
		for _, row := range page.Items {
			dto, err := bindingV1(row)
			if err != nil {
				respondAuthorizationV1(c, nil, err)
				return
			}
			out = append(out, dto)
		}
		respondAuthorizationV1(c, authorizationV1BindingPage{Items: out, NextCursor: page.NextCursor, HasMore: page.HasMore}, nil)
	})
	g.POST("/workspaces/:workspace_id/resource-bindings", func(c *gin.Context) {
		if !bindingBodyRevisionOnly(c, respondAuthorizationV1) {
			return
		}
		var body struct {
			SubjectUserID    string      `json:"subject_user_id"`
			Resource         ResourceRef `json:"resource"`
			Capabilities     []string    `json:"capabilities"`
			ExpiresAt        *time.Time  `json:"expires_at"`
			ExpectedRevision *int64      `json:"expected_revision"`
		}
		if !decodeWithResponder(c, &body, respondAuthorizationV1) {
			return
		}
		out, err := a.Service.GrantResourceBinding(c.Request.Context(), actor(c), c.Param("workspace_id"), body.SubjectUserID, body.Resource, body.Capabilities, body.ExpiresAt, body.ExpectedRevision)
		if err != nil {
			respondAuthorizationV1(c, nil, err)
			return
		}
		dto, err := bindingV1(out)
		respondAuthorizationV1(c, dto, err)
	})
	g.POST("/workspaces/:workspace_id/resource-bindings/:binding_id/revoke", func(c *gin.Context) {
		if !bindingBodyRevisionOnly(c, respondAuthorizationV1) {
			return
		}
		var body struct {
			ExpectedRevision *int64 `json:"expected_revision"`
		}
		if !decodeWithResponder(c, &body, respondAuthorizationV1) {
			return
		}
		err := a.Service.RevokeBinding(c.Request.Context(), actor(c), c.Param("workspace_id"), c.Param("binding_id"), body.ExpectedRevision)
		respondAuthorizationV1(c, gin.H{"ok": true}, err)
	})
}
