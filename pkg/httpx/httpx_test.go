package httpx_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/avito-kitchen/platform/pkg/httpx"
	"github.com/avito-kitchen/platform/pkg/logx"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type envelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
		Details   []struct {
			Field  string `json:"field"`
			Reason string `json:"reason"`
		} `json:"details"`
	} `json:"error"`
}

func decodeEnvelope(t *testing.T, body []byte) envelope {
	t.Helper()
	var out envelope
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return out
}

func newRequest(t *testing.T, method, target string, body io.Reader) *http.Request {
	t.Helper()
	return httptest.NewRequestWithContext(context.Background(), method, target, body)
}

// --------------------------------------------------------------- errors

func TestConstructorsCarryStatusAndCode(t *testing.T) {
	t.Parallel()
	cause := errors.New("connection refused")
	cases := []struct {
		err    *httpx.Error
		status int
		code   string
	}{
		{httpx.BadRequest("m"), http.StatusBadRequest, httpx.CodeValidationFailed},
		{httpx.Unprocessable("m"), http.StatusUnprocessableEntity, httpx.CodeValidationFailed},
		{httpx.Unauthorized("m"), http.StatusUnauthorized, httpx.CodeUnauthorized},
		{httpx.NotFound("m"), http.StatusNotFound, httpx.CodeNotFound},
		{httpx.Conflict("BELOW_RESERVED", "m"), http.StatusConflict, "BELOW_RESERVED"},
		{httpx.PreconditionFailed("m"), http.StatusPreconditionFailed, httpx.CodeVersionConflict},
		{httpx.Internal(cause), http.StatusInternalServerError, httpx.CodeInternal},
		{httpx.Unavailable("m", cause), http.StatusServiceUnavailable, httpx.CodeDependencyDown},
	}
	for _, tc := range cases {
		if tc.err.Status != tc.status || tc.err.Code != tc.code {
			t.Errorf("%v: status %d code %s, want %d %s", tc.err, tc.err.Status, tc.err.Code, tc.status, tc.code)
		}
	}
	if !errors.Is(httpx.Unavailable("m", cause), cause) {
		t.Fatal("the cause must stay reachable through errors.Is")
	}
	if got := httpx.Internal(cause).Error(); !strings.Contains(got, "connection refused") {
		t.Fatalf("Error() = %q, want the cause for the log", got)
	}
	if got := httpx.NotFound("no such order").Error(); got != "NOT_FOUND: no such order" {
		t.Fatalf("Error() = %q", got)
	}
	withDetails := httpx.Unprocessable("bad").WithDetails(
		httpx.FieldViolation{Field: "qty", Reason: "too big"},
		httpx.FieldViolation{Field: "name", Reason: "empty"})
	if len(withDetails.Details) != 2 {
		t.Fatalf("details = %+v", withDetails.Details)
	}
}

// -------------------------------------------------------------- respond

func TestWriteErrorUsesTheEnvelopeAndHidesTheCause(t *testing.T) {
	t.Parallel()
	ctx := logx.WithRequestID(context.Background(), "req-42")
	r := newRequest(t, http.MethodGet, "/x", nil).WithContext(ctx)

	rec := httptest.NewRecorder()
	httpx.WriteError(rec, r, quietLogger(), httpx.Unprocessable("request validation failed").
		WithDetails(httpx.FieldViolation{Field: "limit", Reason: "not an integer"}))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type = %q", ct)
	}
	body := decodeEnvelope(t, rec.Body.Bytes())
	if body.Error.Code != httpx.CodeValidationFailed || body.Error.RequestID != "req-42" ||
		len(body.Error.Details) != 1 || body.Error.Details[0].Field != "limit" {
		t.Fatalf("envelope = %+v", body)
	}

	// An unknown error becomes 500 and its text never reaches the client.
	rec = httptest.NewRecorder()
	httpx.WriteError(rec, r, quietLogger(), errors.New("pq: password authentication failed"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("the internal cause leaked: %s", rec.Body.String())
	}
	if got := decodeEnvelope(t, rec.Body.Bytes()).Error.Code; got != httpx.CodeInternal {
		t.Fatalf("code = %q, want INTERNAL_ERROR", got)
	}
}

func TestDecodeJSON(t *testing.T) {
	t.Parallel()
	type payload struct {
		Name string `json:"name"`
	}
	cases := []struct {
		name        string
		contentType string
		body        string
		limit       int64
		status      int
	}{
		{name: "valid", contentType: "application/json", body: `{"name":"x"}`},
		{name: "with charset", contentType: "application/json; charset=utf-8", body: `{"name":"x"}`},
		{name: "no content type", body: `{"name":"x"}`},
		{name: "wrong content type", contentType: "text/plain", body: `{"name":"x"}`,
			status: http.StatusUnsupportedMediaType},
		{name: "malformed", contentType: "application/json", body: `{"name":`,
			status: http.StatusBadRequest},
		{name: "unknown field", contentType: "application/json", body: `{"nope":1}`,
			status: http.StatusBadRequest},
		{name: "trailing content", contentType: "application/json", body: `{"name":"x"} {"name":"y"}`,
			status: http.StatusBadRequest},
		{name: "empty body", contentType: "application/json", body: ``,
			status: http.StatusBadRequest},
		{name: "too large", contentType: "application/json", body: `{"name":"` + strings.Repeat("x", 64) + `"}`,
			limit: 16, status: http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRequest(t, http.MethodPost, "/x", strings.NewReader(tc.body))
			if tc.contentType != "" {
				r.Header.Set("Content-Type", tc.contentType)
			}
			if tc.limit > 0 {
				r.Body = http.MaxBytesReader(httptest.NewRecorder(), r.Body, tc.limit)
			}
			var dst payload
			err := httpx.DecodeJSON(r, &dst)
			if tc.status == 0 {
				if err != nil || dst.Name != "x" {
					t.Fatalf("DecodeJSON = %v, %+v", err, dst)
				}
				return
			}
			var apiErr *httpx.Error
			if !errors.As(err, &apiErr) || apiErr.Status != tc.status {
				t.Fatalf("DecodeJSON = %v, want status %d", err, tc.status)
			}
		})
	}
}

// ---------------------------------------------------------------- query

func TestParsePage(t *testing.T) {
	t.Parallel()
	page, err := httpx.ParsePage(newRequest(t, http.MethodGet, "/x", nil), 20, 50)
	if err != nil || page.Limit != 20 || page.Offset != 0 {
		t.Fatalf("defaults = %+v, %v", page, err)
	}
	page, err = httpx.ParsePage(newRequest(t, http.MethodGet, "/x?limit=50&offset=100", nil), 20, 50)
	if err != nil || page.Limit != 50 || page.Offset != 100 {
		t.Fatalf("explicit = %+v, %v", page, err)
	}
	for _, query := range []string{"limit=0", "limit=51", "limit=ten", "offset=-1", "offset=x"} {
		_, err := httpx.ParsePage(newRequest(t, http.MethodGet, "/x?"+query, nil), 20, 50)
		var apiErr *httpx.Error
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnprocessableEntity ||
			len(apiErr.Details) != 1 {
			t.Fatalf("%s: ParsePage = %v, want 422 with the field named", query, err)
		}
	}
}

// ----------------------------------------------------------- middleware

func TestChainAppliesTheFirstMiddlewareOutermost(t *testing.T) {
	t.Parallel()
	var order []string
	mark := func(name string) httpx.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := httpx.Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		order = append(order, "handler")
	}), mark("outer"), mark("inner"))
	h.ServeHTTP(httptest.NewRecorder(), newRequest(t, http.MethodGet, "/", nil))
	if strings.Join(order, ",") != "outer,inner,handler" {
		t.Fatalf("order = %v", order)
	}
}

func TestRequestIDIsKeptOrGenerated(t *testing.T) {
	t.Parallel()
	var seen string
	h := httpx.RequestID()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = logx.RequestID(r.Context())
	}))

	r := newRequest(t, http.MethodGet, "/", nil)
	r.Header.Set(httpx.HeaderRequestID, "client-id")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if seen != "client-id" || rec.Header().Get(httpx.HeaderRequestID) != "client-id" {
		t.Fatalf("kept id = %q / %q", seen, rec.Header().Get(httpx.HeaderRequestID))
	}

	for _, incoming := range []string{"", strings.Repeat("x", 129)} {
		r := newRequest(t, http.MethodGet, "/", nil)
		if incoming != "" {
			r.Header.Set(httpx.HeaderRequestID, incoming)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if seen == "" || seen == incoming || rec.Header().Get(httpx.HeaderRequestID) != seen {
			t.Fatalf("generated id = %q for incoming %q", seen, incoming)
		}
	}
}

func TestRecoverAnswers500InTheEnvelope(t *testing.T) {
	t.Parallel()
	h := httpx.Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}), httpx.RequestID(), httpx.Recover(quietLogger()))

	r := newRequest(t, http.MethodGet, "/", nil)
	r.Header.Set(httpx.HeaderRequestID, "req-panic")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := decodeEnvelope(t, rec.Body.Bytes())
	if body.Error.Code != httpx.CodeInternal || body.Error.RequestID != "req-panic" {
		t.Fatalf("envelope = %+v", body)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Fatal("the panic value must not reach the client")
	}
}

func TestAccessLogRecordsTheStatus(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := httpx.AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("short and stout"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), newRequest(t, http.MethodPost, "/pot", nil))

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("decode %q: %v", buf.String(), err)
	}
	if line["status"] != float64(http.StatusTeapot) || line["path"] != "/pot" ||
		line["method"] != http.MethodPost || line["bytes"] != float64(len("short and stout")) {
		t.Fatalf("access log = %v", line)
	}
}

func TestTimeoutAnswers503(t *testing.T) {
	t.Parallel()
	h := httpx.Timeout(20 * time.Millisecond)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(t, http.MethodGet, "/slow", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := decodeEnvelope(t, rec.Body.Bytes()).Error.Code; got != httpx.CodeTimeout {
		t.Fatalf("code = %q, want TIMEOUT", got)
	}
}

func TestBodyLimitStopsLargeBodies(t *testing.T) {
	t.Parallel()
	h := httpx.BodyLimit(8)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var dst map[string]any
		if err := httpx.DecodeJSON(r, &dst); err != nil {
			httpx.WriteError(w, r, quietLogger(), err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest(t, http.MethodPost, "/", strings.NewReader(`{"name":"too long for eight bytes"}`)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if got := decodeEnvelope(t, rec.Body.Bytes()).Error.Code; got != httpx.CodePayloadTooLarge {
		t.Fatalf("code = %q, want PAYLOAD_TOO_LARGE", got)
	}
}

// ------------------------------------------------------------------ ops

func TestOpsProbes(t *testing.T) {
	t.Parallel()
	var readyErr error
	ops := httpx.NewOpsHandler(func(context.Context) error { return readyErr }, time.Second)

	probe := func(path string) (int, map[string]string) {
		rec := httptest.NewRecorder()
		ops.ServeHTTP(rec, newRequest(t, http.MethodGet, path, nil))
		var body map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}

	if code, body := probe("/healthz"); code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("healthz = %d %v", code, body)
	}
	if code, body := probe("/readyz"); code != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("readyz = %d %v", code, body)
	}
	readyErr = errors.New("database is unreachable")
	if code, body := probe("/readyz"); code != http.StatusServiceUnavailable ||
		body["status"] != "not_ready" || body["reason"] != "database is unreachable" {
		t.Fatalf("readyz with a failing check = %d %v", code, body)
	}
	readyErr = nil
	ops.BeginShutdown()
	if code, body := probe("/readyz"); code != http.StatusServiceUnavailable || body["status"] != "shutting_down" {
		t.Fatalf("readyz during shutdown = %d %v", code, body)
	}
	// Liveness is not readiness: a process that is shutting down is alive.
	if code, _ := probe("/healthz"); code != http.StatusOK {
		t.Fatalf("healthz during shutdown = %d, want 200", code)
	}
	if code, _ := probe("/metrics"); code != http.StatusNotFound {
		t.Fatalf("unknown ops path = %d, want 404", code)
	}
}

// The readiness check is bounded, so a hung dependency cannot hold the probe.
func TestReadinessCheckIsBounded(t *testing.T) {
	t.Parallel()
	ops := httpx.NewOpsHandler(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}, 20*time.Millisecond)
	rec := httptest.NewRecorder()
	start := time.Now()
	ops.ServeHTTP(rec, newRequest(t, http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable || time.Since(start) > 2*time.Second {
		t.Fatalf("hung check = %d after %s", rec.Code, time.Since(start))
	}
}

// --------------------------------------------------------------- server

func freeAddr(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	if err := lis.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return addr
}

// Serve answers until its context ends and then shuts down cleanly.
func TestServeShutsDownGracefully(t *testing.T) {
	t.Parallel()
	addr := freeAddr(t)
	srv := httpx.NewServer(httpx.ServerConfig{
		Addr: addr, ReadHeaderTimeout: time.Second, ReadTimeout: time.Second,
		WriteTimeout: time.Second, IdleTimeout: time.Second,
	}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	if srv.ReadHeaderTimeout != time.Second || srv.IdleTimeout != time.Second {
		t.Fatalf("timeouts were not applied: %+v", srv)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- httpx.Serve(ctx, srv, quietLogger(), time.Second) }()

	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr+"/", nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", resp.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server never answered: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve() = %v, want nil on a clean shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after the context ended")
	}
}

func TestServeReportsAListenFailure(t *testing.T) {
	t.Parallel()
	srv := httpx.NewServer(httpx.ServerConfig{Addr: "127.0.0.1:-1"}, http.NotFoundHandler())
	if err := httpx.Serve(context.Background(), srv, quietLogger(), time.Second); err == nil {
		t.Fatal("Serve() = nil, want the listen error")
	}
}
