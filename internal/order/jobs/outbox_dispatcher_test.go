package jobs_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/internal/order/app"
	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/avito-kitchen/platform/internal/order/jobs"
	"github.com/avito-kitchen/platform/internal/order/testsupport"
	"github.com/google/uuid"
)

type env struct {
	svc        *app.Service
	orders     *testsupport.Orders
	ledger     *testsupport.Ledger
	dispatcher *jobs.OutboxDispatcher
}

func newEnv(t *testing.T, interval time.Duration) env {
	t.Helper()
	orders := testsupport.NewOrders()
	ledger := testsupport.NewLedger()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := app.NewService(orders, orders, testsupport.NewDirectory(),
		testsupport.NewCatalogue(), ledger, logger, app.Config{
			ReservationTTL: 30 * time.Minute, MaxDeliveryAttempts: 3,
		})
	return env{
		svc: svc, orders: orders, ledger: ledger,
		dispatcher: jobs.NewOutboxDispatcher(svc, logger, interval, 10, time.Minute, 5*time.Second),
	}
}

// placeAndConfirm produces one order whose confirmation is waiting in the
// outbox.
func placeAndConfirm(t *testing.T, e env) uuid.UUID {
	t.Helper()
	created, err := e.svc.CreateOrder(context.Background(), domain.Draft{
		MerchantID:      testsupport.MerchantID,
		CustomerID:      testsupport.CustomerID,
		Lines:           []domain.Line{{MenuItemID: testsupport.ItemSellable, Qty: 2}},
		CustomerName:    "Иван",
		CustomerPhone:   "+79990000001",
		DeliveryAddress: "Невский проспект, 1",
	}, "key-"+uuid.NewString())
	if err != nil {
		t.Fatalf("CreateOrder() = %v", err)
	}
	if _, err := e.svc.AcceptOrder(context.Background(),
		testsupport.MerchantID, created.Order.ID, 20); err != nil {
		t.Fatalf("AcceptOrder() = %v", err)
	}
	return created.Order.ID
}

func TestDispatchDeliversAPendingDecision(t *testing.T) {
	t.Parallel()
	e := newEnv(t, time.Hour)
	orderID := placeAndConfirm(t, e)

	stats, err := e.dispatcher.Dispatch(context.Background())
	if err != nil {
		t.Fatalf("Dispatch() = %v", err)
	}
	if stats.Sent != 1 {
		t.Fatalf("stats = %+v, want one delivered message", stats)
	}
	if state, _ := e.ledger.HoldState(orderID); state != "COMMITTED" {
		t.Fatalf("hold state = %q, want COMMITTED", state)
	}
	if e.orders.MessagesOf(orderID)[0].State != domain.MessageSent {
		t.Fatal("the message must be marked as sent")
	}
}

// The worker must keep running after a failed pass: the work is idempotent,
// and a transient failure of the database must not stop delivery for good.
func TestRunKeepsWorkingAfterAFailure(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 10*time.Millisecond)
	orderID := placeAndConfirm(t, e)
	e.ledger.SetCommitErr(app.Unavailable("inventory-service", errors.New("down")))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.dispatcher.Run(ctx) }()

	// Wait until the first attempt has been recorded, then let the dependency
	// recover and wait for the delivery. The waits have an explicit condition
	// and a bound; they do not rely on a sleep being long enough.
	waitFor(t, time.Second, func() bool {
		return e.orders.MessagesOf(orderID)[0].Attempts > 0
	}, "the dispatcher never attempted the message")

	e.ledger.SetCommitErr(nil)
	waitFor(t, 5*time.Second, func() bool {
		state, _ := e.ledger.HoldState(orderID)
		return state == "COMMITTED"
	}, "the decision never reached the ledger")

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	if e.ledger.Commits() != 1 {
		t.Fatalf("commits = %d, want exactly one business effect", e.ledger.Commits())
	}
}

func TestRunStopsOnCancellation(t *testing.T) {
	t.Parallel()
	e := newEnv(t, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.dispatcher.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

// waitFor polls a condition until it holds or the deadline passes. A test that
// slept a fixed amount instead would be either slow or flaky.
func waitFor(t *testing.T, limit time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}
