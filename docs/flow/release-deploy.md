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
