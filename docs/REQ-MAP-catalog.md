# Карта требований — catalog-service (итерация 3)

Источники: `docs/TZ-v3.0.md` (версия 3.0), `docs/PROJECT-STATE.md` §6,
зафиксированный контракт `api/proto/catalog/v1/catalog.proto`,
исходное задание Авито (пункты A-01…A-23 в §2.1 ТЗ).

Статусы: **выполнено** — реализовано и подтверждено проверкой;
**не подтверждено** — реализовано, но проверка недоступна в текущем окружении
(причина указана); **н/п** — неприменимо с обоснованием.

Обозначения проверок: `UT` — unit-тест, `CT` — контрактный тест через настоящий
gRPC поверх `bufconn`, `HT` — тест HTTP-адаптера через `httptest`,
`IT` — интеграционный тест против настоящего PostgreSQL, `smoke` —
`scripts/smoke-catalog.sh`, `SC` — статическая сверка (`tools`-проверки,
описанные в отчёте итерации).

## Требования задания Авито, относящиеся к третьему сервису

| ID | Требование | Ответственность catalog-service | Сделано | Проверка | Статус |
|---|---|---|---|---|---|
| A-01 | API для заведений; закрытый список подключённых заведений | Меню заведения; принадлежность проверяется ключом через `merchant-service` | `GET/PUT /api/v1/merchant/menu`, `PATCH /api/v1/merchant/menu/items/{id}` | `HT`, smoke | выполнено |
| A-02 | API для клиентской части; аутентификацию покупателей не реализовывать | Публичное меню без аутентификации | `GET /api/v1/merchants/{id}/menu` | `HT: TestPublicMenuIsAvailableWithoutAKey`, smoke | выполнено |
| A-03 | Один репозиторий | Сервис живёт в том же модуле `github.com/avito-kitchen/platform` | `internal/catalog`, `cmd/catalog-service` | `SC` | выполнено |
| A-04 | Основные пользовательские сценарии | S-C2 (меню с ценами), участие в S-C3/S-C4 через `QuoteItems` | HTTP-меню, gRPC `QuoteItems` | `HT`, `CT`, `IT` | выполнено |
| A-07 | API и схемы БД с расчётом на масштабирование | Меню читается одним запросом; индекс под фактический запрос; версия меню для кэширования по `ETag` | `menu_items_menu_idx`, `readCategories` одним `JOIN` | `IT: TestGetMenuReturnsTheSeededMenu`, `SC` | выполнено |
| A-08 | Миграции со схемами БД в репозитории | Схема и seed базы `catalog` | `migrations/catalog/0001_init.sql`, `0002_seed.sql` | `IT: TestMigrationsAreIdempotent` | выполнено |
| A-09 | OpenAPI-спецификация в репозитории | Внешний контракт сервиса | `api/openapi/catalog.v1.yaml` | `SC`: пути и методы спецификации совпадают с роутером | выполнено |
| A-10 | Связать данные системы с данными заведений и пользователями | `menu_items.external_id` — идентификатор позиции в системе заведения; `merchant_id` — владелец | UNIQUE (`merchant_id`, `external_id`) | `IT: TestRepublishKeepsIdentityAndWithdrawsAbsentItems`, `SC` | выполнено |
| A-11 | Язык — Go | Go 1.24, без новых зависимостей | `go.mod` не изменён | `SC` | выполнено |
| A-12 | БД на выбор | PostgreSQL 18, отдельная логическая база `catalog` | `deploy/postgres/init/01-databases.sql` | `SC` | выполнено |
| A-13 | Запуск через Docker Compose | Блоки `catalog-migrate` и `catalog-service` | `docker-compose.yml`, `deploy/catalog-service.Dockerfile` | `make stack-check` (`docker compose up --build -d`, `make wait smoke e2e`), CI job `e2e` | выполнено |
| A-14 | Линтер и статический анализ | Правила границ для нового сервиса | `.golangci.yml`: `app-purity`, `service-isolation-catalog` | `make lint` (golangci-lint v2.5.0 и v2.14.0): 0 замечаний; CI job `lint` | выполнено |
| A-15 | Кодогенерация для API | Контракт `catalog.v1` уже сгенерирован в итерации 1; реализация ему соответствует | `gen/grpc/catalog/v1/*.pb.go` не изменялись | `SC`: все используемые символы найдены в сгенерированном коде | выполнено |
| A-17 | Приложение поднимается и выполняет бизнес-сценарии | Точка входа, конфигурация, завершение | `cmd/catalog-service/main.go` | smoke, `E2E` (`make e2e`) против запущенного стека | выполнено |
| A-19…A-21 | README: описание, архитектура, схема БД | Разделы «Внешний API catalog-service» и «Схема БД catalog» | `README.md` | `SC` | выполнено |
| A-22, A-23 | Упрощения и подход к решениям | Шесть новых строк в таблице решений README | `README.md` | `SC` | выполнено |
| A-05, A-06, A-16, A-18 | CJM, partner-pos, промпт, диаграммы | Относятся к системе целиком и к другим итерациям | — | — | н/п |

## Требования ТЗ версии 3.0

| ID | Источник | Требуемое поведение | Состояние до итерации | Сделано | Проверка | Статус |
|---|---|---|---|---|---|---|
| C-01 | §3.2 | Каталог владеет `menu_categories`, `menu_items`, `merchant_menu_state` и только ими | отсутствовало | три таблицы, отдельная база и роли | `IT: TestSchemaConstraints` | выполнено |
| C-02 | §1 п. 6, §5.3 | Каталог **не владеет** доступностью и остатками; публикация меню остатков не создаёт | отсутствовало | в схеме нет колонок количеств; публикация не обращается к `inventory` | `IT: TestCatalogueOwnsNoQuantity`, smoke «menu carries no quantity» | выполнено |
| C-03 | §6.2 | `menu_categories`: PK, UNIQUE (`merchant_id`, `name`) | отсутствовало | `0001_init.sql` | `IT` | выполнено |
| C-04 | §6.2 | `menu_items`: PK, UNIQUE (`merchant_id`, `external_id`), CHECK `price_minor > 0`, индекс `(merchant_id, category_id, position) WHERE NOT is_deleted` | отсутствовало | `0001_init.sql` | `IT: TestSchemaConstraints` | выполнено |
| C-05 | §6.2 | `merchant_menu_state`: PK (`merchant_id`), CHECK `menu_version > 0`; отдельная таблица | отсутствовало | `0001_init.sql` | `IT: TestSchemaConstraints` | выполнено |
| C-06 | §6.2 | Публикация — одна транзакция: инкремент версии при совпадении `If-Match`, upsert по (`merchant_id`, `external_id`), пометка отсутствующих `is_deleted` | отсутствовало | `MenuRepository.PublishSnapshot` | `IT: TestRepublishKeepsIdentityAndWithdrawsAbsentItems` | выполнено |
| C-07 | §6.2 | Позиции не удаляются физически: на них ссылаются снимки заказов и карточки остатков | отсутствовало | `is_deleted`, строки сохраняются | `IT`: строки на месте после снятия с продажи | выполнено |
| C-08 | §4.2 S-M2 | Заведение публикует меню целым снимком с `If-Match: menu_version` и уникальностью `external_id` | отсутствовало | `PUT /api/v1/merchant/menu` | `HT: TestPublishReplacesTheMenu`, `TestPublishWithAStaleIfMatchIs412`, smoke | выполнено |
| C-09 | §7.2 | Четыре внешних эндпоинта каталога, порт 8082 | отсутствовало | роутер `httpapi` | `HT`, `SC`: сверка с OpenAPI | выполнено |
| C-10 | §7.3 | `catalog.v1.QuoteItems`: deadline 500 мс на стороне вызывающего, идемпотентно (чтение) | контракт зафиксирован | `grpcapi.Server.QuoteItems` | `CT: TestQuoteItems*` | выполнено |
| C-11 | §7.3 | Проблемные позиции возвращаются кодом проблемы, а не ошибкой вызова | контракт зафиксирован | `ItemProblem` для всех строк | `CT: TestQuoteItemsReportsProblemsWithoutFailing` | выполнено |
| C-12 | §7.1, §7.3 | Единый конверт ошибок и отображение статусов: 422 `VALIDATION_FAILED`, 401 `UNAUTHORIZED`, 404, 412 `VERSION_CONFLICT`, 503 `DEPENDENCY_UNAVAILABLE`, 500 без деталей | общий `pkg/httpx` | `httpapi/errors.go`, `grpcapi/toStatusError` | `HT`, `CT`, smoke | выполнено |
| C-13 | §3.1, §14.3 | Каталог аутентифицирует заведение только через `merchant.v1.AuthenticateApiKey` | контракт зафиксирован | `grpcclient.MerchantDirectory` | `HT: TestUnknownKeyIs401AndOutageIs503` | выполнено |
| C-14 | §7.3 | Недоступность `merchant-service` — 503, а не 401 | отсутствовало | различение `ErrMerchantUnauthenticated` и `ErrMerchantDirectoryUnavailable` | `HT`, `UT: TestAuthenticatePropagatesADirectoryOutage` | выполнено |
| C-15 | §6 | Деньги — целые в копейках, валюта `RUB`; время `timestamptz` UTC, в API RFC 3339 | общее правило | `price_minor bigint`, `Currency = "RUB"`, форматирование RFC 3339 | `HT`, `IT` | выполнено |
| C-16 | §10 | Слои `domain → app → adapters`; домен без транспорта, БД и сгенерированных моделей | общее правило | дерево `internal/catalog` | `SC`: импорты домена — только stdlib и uuid; правила `depguard` добавлены | выполнено |
| C-17 | §11.1 | Никакого состояния в памяти процесса; идентификаторы генерирует приложение; индексы под фактические запросы; тайм-ауты и `context` по всей цепочке | общее правило | `uuid.New()` в репозитории, тайм-аут вызова merchant, `context` во всех портах | `SC`, `UT` | выполнено |
| C-18 | §14.3 | Совместимость: имена gRPC-пакета и методов, модуль, адреса Compose, имена БД и ролей, конверт ошибок из `pkg/httpx` | зафиксировано | ничего из перечисленного не изменено | `SC`: diff общих файлов | выполнено |
| C-19 | §2.3 п. 4 | Миграции применяются с нуля и повторно без изменения состояния | механизм готов | `migrations/catalog` + `pgmigrate` | `IT: TestMigrationsAreIdempotent` | выполнено |
| C-20 | §2.3 п. 6 | `go build`, `go vet`, `gofmt`, `go test -race` без замечаний | — | код написан под эти проверки | `make check`: без замечаний (`docs/PROJECT-STATE.md` §5) | выполнено |

## Инварианты каталога

| ID | Инвариант | Механизм | Проверка | Статус |
|---|---|---|---|---|
| INV-C1 | Цена позиции строго больше нуля и не превышает 100 000 000 копеек | доменная валидация + `CHECK menu_items_price_chk` | `UT: TestSnapshotValidateRejectsBadItems`, `IT: TestSchemaConstraints` | выполнено |
| INV-C2 | `external_id` уникален в пределах заведения | доменная валидация снимка + UNIQUE | `UT: TestSnapshotValidateRejectsDuplicateExternalID`, `IT: TestSchemaConstraints` | выполнено |
| INV-C3 | `menu_item_id` не меняется между публикациями, пока жив `external_id` | `ON CONFLICT (merchant_id, external_id) DO UPDATE` | `IT: TestRepublishKeepsIdentityAndWithdrawsAbsentItems`, `TestWithdrawnItemIsRestoredWithTheSameIdentifier`, smoke | выполнено |
| INV-C4 | Позиция, отсутствующая в снимке, снимается с продажи, но не удаляется | `UPDATE … SET is_deleted = true` | `IT`, smoke | выполнено |
| INV-C5 | Публикация атомарна: отвергнутый снимок не меняет ничего | одна транзакция + валидация до записи | `IT: TestPublishWithAStaleVersionIsRejected`, `UT: TestPublishSnapshotRejectsAnInvalidMenuWithoutWriting` | выполнено |
| INV-C6 | Одновременную публикацию одной версии проходит ровно одна | `SELECT … FOR UPDATE` на `merchant_menu_state` + сверка `If-Match` | `IT: TestConcurrentPublishAllowsExactlyOne` | выполнено |
| INV-C7 | Изменение цены позиции увеличивает `menu_version` | инкремент в той же транзакции | `IT: TestUpdateItemChangesThePriceAndBumpsTheVersion`, `HT`, smoke | выполнено |
| INV-C8 | Категория меню содержит хотя бы одну позицию | доменная валидация | `UT: TestSnapshotValidateRejectsEmptyCategory`, smoke | выполнено |
| INV-C9 | Снятая с продажи позиция не изменяется и не возвращается в меню через `PATCH` | `WHERE … AND NOT is_deleted` | `IT: TestUpdateItemDoesNotResurrectAWithdrawnItem`, smoke | выполнено |
| INV-C10 | Позиция чужого заведения недоступна ни для чтения меню, ни для изменения, и в котировке отмечается `NOT_IN_MERCHANT` | фильтр по `merchant_id`; классификация в `quoteFor` | `IT: TestQuoteItemsClassifiesEveryLine`, `UT: TestUpdateMenuItemOfAnotherMerchantIsNotFound` | выполнено |

## Что сознательно не реализовано в этом сервисе

| Не сделано | Обоснование по источникам |
|---|---|
| Доступность и остатки в меню | §1 п. 6 и §3.2 ТЗ: этим владеет `inventory-service`; каталог, отдающий количества, стал бы вторым источником истины |
| Стоп-лист позиции | §5.3 ТЗ: `sales_state` — измерение остатка, а не каталога |
| Приём и жизненный цикл заказов | §3.2 ТЗ: принадлежит `order-service` (следующая итерация) |
| Список заведений, профиль, ключи | §3.2 ТЗ: принадлежит `merchant-service`; каталог обращается к нему только за аутентификацией |
| Физическое удаление позиций, версионирование истории меню | §6.2 ТЗ требует сохранности ссылок; истории версий меню задание не требует |
| Кэш меню, брокер, отдельный поисковый индекс | §2.2 Q-11: незапрошенная инфраструктура не вводится |
