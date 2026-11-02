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
	"yingce/backend/internal/agricanonical"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

type parcelAuthorityRequest struct {
	ActorUserID          string `json:"actor_user_id"`
	TenantID             string `json:"tenant_id"`
	WorkspaceID          string `json:"workspace_id"`
	AuthorizationVersion int64  `json:"authorization_version"`
	OperationID          string `json:"operation_id"`
	RequestHash          string `json:"request_hash"`
	RequestPayloadHash   string `json:"request_payload_hash"`
}

func parcelReceiptBody(op model.AgricultureParcelOperation) ([]byte, string, error) {
	return farmCanonical(map[string]string{"operation_id": op.ID, "request_hash": op.RequestHash})
}

// This checks continued validity of the already-authorized immutable intent.
// A changed epoch is conservatively reconciled; it never refreshes a direct
// parcel grant to the newer GIS geometry revision or revives revoked grants.
func (p *ParcelPlatform) operationAuthority(tx *gorm.DB, scope workspaceContext, op model.AgricultureParcelOperation) error {
	version, e := currentAuthorizationVersion(tx, scope.Tenant.ID)
	if e != nil {
		return e
	}
	if scope.Tenant.ID != op.TenantID || scope.Workspace.ID != op.WorkspaceID || version != op.AuthorizationVersion {
		return ErrConflict
	}
	action := "agri.parcel.update"
	if op.Action == "create" {
		action = "agri.parcel.create"
	}
	if !roleAllowsAction(scope.Roles, action) {
		return ErrForbidden
	}
	var bindings []model.AgricultureResourceBinding
	if e = tx.Where("tenant_id=? AND workspace_id=? AND subject_type='user' AND subject_id=? AND state='active' AND (expires_at IS NULL OR expires_at>?)", op.TenantID, op.WorkspaceID, op.ActorID, p.Service.now()).Find(&bindings).Error; e != nil {
		return e
	}
	for _, b := range bindings {
		matches := b.ResourceKind == "farm" && b.ResourceID == op.FarmID
		if op.Action == "revise" && b.ResourceKind == "parcel" && b.ResourceID == op.ParcelID && b.ReceiptRevision == op.ExpectedRevision {
			matches = true
		}
		if !matches {
			continue
		}
		caps, e := bindingCapabilities(b)
		if e != nil {
			return e
		}
		for _, cap := range caps {
			if cap == action {
				return nil
			}
		}
	}
	return ErrForbidden
}
func (p *ParcelPlatform) checkReceiptAuthority(tx *gorm.DB, scope workspaceContext, in parcelAuthorityRequest) error {
	if scope.Tenant.ID != in.TenantID || scope.Workspace.ID != in.WorkspaceID {
		return ErrForbidden
	}
	var op model.AgricultureParcelOperation
	if tx.Where("id=? AND actor_id=? AND tenant_id=? AND workspace_id=?", in.OperationID, in.ActorUserID, in.TenantID, in.WorkspaceID).First(&op).Error != nil {
		return ErrForbidden
	}
	if op.RequestHash != in.RequestHash || op.State == "preview" {
		return ErrForbidden
	}
	_, digest, e := parcelReceiptBody(op)
	if e != nil || digest != in.RequestPayloadHash {
		return ErrForbidden
	}
	version, e := currentAuthorizationVersion(tx, scope.Tenant.ID)
	if e != nil {
		return e
	}
	if version != in.AuthorizationVersion {
		return ErrConflict
	}
	return p.operationAuthority(tx, scope, op)
}
func (p *ParcelPlatform) receiptCall(ctx context.Context, op model.AgricultureParcelOperation) ([]byte, error) {
	if p.Client == nil {
		return nil, ErrDependencyUnavailable
	}
	body, digest, e := parcelReceiptBody(op)
	if e != nil {
		return nil, e
	}
	in := parcelAuthorityRequest{op.ActorID, op.TenantID, op.WorkspaceID, op.AuthorizationVersion, op.ID, op.RequestHash, digest}
	e = p.Service.withWorkspace(ctx, op.ActorID, op.WorkspaceID, false, func(tx *gorm.DB, s workspaceContext) error { return p.checkReceiptAuthority(tx, s, in) })
	if e != nil {
		return nil, e
	}
	bound, canonical, e := bindIdentityRequest("POST", parcelReceiptPath, body, op.ID, false)
	if e != nil {
		return nil, e
	}
	now := p.Service.now().UTC().Truncate(time.Second)
	until := now.Add(30 * time.Second)
	claims := map[string]any{"schema_version": "1.0", "issuer": "flow-platform", "subject": "flow-platform", "audience": "agri-gis", "actor": identityActor{"user", op.ActorID}, "tenant_id": op.TenantID, "workspace_id": op.WorkspaceID, "action": "parcel.receipt", "authorization_version": in.AuthorizationVersion, "policy_version": authorizationPolicyVersion, "operation_id": op.ID, "request_hash": op.RequestHash, "issued_at": now.Format(time.RFC3339), "expires_at": until.Format(time.RFC3339), "jti": flow.NewID("jti_"), "trace_id": flow.NewID("trace_"), "request_id": flow.NewID("req_"), "request": bound}
	token, e := p.Client.keys.sign(parcelReceiptType, claims, now, until)
	if e != nil {
		return nil, ErrDependencyUnavailable
	}
	return p.Client.farmSend(ctx, "POST", parcelReceiptPath, canonical, token)
}
func (p *ParcelPlatform) RegisterInternal(root gin.IRouter, keys *ServiceKeyring) {
	root.POST(parcelAuthorityPath, func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		fail := func(status int) { c.JSON(status, gin.H{"error": gin.H{"code": "CAPABILITY_DENIED"}}) }
		if keys == nil {
			fail(503)
			return
		}
		if c.Request.URL.RawQuery != "" || c.Request.URL.RawPath != "" || c.Request.URL.ForceQuery || c.Request.RequestURI != parcelAuthorityPath {
			fail(400)
			return
		}
		raw, e := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
		if e != nil {
			fail(400)
			return
		}
		headers := c.Request.Header.Values("Authorization")
		if len(headers) != 1 || !strings.HasPrefix(headers[0], "Bearer ") {
			fail(401)
			return
		}
		claims, e := verifyServiceAssertion(keys, "parcel.operation.check", strings.TrimPrefix(headers[0], "Bearer "), "POST", parcelAuthorityPath, raw, p.Service.now())
		if e != nil {
			fail(401)
			return
		}
		var in parcelAuthorityRequest
		if strictFarmJSON(raw, &in, 1<<20) != nil || in.TenantID != claims.TenantID || in.WorkspaceID != claims.WorkspaceID || !identityActorID.MatchString(in.ActorUserID) {
			fail(400)
			return
		}
		e = p.Service.withWorkspace(c.Request.Context(), in.ActorUserID, in.WorkspaceID, false, func(tx *gorm.DB, s workspaceContext) error { return p.checkReceiptAuthority(tx, s, in) })
		if e != nil {
			fail(403)
			return
		}
		c.JSON(200, gin.H{"active": true, "authorization_version": in.AuthorizationVersion})
	})
}

func parcelDecodeMap(raw []byte) (map[string]any, error) {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return nil, ErrDependencyUnavailable
	}
	if _, e := agricanonical.Canonicalize(raw); e != nil {
		return nil, ErrDependencyUnavailable
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil, ErrDependencyUnavailable
	}
	return out, nil
}
