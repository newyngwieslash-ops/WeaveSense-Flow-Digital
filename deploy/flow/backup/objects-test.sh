#!/usr/bin/env bash
# Object-storage backup/restore round trip against a live MinIO.
# Run inside an image with mc + coreutils (e.g. the compose minio-client image):
#   docker run --rm --network <project>_internal -e MINIO_ROOT_USER -e MINIO_ROOT_PASSWORD \
#     -v "$PWD/deploy/flow/backup:/b:ro" --entrypoint /bin/bash bitnamilegacy/minio-client:2025.7.21 /b/objects-test.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
mc alias set t "${MINIO_URL:-http://minio:9000}" "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
b1="bk-test-a-$RANDOM"; b2="bk-test-b-$RANDOM"
mc mb -p "t/$b1" "t/$b2" >/dev/null
work="$(mktemp -d)"; trap 'rm -rf "$work"; mc rb --force "t/$b1" "t/$b2" >/dev/null 2>&1 || true' EXIT
head -c 300000 /dev/urandom > "$work/big.bin"; echo "hello" > "$work/a.txt"
mc cp -q "$work/big.bin" "t/$b1/telegram/chan/1.bin" >/dev/null
mc cp -q "$work/a.txt" "t/$b1/reports/r1.txt" >/dev/null
mc cp -q "$work/a.txt" "t/$b2/x/y/z.txt" >/dev/null
"$here/objects-backup.sh" t "$work/backup" "$b1" "$b2"
mc rb --force "t/$b1" "t/$b2" >/dev/null   # disaster
"$here/objects-restore.sh" "$work/backup" t
# tamper detection: a corrupted backup must be refused
echo x >> "$work/backup/$b1/reports/r1.txt"
if "$here/objects-restore.sh" "$work/backup" t >/dev/null 2>&1; then echo "FAIL: corrupted backup accepted"; exit 1; fi
echo "PASS objects backup/restore round trip + tamper detection"
