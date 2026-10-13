#!/usr/bin/env bash
# Usage: restore.sh <dump-file> <empty-target-database-url>
# Verifies the dump checksum, restores in a single transaction, then compares the manifest.
set -euo pipefail
dump="$1"; url="$2"; base="${dump%.dump}"
[ -f "$dump.sha256" ] && sha256sum -c "$dump.sha256" >/dev/null
pg_restore --enable-row-security --no-owner --no-privileges --single-transaction --exit-on-error -d "$url" "$dump"
if [ -f "$base.manifest" ]; then
  diff <(sort "$base.manifest") <("$(dirname "$0")/manifest.sh" "$url" | sort) && echo "restore verified: manifest matches"
fi
