#!/usr/bin/env bash
# Usage: objects-restore.sh <backup-dir> <mc-alias>
# Verifies the local backup against objects.manifest, uploads every bucket, then
# downloads the restored buckets again and compares checksums end to end.
set -euo pipefail
src="$1"; alias="$2"
( cd "$src" && sha256sum --quiet -c objects.manifest )
buckets=$(cd "$src" && awk '{print $2}' objects.manifest | cut -d/ -f1 | sort -u)
for b in $buckets; do
  mc mb -p "$alias/$b" >/dev/null
  mc mirror --quiet --overwrite "$src/$b" "$alias/$b" >/dev/null
done
check="$(mktemp -d)"; trap 'rm -rf "$check"' EXIT
for b in $buckets; do mkdir -p "$check/$b"; mc mirror --quiet "$alias/$b" "$check/$b" >/dev/null; done
( cd "$check" && sha256sum --quiet -c "$src/objects.manifest" ) && echo "objects restore verified: $(wc -l < "$src/objects.manifest") files match"
