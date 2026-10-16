package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// ServerConfig holds the timeouts of an HTTP listener.
type ServerConfig struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

// NewServer builds an http.Server with explicit timeouts. Leaving them at zero
// would allow a slow client to hold a connection indefinitely.
func NewServer(cfg ServerConfig, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.Addr,
		Handler:           h,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
}

// Serve runs srv until ctx is cancelled, then shuts it down gracefully within
// the grace period. It returns nil on a clean shutdown.
func Serve(ctx context.Context, srv *http.Server, logger *slog.Logger, grace time.Duration) error {
	errCh := make(chan error, 1)
	go func() {
		logger.Info("http listener started", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	// The shutdown keeps the values of ctx but not its cancellation: ctx is
	// already done, and the grace period must still run in full.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()
	logger.Info("http listener stopping", slog.String("addr", srv.Addr))
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
		return err
	}
	return <-errCh
}
