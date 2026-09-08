# Состояние работы

Файл восстанавливает контекст между итерациями. Обновляется в конце каждой.

| Поле | Значение |
|---|---|
| Версия ТЗ | **3.0** (`docs/TZ-v3.0.md`, SHA-256 исходной редакции `d08a39f8…`) |
| Итерация | 4 из 5 |
| Реализовано | `merchant-service` (1), `inventory-service` (2), `catalog-service` (3), `order-service` (4) |
| Осталось | `partner-pos` — внешний эмулятор кассы (пункт A-06 задания) |
| Дата | 2026-09-08 |
| Модуль | `github.com/avito-kitchen/platform`, Go 1.24 |

## 1. Архитектурные решения, зафиксированные и не подлежащие пересмотру

1. Четыре бизнес-микросервиса: `merchant-service`, `catalog-service`, `inventory-service`, `order-service`; плюс `partner-pos` как внешний эмулятор заведения, а не пятый сервис платформы.
2. Порядок реализации по зависимостям: **merchant → inventory → catalog → order → partner-pos**.
3. Межсервисное взаимодействие — только gRPC. Kafka не вводится.
4. Слои `domain → app → adapters`, границы проверяются `depguard`. Кросс-сервисные импорты `internal/*` запрещены; интеграция только через опубликованные контракты и `gen/`.
5. Одна логическая БД и пара ролей на сервис; кросс-доступ запрещён правами.
6. Поштучный учёт: учётный объект — `(merchant_id, menu_item_id)`; величины `on_hand_qty`, `reserved_qty`, `available_qty`; стоп-лист `sales_state` не связан с количествами.
7. Зависимости **не вендорируются**: версии в `go.mod`, суммы в `go.sum`, проверка `go mod verify`.
8. Каталог владеет тем, **что** продаётся и **почём**; идентичность позиции — `menu_item_id` (площадка) и `external_id` (система заведения).
9. Заказ владеет собой и **снимком** названий и цен: чтение заказа не обращается к каталогу, а изменение цены не переписывает историю. Идентификатор резерва равен идентификатору заказа. Терминальное решение доставляется в `inventory-service` через транзакционный outbox: at-least-once плюс идемпотентный получатель; exactly-once не заявляется.

## 2. Изменения, внесённые в четвёртой итерации

| Что | Причина | Где отражено |
|---|---|---|
| Каталог миграций `orders` добавлен в общий `migrations/embed.go` | Новая логическая база; `cmd/migrate` валидирует цель по `migrations.Known` | `migrations/embed.go`; `cmd/migrate` не изменялся |
| Блок ролей и базы `orders` в скрипте инициализации PostgreSQL | Владение данными обеспечивается правами, а не договорённостью | `deploy/postgres/init/01-databases.sql` |
| Блоки `order-migrate` и `order-service` в Compose, порт 8084 | Согласованный способ запуска | `docker-compose.yml`, `.env.example` |
| Правила `depguard` для `internal/order` | Те же границы, что у трёх готовых сервисов | `.golangci.yml` |
| `order-service` не публикует gRPC-сервер | В §3.1 и §7.3 нет ни одного вызова в его сторону | `internal/order/config`, Compose, Dockerfile |
| Деталь отказа `Reserve` читается из `status.details` по контракту `inventory.v1`, а не импортом `internal/inventory/adapters/grpcapi` | §10 запрещает кросс-сервисные импорты, `depguard` это проверяет; опубликованный контракт — разрешённая поверхность интеграции. Это **отменяет** пункт 4 плана предыдущей итерации | ТЗ §14.1, `internal/order/adapters/grpcclient/inventory_client.go` |
| Минимальная сумма сравнивается с суммой позиций без доставки | Иначе платформа обещала бы заведению не тот порог, который оно задало | ТЗ §14.1, `app/preview_order.go` |
| Необязательное `expected_total_minor` в оформлении | Закрывает сценарий §4.1 «цена изменилась после предпросмотра» | ТЗ §14.1, `app/create_order.go` |
| Повтор перехода в уже установленный статус — 200 и `Idempotent-Replay: true` | Делает операции заведения безопасными для повтора без отдельного заголовка | ТЗ §14.1, `postgres.Transition` |
| Лишний резерв освобождается сразу, если заказ не записался | Возврат штук раньше TTL; TTL и sweeper остаются гарантией | ТЗ §14.1, `app.releaseOrphanHold` |

Контракты `merchant.v1`, `catalog.v1`, `inventory.v1`, их сгенерированный код,
`pkg/*`, схемы и код `merchant-service`, `inventory-service` и
`catalog-service` не изменялись.

## 3. Готовые файлы

`merchant-service` (1), `inventory-service` (2) и `catalog-service` (3) — без изменений.

`order-service` (итерация 4):

```
api/openapi/order.v1.yaml
cmd/order-service/main.go
internal/order/domain/{order.go, status.go, money.go, outbox.go, errors.go,
                       order_test.go, status_test.go}
internal/order/app/{ports.go, service.go, errors.go, preview_order.go,
                    create_order.go, order_queries.go, cancel_order.go,
                    merchant_commands.go, dispatch_outbox.go, service_test.go}
internal/order/config/config.go
internal/order/testsupport/fakes.go
internal/order/adapters/httpapi/{customer_router.go, merchant_router.go,
                                 dto.go, errors.go, router_test.go}
internal/order/adapters/grpcclient/{merchant_client.go, catalog_client.go,
                                    inventory_client.go, client_test.go}
internal/order/adapters/postgres/{order_repo.go, outbox_repo.go,
                                  repo_integration_test.go}
internal/order/jobs/{outbox_dispatcher.go, outbox_dispatcher_test.go}
migrations/orders/0001_init.sql
deploy/order-service.Dockerfile
scripts/smoke-order.sh
test/e2e/journey_test.go
docs/REQ-MAP-order.md
```

Изменённые общие файлы: `migrations/embed.go`, `docker-compose.yml`,
`deploy/postgres/init/01-databases.sql`, `.golangci.yml`, `.env.example`,
`Makefile`, `README.md`, `docs/TZ-v3.0.md`, `docs/PROJECT-STATE.md`.

## 4. Не начатые файлы

Всё, что относится к `internal/partnerpos`, `cmd/partner-pos`,
`api/openapi/partner-pos.v1.yaml`, `deploy/partner-pos.Dockerfile`,
`test/load`. Полный перечень путей создаёт `scripts/create-structure.sh`.

## 5. Результаты проверок четвёртой итерации

Окружение этой итерации, как и предыдущей, **не содержало Go-тулчейна, Docker
и доступа в сеть** (`go: not found`; прокси возвращает `403 host_not_allowed`;
поиск по ФС `go`/`go1*`/`gccgo` — пусто). Проверки разделены честно.

Выполнено фактически:

| Проверка | Инструмент | Результат |
|---|---|---|
| Баланс скобок, отсутствие неиспользуемых импортов, существование всех символов локальных пакетов (в том числе `merchantv1`, `catalogv1`, `inventoryv1` в сгенерированном коде), полнота реализации пяти портов обеими реализациями | статический анализатор `tools/gocheck.py` (в поставку не входит) | 127 файлов, замечаний нет |
| Пути и методы роутера совпадают с `api/openapi/order.v1.yaml`; ровно десять эндпоинтов §7.2 | `tools/consistency.py` | совпадают |
| Переменные окружения Compose читаются `config.Load`; `GRPC_ADDR` отсутствует | то же | да |
| Схема `orders` содержит ограничения §6.4: UNIQUE (`customer_id`,`idempotency_key`), CHECK сумм, строк, статусов и состояний outbox, частичный индекс диспетчера | то же | да |
| `Transition` блокирует заказ и пишет outbox-сообщение в той же транзакции; `Claim` использует `FOR UPDATE SKIP LOCKED` | то же | да |
| Политика повторов §8.3: 12 попыток, потолок 300 с, `INVALID_ARGUMENT` без повторов; exactly-once явно не заявляется | то же | да |
| Отсутствие кросс-сервисных импортов из `internal/order`; правило `depguard` добавлено | то же | да |
| Отсутствие `TODO`, `FIXME`, `panic(`, «not implemented» в `internal/order`; все компоненты подключены в `main` | то же | да |
| Синтаксис `scripts/smoke-order.sh` | `bash -n` | без ошибок |
| Разбор `api/openapi/order.v1.yaml`, `docker-compose.yml`, `.golangci.yml`, CI | `python3 -c "import yaml; yaml.safe_load(...)"` | корректный YAML |
| Целостность ZIP, распаковка в чистый каталог и повторный прогон обеих проверок на распакованной версии | `unzip -t`, оба анализатора | без замечаний |

Не выполнено, с указанием причины (каждое — **не подтверждено**, не «успешно»):

| Проверка | Причина |
|---|---|
| `gofmt -l`, `go build ./...`, `go vet ./...`, `go test -race -count=1 ./...`, `go mod verify` | в окружении нет Go-тулчейна, установить его нельзя: сеть заблокирована прокси |
| Интеграционные тесты против настоящего PostgreSQL (`internal/*/adapters/postgres`) | требуют Go и сервер БД |
| Контрактные тесты gRPC (`internal/order/adapters/grpcclient`) | требуют Go |
| `docker compose up --build`, `scripts/smoke-*.sh`, `test/e2e/journey_test.go` | Docker и запущенный стек недоступны |
| `golangci-lint run` | бинарь недоступен |

**Первый шаг следующей сессии:** в окружении с Go 1.24, PostgreSQL, Docker и
сетью выполнить `make check`, затем `docker compose up --build` и
`make smoke`, затем `E2E_*_URL=… go test ./test/e2e/... -count=1`; устранить
всё, что они покажут, и только после этого начинать `partner-pos`.

## 6. Следующий шаг

**Реализовать `partner-pos`** — эмулятор кассы одного заведения (A-06).
Он не имеет базы и не ходит во внутреннюю gRPC-сеть: он клиент публичных API
с ключом заведения, то есть проверяет систему снаружи ровно так же, как это
сделает проверяющий.

1. `internal/partnerpos/domain`: правило решения по заказу (принять или
   отклонить) и модель меню кассы; unit-тесты правила.
2. `internal/partnerpos/adapters/platform/client.go`: HTTP-клиент к
   `catalog-service` (`PUT /api/v1/merchant/menu`), `inventory-service`
   (`PUT /api/v1/merchant/stock/{id}`, `POST …/adjust`) и `order-service`
   (`GET /api/v1/merchant/orders`, `accept`, `reject`, `status`).
3. `internal/partnerpos/app`: `publish_menu`, `sync_stock`, `poll_orders`,
   `decide_order` с ограниченными интервалами и тайм-аутами.
4. `internal/partnerpos/adapters/httpapi/router.go`: эндпоинты управления
   эмулятором (пауза, режим решения) и `/healthz`, `/readyz`.
5. `cmd/partner-pos/main.go`, `internal/partnerpos/config/config.go`,
   `api/openapi/partner-pos.v1.yaml`, `deploy/partner-pos.Dockerfile`, блок в
   Compose (HTTP 8085, без базы, зависит от четырёх сервисов).
6. Обновить README, `docs/TZ-v3.0.md` §14 и этот файл.

## 7. Что обязано остаться совместимым

Имена и версии gRPC-пакетов и методов (`merchant.v1`, `catalog.v1`,
`inventory.v1`); модуль `github.com/avito-kitchen/platform`; переменные
окружения из `internal/*/config/config.go`; адреса `merchant-service:9090`,
`inventory-service:9090` и `catalog-service:9090` внутри Compose; публичные
порты 8081, 8082, 8083, 8084; имена логических БД и ролей; формат конверта
ошибок и коды из `pkg/httpx`; сигнатуры общих пакетов `pkg/*`; демонстрационные
идентификаторы заведения и позиций меню
(`11111111-1111-4111-8111-111111111111`,
`a1111111-1111-4111-8111-{111111111111,222222222222,333333333333}`) и внешние
идентификаторы `POS-001…POS-003`; ключ разработчика
`dev-merchant-key-kafe-primer-0001`.
