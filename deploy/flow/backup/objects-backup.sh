#!/usr/bin/env bash
# Usage: objects-backup.sh <mc-alias> <out-dir> <bucket>...
# Mirrors each bucket to <out-dir>/<bucket>/ and writes <out-dir>/objects.manifest
# (sha256 + relative path of every object) for restore verification.
set -euo pipefail
alias="$1"; out="$2"; shift 2
mkdir -p "$out"
for b in "$@"; do
  mkdir -p "$out/$b"
  mc mirror --quiet --overwrite "$alias/$b" "$out/$b" >/dev/null
done
( cd "$out" && find "$@" -type f -print0 | sort -z | xargs -0 -r sha256sum ) > "$out/objects.manifest"
echo "objects: $(wc -l < "$out/objects.manifest") files -> $out"
