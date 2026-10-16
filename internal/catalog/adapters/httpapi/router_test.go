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

	"github.com/avito-kitchen/platform/internal/catalog/adapters/httpapi"
	"github.com/avito-kitchen/platform/internal/catalog/app"
	"github.com/avito-kitchen/platform/internal/catalog/testsupport"
	"github.com/google/uuid"
)

type testEnv struct {
	srv   *httptest.Server
	menus *testsupport.MenuRepo
	dir   *testsupport.MerchantDirectory
}

func newEnv(t *testing.T) testEnv {
	t.Helper()
	menus := testsupport.NewMenuRepo()
	dir := testsupport.NewMerchantDirectory()
	svc := app.NewService(menus, dir, app.Config{MaxQuoteBatch: 10})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpapi.NewHandler(svc, logger).Routes())
	t.Cleanup(srv.Close)
	return testEnv{srv: srv, menus: menus, dir: dir}
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

type menuBody struct {
	MerchantID  string `json:"merchant_id"`
	MenuVersion int64  `json:"menu_version"`
	Currency    string `json:"currency"`
	Categories  []struct {
		Name     string `json:"name"`
		Position int32  `json:"position"`
		Items    []struct {
			MenuItemID string `json:"menu_item_id"`
			ExternalID string `json:"external_id"`
			Name       string `json:"name"`
			PriceMinor int64  `json:"price_minor"`
			Currency   string `json:"currency"`
		} `json:"items"`
	} `json:"categories"`
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Withdrawn int `json:"withdrawn"`
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

func auth() map[string]string {
	return map[string]string{httpapi.HeaderAPIKey: testsupport.DevKey}
}

func TestPublicMenuIsAvailableWithoutAKey(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	resp := do(t, e, http.MethodGet, "/api/v1/merchants/"+testsupport.MerchantID.String()+"/menu", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if etag := resp.Header.Get("ETag"); etag != `"1"` {
		t.Fatalf("ETag = %q, want the menu version", etag)
	}
	menu := decode[menuBody](t, resp.Body)
	if menu.MenuVersion != 1 || menu.Currency != "RUB" {
		t.Fatalf("menu = %+v", menu)
	}
	if len(menu.Categories) != 2 {
		t.Fatalf("got %d categories, want 2", len(menu.Categories))
	}
	first := menu.Categories[0]
	if first.Name != "Горячее" || len(first.Items) != 2 {
		t.Fatalf("first category = %+v", first)
	}
	if first.Items[0].ExternalID != testsupport.ExternalSellable {
		t.Fatalf("items are not in display order: %+v", first.Items)
	}
}

func TestPublicMenuOfAnUnknownMerchantIs404(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	resp := do(t, e, http.MethodGet, "/api/v1/merchants/"+uuid.NewString()+"/menu", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if code := decode[errorBody](t, resp.Body).Error.Code; code != "MENU_NOT_FOUND" {
		t.Fatalf("code = %q, want MENU_NOT_FOUND", code)
	}
}

func TestPublicMenuRejectsANonUUIDMerchant(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	resp := do(t, e, http.MethodGet, "/api/v1/merchants/not-a-uuid/menu", "", nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
}

func TestMerchantEndpointsRequireAKey(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/merchant/menu", ""},
		{http.MethodPut, "/api/v1/merchant/menu", `{"categories":[]}`},
		{http.MethodPatch, "/api/v1/merchant/menu/items/" + testsupport.ItemSellable.String(), `{"name":"x"}`},
	} {
		resp := do(t, e, tc.method, tc.path, tc.body, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", tc.method, tc.path, resp.StatusCode)
		}
	}
}

func TestUnknownKeyIs401AndOutageIs503(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	resp := do(t, e, http.MethodGet, "/api/v1/merchant/menu", "",
		map[string]string{httpapi.HeaderAPIKey: "nope"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown key status = %d, want 401", resp.StatusCode)
	}

	e.dir.Err = app.ErrMerchantDirectoryUnavailable
	resp = do(t, e, http.MethodGet, "/api/v1/merchant/menu", "", auth())
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("directory outage status = %d, want 503", resp.StatusCode)
	}
	if code := decode[errorBody](t, resp.Body).Error.Code; code != "DEPENDENCY_UNAVAILABLE" {
		t.Fatalf("code = %q, want DEPENDENCY_UNAVAILABLE", code)
	}
}

func TestPublishReplacesTheMenu(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	body := `{"categories":[{"name":"Горячее","position":0,"items":[
        {"external_id":"POS-001","name":"Борщ","description":"","price_minor":43000,"position":0},
        {"external_id":"POS-009","name":"Солянка","description":"","price_minor":51000,"position":1}]}]}`

	resp := do(t, e, http.MethodPut, "/api/v1/merchant/menu", body,
		merge(auth(), map[string]string{"If-Match": `"1"`}))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	published := decode[menuBody](t, resp.Body)
	if published.MenuVersion != 2 {
		t.Fatalf("menu version = %d, want 2", published.MenuVersion)
	}
	if published.Created != 1 || published.Updated != 1 || published.Withdrawn != 2 {
		t.Fatalf("created=%d updated=%d withdrawn=%d, want 1/1/2",
			published.Created, published.Updated, published.Withdrawn)
	}
	if published.Categories[0].Items[0].MenuItemID != testsupport.ItemSellable.String() {
		t.Fatalf("a republished item must keep its identifier: %+v", published.Categories[0].Items[0])
	}
}

func TestPublishWithAStaleIfMatchIs412(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	body := `{"categories":[{"name":"Горячее","items":[
        {"external_id":"POS-001","name":"Борщ","price_minor":43000}]}]}`
	resp := do(t, e, http.MethodPut, "/api/v1/merchant/menu", body,
		merge(auth(), map[string]string{"If-Match": `"99"`}))
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412", resp.StatusCode)
	}
	if code := decode[errorBody](t, resp.Body).Error.Code; code != "VERSION_CONFLICT" {
		t.Fatalf("code = %q, want VERSION_CONFLICT", code)
	}
}

func TestPublishRejectsAnInvalidSnapshot(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	body := `{"categories":[{"name":"Горячее","items":[
        {"external_id":"POS-001","name":"Борщ","price_minor":0}]}]}`
	resp := do(t, e, http.MethodPut, "/api/v1/merchant/menu", body, auth())
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	details := decode[errorBody](t, resp.Body).Error.Details
	if len(details) == 0 {
		t.Fatal("a rejected snapshot must report which field is wrong")
	}
}

func TestPublishRejectsAMalformedBody(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	resp := do(t, e, http.MethodPut, "/api/v1/merchant/menu", `{"categories":`, auth())
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed JSON status = %d, want 400", resp.StatusCode)
	}

	resp = do(t, e, http.MethodPut, "/api/v1/merchant/menu", `{"unknown":1}`, auth())
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, want 400", resp.StatusCode)
	}
}

func TestPublishRejectsAMalformedIfMatch(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	body := `{"categories":[{"name":"Горячее","items":[
        {"external_id":"POS-001","name":"Борщ","price_minor":1000}]}]}`
	resp := do(t, e, http.MethodPut, "/api/v1/merchant/menu", body,
		merge(auth(), map[string]string{"If-Match": `"later"`}))
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
}

func TestGetOwnMenuBeforeTheFirstPublication(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	other := uuid.New()
	e.dir.Keys["fresh"] = app.MerchantIdentity{MerchantID: other, Active: true}

	resp := do(t, e, http.MethodGet, "/api/v1/merchant/menu", "",
		map[string]string{httpapi.HeaderAPIKey: "fresh"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	menu := decode[menuBody](t, resp.Body)
	if menu.MenuVersion != 0 || len(menu.Categories) != 0 {
		t.Fatalf("menu = %+v, want an empty menu at version 0", menu)
	}
}

func TestPatchItemChangesThePriceAndBumpsTheVersion(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	resp := do(t, e, http.MethodPatch,
		"/api/v1/merchant/menu/items/"+testsupport.ItemSellable.String(),
		`{"price_minor":44500}`, merge(auth(), map[string]string{"If-Match": `"1"`}))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	payload := decode[struct {
		MenuItemID  string `json:"menu_item_id"`
		PriceMinor  int64  `json:"price_minor"`
		MenuVersion int64  `json:"menu_version"`
	}](t, resp.Body)
	if payload.PriceMinor != 44500 {
		t.Fatalf("price = %d, want 44500", payload.PriceMinor)
	}
	if payload.MenuVersion != 2 {
		t.Fatalf("menu version = %d, want 2", payload.MenuVersion)
	}
	if resp.Header.Get("ETag") != `"2"` {
		t.Fatalf("ETag = %q, want the new menu version", resp.Header.Get("ETag"))
	}
}

func TestPatchUnknownItemIs404(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	resp := do(t, e, http.MethodPatch,
		"/api/v1/merchant/menu/items/"+uuid.NewString(), `{"price_minor":1000}`, auth())
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if code := decode[errorBody](t, resp.Body).Error.Code; code != "MENU_ITEM_NOT_FOUND" {
		t.Fatalf("code = %q, want MENU_ITEM_NOT_FOUND", code)
	}
}

func TestPatchWithoutChangesIs422(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	resp := do(t, e, http.MethodPatch,
		"/api/v1/merchant/menu/items/"+testsupport.ItemSellable.String(), `{}`, auth())
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

func merge(base, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
