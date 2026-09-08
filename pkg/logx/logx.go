// Package logx configures structured JSON logging and carries a request
// correlation id through context.
package logx

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

type ctxKey int

const requestIDKey ctxKey = iota

// New builds a JSON slog logger for the given service and level.
// Unknown level names fall back to info.
func New(service, level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(h).With(slog.String("service", service))
}

// WithRequestID stores a correlation id in the context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID returns the correlation id stored in the context, or "".
func RequestID(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey).(string)
	return v
}

// From returns a logger enriched with the context correlation id.
func From(ctx context.Context, base *slog.Logger) *slog.Logger {
	if id := RequestID(ctx); id != "" {
		return base.With(slog.String("request_id", id))
	}
	return base
}

// RedactDSN removes the password from a PostgreSQL connection string so it can
// be logged safely.
func RedactDSN(dsn string) string {
	at := strings.LastIndex(dsn, "@")
	if at < 0 {
		return dsn
	}
	schemeEnd := strings.Index(dsn, "://")
	if schemeEnd < 0 || schemeEnd+3 > at {
		return dsn
	}
	creds := dsn[schemeEnd+3 : at]
	colon := strings.Index(creds, ":")
	if colon < 0 {
		return dsn
	}
	return dsn[:schemeEnd+3] + creds[:colon] + ":***" + dsn[at:]
}
