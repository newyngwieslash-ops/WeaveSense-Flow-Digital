#!/bin/sh
# First boot only. Existing volumes need the reviewed operator migration path.
# Fixed role names match the audited SQL; passwords never interpolate into SQL.
set -eu
: "${PLATFORM_MIGRATION_PASSWORD:?required independent migration secret}"
: "${INTEL_MIGRATION_PASSWORD:?required independent migration secret}"
: "${PLATFORM_SYSTEM_PASSWORD:?required independent dispatcher secret}"
: "${INTEL_SYSTEM_PASSWORD:?required independent dispatcher secret}"
for domain in platform intel; do
  case "$domain" in
    platform) export FLOW_INIT_OWNER="$PLATFORM_MIGRATION_PASSWORD" FLOW_INIT_APP="$PLATFORM_DB_PASSWORD" FLOW_INIT_SYSTEM="$PLATFORM_SYSTEM_PASSWORD";;
    intel) export FLOW_INIT_OWNER="$INTEL_MIGRATION_PASSWORD" FLOW_INIT_APP="$INTEL_DB_PASSWORD" FLOW_INIT_SYSTEM="$INTEL_SYSTEM_PASSWORD";;
  esac
  psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d postgres \
    -v db="flow_$domain" -v owner="flow_${domain}_migrate" \
    -v app="flow_${domain}_app" -v system="flow_${domain}_system" -v guard="flow_${domain}_guard" <<'SQL'
\getenv owner_password FLOW_INIT_OWNER
\getenv app_password FLOW_INIT_APP
\getenv system_password FLOW_INIT_SYSTEM
CREATE ROLE :"owner" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'owner_password';
CREATE ROLE :"app" LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'app_password';
CREATE ROLE :"system" LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'system_password';
CREATE ROLE :"guard" NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
GRANT :"guard" TO :"owner" WITH INHERIT FALSE, SET TRUE;
CREATE DATABASE :"db" OWNER :"owner";
REVOKE ALL ON DATABASE :"db" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"db" TO :"app", :"system";
SQL
  psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "flow_$domain" \
    -v owner="flow_${domain}_migrate" -v app="flow_${domain}_app" \
    -v system="flow_${domain}_system" -v guard="flow_${domain}_guard" <<'SQL'
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA public TO :"owner";
GRANT USAGE ON SCHEMA public TO :"app", :"system", :"guard";
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner" IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO :"app";
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner" IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO :"app";
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner" IN SCHEMA public REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;
SQL
done
unset FLOW_INIT_OWNER FLOW_INIT_APP FLOW_INIT_SYSTEM
psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d flow_platform <<'SQL'
CREATE ROLE flow_platform_admission NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
GRANT flow_platform_admission TO flow_platform_migrate WITH INHERIT FALSE, SET TRUE;
SQL
