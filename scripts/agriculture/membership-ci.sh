#!/usr/bin/env bash
# Disposable local-only PostgreSQL integration; no production credentials/deployment.
set -euo pipefail
set +x
cd "$(dirname "$0")/../.."
mkdir -p .local/evidence
for report in membership-postgres membership-legacy-flow membership-bootstrap; do
  printf '%s\n' '{"passed":false,"status":"not_run","data_class":"fixture"}' > ".local/evidence/$report.json"
done
command -v docker >/dev/null || { echo 'BLOCKED: Docker CLI is required' >&2; exit 2; }
docker info >/dev/null 2>&1 || { echo 'BLOCKED: Docker daemon is required' >&2; exit 2; }
command -v go >/dev/null
command -v python3 >/dev/null
export MEMBERSHIP_STARTED_AT="$(python3 -c 'import datetime; print(datetime.datetime.now(datetime.timezone.utc).isoformat())')"
project="agri-membership-$(python3 -c 'import secrets; print(secrets.token_hex(12))')"
container="$project-db"; volume="$project-data"; network="$project-net"
if docker container inspect "$container" >/dev/null 2>&1 || docker volume inspect "$volume" >/dev/null 2>&1 || docker network inspect "$network" >/dev/null 2>&1; then
  echo 'Refusing to reuse an existing CI resource' >&2; exit 2
fi
cleanup() {
  code=$?; trap - EXIT
  docker rm -f "$container" >/dev/null 2>&1 || true
  if docker volume inspect "$volume" >/dev/null 2>&1; then
    if ! docker volume rm "$volume" >/dev/null 2>&1; then echo 'Owned CI volume cleanup failed' >&2; code=1; fi
  fi
  if docker network inspect "$network" >/dev/null 2>&1; then
    if ! docker network rm "$network" >/dev/null 2>&1; then echo 'Owned CI network cleanup failed' >&2; code=1; fi
  fi
  MEMBERSHIP_EXIT_CODE="$code" python3 - <<'PY'
import datetime,json,os
from pathlib import Path
path=Path('.local/evidence/membership-bootstrap.json')
evidence=json.loads(path.read_text())
if evidence.get('tests_outcome') == 'running':
    evidence['tests_outcome']='failed'
evidence.update(job_exit_code=int(os.environ['MEMBERSHIP_EXIT_CODE']),finished_at=datetime.datetime.now(datetime.timezone.utc).isoformat())
path.write_text(json.dumps(evidence,indent=2)+'\n')
PY
  exit "$code"
}
trap cleanup EXIT
for name in POSTGRES_PASSWORD PLATFORM_MIGRATION_PASSWORD PLATFORM_RUNTIME_PASSWORD INTEL_MIGRATION_PASSWORD INTEL_RUNTIME_PASSWORD GEO_MIGRATION_PASSWORD GEO_RUNTIME_PASSWORD GEO_WORKER_PASSWORD; do
  value=$(python3 -c 'import secrets; print(secrets.token_hex(32))')
  if [[ ${GITHUB_ACTIONS:-} == true ]]; then printf '::add-mask::%s\n' "$value"; fi
  export "$name=$value"
done
unset value
docker volume create --label "weavesense.ci.owner=$project" "$volume" >/dev/null
docker network create --label "weavesense.ci.owner=$project" "$network" >/dev/null
docker run -d --name "$container" --label "weavesense.ci.owner=$project" --network "$network" \
  -p 127.0.0.1::5432 -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD \
  -e PLATFORM_MIGRATION_PASSWORD -e PLATFORM_RUNTIME_PASSWORD \
  -e INTEL_MIGRATION_PASSWORD -e INTEL_RUNTIME_PASSWORD -e GEO_MIGRATION_PASSWORD -e GEO_RUNTIME_PASSWORD -e GEO_WORKER_PASSWORD \
  --mount "type=volume,source=$volume,target=/var/lib/postgresql/data" \
  --mount "type=bind,source=$PWD/deploy/agriculture/postgres/init.sh,target=/docker-entrypoint-initdb.d/10-agriculture.sh,readonly" \
  postgis/postgis:17-3.5 >/dev/null
ready=false
for ((attempt=0; attempt<90; attempt++)); do
  if docker exec "$container" pg_isready -h 127.0.0.1 -U postgres -d flow_platform >/dev/null 2>&1; then ready=true; break; fi
  sleep 2
done
[[ $ready == true ]] || { echo 'PostgreSQL bootstrap did not become ready; no tests accepted' >&2; exit 1; }
port=$(docker port "$container" 5432/tcp)
[[ $port =~ ^127\.0\.0\.1:([0-9]+)$ ]] || { echo 'Database binding is not loopback-only' >&2; exit 1; }
port=${BASH_REMATCH[1]}
export AGRI_TEST_PG_MIGRATION_DSN="postgres://flow_platform_migrate:$PLATFORM_MIGRATION_PASSWORD@127.0.0.1:$port/flow_platform?sslmode=disable"
export AGRI_TEST_PG_RUNTIME_DSN="postgres://flow_platform_app:$PLATFORM_RUNTIME_PASSWORD@127.0.0.1:$port/flow_platform?sslmode=disable"
export AGRI_TEST_PG_ISOLATED=1
export CANVAS_DATABASE_DRIVER=postgres DATABASE_URL="$AGRI_TEST_PG_MIGRATION_DSN"

sql() {
  local domain=$1 kind=$2 database=$3 statement=$4 variable
  variable="${domain^^}_$([[ $kind == migrate ]] && echo MIGRATION || echo RUNTIME)_PASSWORD"
  docker exec "$container" sh -c 'export PGPASSWORD="$(printenv "$1")"; shift; exec psql -X -qAt -v ON_ERROR_STOP=1 -h 127.0.0.1 "$@"' \
    sh "$variable" -U "flow_${domain}_${kind}" -d "$database" -c "$statement"
}
for domain in platform intel geo; do
  for kind in migrate app; do
    [[ $(sql "$domain" "$kind" "flow_$domain" 'SELECT current_user') == "flow_${domain}_${kind}" ]]
    [[ $(sql "$domain" "$kind" "flow_$domain" 'SELECT NOT (rolsuper OR rolbypassrls OR rolcreatedb OR rolcreaterole OR rolreplication) FROM pg_roles WHERE rolname=current_user') == t ]]
    if [[ $kind == app ]]; then
      [[ $(sql "$domain" "$kind" "flow_$domain" 'SELECT datdba <> (SELECT oid FROM pg_roles WHERE rolname=current_user) FROM pg_database WHERE datname=current_database()') == t ]]
      [[ $(sql "$domain" "$kind" "flow_$domain" "SELECT NOT pg_has_role(current_user, 'flow_${domain}_migrate', 'MEMBER') AND NOT has_schema_privilege(current_user, 'public', 'CREATE')") == t ]]
    fi
    for other in platform intel geo; do
      if [[ $other != "$domain" ]] && sql "$domain" "$kind" "flow_$other" 'SELECT 1' >/dev/null 2>&1; then
        echo 'Cross-database connection unexpectedly permitted' >&2; exit 1
      fi
    done
  done
done
[[ $(sql platform migrate flow_platform "SELECT NOT pg_has_role(current_user, 'flow_platform_admission', 'USAGE') AND pg_has_role(current_user, 'flow_platform_admission', 'SET')") == t ]]
[[ $(sql geo app flow_geo 'SELECT ST_SRID(ST_SetSRID(ST_Point(0,0),4326))') == 4326 ]]

redact() {
  python3 -u -c '
import os,re,sys
values=[v for k,v in os.environ.items() if k.endswith("_PASSWORD") or k.endswith("_DSN") or k == "DATABASE_URL"]
for line in sys.stdin:
    for value in values:
        if value: line=line.replace(value,"[REDACTED]")
    sys.stdout.write(re.sub(r"postgres(?:ql)?://[^\s\"\x27]+","[REDACTED_DATABASE_URL]",line))
'
}
go -C backend run ./cmd/migrate-schema up 2>&1 | redact
before=$(sql platform migrate flow_platform "SELECT md5(string_agg(version::text || ':' || checksum, ',' ORDER BY version)) FROM schema_migrations")
[[ $(sql platform migrate flow_platform 'SELECT count(*) FROM schema_migrations WHERE version=82') == 1 ]]
[[ $(sql platform migrate flow_platform 'SELECT count(*) FROM schema_migrations WHERE version=83') == 1 ]]
go -C backend run ./cmd/migrate-schema up 2>&1 | redact
[[ $(sql platform migrate flow_platform "SELECT md5(string_agg(version::text || ':' || checksum, ',' ORDER BY version)) FROM schema_migrations") == "$before" ]]
go -C backend run ./cmd/migrate-schema verify 2>&1 | redact
export MEMBERSHIP_POSTGRES_VERSION="$(sql platform app flow_platform 'SELECT version()')"
export MEMBERSHIP_IMAGE_ID="$(docker inspect --format '{{.Image}}' "$container")"
export MEMBERSHIP_IMAGE_DIGESTS="$(docker image inspect --format '{{json .RepoDigests}}' "$MEMBERSHIP_IMAGE_ID")"
export MEMBERSHIP_GO_VERSION="$(go version)"
python3 - <<'PY'
import datetime,json,os,platform
from pathlib import Path
manifest=Path('.local/evidence/session-ci-provenance.json')
source=json.loads(manifest.read_text()) if manifest.exists() else {}
evidence={
    'fresh_volume':True,'six_database_roles_checked':True,'runtime_not_owner_checked':True,
    'cross_database_denial_checked':True,'migration_82_applied_and_replayed':True,
    'migration_83_applied_and_replayed':True,'postgis_checked':True,
    'data_class':'fixture','production_deployment':False,'tests_outcome':'running',
    'started_at':os.environ['MEMBERSHIP_STARTED_AT'],
    'bootstrap_completed_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'environment':{'postgres_version':os.environ['MEMBERSHIP_POSTGRES_VERSION'],
        'image_reference':'postgis/postgis:17-3.5','image_id':os.environ['MEMBERSHIP_IMAGE_ID'],
        'image_digests':json.loads(os.environ['MEMBERSHIP_IMAGE_DIGESTS']) or [],
        'go_version':os.environ['MEMBERSHIP_GO_VERSION'],'os':platform.platform()},
    'source_commit':source.get('source_commit'),'source_mode':source.get('mode'),
}
Path('.local/evidence/membership-bootstrap.json').write_text(json.dumps(evidence,indent=2)+'\n')
PY
unset DATABASE_URL CANVAS_DATABASE_DRIVER
go -C backend test -p 1 -count=1 -json ./internal/database ./internal/agriculture -run '^TestAgriculturePostgres' \
  | python3 scripts/agriculture/membership-ci-result.py --suite postgres --output .local/evidence/membership-postgres.json
go -C backend test -count=1 -json ./internal/flow -run '^Test(OrgInviteSingleUseAndGrantRevocation|P7MembersRoleEditing|AgricultureMembership)' \
  | python3 scripts/agriculture/membership-ci-result.py --suite legacy-flow --output .local/evidence/membership-legacy-flow.json
python3 - <<'PY'
import datetime,json
from pathlib import Path
path=Path('.local/evidence/membership-bootstrap.json')
evidence=json.loads(path.read_text())
evidence.update(tests_outcome='passed',completed_at=datetime.datetime.now(datetime.timezone.utc).isoformat())
path.write_text(json.dumps(evidence,indent=2)+'\n')
PY
echo 'PASS: isolated PostgreSQL membership tests and legacy regression; not live farm or production acceptance'
