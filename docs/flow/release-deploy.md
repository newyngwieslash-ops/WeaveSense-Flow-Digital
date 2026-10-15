# Release, deploy, backup and restore

## Topology (`docker-compose.flow.yml`)

| Service | Role |
|---|---|
| `web` (nginx) | The only published port. Proxies `/api/*` to `platform`. `/api/internal/*` returns **404 at the edge**. |
| `platform` | Go backend. Handles auth, scopes, billing, Model Gateway, MCP gateway, agent runs. |
| `loom-engine` | Engine internal API, collection worker and outbox dispatcher. Internal network only. |
| `loom-scheduler` | Single due-window scanner, protected by an advisory lock. Re-checks grants and summarizes briefs through the platform. |
| `postgres` | Two databases with separate non-superuser owners: `flow_platform` and `flow_intel`. |

## Release procedure

1. Merge to `main` only after CI is green: Backend checks, Web checks, Payment plugin artifacts.
2. Tag the release with `git tag vX.Y.Z <main-sha> && git push origin vX.Y.Z`. `publish-images.yml` builds the images.
3. Take a backup before migrating (see below).
4. Deploy with `docker compose -f docker-compose.flow.yml --env-file .env.flow pull && … up -d`.
   - The platform runs versioned migrations at start. P5 adds schema v55 (billing/orgs) and v56 (run leases and canvas conversations).
   - `loom-engine` runs engine migrations 0001–0006. 0005 adds grant re-check; 0006 adds RLS.
   - `loom-scheduler` runs with `LOOM_AUTO_MIGRATE=false`.
5. Smoke test:
   - `GET /api/intelligence/workspaces` returns 200 when logged in.
   - `GET /api/internal/flow/events` through the public port returns **404**.
   - The engine `/healthz` returns `{ok:true}` from inside the network.
6. Roll back by redeploying the previous image tag. Migrations are additive (new tables and columns), so the previous version keeps working. To roll back data, restore the pre-deploy backup.

## Required configuration (`.env.flow`, server-side only)

**Shared secrets and endpoints**
- `FLOW_INTERNAL_JWT_SIGNING_KEY`: at least 32 bytes, shared by platform and engine.
- `FLOW_INTERNAL_JWT_ISSUER` / `FLOW_INTERNAL_JWT_AUDIENCE`.
- `FLOW_PLATFORM_EVENTS_URL` (engine) and `FLOW_PLATFORM_INTERNAL_URL` (scheduler) point at `http://platform:8080`. Never point them at the public edge.

**Model Gateway (optional)**
- `FLOW_MODEL_GATEWAY_URL`, `FLOW_MODEL_GATEWAY_KEY`, `FLOW_MODEL_GATEWAY_MODEL`, `FLOW_MODEL_MICRO_PER_1K_TOKENS`.
- When these are unset, research and briefs stay extractive.

**Credentials**
- `FLOW_MCP_CRED_*`: MCP server credentials, referenced by name only.
- Connector credentials: see `pending-acceptance.md`.

**Database roles**
- The engine role must **not** be a superuser or `BYPASSRLS`. With such a role, RLS (migration 0006) would be silently bypassed.

## Security layers

- **Edge:** nginx denies `/api/internal/`. Internal endpoints also require a signed service token with `aud` set to the platform issuer.
- **Tenancy:**
  - First layer: application scope checks (platform `ResolveScope`, engine `tenant_id` predicates).
  - Second layer: Postgres RLS, with policy `tenant_isolation` on every engine table. API requests set `app.tenant_id` per transaction. Workers run in system context.
- **Egress:**
  - The MCP gateway dials public addresses only. Loopback, private, link-local/metadata and CGNAT addresses are blocked after DNS resolution on every connection.
  - Engine fetches go through `publicFetch`, which re-checks the target on every redirect.

## Backup and restore (`deploy/flow/backup/`)

```sh
# backup (consistent snapshot + manifest of row counts/checksums + sha256)
deploy/flow/backup/backup.sh "$INTEL_DATABASE_URL" /backups/intel
deploy/flow/backup/backup.sh "$PLATFORM_DATABASE_URL" /backups/platform
# restore into an EMPTY database (single transaction; verifies sha256 and the manifest)
deploy/flow/backup/restore.sh /backups/intel/flow-<ts>.dump "$NEW_INTEL_DATABASE_URL"
```

- **Backup role.** The scripts use `--enable-row-security`. Run them with no `app.tenant_id` set (system context sees every row). Alternatively, use a dedicated backup role with `BYPASSRLS`.
- **Order.** Back up platform first, then intel. On restore, restore both before starting services.
  - The outbox is idempotent (`flow_consumed_events`), so engine events newer than the platform snapshot are re-delivered and applied once.
  - Open budget reservations are closed by the billing maintenance loop.
- **Consistency test.** `services/loom-engine/flow/pg.test.ts` runs against real Postgres as a non-superuser. It populates a database, backs it up, restores it into an empty database, checks the manifest diff and app-level reads, and confirms the restored DB is not re-migrated. Run it with `FLOW_TEST_PG_ADMIN_URL=postgres://<superuser>@host/db FLOW_TEST_PG_APP_ROLE=<role>:<password> node --test flow/pg.test.ts`.

### Daily backup (§14, `--profile backup`)
- `flow-backup` (postgres:17-alpine) runs `deploy/flow/backup/daily.sh` once a day at `BACKUP_AT` (UTC, default `03:00`): `backup.sh` for the platform and intel databases (dump + manifest + sha256) into the `backups` volume (or `BACKUP_HOST_DIR`), then prunes dumps older than `BACKUP_RETENTION_DAYS` (default 14) while always keeping the newest `BACKUP_KEEP_MIN` (default 3). Each successful cycle writes `/backups/last-success`.
- `flow-backup-objects` (minio-client) mirrors both buckets into a dated snapshot `/backups/objects/<ts>/` with `objects.manifest`, same retention.
- Without compose: `BACKUP_ONCE=1 BACKUP_DATABASE_URLS="platform=… intel=…" deploy/flow/backup/daily.sh` from host cron.
- CI (`Object storage restore (A18)` job) runs `daily-test.sh` against a real PostgreSQL 17: one cycle, manifest/sha256 check, retention, restore of the new dump. The compose services themselves are not started in CI.
- Restore drills stay manual: restore the latest dump into an empty database with `restore.sh` (it verifies the manifest) at least monthly.

## P7 additions
- Smoke tests: `deploy/flow/smoke.sh [base]` (HTTP through nginx) and `deploy/flow/browser-smoke.mjs` (headless Chrome, `playwright-core`). Both accept `SMOKE_USER`/`SMOKE_PASSWORD`.
- Object storage: `deploy/flow/backup/objects-backup.sh <alias> <dir> <bucket>…`, `objects-restore.sh <dir> <alias>`, and the round-trip test `objects-test.sh`.
- MinIO images default to `cgr.dev/chainguard/minio` and `bitnamilegacy/minio-client` (`FLOW_MINIO_IMAGE` / `FLOW_MC_IMAGE`), because upstream images are no longer pullable.
- Platform RLS (schema v58) requires the platform to connect as a non-superuser for policies to apply.
- Optional `telethon` service: `--profile telegram`.
- Local startup and the A19 checklist: `docs/flow/local-acceptance.md`.

## Gap-closing round (2026-10-14)
- **PostgreSQL 17 everywhere.** `docker-compose.flow.yml` now uses `postgres:17-alpine`, the same as the deploy compose and CI. An existing PG16 data volume will not start under 17. Upgrade it with dump and restore:
  1. Run `deploy/flow/backup/backup.sh` for both databases on the old stack.
  2. `docker compose down`, then move the old `pgdata` volume aside. Don't delete it until the new stack has been verified.
  3. `up -d postgres`. The init script recreates the two roles and databases.
  4. Run `deploy/flow/backup/restore.sh` for each dump, then start everything else.
- **Profiles:**
  - `telegram`: telethon.
  - `wechat`: wechat-gateway on :8790. Set `WECHAT_GATEWAY_URL=http://wechat-gateway:8790`, `WECHAT_GATEWAY_TOKEN` and `WECHAT_PRIMARY_ALLOWED_HOSTS`.
  - `subtitles`.
  - `extract`: ocr-worker plus loom-enrich (`LOOM_ROLE=enrich`). Set `OCR_ENABLED=true` and `OCR_SERVICE_TOKEN`.
  - `research`: loom-research (`LOOM_ROLE=research`, periodic briefs).
  - `monitoring`: Prometheus on 127.0.0.1:9090.
  - When you enable `extract` or `research`, set `LOOM_SPLIT_ROLES=true` so `loom-engine` and `loom-scheduler` stop running those loops themselves.
- **Monitoring:**
  - Loom Engine `GET /metrics` returns aggregate gauges with no tenant ids. An optional `METRICS_TOKEN` restricts access.
  - Platform `GET /metrics/flow` returns 404 unless `FLOW_METRICS_TOKEN` is set, and then requires that bearer token. Put the same token in `deploy/flow/monitoring/platform.token`; that file is git-ignored.
  - Alert rules are in `deploy/flow/monitoring/alerts.yml`: engine/platform down, outbox backlog, expired leases, collection failures, media/OCR failures, reconciliation_required, failed notifications, model tasks stuck in retry_wait, and (audit-6) budget denials / accounts near quota / schedules blocked by budget, MCP errors, citation-verification and Reviewer rejections, duplicate deliveries, and the oldest queued job (collection, research, model task > 30 min).
  - Metrics behind them: platform counters `flow_budget_denied_total`, `flow_mcp_errors_total`, `flow_citation_rejected_total`, `flow_event_duplicate_deliveries_total` and gauges `flow_budget_accounts_near_quota`, `flow_model_task_oldest_queued_seconds`; engine gauges `flow_collection_oldest_queued_seconds`, `flow_research_oldest_queued_seconds`, `flow_outbox_redelivery_attempts`, `flow_schedules{state,reason}`, `flow_review_rejected_claims`. Platform counters reset on restart (alerts use `increase()`).
  - Rules are validated in CI with `promtool check rules` and unit-tested with `promtool test rules alerts.test.yml`.
  - Alertmanager routing (where alerts are sent) is deployment-specific and is not included.
- **Legacy Loom import** (`services/loom-engine/flow/tools/legacy-import.ts`):
  - Source tables: sources, items, keywords.
  - The default is a dry run. `--apply` writes. `--activate` keeps enabled sources active; otherwise they are imported paused.
  - Each old_id→new_id pair is stored in `flow_legacy_id_map`. Running the import again reuses the mapping.
  - Keywords are reported as skipped: monitors need a preview and approval. WeChat sources need a gateway id. Analyses and notes are not imported.
- **Telegram media keys:** `flow/tools/media-keys.ts plan|apply` moves legacy `telegram/<channel>/<id>` keys to tenant/workspace-scoped keys.

## Compose profiles (PRD §14)
- `core` = postgres, redis, minio (+ minio-init), platform, web, loom-engine, loom-scheduler. Every core service now carries `profiles: [core]`; `.env.flow.example` sets `COMPOSE_PROFILES=core`, so `docker compose -f docker-compose.flow.yml --env-file .env.flow up -d` starts the core stack as before.
- **Upgrading an existing `.env.flow`:** add `COMPOSE_PROFILES=core` (or `core,telegram,extract,...`). Without it no service starts.
- With `--profile` on the command line, Compose ignores `COMPOSE_PROFILES`; always include `--profile core`, e.g. `--profile core --profile telegram`.
- Optional: telegram, wechat, subtitles, extract, research, monitoring. `docker compose config` validates every profile combination (checked on the build box).

## Cutover from a legacy Loom instance (§13.4)
- Each source has exactly one scheduler. Import legacy sources paused (`legacy-import.ts` default), stop the legacy instance's cron/tick for those sources, then activate them in Flow. Never run paid collection or advance the same account cursor from both systems.
- Shadow reads or offline comparison are allowed (legacy read-only, Flow paused).
- Rollback: first stop the Flow scheduler (`loom-scheduler`, or disable the schedules), then restart the legacy instance from its verified cursors. Do not replay external actions whose outcome is unknown (`reconciliation_required`); reconcile them first.
