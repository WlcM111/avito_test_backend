#!/usr/bin/env bash
# Smoke test of a running order-service. It drives the whole order lifecycle
# against the live stack and prints the status code of each call.
#
#   BASE_URL=http://localhost:8084 ./scripts/smoke-order.sh
#
# It places and cancels real orders and changes real stock, so run it against a
# local stack, never against real data.
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8084}"
OPS_URL="${OPS_URL:-http://localhost:9094}"
INVENTORY_URL="${INVENTORY_URL:-http://localhost:8083}"
API_KEY="${API_KEY:-dev-merchant-key-kafe-primer-0001}"
MERCHANT_ID="${MERCHANT_ID:-11111111-1111-4111-8111-111111111111}"
ITEM_SELLABLE="${ITEM_SELLABLE:-a1111111-1111-4111-8111-111111111111}"
ITEM_EMPTY="${ITEM_EMPTY:-a1111111-1111-4111-8111-222222222222}"
ITEM_STOPPED="${ITEM_STOPPED:-a1111111-1111-4111-8111-333333333333}"
CUSTOMER_ID="${CUSTOMER_ID:-$(python3 -c 'import uuid;print(uuid.uuid4())')}"

failures=0

call() {
  local name="$1" expected="$2"; shift 2
  local code
  code="$(curl -s -o /tmp/smoke_order_body -w '%{http_code}' "$@")"
  if [ "$code" = "$expected" ]; then
    printf 'OK   %-48s %s\n' "$name" "$code"
  else
    printf 'FAIL %-48s got %s want %s\n' "$name" "$code" "$expected"
    head -c 300 /tmp/smoke_order_body; echo
    failures=$((failures + 1))
  fi
}

json() { python3 -c "import json,sys;print(json.load(open('/tmp/smoke_order_body'))$1)"; }

check() {
  if [ "$2" = "$3" ]; then
    printf 'OK   %-48s %s\n' "$1" "$2"
  else
    printf 'FAIL %-48s got %s want %s\n' "$1" "$2" "$3"
    failures=$((failures + 1))
  fi
}

# available <menu_item_id> - units inventory-service can still hold.
available() {
  curl -s "$INVENTORY_URL/api/v1/merchants/$MERCHANT_ID/availability?menu_item_ids=$1" \
    | python3 -c "import json,sys;d=json.load(sys.stdin);print(d['items'][0]['available_qty'])"
}

# wait_available <menu_item_id> <want> - the terminal decision travels through
# the outbox, so the check waits for the end state instead of assuming it.
wait_available() {
  local item="$1" want="$2" got=""
  for _ in $(seq 1 60); do
    got="$(available "$item")"
    if [ "$got" = "$want" ]; then
      printf 'OK   %-48s %s\n' "units settled at $want" "$got"
      return 0
    fi
    sleep 0.5
  done
  printf 'FAIL %-48s got %s want %s\n' "units never settled" "$got" "$want"
  failures=$((failures + 1))
}

call "liveness"                       200 "$OPS_URL/healthz"
call "readiness"                      200 "$OPS_URL/readyz"

# A known starting point, so the run does not depend on earlier runs.
call "reset the stock to 10"          200 -X PUT -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{"on_hand_qty":10,"reason":"smoke"}' \
     "$INVENTORY_URL/api/v1/merchant/stock/$ITEM_SELLABLE"

basket() { printf '{"merchant_id":"%s","items":[{"menu_item_id":"%s","qty":%s}],
  "customer_name":"Иван","customer_phone":"+79990000001",
  "delivery_address":"Невский проспект, 1"}' "$MERCHANT_ID" "$1" "$2"; }

# Тело предпросмотра отличается от тела оформления: PreviewRequest состоит
# только из merchant_id и items, а order-service отвергает неизвестные поля.
preview_body() { printf '{"merchant_id":"%s","items":[{"menu_item_id":"%s","qty":%s}]}' \
  "$MERCHANT_ID" "$1" "$2"; }

# Минимальная сумма демонстрационного заведения — 50 000 копеек при цене
# позиции 42 000–43 000, поэтому корзина «на один заказ» берёт две штуки.
MIN_ORDER_QTY=2

call "preview"                        200 -H 'Content-Type: application/json' \
     -d "$(preview_body "$ITEM_SELLABLE" 3)" "$BASE_URL/api/v1/orders/preview"
check "preview is orderable"          "$(json "['orderable']")" "True"
total="$(json "['total_minor']")"

call "preview of a stop-listed item"  200 -H 'Content-Type: application/json' \
     -d "$(preview_body "$ITEM_STOPPED" 1)" "$BASE_URL/api/v1/orders/preview"
check "stop list is reported"         "$(json "['items'][0]['problem']")" "ITEM_SALES_STOPPED"
check "not orderable"                 "$(json "['orderable']")" "False"

call "preview of an unknown merchant"  404 -H 'Content-Type: application/json' \
     -d "$(printf '{"merchant_id":"00000000-0000-4000-8000-000000000000","items":[{"menu_item_id":"%s","qty":1}]}' "$ITEM_SELLABLE")" \
     "$BASE_URL/api/v1/orders/preview"
call "preview with an empty basket"   422 -H 'Content-Type: application/json' \
     -d "$(printf '{"merchant_id":"%s","items":[]}' "$MERCHANT_ID")" \
     "$BASE_URL/api/v1/orders/preview"

call "create without a customer"      422 -X POST -H 'Content-Type: application/json' \
     -H 'Idempotency-Key: smoke-1' -d "$(basket "$ITEM_SELLABLE" 3)" "$BASE_URL/api/v1/orders"
call "create without an idempotency key" 422 -X POST -H 'Content-Type: application/json' \
     -H "X-Customer-Id: $CUSTOMER_ID" -d "$(basket "$ITEM_SELLABLE" 3)" "$BASE_URL/api/v1/orders"

before="$(available "$ITEM_SELLABLE")"
call "create the order"               201 -X POST -H 'Content-Type: application/json' \
     -H "X-Customer-Id: $CUSTOMER_ID" -H "Idempotency-Key: smoke-$CUSTOMER_ID" \
     -d "$(basket "$ITEM_SELLABLE" 3)" "$BASE_URL/api/v1/orders"
order_id="$(json "['order_id']")"
check "status is CREATED"             "$(json "['status']")" "CREATED"
check "total matches the preview"     "$(json "['total_minor']")" "$total"
check "the price snapshot is stored"  "$(json "['items'][0]['name'] != ''")" "True"
check "units are held"                "$(available "$ITEM_SELLABLE")" "$((before - 3))"

call "repeat the same key"            201 -X POST -H 'Content-Type: application/json' \
     -H "X-Customer-Id: $CUSTOMER_ID" -H "Idempotency-Key: smoke-$CUSTOMER_ID" \
     -d "$(basket "$ITEM_SELLABLE" 3)" "$BASE_URL/api/v1/orders"
check "the repeat returns the same order" "$(json "['order_id']")" "$order_id"
check "the repeat holds nothing more" "$(available "$ITEM_SELLABLE")" "$((before - 3))"

call "read the order"                 200 -H "X-Customer-Id: $CUSTOMER_ID" \
     "$BASE_URL/api/v1/orders/$order_id"
call "another customer cannot read it" 404 \
     -H "X-Customer-Id: 00000000-0000-4000-8000-000000000000" \
     "$BASE_URL/api/v1/orders/$order_id"
call "order history"                  200 -H "X-Customer-Id: $CUSTOMER_ID" \
     "$BASE_URL/api/v1/orders?limit=10"

call "merchant queue without a key"   401 "$BASE_URL/api/v1/merchant/orders"
call "merchant queue"                 200 -H "X-Api-Key: $API_KEY" \
     "$BASE_URL/api/v1/merchant/orders?status=CREATED&limit=50"

call "accept the order"               200 -X POST -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{"eta_minutes":25}' \
     "$BASE_URL/api/v1/merchant/orders/$order_id/accept"
check "status is CONFIRMED"           "$(json "['status']")" "CONFIRMED"
check "the estimate is kept"          "$(json "['eta_minutes']")" "25"
call "accepting again is idempotent"  200 -X POST -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{"eta_minutes":25}' \
     "$BASE_URL/api/v1/merchant/orders/$order_id/accept"
# Confirming consumes the units: availability stays down and the hold is gone.
wait_available "$ITEM_SELLABLE" "$((before - 3))"
call "the units were consumed"        200 -H "X-Api-Key: $API_KEY" \
     "$INVENTORY_URL/api/v1/merchant/stock/$ITEM_SELLABLE"
check "on hand decreased"             "$(json "['on_hand_qty']")" "$((before - 3))"
check "nothing stays reserved"        "$(json "['reserved_qty']")" "0"

call "refusing a confirmed order"     409 -X POST -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{"reason":"поздно"}' \
     "$BASE_URL/api/v1/merchant/orders/$order_id/reject"
check "conflict code"                 "$(json "['error']['code']")" "ORDER_STATE_CONFLICT"

call "skipping a step"                409 -X POST -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{"status":"DELIVERED"}' \
     "$BASE_URL/api/v1/merchant/orders/$order_id/status"
for status in COOKING DELIVERING DELIVERED; do
  call "move to $status"              200 -X POST -H "X-Api-Key: $API_KEY" \
       -H 'Content-Type: application/json' -d "{\"status\":\"$status\"}" \
       "$BASE_URL/api/v1/merchant/orders/$order_id/status"
done
check "status is DELIVERED"           "$(json "['status']")" "DELIVERED"
call "an unknown status"              422 -X POST -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{"status":"SHIPPED"}' \
     "$BASE_URL/api/v1/merchant/orders/$order_id/status"

# The cancellation path returns the units.
before="$(available "$ITEM_SELLABLE")"
call "create an order to cancel"      201 -X POST -H 'Content-Type: application/json' \
     -H "X-Customer-Id: $CUSTOMER_ID" -H "Idempotency-Key: smoke-cancel-$CUSTOMER_ID" \
     -d "$(basket "$ITEM_SELLABLE" 2)" "$BASE_URL/api/v1/orders"
cancel_id="$(json "['order_id']")"
check "units are held again"          "$(available "$ITEM_SELLABLE")" "$((before - 2))"
call "cancel it"                      200 -X POST -H "X-Customer-Id: $CUSTOMER_ID" \
     "$BASE_URL/api/v1/orders/$cancel_id/cancel"
check "status is CANCELLED"           "$(json "['status']")" "CANCELLED"
wait_available "$ITEM_SELLABLE" "$before"
call "cancelling again is idempotent" 200 -X POST -H "X-Customer-Id: $CUSTOMER_ID" \
     "$BASE_URL/api/v1/orders/$cancel_id/cancel"

# The refusal path a merchant drives.
before="$(available "$ITEM_SELLABLE")"
call "create an order to refuse"      201 -X POST -H 'Content-Type: application/json' \
     -H "X-Customer-Id: $CUSTOMER_ID" -H "Idempotency-Key: smoke-reject-$CUSTOMER_ID" \
     -d "$(basket "$ITEM_SELLABLE" "$MIN_ORDER_QTY")" "$BASE_URL/api/v1/orders"
reject_id="$(json "['order_id']")"
call "refuse it"                      200 -X POST -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{"reason":"закончились продукты"}' \
     "$BASE_URL/api/v1/merchant/orders/$reject_id/reject"
check "status is REJECTED"            "$(json "['status']")" "REJECTED"
wait_available "$ITEM_SELLABLE" "$before"
call "refusing without a reason"      422 -X POST -H "X-Api-Key: $API_KEY" \
     -H 'Content-Type: application/json' -d '{"reason":"  "}' \
     "$BASE_URL/api/v1/merchant/orders/$reject_id/reject"

# Baskets the platform refuses.
call "order a stop-listed item"       409 -X POST -H 'Content-Type: application/json' \
     -H "X-Customer-Id: $CUSTOMER_ID" -H "Idempotency-Key: smoke-stop-$CUSTOMER_ID" \
     -d "$(basket "$ITEM_STOPPED" 1)" "$BASE_URL/api/v1/orders"
check "stop-list code"                "$(json "['error']['code']")" "ITEM_SALES_STOPPED"
call "order more than exists"         409 -X POST -H 'Content-Type: application/json' \
     -H "X-Customer-Id: $CUSTOMER_ID" -H "Idempotency-Key: smoke-empty-$CUSTOMER_ID" \
     -d "$(basket "$ITEM_EMPTY" 1)" "$BASE_URL/api/v1/orders"
check "shortage code"                 "$(json "['error']['code']")" "INSUFFICIENT_UNITS"

call "unknown order"                  404 -H "X-Customer-Id: $CUSTOMER_ID" \
     "$BASE_URL/api/v1/orders/00000000-0000-4000-8000-000000000000"
call "unknown endpoint"               404 "$BASE_URL/api/v1/nothing"
call "malformed body"                 400 -X POST -H 'Content-Type: application/json' \
     -H "X-Customer-Id: $CUSTOMER_ID" -H 'Idempotency-Key: smoke-bad' \
     -d '{"merchant_id":' "$BASE_URL/api/v1/orders"
call "wrong media type"               415 -X POST -H 'Content-Type: text/plain' \
     -H "X-Customer-Id: $CUSTOMER_ID" -H 'Idempotency-Key: smoke-bad-2' \
     -d "$(basket "$ITEM_SELLABLE" 1)" "$BASE_URL/api/v1/orders"

if [ "$failures" -ne 0 ]; then
  echo "smoke failed: $failures check(s)"
  exit 1
fi
echo "smoke passed"
