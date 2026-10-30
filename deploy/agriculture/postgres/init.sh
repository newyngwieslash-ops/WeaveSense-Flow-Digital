#!/bin/sh
# First boot only; SQL identifiers are fixed, passwords are psql literal variables.
set -eu
: "${GEO_WORKER_PASSWORD:?configure independent geo worker secret}"
for domain in platform intel geo; do
  case "$domain" in
    platform) owner_password=$PLATFORM_MIGRATION_PASSWORD; runtime_password=$PLATFORM_RUNTIME_PASSWORD ;;
    intel) owner_password=$INTEL_MIGRATION_PASSWORD; runtime_password=$INTEL_RUNTIME_PASSWORD ;;
    geo) owner_password=$GEO_MIGRATION_PASSWORD; runtime_password=$GEO_RUNTIME_PASSWORD ;;
  esac
  export AGRI_OWNER_PASSWORD="$owner_password" AGRI_RUNTIME_PASSWORD="$runtime_password"
  psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d postgres \
    -v db="flow_$domain" -v owner="flow_${domain}_migrate" \
    -v runtime="flow_${domain}_app" <<'SQL'
\getenv owner_password AGRI_OWNER_PASSWORD
\getenv runtime_password AGRI_RUNTIME_PASSWORD
CREATE ROLE :"owner" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'owner_password';
CREATE ROLE :"runtime" LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'runtime_password';
CREATE DATABASE :"db" OWNER :"owner";
REVOKE ALL ON DATABASE :"db" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"db" TO :"runtime";
SQL
  unset AGRI_OWNER_PASSWORD AGRI_RUNTIME_PASSWORD
  psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "flow_$domain" \
    -v owner="flow_${domain}_migrate" -v runtime="flow_${domain}_app" <<'SQL'
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA public TO :"owner";
GRANT USAGE ON SCHEMA public TO :"runtime";
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner" IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO :"runtime";
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner" IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO :"runtime";
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner" IN SCHEMA public REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;
SQL
done
psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d flow_platform <<'SQL'
-- The resolver owner cannot log in or bypass RLS. Only the migration role may
-- transfer function ownership; the application is never a member of this role.
CREATE ROLE flow_platform_admission NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
-- Ownership transfer requires SET membership, not inherited privileges. Inheriting
-- this role would also apply its restrictive invitation policy to migrations.
GRANT flow_platform_admission TO flow_platform_migrate WITH INHERIT FALSE;
SQL
export AGRI_WORKER_PASSWORD="$GEO_WORKER_PASSWORD"
psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d flow_geo <<'SQL'
\getenv worker_password AGRI_WORKER_PASSWORD
-- No default table grants or role memberships. This is not a system bypass.
CREATE ROLE flow_geo_worker LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'worker_password';
GRANT CONNECT ON DATABASE flow_geo TO flow_geo_worker;
GRANT USAGE ON SCHEMA public TO flow_geo_worker;
CREATE ROLE flow_geo_actor_guard NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
GRANT flow_geo_actor_guard TO flow_geo_migrate WITH INHERIT FALSE;
CREATE EXTENSION IF NOT EXISTS postgis;
GRANT USAGE ON SCHEMA public TO flow_geo_app;
SQL
unset AGRI_WORKER_PASSWORD
