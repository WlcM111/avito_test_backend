#!/usr/bin/env bash
# Smoke test of a running merchant-service. It exercises every endpoint of the
# external contract and prints the status code of each call.
#
#   BASE_URL=http://localhost:8081 ./scripts/smoke.sh
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8081}"
OPS_URL="${OPS_URL:-http://localhost:9091}"
API_KEY="${API_KEY:-dev-merchant-key-kafe-primer-0001}"
MERCHANT_ID="${MERCHANT_ID:-11111111-1111-4111-8111-111111111111}"
SUSPENDED_ID="${SUSPENDED_ID:-44444444-4444-4444-8444-444444444444}"

failures=0

call() {
  local name="$1" expected="$2"; shift 2
  local code
  code="$(curl -s -o /tmp/smoke_body -w '%{http_code}' "$@")"
  if [ "$code" = "$expected" ]; then
    printf 'OK   %-42s %s\n' "$name" "$code"
  else
    printf 'FAIL %-42s got %s want %s\n' "$name" "$code" "$expected"
    head -c 300 /tmp/smoke_body; echo
    failures=$((failures + 1))
  fi
}

call "liveness"                 200 "$OPS_URL/healthz"
call "readiness"                200 "$OPS_URL/readyz"
call "list merchants"           200 "$BASE_URL/api/v1/merchants?limit=10"
call "list filtered by city"    200 "$BASE_URL/api/v1/merchants?city=Москва"
call "list bad limit"           422 "$BASE_URL/api/v1/merchants?limit=999"
call "get merchant"             200 "$BASE_URL/api/v1/merchants/$MERCHANT_ID"
call "get suspended merchant"   404 "$BASE_URL/api/v1/merchants/$SUSPENDED_ID"
call "get merchant bad uuid"    422 "$BASE_URL/api/v1/merchants/not-a-uuid"
call "profile without key"      401 "$BASE_URL/api/v1/merchant/profile"
call "profile with key"         200 -H "X-Api-Key: $API_KEY" "$BASE_URL/api/v1/merchant/profile"
call "patch profile"            200 -X PATCH -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{"avg_prep_minutes":40}' \
     "$BASE_URL/api/v1/merchant/profile"
call "patch with stale If-Match" 412 -X PATCH -H "X-Api-Key: $API_KEY" \
     -H 'If-Match: "1"' -H 'Content-Type: application/json' \
     -d '{"avg_prep_minutes":41}' "$BASE_URL/api/v1/merchant/profile"
call "patch empty body"         422 -X PATCH -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{}' "$BASE_URL/api/v1/merchant/profile"
call "unknown endpoint"         404 "$BASE_URL/api/v1/nothing"

if [ "$failures" -gt 0 ]; then
  echo "smoke test failed: $failures check(s)"
  exit 1
fi
echo "smoke test passed"
