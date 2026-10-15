# Pending real-sample acceptance (deferred to the very last step)

Live, real-account acceptance for the connectors below is **deferred to the very last step** of delivery. Until the user provides these credentials, A19 (and the live part of A14) stays **pending-credentials**. All current connector tests run on fixtures (`mode=fixture`), and no RSS test is counted as Telegram/WeChat acceptance.

## Telegram
- `TELEGRAM_API_ID` and `TELEGRAM_API_HASH` from https://my.telegram.org
- A logged-in Telethon session: either a phone number + login code (run `services/loom-engine/workers/telethon/login.py`), or an existing session string (`TELEGRAM_SESSION`) or session file path (`TELEGRAM_SESSION_PATH`)
- A deployed Telethon worker, with its URL set as `TELEGRAM_WORKER_URL` for Loom Engine
- One public channel handle to sample

## WeChat Official Accounts
- A running WeaveSense auth gateway with a logged-in WeChat session: `WECHAT_GATEWAY_URL` and `WECHAT_GATEWAY_TOKEN`
- One official-account feed ID (the gateway `source_id`)
- Optional backup provider: `DAJIALA_KEY` (plus `DAJIALA_VERIFYCODE` if that account requires it)

## MCP
- One real remote MCP endpoint (HTTPS, streamable HTTP) and its token. Either save the token as a tenant secret (`POST /api/mcp/secrets`, needs `FLOW_SECRETS_KEK`) and reference it as `secret:<id>`, or provide it as `FLOW_MCP_CRED_<NAME>` and reference it as `env:FLOW_MCP_CRED_<NAME>`

## Procedure (when credentials arrive)
1. Configure the env vars above and create one source/connection per connector.
2. Run one collection (or one MCP tool call) with a small budget.
3. Record the `flow_connector_samples` row (`mode=live`) or the `flow_mcp_usage` row, with time (Asia/Shanghai), environment, and receipts, in `delivery-report.md`.

## Added by the gap-closing round (2026-10-14)
These need a real environment. None of them has been run yet.
- `docker compose -f docker-compose.flow.yml --profile core --profile wechat --profile extract --profile research --profile monitoring config`, then boot the stack. Check that Prometheus shows both targets up.
- Upgrade an existing PG16 volume to PG17 with backup.sh and restore.sh (see release-deploy.md).
- Email notifications against a real SMTP server. Webhook notifications against a real receiver; verify the HMAC signature and `X-Flow-Delivery-Id`.
- Legacy Loom import: dry run against a copy of the real legacy database, then `--apply`; spot-check the old_id→new_id map.
- Telegram media key migration (`media-keys.ts plan` / `apply`) on real stored objects, before deleting the old keys.
- OCR on real Telegram images (Chinese text) through `--profile extract`.

## Added by round 5 (2026-10-14)
- Generate a production `FLOW_SECRETS_KEK` (`openssl rand -base64 32`), store it outside the database, and test secret rotation against a real MCP server.
- `docker compose config` now passes for every profile locally; booting the full stack has still not been run.

## Added in round 7 (2026-10-15)
- ASR with a real model: build `workers/asr` with `requirements-model.txt` (faster-whisper) and transcribe a real audio or video file under compose profile `asr`. CI only covers the stub transcriber.
- A18 object restore runs in CI on MinIO, but has not been run against the production object store.

## Added in round 8 (2026-10-15)
- Boot `--profile backup` on the real stack, confirm `/backups/last-success` updates daily and retention prunes, and do one restore drill from a daily dump. CI runs `daily-test.sh` (PG17) only.
- Wire Alertmanager routing for the new §14 alerts (deployment-specific) and confirm they fire on the real stack.
