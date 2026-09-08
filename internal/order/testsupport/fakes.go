// Package testsupport provides in-memory implementations of the storage and
// dependency ports of order-service. They exist so that the use-case and
// transport layers can be tested without a database and without three running
// services. They are used by tests only; the SQL behaviour and the gRPC
// integration themselves are covered by the integration and end-to-end tests.
package testsupport

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/avito-kitchen/platform/internal/order/app"
	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/google/uuid"
)

// Fixed identifiers shared with the seed migrations of the other services, so
// that a test here describes the same world as the local stack.
var (
	// MerchantID is the demonstration merchant "Кафе «Пример»".
	MerchantID = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	// OtherMerchantID is a second outlet, used to prove isolation.
	OtherMerchantID = uuid.MustParse("22222222-2222-4222-8222-222222222222")
	// CustomerID is the demonstration customer.
	CustomerID = uuid.MustParse("cccccccc-1111-4111-8111-111111111111")
	// OtherCustomerID is a second customer, used to prove isolation.
	OtherCustomerID = uuid.MustParse("cccccccc-2222-4222-8222-222222222222")
	// ItemSellable is ENABLED with units on hand.
	ItemSellable = uuid.MustParse("a1111111-1111-4111-8111-111111111111")
	// ItemEnabledEmpty is ENABLED with zero units.
	ItemEnabledEmpty = uuid.MustParse("a1111111-1111-4111-8111-222222222222")
	// ItemStopped is on the stop list although units are on hand.
	ItemStopped = uuid.MustParse("a1111111-1111-4111-8111-333333333333")
	// ItemUnknown belongs to no catalogue.
	ItemUnknown = uuid.MustParse("a1111111-1111-4111-8111-999999999999")
)

// Prices and commercial terms of the demonstration outlet, matching its seed.
const (
	PriceSellable     int64 = 42000
	PriceEnabledEmpty int64 = 56000
	PriceStopped      int64 = 15000
	MinOrderMinor     int64 = 50000
	DeliveryFeeMinor  int64 = 19900
	AvgPrepMinutes    int32 = 35
	// DevKey is the development access key of the demonstration merchant.
	DevKey = "dev-merchant-key-kafe-primer-0001"
)

// Directory is an in-memory MerchantDirectory.
type Directory struct {
	mu       sync.Mutex
	Keys     map[string]app.MerchantIdentity
	Profiles map[uuid.UUID]app.MerchantProfile
	// authErr and profileErr let a test simulate an outage. They are guarded
	// by the mutex because a test may set them while a worker is running.
	authErr    error
	profileErr error
}

// NewDirectory returns a directory holding the demonstration outlet.
func NewDirectory() *Directory {
	return &Directory{
		Keys: map[string]app.MerchantIdentity{
			DevKey: {MerchantID: MerchantID, Active: true},
		},
		Profiles: map[uuid.UUID]app.MerchantProfile{
			MerchantID: {
				ID: MerchantID, Name: "Кафе «Пример»", Active: true,
				IsAcceptingOrders: true, MinOrderMinor: MinOrderMinor,
				DeliveryFeeMinor: DeliveryFeeMinor, AvgPrepMinutes: AvgPrepMinutes,
			},
		},
	}
}

// SetAuthErr makes Authenticate fail with err.
func (d *Directory) SetAuthErr(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.authErr = err
}

// SetProfileErr makes GetMerchant fail with err.
func (d *Directory) SetProfileErr(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.profileErr = err
}

// AddKey registers an access key.
func (d *Directory) AddKey(key string, identity app.MerchantIdentity) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Keys[key] = identity
}

// Profile returns the stored profile of a merchant.
func (d *Directory) Profile(merchantID uuid.UUID) app.MerchantProfile {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.Profiles[merchantID]
}

// SetProfile replaces the stored profile of a merchant.
func (d *Directory) SetProfile(profile app.MerchantProfile) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Profiles[profile.ID] = profile
}

// Authenticate resolves an access key.
func (d *Directory) Authenticate(_ context.Context, apiKey string) (app.MerchantIdentity, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.authErr != nil {
		return app.MerchantIdentity{}, d.authErr
	}
	identity, ok := d.Keys[apiKey]
	if !ok {
		return app.MerchantIdentity{}, app.ErrMerchantUnauthenticated
	}
	return identity, nil
}

// GetMerchant returns a commercial profile.
func (d *Directory) GetMerchant(_ context.Context, merchantID uuid.UUID) (app.MerchantProfile, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.profileErr != nil {
		return app.MerchantProfile{}, d.profileErr
	}
	profile, ok := d.Profiles[merchantID]
	if !ok {
		return app.MerchantProfile{}, app.ErrMerchantNotFound
	}
	return profile, nil
}

// Catalogue is an in-memory MenuCatalogue.
type Catalogue struct {
	mu          sync.Mutex
	MenuVersion int64
	Items       map[uuid.UUID]app.QuotedItem
	Owner       map[uuid.UUID]uuid.UUID
	err         error
}

// SetErr makes QuoteItems fail with err.
func (c *Catalogue) SetErr(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

// NewCatalogue returns a catalogue holding the three demonstration positions.
func NewCatalogue() *Catalogue {
	return &Catalogue{
		MenuVersion: 1,
		Items: map[uuid.UUID]app.QuotedItem{
			ItemSellable: {
				MenuItemID: ItemSellable, ExternalID: "POS-001",
				Name: "Борщ с говядиной", UnitPriceMinor: PriceSellable,
				Problem: domain.ItemProblemNone,
			},
			ItemEnabledEmpty: {
				MenuItemID: ItemEnabledEmpty, ExternalID: "POS-002",
				Name: "Котлета по-киевски", UnitPriceMinor: PriceEnabledEmpty,
				Problem: domain.ItemProblemNone,
			},
			ItemStopped: {
				MenuItemID: ItemStopped, ExternalID: "POS-003",
				Name: "Морс клюквенный", UnitPriceMinor: PriceStopped,
				Problem: domain.ItemProblemNone,
			},
		},
		Owner: map[uuid.UUID]uuid.UUID{
			ItemSellable: MerchantID, ItemEnabledEmpty: MerchantID, ItemStopped: MerchantID,
		},
	}
}

// SetPrice changes the price of one position, as a merchant would.
func (c *Catalogue) SetPrice(menuItemID uuid.UUID, priceMinor int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	item := c.Items[menuItemID]
	item.UnitPriceMinor = priceMinor
	c.Items[menuItemID] = item
	c.MenuVersion++
}

// QuoteItems resolves positions to their names and prices.
func (c *Catalogue) QuoteItems(_ context.Context, merchantID uuid.UUID,
	menuItemIDs []uuid.UUID) (app.Quote, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return app.Quote{}, c.err
	}
	out := app.Quote{MenuVersion: c.MenuVersion, Items: make([]app.QuotedItem, 0, len(menuItemIDs))}
	for _, id := range menuItemIDs {
		item, ok := c.Items[id]
		switch {
		case !ok:
			out.Items = append(out.Items, app.QuotedItem{
				MenuItemID: id, Problem: domain.ItemProblemNotFound,
			})
		case c.Owner[id] != merchantID:
			out.Items = append(out.Items, app.QuotedItem{
				MenuItemID: id, Problem: domain.ItemProblemNotInMerchant,
			})
		default:
			out.Items = append(out.Items, item)
		}
	}
	return out, nil
}

// stockCard is the in-memory equivalent of one stock card.
type stockCard struct {
	tracked  bool
	stopped  bool
	onHand   int32
	reserved int32
}

type hold struct {
	merchantID uuid.UUID
	lines      []domain.Line
	state      string
}

// Ledger is an in-memory StockLedger that respects the accounting rules of
// inventory-service: units are held, consumed and returned, and a hold is
// idempotent by its identifier.
type Ledger struct {
	mu    sync.Mutex
	cards map[uuid.UUID]*stockCard
	holds map[uuid.UUID]*hold
	// reserveErr, commitErr and releaseErr let a test simulate a dependency
	// that refuses or is unreachable. They are guarded by the mutex because a
	// test may set them while the dispatcher is running.
	reserveErr error
	commitErr  error
	releaseErr error
	// commits and releases count the delivered decisions, so a test can prove
	// that a repeat did not double the effect.
	commits  int
	releases int
}

// SetReserveErr makes Reserve fail with err.
func (l *Ledger) SetReserveErr(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reserveErr = err
}

// SetCommitErr makes Commit fail with err.
func (l *Ledger) SetCommitErr(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.commitErr = err
}

// SetReleaseErr makes Release fail with err.
func (l *Ledger) SetReleaseErr(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseErr = err
}

// Commits reports how many holds were consumed.
func (l *Ledger) Commits() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.commits
}

// Releases reports how many holds were returned.
func (l *Ledger) Releases() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.releases
}

// NewLedger returns a ledger seeded like migrations/inventory/0002_seed.sql.
func NewLedger() *Ledger {
	return &Ledger{
		cards: map[uuid.UUID]*stockCard{
			ItemSellable:     {tracked: true, onHand: 10},
			ItemEnabledEmpty: {tracked: true, onHand: 0},
			ItemStopped:      {tracked: true, stopped: true, onHand: 5},
		},
		holds: map[uuid.UUID]*hold{},
	}
}

// SetOnHand replaces the units of one card, as a merchant would.
func (l *Ledger) SetOnHand(menuItemID uuid.UUID, qty int32) {
	l.mu.Lock()
	defer l.mu.Unlock()
	card, ok := l.cards[menuItemID]
	if !ok {
		card = &stockCard{tracked: true}
		l.cards[menuItemID] = card
	}
	card.onHand = qty
}

// Snapshot returns the units of one card, for assertions.
func (l *Ledger) Snapshot(menuItemID uuid.UUID) (onHand, reserved int32, tracked bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	card, ok := l.cards[menuItemID]
	if !ok {
		return 0, 0, false
	}
	return card.onHand, card.reserved, true
}

// HoldState returns the state of one hold, for assertions.
func (l *Ledger) HoldState(reservationID uuid.UUID) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	h, ok := l.holds[reservationID]
	if !ok {
		return "", false
	}
	return h.state, true
}

// GetAvailability reports the units that can still be ordered.
func (l *Ledger) GetAvailability(_ context.Context, _ uuid.UUID,
	menuItemIDs []uuid.UUID) ([]app.Availability, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]app.Availability, 0, len(menuItemIDs))
	for _, id := range menuItemIDs {
		card, ok := l.cards[id]
		if !ok {
			out = append(out, app.Availability{MenuItemID: id})
			continue
		}
		out = append(out, app.Availability{
			MenuItemID:   id,
			Tracked:      card.tracked,
			SalesStopped: card.stopped,
			AvailableQty: card.onHand - card.reserved,
		})
	}
	return out, nil
}

// Reserve holds units all-or-nothing and is idempotent by reservation id.
func (l *Ledger) Reserve(_ context.Context, reservationID, merchantID uuid.UUID,
	lines []domain.Line, _ time.Duration) (app.ReserveResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.reserveErr != nil {
		return app.ReserveResult{}, l.reserveErr
	}
	if _, exists := l.holds[reservationID]; exists {
		return app.ReserveResult{AlreadyExisted: true, ExpiresAt: time.Now().UTC().Add(time.Hour)}, nil
	}

	rejected := &app.ReserveRejectedError{}
	failed := false
	for _, line := range lines {
		card, ok := l.cards[line.MenuItemID]
		switch {
		case !ok || !card.tracked:
			rejected.Lines = append(rejected.Lines, app.RejectedLine{
				MenuItemID: line.MenuItemID, RequestedQty: line.Qty,
				Problem: domain.ItemProblemNotTracked,
			})
			failed = true
		case card.stopped:
			rejected.Lines = append(rejected.Lines, app.RejectedLine{
				MenuItemID: line.MenuItemID, RequestedQty: line.Qty,
				AvailableQty: card.onHand - card.reserved,
				Problem:      domain.ItemProblemSalesStopped,
			})
			failed = true
		case card.onHand-card.reserved < line.Qty:
			rejected.Lines = append(rejected.Lines, app.RejectedLine{
				MenuItemID: line.MenuItemID, RequestedQty: line.Qty,
				AvailableQty: card.onHand - card.reserved,
				Problem:      domain.ItemProblemInsufficientUnits,
			})
			failed = true
		default:
			rejected.Lines = append(rejected.Lines, app.RejectedLine{
				MenuItemID: line.MenuItemID, RequestedQty: line.Qty,
				AvailableQty: card.onHand - card.reserved,
				Problem:      domain.ItemProblemNone,
			})
		}
	}
	if failed {
		return app.ReserveResult{}, rejected
	}
	for _, line := range lines {
		l.cards[line.MenuItemID].reserved += line.Qty
	}
	l.holds[reservationID] = &hold{merchantID: merchantID, lines: lines, state: "HELD"}
	return app.ReserveResult{ExpiresAt: time.Now().UTC().Add(30 * time.Minute)}, nil
}

// Commit consumes the held units.
func (l *Ledger) Commit(_ context.Context, reservationID uuid.UUID) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.commitErr != nil {
		return l.commitErr
	}
	h, ok := l.holds[reservationID]
	if !ok {
		return app.ErrReservationNotFound
	}
	if h.state == "COMMITTED" {
		return nil
	}
	if h.state != "HELD" {
		return app.ErrReservationNotHeld
	}
	for _, line := range h.lines {
		card := l.cards[line.MenuItemID]
		card.onHand -= line.Qty
		card.reserved -= line.Qty
	}
	h.state = "COMMITTED"
	l.commits++
	return nil
}

// Release returns the held units to availability.
func (l *Ledger) Release(_ context.Context, reservationID uuid.UUID, _ domain.ReleaseReason) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.releaseErr != nil {
		return l.releaseErr
	}
	h, ok := l.holds[reservationID]
	if !ok {
		return app.ErrReservationNotFound
	}
	if h.state == "RELEASED" {
		return nil
	}
	if h.state != "HELD" {
		return app.ErrReservationNotHeld
	}
	for _, line := range h.lines {
		l.cards[line.MenuItemID].reserved -= line.Qty
	}
	h.state = "RELEASED"
	l.releases++
	return nil
}

// Orders is an in-memory OrderRepository and OutboxRepository. The two ports
// share one structure because the outbox message and the status change must be
// written together, and a fake that could write one without the other would
// not model the rule it is used to test.
type Orders struct {
	mu       sync.Mutex
	byID     map[uuid.UUID]domain.Order
	byKey    map[string]uuid.UUID
	messages map[uuid.UUID]domain.Message
	order    []uuid.UUID
}

// NewOrders returns an empty repository.
func NewOrders() *Orders {
	return &Orders{
		byID:     map[uuid.UUID]domain.Order{},
		byKey:    map[string]uuid.UUID{},
		messages: map[uuid.UUID]domain.Message{},
	}
}

func idempotencyKey(customerID uuid.UUID, key string) string {
	return customerID.String() + "|" + key
}

// Create stores an order unless the idempotency key was already used.
func (o *Orders) Create(_ context.Context, order domain.Order) (domain.Order, bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	key := idempotencyKey(order.CustomerID, order.IdempotencyKey)
	if existing, ok := o.byKey[key]; ok {
		return o.byID[existing], true, nil
	}
	now := time.Now().UTC()
	order.CreatedAt, order.UpdatedAt = now, now
	o.byID[order.ID] = order
	o.byKey[key] = order.ID
	o.order = append(o.order, order.ID)
	return order, false, nil
}

// GetByID returns one order.
func (o *Orders) GetByID(_ context.Context, orderID uuid.UUID) (domain.Order, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	order, ok := o.byID[orderID]
	if !ok {
		return domain.Order{}, domain.ErrOrderNotFound
	}
	return order, nil
}

// FindByIdempotencyKey returns the order created with that key.
func (o *Orders) FindByIdempotencyKey(_ context.Context, customerID uuid.UUID,
	key string) (domain.Order, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	id, ok := o.byKey[idempotencyKey(customerID, key)]
	if !ok {
		return domain.Order{}, domain.ErrOrderNotFound
	}
	return o.byID[id], nil
}

// ListByCustomer returns the history of one customer, newest first.
func (o *Orders) ListByCustomer(_ context.Context, customerID uuid.UUID,
	limit, offset int) (app.OrderPage, error) {
	return o.list(limit, offset, true, func(order domain.Order) bool {
		return order.CustomerID == customerID
	})
}

// ListByMerchant returns the queue of one merchant, oldest first.
func (o *Orders) ListByMerchant(_ context.Context, merchantID uuid.UUID,
	statuses []domain.Status, limit, offset int) (app.OrderPage, error) {
	allowed := map[domain.Status]struct{}{}
	for _, st := range statuses {
		allowed[st] = struct{}{}
	}
	return o.list(limit, offset, false, func(order domain.Order) bool {
		if order.MerchantID != merchantID {
			return false
		}
		if len(allowed) == 0 {
			return true
		}
		_, ok := allowed[order.Status]
		return ok
	})
}

func (o *Orders) list(limit, offset int, newestFirst bool,
	keep func(domain.Order) bool) (app.OrderPage, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var all []domain.Order
	for _, id := range o.order {
		if order := o.byID[id]; keep(order) {
			all = append(all, order)
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if newestFirst {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})
	total := len(all)
	if offset >= total {
		return app.OrderPage{Total: total, Limit: limit, Offset: offset}, nil
	}
	end := min(offset+limit, total)
	return app.OrderPage{
		Items: all[offset:end], Total: total, Limit: limit, Offset: offset,
	}, nil
}

// Transition applies a lifecycle move together with its outbox message.
func (o *Orders) Transition(_ context.Context, cmd app.TransitionCommand) (app.TransitionResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	order, ok := o.byID[cmd.OrderID]
	if !ok {
		return app.TransitionResult{}, domain.ErrOrderNotFound
	}
	if cmd.ExpectMerchantID != nil && order.MerchantID != *cmd.ExpectMerchantID {
		return app.TransitionResult{}, domain.ErrOrderNotFound
	}
	if cmd.ExpectCustomerID != nil && order.CustomerID != *cmd.ExpectCustomerID {
		return app.TransitionResult{}, domain.ErrOrderNotFound
	}
	if order.Status == cmd.Target {
		return app.TransitionResult{Order: order, AlreadyApplied: true}, nil
	}
	if !order.Status.CanTransitionTo(cmd.Target) {
		return app.TransitionResult{}, &domain.StateConflictError{
			Current: order.Status, Target: cmd.Target,
		}
	}
	order.Status = cmd.Target
	if cmd.ETAMinutes > 0 {
		order.ETAMinutes = cmd.ETAMinutes
	}
	if cmd.RejectReason != "" {
		order.RejectReason = cmd.RejectReason
	}
	order.UpdatedAt = time.Now().UTC()
	o.byID[order.ID] = order

	if cmd.Message != nil {
		msg := *cmd.Message
		msg.State = domain.MessagePending
		msg.AvailableAt = time.Now().UTC().Add(-time.Second)
		o.messages[msg.ID] = msg
	}
	return app.TransitionResult{Order: order}, nil
}

// Claim takes due messages and counts the attempt.
func (o *Orders) Claim(_ context.Context, now time.Time, batch int,
	lease time.Duration) ([]domain.Message, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []domain.Message
	ids := make([]uuid.UUID, 0, len(o.messages))
	for id := range o.messages {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return o.messages[ids[i]].AvailableAt.Before(o.messages[ids[j]].AvailableAt)
	})
	for _, id := range ids {
		msg := o.messages[id]
		if msg.State != domain.MessagePending || msg.AvailableAt.After(now) {
			continue
		}
		msg.Attempts++
		msg.AvailableAt = now.Add(lease)
		o.messages[id] = msg
		out = append(out, msg)
		if len(out) >= batch {
			break
		}
	}
	return out, nil
}

// MarkSent records a delivered message.
func (o *Orders) MarkSent(_ context.Context, id uuid.UUID) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	msg, ok := o.messages[id]
	if !ok || msg.State != domain.MessagePending {
		return nil
	}
	msg.State = domain.MessageSent
	msg.LastError = ""
	o.messages[id] = msg
	return nil
}

// Reschedule moves the next attempt of a message.
func (o *Orders) Reschedule(_ context.Context, id uuid.UUID, availableAt time.Time, lastErr string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	msg, ok := o.messages[id]
	if !ok || msg.State != domain.MessagePending {
		return nil
	}
	msg.AvailableAt = availableAt
	msg.LastError = lastErr
	o.messages[id] = msg
	return nil
}

// MarkFailed parks a message.
func (o *Orders) MarkFailed(_ context.Context, id uuid.UUID, lastErr string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	msg, ok := o.messages[id]
	if !ok || msg.State != domain.MessagePending {
		return nil
	}
	msg.State = domain.MessageFailed
	msg.LastError = lastErr
	o.messages[id] = msg
	return nil
}

// PendingCount reports the undelivered messages.
func (o *Orders) PendingCount(_ context.Context) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, msg := range o.messages {
		if msg.State == domain.MessagePending {
			n++
		}
	}
	return n, nil
}

// Messages returns every stored outbox message, for assertions.
func (o *Orders) Messages() []domain.Message {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]domain.Message, 0, len(o.messages))
	for _, msg := range o.messages {
		out = append(out, msg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// MessagesOf returns the outbox messages of one order, for assertions.
func (o *Orders) MessagesOf(orderID uuid.UUID) []domain.Message {
	var out []domain.Message
	for _, msg := range o.Messages() {
		if msg.AggregateID == orderID {
			out = append(out, msg)
		}
	}
	return out
}

var (
	_ app.OrderRepository   = (*Orders)(nil)
	_ app.OutboxRepository  = (*Orders)(nil)
	_ app.MerchantDirectory = (*Directory)(nil)
	_ app.MenuCatalogue     = (*Catalogue)(nil)
	_ app.StockLedger       = (*Ledger)(nil)
)
