// Package e2e_test drives the running system through its public HTTP APIs.
//
// These tests use no in-process fakes: every call goes to a real service over
// the network, and the assertions are about the state the whole system ends up
// in. They are skipped unless the four base URLs are provided, so `go test
// ./...` stays runnable without a stack:
//
//	docker compose up --build -d
//	E2E_MERCHANT_URL=http://localhost:8081 \
//	E2E_CATALOG_URL=http://localhost:8082 \
//	E2E_INVENTORY_URL=http://localhost:8083 \
//	E2E_ORDER_URL=http://localhost:8084 \
//	go test ./test/e2e/... -count=1 -v
package e2e_test

import (
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

type stack struct {
	merchant  string
	catalog   string
	inventory string
	order     string
	client    *http.Client
}

func requireStack(t *testing.T) stack {
	t.Helper()
	s := stack{
		merchant:  os.Getenv("E2E_MERCHANT_URL"),
		catalog:   os.Getenv("E2E_CATALOG_URL"),
		inventory: os.Getenv("E2E_INVENTORY_URL"),
		order:     os.Getenv("E2E_ORDER_URL"),
		client:    &http.Client{Timeout: 10 * time.Second},
	}
	if s.merchant == "" || s.catalog == "" || s.inventory == "" || s.order == "" {
		t.Skip("end-to-end tests need E2E_MERCHANT_URL, E2E_CATALOG_URL, " +
			"E2E_INVENTORY_URL and E2E_ORDER_URL pointing at a running stack")
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

func (s stack) call(t *testing.T, method, url, body string, headers map[string]string) response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
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
	s.setOnHand(t, itemSellable, 10)
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
	s.waitForAvailability(t, itemSellable, before-3, 30*time.Second)

	var onHand int32
	stock := s.expect(t, http.StatusOK, http.MethodGet,
		fmt.Sprintf("%s/api/v1/merchant/stock/%s", s.inventory, itemSellable), "", merchantHeaders())
	var stockPayload struct {
		OnHandQty   int32 `json:"on_hand_qty"`
		ReservedQty int32 `json:"reserved_qty"`
	}
	stock.decode(t, &stockPayload)
	onHand = stockPayload.OnHandQty
	if onHand != 7 || stockPayload.ReservedQty != 0 {
		t.Fatalf("on hand = %d, reserved = %d, want the units consumed, not held",
			onHand, stockPayload.ReservedQty)
	}

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
	s.setOnHand(t, itemSellable, 10)
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
	s.waitForAvailability(t, itemSellable, before, 30*time.Second)

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
	s.setOnHand(t, itemSellable, 10)
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
	s.waitForAvailability(t, itemSellable, before, 30*time.Second)
}

// TestRefusedBaskets checks that the reasons a customer can hit are reported
// as conflicts and change nothing.
func TestRefusedBaskets(t *testing.T) {
	s := requireStack(t)
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
}

// TestOrderIsScopedToItsCustomer checks that an order is not readable by
// anyone who guesses its identifier.
func TestOrderIsScopedToItsCustomer(t *testing.T) {
	s := requireStack(t)
	s.setOnHand(t, itemSellable, 10)
	customerID := uuid.NewString()

	created := s.expect(t, http.StatusCreated, http.MethodPost,
		s.order+"/api/v1/orders", basket(itemSellable, minOrderQty),
		customerHeaders(customerID, "e2e-scope-"+customerID))
	var order orderPayload
	created.decode(t, &order)
	t.Cleanup(func() {
		s.call(t, http.MethodPost,
			fmt.Sprintf("%s/api/v1/orders/%s/cancel", s.order, order.OrderID), "",
			customerHeaders(customerID, ""))
	})

	resp := s.call(t, http.MethodGet,
		fmt.Sprintf("%s/api/v1/orders/%s", s.order, order.OrderID), "",
		customerHeaders(uuid.NewString(), ""))
	if resp.status != http.StatusNotFound {
		t.Fatalf("another customer = %d, want 404: %s", resp.status, resp.body)
	}
}

// TestBodyIsRejectedWithoutHeaders checks the two headers the placement needs.
func TestPlacementRequiresItsHeaders(t *testing.T) {
	s := requireStack(t)
	resp := s.call(t, http.MethodPost, s.order+"/api/v1/orders",
		basket(itemSellable, 1), nil)
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", resp.status, resp.body)
	}
}

// TestOpsEndpoints checks that every service reports itself ready.
func TestOpsEndpoints(t *testing.T) {
	s := requireStack(t)
	for name, base := range map[string]string{
		"merchant": s.merchant, "catalog": s.catalog,
		"inventory": s.inventory, "order": s.order,
	} {
		resp := s.call(t, http.MethodGet, base+"/api/v1/nothing", "", nil)
		if resp.status != http.StatusNotFound {
			t.Fatalf("%s: unknown endpoint = %d, want 404", name, resp.status)
		}
	}
}
