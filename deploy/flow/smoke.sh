#!/usr/bin/env bash
# HTTP end-to-end smoke test against a running compose stack (docker-compose.flow.yml).
# Usage: deploy/flow/smoke.sh [base_url]   (default http://localhost:8088)
# Logs in as SMOKE_USER/SMOKE_PASSWORD if set (registering it when it does not exist
# yet, e.g. as the first user); otherwise registers smoke_<random>, which only works
# while registration is open. No credentials are printed.
set -euo pipefail
BASE="${1:-http://localhost:8088}"
JAR="$(mktemp)"; trap 'rm -f "$JAR"' EXIT
USER_NAME="${SMOKE_USER:-smoke_$RANDOM$RANDOM}"; PASS="${SMOKE_PASSWORD:-Smoke-$(head -c 9 /dev/urandom | od -An -tx1 | tr -d ' \n')}"
pass=0; failn=0
check() { # name expected_code method path [json]
  local name="$1" want="$2" m="$3" p="$4" body="${5:-}" code
  if [ -n "$body" ]; then
    code=$(curl -s -o /tmp/smoke.out -w '%{http_code}' -b "$JAR" -c "$JAR" -X "$m" -H 'Content-Type: application/json' -d "$body" "$BASE$p")
  else
    code=$(curl -s -o /tmp/smoke.out -w '%{http_code}' -b "$JAR" -c "$JAR" -X "$m" "$BASE$p")
  fi
  if [[ "$code" =~ ^($want)$ ]]; then echo "PASS $name ($code)"; pass=$((pass+1)); else echo "FAIL $name (got $code, want $want): $(head -c 300 /tmp/smoke.out)"; failn=$((failn+1)); fi
}
check "web index"            200 GET  /
grep -qi '<div id="root"' /tmp/smoke.out && echo "PASS web html root" || { echo "FAIL web html root"; failn=$((failn+1)); }
check "platform health"      200 GET  /api/health
grep -q '"ready":true' /tmp/smoke.out || { echo "FAIL health not ready"; failn=$((failn+1)); }
check "internal api blocked" "403|404" POST /api/internal/flow/events '{}'
login=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -c "$JAR" -H 'Content-Type: application/json' -d "{\"username\":\"$USER_NAME\",\"password\":\"$PASS\"}" "$BASE/api/auth/login")
if [ "$login" = 200 ]; then echo "PASS login (200)"; pass=$((pass+1)); else
check "register"             200 POST /api/auth/register "{\"username\":\"$USER_NAME\",\"password\":\"$PASS\",\"displayName\":\"Smoke\",\"acceptedTerms\":true}"
fi
check "session"              200 GET  /api/auth/session
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/intelligence/workspaces")
[ "$code" = 401 ] && { echo "PASS unauth guard (401)"; pass=$((pass+1)); } || { echo "FAIL unauth guard ($code)"; failn=$((failn+1)); }
check "intel workspaces"     200 GET  /api/intelligence/workspaces
check "engine sources (platform→engine)" 200 GET /api/intelligence/sources
check "notifications"        200 GET  /api/intelligence/notifications
check "billing"              200 GET  /api/intelligence/billing
check "members"              200 GET  /api/intelligence/members
check "agent conversations"  200 GET  /api/agent/conversations
check "canvas conversation doc" 200 GET /api/agent/canvas-conversations/smoke-canvas
CID="smoke-canvas-$RANDOM"
check "canvas doc save"      200 PUT  /api/agent/canvas-conversations/$CID '{"baseVersion":0,"document":{"version":1,"activeId":"c1","conversations":[{"id":"c1","title":"Smoke canvas chat","messages":[{"id":"m1","role":"user","text":"hello"}]}]}}'
check "unified directory"    200 GET  /api/agent/conversations
grep -q "\"subjectId\":\"$CID\"" /tmp/smoke.out && { echo "PASS canvas conversation listed in directory"; pass=$((pass+1)); } || { echo "FAIL canvas conversation not in directory"; failn=$((failn+1)); }
echo "smoke: $pass passed, $failn failed"
[ "$failn" -eq 0 ]
