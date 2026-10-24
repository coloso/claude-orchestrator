// Package handler composes the bounded contexts' HTTP adapters into oas.Handler.
package handler

import (
	"context"
	"log/slog"
	"net/http"

	"orch-test/backend/internal/oas"
)

// fallback sits one embedding level deeper than the context adapters, so its
// 501 Not Implemented methods only apply to operations no adapter implements.
// Adding an operation to the spec therefore never breaks the build.
type fallback struct{ oas.UnimplementedHandler }

// Handler implements oas.Handler. Each bounded context's HTTP adapter is embedded
// here (e.g. *todohttp.TodoHandler) and constructed in internal/bootstrap.
type Handler struct {
	fallback

	// Ping checks the storage backend for the health endpoint; nil means nothing to check.
	Ping func(context.Context) error
}

var _ oas.Handler = (*Handler)(nil)

// GetHealth implements getHealth.
func (h *Handler) GetHealth(ctx context.Context) (*oas.Health, error) {
	if h.Ping != nil {
		if err := h.Ping(ctx); err != nil {
			slog.WarnContext(ctx, "health check failed", "err", err)
			return nil, &oas.ErrorStatusCode{
				StatusCode: http.StatusServiceUnavailable,
				Response:   oas.Error{Error: "storage unavailable"},
			}
		}
	}
	return &oas.Health{Status: "ok"}, nil
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
