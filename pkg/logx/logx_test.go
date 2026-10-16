package logx_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/avito-kitchen/platform/pkg/logx"
)

// A password must never reach a log line, whatever the shape of the DSN.
func TestRedactDSN(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"postgres://user:secret@db:5432/app?sslmode=disable": "postgres://user:***@db:5432/app?sslmode=disable",
		"postgres://user:p@ss@db/app":                        "postgres://user:***@db/app",
		"postgres://user@db/app":                             "postgres://user@db/app",
		"postgres://db/app":                                  "postgres://db/app",
		"host=db user=app":                                   "host=db user=app",
		"":                                                   "",
	}
	for in, want := range cases {
		if got := logx.RedactDSN(in); got != want {
			t.Errorf("RedactDSN(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRequestIDTravelsInTheContext(t *testing.T) {
	t.Parallel()
	if got := logx.RequestID(context.Background()); got != "" {
		t.Fatalf("RequestID(empty context) = %q, want empty", got)
	}
	ctx := logx.WithRequestID(context.Background(), "req-1")
	if got := logx.RequestID(ctx); got != "req-1" {
		t.Fatalf("RequestID = %q, want req-1", got)
	}
}

// From enriches a logger with the correlation id only when there is one.
func TestFromAddsTheRequestID(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	base := slog.New(slog.NewJSONHandler(&buf, nil))

	logx.From(logx.WithRequestID(context.Background(), "req-7"), base).Info("with id")
	logx.From(context.Background(), base).Info("without id")

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2: %s", len(lines), buf.String())
	}
	var first, second map[string]any
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := json.Unmarshal(lines[1], &second); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if first["request_id"] != "req-7" {
		t.Fatalf("first line = %v, want request_id req-7", first)
	}
	if _, ok := second["request_id"]; ok {
		t.Fatalf("second line = %v, want no request_id", second)
	}
}

// Unknown level names fall back to info rather than silencing the service.
func TestNewHonoursTheLevel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		level string
		debug bool
		info  bool
		warn  bool
	}{
		{level: "debug", debug: true, info: true, warn: true},
		{level: "info", info: true, warn: true},
		{level: "WARN", warn: true},
		{level: "warning", warn: true},
		{level: "error"},
		{level: "nonsense", info: true, warn: true},
	}
	for _, tc := range cases {
		l := logx.New("svc", tc.level)
		if got := l.Enabled(ctx, slog.LevelDebug); got != tc.debug {
			t.Errorf("%s: debug enabled = %v", tc.level, got)
		}
		if got := l.Enabled(ctx, slog.LevelInfo); got != tc.info {
			t.Errorf("%s: info enabled = %v", tc.level, got)
		}
		if got := l.Enabled(ctx, slog.LevelWarn); got != tc.warn {
			t.Errorf("%s: warn enabled = %v", tc.level, got)
		}
		if !l.Enabled(ctx, slog.LevelError) {
			t.Errorf("%s: error must always be enabled", tc.level)
		}
	}
}
