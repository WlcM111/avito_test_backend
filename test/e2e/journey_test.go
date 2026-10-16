// Package e2e_test drives the running system through its public HTTP APIs.
//
// These tests use no in-process fakes: every call goes to a real service over
// the network, and the assertions are about the state the whole system ends up
// in. They are skipped unless the four base URLs are provided, so `go test
// ./...` stays runnable without a stack. `make e2e` provides them with the
// ports Docker Compose publishes:
//
//	docker compose up --build -d
//	make wait e2e
//
// which is the same as
//
//	E2E_MERCHANT_URL=http://localhost:8081 \
//	E2E_CATALOG_URL=http://localhost:8082 \
//	E2E_INVENTORY_URL=http://localhost:8083 \
//	E2E_ORDER_URL=http://localhost:8084 \
//	E2E_POS_URL=http://localhost:8085 \
//	go test ./test/e2e/... -count=1 -v
//
// E2E_POS_URL is optional: without it the scenario of the demonstration till
// is skipped. Every test establishes the state it relies on itself - the
// profile of the outlet, its stock, the MANUAL mode of the till and a queue
// without orders waiting for a decision - so the suite can be repeated against
// the same stack, after the smoke scripts and after manual experiments.
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Fixed demonstration identifiers of the seed migrations.
const (
	merchantID       = "11111111-1111-4111-8111-111111111111"
	itemSellable     = "a1111111-1111-4111-8111-111111111111"
	itemEnabledEmpty = "a1111111-1111-4111-8111-222222222222"
	itemStopped      = "a1111111-1111-4111-8111-333333333333"
	apiKey           = "dev-merchant-key-kafe-primer-0001"
)

// settleLimit bounds every wait for a decision delivered through the outbox.
const settleLimit = 30 * time.Second

// tillManual is the default behaviour of the demonstration till: it leaves the
// orders alone, so the scenarios decide them themselves.
const tillManual = `{"mode":"MANUAL","eta_minutes":25,"reject_reason":"закончились продукты","kitchen":true}`

type stack struct {
	merchant  string
	catalog   string
	inventory string
	order     string
	pos       string
	client    *http.Client
}

func requireStack(t *testing.T) stack {
	t.Helper()
	s := stack{
		merchant:  os.Getenv("E2E_MERCHANT_URL"),
		catalog:   os.Getenv("E2E_CATALOG_URL"),
		inventory: os.Getenv("E2E_INVENTORY_URL"),
		order:     os.Getenv("E2E_ORDER_URL"),
		pos:       os.Getenv("E2E_POS_URL"),
		client:    &http.Client{Timeout: 10 * time.Second},
	}
	if s.merchant == "" || s.catalog == "" || s.inventory == "" || s.order == "" {
		t.Skip("end-to-end tests need E2E_MERCHANT_URL, E2E_CATALOG_URL, " +
			"E2E_INVENTORY_URL and E2E_ORDER_URL pointing at a running stack (make e2e)")
	}
	return s
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func (r response) decode(t *testing.T, dst any) {
	t.Helper()
	if err := json.Unmarshal(r.body, dst); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

// call performs one request. It uses its own context rather than the one of
// the test, because it also runs from cleanups, after that context is done.
func (s stack) call(t *testing.T, method, url, body string, headers map[string]string) response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: payload}
}

func (s stack) expect(t *testing.T, want int, method, url, body string,
	headers map[string]string) response {
	t.Helper()
	resp := s.call(t, method, url, body, headers)
	if resp.status != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, url, resp.status, want, resp.body)
	}
	return resp
}

func merchantHeaders() map[string]string { return map[string]string{"X-Api-Key": apiKey} }

func customerHeaders(customerID, key string) map[string]string {
	h := map[string]string{"X-Customer-Id": customerID}
	if key != "" {
		h["Idempotency-Key"] = key
	}
	return h
}

// availableQty reads the units of one position from inventory-service.
func (s stack) availableQty(t *testing.T, menuItemID string) int32 {
	t.Helper()
	resp := s.expect(t, http.StatusOK, http.MethodGet,
		fmt.Sprintf("%s/api/v1/merchants/%s/availability?menu_item_ids=%s",
			s.inventory, merchantID, menuItemID), "", nil)
	var payload struct {
		Items []struct {
			MenuItemID   string `json:"menu_item_id"`
			AvailableQty int32  `json:"available_qty"`
		} `json:"items"`
	}
	resp.decode(t, &payload)
	for _, item := range payload.Items {
		if item.MenuItemID == menuItemID {
			return item.AvailableQty
		}
	}
	t.Fatalf("availability of %s not reported", menuItemID)
	return 0
}

// setOnHand puts a known number of units on a card so that a run does not
// depend on what earlier runs left behind.
func (s stack) setOnHand(t *testing.T, menuItemID string, qty int) {
	t.Helper()
	body := fmt.Sprintf(`{"on_hand_qty":%d,"reason":"e2e"}`, qty)
	s.expect(t, http.StatusOK, http.MethodPut,
		fmt.Sprintf("%s/api/v1/merchant/stock/%s", s.inventory, menuItemID),
		body, merchantHeaders())
}

// setSalesState puts a card on the stop list or takes it off.
func (s stack) setSalesState(t *testing.T, menuItemID, state string) {
	t.Helper()
	s.expect(t, http.StatusOK, http.MethodPatch,
		fmt.Sprintf("%s/api/v1/merchant/stock/%s", s.inventory, menuItemID),
		fmt.Sprintf(`{"sales_state":%q}`, state), merchantHeaders())
}

// prepareStock gives a card a known state: no units held, qty on hand and the
// given stop-list switch. Holds of orders decided by an earlier run come back
// asynchronously through the outbox, so it first waits for them to settle;
// counting from a card that still carries them would make every expectation
// of the test wrong.
func (s stack) prepareStock(t *testing.T, menuItemID string, qty int, state string) {
	t.Helper()
	s.waitForStock(t, menuItemID, -1, 0, settleLimit)
	s.setOnHand(t, menuItemID, qty)
	s.setSalesState(t, menuItemID, state)
}

// acceptOrders opens the demonstration outlet for orders with the minimum of
// the seed. A profile left closed or with a higher minimum by a manual
// experiment would otherwise refuse the baskets of the scenarios.
func (s stack) acceptOrders(t *testing.T) {
	t.Helper()
	s.expect(t, http.StatusOK, http.MethodPatch, s.merchant+"/api/v1/merchant/profile",
		`{"is_accepting_orders":true,"min_order_minor":50000}`, merchantHeaders())
}

// manualTill returns the demonstration till to its default MANUAL mode. Left
// in an automatic mode by a manual experiment or an interrupted run, it would
// answer the orders of the scenarios before they do. Without E2E_POS_URL the
// till is out of reach and is left as it is.
func (s stack) manualTill(t *testing.T) {
	t.Helper()
	if s.pos == "" {
		return
	}
	s.expect(t, http.StatusOK, http.MethodPut, s.pos+"/pos/behavior", tillManual, nil)
}

// rejectLeftovers refuses the orders that still wait for a decision of the
// outlet: those of a manual experiment or of a run that stopped half-way. Each
// of them holds its units until the reservation expires (30 minutes by
// default), and a card that carries such a hold never comes to the state the
// scenarios count from. An order decided in the meantime answers 409 and is
// left as it is.
func (s stack) rejectLeftovers(t *testing.T) {
	t.Helper()
	for range 20 {
		var queue struct {
			Items []struct {
				OrderID string `json:"order_id"`
			} `json:"items"`
		}
		s.expect(t, http.StatusOK, http.MethodGet,
			s.order+"/api/v1/merchant/orders?status=CREATED&limit=50", "",
			merchantHeaders()).decode(t, &queue)
		if len(queue.Items) == 0 {
			return
		}
		for _, item := range queue.Items {
			resp := s.call(t, http.MethodPost,
				fmt.Sprintf("%s/api/v1/merchant/orders/%s/reject", s.order, item.OrderID),
				`{"reason":"e2e: заказ остался от прошлого прогона"}`, merchantHeaders())
			if resp.status != http.StatusOK && resp.status != http.StatusConflict {
				t.Fatalf("reject the leftover order %s = %d: %s", item.OrderID, resp.status, resp.body)
			}
		}
	}
	t.Fatal("the queue of the demonstration outlet keeps orders waiting for a decision")
}

// prepareOutlet gives the demonstration outlet the state every scenario counts
// from: open for orders with the minimum of the seed, the till in MANUAL mode
// and no order waiting for a decision.
func (s stack) prepareOutlet(t *testing.T) {
	t.Helper()
	s.acceptOrders(t)
	s.manualTill(t)
	s.rejectLeftovers(t)
}

// prepareSellable is the starting point of every scenario that places an
// order: a prepared outlet and ten sellable units with nothing held.
func (s stack) prepareSellable(t *testing.T) {
	t.Helper()
	s.prepareOutlet(t)
	s.prepareStock(t, itemSellable, 10, "ENABLED")
}

// waitForAvailability polls until the units reach want or the deadline passes.
// The delivery of a terminal decision is asynchronous, so the check has an
// explicit condition and a bound rather than a fixed sleep.
func (s stack) waitForAvailability(t *testing.T, menuItemID string, want int32, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	var last int32
	for time.Now().Before(deadline) {
		last = s.availableQty(t, menuItemID)
		if last == want {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("availability of %s stayed at %d, want %d within %s", menuItemID, last, want, limit)
}

// stockCard читает карточку остатка глазами заведения.
func (s stack) stockCard(t *testing.T, menuItemID string) (onHand, reserved int32) {
	t.Helper()
	resp := s.expect(t, http.StatusOK, http.MethodGet,
		fmt.Sprintf("%s/api/v1/merchant/stock/%s", s.inventory, menuItemID), "", merchantHeaders())
	var payload struct {
		OnHandQty   int32 `json:"on_hand_qty"`
		ReservedQty int32 `json:"reserved_qty"`
	}
	resp.decode(t, &payload)
	return payload.OnHandQty, payload.ReservedQty
}

// waitForStock ждёт, пока карточка остатка придёт в конечное состояние.
// Отрицательный wantOnHand означает «количество не важно».
//
// Для подтверждения заказа ждать по available_qty нельзя: он равен
// on_hand - reserved и одинаков до фиксации и после неё, потому что удержание
// превращается в списание. Ожидание по нему завершилось бы мгновенно, ещё до
// того как решение доедет через outbox, и следующая же проверка увидела бы
// прежнее количество.
func (s stack) waitForStock(t *testing.T, menuItemID string, wantOnHand, wantReserved int32,
	limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	var onHand, reserved int32
	for time.Now().Before(deadline) {
		onHand, reserved = s.stockCard(t, menuItemID)
		if (wantOnHand < 0 || onHand == wantOnHand) && reserved == wantReserved {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("card of %s stayed at on_hand=%d reserved=%d, want %d and %d within %s",
		menuItemID, onHand, reserved, wantOnHand, wantReserved, limit)
}

type orderPayload struct {
	OrderID         string `json:"order_id"`
	Status          string `json:"status"`
	TotalMinor      int64  `json:"total_minor"`
	ItemsTotalMinor int64  `json:"items_total_minor"`
	ETAMinutes      int32  `json:"eta_minutes"`
	Items           []struct {
		MenuItemID     string `json:"menu_item_id"`
		Name           string `json:"name"`
		UnitPriceMinor int64  `json:"unit_price_minor"`
		Qty            int32  `json:"qty"`
	} `json:"items"`
}

// waitForStatus polls the order as its customer sees it until it reaches want.
func (s stack) waitForStatus(t *testing.T, customerID, orderID, want string, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	var last string
	for time.Now().Before(deadline) {
		var order orderPayload
		s.expect(t, http.StatusOK, http.MethodGet,
			fmt.Sprintf("%s/api/v1/orders/%s", s.order, orderID), "",
			customerHeaders(customerID, "")).decode(t, &order)
		last = order.Status
		if last == want {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("order %s stayed %s, want %s within %s", orderID, last, want, limit)
}

func basket(menuItemID string, qty int) string {
	return fmt.Sprintf(`{"merchant_id":"%s","items":[{"menu_item_id":"%s","qty":%d}],
	  "customer_name":"Иван","customer_phone":"+79990000001",
	  "delivery_address":"Невский проспект, 1"}`, merchantID, menuItemID, qty)
}

// previewBody — тело предпросмотра. Оно отличается от тела оформления:
// PreviewRequest состоит только из merchant_id и items, а order-service
// отвергает неизвестные поля (400). Отправлять сюда контакты покупателя нельзя.
func previewBody(menuItemID string, qty int) string {
	return fmt.Sprintf(`{"merchant_id":"%s","items":[{"menu_item_id":"%s","qty":%d}]}`,
		merchantID, menuItemID, qty)
}

// minOrderQty — количество, при котором корзина проходит минимальную сумму
// демонстрационного заведения (50 000 копеек при цене позиции 42 000–43 000).
const minOrderQty = 2

// TestHappyPath walks the whole customer journey across all four services and
// checks the state each of them ends up in.
func TestHappyPath(t *testing.T) {
	s := requireStack(t)
	s.prepareSellable(t)
	customerID := uuid.NewString()

	// The menu comes from catalog-service and the units from
	// inventory-service; the customer sees them joined by menu_item_id.
	menu := s.expect(t, http.StatusOK, http.MethodGet,
		fmt.Sprintf("%s/api/v1/merchants/%s/menu", s.catalog, merchantID), "", nil)
	var menuPayload struct {
		MenuVersion int64 `json:"menu_version"`
		Categories  []struct {
			Items []struct {
				MenuItemID string `json:"menu_item_id"`
				PriceMinor int64  `json:"price_minor"`
			} `json:"items"`
		} `json:"categories"`
	}
	menu.decode(t, &menuPayload)
	if menuPayload.MenuVersion == 0 {
		t.Fatal("the demonstration merchant must have a published menu")
	}
	before := s.availableQty(t, itemSellable)
	if before < 3 {
		t.Fatalf("the test needs at least 3 units, got %d", before)
	}
	onHandBefore, _ := s.stockCard(t, itemSellable)

	// Preview recalculates the basket against all three owners.
	preview := s.expect(t, http.StatusOK, http.MethodPost,
		s.order+"/api/v1/orders/preview", previewBody(itemSellable, 3), nil)
	var previewPayload struct {
		Orderable  bool  `json:"orderable"`
		TotalMinor int64 `json:"total_minor"`
	}
	preview.decode(t, &previewPayload)
	if !previewPayload.Orderable || previewPayload.TotalMinor == 0 {
		t.Fatalf("preview = %s", preview.body)
	}

	// Placing the order holds the units.
	created := s.expect(t, http.StatusCreated, http.MethodPost,
		s.order+"/api/v1/orders", basket(itemSellable, 3),
		customerHeaders(customerID, "e2e-"+customerID))
	var order orderPayload
	created.decode(t, &order)
	if order.Status != "CREATED" || order.TotalMinor != previewPayload.TotalMinor {
		t.Fatalf("order = %s", created.body)
	}
	if len(order.Items) != 1 || order.Items[0].Name == "" {
		t.Fatalf("the order must carry the price snapshot: %s", created.body)
	}
	if got := s.availableQty(t, itemSellable); got != before-3 {
		t.Fatalf("availability = %d, want %d: the units must be held", got, before-3)
	}

	// A repeat of the same key returns the same order and holds nothing more.
	replay := s.expect(t, http.StatusCreated, http.MethodPost,
		s.order+"/api/v1/orders", basket(itemSellable, 3),
		customerHeaders(customerID, "e2e-"+customerID))
	var replayed orderPayload
	replay.decode(t, &replayed)
	if replayed.OrderID != order.OrderID {
		t.Fatalf("a repeat created a second order: %s", replay.body)
	}
	if replay.header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("a repeat must be marked with Idempotent-Replay: %v", replay.header)
	}
	if got := s.availableQty(t, itemSellable); got != before-3 {
		t.Fatalf("availability = %d after the repeat, want %d", got, before-3)
	}

	// The order appears in the merchant queue.
	queue := s.expect(t, http.StatusOK, http.MethodGet,
		s.order+"/api/v1/merchant/orders?status=CREATED&limit=50", "", merchantHeaders())
	if !strings.Contains(string(queue.body), order.OrderID) {
		t.Fatalf("the order is not in the queue: %s", queue.body)
	}

	// Accepting it consumes the held units - asynchronously, through the
	// outbox, so the check waits for the end state rather than for the call.
	s.expect(t, http.StatusOK, http.MethodPost,
		fmt.Sprintf("%s/api/v1/merchant/orders/%s/accept", s.order, order.OrderID),
		`{"eta_minutes":25}`, merchantHeaders())
	s.waitForStock(t, itemSellable, onHandBefore-3, 0, settleLimit)

	// The merchant walks the order to the customer.
	for _, status := range []string{"COOKING", "DELIVERING", "DELIVERED"} {
		s.expect(t, http.StatusOK, http.MethodPost,
			fmt.Sprintf("%s/api/v1/merchant/orders/%s/status", s.order, order.OrderID),
			fmt.Sprintf(`{"status":%q}`, status), merchantHeaders())
	}
	final := s.expect(t, http.StatusOK, http.MethodGet,
		fmt.Sprintf("%s/api/v1/orders/%s", s.order, order.OrderID), "",
		customerHeaders(customerID, ""))
	var delivered orderPayload
	final.decode(t, &delivered)
	if delivered.Status != "DELIVERED" || delivered.ETAMinutes != 25 {
		t.Fatalf("final order = %s", final.body)
	}
}

// TestCancellationReturnsUnits proves the compensating path: a customer
// withdraws the order and the held units come back.
func TestCancellationReturnsUnits(t *testing.T) {
	s := requireStack(t)
	s.prepareSellable(t)
	customerID := uuid.NewString()
	before := s.availableQty(t, itemSellable)

	created := s.expect(t, http.StatusCreated, http.MethodPost,
		s.order+"/api/v1/orders", basket(itemSellable, 2),
		customerHeaders(customerID, "e2e-cancel-"+customerID))
	var order orderPayload
	created.decode(t, &order)
	if got := s.availableQty(t, itemSellable); got != before-2 {
		t.Fatalf("availability = %d, want %d", got, before-2)
	}

	s.expect(t, http.StatusOK, http.MethodPost,
		fmt.Sprintf("%s/api/v1/orders/%s/cancel", s.order, order.OrderID), "",
		customerHeaders(customerID, ""))
	s.waitForAvailability(t, itemSellable, before, settleLimit)

	// Cancelling again changes nothing and says so.
	again := s.expect(t, http.StatusOK, http.MethodPost,
		fmt.Sprintf("%s/api/v1/orders/%s/cancel", s.order, order.OrderID), "",
		customerHeaders(customerID, ""))
	if again.header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("a repeated cancellation must be marked with Idempotent-Replay: %v", again.header)
	}

	// A cancelled order is terminal for the merchant too.
	conflict := s.call(t, http.MethodPost,
		fmt.Sprintf("%s/api/v1/merchant/orders/%s/accept", s.order, order.OrderID),
		`{"eta_minutes":10}`, merchantHeaders())
	if conflict.status != http.StatusConflict {
		t.Fatalf("accepting a cancelled order = %d, want 409: %s", conflict.status, conflict.body)
	}
}

// TestRejectionReturnsUnits proves the same path driven by the merchant.
func TestRejectionReturnsUnits(t *testing.T) {
	s := requireStack(t)
	s.prepareSellable(t)
	customerID := uuid.NewString()
	before := s.availableQty(t, itemSellable)

	created := s.expect(t, http.StatusCreated, http.MethodPost,
		s.order+"/api/v1/orders", basket(itemSellable, 2),
		customerHeaders(customerID, "e2e-reject-"+customerID))
	var order orderPayload
	created.decode(t, &order)

	s.expect(t, http.StatusOK, http.MethodPost,
		fmt.Sprintf("%s/api/v1/merchant/orders/%s/reject", s.order, order.OrderID),
		`{"reason":"закончились продукты"}`, merchantHeaders())
	s.waitForAvailability(t, itemSellable, before, settleLimit)
}

// TestRefusedBaskets checks that the reasons a customer can hit are reported
// as conflicts and change nothing.
func TestRefusedBaskets(t *testing.T) {
	s := requireStack(t)
	s.prepareOutlet(t)
	// The two demonstration states: allowed but without a single unit, and
	// units on hand while the position is on the stop list.
	s.prepareStock(t, itemEnabledEmpty, 0, "ENABLED")
	s.prepareStock(t, itemStopped, 5, "STOPPED")
	customerID := uuid.NewString()

	cases := []struct {
		name string
		item string
		code string
	}{
		{name: "stop list", item: itemStopped, code: "ITEM_SALES_STOPPED"},
		{name: "no units", item: itemEnabledEmpty, code: "INSUFFICIENT_UNITS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.call(t, http.MethodPost, s.order+"/api/v1/orders",
				basket(tc.item, 1), customerHeaders(customerID, "e2e-"+tc.name+"-"+customerID))
			if resp.status != http.StatusConflict {
				t.Fatalf("status = %d, want 409: %s", resp.status, resp.body)
			}
			var payload struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			resp.decode(t, &payload)
			if payload.Error.Code != tc.code {
				t.Fatalf("code = %q, want %q", payload.Error.Code, tc.code)
			}
		})
	}
	// A refused basket holds nothing.
	if _, reserved := s.stockCard(t, itemStopped); reserved != 0 {
		t.Fatalf("a refused basket held %d units", reserved)
	}
}

// TestOrderIsScopedToItsCustomer checks that an order is not readable by
// anyone who guesses its identifier.
func TestOrderIsScopedToItsCustomer(t *testing.T) {
	s := requireStack(t)
	s.prepareSellable(t)
	customerID := uuid.NewString()

	created := s.expect(t, http.StatusCreated, http.MethodPost,
		s.order+"/api/v1/orders", basket(itemSellable, minOrderQty),
		customerHeaders(customerID, "e2e-scope-"+customerID))
	var order orderPayload
	created.decode(t, &order)
	t.Cleanup(func() {
		// The order must not keep its units after the test: the next run, or
		// the next suite, counts from a card without holds.
		s.call(t, http.MethodPost,
			fmt.Sprintf("%s/api/v1/orders/%s/cancel", s.order, order.OrderID), "",
			customerHeaders(customerID, ""))
		s.waitForStock(t, itemSellable, -1, 0, settleLimit)
	})

	resp := s.call(t, http.MethodGet,
		fmt.Sprintf("%s/api/v1/orders/%s", s.order, order.OrderID), "",
		customerHeaders(uuid.NewString(), ""))
	if resp.status != http.StatusNotFound {
		t.Fatalf("another customer = %d, want 404: %s", resp.status, resp.body)
	}
}

// TestPlacementRequiresItsHeaders checks the two headers the placement needs.
func TestPlacementRequiresItsHeaders(t *testing.T) {
	s := requireStack(t)
	resp := s.call(t, http.MethodPost, s.order+"/api/v1/orders",
		basket(itemSellable, 1), nil)
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", resp.status, resp.body)
	}
}

// TestUnknownEndpointsAre404 checks that every service answers a path outside
// its contract with 404 in the shared error envelope.
func TestUnknownEndpointsAre404(t *testing.T) {
	s := requireStack(t)
	for name, base := range map[string]string{
		"merchant": s.merchant, "catalog": s.catalog,
		"inventory": s.inventory, "order": s.order,
	} {
		resp := s.call(t, http.MethodGet, base+"/api/v1/nothing", "", nil)
		if resp.status != http.StatusNotFound {
			t.Fatalf("%s: unknown endpoint = %d, want 404", name, resp.status)
		}
		var payload struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		resp.decode(t, &payload)
		if payload.Error.Code == "" {
			t.Fatalf("%s: a 404 must carry the error envelope: %s", name, resp.body)
		}
	}
}

// TestPartnerPosAnswersTheQueue drives the demonstration till (requirement
// A-06) from the outside: it is switched to automatic answers, and the orders
// it confirms or refuses reach the platform like those of any outlet. The till
// is put back into MANUAL mode whatever happens, because in an automatic mode
// it would answer the orders of every other scenario.
func TestPartnerPosAnswersTheQueue(t *testing.T) {
	s := requireStack(t)
	if s.pos == "" {
		t.Skip("E2E_POS_URL is not set; skipping the scenario of the demonstration till")
	}
	s.prepareSellable(t)
	setBehavior := func(t *testing.T, body string) {
		t.Helper()
		s.expect(t, http.StatusOK, http.MethodPut, s.pos+"/pos/behavior", body, nil)
	}
	t.Cleanup(func() {
		resp := s.call(t, http.MethodPut, s.pos+"/pos/behavior", tillManual, nil)
		if resp.status != http.StatusOK {
			t.Errorf("restore MANUAL mode = %d: %s", resp.status, resp.body)
		}
	})
	customerID := uuid.NewString()
	place := func(t *testing.T, key string) string {
		t.Helper()
		var order orderPayload
		s.expect(t, http.StatusCreated, http.MethodPost, s.order+"/api/v1/orders",
			basket(itemSellable, minOrderQty), customerHeaders(customerID, key)).decode(t, &order)
		return order.OrderID
	}

	// Refusal: the till answers on its own, and the units come back.
	before := s.availableQty(t, itemSellable)
	setBehavior(t, `{"mode":"AUTO_REJECT","eta_minutes":20,"reject_reason":"закончились продукты","kitchen":false}`)
	rejected := place(t, "e2e-pos-reject-"+customerID)
	s.waitForStatus(t, customerID, rejected, "REJECTED", settleLimit)
	s.waitForAvailability(t, itemSellable, before, settleLimit)

	// Confirmation: the till accepts with its own estimate and the units are
	// consumed. The kitchen is off, so the order stays CONFIRMED.
	onHandBefore, _ := s.stockCard(t, itemSellable)
	setBehavior(t, `{"mode":"AUTO_ACCEPT","eta_minutes":30,"reject_reason":"нет","kitchen":false}`)
	accepted := place(t, "e2e-pos-accept-"+customerID)
	s.waitForStatus(t, customerID, accepted, "CONFIRMED", settleLimit)
	s.waitForStock(t, itemSellable, onHandBefore-minOrderQty, 0, settleLimit)

	var order orderPayload
	s.expect(t, http.StatusOK, http.MethodGet,
		fmt.Sprintf("%s/api/v1/orders/%s", s.order, accepted), "",
		customerHeaders(customerID, "")).decode(t, &order)
	if order.ETAMinutes != 30 {
		t.Fatalf("eta = %d, want the estimate of the till (30)", order.ETAMinutes)
	}
}
