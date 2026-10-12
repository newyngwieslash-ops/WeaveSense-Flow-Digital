# Upstreams and Licenses

| Remote | Repository | Pinned commit | Imported as | License |
|---|---|---|---|---|
| canvas-upstream | https://github.com/ddcat-ai/open-ai-canvas | `e70fe1b9c93d6af3b807a87ed15bb621af78095c` | repository root (full upstream history kept; `main` builds on this commit) | MIT, © 2026 ddcat and Open AI Canvas contributors — `LICENSE`, `NOTICE` |
| loom-upstream | https://github.com/newyngwieslash-ops/WeaveSense | `bec6212ac0cdc67272e488073e35d97087e7447a` | `services/loom-engine/` via `git subtree add` (history kept, not squashed) | MIT, © 2026 织识 Loom — `services/loom-engine/LICENSE` |

Both pinned commits equal the upstream `main` HEADs at import time (2026-10-12), so there is no discrepancy with the plan's audit baseline.

Updating: `git subtree pull --prefix=services/loom-engine loom-upstream <commit>`; for Canvas, `git fetch canvas-upstream && git merge <commit>`. Re-audit interfaces and migrations after any upgrade and update this table.

## Notes
- The root MIT license does **not** cover every dependency; lockfiles (`web/bun.lock`, `backend/go.sum`, `services/loom-engine/package-lock.json`, …) pull third-party components under their own licenses.
- **tldraw**: Canvas includes the optional tldraw drawing module, which has its own license (production use requires meeting tldraw's terms / license key; see https://tldraw.dev). If enabled in production, satisfy that license separately. Intelligence features must not depend on it.
