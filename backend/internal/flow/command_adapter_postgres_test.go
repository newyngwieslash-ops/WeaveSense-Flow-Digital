package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"yingce/backend/internal/database"
	"yingce/backend/internal/model"
)

// The adapter is an explicit protocol fixture. PostgreSQL, RLS, concurrent
// claims, scheduler and receipt transactions are the actual implementation.
func TestCommandAdapterPostgres(t *testing.T) {
	if os.Getenv("AGRI_TEST_PG_ISOLATED") != "1" || os.Getenv("AGRI_TEST_PG_MIGRATION_DSN") == "" || os.Getenv("AGRI_TEST_PG_RUNTIME_DSN") == "" {
		t.Skip("requires isolated migration/runtime PostgreSQL roles")
	}
	open := func(key string) *gorm.DB {
		db, e := database.Open(database.Config{Driver: "postgres", DSN: os.Getenv(key)})
		if e != nil {
			t.Fatal("database unavailable")
		}
		db = db.Session(&gorm.Session{Logger: logger.Discard})
		pool, e := db.DB()
		if e != nil {
			t.Fatal(e)
		}
		pool.SetMaxOpenConns(1)
		pool.SetMaxIdleConns(1)
		t.Cleanup(func() { pool.Close() })
		return db
	}
	owner, app := open("AGRI_TEST_PG_MIGRATION_DSN"), open("AGRI_TEST_PG_RUNTIME_DSN")
	if e := database.MigrateSchema(owner); e != nil {
		t.Fatal("migration failed")
	}
	suffix := fmt.Sprintf("adapter_%d", time.Now().UnixNano())
	tenant, workspace, actor := "t_"+suffix, "w_"+suffix, "u_"+suffix
	for _, row := range []any{&model.User{ID: actor, Username: actor, Status: "active"}, &model.Tenant{ID: tenant, Kind: "organization", OwnerUserID: actor, Status: "active"}, &model.Workspace{ID: workspace, TenantID: tenant, Name: "Adapter fixture", Slug: suffix, Product: "agriculture"}} {
		if e := owner.Create(row).Error; e != nil {
			t.Fatal("owner fixture creation failed")
		}
	}
	in := adapterInput()
	in.Scope = Scope{TenantID: tenant, WorkspaceID: workspace}
	in.Actor.ID = actor
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var tick atomic.Int64
	tick.Store(time.Now().UTC().UnixNano())
	now := func() time.Time { return time.Unix(0, tick.Load()).UTC() }
	d := CommandDispatcher{DB: app, Now: now, Lease: time.Second, Adapters: map[string]CommandAdapter{"agri.farm.create": completeAdapter()}}.ForTenant(tenant)
	enqueue := func(input CommandInput) (model.FlowCommandOutbox, bool, error) {
		var r model.FlowCommandOutbox
		var replay bool
		e := TenantTx(app, tenant, func(tx *gorm.DB) error {
			scoped := d
			scoped.DB = tx
			var e error
			r, replay, e = scoped.Enqueue(ctx, input)
			return e
		})
		return r, replay, e
	}
	t.Run("concurrent_enqueue_no_orphan", func(t *testing.T) {
		pool, _ := app.DB()
		pool.SetMaxOpenConns(4)
		defer pool.SetMaxOpenConns(1)
		type result struct {
			row    model.FlowCommandOutbox
			replay bool
			err    error
		}
		results := make(chan result, 12)
		var wg sync.WaitGroup
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); r, replay, e := enqueue(in); results <- result{r, replay, e} }()
		}
		wg.Wait()
		close(results)
		id, created := "", 0
		for r := range results {
			if r.err != nil {
				t.Fatal("concurrent enqueue failed")
			}
			if id == "" {
				id = r.row.ID
			}
			if r.row.ID != id {
				t.Fatal("duplicate outbox identity")
			}
			if !r.replay {
				created++
			}
		}
		if created != 1 {
			t.Fatal("multiple creation winners")
		}
		var rows, projections int64
		if e := TenantTx(app, tenant, func(tx *gorm.DB) error {
			if e := tx.Model(&model.FlowCommandOutbox{}).Count(&rows).Error; e != nil {
				return e
			}
			return tx.Model(&model.FlowOperationProjection{}).Count(&projections).Error
		}); e != nil || rows != 1 || projections != 1 {
			t.Fatal("orphan projection or duplicate command")
		}
	})
	t.Run("expired_worker_fenced_and_io_outside_transaction", func(t *testing.T) {
		input := in
		input.IdempotencyKey = "adapter-fencing-fixture"
		r, _, e := enqueue(input)
		if e != nil {
			t.Fatal(e)
		}
		started, release := make(chan struct{}), make(chan struct{})
		defer close(release)
		var executions, applied, probes atomic.Int32
		a := testCommandAdapter{execute: func(c context.Context, row model.FlowCommandOutbox) (CommandOutcome, error) {
			probeCtx, stop := context.WithTimeout(c, time.Second)
			defer stop()
			var safe bool
			if e := app.WithContext(probeCtx).Raw("SELECT coalesce(current_setting('app.tenant_ids',true),'')='' AND txid_current_if_assigned() IS NULL").Scan(&safe).Error; e != nil || !safe {
				return CommandOutcome{}, errors.New("transaction held during I/O")
			}
			probes.Add(1)
			if executions.Add(1) == 1 {
				close(started)
				select {
				case <-release:
				case <-c.Done():
					return CommandOutcome{}, c.Err()
				}
			}
			return CommandOutcome{State: CommandCompleted, HTTPStatus: 201}, nil
		}, apply: func(_ context.Context, _ *gorm.DB, _ model.FlowCommandOutbox, _ CommandOutcome) error {
			applied.Add(1)
			return nil
		}}
		d.Adapters["agri.farm.create"] = a
		oldDone := make(chan error, 1)
		go func() { _, e := d.DispatchOne(ctx, r.ID); oldDone <- e }()
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("first dispatch did not reach external phase")
		}
		tick.Add(int64(2 * time.Second))
		newRow, e := d.DispatchOne(ctx, r.ID)
		if e != nil || newRow.Status != "dispatched" || newRow.Attempts != 2 {
			t.Fatal("new generation could not reclaim", e)
		}
		// Release the old external response without closing the channel twice.
		release <- struct{}{}
		if e := <-oldDone; !errors.Is(e, ErrCommandLeaseLost) {
			t.Fatal("old generation not fenced", e)
		}
		if applied.Load() != 1 || probes.Load() != 2 {
			t.Fatal("stale receipt applied or I/O retained scope")
		}
	})
	t.Run("cancelled_before_receipt_no_apply", func(t *testing.T) {
		input := in
		input.IdempotencyKey = "adapter-cancel-fixture"
		r, _, e := enqueue(input)
		if e != nil {
			t.Fatal(e)
		}
		var applied atomic.Int32
		a := completeAdapter()
		a.execute = func(context.Context, model.FlowCommandOutbox) (CommandOutcome, error) {
			e := TenantTx(app, tenant, func(tx *gorm.DB) error {
				return tx.Model(&model.FlowCommandOutbox{}).Where("id=?", r.ID).Update("status", "cancelled").Error
			})
			return CommandOutcome{State: CommandCompleted, HTTPStatus: 201}, e
		}
		a.apply = func(context.Context, *gorm.DB, model.FlowCommandOutbox, CommandOutcome) error {
			applied.Add(1)
			return nil
		}
		d.Adapters["agri.farm.create"] = a
		_, e = d.DispatchOne(ctx, r.ID)
		if !errors.Is(e, ErrCommandLeaseLost) || applied.Load() != 0 {
			t.Fatal("cancelled operation applied receipt", e)
		}
	})
	t.Run("cross_tenant_and_empty_scope_denied", func(t *testing.T) {
		var rows int64
		if e := app.Model(&model.FlowCommandOutbox{}).Count(&rows).Error; e != nil || rows != 0 {
			t.Fatal("empty scope exposed rows")
		}
		r, _, e := enqueue(in)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = d.ForTenant("not_authorized_fixture").DispatchOne(ctx, r.ID); e == nil {
			t.Fatal("foreign tenant dispatched command")
		}
	})
}
