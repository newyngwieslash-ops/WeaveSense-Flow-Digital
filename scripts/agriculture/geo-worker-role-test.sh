#!/usr/bin/env bash
# Source only inside the owned, disposable ci-smoke database fixture. This
# extends, never replaces, its six existing role checks. sql uses TCP/password.
declare -F sql >/dev/null && declare -F deny >/dev/null || return 2
[[ $(sql geo worker flow_geo 'SELECT current_user') == flow_geo_worker ]]
[[ $(sql geo worker flow_geo 'SELECT rolcanlogin AND NOT (rolinherit OR rolsuper OR rolbypassrls OR rolcreatedb OR rolcreaterole OR rolreplication) FROM pg_roles WHERE rolname=current_user') == t ]]
[[ $(sql geo worker flow_geo "SELECT count(*) FROM pg_auth_members WHERE member=(SELECT oid FROM pg_roles WHERE rolname=current_user)") == 0 ]]
for database in flow_platform flow_intel; do deny geo worker "$database" 'SELECT 1'; done
for role in flow_geo_migrate flow_geo_app flow_geo_actor_guard; do
  deny geo worker flow_geo "SET ROLE $role"
done
deny geo app flow_geo 'SET ROLE flow_geo_worker'
deny geo worker flow_geo 'CREATE TABLE public.ci_worker_must_not_exist(id integer)'
# A fresh owner table must not accidentally inherit business-role grants.
sql geo migrate flow_geo 'CREATE TABLE public.ci_worker_privilege_probe(id integer PRIMARY KEY, tenant_id text NOT NULL); INSERT INTO public.ci_worker_privilege_probe VALUES (1, '\''fixture_a'\'')' >/dev/null
deny geo worker flow_geo 'SELECT * FROM public.ci_worker_privilege_probe'
deny geo worker flow_geo "BEGIN; SELECT set_config('app.tenant_ids','fixture_a',true); SELECT * FROM public.ci_worker_privilege_probe; COMMIT"
deny geo worker flow_geo "INSERT INTO public.ci_worker_privilege_probe VALUES (2,'fixture_b')"
deny geo worker flow_geo 'UPDATE public.ci_worker_privilege_probe SET tenant_id='\''fixture_b'\'''
deny geo worker flow_geo 'DELETE FROM public.ci_worker_privilege_probe'
deny geo worker flow_geo 'TRUNCATE public.ci_worker_privilege_probe'
deny geo worker flow_geo 'DROP TABLE public.ci_worker_privilege_probe'
[[ $(sql geo migrate flow_geo "SELECT count(*) FROM public.ci_worker_privilege_probe WHERE id=1 AND tenant_id='fixture_a'") == 1 ]]
sql geo migrate flow_geo 'DROP TABLE public.ci_worker_privilege_probe' >/dev/null
echo 'PASS: restricted geo worker password connection, no membership, cross-database/DDL/table access denied even with scope'
