# P2 — User Agent + evidence (implementation notes)

## Platform (Go, `backend/internal/flow`)
| Piece | Where |
|---|---|
| `AgentSubject {kind: canvas \| intelligence_workspace \| topic, id}`; `ResolveSubject` re-derives tenant/workspace/role from the session on every request (no virtual canvasId) | `subject.go` |
| Server-side conversation directory + seq-paged messages; links CloudAgent runs via `CloudAgentExecution.ConversationID` | `conversations.go`, models in `internal/model/models_flow_agent.go`, migration **v52 `flow_agent`** |
| Per-turn context: recent window after persisted summary, current topic, evidence scope (auto-tracked recent document ids), confirmed plans, write-action receipts, background jobs (operation projections + receipt operations); rendered as a system message + chat messages | `context.go` |
| Tool bridge: 7 intel tools with JSON schema, read/write class, required Engine scope (role-mapped), timeout, output cap, per-conversation `callId` idempotency (same args → stored result; different → 409); write tools send `X-Idempotency-Key`; `intel_apply_monitor` requires approval and runs exactly once on approve | `tools.go` |
| HTTP: `GET /api/agent/tools`, `GET/POST /api/agent/conversations`, `GET/POST /api/agent/conversations/:id/messages`, `GET /api/agent/conversations/:id/context`, `POST /api/agent/conversations/:id/tool-calls`, `POST /api/agent/approvals/:id/decision`, `GET /api/intelligence/monitors` | `agent_routes.go`, `routes.go` |

## Loom Engine (`services/loom-engine/flow`)
- Migration `0002`: `flow_document_chunks` (tsvector + GIN, paragraph-aware ~1200-char chunks indexed on every revision), `flow_evidence`, `flow_monitor_previews`, monitor columns (interval, timezone, schedule, approval, idempotency), run `cancel_requested`.
- `searchDocuments` (FTS + substring fallback, source/time filters, paging), `readDocument` (offset/max_chars with coverage), `previewMonitor` / `applyMonitor` (atomic monitor + schedule, idempotent, preview single-use and expiring), `listMonitors`, `cancelOperation`.
- API: `POST /v1/documents/search`, `POST /v1/documents/read`, `GET /v1/monitors`, `POST /v1/monitors/preview`, `POST /v1/monitors`, `POST /v1/operations/:id/cancel`.

## Web
`/intelligence` page (`web/src/pages/intelligence`): workspace selector, server conversation directory (localStorage only as a warm cache), messages, Search tool action, approval cards, monitors list. API client `web/src/services/api/intelligence.ts`.

## Gaps (honest)
- **Pi runtime not yet wired.** The intel tools, turn context and conversation directory are exposed for the Pi/Go bridge (`GET /api/agent/conversations/:id/context` returns messages + function tools; `/tool-calls` executes), but the existing CloudAgent/Pi loop (`internal/app/cloud_agent_*`, canvas-bound) has **not** been refactored to use `AgentSubject` or to dispatch `intel_*` calls. The web chat therefore stores messages and runs tools directly; there is no model reply yet.
- Approvals use a new `flow_agent_approvals` table, not the existing CloudAgent approval chain.
- Summaries are stored/consumed but not generated (no compaction job yet). Confirmed plans are stored but no tool writes them yet.
- `intel_read_document` / `flow_evidence` table exists but no tool creates evidence rows yet (P3 report citations).
- Cross-device: directory is server-side; existing Canvas agent sidebar still uses its localStorage directory.
- Tests use PGlite and SQLite; no real Postgres/Docker run.
