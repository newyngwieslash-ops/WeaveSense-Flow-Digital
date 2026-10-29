#!/usr/bin/env bash
# Disposable six-role databases; private raw diagnostics are never uploaded.
set -euo pipefail
set +x
cd "$(dirname "$0")/../.."
: "${AGRI_IDENTITY_PYTHON:?Explicit locked Python environment required}"
AGRI_IDENTITY_PYTHON="$(cd "$(dirname "$AGRI_IDENTITY_PYTHON")" && pwd)/$(basename "$AGRI_IDENTITY_PYTHON")"
export AGRI_IDENTITY_PYTHON
mkdir -p .local/evidence
for suite in go python; do
  printf '%s\n' '{"passed":false,"status":"not_run","tests":[]}' > ".local/evidence/identity-$suite.json"
done
command -v docker >/dev/null
docker info >/dev/null 2>&1 || { echo 'BLOCKED: Docker daemon required'; exit 2; }
"$AGRI_IDENTITY_PYTHON" -c 'import fastapi,uvicorn,httpx,cryptography,rfc8785,asyncpg,pytest'
private=$(mktemp -d); chmod 700 "$private"
project="agri-identity-$(python3 -c 'import secrets; print(secrets.token_hex(12))')"
container="$project-db"; volume="$project-data"; network="$project-net"
if docker container inspect "$container" >/dev/null 2>&1 || docker volume inspect "$volume" >/dev/null 2>&1 || docker network inspect "$network" >/dev/null 2>&1; then
  rm -rf -- "$private"; echo 'Refusing existing CI resource'; exit 2
fi
cleanup() {
  code=$?; trap - EXIT
  docker rm -f "$container" >/dev/null 2>&1 || true
  if docker volume inspect "$volume" >/dev/null 2>&1; then docker volume rm "$volume" >/dev/null 2>&1 || code=1; fi
  if docker network inspect "$network" >/dev/null 2>&1; then docker network rm "$network" >/dev/null 2>&1 || code=1; fi
  rm -rf -- "$private"
  exit "$code"
}
trap cleanup EXIT
for name in POSTGRES_PASSWORD PLATFORM_MIGRATION_PASSWORD PLATFORM_RUNTIME_PASSWORD INTEL_MIGRATION_PASSWORD INTEL_RUNTIME_PASSWORD GEO_MIGRATION_PASSWORD GEO_RUNTIME_PASSWORD; do
  value=$(python3 -c 'import secrets; print(secrets.token_hex(32))')
  if [[ ${GITHUB_ACTIONS:-} == true ]]; then printf '::add-mask::%s\n' "$value"; fi
  export "$name=$value"
done
unset value
docker volume create --label "weavesense.ci.owner=$project" "$volume" >/dev/null
docker network create --label "weavesense.ci.owner=$project" "$network" >/dev/null
docker run -d --name "$container" --label "weavesense.ci.owner=$project" --network "$network" \
  -p 127.0.0.1::5432 -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD \
  -e PLATFORM_MIGRATION_PASSWORD -e PLATFORM_RUNTIME_PASSWORD -e INTEL_MIGRATION_PASSWORD \
  -e INTEL_RUNTIME_PASSWORD -e GEO_MIGRATION_PASSWORD -e GEO_RUNTIME_PASSWORD \
  --mount "type=volume,source=$volume,target=/var/lib/postgresql/data" \
  --mount "type=bind,source=$PWD/deploy/agriculture/postgres/init.sh,target=/docker-entrypoint-initdb.d/10-agriculture.sh,readonly" \
  postgis/postgis:17-3.5 >/dev/null
ready=false
for ((attempt=0;attempt<90;attempt++)); do
  if docker exec "$container" pg_isready -h 127.0.0.1 -U postgres -d flow_geo >/dev/null 2>&1; then ready=true; break; fi
  sleep 2
done
[[ $ready == true ]] || { echo 'Database bootstrap unavailable'; exit 1; }
port=$(docker port "$container" 5432/tcp)
[[ $port =~ ^127\.0\.0\.1:([0-9]+)$ ]] || { echo 'Non-loopback database binding refused'; exit 1; }
port=${BASH_REMATCH[1]}
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
      if [[ $other != "$domain" ]] && sql "$domain" "$kind" "flow_$other" 'SELECT 1' >/dev/null 2>&1; then echo 'Cross-database connection permitted'; exit 1; fi
    done
  done
done
export AGRI_TEST_GEO_MIGRATION_DSN="postgres://flow_geo_migrate:$GEO_MIGRATION_PASSWORD@127.0.0.1:$port/flow_geo?sslmode=disable"
export AGRI_TEST_GEO_RUNTIME_DSN="postgres://flow_geo_app:$GEO_RUNTIME_PASSWORD@127.0.0.1:$port/flow_geo?sslmode=disable"
export AGRI_TEST_PG_ISOLATED=1
export IDENTITY_PG_VERSION="$(sql geo app flow_geo 'SELECT version()')"
export IDENTITY_IMAGE_ID="$(docker inspect --format '{{.Image}}' "$container")"
python3 - <<'PY'
import datetime,json,os,platform
from pathlib import Path
Path('.local/evidence/identity-bootstrap.json').write_text(json.dumps({
 'fresh_volume':True,'six_roles_checked':True,'runtime_not_owner_checked':True,
 'cross_database_denial_checked':True,'data_class':'fixture','production_deployment':False,
 'postgres_version':os.environ['IDENTITY_PG_VERSION'],'image_id':os.environ['IDENTITY_IMAGE_ID'],
 'image_reference':'postgis/postgis:17-3.5','python':platform.python_version(),
 'started_at':datetime.datetime.now(datetime.timezone.utc).isoformat()},indent=2)+'\n')
PY
set +e
(cd services/agri-gis/backend && "$AGRI_IDENTITY_PYTHON" -m pytest tests/test_identity_canonical.py tests/test_agriculture_identity.py tests/test_agriculture_identity_postgres.py --junitxml="$private/python.xml" -q) > "$private/python.log" 2>&1
python_code=$?
python3 scripts/agriculture/identity-ci-result.py --kind python --input "$private/python.xml" --output .local/evidence/identity-python.json
python_gate=$?
"${GO_BIN:-go}" -C backend test ./internal/agriculture -run '^TestIdentityRealHTTPGoPython$' -count=1 -json > "$private/go.jsonl" 2> "$private/go-stderr.log"
go_code=$?
python3 scripts/agriculture/identity-ci-result.py --kind go --input "$private/go.jsonl" --output .local/evidence/identity-go.json
go_gate=$?
set -e
[[ $python_code == 0 && $python_gate == 0 && $go_code == 0 && $go_gate == 0 ]] || { echo 'Identity acceptance failed; inspect sanitized reports, no raw secrets exported'; exit 1; }
echo 'PASS: actual Go/Python HTTP and isolated PostgreSQL replay; fixture-domain acceptance only'
