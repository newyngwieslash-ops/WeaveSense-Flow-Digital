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
