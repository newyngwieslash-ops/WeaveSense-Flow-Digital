# Implementation status (PRD v2 mapping)

Last updated 2026-10-14 (Asia/Shanghai) after gap-closing rounds (PRs #30–#33) and round 5 (PRs #34 onward). Phase reports are in `docs/flow/delivery-report.md`. Commands and receipts are in `docs/verification/`.

**A file existing does not mean the feature has passed:** the evidence column says what was actually exercised.

| PRD section | Status | Code | Evidence |
|---|---|---|---|
| §1.2/§17 upstream lock and licenses | done | `third_party/upstreams.lock.json`, `THIRD_PARTY_UPSTREAMS.md` | review only |
| §3 architecture: two logical databases | done | `backend/internal/flow`, `services/loom-engine/flow` | Go + Node tests |
| §5.2 states, 202, notifications | done | `retry_wait` / `reconciliation_required` (`billing.go`, `model_tasks.go`, `mcp_idem.go`, `notify.go`); email and webhook delivery with `delivery_id`, HMAC, retries and idempotency (`notify.go`, `routes_notify.go`) | `notify_test.go` (httptest webhook, fake mailer); **no real SMTP/webhook endpoint exercised** |
| §6.2 leases, fencing, idempotency | done | engine 0001 / `lease.go` | `reliability.test.ts`, A05–A07 |
| §6.2 job state machine | done; PRD `pending` is named `queued` | engine `0012_job_states_misfire.sql`, `engine.ts` (`queued→leased→running→succeeded/partial/failed`, `retry_wait` with backoff and `max_attempts`, `cancel_requested→cancelled`, `reconciliation_required` + `reconcile()`), heartbeat | `gaps5.test.ts` (PGlite + PG17 in CI) |
| §6.1 schedules: cron, timezone, misfire_policy | done | `cron.ts`, `dueWindows` (`coalesce`/`skip`/`catch_up`, max 3 catch-up windows, 120 s grace) | `gaps5.test.ts` |
| §6.4 enrichment, OCR, media | done (OCR live only with tesseract) | `enrichment.ts` `ocrClient`, `workers/ocr`, `engine.retryMedia` / `ocrTick` | `gaps2.test.ts`; worker unit tests; the real-tesseract test runs in CI |
| §7.3 Chinese term dictionary and aliases | done | `flow/terms/zh-industry.json`, `flow/terms.ts`, `flow_term_aliases` | `gaps1.test.ts` |
| §8.1 run-chain interfaces | done for the intelligence chain; canvas path not converted | `runchain.go` (`ScopeResolver`, `ContextProvider`, `ToolProvider`, `ToolExecutor`, `UsageRecorder`), used by `agent_routes.go` and `app/cloud_agent_flow.go`; usage writes go through `DefaultUsageRecorder` | `runchain_test.go` |
| §8.3 tools | done | 17 `intel_*` tools, including `intel_list_sources` and `intel_compare_evidence` | `routes_contract_test.go`, tool tests |
| §9.2 industry packs | done (export) | `industry-packs/*` (manifest, schemas, evals) generated from `backend/internal/flow/skillpacks/*.json` | `TestIndustryPackExport` (drift check); eval sets are review cases, not an automated grader |
| §9.3 MCP Gateway | done; real server acceptance deferred (A19) | `mcp*.go`; tenant-saved credentials `mcp_secrets.go` (AES-256-GCM envelope, per-secret data key wrapped by `FLOW_SECRETS_KEK`, AAD binds tenant/workspace/id; referenced as `secret:<id>`, never returned by the API); `/api/mcp/connections` and `/api/mcp/secrets` aliases | A14/A16 tests, `mcp_secrets_test.go` |
| §9.4 research sub-tasks | done; Eino/OpenCode executor not wired (optional) | `tools_p3.go`, `reports.ts` | tests |
| §10.3 RLS | done; the platform agent runtime still runs in system context internally | engine 0006 + every new table; `flow_rls.go` (incl. `flow_mcp_secrets`) | `pg.test.ts` on PG17 in CI; platform `TestFlowRLSPostgres` / `TestRuntimeRLSBlocksUnfilteredHandlerQueries` / `TestBackgroundWorkIsTenantScoped` run in CI as non-superuser `flow_app` (PR #34) |
| §10.4 private objects and tenant-scoped media keys | done | `connectors.ts` `SCOPED_MEDIA_KEY`, telethon `media_object_key`, `tools/media-keys.ts` | `gaps2.test.ts`, `test_history.py` |
| §11 API contract | done | `routes_contract.go`, engine `server.ts`: PATCH sources with `expected_revision`, document revisions, research start (202) and cancel, evidence compare, terms | `routes_contract_test.go`, `gaps1.test.ts` |
| §12 billing_account.owner_type | done | schema v63 backfill (`personal`/`organization`) | migration tests |
| §12 usage, price_version | done | `usage.go` (all usage types, `FLOW_PRICE_BOOK`), `price_version` on reservations, gateway headers | `notify_test.go` / usage tests |
| §13.3 legacy import, PG version | done | `flow/tools/legacy-import.ts` (`flow_legacy_id_map`); PG17 in every compose file and in CI | `legacy-import.test.ts` (PGlite); **not run against a real legacy production dump** |
| §14 profiles and monitoring | done (config) | `docker-compose.flow.yml` profiles core / telegram / wechat / subtitles / extract / research / monitoring (`COMPOSE_PROFILES=core` in `.env.flow.example`); `/metrics`, `/metrics/flow`, `deploy/flow/monitoring/*` | `ops.test.ts`, `metrics_test.go`; `docker compose config` validated for every profile; **full stack boot not run** |
| A08 / A10 / A18 / A20 direct tests | done | `app/flow_acceptance_test.go` (A08, A10), `pg.test.ts` (A18 backup), `flow/a20.test.ts` | Go + Node tests |
| A19 live acceptance (Telegram, WeChat, MCP, OAuth) | deferred to user | `docs/flow/local-acceptance.md` | — |

## Known deviations
- Directory layout: everything lives under `backend/internal/flow/` rather than `intelligence/`, `mcpgateway/` and `tenant/`. Workers live under `services/loom-engine/workers/`.
- `web/.prettierignore` no longer excludes source files. The former snippet tests use format-normalized source contracts (`web/test/helpers/source-contract.ts`); they are not rendering tests.
- §8.1: the canvas agent path still calls its own code directly, not through `RunChain` adapters.
- §6.2: the PRD's `pending` state is called `queued`.
- Eino/OpenCode research executor: not integrated (optional in the PRD).
