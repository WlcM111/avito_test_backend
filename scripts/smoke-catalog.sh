#!/usr/bin/env bash
# Smoke test of a running catalog-service. It walks the whole external menu
# contract and prints the status code of each call.
#
#   BASE_URL=http://localhost:8082 ./scripts/smoke-catalog.sh
#
# The script publishes a menu, so run it against a local stack, never against
# real data.
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8082}"
OPS_URL="${OPS_URL:-http://localhost:9092}"
API_KEY="${API_KEY:-dev-merchant-key-kafe-primer-0001}"
MERCHANT_ID="${MERCHANT_ID:-11111111-1111-4111-8111-111111111111}"
ITEM_SELLABLE="${ITEM_SELLABLE:-a1111111-1111-4111-8111-111111111111}"
ITEM_EMPTY="${ITEM_EMPTY:-a1111111-1111-4111-8111-222222222222}"

failures=0

call() {
  local name="$1" expected="$2"; shift 2
  local code
  code="$(curl -s -o /tmp/smoke_cat_body -w '%{http_code}' "$@")"
  if [ "$code" = "$expected" ]; then
    printf 'OK   %-46s %s\n' "$name" "$code"
  else
    printf 'FAIL %-46s got %s want %s\n' "$name" "$code" "$expected"
    head -c 300 /tmp/smoke_cat_body; echo
    failures=$((failures + 1))
  fi
}

json() { # field of the last response body
  python3 -c "import json,sys;print(json.load(open('/tmp/smoke_cat_body'))$1)"
}

check() { # check <name> <actual> <expected>
  if [ "$2" = "$3" ]; then
    printf 'OK   %-46s %s\n' "$1" "$2"
  else
    printf 'FAIL %-46s got %s want %s\n' "$1" "$2" "$3"
    failures=$((failures + 1))
  fi
}

call "liveness"                     200 "$OPS_URL/healthz"
call "readiness"                    200 "$OPS_URL/readyz"

call "public menu"                  200 "$BASE_URL/api/v1/merchants/$MERCHANT_ID/menu"
# Версия меню не проверяется на «1»: касса заведения публикует своё меню при
# старте стенда, и это штатное поведение. Значимо, что меню опубликовано.
check "menu is published"           "$(json "['menu_version'] > 0")" "True"
check "seeded categories"           "$(json "['categories'].__len__()")" "2"
# The catalogue owns no availability: a menu item carries a price, not a count.
if json "['categories'][0]['items'][0]['available_qty']" >/dev/null 2>&1; then
  echo "FAIL the catalogue must not report quantities"; failures=$((failures + 1))
else
  printf 'OK   %-46s %s\n' "menu carries no quantity" "checked"
fi

call "public menu of unknown merchant" 404 \
     "$BASE_URL/api/v1/merchants/00000000-0000-4000-8000-000000000000/menu"
call "public menu with a bad id"    422 "$BASE_URL/api/v1/merchants/not-a-uuid/menu"

call "own menu without a key"       401 "$BASE_URL/api/v1/merchant/menu"
call "own menu"                     200 -H "X-Api-Key: $API_KEY" "$BASE_URL/api/v1/merchant/menu"
version="$(json "['menu_version']")"

snapshot='{"categories":[{"name":"Горячее","position":0,"items":[
  {"external_id":"POS-001","name":"Борщ с говядиной","description":"Со сметаной.","price_minor":43000,"position":0},
  {"external_id":"POS-002","name":"Котлета по-киевски","description":"С пюре.","price_minor":56000,"position":1}]},
 {"name":"Напитки","position":1,"items":[
  {"external_id":"POS-003","name":"Морс клюквенный","description":"400 мл.","price_minor":15000,"position":0}]}]}'

call "publish with a stale If-Match" 412 -X PUT -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -H 'If-Match: "999"' -d "$snapshot" \
     "$BASE_URL/api/v1/merchant/menu"

call "publish the menu"             200 -X PUT -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -H "If-Match: \"$version\"" -d "$snapshot" \
     "$BASE_URL/api/v1/merchant/menu"
check "version incremented"         "$(json "['menu_version']")" "$((version + 1))"
# Republishing an existing item must keep its platform identifier: stock cards
# and orders point at it.
check "item identity is stable"     "$(json "['categories'][0]['items'][0]['menu_item_id']")" "$ITEM_SELLABLE"
version="$(json "['menu_version']")"

withdraw='{"categories":[{"name":"Горячее","position":0,"items":[
  {"external_id":"POS-001","name":"Борщ с говядиной","price_minor":43000,"position":0}]}]}'
call "publish without two items"    200 -X PUT -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -H "If-Match: \"$version\"" -d "$withdraw" \
     "$BASE_URL/api/v1/merchant/menu"
check "two items withdrawn"         "$(json "['withdrawn']")" "2"
version="$(json "['menu_version']")"

# A withdrawn item is not deleted: it is simply no longer part of the menu.
call "withdrawn item is not shown"  200 "$BASE_URL/api/v1/merchants/$MERCHANT_ID/menu"
check "one category remains"        "$(json "['categories'].__len__()")" "1"

call "patch an unknown item"        404 -X PATCH -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{"price_minor":1000}' \
     "$BASE_URL/api/v1/merchant/menu/items/00000000-0000-4000-8000-000000000000"
call "patch a withdrawn item"       404 -X PATCH -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{"price_minor":1000}' \
     "$BASE_URL/api/v1/merchant/menu/items/$ITEM_EMPTY"
call "patch without changes"        422 -X PATCH -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{}' \
     "$BASE_URL/api/v1/merchant/menu/items/$ITEM_SELLABLE"
call "patch the price"              200 -X PATCH -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -H "If-Match: \"$version\"" \
     -d '{"price_minor":44500}' \
     "$BASE_URL/api/v1/merchant/menu/items/$ITEM_SELLABLE"
check "price changed"               "$(json "['price_minor']")" "44500"
check "patch bumps the version"     "$(json "['menu_version']")" "$((version + 1))"
version="$(json "['menu_version']")"

call "publish a zero price"         422 -X PUT -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' \
     -d '{"categories":[{"name":"Горячее","items":[{"external_id":"POS-001","name":"Борщ","price_minor":0}]}]}' \
     "$BASE_URL/api/v1/merchant/menu"
call "publish a duplicate external id" 422 -X PUT -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' \
     -d '{"categories":[{"name":"Горячее","items":[
          {"external_id":"POS-001","name":"Борщ","price_minor":1000},
          {"external_id":"POS-001","name":"Борщ 2","price_minor":2000}]}]}' \
     "$BASE_URL/api/v1/merchant/menu"
call "publish an empty category"    422 -X PUT -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' \
     -d '{"categories":[{"name":"Пусто","items":[]}]}' \
     "$BASE_URL/api/v1/merchant/menu"
call "publish with a wrong media type" 415 -X PUT -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: text/plain' -d "$snapshot" \
     "$BASE_URL/api/v1/merchant/menu"
call "publish with an unknown field" 400 -X PUT -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{"menu":[]}' \
     "$BASE_URL/api/v1/merchant/menu"
call "unknown key"                  401 -H "X-Api-Key: nope" "$BASE_URL/api/v1/merchant/menu"
call "unknown endpoint"             404 "$BASE_URL/api/v1/nothing"

# Restore the demonstration menu so the stack stays usable after the smoke run.
call "restore the demonstration menu" 200 -X PUT -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -H "If-Match: \"$version\"" -d "$snapshot" \
     "$BASE_URL/api/v1/merchant/menu"

if [ "$failures" -ne 0 ]; then
  echo "smoke failed: $failures check(s)"
  exit 1
fi
echo "smoke passed"
