package agriculture

import (
	"context"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"yingce/backend/internal/flow"
)

const assetCatalogObjectType = "weavesense-agri-asset-object+jws"
const assetCatalogDownloadAuthorityPath = "/internal/agri/v1/assets/download/authority/check"

type assetCatalogDownloadRequest struct {
	ActorUserID          string `json:"actor_user_id"`
	TenantID             string `json:"tenant_id"`
	WorkspaceID          string `json:"workspace_id"`
	Action               string `json:"action"`
	AuthorizationVersion int64  `json:"authorization_version"`
	AssetID              string `json:"asset_id"`
	AssetRevision        int64  `json:"asset_revision"`
	AssetVersion         int64  `json:"asset_version"`
	OwnershipHash        string `json:"ownership_hash"`
	ObjectID             string `json:"object_id"`
	ObjectVersion        int64  `json:"object_version"`
}

func (p *AssetCatalogPlatform) downloadProof(ctx context.Context, actor, ws, id string) (AuthorizationDecision, ResourceOwnership, error) {
	d, e := p.AuthorizeAssetDownload(ctx, actor, ws, id)
	if e != nil {
		return d, ResourceOwnership{}, e
	}
	proofs, e := p.Client.ResolveAssetCatalog(ctx, OwnershipContext{d.TenantID, ws}, []string{id})
	if e != nil {
		return d, ResourceOwnership{}, e
	}
	if len(proofs) != 1 || len(d.ResourceVersions) != 1 || proofs[0].Revision != d.ResourceVersions[0].Revision || proofs[0].Status != "ready" {
		return d, ResourceOwnership{}, ErrConflict
	}
	current, e := p.AuthorizeAssetDownload(ctx, actor, ws, id)
	if e != nil {
		return d, ResourceOwnership{}, e
	}
	if current.AuthorizationVersion != d.AuthorizationVersion || current.ResourceVersions[0].Revision != proofs[0].Revision {
		return d, ResourceOwnership{}, ErrConflict
	}
	return current, proofs[0], nil
}

// The trusted object gateway supplies the immutable object mapping. This method
// never exposes a token or internal URL through a public API.
func (p *AssetCatalogPlatform) IssueAssetDownload(ctx context.Context, actor, ws, assetID string, assetVersion int64, objectID string, objectVersion int64, method string) (string, string, error) {
	if (method != "GET" && method != "HEAD") || !ownershipID.MatchString(objectID) || objectVersion < 1 || objectVersion > maxOwnershipRevision || assetVersion < 1 {
		return "", "", ErrInvalid
	}
	d, proof, e := p.downloadProof(ctx, actor, ws, assetID)
	if e != nil {
		return "", "", e
	}
	if assetVersion > proof.Revision {
		return "", "", ErrNotFound
	}
	hash, e := ownershipBindingHash(proof)
	if e != nil {
		return "", "", e
	}
	path := "/internal/agri/v1/tenants/" + d.TenantID + "/workspaces/" + ws + "/assets/catalog-objects/" + objectID + "/versions/" + strconv.FormatInt(objectVersion, 10)
	now := p.Service.now().UTC().Truncate(time.Second)
	until := now.Add(30 * time.Second)
	if d.ExpiresAt.Before(until) {
		until = d.ExpiresAt.UTC().Truncate(time.Second)
	}
	if !until.After(now) {
		return "", "", ErrForbidden
	}
	claims := map[string]any{"schema_version": "1.0", "issuer": "flow-platform", "subject": "flow-platform", "audience": "agri-gis", "actor": identityActor{"user", actor}, "tenant_id": d.TenantID, "workspace_id": ws, "action": "agri.asset_download.read", "authorization_version": d.AuthorizationVersion, "policy_version": authorizationPolicyVersion, "asset_id": assetID, "asset_revision": proof.Revision, "asset_version": assetVersion, "ownership_hash": hash, "object_id": objectID, "object_version": objectVersion, "issued_at": now.Format(time.RFC3339), "expires_at": until.Format(time.RFC3339), "jti": flow.NewID("jti_"), "trace_id": flow.NewID("trace_"), "request_id": flow.NewID("req_"), "request": map[string]string{"method": method, "path": path}}
	token, e := p.Client.keys.sign(assetCatalogObjectType, claims, now, until)
	if e != nil {
		return "", "", ErrDependencyUnavailable
	}
	return token, path, nil
}
func (p *AssetCatalogPlatform) checkDownload(ctx context.Context, in assetCatalogDownloadRequest) error {
	if in.Action != "agri.asset_download.read" || !assetCatalogID.MatchString(in.AssetID) || !ownershipID.MatchString(in.ObjectID) || in.ObjectVersion < 1 || in.ObjectVersion > maxOwnershipRevision || in.AssetRevision < 1 || in.AssetVersion < 1 || in.AssetVersion > in.AssetRevision || !parcelHashPattern.MatchString(in.OwnershipHash) {
		return ErrForbidden
	}
	d, proof, e := p.downloadProof(ctx, in.ActorUserID, in.WorkspaceID, in.AssetID)
	if e != nil {
		return e
	}
	hash, e := ownershipBindingHash(proof)
	if e != nil {
		return e
	}
	if d.TenantID != in.TenantID || d.AuthorizationVersion != in.AuthorizationVersion || proof.Revision != in.AssetRevision || hash != in.OwnershipHash {
		return ErrForbidden
	}
	return nil
}
func (p *AssetCatalogPlatform) readAssetCatalogObject(ctx context.Context, actor, ws string, ref objectReference, method, byteRange string) (objectBytes, error) {
	var empty objectBytes
	if ref.Resource.Kind != "asset" || !assetCatalogID.MatchString(ref.Resource.ID) || ref.WorkspaceID != ws || ref.ResourceRevision < 1 {
		return empty, ErrInvalid
	}
	if e := validateObjectRange(byteRange); e != nil {
		return empty, e
	}
	before, proof, e := p.downloadProof(ctx, actor, ws, ref.Resource.ID)
	if e != nil {
		return empty, e
	}
	if before.TenantID != ref.TenantID || ref.ResourceRevision > proof.Revision {
		return empty, ErrNotFound
	}
	hash, e := ownershipBindingHash(proof)
	if e != nil {
		return empty, e
	}
	token, path, e := p.IssueAssetDownload(ctx, actor, ws, ref.Resource.ID, ref.ResourceRevision, ref.ObjectID, ref.Version, method)
	if e != nil {
		return empty, e
	}
	value, e := p.Client.readSignedObject(ctx, path, token, method, byteRange)
	if e != nil {
		return empty, e
	}
	after, last, e := p.downloadProof(ctx, actor, ws, ref.Resource.ID)
	if e != nil {
		return empty, e
	}
	lastHash, e := ownershipBindingHash(last)
	if e != nil {
		return empty, e
	}
	if before.AuthorizationVersion != after.AuthorizationVersion || lastHash != hash || last.Revision != proof.Revision {
		return empty, ErrConflict
	}
	return value, nil
}
func (p *AssetCatalogPlatform) RegisterDownloadAuthority(root gin.IRouter, keys *ServiceKeyring) {
	root.POST(assetCatalogDownloadAuthorityPath, func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		fail := func(status int) { c.JSON(status, gin.H{"error": gin.H{"code": "CAPABILITY_DENIED"}}) }
		if keys == nil {
			fail(503)
			return
		}
		if c.Request.RequestURI != assetCatalogDownloadAuthorityPath || c.Request.URL.RawPath != "" || c.Request.URL.RawQuery != "" || c.Request.URL.ForceQuery {
			fail(400)
			return
		}
		raw, e := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
		var in assetCatalogDownloadRequest
		if e != nil || strictIdentityJSON(raw, &in, 1<<20) != nil {
			fail(400)
			return
		}
		h := c.Request.Header.Values("Authorization")
		if len(h) != 1 || !strings.HasPrefix(h[0], "Bearer ") {
			fail(401)
			return
		}
		claims, e := verifyServiceAssertion(keys, "asset.download.check", strings.TrimPrefix(h[0], "Bearer "), "POST", assetCatalogDownloadAuthorityPath, raw, p.Service.now())
		if e != nil || claims.TenantID != in.TenantID || claims.WorkspaceID != in.WorkspaceID {
			fail(401)
			return
		}
		if e = p.checkDownload(c.Request.Context(), in); e != nil {
			fail(403)
			return
		}
		c.JSON(200, gin.H{"active": true, "authorization_version": in.AuthorizationVersion})
	})
}
