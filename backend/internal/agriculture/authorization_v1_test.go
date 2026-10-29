package agriculture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"yingce/backend/internal/model"
)

func assertV1(t *testing.T, w *httptest.ResponseRecorder, status int, code string) json.RawMessage {
	t.Helper()
	var body struct {
		Data json.RawMessage `json:"data"`
		Meta struct {
			RequestID     string `json:"request_id"`
			SchemaVersion string `json:"schema_version"`
		} `json:"meta"`
		Error *struct {
			Code      string         `json:"code"`
			Message   string         `json:"message"`
			Retryable bool           `json:"retryable"`
			Details   map[string]any `json:"details"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != status {
		t.Fatalf("got %d %s, want %d", w.Code, w.Body.String(), status)
	}
	requestID := body.Meta.RequestID
	if status >= 400 {
		requestID = body.RequestID
		if body.Error == nil || body.Error.Code != code || body.Error.Message == "" || body.Error.Details == nil || len(body.Error.Details) != 0 || len(body.Data) != 0 {
			t.Fatalf("invalid error envelope: %s", w.Body.String())
		}
		if body.Error.Retryable != (status == 503) {
			t.Fatalf("wrong retryability: %s", w.Body.String())
		}
	} else if body.Error != nil || body.Meta.SchemaVersion != "1.0" || body.Data == nil {
		t.Fatalf("invalid success envelope: %s", w.Body.String())
	}
	if !strings.HasPrefix(requestID, "req_") || requestID != w.Header().Get("X-Request-ID") {
		t.Fatalf("server request ID absent/mismatched: %s", w.Body.String())
	}
	return body.Data
}

func TestAuthorizationV1CanonicalDTOAndBindingCRUD(t *testing.T) {
	s, org, _ := authorizationFixture(t)
	join(t, s, org, "bob", "viewer")
	base := "/api/agri/v1/workspaces/" + org.Workspace.ID
	body := `{"subject_user_id":"bob","resource":{"kind":"farm","id":"farm_a"},"capabilities":["agri.farm.read"],"expected_revision":0}`
	data := assertV1(t, authorizationHTTP(t, s, "owner", "POST", base+"/resource-bindings", body, ""), 200, "")
	var binding authorizationV1Binding
	if err := json.Unmarshal(data, &binding); err != nil {
		t.Fatal(err)
	}
	if binding.ID == "" || binding.SubjectUserID != "bob" || binding.TenantID != org.Tenant.ID || len(binding.Capabilities) != 1 {
		t.Fatalf("binding DTO: %s", data)
	}
	for _, forbidden := range []string{"receipt", "tenantId", "workspaceId", "subjectId", "authVersion"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("internal/legacy field %s leaked: %s", forbidden, data)
		}
	}
	data = assertV1(t, authorizationHTTP(t, s, "bob", "POST", base+"/authorization", `{"action":"agri.farm.read","resources":[{"kind":"farm","id":"farm_a"}]}`, ""), 200, "")
	var decision map[string]json.RawMessage
	if err := json.Unmarshal(data, &decision); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"actor_user_id", "tenant_id", "workspace_id", "action", "resource_scope", "resource_versions", "farm_ids", "authorization_version", "policy_version", "issued_at", "expires_at"} {
		if _, ok := decision[field]; !ok {
			t.Fatalf("missing %s: %s", field, data)
		}
	}
	if len(decision) != 11 || string(decision["actor_user_id"]) != `"bob"` {
		t.Fatalf("decision contract or actor changed: %s", data)
	}
	assertV1(t, authorizationHTTP(t, s, "owner", "GET", base+"/resource-bindings", "", ""), 200, "")
	assertV1(t, authorizationHTTP(t, s, "owner", "POST", base+"/resource-bindings/"+binding.ID+"/revoke", fmt.Sprintf(`{"expected_revision":%d}`, binding.AuthorizationVersion), ""), 200, "")
	var stored model.AgricultureResourceBinding
	if err := s.DB.First(&stored, "id=?", binding.ID).Error; err != nil || stored.State != "revoked" {
		t.Fatalf("canonical revoke not persisted: %+v %v", stored, err)
	}
}

func TestAuthorizationV1ErrorsAndIdentityFieldsFailClosed(t *testing.T) {
	s, org, _ := authorizationFixture(t)
	join(t, s, org, "bob", "viewer")
	row, err := s.GrantResourceBinding(context.Background(), "owner", org.Workspace.ID, "bob", ResourceRef{"farm", "farm_a"}, []string{"agri.farm.read"}, nil, revisionPointer(0))
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/agri/v1/workspaces/" + org.Workspace.ID
	read := `{"action":"agri.farm.read","resources":[{"kind":"farm","id":"farm_a"}]}`
	for _, tc := range []struct {
		actor, body, origin string
		status              int
		code                string
	}{
		{"", read, "", 401, "UNAUTHENTICATED"},
		{"bob", `{"action":"agri.farm.read","resources":[{"kind":"farm","id":"farm_b"}]}`, "", 404, "RESOURCE_NOT_FOUND"},
		{"bob", `{"action":"agri.parcel.update","resources":[{"kind":"parcel","id":"parcel_a"}]}`, "", 403, "CAPABILITY_DENIED"},
		{"bob", `{"action":"agri.asset_download.read","resources":[{"kind":"asset","id":"asset_a"}]}`, "", 403, "CAPABILITY_DENIED"},
		{"bob", `{"action":"agri.farm.destroy","resources":[{"kind":"farm","id":"farm_a"}]}`, "", 400, "INVALID_ARGUMENT"},
		{"bob", read, "https://attacker.invalid", 403, "CAPABILITY_DENIED"},
		{"bob", read + ` {}`, "", 400, "INVALID_ARGUMENT"},
	} {
		assertV1(t, authorizationHTTP(t, s, tc.actor, "POST", base+"/authorization", tc.body, tc.origin), tc.status, tc.code)
	}
	before := bindingEpoch(t, s, org.Tenant.ID)
	for _, field := range []string{"tenant_id", "tenantId", "user_id", "userId", "actor_user_id", "operator_code", "requester_code", "subjectId"} {
		t.Run(field, func(t *testing.T) {
			for _, endpoint := range []string{"authorization", "resource-bindings", "resource-bindings/" + row.ID + "/revoke"} {
				body := map[string]any{field: "owner"}
				switch endpoint {
				case "authorization":
					body["action"] = "agri.farm.read"
					body["resources"] = []ResourceRef{{"farm", "farm_a"}}
				case "resource-bindings":
					body["subject_user_id"] = "bob"
					body["resource"] = ResourceRef{"farm", "farm_b"}
					body["capabilities"] = []string{"agri.farm.read"}
				}
				raw, _ := json.Marshal(body)
				assertV1(t, authorizationHTTP(t, s, "owner", "POST", base+"/"+endpoint, string(raw), ""), 400, "INVALID_ARGUMENT")
			}
		})
	}
	if bindingEpoch(t, s, org.Tenant.ID) != before {
		t.Fatal("forged fields mutated authorization epoch")
	}
	var stored model.AgricultureResourceBinding
	if err := s.DB.First(&stored, "id=?", row.ID).Error; err != nil || stored.State != "active" {
		t.Fatalf("forged revoke had effect: %+v %v", stored, err)
	}
	s.Ownership = nil
	assertV1(t, authorizationHTTP(t, s, "bob", "POST", base+"/authorization", read, ""), 503, "DEPENDENCY_UNAVAILABLE")
}

func TestAuthorizationV1SharedMiddlewareInternalErrorsAndRequestID(t *testing.T) {
	s, _, _ := authorizationFixture(t)
	router := gin.New()
	API{Service: s, User: func(*gin.Context) (*model.User, error) { return nil, errors.New("private SQL path /secrets/token") }}.Register(router.Group("/api"))
	for _, path := range []string{"/api/agri/v1/workspaces/missing/resource-bindings", "/api/agriculture/workspaces"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Request-ID", "attacker-controlled")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "/secrets") || strings.Contains(w.Body.String(), "attacker-controlled") {
			t.Fatalf("internal/request metadata leaked: %s", w.Body.String())
		}
		if strings.Contains(path, "/agri/v1/") {
			assertV1(t, w, 500, "INTERNAL_ERROR")
		} else {
			assertAuthorizationHTTP(t, w, 500, "agriculture_unavailable")
		}
	}
}

func TestBindingHTTPExplicitRevisionOnBothAdapters(t *testing.T) {
	for _, canonical := range []bool{false, true} {
		name := "compatibility"
		if canonical {
			name = "canonical"
		}
		t.Run(name, func(t *testing.T) {
			s, org, _ := authorizationFixture(t)
			join(t, s, org, "bob", "viewer")
			join(t, s, org, "carol", "tenant_admin")
			prefix, subjectKey, revisionKey := "/api/agriculture/workspaces/", "subjectId", "expectedRevision"
			if canonical {
				prefix, subjectKey, revisionKey = "/api/agri/v1/workspaces/", "subject_user_id", "expected_revision"
			}
			base := prefix + org.Workspace.ID + "/resource-bindings"
			assert := func(w *httptest.ResponseRecorder, status int, canonicalCode, legacyCode string) json.RawMessage {
				if canonical {
					return assertV1(t, w, status, canonicalCode)
				}
				return assertAuthorizationHTTP(t, w, status, legacyCode)
			}
			grantBody := func(revision any, include bool) string {
				body := map[string]any{subjectKey: "bob", "resource": ResourceRef{"farm", "farm_a"}, "capabilities": []string{"agri.farm.read"}}
				if include {
					body[revisionKey] = revision
				}
				raw, _ := json.Marshal(body)
				return string(raw)
			}
			before := bindingEpoch(t, s, org.Tenant.ID)
			for _, tc := range []struct {
				value        any
				include      bool
				status       int
				code, legacy string
			}{
				{nil, false, 428, "PRECONDITION_REQUIRED", "precondition_required"}, {nil, true, 428, "PRECONDITION_REQUIRED", "precondition_required"},
				{-1, true, 400, "INVALID_ARGUMENT", "invalid_request"}, {maximumAuthorizationVersion + 1, true, 400, "INVALID_ARGUMENT", "invalid_request"},
				{1.5, true, 400, "INVALID_ARGUMENT", "invalid_request"}, {"0", true, 400, "INVALID_ARGUMENT", "invalid_request"},
				{1, true, 409, "REVISION_CONFLICT", "state_conflict"},
			} {
				assert(authorizationHTTP(t, s, "owner", "POST", base, grantBody(tc.value, tc.include), ""), tc.status, tc.code, tc.legacy)
			}
			for _, header := range []string{"*", `"1"`, ""} {
				assert(authorizationHTTP(t, s, "owner", "POST", base, grantBody(0, true), "", map[string]string{"If-Match": header}), 400, "INVALID_ARGUMENT", "invalid_request")
			}
			if bindingEpoch(t, s, org.Tenant.ID) != before {
				t.Fatal("invalid preconditions changed epoch")
			}
			data := assert(authorizationHTTP(t, s, "owner", "POST", base, grantBody(0, true), ""), 200, "", "")
			var id string
			var initialRevision int64
			if canonical {
				var dto authorizationV1Binding
				if err := json.Unmarshal(data, &dto); err != nil {
					t.Fatal(err)
				}
				id, initialRevision = dto.ID, dto.AuthorizationVersion
			} else {
				var row model.AgricultureResourceBinding
				if err := json.Unmarshal(data, &row); err != nil {
					t.Fatal(err)
				}
				id, initialRevision = row.ID, row.AuthVersion
			}
			revokeURL := base + "/" + id + "/revoke"
			revokeBody := func(revision any) string {
				raw, _ := json.Marshal(map[string]any{revisionKey: revision})
				return string(raw)
			}
			assert(authorizationHTTP(t, s, "owner", "POST", revokeURL, "{}", ""), 428, "PRECONDITION_REQUIRED", "precondition_required")
			assert(authorizationHTTP(t, s, "owner", "POST", revokeURL, revokeBody(nil), ""), 428, "PRECONDITION_REQUIRED", "precondition_required")
			for _, value := range []any{0, -1, maximumAuthorizationVersion + 1, "1"} {
				assert(authorizationHTTP(t, s, "owner", "POST", revokeURL, revokeBody(value), ""), 400, "INVALID_ARGUMENT", "invalid_request")
			}
			assert(authorizationHTTP(t, s, "owner", "POST", revokeURL, revokeBody(initialRevision), "", map[string]string{"If-Match": `"conflicting"`}), 400, "INVALID_ARGUMENT", "invalid_request")
			assert(authorizationHTTP(t, s, "owner", "POST", base, grantBody(0, true), ""), 409, "REVISION_CONFLICT", "state_conflict")
			// A second administrator revokes the exact version the first observed.
			assert(authorizationHTTP(t, s, "carol", "POST", revokeURL, revokeBody(initialRevision), ""), 200, "", "")
			afterRevoke := bindingEpoch(t, s, org.Tenant.ID)
			for _, stale := range []int64{0, initialRevision} {
				assert(authorizationHTTP(t, s, "owner", "POST", base, grantBody(stale, true), ""), 409, "REVISION_CONFLICT", "state_conflict")
			}
			assert(authorizationHTTP(t, s, "owner", "POST", revokeURL, revokeBody(initialRevision), ""), 409, "REVISION_CONFLICT", "state_conflict")
			if bindingEpoch(t, s, org.Tenant.ID) != afterRevoke {
				t.Fatal("stale writes mutated epoch")
			}
			var revoked model.AgricultureResourceBinding
			if err := s.DB.First(&revoked, "id=?", id).Error; err != nil || revoked.State != "revoked" || revoked.Capabilities != "[]" {
				t.Fatalf("stale grant resurrected authority: %+v %v", revoked, err)
			}
			// Only an explicit read of the revoked version can support a deliberate regrant.
			assert(authorizationHTTP(t, s, "owner", "POST", base, grantBody(revoked.AuthVersion, true), ""), 200, "", "")
			assert(authorizationHTTP(t, s, "carol", "POST", revokeURL, revokeBody(revoked.AuthVersion), ""), 409, "REVISION_CONFLICT", "state_conflict")
			if _, err := s.Authorize(context.Background(), "bob", org.Workspace.ID, "agri.farm.read", []ResourceRef{{"farm", "farm_a"}}); err != nil {
				t.Fatalf("old revoke removed deliberate new grant: %v", err)
			}
		})
	}
}
