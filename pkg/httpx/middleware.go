package httpx

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/avito-kitchen/platform/pkg/logx"
	"github.com/google/uuid"
)

// HeaderRequestID is the correlation header accepted from and returned to
// clients across every service.
const HeaderRequestID = "X-Request-Id"

// Middleware decorates an http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware so that the first argument is the outermost layer.
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// RequestID takes the correlation id from the request or generates one, puts
// it into the context and echoes it back in the response.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(HeaderRequestID)
			if id == "" || len(id) > 128 {
				id = uuid.NewString()
			}
			w.Header().Set(HeaderRequestID, id)
			next.ServeHTTP(w, r.WithContext(logx.WithRequestID(r.Context(), id)))
		})
	}
}

// Recover turns a panic into a 500 response and logs it with the stack.
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logx.From(r.Context(), logger).Error("panic in handler",
						slog.Any("panic", rec), slog.String("path", r.URL.Path))
					WriteJSON(w, r, http.StatusInternalServerError, errorEnvelope{
						Error: errorBody{
							Code:      CodeInternal,
							Message:   "internal error",
							RequestID: logx.RequestID(r.Context()),
						},
					})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// AccessLog records one structured line per request.
func AccessLog(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			logx.From(r.Context(), logger).Info("http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int("bytes", rec.bytes),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			)
		})
	}
}

// Timeout bounds the handler with a context deadline.
func Timeout(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.TimeoutHandler(next, d, `{"error":{"code":"TIMEOUT","message":"request timed out"}}`)
	}
}

// BodyLimit caps the size of the request body.
func BodyLimit(maxBytes int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}
