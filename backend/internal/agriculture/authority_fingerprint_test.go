package agriculture

import (
	"context"
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/model"
)

func fingerprintFor(t *testing.T, s Service, actor, workspace string) string {
	t.Helper()
	rows, err := s.ListWorkspaces(context.Background(), actor)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == workspace {
			if len(row.AuthorityFingerprint) != 64 {
				t.Fatal("missing fingerprint")
			}
			return row.AuthorityFingerprint
		}
	}
	t.Fatal("workspace missing")
	return ""
}

func TestAuthorityFingerprintIgnoresAuditAndOtherActors(t *testing.T) {
	s, org := fixture(t)
	ctx := context.Background()
	before := fingerprintFor(t, s, "owner", org.Workspace.ID)
	invite(t, s, org, "bob", "viewer")
	if after := fingerprintFor(t, s, "owner", org.Workspace.ID); after != before {
		t.Fatal("invite audit invalidated unchanged authority")
	}
	if err := s.DB.Model(&model.Tenant{}).Where("id=?", org.Tenant.ID).Update("name", "display-only rename").Error; err != nil {
		t.Fatal(err)
	}
	join(t, s, org, "carol", "viewer")
	if after := fingerprintFor(t, s, "owner", org.Workspace.ID); after != before {
		t.Fatal("display/other member contaminated authority")
	}
	roles := []string{"editor"}
	if err := s.ChangeMember(ctx, "owner", org.Workspace.ID, "carol", &roles, nil); err != nil {
		t.Fatal(err)
	}
	if after := fingerprintFor(t, s, "owner", org.Workspace.ID); after != before {
		t.Fatal("other actor role contaminated authority")
	}
	if err := s.ChangeMember(ctx, "owner", org.Workspace.ID, "owner", &roles, nil); err != nil {
		t.Fatal(err)
	}
	changed := fingerprintFor(t, s, "owner", org.Workspace.ID)
	if changed == before {
		t.Fatal("own role change invisible")
	}
	if err := s.DB.Model(&model.Tenant{}).Where("id=?", org.Tenant.ID).Update("owner_user_id", "carol").Error; err != nil {
		t.Fatal(err)
	}
	if after := fingerprintFor(t, s, "owner", org.Workspace.ID); after == changed {
		t.Fatal("ownership change invisible")
	}
}

func TestAuthorityFingerprintGrantsExpiryReceiptAndStableOrdering(t *testing.T) {
	s, org, _ := authorizationFixture(t)
	ctx := context.Background()
	join(t, s, org, "bob", "viewer")
	baseline := fingerprintFor(t, s, "bob", org.Workspace.ID)
	expires := s.now().Add(time.Hour)
	row, err := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ResourceRef{"farm", "farm_a"}, []string{"agri.parcel.read", "agri.farm.read"}, &expires, revisionPointer(0))
	if err != nil {
		t.Fatal(err)
	}
	granted := fingerprintFor(t, s, "bob", org.Workspace.ID)
	if granted == baseline {
		t.Fatal("grant invisible")
	}
	if err = s.DB.Model(&row).Updates(map[string]any{"capabilities": `[ "agri.farm.read", "agri.parcel.read" ]`, "auth_version": 99, "updated_at": s.now().Add(time.Second)}).Error; err != nil {
		t.Fatal(err)
	}
	if after := fingerprintFor(t, s, "bob", org.Workspace.ID); after != granted {
		t.Fatal("cap order/metadata changed semantic fingerprint")
	}
	if err = s.DB.Model(&row).Update("receipt_hash", strings.Repeat("b", 64)).Error; err != nil {
		t.Fatal(err)
	}
	receipt := fingerprintFor(t, s, "bob", org.Workspace.ID)
	if receipt == granted {
		t.Fatal("receipt change invisible")
	}
	if err = s.DB.Model(&row).Update("receipt_revision", 2).Error; err != nil {
		t.Fatal(err)
	}
	if after := fingerprintFor(t, s, "bob", org.Workspace.ID); after == receipt {
		t.Fatal("receipt revision change invisible")
	}
	if err = s.DB.Model(&row).Update("state", "revoked").Error; err != nil {
		t.Fatal(err)
	}
	if after := fingerprintFor(t, s, "bob", org.Workspace.ID); after != baseline {
		t.Fatal("revoked grant retained")
	}
	if err = s.DB.Model(&row).Update("state", "active").Error; err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return expires }
	if after := fingerprintFor(t, s, "bob", org.Workspace.ID); after != baseline {
		t.Fatal("expired grant retained at boundary")
	}
}

func TestAuthorityFingerprintScopeAndCorruptCapabilitiesFailClosed(t *testing.T) {
	s, org := fixture(t)
	before := fingerprintFor(t, s, "owner", org.Workspace.ID)
	row := model.AgricultureResourceBinding{ID: "fixture_binding", TenantID: org.Tenant.ID, WorkspaceID: "other_workspace", SubjectType: "user", SubjectID: "owner", State: "active", ResourceKind: "farm", ResourceID: "fixture_farm", Capabilities: `["invalid.capability"]`}
	if err := s.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if after := fingerprintFor(t, s, "owner", org.Workspace.ID); after != before {
		t.Fatal("other workspace contaminated fingerprint")
	}
	if err := s.DB.Model(&row).Updates(map[string]any{"workspace_id": org.Workspace.ID, "subject_id": "bob"}).Error; err != nil {
		t.Fatal(err)
	}
	if after := fingerprintFor(t, s, "owner", org.Workspace.ID); after != before {
		t.Fatal("other actor contaminated fingerprint")
	}
	if err := s.DB.Model(&row).Update("subject_id", "owner").Error; err != nil {
		t.Fatal(err)
	}
	for _, caps := range []string{`["invalid.capability"]`, `["agri.farm.read","agri.farm.read"]`, `{}`, `not-json`} {
		if err := s.DB.Model(&row).Update("capabilities", caps).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := s.ListWorkspaces(context.Background(), "owner"); err == nil {
			t.Fatal("invalid capability JSON concealed")
		}
	}
}

func TestAuthorityFingerprintBindingRowOrderStable(t *testing.T) {
	s, org := fixture(t)
	rows := []model.AgricultureResourceBinding{
		{ID: "order_a", TenantID: org.Tenant.ID, WorkspaceID: org.Workspace.ID, SubjectType: "user", SubjectID: "owner", State: "active", ResourceKind: "farm", ResourceID: "farm_z", Capabilities: `["agri.farm.read"]`},
		{ID: "order_b", TenantID: org.Tenant.ID, WorkspaceID: org.Workspace.ID, SubjectType: "user", SubjectID: "owner", State: "active", ResourceKind: "farm", ResourceID: "farm_a", Capabilities: `["agri.farm.read"]`},
	}
	for i := range rows {
		if err := s.DB.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	before := fingerprintFor(t, s, "owner", org.Workspace.ID)
	if err := s.DB.Where("id IN ?", []string{"order_a", "order_b"}).Delete(&model.AgricultureResourceBinding{}).Error; err != nil {
		t.Fatal(err)
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if err := s.DB.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if after := fingerprintFor(t, s, "owner", org.Workspace.ID); after != before {
		t.Fatal("database insertion order changed authority fingerprint")
	}
	if err := s.DB.Migrator().DropTable(&model.AgricultureResourceBinding{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListWorkspaces(context.Background(), "owner"); err == nil {
		t.Fatal("binding query failure hidden as empty grants")
	}
}
