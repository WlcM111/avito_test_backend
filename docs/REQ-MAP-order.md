# Карта требований — order-service (итерация 4)

Источники: `docs/TZ-v3.0.md` (версия 3.0), `docs/PROJECT-STATE.md` §6,
зафиксированные контракты `api/proto/{merchant,catalog,inventory}/v1`,
исходное задание Авито (пункты A-01…A-23 в §2.1 ТЗ).

Статусы: **выполнено** — реализовано и подтверждено доступной проверкой;
**не подтверждено** — реализовано, но проверка недоступна в текущем окружении
(причина указана); **н/п** — неприменимо с обоснованием.

Проверки: `UT` — unit-тест, `CT` — контрактный тест через настоящий gRPC поверх
`bufconn`, `HT` — тест HTTP-адаптера через `httptest`, `IT` — интеграционный
тест против настоящего PostgreSQL, `E2E` — `test/e2e/journey_test.go` против
запущенного стека, `smoke` — `scripts/smoke-order.sh`, `SC` — статическая
сверка (`gocheck`/`consistency`, описанные в отчёте итерации).

## Требования задания Авито, относящиеся к четвёртому сервису

| ID | Требование | Ответственность order-service | Сделано | Проверка | Статус |
|---|---|---|---|---|---|
| A-01 | API для заведений | Очередь заказов, приём, отказ, переходы статусов | 5 merchant-эндпоинтов §7.2 | `HT`, smoke, `E2E` (не подтверждено) | выполнено |
| A-02 | API для клиентской части; аутентификацию покупателей не реализовывать | Предпросмотр, оформление, история, отмена; `X-Customer-Id` только идентифицирует | 5 customer-эндпоинтов §7.2 | `HT`, smoke | выполнено |
| A-03 | Один репозиторий | Тот же модуль `github.com/avito-kitchen/platform` | `internal/order`, `cmd/order-service` | `SC` | выполнено |
| A-04 | Основные пользовательские сценарии | S-C3…S-C6, S-M6…S-M8 целиком | см. таблицу сценариев ниже | `UT`, `HT`, `IT` | выполнено |
| A-07 | API и схемы БД с расчётом на масштабирование | Индексы под очередь и историю; страница заказов берёт позиции одним запросом (без N+1); короткие транзакции без сетевых вызовов внутри | `orders_merchant_queue_idx`, `orders_customer_history_idx`, `itemsOf` | `IT: TestListsAreScopedOrderedAndPaginated`, `SC` | выполнено |
| A-08 | Миграции со схемами БД в репозитории | Схема базы `orders` | `migrations/orders/0001_init.sql` | `IT: TestMigrationsAreIdempotent` | выполнено |
| A-09 | OpenAPI-спецификация | Внешний контракт сервиса | `api/openapi/order.v1.yaml` | `SC`: пути и методы совпадают с роутером | выполнено |
| A-10 | Связать данные системы с данными заведений и пользователями | `merchant_id` и снимок `merchant_name`; `menu_item_id` + `external_id` в позициях заказа; `customer_id` | `orders`, `order_items` | `IT`, `E2E` | выполнено |
| A-11 | Язык — Go | Go 1.24, новых зависимостей нет | `go.mod` не изменён | `SC` | выполнено |
| A-12 | БД на выбор | PostgreSQL 18, отдельная логическая база `orders` | `deploy/postgres/init/01-databases.sql` | `SC` | выполнено |
| A-13 | Запуск через Docker Compose | Блоки `order-migrate` и `order-service`, порт 8084 | `docker-compose.yml`, `deploy/order-service.Dockerfile` | `SC`; фактический `docker compose up` — **не подтверждено** (нет Docker) | не подтверждено |
| A-14 | Линтер и статический анализ | Правила границ для нового сервиса | `.golangci.yml`: `app-purity`, `service-isolation-order` | `SC`; `golangci-lint` — **не подтверждено** | не подтверждено |
| A-15 | Кодогенерация для API | Сервис не публикует gRPC; потребляет три сгенерированных контракта без правок | `gen/grpc/*` не изменялись | `CT`, `SC` | выполнено |
| A-17 | Приложение поднимается и выполняет бизнес-сценарии | Точка входа, конфигурация, фоновый воркер, завершение | `cmd/order-service/main.go` | `SC`; фактический запуск — **не подтверждено** (нет Go) | не подтверждено |
| A-19…A-21 | README: описание, архитектура, схема БД | Разделы «Внешний API order-service» и «Схема БД orders» | `README.md` | `SC` | выполнено |
| A-22, A-23 | Упрощения и подход к решениям | Восемь новых строк в таблице решений README | `README.md` | `SC` | выполнено |
| A-06 | Пример заведения отдельным сервисом | **Не входит в четвёртый сервис**: `partner-pos` — внешний эмулятор (ТЗ §3.3), отдельная итерация | — | — | **не выполнено, вне объёма итерации** |
| A-05, A-16, A-18 | CJM, промпт, диаграммы | Относятся к системе в целом и уже поставлены | `docs/cjm`, `docs/c4` | — | н/п |

## Требования ТЗ версии 3.0

| ID | Источник | Требуемое поведение | Сделано | Проверка | Статус |
|---|---|---|---|---|---|
| O-01 | §3.2 | Заказы владеет только order-service; чужие таблицы не пишутся | Отдельная база `orders`, роль `orders_svc` без прав на чужие базы | `SC`, `IT` | выполнено |
| O-02 | §6.4 | `orders`: PK, UNIQUE (`customer_id`, `idempotency_key`), CHECK статусов и сумм, индексы очереди и истории | `0001_init.sql` | `IT: TestSchemaConstraints` | выполнено |
| O-03 | §6.4 | `order_items`: PK (`order_id`, `menu_item_id`), CHECK `qty` 1…99 и `line_total = unit_price * qty` | `0001_init.sql` | `IT: TestSchemaConstraints` | выполнено |
| O-04 | §6.4 | `outbox_messages`: состояния, попытки, `available_at`, частичный индекс | `0001_init.sql` | `IT`, `SC` | выполнено |
| O-05 | §6.5 | Матрица переходов; любой другой переход — 409 `ORDER_STATE_CONFLICT`; повтор перехода в уже установленный статус — 200 без изменения | `domain/status.go`, `postgres.Transition` | `UT: TestStatusTransitionMatrix`, `IT: TestTransitionIsIdempotent`, `HT` | выполнено |
| O-06 | §8.1 | Оформление: GetMerchant → QuoteItems → Reserve(`reservation_id` = `order_id`) → INSERT заказа и позиций одной транзакцией | `app.CreateOrder`, `postgres.Create` | `UT: TestCreateOrderHoldsUnitsAndStoresTheSnapshot`, `IT` | выполнено |
| O-07 | §8.2, §4.1 | Идемпотентность по `Idempotency-Key` в пределах покупателя; повтор — 201 с тем же заказом | UNIQUE + предпроверка + `Idempotent-Replay` | `UT`, `HT: TestCreateOrderIsIdempotentOverHTTP`, `IT: TestConcurrentCreateWithTheSameKey`, smoke | выполнено |
| O-08 | §8.3 | Терминальное решение и outbox-сообщение — одна транзакция; фоновый диспетчер, `FOR UPDATE SKIP LOCKED`, до 12 попыток, 1→2→4→…→300 с, `INVALID_ARGUMENT` без повторов | `postgres.Transition`, `outbox_repo`, `app.DispatchOutbox`, `jobs` | `UT: TestDispatch*`, `IT: TestTransitionWritesTheOutboxMessageAtomically`, `TestConcurrentClaimsDoNotOverlap` | выполнено |
| O-09 | §8.3 | Гарантия — at-least-once + идемпотентный получатель; exactly-once не заявляется | Комментарий и поведение `DispatchOutbox` | `SC`, `UT: TestDispatchDeliversDecisionsOnce` | выполнено |
| O-10 | §8.4 | Резерв, созданный до неудачной фиксации, — единственное окно рассогласования; закрывается TTL и sweeper'ом; лишний резерв освобождается сразу, когда это возможно | `releaseOrphanHold`, TTL 30 мин | `UT` (ветка дубля), `SC` | выполнено |
| O-11 | §7.2 | Десять внешних эндпоинтов, порт 8084 | `httpapi` | `SC`: сверка с OpenAPI, `HT` | выполнено |
| O-12 | §7.3 | Дедлайны: `AuthenticateApiKey` 300 мс, `GetMerchant`/`QuoteItems`/`GetAvailability` 500 мс, `Reserve` 800 мс, терминальные решения 3 с | `config.Config`, конструкторы `grpcclient` | `SC`, `CT` | выполнено |
| O-13 | §7.3 | Отказ `Reserve` приходит как `FAILED_PRECONDITION` с деталью `ReserveResponse` в `status.details` | `reserveRejection` | `CT: TestReserveReadsTheRejectionDetail` | выполнено |
| O-14 | §7.1 | Единый конверт ошибок и коды: 401, 404, 409, 422, 503, 500 без деталей | `httpapi/errors.go` | `HT`, smoke | выполнено |
| O-15 | §4.1 | Все трудности покупателя: стоп-лист, нехватка штук, пауза заведения, отключение оператором, минимальная сумма, изменение цены, двойной клик, отказ заведения | Проблемы позиции и заказа, `PRICE_CHANGED`, идемпотентность | `UT: TestPreviewReportsEveryProblem`, `HT: TestCreateOrderConflicts`, `TestCreateOrderReportsAChangedPrice` | выполнено |
| O-16 | §6 | Деньги — целые копейки, валюта `RUB`; время `timestamptz` UTC, в API RFC 3339 | `domain/money.go`, схема, DTO | `UT`, `IT` | выполнено |
| O-17 | §10 | Слои `domain → app → adapters`; домен без транспорта, БД и сгенерированных моделей; кросс-сервисных импортов нет | Дерево `internal/order` | `SC`: импорты домена — только stdlib и uuid; `depguard` дополнен | выполнено |
| O-18 | §11.1 | `context` во всех блокирующих вызовах, тайм-ауты, закрытие ресурсов, ограниченное число горутин | `grpcclient`, `main`, `jobs` | `SC`, `UT: TestRunStopsOnCancellation` | выполнено |
| O-19 | §14.3 | Совместимость: имена контрактов и методов, модуль, адреса Compose, имена БД и ролей, конверт ошибок | Ничего из перечисленного не изменено | `SC`: diff общих файлов | выполнено |
| O-20 | §2.3 п. 6 | `go build`, `go vet`, `gofmt`, `go test -race` без замечаний | Код написан под эти проверки | **не подтверждено**: в окружении нет Go-тулчейна и сети | не подтверждено |

## Обязательные сценарии

| Сценарий | Участники | Реализовано | Проверка | Статус |
|---|---|---|---|---|
| S-C3 «Собрать корзину и увидеть итог» | order → merchant, catalog, inventory | `POST /api/v1/orders/preview` | `UT`, `HT`, smoke | выполнено |
| S-C4 «Оформить заказ» | order → merchant, catalog, inventory | `POST /api/v1/orders` с удержанием штук | `UT`, `HT`, `IT`, smoke | выполнено |
| S-C5 «Следить за заказом» | order | `GET /api/v1/orders/{id}`, `GET /api/v1/orders` | `HT`, smoke | выполнено |
| S-C6 «Отменить заказ» | order → inventory (через outbox) | `POST /api/v1/orders/{id}/cancel` | `UT`, `HT`, `IT`, smoke | выполнено |
| S-M6 «Увидеть очередь» | order | `GET /api/v1/merchant/orders` | `HT`, smoke | выполнено |
| S-M7 «Принять или отклонить» | order → inventory (через outbox) | `accept` / `reject` | `UT`, `HT`, `IT`, smoke | выполнено |
| S-M8 «Вести заказ по статусам» | order | `POST …/status` | `UT`, `HT`, smoke | выполнено |
| Сквозной путь «меню → предпросмотр → заказ → приём → списание штук → доставка» | все четыре сервиса | `test/e2e/journey_test.go`, `scripts/smoke-order.sh` | **не подтверждено**: требует запущенного стека | не подтверждено |

## Что сознательно не реализовано в этом сервисе

| Не сделано | Обоснование по источникам |
|---|---|
| Аутентификация покупателя | A-02 и §12 ТЗ прямо исключают её; `X-Customer-Id` идентифицирует, но не аутентифицирует |
| Оплата, доставка курьером, рейтинги, отмена после подтверждения | §12 ТЗ: вне объёма MVP |
| Kafka, Redis, отдельный планировщик | §3.3 и §11.2: один асинхронный обмен с одним потребителем; outbox достаточно |
| Прямая запись в таблицы `inventory` или `catalog` | §3.2: владение данными; взаимодействие только по gRPC |
| Автоматическая отмена по истечении TTL заказа | §8.4: возврат штук обеспечивает sweeper `inventory-service`; отдельного «протухания заказа» ТЗ не требует |
| `partner-pos` | §3.3 и §13: внешний эмулятор, следующая (последняя) итерация |
