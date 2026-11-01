package flow

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	"yingce/backend/internal/model"
)

type testCommandAdapter struct {
	execute func(context.Context, model.FlowCommandOutbox) (CommandOutcome, error)
	apply   func(context.Context, *gorm.DB, model.FlowCommandOutbox, CommandOutcome) error
}

func (a testCommandAdapter) Execute(c context.Context, r model.FlowCommandOutbox) (CommandOutcome, error) {
	return a.execute(c, r)
}
func (a testCommandAdapter) ApplyReceipt(c context.Context, tx *gorm.DB, r model.FlowCommandOutbox, o CommandOutcome) error {
	if a.apply != nil {
		return a.apply(c, tx, r, o)
	}
	return nil
}
func adapterInput() CommandInput {
	return CommandInput{Scope: Scope{TenantID: "fixture_tenant", WorkspaceID: "fixture_workspace"}, Actor: Actor{Type: "user", ID: "fixture_actor"}, Command: "agri.farm.create", Method: "POST", Path: "/internal/agri/v1/farms", IdempotencyKey: "fixture-idempotency-1", Payload: []byte(`{"operation":"fixture"}`)}
}
func completeAdapter() testCommandAdapter {
	return testCommandAdapter{execute: func(context.Context, model.FlowCommandOutbox) (CommandOutcome, error) {
		return CommandOutcome{State: CommandCompleted, HTTPStatus: 201, ResponseBody: []byte(`{"data":{"id":"fixture"}}`)}, nil
	}}
}

func TestCommandAdapterEnqueueIdentityAndReplay(t *testing.T) {
	db := testDB(t)
	d := CommandDispatcher{DB: db, Adapters: map[string]CommandAdapter{"agri.farm.create": completeAdapter()}}
	in := adapterInput()
	r, replayed, e := d.Enqueue(context.Background(), in)
	if e != nil || replayed || r.ActorRole != "" {
		t.Fatal("first adapter enqueue", e)
	}
	r2, replayed, e := d.Enqueue(context.Background(), in)
	if e != nil || !replayed || r2.ID != r.ID {
		t.Fatal("same operation replay", e)
	}
	for _, change := range []func(*CommandInput){func(v *CommandInput) { v.Actor.ID = "other" }, func(v *CommandInput) { v.Scope.WorkspaceID = "other" }, func(v *CommandInput) { v.Payload = []byte(`{"changed":true}`) }} {
		v := in
		change(&v)
		if _, _, e = d.Enqueue(context.Background(), v); !errors.Is(e, ErrCommandConflict) {
			t.Fatal("identity/payload conflict not rejected", e)
		}
	}
	var n int64
	db.Model(&model.FlowOperationProjection{}).Count(&n)
	if n != 1 {
		t.Fatal("replay created orphan projection")
	}
	done, e := d.DispatchOne(context.Background(), r.ID)
	if e != nil || done.Status != "dispatched" || done.ResponseStatus != 201 {
		t.Fatal("adapter dispatch without Loom", e)
	}
	if _, _, e = (CommandDispatcher{DB: db}).Enqueue(context.Background(), in); !errors.Is(e, ErrCommandAdapterUnavailable) {
		t.Fatal("missing adapter fell through")
	}
}

func TestCommandAdapterExplicitOutcomes(t *testing.T) {
	for _, state := range []CommandOutcomeState{CommandPending, CommandUnknown, CommandRejected, CommandReconciliation, CommandOutcomeState("invalid")} {
		t.Run(string(state), func(t *testing.T) {
			db := testDB(t)
			a := testCommandAdapter{execute: func(context.Context, model.FlowCommandOutbox) (CommandOutcome, error) {
				status := 202
				if state == CommandRejected {
					status = 403
				}
				return CommandOutcome{State: state, HTTPStatus: status}, nil
			}}
			d := CommandDispatcher{DB: db, Adapters: map[string]CommandAdapter{"agri.farm.create": a}}
			r, _, e := d.Enqueue(context.Background(), adapterInput())
			if e != nil {
				t.Fatal(e)
			}
			r, e = d.DispatchOne(context.Background(), r.ID)
			want := StatusRetryWait
			if state == CommandRejected {
				want = "failed"
			}
			if state == CommandReconciliation {
				want = StatusReconciliation
			}
			if r.Status != want {
				t.Fatalf("202 inferred success: %s", r.Status)
			}
			if state != "invalid" && e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestCommandAdapterAcceptedIsNotCompletion(t *testing.T) {
	db := testDB(t)
	a := testCommandAdapter{execute: func(context.Context, model.FlowCommandOutbox) (CommandOutcome, error) {
		return CommandOutcome{State: CommandCompleted, HTTPStatus: 202}, nil
	}}
	d := CommandDispatcher{DB: db, Adapters: map[string]CommandAdapter{"agri.farm.create": a}}
	r, _, e := d.Enqueue(context.Background(), adapterInput())
	if e != nil {
		t.Fatal(e)
	}
	r, e = d.DispatchOne(context.Background(), r.ID)
	if e == nil || r.Status != "retry_wait" {
		t.Fatal("202 was treated as completion")
	}
}

func TestCommandAdapterFencesOldGenerationAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		name := "generation"
		if cancel {
			name = "cancelled"
		}
		t.Run(name, func(t *testing.T) {
			db := testDB(t)
			var apply atomic.Int32
			a := completeAdapter()
			a.execute = func(_ context.Context, r model.FlowCommandOutbox) (CommandOutcome, error) {
				updates := map[string]any{"attempts": r.Attempts + 1}
				if cancel {
					updates = map[string]any{"status": "cancelled"}
				}
				if e := db.Model(&model.FlowCommandOutbox{}).Where("id=?", r.ID).Updates(updates).Error; e != nil {
					return CommandOutcome{}, e
				}
				return CommandOutcome{State: CommandCompleted, HTTPStatus: 201}, nil
			}
			a.apply = func(context.Context, *gorm.DB, model.FlowCommandOutbox, CommandOutcome) error {
				apply.Add(1)
				return nil
			}
			d := CommandDispatcher{DB: db, Adapters: map[string]CommandAdapter{"agri.farm.create": a}}
			r, _, e := d.Enqueue(context.Background(), adapterInput())
			if e != nil {
				t.Fatal(e)
			}
			_, e = d.DispatchOne(context.Background(), r.ID)
			if !errors.Is(e, ErrCommandLeaseLost) || apply.Load() != 0 {
				t.Fatal("stale worker entered receipt", e)
			}
		})
	}
}

func TestCommandAdapterLeaseExpiryRollsBackReceipt(t *testing.T) {
	db := testDB(t)
	now := time.Now().UTC()
	a := completeAdapter()
	a.apply = func(_ context.Context, tx *gorm.DB, r model.FlowCommandOutbox, _ CommandOutcome) error {
		if e := tx.Model(&model.FlowOperationProjection{}).Where("operation_id=?", r.OperationID).Update("status", "forbidden-stale-result").Error; e != nil {
			return e
		}
		now = now.Add(time.Minute)
		return nil
	}
	d := CommandDispatcher{DB: db, Now: func() time.Time { return now }, Lease: time.Second, Adapters: map[string]CommandAdapter{"agri.farm.create": a}}
	r, _, e := d.Enqueue(context.Background(), adapterInput())
	if e != nil {
		t.Fatal(e)
	}
	_, e = d.DispatchOne(context.Background(), r.ID)
	if !errors.Is(e, ErrCommandLeaseLost) {
		t.Fatal("expired callback committed", e)
	}
	var p model.FlowOperationProjection
	db.First(&p, "operation_id=?", r.OperationID)
	if p.Status != "queued" {
		t.Fatal("receipt rollback failed")
	}
	d.Adapters["agri.farm.create"] = completeAdapter()
	n, e := d.DispatchDue(context.Background(), 10)
	if e != nil || n != 1 {
		t.Fatal("expired adapter did not recover same command", n, e)
	}
	db.First(&r, "id=?", r.ID)
	if r.Status != "dispatched" || r.Attempts != 2 {
		t.Fatal("recovery did not advance generation")
	}
}

func TestCommandAdapterTransportErrorIsDurableAndSanitized(t *testing.T) {
	db := testDB(t)
	a := testCommandAdapter{execute: func(context.Context, model.FlowCommandOutbox) (CommandOutcome, error) {
		return CommandOutcome{State: CommandCompleted, HTTPStatus: 201, ResponseBody: []byte("secret")}, errors.New("secret upstream token")
	}}
	d := CommandDispatcher{DB: db, Adapters: map[string]CommandAdapter{"agri.farm.create": a}}
	in := adapterInput()
	in.MaxAttempts = 1
	r, _, e := d.Enqueue(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	r, e = d.DispatchOne(context.Background(), r.ID)
	if e == nil || r.Status != StatusReconciliation || r.ResponseBody != "" || r.LastError != "" {
		t.Fatal("transport outcome not durably sanitized")
	}
}
