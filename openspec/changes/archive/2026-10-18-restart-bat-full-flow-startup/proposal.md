# Proposal: restart.bat full startup for WeaveSense-Flow

## Problem

Double-clicking restart.bat should start the whole project, but the repo root has no restart.bat. The only one lives at `services/loom-engine/restart.bat` and:

1. `cd /d "%~dp0"` switches into `services/loom-engine/`, loading the legacy Zhishi Loom `docker-compose.yml` (port 8080, single `app` container).
2. It never touches the Flow stack orchestration `docker-compose.flow.yml` (Go platform + web + loom-engine + loom-scheduler + postgres + redis + minio; single published port 8088).

## Root cause

The Flow project never shipped a root-level launcher; the old script is a leftover of the loom-engine sub-project whose scope is that subdirectory, not the repo root.

## RED evidence (before fix)

- `ls restart.bat` -> not found (no output)
- `find . -iname "*.bat"` -> only `./services/loom-engine/restart.bat`
- Running that script boots the legacy single Loom stack on 8080; `platform` / `web` never start; unrelated to the Flow stack.

## Fix goal

Add a root `restart.bat` that: bootstraps `.env.flow` (copy from example and generate random secrets when missing), validates that compose still publishes 8088, starts all core services of `docker-compose.flow.yml`, waits for readiness, and optionally runs the smoke test. It reuses the existing conventions in `docs/flow/local-acceptance.md` and `deploy/flow/smoke.sh`; no bespoke flow is invented.

## Impact

One new file at repo root (`restart.bat`); no Compose, backend, or frontend code changes.
