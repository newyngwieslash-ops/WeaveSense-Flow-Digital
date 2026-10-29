#!/usr/bin/env bash
# Required real HTTP cross-language test: absent interpreter is failure, not skip.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
: "${AGRI_IDENTITY_PYTHON:?Set explicit Python interpreter with locked identity dependencies}"
# Preserve the venv interpreter symlink; resolving it would bypass pyvenv.cfg.
AGRI_IDENTITY_PYTHON="$(cd "$(dirname "$AGRI_IDENTITY_PYTHON")" && pwd)/$(basename "$AGRI_IDENTITY_PYTHON")"
export AGRI_IDENTITY_PYTHON
"$AGRI_IDENTITY_PYTHON" -c 'import fastapi, uvicorn, httpx, cryptography, rfc8785, asyncpg'
cd "$ROOT/backend"
"${GO_BIN:-go}" test ./internal/agriculture -run '^TestIdentityRealHTTPGoPython$' -count=1 -v
