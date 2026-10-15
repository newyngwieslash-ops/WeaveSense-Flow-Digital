# Verification receipts

This file records what was run, where it ran, and the result. Rules:

- Record only commands that actually ran.
- "Not run" means not run.
- Times are Asia/Shanghai.

## Environments
- **Build box**: Linux, Go 1.24, Node 22.19 (PGlite), Python 3.13, bun 1.3. No Docker. No Postgres server. No tesseract.
- **GitHub Actions** (`.github/workflows/quality.yml`):
  - Backend checks (Go plus a `postgres:17-alpine` service).
  - Loom Engine flow tests: a `postgres:17-alpine` service, the non-superuser `flow_app` role, Python worker tests, and tesseract.
  - Web checks (bun, including the Previs Chrome E2E).
  - Payment plugin artifacts.

## How to reproduce
```sh
cd backend && go vet ./... && go test ./internal/flow/ ./internal/database/ -count=1
cd services/loom-engine && npm ci --ignore-scripts && npm run test:flow    # PGlite; set FLOW_TEST_PG_ADMIN_URL for real PG17
cd services/loom-engine/workers/ocr && python -m pytest -q                 # real OCR case skipped without tesseract
cd web && bun install --frozen-lockfile && bun run typecheck && bun run lint && bun test && npx prettier --check .
```

## Receipts: gap-closing round (2026-10-14)
See `receipts-2026-10-14.md`.
