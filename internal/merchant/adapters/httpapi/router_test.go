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

	"github.com/avito-kitchen/platform/internal/merchant/adapters/httpapi"
	"github.com/avito-kitchen/platform/internal/merchant/app"
	"github.com/avito-kitchen/platform/internal/merchant/domain"
	"github.com/avito-kitchen/platform/internal/merchant/testsupport"
)

func newTestServer(t *testing.T) (*httptest.Server, *testsupport.MerchantRepo) {
	t.Helper()
	active := testsupport.SampleMerchant()
	suspended := testsupport.SuspendedMerchant()
	repo := testsupport.NewMerchantRepo(active, suspended)
	keys := testsupport.NewAPIKeyRepo(map[string]domain.Merchant{testsupport.DevKey: active})
	svc := app.NewService(repo, keys, 100)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpapi.NewHandler(svc, logger).Routes())
	t.Cleanup(srv.Close)
	return srv, repo
}

func decodeError(t *testing.T, body io.Reader) struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details []struct {
			Field  string `json:"field"`
			Reason string `json:"reason"`
		} `json:"details"`
	} `json:"error"`
} {
	t.Helper()
	var out struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details []struct {
				Field  string `json:"field"`
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return out
}

// response is an answer read in full: do drains and closes the body itself,
// so a test never keeps a connection open and has nothing left to close.
type response struct {
	StatusCode int
	Header     http.Header
	Body       *bytes.Reader
}

func do(t *testing.T, srv *httptest.Server, method, path, body string, headers map[string]string) response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
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

func TestListMerchants(t *testing.T) {
	t.Parallel()
	srv, repo := newTestServer(t)

	resp := do(t, srv, http.MethodGet, "/api/v1/merchants?city=Санкт-Петербург&limit=5&offset=0", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var page struct {
		Items  []map[string]any `json:"items"`
		Total  int              `json:"total"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Limit != 5 {
		t.Fatalf("limit = %d, want 5", page.Limit)
	}
	if repo.LastFilter.City != "Санкт-Петербург" {
		t.Fatalf("city filter = %q", repo.LastFilter.City)
	}
}

func TestListMerchantsRejectsBadPagination(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)

	cases := map[string]string{
		"limit above maximum": "/api/v1/merchants?limit=1000",
		"limit not a number":  "/api/v1/merchants?limit=abc",
		"negative offset":     "/api/v1/merchants?offset=-1",
		"bad only_accepting":  "/api/v1/merchants?only_accepting=maybe",
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resp := do(t, srv, http.MethodGet, path, "", nil)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", resp.StatusCode)
			}
			if got := decodeError(t, resp.Body).Error.Code; got != "VALIDATION_FAILED" {
				t.Fatalf("code = %q", got)
			}
		})
	}
}

func TestGetMerchant(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	active := testsupport.SampleMerchant()

	resp := do(t, srv, http.MethodGet, "/api/v1/merchants/"+active.ID.String(), "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m["id"] != active.ID.String() || m["can_accept_orders"] != true || m["currency"] != "RUB" {
		t.Fatalf("payload = %v", m)
	}
}

func TestGetMerchantHidesSuspended(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	suspended := testsupport.SuspendedMerchant()

	resp := do(t, srv, http.MethodGet, "/api/v1/merchants/"+suspended.ID.String(), "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a suspended merchant", resp.StatusCode)
	}
	if got := decodeError(t, resp.Body).Error.Code; got != "MERCHANT_NOT_FOUND" {
		t.Fatalf("code = %q", got)
	}
}

func TestGetMerchantRejectsNonUUID(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	resp := do(t, srv, http.MethodGet, "/api/v1/merchants/not-a-uuid", "", nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
}

func TestMerchantProfileRequiresKey(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)

	for name, headers := range map[string]map[string]string{
		"missing key": nil,
		"unknown key": {httpapi.HeaderAPIKey: "dev-merchant-key-unknown-00000000"},
		"short key":   {httpapi.HeaderAPIKey: "abc"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resp := do(t, srv, http.MethodGet, "/api/v1/merchant/profile", "", headers)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}
			if got := decodeError(t, resp.Body).Error.Code; got != "UNAUTHORIZED" {
				t.Fatalf("code = %q", got)
			}
		})
	}
}

func TestGetProfileReturnsETag(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	resp := do(t, srv, http.MethodGet, "/api/v1/merchant/profile", "",
		map[string]string{httpapi.HeaderAPIKey: testsupport.DevKey})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if resp.Header.Get("ETag") != `"1"` {
		t.Fatalf("ETag = %q, want \"1\"", resp.Header.Get("ETag"))
	}
}

func TestPatchProfile(t *testing.T) {
	t.Parallel()
	srv, repo := newTestServer(t)
	auth := map[string]string{httpapi.HeaderAPIKey: testsupport.DevKey}

	resp := do(t, srv, http.MethodPatch, "/api/v1/merchant/profile",
		`{"is_accepting_orders":false,"min_order_minor":70000}`, auth)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m["is_accepting_orders"] != false || m["min_order_minor"] != float64(70000) {
		t.Fatalf("payload = %v", m)
	}
	if m["version"] != float64(2) {
		t.Fatalf("version = %v, want 2", m["version"])
	}
	stored := repo.Items[testsupport.SampleMerchant().ID]
	if stored.AvgPrepMinutes != 35 {
		t.Fatalf("absent field was overwritten: %d", stored.AvgPrepMinutes)
	}
}

func TestPatchProfileValidation(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	auth := map[string]string{httpapi.HeaderAPIKey: testsupport.DevKey}

	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{"empty object", `{}`, http.StatusUnprocessableEntity, "NO_PROFILE_CHANGES"},
		{"out of range", `{"avg_prep_minutes":0}`, http.StatusUnprocessableEntity, "VALIDATION_FAILED"},
		{"negative fee", `{"delivery_fee_minor":-1}`, http.StatusUnprocessableEntity, "VALIDATION_FAILED"},
		{"unknown field", `{"nope":1}`, http.StatusBadRequest, "VALIDATION_FAILED"},
		{"broken json", `{`, http.StatusBadRequest, "VALIDATION_FAILED"},
		{"two objects", `{"avg_prep_minutes":10}{"avg_prep_minutes":11}`, http.StatusBadRequest, "VALIDATION_FAILED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resp := do(t, srv, http.MethodPatch, "/api/v1/merchant/profile", tc.body, auth)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if got := decodeError(t, resp.Body).Error.Code; got != tc.wantCode {
				t.Fatalf("code = %q, want %q", got, tc.wantCode)
			}
		})
	}
}

func TestPatchProfileIfMatch(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	auth := map[string]string{httpapi.HeaderAPIKey: testsupport.DevKey}

	stale := map[string]string{httpapi.HeaderAPIKey: testsupport.DevKey, "If-Match": `"999"`}
	resp := do(t, srv, http.MethodPatch, "/api/v1/merchant/profile", `{"avg_prep_minutes":40}`, stale)
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412", resp.StatusCode)
	}
	if got := decodeError(t, resp.Body).Error.Code; got != "VERSION_CONFLICT" {
		t.Fatalf("code = %q", got)
	}

	fresh := map[string]string{httpapi.HeaderAPIKey: testsupport.DevKey, "If-Match": `"1"`}
	resp = do(t, srv, http.MethodPatch, "/api/v1/merchant/profile", `{"avg_prep_minutes":40}`, fresh)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	bad := map[string]string{httpapi.HeaderAPIKey: testsupport.DevKey, "If-Match": `"abc"`}
	resp = do(t, srv, http.MethodPatch, "/api/v1/merchant/profile", `{"avg_prep_minutes":41}`, bad)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 for a malformed If-Match", resp.StatusCode)
	}
	_ = auth
}

func TestUnknownEndpointAndMethod(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)

	resp := do(t, srv, http.MethodGet, "/api/v1/nothing", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	resp = do(t, srv, http.MethodDelete, "/api/v1/merchants", "", nil)
	if resp.StatusCode != http.StatusMethodNotAllowed && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 or 405", resp.StatusCode)
	}
}
