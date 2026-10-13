#!/usr/bin/env bash
# Prints "<table> <rows> <md5 of ordered rows>" for every public table (stable across dump/restore).
set -euo pipefail
url="$1"
for t in $(psql "$url" -At -c "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY 1"); do
  psql "$url" -At -c "SELECT '$t', count(*), coalesce(md5(string_agg(md5(x::text), '' ORDER BY md5(x::text))), '-') FROM \"$t\" x" | tr '|' ' '
done
