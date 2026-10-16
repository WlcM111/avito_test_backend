package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/internal/order/adapters/httpapi"
	"github.com/avito-kitchen/platform/internal/order/app"
	"github.com/avito-kitchen/platform/internal/order/domain"
	"github.com/avito-kitchen/platform/internal/order/testsupport"
	"github.com/google/uuid"
)

type testEnv struct {
	srv       *httptest.Server
	orders    *testsupport.Orders
	directory *testsupport.Directory
	catalogue *testsupport.Catalogue
	ledger    *testsupport.Ledger
	svc       *app.Service
}

func newEnv(t *testing.T) testEnv {
	t.Helper()
	orders := testsupport.NewOrders()
	directory := testsupport.NewDirectory()
	catalogue := testsupport.NewCatalogue()
	ledger := testsupport.NewLedger()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := app.NewService(orders, orders, directory, catalogue, ledger, logger, app.Config{
		ReservationTTL: 30 * time.Minute, MaxDeliveryAttempts: 3,
	})
	srv := httptest.NewServer(httpapi.NewHandler(svc, logger).Routes())
	t.Cleanup(srv.Close)
	return testEnv{srv: srv, orders: orders, directory: directory,
		catalogue: catalogue, ledger: ledger, svc: svc}
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details []struct {
			Field  string `json:"field"`
			Reason string `json:"reason"`
		} `json:"details"`
	} `json:"error"`
}

type orderBody struct {
	OrderID          string `json:"order_id"`
	MerchantID       string `json:"merchant_id"`
	MerchantName     string `json:"merchant_name"`
	CustomerID       string `json:"customer_id"`
	Status           string `json:"status"`
	ItemsTotalMinor  int64  `json:"items_total_minor"`
	DeliveryFeeMinor int64  `json:"delivery_fee_minor"`
	TotalMinor       int64  `json:"total_minor"`
	Currency         string `json:"currency"`
	ETAMinutes       int32  `json:"eta_minutes"`
	RejectReason     string `json:"reject_reason"`
	Items            []struct {
		MenuItemID     string `json:"menu_item_id"`
		ExternalID     string `json:"external_id"`
		Name           string `json:"name"`
		UnitPriceMinor int64  `json:"unit_price_minor"`
		Qty            int32  `json:"qty"`
		LineTotalMinor int64  `json:"line_total_minor"`
	} `json:"items"`
}

type pageBody struct {
	Items  []orderBody `json:"items"`
	Total  int         `json:"total"`
	Limit  int         `json:"limit"`
	Offset int         `json:"offset"`
}

type previewBody struct {
	MerchantID      string   `json:"merchant_id"`
	MerchantName    string   `json:"merchant_name"`
	MenuVersion     int64    `json:"menu_version"`
	ItemsTotalMinor int64    `json:"items_total_minor"`
	TotalMinor      int64    `json:"total_minor"`
	MinOrderMinor   int64    `json:"min_order_minor"`
	Orderable       bool     `json:"orderable"`
	Problems        []string `json:"problems"`
	Items           []struct {
		MenuItemID   string `json:"menu_item_id"`
		Problem      string `json:"problem"`
		AvailableQty int32  `json:"available_qty"`
	} `json:"items"`
}

// response is an answer read in full: do drains and closes the body itself,
// so a test never keeps a connection open and has nothing left to close.
type response struct {
	StatusCode int
	Header     http.Header
	Body       *bytes.Reader
}

func do(t *testing.T, e testEnv, method, path, body string, headers map[string]string) response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response{StatusCode: resp.StatusCode, Header: resp.Header, Body: bytes.NewReader(payload)}
}

func decode[T any](t *testing.T, r io.Reader) T {
	t.Helper()
	var out T
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return out
}

func customerHeaders(key string) map[string]string {
	return map[string]string{
		httpapi.HeaderCustomerID:     testsupport.CustomerID.String(),
		httpapi.HeaderIdempotencyKey: key,
	}
}

func merchantHeaders() map[string]string {
	return map[string]string{httpapi.HeaderAPIKey: testsupport.DevKey}
}

func basket(qty int32) string {
	return `{"merchant_id":"` + testsupport.MerchantID.String() + `",
	         "items":[{"menu_item_id":"` + testsupport.ItemSellable.String() + `","qty":` +
		strconv.FormatInt(int64(qty), 10) + `}],
	         "customer_name":"Иван","customer_phone":"+79990000001",
	         "delivery_address":"Невский проспект, 1"}`
}

func createOrder(t *testing.T, e testEnv, key string) orderBody {
	t.Helper()
	resp := do(t, e, http.MethodPost, "/api/v1/orders", basket(2), customerHeaders(key))
	if resp.StatusCode != http.StatusCreated {
		body := decode[errorBody](t, resp.Body)
		t.Fatalf("create status = %d, code = %s", resp.StatusCode, body.Error.Code)
	}
	return decode[orderBody](t, resp.Body)
}

// ------------------------------------------------------------- preview

func TestPreviewNeedsNoHeaders(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	body := `{"merchant_id":"` + testsupport.MerchantID.String() + `",
	          "items":[{"menu_item_id":"` + testsupport.ItemSellable.String() + `","qty":2}]}`
	resp := do(t, e, http.MethodPost, "/api/v1/orders/preview", body, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	preview := decode[previewBody](t, resp.Body)
	if !preview.Orderable || preview.MerchantName == "" {
		t.Fatalf("preview = %+v", preview)
	}
	if preview.TotalMinor != testsupport.PriceSellable*2+testsupport.DeliveryFeeMinor {
		t.Fatalf("total = %d", preview.TotalMinor)
	}
}

func TestPreviewReportsProblemsWithoutFailing(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	body := `{"merchant_id":"` + testsupport.MerchantID.String() + `","items":[
	          {"menu_item_id":"` + testsupport.ItemStopped.String() + `","qty":1}]}`
	resp := do(t, e, http.MethodPost, "/api/v1/orders/preview", body, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	preview := decode[previewBody](t, resp.Body)
	if preview.Orderable {
		t.Fatal("a stop-listed position must not be orderable")
	}
	if preview.Items[0].Problem != string(domain.ItemProblemSalesStopped) {
		t.Fatalf("problem = %s", preview.Items[0].Problem)
	}
}

func TestPreviewRejectsBadInput(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	cases := []struct {
		name, body string
		status     int
	}{
		{name: "not a uuid merchant", body: `{"merchant_id":"nope","items":[]}`, status: 422},
		{
			name:   "not a uuid item",
			body:   `{"merchant_id":"` + testsupport.MerchantID.String() + `","items":[{"menu_item_id":"x","qty":1}]}`,
			status: 422,
		},
		{
			name:   "empty basket",
			body:   `{"merchant_id":"` + testsupport.MerchantID.String() + `","items":[]}`,
			status: 422,
		},
		{name: "malformed json", body: `{"merchant_id":`, status: 400},
		{name: "unknown field", body: `{"nope":1}`, status: 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resp := do(t, e, http.MethodPost, "/api/v1/orders/preview", tc.body, nil)
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.status)
			}
		})
	}
}

func TestPreviewOfAnUnknownMerchantIs404(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	body := `{"merchant_id":"` + uuid.NewString() + `","items":[
	          {"menu_item_id":"` + testsupport.ItemSellable.String() + `","qty":1}]}`
	resp := do(t, e, http.MethodPost, "/api/v1/orders/preview", body, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if code := decode[errorBody](t, resp.Body).Error.Code; code != "MERCHANT_NOT_FOUND" {
		t.Fatalf("code = %q", code)
	}
}

// -------------------------------------------------------------- create

func TestCreateOrderReturns201WithTheSnapshot(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	order := createOrder(t, e, "key-1")
	if order.Status != "CREATED" || order.Currency != "RUB" {
		t.Fatalf("order = %+v", order)
	}
	if len(order.Items) != 1 || order.Items[0].Name == "" {
		t.Fatalf("the order must carry the snapshot: %+v", order.Items)
	}
	if order.TotalMinor != order.ItemsTotalMinor+order.DeliveryFeeMinor {
		t.Fatalf("totals do not add up: %+v", order)
	}
}

func TestCreateOrderRequiresTheCustomerAndTheKey(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	resp := do(t, e, http.MethodPost, "/api/v1/orders", basket(2),
		map[string]string{httpapi.HeaderIdempotencyKey: "key-1"})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("without a customer status = %d, want 422", resp.StatusCode)
	}

	resp = do(t, e, http.MethodPost, "/api/v1/orders", basket(2),
		map[string]string{httpapi.HeaderCustomerID: testsupport.CustomerID.String()})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("without a key status = %d, want 422", resp.StatusCode)
	}

	resp = do(t, e, http.MethodPost, "/api/v1/orders", basket(2),
		map[string]string{
			httpapi.HeaderCustomerID:     "not-a-uuid",
			httpapi.HeaderIdempotencyKey: "key-1",
		})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("with a bad customer status = %d, want 422", resp.StatusCode)
	}
}

// A double submit must not produce a second order and must not answer with a
// failure: the client cannot tell the two calls apart.
func TestCreateOrderIsIdempotentOverHTTP(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	first := createOrder(t, e, "key-1")

	resp := do(t, e, http.MethodPost, "/api/v1/orders", basket(2), customerHeaders("key-1"))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if resp.Header.Get("Idempotent-Replay") != "true" {
		t.Fatal("a replay must be marked in the response")
	}
	second := decode[orderBody](t, resp.Body)
	if second.OrderID != first.OrderID {
		t.Fatalf("a repeat produced a second order: %s vs %s", second.OrderID, first.OrderID)
	}
	if _, reserved, _ := e.ledger.Snapshot(testsupport.ItemSellable); reserved != 2 {
		t.Fatalf("reserved = %d, want 2", reserved)
	}
}

func TestCreateOrderConflicts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		prepare func(testEnv)
		item    uuid.UUID
		code    string
	}{
		{
			name: "stop list",
			item: testsupport.ItemStopped,
			code: string(domain.ItemProblemSalesStopped),
		},
		{
			name: "not enough units",
			item: testsupport.ItemEnabledEmpty,
			code: string(domain.ItemProblemInsufficientUnits),
		},
		{
			name: "outlet paused intake",
			prepare: func(e testEnv) {
				p := e.directory.Profile(testsupport.MerchantID)
				p.IsAcceptingOrders = false
				e.directory.SetProfile(p)
			},
			item: testsupport.ItemSellable,
			code: string(domain.OrderProblemNotAcceptingOrders),
		},
		{
			name: "below the minimum",
			prepare: func(e testEnv) {
				p := e.directory.Profile(testsupport.MerchantID)
				p.MinOrderMinor = 10_000_000
				e.directory.SetProfile(p)
			},
			item: testsupport.ItemSellable,
			code: string(domain.OrderProblemBelowMinOrder),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			if tc.prepare != nil {
				tc.prepare(e)
			}
			body := `{"merchant_id":"` + testsupport.MerchantID.String() + `","items":[
			          {"menu_item_id":"` + tc.item.String() + `","qty":2}],
			          "customer_name":"Иван","customer_phone":"+79990000001",
			          "delivery_address":"Невский проспект, 1"}`
			resp := do(t, e, http.MethodPost, "/api/v1/orders", body, customerHeaders("key-1"))
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409", resp.StatusCode)
			}
			payload := decode[errorBody](t, resp.Body)
			if payload.Error.Code != tc.code {
				t.Fatalf("code = %q, want %q", payload.Error.Code, tc.code)
			}
			if len(payload.Error.Details) == 0 {
				t.Fatal("a refusal must say which position or rule failed")
			}
		})
	}
}

func TestCreateOrderReportsAChangedPrice(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	agreed := testsupport.PriceSellable*2 + testsupport.DeliveryFeeMinor
	e.catalogue.SetPrice(testsupport.ItemSellable, testsupport.PriceSellable+500)

	body := `{"merchant_id":"` + testsupport.MerchantID.String() + `","items":[
	          {"menu_item_id":"` + testsupport.ItemSellable.String() + `","qty":2}],
	          "customer_name":"Иван","customer_phone":"+79990000001",
	          "delivery_address":"Невский проспект, 1",
	          "expected_total_minor":` + itoa(agreed) + `}`
	resp := do(t, e, http.MethodPost, "/api/v1/orders", body, customerHeaders("key-1"))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if code := decode[errorBody](t, resp.Body).Error.Code; code != "PRICE_CHANGED" {
		t.Fatalf("code = %q, want PRICE_CHANGED", code)
	}
}

func TestCreateOrderReports503OnAnOutage(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.directory.SetProfileErr(app.Unavailable("merchant-service", http.ErrServerClosed))
	resp := do(t, e, http.MethodPost, "/api/v1/orders", basket(2), customerHeaders("key-1"))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if code := decode[errorBody](t, resp.Body).Error.Code; code != "DEPENDENCY_UNAVAILABLE" {
		t.Fatalf("code = %q", code)
	}
}

// ---------------------------------------------------- customer queries

func TestCustomerCanReadOnlyItsOwnOrders(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	order := createOrder(t, e, "key-1")

	resp := do(t, e, http.MethodGet, "/api/v1/orders/"+order.OrderID, "",
		map[string]string{httpapi.HeaderCustomerID: testsupport.CustomerID.String()})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner status = %d, want 200", resp.StatusCode)
	}

	resp = do(t, e, http.MethodGet, "/api/v1/orders/"+order.OrderID, "",
		map[string]string{httpapi.HeaderCustomerID: testsupport.OtherCustomerID.String()})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("other customer status = %d, want 404", resp.StatusCode)
	}
	if code := decode[errorBody](t, resp.Body).Error.Code; code != "ORDER_NOT_FOUND" {
		t.Fatalf("code = %q", code)
	}
}

func TestCustomerHistory(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	createOrder(t, e, "key-1")
	createOrder(t, e, "key-2")

	resp := do(t, e, http.MethodGet, "/api/v1/orders?limit=1", "",
		map[string]string{httpapi.HeaderCustomerID: testsupport.CustomerID.String()})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	page := decode[pageBody](t, resp.Body)
	if page.Total != 2 || len(page.Items) != 1 || page.Limit != 1 {
		t.Fatalf("page = %+v", page)
	}

	resp = do(t, e, http.MethodGet, "/api/v1/orders?limit=0", "",
		map[string]string{httpapi.HeaderCustomerID: testsupport.CustomerID.String()})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an out-of-range limit must be rejected, got %d", resp.StatusCode)
	}
}

func TestCancelOverHTTP(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	order := createOrder(t, e, "key-1")

	resp := do(t, e, http.MethodPost, "/api/v1/orders/"+order.OrderID+"/cancel", "",
		map[string]string{httpapi.HeaderCustomerID: testsupport.CustomerID.String()})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if decode[orderBody](t, resp.Body).Status != "CANCELLED" {
		t.Fatal("the order must be cancelled")
	}

	// Cancelling an already cancelled order changes nothing and is reported
	// as a replay rather than as a conflict.
	resp = do(t, e, http.MethodPost, "/api/v1/orders/"+order.OrderID+"/cancel", "",
		map[string]string{httpapi.HeaderCustomerID: testsupport.CustomerID.String()})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a repeated cancellation of a cancelled order must be idempotent, got %d",
			resp.StatusCode)
	}
	if resp.Header.Get("Idempotent-Replay") != "true" {
		t.Fatal("the repeat must be marked as a replay")
	}
}

// -------------------------------------------------------------- merchant

func TestMerchantEndpointsRequireAKey(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	order := createOrder(t, e, "key-1")
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/merchant/orders", ""},
		{http.MethodGet, "/api/v1/merchant/orders/" + order.OrderID, ""},
		{http.MethodPost, "/api/v1/merchant/orders/" + order.OrderID + "/accept", `{"eta_minutes":20}`},
		{http.MethodPost, "/api/v1/merchant/orders/" + order.OrderID + "/reject", `{"reason":"нет"}`},
		{http.MethodPost, "/api/v1/merchant/orders/" + order.OrderID + "/status", `{"status":"COOKING"}`},
	} {
		resp := do(t, e, tc.method, tc.path, tc.body, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s = %d, want 401", tc.method, tc.path, resp.StatusCode)
		}
	}
}

func TestMerchantQueueAndDecision(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	order := createOrder(t, e, "key-1")

	resp := do(t, e, http.MethodGet, "/api/v1/merchant/orders?status=CREATED", "", merchantHeaders())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("queue status = %d, want 200", resp.StatusCode)
	}
	page := decode[pageBody](t, resp.Body)
	if page.Total != 1 || page.Items[0].OrderID != order.OrderID {
		t.Fatalf("queue = %+v", page)
	}

	resp = do(t, e, http.MethodPost, "/api/v1/merchant/orders/"+order.OrderID+"/accept",
		`{"eta_minutes":25}`, merchantHeaders())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("accept status = %d, want 200", resp.StatusCode)
	}
	accepted := decode[orderBody](t, resp.Body)
	if accepted.Status != "CONFIRMED" || accepted.ETAMinutes != 25 {
		t.Fatalf("accepted = %+v", accepted)
	}

	// Repeating the confirmation changes nothing and is reported as a replay.
	resp = do(t, e, http.MethodPost, "/api/v1/merchant/orders/"+order.OrderID+"/accept",
		`{"eta_minutes":25}`, merchantHeaders())
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("repeat status = %d, replay = %q",
			resp.StatusCode, resp.Header.Get("Idempotent-Replay"))
	}

	// Refusing a confirmed order is a conflict.
	resp = do(t, e, http.MethodPost, "/api/v1/merchant/orders/"+order.OrderID+"/reject",
		`{"reason":"поздно"}`, merchantHeaders())
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("reject status = %d, want 409", resp.StatusCode)
	}
	if code := decode[errorBody](t, resp.Body).Error.Code; code != "ORDER_STATE_CONFLICT" {
		t.Fatalf("code = %q", code)
	}
}

func TestMerchantStatusProgression(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	order := createOrder(t, e, "key-1")
	do(t, e, http.MethodPost, "/api/v1/merchant/orders/"+order.OrderID+"/accept",
		`{"eta_minutes":20}`, merchantHeaders())

	for _, status := range []string{"COOKING", "DELIVERING", "DELIVERED"} {
		resp := do(t, e, http.MethodPost, "/api/v1/merchant/orders/"+order.OrderID+"/status",
			`{"status":"`+status+`"}`, merchantHeaders())
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", status, resp.StatusCode)
		}
		if decode[orderBody](t, resp.Body).Status != status {
			t.Fatalf("order did not move to %s", status)
		}
	}

	resp := do(t, e, http.MethodPost, "/api/v1/merchant/orders/"+order.OrderID+"/status",
		`{"status":"SHIPPED"}`, merchantHeaders())
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown status = %d, want 422", resp.StatusCode)
	}
}

func TestMerchantRejectRequiresAReason(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	order := createOrder(t, e, "key-1")
	resp := do(t, e, http.MethodPost, "/api/v1/merchant/orders/"+order.OrderID+"/reject",
		`{"reason":"  "}`, merchantHeaders())
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
}

func TestMerchantQueueRejectsAnUnknownStatusFilter(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	resp := do(t, e, http.MethodGet, "/api/v1/merchant/orders?status=SHIPPED", "", merchantHeaders())
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
}

func TestUnknownEndpointIs404(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	resp := do(t, e, http.MethodGet, "/api/v1/nothing", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}
