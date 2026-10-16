package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/internal/partnerpos/adapters/httpapi"
	"github.com/avito-kitchen/platform/internal/partnerpos/app"
	"github.com/avito-kitchen/platform/internal/partnerpos/domain"
	"github.com/avito-kitchen/platform/internal/partnerpos/testsupport"
)

type env struct {
	srv      *httptest.Server
	platform *testsupport.Platform
	svc      *app.Service
}

func newEnv(t *testing.T) env {
	t.Helper()
	pf := testsupport.NewPlatform()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := app.NewService(pf, domain.DefaultMenu(), logger, app.Config{
		StockSyncInterval: time.Hour, PollInterval: time.Hour,
		KitchenStep: time.Minute, OrderPageLimit: 50,
	})
	srv := httptest.NewServer(httpapi.NewHandler(svc, logger).Routes())
	t.Cleanup(srv.Close)
	return env{srv: srv, platform: pf, svc: svc}
}

// response is an answer read in full: do drains and closes the body itself,
// so a test never keeps a connection open and has nothing left to close.
type response struct {
	StatusCode int
	Header     http.Header
	Body       *bytes.Reader
}

func do(t *testing.T, e env, method, path, body string) response {
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
		t.Fatalf("decode: %v", err)
	}
	return out
}

type menuBody struct {
	Published   bool   `json:"published"`
	MenuVersion int64  `json:"menu_version"`
	Currency    string `json:"currency"`
	Items       []struct {
		ExternalID   string `json:"external_id"`
		MenuItemID   string `json:"menu_item_id"`
		PriceMinor   int64  `json:"price_minor"`
		OnHandQty    int32  `json:"on_hand_qty"`
		SalesStopped bool   `json:"sales_stopped"`
	} `json:"items"`
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Details []struct {
			Field  string `json:"field"`
			Reason string `json:"reason"`
		} `json:"details"`
	} `json:"error"`
}

func TestMenuIsReadableBeforePublication(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	resp := do(t, e, http.MethodGet, "/pos/menu", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	menu := decode[menuBody](t, resp.Body)
	if menu.Published || len(menu.Items) != 3 || menu.Currency != "RUB" {
		t.Fatalf("menu = %+v", menu)
	}
}

func TestPublishFillsTheIdentifiersAndPushesStock(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	resp := do(t, e, http.MethodPost, "/pos/publish", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	published := decode[struct {
		MenuVersion int64 `json:"menu_version"`
		Items       int   `json:"items"`
		Matched     int   `json:"matched"`
	}](t, resp.Body)
	if published.Items != 3 || published.Matched != 3 {
		t.Fatalf("publish = %+v", published)
	}
	if stock, ok := e.platform.Stock(testsupport.MenuItemSellable); !ok || stock.Qty != 10 {
		t.Fatalf("stock = %+v, ok=%v", stock, ok)
	}
	menu := decode[menuBody](t, do(t, e, http.MethodGet, "/pos/menu", "").Body)
	if !menu.Published || menu.Items[0].MenuItemID == "" {
		t.Fatalf("menu = %+v", menu)
	}
}

func TestPatchItem(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	resp := do(t, e, http.MethodPatch, "/pos/menu/POS-001", `{"price_minor":43000,"on_hand_qty":7}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	item := decode[struct {
		PriceMinor int64 `json:"price_minor"`
		OnHandQty  int32 `json:"on_hand_qty"`
	}](t, resp.Body)
	if item.PriceMinor != 43000 || item.OnHandQty != 7 {
		t.Fatalf("item = %+v", item)
	}

	if resp := do(t, e, http.MethodPatch, "/pos/menu/NOPE", `{"price_minor":1000}`); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown position = %d, want 404", resp.StatusCode)
	}
	resp = do(t, e, http.MethodPatch, "/pos/menu/POS-001", `{}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("empty patch = %d, want 422", resp.StatusCode)
	}
	if len(decode[errorBody](t, resp.Body).Error.Details) == 0 {
		t.Fatal("a rejected patch must say which field is wrong")
	}
	if resp := do(t, e, http.MethodPatch, "/pos/menu/POS-001", `{"price_minor":`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed body = %d, want 400", resp.StatusCode)
	}
	if resp := do(t, e, http.MethodPatch, "/pos/menu/POS-001", `{"nope":1}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field = %d, want 400", resp.StatusCode)
	}
}

func TestBehaviorIsSwitchableAndValidated(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	resp := do(t, e, http.MethodPut, "/pos/behavior",
		`{"mode":"AUTO_REJECT","eta_minutes":15,"reject_reason":"нет продуктов","kitchen":false}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if e.svc.Behavior().Mode != domain.ModeAutoReject {
		t.Fatalf("behaviour = %+v", e.svc.Behavior())
	}
	if resp := do(t, e, http.MethodPut, "/pos/behavior", `{"mode":"NOPE"}`); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unknown mode = %d, want 422", resp.StatusCode)
	}
	resp = do(t, e, http.MethodPut, "/pos/behavior", `{"mode":"AUTO_REJECT","eta_minutes":10}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("refusal without a reason = %d, want 422", resp.StatusCode)
	}
}

func TestOrdersMirrorTheQueue(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.platform.SetOrders(app.Order{
		ID: "o-1", Status: domain.StatusCreated, TotalMinor: 61900,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Items: []app.OrderItem{{
			MenuItemID: testsupport.MenuItemSellable, ExternalID: "POS-001",
			Name: "Борщ с говядиной", Qty: 1,
		}},
	})
	resp := do(t, e, http.MethodGet, "/pos/orders", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	page := decode[struct {
		Total int `json:"total"`
		Items []struct {
			OrderID string `json:"order_id"`
			Status  string `json:"status"`
		} `json:"items"`
	}](t, resp.Body)
	if page.Total != 1 || page.Items[0].OrderID != "o-1" {
		t.Fatalf("page = %+v", page)
	}
}

// An outage of the platform is reported as 503, never as a business answer.
func TestPlatformOutageIs503(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.platform.ListErr = app.Unavailable("order list queue", io.ErrUnexpectedEOF)
	if resp := do(t, e, http.MethodGet, "/pos/orders", ""); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestUnknownEndpointIs404(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	if resp := do(t, e, http.MethodGet, "/pos/nothing", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
