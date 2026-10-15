#!/usr/bin/env bash
# CI test for daily.sh against a real PostgreSQL (DATABASE_URL): one cycle produces
# dump + manifest + sha256 + last-success; retention prunes old dumps but keeps the minimum.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
out="$(mktemp -d)"
psql "$DATABASE_URL" -qc "CREATE TABLE IF NOT EXISTS t_daily (id int primary key, v text); INSERT INTO t_daily VALUES (1,'a') ON CONFLICT DO NOTHING"
# Five stale dumps from earlier days.
mkdir -p "$out/intel"
for i in 1 2 3 4 5; do
  f="$out/intel/flow-2000010${i}T000000Z.dump"; echo old > "$f"; echo x > "$f.sha256"; echo m > "${f%.dump}.manifest"
  touch -t "$(date -u -d "@$(( $(date -u +%s) - (20 + i) * 86400 ))" +%Y%m%d%H%M)" "$f"
done
BACKUP_ONCE=1 BACKUP_DIR="$out" BACKUP_RETENTION_DAYS=14 BACKUP_KEEP_MIN=3 BACKUP_DATABASE_URLS="intel=$DATABASE_URL" "$here/daily.sh"
test -s "$out/last-success"
new=$(ls -1t "$out"/intel/flow-*.dump | head -1)
grep -q "t_daily 1 " "${new%.dump}.manifest"
sha256sum -c "$new.sha256" >/dev/null
count=$(ls -1 "$out"/intel/flow-*.dump | wc -l)
[ "$count" -eq 3 ] || { echo "retention: expected 3 dumps (new + 2 newest old), got $count"; ls -l "$out/intel"; exit 1; }
# The restored dump matches.
"$here/restore.sh" "$new" "$RESTORE_URL" >/dev/null
"$here/manifest.sh" "$RESTORE_URL" | grep -q "t_daily 1 "
echo "daily backup test: OK ($count dumps kept)"
# Scheduler: the next run is within the next 24h.
s=$(bash -c "source <(sed -n '/^seconds_until/,/^}/p' '$here/daily.sh'); seconds_until 03:00")
[ "$s" -gt 0 ] && [ "$s" -le 86400 ] || { echo "seconds_until out of range: $s"; exit 1; }
