# Delivery Report

## P0 — Engineering baseline (2026-10-12, Asia/Shanghai)

| Item | Value |
|---|---|
| Target repository | https://github.com/newyngwieslash-ops/WeaveSense-Flow |
| Branch | `main` (repository was empty; initialized directly, no force-push) |
| Remote commit SHA | the `main` HEAD containing this file; verified with `git ls-remote origin` after push (recorded in the push report) |
| PR / merge state | n/a — initial direct push to empty repo |
| Canvas upstream | ddcat-ai/open-ai-canvas @ e70fe1b9c93d6af3b807a87ed15bb621af78095c (= upstream main at import) |
| Loom upstream | newyngwieslash-ops/WeaveSense @ bec6212ac0cdc67272e488073e35d97087e7447a (= upstream main at import) |
| Phases completed | P0 only |

### What was delivered
- Remotes: `origin`, `canvas-upstream`, `loom-upstream` as specified in the plan.
- Canvas at repo root with full upstream history; WeaveSense imported via `git subtree` into `services/loom-engine/` with history. Both MIT LICENSE files retained; see `THIRD_PARTY_UPSTREAMS.md` (incl. tldraw note).
- `docker-compose.flow.yml`, `.env.flow.example`, `deploy/flow/postgres/init-databases.sh` (two logical DBs, separate roles).
- `docs/flow/architecture.md`, `service-contracts.md`, `roadmap.md`, this report.

### Verification actually executed
| Check | Result |
|---|---|
| Pinned commits reachable in upstreams | yes, both |
| `go build ./...` in `backend/` (go1.24.4) | pass (exit 0) |
| `go vet ./...` in `backend/` | pass (exit 0) |
| `docker compose config` | **not run** — Docker not available on build host |
| Compose YAML parse | not run (no YAML parser on host); validate with `docker compose -f docker-compose.flow.yml config` |
| Frontend (`web/`, loom-engine) lint/typecheck | **not run** (skipped to save quota; requires bun/npm install) |
| Stack boot | **not run** |

### Known open items / blockers
- Compose wiring is a baseline: the upstream images are built unchanged; env var names consumed by the Go backend and Loom Engine (DATABASE_URL, LOOM_ROLE, etc.) must be reconciled with each service's actual config in P1. `LOOM_ROLE` scheduler split does not exist upstream yet.
- Upstream Canvas compose files (`docker-compose*.yml`) and Loom's own `services/loom-engine/docker-compose.yml` are kept untouched for reference.
- No P1+ functionality (tenancy, internal auth, outbox/inbox, intel tools) is implemented.

### P0 push verification (recorded 2026-10-12)
`main` = `6453c151b2830575d1ad2e9bf2977ef199a6310b`, confirmed via `git ls-remote origin` after the token gained `workflow` scope.

## P1 — Tenants + RSS (2026-10-12, Asia/Shanghai)

| Item | Value |
|---|---|
| Branch | `p1-tenants-rss` (from main 6453c151) |
| Implementation commit | `af3ecf23bc7a4eea22707ae98ccfc7a839d773fa` (verified by `git ls-remote`); this report is updated in a follow-up commit on the same branch |
| PR | opened against `main` for review (not merged) — URL in PR list |
| Scope / gaps | `docs/flow/p1-implementation.md` |

### Verification actually executed
| Check | Result |
|---|---|
| `go build ./...`, `go vet ./...` (backend) | pass |
| `go test ./internal/flow/ ./internal/database/` | pass — personal-tenant migration (idempotent), scope isolation, guessed workspace rejected, revoked membership rejected, token sign/verify/expiry/audience/tamper, Go⇄Node token interop, inbox dedupe + monotonic projection + cross-tenant event rejected; existing migration tests pass with schema v51 |
| `go test ./...` (whole backend) | all packages pass **except** 5 `internal/app` CloudAgent Pi-runtime e2e tests; these also fail on unmodified `main` on this host because `backend/agent-runtime/pi/node_modules` is not installed — environmental, unrelated to P1 |
| `node --test flow/*.test.ts` (loom-engine, Node 22.19, PGlite embedded Postgres) | **12/12 pass**: A01 two tenants same feed isolated; A02 guessed foreign source/operation/document ids → 404; A03/A04 replay creates no extra run/outbox event, same key different params → 409; A05 queued job completes after engine restart and expired lease is reclaimed; A06 crash before commit leaves no docs/cursor/completion event; A07 stale worker rejected by fencing token; dedup + revisions; scheduler window key + idempotent ticks + missed-window collapse; fetch failure path; ordered outbox dispatch; HTTP API auth/scope/cross-tenant/202; reuse of WeaveSense RSS parser |
| Compose YAML parse (`yaml` lib) | pass; `docker compose config` / stack boot **not run** (no Docker on host) |
| Real PostgreSQL 16 server run | **not run** (PGlite only) |

## P2 — User Agent + evidence (2026-10-13, Asia/Shanghai)

| Item | Value |
|---|---|
| Branch | `p2-agent-evidence` (from main `ca239238`) |
| PR | https://github.com/newyngwieslash-ops/WeaveSense-Flow/pull/7 (number 7 because Dependabot opened #2–#6), not merged; CI on c5f13443: Backend checks, Web checks, Payment plugin artifacts all pass |
| Scope / gaps | `docs/flow/p2-implementation.md` — **Pi runtime loop not yet wired to intel tools** |

### Verification actually executed
| Check | Result |
|---|---|
| `go build ./...`, `go vet ./...` | pass |
| `go test ./internal/flow/ ./internal/database/ ./cmd/server` | pass — multi-turn context carries prior turns + evidence scope + receipts + background job; apply_monitor waits for approval, executes once on approve, visible via engine monitors API, reject does not execute, other user cannot decide; cross-tenant conversation/workspace/topic/tool calls rejected and engine not called; viewer cannot run write tools; callId replay returns stored result with one engine call, different args → conflict; schema type/unknown-field validation; message pagination |
| Loom Engine `node --test flow/*.test.ts` (Node 22.19 + PGlite) | 15/15 pass (12 P1 + FTS/filter/chunked read, monitor preview→apply idempotent + cross-tenant, cancel) |
| Web `tsc --noEmit` (whole project) | pass |
| Web `eslint` + `prettier --check` on touched files | pass |
| Web `bun test`, full backend `go test ./...`, Docker, real Postgres | not run (quota); full Go suite has 5 known env failures (Pi node_modules) per P1 |

### P2b (agent loop ↔ intel tools)
The Pi agent loop now drives the intel tools for intelligence subjects; approvals go through the Pi chain, evidence and confirmed plans are recorded, and the web chat streams agent replies. Known gaps: no browser e2e against a live model/engine; the manual "Search" button still calls the tool route directly; topic subjects use the same tool table as workspaces.

### P3 (skills, research, reports, briefs)
- Built: versioned skill packs (5 core skills plus the energy-storage industry pack), install and per-run pinning, single-depth research (≤3 per run), reports with claim kinds and citation maps, platform citation validation, brief schedules (cron + timezone, approval through the Pi chain) and scheduler generation that surfaces partial failure, truncation and conflicts, in-app report notifications, and minimal web views.
- Tests:
  - engine `p3.test.ts`: cron/timezone; research depth and limit; reproducibility after a document update; brief idempotency window and partial failure;
  - Go `p3_test.go`: citation rejections (foreign tenant, wrong revision or chunk, out of scope); research child limit; schedule approval; skill pinning; single notification.
- Gaps:
  - research and briefs are extractive (no LLM synthesis);
  - notifications are per workspace (no per-user read state UI);
  - no web UI for skill install;
  - no browser e2e against a live model.

### P4 (managed MCP + complex sources)
- Built:
  - MCP Gateway (official Go SDK): operator approval, schema-hash drift blocking, server-side credential injection with redaction, budget/usage, tenant-scoped connections, agent tool exposure;
  - Telegram source (Telethon incremental + t.me/s fallback, forward-only cursor, media marked unsupported);
  - WeChat OA source via the auth-gateway feed with provider metadata;
  - source revocation;
  - on-demand main-text extraction;
  - connector acceptance records;
  - web pages for MCP connections and source types.
- Tests:
  - Go `mcp_test.go`: credential never in output/prompt/API/stored calls; schema drift blocks until re-approval; tenant isolation; revoke stops calls; budget; validation;
  - engine `p4.test.ts`: Telegram cursor safety (failed run, preview repeats); WeChat gateway; revoked source stops scheduled/queued runs; extraction revisions.
- **A19 live acceptance is PENDING user credentials** (see p4-implementation.md). No RSS test is counted as Telegram/WeChat acceptance.
- Gaps:
  - MCP endpoint SSRF guard is scheme-only (https). There is no private-IP egress policy yet.
  - Only streamable HTTP transport; no OAuth flow for MCP servers.
  - MCP calls aren't idempotent against the remote (A16 reconciliation not done).
  - No media download.
  - The MCP tool snapshot is fixed per run (approval changes apply to new runs; execution still re-checks).

Pending live acceptance and required credentials: see [pending-acceptance.md](pending-acceptance.md).

## P5 — Billing/SaaS, Model Gateway, hardening — final delivery (2026-10-13, Asia/Shanghai)

| Item | Value |
|---|---|
| Target repository | https://github.com/newyngwieslash-ops/WeaveSense-Flow |
| Branches / PRs | `p5-billing-saas` → PR #10 (merged, merge commit `5abe97947157948e3c7a75be8103849963227e59`); `p5-hardening` → PR #11 (merged after green CI, merge commit `07f49467338d671882d98b95fa1806308fffbbf2`, verified with `git ls-remote origin refs/heads/main`); this report update → PR #12 |
| Earlier merges | PR #7 P2b `f7e29746`, PR #8 P3 `339b977c`, PR #9 P4 `937e4ac3` |
| Scope / gaps | [p5-implementation.md](p5-implementation.md), [release-deploy.md](release-deploy.md), [pending-acceptance.md](pending-acceptance.md) |
| Not touched | Dependabot PRs #2–#6; no force-push |

### Acceptance matrix status

| ID | Status | Evidence |
|---|---|---|
| A01 | pass | engine `engine.test.ts`: two tenants with the same feed are isolated. **Plus Postgres RLS on real PG17** (`pg.test.ts`). |
| A02 | pass | Engine: guessed IDs → 404, plus HTTP cross-tenant checks. Go: cross-tenant conversation and tool calls are rejected. RLS hides foreign rows even when an app query asks for them. |
| A03 | pass | Engine replay creates no new run or outbox event. Go: callId replay. Budget `Reserve`/`Allocate` are idempotent per key, and a double settle has no second ledger effect. |
| A04 | pass | Same key with different parameters → 409. |
| A05 | pass | A queued job survives an engine restart. Pi runs: DB lease (`flow_run_leases`) with takeover after expiry. |
| A06 | pass | A crash before commit leaves no cursor advance and no documents. |
| A07 | pass | Fencing token rejects a stale worker. Run lease: a stale owner's renew fails, which cancels its session. |
| A08 | pass (P7: HTTP + browser smoke on the compose stack) | Jobs run in the background. Agent run events are persisted and replayable. |
| A09 | pass | Multi-turn context carries prior turns and evidence scope. |
| A10 | pass | Approve, reject and expire are handled; only an approved action executes, exactly once. |
| A11 | pass | Briefs and reports surface truncation and source failures. LLM synthesis drops claims with invalid citations, and the engine re-validates. |
| A12 | pass | A new revision is created and old citations stay readable. |
| A13 | pass | A 429 or transient failure triggers exponential backoff. Invalid credentials pause the source. Paused or backing-off sources get no new scheduled runs (`p5.test.ts`). |
| A14 | pass (live MCP server pending-credentials) | Schema drift blocks until re-approval. Tools that are not approved don't execute. |
| A15 | pass (P6) | `RunSlots`: per-user agent-run and per-tenant research-executor limits (`FLOW_MAX_CONCURRENT_RUNS_PER_USER`, `FLOW_MAX_CONCURRENT_RESEARCH_PER_TENANT`); busy => FIFO queue, release starts the next run, cancel removes a queued run, a cancelled waiter leaves the queue; costs visible in billing (`p6_test.go`). |
| A16 | pass (P6) | Model Gateway: unknown → reconciled. MCP: `ExecuteKeyed` per tool call key (sent to the server as `_meta.idempotency_key`); a lost response becomes `unknown`; retries reconcile first via the connection's `reconcile_tool` and re-execute only on `not_found`; with no reconcile tool, the call is never retried; credits are charged once (test: server executed once, billed once). |
| A17 | pass | An insufficient budget or quota blocks new reservations. A revoked MCP connection or source stops new calls, and old evidence stays readable. |
| A18 | pass (P7: object storage round trip on live MinIO added) | `pg.test.ts` on real PG17: backup → restore into an empty DB; the manifest (row counts and checksums) matches, app reads work, and there is no re-migration. |
| A19 | **pending-credentials** | Telegram and WeChat live samples: see pending-acceptance.md. Deferred to the very last step. |
| A20 | pass | Each PR merged after green CI, and the remote `main` SHA was verified with `git ls-remote`. |
| A21 | pass (P6; P7 generalized to every async intel tool) | `flow_external_waits`: `intel_collect_once` parks the run (checkpoint = run request, reservation ref) on its operation. The deduplicated inbox event marks it ready (and completion consumed before parking is picked up from the projection after a restart). The resumer claims each wait with a conditional update, re-authorizes the grant (revoked → `denied`), and creates exactly one continuation run (idempotency key `autocontinue:<wait>`). Test: 3 duplicate deliveries + 2 resumer passes → 1 resume. |

### Verification actually executed (P5)
| Check | Result |
|---|---|
| `go build ./...`, `go vet` (flow, app, cmd) | pass |
| `go test ./internal/flow ./internal/database` | pass. Covers: budget reserve, allocate, settle and release; child over-allocation rejected; root with open children can't close; unknown → reconcile; quota; Synthesize drops invalid citations, and an unknown outcome is marked; org, shared billing account, single-use invite; a removed member's grant is denied; egress blocks loopback, private and metadata addresses (including a real loopback dial); run lease takeover and fencing; canvas conversation doc optimistic concurrency and per-user isolation. |
| `go test ./internal/app` | Everything passes except the 5 known Pi-runtime e2e tests (missing Pi `node_modules` on this host; CI runs them) |
| Engine `node --test flow/*.test.ts` (PGlite) | 29 pass, 3 skipped (the real-PG tests) |
| Engine `pg.test.ts` on **real PostgreSQL 17**, app role `flow_app` (non-superuser) | 3/3 pass: RLS isolation, HTTP API end to end under RLS, backup/restore consistency |
| Web `tsc --noEmit`, eslint, changed-file formatting, `bun test` (touched suites) | pass |
| CI on PR #10 / #11 | Backend checks, Web checks, Payment plugin artifacts: green before merge |
| Docker stack boot | **not run** (no Docker on host). Compose changes were reviewed only. |

### Remaining gaps
- A21 continuation runs are created server-side but have no browser e2e; only `intel_collect_once` parks (other async tools return operation ids without auto-continue). A16 MCP reconciliation depends on the remote server offering a reconcile tool; without one, unknown calls stay unknown for an operator to resolve. A15 concurrency limits are off by default (env = 0).
- The Model Gateway is a synchronous OpenAI-compatible client. It is not routed through the canvas async task/channel pipeline, and the gateway offers no status-lookup API, so unknown outcomes are billed at the reserved amount after 24h.
- RLS applies to the engine database. The platform database relies on application scoping.
- The canvas sidebar keeps its own conversation format: it is now stored server-side, but it is not unified with the intelligence `flow_agent_conversations`.
- Org UI is minimal: create, invite, accept. There is no member list or role editing UI, and no per-user notification read state (read state is per workspace).
- A19 live connector acceptance is pending user credentials and is the very last step.
- There is no browser e2e and no Docker stack boot.

## P6 — Acceptance gaps A15/A16/A21 (2026-10-13, Asia/Shanghai)

| Item | Value |
|---|---|
| Branch / PR | `p6-acceptance-gaps` → PR #13 (merged after green CI; merge SHA in the hand-off report, verified with `git ls-remote`) |
| Schema | platform v57 `flow_acceptance_gaps` (`flow_mcp_calls`, `flow_run_slots`, `flow_external_waits`) |
| Tests | `backend/internal/flow/p6_test.go`: A15 FIFO queue, per-owner isolation, release → next, queued cancel, cancelled waiter; A16 lost response → unknown → reconcile without re-execution, billed once, key reuse with different args rejected, no reconcile tool → never retried; A21 idempotent park, duplicate completion events, repeated resumer, completion before park, revoked grant denied. Plus `go test ./internal/flow ./internal/database ./cmd/...` all pass; `internal/app` only the 5 known Pi e2e env failures. |

## P7 — Close remaining development gaps (2026-10-13, Asia/Shanghai)

| Item | Value |
|---|---|
| Branch / PR | `p7-close-gaps` (merged after green CI; the merge SHA is recorded by the follow-up docs PR) |
| Schema | platform v58 `flow_gaps_p7`: `flow_notification_reads`, `flow_oauth_tokens`, Postgres RLS policies on platform flow tables |

### Closed gaps
| Gap | Resolution |
|---|---|
| Auto-continue only for `intel_collect_once` | Every `intel_*` tool whose output carries a non-terminal `operation_id`/`operation` now parks (research, extract, brief, collect, …). `intel_get_operation` and `intel_cancel_operation` never park, and terminal outputs are not parked. |
| MCP unknown calls need an operator path | `GET /intelligence/mcp/calls?status=unknown` and `POST /intelligence/mcp/calls/:id/resolve {outcome: succeeded\|not_executed}`. Admin only; charged once; cross-tenant access → 404. UI: "Unknown-outcome calls" on the MCP page. |
| Model Gateway has no status path | Requests carry `Idempotency-Key`. With `FLOW_MODEL_GATEWAY_STATUS_URL` set, unknown reservations are reconciled after 10 min (`succeeded` → settle at actual tokens; `not_found`/`failed` → release; `pending` → keep). The 24 h conservative charge stays as a last resort. Operator resolve: `POST /intelligence/billing/reservations/:id/resolve`; the billing UI has "Mark executed / not executed". |
| Platform DB has no RLS | v58 enables and forces RLS with a `tenant_isolation` policy on 11 platform tables (conversations, messages, tool calls, notifications, MCP connections/usages/calls, reservations, invites, external waits, skill installs). `flow.TenantTx` sets `app.tenant_id` per transaction; an empty value is system context. Verified on real Postgres 17 as a non-superuser (`TestFlowRLSPostgres`): foreign rows are hidden, a cross-tenant update affects 0 rows, and a cross-tenant insert is rejected. |
| Canvas sidebar conversations are separate | A canvas document save mirrors each sidebar conversation into the shared directory (`subject=canvas`, stable id, new user/assistant messages appended idempotently). The intelligence list shows them tagged "canvas". |
| Org UI has no member list or role editing | `GET /intelligence/members`, `PATCH /intelligence/members/:userId {role: admin\|member\|viewer}`. Owners are immutable, the last admin is protected, and only admins can edit. Workspace roles follow (viewer loses write scopes). UI: a "Members" card with a role selector. |
| Notification read state is per workspace | Read state is now per user (`flow_notification_reads`): "mark all read" affects only the caller. |
| No topic-specific tool sets | Topic subjects get an analysis-only set: search, read, extract, get/cancel operation, load skill, research, save report. No monitors, schedules, collection or MCP. Enforced in both the Pi tool table and `Tools.Invoke` (`ErrToolNotInSubjectSet`). |
| Web run-end detection relied on heuristics | The SSE stream sends an explicit `run_terminal {runId,status}` event before closing. The client emits it and stops; the intelligence page finishes on it (a `run_status` fallback remains for older servers). |
| Object storage backup/restore untested | `deploy/flow/backup/objects-{backup,restore,test}.sh`: mirror plus a sha256 manifest, and a restore that re-downloads and verifies end to end. Tested on the live compose MinIO: 3 objects across 2 buckets, bucket deletion → restore verified, and a tampered backup is refused. |
| Telegram media not downloaded | The Telethon worker's `save_media` downloads with `message.download_media` and enforces `TELEGRAM_MEDIA_MAX_BYTES` (checked before and after download) and `TELEGRAM_MEDIA_TIMEOUT_SECONDS`. It uploads to the intel bucket (`telegram/<channel>/<id>.<ext>`) and reports `media_status` saved/none/disabled/too_large/timeout/failed. The engine maps this to stored/too_large/timeout/failed/disabled and counts only non-stored media as unsupported. Covered by 5 fixture tests (`test_media.py`) and an engine test. Compose has an optional `telethon` service (`--profile telegram`). |
| MCP OAuth | credentialRef `oauth:FLOW_MCP_OAUTH_<NAME>` uses the client-credentials grant from server env, with the token cached server-side (`flow_oauth_tokens`) until 60 s before expiry. Never returned by the API. Tested against an httptest token server: basic auth, scope, caching, wrong secret rejected. |
| No Docker boot / e2e | Docker 26 + compose 2.26 installed on the build box and the full stack built and booted. Upstream MinIO images are no longer pullable, so the defaults were switched to `cgr.dev/chainguard/minio` and `bitnamilegacy/minio-client` (overridable). `deploy/flow/smoke.sh` passed 16/16 over HTTP through nginx. `deploy/flow/browser-smoke.mjs` (headless Chrome) passed 6/6 and rendered the intelligence and MCP pages with no page errors. |
| `.env.flow.example` pointed the Model Gateway at a nonexistent internal URL | Now empty by default, with documented gateway, status, OAuth, Telegram media and connector variables. |

### Verification actually executed (P7)
| Check | Result |
|---|---|
| `go vet ./...`, `go test ./...` | pass. The only failures are the 5 known `internal/app` Pi e2e tests (local Pi node_modules missing; green in CI). New: `p7_test.go` (7 tests). |
| `TestFlowRLSPostgres` on real Postgres 17 (non-superuser `flow_app`) | pass |
| Engine `node --test flow/*.test.ts` with real-PG tests enabled (PG17 + `flow_app`) | 33/33 pass |
| Telethon worker `python3 -m unittest test_media test_history` | 10/10 pass |
| Web `tsc --noEmit`, eslint, prettier (changed files), `bun test` | pass (2220 tests) |
| Compose stack: build + boot + `smoke.sh` + `browser-smoke.mjs` + `objects-test.sh` | pass (16/16, 6/6, round trip + tamper) |

### Remaining (honest)
- **A19** live Telegram/WeChat (and live MCP) acceptance needs your credentials. Follow `docs/flow/local-acceptance.md`.
- Platform RLS is defense in depth. It applies only when the platform connects as a non-superuser role (the compose `PLATFORM_DB_USER` is not a superuser) and inside `TenantTx`. Notification handlers use `TenantTx`; other handlers still run in system context and rely on application scoping, as before.
- The Model Gateway is still a synchronous OpenAI-compatible call (now idempotency-keyed and status-reconcilable). It is not routed through the canvas async task pipeline.
- The canvas mirror is one-way (canvas → directory). Canvas conversations are read-only in the intelligence page and continue in the canvas sidebar.
- MCP OAuth supports the client-credentials grant only. Interactive authorization-code flows are not implemented.
- The browser smoke checks rendering and API wiring. It does not drive an LLM agent run (no model credentials on the build box).
- The build box needed an `iptables-legacy` FORWARD rule for the compose bridge (a host quirk; documented in local-acceptance.md).

### P7 merge record
- PR #14 (`p7-close-gaps`) merged 2026-10-13 (Asia/Shanghai) after green CI (Backend checks, Web checks, Payment plugin artifacts). Merge commit: `7a47be888fe1d0b540f61bcacf7ab600b2d2546e`, verified on `main` with `git ls-remote`.
- Note: `smoke.sh` without `SMOKE_USER`/`SMOKE_PASSWORD` fails once a first user exists, because registration is closed by default after that. Pass credentials for an existing account.

## P8 — Runtime RLS, async Model Gateway, two-way canvas sync, MCP OAuth code flow (2026-10-13, Asia/Shanghai)

| Item | Value |
|---|---|
| Branch / PR | `p8-runtime-hardening` (merged after green CI; the merge SHA is recorded by the follow-up docs PR) |
| Schema | platform v59 `flow_runtime_p8`: `flow_model_tasks`, `flow_mcp_oauth_grants`, `flow_oauth_states`, `external_id` columns on conversations/messages, and an RLS policy based on `app.tenant_ids` (covering the new tables too) |

### Delivered
| Item | Resolution |
|---|---|
| Platform RLS at runtime | `flow.TenantMiddleware` wraps every authenticated flow route (`/intelligence/*`, `/agent/conversations*`, `/agent/canvas-conversations*`, MCP, billing, members, model tasks) in one transaction. That transaction sets `app.tenant_ids` to the caller's active tenants, and every handler uses the request transaction (`reqDB`). It commits on status < 500 and rolls back otherwise. Engine callbacks (`/internal/*`) and background workers stay in system context. Proof on real Postgres 17 as a non-superuser (`TestRuntimeRLSBlocksUnfilteredHandlerQueries`): a deliberately unfiltered handler counts only the caller's rows, and its unfiltered `UPDATE` cannot touch another tenant's row. Real handlers (conversations, cross-tenant 404, notifications, members) keep working under RLS. |
| Async Model Gateway | `flow_model_tasks` pipeline. Enqueue is idempotent per (tenant, key) and creates or allocates the reservation. Work is claimed with a conditional update and a lease; `Idempotency-Key` equals the task key. 429 → re-queued with exponential backoff, up to `max_attempts`. 5xx or network loss → `unknown` (reservation marked unknown). An expired lease (worker crash) → `unknown`. Reconciliation goes through the status endpoint: `succeeded` settles (optionally with the returned content); `not_found` reopens the reservation and re-queues under the same key. Settlement happens exactly once. A background worker (`RunModelTaskWorker`) runs the queue; `Synthesize` enqueues and `Wait`s, executing due attempts inline. Status API: `GET /intelligence/model-tasks/:id`. Tests: retries + single charge; 5 concurrent claims → 1 execution; unknown → not_found → re-queue → success; lease expiry → reconciled with content. |
| Two-way canvas ↔ intelligence sync | Canvas → directory: messages are keyed by sidebar message id (`external_id`); rows mirrored before v59 are backfilled by position. Directory → canvas: user/assistant turns added from the intelligence page (agent runs on a `canvas` subject with `conversationId` run server-side with intelligence tools) are written into the sidebar document as `dir_<id>` messages. The write uses version CAS and is retried on conflict, and other document fields are preserved. Re-imported `dir_*` messages are recognised, not duplicated. The canvas client's 409 merge is now message-level (union by id, order-preserving), so a stale canvas save cannot drop intelligence turns. Tests: Go round trip with conflict (`canvas_sync_test.go`); web merge test. |
| MCP OAuth authorization code + PKCE | credentialRef `oauthcode:FLOW_MCP_OAUTHCODE_<NAME>` (AUTH/TOKEN/REVOKE/REDIRECT URLs, client id/secret, scope from server env). Admin start → S256 challenge, single-use state bound to the user (10 min). Callback `GET /api/intelligence/mcp/oauth/callback` exchanges the code with the verifier. Tokens are AES-256-GCM encrypted (`FLOW_OAUTH_ENC_KEY`, AAD bound to connection and kind) and refreshed 60 s before expiry under a row lock (rotation honoured; `invalid_grant` → re-authorization required). Revoke follows RFC 7009 for the refresh and access tokens, then deletes the grant. The MCP page has Authorize / Re-authorize / Revoke OAuth controls. Tested against a mock OAuth server: wrong user, state replay, cleartext check, refresh rotation, wrong key, editor denied, revoke. |

### Remaining (honest)
- **A19** live acceptance still needs your credentials (`docs/flow/local-acceptance.md`).
- **Canvas subject from the intelligence page:** a run started there uses intelligence tools only. Canvas-editing tools need the live canvas snapshot and are available only from the canvas sidebar.
- **Message deletion is not synced.** Edits or deletions of individual messages in the sidebar are not propagated; the sync is append-only.
- **Agent-runtime internals still bypass RLS.** The Pi tools, auto-continue resumer and billing maintenance run in system context; they are scoped by application code and protected by the tenant checks in `Tools.Invoke`.
- **No real OAuth provider tested.** OAuth was verified only against the mock server; a real provider needs your client registration.
- **No async status for the engine's synthesis call.** Model tasks are pollable via the status API, but `/internal/flow/summarize` still waits for its own task synchronously (within the request context).

### P8 merge record
- PR #16 (`p8-runtime-hardening`) merged 2026-10-13 (Asia/Shanghai) after green CI (Backend checks, Web checks, Payment plugin artifacts). Merge commit: `ab257d7be258b5da076a2d5e5ff02cca60252b0c`, verified on `main` with `git ls-remote`.
- Compose stack on the build box at this commit: schema v59. The platform DB role `flow_platform` is not a superuser, so RLS is live; the `tenant_isolation` policies are present. `smoke.sh` 16/16 (register, then re-run with login), `browser-smoke.mjs` 6/6.

## P9 — Canvas tools from intelligence, edit/delete sync, background RLS, async summarize (2026-10-13, Asia/Shanghai)

| Item | Value |
|---|---|
| Branch / PR | `p9-close-gaps` (merged after green CI; the merge SHA is recorded by the follow-up docs PR) |
| Schema | platform v60 `flow_message_tombstones_p9`: `revision`, `synced_revision`, `edited_at`, `tombstoned_at` on `flow_agent_messages` |

### Delivered
| Item | Resolution |
|---|---|
| Canvas tools from the intelligence page | Continuing a canvas conversation from the intelligence page now sends `canvasId` = subject id, so the run is a normal canvas run with the full canvas tool set. Those tools act on the server-side canvas document (`CanvasProject`), check ownership with `CanvasProjectForUser`, and make revisioned, conflict-checked writes that emit `canvas_updated`. The directory conversation is re-checked for ownership and subject match (a foreign or mismatched canvas/conversation pair is rejected). The user and assistant turns are recorded in the directory and written back to the sidebar. Test: `TestCanvasConversationContinuedFromIntelligenceUsesCanvasTools`. |
| Message edit/delete sync | `PATCH` / `DELETE /api/agent/conversations/:id/messages/:msgId` (user and assistant messages only). An optional `baseRevision` gives optimistic concurrency and returns 409 on conflict; repeating a request is idempotent. A delete leaves a tombstone, hidden from listings and the agent context. Directory → canvas: write-back uses version CAS with retry. It applies the edit in place, removes the deleted message, and adds its id to the conversation's `deletedIds`; `synced_revision` is advanced under a revision guard. Canvas → directory: edits are mirrored with a revision bump. Deletes are mirrored only from explicit `deletedIds`: a message missing from a stale replica is never treated as deleted, and a stale replica cannot bring a deleted message back. While a directory-side change is still waiting for write-back, the directory version wins. Web: `mergeDocuments` honours tombstones from both sides; `deleteConversationMessage` and `editConversationMessage` helpers; edit and delete controls on the intelligence page. Tests: `TestCanvasTwoWayEditDeleteTombstones` and a web merge test. |
| Background RLS | The agent tool bridge (`executeIntelTool`, which covers receipts, writes, parking and the transcript), conversation context/turn recording, confirmed plans and budget settlement run in `flow.UserTx`. That is the background equivalent of `TenantMiddleware`: `app.tenant_ids` is set to the user's tenants. The auto-continue grant re-check runs in `TenantTx(wait.tenant)`, and the continuation run records through `UserTx`. Billing maintenance discovers tenants with open reservations, then sweeps and reconciles each one in its own `TenantTx`. Test on real Postgres 17 as the non-superuser owner (`TestBackgroundWorkIsTenantScoped`): an unfiltered background query and update see and touch only the user's tenant, all tenants' stale reservations are closed per tenant, and a wait for a tenant the user cannot act in is denied. |
| Async engine summarize | `/internal/flow/summarize` no longer waits on the model. It enqueues the model task (idempotent per key) and returns `202 {task_id, operation:{id, kind:"model_task", status}}`. The model task worker completes the task (retries and reconciliation as in P8). The engine parks the brief window: no report is saved and the schedule is not advanced (`pendingSummaries`). The next scheduler tick re-submits the same key and receives the citation-validated result (200), or a 502 that falls back to the extractive brief. Tests: Go `TestSummarizeIsAsyncModelTask` (202 → idempotent 202 → worker → 200, one task); engine `brief summarization is non-blocking…`. |
| Docs | `local-acceptance.md` §5.3.1 has the steps for MCP OAuth with a real provider (registration, env, authorize, refresh, replay/revoke negatives, log check) plus an A19 checklist line. |

### Remaining (honest)
- **A19** live acceptance still needs your credentials. That includes the real OAuth provider run (§5.3.1); OAuth is still verified only against the mock server.
- **Canvas sidebar edit/delete controls.** The canvas sidebar UI has no edit or delete buttons yet. The sync supports them (helpers exported and tested), but sidebar edits reach the directory only once that UI is added. Edit and delete from the intelligence page are wired in.
- **Engine summarize continuation is tick-driven.** The parked brief window is completed by the next scheduler tick (default 15 s), not by a push event from the platform.
- **Agent-run summarize.** The agent's research synthesis tool still uses `Synthesize` (enqueue, then wait inside the tool call); only the engine path is non-blocking.

### P9 merge record
- PR #18 (`p9-close-gaps`) was merged on 2026-10-13 at 22:34 Asia/Shanghai after green CI (Backend checks, Web checks, Payment plugin artifacts). Merge commit: `895382744800221541bbd3c61285ed47bd3e378f`.
- Compose stack on the build box at this commit: schema v60 (the `revision`/`tombstoned_at` columns are present); `smoke.sh` 16/16; `browser-smoke.mjs` 6/6.
- Local tests:
  - `go vet` passes. `go test ./...` passes everything except the 5 known Pi e2e tests in `internal/app`, which need the Pi `node_modules` (they pass in CI). The RLS tests ran against real Postgres 17 as a non-superuser.
  - Engine `test:flow` 34/34 and typecheck pass. The full engine `npm test` has 1 failure, `service-env-least-privilege` ("runtime requires pids_limit: 64"); it fails the same way on the base commit `c36991da` and is unrelated to P9.
  - Telethon worker unittest passes.
  - Web: tsc and eslint pass, `bun test` 2222/2222, and Prettier passes on the changed files.

## P10 — Sidebar edit/delete, push completion, async research, smoke, test/format hygiene (2026-10-13, Asia/Shanghai)

| Item | Value |
|---|---|
| Branch / PR | `p10-close-gaps` (merged after green CI; the merge SHA is recorded by the follow-up docs PR) |
| Schema | platform v61 `flow_model_task_push_p10`: `context` and `notified_at` on `flow_model_tasks` |

### Delivered
| Item | Resolution |
|---|---|
| Canvas sidebar edit/delete | The canvas agent panel has per-message **编辑 / 删除** controls (user and assistant messages; hidden while a run is busy; delete needs a confirming second click). An edit updates the text, and the newer copy wins in `mergeDocuments`. A delete removes the message and records a tombstone in `deletedIds`. The panel's conversation rebuild keeps the tombstones. Both reach the directory through the existing canvas-document sync (P9: mirrored edits with a revision bump, explicit tombstones). Tests: `test/agent-message-edit-controls.test.tsx`. |
| Push-based brief completion | The model task worker calls `ModelTasks.NotifyDone` on every pass. Each finished task is pushed once: the `notified_at` claim prevents repeats, and a failed push is retried on the next pass. For a brief task, the push is `POST /v1/briefs/wake {key}` to the engine (signed context, `intel.reports.write`), which completes that schedule's parked window right away (`briefTick` limited to tenant and schedule; idempotent with the scheduler). The scheduler tick stays the fallback. Tests: Go `TestResearchSynthesisIsAsync` (brief wake-up reaches the engine); engine `pushed wake-up completes the parked brief window via the API` (403 without scope, 400 for a bad key, 200 completes the window, a repeat makes nothing). |
| Non-blocking research synthesis | `intel_start_research` enqueues the synthesis model task and returns `synthesis: {status, task_id, operation:{id, kind:"model_task"}}` without waiting (a finished task on replay is returned inline). The agent run parks on the task (`asyncOperationID` recognises a queued synthesis). When the task finishes, `NotifyDone` marks the wait ready; `ResumeReady` also checks `flow_model_tasks` as a fallback. Auto-continue then resumes the run. `intel_get_operation` on an `mt_…` id returns the task status and, once it has succeeded, the citation-validated synthesis rebuilt from the stored context. Tenant-checked. Tests: `TestResearchSynthesisIsAsync`, `TestResearchParksOnQueuedSynthesisTask`. |
| Compose smoke | `smoke.sh` now has 25 checks. New ones: directory messages, edit (200), stale-revision edit (409), edit synced into the canvas document, delete, repeated delete (200), and the delete synced as a tombstone (`deletedIds`) with the text gone. |
| Engine test `service-env-least-privilege` | The test was wrong, not the config. `opencode-runtime` uses `pids_limit: 512` on purpose (the compose comment documents a SIGABRT at 64). The test now requires a pid cap in (0, 1024] and allows the non-secret `OPENCODE_MAX_OUTPUT_TOKENS`. Full engine `npm test` passes. |
| Prettier | `prettier --check .` passes in `web/`. Removed `src/styles/globals-1..7.css`: stale split fragments of `globals.css`, imported nowhere and not parseable as standalone CSS. Formatted the repository. Ignored `public/` (minified vendor bundles). 34 files whose exact source text is asserted by source-text contract tests keep their hand formatting and are listed in `.prettierignore` with that reason. |

### Remaining (honest)
- **A19** live acceptance still needs your credentials, including the real OAuth provider run in `local-acceptance.md` §5.3.1.
- **Source-text contract tests.** 34 files are excluded from Prettier because about 15 tests match exact source snippets. Making those files formattable requires rewriting the tests against behaviour.
- **Brief wake-up delivery.** The wake-up is best-effort, at least once per finished task. If the engine is unreachable for long, the platform keeps retrying each pass and the scheduler tick still completes the window.
- **Research synthesis budget.** It is allocated when the task is queued. If the agent never reads the result, the task still completes and is billed (as before).

### P10 merge record
- PR #20 (`p10-close-gaps`) was merged on 2026-10-13 at 23:09 Asia/Shanghai after green CI (Backend checks, Web checks, Payment plugin artifacts). Merge commit: `de99b15b8b446485cfc70e24b746bd0032756137`.
- The first CI run failed on a Bun module-cache collision: a test imported `canvas-cloud-agent-panel-parts.tsx` as text while the new test imported it as a module. Moving the controls into `canvas-agent-message-edit-controls.tsx` fixed it.
- Compose stack on the build box: schema v61. `smoke.sh` 25/25; `browser-smoke.mjs` 6/6.
- Local tests:
  - `go vet` passes. `go test ./...` passes except the 5 known Pi e2e tests in `internal/app`, which pass in CI. The RLS tests ran on real Postgres 17 as a non-superuser.
  - Engine full `npm test` passes, as do `test:flow` 35/35 and typecheck.
  - Web: `prettier --check .`, tsc, eslint and `bun test` 2224/2224 all pass.

## Loom upstream sync — collection capability (2026-10-14, Asia/Shanghai)

| Item | Value |
|---|---|
| Branch / PR | `sync-loom-upstream` (merged after green CI; merge SHA recorded by the follow-up docs PR) |
| Upstream | `loom-upstream` `codex/collection-capability` @ `cdcb787f` (28 commits on top of the previous pin `bec6212a`; upstream `main` unchanged) |
| Method | `git subtree pull --prefix services/loom-engine loom-upstream codex/collection-capability` (no squash, history kept) |
| Conflicts | `package.json` (kept Flow `test:flow`/`flow:start` and added upstream `test:collection`/`qa:collection:*`/`test:subtitles:isolation`); `scripts/service-env-least-privilege.test.mjs` (kept Flow's P10 pid-cap assertion, which upstream's literal `pids_limit: 512` would have reverted). All other files merged cleanly; `flow/`, tenancy/RLS, cursor/lease, egress guard and Telegram `save_media` were untouched by upstream. |

### Upstream capabilities (bec6212a..cdcb787f)
- **Bounded feed ingestion:** every entry in a bounded feed is kept, keyword search sources are merged fairly, the discovery anchor is the newest finite window, invalid-date windows are finalized, and feed refusals are classified. Window coverage is recorded (migration `0030_collection_coverage`).
- **Article enrichment:** bounded public article candidates come from a hosted reader with an exact-host allowlist and public-DNS checks. Adoption uses version checks, a fresh cache is reused after lease admission, and the article is shown in item details.
- **YouTube subtitle enrichment:** an isolated Python `subtitle-worker` plus an authenticated `subtitle-egress` proxy. The 2 MiB json3 output is parsed server-side, the extraction deadline is preserved, and the transcript is appended to the item.
- **Outbound-policy hardening:** undeclared IPv6 benchmarking ranges are blocked, stream cancellation cleanup is bounded, and article validation stops on ambiguous DNS barriers.
- **Validation tooling and docs:** portable collection verification, GitHub-release and public-feed observation scripts, channel/subtitle pilots, and a capability acceptance doc.

### Integrated into Flow
| Capability | How |
|---|---|
| Feed fixes and outbound-policy hardening | Automatic: Flow's RSS connector already reuses upstream `fetchRss`/`parseFeedXml` and the outbound-policy guarded fetch. |
| Article reader | `POST /v1/documents/extract {"via":"reader"}` uses upstream `readArticle`. The platform proxy is `POST /api/intelligence/documents/extract`, and the tool `intel_extract_document` gains a `via` parameter. Opt-in with `ARTICLE_READER_ENABLED` and `ARTICLE_READER_ALLOWED_HOSTS`. The document is resolved under tenant/workspace before any outbound call, and the result becomes a new revision (re-chunked). |
| YouTube transcripts | `POST /v1/documents/transcript` uses the upstream worker contract with upstream `youtubeVideoId`/`transcriptText`. The platform proxy is `POST /api/intelligence/documents/transcript`, plus a new tool `intel_transcribe_document` (`intel.collect`, also allowed in topic conversations). Compose gets `--profile subtitles` (an isolated `subtitle-worker` and a `subtitle-egress` on the internal network). New `.env.flow.example` keys. |
| Revision fencing | Enrichment revisions are written with `UPDATE … WHERE current_revision=$read`, so a concurrent enrichment gets 409 instead of a lost update. HTML extraction shares the same path. |
| Rename | The new upstream text does not contain the old brand name, so nothing needed renaming to 源铭振跃. |

Tests: `flow/enrichment.test.ts` (4 tests: tenant isolation with no outbound call for a foreign tenant, idempotency, error mapping, worker-contract checks, 2 MiB cap, searchable transcript) and Go `TestEnrichmentToolsRouteToEngine`. `smoke.sh` gains 3 platform→engine enrichment checks.

### Not integrated (and why)
- **Loom-native item UI and collection coverage** (`inbox.$itemId.tsx` article panel, `jobs.tsx`, migration `0030` on the `items` table). These belong to the bundled WeaveSense app, which Flow does not deploy. Flow's documents and revisions are the equivalent surface. The files are present in the subtree, unused by Flow.
- **`codex/collection-reliability`** (39 commits, Oct 4–5). It forked before `bec6212a`, was never merged into upstream `main`, and is not an ancestor of `collection-capability`. Pulling it would reintroduce a stale divergent line, so it waits until upstream merges it.
- **Validation runners** (`validation/collection/b03-*`, `c02-*`). These are upstream's own live/Windows acceptance harnesses. Their offline self-checks run in `npm test` (`test:collection`).

### Loom upstream sync merge record
- PR #23 (`sync-loom-upstream`) was merged on 2026-10-14 at 08:08 Asia/Shanghai after green CI (Backend checks, Web checks, Payment plugin artifacts). Merge commit: `52affddf7c0f8121aecdde783bfcb775a443800f`.
- Compose stack on the build box: `smoke.sh` 28/28 (25 earlier checks plus 3 enrichment checks). The `--profile subtitles` config validates.
- Local tests:
  - Engine full `npm test` passes, including upstream `test:collection`. `test:flow` 39/39 (the RLS and backup tests ran on real Postgres 17 as a non-superuser). `tsc --noEmit` and eslint pass.
  - `go vet` passes. `go test ./...` passes except the 5 known Pi e2e tests in `internal/app`, which pass in CI.
  - Web: tsc and `bun test` 2224/2224 pass.

## Loom collection-reliability port (2026-10-14, Asia/Shanghai)

| Item | Value |
|---|---|
| Branch / PR | `cherry-pick-collection-reliability` (merged after green CI; the merge SHA is recorded by the follow-up docs PR) |
| Upstream | `loom-upstream` `codex/collection-reliability` @ `ff42cd5a` (39 commits, Oct 4–5). It forked at `c49829f3`, before the pinned `bec6212a`, and upstream never merged it. |
| Method | `git cherry-pick -X subtree` conflicted in almost every file, because each commit builds on earlier commits in the branch that upstream `main` reworked differently. So the port is file-level 3-way merges (base `c49829f3`, ours = current subtree, theirs = `ff42cd5a`) for the components Flow runs, plus hand ports into the current egress guard. Newer `collection-capability` behaviour wins where the two disagree. |
| Schema | Flow engine `0008_feed_validators`: `feed_etag`, `feed_last_modified`, `feed_validator_identity` on `flow_source_cursors` (tenant-scoped, RLS unchanged); `items_rejected` and `not_modified` on `flow_collection_runs`. |

### Integrated
| Commit | What Flow gets |
|---|---|
| `6d9aadb3` preserve feed entries, reject invalid feeds | Feed parser: one bad entry is counted and skipped instead of failing the feed (`feed_no_usable_entries` only when every entry is bad); `http(s)`-only links resolved against the final redirect URL; strict single-root RSS/Atom/RDF; overlong IDs rejected instead of truncated. The WeChat gateway primary feed gets strict roots and final-URL links. **Not taken:** rejecting entries with invalid dates, because `collection-capability` (`b8cce07b`) keeps them and finalizes those windows downstream. Error names keep the mainline spelling (`invalid_feed_xml` / `invalid_feed_root`). |
| `cdd53353` legacy Atom link identities | Self/enclosure links stay the identity (no duplicate documents), while only article links become clickable URLs. |
| `235466b9` conditional requests | Flow RSS sends `If-None-Match` / `If-Modified-Since` from validators stored on the cursor row. A 304 commits an empty run (`not_modified=true`). Validators are written in the same commit as the cursor, keyed to URL + platform + parser version, so a failed run or a parser change never skips content. `publicFetch` gains `allowNotModified` (first hop only). |
| `d0602ef0` bound requests (partial) | Redirect header hygiene in `publicFetch`: validators never follow a redirect, and auth/cookie/token/key headers are dropped on cross-origin hops. The rest (`CollectionHttpError`, metrics, collector budgets) belongs to the bundled app's collectors, which Flow doesn't run. |
| `25e19ea9`, `96733504`, `1e260c4f` Telethon worker reliability | Worker taken at `ff42cd5a`: connection lifecycle with bounded reconnect backoff (no auto-reconnect storms), 50 s history deadline, monotonic FloodWait cooldown shared by history and media, service messages consumed (no page loops), `has_more`, `membership_required` / `unavailable` / `timeout` statuses, and an admission recheck after storage recovery. 29 worker tests (`python -m unittest test_history`). |
| `554599c0` async Telegram attachments, `096ece89` audio | History returns `telegram://<channel>/<id>` refs. Flow then stores each one through the worker's authenticated `/v1/media` (`TELEGRAM_MEDIA_TOKEN`; streamed, size-checked, sha256), at most 10 per run within a 90 s budget, and stops at a flood wait. The stored object key goes into the document's media. `too_large` / `timeout` / `disabled` / `failed` keep a reason, and the body notes uncollected attachments as before. Old workers that report `media_status` inline still work. `audio` is now a media type. This replaces Flow's inline `save_media`, which upstream identified as able to exhaust the 60 s history budget. |
| `10b5c443`, `4b7ff349` WeChat freshness vs availability | Gateway: optional `expected_update_hours` per source, `freshness_state` (fresh / suspected_stale / unknown) with content age in status, `risk_control` failures recorded as such, and legacy freshness evidence preserved on upgrade. Feed headers are unchanged, so Flow's `fetchWechat` is unaffected. |
| Failure classes | Flow `classifyFailure`: worker `unavailable` → transient (backoff), `membership_required` → auth (pause for operator action). |

Tests: `flow/reliability.test.ts` (6 tests: validator reuse rules and 304; redirect hygiene; validators persist with the cursor, survive a failed run and never cross tenants; `/v1/media` storage, error mapping, flood stop, batch bound, untrusted object keys rejected; failure classes), upstream parser tests merged into `rss.test.ts`, gateway tests (128), and Telethon worker tests (29).

### Skipped (and why)
| Commits | Reason |
|---|---|
| `721663f8`, `bade923c`, `a141a5b1` | Baseline reconciliation, per-service env and preflight scripts for the bundled app's own (Windows-path) compose. Flow has its own compose and env. |
| `59b40ede`, `10c17665`, `5664be2e` | Resumable progress, identity merges and atomic item+progress commits for the bundled app's `items` tables (migrations 0022/0024, which also collide with mainline's assistant migrations). Flow already commits documents, cursor and outbox in one fenced transaction. |
| `8d9abdf2`, `4121d240` | Persisted retry schedules and paid-analysis cadence for the bundled app's scheduler (0023). Flow has `backoff_until` / `consecutive_failures` (0007). |
| `0aa18e69`, `106e32f5`, `e64235c1`, `bb0fb5d1` | The bundled app's media and extraction pipeline (0025, `media.ts`, `extract.ts`, `storage.ts`). Flow records per-attachment status instead. |
| `91163816`, `a98f81a1`, `d6715a2b`, `28a0e769`, `bd7798a1` | The bundled app's source UI, validation functions and ops health (0027). Flow validates at `createSource` and records runs and connector samples. |
| `5e0bdd31` | Tests the reliability branch's redirect-budget code, which wasn't ported. |
| `52a16ea2`, `2e58e723`, `6acbd26f`, `fec0739e`, `5d5d6174`, `1ea780f7`, `ff42cd5a` | Acceptance-evidence records and source zips for the bundled app's release (about 30k lines of logs/JSON). |
| `d34dabf5`, `7cf37b75`, `17b91d79` | Encrypted SQLite recovery and fixed-image release artifacts for the bundled app's Windows deployment. Flow has its own Postgres backup/restore test and release workflow. |
| `096ece89` / `d0602ef0` TypeScript collector parts, `telegram.ts` preview IDs | The bundled app's collectors. The preview-ID change (`channel/123` → `123`) would duplicate documents Flow already stored. |

### Loom collection-reliability merge record
- PR #27 (`cherry-pick-collection-reliability`) was merged on 2026-10-14 at 12:39 Asia/Shanghai after green CI (Backend checks, Web checks, Payment plugin artifacts). Merge commit: `7de81c0b06401723a634950eab623ffbe13e864c`.
- Compose stack on the build box: migration `0008_feed_validators` applied to the existing database. `smoke.sh` 28/28. The Telethon image (`--profile telegram`) builds and imports.
- Local tests:
  - Engine full `npm test` passes. `test:flow` 45/45 (RLS and backup on real Postgres 17 as a non-superuser). `tsc` passes, and eslint has no errors. Telethon worker 29/29; WeChat gateway 128/128.
  - `go vet` passes. `go test ./...` passes except the 5 known Pi e2e tests in `internal/app`, which pass in CI.
  - Web: tsc and `bun test` 2224/2224 pass.

### Container image publishing (PR #26) — status
- PR #26 was merged on 2026-10-14 at 12:17 Asia/Shanghai (`5e5956edb63b3664eb4224e46d158d88edb14759`). The push-triggered "Publish container images" run is https://github.com/newyngwieslash-ops/WeaveSense-Flow/actions/runs/38510624108.
- The 403 probe no longer blocks: "Reuse an existing commit image" passes. All four image builds then fail at the push with `denied: permission_denied: read_package` for `ghcr.io/newyngwieslash-ops/open-ai-canvas-{web,backend,yingce-agent}`.
- Cause: those three container packages already exist in the account (the anonymous token probe answers `UNAUTHORIZED` for them and `DENIED` for a non-existent name) and are not linked to this repository, so this repo's `GITHUB_TOKEN` (which does have `packages: write`) may not touch them. Changing the repository's default workflow permissions would not help, because the job already declares `packages: write`. That setting was left unchanged (`read`).
- Fix (owner action in GitHub, no API for it): for each of the three packages, open Package settings → "Manage Actions access" → add `WeaveSense-Flow` with the Write role (or "Connect repository"). Alternatively, delete the stale packages so the workflow recreates them linked to this repo. Then re-run the workflow.
- Resolved: the manually started "Publish container images" run on `main` (`fb72b364e49eaf91bba873ddcf66673e3e7259db`) succeeded: https://github.com/newyngwieslash-ops/WeaveSense-Flow/actions/runs/38514119586 (started 12:57, finished 14:29 Asia/Shanghai). All four image builds and the three manifest publishes (`open-ai-canvas-web`, `open-ai-canvas-backend`, `open-ai-canvas-yingce-agent`) passed; "Publish release assets" was skipped, as expected for a non-release run.

## Gap-closing round after the PRD audit (2026-10-14, Asia/Shanghai)

The audit is in `docs/implementation-status.md`. Work was done in batches, one PR each, merged only after CI was green. There were no force-pushes. Dependabot PRs #2–#6 were not touched.

| PR | Branch | What it closes |
|---|---|---|
| #29 | record-publish-success | Records the successful image publish (docs) |
| #30 | gaps-1-api-terms | §11 routes: PATCH sources with `expected_revision` (409 on conflict), document revisions, research start (202) and cancel, evidence compare, terms. Tools `intel_list_sources` and `intel_compare_evidence`. §7.3 Chinese term dictionary and tenant aliases (`flow_term_aliases`), with query expansion. New CI job runs the engine tests on real PostgreSQL 17, and backend CI moves to PG17. |
| #31 | gaps-2-media-ocr | §10.4 tenant/workspace-scoped Telegram media keys (engine and telethon), plus a legacy-key migration tool. §6.4 bounded retry of attachments that were not stored, an OCR worker (tesseract chi_sim+eng), `ocr_pages` usage, and the tool `intel_ocr_document`. |
| #32 | gaps-3-billing-notify | §5.2/§12 `retry_wait` / `reconciliation_required` (schema v62 migrates `unknown`), `price_version`, a usage ledger for all usage types, and email/webhook notifications (delivery_id, HMAC, backoff, idempotency, operator resolve). |
| #33 | gaps-4-ops | §13.3/§14 PG17 in the flow compose; profiles wechat / extract / research / monitoring; engine `/metrics` and platform `/metrics/flow`; Prometheus alert rules; legacy Loom import (`flow_legacy_id_map`); §9.2 industry-pack export (manifest, schemas, evals) with a drift test; `third_party/upstreams.lock.json`; `docs/implementation-status.md`; `docs/verification/`. Prettier exclusions cut from 34 files to 12. |

### Verification
- Local runs:
  - `go vet ./...` passes, and `go test ./internal/flow/ ./internal/database/ ./cmd/...` passes. The 5 known `internal/app` Pi e2e tests fail on this box only because the Pi runtime's node_modules aren't installed here (`undici` missing). They pass in CI.
  - Engine `test:flow` on PGlite: 61 tests, 58 pass, 3 skipped (the real-PG tests, which run in CI).
  - Web: `bun test` 2224/2224, tsc passes, and `prettier --check .` passes.
- CI: the engine job ran 58/58 tests with 0 skipped, including the real PostgreSQL 17 RLS and backup tests as a non-superuser. The OCR tests, including the real tesseract test, pass in CI.
- Not run:
  - `docker compose config` and booting the stack with the new profiles (no Docker on this box).
  - Prometheus rule syntax check (`promtool`).
  - A real SMTP server and a real webhook receiver.
  - Legacy import against a real legacy production dump.
  - A19 live acceptance.

## Gap-closing round 5 (2026-10-14, Asia/Shanghai)

Based on the audit in `prd-audit-2`. Merged by PR (numbers and merge SHAs are recorded below once merged):

| PR | Branch | Closes |
|---|---|---|
| #34 | `gaps5-ci` | The platform PostgreSQL RLS tests (`TestFlowRLSPostgres`, `TestRuntimeRLSBlocksUnfilteredHandlerQueries`, `TestBackgroundWorkIsTenantScoped`) now run in CI as the non-superuser `flow_app` role. The dependency-test guard requires them. |
| #35 | `gaps5-engine-states` | §6.2 job state machine (`queued`/`leased`/`running`/`retry_wait`/`partial`/`reconciliation_required`/`cancel_requested`/`cancelled`/`succeeded`/`failed`), heartbeat, `max_attempts` + backoff, outbox-based `reconcile()`; §6.1 cron schedules with timezone and `misfire_policy` (`coalesce`/`skip`/`catch_up`). |
| #36 | `gaps5-mcp-runchain` (merge `28f62a4542a3dee980df8ecc8545d6b3a11a2900`) | §9.3 tenant-saved encrypted MCP credentials (`flow_mcp_secrets`, AES-256-GCM envelope with `FLOW_SECRETS_KEK`, `secret:<id>` refs, under RLS); `/api/mcp/connections` + `/api/mcp/secrets` aliases; §8.1 run-chain interfaces (`runchain.go`) used by the intelligence chain; `billing_account.owner_type` (schema v63); compose `core` profile; enum validation in tool args; direct tests for `intel_preview_monitor`, `intel_cancel_operation`, `intel_preview_schedule`; A08, A10, A18 and A20 tests; sqlite test DB pinned to one connection (fixes a flaky A15 lock). |
| #37 | `gaps5-web-prettier` (merge `97c8db33f5a392ec3b96e034455c44375d9287c7`) | Removes the last 12 `web/.prettierignore` source exclusions; the snippet tests now compare format-normalized source (`web/test/helpers/source-contract.ts`). |

### Verification actually executed (local, before push)
| Check | Result |
|---|---|
| `go test ./internal/flow/ ./internal/database/` with `FLOW_TEST_PG_RLS_DSN` (PG17 container, role `flow_app`) | pass |
| `go test ./internal/app/` | A08/A10 and Flow tests pass; the 5 CloudAgent Pi-runtime tests fail locally only because `agent-runtime/pi/node_modules` is not installed (known, see P1) |
| Loom Engine `node --test flow/*.test.ts` (PGlite + PG17 via `FLOW_TEST_PG_ADMIN_URL`) | 72/72 pass |
| Web `bun test test/` | 2200 pass, 0 fail |
| Web `tsc --noEmit`, `eslint src test`, `prettier --check .` | pass |
| `docker compose -f docker-compose.flow.yml config` for every profile | pass |

### Post-merge
`main` = `97c8db33f5a392ec3b96e034455c44375d9287c7` (merge of #37, which sits on #36). CI on both PRs: Backend / Loom Engine flow / Web / Payment plugin artifacts all green before merge.

### Still not done (honest)
- §8.1: the canvas agent path is not converted to `RunChain` adapters.
- §6.2: the PRD's `pending` state is called `queued`.
- The former snippet tests are format-independent source contracts, not rendering tests.
- A19 (live Telegram/WeChat/MCP/OAuth), real SMTP/webhook, full stack boot, promtool, legacy production import and the PG16→17 volume upgrade still need a real environment (see `pending-acceptance.md`).
