package agriculture

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestServiceAssertionIsDirectionScopeAndPayloadBound(t *testing.T) {
	now := time.Now().UTC()
	platform, _ := identityTestKeys(t, now, 0x41)
	gis, _ := identityTestKeys(t, now, 0x42)
	scope := OwnershipContext{TenantID: "tenant_a", WorkspaceID: "workspace_a"}
	body := []byte(`{"tenant_id":"tenant_a","workspace_id":"workspace_a","resources":[{"kind":"farm","id":"farm_a"}]}`)
	token, canonical, err := issueServiceAssertion(platform, "ownership.resolve", scope, body, now)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := verifyServiceAssertion(platform, "ownership.resolve", token, "POST", ownershipResolvePath, canonical, now)
	if err != nil {
		t.Fatal(err)
	}
	if claims.TenantID != scope.TenantID || claims.Request.OperationID != "" || claims.Capability != "ownership.resolve" {
		t.Fatal("service assertion fields differ")
	}
	if _, err := verifyServiceAssertion(gis, "ownership.resolve", token, "POST", ownershipResolvePath, canonical, now); err == nil {
		t.Fatal("direction key interchange accepted")
	}
	if _, err := verifyServiceAssertion(platform, "authority.check", token, "POST", authorityCheckPath, canonical, now); err == nil {
		t.Fatal("ownership capability used for current authority")
	}
	if _, err := verifyServiceAssertion(platform, "ownership.resolve", token, "POST", ownershipResolvePath, []byte(`{}`), now); err == nil {
		t.Fatal("payload substituted")
	}
	if _, err := verifyDelegation(platform, token, "POST", ownershipResolvePath, canonical, now); err == nil {
		t.Fatal("service assertion accepted as user delegation")
	}
	if _, _, err := issueServiceAssertion(platform, "arbitrary.admin", scope, body, now); err == nil {
		t.Fatal("unknown service capability minted")
	}
}

func TestInternalAuthorityChecksLiveDatabaseWithoutGISRecursion(t *testing.T) {
	s, decision, platform, _ := delegationFixture(t)
	gis, _ := identityTestKeys(t, s.now(), 0x42)
	s.Ownership = &fixtureOwnershipResolver{err: errors.New("must not invoke ownership in callback")}
	gin.SetMode(gin.TestMode)
	root := gin.New()
	s.RegisterInternalAuthority(root, gis)
	requestBody := authorityCheckBody{ActorUserID: "bob", TenantID: decision.TenantID, WorkspaceID: decision.WorkspaceID, AuthorizationVersion: decision.AuthorizationVersion}
	call := func(body authorityCheckBody, keys *ServiceKeyring, path string, mutate func(string) string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		token, canonical, err := issueServiceAssertion(keys, "authority.check", OwnershipContext{TenantID: body.TenantID, WorkspaceID: body.WorkspaceID}, raw, s.now())
		if err != nil {
			t.Fatal(err)
		}
		if mutate != nil {
			token = mutate(token)
		}
		req := httptest.NewRequest("POST", path, strings.NewReader(string(canonical)))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		root.ServeHTTP(w, req)
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("authority response cacheable")
		}
		if strings.Contains(w.Body.String(), "must not invoke") || strings.Contains(w.Body.String(), token) {
			t.Fatal("service secret/error leaked")
		}
		return w
	}
	if w := call(requestBody, gis, authorityCheckPath, nil); w.Code != 200 {
		t.Fatalf("live callback rejected: %d %s", w.Code, w.Body.String())
	} else {
		var response struct {
			Active  bool  `json:"active"`
			Version int64 `json:"authorization_version"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || !response.Active || response.Version != decision.AuthorizationVersion {
			t.Fatal("invalid authority response")
		}
	}
	if w := call(requestBody, platform, authorityCheckPath, nil); w.Code != 401 {
		t.Fatal("platform key impersonated GIS callback")
	}
	if w := call(requestBody, gis, authorityCheckPath+"?tenant_id=other", nil); w.Code != 400 {
		t.Fatal("query ambiguity accepted")
	}
	if w := call(requestBody, gis, authorityCheckPath, func(token string) string { return token + "=" }); w.Code != 401 {
		t.Fatal("malformed bearer accepted")
	}
	stale := requestBody
	stale.AuthorizationVersion++
	if w := call(stale, gis, authorityCheckPath, nil); w.Code != 403 {
		t.Fatal("future epoch accepted")
	}
	status := "suspended"
	if err := s.ChangeMember(context.Background(), "owner", decision.WorkspaceID, "bob", nil, &status); err != nil {
		t.Fatal(err)
	}
	if w := call(requestBody, gis, authorityCheckPath, nil); w.Code != 403 {
		t.Fatal("withdrawn actor accepted")
	}
	conn, err := s.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if w := call(requestBody, gis, authorityCheckPath, nil); w.Code != 503 {
		t.Fatal("database failure did not fail closed")
	}
}

func TestInternalAuthorityRequiresServiceCredentialAndSignedScope(t *testing.T) {
	s, decision, _, _ := delegationFixture(t)
	gis, _ := identityTestKeys(t, s.now(), 0x42)
	gin.SetMode(gin.TestMode)
	root := gin.New()
	s.RegisterInternalAuthority(root, gis)
	input := authorityCheckBody{ActorUserID: "bob", TenantID: decision.TenantID, WorkspaceID: decision.WorkspaceID, AuthorizationVersion: decision.AuthorizationVersion}
	raw, _ := json.Marshal(input)
	token, body, err := issueServiceAssertion(gis, "authority.check", OwnershipContext{TenantID: "different_tenant", WorkspaceID: input.WorkspaceID}, raw, s.now())
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range []string{"", "Bearer " + token} {
		req := httptest.NewRequest("POST", authorityCheckPath, strings.NewReader(string(body)))
		if credential != "" {
			req.Header.Set("Authorization", credential)
		}
		w := httptest.NewRecorder()
		root.ServeHTTP(w, req)
		want := 401
		if credential != "" {
			want = 400
		}
		if w.Code != want {
			t.Fatalf("scope/credential mismatch: %d", w.Code)
		}
	}
	missing := gin.New()
	s.RegisterInternalAuthority(missing, nil)
	req := httptest.NewRequest("POST", authorityCheckPath, strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	missing.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatal("missing service configuration accepted")
	}
}
