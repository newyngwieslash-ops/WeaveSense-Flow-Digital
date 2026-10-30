#!/usr/bin/env bash
# Destructive only to this invocation's unique Docker Compose project/volumes.
# This probes W00 dependencies, not tenant RLS or business/live acceptance.
set -euo pipefail
set +x
mode=${1:-core}
case "$mode" in core|migrations) ;; *) echo 'Usage: ci-smoke.sh [core|migrations]' >&2; exit 2;; esac
cd "$(dirname "$0")/../.."
command -v docker >/dev/null || { echo 'BLOCKED: Docker CLI is required' >&2; exit 2; }
docker compose version >/dev/null
docker info >/dev/null 2>&1 || { echo 'BLOCKED: Docker daemon is required' >&2; exit 2; }
command -v python3 >/dev/null

# Do not read .env or reuse caller passwords, ports, or a caller Compose project.
for name in POSTGRES_PASSWORD PLATFORM_MIGRATION_PASSWORD PLATFORM_RUNTIME_PASSWORD \
  INTEL_MIGRATION_PASSWORD INTEL_RUNTIME_PASSWORD GEO_MIGRATION_PASSWORD \
  GEO_RUNTIME_PASSWORD REDIS_PASSWORD FLOW_INTERNAL_JWT_SIGNING_KEY; do
  value=$(python3 -c 'import secrets; print(secrets.token_hex(32))')
  if [[ ${GITHUB_ACTIONS:-} == true ]]; then printf '::add-mask::%s\n' "$value"; fi
  export "$name=$value"
done
unset value
export AGRI_HTTP_PORT=0
export FLOW_AGRICULTURE_EXTERNAL_ORIGIN=http://127.0.0.1:8091
export FLOW_AGRICULTURE_ALLOW_HTTP_LOOPBACK_DEV=true
project="agri-ci-$(python3 -c 'import secrets; print(secrets.token_hex(12))')"
compose=(docker compose --env-file /dev/null -p "$project" -f docker-compose.agriculture.yml --profile core)
if [[ $mode == migrations ]]; then compose+=(--profile platform --profile knowledge); fi
# Random namespace is also checked before cleanup is armed. Never prune Docker.
[[ -z $(docker ps -aq --filter "label=com.docker.compose.project=$project") ]]
[[ -z $(docker volume ls -q --filter "label=com.docker.compose.project=$project") ]]
csrf_dir=$(mktemp -d)
export FLOW_AGRICULTURE_CSRF_KEY_FILE="$csrf_dir/csrf.key"
compose_started=false
cleanup() {
  code=$?
  trap - EXIT
  if [[ $compose_started == true ]] && ! "${compose[@]}" down --volumes --remove-orphans --timeout 15 >/dev/null 2>&1; then
    echo "ERROR: cleanup failed for owned project $project" >&2
    [[ $code != 0 ]] || code=1
  fi
  if [[ -n ${csrf_dir:-} ]]; then rm -f -- "$csrf_dir/csrf.key"; rmdir -- "$csrf_dir"; fi
  exit "$code"
}
trap cleanup EXIT
python3 -c 'import os,secrets; p=os.environ["FLOW_AGRICULTURE_CSRF_KEY_FILE"]; fd=os.open(p,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600); os.write(fd,secrets.token_bytes(32)); os.close(fd)'
echo "W00 smoke mode=$mode project=$project (fresh ephemeral demo databases)"
compose_started=true
"${compose[@]}" up -d --build --wait --wait-timeout 180 postgres redis geo-readiness

# TCP forces password authentication; Unix-socket init trust is not an auth test.
sql() {
  local domain=$1 kind=$2 db=$3 query=$4 password_var
  case "$domain:$kind" in
    platform:migrate) password_var=PLATFORM_MIGRATION_PASSWORD;;
    platform:app) password_var=PLATFORM_RUNTIME_PASSWORD;;
    intel:migrate) password_var=INTEL_MIGRATION_PASSWORD;;
    intel:app) password_var=INTEL_RUNTIME_PASSWORD;;
    geo:migrate) password_var=GEO_MIGRATION_PASSWORD;;
    geo:app) password_var=GEO_RUNTIME_PASSWORD;;
    *) return 2;;
  esac
  "${compose[@]}" exec -T postgres sh -c \
    'export PGPASSWORD="$(printenv "$1")"; shift; exec psql -X -qAt -v ON_ERROR_STOP=1 -h 127.0.0.1 "$@"' \
    sh "$password_var" -U "flow_${domain}_${kind}" -d "$db" -c "$query"
}
deny() {
  if sql "$@" >/dev/null 2>&1; then echo 'FAIL: forbidden database operation succeeded' >&2; exit 1; fi
}
redact_output() {
  python3 -u -c '
import os, re, sys
secrets = [v for k, v in os.environ.items() if k.endswith("_PASSWORD") or k == "FLOW_INTERNAL_JWT_SIGNING_KEY"]
for line in sys.stdin:
    line = re.sub(r"(?:postgres(?:ql)?|redis)://[^\s\"\x27]+", "[REDACTED_DATABASE_URL]", line)
    for value in secrets:
        if value:
            line = line.replace(value, "[REDACTED]")
    sys.stdout.write(line)
'
}
for domain in platform intel geo; do
  db="flow_$domain"
  for kind in migrate app; do
    [[ $(sql "$domain" "$kind" "$db" 'SELECT current_user') == "flow_${domain}_${kind}" ]]
    [[ $(sql "$domain" "$kind" "$db" 'SELECT NOT (rolsuper OR rolbypassrls OR rolcreatedb OR rolcreaterole OR rolreplication) FROM pg_roles WHERE rolname=current_user') == t ]]
    for other in platform intel geo; do
      [[ $other == "$domain" ]] || deny "$domain" "$kind" "flow_$other" 'SELECT 1'
    done
  done
  deny "$domain" app "$db" 'CREATE TABLE public.must_not_exist(id integer)'
  deny "$domain" app "$db" "SET ROLE flow_${domain}_migrate"
  sql "$domain" migrate "$db" 'CREATE TABLE public.ci_privilege_probe(id integer PRIMARY KEY)' >/dev/null
  sql "$domain" app "$db" 'INSERT INTO public.ci_privilege_probe VALUES (1); UPDATE public.ci_privilege_probe SET id=2; DELETE FROM public.ci_privilege_probe' >/dev/null
  deny "$domain" app "$db" 'DROP TABLE public.ci_privilege_probe'
  sql "$domain" migrate "$db" 'DROP TABLE public.ci_privilege_probe' >/dev/null
done
[[ $(sql geo app flow_geo 'SELECT ST_SRID(ST_SetSRID(ST_Point(0,0),4326))') == 4326 ]]
"${compose[@]}" exec -T geo-readiness python -c '
import json, urllib.request, urllib.error
with urllib.request.urlopen("http://127.0.0.1:8000/health/ready", timeout=5) as response:
    body = json.load(response)
    assert response.status == 200 and body["business_api"] == "not_implemented"
try:
    urllib.request.urlopen("http://127.0.0.1:8000/api/agri/v1/parcels", timeout=5)
except urllib.error.HTTPError as error:
    assert error.code == 503
else:
    raise AssertionError("Business route unexpectedly enabled")
'
echo 'PASS: fresh database roles, password connections, cross-database denial, runtime DDL denial, default DML privileges, PostGIS and bounded readiness'

if [[ $mode == migrations ]]; then
  # Build and run the real pinned upstream migrations with separate owner roles.
  # Never launch a release workflow, business server, external provider or agent.
  "${compose[@]}" build platform-migrate loom-migrate
  for service in platform-migrate loom-migrate; do
    case "$service" in platform-migrate) domain=platform;; loom-migrate) domain=intel;; esac
    "${compose[@]}" run --rm --no-deps "$service" 2>&1 | redact_output
    count=$(sql "$domain" migrate "flow_$domain" "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'")
    [[ $count =~ ^[0-9]+$ && $count -gt 0 ]]
    "${compose[@]}" run --rm --no-deps "$service" 2>&1 | redact_output
    [[ $(sql "$domain" migrate "flow_$domain" "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'") == "$count" ]]
    echo "PASS: $service applied and replayed, tables=$count"
  done
  "${compose[@]}" run --rm --no-deps platform-migrate migrate-schema verify 2>&1 | redact_output
  [[ $(sql intel app flow_intel 'SELECT count(*) > 0 FROM flow_schema_migrations') == t ]]
fi
echo "PASS: W00 $mode smoke; NOT tenant isolation, GIS migration, live data or product acceptance"
