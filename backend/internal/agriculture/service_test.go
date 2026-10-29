package agriculture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/kernel"
	"yingce/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func fixture(t *testing.T) (Service, OrganizationResult) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })
	err = db.AutoMigrate(&model.User{}, &model.Tenant{}, &model.Membership{}, &model.Workspace{}, &model.WorkspaceMembership{}, &model.FlowInvite{}, &model.FlowBillingAccount{}, &model.AgricultureRoleBinding{}, &model.AgricultureAuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, id := range []string{"owner", "bob", "carol"} {
		if err := db.Create(&model.User{ID: id, Username: id, DisplayName: id, Email: id + "@fixture.invalid", EmailVerifiedAt: &now, Status: model.UserStatusActive, Role: model.UserRoleUser}).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := Service{DB: db}
	org, err := s.CreateOrganization(context.Background(), "owner", "Fixture farm enterprise", "fixture-create-0001")
	if err != nil {
		t.Fatal(err)
	}
	return s, org
}
func invite(t *testing.T, s Service, org OrganizationResult, id, role string) InviteResult {
	t.Helper()
	v, e := s.CreateInvite(context.Background(), "owner", org.Workspace.ID, id+"@fixture.invalid", role)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func join(t *testing.T, s Service, org OrganizationResult, id, role string) AcceptResult {
	t.Helper()
	v := invite(t, s, org, id, role)
	out, e := s.AcceptInvite(context.Background(), id, v.Token)
	if e != nil {
		t.Fatal(e)
	}
	return out
}

func TestCreateOrganizationIdempotentAndNoImplicitContentRole(t *testing.T) {
	s, org := fixture(t)
	ctx := context.Background()
	again, err := s.CreateOrganization(ctx, "owner", "Fixture farm enterprise", "fixture-create-0001")
	if err != nil || again.Tenant.ID != org.Tenant.ID {
		t.Fatalf("idempotency: %v", err)
	}
	if _, err = s.CreateOrganization(ctx, "owner", "Changed name", "fixture-create-0001"); !errors.Is(err, ErrConflict) {
		t.Fatal("same key changed request accepted")
	}
	if len(org.Workspace.Roles) != 0 || !org.Workspace.CanManageMembers {
		t.Fatal("owner governance incorrectly grants business role")
	}
	if len("ba_"+org.Tenant.ID) > 40 {
		t.Fatal("generated tenant overflows existing PostgreSQL billing ID")
	}
	var count int64
	s.DB.Model(&model.FlowBillingAccount{}).Where("tenant_id=?", org.Tenant.ID).Count(&count)
	if count != 1 {
		t.Fatal("existing billing account not reused")
	}
	if _, err = flow.ResolveScope(s.DB, "owner", org.Workspace.ID); !errors.Is(err, flow.ErrForbidden) {
		t.Fatal("agriculture owner bypassed through legacy Intel scope")
	}
}

func checkOwnerBusinessRoleLifecycle(t *testing.T, s Service, actor string, org OrganizationResult) {
	t.Helper()
	ctx := context.Background()
	members, err := s.Members(ctx, actor, org.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range members.Members {
		if m.UserID != actor {
			continue
		}
		found = true
		if !m.IsOwner || !m.CanChangeRoles || len(m.Roles) != 0 || len(m.AssignableRoles) != 6 {
			t.Fatal("owner explicit role capabilities missing")
		}
		for _, role := range m.AssignableRoles {
			if governanceRole(role) {
				t.Fatal("owner self assignment exposes governance")
			}
		}
	}
	if !found {
		t.Fatal("owner member absent")
	}
	for _, forbidden := range []string{"tenant_admin", "billing_admin", "admin"} {
		roles := []string{forbidden}
		if err := s.ChangeMember(ctx, actor, org.Workspace.ID, actor, &roles, nil); !errors.Is(err, ErrForbidden) {
			t.Fatal("owner governance self assignment accepted")
		}
	}
	for _, status := range []string{"active", "suspended", "removed"} {
		if err := s.ChangeMember(ctx, actor, org.Workspace.ID, actor, nil, &status); !errors.Is(err, ErrForbidden) {
			t.Fatal("owner identity status changed")
		}
	}
	for _, roles := range [][]string{{"analyst", "editor"}, {}} {
		if err := s.ChangeMember(ctx, actor, org.Workspace.ID, actor, &roles, nil); err != nil {
			t.Fatal(err)
		}
		views, err := s.ListWorkspaces(ctx, actor)
		if err != nil {
			t.Fatal(err)
		}
		matched := false
		for _, view := range views {
			if view.ID == org.Workspace.ID {
				matched = true
				if len(view.Roles) != len(roles) || !view.CanManageMembers || !view.CanManageOrganization {
					t.Fatal("explicit business roles damaged owner governance")
				}
			}
		}
		if !matched {
			t.Fatal("owner workspace disappeared")
		}
	}
}

func TestOwnerExplicitBusinessRolesAndRevocation(t *testing.T) {
	s, org := fixture(t)
	checkOwnerBusinessRoleLifecycle(t, s, "owner", org)
	join(t, s, org, "bob", "tenant_admin")
	roles := []string{"editor"}
	if err := s.ChangeMember(context.Background(), "bob", org.Workspace.ID, "owner", &roles, nil); !errors.Is(err, ErrForbidden) {
		t.Fatal("administrator changed owner roles")
	}
	members, err := s.Members(context.Background(), "bob", org.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range members.Members {
		if member.IsOwner && (member.CanChangeRoles || len(member.AssignableRoles) != 0) {
			t.Fatal("administrator sees editable owner")
		}
	}
}

func TestMemberWithoutCurrentWorkspaceHasNoRoleEditingCapability(t *testing.T) {
	s, org := fixture(t)
	join(t, s, org, "bob", "viewer")
	if err := s.DB.Where("workspace_id=? AND user_id=?", org.Workspace.ID, "bob").Delete(&model.WorkspaceMembership{}).Error; err != nil {
		t.Fatal(err)
	}
	members, err := s.Members(context.Background(), "owner", org.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range members.Members {
		if member.UserID == "bob" && (member.CanChangeRoles || len(member.AssignableRoles) != 0) {
			t.Fatal("tenant-only member exposes workspace role editing")
		}
	}
	roles := []string{"editor"}
	if err := s.ChangeMember(context.Background(), "owner", org.Workspace.ID, "bob", &roles, nil); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing workspace membership accepted")
	}
}

func TestOwnerBusinessRoleChangeRollsBackWithAuditFailure(t *testing.T) {
	s, org := fixture(t)
	ctx := context.Background()
	roles := []string{"editor"}
	if err := s.ChangeMember(ctx, "owner", org.Workspace.ID, "owner", &roles, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("CREATE TRIGGER reject_role_audit BEFORE INSERT ON flow_agriculture_audit_events BEGIN SELECT RAISE(ABORT,'fixture injected audit failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	empty := []string{}
	if err := s.ChangeMember(ctx, "owner", org.Workspace.ID, "owner", &empty, nil); err == nil {
		t.Fatal("failed audit returned successful role revocation")
	}
	views, err := s.ListWorkspaces(ctx, "owner")
	if err != nil || len(views) != 1 || len(views[0].Roles) != 1 || views[0].Roles[0] != "editor" || !views[0].CanManageOrganization {
		t.Fatal("audit failure partially changed owner roles")
	}
}

func TestCanonicalRolesAndLegacyInviteCannotConsumeProduct(t *testing.T) {
	for _, role := range AssignableRoles {
		t.Run(role, func(t *testing.T) {
			s, org := fixture(t)
			v := invite(t, s, org, "bob", role)
			if _, err := flow.AcceptInvite(s.DB, "bob", v.Token); !errors.Is(err, flow.ErrInviteInvalid) {
				t.Fatal("legacy admission consumed agricultural invite")
			}
			out, err := s.AcceptInvite(context.Background(), "bob", v.Token)
			if err != nil || len(out.Roles) != 1 || out.Roles[0] != role {
				t.Fatalf("role binding: %+v %v", out, err)
			}
			var binding model.AgricultureRoleBinding
			if err := s.DB.Where("user_id=?", "bob").First(&binding).Error; err != nil {
				t.Fatal(err)
			}
			if (binding.WorkspaceID == "") != governanceRole(role) {
				t.Fatal("governance/business binding scope mixed")
			}
			if _, err = flow.ResolveScope(s.DB, "bob", org.Workspace.ID); !errors.Is(err, flow.ErrForbidden) {
				t.Fatal("legacy content scope accessible")
			}
			if _, err = s.AcceptInvite(context.Background(), "bob", v.Token); !errors.Is(err, ErrInvitation) {
				t.Fatal("invite replay succeeded")
			}
		})
	}
}

func TestInviteNegativeConditionsHaveNoSideEffects(t *testing.T) {
	for _, kind := range []string{"wrong_recipient", "unverified", "disabled_user", "expired", "revoked", "disabled_tenant", "suspended_member", "removed_member", "revoked_inviter", "wrong_product"} {
		t.Run(kind, func(t *testing.T) {
			s, org := fixture(t)
			v := invite(t, s, org, "bob", "analyst")
			actor := "bob"
			switch kind {
			case "wrong_recipient":
				actor = "carol"
			case "unverified":
				s.DB.Model(&model.User{}).Where("id='bob'").Update("email_verified_at", nil)
			case "disabled_user":
				s.DB.Model(&model.User{}).Where("id='bob'").Update("status", model.UserStatusDisabled)
			case "expired":
				s.DB.Model(&model.FlowInvite{}).Where("id=?", v.Invite.ID).Update("expires_at", time.Now().Add(-time.Second))
			case "revoked":
				if err := s.RevokeInvite(context.Background(), "owner", org.Workspace.ID, v.Invite.ID); err != nil {
					t.Fatal(err)
				}
			case "disabled_tenant":
				s.DB.Model(&model.Tenant{}).Where("id=?", org.Tenant.ID).Update("status", "disabled")
			case "suspended_member", "removed_member":
				status := "suspended"
				if kind == "removed_member" {
					status = "removed"
				}
				if err := s.DB.Create(&model.Membership{ID: "blocked", TenantID: org.Tenant.ID, UserID: "bob", Role: "member", Status: status}).Error; err != nil {
					t.Fatal(err)
				}
			case "revoked_inviter":
				s.DB.Model(&model.Membership{}).Where("tenant_id=? AND user_id='owner'", org.Tenant.ID).Update("status", "suspended")
			case "wrong_product":
				s.DB.Model(&model.Workspace{}).Where("id=?", org.Workspace.ID).Update("product", "")
			}
			if _, err := s.AcceptInvite(context.Background(), actor, v.Token); err == nil {
				t.Fatal("invalid admission succeeded")
			}
			var stored model.FlowInvite
			s.DB.First(&stored, "id=?", v.Invite.ID)
			if stored.AcceptedAt != nil {
				t.Fatal("failed admission consumed invite")
			}
			var n int64
			s.DB.Model(&model.WorkspaceMembership{}).Where("workspace_id=? AND user_id='bob'", org.Workspace.ID).Count(&n)
			if n != 0 {
				t.Fatal("failed admission created workspace access")
			}
		})
	}
}

func TestExistingMemberInviteNeverEscalates(t *testing.T) {
	s, org := fixture(t)
	join(t, s, org, "bob", "viewer")
	v := invite(t, s, org, "bob", "tenant_admin")
	out, err := s.AcceptInvite(context.Background(), "bob", v.Token)
	if err != nil || len(out.Roles) != 1 || out.Roles[0] != "viewer" {
		t.Fatalf("existing member escalated: %+v %v", out, err)
	}
}

func TestMemberGovernanceAndRevocation(t *testing.T) {
	s, org := fixture(t)
	ctx := context.Background()
	join(t, s, org, "bob", "tenant_admin")
	join(t, s, org, "carol", "analyst")
	roles := []string{"billing_admin"}
	if err := s.ChangeMember(ctx, "bob", org.Workspace.ID, "carol", &roles, nil); !errors.Is(err, ErrForbidden) {
		t.Fatal("tenant admin granted governance")
	}
	status := "removed"
	if err := s.ChangeMember(ctx, "bob", org.Workspace.ID, "owner", nil, &status); !errors.Is(err, ErrForbidden) {
		t.Fatal("owner removed")
	}
	if err := s.ChangeMember(ctx, "bob", org.Workspace.ID, "bob", nil, &status); !errors.Is(err, ErrForbidden) {
		t.Fatal("self governance changed")
	}
	roles = []string{"editor"}
	if err := s.ChangeMember(ctx, "owner", org.Workspace.ID, "bob", &roles, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Members(ctx, "bob", org.Workspace.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("downgraded member retained management")
	}
	if err := s.ChangeMember(ctx, "owner", org.Workspace.ID, "carol", nil, &status); err != nil {
		t.Fatal(err)
	}
	views, err := s.ListWorkspaces(ctx, "carol")
	if err != nil || len(views) != 0 {
		t.Fatal("removed member retained discovery")
	}
	if err := flow.AuthorizeGrant(s.DB, "user:carol", org.Tenant.ID, org.Workspace.ID, "intel.documents.read"); !errors.Is(err, flow.ErrForbidden) {
		t.Fatal("removed background grant retained")
	}
	if _, err := s.Members(ctx, "carol", org.Workspace.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("removed member retained protected access")
	}
}

func TestTenantDisableRestoreAndCrossTenantIDs(t *testing.T) {
	s, org := fixture(t)
	ctx := context.Background()
	join(t, s, org, "bob", "viewer")
	other, err := s.CreateOrganization(ctx, "carol", "Other tenant", "fixture-other-0001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Members(ctx, "owner", other.Workspace.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross tenant resource revealed")
	}
	if err = s.ChangeOrganizationStatus(ctx, "bob", org.Workspace.ID, "disabled"); !errors.Is(err, ErrForbidden) {
		t.Fatal("viewer disabled tenant")
	}
	if err = s.ChangeOrganizationStatus(ctx, "owner", org.Workspace.ID, "disabled"); err != nil {
		t.Fatal(err)
	}
	views, err := s.ListWorkspaces(ctx, "bob")
	if err != nil || len(views) != 0 {
		t.Fatal("disabled tenant member still discovers workspace")
	}
	views, err = s.ListWorkspaces(ctx, "owner")
	if err != nil || len(views) != 1 || !views[0].CanManageOrganization || views[0].CanManageMembers {
		t.Fatal("owner restoration projection wrong")
	}
	if err = s.ChangeOrganizationStatus(ctx, "owner", org.Workspace.ID, "active"); err != nil {
		t.Fatal(err)
	}
	views, err = s.ListWorkspaces(ctx, "bob")
	if err != nil || len(views) != 1 {
		t.Fatal("explicit restore failed")
	}
}

func TestAuditFailureRollsBackOrganizationAndBilling(t *testing.T) {
	s, _ := fixture(t)
	if err := s.DB.Exec("CREATE TRIGGER reject_audit BEFORE INSERT ON flow_agriculture_audit_events BEGIN SELECT RAISE(ABORT,'fixture injected audit failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateOrganization(context.Background(), "owner", "Rollback target", "rollback-create-0001"); err == nil {
		t.Fatal("audit failure reported success")
	}
	var n int64
	s.DB.Model(&model.Tenant{}).Where("name=?", "Rollback target").Count(&n)
	if n != 0 {
		t.Fatal("partial organization committed")
	}
}

func TestRoleInputAliasesUnknownAndContentBoundaries(t *testing.T) {
	for input, want := range map[string]string{"admin": "tenant_admin", "field_worker": "field_operator"} {
		out, err := normalizeRoles([]string{input})
		if err != nil || out[0] != want {
			t.Fatal("alias mismatch")
		}
	}
	for _, roles := range [][]string{{}, {"owner"}, {"root"}, {"member"}, {"viewer", "viewer"}, {"admin", "tenant_admin"}} {
		if _, err := normalizeRoles(roles); err == nil {
			t.Fatal("invalid role input accepted")
		}
	}
}

func TestHTTPTrustedActorStrictInputAndRealFailures(t *testing.T) {
	s, org := fixture(t)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	API{Service: s, User: func(c *gin.Context) (*model.User, error) {
		switch c.GetHeader("X-Fixture-Actor") {
		case "failure":
			return nil, fmt.Errorf("private database cause must not leak")
		case "":
			return nil, kernel.Unauthorized("fixture absent")
		}
		return &model.User{ID: c.GetHeader("X-Fixture-Actor")}, nil
	}}.Register(router.Group("/api"))
	request := func(method, path, actor, body, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Fixture-Actor", actor)
		r.Header.Set("X-Idempotency-Key", "http-create-00001")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		actor string
		code  int
	}{{"", 401}, {"failure", 500}} {
		w := request("GET", "/api/agriculture/workspaces", tc.actor, "", "")
		if w.Code != tc.code || strings.Contains(w.Body.String(), "private database") {
			t.Fatalf("auth error fidelity: %d", w.Code)
		}
	}
	w := request("POST", "/api/agriculture/organizations", "owner", `{"name":"X","tenant_id":"forged","user_id":"carol"}`, "")
	if w.Code != 400 {
		t.Fatal("client authority fields accepted")
	}
	w = request("POST", "/api/agriculture/organizations", "owner", `{"name":"X"}`, "https://attacker.invalid")
	if w.Code != 403 {
		t.Fatal("cross origin write accepted")
	}
	w = request("GET", "/api/agriculture/workspaces/"+org.Workspace.ID+"/members", "carol", "", "")
	if w.Code != 404 {
		t.Fatal("foreign workspace response not opaque")
	}
	w = request("GET", "/api/agriculture/workspaces", "owner", "", "")
	var result struct {
		Data []WorkspaceView `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Data) != 1 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("actual API projection failed")
	}
}
