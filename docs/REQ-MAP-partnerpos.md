# Карта требований — partner-pos (итерация 5, завершение проекта)

Источники: `docs/TZ-v3.0.md` (версия 3.0) §3.3, §4.2, §7.2, §13, §14;
исходное задание Авито, пункт **A-06**; фактические публичные контракты
`api/openapi/{catalog,inventory,order}.v1.yaml`.

Статусы: **выполнено** — реализовано и подтверждено доступной проверкой;
**не подтверждено** — реализовано, но проверка недоступна в текущем окружении
(причина указана); **н/п** — неприменимо с обоснованием.

Проверки: `UT` — unit-тест, `HT` — тест HTTP-адаптера через `httptest`,
`smoke` — `scripts/smoke-pos.sh` против запущенного стека, `SC` — статическая
сверка.

## Требование задания Авито

| ID | Требование | Как выполнено | Проверка | Статус |
|---|---|---|---|---|
| A-06 | Пример одного заведения отдельным сервисом, запускаемым через Docker Compose | `cmd/partner-pos`, `internal/partnerpos/**`, `deploy/partner-pos.Dockerfile`, блок `partner-pos` в `docker-compose.yml` (порт 8085) | `SC`, `HT`, smoke-pos, `E2E` (`TestPartnerPosAnswersTheQueue`), `make stack-check`, CI job `e2e` | выполнено |
| A-09 | OpenAPI-спецификация в репозитории | `api/openapi/partner-pos.v1.yaml` | `SC`: пути и методы совпадают с роутером | выполнено |
| A-13 | Запуск через Docker Compose | Блок `partner-pos` зависит от готовности трёх сервисов, публикует меню при старте | `make stack-check`, CI job `e2e` | выполнено |
| A-14 | Линтер и статический анализ | Правило `partner-pos-is-external` в `.golangci.yml` запрещает импорт `internal/*` других сервисов и `gen/grpc` | `SC` | выполнено |
| A-17 | Приложение поднимается и выполняет бизнес-сценарии | Точка входа, конфигурация с валидацией, два фоновых цикла, корректное завершение | `UT`, smoke-pos, `E2E` | выполнено |
| A-19…A-21 | README | Раздел «partner-pos — эмулятор кассы заведения» | `README.md` | выполнено |

## Требования ТЗ версии 3.0

| ID | Источник | Требуемое поведение | Как выполнено | Проверка | Статус |
|---|---|---|---|---|---|
| P-01 | §3.3 | `partner-pos` — **внешняя** организация, а не пятый бизнес-сервис: нет базы, нет доступа во внутреннюю gRPC-сеть, аутентификация ключом заведения | Только HTTP-клиент трёх публичных API; `depguard` это проверяет; в Compose нет доступа к postgres | `SC` | выполнено |
| P-02 | §7.2 | Пять эндпоинтов управления: `GET /pos/menu`, `PATCH /pos/menu/{external_id}`, `POST /pos/publish`, `GET /pos/orders`, `PUT /pos/behavior`; порт 8085 | `internal/partnerpos/adapters/httpapi/router.go` | `SC`: сверка с OpenAPI, `HT`, smoke | выполнено |
| P-03 | §4.2 S-M2 | Касса публикует меню целым снимком с `If-Match: menu_version` | `platform.Client.PublishMenu` читает версию и отправляет её обратно | `UT`, smoke | выполнено |
| P-04 | §4.2 S-M3 | Касса устанавливает остаток в штуках абсолютным значением | `SetStock` → `PUT /api/v1/merchant/stock/{id}` | `UT: TestSyncStockNeedsAPublishedMenu`, smoke | выполнено |
| P-05 | §4.2 S-M5 | Стоп-лист — отдельный вызов, количеств не меняет | `SetSalesState` → `PATCH …/stock/{id}` | `UT: TestSyncStockCarriesTheStopListSeparately` | выполнено |
| P-06 | §4.2 S-M6 | Опрос очереди раз в 5 с | `ORDER_POLL_INTERVAL` по умолчанию `5s` | `SC`, `UT` | выполнено |
| P-06a | §4.2 S-M6 | Касса не забирает заказы, которыми управляет кто-то другой | Режим по умолчанию `MANUAL`; автоматические режимы включаются явно через `PUT /pos/behavior` | `UT: TestDefaultBehaviorIsManual`, `TestDecideInManualModeTouchesNothing` | выполнено |
| P-07 | §4.2 S-M7 | Подтверждение или отказ заказа | Режимы `AUTO_ACCEPT` и `AUTO_REJECT` | `UT: TestPollAcceptsNewOrders`, `TestPollRefusesWhenConfigured`, smoke | выполнено |
| P-08 | §4.2 S-M8 | Продвижение статуса приготовления и доставки | Имитация кухни: один шаг за `KITCHEN_STEP`, ладдер `CONFIRMED → COOKING → DELIVERING → DELIVERED` | `UT: TestPollWalksThePreparationLadderOneStepAtATime`, smoke | выполнено |
| P-09 | §5.3 | Публикация меню не создаёт остаток | Публикация и синхронизация — два отдельных шага; без публикации синхронизация отказывает | `UT: TestSyncStockNeedsAPublishedMenu` | выполнено |
| P-09a | §5.3, §4 | Демонстрационное меню кассы повторяет seed платформы, иначе синхронизация уничтожает сценарии «ноль штук» и «стоп-лист» | `DefaultMenu`: POS-001 10 шт, POS-002 0 шт, POS-003 5 шт и стоп-лист | `UT: TestDefaultMenuRepeatsThePlatformSeed` | выполнено |
| P-09b | §11.1 | Касса не откатывает изменения, сделанные через API платформы | Выталкиваются только собственные изменения кассы | `UT: TestSyncPassPushesNothingWithoutOwnChanges` | выполнено |
| P-09c | A-13, §2.3 п. 1 | Сбой платформы при старте не оставляет кассу неопубликованной | Фоновый цикл повторяет публикацию до успеха | `UT: TestRunStockSyncPublishesAndRecoversAfterAFailure` | выполнено |
| P-10 | §6, A-10 | Соответствие `external_id` ↔ `menu_item_id` берётся из опубликованного меню, а не угадывается | `PublishMenu` читает меню обратно и заполняет соответствие | `UT: TestPublishMenuLearnsThePlatformIdentifiers` | выполнено |
| P-11 | §11.1 | `context` во всех блокирующих вызовах, тайм-ауты, ограниченное число горутин, корректное завершение | `PLATFORM_TIMEOUT` на каждый вызов, четыре горутины в `main`, `RunStockSync`/`RunOrderPolling` завершаются по отмене | `UT: TestLoopsStopOnCancellation`, `SC` | выполнено |
| P-12 | §7.1 | Единый конверт ошибок и коды: 404, 409, 422, 503, 500 без деталей | `internal/partnerpos/adapters/httpapi/router.go`, `toHTTPError` | `HT`, smoke | выполнено |
| P-13 | §13.1 | Допустимое сокращение — «без имитации кухни». Сокращение **не применялось**: реализован полный вариант с имитацией | `KITCHEN_STEP`, флаг `kitchen` в `PUT /pos/behavior` | `UT`, `HT` | выполнено |
| P-14 | §2.3 п. 6 | `go build`, `go vet`, `gofmt`, `go test -race` без замечаний | Код написан под эти проверки | `make check`: без замечаний (`docs/PROJECT-STATE.md` §5) | выполнено |

## Сквозной сценарий, который закрывает эта итерация

| Шаг | Кто | Что происходит | Проверка |
|---|---|---|---|
| 1 | partner-pos → catalog | Касса публикует своё меню с `If-Match` | smoke: «publish the menu», «every position matched» |
| 2 | partner-pos → inventory | Касса выставляет остатки и стоп-лист | smoke: «units pushed by the till» |
| 3 | покупатель → order | Оформление заказа удерживает штуки | smoke: «customer places an order» |
| 4 | partner-pos → order | Касса видит заказ в очереди и отвечает по режиму | smoke: «order reached REJECTED» / «CONFIRMED» |
| 5 | order → inventory | Через outbox штуки возвращаются или списываются | smoke `scripts/smoke-order.sh` |
| 6 | partner-pos → order | Имитация кухни доводит заказ до `DELIVERED` | smoke: «order reached DELIVERED» |

## Что сознательно не реализовано

| Не сделано | Обоснование по источникам |
|---|---|
| База данных у кассы | §3.3: касса моделирует внешнюю организацию, всё долговременное живёт в платформе; после перезапуска она публикует меню заново и берёт очередь из платформы |
| gRPC-клиенты в кассе | §3.3: у неё нет доступа во внутреннюю сеть; правило `partner-pos-is-external` в `.golangci.yml` это фиксирует |
| Аутентификация её собственного API управления | Он управляет демонстрацией, не отдаёт данных платформы и работает в том же compose-стенде; ключ заведения из процесса не выходит |
| Нагрузочный прогон `test/load` | §13.1: сокращается при отставании; обязательные критерии §2.3 его не содержат |