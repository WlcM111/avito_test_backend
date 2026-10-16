# Авито.Кухня — платформа заказа еды

Тестовое задание Backend. Микросервисная система на Go и PostgreSQL.
Действующее техническое задание — [`docs/TZ-v3.0.md`](docs/TZ-v3.0.md).

## Состояние

Система состоит из четырёх бизнес-микросервисов и сервиса-эмулятора заведения.
**Реализовано всё.**

| Сервис | Роль | Состояние |
|---|---|---|
| **merchant-service** | Заведения, ключи доступа, коммерческий профиль | **реализован** (итерация 1) |
| **inventory-service** | Поштучный учёт остатков, резервы, журнал движений | **реализован** (итерация 2) |
| **catalog-service** | Меню: структура, идентичность позиций, цены | **реализован** (итерация 3) |
| **order-service** | Заказы, жизненный цикл, идемпотентность, outbox | **реализован** (итерация 4) |
| **partner-pos** | Эмулятор кассы заведения (внешняя система) | **реализован** (итерация 5) |

`merchant-service` не зависит ни от одного другого сервиса. `inventory-service`
и `catalog-service` обращаются к нему по gRPC только для проверки ключа
заведения и между собой не связаны. `order-service` зависит от всех трёх и
никем не вызывается, поэтому gRPC-сервера у него нет. `partner-pos` не входит
в платформу: это эмулятор кассы одного заведения, у него нет базы и доступа во
внутреннюю сеть, он работает через те же публичные HTTP-API и тот же ключ
заведения, что и любая реальная интеграция.

Разделение ответственности между каталогом и остатками намеренное:
`catalog-service` отвечает за то, **что** продаётся и **почём**, и не владеет
количествами; сколько штук доступно, знает только `inventory-service`.
Публикация меню не создаёт и не подтверждает остаток. Клиент, которому нужно
показать заказуемость позиции, объединяет ответ
`GET /api/v1/merchants/{id}/menu` каталога с
`GET /api/v1/merchants/{id}/availability` остатков.

## Архитектура

Диаграмма C4 Level 2 — [`docs/c4/containers.puml`](docs/c4/containers.puml).
CJM покупателя и заведения — [`docs/cjm/`](docs/cjm/).

```
   customer ─► order-service ─┬─ gRPC GetMerchant / AuthenticateApiKey ─► merchant-service
   merchant ─►    │           ├─ gRPC QuoteItems ───────────────────────► catalog-service
                  │           └─ gRPC Reserve / Commit / Release ───────► inventory-service
                  │                                                            │
                  ▼                                                            ▼
        PostgreSQL (db orders)                                  PostgreSQL (db inventory)
        заказы, позиции, outbox

   Приём, отказ и отмена пишут статус и outbox-сообщение одной транзакцией;
   фоновый диспетчер order-service доставляет решение в inventory-service.
```

Границы сервисов проведены по владению данными и профилю нагрузки:
`merchant-service` отвечает за то, **кто** торгует; `catalog-service` — **что и почём**;
`inventory-service` — **сколько штук доступно**; `order-service` — **заказ и его жизненный цикл**.
Обоснование числа сервисов и отвергнутые варианты — §3 ТЗ.

Внутри сервиса — чистая слоистая архитектура: `domain → app → adapters`.
Направления зависимостей проверяются автоматически правилами `depguard`
в [`.golangci.yml`](.golangci.yml): домен не может импортировать транспорт или
драйвер БД, сценарии — адаптеры, один сервис — внутренности другого.

Межсервисное взаимодействие — только gRPC. Kafka не вводится: асинхронный обмен
в системе один (доставка терминального решения по заказу в `inventory-service`),
у него один потребитель, и надёжность обеспечивает transactional outbox.

## Быстрый старт

Нужны Docker с плагином Compose v2, `make`, `curl` и `python3`; полный список —
раздел «Требования окружения».

```bash
git clone https://github.com/WlcM111/avito_test_backend.git && cd avito_test_backend
cp .env.example .env   # необязательно: у всех переменных есть значения по умолчанию
make up                # docker compose up --build -d
make wait              # ждёт /readyz всех пяти сервисов
```

Без `make` — `docker compose up --build -d` и `docker compose ps -a`: стенд
готов, когда пять сервисов в состоянии `healthy`, а четыре job миграций
завершились с кодом 0.

Порядок готовности: PostgreSQL → job миграций (`merchant-migrate`,
`inventory-migrate`, `catalog-migrate`, `order-migrate`) → `merchant-service` →
`inventory-service` и `catalog-service` → `order-service` → `partner-pos`.
Публичные API: `http://localhost:8081` (заведения), `http://localhost:8082`
(меню), `http://localhost:8083` (поштучные остатки), `http://localhost:8084`
(заказы) и `http://localhost:8085` (управление кассой заведения). Служебные
`/healthz` и `/readyz` — на портах 9091 (merchant), 9092 (catalog),
9093 (inventory), 9094 (order) и 9095 (partner-pos).

Проверка всего решения одной командой — `make stack-check` (раздел «Проверка
решения»). Остановка с удалением данных — `make down` (`docker compose down -v`).

Касса при старте публикует своё меню и выставляет остатки. В очередь заказов
она по умолчанию не вмешивается: режим `MANUAL`. Чтобы увидеть автоматический
цикл «принято → готовится → в пути → доставлено», её нужно переключить явно:

```bash
curl -s -X PUT -H 'Content-Type: application/json' \
     -d '{"mode":"AUTO_ACCEPT","eta_minutes":25,"reject_reason":"нет","kitchen":true}' \
     localhost:8085/pos/behavior

# вернуть ручной режим: в автоматическом касса сама отвечает на все заказы
# заведения, в том числе на заказы примеров ниже и smoke-проверок
curl -s -X PUT -H 'Content-Type: application/json' \
     -d '{"mode":"MANUAL","eta_minutes":25,"reject_reason":"закончились продукты","kitchen":true}' \
     localhost:8085/pos/behavior
```

Примеры ниже выполняются по порядку на поднятом стенде. За командами, которые
закрывают заведение, ставят позицию в стоп-лист, публикуют урезанное меню или
переключают кассу в автоматический режим, следует команда, возвращающая
прежнее состояние: на нём держатся smoke-проверки.

Заведения:

```bash
curl -s localhost:8081/api/v1/merchants | head -c 400

curl -s -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     localhost:8081/api/v1/merchant/profile

# закрыть приём заказов и поднять минимальную сумму
curl -s -X PATCH -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     -H 'Content-Type: application/json' \
     -d '{"is_accepting_orders":false,"min_order_minor":70000}' \
     localhost:8081/api/v1/merchant/profile

# вернуть значения seed
curl -s -X PATCH -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     -H 'Content-Type: application/json' \
     -d '{"is_accepting_orders":true,"min_order_minor":50000}' \
     localhost:8081/api/v1/merchant/profile
```

Поштучный учёт:

```bash
# публичная доступность в штуках
curl -s 'localhost:8083/api/v1/merchants/11111111-1111-4111-8111-111111111111/availability?menu_item_ids=a1111111-1111-4111-8111-111111111111,a1111111-1111-4111-8111-222222222222'

# установить остаток
curl -s -X PUT -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     -H 'Content-Type: application/json' -d '{"on_hand_qty":12,"reason":"stocktake"}' \
     localhost:8083/api/v1/merchant/stock/a1111111-1111-4111-8111-111111111111

# относительная корректировка: повтор с тем же operation_key ничего не меняет
curl -s -X POST -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     -H 'Content-Type: application/json' -d '{"delta":3,"operation_key":"delivery-1"}' \
     localhost:8083/api/v1/merchant/stock/a1111111-1111-4111-8111-111111111111/adjust

# стоп-лист: не меняет количеств
curl -s -X PATCH -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     -H 'Content-Type: application/json' -d '{"sales_state":"STOPPED"}' \
     localhost:8083/api/v1/merchant/stock/a1111111-1111-4111-8111-111111111111

# снять позицию со стоп-листа
curl -s -X PATCH -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     -H 'Content-Type: application/json' -d '{"sales_state":"ENABLED"}' \
     localhost:8083/api/v1/merchant/stock/a1111111-1111-4111-8111-111111111111

# журнал движений
curl -s -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     localhost:8083/api/v1/merchant/stock/a1111111-1111-4111-8111-111111111111/movements
```

Меню:

```bash
# публичное меню заведения с версией
curl -s localhost:8082/api/v1/merchants/11111111-1111-4111-8111-111111111111/menu

# своё меню (в ответе ETag с menu_version)
curl -si -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     localhost:8082/api/v1/merchant/menu | head -20

# публикация снимка целиком: отсутствующие позиции снимаются с продажи,
# у оставшихся сохраняется menu_item_id. If-Match — текущая menu_version:
# касса уже опубликовала меню при старте, поэтому версия больше 1
VERSION=$(curl -s -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     localhost:8082/api/v1/merchant/menu |
     python3 -c 'import json,sys; print(json.load(sys.stdin)["menu_version"])')
curl -s -X PUT -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     -H 'Content-Type: application/json' -H "If-Match: \"$VERSION\"" \
     -d '{"categories":[{"name":"Горячее","position":0,"items":[
           {"external_id":"POS-001","name":"Борщ с говядиной","price_minor":43000,"position":0}]}]}' \
     localhost:8082/api/v1/merchant/menu

# изменить цену одной позиции: бампает menu_version
curl -s -X PATCH -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     -H 'Content-Type: application/json' -d '{"price_minor":44500}' \
     localhost:8082/api/v1/merchant/menu/items/a1111111-1111-4111-8111-111111111111

# вернуть меню и остатки кассы: она заново публикует все три позиции и
# выставляет их количества и стоп-лист
curl -s -X POST localhost:8085/pos/publish
```

Меню и остатки ведутся по одним и тем же идентификаторам позиций, поэтому
ответы двух сервисов соединяются по `menu_item_id` без дополнительной
трансляции.

Заказы — весь путь от корзины до доставки:

```bash
CUSTOMER=$(python3 -c 'import uuid; print(uuid.uuid4())')
BASKET='"merchant_id":"11111111-1111-4111-8111-111111111111",
        "items":[{"menu_item_id":"a1111111-1111-4111-8111-111111111111","qty":3}]'
DELIVERY='"customer_name":"Иван","customer_phone":"+79990000001",
          "delivery_address":"Невский проспект, 1"'

# пересчёт корзины: цены из каталога, штуки из остатков, условия из профиля;
# тело — только merchant_id и items, неизвестные поля отвергаются (400)
curl -s -H 'Content-Type: application/json' -d "{$BASKET}" \
     localhost:8084/api/v1/orders/preview

# оформление: удерживает штуки
curl -s -X POST -H 'Content-Type: application/json' \
     -H "X-Customer-Id: $CUSTOMER" -H "Idempotency-Key: demo-$CUSTOMER" \
     -d "{$BASKET,$DELIVERY}" localhost:8084/api/v1/orders

# повтор с тем же ключом возвращает тот же заказ (Idempotent-Replay: true) и
# ничего не удерживает повторно; из ответа берётся идентификатор заказа
ORDER=$(curl -s -X POST -H 'Content-Type: application/json' \
     -H "X-Customer-Id: $CUSTOMER" -H "Idempotency-Key: demo-$CUSTOMER" \
     -d "{$BASKET,$DELIVERY}" localhost:8084/api/v1/orders |
     python3 -c 'import json,sys; print(json.load(sys.stdin)["order_id"])')

# очередь заведения и приём заказа (штуки спишутся через outbox)
curl -s -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     'localhost:8084/api/v1/merchant/orders?status=CREATED'
curl -s -X POST -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     -H 'Content-Type: application/json' -d '{"eta_minutes":25}' \
     "localhost:8084/api/v1/merchant/orders/$ORDER/accept"

# заказ глазами покупателя
curl -s -H "X-Customer-Id: $CUSTOMER" "localhost:8084/api/v1/orders/$ORDER"
```

## Проверка решения

Всё решение проверяется одной командой против стенда из `docker-compose.yml`:

```bash
make stack-check   # make up wait smoke e2e
make down          # остановить стенд и удалить данные
```

`make stack-check` собирает образы и поднимает стенд, ждёт готовности всех пяти
сервисов, прогоняет smoke-сценарии и сквозные тесты; первая же неудача
завершает команду с ненулевым кодом. На поднятом стенде шаги запускаются и по
отдельности, сколько угодно раз:

| Команда | Что делает |
|---|---|
| `make up` | `docker compose up --build -d`: образы, PostgreSQL, миграции, пять сервисов |
| `make wait` | опрашивает `/readyz` на портах `OPS_PORTS` (по умолчанию `9091 9093 9092 9094 9095`), каждый не дольше `WAIT_TIMEOUT` секунд (180) |
| `make smoke` | пять smoke-скриптов подряд, таблица ниже |
| `make e2e` | `go test -count=1 -v ./test/e2e/...` с адресами стенда в `E2E_*_URL` |
| `make test-race` | все unit- и интеграционные тесты под race-детектором; интеграционные — против PostgreSQL стенда на порту 5499 |
| `make check` | `make fmt-check vet lint verify test-race` — локально то, что проверяют job `build-and-test` и `lint` в CI |

Smoke-скрипт печатает по строке на проверку — `OK` или `FAIL` с телом ответа —
и при любой неудаче завершается с кодом 1.

| Скрипт | Сервис | Проверок | Что покрывает |
|---|---|---|---|
| `scripts/smoke.sh` | merchant-service, `:8081` | 14 | список и фильтр по городу, карточка, отключённое заведение (404), профиль по ключу (401 без ключа), `PATCH` с устаревшим `If-Match` (412), ошибки валидации (422) |
| `scripts/smoke-inventory.sh` | inventory-service, `:8083` | 22 | 401, 404, 409, 412 и 422, повтор неидемпотентной операции |
| `scripts/smoke-catalog.sh` | catalog-service, `:8082` | 32 | конфликт версии, снятие позиций с продажи, стабильность идентификаторов |
| `scripts/smoke-order.sh` | order-service, `:8084` | 60 | весь жизненный цикл заказа, идемпотентность, конфликты статусов, возврат и списание штук |
| `scripts/smoke-pos.sh` | partner-pos, `:8085` | 36 | публикация меню кассой, синхронизация остатков, автоматический отказ и автоматический приём с доведением заказа до `DELIVERED`; начинает с демонстрационных значений `POS-001` в кассе и при любом выходе возвращает ей режим `MANUAL` |

Сквозные тесты (`test/e2e`) проходят путь покупателя и заведения через все
сервисы и проверяют состояние, в котором оказывается каждый: оформление с
удержанием штук и идемпотентный повтор, приём со списанием через outbox и
доведение до `DELIVERED`, отмену и отказ с возвратом штук, отказ корзины из-за
стоп-листа и нехватки штук, недоступность чужого заказа, обязательные
заголовки, конверт ошибки на неизвестном пути и автоматические ответы кассы.
Каждый тест сам приводит демонстрационное заведение в нужное состояние:
профиль, остатки, режим кассы `MANUAL`, отказ по заказам, оставшимся без
решения. Поэтому тесты повторяются на одном стенде и проходят после
smoke-скриптов и ручных экспериментов.

Без `make` сквозные тесты запускаются так:

```bash
E2E_MERCHANT_URL=http://localhost:8081 E2E_CATALOG_URL=http://localhost:8082 \
E2E_INVENTORY_URL=http://localhost:8083 E2E_ORDER_URL=http://localhost:8084 \
E2E_POS_URL=http://localhost:8085 \
go test ./test/e2e/... -count=1 -v
```

Без `E2E_POS_URL` пропускается только сценарий кассы; без любой из четырёх
остальных переменных пропускаются все сквозные тесты, поэтому `go test ./...`
работает и без стенда.

Если порты в `.env` изменены, их нужно передать проверкам:
`make wait OPS_PORTS="…"`, `make e2e E2E_ORDER_URL=http://localhost:18084`
(так же остальные `E2E_*_URL`), а smoke-скрипты запускать по одному с
`BASE_URL`, `OPS_URL` и адресами соседних сервисов (`INVENTORY_URL`,
`CATALOG_URL`, `ORDER_URL`; переменные перечислены в начале каждого скрипта).

CI (`.github/workflows/ci.yml`) выполняет три job: `build-and-test`
(`make deps verify fmt-check build vet test-race` против PostgreSQL 18),
`lint` (golangci-lint v2.14.0, который сначала проверяет `.golangci.yml` по
схеме) и `e2e` (`make up wait smoke e2e` на стенде Docker Compose; при неудаче
печатает логи стенда).

## Переменные окружения

### `.env` для Docker Compose

Файл необязателен: `docker-compose.yml` задаёт каждой переменной значение по
умолчанию, `.env.example` повторяет их.

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `LOG_LEVEL` | `info` | Уровень логов сервисов и job миграций |
| `POSTGRES_USER`, `POSTGRES_PASSWORD` | `postgres`, `postgres` | Суперпользователь PostgreSQL; с ним работают интеграционные тесты |
| `POSTGRES_PORT` | `5499` | Порт PostgreSQL на хосте |
| `MERCHANT_SVC_PASSWORD`, `MERCHANT_MIGRATOR_PASSWORD`, так же `INVENTORY_*`, `CATALOG_*`, `ORDERS_*` | имя роли: `merchant_svc`, `merchant_migrator`, … | Пароли ролей сервиса и миграций. Роли получают их при первой инициализации тома (`deploy/postgres/init/02-role-passwords.sh`), поэтому после смены нужны `make down` и `make up` |
| `MERCHANT_HTTP_PORT`, `CATALOG_HTTP_PORT`, `INVENTORY_HTTP_PORT`, `ORDER_HTTP_PORT`, `POS_HTTP_PORT` | `8081`, `8082`, `8083`, `8084`, `8085` | Публичные HTTP-порты на хосте |
| `MERCHANT_OPS_PORT`, `CATALOG_OPS_PORT`, `INVENTORY_OPS_PORT`, `ORDER_OPS_PORT`, `POS_OPS_PORT` | `9091`, `9092`, `9093`, `9094`, `9095` | Порты `/healthz` и `/readyz` на хосте |
| `RESERVATION_DEFAULT_TTL`, `SWEEPER_INTERVAL` | `30m`, `1m` | Передаются `inventory-service` |
| `CATALOG_MAX_BODY_BYTES` | `1048576` | `HTTP_MAX_BODY_BYTES` для `catalog-service` |
| `RESERVATION_TTL`, `OUTBOX_INTERVAL`, `OUTBOX_BATCH` | `30m`, `1s`, `50` | Передаются `order-service` |
| `PARTNER_POS_API_KEY` | `dev-merchant-key-kafe-primer-0001` | Ключ заведения, от имени которого работает касса |
| `POS_PUBLISH_ON_START`, `POS_STOCK_SYNC_INTERVAL`, `POS_ORDER_POLL_INTERVAL`, `POS_KITCHEN_STEP` | `true`, `1m`, `5s`, `20s` | `PUBLISH_ON_START`, `STOCK_SYNC_INTERVAL`, `ORDER_POLL_INTERVAL` и `KITCHEN_STEP` кассы |
| `DEV_MERCHANT_KEY_*` | | Справочно: демонстрационные ключи из `migrations/merchant/0002_seed.sql`; стенд их не читает |

### Переменные сервисов

Сервис читает конфигурацию из окружения при старте
(`internal/<сервис>/config/config.go`) и при недопустимом значении не
запускается, перечислив все ошибки сразу. Длительности записываются в формате Go:
`300ms`, `5s`, `1m`, `2h`.

Общие для `merchant-service`, `inventory-service`, `catalog-service` и
`order-service`:

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `DATABASE_URL` | обязательна | Строка подключения к своей базе |
| `SERVICE_NAME` | имя сервиса | Поле `service` в логах |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` или `error`; другое значение — `info` |
| `HTTP_ADDR` | `:8080` | Публичный HTTP API |
| `GRPC_ADDR` | `:9090` | Внутренний gRPC; у `order-service` его нет |
| `OPS_ADDR` | `:9091` | `/healthz` и `/readyz` |
| `DB_MAX_CONNS`, `DB_MIN_CONNS` | `10`, `2` | Размер пула соединений; `DB_MAX_CONNS > 0`, `DB_MIN_CONNS ≤ DB_MAX_CONNS` |
| `DB_CONNECT_TIMEOUT` | `5s` | Установка соединения |
| `DB_STATEMENT_TIMEOUT` | `3s` у merchant и inventory, `5s` у catalog и order | `statement_timeout` соединений пула |
| `DB_MAX_CONN_LIFETIME`, `DB_MAX_CONN_IDLE_TIME` | `30m`, `5m` | Ротация соединений пула |
| `HTTP_READ_HEADER_TIMEOUT`, `HTTP_READ_TIMEOUT`, `HTTP_WRITE_TIMEOUT`, `HTTP_IDLE_TIMEOUT` | `5s`, `15s`, `20s`, `60s` | Таймауты HTTP-сервера |
| `HTTP_HANDLER_TIMEOUT` | `10s` | Предел обработки одного запроса |
| `HTTP_MAX_BODY_BYTES` | `65536`, у catalog `1048576` | Предел тела запроса; больше — 413 `PAYLOAD_TOO_LARGE` |
| `GRPC_HANDLER_TIMEOUT`, `GRPC_MAX_CONNECTION_AGE` | `5s`, `30m` | gRPC-сервер merchant, inventory и catalog |
| `GRPC_KEEPALIVE_TIME`, `GRPC_KEEPALIVE_TIMEOUT`, `GRPC_MAX_RECV_MSG_BYTES` | `30s`, `10s`, `4194304` | Keepalive и предел сообщения gRPC; у `order-service` — для его клиентов |
| `SHUTDOWN_TIMEOUT` | `25s` | Время на завершение запросов после SIGTERM |
| `READINESS_TIMEOUT` | `2s` | Проверка базы в `/readyz` |
| `RUN_MIGRATIONS` | `false` | Применять миграции при старте сервиса, а не отдельным job |

Свои у каждого сервиса:

| Сервис | Переменная | По умолчанию | Назначение |
|---|---|---|---|
| merchant | `MAX_MERCHANT_BATCH` | `100` | Наибольшее число идентификаторов в одном `GetMerchants` |
| inventory, catalog | `MERCHANT_GRPC_TARGET` | `dns:///merchant-service:9090` | Адрес merchant-service для проверки ключей заведений |
| inventory, catalog | `MERCHANT_CALL_TIMEOUT` | `300ms` | Дедлайн `AuthenticateApiKey` |
| inventory, catalog | `MERCHANT_KEEPALIVE_TIME` | `30s` | Keepalive соединения с merchant-service |
| inventory | `RESERVATION_DEFAULT_TTL` | `30m` | Срок удержания, если `Reserve` пришёл без TTL |
| inventory | `RESERVATION_MAX_TTL` | `2h` | Больший запрошенный срок урезается до него; не меньше `RESERVATION_DEFAULT_TTL` |
| inventory | `MAX_AVAILABILITY_BATCH` | `100` | Наибольшее число позиций в одном запросе доступности |
| inventory | `SWEEPER_INTERVAL`, `SWEEPER_BATCH`, `SWEEPER_TIMEOUT` | `1m`, `100`, `30s` | Период, размер пачки и предел одного прохода sweeper просроченных удержаний |
| catalog | `MAX_QUOTE_BATCH` | `100` | Наибольшее число позиций в одном `QuoteItems` |
| order | `MERCHANT_GRPC_TARGET`, `CATALOG_GRPC_TARGET`, `INVENTORY_GRPC_TARGET` | `dns:///merchant-service:9090`, `dns:///catalog-service:9090`, `dns:///inventory-service:9090` | Адреса трёх сервисов-владельцев |
| order | `MERCHANT_CALL_TIMEOUT`, `MERCHANT_PROFILE_TIMEOUT` | `300ms`, `500ms` | Дедлайны `AuthenticateApiKey` и `GetMerchant` |
| order | `CATALOG_QUOTE_TIMEOUT` | `500ms` | Дедлайн `QuoteItems` |
| order | `INVENTORY_READ_TIMEOUT`, `INVENTORY_RESERVE_TIMEOUT`, `INVENTORY_DECISION_TIMEOUT` | `500ms`, `800ms`, `3s` | Дедлайны `GetAvailability`, `Reserve` и `CommitReservation` / `ReleaseReservation` |
| order | `RESERVATION_TTL` | `30m` | Срок удержания, который сервис запрашивает при оформлении. Должен быть больше окна повторов outbox (около 23,5 минуты при 12 попытках), иначе сервис не запустится |
| order | `OUTBOX_MAX_ATTEMPTS` | `12` | Число попыток, после которого сообщение переводится в `FAILED` |
| order | `OUTBOX_INTERVAL`, `OUTBOX_BATCH` | `1s`, `50` | Период и размер пачки диспетчера outbox |
| order | `OUTBOX_LEASE` | `1m` | На сколько захваченное сообщение скрыто от других диспетчеров; не меньше `INVENTORY_DECISION_TIMEOUT` |
| order | `OUTBOX_TIMEOUT` | `30s` | Предел одного прохода диспетчера |

`partner-pos` (базы нет):

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `PARTNER_POS_API_KEY` | обязательна | Ключ заведения, от имени которого касса вызывает платформу |
| `CATALOG_BASE_URL`, `INVENTORY_BASE_URL`, `ORDER_BASE_URL` | `http://catalog-service:8080`, `http://inventory-service:8080`, `http://order-service:8080` | Публичные API платформы |
| `PLATFORM_TIMEOUT` | `5s` | Предел одного HTTP-вызова платформы |
| `PUBLISH_ON_START` | `true` | Публиковать меню при старте и повторять до успеха; при `false` касса ждёт `POST /pos/publish` |
| `STOCK_SYNC_INTERVAL` | `1m` | Период выталкивания изменённых кассой остатков и стоп-листа |
| `ORDER_POLL_INTERVAL` | `5s` | Период опроса очереди заказов |
| `KITCHEN_STEP` | `20s` | Длительность одного шага имитации кухни |
| `ORDER_PAGE_LIMIT` | `50` | Заказов за один опрос |
| `SERVICE_NAME`, `LOG_LEVEL`, `HTTP_ADDR`, `OPS_ADDR` | `partner-pos`, `info`, `:8080`, `:9091` | Как у сервисов платформы |
| `HTTP_READ_HEADER_TIMEOUT`, `HTTP_READ_TIMEOUT`, `HTTP_WRITE_TIMEOUT`, `HTTP_IDLE_TIMEOUT`, `HTTP_MAX_BODY_BYTES` | `5s`, `15s`, `20s`, `60s`, `65536` | Как у сервисов платформы |
| `HTTP_HANDLER_TIMEOUT` | `30s` | Предел обработки запроса; больше `PLATFORM_TIMEOUT`, потому что `POST /pos/publish` вызывает платформу несколько раз |
| `SHUTDOWN_TIMEOUT` | `15s` | Время на завершение запросов после SIGTERM |

Job миграций (`cmd/migrate`):

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `MIGRATE_SERVICE` | `merchant` | `merchant`, `inventory`, `catalog` или `orders` |
| `DATABASE_URL` | обязательна | Строка подключения ролью `*_migrator` |
| `MIGRATE_TIMEOUT` | `2m` | Предел всего прогона |
| `LOG_LEVEL` | `info` | Уровень логов |

Тесты:

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `TEST_DATABASE_URL` | в `Makefile`: `postgres://postgres:postgres@127.0.0.1:5499/postgres?sslmode=disable` | Подключение суперпользователя PostgreSQL для интеграционных тестов; каждый тест создаёт и удаляет свою базу. Без переменной `go test` пропускает эти тесты |
| `E2E_MERCHANT_URL`, `E2E_CATALOG_URL`, `E2E_INVENTORY_URL`, `E2E_ORDER_URL` | в `Makefile`: `http://localhost:8081` … `:8084` | Адреса стенда для сквозных тестов; без любой из них тесты пропускаются |
| `E2E_POS_URL` | в `Makefile`: `http://localhost:8085` | Касса для сквозных тестов; без неё пропускается сценарий кассы, а её режим тесты не трогают |

## Разработка без Docker

Зависимости не вендорируются: их версии фиксирует `go.mod`, а контрольные
суммы — `go.sum`. На «холодном» кэше первая команда требует доступа к прокси
модулей.

```bash
make deps             # go mod download (нужна сеть, один раз)
make verify           # go mod verify — сверка контрольных сумм
make build            # go build ./...
make test             # unit-тесты, БД не требуется
make test-integration # + тесты против настоящего PostgreSQL
make test-race        # полный набор под детектором гонок
make lint             # golangci-lint v2; если он не установлен — go vet
make check            # fmt-check, vet, lint, verify и test-race
make generate         # регенерация gRPC-кода из api/proto
```

Интеграционные тесты берут строку подключения из `TEST_DATABASE_URL` и создают
отдельную базу на каждый тест. Если переменная не задана, эти тесты
пропускаются — `go test ./...` остаётся работоспособным без базы.
`make test-integration`, `make test-race` и `make check` по умолчанию
подставляют PostgreSQL стенда, поэтому он должен быть запущен — весь стенд
(`make up`) или только база:

```bash
docker compose up -d --wait postgres
make test-race
```

Для `go test` без `make` строка подключения задаётся явно:

```bash
export TEST_DATABASE_URL='postgres://postgres:postgres@127.0.0.1:5499/postgres?sslmode=disable'
go test -race -count=1 ./...
```

Порт 5499 — это порт, на котором стенд публикует PostgreSQL наружу
(`POSTGRES_PORT` в `.env`). Внутри сети Compose сервисы обращаются к
`postgres:5432`; порт хоста отличается, чтобы не конфликтовать с локально
установленным PostgreSQL.

Применение миграций вручную:

```bash
MIGRATE_SERVICE=merchant  DATABASE_URL='postgres://...' go run ./cmd/migrate
MIGRATE_SERVICE=inventory DATABASE_URL='postgres://...' go run ./cmd/migrate
MIGRATE_SERVICE=catalog   DATABASE_URL='postgres://...' go run ./cmd/migrate
MIGRATE_SERVICE=orders    DATABASE_URL='postgres://...' go run ./cmd/migrate
```

Создание полной структуры каталогов системы (безопасно повторно, ничего не
удаляет и не перезаписывает): `./scripts/create-structure.sh`.

## Внешний API merchant-service

Спецификация — [`api/openapi/merchant.v1.yaml`](api/openapi/merchant.v1.yaml).

| Метод и путь | Доступ | Назначение |
|---|---|---|
| `GET /api/v1/merchants` | публичный | Список активных заведений: `city`, `q`, `only_accepting`, `limit`, `offset` |
| `GET /api/v1/merchants/{merchant_id}` | публичный | Карточка заведения; отключённое оператором возвращает 404 |
| `GET /api/v1/merchant/profile` | `X-Api-Key` | Свой профиль, `ETag` с версией |
| `PATCH /api/v1/merchant/profile` | `X-Api-Key` | Частичное изменение; `If-Match` включает оптимистический контроль |

Ошибки единого формата: `{"error":{"code","message","details","request_id"}}`.
Коды: `VALIDATION_FAILED`, `UNAUTHORIZED`, `MERCHANT_NOT_FOUND`,
`NO_PROFILE_CHANGES`, `VERSION_CONFLICT`, `PAYLOAD_TOO_LARGE`,
`UNSUPPORTED_MEDIA_TYPE`, `INTERNAL_ERROR`.

Внутренний контракт — `merchant.v1.MerchantService` на порту 9090:
`AuthenticateApiKey`, `GetMerchant`, `GetMerchants`. Порт наружу не публикуется.
Ops-эндпоинты `/healthz` и `/readyz` — на отдельном порту 9091.

## Схема БД merchant

Одна логическая база `merchant` с двумя ролями: `merchant_svc` (только DML) и
`merchant_migrator` (владелец схемы, DDL). Роль сервиса не имеет прав на чужие
базы, поэтому доступ к данным другого сервиса невозможен физически.

**`merchants`** — заведения закрытого списка.

| Колонка | Тип | Комментарий |
|---|---|---|
| `id` | `uuid` PK | Глобальный идентификатор заведения |
| `slug` | `text` UNIQUE | Человекочитаемый идентификатор |
| `name`, `description`, `cuisine`, `city`, `address`, `phone` | `text` | Витрина |
| `status` | `text` | `ACTIVE` / `SUSPENDED`; меняет только оператор площадки |
| `is_accepting_orders` | `boolean` | Оперативный переключатель заведения |
| `min_order_minor`, `delivery_fee_minor` | `bigint` | Копейки, `RUB` |
| `avg_prep_minutes` | `integer` | 1…240 |
| `version` | `bigint` | Растёт при каждом изменении профиля |
| `created_at`, `updated_at` | `timestamptz` | UTC |

Ограничения: `CHECK` на диапазоны сумм, времени приготовления, длины названия,
города и адреса, допустимые значения `status` и `version > 0`.
Индекс `(city, name, id) WHERE status = 'ACTIVE'` обслуживает публичный список.

`status` и `is_accepting_orders` разделены намеренно: отключение оператором и
оперативная пауза — решения разных субъектов, и заказ принимается только когда
оба разрешают.

**`merchant_api_keys`** — ключи доступа заведений.

| Колонка | Тип | Комментарий |
|---|---|---|
| `key_hash` | `bytea` PK | SHA-256 ключа; сам ключ не хранится |
| `merchant_id` | `uuid` FK | Владелец |
| `is_active`, `revoked_at` | | Отзыв ключа; согласованность гарантируется `CHECK` |

Аутентификация — один запрос с `JOIN` по первичному ключу: она лежит на горячем
пути всех merchant-эндпоинтов всех сервисов.

Демонстрационные ключи создаёт `migrations/merchant/0002_seed.sql`. Это
**ключи локального стенда**, они намеренно закоммичены, чтобы окружение было
работоспособно сразу после `docker compose up`. Production-ключи выпускает
оператор, они не попадают в репозиторий; в базе в любом случае хранится только
SHA-256.

## Внешний API inventory-service

Спецификация — [`api/openapi/inventory.v1.yaml`](api/openapi/inventory.v1.yaml).

Модель учёта: единица учёта — **одна штука** позиции у заведения; штуки
однородны и по отдельности не идентифицируются. Учётный объект — карточка
остатка, адресуемая парой (заведение, позиция меню). У карточки два
**независимых** измерения: переключатель стоп-листа `sales_state` и количества
`on_hand_qty` / `reserved_qty`. Включение продажи никогда не подтверждает
количество: карточка может быть `ENABLED` с нулём штук и тогда не заказуема.

| Метод и путь | Доступ | Назначение |
|---|---|---|
| `GET /api/v1/merchants/{merchant_id}/availability?menu_item_ids=` | публичный | Доступность в штуках; `tracked=false` отличает «карточки нет» от «ноль штук» |
| `GET /api/v1/merchant/stock` | `X-Api-Key` | Свои карточки остатков |
| `GET /api/v1/merchant/stock/{menu_item_id}` | `X-Api-Key` | Одна карточка, `ETag` с версией |
| `PUT /api/v1/merchant/stock/{menu_item_id}` | `X-Api-Key` | Абсолютная установка штук; `If-Match`; 409 `BELOW_RESERVED`, если ниже удержанного |
| `POST /api/v1/merchant/stock/{menu_item_id}/adjust` | `X-Api-Key` | Относительное изменение; `operation_key` делает повтор безопасным |
| `PATCH /api/v1/merchant/stock/{menu_item_id}` | `X-Api-Key` | Стоп-лист; количеств не меняет |
| `GET /api/v1/merchant/stock/{menu_item_id}/movements` | `X-Api-Key` | Журнал изменений количеств, новые записи первыми |

Коды ошибок: `VALIDATION_FAILED`, `UNAUTHORIZED`, `STOCK_ITEM_NOT_FOUND`,
`BELOW_RESERVED`, `NEGATIVE_ON_HAND`, `VERSION_CONFLICT`,
`DEPENDENCY_UNAVAILABLE`, `INTERNAL_ERROR`.

Внутренний контракт — `inventory.v1.InventoryService` на порту 9090:
`GetAvailability`, `Reserve`, `CommitReservation`, `ReleaseReservation`.
`Reserve` удерживает штуки по всем строкам целиком или не удерживает ничего и
идемпотентен по `reservation_id`. Отказ приходит как `FAILED_PRECONDITION`, а
детализация по строкам — в деталях статуса; извлекается `grpcapi.RejectionDetail`.

## Схема БД inventory

Логическая база `inventory` с ролями `inventory_svc` (DML) и
`inventory_migrator` (DDL).

| Таблица | Назначение | Ключевые ограничения |
|---|---|---|
| `stock_items` | Карточка остатка позиции | UNIQUE (`merchant_id`, `menu_item_id`); `on_hand_qty >= 0`; `reserved_qty >= 0`; **`reserved_qty <= on_hand_qty`**; `sales_state IN ('ENABLED','STOPPED')` |
| `stock_reservations` | Удержание штук под заказ | PK = идентификатор заказа (идемпотентность `Reserve`); `state IN ('HELD','COMMITTED','RELEASED')`; частичный индекс по `expires_at` для sweeper |
| `stock_reservation_lines` | Строки удержания | PK (`reservation_id`, `stock_item_id`); `qty > 0` |
| `stock_movements` | Append-only журнал изменений количеств | `kind IN ('SET','ADJUST','RESERVE','RELEASE','COMMIT','EXPIRE')`; запись в той же транзакции, что и изменение |
| `stock_operations` | Дедупликация относительных изменений | PK (`stock_item_id`, `operation_key`) |

Конкурентность удержаний решается одним условным `UPDATE` на строку:
`SET reserved_qty = reserved_qty + $qty WHERE ... AND sales_state = 'ENABLED'
AND on_hand_qty - reserved_qty >= $qty`. PostgreSQL сам блокирует строку и
перепроверяет условие, поэтому перепродажа невозможна без явных блокировок и
без уровня изоляции выше READ COMMITTED. Строки многопозиционного удержания
захватываются в порядке возрастания `menu_item_id`, что исключает взаимные
блокировки. Терминальный переход удержания выполняется условием
`WHERE id = $1 AND state = 'HELD'` и потому происходит ровно один раз, даже
когда подтверждение, освобождение и sweeper соревнуются между собой.

Операции заведения (`PUT`, `POST …/adjust` и `PATCH` карточки) вычисляют новое
значение из прочитанной карточки, поэтому сначала блокируют её
`SELECT … FOR UPDATE`. Без блокировки параллельные корректировки теряли бы
друг друга, а корректировка во время списания подтверждённого заказа
возвращала бы списанные штуки.

## Внешний API catalog-service

Спецификация — [`api/openapi/catalog.v1.yaml`](api/openapi/catalog.v1.yaml),
порт 8082. Общие правила (конверт ошибок, `X-Request-Id`, `ETag`/`If-Match`,
пагинация) — §7.1 ТЗ.

| Метод и путь | Аутентификация | Назначение |
|---|---|---|
| `GET /api/v1/merchants/{merchant_id}/menu` | нет | Публичное меню с `menu_version` |
| `GET /api/v1/merchant/menu` | `X-Api-Key` | Своё меню; до первой публикации — версия 0 |
| `PUT /api/v1/merchant/menu` | `X-Api-Key` | Публикация снимка целиком, `If-Match: menu_version` |
| `PATCH /api/v1/merchant/menu/items/{item_id}` | `X-Api-Key` | Изменение названия и цены позиции |

Внутренний контракт — `catalog.v1.CatalogService.QuoteItems`
([`api/proto/catalog/v1/catalog.proto`](api/proto/catalog/v1/catalog.proto)):
`order-service` получает по идентификаторам позиций их текущие названия и цены
вместе с `menu_version`. Неизвестная, чужая или снятая с продажи позиция
возвращается с кодом проблемы (`NOT_FOUND`, `NOT_IN_MERCHANT`, `UNPUBLISHED`),
а не обрушивает вызов: заказу нужен исход по каждой строке сразу. Позиция в
стоп-листе для каталога проблемой не является — стоп-лист принадлежит
`inventory-service`.

Коды ошибок сверх общих: `MENU_NOT_FOUND` (404), `MENU_ITEM_NOT_FOUND` (404).
Несовпадение `If-Match` — 412 `VERSION_CONFLICT`.

Публикация — идемпотентная по содержанию операция: повторная отправка того же
снимка меняет только `menu_version`. Позиции сопоставляются по `external_id`,
поэтому `menu_item_id` остаётся прежним между публикациями, и карточки остатков
в `inventory-service` продолжают указывать на ту же позицию.

## Схема БД catalog

База `catalog`, роли `catalog_svc` (DML) и `catalog_migrator` (владелец схемы).
Миграции — [`migrations/catalog/`](migrations/catalog/).

**`menu_categories`** — разделы меню.

| Колонка | Тип | Комментарий |
|---|---|---|
| `id` | `uuid` PK | |
| `merchant_id` | `uuid` | Владелец меню |
| `name` | `text` | UNIQUE вместе с `merchant_id` |
| `position` | `integer` | Порядок вывода |

**`menu_items`** — позиции меню.

| Колонка | Тип | Комментарий |
|---|---|---|
| `id` | `uuid` PK | Идентификатор позиции на площадке; на него ссылаются остатки и заказы |
| `merchant_id` | `uuid` | Владелец |
| `category_id` | `uuid` FK | Раздел |
| `external_id` | `text` | Идентификатор позиции в системе заведения; UNIQUE вместе с `merchant_id` |
| `name`, `description` | `text` | Витрина |
| `price_minor` | `bigint` | Копейки, `RUB`; `CHECK price_minor > 0` |
| `is_deleted` | `boolean` | Снята с продажи; строка не удаляется |
| `position` | `integer` | Порядок внутри раздела |
| `created_at`, `updated_at` | `timestamptz` | UTC |

Индекс `(merchant_id, category_id, position) WHERE NOT is_deleted` обслуживает
чтение меню одним запросом.

**`merchant_menu_state`** — версия меню заведения.

| Колонка | Тип | Комментарий |
|---|---|---|
| `merchant_id` | `uuid` PK | |
| `menu_version` | `bigint` | `CHECK menu_version > 0`; значение для `If-Match` |
| `updated_at` | `timestamptz` | UTC |

Версия вынесена в отдельную таблицу, потому что каталог не владеет сущностью
заведения и не может хранить её в чужой строке.

Публикация выполняется одной транзакцией: строка версии блокируется
`SELECT … FOR UPDATE`, что сериализует публикации одного заведения и фиксирует
значение, с которым сверяется `If-Match`; затем позиции снимка вставляются или
обновляются `ON CONFLICT (merchant_id, external_id)`, отсутствующие помечаются
`is_deleted`, и версия увеличивается последней. Порядок записи детерминирован
(категории по имени, позиции по `external_id`), поэтому параллельные публикации
не образуют взаимных блокировок. Одновременная публикация одной и той же версии
двумя терминалами пропускает ровно одну и отдаёт второй 412.

## Внешний API order-service

Спецификация — [`api/openapi/order.v1.yaml`](api/openapi/order.v1.yaml),
порт 8084. Общие правила (конверт ошибок, `X-Request-Id`, пагинация) — §7.1 ТЗ.

| Метод и путь | Аутентификация | Назначение |
|---|---|---|
| `POST /api/v1/orders/preview` | нет | Пересчёт корзины: цены, штуки, условия заведения |
| `POST /api/v1/orders` | `X-Customer-Id` + `Idempotency-Key` | Оформление: удерживает штуки и сохраняет снимок цен |
| `GET /api/v1/orders` | `X-Customer-Id` | История покупателя |
| `GET /api/v1/orders/{order_id}` | `X-Customer-Id` | Один заказ покупателя |
| `POST /api/v1/orders/{order_id}/cancel` | `X-Customer-Id` | Отмена до решения заведения |
| `GET /api/v1/merchant/orders` | `X-Api-Key` | Очередь заведения, старые сверху |
| `GET /api/v1/merchant/orders/{order_id}` | `X-Api-Key` | Один заказ заведения |
| `POST /api/v1/merchant/orders/{order_id}/accept` | `X-Api-Key` | Принять, `eta_minutes` |
| `POST /api/v1/merchant/orders/{order_id}/reject` | `X-Api-Key` | Отклонить, `reason` |
| `POST /api/v1/merchant/orders/{order_id}/status` | `X-Api-Key` | `COOKING` → `DELIVERING` → `DELIVERED` |

`order-service` не публикует gRPC-контракт: его никто не вызывает. Он сам
обращается к трём сервисам — `merchant.v1.GetMerchant` (условия заведения),
`catalog.v1.QuoteItems` (названия и цены), `inventory.v1.Reserve` /
`CommitReservation` / `ReleaseReservation` (штуки).

Коды ошибок сверх общих: `ORDER_NOT_FOUND` (404), `MERCHANT_NOT_FOUND` (404),
`ORDER_STATE_CONFLICT` (409), `PRICE_CHANGED` (409). Отказ корзины — 409, где
код называет главную причину (`INSUFFICIENT_UNITS`, `ITEM_SALES_STOPPED`,
`MIN_ORDER_NOT_MET`, `MERCHANT_NOT_ACCEPTING_ORDERS` и другие), а `details`
перечисляют все проблемные позиции и правила сразу.

Идемпотентность двух видов. Оформление — по `Idempotency-Key` в пределах
покупателя: повтор отвечает 201 с тем же заказом и заголовком
`Idempotent-Replay: true`. Операции заведения — по целевому статусу: повтор
перехода в уже установленный статус отвечает 200 и тем же заголовком, ничего не
меняя и не планируя второе сообщение.

**Штуки списываются не в момент вызова.** Приём, отказ и отмена записывают
решение и outbox-сообщение одной транзакцией, а фоновый диспетчер доставляет
его в `inventory-service`. Гарантия — доставка не менее одного раза плюс
идемпотентный получатель, то есть бизнес-эффект ровно один. Exactly-once
доставка не заявляется и таким механизмом не обеспечивается.

## Схема БД orders

База `orders`, роли `orders_svc` (DML) и `orders_migrator` (владелец схемы).
Миграции — [`migrations/orders/`](migrations/orders/).

**`orders`** — заказы и их жизненный цикл.

| Колонка | Тип | Комментарий |
|---|---|---|
| `id` | `uuid` PK | Он же идентификатор резерва в `inventory-service` |
| `merchant_id`, `merchant_name` | `uuid`, `text` | Заведение и снимок его названия |
| `customer_id` | `uuid` | Покупатель (идентификация, не аутентификация) |
| `idempotency_key` | `text` | UNIQUE вместе с `customer_id` |
| `customer_name`, `customer_phone`, `delivery_address`, `comment` | `text` | Данные доставки |
| `status` | `text` | Семь значений §6.5, ограничены `CHECK` |
| `items_total_minor`, `delivery_fee_minor`, `total_minor` | `bigint` | Копейки; `CHECK total = items + delivery` |
| `eta_minutes` | `integer` | 0…240 |
| `reject_reason` | `text` | Причина отказа заведения |
| `created_at`, `updated_at` | `timestamptz` | UTC |

Индексы: `(merchant_id, status, created_at)` — очередь заведения;
`(customer_id, created_at DESC)` — история покупателя.

**`order_items`** — снимок позиций на момент оформления.

| Колонка | Тип | Комментарий |
|---|---|---|
| `order_id`, `menu_item_id` | `uuid` | PK; `order_id` — FK на `orders` |
| `external_id`, `name` | `text` | Снимок из каталога |
| `unit_price_minor`, `qty`, `line_total_minor` | `bigint`, `integer`, `bigint` | `CHECK qty 1…99`, `CHECK line_total = unit_price * qty` |

Снимок цен хранится вместе с заказом, поэтому чтение заказа не обращается к
`catalog-service`, а изменение цены в меню не переписывает историю.

**`outbox_messages`** — терминальные решения, ожидающие доставки.

| Колонка | Тип | Комментарий |
|---|---|---|
| `id` | `uuid` PK | |
| `aggregate_id` | `uuid` | Заказ |
| `type` | `text` | `RESERVATION_COMMIT` или `RESERVATION_RELEASE` |
| `payload` | `jsonb` | `reservation_id` и причина возврата |
| `state`, `attempts`, `available_at`, `last_error` | | Состояние доставки |

Частичный индекс `(available_at, id) WHERE state = 'PENDING'` обслуживает
диспетчер: он выбирает пачку `FOR UPDATE SKIP LOCKED`, сразу засчитывает
попытку и отодвигает следующую видимость, а сетевые вызовы делает уже вне
транзакции. Повторы — до 12 попыток с задержкой 1→2→4→…→300 с; общее окно
(около 23 минут) намеренно меньше 30-минутного TTL резерва, поэтому потерянный
возврат исправляет sweeper `inventory-service`, а не человек. Ответ
`INVALID_ARGUMENT` повторам не подлежит и сразу переводит сообщение в `FAILED`.

## partner-pos — эмулятор кассы заведения

Спецификация — [`api/openapi/partner-pos.v1.yaml`](api/openapi/partner-pos.v1.yaml),
порт 8085. Это **не пятый сервис платформы**: касса моделирует внешнюю
организацию (§3.3 ТЗ). У неё нет базы, нет доступа во внутреннюю gRPC-сеть и
нет никаких привилегий — она ходит в те же публичные HTTP-API и с тем же
ключом заведения, что и реальная интеграция. Правило `partner-pos-is-external`
в `.golangci.yml` запрещает ей импортировать `internal/*` других сервисов и
`gen/grpc`, поэтому демонстрация остаётся достоверной.

| Метод и путь | Назначение |
|---|---|
| `GET /pos/menu` | Меню кассы и то, что о нём знает платформа |
| `PATCH /pos/menu/{external_id}` | Изменить цену, название, остаток или стоп-лист позиции |
| `POST /pos/publish` | Опубликовать меню и сразу выставить остатки |
| `GET /pos/orders` | Очередь заказов, которую касса сейчас ведёт |
| `PUT /pos/behavior` | Как касса отвечает на заказы |

Что касса делает сама:

* публикует меню в `catalog-service` (`PUT /api/v1/merchant/menu` с `If-Match`),
  читает его обратно и так узнаёт `menu_item_id` своих позиций. Публикация
  повторяется до успеха: если платформа при старте ещё прогревалась, следующий
  проход фонового цикла доводит дело до конца, а не оставляет кассу
  неопубликованной до ручного вмешательства;
* выталкивает в `inventory-service` те остатки и стоп-листы, которые изменила у
  себя (`PUT` и `PATCH /api/v1/merchant/stock/{id}` — два отдельных вызова,
  потому что стоп-лист не должен менять количества). Слепой перезаписи раз в
  минуту нет намеренно: она откатывала бы всё, что заведение или оператор
  сделали через API платформы;
* раз в пять секунд опрашивает очередь `order-service` и отвечает по режиму.

Режимы `PUT /pos/behavior`: `MANUAL` (по умолчанию) — касса не трогает заказы
вообще, ими управляет человек; `AUTO_ACCEPT` подтверждает каждый новый заказ;
`AUTO_REJECT` отклоняет с указанной причиной. Флаг `kitchen` включает имитацию
кухни: подтверждённый заказ шагает `COOKING → DELIVERING → DELIVERED`, один шаг
за `KITCHEN_STEP`; в ручном режиме имитация тоже молчит.

Демонстрационное меню кассы повторяет seed платформы: `POS-001` — 10 штук в
продаже, `POS-002` — разрешена, но ноль штук, `POS-003` — штуки есть, но
позиция в стоп-листе. Так первая же синхронизация не разрушает сценарии §4 ТЗ,
на которых держатся проверки.

```bash
# посмотреть меню кассы
curl -s localhost:8085/pos/menu

# поднять цену и остаток одной позиции и опубликовать
curl -s -X PATCH -H 'Content-Type: application/json' \
     -d '{"price_minor":43000,"on_hand_qty":12}' localhost:8085/pos/menu/POS-001
curl -s -X POST localhost:8085/pos/publish

# переключить кассу на автоматический отказ
curl -s -X PUT -H 'Content-Type: application/json' \
     -d '{"mode":"AUTO_REJECT","eta_minutes":20,"reject_reason":"закончились продукты","kitchen":false}' \
     localhost:8085/pos/behavior

# вернуть ручной режим по умолчанию
curl -s -X PUT -H 'Content-Type: application/json' \
     -d '{"mode":"MANUAL","eta_minutes":25,"reject_reason":"закончились продукты","kitchen":true}' \
     localhost:8085/pos/behavior
```

Решение о статусе выводится из того, что сообщает платформа: из статуса заказа
и возраста этого статуса. Касса не хранит о заказах ничего, поэтому её
перезапуск ничего не меняет, а повторный опрос не может продвинуть заказ
дважды.

## Принятые решения и упрощения

| Решение | Обоснование |
|---|---|
| Четыре бизнес-сервиса, а не три | Аутентификацией заведения пользуются `catalog`, `inventory` и `order`; хранение ключей в каталоге сделало бы его скрытым провайдером аутентификации, и отказ каталога ломал бы приём заказов |
| `inventory-service` отдельно от `catalog-service` | Разные транзакционные границы и профили записи: публикация меню — редкая операция над многими строками, резервирование штук — частая над одной строкой с высокой конкуренцией |
| Только gRPC между сервисами, без Kafka | Один асинхронный обмен с одним потребителем; надёжность даёт outbox, брокер не изменил бы гарантий |
| Идемпотентность оформления — колонка `orders.idempotency_key` с UNIQUE, а не отдельная таблица с двухфазным захватом | Между внешним вызовом и локальной фиксацией нет второго изменения состояния платформы, которое пришлось бы откатывать |
| Оптимистический контроль через `version` и `If-Match` | Конкурентное изменение профиля становится явной ошибкой 412 вместо молча потерянного обновления |
| Собственный runner миграций (~120 строк) вместо goose | Нужны ровно упорядоченное применение и advisory-lock; отдельная зависимость не оправдана |
| Зависимости не вендорированы, фиксируются `go.mod` и `go.sum` | Поставка не содержит исходников сторонних библиотек; целостность проверяется `go mod verify` |
| `grpc-go` v1.73.0, а не v1.74.x | v1.74.x требует `genproto`, собирающийся только на Go ≥ 1.25, что противоречит директиве `go 1.24` |
| Публикация меню — снимок целиком, а не пооперационное редактирование | Интеграция кассы экспортирует полное меню; частичная публикация оставила бы в продаже позицию, потерянную при экспорте |
| Позиции меню не удаляются физически, а помечаются `is_deleted` | На них ссылаются снимки в заказах и карточки остатков; удаление разорвало бы эти ссылки |
| Совпадение позиций при публикации по `external_id`, а не по имени | Идентификатор в системе заведения стабилен, поэтому `menu_item_id` не меняется между публикациями и остаётся тем же для остатков и заказов |
| Категория без позиций не публикуется | Категория читается через ссылающиеся на неё позиции, поэтому пустая категория непредставима в выданном меню |
| `PATCH` цены позиции бампает `menu_version` | Цена — содержимое меню, которое `order-service` котирует; клиент, читавший версию N, должен увидеть, что она больше не актуальна |
| Публичное меню незнакомого заведения — 404, своё меню до первой публикации — 200 с версией 0 | Каталог не владеет сущностью заведения и отвечает только за меню, которые хранит; аутентифицированному заведению нужно увидеть пустое состояние, чтобы его покинуть |
| Оформление удерживает штуки до записи заказа, а не после | Обратный порядок означал бы, что заказ уже создан, а последней порции нет; окно между удержанием и записью — единственное рассогласование в системе, и его закрывают TTL резерва и sweeper |
| Идентификатор резерва равен идентификатору заказа | Удержание и заказ не могут разойтись, а повторный `Reserve` идемпотентен по тому же ключу |
| Терминальное решение доставляется через outbox, а не прямым вызовом в обработчике | Иначе одна операция меняла бы состояние в двух системах, и сбой второй половины оставил бы подтверждённый заказ с висящими штуками |
| Заявляется at-least-once, а не exactly-once | Подтверждение получателя и наша запись о нём — разные системы; ровно один бизнес-эффект даёт идемпотентность получателя, а не свойство доставки |
| Снимок названий и цен хранится в заказе | Чтение заказа не зависит от каталога, а изменение цены не переписывает историю |
| Минимальная сумма заведения сравнивается с суммой позиций без доставки | Иначе платформа обещала бы заведению не тот порог, который оно задало |
| Заказ чужого покупателя или заведения — 404, а не 403 | Аутентификации покупателя нет; подтверждение существования заказа выдало бы его любому, кто угадает идентификатор |
| Повтор перехода в уже установленный статус — 200, а не 409 | Заведение просило состояние, и заказ в этом состоянии; иначе повтор после обрыва связи выглядел бы как ошибка |
| `partner-pos` — внешний клиент, а не пятый сервис платформы | Он моделирует другую организацию; доступ во внутреннюю сеть сделал бы демонстрацию интеграции недостоверной |
| Касса не хранит состояние заказов | Решение выводится из статуса и его возраста, которые сообщает платформа: перезапуск кассы ничего не теряет, повторный опрос ничего не удваивает |
| Касса по умолчанию в режиме `MANUAL` | Она опрашивает ту же очередь заведения, что и любой другой клиент merchant-API. Автоматический приём по умолчанию означал бы, что касса молча забирает заказы, которыми управляют проверки и человек с curl |
| Касса выталкивает только свои изменения остатков | Периодическая слепая перезапись откатывала бы то, что заведение изменило через API платформы, и делала бы проверки недетерминированными |
| API управления кассой без аутентификации | Он управляет демонстрацией, не отдаёт данных платформы и живёт в том же стенде; ключ заведения из процесса не выходит |
| Пароли ролей БД выравниваются отдельным init-скриптом | `.sql` не читает окружение, а DSN в Compose собирается из переменных; без этого шага смена пароля в `.env` оставила бы роли на прежнем |
| Готовность `inventory-service`, `catalog-service` и `order-service` проверяет только свою БД | Недоступность `merchant-service` не должна выводить публичную выдачу остатков из ротации; она проявляется как 503 на merchant-эндпоинтах |
| Штуки однородны, серийных номеров и складов нет | Прямое ограничение ТЗ §5.7 |
| Пагинация `limit`/`offset` | Достаточно при объёмах MVP; переход на keyset-курсор описан в §11.2 ТЗ вместе с триггером |
| Наблюдаемость — структурные логи и `/healthz`, `/readyz`, без Prometheus | Соразмерно MVP; метрики добавляются без изменения контрактов |
| Аутентификации покупателя нет, идентификация по `X-Customer-Id` | Прямо исключена заданием; при появлении аутентификации поле заменяется субъектом токена без изменения контракта |
| Единый экземпляр PostgreSQL, логическое разделение по базам и ролям | Достаточно для воспроизводимой сдачи; разнесение на отдельные экземпляры меняет только строки подключения |

Полный перечень упрощений и условий возврата — §12 и §11.2 ТЗ.

## Структура репозитория

```
api/openapi/        внешние контракты (OpenAPI 3.0.3)
api/proto/          внутренние контракты (gRPC): merchant, catalog, inventory
gen/grpc/           сгенерированный код контрактов (make generate)
cmd/                точки входа сервисов и job миграций
internal/<service>/ domain → app → adapters, по одному дереву на сервис
pkg/                общая техника: logx, envcfg, httpx, pgxx, pgmigrate, grpcx
migrations/<db>/    SQL-миграции, по каталогу на логическую базу
deploy/             Dockerfile сервисов и инициализация PostgreSQL
docs/               ТЗ, карты требований, состояние работы, диаграммы C4 и CJM
scripts/            создание структуры, smoke-проверки по сервисам
test/testenv/       поднятие настоящего PostgreSQL для интеграционных тестов
test/e2e/           сквозные сценарии против запущенного стека
```

## Требования окружения

| Что | Для чего |
|---|---|
| Docker с плагином Compose v2 (`docker compose`) | Стенд: `make up`, `make stack-check` |
| `make`, `bash`, `curl` | Цели `Makefile`, `make wait`, smoke-скрипты |
| `python3` | Разбор JSON в smoke-скриптах и примерах README |
| Go 1.24+ | Сборка без Docker, все тесты, `make e2e` |
| PostgreSQL 16+ | Интеграционные тесты; подходит PostgreSQL 18 стенда на порту 5499 |
| Доступ к Docker Hub и прокси Go-модулей | Первая сборка: базовые образы `golang:1.24-alpine`, `alpine:3.21`, `postgres:18.6-alpine` и зависимости из `go.mod` (`make deps`); в поставку они не включены |
| golangci-lint v2 (необязательно) | `make lint`; без него `make lint` выполняет `go vet`. CI использует v2.14.0 |
| `protoc` с плагинами `protoc-gen-go` и `protoc-gen-go-grpc` | Только `make generate`: сгенерированный код закоммичен, для сборки они не нужны |
