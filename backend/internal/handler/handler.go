// Package handler composes the bounded contexts' HTTP adapters into oas.Handler.
package handler

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	cataloghttp "orch-test/backend/internal/catalog/adapter/httpapi"
	"orch-test/backend/internal/oas"
)

// fallback sits one embedding level deeper than the context adapters, so its
// 501 Not Implemented methods only apply to operations no adapter implements.
// Adding an operation to the spec therefore never breaks the build.
type fallback struct{ oas.UnimplementedHandler }

// Handler implements oas.Handler. Each bounded context's HTTP adapter is embedded
// here (currently *cataloghttp.CatalogHandler) and constructed in internal/bootstrap.
type Handler struct {
	fallback
	*cataloghttp.CatalogHandler

	// Ping checks the storage backend for the health endpoint; nil means nothing to check.
	Ping func(context.Context) error

	now    func() time.Time // clock for the health cache; nil means time.Now
	health healthCache
}

// healthCacheTTL is how long a Ping result, success or failure, is reused.
// The health path is exempt from rate limiting, so this bounds the load it puts on storage.
const healthCacheTTL = time.Second

// healthCache remembers the last Ping result. The mutex is held while pinging, so
// concurrent callers wait for the one ping in flight instead of starting their own.
type healthCache struct {
	mu      sync.Mutex
	checked time.Time
	err     error
	valid   bool
}

var _ oas.Handler = (*Handler)(nil)

// GetHealth implements getHealth.
func (h *Handler) GetHealth(ctx context.Context) (*oas.Health, error) {
	if err := h.ping(ctx); err != nil {
		slog.WarnContext(ctx, "health check failed", "err", err)
		return nil, &oas.ErrorStatusCode{
			StatusCode: http.StatusServiceUnavailable,
			Response:   oas.Error{Error: "storage unavailable"},
		}
	}
	return &oas.Health{Status: "ok"}, nil
}

// ping returns the cached Ping result if it is younger than healthCacheTTL, and pings otherwise.
// A failure caused by the calling request's own cancellation is not cached.
func (h *Handler) ping(ctx context.Context) error {
	if h.Ping == nil {
		return nil
	}
	now := time.Now
	if h.now != nil {
		now = h.now
	}
	c := &h.health
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.valid && now().Sub(c.checked) < healthCacheTTL {
		return c.err
	}
	err := h.Ping(ctx)
	if err != nil && ctx.Err() != nil {
		return err
	}
	c.checked, c.err, c.valid = now(), err, true
	return err
}

// NewError handles errors that no adapter mapped. Adapters return *oas.ErrorStatusCode
// for expected failures (validation, not found), so anything reaching here is a bug.
func (h *Handler) NewError(ctx context.Context, err error) *oas.ErrorStatusCode {
	slog.ErrorContext(ctx, "unmapped error", "err", err)
	return &oas.ErrorStatusCode{
		StatusCode: http.StatusInternalServerError,
		Response:   oas.Error{Error: "internal server error"},
	}
}
