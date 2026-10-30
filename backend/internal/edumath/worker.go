package edumath

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// SyncOnce advances every non-terminal (or unsettled) job: it pulls the service
// state, ingests artifacts, settles finished stages once, and reconciles unknown
// submits. Restart-safe: everything it needs is in the database.
func (s *Service) SyncOnce(ctx context.Context, limit int) (int, error) {
	var jobs []model.EduMathJob
	load := func(tx *gorm.DB) error {
		var batch []model.EduMathJob
		err := tx.Where("status IN ? OR settlement = ?", []string{"queued", "running", "reconciliation_required"}, "open").Order("updated_at").Limit(limit).Find(&batch).Error
		jobs = append(jobs, batch...)
		return err
	}
	if s.DB.Dialector.Name() == "postgres" {
		tenants, err := s.Scanner.Next(ctx, flow.ScanEduJobs)
		if err != nil {
			return 0, err
		}
		for _, tenant := range tenants {
			if err := flow.TenantTx(s.DB.WithContext(ctx), tenant, load); err != nil {
				return 0, err
			}
		}
	} else if err := load(s.DB.WithContext(ctx)); err != nil {
		return 0, err
	}
	n := 0
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		err := flow.TenantTx(s.DB, j.TenantID, func(tx *gorm.DB) error {
			var cur model.EduMathJob
			if err := tx.First(&cur, "id = ?", j.ID).Error; err != nil {
				return err
			}
			_, err := s.Sync(ctx, tx, cur)
			return err
		})
		if err != nil {
			slog.Warn("edumath sync failed", "job", j.ID, "error", err)
			continue
		}
		n++
	}
	return n, nil
}

// Run polls until ctx is cancelled.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	ledgerEvery, nextLedger := LedgerReconcileEvery(), time.Now().Add(time.Minute)
	for {
		c, cancel := context.WithTimeout(ctx, every*10)
		_, _ = s.SyncOnce(c, 50)
		cancel()
		if ledgerEvery > 0 && time.Now().After(nextLedger) {
			if n, found, err := s.ReconcileAll(ctx, ledgerWindow); err != nil {
				slog.Warn("edumath scheduled ledger reconcile failed", "error", err)
			} else {
				slog.Info("edumath scheduled ledger reconcile", "tenants", n, "findings", found)
			}
			nextLedger = time.Now().Add(ledgerEvery)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ledgerWindow is how far back the scheduled reconciliation looks (jobs created).
const ledgerWindow = 35 * 24 * time.Hour

// LedgerReconcileEvery is the scheduled ledger reconciliation interval
// (EDU_LEDGER_RECONCILE_EVERY, Go duration; default 24h; "0" or "off" disables).
func LedgerReconcileEvery() time.Duration {
	v := os.Getenv("EDU_LEDGER_RECONCILE_EVERY")
	if v == "" {
		return 24 * time.Hour
	}
	if v == "off" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 24 * time.Hour
	}
	if d > 0 && d < 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}

// ReconcileAll runs the ledger reconciliation for every tenant with math jobs
// in the window, each in its own tenant transaction, recording findings with
// the system actor exactly as an admin-run reconciliation would (DEV-063).
func (s *Service) ReconcileAll(ctx context.Context, window time.Duration) (tenants, findings int, err error) {
	now := s.now()
	from := now.Add(-window)
	var ids []string
	postgres := s.DB.Dialector.Name() == "postgres"
	afterTenant := ""
	if !postgres {
		if err = s.DB.WithContext(ctx).Model(&model.EduMathJob{}).Where("created_at >= ?", from).Distinct().Pluck("tenant_id", &ids).Error; err != nil {
			return 0, 0, err
		}
	}
	for {
		if ctx.Err() != nil {
			return tenants, findings, ctx.Err()
		}
		if postgres {
			page, pageErr := s.Scanner.Page(ctx, flow.ScanEduLedger, afterTenant)
			if pageErr != nil {
				return tenants, findings, pageErr
			}
			ids = page
		}
		for _, tenant := range ids {
			if ctx.Err() != nil {
				return tenants, findings, ctx.Err()
			}
			visited, tenantFindings := false, 0
			e := flow.TenantTx(s.DB.WithContext(ctx), tenant, func(tx *gorm.DB) error {
				lines, err := s.LedgerReport(tx, tenant, from, now.Add(time.Second))
				if err != nil {
					return err
				}
				// Discovery exposes only bounded IDs. Reapply the original time
				// window inside the scoped transaction; old-only schools are not
				// reported/audited as reconciled this cycle.
				if len(lines) == 0 {
					return nil
				}
				visited = true
				a := Actor{UserID: "system:ledger", TenantID: tenant}
				if _, err := s.RecordLedgerAnomalies(tx, a, lines); err != nil {
					return err
				}
				n := 0
				for _, l := range lines {
					n += len(l.Anomalies)
				}
				tenantFindings = n
				s.audit(tx, a, "ledger.reconcile.scheduled", tenant, fmt.Sprintf("jobs=%d findings=%d", len(lines), n))
				return nil
			})
			if e != nil {
				slog.Warn("edumath ledger reconcile failed", "tenant", tenant, "error", e)
				err = e
				continue
			}
			if visited {
				tenants++
				findings += tenantFindings
			}
		}
		// A daily pass must drain all pages, unlike frequent worker ticks.
		// An exact multiple requests the final empty page. Cursor is local to
		// this pass; a cancelled invocation cannot skip tenants on retry.
		if !postgres || len(ids) < 100 {
			break
		}
		afterTenant = ids[len(ids)-1]
	}
	return tenants, findings, err
}

// FromEnv builds the service from FLOW_MATH_SERVICE_URL / FLOW_MATH_SERVICE_KEY
// (and optional EDU_ARTIFACT_URL_KEY). Returns nil when not configured.
func FromEnv(db *gorm.DB) *Service {
	url, key := os.Getenv("FLOW_MATH_SERVICE_URL"), os.Getenv("FLOW_MATH_SERVICE_KEY")
	if url == "" || len(key) < 32 {
		return nil
	}
	uk := os.Getenv("EDU_ARTIFACT_URL_KEY")
	if len(uk) < 32 {
		uk = "edu-artifact-url:" + key
	}
	return &Service{DB: db, Client: Client{BaseURL: url, Key: []byte(key)}, URLKey: []byte(uk)}
}
