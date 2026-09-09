#!/usr/bin/env bash
# Smoke test of a running partner-pos together with the platform behind it.
# It walks the whole demonstration: the till publishes its menu, pushes its
# quantities, a customer places an order and the till answers it.
#
#   BASE_URL=http://localhost:8085 ./scripts/smoke-pos.sh
#
# It publishes a menu, changes stock and places real orders, so run it against
# a local stack, never against real data.
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8085}"
OPS_URL="${OPS_URL:-http://localhost:9095}"
CATALOG_URL="${CATALOG_URL:-http://localhost:8082}"
INVENTORY_URL="${INVENTORY_URL:-http://localhost:8083}"
ORDER_URL="${ORDER_URL:-http://localhost:8084}"
MERCHANT_ID="${MERCHANT_ID:-11111111-1111-4111-8111-111111111111}"
CUSTOMER_ID="${CUSTOMER_ID:-$(python3 -c 'import uuid;print(uuid.uuid4())')}"

failures=0

call() {
  local name="$1" expected="$2"; shift 2
  local code
  code="$(curl -s -o /tmp/smoke_pos_body -w '%{http_code}' "$@")"
  if [ "$code" = "$expected" ]; then
    printf 'OK   %-46s %s\n' "$name" "$code"
  else
    printf 'FAIL %-46s got %s want %s\n' "$name" "$code" "$expected"
    head -c 300 /tmp/smoke_pos_body; echo
    failures=$((failures + 1))
  fi
}

json() { python3 -c "import json,sys;print(json.load(open('/tmp/smoke_pos_body'))$1)"; }

check() {
  if [ "$2" = "$3" ]; then
    printf 'OK   %-46s %s\n' "$1" "$2"
  else
    printf 'FAIL %-46s got %s want %s\n' "$1" "$2" "$3"
    failures=$((failures + 1))
  fi
}

# wait_status <order_id> <want> - the till answers the queue on its own timer,
# so the check waits for the end state instead of assuming it.
wait_status() {
  local id="$1" want="$2" got=""
  # Ладдер приготовления занимает три шага KITCHEN_STEP (по умолчанию 20 с)
  # плюс задержку опроса очереди, поэтому ждём с запасом.
  for _ in $(seq 1 150); do
    got="$(curl -s -H "X-Customer-Id: $CUSTOMER_ID" "$ORDER_URL/api/v1/orders/$id" \
      | python3 -c "import json,sys;print(json.load(sys.stdin)['status'])")"
    if [ "$got" = "$want" ]; then
      printf 'OK   %-46s %s\n' "order reached $want" "$got"
      return 0
    fi
    sleep 1
  done
  printf 'FAIL %-46s got %s want %s\n' "order never reached $want" "$got" "$want"
  failures=$((failures + 1))
}

call "liveness"                     200 "$OPS_URL/healthz"
call "readiness"                    200 "$OPS_URL/readyz"

call "till menu"                    200 "$BASE_URL/pos/menu"
check "three seeded positions"      "$(json "['items'].__len__()")" "3"
check "currency"                    "$(json "['currency']")" "RUB"

call "publish the menu"             200 -X POST "$BASE_URL/pos/publish"
check "every position matched"      "$(json "['matched']")" "3"
items="$(json "['items']")"
check "all positions published"     "$items" "3"

call "till menu after publication"  200 "$BASE_URL/pos/menu"
check "menu is published"           "$(json "['published']")" "True"
check "platform id is known"        "$(json "['items'][0]['menu_item_id'] != ''")" "True"
item_id="$(json "['items'][0]['menu_item_id']")"

# The platform must now agree with the till.
call "catalogue shows the menu"     200 "$CATALOG_URL/api/v1/merchants/$MERCHANT_ID/menu"
call "inventory shows the units"    200 \
     "$INVENTORY_URL/api/v1/merchants/$MERCHANT_ID/availability?menu_item_ids=$item_id"
check "units pushed by the till"    "$(json "['items'][0]['available_qty']")" "10"

call "change a price in the till"   200 -X PATCH -H 'Content-Type: application/json' \
     -d '{"price_minor":43000,"on_hand_qty":12}' "$BASE_URL/pos/menu/POS-001"
check "price changed"               "$(json "['price_minor']")" "43000"
call "unknown position"             404 -X PATCH -H 'Content-Type: application/json' \
     -d '{"price_minor":1000}' "$BASE_URL/pos/menu/NOPE"
call "empty patch"                  422 -X PATCH -H 'Content-Type: application/json' \
     -d '{}' "$BASE_URL/pos/menu/POS-001"

call "republish"                    200 -X POST "$BASE_URL/pos/publish"
call "inventory shows the new units" 200 \
     "$INVENTORY_URL/api/v1/merchants/$MERCHANT_ID/availability?menu_item_ids=$item_id"
check "units follow the till"       "$(json "['items'][0]['available_qty']")" "12"

call "behaviour: automatic refusal" 200 -X PUT -H 'Content-Type: application/json' \
     -d '{"mode":"AUTO_REJECT","eta_minutes":20,"reject_reason":"закончились продукты","kitchen":false}' \
     "$BASE_URL/pos/behavior"
check "mode switched"               "$(json "['mode']")" "AUTO_REJECT"
call "refusal without a reason"     422 -X PUT -H 'Content-Type: application/json' \
     -d '{"mode":"AUTO_REJECT","eta_minutes":20}' "$BASE_URL/pos/behavior"
call "unknown mode"                 422 -X PUT -H 'Content-Type: application/json' \
     -d '{"mode":"NOPE"}' "$BASE_URL/pos/behavior"

# Две штуки, а не одна: минимальная сумма демонстрационного заведения —
# 50 000 копеек, а позиция стоит 43 000.
basket() { printf '{"merchant_id":"%s","items":[{"menu_item_id":"%s","qty":2}],
  "customer_name":"Иван","customer_phone":"+79990000001",
  "delivery_address":"Невский проспект, 1"}' "$MERCHANT_ID" "$item_id"; }

call "customer places an order"     201 -X POST -H 'Content-Type: application/json' \
     -H "X-Customer-Id: $CUSTOMER_ID" -H "Idempotency-Key: pos-reject-$CUSTOMER_ID" \
     -d "$(basket)" "$ORDER_URL/api/v1/orders"
rejected_id="$(json "['order_id']")"
wait_status "$rejected_id" "REJECTED"

call "the till sees its queue"      200 "$BASE_URL/pos/orders"

call "behaviour: automatic kitchen" 200 -X PUT -H 'Content-Type: application/json' \
     -d '{"mode":"AUTO_ACCEPT","eta_minutes":25,"reject_reason":"нет","kitchen":true}' \
     "$BASE_URL/pos/behavior"
call "customer places another order" 201 -X POST -H 'Content-Type: application/json' \
     -H "X-Customer-Id: $CUSTOMER_ID" -H "Idempotency-Key: pos-accept-$CUSTOMER_ID" \
     -d "$(basket)" "$ORDER_URL/api/v1/orders"
accepted_id="$(json "['order_id']")"
wait_status "$accepted_id" "CONFIRMED"
# With the kitchen on the till walks the order all the way to the customer.
wait_status "$accepted_id" "DELIVERED"

call "unknown endpoint"             404 "$BASE_URL/pos/nothing"

# Возвращаем кассу в исходный режим: иначе она продолжит принимать заказы,
# созданные другими проверками, и следующий прогон smoke-order станет
# недетерминированным.
call "restore the manual mode"      200 -X PUT -H 'Content-Type: application/json' \
     -d '{"mode":"MANUAL","eta_minutes":25,"reject_reason":"закончились продукты","kitchen":true}' \
     "$BASE_URL/pos/behavior"
check "mode restored"               "$(json "['mode']")" "MANUAL"

if [ "$failures" -ne 0 ]; then
  echo "smoke failed: $failures check(s)"
  exit 1
fi
echo "smoke passed"
