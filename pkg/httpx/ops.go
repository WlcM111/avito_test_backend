package httpx

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"
)

// ReadinessCheck reports whether the service can serve traffic.
type ReadinessCheck func(ctx context.Context) error

// OpsHandler serves liveness and readiness on a separate, internal listener so
// that they are never exposed together with the public API.
//
// /healthz answers while the process is alive and its handlers respond; it
// deliberately does not touch dependencies, because restarting a process that
// is merely waiting for its database would not help.
// /readyz reports whether dependencies are usable and flips to 503 as soon as
// shutdown starts, so a balancer stops sending new requests.
type OpsHandler struct {
	mux          *http.ServeMux
	shuttingDown atomic.Bool
}

// NewOpsHandler builds the ops endpoints. checkTimeout bounds each readiness
// probe so a hung dependency cannot hold the probe open.
func NewOpsHandler(ready ReadinessCheck, checkTimeout time.Duration) *OpsHandler {
	h := &OpsHandler{mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeOps(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	h.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if h.shuttingDown.Load() {
			writeOps(w, http.StatusServiceUnavailable,
				map[string]string{"status": "shutting_down"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
		defer cancel()
		if err := ready(ctx); err != nil {
			writeOps(w, http.StatusServiceUnavailable,
				map[string]string{"status": "not_ready", "reason": err.Error()})
			return
		}
		writeOps(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	return h
}

// ServeHTTP implements http.Handler.
func (h *OpsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// BeginShutdown makes /readyz report 503 before the listeners stop.
func (h *OpsHandler) BeginShutdown() { h.shuttingDown.Store(true) }

func writeOps(w http.ResponseWriter, status int, body map[string]string) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
