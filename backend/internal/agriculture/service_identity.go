package agriculture

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"yingce/backend/internal/flow"
)

const ownershipResolvePath = "/internal/agri/v1/ownership/resolve"
const authorityCheckPath = "/internal/agri/v1/authority/check"

type serviceAssertion struct {
	SchemaVersion string          `json:"schema_version"`
	Issuer        string          `json:"issuer"`
	Audience      string          `json:"audience"`
	Subject       string          `json:"subject"`
	Capability    string          `json:"capability"`
	TenantID      string          `json:"tenant_id"`
	WorkspaceID   string          `json:"workspace_id"`
	IssuedAt      string          `json:"issued_at"`
	ExpiresAt     string          `json:"expires_at"`
	JTI           string          `json:"jti"`
	TraceID       string          `json:"trace_id"`
	RequestID     string          `json:"request_id"`
	Request       identityRequest `json:"request"`
}

func serviceDirection(capability string) (issuer, audience, path string, err error) {
	switch capability {
	case "ownership.resolve":
		return "flow-platform", "agri-gis", ownershipResolvePath, nil
	case "authority.check":
		return "agri-gis", "flow-platform", authorityCheckPath, nil
	default:
		return "", "", "", ErrServiceIdentity
	}
}

func issueServiceAssertion(keys *ServiceKeyring, capability string, scope OwnershipContext, body []byte, now time.Time) (string, []byte, error) {
	issuer, audience, path, err := serviceDirection(capability)
	if err != nil {
		return "", nil, err
	}
	if !ownershipID.MatchString(scope.TenantID) || !ownershipID.MatchString(scope.WorkspaceID) {
		return "", nil, ErrServiceIdentity
	}
	request, canonical, err := bindIdentityRequest("POST", path, body, "", true)
	if err != nil {
		return "", nil, err
	}
	now = now.Truncate(time.Second)
	expires := now.Add(30 * time.Second)
	claims := serviceAssertion{SchemaVersion: "1.0", Issuer: issuer, Audience: audience, Subject: issuer, Capability: capability, TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID, IssuedAt: now.Format("2006-01-02T15:04:05Z"), ExpiresAt: expires.Format("2006-01-02T15:04:05Z"), JTI: flow.NewID("jti_"), TraceID: flow.NewID("trace_"), RequestID: flow.NewID("req_"), Request: request}
	token, err := keys.sign(serviceAssertionType, claims, now, expires)
	return token, canonical, err
}

func verifyServiceAssertion(keys *ServiceKeyring, capability, token, method, path string, body []byte, now time.Time) (serviceAssertion, error) {
	var claims serviceAssertion
	issuer, audience, wantPath, err := serviceDirection(capability)
	if err != nil {
		return claims, err
	}
	key, err := keys.verify(token, serviceAssertionType, now, &claims)
	if err != nil {
		return serviceAssertion{}, err
	}
	if claims.SchemaVersion != "1.0" || claims.Issuer != issuer || claims.Subject != issuer || claims.Audience != audience || claims.Capability != capability || !ownershipID.MatchString(claims.TenantID) || !ownershipID.MatchString(claims.WorkspaceID) || !ownershipID.MatchString(claims.JTI) || !ownershipID.MatchString(claims.TraceID) || !ownershipID.MatchString(claims.RequestID) || method != "POST" || path != wantPath || validateIdentityTimes(claims.IssuedAt, claims.ExpiresAt, now, key) != nil {
		return serviceAssertion{}, ErrServiceIdentity
	}
	bound, _, err := bindIdentityRequest(method, path, body, "", true)
	if err != nil || bound != claims.Request {
		return serviceAssertion{}, ErrServiceIdentity
	}
	return claims, nil
}

type authorityCheckBody struct {
	ActorUserID          string `json:"actor_user_id"`
	TenantID             string `json:"tenant_id"`
	WorkspaceID          string `json:"workspace_id"`
	AuthorizationVersion int64  `json:"authorization_version"`
}

// RegisterInternalAuthority is mounted on the actual root router, never a
// public session API group. It authenticates only the independently keyed GIS
// service. Current DB state is checked without invoking GIS or Authorize again.
func (s Service) RegisterInternalAuthority(root gin.IRouter, gisKeys *ServiceKeyring) {
	root.POST(authorityCheckPath, func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		fail := func(status int, code string) { c.JSON(status, gin.H{"error": gin.H{"code": code}}) }
		if gisKeys == nil {
			fail(503, "DEPENDENCY_UNAVAILABLE")
			return
		}
		if c.Request.URL.RawQuery != "" || c.Request.URL.ForceQuery || c.Request.URL.RawPath != "" || c.Request.RequestURI != authorityCheckPath {
			fail(400, "INVALID_ARGUMENT")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
		if err != nil {
			fail(400, "INVALID_ARGUMENT")
			return
		}
		headers := c.Request.Header.Values("Authorization")
		if len(headers) != 1 || !strings.HasPrefix(headers[0], "Bearer ") {
			fail(401, "UNAUTHENTICATED")
			return
		}
		claims, err := verifyServiceAssertion(gisKeys, "authority.check", strings.TrimPrefix(headers[0], "Bearer "), c.Request.Method, c.Request.URL.Path, body, s.now())
		if err != nil {
			fail(401, "UNAUTHENTICATED")
			return
		}
		var input authorityCheckBody
		if strictIdentityJSON(body, &input, 1<<20) != nil || !identityActorID.MatchString(input.ActorUserID) || input.TenantID != claims.TenantID || input.WorkspaceID != claims.WorkspaceID || input.AuthorizationVersion < 1 || input.AuthorizationVersion > maximumAuthorizationVersion {
			fail(400, "INVALID_ARGUMENT")
			return
		}
		err = s.withWorkspace(c.Request.Context(), input.ActorUserID, input.WorkspaceID, false, func(tx *gorm.DB, scope workspaceContext) error {
			if scope.Tenant.ID != claims.TenantID {
				return ErrForbidden
			}
			version, err := currentAuthorizationVersion(tx, scope.Tenant.ID)
			if err != nil {
				return err
			}
			if version != input.AuthorizationVersion {
				return ErrConflict
			}
			return nil
		})
		if err != nil {
			if err == ErrNotFound || err == ErrForbidden || err == ErrConflict {
				fail(403, "CAPABILITY_DENIED")
			} else {
				fail(503, "DEPENDENCY_UNAVAILABLE")
			}
			return
		}
		c.JSON(200, gin.H{"active": true, "authorization_version": input.AuthorizationVersion})
	})
}

func marshalIdentityBody(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalid
	}
	return raw, nil
}
