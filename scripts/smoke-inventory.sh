#!/usr/bin/env bash
# Smoke test of a running inventory-service. It walks the whole per-unit
# accounting contract and prints the status code of each call.
#
#   BASE_URL=http://localhost:8083 ./scripts/smoke-inventory.sh
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8083}"
OPS_URL="${OPS_URL:-http://localhost:9093}"
API_KEY="${API_KEY:-dev-merchant-key-kafe-primer-0001}"
MERCHANT_ID="${MERCHANT_ID:-11111111-1111-4111-8111-111111111111}"
ITEM_SELLABLE="${ITEM_SELLABLE:-a1111111-1111-4111-8111-111111111111}"
ITEM_EMPTY="${ITEM_EMPTY:-a1111111-1111-4111-8111-222222222222}"
ITEM_STOPPED="${ITEM_STOPPED:-a1111111-1111-4111-8111-333333333333}"

failures=0

call() {
  local name="$1" expected="$2"; shift 2
  local code
  code="$(curl -s -o /tmp/smoke_inv_body -w '%{http_code}' "$@")"
  if [ "$code" = "$expected" ]; then
    printf 'OK   %-46s %s\n' "$name" "$code"
  else
    printf 'FAIL %-46s got %s want %s\n' "$name" "$code" "$expected"
    head -c 300 /tmp/smoke_inv_body; echo
    failures=$((failures + 1))
  fi
}

json() { # field of the last response body
  python3 -c "import json,sys;print(json.load(open('/tmp/smoke_inv_body'))$1)"
}

call "liveness"                    200 "$OPS_URL/healthz"
call "readiness"                   200 "$OPS_URL/readyz"

call "public availability"         200 \
     "$BASE_URL/api/v1/merchants/$MERCHANT_ID/availability?menu_item_ids=$ITEM_SELLABLE,$ITEM_EMPTY,$ITEM_STOPPED"
# An ENABLED card holding zero units must not be sellable.
if [ "$(json "['items'][1]['sellable']")" != "False" ]; then
  echo "FAIL enabled-but-empty card reported as sellable"; failures=$((failures + 1))
else
  printf 'OK   %-46s %s\n' "enabled but empty is not sellable" "checked"
fi

call "availability without ids"    422 "$BASE_URL/api/v1/merchants/$MERCHANT_ID/availability"
call "stock list without key"      401 "$BASE_URL/api/v1/merchant/stock"
call "stock list"                  200 -H "X-Api-Key: $API_KEY" "$BASE_URL/api/v1/merchant/stock"
call "read one card"               200 -H "X-Api-Key: $API_KEY" "$BASE_URL/api/v1/merchant/stock/$ITEM_SELLABLE"

call "set units on hand"           200 -X PUT -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{"on_hand_qty":12,"reason":"smoke"}' \
     "$BASE_URL/api/v1/merchant/stock/$ITEM_SELLABLE"
call "set with stale If-Match"      412 -X PUT -H "X-Api-Key: $API_KEY" -H 'If-Match: "1"' \
     -H 'Content-Type: application/json' -d '{"on_hand_qty":13}' \
     "$BASE_URL/api/v1/merchant/stock/$ITEM_SELLABLE"
call "set without quantity"        422 -X PUT -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{}' \
     "$BASE_URL/api/v1/merchant/stock/$ITEM_SELLABLE"

call "adjust units"                200 -X POST -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{"delta":3,"operation_key":"smoke-1","reason":"delivery"}' \
     "$BASE_URL/api/v1/merchant/stock/$ITEM_SELLABLE/adjust"
call "repeat the same operation"   200 -X POST -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{"delta":3,"operation_key":"smoke-1","reason":"delivery"}' \
     "$BASE_URL/api/v1/merchant/stock/$ITEM_SELLABLE/adjust"
if [ "$(json "['duplicate']")" != "True" ] || [ "$(json "['on_hand_qty']")" != "15" ]; then
  echo "FAIL the repeated adjustment was applied twice"; failures=$((failures + 1))
else
  printf 'OK   %-46s %s\n' "adjustment applied exactly once" "15 units"
fi
call "write off more than present" 409 -X POST -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{"delta":-999,"operation_key":"smoke-2"}' \
     "$BASE_URL/api/v1/merchant/stock/$ITEM_SELLABLE/adjust"

call "stop sales"                  200 -X PATCH -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{"sales_state":"STOPPED"}' \
     "$BASE_URL/api/v1/merchant/stock/$ITEM_SELLABLE"
if [ "$(json "['on_hand_qty']")" != "15" ]; then
  echo "FAIL the stop-list switch changed a quantity"; failures=$((failures + 1))
else
  printf 'OK   %-46s %s\n' "stop list did not change quantities" "15 units"
fi
call "enable sales"                200 -X PATCH -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{"sales_state":"ENABLED"}' \
     "$BASE_URL/api/v1/merchant/stock/$ITEM_SELLABLE"
call "unknown sales state"         422 -X PATCH -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{"sales_state":"PAUSED"}' \
     "$BASE_URL/api/v1/merchant/stock/$ITEM_SELLABLE"

call "movements journal"           200 -H "X-Api-Key: $API_KEY" \
     "$BASE_URL/api/v1/merchant/stock/$ITEM_SELLABLE/movements"
call "unknown card"                404 -H "X-Api-Key: $API_KEY" \
     "$BASE_URL/api/v1/merchant/stock/00000000-0000-4000-8000-000000000000/movements"
call "unknown endpoint"            404 "$BASE_URL/api/v1/nothing"

if [ "$failures" -gt 0 ]; then
  echo "smoke test failed: $failures check(s)"
  exit 1
fi
echo "smoke test passed"
