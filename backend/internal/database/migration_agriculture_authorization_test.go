package database

import (
	"errors"
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func authorizationFixture(t *testing.T, db *gorm.DB, prefix string) model.AgricultureResourceBinding {
	t.Helper()
	for _, row := range []any{
		&model.User{ID: prefix + "u", Username: prefix + "u"},
		&model.Tenant{ID: prefix + "t", Status: "active"},
		&model.Tenant{ID: prefix + "x", Status: "active"},
		&model.Membership{ID: prefix + "m", TenantID: prefix + "t", UserID: prefix + "u", Status: "active"},
		&model.Workspace{ID: prefix + "w", TenantID: prefix + "t", Product: "agriculture"},
		&model.Workspace{ID: prefix + "z", TenantID: prefix + "x", Product: ""},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal("cannot create authorization fixture")
		}
	}
	now := time.Now().UTC()
	return model.AgricultureResourceBinding{ID: prefix + "b", TenantID: prefix + "t", WorkspaceID: prefix + "w", ResourceKind: "farm", ResourceID: "gis-fixture", SubjectType: "user", SubjectID: prefix + "u", Capabilities: `["farm.read"]`, AuthVersion: 1, State: "active", ReceiptHash: strings.Repeat("a", 64), ReceiptRevision: 1, ConfirmedAt: now, CreatedAt: now, UpdatedAt: now}
}

func TestAgricultureAuthorizationSQLiteConstraintsAndReplay(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:" + t.Name() + "?mode=memory&cache=shared&_foreign_keys=on"})
	if err != nil {
		t.Fatal(err)
	}
	db = db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	pool, _ := db.DB()
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.AutoMigrate(&model.User{}, &model.Tenant{}, &model.Membership{}, &model.Workspace{}); err != nil {
		t.Fatal(err)
	}
	binding := authorizationFixture(t, db, "sql")
	if err := migrateAgricultureAuthorization(db); err != nil {
		t.Fatal(err)
	}
	var epoch model.AgricultureAuthorizationEpoch
	if err := db.First(&epoch, "tenant_id = ?", binding.TenantID).Error; err != nil || epoch.Version != 1 {
		t.Fatal("existing agriculture tenant was not initialized")
	}
	var legacy int64
	db.Model(&epoch).Where("tenant_id = ?", "sqlx").Count(&legacy)
	if legacy != 0 {
		t.Fatal("non-agriculture tenant backfilled")
	}
	for _, invalidVersion := range []int64{0, 9007199254740992} {
		if db.Model(&epoch).Where("tenant_id = ?", binding.TenantID).Update("version", invalidVersion).Error == nil {
			t.Fatal("invalid authorization epoch accepted")
		}
	}
	if err := db.Model(&epoch).Where("tenant_id = ?", binding.TenantID).Update("version", 7).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateAgricultureAuthorization(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&epoch, "tenant_id = ?", binding.TenantID).Error; err != nil || epoch.Version != 7 {
		t.Fatal("migration replay reset epoch")
	}
	if err := db.Create(&binding).Error; err != nil {
		t.Fatal(err)
	}
	copy := binding
	copy.ID = "duplicate"
	if db.Create(&copy).Error == nil {
		t.Fatal("duplicate authorization scope accepted")
	}
	for _, change := range []struct {
		name   string
		mutate func(*model.AgricultureResourceBinding)
	}{
		{"cross_workspace", func(b *model.AgricultureResourceBinding) { b.WorkspaceID = "sqlz" }},
		{"cross_member", func(b *model.AgricultureResourceBinding) { b.SubjectID = "unknown" }},
		{"empty_tenant", func(b *model.AgricultureResourceBinding) { b.TenantID = "" }},
		{"invalid_kind", func(b *model.AgricultureResourceBinding) { b.ResourceKind = "workspace" }},
		{"invalid_subject", func(b *model.AgricultureResourceBinding) { b.SubjectType = "service" }},
		{"invalid_state", func(b *model.AgricultureResourceBinding) { b.State = "pending" }},
		{"zero_version", func(b *model.AgricultureResourceBinding) { b.AuthVersion = 0 }},
		{"overflow_version", func(b *model.AgricultureResourceBinding) { b.AuthVersion = 9007199254740992 }},
		{"invalid_hash", func(b *model.AgricultureResourceBinding) { b.ReceiptHash = strings.Repeat("z", 64) }},
		{"zero_receipt", func(b *model.AgricultureResourceBinding) { b.ReceiptRevision = 0 }},
		{"invalid_json", func(b *model.AgricultureResourceBinding) { b.Capabilities = "[" }},
		{"object_json", func(b *model.AgricultureResourceBinding) { b.Capabilities = "{}" }},
		{"null_json", func(b *model.AgricultureResourceBinding) { b.Capabilities = "null" }},
		{"empty_active", func(b *model.AgricultureResourceBinding) { b.Capabilities = "[]" }},
		{"too_many", func(b *model.AgricultureResourceBinding) {
			b.Capabilities = "[" + strings.Repeat(`"read",`, 200) + `"read"]`
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			b := binding
			b.ID = change.name
			b.ResourceID = change.name
			change.mutate(&b)
			if db.Create(&b).Error == nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
	copy.ID = "revoked"
	copy.ResourceID = "revoked"
	copy.State = "revoked"
	copy.Capabilities = "[]"
	if err := db.Create(&copy).Error; err != nil {
		t.Fatal("empty revoked capabilities rejected")
	}
	rollback := errors.New("fixture rollback")
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&epoch).Where("tenant_id = ?", binding.TenantID).Update("version", 8).Error; err != nil {
			return err
		}
		if err := tx.Model(&binding).Where("id = ?", binding.ID).Update("state", "revoked").Error; err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if err := db.First(&epoch, "tenant_id = ?", binding.TenantID).Error; err != nil || epoch.Version != 7 {
		t.Fatal("rollback advanced authorization clock")
	}
	if err := db.First(&binding, "id = ?", binding.ID).Error; err != nil || binding.State != "active" {
		t.Fatal("rollback altered binding")
	}
}
