#!/usr/bin/env bash
# PRD §14 "每日备份": scheduled daily backup of both databases (backup.sh: dump +
# row-count/checksum manifest + sha256), with retention.
#
# Env:
#   BACKUP_DATABASE_URLS   space-separated "name=postgres://..." pairs (e.g. platform=... intel=...)
#   BACKUP_DIR             output root (default /backups); one sub-directory per name
#   BACKUP_AT              daily run time, UTC HH:MM (default 03:00)
#   BACKUP_RETENTION_DAYS  delete dumps older than this many days (default 14; keeps at least BACKUP_KEEP_MIN)
#   BACKUP_KEEP_MIN        never prune below this many dumps per database (default 3)
#   BACKUP_OBJECT_BUCKETS  optional: space-separated buckets to mirror with objects-backup.sh
#                          (needs mc + MINIO_URL/MINIO_ROOT_USER/MINIO_ROOT_PASSWORD); one dated snapshot per day
#   BACKUP_ONCE=1          run one backup cycle and exit (cron / CI / manual)
# Each cycle writes $BACKUP_DIR/last-success (UTC timestamp) or exits non-zero.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
dir="${BACKUP_DIR:-/backups}"; at="${BACKUP_AT:-03:00}"
keep_days="${BACKUP_RETENTION_DAYS:-14}"; keep_min="${BACKUP_KEEP_MIN:-3}"

prune() { # prune <dir>: drop dumps (and their manifest/sha) older than keep_days, keeping the newest keep_min
  local d="$1" n=0 f base
  for f in $(ls -1t "$d"/flow-*.dump 2>/dev/null); do
    n=$((n + 1))
    [ "$n" -le "$keep_min" ] && continue
    if [ -n "$(find "$f" -mtime +"$((keep_days - 1))" -print)" ]; then
      base="${f%.dump}"
      rm -f "$f" "$f.sha256" "$base.manifest"
      echo "pruned $f"
    fi
  done
}

prune_dirs() { # same retention for dated object snapshots
  local d="$1" n=0 f
  for f in $(ls -1td "$d"/*/ 2>/dev/null); do
    n=$((n + 1))
    [ "$n" -le "$keep_min" ] && continue
    if [ -n "$(find "${f%/}" -maxdepth 0 -mtime +"$((keep_days - 1))" -print)" ]; then rm -rf "${f%/}"; echo "pruned ${f%/}"; fi
  done
}

cycle() {
  [ -n "${BACKUP_DATABASE_URLS:-}${BACKUP_OBJECT_BUCKETS:-}" ] || { echo "BACKUP_DATABASE_URLS / BACKUP_OBJECT_BUCKETS are empty" >&2; return 2; }
  local pair name url
  if [ -n "${BACKUP_OBJECT_BUCKETS:-}" ]; then
    local snap="$dir/objects/$(date -u +%Y%m%dT%H%M%SZ)"
    mc alias set flowbk "${MINIO_URL:-http://minio:9000}" "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
    # shellcheck disable=SC2086
    "$here/objects-backup.sh" flowbk "$snap" $BACKUP_OBJECT_BUCKETS
    prune_dirs "$dir/objects"
  fi
  for pair in ${BACKUP_DATABASE_URLS:-}; do
    name="${pair%%=*}"; url="${pair#*=}"
    mkdir -p "$dir/$name"
    "$here/backup.sh" "$url" "$dir/$name"
    prune "$dir/$name"
  done
  date -u +%Y-%m-%dT%H:%M:%SZ > "$dir/last-success"
}

seconds_until() { # seconds from now until the next HH:MM UTC
  local now target
  now=$(date -u +%s)
  target=$(date -u -d "$(date -u +%Y-%m-%d) $1" +%s)
  [ "$target" -le "$now" ] && target=$((target + 86400))
  echo $((target - now))
}

if [ "${BACKUP_ONCE:-}" = "1" ]; then cycle; exit $?; fi
while true; do
  s=$(seconds_until "$at"); echo "next backup in ${s}s (at $at UTC)"; sleep "$s"
  cycle || echo "backup cycle FAILED at $(date -u +%FT%TZ)" >&2
done
