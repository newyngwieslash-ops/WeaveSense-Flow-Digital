# P5 — Billing, SaaS organizations, Model Gateway, hardening

## p5-billing-saas

**Budget reservations (platform ledger).** `backend/internal/flow/billing.go`
- `flow_billing_accounts`: one per tenant (migration v55 backfills `ba_<tenant>`; organizations get a `shared` account paid by the owner; personal tenants a `personal` one). `monthly_quota_micro` and `max_open_reserves` are tenant quotas.
- `Reserve` (root, idempotent by tenant+key) moves credits from available → reserved with a conditional `UPDATE … WHERE available >= amount` and a `reserve` ledger entry.
- `Allocate` takes a child budget from a parent reservation with one conditional `UPDATE … WHERE reserved-allocated-consumed >= amount`, so concurrent children cannot over-allocate.
- `Settle`/`Release` close a reservation; a child rolls its consumption up into the parent; a root writes `consume` and `refund` ledger entries. A root with open children cannot be closed. Closing twice changes nothing.
- `MarkUnknown` records a provider call whose result is unknown (timeout or 5xx after the request was sent). `Reconcile` resolves unknown reservations through an idempotent status lookup and never retries the call. With no lookup API at the gateway, the maintenance loop bills the reserved amount after 24h. `SweepStale` closes reservations for runs that died before completing.
- Every agent run reserves a root budget (`run:<runID>`, 2 credits). Each research child allocates from that root, and the run's completion settles it.

**Model Gateway.** `model_gateway.go`
- The gateway is an OpenAI-compatible platform endpoint, configured with `FLOW_MODEL_GATEWAY_URL`, `FLOW_MODEL_GATEWAY_KEY` and `FLOW_MODEL_GATEWAY_MODEL`. The key is server-side env only.
- `Synthesize` works in this order: reserve or allocate, call the model, settle, parse the JSON claims, then run `ValidateCitations` on each claim. Invalid claims are dropped and reported as `dropped_claims`.
- `intel_start_research` attaches a `synthesis` field to the output when the gateway is configured.
- Scheduled briefs call `POST /api/internal/flow/summarize`. The engine re-validates every citation in `saveReport`. If the gateway fails, the brief falls back to the extractive brief and records `coverage.summarization_error`.

**Organizations.** `orgs.go`
- `POST /api/intelligence/orgs` creates the tenant, the owner membership, a default workspace and a shared billing account.
- `POST /api/intelligence/invites` is admin-only and works on org tenants only. The token is shown once and only its sha256 is stored. Tokens expire after 7 days.
- `POST /api/intelligence/invites/accept` is single-use. `DELETE /api/intelligence/members/:userId` removes a member.

**Scheduled runs re-check authorization.**
- The engine calls `POST /api/internal/flow/authorize` before each due collection window and before each brief.
- If the grant actor no longer holds the scope, the schedule is disabled with `disabled_reason='grant_revoked'` (engine migration 0005).
- Errors fail closed: nothing runs, and the check is retried on the next tick.

**Transactional briefs.** Generation runs outside the transaction. The report insert and the `next_run_at` advance then commit in one transaction. If the save fails, the window stays due and no report is written.

**UI.** `/intelligence` has a Workspace admin panel with:
- notifications (unread badge, mark read, mark all read);
- skill pack install;
- billing account and reservations;
- create organization, invite, and accept invite.

**Tests.**
- `backend/internal/flow/p5_test.go`: reserve, allocate and settle; child over-allocation rejected; unknown → reconcile; quota; Synthesize drops invalid citations; unknown outcome; invite single-use; removed member's grant denied.
- `services/loom-engine/flow/p5.test.ts`: revoked grant disables collection and brief schedules; LLM brief path; transactional brief.

## p5-hardening (local, uncommitted on branch `p5-hardening`)

**Agent run leases.** `backend/internal/flow/lease.go`, `cloud_agent_pi_lease.go`, migration v56 `flow_run_leases`
- One replica owns a run; fencing token rises on takeover so a stale owner’s renew fails and cancels its session.
- TTL 60s with renew every TTL/3; wired at `startCloudAgentPi`.

**MCP egress allowlist.** `backend/internal/flow/egress.go`
- `PublicTransport` blocks loopback, private, link-local/metadata, CGNAT, and related prefixes on the resolved dial address.
- Production MCP gateway uses it; `AllowInsecure` keeps DefaultTransport for loopback test mocks.

**Nginx.** `/api/internal/` returns 404 at the edge so engine/platform internal routes stay off the public entry.

**Loom Engine RLS.** migration `0006_rls.sql`, `db.ts` `withTenant` / `app.tenant_id`
- Request handlers run under the token tenant; workers leave tenant empty (system context).
- `FORCE ROW LEVEL SECURITY`; deploy engine as non-superuser. Real-PG coverage in `pg.test.ts` (needs `FLOW_TEST_PG_*`).

**Backup/restore.** `deploy/flow/backup/{backup,manifest,restore}.sh`
- `pg_dump` custom format + per-table row counts/checksums; restore verifies the manifest.

**Tests.** `TestEgressBlocksPrivateAddresses`, `TestRunLeaseOwnershipAndTakeover` in `p5_test.go` (pass locally).

## p5-hardening

- **Postgres RLS (second layer).**
  - Engine migration `0006_rls.sql` turns on ENABLE + FORCE RLS for all 17 tenant tables, with policy `tenant_id = current_setting('app.tenant_id')`. An empty value means system context.
  - `db.ts` `withTenant()` uses AsyncLocalStorage. Every API request runs its statements inside a transaction with `set_config('app.tenant_id', …, true)`.
  - Verified on real PostgreSQL 17 as a non-superuser (`flow/pg.test.ts`): rows are hidden, cross-tenant INSERT is rejected, cross-tenant UPDATE affects 0 rows, and the HTTP API passes end to end.
- **Egress block.**
  - `flow/egress.go` `PublicTransport()` checks the resolved socket address in `net.Dialer.Control`, so DNS rebinding and redirects are covered too. The MCP gateway uses it in production.
  - Engine fetches already use `publicFetch`.
- **nginx.** `location ^~ /api/internal/ { return 404; }`.
- **Pi run lease/recovery ownership.**
  - `flow_run_leases` (schema v56) holds owner, fencing token and expiry. A Pi session starts only after `Acquire` succeeds, and renews every TTL/3 (TTL 60s).
  - A lost lease cancels the session. Recovery on another replica takes over after expiry, with a new fencing token.
  - Release on exit allows immediate recovery.
- **Canvas sidebar server conversations.**
  - The canvas agent conversation directory is stored server-side via `GET/PUT /api/agent/canvas-conversations/:canvasId`, per user, with optimistic concurrency on `version`.
  - The client merges on 409 (newest `updatedAt` wins per conversation). localForage remains the warm cache and offline fallback.
- **Backup/restore + release/deploy docs.** See `release-deploy.md`.
