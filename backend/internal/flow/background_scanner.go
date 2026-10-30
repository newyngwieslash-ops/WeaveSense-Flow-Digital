package flow

import (
	"context"
	"errors"
	"sync"

	"gorm.io/gorm"
	"yingce/backend/internal/database"
)

type ScanKind string

const (
	ScanBilling       ScanKind = "billing"
	ScanNotifications ScanKind = "notifications"
	ScanModelTasks    ScanKind = "model_tasks"
	ScanCommands      ScanKind = "commands"
	ScanExternalWaits ScanKind = "external_waits"
	ScanEduJobs       ScanKind = "edu_jobs"
	ScanEduLedger     ScanKind = "edu_ledger"
)

var ErrBackgroundScanner = errors.New("flow: restricted background scanner unavailable")

// No exposed database handle and no arbitrary SQL/table API. Only the server's
// worker composition receives this capability; HTTP/Agent DTOs cannot select it.
type BackgroundScanner struct {
	db      *gorm.DB
	mu      sync.Mutex
	cursors map[ScanKind]string
}

func OpenBackgroundScanner(dsn string) (*BackgroundScanner, error) {
	if dsn == "" {
		return nil, ErrBackgroundScanner
	}
	db, err := database.Open(database.Config{Driver: "postgres", DSN: dsn})
	if err != nil {
		return nil, ErrBackgroundScanner
	}
	pool, err := db.DB()
	if err != nil {
		return nil, ErrBackgroundScanner
	}
	pool.SetMaxOpenConns(2)
	pool.SetMaxIdleConns(1)
	var safe bool
	err = db.Raw(`SELECT current_user=session_user AND r.rolname='flow_platform_system' AND r.rolcanlogin
 AND NOT(r.rolsuper OR r.rolbypassrls OR r.rolinherit OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication)
 AND NOT pg_has_role(current_user,'flow_platform_guard','MEMBER')
 AND NOT pg_has_role(current_user,'flow_platform_migrate','MEMBER')
 AND NOT pg_has_role(current_user,'flow_platform_app','MEMBER')
 AND NOT pg_has_role(current_user,'flow_platform_admission','MEMBER')
 AND NOT EXISTS(SELECT 1 FROM pg_database d WHERE d.datname=current_database() AND d.datdba=r.oid)
 AND NOT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m','f') AND
 (c.relowner=r.oid OR has_table_privilege(current_user,c.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
 OR has_any_column_privilege(current_user,c.oid,'SELECT,INSERT,UPDATE,REFERENCES')))
 AND EXISTS(SELECT 1 FROM pg_proc p JOIN pg_roles g ON g.oid=p.proowner
 WHERE p.oid=to_regprocedure('flow_system.pending_tenants(text,text,integer)') AND p.prosecdef
 AND g.rolname='flow_platform_guard' AND NOT(g.rolcanlogin OR g.rolsuper OR g.rolbypassrls OR g.rolinherit)
 AND p.proconfig @> ARRAY['search_path=pg_catalog']::text[] AND has_function_privilege(current_user,p.oid,'EXECUTE')
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.aclexplode(p.proacl) a WHERE a.grantee=0 AND a.privilege_type='EXECUTE'))
 FROM pg_roles r WHERE r.rolname=current_user`).Scan(&safe).Error
	if err != nil || !safe {
		pool.Close()
		return nil, ErrBackgroundScanner
	}
	return &BackgroundScanner{db: db, cursors: map[ScanKind]string{}}, nil
}
func (s *BackgroundScanner) Close() error {
	if s == nil {
		return nil
	}
	pool, err := s.db.DB()
	if err != nil {
		return err
	}
	return pool.Close()
}
func (s *BackgroundScanner) Next(ctx context.Context, kind ScanKind) ([]string, error) {
	switch kind {
	case ScanBilling, ScanNotifications, ScanModelTasks, ScanCommands, ScanExternalWaits, ScanEduJobs, ScanEduLedger:
	default:
		return nil, ErrInvalid
	}
	if s == nil || s.db == nil {
		return nil, ErrBackgroundScanner
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []string
	if err := s.db.WithContext(ctx).Raw("SELECT tenant_id FROM flow_system.pending_tenants(?,?,100)", string(kind), s.cursors[kind]).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 100 {
		s.cursors[kind] = rows[len(rows)-1]
	} else {
		s.cursors[kind] = ""
	}
	return rows, nil
}

// Use only for bounded database work. External I/O must be outside fn.
// Empty tenant preserves an already scoped request transaction; it never
// supplies a system role or changes the strict PostgreSQL policy.
func shortTenantWork(db *gorm.DB, tenant string, fn func(*gorm.DB) error) error {
	if tenant == "" {
		return fn(db)
	}
	return TenantTx(db, tenant, fn)
}
