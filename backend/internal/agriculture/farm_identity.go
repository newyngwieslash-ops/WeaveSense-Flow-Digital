package agriculture

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"io"
	"net/http"
	"strings"
	"time"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

type farmAuthorityRequest struct {
	ActorUserID          string   `json:"actor_user_id"`
	TenantID             string   `json:"tenant_id"`
	WorkspaceID          string   `json:"workspace_id"`
	Action               string   `json:"action"`
	AuthorizationVersion int64    `json:"authorization_version"`
	OperationID          string   `json:"operation_id"`
	RequestHash          string   `json:"request_hash"`
	GrantPlanHash        string   `json:"grant_plan_hash"`
	FarmIDs              []string `json:"farm_ids"`
	RequestPayloadHash   string   `json:"request_payload_hash"`
}

func (p *FarmPlatform) checkAuthority(tx *gorm.DB, scope workspaceContext, in farmAuthorityRequest) error {
	if scope.Tenant.ID != in.TenantID || scope.Workspace.ID != in.WorkspaceID {
		return ErrForbidden
	}
	version, e := currentAuthorizationVersion(tx, scope.Tenant.ID)
	if e != nil {
		return e
	}
	if version != in.AuthorizationVersion {
		return ErrConflict
	}
	switch in.Action {
	case "farm.context":
		if len(in.FarmIDs) != 0 || in.RequestHash != zeroFarmHash || in.GrantPlanHash != zeroFarmHash {
			return ErrForbidden
		}
		return nil
	case "farm.list", "farm.history.read":
		if len(in.FarmIDs) == 0 || len(in.FarmIDs) > 200 || in.RequestHash != zeroFarmHash || in.GrantPlanHash != zeroFarmHash || !roleAllowsAction(scope.Roles, "agri.farm.read") {
			return ErrForbidden
		}
		if in.Action == "farm.history.read" && len(in.FarmIDs) != 1 {
			return ErrForbidden
		}
		seen := map[string]bool{}
		for _, id := range in.FarmIDs {
			if seen[id] || !ownershipID.MatchString(id) {
				return ErrForbidden
			}
			seen[id] = true
			var b model.AgricultureResourceBinding
			if tx.Where("tenant_id=? AND workspace_id=? AND resource_kind='farm' AND resource_id=? AND subject_type='user' AND subject_id=? AND state='active' AND (expires_at IS NULL OR expires_at>?)", scope.Tenant.ID, scope.Workspace.ID, id, in.ActorUserID, p.Service.now()).First(&b).Error != nil {
				return ErrForbidden
			}
			caps, e := bindingCapabilities(b)
			if e != nil {
				return e
			}
			found := false
			for _, c := range caps {
				if c == "agri.farm.read" {
					found = true
				}
			}
			if !found {
				return ErrForbidden
			}
		}
		return nil
	case "farm.preview.create", "farm.create", "farm.receipt":
		if len(in.FarmIDs) != 0 {
			return ErrForbidden
		}
		var op model.AgricultureFarmOperation
		if tx.Where("id=? AND tenant_id=? AND workspace_id=? AND actor_id=?", in.OperationID, scope.Tenant.ID, scope.Workspace.ID, in.ActorUserID).First(&op).Error != nil {
			return ErrForbidden
		}
		if op.RequestHash != in.RequestHash || op.GrantPlanHash != in.GrantPlanHash {
			return ErrForbidden
		}
		if in.Action != "farm.receipt" {
			if op.Action != "create" {
				return ErrForbidden
			}
			if e := p.validateCreateAuthority(tx, scope, op); e != nil {
				return e
			}
		}
		if in.Action == "farm.preview.create" && (op.State != "preview" || !op.ExpiresAt.After(p.Service.now())) {
			return ErrForbidden
		}
		if in.Action == "farm.create" && op.State != "pending" {
			return ErrForbidden
		}
		body, e := farmCommandBody(op, in.Action == "farm.receipt")
		if e != nil {
			return e
		}
		var value any
		if json.Unmarshal(body, &value) != nil {
			return ErrInvalid
		}
		_, digest, e := farmCanonical(value)
		if e != nil || digest != in.RequestPayloadHash {
			return ErrForbidden
		}
		return nil
	default:
		return ErrForbidden
	}
}

func (p *FarmPlatform) validateCreateAuthority(tx *gorm.DB, scope workspaceContext, op model.AgricultureFarmOperation) error {
	if !scope.Owner && !hasRole(scope.Roles, "tenant_admin") {
		return ErrForbidden
	}
	payload, e := farmPayload(op)
	if e != nil {
		return e
	}
	for _, g := range payload.InitialGrants {
		target, e := resolve(tx, g.SubjectUserID, scope.Workspace.ID, false)
		if e != nil {
			return e
		}
		if g.ExpiresAt != nil && (!g.ExpiresAt.After(p.Service.now()) || g.ExpiresAt.After(p.Service.now().Add(366*24*time.Hour))) {
			return ErrInvalid
		}
		for _, cap := range g.Capabilities {
			if !roleAllowsAction(target.Roles, cap) {
				return ErrForbidden
			}
		}
	}
	return nil
}

func (p *FarmPlatform) workspaceCall(ctx context.Context, actor, ws, action, opID, requestHash, grantHash, path string, ids []string, body []byte) ([]byte, error) {
	if p.Client == nil {
		return nil, ErrDependencyUnavailable
	}
	bound, canonical, e := bindIdentityRequest("POST", path, body, opID, false)
	if e != nil {
		return nil, e
	}
	in := farmAuthorityRequest{ActorUserID: actor, WorkspaceID: ws, Action: action, OperationID: opID, RequestHash: requestHash, GrantPlanHash: grantHash, FarmIDs: ids, RequestPayloadHash: bound.PayloadHash}
	e = p.Service.withWorkspace(ctx, actor, ws, false, func(tx *gorm.DB, s workspaceContext) error {
		in.TenantID = s.Tenant.ID
		var err error
		in.AuthorizationVersion, err = currentAuthorizationVersion(tx, s.Tenant.ID)
		if err != nil {
			return err
		}
		return p.checkAuthority(tx, s, in)
	})
	if e != nil {
		return nil, e
	}
	now := p.Service.now().Truncate(time.Second)
	until := now.Add(30 * time.Second)
	claims := map[string]any{"schema_version": "1.0", "issuer": "flow-platform", "subject": "flow-platform", "audience": "agri-gis", "actor": map[string]string{"kind": "user", "id": actor}, "tenant_id": in.TenantID, "workspace_id": ws, "action": action, "authorization_version": in.AuthorizationVersion, "policy_version": authorizationPolicyVersion, "operation_id": opID, "request_hash": requestHash, "grant_plan_hash": grantHash, "farm_ids": ids, "issued_at": now.Format("2006-01-02T15:04:05Z"), "expires_at": until.Format("2006-01-02T15:04:05Z"), "jti": flow.NewID("jti_"), "trace_id": flow.NewID("trace_"), "request_id": flow.NewID("req_"), "request": bound}
	token, e := p.Client.keys.sign(farmWorkspaceType, claims, now, until)
	if e != nil {
		return nil, ErrDependencyUnavailable
	}
	return p.Client.farmSend(ctx, "POST", path, canonical, token)
}

func (p *FarmPlatform) RegisterInternal(root gin.IRouter, keys *ServiceKeyring) {
	root.POST(farmAuthorityPath, func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		fail := func(n int) { c.JSON(n, gin.H{"error": gin.H{"code": "CAPABILITY_DENIED"}}) }
		if keys == nil {
			fail(503)
			return
		}
		if c.Request.URL.RawQuery != "" || c.Request.URL.ForceQuery || c.Request.URL.RawPath != "" || c.Request.RequestURI != farmAuthorityPath {
			fail(400)
			return
		}
		raw, e := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
		if e != nil {
			fail(400)
			return
		}
		h := c.Request.Header.Values("Authorization")
		if len(h) != 1 || !strings.HasPrefix(h[0], "Bearer ") {
			fail(401)
			return
		}
		assertion, e := verifyServiceAssertion(keys, "farm.operation.check", strings.TrimPrefix(h[0], "Bearer "), "POST", farmAuthorityPath, raw, p.Service.now())
		if e != nil {
			fail(401)
			return
		}
		var in farmAuthorityRequest
		if strictFarmJSON(raw, &in, 1<<20) != nil || in.TenantID != assertion.TenantID || in.WorkspaceID != assertion.WorkspaceID || !identityActorID.MatchString(in.ActorUserID) {
			fail(400)
			return
		}
		e = p.Service.withWorkspace(c.Request.Context(), in.ActorUserID, in.WorkspaceID, false, func(tx *gorm.DB, s workspaceContext) error { return p.checkAuthority(tx, s, in) })
		if e != nil {
			fail(403)
			return
		}
		c.JSON(200, gin.H{"active": true, "authorization_version": in.AuthorizationVersion})
	})
}
