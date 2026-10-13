package database

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"yingce/backend/internal/model"
)

// TestFlowRLSPostgres needs a NON-superuser DSN (superusers bypass RLS), e.g.
// FLOW_TEST_PG_RLS_DSN=postgres://flow_app:flow_app@127.0.0.1:55432/rls_test?sslmode=disable
func TestFlowRLSPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("FLOW_TEST_PG_RLS_DSN"))
	if dsn == "" {
		t.Skip("FLOW_TEST_PG_RLS_DSN is not configured")
	}
	base, err := Open(Config{Driver: "postgres", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("flow_rls_%d", time.Now().UnixNano())
	if err := base.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`) })
	sdsn, err := postgresDSNWithSearchPath(dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	db, err := Open(Config{Driver: "postgres", DSN: sdsn})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	var forced bool
	db.Raw("SELECT relforcerowsecurity FROM pg_class WHERE oid = 'flow_notifications'::regclass").Scan(&forced)
	if !forced {
		t.Fatal("RLS not forced on flow_notifications")
	}
	for _, tn := range []string{"tn_a", "tn_b"} {
		if err := db.Create(&model.FlowNotification{ID: "n_" + tn, TenantID: tn, WorkspaceID: "ws", EventID: "ev_" + tn, Type: "x", CreatedAt: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	scoped := func(tn string, fn func(tx *gorm.DB) error) error {
		return db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SELECT set_config('app.tenant_id', ?, true)", tn).Error; err != nil {
				return err
			}
			return fn(tx)
		})
	}
	err = scoped("tn_a", func(tx *gorm.DB) error {
		var n int64
		tx.Model(&model.FlowNotification{}).Count(&n)
		if n != 1 {
			return fmt.Errorf("tenant a sees %d rows", n)
		}
		r := tx.Model(&model.FlowNotification{}).Where("id = ?", "n_tn_b").Update("title", "hijack")
		if r.RowsAffected != 0 {
			return fmt.Errorf("cross-tenant update affected %d", r.RowsAffected)
		}
		tx.SavePoint("ins")
		ierr := tx.Create(&model.FlowNotification{ID: "n_x", TenantID: "tn_b", WorkspaceID: "ws", EventID: "ev_x", Type: "x"}).Error
		tx.RollbackTo("ins")
		if ierr == nil || !strings.Contains(ierr.Error(), "row-level security") {
			return fmt.Errorf("cross-tenant insert not blocked: %v", ierr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var all int64
	db.Model(&model.FlowNotification{}).Count(&all)
	if all != 2 {
		t.Fatalf("system context sees %d", all)
	}
}
