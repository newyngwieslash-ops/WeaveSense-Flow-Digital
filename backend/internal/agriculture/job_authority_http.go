package agriculture

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const jobAuthorityCheckPath = "/internal/agri/v1/jobs/authority/check"

type jobAuthorityCheckBody struct {
	Snapshot     JobAuthoritySnapshot `json:"snapshot"`
	SnapshotHash string               `json:"snapshot_hash"`
	Phase        string               `json:"phase"`
}

// This service-only callback consumes the GIS registry's persisted snapshot.
// It is not a public mint endpoint, queue claim, or substitute for GIS fencing.
func (s Service) RegisterInternalJobAuthority(root gin.IRouter, gisKeys *ServiceKeyring) {
	root.POST(jobAuthorityCheckPath, func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		fail := func(status int, code string) { c.JSON(status, gin.H{"error": gin.H{"code": code}}) }
		if gisKeys == nil {
			fail(503, "DEPENDENCY_UNAVAILABLE")
			return
		}
		if c.Request.URL.RawQuery != "" || c.Request.URL.ForceQuery || c.Request.URL.RawPath != "" || c.Request.RequestURI != jobAuthorityCheckPath {
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
		claims, err := verifyServiceAssertion(gisKeys, "job.authority.check", strings.TrimPrefix(headers[0], "Bearer "), c.Request.Method, c.Request.URL.Path, body, s.now())
		if err != nil {
			fail(401, "UNAUTHENTICATED")
			return
		}
		var input jobAuthorityCheckBody
		if strictIdentityJSON(body, &input, 1<<20) != nil || input.Snapshot.TenantID != claims.TenantID || input.Snapshot.WorkspaceID != claims.WorkspaceID || (input.Phase != "claim" && input.Phase != "read_input" && input.Phase != "publish") {
			fail(400, "INVALID_ARGUMENT")
			return
		}
		decision, err := s.CheckJobAuthority(c.Request.Context(), input.Snapshot, input.SnapshotHash)
		if err != nil {
			switch {
			case errors.Is(err, ErrInvalid):
				fail(400, "INVALID_ARGUMENT")
			case errors.Is(err, ErrForbidden), errors.Is(err, ErrNotFound), errors.Is(err, ErrConflict):
				fail(403, "CAPABILITY_DENIED")
			default:
				fail(503, "DEPENDENCY_UNAVAILABLE")
			}
			return
		}
		c.JSON(200, gin.H{"active": true, "snapshot_hash": input.SnapshotHash, "authorization_version": decision.AuthorizationVersion, "checked_at": s.now().UTC().Truncate(time.Second).Format(time.RFC3339), "valid_until": decision.ExpiresAt.UTC().Truncate(time.Second).Format(time.RFC3339)})
	})
}
