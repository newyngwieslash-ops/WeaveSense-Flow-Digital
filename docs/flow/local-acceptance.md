# Local startup and A19 acceptance (user checklist)

This guide covers two things: starting the full stack on your own machine, and closing **A19**, the live-connector
acceptance that needs your real Telegram and WeChat credentials. Everything else (A01–A21 except A19) was
verified in CI or on the build box; see `delivery-report.md`.

Credentials stay in `.env.flow` on your machine. That file is gitignored. Never paste credentials into
chat, prompts, skills, or the web UI.

## 1. Prerequisites
- Docker 24+ with Compose v2 (`docker compose version`).
- About 4 GB of free RAM and 10 GB of disk.
- Port 8088 free. The web/nginx container is the only published port.

## 2. Configure
```bash
cp .env.flow.example .env.flow
# Replace every change-me value with a random secret, e.g. `openssl rand -hex 24`:
#   POSTGRES_PASSWORD, PLATFORM_DB_PASSWORD, INTEL_DB_PASSWORD, REDIS_PASSWORD,
#   MINIO_ROOT_USER / MINIO_ROOT_PASSWORD, FLOW_INTERNAL_JWT_SIGNING_KEY (>= 32 bytes)
# Optional: FLOW_MODEL_GATEWAY_URL/_KEY/_MODEL (OpenAI-compatible) to enable LLM synthesis.
```
The upstream `minio/minio` and `minio/mc` images can no longer be pulled from Docker Hub or quay. The compose
file therefore defaults to `cgr.dev/chainguard/minio` and `bitnamilegacy/minio-client`. To use your own
mirror, override `FLOW_MINIO_IMAGE` and `FLOW_MC_IMAGE`.

## 3. Build and start
```bash
docker compose -f docker-compose.flow.yml --env-file .env.flow build
docker compose -f docker-compose.flow.yml --env-file .env.flow up -d
docker compose -f docker-compose.flow.yml --env-file .env.flow ps   # platform + web "healthy", minio-init "Exited (0)"
```
Open http://localhost:8088. The first registered user becomes the administrator.

**Troubleshooting: containers cannot reach each other** (`minio-init` loops with `dial tcp … i/o timeout`).
This happens on hosts with a leftover `iptables-legacy` FORWARD policy of DROP. It happened on the build box.
Allow the compose bridge, then restart:
```bash
BR=br-$(docker network inspect -f '{{.Id}}' weavesense-flow_internal | cut -c1-12)
sudo iptables-legacy -I FORWARD -i "$BR" -j ACCEPT && sudo iptables-legacy -I FORWARD -o "$BR" -j ACCEPT
docker compose -f docker-compose.flow.yml --env-file .env.flow restart
```

## 4. Smoke test (HTTP end to end through nginx)
```bash
deploy/flow/smoke.sh http://localhost:8088
```
The script checks the following:
- web HTML and platform health/schema;
- that `/api/internal/*` is blocked at the edge, and that unauthenticated requests are rejected;
- registration and session;
- intelligence workspaces;
- engine sources (proxied platform → engine with a signed context token);
- notifications, billing, members, conversation directory, and canvas conversation documents.

It must end with `0 failed`.

Optional object-storage backup/restore check, run against the live MinIO:
```bash
set -a; . ./.env.flow; set +a
docker run --rm --network weavesense-flow_internal -e MINIO_ROOT_USER -e MINIO_ROOT_PASSWORD \
  -v "$PWD/deploy/flow/backup:/b:ro" --entrypoint /bin/bash bitnamilegacy/minio-client:2025.7.21 /b/objects-test.sh
```

## 5. A19: live connector acceptance

### 5.1 Telegram (Telethon, MTProto)
1. Get `TELEGRAM_API_ID` and `TELEGRAM_API_HASH` from https://my.telegram.org.
2. Create a session once, interactively, on your machine:
   ```bash
   cd services/loom-engine/workers/telethon && pip install -r requirements.txt
   TELEGRAM_API_ID=… TELEGRAM_API_HASH=… python login.py   # prints a session string; put it in TELEGRAM_SESSION
   ```
3. In `.env.flow`, set the following:
   - `TELEGRAM_API_ID`, `TELEGRAM_API_HASH`, `TELEGRAM_SESSION`;
   - `TELEGRAM_WORKER_URL=http://telethon:8787`;
   - optionally `TELEGRAM_MEDIA_MAX_BYTES` (default 20 MB) and `TELEGRAM_MEDIA_TIMEOUT_SECONDS` (default 15).
4. Start the worker and restart the engine:
   ```bash
   docker compose -f docker-compose.flow.yml --env-file .env.flow --profile core --profile telegram up -d --build telethon
   docker compose -f docker-compose.flow.yml --env-file .env.flow up -d loom-engine loom-scheduler
   ```
5. In the web UI, go to Intelligence → Sources → add a Telegram source with a public channel handle. Then ask the
   agent "collect now" (or wait for the schedule).

### 5.2 WeChat Official Accounts
1. Run your WeaveSense auth gateway with a logged-in WeChat session.
2. Set `WECHAT_GATEWAY_URL` and `WECHAT_GATEWAY_TOKEN` in `.env.flow`. A backup provider is optional: `DAJIALA_KEY`.
3. Restart `loom-engine` and `loom-scheduler`, then add a WeChat source (the gateway `source_id`) and collect once.

### 5.3 MCP (live part of A14)
Set either `FLOW_MCP_CRED_<NAME>` (bearer token) or an OAuth client-credentials set,
`FLOW_MCP_OAUTH_<NAME>_TOKEN_URL/_CLIENT_ID/_CLIENT_SECRET[/_SCOPE]`. Then restart `platform`, and add the
connection in Intelligence → MCP with credentialRef `env:FLOW_MCP_CRED_<NAME>` or `oauth:FLOW_MCP_OAUTH_<NAME>`.
Discover the tools, approve them, and make one call.

### 5.3.1 MCP OAuth with a real provider (authorization code + PKCE)
Use this when the MCP server requires an interactive user login (any OAuth 2.0/2.1 provider that supports
authorization code + PKCE, e.g. the provider in front of your hosted MCP server). Never paste client secrets or tokens
into chat, tickets or the browser console; they live only in `.env.flow`.
1. At the provider, register an OAuth client (confidential or public) with redirect URI
   `https://<your-host>/api/intelligence/mcp/oauth/callback` (for a local stack: `http://localhost:<WEB_PORT>/api/intelligence/mcp/oauth/callback`)
   and the scopes the MCP server needs. Enable PKCE (S256).
2. In `.env.flow`, pick a NAME (upper case, e.g. `ACME`) and set:
   `FLOW_MCP_OAUTHCODE_ACME_AUTH_URL`, `FLOW_MCP_OAUTHCODE_ACME_TOKEN_URL`, `FLOW_MCP_OAUTHCODE_ACME_CLIENT_ID`,
   `FLOW_MCP_OAUTHCODE_ACME_CLIENT_SECRET` (empty for a public client), optional `_SCOPE` and `_REVOKE_URL`, and
   `FLOW_MCP_OAUTHCODE_ACME_REDIRECT_URL` (exactly the URI registered in step 1).
   Set `FLOW_OAUTH_ENC_KEY` to a random value of at least 32 characters (`openssl rand -hex 32`); tokens are stored
   AES-GCM encrypted with it. Changing it later invalidates stored grants (users re-authorize).
3. Restart `platform` (`docker compose -f docker-compose.flow.yml --env-file .env.flow up -d platform`).
4. In Intelligence → MCP, add the connection with credentialRef `oauthcode:FLOW_MCP_OAUTHCODE_ACME` and press
   **Authorize**. The browser goes to the provider's login/consent page (`POST /api/intelligence/mcp/connections/<id>/oauth/start`
   returns that URL with `state` + `code_challenge`).
5. Log in and consent. The provider redirects to `/api/intelligence/mcp/oauth/callback`; the page reports success and the
   connection status (`GET …/connections/<id>/oauth`) shows `authorized`, the granted scope and the expiry — never the token.
6. Discover tools, approve one, and make a call. Check MCP usage shows it with its credits.
7. Refresh: wait for (or force, by setting a short token lifetime at the provider) access-token expiry and call again;
   the platform refreshes with the stored refresh token without a new login.
8. Negative checks: open the callback URL a second time (the `state` is single-use → rejected); start authorization as
   another user and try to finish it with the first user's session (rejected); press **Revoke** (`POST …/oauth/revoke`),
   confirm the provider shows the grant revoked and the next tool call asks for re-authorization.
9. Confirm no token or client secret appears in `docker compose logs platform`, the UI, or the network responses.

### 5.4 A19 checklist (tick each one and record it in `delivery-report.md`)
- [ ] Telegram: one collection run succeeded. The Sources panel "connector samples" shows `collector=telethon`, `mode=live`, items > 0.
- [ ] Telegram: posts with photos have `media_status=saved` (objects under `telegram/<channel>/` in the intel bucket). Oversized media show `too_large`, not a failure.
- [ ] Telegram: a second run ingests only newer posts (the cursor advanced, with no duplicate documents).
- [ ] WeChat: one collection run with `collector` = the gateway or provider, `mode=live`, items > 0. Article bodies are readable in the document view.
- [ ] Revoking a source stops new runs, and existing evidence stays readable (A17 live).
- [ ] MCP: one approved tool call succeeded and appears in MCP usage with its credits. The token never appears in the UI or logs.
- [ ] MCP OAuth (5.3.1): real-provider authorization succeeded, a tool call worked, refresh worked after expiry, replayed callback and revoke behaved as described.
- [ ] Record the time (Asia/Shanghai), the commit SHA (`git rev-parse HEAD`) and the sample ids. Then set A19 to **pass** in `delivery-report.md`.

## 6. Stop / reset
```bash
docker compose -f docker-compose.flow.yml --env-file .env.flow down        # keep data
docker compose -f docker-compose.flow.yml --env-file .env.flow down -v     # wipe volumes
```
