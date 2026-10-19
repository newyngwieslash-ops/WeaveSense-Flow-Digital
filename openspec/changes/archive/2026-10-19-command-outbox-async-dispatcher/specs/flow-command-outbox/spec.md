# flow-command-outbox Specification

## ADDED Requirements

### Requirement: Platform transactional command outbox and async dispatcher (PRD §11.3)
The platform SHALL persist manual collection (`collection.request`) and research (`research.request`) commands in `flow_command_outbox` within the same platform database transaction as the initial `flow_operation_projections` row and any requested `flow_budget_reservations` hold, enforce `(tenant_id, idempotency_key)` idempotency with `409 Conflict` on parameter mismatch, and dispatch pending or retry-due commands to Loom Engine via `CommandDispatcher`.

#### Scenario: Transactional enqueue and immediate dispatch
- **WHEN** a caller invokes `intel_collect_once`, `intel_start_research`, `POST /api/intelligence/collection-runs`, or `POST /api/intelligence/research-runs` with an idempotency key
- **THEN** the platform SHALL atomically create the `FlowCommandOutbox` and `FlowOperationProjection` rows, dispatch the command to Loom Engine with `X-Idempotency-Key`, and persist the dispatched status and Engine response

#### Scenario: Idempotent replay and parameter conflict detection
- **WHEN** the same `(tenant_id, idempotency_key)` is submitted again with identical parameters
- **THEN** the platform SHALL replay the stored result without dispatching a duplicate command to Loom Engine
- **AND WHEN** the same `(tenant_id, idempotency_key)` is submitted with different parameters
- **THEN** the platform SHALL reject the request with `409 Conflict` (`ErrCommandConflict`)

#### Scenario: Transient Engine failure and background retry dispatcher
- **WHEN** Loom Engine is temporarily unreachable or returns HTTP 5xx during command dispatch
- **THEN** the `FlowCommandOutbox` row SHALL transition to `retry_wait` with backoff (or `reconciliation_required` once `max_attempts` is reached) and `CommandDispatcher.DispatchDue` SHALL deliver the command once Loom Engine recovers
