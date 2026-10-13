# P1 — Tenants + RSS (implementation notes)

## Platform (Go, `backend/`)
| Piece | Location |
|---|---|
| Models `Tenant`, `Membership`, `Workspace`, `WorkspaceMembership`, `ServicePrincipal`, `FlowEventInbox`, `FlowOperationProjection` (tables `flow_*`) | `internal/model/models_tenancy.go` |
| Schema migration **v51 `flow_tenancy`**: creates tables, maps every existing user to a personal tenant + `default` workspace (idempotent, deterministic ids `tn_u_<user>`, `ws_u_<user>`) | `internal/database/migrations.go`, `internal/model/tenancy_personal.go` |
| Scope resolution from the session user (+ optional `X-Workspace-Id`); requires active tenant membership **and** workspace membership, tenant active; new users get a personal tenant lazily | `internal/flow/scope.go` |
| Signed execution context (HS256 JWT; fields per service-contracts §1; TTL clamped ≤ 300 s; aud/iss/exp/signature checked) | `internal/flow/token.go` |
| Engine client — mints a fresh context per request, forwards `X-Idempotency-Key` | `internal/flow/client.go` |
| Routes: `GET /api/intelligence/workspaces`, `GET/POST /api/intelligence/sources`, `GET/POST /api/intelligence/schedules`, `POST /api/intelligence/collection-runs`, `GET /api/intelligence/documents`, `GET /api/intelligence/operations/:id`; inbox sink `POST /api/internal/flow/events` | `internal/flow/routes.go`, wired in `cmd/server/main.go` |
| Inbox: dedupe on `(consumer_id, event_id)`, projection updated monotonically by `sequence`, cross-tenant events rejected | `internal/flow/events.go` |

## Loom Engine (`services/loom-engine/flow/`, Node 22 TS)
| Piece | Location |
|---|---|
| flow_intel migrations: `flow_sources`, `flow_source_cursors`, `flow_monitors`, `flow_schedules`, `flow_collection_runs`, `flow_documents`, `flow_document_revisions`, `flow_outbox` — all tenant-scoped, uniques include `tenant_id` | `migrations/0001_flow_intel.sql`, runner in `db.ts` |
| RSS/Atom fetch+parse **reused** from WeaveSense (`src/lib/server/rss.ts` → outbound-policy guarded `fetchText` + `parseFeedXml`) | `engine.ts` `defaultFetcher` |
| Manual collect: same key+params replays, same key/different params → 409; foreign/guessed ids → 404 | `Engine.requestCollection` |
| Scheduler: single scanner (`pg_try_advisory_xact_lock`), `FOR UPDATE SKIP LOCKED`, run key `tenant_id:schedule_id:scheduled_at` + unique index, missed windows collapse to latest | `Engine.schedulerTick` |
| Lease + fencing token claim; expired leases are reclaimed (restart recovery); stale worker commit rejected | `Engine.claim/execute` |
| Docs, revisions, dedup (content hash), cursor advance, run status and outbox event committed in **one** transaction | `Engine.execute` |
| Ordered at-least-once outbox dispatch to the platform (context token aud=`flow-platform`, actor `service_principal`) | `Engine.dispatchOutbox`, `main.ts` |
| Internal HTTP API with context verification + scope checks | `server.ts` |
| Entrypoint `LOOM_ROLE=api|scheduler|all` | `main.ts` |

## Env names (actually read)
Platform: `CANVAS_DATABASE_DRIVER`, `DATABASE_URL`, `CANVAS_BACKEND_ADDR`, `CANVAS_BACKEND_DATA_DIR`, `LOOM_ENGINE_URL`, `FLOW_INTERNAL_JWT_SIGNING_KEY|ISSUER|AUDIENCE`, `FLOW_INTERNAL_TOKEN_TTL_SECONDS`.
Engine: `DATABASE_URL`, `LOOM_ROLE`, `LOOM_PORT`, `LOOM_AUTO_MIGRATE`, `LOOM_SCHEDULER_TICK_MS`, `FLOW_PLATFORM_EVENTS_URL`, `FLOW_INTERNAL_JWT_SIGNING_KEY|ISSUER|AUDIENCE`.

## Known gaps (carried to later phases)
- No UI pages yet (API only). Monitors table exists; monitor preview/apply tools are P2.
- Scheduled runs re-check source status but not yet platform-side durable grants / membership revocation (needs platform grant endpoint) and no budget reservation (P5).
- Engine tests use PGlite (embedded Postgres); not yet run against a real Postgres 16 server or via `docker compose up` (no Docker on build host).
- `/api/internal/flow/events` is reachable through the nginx `/api` proxy; it is token-protected but should be blocked at the edge in production.
- Organization tenants/invites, ServicePrincipal issuance API, RLS second layer: not built.
