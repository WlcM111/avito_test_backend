# Карта требований — inventory-service (итерация 2)

Источники: `docs/TZ-v3.0.md` (версия 3.0, SHA-256 `d08a39f8…`), `docs/PROJECT-STATE.md` §5,
зафиксированный контракт `api/proto/inventory/v1/inventory.proto`.

Статусы: **выполнено** — реализовано и подтверждено проверкой; **н/п** — неприменимо с обоснованием.

## Функциональные требования

| ID | Источник | Требуемое поведение | Состояние до итерации | Сделано | Проверка | Статус |
|---|---|---|---|---|---|---|
| R-01 | ТЗ §5.1 | Учётный объект — карточка `(merchant_id, menu_item_id)`; штуки однородны, не идентифицируются | отсутствовало | `domain.StockItem`, UNIQUE (`merchant_id`,`menu_item_id`) | `TestAvailableQty`, `TestSellableRequiresBothDimensions`, `IT: TestSetOnHandCreatesCardAndRecordsMovement` | выполнено |
| R-02 | ТЗ §5.2 | Величины `on_hand_qty`, `reserved_qty`, вычисляемое `available_qty`, `version` | отсутствовало | поля `stock_items`, `StockItem.AvailableQty()` | `TestAvailableQty` | выполнено |
| R-03 | ТЗ §5.3 | `sales_state` ∈ {ENABLED, STOPPED}, независимое от количеств измерение | отсутствовало | `domain.SalesState`, CHECK в схеме | `TestSalesStateValid` | выполнено |
| R-04 | ТЗ §5.3, INV-S8 | Перевод в `ENABLED`/`STOPPED` не изменяет количеств; карточка создаётся с `on_hand_qty = 0` | отсутствовало | отдельный `UPDATE` без колонок количеств | `TestSetSalesStateDoesNotTouchQuantities` (unit + IT) | выполнено |
| R-05 | ТЗ §5.4 | `SetOnHand` — абсолютная установка, `If-Match` по версии | отсутствовало | `PUT /api/v1/merchant/stock/{id}` | `IT: TestSetOnHand*`, smoke | выполнено |
| R-06 | ТЗ §5.4 | `AdjustOnHand` — относительное изменение, дедупликация по `operation_key` | отсутствовало | `POST .../adjust`, таблица `stock_operations` | `IT: TestAdjustOnHandIsDeduplicated` | выполнено |
| R-07 | ТЗ §5.4 | `SetSalesState` — идемпотентное присваивание | отсутствовало | `PATCH /api/v1/merchant/stock/{id}` | `IT: TestSetSalesState*` | выполнено |
| R-08 | ТЗ §5.4, §7.2 | `GetAvailability` по HTTP и gRPC | отсутствовало | `GET /api/v1/merchants/{id}/availability`, gRPC `GetAvailability` | `CT: TestGetAvailability`, smoke | выполнено |
| R-09 | ТЗ §5.4 | `Reserve` — удержание all-or-nothing, идемпотентно по `reservation_id` | отсутствовало | gRPC `Reserve`, транзакция с условными `UPDATE` | `IT: TestReserve*`, `CT` | выполнено |
| R-10 | ТЗ §5.4 | `CommitReservation` — списание удержанного, идемпотентно по `state='HELD'` | отсутствовало | gRPC `CommitReservation` | `IT: TestCommit*`, `CT` | выполнено |
| R-11 | ТЗ §5.4 | `ReleaseReservation` — возврат удержанного, идемпотентно | отсутствовало | gRPC `ReleaseReservation` | `IT: TestRelease*`, `CT` | выполнено |
| R-12 | ТЗ §7.2 | Журнал движений заведению | отсутствовало | `GET /api/v1/merchant/stock/{id}/movements` | `IT: TestMovementsJournal`, smoke | выполнено |
| R-13 | ТЗ §7.2 | Список своих карточек остатков | отсутствовало | `GET /api/v1/merchant/stock` | smoke, `IT` | выполнено |
| R-14 | ТЗ §5.6, §7.2 | Просроченный резерв освобождается фоновым sweeper'ом | отсутствовало | `internal/inventory/jobs/reservation_sweeper.go`, подключён в `main` | `IT: TestSweeper*` | выполнено |

## Инварианты

| ID | Требование (ТЗ §5.5) | Механизм | Проверка | Статус |
|---|---|---|---|---|
| INV-S1 | `on_hand_qty ≥ 0` | `CHECK stock_items_on_hand_chk` | `IT: TestCheckConstraintsProtectInvariants` | выполнено |
| INV-S2 | `reserved_qty ≥ 0` | `CHECK stock_items_reserved_chk` | `IT: TestCheckConstraintsProtectInvariants` | выполнено |
| INV-S3 | `reserved_qty ≤ on_hand_qty` | `CHECK stock_items_reserved_le_on_hand_chk` | `IT: TestCheckConstraintsProtectInvariants`, `TestSetOnHandVersionConflictAndBelowReserved` | выполнено |
| INV-S4 | Резерв только если по каждой строке `ENABLED` и `available_qty ≥ qty` | условный `UPDATE`, 0 строк ⇒ откат всей транзакции | `TestReserveIsAllOrNothing`, `IT: TestReserveHoldsAndReportsProblems`, `IT: TestReserveConcurrentLastUnits` | выполнено |
| INV-S5 | Резерв ровно один раз переходит в терминальное состояние | `UPDATE ... WHERE id=$1 AND state='HELD'` | `IT: TestCommitReleaseRace` | выполнено |
| INV-S6 | Один `reservation_id` — одно удержание | PK `stock_reservations` + `ON CONFLICT DO NOTHING` | `IT: TestReserveIsIdempotent` | выполнено |
| INV-S7 | Один `operation_key` применяется один раз | PK (`stock_item_id`,`operation_key`) | `IT: TestAdjustOnHandIsDeduplicated` | выполнено |
| INV-S8 | `SetSalesState` не изменяет количеств | отдельный `UPDATE` | unit + `IT` | выполнено |
| INV-S9 | Каждое изменение количеств отражено в журнале | вставка `stock_movements` в той же транзакции | `IT: TestSetOnHandCreatesCardAndRecordsMovement`, `IT: TestSetOnHandJournalStaysExactUnderAConcurrentCommit` | выполнено |
| INV-S10 | `qty > 0` в строке резерва | `CHECK stock_reservation_lines_qty_chk` | `IT: TestCheckConstraintsProtectInvariants` | выполнено |

## Конкурентность и повторы (ТЗ §5.6)

| Ситуация | Реализация | Проверка | Статус |
|---|---|---|---|
| Двое резервируют последнюю штуку | условный `UPDATE`, отказ `INSUFFICIENT_UNITS` с фактическим `available_qty` | `TestReserveConcurrentLastUnits` (16 горутин) | выполнено |
| Многопозиционный заказ, deadlock | строки захватываются в порядке возрастания `menu_item_id` | `TestReserveNoDeadlockOnOppositeOrder` | выполнено |
| Повтор `Reserve` | количества не удерживаются повторно, `already_existed = true` | `TestReserveIsIdempotent` | выполнено |
| Одновременные `Commit` и `Release` | ровно один проходит, второй — `FAILED_PRECONDITION` | `TestCommitReleaseRace` | выполнено |
| Повтор `Commit` | `already_committed = true`, успех | `TestCommitAndReleaseAreIdempotent` | выполнено |
| Повтор `AdjustOnHand` | ничего не меняет, возвращает текущую карточку с `duplicate = true`; остаток меняется один раз | `TestAdjustOnHandIsDeduplicated` | выполнено |
| Корректировка или установка одновременно со списанием или другой корректировкой | карточка блокируется `SELECT … FOR UPDATE`, новое значение вычисляется из заблокированной строки (аудит, `docs/PROJECT-STATE.md` §2) | `IT: TestAdjustOnHandConcurrentDeltasAreAllApplied`, `IT: TestMerchantWritesDoNotUndoAConcurrentCommit`, `IT: TestSetOnHandJournalStaysExactUnderAConcurrentCommit` | выполнено |
| Отказ резерва при удержанных ранее строках той же транзакции | удержанные строки возвращаются в расчёт доступности и не помечаются нехваткой | `IT: TestReserveRejectionCountsBackTheHeldLines` | выполнено |
| `on_hand_qty` ниже `reserved_qty` | 409 `BELOW_RESERVED`, без клампинга | `TestSetOnHandVersionConflictAndBelowReserved` | выполнено |
| `STOPPED` при активных резервах | разрешено, удержанное остаётся | `TestSetSalesStateDoesNotTouchQuantities` | выполнено |
| Гонка sweeper и `Commit` | оба по условию `state='HELD'`, выигрывает первый | `TestSweeperCommitRace` | выполнено |

## Нефункциональные требования

| ID | Источник | Требование | Реализация | Статус |
|---|---|---|---|---|
| N-01 | ТЗ §2.2 Q-01/Q-02 | Go 1.24, PostgreSQL 18 | `go.mod`, образ `postgres:18.6-alpine` | выполнено |
| N-02 | ТЗ §2.2 Q-04 | Межсервисное взаимодействие только gRPC | клиент `merchant.v1` через gRPC, HTTP-клиентов к сервисам нет | выполнено |
| N-03 | ТЗ §2.2 Q-05, §10 | Слои `domain → app → adapters`, проверка `depguard` | правила расширены на `internal/inventory` | выполнено |
| N-04 | ТЗ §2.2 Q-06, §6 | Своя логическая БД и пара ролей, нет доступа к чужим | `inventory`, роли `inventory_svc`/`inventory_migrator` | выполнено |
| N-05 | ТЗ §7.1 | Общие правила внешнего API: конверт ошибок, пагинация, `X-Request-Id`, `ETag`/`If-Match` | `pkg/httpx` без изменений семантики | выполнено |
| N-06 | ТЗ §7.3 | Контракт `inventory.v1` реализован без изменений | `internal/inventory/adapters/grpcapi` | выполнено |
| N-07 | ТЗ §11.1 | Отсутствие состояния в памяти процесса, ограниченный пул, тайм-ауты, `context` | реализовано; sweeper — единственный фоновый процесс, безопасен при нескольких экземплярах (`FOR UPDATE SKIP LOCKED`) | выполнено |
| N-08 | ТЗ §13, §14 | Корректная остановка, `/healthz`, `/readyz` | `cmd/inventory-service/main.go` | выполнено |
| N-09 | ТЗ §2.3 | Миграции с нуля и повторно | `migrations/inventory` + `pkg/pgmigrate` | выполнено |
| N-10 | Запрос §13 | Секретов в коде и логах нет | dev-ключи только в seed, помечены; `logx.RedactDSN` | выполнено |

## Неприменимо

| Пункт | Обоснование |
|---|---|
| Брокер сообщений, outbox, DLQ (запрос §12) | ТЗ §8.5: асинхронный обмен в системе один и принадлежит `order-service`. `inventory-service` — приёмник gRPC-команд и владелец одного фонового задания; публикации сообщений у него нет |
| Аутентификация покупателя | ТЗ §2.1 A-02: прямо исключена заданием; публичный эндпоинт доступности не аутентифицируется |
| Численные требования по нагрузке (запрос §13) | ТЗ §11: числовых обязательств нет, есть расчётный ориентир; нагрузочный прогон отнесён к финальной итерации (ТЗ §11.3) |
