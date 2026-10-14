# P4 — Managed MCP + complex sources (implementation notes)

## MCP Gateway (platform, `backend/internal/flow/mcp.go`)
- Built on the official Go SDK `github.com/modelcontextprotocol/go-sdk` v1.6.1, with the streamable HTTP client transport.
- `flow_mcp_connections` (migration v54) stores:
  - tenant/workspace, endpoint, transport (`streamable_http` only), and the credential **reference** (`env:FLOW_MCP_CRED_*`, never a value);
  - the allow-list and the approved per-tool schema snapshot and hash (sha256 of canonical name+description+inputSchema);
  - timeout (≤30 s), output cap (≤64k), cost rule (`per_call_credits`, `max_calls_per_day`), and status (`pending` | `approved` | `needs_reapproval` | `revoked`) with version.
- Lifecycle:
  - a workspace admin creates the connection (status `pending`);
  - a platform operator (`User.Role = admin`) reviews the discovered tools and approves an allow-list, which freezes the hashes;
  - a workspace admin or operator can revoke.
- Execution:
  - the connection must be `approved` and the tool must be allow-listed;
  - the daily budget is checked;
  - the gateway reconnects, re-lists tools and compares the live hash. Any difference flips the connection to `needs_reapproval` and blocks it. Newly offered tools are never exposed.
  - The call is then made with a timeout. Output is reduced to text, the credential is redacted, the output is capped, and usage is recorded in `flow_mcp_usage`.
- Credentials are resolved server-side and injected only as an `Authorization` header by an HTTP RoundTripper. They are never in the prompt tool table, the API views, stored tool calls or tool output.
- Agent exposure:
  - Approved tools become Pi function tools `mcp_<slug>_<tool>`, using the **approved** schema snapshot.
  - The tool table is snapshotted server-side into the run request (`flowMcpTools`). Client-supplied values are discarded.
  - Calls go through the existing intel tool bridge (`Tools.Invoke` → `MCPGateway.Execute`) under the scope `intel.mcp.use` (admin/editor).
- API: `GET/POST /api/intelligence/mcp/connections`, `GET /:id/tools`, `POST /:id/approve`, `POST /:id/revoke`. Web: `/intelligence/mcp`.

## Sources (engine, `services/loom-engine/flow/connectors.ts`, migration 0004)
- **Telegram** (`kind=telegram`, `config.handle`):
  - Uses the WeaveSense Telethon worker contract (`POST {TELEGRAM_WORKER_URL}/v1/history {channel, min_id, limit}`) when configured; otherwise the WeaveSense `t.me/s` web-preview parser (`parseTelegramHtml`).
  - The cursor (`flow_source_cursors.last_external_id`, the message id) only moves forward and only inside the collection commit. Items at or below the cursor are dropped, so a preview page that repeats old posts can't re-ingest them.
  - FloodWait or worker errors fail the run without moving the cursor.
  - **Media is not downloaded** (the worker's `save_media` is a stub). Every media URL is stored in `flow_documents.media` with `status: "unsupported"` and noted in the body.
- **WeChat OA** (`kind=wechat`, `config.gateway_source_id` required):
  - Reads the WeaveSense auth-gateway Atom feed `{WECHAT_GATEWAY_URL}/v1/feeds/<id>.atom` with a bearer token, and refuses redirects.
  - The backup-provider path is reported by the gateway (`x-loom-provider`: primary | dajiala | mixed) and recorded on the run and sample.
- **Revocation:** `POST /v1/sources/:id/revoke` marks the source `revoked`, cancels queued runs and fences out running ones. The scheduler only enqueues active sources, and `execute` refuses revoked sources before fetching.
- **On-demand extraction:** `POST /v1/documents/extract` (agent tool `intel_extract_document`) fetches the document URL through WeaveSense's outbound-policy fetch and extracts the main text (drops nav/footer/script and prefers article/main or paragraphs). A longer, changed text becomes a new revision; old revisions and citations stay intact. The extractor can be swapped out (`Engine.extractor`).
- **Reader extraction (loom `cdcb787f`):** `POST /v1/documents/extract {"document_id","via":"reader"}` (tool `intel_extract_document` with `via`) uses upstream `readArticle` (hosted reader, exact-host allowlist `ARTICLE_READER_ALLOWED_HOSTS`, public-DNS rechecks before and after, WeChat refused, login/captcha pages rejected). It is off unless `ARTICLE_READER_ENABLED=true`. The document is resolved under the caller's tenant/workspace before any outbound call. Disabled/not-allowed → 422, reader failure → 502. Swappable as `Engine.articleReader`.
- **YouTube transcripts (loom `cdcb787f`):** `POST /v1/documents/transcript {"document_id","language":"en|zh-Hans|zh-Hant"}` (tool `intel_transcribe_document`, scope `intel.collect`) calls the upstream subtitle worker contract (`POST /v1/transcript`, bearer `SUBTITLE_SERVICE_TOKEN`, redirects refused, 2 MiB cap, `text` must equal the server-side parse of `json3`). The transcript is appended as `[字幕: <lang>; <manual|automatic>]` in a new revision, so it is chunked, searchable and citable. Repeats are no-ops; `no_subtitles` changes nothing; busy → 429. Revision writes are fenced on `current_revision` (concurrent enrichment → 409). Enable with `docker compose --profile subtitles` (isolated `subtitle-worker` + authenticated `subtitle-egress`) and `SUBTITLE_ENABLED=true`.
- **Acceptance records:** each successful Telegram/WeChat collection writes `flow_connector_samples` (collector, provider, item count, unsupported media count, first 3 item titles, `mode` live|fixture). `GET /api/intelligence/connector-samples`.

## Real-sample acceptance — PENDING user credentials
All connector tests in this phase run on **fixtures** (`mode=fixture`), not on RSS and not on live accounts. Live acceptance (A19) is PENDING until the user provides:
- Telegram (Telethon path):
  - `TELEGRAM_API_ID` and `TELEGRAM_API_HASH` (my.telegram.org);
  - a logged-in Telethon session: run `workers/telethon/login.py` with a phone number and login code, or provide an existing `TELEGRAM_SESSION` string (or a `TELEGRAM_SESSION_PATH` file);
  - the worker deployed with `TELEGRAM_WORKER_URL` set for the engine;
  - one public channel handle to sample.
  - The web-preview fallback needs no credentials, only outbound access to t.me and a public channel.
- WeChat OA:
  - `WECHAT_GATEWAY_URL` and `WECHAT_GATEWAY_TOKEN` for the WeaveSense auth gateway, which has a logged-in WeChat session;
  - optionally the backup provider credentials the gateway uses (`DAJIALA_KEY`, plus `DAJIALA_VERIFYCODE` if the account requires it);
  - one OA's gateway `source_id`.
- MCP: one real remote MCP endpoint (HTTPS, streamable HTTP) and its token, exposed to the platform as `FLOW_MCP_CRED_<NAME>`.
