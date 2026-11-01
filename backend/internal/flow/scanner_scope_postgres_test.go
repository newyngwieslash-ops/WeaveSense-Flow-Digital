package flow

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"yingce/backend/internal/database"
	"yingce/backend/internal/model"
)

// Bootstrap credentials exist only in this fresh-cluster drift fixture. The
// application and scanner assertions always connect as the actual runtime roles.
func TestFlowPostgresScannerAndRuntimeAdmission(t *testing.T) {
	if os.Getenv("AGRI_TEST_PG_ISOLATED") != "1" || os.Getenv("AGRI_TEST_PG_FIXTURE_BOOTSTRAP_DSN") == "" {
		t.Skip("requires owned fresh cluster drift fixture")
	}
	open := func(key string) *gorm.DB {
		db, e := database.Open(database.Config{Driver: "postgres", DSN: os.Getenv(key)})
		if e != nil {
			t.Fatal("fixture connection failed")
		}
		db = db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
		p, e := db.DB()
		if e != nil {
			t.Fatal(e)
		}
		p.SetMaxOpenConns(1)
		t.Cleanup(func() { p.Close() })
		return db
	}
	admin, owner, app, system := open("AGRI_TEST_PG_FIXTURE_BOOTSTRAP_DSN"), open("AGRI_TEST_PG_MIGRATION_DSN"), open("AGRI_TEST_PG_RUNTIME_DSN"), open("AGRI_TEST_PG_SYSTEM_DSN")
	if e := database.MigrateSchema(owner); e != nil {
		t.Fatal(e)
	}
	exec := func(q string) {
		t.Helper()
		if e := admin.Exec(q).Error; e != nil {
			t.Fatal("fixture drift setup failed")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	scanner, e := OpenBackgroundScanner(os.Getenv("AGRI_TEST_PG_SYSTEM_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	defer scanner.Close()
	t.Run("fixed_scanner_no_business_access", func(t *testing.T) {
		for _, q := range []string{"SELECT * FROM flow_event_inbox", "SELECT * FROM users", "UPDATE flow_topics SET title='forbidden'", "SET ROLE flow_platform_guard", "SET ROLE flow_platform_migrate", "SET ROLE flow_platform_app"} {
			if system.Exec(q).Error == nil {
				t.Fatal("scanner acquired business authority")
			}
		}
		if app.Exec("SELECT * FROM flow_system.pending_tenants('external_waits','',100)").Error == nil {
			t.Fatal("HTTP runtime enumerated all tenants")
		}
		for _, q := range []string{"SELECT * FROM flow_system.pending_tenants('arbitrary','',1)", "SELECT * FROM flow_system.pending_tenants('billing','',101)", "SELECT * FROM flow_system.pending_tenants('billing',NULL,1)"} {
			if system.Exec(q).Error == nil {
				t.Fatal("invalid fixed scan accepted")
			}
		}
		for _, key := range []string{"AGRI_TEST_PG_RUNTIME_DSN", "AGRI_TEST_PG_MIGRATION_DSN"} {
			if s, e := OpenBackgroundScanner(os.Getenv(key)); e == nil {
				s.Close()
				t.Fatal("privileged/wrong scanner role accepted")
			}
		}
	})
	t.Run("privilege_drift_rejected_and_restored", func(t *testing.T) {
		if e := admin.Transaction(func(tx *gorm.DB) error {
			if e := tx.Exec("SET LOCAL ROLE flow_platform_app").Error; e != nil {
				return e
			}
			if database.RequireFlowScope(tx) == nil {
				t.Error("session owner impersonating app accepted")
			}
			return nil
		}); e != nil {
			t.Fatal("fixture role transaction failed")
		}
		impersonated, e := url.Parse(os.Getenv("AGRI_TEST_PG_FIXTURE_BOOTSTRAP_DSN"))
		if e != nil {
			t.Fatal("fixture URL invalid")
		}
		q := impersonated.Query()
		q.Set("options", "-c role=flow_platform_system")
		impersonated.RawQuery = q.Encode()
		if s, e := OpenBackgroundScanner(impersonated.String()); e == nil {
			s.Close()
			t.Error("session owner impersonating scanner accepted")
		}
		cases := [][2]string{{"GRANT SELECT ON users TO flow_platform_system", "REVOKE SELECT ON users FROM flow_platform_system"}, {"GRANT UPDATE(title) ON flow_topics TO flow_platform_system", "REVOKE UPDATE(title) ON flow_topics FROM flow_platform_system"}, {"GRANT TRUNCATE ON flow_event_inbox TO flow_platform_system", "REVOKE TRUNCATE ON flow_event_inbox FROM flow_platform_system"}}
		for _, pair := range cases {
			func() {
				exec(pair[0])
				defer exec(pair[1])
				s, e := OpenBackgroundScanner(os.Getenv("AGRI_TEST_PG_SYSTEM_DSN"))
				if e == nil {
					s.Close()
					t.Error("scanner privilege drift accepted")
				}
			}()
		}
		for _, pair := range [][2]string{{"ALTER TABLE flow_topics OWNER TO flow_platform_app", "ALTER TABLE flow_topics OWNER TO flow_platform_migrate"}, {"GRANT flow_platform_admission TO flow_platform_app", "REVOKE flow_platform_admission FROM flow_platform_app"}, {"ALTER DATABASE flow_platform OWNER TO flow_platform_app", "ALTER DATABASE flow_platform OWNER TO flow_platform_migrate"}, {"GRANT SELECT(id) ON flow_provider_accounts TO flow_platform_app", "REVOKE SELECT(id) ON flow_provider_accounts FROM flow_platform_app"}} {
			func() {
				exec(pair[0])
				defer exec(pair[1])
				if database.RequireFlowScope(app) == nil {
					t.Error("runtime privilege drift accepted")
				}
			}()
		}
		if e := database.RequireFlowScope(app); e != nil {
			t.Fatal("restored runtime rejected")
		}
		s, e := OpenBackgroundScanner(os.Getenv("AGRI_TEST_PG_SYSTEM_DSN"))
		if e != nil {
			t.Fatal("restored scanner rejected")
		}
		s.Close()
	})
	t.Run("bounded_keyset_reaches_all_tenants", func(t *testing.T) {
		prefix := fmt.Sprintf("scan%d", time.Now().UnixNano())
		for i := 0; i < 105; i++ {
			id := fmt.Sprintf("%s_%03d", prefix, i)
			if e := owner.Create(&model.FlowExternalWait{ID: id, TenantID: id, WorkspaceID: "fixture", UserID: "fixture", RunID: id, OperationID: id, Status: "waiting"}).Error; e != nil {
				t.Fatal(e)
			}
		}
		defer owner.Where("id LIKE ?", prefix+"_%").Delete(&model.FlowExternalWait{})
		seen := map[string]bool{}
		for i := 0; i < 3; i++ {
			ids, e := scanner.Next(ctx, ScanExternalWaits)
			if e != nil {
				t.Fatal(e)
			}
			if len(ids) > 100 {
				t.Fatal("unbounded scan")
			}
			for _, id := range ids {
				seen[id] = true
			}
		}
		for i := 0; i < 105; i++ {
			if !seen[fmt.Sprintf("%s_%03d", prefix, i)] {
				t.Fatal("keyset starved a tenant")
			}
		}
	})
}
