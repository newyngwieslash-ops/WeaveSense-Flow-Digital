package agriculture

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestJobAuthorityInternalCallbackRequiresIndependentIdentity(t *testing.T) {
	s, snapshot, _ := jobAuthorityFixture(t, nil)
	gis, _ := identityTestKeys(t, s.now(), 0x42)
	wrong, _ := identityTestKeys(t, s.now(), 0x43)
	router := gin.New()
	s.RegisterInternalJobAuthority(router, gis)
	digest, _ := snapshot.Digest()
	call := func(key *ServiceKeyring, body jobAuthorityCheckBody, mutate func(string) string, want int) {
		t.Helper()
		raw, _ := json.Marshal(body)
		token, canonical, err := issueServiceAssertion(key, "job.authority.check", OwnershipContext{snapshot.TenantID, snapshot.WorkspaceID}, raw, s.now())
		if err != nil {
			t.Fatal(err)
		}
		if mutate != nil {
			token = mutate(token)
		}
		req := httptest.NewRequest("POST", jobAuthorityCheckPath, strings.NewReader(string(canonical)))
		req.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		if out.Code != want || out.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("callback status=%d expected=%d", out.Code, want)
		}
		if strings.Contains(out.Body.String(), token) {
			t.Fatal("credential leaked")
		}
	}
	body := jobAuthorityCheckBody{snapshot, digest, "claim"}
	call(gis, body, nil, 200)
	call(wrong, body, nil, 401)
	call(gis, body, func(_ string) string { return "invalid" }, 401)
	body.Phase = "system"
	call(gis, body, nil, 400)
	body.Phase = "publish"
	body.Snapshot.TenantID = "other"
	call(gis, body, nil, 400)
	body.Snapshot = snapshot
	body.SnapshotHash = strings.Repeat("0", 64)
	call(gis, body, nil, 400)
	body.SnapshotHash = digest
	body.Snapshot.CreatedAt = s.now().Add(time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	body.SnapshotHash, _ = body.Snapshot.Digest()
	call(gis, body, nil, 400)
}
