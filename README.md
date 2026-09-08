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

```bash
git clone <repo> && cd avito-kitchen
cp .env.example .env
docker compose up --build
```

Порядок готовности: PostgreSQL → job миграций (`merchant-migrate`,
`inventory-migrate`, `catalog-migrate`, `order-migrate`) → `merchant-service` →
`inventory-service` и `catalog-service` → `order-service` → `partner-pos`.
Публичные API: `http://localhost:8081` (заведения), `http://localhost:8082`
(меню), `http://localhost:8083` (поштучные остатки), `http://localhost:8084`
(заказы) и `http://localhost:8085` (управление кассой заведения).

Касса при старте публикует своё меню и выставляет остатки, а затем раз в пять
секунд отвечает на новые заказы, поэтому после `docker compose up` система
работает сама: достаточно оформить заказ и смотреть, как меняется его статус.

Проверка:

```bash
curl -s localhost:8081/api/v1/merchants | head -c 400

curl -s -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     localhost:8081/api/v1/merchant/profile

curl -s -X PATCH -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     -H 'Content-Type: application/json' \
     -d '{"is_accepting_orders":false,"min_order_minor":70000}' \
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
# у оставшихся сохраняется menu_item_id
curl -s -X PUT -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     -H 'Content-Type: application/json' -H 'If-Match: "1"' \
     -d '{"categories":[{"name":"Горячее","position":0,"items":[
           {"external_id":"POS-001","name":"Борщ с говядиной","price_minor":43000,"position":0}]}]}' \
     localhost:8082/api/v1/merchant/menu

# изменить цену одной позиции: бампает menu_version
curl -s -X PATCH -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     -H 'Content-Type: application/json' -d '{"price_minor":44500}' \
     localhost:8082/api/v1/merchant/menu/items/a1111111-1111-4111-8111-111111111111
```

Меню и остатки ведутся по одним и тем же идентификаторам позиций, поэтому
ответы двух сервисов соединяются по `menu_item_id` без дополнительной
трансляции.

Заказы — весь путь от корзины до доставки:

```bash
CUSTOMER=$(uuidgen)
ITEM=a1111111-1111-4111-8111-111111111111
MERCHANT=11111111-1111-4111-8111-111111111111
BASKET="{\"merchant_id\":\"$MERCHANT\",\"items\":[{\"menu_item_id\":\"$ITEM\",\"qty\":3}],
         \"customer_name\":\"Иван\",\"customer_phone\":\"+79990000001\",
         \"delivery_address\":\"Невский проспект, 1\"}"

# пересчёт корзины: цены из каталога, штуки из остатков, условия из профиля
curl -s -H 'Content-Type: application/json' -d "$BASKET" \
     localhost:8084/api/v1/orders/preview

# оформление: удерживает штуки; повтор с тем же ключом вернёт тот же заказ
curl -s -X POST -H 'Content-Type: application/json' \
     -H "X-Customer-Id: $CUSTOMER" -H "Idempotency-Key: demo-$CUSTOMER" \
     -d "$BASKET" localhost:8084/api/v1/orders

# очередь заведения и приём заказа (штуки спишутся через outbox)
curl -s -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     'localhost:8084/api/v1/merchant/orders?status=CREATED'
curl -s -X POST -H 'X-Api-Key: dev-merchant-key-kafe-primer-0001' \
     -H 'Content-Type: application/json' -d '{"eta_minutes":25}' \
     localhost:8084/api/v1/merchant/orders/<order_id>/accept
```

Полные сценарии:
`BASE_URL=http://localhost:8081 ./scripts/smoke.sh` (14 проверок),
`BASE_URL=http://localhost:8083 ./scripts/smoke-inventory.sh` (22 проверки,
включая 401, 404, 409, 412 и 422 и повтор неидемпотентной операции),
`BASE_URL=http://localhost:8082 ./scripts/smoke-catalog.sh` (32 проверки,
включая конфликт версии, снятие позиций с продажи и стабильность
идентификаторов) и `BASE_URL=http://localhost:8084 ./scripts/smoke-order.sh`
(57 проверок: весь жизненный цикл заказа, идемпотентность, конфликты статусов,
возврат и списание штук) и
`BASE_URL=http://localhost:8085 ./scripts/smoke-pos.sh` (публикация меню
кассой, синхронизация остатков, автоматический отказ и автоматический приём с
доведением заказа до `DELIVERED`).

Сквозные тесты против запущенного стека:

```bash
E2E_MERCHANT_URL=http://localhost:8081 E2E_CATALOG_URL=http://localhost:8082 \
E2E_INVENTORY_URL=http://localhost:8083 E2E_ORDER_URL=http://localhost:8084 \
go test ./test/e2e/... -count=1 -v
```

Остановка: `docker compose down -v`.

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
make check            # то же, что делает CI
make generate         # регенерация gRPC-кода из api/proto
```

Интеграционные тесты берут строку подключения из `TEST_DATABASE_URL` и создают
отдельную базу на каждый тест. Если переменная не задана, эти тесты
пропускаются — `go test ./...` остаётся работоспособным без базы.

`````markdown
export TEST_DATABASE_URL='postgres://postgres:postgres@127.0.0.1:5499/postgres?sslmode=disable'
make test-race
````

Порт 5499 — это порт, на котором стенд публикует PostgreSQL наружу
(`POSTGRES_PORT` в `.env`). Внутри сети Compose сервисы обращаются к
`postgres:5432`; порт хоста отличается, чтобы не конфликтовать с локально
установленным PostgreSQL.
````


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

Конкурентность решается одним условным `UPDATE` на строку:
`SET reserved_qty = reserved_qty + $qty WHERE ... AND sales_state = 'ENABLED'
AND on_hand_qty - reserved_qty >= $qty`. PostgreSQL сам блокирует строку и
перепроверяет условие, поэтому перепродажа невозможна без явных блокировок и
без уровня изоляции выше READ COMMITTED. Строки многопозиционного удержания
захватываются в порядке возрастания `menu_item_id`, что исключает взаимные
блокировки. Терминальный переход удержания выполняется условием
`WHERE id = $1 AND state = 'HELD'` и потому происходит ровно один раз, даже
когда подтверждение, освобождение и sweeper соревнуются между собой.

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

* при старте публикует меню в `catalog-service` (`PUT /api/v1/merchant/menu`
  с `If-Match`), читает его обратно и так узнаёт `menu_item_id` своих позиций;
* раз в минуту выставляет остатки и стоп-лист в `inventory-service`
  (`PUT` и `PATCH /api/v1/merchant/stock/{id}` — два отдельных вызова, потому
  что стоп-лист не должен менять количества);
* раз в пять секунд опрашивает очередь `order-service` и отвечает по режиму.

Режимы `PUT /pos/behavior`: `AUTO_ACCEPT` подтверждает каждый новый заказ,
`AUTO_REJECT` отклоняет с указанной причиной, `MANUAL` оставляет заказ
человеку. Флаг `kitchen` включает имитацию кухни: подтверждённый заказ шагает
`COOKING → DELIVERING → DELIVERED`, один шаг за `KITCHEN_STEP`.

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

Docker c плагином Compose — для запуска. Go 1.24+ и PostgreSQL 16+ — для
разработки и интеграционных тестов. Доступ к прокси Go-модулей — для первой
сборки (`make deps`); зависимости в поставку не включены. `protoc` с плагинами `protoc-gen-go` и
`protoc-gen-go-grpc` — только для `make generate`; сгенерированный код
закоммичен, поэтому для сборки они не нужны.
