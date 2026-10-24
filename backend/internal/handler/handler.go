// Package handler implements oas.Handler, the interface ogen generates from api/openapi.yaml.
package handler

import (
	"context"
	"errors"
	"net/http"

	ht "github.com/ogen-go/ogen/http"

	"orch-test/backend/internal/apperr"
	"orch-test/backend/internal/oas"
)

// fallback sits one embedding level deeper than the feature services, so its
// 501 Not Implemented methods only apply to operations no service implements.
// Adding an operation to the spec therefore never breaks the build.
type fallback struct{ oas.UnimplementedHandler }

// Handler composes the feature services. Embed each service here, e.g. *todos.Service.
type Handler struct {
	fallback
}

var _ oas.Handler = (*Handler)(nil)

// New returns a Handler with all feature services wired in.
func New() *Handler {
	return &Handler{}
}

// GetHealth implements getHealth.
func (h *Handler) GetHealth(ctx context.Context) (*oas.Health, error) {
	return &oas.Health{Status: "ok"}, nil
}

// NewError maps errors returned by operations to the spec's Error response.
func (h *Handler) NewError(ctx context.Context, err error) *oas.ErrorStatusCode {
	status := http.StatusInternalServerError
	var appErr *apperr.Error
	switch {
	case errors.As(err, &appErr):
		status = appErr.Status
	case errors.Is(err, ht.ErrNotImplemented):
		status = http.StatusNotImplemented
	}
	return &oas.ErrorStatusCode{StatusCode: status, Response: oas.Error{Error: err.Error()}}
}
