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
