# Upstreams and Licenses

| Remote | Repository | Pinned commit | Imported as | License |
|---|---|---|---|---|
| canvas-upstream | https://github.com/ddcat-ai/open-ai-canvas | `e70fe1b9c93d6af3b807a87ed15bb621af78095c` | repository root (full upstream history kept; `main` builds on this commit) | MIT, © 2026 ddcat and Open AI Canvas contributors — `LICENSE`, `NOTICE` |
| loom-upstream | https://github.com/newyngwieslash-ops/WeaveSense | `cdcb787f925e584da0302f7968431335b8f8122e` (branch `codex/collection-capability`; upstream `main` is still `bec6212a`) | `services/loom-engine/` via `git subtree add`, upgraded with `git subtree pull` (history kept, not squashed) | MIT, © 2026 织识 Loom — `services/loom-engine/LICENSE` |

Both pinned commits equalled the upstream `main` HEADs at import time (2026-10-12). On 2026-10-14 loom was upgraded from `bec6212a` to `cdcb787f`, the head of `codex/collection-capability`, which builds directly on `bec6212a` (28 commits). Upstream `main` had not moved. The older `codex/collection-reliability` branch (head `ff42cd5a`) forked before `bec6212a` and was never merged upstream, so it is not part of the subtree history. On 2026-10-14 the parts that apply to code Flow runs were ported file by file (3-way merges against the fork point `c49829f3`, plus hand ports), each marked in the commit message. The per-commit integrate/skip table is in `docs/flow/delivery-report.md` (Loom collection-reliability port).

Updating: `git subtree pull --prefix=services/loom-engine loom-upstream <commit>`; for Canvas, `git fetch canvas-upstream && git merge <commit>`. Re-audit interfaces and migrations after any upgrade and update this table.

## Notes
- The root MIT license does **not** cover every dependency; lockfiles (`web/bun.lock`, `backend/go.sum`, `services/loom-engine/package-lock.json`, …) pull third-party components under their own licenses.
- **tldraw**: Canvas includes the optional tldraw drawing module, which has its own license (production use requires meeting tldraw's terms / license key; see https://tldraw.dev). If enabled in production, satisfy that license separately. Intelligence features must not depend on it.
