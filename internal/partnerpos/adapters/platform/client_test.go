package platform_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/internal/partnerpos/adapters/platform"
	"github.com/avito-kitchen/platform/internal/partnerpos/app"
	"github.com/avito-kitchen/platform/internal/partnerpos/domain"
)

const apiKey = "dev-merchant-key-kafe-primer-0001"

// recorded is one request as the fake platform received it.
type recorded struct {
	Method      string
	Path        string
	Query       string
	APIKey      string
	IfMatch     string
	ContentType string
	Body        map[string]any
}

// fakePlatform plays the three public APIs. Every handler answers what the
// real service answers for the happy path; status forces a failure.
type fakePlatform struct {
	mu       sync.Mutex
	requests []recorded
	status   int
	version  int64
}

func (f *fakePlatform) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.requests = append(f.requests, recorded{
		Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery,
		APIKey: r.Header.Get("X-Api-Key"), IfMatch: r.Header.Get("If-Match"),
		ContentType: r.Header.Get("Content-Type"), Body: body,
	})
	status, version := f.status, f.version
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if status != 0 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":{"code":"VERSION_CONFLICT","message":"stale"}}`)
		return
	}
	switch {
	case r.URL.Path == "/api/v1/merchant/menu" && r.Method == http.MethodGet:
		_, _ = io.WriteString(w, `{"menu_version":`+itoa(version)+`,"categories":[
		  {"items":[{"menu_item_id":"id-1","external_id":"POS-001"},{"menu_item_id":"id-2","external_id":"POS-002"}]},
		  {"items":[{"menu_item_id":"id-3","external_id":"POS-003"}]}]}`)
	case r.URL.Path == "/api/v1/merchant/menu" && r.Method == http.MethodPut:
		_, _ = io.WriteString(w, `{"menu_version":`+itoa(version+1)+`,"categories":[]}`)
	case r.URL.Path == "/api/v1/merchant/orders":
		_, _ = io.WriteString(w, `{"items":[{"order_id":"o-1","status":"CREATED","total_minor":103900,
		  "eta_minutes":35,"updated_at":"2026-10-14T12:00:00Z",
		  "items":[{"menu_item_id":"id-1","external_id":"POS-001","name":"Борщ","qty":2}]}],"total":1}`)
	default:
		_, _ = io.WriteString(w, `{}`)
	}
}

func (f *fakePlatform) last(t *testing.T) recorded {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("the platform received nothing")
	}
	return f.requests[len(f.requests)-1]
}

func (f *fakePlatform) all() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.requests...)
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func newClient(t *testing.T, f *fakePlatform) *platform.Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return platform.NewClient(platform.Config{
		CatalogBaseURL:   srv.URL + "/",
		InventoryBaseURL: srv.URL,
		OrderBaseURL:     srv.URL,
		APIKey:           apiKey,
		Timeout:          2 * time.Second,
	})
}

// Publication reads the current version first and sends it back in If-Match,
// so a menu changed by someone else is refused instead of overwritten.
func TestPublishMenuUsesTheCurrentVersionAsPrecondition(t *testing.T) {
	t.Parallel()
	f := &fakePlatform{version: 7}
	client := newClient(t, f)

	version, err := client.PublishMenu(t.Context(), domain.DefaultMenu())
	if err != nil {
		t.Fatalf("PublishMenu() = %v", err)
	}
	if version != 8 {
		t.Fatalf("version = %d, want the one the catalogue returned (8)", version)
	}
	reqs := f.all()
	if len(reqs) != 2 || reqs[0].Method != http.MethodGet || reqs[1].Method != http.MethodPut {
		t.Fatalf("requests = %+v, want GET then PUT of the menu", reqs)
	}
	put := reqs[1]
	if put.IfMatch != "7" || put.APIKey != apiKey || put.ContentType != "application/json" {
		t.Fatalf("PUT headers = %+v", put)
	}
	categories, ok := put.Body["categories"].([]any)
	if !ok || len(categories) != 2 {
		t.Fatalf("published categories = %v, want the two of the till", put.Body["categories"])
	}
	first := categories[0].(map[string]any)
	items := first["items"].([]any)
	if first["name"] != "Горячее" || len(items) != 2 ||
		items[0].(map[string]any)["external_id"] != "POS-001" {
		t.Fatalf("first category = %v", first)
	}
}

func TestFetchMenuMapsExternalIdentifiers(t *testing.T) {
	t.Parallel()
	f := &fakePlatform{version: 3}
	version, items, err := newClient(t, f).FetchMenu(t.Context())
	if err != nil {
		t.Fatalf("FetchMenu() = %v", err)
	}
	if version != 3 || len(items) != 3 {
		t.Fatalf("version %d items %+v", version, items)
	}
	if items[2] != (app.PublishedItem{ExternalID: "POS-003", MenuItemID: "id-3"}) {
		t.Fatalf("mapping = %+v", items)
	}
	if got := f.last(t); got.APIKey != apiKey || got.ContentType != "" {
		t.Fatalf("a read must carry the key and no body: %+v", got)
	}
}

// Quantity and stop list travel as two separate calls: the stop list must
// never change a quantity.
func TestStockCalls(t *testing.T) {
	t.Parallel()
	f := &fakePlatform{}
	client := newClient(t, f)

	if err := client.SetStock(t.Context(), "id/1", 12, "pos sync"); err != nil {
		t.Fatalf("SetStock() = %v", err)
	}
	put := f.last(t)
	if put.Method != http.MethodPut || put.Path != "/api/v1/merchant/stock/id%2F1" {
		t.Fatalf("SetStock request = %+v, want PUT with an escaped id", put)
	}
	if put.Body["on_hand_qty"] != float64(12) || put.Body["reason"] != "pos sync" {
		t.Fatalf("SetStock body = %v", put.Body)
	}

	for stopped, want := range map[bool]string{true: "STOPPED", false: "ENABLED"} {
		if err := client.SetSalesState(t.Context(), "id-1", stopped); err != nil {
			t.Fatalf("SetSalesState() = %v", err)
		}
		patch := f.last(t)
		if patch.Method != http.MethodPatch || patch.Body["sales_state"] != want {
			t.Fatalf("SetSalesState(%v) = %+v", stopped, patch)
		}
		if _, ok := patch.Body["on_hand_qty"]; ok {
			t.Fatal("the stop list must not carry a quantity")
		}
	}
}

func TestOrderCalls(t *testing.T) {
	t.Parallel()
	f := &fakePlatform{}
	client := newClient(t, f)

	orders, err := client.ListOrders(t.Context(), domain.WatchedStatuses(), 50)
	if err != nil {
		t.Fatalf("ListOrders() = %v", err)
	}
	list := f.last(t)
	if list.Path != "/api/v1/merchant/orders" ||
		list.Query != "limit=50&status=CREATED%2CCONFIRMED%2CCOOKING%2CDELIVERING" {
		t.Fatalf("ListOrders request = %+v", list)
	}
	if len(orders) != 1 || orders[0].ID != "o-1" || orders[0].Status != "CREATED" ||
		orders[0].UpdatedAt != "2026-10-14T12:00:00Z" || len(orders[0].Items) != 1 ||
		orders[0].Items[0].Qty != 2 {
		t.Fatalf("orders = %+v", orders)
	}

	if err := client.AcceptOrder(t.Context(), "o-1", 25); err != nil {
		t.Fatalf("AcceptOrder() = %v", err)
	}
	if got := f.last(t); got.Method != http.MethodPost ||
		got.Path != "/api/v1/merchant/orders/o-1/accept" || got.Body["eta_minutes"] != float64(25) {
		t.Fatalf("AcceptOrder request = %+v", got)
	}
	if err := client.RejectOrder(t.Context(), "o-1", "нет продуктов"); err != nil {
		t.Fatalf("RejectOrder() = %v", err)
	}
	if got := f.last(t); got.Path != "/api/v1/merchant/orders/o-1/reject" || got.Body["reason"] != "нет продуктов" {
		t.Fatalf("RejectOrder request = %+v", got)
	}
	if err := client.AdvanceOrder(t.Context(), "o-1", domain.StatusCooking); err != nil {
		t.Fatalf("AdvanceOrder() = %v", err)
	}
	if got := f.last(t); got.Path != "/api/v1/merchant/orders/o-1/status" || got.Body["status"] != "COOKING" {
		t.Fatalf("AdvanceOrder request = %+v", got)
	}
}

// The till has no business reading of a refusal: every failure is
// ErrPlatformUnavailable, named by the call, and retried on the next tick.
func TestFailuresAreReportedAsAnUnavailablePlatform(t *testing.T) {
	t.Parallel()
	f := &fakePlatform{status: http.StatusPreconditionFailed}
	client := newClient(t, f)

	_, err := client.PublishMenu(t.Context(), domain.DefaultMenu())
	if !errors.Is(err, app.ErrPlatformUnavailable) {
		t.Fatalf("PublishMenu() = %v, want ErrPlatformUnavailable", err)
	}
	if !strings.Contains(err.Error(), "status 412") || !strings.Contains(err.Error(), "catalog") {
		t.Fatalf("error = %q, want the call and the status named", err)
	}

	unreachable := platform.NewClient(platform.Config{
		CatalogBaseURL: "http://127.0.0.1:1", InventoryBaseURL: "http://127.0.0.1:1",
		OrderBaseURL: "http://127.0.0.1:1", APIKey: apiKey, Timeout: time.Second,
	})
	if err := unreachable.SetStock(t.Context(), "id-1", 1, "x"); !errors.Is(err, app.ErrPlatformUnavailable) {
		t.Fatalf("SetStock(unreachable) = %v, want ErrPlatformUnavailable", err)
	}
	if _, err := unreachable.ListOrders(t.Context(), nil, 10); !errors.Is(err, app.ErrPlatformUnavailable) {
		t.Fatalf("ListOrders(unreachable) = %v, want ErrPlatformUnavailable", err)
	}
}

func TestAMalformedAnswerIsAFailure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"menu_version":`)
	}))
	t.Cleanup(srv.Close)
	client := platform.NewClient(platform.Config{CatalogBaseURL: srv.URL, APIKey: apiKey})
	if _, _, err := client.FetchMenu(t.Context()); !errors.Is(err, app.ErrPlatformUnavailable) {
		t.Fatalf("FetchMenu(malformed) = %v, want ErrPlatformUnavailable", err)
	}
}
