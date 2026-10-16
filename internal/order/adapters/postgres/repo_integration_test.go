package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pgstore "github.com/avito-kitchen/platform/internal/order/adapters/postgres"
	"github.com/avito-kitchen/platform/internal/order/app"
	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/avito-kitchen/platform/migrations"
	"github.com/avito-kitchen/platform/pkg/pgxx"
	"github.com/avito-kitchen/platform/test/testenv"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Identifiers shared with the seed migrations of the other services.
var (
	merchantID = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	customerID = uuid.MustParse("cccccccc-1111-4111-8111-111111111111")
	itemA      = uuid.MustParse("a1111111-1111-4111-8111-111111111111")
	itemB      = uuid.MustParse("a1111111-1111-4111-8111-222222222222")
)

type env struct {
	pool   *pgxpool.Pool
	orders *pgstore.OrderRepository
	outbox *pgstore.OutboxRepository
}

func newEnv(t *testing.T) env {
	t.Helper()
	pool := testenv.RequireDB(t, migrations.DirOrders)
	tx := pgxx.NewTxManager(pool)
	return env{pool: pool, orders: pgstore.NewOrderRepository(tx), outbox: pgstore.NewOutboxRepository(tx)}
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func newOrder(key string, items ...domain.Item) domain.Order {
	var itemsTotal int64
	for _, item := range items {
		itemsTotal += item.LineTotalMinor
	}
	const deliveryFee int64 = 19900
	return domain.Order{
		ID:               uuid.New(),
		MerchantID:       merchantID,
		MerchantName:     "Кафе «Пример»",
		CustomerID:       customerID,
		IdempotencyKey:   key,
		CustomerName:     "Иван",
		CustomerPhone:    "+79990000001",
		DeliveryAddress:  "Невский проспект, 1",
		Status:           domain.StatusCreated,
		Items:            items,
		ItemsTotalMinor:  itemsTotal,
		DeliveryFeeMinor: deliveryFee,
		TotalMinor:       itemsTotal + deliveryFee,
		Currency:         domain.Currency,
		ETAMinutes:       35,
	}
}

func item(id uuid.UUID, external string, price int64, qty int32) domain.Item {
	return domain.Item{
		MenuItemID: id, ExternalID: external, Name: "Позиция",
		UnitPriceMinor: price, Qty: qty, LineTotalMinor: price * int64(qty),
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	before, err := testenv.MigrationVersion(ctx, e.pool)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if err := testenv.ApplyAgain(ctx, e.pool, migrations.DirOrders); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	after, err := testenv.MigrationVersion(ctx, e.pool)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if before != after {
		t.Fatalf("migration version changed from %d to %d on a repeated apply", before, after)
	}
}

func TestCreateStoresTheOrderAndItsPositions(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	order := newOrder("key-1", item(itemA, "POS-001", 42000, 2), item(itemB, "POS-002", 56000, 1))
	stored, duplicate, err := e.orders.Create(ctx, order)
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if duplicate {
		t.Fatal("the first placement is not a duplicate")
	}
	if stored.CreatedAt.IsZero() || stored.Status != domain.StatusCreated {
		t.Fatalf("stored = %+v", stored)
	}

	read, err := e.orders.GetByID(ctx, order.ID)
	if err != nil {
		t.Fatalf("GetByID() = %v", err)
	}
	if len(read.Items) != 2 {
		t.Fatalf("got %d positions, want 2", len(read.Items))
	}
	if read.TotalMinor != read.ItemsTotalMinor+read.DeliveryFeeMinor {
		t.Fatalf("totals do not add up: %+v", read)
	}
}

// The unique index is the authority for idempotency: a second insert with the
// same key must return the first order, not a second one.
func TestCreateIsIdempotentPerCustomer(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	first, _, err := e.orders.Create(ctx, newOrder("key-1", item(itemA, "POS-001", 42000, 1)))
	if err != nil {
		t.Fatalf("first Create() = %v", err)
	}
	second, duplicate, err := e.orders.Create(ctx, newOrder("key-1", item(itemA, "POS-001", 42000, 5)))
	if err != nil {
		t.Fatalf("second Create() = %v", err)
	}
	if !duplicate || second.ID != first.ID {
		t.Fatalf("a repeat must return the first order: %+v", second)
	}
	if len(second.Items) != 1 || second.Items[0].Qty != 1 {
		t.Fatalf("the stored order must be unchanged: %+v", second.Items)
	}

	// The same key of another customer is a different order.
	other := newOrder("key-1", item(itemA, "POS-001", 42000, 1))
	other.CustomerID = uuid.New()
	third, duplicate, err := e.orders.Create(ctx, other)
	if err != nil || duplicate || third.ID == first.ID {
		t.Fatalf("the key of another customer must create a new order: %+v, %v, %v",
			third, duplicate, err)
	}
}

// Two simultaneous submits of the same key take the same index entry; exactly
// one of them creates the order.
func TestConcurrentCreateWithTheSameKey(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	const attempts = 4
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		created   int
		duplicate int
		ids       = map[uuid.UUID]struct{}{}
		other     []error
	)
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			stored, dup, err := e.orders.Create(ctx, newOrder("race", item(itemA, "POS-001", 42000, 1)))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				other = append(other, err)
			case dup:
				duplicate++
				ids[stored.ID] = struct{}{}
			default:
				created++
				ids[stored.ID] = struct{}{}
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(other) > 0 {
		t.Fatalf("unexpected errors: %v", other)
	}
	if created != 1 || duplicate != attempts-1 {
		t.Fatalf("created = %d, duplicate = %d, want 1 and %d", created, duplicate, attempts-1)
	}
	if len(ids) != 1 {
		t.Fatalf("every caller must see the same order, got %d distinct ids", len(ids))
	}
}

func TestGetByIDAndFindByKey(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	order := newOrder("key-1", item(itemA, "POS-001", 42000, 2))
	if _, _, err := e.orders.Create(ctx, order); err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if _, err := e.orders.GetByID(ctx, uuid.New()); !errors.Is(err, domain.ErrOrderNotFound) {
		t.Fatalf("GetByID(unknown) = %v, want ErrOrderNotFound", err)
	}
	found, err := e.orders.FindByIdempotencyKey(ctx, customerID, "key-1")
	if err != nil || found.ID != order.ID || len(found.Items) != 1 {
		t.Fatalf("FindByIdempotencyKey() = %+v, %v", found, err)
	}
	if _, err := e.orders.FindByIdempotencyKey(ctx, customerID, "nope"); !errors.Is(err, domain.ErrOrderNotFound) {
		t.Fatalf("FindByIdempotencyKey(unknown) = %v, want ErrOrderNotFound", err)
	}
}

func TestListsAreScopedOrderedAndPaginated(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	var ids []uuid.UUID
	for i := 0; i < 3; i++ {
		order := newOrder("key-"+uuid.NewString(), item(itemA, "POS-001", 42000, 1))
		stored, _, err := e.orders.Create(ctx, order)
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}
		ids = append(ids, stored.ID)
		time.Sleep(2 * time.Millisecond)
	}
	// An order of another merchant and another customer must not leak in.
	foreign := newOrder("key-foreign", item(itemA, "POS-001", 42000, 1))
	foreign.MerchantID = uuid.New()
	foreign.CustomerID = uuid.New()
	if _, _, err := e.orders.Create(ctx, foreign); err != nil {
		t.Fatalf("Create(foreign) = %v", err)
	}

	history, err := e.orders.ListByCustomer(ctx, customerID, 2, 0)
	if err != nil {
		t.Fatalf("ListByCustomer() = %v", err)
	}
	if history.Total != 3 || len(history.Items) != 2 {
		t.Fatalf("history = %+v", history)
	}
	// Newest first for the customer.
	if history.Items[0].ID != ids[2] {
		t.Fatalf("the history must start with the newest order")
	}
	if len(history.Items[0].Items) != 1 {
		t.Fatal("a listed order must carry its positions")
	}

	queue, err := e.orders.ListByMerchant(ctx, merchantID, nil, 20, 0)
	if err != nil {
		t.Fatalf("ListByMerchant() = %v", err)
	}
	if queue.Total != 3 {
		t.Fatalf("queue total = %d, want 3", queue.Total)
	}
	// Oldest first for the merchant queue.
	if queue.Items[0].ID != ids[0] {
		t.Fatal("the queue must start with the oldest order")
	}

	filtered, err := e.orders.ListByMerchant(ctx, merchantID,
		[]domain.Status{domain.StatusConfirmed}, 20, 0)
	if err != nil {
		t.Fatalf("ListByMerchant(filtered) = %v", err)
	}
	if filtered.Total != 0 {
		t.Fatalf("no order is confirmed yet, got %d", filtered.Total)
	}

	// A page past the end has no rows, yet both lists must still report the
	// total, with the status filter applied.
	pastHistory, err := e.orders.ListByCustomer(ctx, customerID, 2, 10)
	if err != nil {
		t.Fatalf("ListByCustomer(past the end) = %v", err)
	}
	if len(pastHistory.Items) != 0 || pastHistory.Total != 3 {
		t.Fatalf("history past the end = %+v, want no items and total 3", pastHistory)
	}
	pastQueue, err := e.orders.ListByMerchant(ctx, merchantID,
		[]domain.Status{domain.StatusCreated}, 2, 10)
	if err != nil {
		t.Fatalf("ListByMerchant(past the end) = %v", err)
	}
	if len(pastQueue.Items) != 0 || pastQueue.Total != 3 {
		t.Fatalf("queue past the end = %+v, want no items and total 3", pastQueue)
	}
}

// The status change and the outbox message are one transaction: a decision
// cannot be recorded without its delivery being scheduled.
func TestTransitionWritesTheOutboxMessageAtomically(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	order := newOrder("key-1", item(itemA, "POS-001", 42000, 2))
	if _, _, err := e.orders.Create(ctx, order); err != nil {
		t.Fatalf("Create() = %v", err)
	}
	message := domain.NewCommitMessage(order.ID, "req-1")
	result, err := e.orders.Transition(ctx, app.TransitionCommand{
		OrderID:          order.ID,
		ExpectMerchantID: &merchantID,
		Target:           domain.StatusConfirmed,
		ETAMinutes:       25,
		Message:          &message,
	})
	if err != nil {
		t.Fatalf("Transition() = %v", err)
	}
	if result.Order.Status != domain.StatusConfirmed || result.Order.ETAMinutes != 25 {
		t.Fatalf("order = %+v", result.Order)
	}
	if len(result.Order.Items) != 1 {
		t.Fatal("the transition result must carry the positions")
	}

	var (
		count   int
		payload string
		kind    string
	)
	if err := e.pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox_messages WHERE aggregate_id = $1`, order.ID).Scan(&count); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != 1 {
		t.Fatalf("outbox messages = %d, want 1", count)
	}
	if err := e.pool.QueryRow(ctx,
		`SELECT type, payload::text FROM outbox_messages WHERE aggregate_id = $1`,
		order.ID).Scan(&kind, &payload); err != nil {
		t.Fatalf("read message: %v", err)
	}
	if kind != string(domain.MessageCommitReservation) {
		t.Fatalf("type = %q", kind)
	}
	if payload == "" || payload == "{}" {
		t.Fatalf("payload = %q, want the reservation id", payload)
	}
}

func TestTransitionRefusesForbiddenMovesAndForeignOwners(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	order := newOrder("key-1", item(itemA, "POS-001", 42000, 2))
	if _, _, err := e.orders.Create(ctx, order); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	// Skipping the confirmation is refused.
	_, err := e.orders.Transition(ctx, app.TransitionCommand{
		OrderID: order.ID, ExpectMerchantID: &merchantID, Target: domain.StatusDelivered,
	})
	conflict, ok := domain.AsStateConflict(err)
	if !ok || conflict.Current != domain.StatusCreated {
		t.Fatalf("Transition(DELIVERED) = %v, want a state conflict", err)
	}

	// An order of another merchant is reported as absent.
	stranger := uuid.New()
	if _, err := e.orders.Transition(ctx, app.TransitionCommand{
		OrderID: order.ID, ExpectMerchantID: &stranger, Target: domain.StatusConfirmed,
	}); !errors.Is(err, domain.ErrOrderNotFound) {
		t.Fatalf("Transition(other merchant) = %v, want ErrOrderNotFound", err)
	}
	// ... and so is an order of another customer.
	if _, err := e.orders.Transition(ctx, app.TransitionCommand{
		OrderID: order.ID, ExpectCustomerID: &stranger, Target: domain.StatusCancelled,
	}); !errors.Is(err, domain.ErrOrderNotFound) {
		t.Fatalf("Transition(other customer) = %v, want ErrOrderNotFound", err)
	}
	if _, err := e.orders.Transition(ctx, app.TransitionCommand{
		OrderID: uuid.New(), Target: domain.StatusConfirmed,
	}); !errors.Is(err, domain.ErrOrderNotFound) {
		t.Fatalf("Transition(unknown order) = %v, want ErrOrderNotFound", err)
	}

	// A refused transition scheduled nothing.
	var count int
	if err := e.pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox_messages WHERE aggregate_id = $1`, order.ID).Scan(&count); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != 0 {
		t.Fatalf("outbox messages = %d, want 0", count)
	}
}

// Repeating a move that already happened changes nothing and must not schedule
// a second message; the row lock is what makes the check reliable.
func TestTransitionIsIdempotent(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	order := newOrder("key-1", item(itemA, "POS-001", 42000, 2))
	if _, _, err := e.orders.Create(ctx, order); err != nil {
		t.Fatalf("Create() = %v", err)
	}
	for i := 0; i < 2; i++ {
		message := domain.NewCommitMessage(order.ID, "req-1")
		result, err := e.orders.Transition(ctx, app.TransitionCommand{
			OrderID: order.ID, ExpectMerchantID: &merchantID,
			Target: domain.StatusConfirmed, Message: &message,
		})
		if err != nil {
			t.Fatalf("Transition() = %v", err)
		}
		if i == 1 && !result.AlreadyApplied {
			t.Fatal("a repeat must report that nothing changed")
		}
	}
	var count int
	if err := e.pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox_messages WHERE aggregate_id = $1`, order.ID).Scan(&count); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != 1 {
		t.Fatalf("outbox messages = %d, want exactly one", count)
	}
}

// Two decisions arriving at the same moment must not both apply: the row lock
// serialises them and the loser sees a conflict.
func TestConcurrentDecisionsOnOneOrder(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	order := newOrder("key-1", item(itemA, "POS-001", 42000, 2))
	if _, _, err := e.orders.Create(ctx, order); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
		conflicts int
		other     []error
	)
	start := make(chan struct{})
	targets := []domain.Status{domain.StatusConfirmed, domain.StatusRejected, domain.StatusCancelled}
	for _, target := range targets {
		wg.Add(1)
		go func(target domain.Status) {
			defer wg.Done()
			<-start
			message, _ := domain.MessageForTransition(order.ID, target, "req")
			cmd := app.TransitionCommand{OrderID: order.ID, Target: target, Message: &message}
			if target == domain.StatusRejected {
				cmd.RejectReason = "нет продуктов"
			}
			_, err := e.orders.Transition(ctx, cmd)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, domain.ErrStateConflict):
				conflicts++
			default:
				other = append(other, err)
			}
		}(target)
	}
	close(start)
	wg.Wait()

	if len(other) > 0 {
		t.Fatalf("unexpected errors: %v", other)
	}
	if succeeded != 1 || conflicts != len(targets)-1 {
		t.Fatalf("succeeded = %d, conflicts = %d, want 1 and %d",
			succeeded, conflicts, len(targets)-1)
	}
	var count int
	if err := e.pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox_messages WHERE aggregate_id = $1`, order.ID).Scan(&count); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != 1 {
		t.Fatalf("outbox messages = %d, want exactly one decision", count)
	}
}

// --------------------------------------------------------------- outbox

func scheduleMessage(ctx context.Context, t *testing.T, e env) (uuid.UUID, domain.Order) {
	t.Helper()
	order := newOrder("key-"+uuid.NewString(), item(itemA, "POS-001", 42000, 2))
	if _, _, err := e.orders.Create(ctx, order); err != nil {
		t.Fatalf("Create() = %v", err)
	}
	message := domain.NewReleaseMessage(order.ID, domain.ReleaseOrderCancelled, "req-1")
	if _, err := e.orders.Transition(ctx, app.TransitionCommand{
		OrderID: order.ID, Target: domain.StatusCancelled, Message: &message,
	}); err != nil {
		t.Fatalf("Transition() = %v", err)
	}
	return message.ID, order
}

func TestClaimReadsThePayloadAndCountsTheAttempt(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)
	messageID, order := scheduleMessage(ctx, t, e)

	claimed, err := e.outbox.Claim(ctx, time.Now().UTC(), 10, time.Minute)
	if err != nil {
		t.Fatalf("Claim() = %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d messages, want 1", len(claimed))
	}
	msg := claimed[0]
	if msg.ID != messageID || msg.AggregateID != order.ID {
		t.Fatalf("message = %+v", msg)
	}
	if msg.Type != domain.MessageReleaseReservation || msg.Reason != domain.ReleaseOrderCancelled {
		t.Fatalf("the payload was not decoded: %+v", msg)
	}
	// The reservation identifier is the order identifier.
	if msg.ReservationID != order.ID {
		t.Fatalf("reservation id = %s, want %s", msg.ReservationID, order.ID)
	}
	if msg.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", msg.Attempts)
	}

	// The claim hides the message for the lease, so a second pass sees nothing.
	again, err := e.outbox.Claim(ctx, time.Now().UTC(), 10, time.Minute)
	if err != nil {
		t.Fatalf("second Claim() = %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("a leased message must not be claimed again: %+v", again)
	}
}

// Several dispatchers must divide the work rather than send the same decision.
func TestConcurrentClaimsDoNotOverlap(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)
	for i := 0; i < 6; i++ {
		scheduleMessage(ctx, t, e)
	}

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		seen  = map[uuid.UUID]int{}
		total int
	)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := e.outbox.Claim(ctx, time.Now().UTC(), 6, time.Minute)
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, msg := range claimed {
				seen[msg.ID]++
				total++
			}
		}()
	}
	wg.Wait()

	for id, times := range seen {
		if times != 1 {
			t.Fatalf("message %s was claimed %d times", id, times)
		}
	}
	if total != 6 {
		t.Fatalf("claimed %d messages in total, want 6", total)
	}
}

func TestMarkSentRescheduleAndFail(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)
	sentID, _ := scheduleMessage(ctx, t, e)
	retryID, _ := scheduleMessage(ctx, t, e)
	failedID, _ := scheduleMessage(ctx, t, e)

	if _, err := e.outbox.Claim(ctx, time.Now().UTC(), 10, time.Minute); err != nil {
		t.Fatalf("Claim() = %v", err)
	}
	if err := e.outbox.MarkSent(ctx, sentID); err != nil {
		t.Fatalf("MarkSent() = %v", err)
	}
	next := time.Now().UTC().Add(-time.Second)
	if err := e.outbox.Reschedule(ctx, retryID, next, "connection refused"); err != nil {
		t.Fatalf("Reschedule() = %v", err)
	}
	if err := e.outbox.MarkFailed(ctx, failedID, "invalid argument"); err != nil {
		t.Fatalf("MarkFailed() = %v", err)
	}

	states := map[uuid.UUID]string{}
	rows, err := e.pool.Query(ctx, `SELECT id, state, last_error FROM outbox_messages`)
	if err != nil {
		t.Fatalf("read states: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id        uuid.UUID
			state     string
			lastError string
		)
		if err := rows.Scan(&id, &state, &lastError); err != nil {
			t.Fatalf("scan: %v", err)
		}
		states[id] = state
		if id == retryID && lastError == "" {
			t.Fatal("a rescheduled message must record why it failed")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if states[sentID] != "SENT" || states[retryID] != "PENDING" || states[failedID] != "FAILED" {
		t.Fatalf("states = %+v", states)
	}

	// Only the rescheduled message comes back.
	claimed, err := e.outbox.Claim(ctx, time.Now().UTC(), 10, time.Minute)
	if err != nil {
		t.Fatalf("Claim() = %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != retryID {
		t.Fatalf("claimed = %+v, want only the rescheduled message", claimed)
	}
	if claimed[0].Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", claimed[0].Attempts)
	}

	pending, err := e.outbox.PendingCount(ctx)
	if err != nil {
		t.Fatalf("PendingCount() = %v", err)
	}
	if pending != 1 {
		t.Fatalf("pending = %d, want 1", pending)
	}
}

// A parked message must not be revived by a late acknowledgement.
func TestMarkSentDoesNotReviveAFailedMessage(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)
	messageID, _ := scheduleMessage(ctx, t, e)

	if err := e.outbox.MarkFailed(ctx, messageID, "parked"); err != nil {
		t.Fatalf("MarkFailed() = %v", err)
	}
	if err := e.outbox.MarkSent(ctx, messageID); err != nil {
		t.Fatalf("MarkSent() = %v", err)
	}
	var state string
	if err := e.pool.QueryRow(ctx,
		`SELECT state FROM outbox_messages WHERE id = $1`, messageID).Scan(&state); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state != "FAILED" {
		t.Fatalf("state = %q, want FAILED", state)
	}
}

// The schema is the last line of defence for the money invariants.
func TestSchemaConstraints(t *testing.T) {
	e := newEnv(t)
	ctx := testCtx(t)

	order := newOrder("key-1", item(itemA, "POS-001", 42000, 2))
	if _, _, err := e.orders.Create(ctx, order); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	_, err := e.pool.Exec(ctx,
		`UPDATE orders SET total_minor = total_minor + 1 WHERE id = $1`, order.ID)
	if !pgxx.IsCheckViolation(err) {
		t.Fatalf("breaking the total = %v, want a CHECK violation", err)
	}
	_, err = e.pool.Exec(ctx, `UPDATE orders SET status = 'SHIPPED' WHERE id = $1`, order.ID)
	if !pgxx.IsCheckViolation(err) {
		t.Fatalf("an unknown status = %v, want a CHECK violation", err)
	}
	_, err = e.pool.Exec(ctx,
		`INSERT INTO order_items (order_id, menu_item_id, external_id, name,
		                          unit_price_minor, qty, line_total_minor)
		 VALUES ($1, $2, 'POS-X', 'Позиция', 1000, 0, 0)`, order.ID, uuid.New())
	if !pgxx.IsCheckViolation(err) {
		t.Fatalf("a zero quantity = %v, want a CHECK violation", err)
	}
	_, err = e.pool.Exec(ctx,
		`INSERT INTO order_items (order_id, menu_item_id, external_id, name,
		                          unit_price_minor, qty, line_total_minor)
		 VALUES ($1, $2, 'POS-X', 'Позиция', 1000, 2, 1500)`, order.ID, uuid.New())
	if !pgxx.IsCheckViolation(err) {
		t.Fatalf("an inconsistent line total = %v, want a CHECK violation", err)
	}
	_, err = e.pool.Exec(ctx,
		`INSERT INTO order_items (order_id, menu_item_id, external_id, name,
		                          unit_price_minor, qty, line_total_minor)
		 VALUES ($1, $2, 'POS-DUP', 'Позиция', 1000, 1, 1000)`, order.ID, itemA)
	if !pgxx.IsUniqueViolation(err) {
		t.Fatalf("a duplicate position = %v, want a UNIQUE violation", err)
	}
	_, err = e.pool.Exec(ctx,
		`INSERT INTO order_items (order_id, menu_item_id, external_id, name,
		                          unit_price_minor, qty, line_total_minor)
		 VALUES ($1, $2, 'POS-X', 'Позиция', 1000, 1, 1000)`, uuid.New(), uuid.New())
	if !pgxx.IsForeignKeyViolation(err) {
		t.Fatalf("a position without an order = %v, want a FOREIGN KEY violation", err)
	}
	_, err = e.pool.Exec(ctx,
		`INSERT INTO outbox_messages (id, aggregate_id, type, payload)
		 VALUES ($1, $2, 'NOPE', '{}')`, uuid.New(), order.ID)
	if !pgxx.IsCheckViolation(err) {
		t.Fatalf("an unknown message type = %v, want a CHECK violation", err)
	}
}

var (
	_ app.OrderRepository  = (*pgstore.OrderRepository)(nil)
	_ app.OutboxRepository = (*pgstore.OutboxRepository)(nil)
)
