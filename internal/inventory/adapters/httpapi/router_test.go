package httpapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/internal/inventory/adapters/httpapi"
	"github.com/avito-kitchen/platform/internal/inventory/app"
	"github.com/avito-kitchen/platform/internal/inventory/domain"
	"github.com/avito-kitchen/platform/internal/inventory/testsupport"
	"github.com/google/uuid"
)

type testEnv struct {
	srv   *httptest.Server
	stock *testsupport.StockRepo
	dir   *testsupport.MerchantDirectory
	svc   *app.Service
}

func newEnv(t *testing.T) testEnv {
	t.Helper()
	stock := testsupport.NewStockRepo()
	dir := testsupport.NewMerchantDirectory()
	svc := app.NewService(stock, testsupport.NewReservationRepo(stock), dir, app.Config{
		DefaultReservationTTL: time.Minute,
		MaxReservationTTL:     time.Hour,
		MaxAvailabilityBatch:  10,
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpapi.NewHandler(svc, logger).Routes())
	t.Cleanup(srv.Close)
	return testEnv{srv: srv, stock: stock, dir: dir, svc: svc}
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

func do(t *testing.T, e testEnv, method, path, body string, headers map[string]string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, reader)
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
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func decode[T any](t *testing.T, r io.Reader) T {
	t.Helper()
	var out T
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return out
}

func auth() map[string]string {
	return map[string]string{httpapi.HeaderAPIKey: testsupport.DevKey}
}

func TestGetAvailabilityIsPublic(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	path := "/api/v1/merchants/" + testsupport.MerchantID.String() +
		"/availability?menu_item_ids=" + testsupport.ItemSellable.String() +
		"," + testsupport.ItemEnabledEmpty.String() +
		"," + testsupport.ItemStopped.String()
	resp := do(t, e, http.MethodGet, path, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	payload := decode[struct {
		MerchantID string `json:"merchant_id"`
		Items      []struct {
			MenuItemID   string `json:"menu_item_id"`
			Tracked      bool   `json:"tracked"`
			SalesState   string `json:"sales_state"`
			AvailableQty int32  `json:"available_qty"`
			Sellable     bool   `json:"sellable"`
		} `json:"items"`
	}](t, resp.Body)

	if len(payload.Items) != 3 {
		t.Fatalf("got %d items, want 3", len(payload.Items))
	}
	byID := map[string]bool{}
	qty := map[string]int32{}
	for _, item := range payload.Items {
		byID[item.MenuItemID] = item.Sellable
		qty[item.MenuItemID] = item.AvailableQty
	}
	if !byID[testsupport.ItemSellable.String()] {
		t.Fatal("item with units is not reported as sellable")
	}
	// Enabled with zero units must not be sellable.
	if byID[testsupport.ItemEnabledEmpty.String()] || qty[testsupport.ItemEnabledEmpty.String()] != 0 {
		t.Fatal("an enabled but empty card was reported as sellable")
	}
	if byID[testsupport.ItemStopped.String()] {
		t.Fatal("a stop-listed card was reported as sellable")
	}
}

func TestGetAvailabilityValidation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	base := "/api/v1/merchants/" + testsupport.MerchantID.String() + "/availability"

	cases := map[string]struct {
		path string
		want int
	}{
		"missing ids":     {base, http.StatusUnprocessableEntity},
		"non-uuid id":     {base + "?menu_item_ids=nope", http.StatusUnprocessableEntity},
		"bad merchant id": {"/api/v1/merchants/nope/availability?menu_item_ids=" + uuid.NewString(), http.StatusUnprocessableEntity},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resp := do(t, e, http.MethodGet, tc.path, "", nil)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			if code := decode[errorBody](t, resp.Body).Error.Code; code != "VALIDATION_FAILED" {
				t.Fatalf("code = %q", code)
			}
		})
	}
}

func TestMerchantEndpointsRequireKey(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	item := testsupport.ItemSellable.String()

	endpoints := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/merchant/stock", ""},
		{http.MethodGet, "/api/v1/merchant/stock/" + item, ""},
		{http.MethodPut, "/api/v1/merchant/stock/" + item, `{"on_hand_qty":1}`},
		{http.MethodPatch, "/api/v1/merchant/stock/" + item, `{"sales_state":"ENABLED"}`},
		{http.MethodPost, "/api/v1/merchant/stock/" + item + "/adjust", `{"delta":1,"operation_key":"k"}`},
		{http.MethodGet, "/api/v1/merchant/stock/" + item + "/movements", ""},
	}
	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			t.Parallel()
			resp := do(t, e, ep.method, ep.path, ep.body, nil)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}
			if code := decode[errorBody](t, resp.Body).Error.Code; code != "UNAUTHORIZED" {
				t.Fatalf("code = %q", code)
			}
		})
	}
}

// An outage of merchant-service must surface as 503, not as 401: telling a
// merchant its key is invalid during our own outage would be false.
func TestMerchantDirectoryOutageIs503(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.dir.Err = app.ErrMerchantDirectoryUnavailable

	resp := do(t, e, http.MethodGet, "/api/v1/merchant/stock", "", auth())
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if code := decode[errorBody](t, resp.Body).Error.Code; code != "DEPENDENCY_UNAVAILABLE" {
		t.Fatalf("code = %q", code)
	}
}

func TestListStock(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	resp := do(t, e, http.MethodGet, "/api/v1/merchant/stock?limit=2", "", auth())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	page := decode[struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
		Limit int              `json:"limit"`
	}](t, resp.Body)
	if page.Total != 3 || page.Limit != 2 || len(page.Items) != 2 {
		t.Fatalf("page = %+v", page)
	}

	resp = do(t, e, http.MethodGet, "/api/v1/merchant/stock?limit=999", "", auth())
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("limit above the maximum: status = %d, want 422", resp.StatusCode)
	}
}

func TestSetOnHand(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	path := "/api/v1/merchant/stock/" + testsupport.ItemEnabledEmpty.String()

	resp := do(t, e, http.MethodPut, path, `{"on_hand_qty":12,"reason":"stocktake"}`, auth())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if resp.Header.Get("ETag") == "" {
		t.Fatal("ETag is missing")
	}
	item := decode[map[string]any](t, resp.Body)
	if item["on_hand_qty"] != float64(12) || item["sellable"] != true {
		t.Fatalf("payload = %v", item)
	}

	cases := map[string]struct {
		body   string
		status int
		code   string
	}{
		"missing field":  {`{}`, http.StatusUnprocessableEntity, "VALIDATION_FAILED"},
		"negative":       {`{"on_hand_qty":-1}`, http.StatusUnprocessableEntity, "VALIDATION_FAILED"},
		"unknown field":  {`{"nope":1}`, http.StatusBadRequest, "VALIDATION_FAILED"},
		"malformed json": {`{`, http.StatusBadRequest, "VALIDATION_FAILED"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resp := do(t, e, http.MethodPut, path, tc.body, auth())
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.status)
			}
			if code := decode[errorBody](t, resp.Body).Error.Code; code != tc.code {
				t.Fatalf("code = %q, want %q", code, tc.code)
			}
		})
	}
}

func TestSetOnHandIfMatch(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	path := "/api/v1/merchant/stock/" + testsupport.ItemSellable.String()

	stale := map[string]string{httpapi.HeaderAPIKey: testsupport.DevKey, "If-Match": `"99"`}
	resp := do(t, e, http.MethodPut, path, `{"on_hand_qty":5}`, stale)
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412", resp.StatusCode)
	}
	if code := decode[errorBody](t, resp.Body).Error.Code; code != "VERSION_CONFLICT" {
		t.Fatalf("code = %q", code)
	}

	fresh := map[string]string{httpapi.HeaderAPIKey: testsupport.DevKey, "If-Match": `"1"`}
	if resp := do(t, e, http.MethodPut, path, `{"on_hand_qty":5}`, fresh); resp.StatusCode != http.StatusOK {
		t.Fatalf("fresh version: status = %d, want 200", resp.StatusCode)
	}

	bad := map[string]string{httpapi.HeaderAPIKey: testsupport.DevKey, "If-Match": `"abc"`}
	if resp := do(t, e, http.MethodPut, path, `{"on_hand_qty":5}`, bad); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("malformed If-Match: status = %d, want 422", resp.StatusCode)
	}
}

func TestSetOnHandBelowReservedIsRejected(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	if _, err := e.svc.Reserve(t.Context(), uuid.New(), testsupport.MerchantID,
		[]domain.ReserveLine{{MenuItemID: testsupport.ItemSellable, Qty: 6}}, time.Minute); err != nil {
		t.Fatalf("Reserve() = %v", err)
	}

	path := "/api/v1/merchant/stock/" + testsupport.ItemSellable.String()
	resp := do(t, e, http.MethodPut, path, `{"on_hand_qty":3}`, auth())
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if code := decode[errorBody](t, resp.Body).Error.Code; code != "BELOW_RESERVED" {
		t.Fatalf("code = %q", code)
	}
	item, _ := e.stock.Snapshot(testsupport.ItemSellable)
	if item.OnHandQty != 10 || item.ReservedQty != 6 {
		t.Fatalf("state changed on a rejected request: %+v", item)
	}
}

func TestAdjustOnHand(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	path := "/api/v1/merchant/stock/" + testsupport.ItemSellable.String() + "/adjust"

	resp := do(t, e, http.MethodPost, path, `{"delta":5,"operation_key":"delivery-42","reason":"delivery"}`, auth())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	first := decode[map[string]any](t, resp.Body)
	if first["on_hand_qty"] != float64(15) || first["duplicate"] != false {
		t.Fatalf("payload = %v", first)
	}

	resp = do(t, e, http.MethodPost, path, `{"delta":5,"operation_key":"delivery-42","reason":"delivery"}`, auth())
	repeat := decode[map[string]any](t, resp.Body)
	if repeat["on_hand_qty"] != float64(15) || repeat["duplicate"] != true {
		t.Fatalf("repeat payload = %v, want the stored result", repeat)
	}

	resp = do(t, e, http.MethodPost, path, `{"delta":-100,"operation_key":"writeoff-1"}`, auth())
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("over-write-off: status = %d, want 409", resp.StatusCode)
	}
	if code := decode[errorBody](t, resp.Body).Error.Code; code != "NEGATIVE_ON_HAND" {
		t.Fatalf("code = %q", code)
	}

	resp = do(t, e, http.MethodPost, path, `{"delta":1}`, auth())
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("missing operation key: status = %d, want 422", resp.StatusCode)
	}
}

func TestSetSalesState(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	path := "/api/v1/merchant/stock/" + testsupport.ItemStopped.String()

	before, _ := e.stock.Snapshot(testsupport.ItemStopped)
	resp := do(t, e, http.MethodPatch, path, `{"sales_state":"ENABLED"}`, auth())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	item := decode[map[string]any](t, resp.Body)
	if item["sales_state"] != "ENABLED" {
		t.Fatalf("payload = %v", item)
	}
	// The switch must not confirm units.
	if item["on_hand_qty"] != float64(before.OnHandQty) {
		t.Fatalf("quantity changed: %v vs %d", item["on_hand_qty"], before.OnHandQty)
	}

	if resp := do(t, e, http.MethodPatch, path, `{"sales_state":"PAUSED"}`, auth()); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unknown state: status = %d, want 422", resp.StatusCode)
	}
}

func TestMovementsJournal(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	item := testsupport.ItemSellable.String()

	do(t, e, http.MethodPut, "/api/v1/merchant/stock/"+item, `{"on_hand_qty":20,"reason":"stocktake"}`, auth())
	do(t, e, http.MethodPost, "/api/v1/merchant/stock/"+item+"/adjust", `{"delta":-2,"operation_key":"spoilage-1"}`, auth())

	resp := do(t, e, http.MethodGet, "/api/v1/merchant/stock/"+item+"/movements", "", auth())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	page := decode[struct {
		Items []struct {
			Kind        string `json:"kind"`
			DeltaOnHand int32  `json:"delta_on_hand"`
			OnHandAfter int32  `json:"on_hand_after"`
		} `json:"items"`
		Total int `json:"total"`
	}](t, resp.Body)
	if page.Total != 2 {
		t.Fatalf("total = %d, want 2", page.Total)
	}
	// Newest first.
	if page.Items[0].Kind != "ADJUST" || page.Items[0].DeltaOnHand != -2 || page.Items[0].OnHandAfter != 18 {
		t.Fatalf("newest movement = %+v", page.Items[0])
	}
	if page.Items[1].Kind != "SET" {
		t.Fatalf("older movement = %+v", page.Items[1])
	}
}

func TestUnknownItemAndEndpoint(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	resp := do(t, e, http.MethodGet,
		"/api/v1/merchant/stock/"+uuid.NewString()+"/movements", "", auth())
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if code := decode[errorBody](t, resp.Body).Error.Code; code != "STOCK_ITEM_NOT_FOUND" {
		t.Fatalf("code = %q", code)
	}

	if resp := do(t, e, http.MethodGet, "/api/v1/nothing", "", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown endpoint: status = %d, want 404", resp.StatusCode)
	}
	resp = do(t, e, http.MethodGet,
		"/api/v1/merchant/stock/not-a-uuid", "", auth())
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("non-uuid path: status = %d, want 422", resp.StatusCode)
	}
}
