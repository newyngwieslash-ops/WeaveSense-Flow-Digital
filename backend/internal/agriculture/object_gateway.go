package agriculture

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const objectResolvePath = "/internal/agri/v1/objects/resolve"
const maximumObjectBytes int64 = 64 << 20

var ErrObjectRange = errors.New("agriculture: object range unavailable")
var objectRangePattern = regexp.MustCompile(`^bytes=([0-9]*)-([0-9]*)$`)
var objectContentRangePattern = regexp.MustCompile(`^bytes ([0-9]+)-([0-9]+)/([0-9]+)$`)

// Bound buffered byte validation independently of the general GIS JSON client.
// Larger imagery/COG transfer is a later explicit provider capability, not an
// unbounded allocation or a caller-selected URL in this foundational gateway.
var objectReadSlots = make(chan struct{}, 2)

type objectReference struct {
	TenantID         string      `json:"tenant_id"`
	WorkspaceID      string      `json:"workspace_id"`
	ObjectID         string      `json:"object_id"`
	Version          int64       `json:"version"`
	Resource         ResourceRef `json:"resource"`
	ResourceRevision int64       `json:"resource_revision"`
}

func objectAction(kind string) (action, segment string, err error) {
	switch kind {
	case "asset":
		return "agri.asset_download.read", "assets", nil
	case "report":
		return "agri.report.read", "reports", nil
	default:
		return "", "", ErrDependencyUnavailable
	}
}

func (g *GISClient) resolveObject(ctx context.Context, scope OwnershipContext, objectID string, version int64) (objectReference, error) {
	var out objectReference
	if g == nil {
		return out, ErrDependencyUnavailable
	}
	if !ownershipID.MatchString(objectID) || version < 1 || version > maxOwnershipRevision {
		return out, ErrInvalid
	}
	body, err := marshalIdentityBody(struct {
		TenantID    string `json:"tenant_id"`
		WorkspaceID string `json:"workspace_id"`
		ObjectID    string `json:"object_id"`
		Version     int64  `json:"version"`
	}{scope.TenantID, scope.WorkspaceID, objectID, version})
	if err != nil {
		return out, err
	}
	token, canonical, err := issueServiceAssertion(g.keys, "object.resolve", scope, body, g.now())
	if err != nil {
		return out, err
	}
	raw, err := g.send(ctx, objectResolvePath, canonical, token)
	if err != nil {
		return out, err
	}
	if strictIdentityJSON(raw, &out, 16<<10) != nil || out.TenantID != scope.TenantID || out.WorkspaceID != scope.WorkspaceID || out.ObjectID != objectID || out.Version != version || !ownershipID.MatchString(out.Resource.ID) || out.ResourceRevision < 1 || out.ResourceRevision > maxOwnershipRevision {
		return objectReference{}, ErrDependencyUnavailable
	}
	if _, _, err = objectAction(out.Resource.Kind); err != nil {
		return objectReference{}, err
	}
	return out, nil
}

func validateObjectRange(value string) error {
	if value == "" {
		return nil
	}
	m := objectRangePattern.FindStringSubmatch(value)
	if m == nil || (m[1] == "" && m[2] == "") {
		return ErrInvalid
	}
	var start, end int64
	var err error
	if m[1] != "" {
		start, err = strconv.ParseInt(m[1], 10, 64)
		if err != nil || start < 0 {
			return ErrInvalid
		}
	}
	if m[2] != "" {
		end, err = strconv.ParseInt(m[2], 10, 64)
		if err != nil || end < 0 {
			return ErrInvalid
		}
	}
	if m[1] == "" && end == 0 || m[1] != "" && m[2] != "" && start > end {
		return ErrInvalid
	}
	return nil
}

type objectBytes struct {
	status       int
	length       int64
	contentRange string
	mime         string
	body         []byte
}

func (g *GISClient) readObject(ctx context.Context, s Service, actor string, decision AuthorizationDecision, ref objectReference, method, byteRange string) (objectBytes, error) {
	var out objectBytes
	_, segment, err := objectAction(ref.Resource.Kind)
	if err != nil {
		return out, err
	}
	if g == nil || g.base == nil || g.client == nil || (method != "GET" && method != "HEAD") {
		return out, ErrDependencyUnavailable
	}
	if err = validateObjectRange(byteRange); err != nil {
		return out, err
	}
	path := fmt.Sprintf("/internal/agri/v1/tenants/%s/workspaces/%s/%s/objects/%s/versions/%d", ref.TenantID, ref.WorkspaceID, segment, ref.ObjectID, ref.Version)
	// This performs fresh platform authorization again; no caller-supplied actor,
	// role, price, service credential or old metadata response grants access.
	token, _, err := s.issueDelegation(ctx, g.keys, actor, decision, method, path, nil, "")
	if err != nil {
		return out, err
	}
	endpoint := *g.base
	endpoint.Path = path
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), nil)
	if err != nil {
		return out, ErrInvalid
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Accept-Encoding", "identity")
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}
	response, err := g.client.Do(req)
	if err != nil {
		return out, ErrDependencyUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case 401, 403:
		return out, ErrForbidden
	case 404:
		return out, ErrNotFound
	case 409:
		return out, ErrConflict
	case 416:
		return out, ErrObjectRange
	case 200, 206:
	default:
		return out, ErrDependencyUnavailable
	}
	if response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity" {
		return out, ErrDependencyUnavailable
	}
	if response.ContentLength < 0 || response.ContentLength > maximumObjectBytes {
		return out, ErrDependencyUnavailable
	}
	out.status, out.length = response.StatusCode, response.ContentLength
	if out.status == 206 {
		m := objectContentRangePattern.FindStringSubmatch(response.Header.Get("Content-Range"))
		if byteRange == "" || m == nil {
			return objectBytes{}, ErrDependencyUnavailable
		}
		first, e1 := strconv.ParseInt(m[1], 10, 64)
		last, e2 := strconv.ParseInt(m[2], 10, 64)
		total, e3 := strconv.ParseInt(m[3], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || first < 0 || last < first || total <= last || total > maximumObjectBytes || last-first+1 != out.length {
			return objectBytes{}, ErrDependencyUnavailable
		}
		want := objectRangePattern.FindStringSubmatch(byteRange)
		var start, end int64
		end = total - 1
		if want[1] == "" {
			suffix, _ := strconv.ParseInt(want[2], 10, 64)
			if suffix < total {
				start = total - suffix
			}
		} else {
			start, _ = strconv.ParseInt(want[1], 10, 64)
			if want[2] != "" {
				requested, _ := strconv.ParseInt(want[2], 10, 64)
				if requested < end {
					end = requested
				}
			}
		}
		if first != start || last != end {
			return objectBytes{}, ErrDependencyUnavailable
		}
		out.contentRange = response.Header.Get("Content-Range")
	} else if response.Header.Get("Content-Range") != "" {
		return objectBytes{}, ErrDependencyUnavailable
	}
	// Never forward cookies, credentials, redirects, upstream cache metadata or
	// arbitrary active content into the platform's authenticated origin.
	switch response.Header.Get("Content-Type") {
	case "image/png", "image/jpeg", "image/webp", "image/avif":
		out.mime = response.Header.Get("Content-Type")
	default:
		out.mime = "application/octet-stream"
	}
	if method != "HEAD" {
		out.body, err = io.ReadAll(io.LimitReader(response.Body, out.length+1))
		if err != nil || int64(len(out.body)) != out.length {
			return objectBytes{}, ErrDependencyUnavailable
		}
	}
	return out, nil
}

func (a API) registerObjectGateway(api gin.IRouter) {
	g := api.Group("/agri/v1/workspaces/:workspace_id/objects")
	g.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "private, no-store")
		c.Header("Vary", "Cookie")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Content-Security-Policy", "default-src 'none'; sandbox")
		c.Next()
	}, a.sessionMiddleware(respond))
	handler := func(c *gin.Context) {
		if c.Request.URL.RawQuery != "" || c.Request.URL.ForceQuery || c.Request.URL.RawPath != "" || c.Request.ContentLength > 0 || len(c.Request.TransferEncoding) != 0 || len(c.Request.Header.Values("Range")) > 1 || len(c.Request.Header.Values("If-Range")) != 0 {
			respond(c, nil, ErrInvalid)
			return
		}
		workspace, objectID, versionText := c.Param("workspace_id"), c.Param("object_id"), c.Param("version")
		version, versionErr := strconv.ParseInt(versionText, 10, 64)
		if !ownershipID.MatchString(workspace) || !ownershipID.MatchString(objectID) || versionErr != nil || version < 1 || version > maxOwnershipRevision || strconv.FormatInt(version, 10) != versionText || validateObjectRange(c.GetHeader("Range")) != nil {
			respond(c, nil, ErrInvalid)
			return
		}
		client, ok := a.Service.Ownership.(*GISClient)
		if !ok || client == nil {
			respond(c, nil, ErrDependencyUnavailable)
			return
		}
		actor := c.GetString("agriculture.actor")
		var scope OwnershipContext
		err := a.Service.withWorkspace(c.Request.Context(), actor, workspace, false, func(_ *gorm.DB, w workspaceContext) error {
			scope = OwnershipContext{TenantID: w.Tenant.ID, WorkspaceID: w.Workspace.ID}
			return nil
		})
		if err != nil {
			respond(c, nil, err)
			return
		}
		ref, err := client.resolveObject(c.Request.Context(), scope, objectID, version)
		if err != nil {
			respond(c, nil, err)
			return
		}
		action, _, err := objectAction(ref.Resource.Kind)
		if err != nil {
			respond(c, nil, err)
			return
		}
		decision, err := a.Service.Authorize(c.Request.Context(), actor, workspace, action, []ResourceRef{ref.Resource})
		if err == nil && (len(decision.ResourceVersions) != 1 || decision.ResourceVersions[0].Revision != ref.ResourceRevision) {
			err = ErrConflict
		}
		if err != nil {
			respond(c, nil, err)
			return
		}
		// Retain the permit through the client write, not just upstream reading:
		// slow readers must not accumulate unbounded full-object buffers.
		select {
		case objectReadSlots <- struct{}{}:
			defer func() { <-objectReadSlots }()
		case <-c.Request.Context().Done():
			respond(c, nil, ErrDependencyUnavailable)
			return
		}
		data, err := client.readObject(c.Request.Context(), a.Service, actor, decision, ref, c.Request.Method, c.GetHeader("Range"))
		if err != nil {
			respond(c, nil, err)
			return
		}
		c.Header("Content-Type", data.mime)
		if strings.HasPrefix(data.mime, "image/") {
			c.Header("Content-Disposition", "inline")
		} else {
			c.Header("Content-Disposition", `attachment; filename="object.bin"`)
		}
		c.Header("Content-Length", strconv.FormatInt(data.length, 10))
		c.Header("Accept-Ranges", "bytes")
		if data.contentRange != "" {
			c.Header("Content-Range", data.contentRange)
		}
		c.Status(data.status)
		if c.Request.Method != "HEAD" {
			_, _ = c.Writer.Write(data.body)
		}
	}
	g.GET("/:object_id/versions/:version", handler)
	g.HEAD("/:object_id/versions/:version", handler)
}
