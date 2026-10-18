# Design: restart.bat full startup for WeaveSense-Flow

## Approach (single)

Add a root `restart.bat` aligned with `docs/flow/local-acceptance.md` sections 2-4:

1. **Env bootstrap**: when `.env.flow` is absent, copy `.env.flow.example` and replace the six `change-me` placeholders (POSTGRES_SUPERUSER_PASSWORD, PLATFORM_DB_PASSWORD, INTEL_DB_PASSWORD, REDIS_PASSWORD, MINIO_ROOT_PASSWORD, FLOW_INTERNAL_JWT_SIGNING_KEY) with random values from `openssl`/PowerShell. Never touch an existing `.env.flow` that is already placeholder-free; reject an existing file that still contains any `change-me` (a short JWT key silently disables the engine proxy). After bootstrap, re-scan for leftover placeholders and abort if any survive.
2. **Port guard** (pattern reused from `services/loom-engine/restart.bat`): parse `docker compose config` output to confirm `published: 8088` still exists; abort with an error instead of silently starting on another port.
3. **Start**: `docker compose -f docker-compose.flow.yml --env-file .env.flow up -d --build`; on build failure retry with `--no-build` using existing local images (same behavior as the loom-engine script).
4. **Readiness wait**: poll `curl http://127.0.0.1:8088/api/health` (platform `/api/health` must return `"ready":true`; 10 minute cap), then open the browser. If the web serves but health never turns ready within a 60s grace, proceed with a clearly labelled DEGRADED warning instead of a misleading "ready" message. `curl.exe` presence is pre-checked with a clear error.
5. **Optional smoke**: `restart.bat smoke` additionally runs `deploy/flow/smoke.sh` via Git Bash after readiness; default double-click does not run it.

## Key decisions

- **No extra profiles flag**: `.env.flow.example` line 2 already sets `COMPOSE_PROFILES=core`; passing `--env-file` enables all core services. Extra profiles (telegram/wechat/...) are enabled by editing `.env.flow`, not by the script.
- **Simplified port ownership**: the loom-engine script needs complex container identity checks because a naive kill could take down Docker Desktop's port proxy. The Flow script is read-only: if 8088 is taken and `docker compose ps` shows this project's web container, do a `down` + restart; otherwise error out. Never force-kill processes.
- **Secret generation**: prefer `openssl rand -hex` (bundled with Git for Windows), fall back to PowerShell `[guid]::NewGuid()`; JWT signing key is guaranteed >= 32 bytes.
- **CRLF**: the `.bat` must be saved with CRLF line endings (cmd mis-parses LF-only batch files at labels); verified after writing.

## Non-goals

- No changes to `docker-compose.flow.yml`, no new compose services, no edits to the loom-engine script.
- No Docker Desktop auto-start (`ensure_docker`); for a full stack, a Docker-not-running state gets a clear error message instead.
