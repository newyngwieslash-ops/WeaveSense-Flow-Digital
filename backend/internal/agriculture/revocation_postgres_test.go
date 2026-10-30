package agriculture

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Real PostgreSQL, runtime roles, services and signed callback handler; GIS
// ownership and signing material are explicit fixtures, not live farm evidence.
func TestAgriculturePostgresRoleDowngradeRevokesPendingDelegation(t *testing.T) {
	s, _, owner, actor := postgresFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	s.Now = func() time.Time { return now }
	org, err := s.CreateOrganization(ctx, owner, "Pending operation revocation fixture", "pg-revocation-001")
	if err != nil {
		t.Fatal("fixture organization creation failed")
	}
	ownerFingerprint := fingerprintFor(t, s, owner, org.Workspace.ID)
	invite, err := s.CreateInvite(ctx, owner, org.Workspace.ID, actor+"@fixture.invalid", "editor")
	if err != nil {
		t.Fatal("fixture editor invitation failed")
	}
	if fingerprintFor(t, s, owner, org.Workspace.ID) != ownerFingerprint {
		t.Fatal("invite audit changed unchanged owner's authority fingerprint")
	}
	if _, err := s.AcceptInvite(ctx, actor, invite.Token); err != nil {
		t.Fatal("fixture editor admission failed")
	}
	ref := ResourceRef{"parcel", "pending_parcel_fixture"}
	s.Ownership = &fixtureOwnershipResolver{rows: map[ResourceRef]ResourceOwnership{ref: {
		Resource: ref, TenantID: org.Tenant.ID, WorkspaceID: org.Workspace.ID,
		FarmIDs: []string{"pending_farm_fixture"}, Revision: 1, Status: "active",
		ReceiptID: "fixture_pending_receipt", IssuedAt: now, ExpiresAt: now.Add(time.Minute),
	}}}
	if _, err := s.GrantResourceBinding(ctx, owner, org.Workspace.ID, actor, ref,
		[]string{"agri.parcel.read", "agri.parcel.update"}, nil, revisionPointer(0)); err != nil {
		t.Fatal("fixture explicit parcel update grant failed")
	}
	decision, err := s.Authorize(ctx, actor, org.Workspace.ID, "agri.parcel.update", []ResourceRef{ref})
	if err != nil {
		t.Fatal("editor update authorization failed")
	}
	platformKeys, _ := identityTestKeys(t, now, 0x61)
	gisKeys, _ := identityTestKeys(t, now, 0x62)
	path := "/internal/agri/v1/delegation/check"
	body := []byte(`{"resources":[{"kind":"parcel","id":"pending_parcel_fixture"}]}`)
	ticket, canonical, err := s.issueDelegation(ctx, platformKeys, actor, decision, "POST", path, body, "pending_operation_fixture")
	if err != nil {
		t.Fatal("initial pending operation ticket issuance failed")
	}
	claims, err := verifyDelegation(platformKeys, ticket, "POST", path, canonical, now)
	if err != nil || claims.AuthorizationVersion != decision.AuthorizationVersion {
		t.Fatal("initial signed ticket failed verification")
	}
	callback := func(service Service, version int64) int {
		t.Helper()
		gin.SetMode(gin.TestMode)
		router := gin.New()
		service.RegisterInternalAuthority(router, gisKeys)
		raw, err := json.Marshal(authorityCheckBody{ActorUserID: actor, TenantID: org.Tenant.ID,
			WorkspaceID: org.Workspace.ID, AuthorizationVersion: version})
		if err != nil {
			t.Fatal("callback fixture encoding failed")
		}
		token, payload, err := issueServiceAssertion(gisKeys, "authority.check",
			OwnershipContext{TenantID: org.Tenant.ID, WorkspaceID: org.Workspace.ID}, raw, now)
		if err != nil {
			t.Fatal("callback fixture signing failed")
		}
		request := httptest.NewRequest("POST", authorityCheckPath, strings.NewReader(string(payload)))
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("authority revocation response was cacheable")
		}
		return response.Code
	}
	if callback(s, claims.AuthorizationVersion) != 200 {
		t.Fatal("initial current authority denied")
	}
	viewer := []string{"viewer"}
	editorFingerprint := fingerprintFor(t, s, actor, org.Workspace.ID)
	if err := s.ChangeMember(ctx, owner, org.Workspace.ID, actor, &viewer, nil); err != nil {
		t.Fatal("editor downgrade failed")
	}
	if fingerprintFor(t, s, actor, org.Workspace.ID) == editorFingerprint {
		t.Fatal("persisted role downgrade did not change authority fingerprint")
	}
	if _, err := s.Reauthorize(ctx, actor, decision); !errors.Is(err, ErrForbidden) {
		t.Fatal("old editor decision retained write capability after downgrade")
	}
	if _, _, err := s.issueDelegation(ctx, platformKeys, actor, decision, "POST", path, body, "pending_operation_fixture"); err == nil {
		t.Fatal("downgraded actor reissued a pending privileged operation")
	}
	// Cryptographic validity is not authority. No time advancement is needed:
	// the still-unexpired ticket must be denied by the mandatory live callback.
	if _, err := verifyDelegation(platformKeys, ticket, "POST", path, canonical, now); err != nil {
		t.Fatal("fixture ticket unexpectedly expired; downgrade assertion would be vacuous")
	}
	if callback(s, claims.AuthorizationVersion) != 403 {
		t.Fatal("old ticket epoch survived real PostgreSQL downgrade")
	}
	// A new pool and Service discard process-local state, proving the persisted
	// authority boundary. This is not a full OS-process/cookie restart test.
	db, err := gorm.Open(postgres.Open(os.Getenv("AGRI_TEST_PG_RUNTIME_DSN")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("fresh runtime pool failed")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal("fresh runtime connection unavailable")
	}
	defer pool.Close()
	fresh := Service{DB: db, Now: s.Now, Ownership: s.Ownership}
	if callback(fresh, claims.AuthorizationVersion) != 403 {
		t.Fatal("new service instance forgot persisted revocation")
	}
	if _, err := fresh.Authorize(ctx, actor, org.Workspace.ID, "agri.parcel.update", []ResourceRef{ref}); !errors.Is(err, ErrForbidden) {
		t.Fatal("new service instance reused old editor authority")
	}
	editor := []string{"editor"}
	if err := s.ChangeMember(ctx, owner, org.Workspace.ID, actor, &editor, nil); err != nil {
		t.Fatal("explicit editor restoration failed")
	}
	newDecision, err := fresh.Authorize(ctx, actor, org.Workspace.ID, "agri.parcel.update", []ResourceRef{ref})
	if err != nil || newDecision.AuthorizationVersion <= decision.AuthorizationVersion {
		t.Fatal("explicit restoration failed to acquire a new authorization epoch")
	}
	if callback(fresh, claims.AuthorizationVersion) != 403 || callback(fresh, newDecision.AuthorizationVersion) != 200 {
		t.Fatal("restored membership revived stale epoch or denied current epoch")
	}
	removed := "removed"
	if err := s.ChangeMember(ctx, owner, org.Workspace.ID, actor, nil, &removed); err != nil {
		t.Fatal("member removal failed")
	}
	if callback(fresh, newDecision.AuthorizationVersion) != 403 {
		t.Fatal("removed member retained signed callback authority")
	}
	if _, err := fresh.Reauthorize(ctx, actor, newDecision); !errors.Is(err, ErrNotFound) {
		t.Fatal("removed member retained pending operation authority")
	}
}
