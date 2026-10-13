#!/usr/bin/env bash
# Usage: backup.sh <database-url> <out-dir>
# Consistent snapshot (pg_dump uses one repeatable-read transaction) in custom format,
# plus a manifest of per-table row counts and content checksums for restore verification.
set -euo pipefail
url="$1"; out="$2"; ts="$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$out"
pg_dump --enable-row-security --format=custom --no-owner --no-privileges --file "$out/flow-$ts.dump" "$url"
"$(dirname "$0")/manifest.sh" "$url" > "$out/flow-$ts.manifest"
sha256sum "$out/flow-$ts.dump" > "$out/flow-$ts.dump.sha256"
echo "$out/flow-$ts.dump"
